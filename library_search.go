package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/LukeHagar/plexgo/models/components"
)

const (
	maxLibrarySearchQueryRunes = 128
	maxLibrarySearchResults    = 50
	maxLibrarySearchCandidates = 100
	maxLibrarySearchRefetches  = 12
	// Hierarchy is explicitly rejected above this aggregate cap rather than
	// returning a silently truncated page. Phase 3B can add bounded paging.
	maxLibraryChildren      = 100
	librarySearchTimeout    = 10 * time.Second
	libraryChildrenTimeout  = 10 * time.Second
	maxLibraryResponseBytes = 8 << 20
)

// callerPlexHTTPClient never follows redirects. Plex tokens are credentials;
// stopping at the redirect response is safer than relying on net/http's
// redirect-header heuristics, and applies equally to search, metadata, and
// hierarchy requests.
var callerPlexHTTPClient = &http.Client{
	Timeout: librarySearchTimeout,
	CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
		return http.ErrUseLastResponse
	},
}

// LibrarySearchResult is deliberately independent of Plex's hub response. It
// is the stable, small contract used by callers to select a source. In
// particular, it never exposes a Plex filesystem path.
type LibrarySearchResult struct {
	RatingKey            string `json:"ratingKey"`
	MediaID              int64  `json:"mediaId,omitempty"`
	PartID               int64  `json:"partId,omitempty"`
	Title                string `json:"title"`
	Type                 string `json:"type"`
	Year                 *int   `json:"year,omitempty"`
	GrandparentTitle     string `json:"grandparentTitle,omitempty"`
	ParentTitle          string `json:"parentTitle,omitempty"`
	ParentRatingKey      string `json:"parentRatingKey,omitempty"`
	GrandparentRatingKey string `json:"grandparentRatingKey,omitempty"`
	SeasonNumber         *int   `json:"seasonNumber,omitempty"`
	EpisodeNumber        *int   `json:"episodeNumber,omitempty"`
	Duration             int64  `json:"duration"`
	Artwork              string `json:"artwork,omitempty"`

	VideoResolution string `json:"videoResolution,omitempty"`
	VideoCodec      string `json:"videoCodec,omitempty"`
	VideoProfile    string `json:"videoProfile,omitempty"`
	AudioCodec      string `json:"audioCodec,omitempty"`
	AudioChannels   int    `json:"audioChannels,omitempty"`
	Container       string `json:"container,omitempty"`
	Bitrate         int    `json:"bitrate,omitempty"`
	Width           int    `json:"width,omitempty"`
	Height          int    `json:"height,omitempty"`
	FileSize        int64  `json:"fileSize,omitempty"`
}

type plexSearchResponse struct {
	MediaContainer *struct {
		Hub      []plexSearchHub       `json:"Hub,omitempty"`
		Metadata []components.Metadata `json:"Metadata,omitempty"`
	} `json:"MediaContainer"`
}

type plexSearchHub struct {
	Metadata []components.Metadata `json:"Metadata,omitempty"`
}

type plexMetadataResponse struct {
	MediaContainer *struct {
		Metadata  []components.Metadata `json:"Metadata,omitempty"`
		TotalSize int                   `json:"totalSize,omitempty"`
		Offset    int                   `json:"offset,omitempty"`
		Size      int                   `json:"size,omitempty"`
	} `json:"MediaContainer"`
}

func validateLibrarySearchQuery(query string) (string, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return "", errors.New("query is required")
	}
	if !utf8.ValidString(query) || utf8.RuneCountInString(query) > maxLibrarySearchQueryRunes {
		return "", fmt.Errorf("query must be between 1 and %d characters", maxLibrarySearchQueryRunes)
	}
	for _, r := range query {
		if r < 0x20 || r == 0x7f {
			return "", errors.New("query contains invalid characters")
		}
	}
	return query, nil
}

func (a *Application) SearchLibrary(ctx context.Context, query string) ([]LibrarySearchResult, error) {
	query, err := validateLibrarySearchQuery(query)
	if err != nil {
		return nil, err
	}
	token := AuthTokenFromContext(ctx)
	if token == nil || strings.TrimSpace(*token) == "" {
		return nil, errors.New("missing auth token")
	}
	searchCtx, cancel := context.WithTimeout(ctx, librarySearchTimeout)
	defer cancel()

	base, err := url.Parse(a.config.Plex.Host)
	if err != nil || (base.Scheme != "http" && base.Scheme != "https") || base.Host == "" || base.User != nil {
		return nil, errors.New("configured Plex origin is invalid")
	}
	base.Path = strings.TrimRight(base.Path, "/") + "/hubs/search"
	values := base.Query()
	values.Set("query", query)
	values.Set("X-Plex-Container-Size", fmt.Sprintf("%d", maxLibrarySearchResults))
	base.RawQuery = values.Encode()

	req, err := http.NewRequestWithContext(searchCtx, http.MethodGet, base.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Plex-Token", *token)
	req.Header.Set("Accept", "application/json")
	resp, err := callerPlexHTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("could not search Plex library: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("Plex library search returned status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxLibraryResponseBytes))
	if err != nil {
		return nil, err
	}
	var search plexSearchResponse
	if err := unmarshalPlexLibraryJSON(body, &search); err != nil {
		return nil, fmt.Errorf("could not decode Plex library search: %w", err)
	}
	if search.MediaContainer == nil {
		return nil, errors.New("could not decode Plex library search: missing media container")
	}

	items := make([]components.Metadata, 0)
	for _, hub := range search.MediaContainer.Hub {
		items = append(items, hub.Metadata...)
	}
	// A few PMS versions return Metadata without Hub for a single result.
	items = append(items, search.MediaContainer.Metadata...)

	results := make([]LibrarySearchResult, 0, minInt(len(items), maxLibrarySearchResults))
	seen := make(map[string]struct{})
	refetches := 0
	for candidate, item := range items {
		if candidate >= maxLibrarySearchCandidates {
			break
		}
		if len(results) >= maxLibrarySearchResults || (item.Type != "movie" && item.Type != "episode" && item.Type != "show") {
			continue
		}
		ratingKey := stringValue(item.RatingKey)
		if ratingKey == "" || item.Title == "" {
			continue
		}
		if _, keyErr := validateLibraryRatingKey(ratingKey); keyErr != nil {
			continue
		}
		if _, ok := seen[ratingKey]; ok {
			continue
		}
		if item.Type == "show" {
			if !validLibraryNavigationMetadata(&item, ratingKey, "show") {
				continue
			}
			seen[ratingKey] = struct{}{}
			results = append(results, libraryNavigationResultFromMetadata(&item))
			continue
		}

		selectedItem := &item
		media, part, sourceErr := selectLibraryMetadataSourceForDiscovery(selectedItem)
		if sourceErr != nil {
			// Hub entries are often intentionally abbreviated. Re-fetch through
			// the caller's PMS token before deciding that the item is not playable.
			if refetches >= maxLibrarySearchRefetches {
				// The detail budget is an intentional bound, not an upstream
				// failure. Preserve the successfully resolved prefix rather than
				// turning it into an all-or-nothing error response.
				break
			}
			refetches++
			selectedItem, sourceErr = a.getMetadataItem(searchCtx, ratingKey, true)
			if sourceErr != nil {
				if !isExplicitLibraryItemFailure(sourceErr) {
					return nil, sourceErr
				}
				continue
			}
			media, part, sourceErr = selectLibraryMetadataSourceForDiscovery(selectedItem)
		}
		if sourceErr != nil || selectedItem == nil || media == nil || part == nil {
			continue
		}
		if !validPlayableLibraryMetadata(selectedItem, ratingKey, media, part) {
			continue
		}
		if duration, durationErr := selectedDiscoverySourceDuration(selectedItem, media, part); durationErr != nil || duration <= 0 {
			continue
		}
		seen[ratingKey] = struct{}{}
		results = append(results, librarySearchResultFromMetadata(selectedItem, media, part))
	}
	return results, nil
}

func librarySearchResultFromMetadata(item *components.Metadata, media *components.Media, part *components.Part) LibrarySearchResult {
	result := LibrarySearchResult{
		RatingKey: stringValue(item.RatingKey), MediaID: media.ID, PartID: part.ID,
		Title: item.Title, Type: item.Type, GrandparentTitle: stringValue(item.GrandparentTitle),
		ParentTitle: stringValue(item.ParentTitle), ParentRatingKey: stringValue(item.ParentRatingKey),
		GrandparentRatingKey: stringValue(item.GrandparentRatingKey), Artwork: stringValue(item.Thumb),
	}
	if result.Artwork == "" {
		result.Artwork = stringValue(item.GrandparentThumb)
	}
	if result.Artwork == "" {
		result.Artwork = stringValue(item.Art)
	}
	result.Year = intValue(item.Year)
	result.SeasonNumber = intValue(item.ParentIndex)
	result.EpisodeNumber = intValue(item.Index)
	if duration, err := selectedDiscoverySourceDuration(item, media, part); err == nil {
		result.Duration = duration
	}
	result.VideoResolution = stringValue(media.VideoResolution)
	result.VideoCodec = stringValue(media.VideoCodec)
	result.VideoProfile = stringValue(media.VideoProfile)
	result.AudioCodec = stringValue(media.AudioCodec)
	if media.AudioChannels != nil {
		result.AudioChannels = *media.AudioChannels
	}
	result.Container = stringValue(media.Container)
	if result.Container == "" {
		result.Container = stringValue(part.Container)
	}
	if media.Bitrate != nil {
		result.Bitrate = *media.Bitrate
	}
	if media.Width != nil {
		result.Width = *media.Width
	}
	if media.Height != nil {
		result.Height = *media.Height
	}
	if part.Size != nil {
		result.FileSize = *part.Size
	}
	return result
}

// libraryNavigationResultFromMetadata deliberately copies only presentation
// fields. Shows and seasons have no playable source and therefore never carry
// media/part identifiers or Plex file paths in the public contract.
func libraryNavigationResultFromMetadata(item *components.Metadata) LibrarySearchResult {
	result := LibrarySearchResult{
		RatingKey: stringValue(item.RatingKey), Title: item.Title, Type: item.Type,
		GrandparentTitle: stringValue(item.GrandparentTitle), ParentTitle: stringValue(item.ParentTitle),
		ParentRatingKey: stringValue(item.ParentRatingKey), GrandparentRatingKey: stringValue(item.GrandparentRatingKey),
		Artwork: stringValue(item.Thumb), Year: intValue(item.Year),
	}
	if result.Artwork == "" {
		result.Artwork = stringValue(item.GrandparentThumb)
	}
	if result.Artwork == "" {
		result.Artwork = stringValue(item.Art)
	}
	switch item.Type {
	case "show":
		// Shows are navigation roots and have no episode coordinates.
	case "season":
		result.SeasonNumber = intValue(item.Index)
	case "episode":
		result.SeasonNumber = intValue(item.ParentIndex)
		result.EpisodeNumber = intValue(item.Index)
	}
	return result
}

// GetLibraryMetadataChildren returns one level of a caller-visible Plex TV
// hierarchy. Shows expose seasons and seasons expose playable episodes. The
// Plex /children response is intentionally decoded into Metadata and then
// reduced to LibrarySearchResult so Plex keys and local file paths never leak.
func (a *Application) GetLibraryMetadataChildren(ctx context.Context, ratingKey string) ([]LibrarySearchResult, error) {
	ratingKey, err := validateLibraryRatingKey(ratingKey)
	if err != nil {
		return nil, err
	}
	token := AuthTokenFromContext(ctx)
	if token == nil || strings.TrimSpace(*token) == "" {
		return nil, errors.New("missing auth token")
	}

	childrenCtx, cancel := context.WithTimeout(ctx, libraryChildrenTimeout)
	defer cancel()
	parent, err := a.getMetadataItem(childrenCtx, ratingKey, true)
	if err != nil {
		return nil, err
	}
	if parent == nil || (parent.Type != "show" && parent.Type != "season") {
		return nil, &sourceValidationError{message: "requested media is not navigable"}
	}

	children, err := a.getLibraryChildren(childrenCtx, ratingKey, *token)
	if err != nil {
		return nil, err
	}
	results := make([]LibrarySearchResult, 0, len(children))
	for i := range children {
		child := &children[i]
		childRatingKey := stringValue(child.RatingKey)
		if childRatingKey == "" || len(childRatingKey) > 512 || child.Title == "" {
			continue
		}
		if _, keyErr := validateLibraryRatingKey(childRatingKey); keyErr != nil {
			continue
		}
		if !libraryChildBelongsTo(child, ratingKey) {
			continue
		}

		if parent.Type == "show" {
			if child.Type != "season" {
				continue
			}
			result := libraryNavigationResultFromMetadata(child)
			if result.SeasonNumber == nil {
				continue
			}
			results = append(results, result)
			continue
		}

		if child.Type != "episode" {
			continue
		}
		// A complete /children item is already caller-scoped and has passed the
		// listing containment check above. Use it directly when it is already
		// clip-ready; Plex detail responses are not guaranteed to preserve the
		// same parent-key representation.
		if media, part, resolveErr := selectLibraryMetadataSourceForDiscovery(child); resolveErr == nil && media != nil && part != nil {
			if duration, durationErr := selectedDiscoverySourceDuration(child, media, part); durationErr == nil && duration > 0 {
				results = append(results, librarySearchResultFromMetadata(child, media, part))
				continue
			}
		}
		// Hub/children entries are commonly abbreviated. Re-fetch each episode
		// through the caller-scoped Plex client before resolving a playable Part.
		episode, fetchErr := a.getMetadataItem(childrenCtx, childRatingKey, true)
		if fetchErr != nil {
			if isExplicitLibraryItemFailure(fetchErr) {
				continue
			}
			return nil, fetchErr
		}
		// Containment was authorized from the /children listing above. Plex's
		// detailed metadata may omit or rewrite ParentRatingKey, so do not apply
		// that parent-key check a second time to the refetched episode.
		if episode == nil || !validLibraryNavigationMetadata(episode, childRatingKey, "episode") {
			continue
		}
		media, part, resolveErr := selectLibraryMetadataSourceForDiscovery(episode)
		if resolveErr != nil || media == nil || part == nil || media.ID <= 0 || part.ID <= 0 {
			continue
		}
		if duration, durationErr := selectedDiscoverySourceDuration(episode, media, part); durationErr != nil || duration <= 0 {
			continue
		}
		// Preserve hierarchy fields from the children listing when the detailed
		// metadata response omits them. This keeps season/episode numbering
		// accurate without trusting any unvalidated child media fields.
		epForResult := *episode
		if epForResult.ParentIndex == nil {
			epForResult.ParentIndex = intValue(child.ParentIndex)
		}
		if epForResult.Index == nil {
			epForResult.Index = intValue(child.Index)
		}
		if epForResult.ParentTitle == nil {
			epForResult.ParentTitle = stringPointerValue(child.ParentTitle)
		}
		if epForResult.GrandparentTitle == nil {
			epForResult.GrandparentTitle = stringPointerValue(child.GrandparentTitle)
		}
		if epForResult.ParentRatingKey == nil {
			epForResult.ParentRatingKey = stringPointerValue(child.ParentRatingKey)
		}
		if epForResult.GrandparentRatingKey == nil {
			epForResult.GrandparentRatingKey = stringPointerValue(child.GrandparentRatingKey)
		}
		results = append(results, librarySearchResultFromMetadata(&epForResult, media, part))
	}
	return results, nil
}

func validateLibraryRatingKey(ratingKey string) (string, error) {
	if ratingKey == "" || len(ratingKey) > 512 || !utf8.ValidString(ratingKey) {
		return "", &sourceValidationError{message: "ratingKey is invalid"}
	}
	for _, r := range ratingKey {
		if r < 0x20 || r == 0x7f || r == '/' || r == '\\' {
			return "", &sourceValidationError{message: "ratingKey is invalid"}
		}
	}
	return ratingKey, nil
}

func libraryChildBelongsTo(child *components.Metadata, parentRatingKey string) bool {
	if child == nil {
		return false
	}
	if parent := strings.TrimSpace(stringValue(child.ParentRatingKey)); parent != "" && parent != parentRatingKey {
		return false
	}
	return true
}

func validLibraryNavigationMetadata(item *components.Metadata, requestedRatingKey, expectedType string) bool {
	return item != nil && item.Type == expectedType && strings.TrimSpace(item.Title) != "" &&
		stringValue(item.RatingKey) == requestedRatingKey
}

func validPlayableLibraryMetadata(item *components.Metadata, requestedRatingKey string, media *components.Media, part *components.Part) bool {
	return item != nil && (item.Type == "movie" || item.Type == "episode") &&
		strings.TrimSpace(item.Title) != "" && stringValue(item.RatingKey) == requestedRatingKey &&
		media != nil && media.ID > 0 && part != nil && part.ID > 0
}

func isExplicitLibraryItemFailure(err error) bool {
	var validationErr *sourceValidationError
	if errors.As(err, &validationErr) {
		return true
	}
	status := plexMetadataStatus(err)
	return status == http.StatusBadRequest || status == http.StatusUnauthorized ||
		status == http.StatusForbidden || status == http.StatusNotFound || status == http.StatusUnprocessableEntity
}

func stringPointerValue(value *string) *string {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func (a *Application) getLibraryChildren(ctx context.Context, ratingKey, token string) ([]components.Metadata, error) {
	base, err := url.Parse(a.config.Plex.Host)
	if err != nil || (base.Scheme != "http" && base.Scheme != "https") || base.Host == "" || base.User != nil {
		return nil, errors.New("configured Plex origin is invalid")
	}
	base.Path = strings.TrimRight(base.Path, "/") + "/library/metadata/" + url.PathEscape(ratingKey) + "/children"
	values := base.Query()
	values.Set("X-Plex-Container-Size", fmt.Sprintf("%d", maxLibraryChildren))
	values.Set("X-Plex-Container-Start", "0")
	base.RawQuery = values.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Plex-Token", token)
	req.Header.Set("Accept", "application/json")
	resp, err := callerPlexHTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("could not get Plex library children: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, &libraryHTTPError{status: resp.StatusCode}
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxLibraryResponseBytes))
	if err != nil {
		return nil, fmt.Errorf("could not read Plex library children: %w", err)
	}
	var response plexMetadataResponse
	if err := unmarshalPlexLibraryJSON(body, &response); err != nil {
		return nil, fmt.Errorf("could not decode Plex library children: %w", err)
	}
	if response.MediaContainer == nil {
		return nil, errors.New("could not decode Plex library children: missing media container")
	}
	if response.MediaContainer.Offset != 0 {
		return nil, &sourceValidationError{message: "library hierarchy pagination is incomplete"}
	}
	if response.MediaContainer.TotalSize > maxLibraryChildren || len(response.MediaContainer.Metadata) > maxLibraryChildren {
		return nil, &sourceValidationError{message: "requested hierarchy is too large"}
	}
	if response.MediaContainer.TotalSize > 0 && len(response.MediaContainer.Metadata) < response.MediaContainer.TotalSize {
		return nil, &sourceValidationError{message: "library hierarchy pagination is incomplete"}
	}
	if response.MediaContainer.TotalSize == 0 && len(response.MediaContainer.Metadata) >= maxLibraryChildren {
		return nil, &sourceValidationError{message: "library hierarchy pagination is incomplete"}
	}
	return response.MediaContainer.Metadata, nil
}

type libraryHTTPError struct{ status int }

func (e *libraryHTTPError) Error() string {
	return fmt.Sprintf("Plex library request returned status %d", e.status)
}

// getCallerMetadataItem is the caller-scoped metadata path. It is kept as a
// small HTTP helper rather than relying on an SDK client's redirect policy, so
// the no-redirect credential rule is enforced even for tests or callers that
// construct an Application with a custom PlexGo client.
func (a *Application) getCallerMetadataItem(ctx context.Context, ratingKey, token string) (*components.Metadata, error) {
	base, err := url.Parse(a.config.Plex.Host)
	if err != nil || (base.Scheme != "http" && base.Scheme != "https") || base.Host == "" || base.User != nil {
		return nil, errors.New("configured Plex origin is invalid")
	}
	base.Path = strings.TrimRight(base.Path, "/") + "/library/metadata/" + url.PathEscape(ratingKey)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, base.String(), nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("X-Plex-Token", token)
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
	if decoded.MediaContainer == nil {
		return nil, errors.New("could not decode Plex metadata: missing media container")
	}
	if len(decoded.MediaContainer.Metadata) == 0 {
		return nil, &sourceValidationError{message: "requested media is unavailable"}
	}
	metadata := decoded.MediaContainer.Metadata[0]
	if metadata.RatingKey == nil || *metadata.RatingKey == "" || *metadata.RatingKey != ratingKey {
		return nil, &sourceValidationError{message: "requested media is unavailable"}
	}
	return &metadata, nil
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// unmarshalPlexLibraryJSON is the compatibility boundary for PMS library
// responses. The generated Plex models use *bool for several flags, while PMS
// versions also emit those flags as 0/1 or "0"/"1" and occasionally quote
// numeric fields. Normalize only values whose reflected destination supports
// the compatibility conversion; unknown properties retain normal JSON
// semantics.
func unmarshalPlexLibraryJSON(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()

	var value any
	if err := decoder.Decode(&value); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return errors.New("invalid JSON: multiple values")
		}
		return err
	}

	value, err := normalizePlexLibraryJSONValue(value, reflect.TypeOf(target), "")
	if err != nil {
		return err
	}
	normalized, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return json.Unmarshal(normalized, target)
}

func normalizePlexLibraryJSONValue(value any, targetType reflect.Type, path string) (any, error) {
	if targetType == nil || value == nil {
		return value, nil
	}
	for targetType.Kind() == reflect.Pointer {
		targetType = targetType.Elem()
	}

	if targetType.Kind() == reflect.Bool {
		switch typed := value.(type) {
		case bool:
			return typed, nil
		case string:
			switch typed {
			case "0":
				return false, nil
			case "1":
				return true, nil
			}
		case json.Number:
			switch typed.String() {
			case "0":
				return false, nil
			case "1":
				return true, nil
			}
		}
		return nil, invalidPlexValueError(path, targetType, "bool or 0/1", value)
	}

	// A few generated Plex union types (for example SkipChildren and
	// HasVoiceActivity) accept a bool or a string 0/1. Numeric 0/1 should enter
	// the same bool branch without changing unrelated numeric enum fields.
	if isPlexBooleanUnion(targetType) {
		if number, ok := value.(json.Number); ok {
			switch number.String() {
			case "0":
				return false, nil
			case "1":
				return true, nil
			default:
				return nil, invalidPlexValueError(path, targetType, "Plex boolean 0/1", value)
			}
		}
		return value, nil
	}

	if stringValue, ok := value.(string); ok {
		if number, ok := normalizeQuotedPlexNumber(stringValue, targetType); ok {
			return number, nil
		}
		if isPlexNumericType(targetType) {
			return nil, invalidPlexValueError(path, targetType, targetType.String(), value)
		}
	}

	switch targetType.Kind() {
	case reflect.Struct:
		object, ok := value.(map[string]any)
		if !ok {
			return value, nil
		}
		for i := 0; i < targetType.NumField(); i++ {
			field := targetType.Field(i)
			jsonName := strings.Split(field.Tag.Get("json"), ",")[0]
			if jsonName == "-" {
				continue
			}
			if jsonName == "" {
				jsonName = field.Name
			}
			fieldValue, ok := object[jsonName]
			if !ok {
				continue
			}
			normalized, err := normalizePlexLibraryJSONValue(fieldValue, field.Type, plexJSONPath(path, jsonName))
			if err != nil {
				return nil, err
			}
			object[jsonName] = normalized
		}
	case reflect.Slice, reflect.Array:
		items, ok := value.([]any)
		if !ok {
			return value, nil
		}
		for i := range items {
			normalized, err := normalizePlexLibraryJSONValue(items[i], targetType.Elem(), fmt.Sprintf("%s[%d]", path, i))
			if err != nil {
				return nil, err
			}
			items[i] = normalized
		}
	case reflect.Map:
		items, ok := value.(map[string]any)
		if !ok {
			return value, nil
		}
		for key, item := range items {
			normalized, err := normalizePlexLibraryJSONValue(item, targetType.Elem(), plexJSONPath(path, key))
			if err != nil {
				return nil, err
			}
			items[key] = normalized
		}
	}
	return value, nil
}

func isPlexBooleanUnion(targetType reflect.Type) bool {
	if targetType.Kind() != reflect.Struct {
		return false
	}
	booleanField, ok := targetType.FieldByName("Boolean")
	return ok && booleanField.Type == reflect.TypeOf((*bool)(nil))
}

func isPlexNumericType(targetType reflect.Type) bool {
	switch targetType.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr,
		reflect.Float32, reflect.Float64:
		return true
	default:
		return false
	}
}

func normalizeQuotedPlexNumber(value string, targetType reflect.Type) (json.Number, bool) {
	if !isPlexNumericType(targetType) || value == "" {
		return "", false
	}
	switch targetType.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		parsed, err := strconv.ParseInt(value, 10, targetType.Bits())
		if err != nil {
			return "", false
		}
		return json.Number(strconv.FormatInt(parsed, 10)), true
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		parsed, err := strconv.ParseUint(value, 10, targetType.Bits())
		if err != nil {
			return "", false
		}
		return json.Number(strconv.FormatUint(parsed, 10)), true
	case reflect.Float32, reflect.Float64:
		parsed, err := strconv.ParseFloat(value, targetType.Bits())
		if err != nil || math.IsNaN(parsed) || math.IsInf(parsed, 0) {
			return "", false
		}
		return json.Number(strconv.FormatFloat(parsed, 'g', -1, targetType.Bits())), true
	}
	return "", false
}

func invalidPlexValueError(path string, targetType reflect.Type, expected string, value any) error {
	encoded, err := json.Marshal(value)
	if err != nil {
		encoded = []byte(fmt.Sprintf("%v", value))
	}
	if path == "" {
		path = "$"
	}
	return fmt.Errorf("invalid Plex value at %s: expected %s, got %s", path, expected, encoded)
}

func plexJSONPath(parent, field string) string {
	if parent == "" {
		return field
	}
	return parent + "." + field
}
