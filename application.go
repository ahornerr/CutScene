package main

import (
	"context"
	"errors"
	"fmt"
	"github.com/LukeHagar/plexgo/models/components"
	"io"
	"os"
	"strconv"

	"github.com/LukeHagar/plexgo"
	"github.com/LukeHagar/plexgo/models/operations"
)

var (
	ErrUserNotInvited = errors.New("user not invited to server")
)

type Application struct {
	config            Config
	plexAdmin         *plexgo.PlexAPI
	plexUser          *plexgo.PlexAPI
	plexTv            *PlexTV
	machineIdentifier string
	ownerEmail        string
}

func NewApplication(config Config) (*Application, error) {
	app := &Application{
		config: config,
		plexTv: NewPlexTV(config.Plex.Token),
		plexAdmin: plexgo.New(
			plexgo.WithServerURL(config.Plex.Host),
			plexgo.WithSecurity(config.Plex.Token),
		),
	}

	identity, err := app.plexAdmin.Server.GetServerIdentity(context.Background())
	if err != nil {
		return nil, fmt.Errorf("could not get server identity: %w", err)
	}

	app.machineIdentifier = *identity.Object.MediaContainer.MachineIdentifier

	account, err := app.plexAdmin.Server.GetMyPlexAccount(context.Background())
	if err != nil {
		return nil, fmt.Errorf("could not get account info: %w", err)
	}

	app.ownerEmail = *account.Object.MyPlex.Username

	// TODO: If configured, ignore auth from context and just use the configured token for all requests
	app.plexUser = plexgo.New(
		plexgo.WithServerURL(config.Plex.Host),
		plexgo.WithSecuritySource(app.plexSecurityUserToken),
	)

	return app, nil
}

func (a *Application) plexSecurityUserToken(ctx context.Context) (components.Security, error) {
	authToken := AuthTokenFromContext(ctx)
	if authToken == nil {
		return components.Security{}, fmt.Errorf("missing auth token")
	}

	return components.Security{
		AccessToken: *authToken,
	}, nil
}

func (a *Application) GetValidatedUser(ctx context.Context) (*User, error) {
	serverUsers, err := a.plexTv.getUsers()
	if err != nil {
		return nil, err
	}

	user, err := NewPlexTV(*AuthTokenFromContext(ctx)).getUser()
	if err != nil {
		return nil, err
	}

	// Check for server owner
	if user.Email == a.ownerEmail {
		return user, nil
	}

	// Check for users invited to server
	if serverUsers.HasUser(strconv.Itoa(user.Id), a.machineIdentifier) {
		return user, nil
	}

	return nil, ErrUserNotInvited
}

func (a *Application) GetSessions(ctx context.Context) ([]operations.GetSessionsMetadata, error) {
	sessions, err := a.plexAdmin.Sessions.GetSessions(ctx)
	if err != nil {
		return nil, fmt.Errorf("could not get sessions: %w", err)
	}

	var filteredSessions []operations.GetSessionsMetadata
	user := UserFromContext(ctx)
	if user != nil && user.Email != a.ownerEmail {
		for _, session := range sessions.Object.MediaContainer.Metadata {
			if strconv.Itoa(user.Id) == *session.User.ID {
				filteredSessions = append(filteredSessions, session)
			}
		}
	} else {
		filteredSessions = sessions.Object.MediaContainer.Metadata
	}

	return filteredSessions, nil
}

// SubtitleStream describes a text-based subtitle track available in a media item.
type SubtitleStream struct {
	Index        int    `json:"index"`        // 0-based subtitle stream index (for FFmpeg -map 0:s:N)
	Language     string `json:"language"`
	DisplayTitle string `json:"displayTitle"`
	Codec        string `json:"codec"`
	Default      bool   `json:"default"`
}

// textSubtitleCodecs are codecs that can be extracted to SRT and rendered by libass.
var textSubtitleCodecs = map[string]bool{
	"srt":      true,
	"subrip":   true,
	"ass":      true,
	"ssa":      true,
	"webvtt":   true,
	"mov_text": true,
	"text":     true,
}

func (a *Application) GetSubtitleStreams(ctx context.Context, ratingKeyStr string) ([]SubtitleStream, error) {
	ratingKey, err := strconv.ParseFloat(ratingKeyStr, 0)
	if err != nil {
		return nil, fmt.Errorf("could not parse rating key: %w", err)
	}

	libraryMetadata, err := a.plexAdmin.Library.GetMetadata(ctx, ratingKey)
	if err != nil {
		return nil, err
	}

	metadata := libraryMetadata.Object.MediaContainer.Metadata[0]
	if len(metadata.Media) == 0 || len(metadata.Media[0].Part) == 0 {
		return nil, nil
	}

	var result []SubtitleStream
	subtitleIdx := 0
	for _, stream := range metadata.Media[0].Part[0].Stream {
		if stream.StreamType == nil || *stream.StreamType != 3 {
			continue
		}

		codec := ""
		if stream.Codec != nil {
			codec = *stream.Codec
		}

		if textSubtitleCodecs[codec] {
			s := SubtitleStream{
				Index: subtitleIdx,
				Codec: codec,
			}
			if stream.Language != nil {
				s.Language = *stream.Language
			}
			if stream.DisplayTitle != nil {
				s.DisplayTitle = *stream.DisplayTitle
			}
			if stream.Default != nil {
				s.Default = *stream.Default
			}
			result = append(result, s)
		}

		subtitleIdx++ // always increment to reflect true FFmpeg stream index
	}

	return result, nil
}

func (a *Application) Clip(ctx context.Context, ratingKeyStr, mediaIdStr, from, to string, height, qp, subtitleIndex int) (string, error) {
	ratingKey, err := strconv.ParseFloat(ratingKeyStr, 0)
	if err != nil {
		return "", fmt.Errorf("could not parse rating key: %w", err)
	}

	libraryMetadata, err := a.plexAdmin.Library.GetMetadata(ctx, ratingKey)
	if err != nil {
		return "", fmt.Errorf("could not get library metadata: %w", err)
	}

	metadata := libraryMetadata.Object.MediaContainer.Metadata[0]

	var media *operations.GetMetadataMedia
	if mediaIdStr != "" {
		mediaId, err := strconv.Atoi(mediaIdStr)
		if err != nil {
			return "", fmt.Errorf("could not parse media id: %w", err)
		}

		for _, m := range metadata.Media {
			if m.ID != nil && *m.ID == mediaId {
				media = &m
			}
		}
	}

	if media == nil {
		for _, m := range metadata.Media {
			// 10 bit encoding doesn't work correctly on NVIDIA hardware (and maybe others)
			if m.VideoProfile != nil && *m.VideoProfile == "main 10" {
				continue
			}
			media = &m
			break
		}
	}

	if media == nil {
		return "", fmt.Errorf("could not find suitable media for rating key")
	}

	fileURL := fmt.Sprintf("%s%s?X-Plex-Token=%s",
		a.config.Plex.Host,
		*media.Part[0].Key,
		a.config.Plex.Token,
	)

	// Extract subtitle for burning in if requested. ExtractSubtitle runs a short
	// FFmpeg pass to demux the subtitle stream into a temp SRT file with timestamps
	// relative to `from`, so they align with the clip's video timeline.
	var subtitleFile string
	if subtitleIndex >= 0 {
		var err error
		subtitleFile, err = ExtractSubtitle(fileURL, from, to, subtitleIndex)
		if err != nil {
			return "", fmt.Errorf("could not extract subtitle: %w", err)
		}
		defer os.Remove(subtitleFile)
	}

	var fileName string
	if *metadata.Type == "episode" {
		fileName = fmt.Sprintf("%s S%02dE%02d %s (%s - %s).mp4",
			*metadata.GrandparentTitle,
			*metadata.ParentIndex,
			*metadata.Index,
			*metadata.Title,
			from,
			to,
		)
	} else {
		fileName = fmt.Sprintf("%s (%d) (%s - %s).mp4",
			*metadata.Title,
			*metadata.Year,
			from,
			to,
		)
	}

	params := FfmpegParams{
		URL:          fileURL,
		From:         from,
		To:           to,
		Filename:     fileName,
		Codec:        a.config.Ffmpeg.Codec,
		Height:       height,
		QP:           qp,
		SubtitleFile: subtitleFile,
		Metadata: FfmpegParamsMetadata{
			Title: *metadata.Title,
		},
	}

	if metadata.GrandparentTitle != nil {
		params.Metadata.Show = *metadata.GrandparentTitle
	}
	if metadata.ParentIndex != nil {
		params.Metadata.SeasonNumber = *metadata.ParentIndex
	}
	if metadata.Index != nil {
		params.Metadata.EpisodeID = *metadata.Index
	}
	if metadata.Year != nil {
		params.Metadata.Year = *metadata.Year
	}

	return DoFfmpeg(params)
}

func (a *Application) Thumb(ctx context.Context, thumb string) (io.ReadCloser, error) {
	req := operations.GetResizedPhotoRequest{
		Width:  320,
		Height: 320,
		URL:    thumb,
	}
	resp, err := a.plexAdmin.Server.GetResizedPhoto(ctx, req)
	if err != nil {
		return nil, err
	}

	return resp.RawResponse.Body, nil
}
