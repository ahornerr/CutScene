package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"log"
	"mime"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/LukeHagar/plexgo"
	"github.com/LukeHagar/plexgo/models/components"
	"github.com/LukeHagar/plexgo/models/operations"
)

// subtitleCache is a bounded FIFO cache for subtitle entries.
type subtitleCache struct {
	mu    sync.RWMutex
	max   int
	m     map[subtitleCacheEntryKey][]SubtitleEntry
	order []subtitleCacheEntryKey
}

type subtitleCacheEntryKey struct {
	key      subtitleCacheKey
	callerID string
}

func newSubtitleCache(max int) *subtitleCache {
	return &subtitleCache{
		max: max,
		m:   make(map[subtitleCacheEntryKey][]SubtitleEntry),
	}
}

func (c *subtitleCache) get(key subtitleCacheKey) ([]SubtitleEntry, bool) {
	return c.getForCaller(key, "legacy")
}

func (c *subtitleCache) getForCaller(key subtitleCacheKey, callerID string) ([]SubtitleEntry, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	entries, ok := c.m[subtitleCacheEntryKey{key: key, callerID: callerID}]
	if !ok {
		return nil, false
	}
	// defensive copy: prevent caller mutation from escaping
	result := make([]SubtitleEntry, len(entries))
	copy(result, entries)
	return result, true
}

func (c *subtitleCache) set(key subtitleCacheKey, entries []SubtitleEntry) {
	c.setForCaller(key, entries, "legacy")
}

func (c *subtitleCache) setForCaller(key subtitleCacheKey, entries []SubtitleEntry, callerID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	// defensive copy
	entriesCopy := make([]SubtitleEntry, len(entries))
	copy(entriesCopy, entries)

	entryKey := subtitleCacheEntryKey{key: key, callerID: callerID}
	if _, exists := c.m[entryKey]; !exists && len(c.m) >= c.max && c.max > 0 {
		// FIFO eviction: remove oldest entry
		oldest := c.order[0]
		delete(c.m, oldest)
		c.order = c.order[1:]
	}
	if _, exists := c.m[entryKey]; !exists {
		c.order = append(c.order, entryKey)
	}
	c.m[entryKey] = entriesCopy
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
	Title              string      `json:"title"`
	Type               string      `json:"type"`
	GrandparentTitle   *string     `json:"grandparentTitle,omitempty"`
	ParentIndex        *int        `json:"parentIndex,omitempty"`
	Index              *int        `json:"index,omitempty"`
	Year               *int        `json:"year,omitempty"`
	Thumb              *string     `json:"thumb,omitempty"`
	GrandparentThumb   *string     `json:"grandparentThumb,omitempty"`
	Key                string      `json:"key"`
	RatingKey          *string     `json:"ratingKey,omitempty"`
	Duration           *int        `json:"duration,omitempty"`
	ViewOffset         *int64      `json:"viewOffset,omitempty"`
	User               sessionUser `json:"User"`
	OwnedByCurrentUser bool        `json:"ownedByCurrentUser"`
	Player             struct {
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

// sessionUser accepts both forms emitted by Plex: User.id is normally a
// quoted value, but some Plex versions serialize it as a JSON number.
type sessionUser struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Thumb string `json:"thumb"`
}

func (u *sessionUser) UnmarshalJSON(data []byte) error {
	var raw struct {
		ID    json.RawMessage `json:"id"`
		Title string          `json:"title"`
		Thumb string          `json:"thumb"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}

	id, err := plexNumericIDText(raw.ID)
	if err != nil {
		return fmt.Errorf("invalid session user id: %w", err)
	}
	u.ID = id
	u.Title = raw.Title
	u.Thumb = raw.Thumb
	return nil
}

type sessionMedia struct {
	ID              any           `json:"id"`
	Duration        *int          `json:"duration,omitempty"`
	Width           *int          `json:"width,omitempty"`
	Height          *int          `json:"height,omitempty"`
	Bitrate         *int          `json:"bitrate,omitempty"`
	AudioCodec      *string       `json:"audioCodec,omitempty"`
	AudioChannels   *int          `json:"audioChannels,omitempty"`
	VideoCodec      *string       `json:"videoCodec,omitempty"`
	VideoProfile    *string       `json:"videoProfile,omitempty"`
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
	ownerUUID         string
	subtitleCache     *subtitleCache
	ffmpegLimiter     *ffmpegLimiter
	renderJobs        *renderJobManager
	clipStore         *clipStore
	lifetime          context.Context
	cancelLifetime    context.CancelFunc
	closeOnce         sync.Once
	closeErr          error
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
	app.ownerUUID = tokenDetails.UserPlexAccount.UUID

	// TODO: If configured, ignore auth from context and just use the configured token for all requests
	app.plexUser = plexgo.New(
		plexgo.WithServerURL(config.Plex.Host),
		plexgo.WithSecuritySource(app.plexSecurityUserToken),
		plexgo.WithClient(callerPlexHTTPClient),
	)

	app.clipStore, err = newClipStore(durableStorageRoot(config), config.Storage.Database)
	if err != nil {
		return nil, fmt.Errorf("could not initialize clip storage: %w", err)
	}
	app.renderJobs, err = newRenderJobManagerWithContextAndPromotion(app.lifetime, renderRoot, app.executeRenderSpec, func(job *renderJob, outputPath string) error {
		artworkContext, cancelArtwork := context.WithTimeout(app.lifetime, 30*time.Second)
		defer cancelArtwork()
		artworkToken := job.spec.SourceToken
		if artworkToken == "" {
			artworkToken = app.config.Plex.Token
		}
		artworkPath, artworkMIME, artworkErr := app.fetchClipArtworkWithToken(artworkContext, job.spec.ThumbnailURL, artworkToken)
		if artworkErr != nil {
			log.Printf("clip artwork snapshot unavailable: %s", redactedDiagnostic(artworkErr))
		}
		if artworkPath != "" {
			defer os.Remove(artworkPath)
		}
		clip, promoteErr := app.clipStore.promoteWithArtwork(job.spec, outputPath, artworkPath, artworkMIME)
		if promoteErr != nil {
			return newRenderStageFailure("storage", "storage_full", promoteErr)
		}
		job.mu.Lock()
		job.clipID = clip.ID
		job.shareURL = clipShareURLForDomain(config.API.Domain, clip.ShareToken)
		job.mu.Unlock()
		return nil
	})
	if err != nil {
		_ = app.clipStore.close()
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
		a.stopWork()
		if a.clipStore != nil {
			if err := a.clipStore.close(); err != nil {
				a.closeErr = err
				log.Printf("clip storage close failed: %s", redactedDiagnostic(err))
			}
		}
	})
	return a.closeErr
}

const maxClipArtworkBytes int64 = 8 << 20

func (a *Application) fetchClipArtwork(ctx context.Context, artworkPath string) (string, string, error) {
	return a.fetchClipArtworkWithToken(ctx, artworkPath, a.config.Plex.Token)
}

func (a *Application) fetchClipArtworkWithToken(ctx context.Context, artworkPath, token string) (string, string, error) {
	if strings.TrimSpace(artworkPath) == "" {
		return "", "", nil
	}
	base, err := url.Parse(a.config.Plex.Host)
	if err != nil {
		return "", "", err
	}
	if (base.Scheme != "http" && base.Scheme != "https") || base.Host == "" || base.User != nil {
		return "", "", errors.New("configured Plex origin is invalid")
	}
	if strings.HasPrefix(artworkPath, "//") {
		return "", "", errors.New("scheme-relative artwork URL is not allowed")
	}
	parsed, err := url.Parse(artworkPath)
	if err != nil {
		return "", "", err
	}
	if parsed.User != nil {
		return "", "", errors.New("artwork URL userinfo is not allowed")
	}
	if !parsed.IsAbs() && !strings.HasPrefix(parsed.Path, "/") {
		return "", "", errors.New("artwork URL must be an absolute Plex path")
	}
	if parsed.IsAbs() && (!strings.EqualFold(parsed.Scheme, base.Scheme) || !strings.EqualFold(parsed.Host, base.Host)) {
		return "", "", errors.New("clip artwork is not hosted by configured Plex")
	}
	if !parsed.IsAbs() {
		parsed = base.ResolveReference(parsed)
	}
	if parsed.User != nil || !strings.EqualFold(parsed.Scheme, base.Scheme) || !strings.EqualFold(parsed.Host, base.Host) {
		return "", "", errors.New("clip artwork origin is not the configured Plex origin")
	}
	query := parsed.Query()
	query.Set("X-Plex-Token", token)
	parsed.RawQuery = query.Encode()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return "", "", err
	}
	client := &http.Client{CheckRedirect: func(next *http.Request, via []*http.Request) error {
		if len(via) == 0 {
			return nil
		}
		previous := via[0].URL
		if next.URL.User != nil || !strings.EqualFold(next.URL.Scheme, previous.Scheme) || !strings.EqualFold(next.URL.Host, previous.Host) {
			return errors.New("artwork redirect leaves configured Plex origin")
		}
		return nil
	}}
	response, err := client.Do(request)
	if err != nil {
		return "", "", err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return "", "", fmt.Errorf("artwork returned status %d", response.StatusCode)
	}
	contentType, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil {
		return "", "", errors.New("artwork response has invalid content type")
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, maxClipArtworkBytes+1))
	if err != nil || int64(len(data)) == 0 || int64(len(data)) > maxClipArtworkBytes {
		if err != nil {
			return "", "", err
		}
		return "", "", errors.New("artwork response is empty or too large")
	}
	if err := validateRasterArtwork(contentType, data); err != nil {
		return "", "", err
	}
	tmp, err := os.CreateTemp("", "cutscene-artwork-*")
	if err != nil {
		return "", "", err
	}
	tmpPath := tmp.Name()
	count, copyErr := io.Copy(tmp, bytes.NewReader(data))
	closeErr := tmp.Close()
	if copyErr != nil || closeErr != nil || count == 0 || count > maxClipArtworkBytes {
		_ = os.Remove(tmpPath)
		if copyErr != nil {
			return "", "", copyErr
		}
		if closeErr != nil {
			return "", "", closeErr
		}
		return "", "", errors.New("artwork response is empty or too large")
	}
	return tmpPath, contentType, nil
}

func validateRasterArtwork(contentType string, data []byte) error {
	switch strings.ToLower(contentType) {
	case "image/jpeg", "image/png", "image/gif":
		_, format, err := image.DecodeConfig(bytes.NewReader(data))
		if err != nil || format == "" {
			return errors.New("artwork bytes are not a valid raster image")
		}
		return nil
	case "image/webp":
		if len(data) < 12 || string(data[:4]) != "RIFF" || string(data[8:12]) != "WEBP" {
			return errors.New("artwork bytes are not a valid WebP image")
		}
		return nil
	default:
		return errors.New("artwork response is not an allowed raster image")
	}
}

// stopWork cancels request-scoped background work without closing durable
// storage. API shutdown uses this before draining HTTP handlers so handlers
// can finish their SQLite/file operations safely.
func (a *Application) stopWork() {
	if a == nil {
		return
	}
	if a.cancelLifetime != nil {
		a.cancelLifetime()
	}
	if a.renderJobs != nil {
		a.renderJobs.stopAndWait()
	}
}

func normalizeOwnerIdentity(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

// isServerOwner is the single administrator invariant. Plex account UUID is
// stable and preferred; older/test identities without UUID fall back to the
// normalized account email captured from the configured server-owner token.
func (a *Application) isServerOwner(user *User) bool {
	if a == nil || user == nil {
		return false
	}
	if ownerUUID := normalizeOwnerIdentity(a.ownerUUID); ownerUUID != "" && normalizeOwnerIdentity(user.Uuid) != "" {
		return ownerUUID == normalizeOwnerIdentity(user.Uuid)
	}
	ownerEmail := normalizeOwnerIdentity(a.ownerEmail)
	return ownerEmail != "" && ownerEmail == normalizeOwnerIdentity(user.Email)
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

// getMetadataItem keeps the access decision explicit. Library requests must
// use the token belonging to the caller; the configured administrator client
// is retained for the existing active-session path.
func (a *Application) getMetadataItem(ctx context.Context, ratingKey string, userScoped bool) (*components.Metadata, error) {
	if userScoped {
		if token := AuthTokenFromContext(ctx); token == nil || strings.TrimSpace(*token) == "" {
			return nil, errors.New("caller-scoped Plex client is unavailable")
		} else {
			return a.getCallerMetadataItem(ctx, ratingKey, *token)
		}
	} else {
		if a.plexAdmin == nil {
			return nil, errors.New("Plex client is unavailable")
		}
		return a.getAdminMetadataItem(ctx, ratingKey)
	}
}

// getAdminMetadataItem keeps the historical admin behavior (a missing
// ratingKey is tolerated) while using the bounded library decoder rather than
// the generated Plex client decoder.
func (a *Application) getAdminMetadataItem(ctx context.Context, ratingKey string) (*components.Metadata, error) {
	base, err := url.Parse(a.config.Plex.Host)
	if err != nil || (base.Scheme != "http" && base.Scheme != "https") || base.Host == "" || base.User != nil {
		return nil, errors.New("configured Plex origin is invalid")
	}
	base.Path = strings.TrimRight(base.Path, "/") + "/library/metadata/" + url.PathEscape(ratingKey)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, base.String(), nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("X-Plex-Token", a.config.Plex.Token)
	request.Header.Set("Accept", "application/json")
	response, err := callerPlexHTTPClient.Do(request)
	if err != nil {
		return nil, fmt.Errorf("could not get Plex metadata: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, &libraryHTTPError{status: response.StatusCode}
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxLibraryResponseBytes))
	if err != nil {
		return nil, fmt.Errorf("could not read Plex metadata: %w", err)
	}
	var decoded plexMetadataResponse
	if err := unmarshalPlexLibraryJSON(body, &decoded); err != nil {
		return nil, fmt.Errorf("could not decode Plex metadata: %w", err)
	}
	if decoded.MediaContainer == nil || len(decoded.MediaContainer.Metadata) == 0 {
		return nil, &sourceValidationError{message: "requested media is unavailable"}
	}
	metadata := decoded.MediaContainer.Metadata[0]
	if metadata.RatingKey != nil && *metadata.RatingKey != "" && *metadata.RatingKey != ratingKey {
		return nil, &sourceValidationError{message: "requested media is unavailable"}
	}
	return &metadata, nil
}

// GetLibrarySource resolves one caller-visible library Media/Part pair and
// reduces it to the stable LibrarySearchResult contract. The explicit IDs are
// required to keep a deep link bound to exactly the source it names.
func (a *Application) GetLibrarySource(ctx context.Context, ratingKey string, mediaID, partID int64) (LibrarySearchResult, error) {
	ratingKey, err := validateLibraryRatingKey(ratingKey)
	if err != nil {
		return LibrarySearchResult{}, err
	}
	if mediaID <= 0 {
		return LibrarySearchResult{}, errors.New("mediaId is invalid")
	}
	if partID <= 0 {
		return LibrarySearchResult{}, errors.New("partId is invalid")
	}
	token := AuthTokenFromContext(ctx)
	if token == nil || strings.TrimSpace(*token) == "" {
		return LibrarySearchResult{}, errors.New("missing auth token")
	}

	sourceCtx, cancel := context.WithTimeout(ctx, librarySearchTimeout)
	defer cancel()
	metadata, err := a.getMetadataItem(sourceCtx, ratingKey, true)
	if err != nil {
		return LibrarySearchResult{}, err
	}
	media, part, err := resolveLibraryMetadataSource(metadata, mediaID, partID)
	if err != nil {
		return LibrarySearchResult{}, err
	}
	duration, err := selectedSourceDuration(media, part)
	if err != nil {
		return LibrarySearchResult{}, err
	}
	result := librarySearchResultFromMetadata(metadata, media, part)
	// Keep the discovery formatter's title-duration fallback when the selected
	// source does not carry its own duration. In particular, a single-part
	// source may only expose duration on the top-level metadata item.
	if duration > 0 {
		result.Duration = duration
	}
	return result, nil
}

func (a *Application) plexSourceToken(ctx context.Context, userScoped bool) string {
	if userScoped {
		if token := AuthTokenFromContext(ctx); token != nil && strings.TrimSpace(*token) != "" {
			return *token
		}
	}
	return a.config.Plex.Token
}

func (a *Application) GetValidatedUser(ctx context.Context) (*User, error) {
	user, err := NewPlexTV(*AuthTokenFromContext(ctx)).getUser()
	if err != nil {
		return nil, err
	}

	if a.isServerOwner(user) {
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

	user := UserFromContext(ctx)
	serverOwner := a.isServerOwner(user)
	for i := range sessions.MediaContainer.Metadata {
		sessions.MediaContainer.Metadata[i].OwnedByCurrentUser = sessionOwnedByCurrentUser(sessions.MediaContainer.Metadata[i], user, serverOwner)
	}

	// Non-owner users see only their own sessions
	if user != nil && !serverOwner {
		var filtered []sessionMetadata
		for _, s := range sessions.MediaContainer.Metadata {
			if sessionVisibleToCurrentUser(s, user) {
				filtered = append(filtered, s)
			}
		}
		return filtered, nil
	}

	return sessions.MediaContainer.Metadata, nil
}

func sessionOwnedByCurrentUser(session sessionMetadata, user *User, serverOwner bool) bool {
	if user == nil {
		return false
	}

	sessionUserID, err := strconv.ParseInt(session.User.ID, 10, 64)
	if err != nil {
		return false
	}

	// PMS uses local playback-profile IDs. The configured server owner's
	// profile is the stable local ID 1, not necessarily the Plex account ID.
	if serverOwner {
		return sessionUserID == 1
	}

	// A non-owner must never claim the owner's local profile. For other local
	// IDs, retain the direct numeric match as an additional positive signal.
	if sessionUserID == 1 {
		return false
	}
	if sessionUserID == int64(user.Id) {
		return true
	}

	for _, identity := range []string{user.Username, user.Title, user.Email} {
		if identity != "" && strings.EqualFold(strings.TrimSpace(session.User.Title), strings.TrimSpace(identity)) {
			return true
		}
	}

	return false
}

// sessionVisibleToCurrentUser intentionally remains ID-based. Ownership
// annotations must not change the existing non-owner access boundary.
func sessionVisibleToCurrentUser(session sessionMetadata, user *User) bool {
	if user == nil {
		return false
	}

	sessionUserID, err := strconv.ParseInt(session.User.ID, 10, 64)
	return err == nil && sessionUserID == int64(user.Id)
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

func subtitleCallerID(ctx context.Context) (string, error) {
	if user := UserFromContext(ctx); user != nil && strings.TrimSpace(user.Uuid) != "" {
		return "plex-user:" + normalizeOwnerIdentity(user.Uuid), nil
	}
	if AuthTokenFromContext(ctx) != nil {
		return "", errors.New("authenticated user is missing a stable id")
	}
	// Direct unauthenticated unit callers retain the old in-process namespace;
	// HTTP callers cannot reach subtitle handlers without validated auth.
	return "legacy", nil
}

func resolveRequestedSource(metadata *components.Metadata, mediaIDStr, partIDStr string) (*components.Media, *components.Part, error) {
	var mediaID, partID int64
	var err error
	if mediaIDStr != "" {
		mediaID, err = strconv.ParseInt(mediaIDStr, 10, 64)
		if err != nil || mediaID <= 0 {
			return nil, nil, errors.New("mediaId is invalid")
		}
	}
	if partIDStr != "" {
		partID, err = strconv.ParseInt(partIDStr, 10, 64)
		if err != nil || partID <= 0 {
			return nil, nil, errors.New("partId is invalid")
		}
	}
	if partID > 0 {
		return resolveLibraryMetadataSource(metadata, mediaID, partID)
	}
	if mediaID > 0 {
		return selectLibraryMetadataSource(metadata, mediaID, true)
	}
	return selectLibraryMetadataSource(metadata, 0, false)
}

func (a *Application) GetSubtitleStreams(ctx context.Context, ratingKeyStr string, mediaIdStr ...string) ([]SubtitleStream, error) {
	mediaID := ""
	if len(mediaIdStr) > 0 {
		mediaID = mediaIdStr[0]
	}
	return a.GetSubtitleStreamsForSource(ctx, ratingKeyStr, mediaID, "")
}

func (a *Application) GetSubtitleStreamsForSource(ctx context.Context, ratingKeyStr, mediaIdStr, partIdStr string) ([]SubtitleStream, error) {
	metadata, err := a.getMetadataItem(ctx, ratingKeyStr, AuthTokenFromContext(ctx) != nil)
	if err != nil {
		return nil, err
	}
	if len(metadata.Media) == 0 {
		return nil, nil
	}

	_, selectedPart, err := resolveRequestedSource(metadata, mediaIdStr, partIdStr)
	if err != nil {
		return nil, err
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
	return a.GetSubtitleEntriesForSource(ctx, ratingKeyStr, mediaIdStr, "", subtitleIndex)
}

func (a *Application) GetSubtitleEntriesForSource(ctx context.Context, ratingKeyStr, mediaIdStr, partIdStr string, subtitleIndex int) ([]SubtitleEntry, error) {
	operationCtx, cancelOperation := a.operationContext(ctx)
	defer cancelOperation()

	metadata, err := a.getMetadataItem(operationCtx, ratingKeyStr, AuthTokenFromContext(operationCtx) != nil)
	if err != nil {
		return nil, fmt.Errorf("could not get library metadata: %w", err)
	}

	_, part, err := resolveRequestedSource(metadata, mediaIdStr, partIdStr)
	if err != nil {
		return nil, err
	}
	if part == nil {
		return nil, nil
	}
	callerID, err := subtitleCallerID(operationCtx)
	if err != nil {
		return nil, err
	}
	if a.subtitleCache == nil {
		return nil, errors.New("subtitle cache is unavailable")
	}
	cacheKey := subtitleCacheKey{ratingKey: ratingKeyStr, mediaId: part.ID, subtitleIndex: subtitleIndex}
	if entries, ok := a.subtitleCache.getForCaller(cacheKey, callerID); ok {
		return entries, nil
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
		a.subtitleCache.setForCaller(cacheKey, entries, callerID)
		return entries, nil
	}

	fileURL := fmt.Sprintf("%s%s?X-Plex-Token=%s",
		a.config.Plex.Host,
		part.Key,
		a.plexSourceToken(operationCtx, AuthTokenFromContext(operationCtx) != nil),
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

	a.subtitleCache.setForCaller(cacheKey, entries, callerID)

	return entries, nil
}

func (a *Application) downloadSubtitle(ctx context.Context, streamKey, codec string) ([]SubtitleEntry, error) {
	return a.downloadSubtitleWithToken(ctx, streamKey, codec, a.plexSourceToken(ctx, AuthTokenFromContext(ctx) != nil))
}

func (a *Application) downloadSubtitleWithToken(ctx context.Context, streamKey, codec, token string) ([]SubtitleEntry, error) {
	streamURL := fmt.Sprintf("%s%s?X-Plex-Token=%s",
		a.config.Plex.Host,
		streamKey,
		token,
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

func (a *Application) prepareExternalSubtitle(ctx context.Context, source subtitleSource, fromMs, toMs int64, subtitleOffsets ...int64) (string, error) {
	return a.prepareExternalSubtitleWithToken(ctx, source, fromMs, toMs, subtitleOffsets, a.plexSourceToken(ctx, AuthTokenFromContext(ctx) != nil))
}

func (a *Application) prepareExternalSubtitleWithToken(ctx context.Context, source subtitleSource, fromMs, toMs int64, subtitleOffsets []int64, token string) (string, error) {
	if !source.External || source.StreamKey == "" {
		return "", fmt.Errorf("external subtitle stream key is missing")
	}
	if source.PGS || !isSupportedTextSubtitle(source) {
		return "", fmt.Errorf("external subtitle codec is not supported")
	}
	entries, err := a.downloadSubtitleWithToken(ctx, source.StreamKey, subtitleSourceCodec(source), token)
	if err != nil {
		return "", fmt.Errorf("could not download external subtitle: %w", err)
	}
	subtitleFile, err := WriteClipSRT(entries, fromMs, toMs, subtitleOffsets...)
	if err != nil {
		return "", fmt.Errorf("could not write external subtitle: %w", err)
	}
	return subtitleFile, nil
}

func (a *Application) GetCachedSubtitleEntries(ctx context.Context, ratingKeyStr, mediaIdStr, partIdStr string, subtitleIndex int) ([]SubtitleEntry, bool) {
	if a.subtitleCache == nil {
		return nil, false
	}
	metadata, err := a.getMetadataItem(ctx, ratingKeyStr, AuthTokenFromContext(ctx) != nil)
	if err != nil {
		return nil, false
	}
	_, part, err := resolveRequestedSource(metadata, mediaIdStr, partIdStr)
	if err != nil || part == nil {
		return nil, false
	}
	callerID, err := subtitleCallerID(ctx)
	if err != nil {
		return nil, false
	}
	return a.subtitleCache.getForCaller(subtitleCacheKey{
		ratingKey: ratingKeyStr, mediaId: part.ID, subtitleIndex: subtitleIndex,
	}, callerID)
}

func (a *Application) Clip(ctx context.Context, ratingKeyStr, mediaIdStr, from, to string, height, qp, subtitleIndex int) (string, error) {
	metadata, err := a.getMetadataItem(ctx, ratingKeyStr, AuthTokenFromContext(ctx) != nil)
	if err != nil {
		return "", fmt.Errorf("could not get library metadata: %w", err)
	}

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
		a.plexSourceToken(ctx, AuthTokenFromContext(ctx) != nil),
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
	token := a.plexSourceToken(ctx, AuthTokenFromContext(ctx) != nil)
	transcodeURL := fmt.Sprintf("%s/photo/:/transcode?width=320&height=320&url=%s&X-Plex-Token=%s",
		a.config.Plex.Host,
		url.QueryEscape(thumb),
		token,
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
