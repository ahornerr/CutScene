package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/LukeHagar/plexgo/models/components"
)

// Render-job request validation.
//
// Resolves the requested source against active sessions or the library,
// validates field ranges, derives the output height from the requested
// resolution, and applies presentation metadata to the resulting spec.

func validateRenderJobRequest(request RenderJobCreateRequest, user User, sessions []sessionMetadata) (renderJobSpec, error) {
	return validateRenderJobRequestWithMetadata(request, user, sessions, nil, previewSessionSelection{})
}

func validateRenderJobRequestWithMetadata(request RenderJobCreateRequest, user User, sessions []sessionMetadata, metadata []components.Media, selection previewSessionSelection) (renderJobSpec, error) {
	return validateRenderJobRequestWithMetadataAndItem(request, user, sessions, metadata, selection, nil)
}

type renderPresentation struct {
	CreatorDisplayName string
	MediaKind          string
	MovieTitle         string
	MovieYear          *int
	ShowTitle          string
	SeasonNumber       *int
	EpisodeNumber      *int
	EpisodeTitle       string
	ThumbnailURL       string
}

func creatorDisplayName(user User) string {
	if strings.TrimSpace(user.Username) != "" {
		return strings.TrimSpace(user.Username)
	}
	return strings.TrimSpace(user.Title)
}

func presentationFromSession(user User, sessions []sessionMetadata, ratingKey string) renderPresentation {
	presentation := renderPresentation{CreatorDisplayName: creatorDisplayName(user)}
	for _, session := range sessions {
		key := session.Key
		if session.RatingKey != nil {
			key = *session.RatingKey
		}
		if key != ratingKey {
			continue
		}
		presentation.MediaKind = session.Type
		if session.Thumb != nil && strings.TrimSpace(*session.Thumb) != "" {
			presentation.ThumbnailURL = *session.Thumb
		} else if session.GrandparentThumb != nil {
			presentation.ThumbnailURL = *session.GrandparentThumb
		}
		if session.Type == "episode" || session.GrandparentTitle != nil || session.ParentIndex != nil || session.Index != nil {
			presentation.ShowTitle = stringValue(session.GrandparentTitle)
			presentation.SeasonNumber = intValue(session.ParentIndex)
			presentation.EpisodeNumber = intValue(session.Index)
			presentation.EpisodeTitle = session.Title
		}
		break
	}
	return presentation
}

func presentationFromMetadata(user User, sessions []sessionMetadata, ratingKey string, metadata *components.Metadata) renderPresentation {
	presentation := presentationFromSession(user, sessions, ratingKey)
	if metadata == nil {
		return presentation
	}
	if metadata.Type != "" {
		presentation.MediaKind = metadata.Type
	}
	if presentation.ThumbnailURL == "" && metadata.Thumb != nil && strings.TrimSpace(*metadata.Thumb) != "" {
		presentation.ThumbnailURL = *metadata.Thumb
	} else if presentation.ThumbnailURL == "" && metadata.GrandparentThumb != nil {
		presentation.ThumbnailURL = *metadata.GrandparentThumb
	}
	switch metadata.Type {
	case "movie":
		if metadata.Title != "" {
			presentation.MovieTitle = metadata.Title
		}
		if metadata.Year != nil {
			presentation.MovieYear = intValue(metadata.Year)
		}
	case "episode":
		if value := stringValue(metadata.GrandparentTitle); value != "" {
			presentation.ShowTitle = value
		} else if presentation.ShowTitle == "" {
			// ParentTitle is only a fallback when the selected session did not
			// provide a show title; it must never replace a valid session title.
			if value := stringValue(metadata.ParentTitle); value != "" {
				presentation.ShowTitle = value
			}
		}
		if metadata.ParentIndex != nil {
			presentation.SeasonNumber = intValue(metadata.ParentIndex)
		}
		if metadata.Index != nil {
			presentation.EpisodeNumber = intValue(metadata.Index)
		}
		if metadata.Title != "" {
			presentation.EpisodeTitle = metadata.Title
		}
	}
	return presentation
}

func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func intValue(value *int) *int {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func (p renderPresentation) apply(spec *renderJobSpec) {
	spec.CreatorDisplayName = p.CreatorDisplayName
	spec.MediaKind = p.MediaKind
	spec.MovieTitle = p.MovieTitle
	spec.MovieYear = p.MovieYear
	spec.ShowTitle = p.ShowTitle
	spec.SeasonNumber = p.SeasonNumber
	spec.EpisodeNumber = p.EpisodeNumber
	spec.EpisodeTitle = p.EpisodeTitle
	spec.ThumbnailURL = p.ThumbnailURL
}

func validateRenderJobRequestWithMetadataAndItem(request RenderJobCreateRequest, user User, sessions []sessionMetadata, metadata []components.Media, selection previewSessionSelection, metadataItem *components.Metadata) (renderJobSpec, error) {
	if err := validateRenderJobRequestFields(request); err != nil {
		return renderJobSpec{}, err
	}
	audioMode, err := parseAudioMode(string(request.AudioMode))
	if err != nil {
		return renderJobSpec{}, errors.New("audioMode is invalid")
	}
	if user.Uuid == "" {
		return renderJobSpec{}, errors.New("authenticated user is missing a stable id")
	}
	if err := validateSubtitleOffsetMs(request.SubtitleOffsetMs); err != nil {
		return renderJobSpec{}, err
	}
	if metadata != nil {
		previewMedia, previewPart, err := resolvePreviewMetadataSource(metadata, selection)
		if err != nil {
			return renderJobSpec{}, err
		}
		mediaDuration, durationErr := selectedSourceDuration(previewMedia, previewPart)
		if durationErr != nil {
			return renderJobSpec{}, durationErr
		}
		if mediaDuration == 0 && selection.duration > 0 {
			mediaDuration = selection.duration
		}
		if mediaDuration > 0 && request.ToMs > mediaDuration {
			return renderJobSpec{}, errors.New("requested range exceeds the selected media duration")
		}

		subtitle := subtitleSource{EmbeddedIndex: -1}
		if request.SubtitleIndex >= 0 {
			var subtitleErr error
			subtitle, subtitleErr = selectSubtitleSource(previewPart.Stream, request.SubtitleIndex)
			if subtitleErr != nil {
				return renderJobSpec{}, errors.New("subtitleIndex is not available for the requested media")
			}
			if subtitle.PGS && subtitle.External {
				return renderJobSpec{}, errors.New("external subtitle codec is not supported")
			}
			if !subtitle.PGS && !isSupportedTextSubtitle(subtitle) {
				return renderJobSpec{}, errors.New("subtitle codec is not supported")
			}
		}
		height, err := resolveRequestRenderHeight(request, mediaHeight(previewMedia))
		if err != nil {
			return renderJobSpec{}, err
		}
		sourceMediaID := request.MediaID
		if sourceMediaID <= 0 {
			sourceMediaID = previewMedia.ID
		}
		spec := renderJobSpec{
			OwnerUUID:             user.Uuid,
			RatingKey:             request.RatingKey,
			MediaID:               sourceMediaID,
			PartID:                previewPart.ID,
			PartKey:               previewPart.Key,
			Title:                 sourceTitle(sessions, request.RatingKey, metadataItem),
			FromMs:                request.FromMs,
			ToMs:                  request.ToMs,
			SubtitleIndex:         request.SubtitleIndex,
			SubtitleOffsetMs:      request.SubtitleOffsetMs,
			SubtitlePGS:           subtitle.PGS,
			SubtitleExternal:      subtitle.External,
			SubtitleStreamKey:     subtitle.StreamKey,
			SubtitleCodec:         subtitle.Codec,
			SubtitleFormat:        subtitle.Format,
			SubtitleEmbeddedIndex: subtitle.EmbeddedIndex,
			Resolution:            normalizedRenderResolution(request),
			Height:                height,
			QP:                    request.QP,
			AudioMode:             audioMode,
		}
		presentationFromMetadata(user, sessions, request.RatingKey, metadataItem).apply(&spec)
		return spec, nil
	}

	for _, session := range sessions {
		ratingKey := session.Key
		if session.RatingKey != nil {
			ratingKey = *session.RatingKey
		}
		if ratingKey != request.RatingKey {
			continue
		}
		for _, media := range session.Media {
			for _, part := range media.Part {
				if !sessionValueMatchesID(media.ID, request.MediaID) && !sessionValueMatchesID(part.ID, request.MediaID) {
					continue
				}
				if part.Key == "" {
					return renderJobSpec{}, errors.New("requested media has no playable part")
				}
				mediaDuration := int64(0)
				if media.Duration != nil && *media.Duration > 0 && len(media.Part) <= 1 {
					mediaDuration = int64(*media.Duration)
				} else if session.Duration != nil && *session.Duration > 0 && len(media.Part) <= 1 {
					mediaDuration = int64(*session.Duration)
				} else if len(media.Part) > 1 {
					return renderJobSpec{}, errors.New("multipart media requires a part duration")
				}
				if mediaDuration > 0 && request.ToMs > mediaDuration {
					return renderJobSpec{}, errors.New("requested range exceeds the selected media duration")
				}
				subtitleCount := 0
				subtitlePGS := false
				subtitleEmbeddedIndex := -1
				for _, stream := range part.Stream {
					if stream.Type != 3 {
						continue
					}
					if subtitleCount == request.SubtitleIndex {
						subtitlePGS = isPGSSubtitle(stream.Codec, "")
						subtitleEmbeddedIndex = subtitleCount
					}
					subtitleCount++
				}
				if request.SubtitleIndex >= subtitleCount {
					return renderJobSpec{}, errors.New("subtitleIndex is not available for the requested media")
				}
				if request.SubtitleIndex >= 0 && subtitleEmbeddedIndex < 0 {
					return renderJobSpec{}, errors.New("subtitle codec is not supported")
				}
				height, err := resolveRequestRenderHeight(request, sessionMediaHeight(media))
				if err != nil {
					return renderJobSpec{}, err
				}
				partID, _ := sessionIDAsInt64(part.ID)
				spec := renderJobSpec{
					OwnerUUID:             user.Uuid,
					RatingKey:             request.RatingKey,
					MediaID:               request.MediaID,
					PartID:                partID,
					PartKey:               part.Key,
					Title:                 session.Title,
					FromMs:                request.FromMs,
					ToMs:                  request.ToMs,
					SubtitleIndex:         request.SubtitleIndex,
					SubtitleOffsetMs:      request.SubtitleOffsetMs,
					SubtitlePGS:           subtitlePGS,
					SubtitleEmbeddedIndex: subtitleEmbeddedIndex,
					Resolution:            normalizedRenderResolution(request),
					Height:                height,
					QP:                    request.QP,
					AudioMode:             audioMode,
				}
				presentationFromSession(user, sessions, request.RatingKey).apply(&spec)
				return spec, nil
			}
		}
	}
	return renderJobSpec{}, errors.New("requested media is not visible in the caller's sessions")
}

func validateRenderJobRequestFields(request RenderJobCreateRequest) error {
	if request.RatingKey == "" || len(request.RatingKey) > 512 {
		return errors.New("ratingKey is invalid")
	}
	if request.MediaID <= 0 && request.PartID == nil {
		return errors.New("mediaId is invalid")
	}
	if request.PartID != nil && *request.PartID <= 0 {
		return errors.New("partId is invalid")
	}
	if request.FromMs < 0 || request.ToMs < 0 || request.ToMs <= request.FromMs {
		return errors.New("fromMs and toMs must be nonnegative and ordered")
	}
	if request.ToMs-request.FromMs > renderMaxDurationMs {
		return fmt.Errorf("clip duration exceeds %d minutes", renderMaxDurationMs/60000)
	}
	if request.SubtitleIndex < -1 {
		return errors.New("subtitleIndex is invalid")
	}
	if err := validateSubtitleOffsetMs(request.SubtitleOffsetMs); err != nil {
		return err
	}
	if _, err := renderResolutionTarget(request.Resolution); err != nil {
		return err
	}
	if request.Resolution != "" && request.Height != 0 {
		return errors.New("resolution and height cannot both be specified")
	}
	if request.Height < 0 || request.Height > 2160 || request.Height > 0 && (request.Height < 144 || request.Height%2 != 0) {
		return errors.New("height is invalid")
	}
	if request.QP < 0 || request.QP > 51 {
		return errors.New("qp is invalid")
	}
	return nil
}

func normalizedRenderResolution(request RenderJobCreateRequest) string {
	if request.Resolution == "" {
		return ""
	}
	return request.Resolution
}

func resolveRequestRenderHeight(request RenderJobCreateRequest, sourceHeight int) (int, error) {
	if request.Resolution != "" {
		return resolveRenderHeight(request.Resolution, sourceHeight)
	}
	return request.Height, nil
}

func mediaHeight(media *components.Media) int {
	if media != nil && media.Height != nil && *media.Height > 0 {
		return *media.Height
	}
	return 0
}

func sessionMediaHeight(media sessionMedia) int {
	if media.Height != nil && *media.Height > 0 {
		return *media.Height
	}
	return 0
}

func sourceTitle(sessions []sessionMetadata, ratingKey string, metadata *components.Metadata) string {
	if title := sessionTitle(sessions, ratingKey); title != "" {
		return title
	}
	if metadata != nil {
		return metadata.Title
	}
	return ""
}

func sessionTitle(sessions []sessionMetadata, ratingKey string) string {
	for _, session := range sessions {
		key := session.Key
		if session.RatingKey != nil {
			key = *session.RatingKey
		}
		if key == ratingKey {
			return session.Title
		}
	}
	return ""
}

func sessionValueMatchesID(value any, wanted int64) bool {
	switch value := value.(type) {
	case int:
		return int64(value) == wanted
	case int64:
		return value == wanted
	case float64:
		return int64(value) == wanted && value == float64(wanted)
	case json.Number:
		parsed, err := value.Int64()
		return err == nil && parsed == wanted
	case string:
		parsed, err := strconv.ParseInt(value, 10, 64)
		return err == nil && parsed == wanted
	default:
		return false
	}
}
