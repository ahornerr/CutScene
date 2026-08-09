package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"

	"github.com/LukeHagar/plexgo"
	"github.com/LukeHagar/plexgo/models/components"
	"github.com/LukeHagar/plexgo/models/operations"
)

// subtitleCache is a bounded FIFO cache for subtitle entries.
type subtitleCache struct {
	mu    sync.RWMutex
	max   int
	m     map[subtitleCacheKey][]SubtitleEntry
	order []subtitleCacheKey
}

func newSubtitleCache(max int) *subtitleCache {
	return &subtitleCache{
		max: max,
		m:   make(map[subtitleCacheKey][]SubtitleEntry),
	}
}

func (c *subtitleCache) get(key subtitleCacheKey) ([]SubtitleEntry, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	entries, ok := c.m[key]
	if !ok {
		return nil, false
	}
	// defensive copy: prevent caller mutation from escaping
	result := make([]SubtitleEntry, len(entries))
	copy(result, entries)
	return result, true
}

func (c *subtitleCache) set(key subtitleCacheKey, entries []SubtitleEntry) {
	c.mu.Lock()
	defer c.mu.Unlock()
	// defensive copy
	entriesCopy := make([]SubtitleEntry, len(entries))
	copy(entriesCopy, entries)

	if _, exists := c.m[key]; !exists && len(c.m) >= c.max && c.max > 0 {
		// FIFO eviction: remove oldest entry
		oldest := c.order[0]
		delete(c.m, oldest)
		c.order = c.order[1:]
	}
	if _, exists := c.m[key]; !exists {
		c.order = append(c.order, key)
	}
	c.m[key] = entriesCopy
}

func (c *subtitleCache) len() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.m)
}

var (
	ErrUserNotInvited = errors.New("user not invited to server")
)

// sessionContainer represents the raw Plex /status/sessions JSON response
type sessionContainer struct {
	MediaContainer struct {
		Metadata []sessionMetadata `json:"Metadata,omitempty"`
	} `json:"MediaContainer"`
}

type sessionMetadata struct {
	Title            string  `json:"title"`
	Type             string  `json:"type"`
	GrandparentTitle *string `json:"grandparentTitle,omitempty"`
	ParentIndex      *int    `json:"parentIndex,omitempty"`
	Index            *int    `json:"index,omitempty"`
	Year             *int    `json:"year,omitempty"`
	Thumb            *string `json:"thumb,omitempty"`
	GrandparentThumb *string `json:"grandparentThumb,omitempty"`
	Key              string  `json:"key"`
	RatingKey        *string `json:"ratingKey,omitempty"`
	Duration         *int    `json:"duration,omitempty"`
	ViewOffset       *int64  `json:"viewOffset,omitempty"`
	User             struct {
		ID    string `json:"id"`
		Title string `json:"title"`
		Thumb string `json:"thumb"`
	} `json:"User"`
	Player struct {
		Title             string `json:"title"`
		UUID              string `json:"uuid"`
		MachineIdentifier string `json:"machineIdentifier"`
		State             string `json:"state"`
		Address           string `json:"address"`
	} `json:"Player"`
	Session struct {
		Key       string `json:"key"`
		Bandwidth *int64 `json:"bandwidth,omitempty"`
		Location  string `json:"location,omitempty"`
	} `json:"Session"`
	Media []sessionMedia `json:"Media,omitempty"`
}

type sessionMedia struct {
	ID              any           `json:"id"`
	Duration        *int          `json:"duration,omitempty"`
	Bitrate         *int          `json:"bitrate,omitempty"`
	AudioCodec      *string       `json:"audioCodec,omitempty"`
	AudioChannels   *int          `json:"audioChannels,omitempty"`
	VideoCodec      *string       `json:"videoCodec,omitempty"`
	VideoResolution *string       `json:"videoResolution,omitempty"`
	Container       *string       `json:"container,omitempty"`
	Part            []sessionPart `json:"Part,omitempty"`
}

type sessionPart struct {
	ID     any             `json:"id"`
	Key    string          `json:"key"`
	File   string          `json:"file,omitempty"`
	Size   *int64          `json:"size,omitempty"`
	Stream []sessionStream `json:"Stream,omitempty"`
}

type sessionStream struct {
	ID           interface{} `json:"id"`
	Type         int         `json:"type"`
	Codec        string      `json:"codec"`
	Index        *int        `json:"index,omitempty"`
	Channels     *int        `json:"channels,omitempty"`
	Language     *string     `json:"language,omitempty"`
	LanguageCode *string     `json:"languageCode,omitempty"`
	Bitrate      *int        `json:"bitrate,omitempty"`
	Default      *bool       `json:"default,omitempty"`
	Forced       *bool       `json:"forced,omitempty"`
}

type Application struct {
	config            Config
	plexAdmin         *plexgo.PlexAPI
	plexUser          *plexgo.PlexAPI
	plexTv            *PlexTV
	machineIdentifier string
	ownerEmail        string
	subtitleCache     *subtitleCache
	ffmpegLimiter     *ffmpegLimiter
	renderJobs        *renderJobManager
	lifetime          context.Context
	cancelLifetime    context.CancelFunc
	closeOnce         sync.Once
}

func NewApplication(config Config) (*Application, error) {
	config.Ffmpeg.Concurrency = normalizeFFmpegConcurrency(config.Ffmpeg.Concurrency)
	lifetime, cancelLifetime := context.WithCancel(context.Background())
	app := &Application{
		config: config,
		plexTv: NewPlexTV(config.Plex.Token),
		plexAdmin: plexgo.New(
			plexgo.WithServerURL(config.Plex.Host),
			plexgo.WithSecurity(config.Plex.Token),
		),
		subtitleCache:  newSubtitleCache(128),
		ffmpegLimiter:  newFFmpegLimiter(config.Ffmpeg.Concurrency),
		lifetime:       lifetime,
		cancelLifetime: cancelLifetime,
	}

	identity, err := app.plexAdmin.General.GetIdentity(context.Background())
	if err != nil {
		return nil, fmt.Errorf("could not get server identity: %w", err)
	}

	app.machineIdentifier = *identity.Object.MediaContainer.MachineIdentifier

	tokenDetails, err := app.plexAdmin.Authentication.GetTokenDetails(context.Background(), operations.GetTokenDetailsRequest{})
	if err != nil {
		return nil, fmt.Errorf("could not get token details: %w", err)
	}

	app.ownerEmail = tokenDetails.UserPlexAccount.Email

	// TODO: If configured, ignore auth from context and just use the configured token for all requests
	app.plexUser = plexgo.New(
		plexgo.WithServerURL(config.Plex.Host),
		plexgo.WithSecuritySource(app.plexSecurityUserToken),
	)

	app.renderJobs, err = newRenderJobManagerWithContext(app.lifetime, renderRoot, app.executeRenderSpec)
	if err != nil {
		return nil, fmt.Errorf("could not initialize render jobs: %w", err)
	}

	return app, nil
}

// Close stops background render workers and the retention loop. It is safe to
// call more than once and gives in-flight FFmpeg work a cancellation signal.
func (a *Application) Close() error {
	if a == nil {
		return nil
	}
	a.closeOnce.Do(func() {
		if a.cancelLifetime != nil {
			a.cancelLifetime()
		}
		if a.renderJobs != nil {
			a.renderJobs.stopAndWait()
		}
	})
	return nil
}

func (a *Application) operationContext(parent context.Context) (context.Context, func()) {
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancel(parent)
	stopLifetime := func() {}
	if a.lifetime != nil {
		afterLifetime := context.AfterFunc(a.lifetime, cancel)
		stopLifetime = func() { afterLifetime() }
	}
	return ctx, func() {
		stopLifetime()
		cancel()
	}
}

func (a *Application) plexSecurityUserToken(ctx context.Context) (components.Security, error) {
	authToken := AuthTokenFromContext(ctx)
	if authToken == nil {
		return components.Security{}, fmt.Errorf("missing auth token")
	}

	return components.Security{
		Token: authToken,
	}, nil
}

func (a *Application) GetValidatedUser(ctx context.Context) (*User, error) {
	user, err := NewPlexTV(*AuthTokenFromContext(ctx)).getUser()
	if err != nil {
		return nil, err
	}

	if user.Email == a.ownerEmail {
		return user, nil
	}

	// Check if user is an invited user on this server
	users, err := a.plexTv.getUsers()
	if err != nil {
		return nil, fmt.Errorf("could not get server users: %w", err)
	}

	if users.HasUser(strconv.Itoa(user.Id), a.machineIdentifier) {
		return user, nil
	}

	return nil, ErrUserNotInvited
}

func (a *Application) GetSessions(ctx context.Context) ([]sessionMetadata, error) {
	sessionsURL := fmt.Sprintf("%s/status/sessions", a.config.Plex.Host)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, sessionsURL, nil)
	if err != nil {
		return nil, fmt.Errorf("could not create sessions request: %w", err)
	}
	req.Header.Set("X-Plex-Token", a.config.Plex.Token)
	req.Header.Set("Accept", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("could not get sessions: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("could not read sessions response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("sessions returned status %d: %s", resp.StatusCode, string(body))
	}

	var sessions sessionContainer
	if err := json.Unmarshal(body, &sessions); err != nil {
		return nil, fmt.Errorf("could not decode sessions: %w\nRaw response:\n%s", err, string(body))
	}

	// Non-owner users see only their own sessions
	user := UserFromContext(ctx)
	if user != nil && user.Email != a.ownerEmail {
		userIdStr := strconv.Itoa(user.Id)
		var filtered []sessionMetadata
		for _, s := range sessions.MediaContainer.Metadata {
			if s.User.ID == userIdStr {
				filtered = append(filtered, s)
			}
		}
		return filtered, nil
	}

	return sessions.MediaContainer.Metadata, nil
}

// SubtitleStream describes a subtitle track available in a media item.
type SubtitleStream struct {
	Index        int    `json:"index"` // 0-based subtitle stream index (for FFmpeg -map 0:s:N)
	Language     string `json:"language"`
	DisplayTitle string `json:"displayTitle"`
	Codec        string `json:"codec"`
	Default      bool   `json:"default"`
	Type         string `json:"type"` // "text" or "pgs" (raster, burn-in only)
}

// SubtitleEntry represents a single subtitle line block with its time range and text.
type SubtitleEntry struct {
	Start int64  `json:"start"` // milliseconds from start of video
	End   int64  `json:"end"`   // milliseconds from start of video
	Text  string `json:"text"`
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

func isPGSSubtitle(codec, format string) bool {
	if codec == "hdmv_pgs_subtitle" || codec == "pgssub" || codec == "pgs" || codec == "PGS" {
		return true
	}
	if format == "hdmv_pgs_subtitle" || format == "pgssub" || format == "pgs" || format == "PGS" {
		return true
	}
	return false
}

// subtitleSource is the immutable selection of one subtitle from Plex
// metadata. Index is the 0-based subtitle ordinal exposed to callers;
// EmbeddedIndex is the separate ordinal used by FFmpeg for subtitles that are
// actually present in the part. External subtitles must never use
// EmbeddedIndex or the part URL for extraction.
type subtitleSource struct {
	Index         int
	EmbeddedIndex int
	StreamKey     string
	Codec         string
	Format        string
	External      bool
	PGS           bool
}

func isExternalSubtitleStream(stream components.Stream) bool {
	if stream.StreamType != 3 || stream.Key == "" {
		return false
	}
	// Plex supplies EmbeddedInVideo for streams that are present in the part
	// even when a stream key is also available.
	return stream.EmbeddedInVideo == nil
}

func selectSubtitleSource(streams []components.Stream, subtitleIndex int) (subtitleSource, error) {
	if subtitleIndex < 0 {
		return subtitleSource{}, fmt.Errorf("subtitle index is invalid")
	}

	subtitleOrdinal := 0
	embeddedOrdinal := 0
	for _, stream := range streams {
		if stream.StreamType != 3 {
			continue
		}
		external := isExternalSubtitleStream(stream)
		if subtitleOrdinal == subtitleIndex {
			format := ""
			if stream.Format != nil {
				format = *stream.Format
			}
			embeddedIndex := -1
			if !external {
				embeddedIndex = embeddedOrdinal
			}
			return subtitleSource{
				Index:         subtitleOrdinal,
				EmbeddedIndex: embeddedIndex,
				StreamKey:     stream.Key,
				Codec:         stream.Codec,
				Format:        format,
				External:      external,
				PGS:           isPGSSubtitle(stream.Codec, format),
			}, nil
		}
		subtitleOrdinal++
		if !external {
			embeddedOrdinal++
		}
	}
	return subtitleSource{}, fmt.Errorf("subtitle index %d is not available", subtitleIndex)
}

func isSupportedTextSubtitle(source subtitleSource) bool {
	codec := strings.ToLower(strings.TrimSpace(source.Codec))
	format := strings.ToLower(strings.TrimSpace(source.Format))
	return textSubtitleCodecs[codec] || textSubtitleCodecs[format]
}

func subtitleSourceCodec(source subtitleSource) string {
	codec := strings.TrimSpace(source.Codec)
	if textSubtitleCodecs[strings.ToLower(codec)] || isPGSSubtitle(codec, "") {
		return source.Codec
	}
	if strings.TrimSpace(source.Format) != "" {
		return source.Format
	}
	if codec != "" {
		return source.Codec
	}
	return source.Format
}

// findMediaByID returns the first media whose Media.ID or any Part.ID matches
// the given id. Plex media ids and part ids live in the same namespace, so a
// caller may supply either. Returns nil when there is no match.
func findMediaByID(media []components.Media, id int64) *components.Media {
	for i := range media {
		if media[i].ID == id {
			return &media[i]
		}
		for j := range media[i].Part {
			if media[i].Part[j].ID == id {
				return &media[i]
			}
		}
	}
	return nil
}

// findDefaultMedia returns the first media part suitable for transcoding,
// skipping 10-bit (main 10) encodes that don't work correctly on NVIDIA
// hardware (and maybe others). Returns nil when there is no suitable media.
func findDefaultMedia(media []components.Media) *components.Media {
	for i := range media {
		if media[i].VideoProfile != nil && *media[i].VideoProfile == "main 10" {
			continue
		}
		return &media[i]
	}
	return nil
}

// resolveMedia returns the media to use for a metadata item. When mediaIdSupplied
// is true, the returned media must match the supplied id (either by Media.ID or
// any Part.ID) and a descriptive error is returned when no media matches. When
// mediaId is omitted, the default media is selected automatically.
func resolveMedia(media []components.Media, mediaId int64, mediaIdSupplied bool) (*components.Media, error) {
	if mediaIdSupplied {
		m := findMediaByID(media, mediaId)
		if m == nil {
			return nil, fmt.Errorf("could not find media with id %d", mediaId)
		}
		return m, nil
	}

	m := findDefaultMedia(media)
	if m == nil {
		return nil, fmt.Errorf("could not find suitable media for rating key")
	}
	return m, nil
}

// resolvePart selects the part represented by mediaId. A Plex part ID points
// at an individual part, while a Plex media ID uses the media's first part,
// matching the source selection used by preview/render. When no ID is
// supplied, the caller's existing default-part behavior is preserved.
func resolvePart(media *components.Media, mediaId int64, mediaIdSupplied bool) (*components.Part, error) {
	if media == nil || len(media.Part) == 0 {
		return nil, nil
	}
	if !mediaIdSupplied || media.ID == mediaId {
		return &media.Part[0], nil
	}
	for i := range media.Part {
		if media.Part[i].ID == mediaId {
			return &media.Part[i], nil
		}
	}
	return nil, fmt.Errorf("could not find part with id %d", mediaId)
}

type subtitleCacheKey struct {
	ratingKey     string
	mediaId       int64
	subtitleIndex int
}

func (a *Application) GetSubtitleStreams(ctx context.Context, ratingKeyStr string, mediaIdStr ...string) ([]SubtitleStream, error) {
	libraryMetadata, err := a.plexAdmin.Content.GetMetadataItem(ctx, operations.GetMetadataItemRequest{
		Ids: []string{ratingKeyStr},
	})
	if err != nil {
		return nil, err
	}

	metadata := libraryMetadata.MediaContainerWithMetadata.MediaContainer.Metadata[0]
	if len(metadata.Media) == 0 {
		return nil, nil
	}

	var selectedPart *components.Part
	if len(metadata.Media[0].Part) > 0 {
		selectedPart = &metadata.Media[0].Part[0]
	}
	if len(mediaIdStr) > 0 && mediaIdStr[0] != "" {
		mediaId, err := strconv.ParseInt(mediaIdStr[0], 10, 64)
		if err != nil {
			return nil, fmt.Errorf("could not parse media id: %w", err)
		}
		media, err := resolveMedia(metadata.Media, mediaId, true)
		if err != nil {
			return nil, err
		}
		selectedPart, err = resolvePart(media, mediaId, true)
		if err != nil {
			return nil, err
		}
		if selectedPart == nil {
			return nil, nil
		}
	}
	if selectedPart == nil {
		return nil, nil
	}

	var result []SubtitleStream
	subtitleIdx := 0
	for _, stream := range selectedPart.Stream {
		if stream.StreamType != 3 {
			continue
		}

		codec := stream.Codec
		format := ""
		if stream.Format != nil {
			format = *stream.Format
		}

		s := SubtitleStream{
			Index: subtitleIdx,
			Codec: codec,
		}
		if stream.Language != nil {
			s.Language = *stream.Language
		}
		s.DisplayTitle = stream.DisplayTitle
		if stream.Default != nil {
			s.Default = *stream.Default
		}

		if textSubtitleCodecs[codec] {
			s.Type = "text"
		} else if isPGSSubtitle(codec, format) {
			s.Type = "pgs"
		} else {
			subtitleIdx++
			continue
		}
		result = append(result, s)

		subtitleIdx++
	}

	return result, nil
}

func (a *Application) GetSubtitleEntries(ctx context.Context, ratingKeyStr, mediaIdStr string, subtitleIndex int) ([]SubtitleEntry, error) {
	operationCtx, cancelOperation := a.operationContext(ctx)
	defer cancelOperation()

	var mediaId int64
	if mediaIdStr != "" {
		var err error
		mediaId, err = strconv.ParseInt(mediaIdStr, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("could not parse media id: %w", err)
		}
	}

	cacheKey := subtitleCacheKey{
		ratingKey:     ratingKeyStr,
		mediaId:       mediaId,
		subtitleIndex: subtitleIndex,
	}

	if entries, ok := a.subtitleCache.get(cacheKey); ok {
		return entries, nil
	}

	libraryMetadata, err := a.plexAdmin.Content.GetMetadataItem(operationCtx, operations.GetMetadataItemRequest{
		Ids: []string{ratingKeyStr},
	})
	if err != nil {
		return nil, fmt.Errorf("could not get library metadata: %w", err)
	}

	metadata := libraryMetadata.MediaContainerWithMetadata.MediaContainer.Metadata[0]

	media, err := resolveMedia(metadata.Media, mediaId, mediaIdStr != "")
	if err != nil {
		return nil, err
	}
	part, err := resolvePart(media, mediaId, mediaIdStr != "")
	if err != nil {
		return nil, err
	}
	if part == nil {
		return nil, nil
	}

	source, err := selectSubtitleSource(part.Stream, subtitleIndex)
	if err != nil {
		return nil, err
	}
	if source.PGS {
		return nil, fmt.Errorf("PGS subtitles cannot be extracted as text")
	}

	var entries []SubtitleEntry

	if source.External {
		if !isSupportedTextSubtitle(source) {
			return nil, fmt.Errorf("external subtitle codec is not supported")
		}
		entries, err = a.downloadSubtitle(operationCtx, source.StreamKey, subtitleSourceCodec(source))
		if err != nil {
			return nil, fmt.Errorf("could not download external subtitle: %w", err)
		}
		a.subtitleCache.set(cacheKey, entries)
		return entries, nil
	}

	fileURL := fmt.Sprintf("%s%s?X-Plex-Token=%s",
		a.config.Plex.Host,
		part.Key,
		a.config.Plex.Token,
	)

	release, err := a.acquireFFmpeg(operationCtx)
	if err != nil {
		return nil, fmt.Errorf("could not acquire encoder: %w", err)
	}
	tmpFile, err := ExtractSubtitleFullContext(operationCtx, fileURL, source.EmbeddedIndex)
	release()
	if err != nil {
		return nil, fmt.Errorf("could not extract subtitle: %w", err)
	}
	defer os.Remove(tmpFile)

	entries, err = ParseSRT(tmpFile)
	if err != nil {
		return nil, err
	}

	a.subtitleCache.set(cacheKey, entries)

	return entries, nil
}

func (a *Application) downloadSubtitle(ctx context.Context, streamKey, codec string) ([]SubtitleEntry, error) {
	streamURL := fmt.Sprintf("%s%s?X-Plex-Token=%s",
		a.config.Plex.Host,
		streamKey,
		a.config.Plex.Token,
	)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, streamURL, nil)
	if err != nil {
		return nil, err
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("subtitle download returned status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	switch strings.ToLower(strings.TrimSpace(codec)) {
	case "webvtt":
		return ParseWebVTT(body)
	case "ass", "ssa":
		return ParseASS(body)
	case "srt", "subrip", "text":
		tmpFile, err := os.CreateTemp("", "cutscene_sub_*.srt")
		if err != nil {
			return nil, err
		}
		tmpFilePath := tmpFile.Name()
		defer os.Remove(tmpFilePath)
		if _, err := tmpFile.Write(body); err != nil {
			_ = tmpFile.Close()
			return nil, err
		}
		if err := tmpFile.Close(); err != nil {
			return nil, err
		}
		return ParseSRT(tmpFilePath)
	default:
		return nil, fmt.Errorf("unsupported native subtitle codec %q", codec)
	}
}

func (a *Application) prepareExternalSubtitle(ctx context.Context, source subtitleSource, fromMs, toMs int64) (string, error) {
	if !source.External || source.StreamKey == "" {
		return "", fmt.Errorf("external subtitle stream key is missing")
	}
	if source.PGS || !isSupportedTextSubtitle(source) {
		return "", fmt.Errorf("external subtitle codec is not supported")
	}
	entries, err := a.downloadSubtitle(ctx, source.StreamKey, subtitleSourceCodec(source))
	if err != nil {
		return "", fmt.Errorf("could not download external subtitle: %w", err)
	}
	subtitleFile, err := WriteClipSRT(entries, fromMs, toMs)
	if err != nil {
		return "", fmt.Errorf("could not write external subtitle: %w", err)
	}
	return subtitleFile, nil
}

func (a *Application) GetCachedSubtitleEntries(ratingKeyStr, mediaIdStr string, subtitleIndex int) ([]SubtitleEntry, bool) {
	var mediaId int64
	if mediaIdStr != "" {
		var err error
		mediaId, err = strconv.ParseInt(mediaIdStr, 10, 64)
		if err != nil {
			return nil, false
		}
	}

	return a.subtitleCache.get(subtitleCacheKey{
		ratingKey:     ratingKeyStr,
		mediaId:       mediaId,
		subtitleIndex: subtitleIndex,
	})
}

func (a *Application) Clip(ctx context.Context, ratingKeyStr, mediaIdStr, from, to string, height, qp, subtitleIndex int) (string, error) {
	libraryMetadata, err := a.plexAdmin.Content.GetMetadataItem(ctx, operations.GetMetadataItemRequest{
		Ids: []string{ratingKeyStr},
	})
	if err != nil {
		return "", fmt.Errorf("could not get library metadata: %w", err)
	}

	metadata := libraryMetadata.MediaContainerWithMetadata.MediaContainer.Metadata[0]

	var mediaId int64
	mediaIdSupplied := mediaIdStr != ""
	if mediaIdSupplied {
		mediaId, err = strconv.ParseInt(mediaIdStr, 10, 64)
		if err != nil {
			return "", fmt.Errorf("could not parse media id: %w", err)
		}
	}

	media, err := resolveMedia(metadata.Media, mediaId, mediaIdSupplied)
	if err != nil {
		return "", err
	}
	part, err := resolvePart(media, mediaId, mediaIdSupplied)
	if err != nil {
		return "", err
	}
	if part == nil {
		return "", fmt.Errorf("could not find playable part for media")
	}

	fileURL := fmt.Sprintf("%s%s?X-Plex-Token=%s",
		a.config.Plex.Host,
		part.Key,
		a.config.Plex.Token,
	)

	// Extract subtitle for burning in if requested. For text subtitles, ExtractSubtitle
	// runs a short FFmpeg pass to demux the subtitle stream into a temp SRT file.
	// For PGS subtitles, we pass SubtitleIndex directly to FFmpeg overlay filter.
	var subtitleFile string
	var subtitleIdxForFFmpeg int = -1
	if subtitleIndex >= 0 {
		source, sourceErr := selectSubtitleSource(part.Stream, subtitleIndex)
		if sourceErr != nil {
			return "", sourceErr
		}
		if source.External {
			fromMs, fromErr := ParseTimestampToMs(from)
			toMs, toErr := ParseTimestampToMs(to)
			if fromErr != nil || toErr != nil || toMs <= fromMs {
				return "", fmt.Errorf("could not parse clip range for external subtitle")
			}
			subtitleFile, err = a.prepareExternalSubtitle(ctx, source, fromMs, toMs)
			if err != nil {
				return "", err
			}
			defer os.Remove(subtitleFile)
		} else if source.PGS {
			subtitleIdxForFFmpeg = source.EmbeddedIndex
		} else {
			release, acquireErr := a.acquireFFmpeg(ctx)
			if acquireErr != nil {
				return "", fmt.Errorf("could not acquire encoder: %w", acquireErr)
			}
			subtitleFile, err = ExtractSubtitleContext(ctx, fileURL, from, to, source.EmbeddedIndex)
			release()
			if err != nil {
				return "", fmt.Errorf("could not extract subtitle: %w", err)
			}
			defer os.Remove(subtitleFile)
		}
	}

	var fileName string
	if metadata.Type == "episode" {
		fileName = fmt.Sprintf("%s S%02dE%02d %s (%s - %s).mp4",
			*metadata.GrandparentTitle,
			*metadata.ParentIndex,
			*metadata.Index,
			metadata.Title,
			from,
			to,
		)
	} else {
		fileName = fmt.Sprintf("%s (%d) (%s - %s).mp4",
			metadata.Title,
			*metadata.Year,
			from,
			to,
		)
	}

	params := FfmpegParams{
		URL:           fileURL,
		From:          from,
		To:            to,
		Filename:      fileName,
		Codec:         a.config.Ffmpeg.Codec,
		Height:        height,
		QP:            qp,
		SubtitleFile:  subtitleFile,
		SubtitleIndex: subtitleIdxForFFmpeg,
		Metadata: FfmpegParamsMetadata{
			Title: metadata.Title,
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

	release, err := a.acquireFFmpeg(ctx)
	if err != nil {
		return "", fmt.Errorf("could not acquire encoder: %w", err)
	}
	defer release()
	return DoFfmpeg(params)
}

func (a *Application) Thumb(ctx context.Context, thumb string) (io.ReadCloser, string, error) {
	transcodeURL := fmt.Sprintf("%s/photo/:/transcode?width=320&height=320&url=%s&X-Plex-Token=%s",
		a.config.Plex.Host,
		url.QueryEscape(thumb),
		a.config.Plex.Token,
	)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, transcodeURL, nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("Accept", "image/*")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, "", err
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		resp.Body.Close()
		return nil, "", fmt.Errorf("thumbnail proxy returned status %d: %s", resp.StatusCode, string(body))
	}

	contentType := resp.Header.Get("Content-Type")
	return resp.Body, contentType, nil
}
