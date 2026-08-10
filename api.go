package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/LukeHagar/plexgo/models/components"
	"github.com/LukeHagar/plexgo/models/sdkerrors"
	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/session"
	"github.com/gofiber/fiber/v3/middleware/static"
	"github.com/gofiber/storage/sqlite3"
	"github.com/google/uuid"
)

var storage = sqlite3.New()

var store = session.New(session.Config{
	Storage: storage,
})

func init() {
	store.RegisterType(User{})
}

const (
	productCutScene = "CutScene"

	routeNameAuthUrl = "authUrl"

	apiShutdownTimeout = 5 * time.Second

	sessKeyAuthToken = "authToken"
	sessKeyUser      = "user"
	sessKeyClientID  = "clientID"
	sessKeyPinID     = "pinID"
	sessKeyAuthUrl   = "authURL"
)

type API struct {
	config                          Config
	app                             *Application
	http                            *fiber.App
	validateUser                    func(context.Context) (*User, error)
	previewRunner                   func(context.Context, string, string, string, string, int, Codec, io.Writer, AudioMode) error
	previewRunnerWithSubtitleOffset func(context.Context, string, string, string, string, int, Codec, io.Writer, AudioMode, int64) error
}

func NewAPI(config Config, app *Application) (*API, error) {
	api := &API{
		config: config,
		app:    app,
		http: fiber.New(fiber.Config{
			BodyLimit: renderMaxBodyBytes,
			ErrorHandler: func(ctx fiber.Ctx, err error) error {
				var fiberErr *fiber.Error
				if ctx.Path() == "/render-jobs" && errors.As(err, &fiberErr) && fiberErr.Code == http.StatusRequestEntityTooLarge {
					return renderAPIError(ctx, http.StatusRequestEntityTooLarge, "request body is too large")
				}
				return fiber.DefaultErrorHandler(ctx, err)
			},
		}),
	}

	api.http.Get("/sessions", api.getSessions, api.authMiddleware)
	api.http.Get("/library/search", api.searchLibrary, api.authMiddlewareJSON)
	api.http.Get("/library/metadata/:ratingKey/children", api.getLibraryMetadataChildren, api.authMiddlewareJSON)
	api.http.Get("/thumb", api.thumb, api.authMiddleware)
	api.http.Get("/streams/:ratingKey", api.getStreams, api.authMiddleware)
	api.http.Get("/subtitles/:ratingKey", api.getSubtitleEntries, api.authMiddleware)
	api.http.Post("/render-jobs", api.createRenderJob, api.authMiddlewareJSON)
	api.http.Get("/render-jobs/:id", api.getRenderJob, api.authMiddlewareJSON)
	api.http.Get("/render-jobs/:id/download", api.downloadRenderJob, api.authMiddlewareJSON)
	api.http.Get("/clips", api.listClips, api.authMiddlewareJSON)
	api.http.Get("/clips/:id/download", api.downloadClip, api.authMiddlewareJSON)
	api.http.Get("/clips/:id/artwork", api.downloadClipArtwork, api.authMiddlewareJSON)
	api.http.Get("/clips/:id", api.getClip, api.authMiddlewareJSON)
	api.http.Delete("/clips/:id", api.deleteClip, api.authMiddlewareJSON)
	api.http.Get("/shared/clips/:token/download", api.publicDownloadClip)
	api.http.Get("/shared/clips/:token", api.publicClip)
	api.http.Get("/preview/:ratingKey/:from/:to", api.preview, api.authMiddleware)

	api.http.Get("/authUrl", api.authUrl).Name(routeNameAuthUrl)

	api.http.Get("/*", static.New("./frontend/build"))

	return api, nil
}

func (a *API) validateAuthToken(ctx fiber.Ctx, sess *session.Session, authToken string) error {
	sess.Set(sessKeyAuthToken, authToken)

	newCtx := ContextWithAuthToken(ctx.UserContext(), authToken)

	// TODO: Probably not necessary to do auth so frequently
	user, err := a.getValidatedUser(newCtx)
	if err != nil {
		if errors.Is(err, ErrUserNotInvited) {
			if err := sess.Save(); err != nil {
				return err
			}

			return fmt.Errorf("user not invited")
		}

		// TODO: We may want to be smarter about how we handle these errors to only conditionally delete session items.
		sess.Delete(sessKeyClientID)
		sess.Delete(sessKeyPinID)
		sess.Delete(sessKeyAuthUrl)

		if err := sess.Save(); err != nil {
			return err
		}

		return fmt.Errorf("verification of auth token failed: %w", err)
	}

	sess.Set(sessKeyUser, *user)

	if err := sess.Save(); err != nil {
		return err
	}

	ctx.SetUserContext(ContextWithUser(newCtx, *user))

	return ctx.Next()
}

func (a *API) getValidatedUser(ctx context.Context) (*User, error) {
	if a.validateUser != nil {
		return a.validateUser(ctx)
	}
	return a.app.GetValidatedUser(ctx)
}

func (a *API) authMiddleware(ctx fiber.Ctx) error {
	// TODO: If configured to not use user auth, skip all of this

	if AuthTokenFromContext(ctx.UserContext()) != nil && UserFromContext(ctx.UserContext()) != nil {
		// Short circuit for when the auth token is already in the context
		return ctx.Next()
	}

	sess, err := store.Get(ctx)
	if err != nil {
		return err
	}

	authToken, ok := sess.Get(sessKeyAuthToken).(string)
	if !ok {
		if token := AuthTokenFromContext(ctx.UserContext()); token != nil && *token != "" {
			return a.validateAuthToken(ctx, sess, *token)
		}
	}
	if ok {
		return a.validateAuthToken(ctx, sess, authToken)
	}

	clientID, clientIDOk := sess.Get(sessKeyClientID).(string)
	pinIDStr, pinIDOk := sess.Get(sessKeyPinID).(string)
	if clientIDOk && pinIDOk {
		pinID, err := strconv.ParseInt(pinIDStr, 10, 64)
		if err != nil {
			return err
		}
		tokenResp, err := plexGetToken(ctx.UserContext(), pinID, clientID)
		if err != nil {
			return err
		}

		if tokenResp.AuthToken == "" {
			authUrl, authUrlOk := sess.Get(sessKeyAuthUrl).(string)
			if authUrlOk {
				return ctx.Redirect().To(authUrl)
			}
			return ctx.Redirect().Route(routeNameAuthUrl)
		}

		return a.validateAuthToken(ctx, sess, tokenResp.AuthToken)
	}

	// User has not started the auth flow. Redirect them to the beginning of it.
	return ctx.Redirect().Route(routeNameAuthUrl)
}

func (a *API) authMiddlewareJSON(ctx fiber.Ctx) error {
	if AuthTokenFromContext(ctx.UserContext()) != nil && UserFromContext(ctx.UserContext()) != nil {
		return ctx.Next()
	}
	sess, err := store.Get(ctx)
	if err != nil {
		return renderAPIErrorCode(ctx, http.StatusUnauthorized, "authentication_required", "authentication required")
	}
	if authToken, ok := sess.Get(sessKeyAuthToken).(string); ok && authToken != "" {
		return a.validateAuthTokenJSON(ctx, sess, authToken)
	}
	if authToken := AuthTokenFromContext(ctx.UserContext()); authToken != nil && *authToken != "" {
		return a.validateAuthTokenJSON(ctx, sess, *authToken)
	}
	return renderAPIErrorCode(ctx, http.StatusUnauthorized, "authentication_required", "authentication required")
}

func (a *API) validateAuthTokenJSON(ctx fiber.Ctx, sess *session.Session, authToken string) error {
	newCtx := ContextWithAuthToken(ctx.UserContext(), authToken)
	user, err := a.getValidatedUser(newCtx)
	if err != nil || user == nil || user.Uuid == "" {
		return renderAPIErrorCode(ctx, http.StatusUnauthorized, "authentication_required", "authentication required")
	}
	sess.Set(sessKeyAuthToken, authToken)
	sess.Set(sessKeyUser, *user)
	if err := sess.Save(); err != nil {
		return renderAPIErrorCode(ctx, http.StatusUnauthorized, "authentication_required", "authentication required")
	}
	ctx.SetUserContext(ContextWithUser(newCtx, *user))
	return ctx.Next()
}

func (a *API) authUrl(ctx fiber.Ctx) error {
	sess, err := store.Get(ctx)
	if err != nil {
		return err
	}

	clientId := uuid.New().String()

	pinResp, err := plexGetPin(ctx.UserContext(), productCutScene, true, clientId)
	if err != nil {
		return err
	}

	values := url.Values{}
	values.Set("clientID", clientId)
	values.Set("code", pinResp.Code)
	values.Set("forwardUrl", a.config.API.Domain)
	values.Set("context[device][product]", productCutScene)

	authUrl := fmt.Sprintf("https://app.plex.tv/auth#?%s", values.Encode())

	sess.Set(sessKeyClientID, clientId)
	sess.Set(sessKeyPinID, strconv.FormatInt(pinResp.ID, 10))
	sess.Set(sessKeyAuthUrl, authUrl)

	if err := sess.Save(); err != nil {
		return err
	}

	return ctx.Redirect().To(authUrl)
}

func (a *API) Start() error {
	defer func() {
		if a.app != nil {
			_ = a.app.Close()
		}
	}()
	return a.http.Listen(a.config.API.ListenAddr)
}

// Shutdown cancels application work before bounded HTTP draining. This lets
// active previews observe lifetime cancellation instead of keeping shutdown
// blocked for the full preview timeout.
func (a *API) Shutdown() error {
	if a == nil {
		return nil
	}
	var shutdownErr error
	if a.app != nil {
		// Cancel active work first, but keep SQLite open while the HTTP server
		// drains handlers that may still be reading clip metadata or bytes.
		a.app.stopWork()
	}
	if a.http != nil {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), apiShutdownTimeout)
		err := a.http.ShutdownWithContext(shutdownCtx)
		cancel()
		if shutdownErr == nil {
			shutdownErr = err
		}
	}
	if a.app != nil {
		if err := a.app.Close(); shutdownErr == nil && err != nil {
			shutdownErr = err
		} else if err != nil {
			shutdownErr = errors.Join(shutdownErr, err)
		}
	}
	return shutdownErr
}

func (a *API) getSessions(ctx fiber.Ctx) error {
	sessions, err := a.app.GetSessions(ctx.UserContext())
	if err != nil {
		return err
	}

	return ctx.JSON(sessions)
}

func (a *API) searchLibrary(ctx fiber.Ctx) error {
	token := AuthTokenFromContext(ctx.UserContext())
	if UserFromContext(ctx.UserContext()) == nil || token == nil || strings.TrimSpace(*token) == "" {
		return renderAPIError(ctx, http.StatusUnauthorized, "authentication required")
	}
	query := ctx.Query("query")
	if query == "" {
		// Accept the conventional short spelling without changing the stable
		// response contract.
		query = ctx.Query("q")
	}
	if _, err := validateLibrarySearchQuery(query); err != nil {
		return renderAPIErrorCode(ctx, http.StatusUnprocessableEntity, "validation_error", err.Error())
	}
	results, err := a.app.SearchLibrary(ctx.UserContext(), query)
	if err != nil {
		log.Printf("library search failed: %s", redactedDiagnostic(err))
		ctx.Set("Retry-After", "5")
		return renderAPIErrorCode(ctx, http.StatusServiceUnavailable, "search_unavailable", "could not search the Plex library")
	}
	ctx.Set("Cache-Control", "no-store")
	return ctx.JSON(results)
}

func (a *API) getLibraryMetadataChildren(ctx fiber.Ctx) error {
	ratingKey, err := validateLibraryRatingKey(ctx.Params("ratingKey"))
	if err != nil {
		return renderAPIErrorCode(ctx, http.StatusUnprocessableEntity, "validation_error", "ratingKey is invalid")
	}
	token := AuthTokenFromContext(ctx.UserContext())
	if UserFromContext(ctx.UserContext()) == nil || token == nil || strings.TrimSpace(*token) == "" {
		return renderAPIError(ctx, http.StatusUnauthorized, "authentication required")
	}
	results, err := a.app.GetLibraryMetadataChildren(ctx.UserContext(), ratingKey)
	if err != nil {
		log.Printf("library hierarchy failed: %s", redactedDiagnostic(err))
		return renderLibraryHierarchyAPIError(ctx, err)
	}
	ctx.Set("Cache-Control", "no-store")
	return ctx.JSON(results)
}

func (a *API) getStreams(ctx fiber.Ctx) error {
	ratingKeyStr := ctx.Params("ratingKey")
	if ratingKeyStr == "" || len(ratingKeyStr) > 512 {
		return fmt.Errorf("ratingKey not specified")
	}

	mediaID, partID, err := parseSourceQuery(ctx)
	if err != nil {
		return renderAPIErrorCode(ctx, http.StatusUnprocessableEntity, "validation_error", err.Error())
	}
	streams, err := a.app.GetSubtitleStreamsForSource(ctx.UserContext(), ratingKeyStr, mediaID, partID)
	if err != nil {
		return renderSourceAPIError(ctx, err)
	}

	return ctx.JSON(streams)
}

func parseSourceQuery(ctx fiber.Ctx) (string, string, error) {
	mediaID := ctx.Query("mediaId")
	partID := ctx.Query("partId")
	if mediaID != "" {
		value, err := strconv.ParseInt(mediaID, 10, 64)
		if err != nil || value <= 0 {
			return "", "", errors.New("mediaId is invalid")
		}
	}
	if partID != "" {
		value, err := strconv.ParseInt(partID, 10, 64)
		if err != nil || value <= 0 {
			return "", "", errors.New("partId is invalid")
		}
	}
	return mediaID, partID, nil
}

func plexMetadataStatus(err error) int {
	var sdkErr *sdkerrors.SDKError
	if errors.As(err, &sdkErr) {
		return sdkErr.StatusCode
	}
	var libraryErr *libraryHTTPError
	if errors.As(err, &libraryErr) {
		return libraryErr.status
	}
	return 0
}

func renderLibraryHierarchyAPIError(ctx fiber.Ctx, err error) error {
	var validationErr *sourceValidationError
	if errors.As(err, &validationErr) {
		return renderAPIErrorCode(ctx, http.StatusUnprocessableEntity, "validation_error", validationErr.Error())
	}
	status := plexMetadataStatus(err)
	if status == http.StatusUnauthorized || status == http.StatusForbidden || status == http.StatusNotFound {
		return renderAPIErrorCode(ctx, http.StatusNotFound, "not_found", "requested library item is unavailable")
	}
	return renderAPIErrorCode(ctx, http.StatusServiceUnavailable, "metadata_unavailable", "could not load the library hierarchy")
}

func normalizeSourceError(err error) error {
	var validationErr *sourceValidationError
	if errors.As(err, &validationErr) {
		return err
	}
	status := plexMetadataStatus(err)
	if status == http.StatusUnauthorized || status == http.StatusForbidden || status == http.StatusNotFound {
		return errors.New("requested media is unavailable")
	}
	return newPreviewUpstreamFailure("could not get library metadata", errors.New("Plex metadata service unavailable"))
}

func renderSourceAPIError(ctx fiber.Ctx, err error) error {
	var validationErr *sourceValidationError
	if errors.As(err, &validationErr) {
		return renderAPIErrorCode(ctx, http.StatusUnprocessableEntity, "validation_error", validationErr.Error())
	}
	status := plexMetadataStatus(err)
	if status == 0 || status >= 500 {
		return renderAPIErrorCode(ctx, http.StatusServiceUnavailable, "metadata_unavailable", "could not validate the requested media")
	}
	return renderAPIErrorCode(ctx, http.StatusUnprocessableEntity, "validation_error", "requested media is unavailable")
}

func (a *API) getSubtitleEntries(ctx fiber.Ctx) error {
	ratingKeyStr := ctx.Params("ratingKey")
	if ratingKeyStr == "" || len(ratingKeyStr) > 512 {
		return fmt.Errorf("ratingKey not specified")
	}

	mediaIdStr := ctx.Query("mediaId")
	partIdStr := ctx.Query("partId")
	if _, _, err := parseSourceQuery(ctx); err != nil {
		return renderAPIErrorCode(ctx, http.StatusUnprocessableEntity, "validation_error", err.Error())
	}

	subtitleIndexStr := ctx.Query("subtitle", "-1")
	subtitleIndex, err := strconv.Atoi(subtitleIndexStr)
	if err != nil {
		return fmt.Errorf("subtitle not an integer")
	}

	if subtitleIndex < 0 {
		return ctx.JSON([]SubtitleEntry{})
	}

	entries, err := a.app.GetSubtitleEntriesForSource(ctx.UserContext(), ratingKeyStr, mediaIdStr, partIdStr, subtitleIndex)
	if err != nil {
		return renderSourceAPIError(ctx, err)
	}

	return ctx.JSON(entries)
}

func (a *API) clip(ctx fiber.Ctx) error {
	ratingKeyStr := ctx.Params("ratingKey")
	if ratingKeyStr == "" || len(ratingKeyStr) > 512 {
		return fmt.Errorf("ratingKey not specified")
	}

	mediaIdStr := ctx.Query("mediaId")

	from := ctx.Params("from")
	if from == "" {
		return fmt.Errorf("from not specified")
	}

	to := ctx.Params("to")
	if to == "" {
		return fmt.Errorf("to not specified")
	}

	heightStr := ctx.Query("height", "0")
	height, err := strconv.Atoi(heightStr)
	if err != nil {
		return fmt.Errorf("height not an integer")
	}

	qpStr := ctx.Query("qp", "0")
	qp, err := strconv.Atoi(qpStr)
	if err != nil {
		return fmt.Errorf("qp not an integer")
	}

	subtitleIndexStr := ctx.Query("subtitle", "-1")
	subtitleIndex, err := strconv.Atoi(subtitleIndexStr)
	if err != nil {
		return fmt.Errorf("subtitle not an integer")
	}

	filePath, err := a.app.Clip(ctx.UserContext(), ratingKeyStr, mediaIdStr, from, to, height, qp, subtitleIndex)
	if err != nil {
		return err
	}

	fileName := filepath.Base(filePath)
	ctx.Type(filepath.Ext(fileName))
	ctx.Set(fiber.HeaderContentDisposition, fmt.Sprintf(`attachment; filename="%s"`, fileName))

	return ctx.SendFile(filePath, fiber.SendFile{
		ByteRange: true,
	})
}

func (a *API) thumb(ctx fiber.Ctx) error {
	path := ctx.Query("path")
	if path == "" {
		return fmt.Errorf("path not specified")
	}

	respBody, contentType, err := a.app.Thumb(ctx.UserContext(), path)
	if err != nil {
		return err
	}

	defer respBody.Close()

	if contentType != "" {
		ctx.Set("Content-Type", contentType)
	}

	_, _ = io.Copy(ctx.Response().BodyWriter(), respBody)

	return nil
}

func (a *API) preview(ctx fiber.Ctx) error {
	if err := a.previewStream(ctx); err != nil {
		if !isExpectedPreviewTermination(err, nil) {
			log.Printf("preview preparation failed: %s", redactedDiagnostic(err))
		}
		var upstream *previewFailure
		if errors.As(err, &upstream) {
			ctx.Set("Retry-After", "5")
			return renderAPIErrorCode(ctx, http.StatusServiceUnavailable, upstream.code, upstream.message)
		}
		return renderAPIErrorCode(ctx, http.StatusUnprocessableEntity, "preview_unavailable", "preview is unavailable")
	}
	return nil
}

type previewFailure struct {
	code    string
	message string
	err     error
}

func (e *previewFailure) Error() string { return e.message + ": " + e.err.Error() }
func (e *previewFailure) Unwrap() error { return e.err }

func newPreviewUpstreamFailure(message string, err error) error {
	if err == nil {
		err = errors.New(message)
	}
	return &previewFailure{code: "preview_unavailable", message: message, err: err}
}

type previewFFmpegRunner func(context.Context, string, string, string, string, int, Codec, io.Writer) error

// runPreviewStream owns the successful encoder acquisition until the stream
// writer has finished. It also flushes the response writer itself so a client
// disconnect in the final buffered bytes cancels FFmpeg and follows the same
// cleanup path as an earlier write error.
func runPreviewStream(streamCtx context.Context, writer *bufio.Writer, release func(), cancelStream, finish func(), fileURL, from, to, subtitleFile string, subtitleIndex int, codec Codec, run previewFFmpegRunner) {
	defer release()
	defer cancelStream()
	defer finish()

	output := &previewClientDisconnectWriter{writer: writer, cancel: cancelStream}
	err := run(streamCtx, fileURL, from, to, subtitleFile, subtitleIndex, codec, output)
	if flushErr := output.Flush(); err == nil {
		err = flushErr
	}
	if err == nil {
		return
	}
	if isExpectedPreviewTermination(err, output) {
		return
	}
	log.Printf("preview ffmpeg failed: %s", redactedDiagnostic(err))
}

type previewSessionSelection struct {
	mediaID  int64
	partID   int64
	duration int64
	selected int64
}

type sourceValidationError struct{ message string }

func (e *sourceValidationError) Error() string { return e.message }

// selectPreviewSessionSource authorizes a preview against the caller-visible
// session list. Active-session responses do not reliably include the part key
// (or file), so this deliberately checks IDs only. The playable key is taken
// from the separately fetched metadata after this authorization succeeds.
func selectPreviewSessionSource(sessions []sessionMetadata, ratingKey string, requestedID int64, supplied bool) (previewSessionSelection, error) {
	for _, session := range sessions {
		key := session.Key
		if session.RatingKey != nil {
			key = *session.RatingKey
		}
		if key != ratingKey {
			continue
		}
		for _, media := range session.Media {
			mediaID, _ := sessionIDAsInt64(media.ID)
			if mediaID <= 0 && supplied && sessionValueMatchesID(media.ID, requestedID) {
				mediaID = requestedID
			}
			duration := int64(0)
			if media.Duration != nil && *media.Duration > 0 {
				duration = int64(*media.Duration)
			} else if session.Duration != nil && *session.Duration > 0 {
				duration = int64(*session.Duration)
			}

			if supplied && sessionValueMatchesID(media.ID, requestedID) {
				partID := int64(0)
				for _, part := range media.Part {
					if id, ok := sessionIDAsInt64(part.ID); ok && id > 0 {
						partID = id
						break
					}
				}
				return previewSessionSelection{mediaID: mediaID, partID: partID, duration: duration, selected: requestedID}, nil
			}

			for _, part := range media.Part {
				if supplied && sessionValueMatchesID(part.ID, requestedID) {
					return previewSessionSelection{mediaID: mediaID, partID: requestedID, duration: duration, selected: requestedID}, nil
				}
				if !supplied {
					if id, ok := sessionIDAsInt64(part.ID); ok && id > 0 {
						return previewSessionSelection{mediaID: mediaID, partID: id, duration: duration, selected: id}, nil
					}
				}
			}
			if !supplied && mediaID > 0 {
				return previewSessionSelection{mediaID: mediaID, duration: duration, selected: mediaID}, nil
			}
		}
	}
	return previewSessionSelection{}, errors.New("requested media is not visible in the caller's sessions")
}

func previewSessionMediaID(sessions []sessionMetadata, ratingKey string, requestedID int64, supplied bool) (int64, error) {
	selection, err := selectPreviewSessionSource(sessions, ratingKey, requestedID, supplied)
	if err != nil {
		return 0, err
	}
	return selection.selected, nil
}

// resolvePreviewMetadataSource maps the authorized session selection to the
// library metadata response. In particular, a frontend mediaId may be a Plex
// Part ID, whose parent metadata Media.ID is different.
func resolvePreviewMetadataSource(metadata []components.Media, selection previewSessionSelection) (*components.Media, *components.Part, error) {
	if selection.partID > 0 {
		for i := range metadata {
			if selection.mediaID > 0 && !sessionValueMatchesID(metadata[i].ID, selection.mediaID) {
				continue
			}
			for j := range metadata[i].Part {
				part := &metadata[i].Part[j]
				if sessionValueMatchesID(part.ID, selection.partID) && part.Key != "" {
					return &metadata[i], part, nil
				}
			}
		}
	}

	if selection.mediaID > 0 {
		for i := range metadata {
			if !sessionValueMatchesID(metadata[i].ID, selection.mediaID) {
				continue
			}
			for j := range metadata[i].Part {
				if metadata[i].Part[j].Key != "" {
					return &metadata[i], &metadata[i].Part[j], nil
				}
			}
		}
	}

	return nil, nil, errors.New("requested preview source is unavailable")
}

// selectLibraryMetadataSource applies the one source policy used by search,
// preview, subtitles, and render: movie/episode metadata must resolve to a
// real, accessible Part with a Plex stream key. Media are considered in PMS
// order, with main-10 encodes skipped as they are by the existing renderer.
// A supplied ID may identify either the parent Media or one of its Parts.
func selectLibraryMetadataSource(metadata *components.Metadata, requestedID int64, supplied bool) (*components.Media, *components.Part, error) {
	if metadata == nil {
		return nil, nil, &sourceValidationError{message: "requested media is not clip-capable"}
	}
	if supplied {
		for _, media := range metadata.Media {
			if media.ID == requestedID {
				return resolveLibraryMetadataSource(metadata, requestedID, 0)
			}
		}
		return resolveLibraryMetadataSource(metadata, 0, requestedID)
	}
	return resolveLibraryMetadataSource(metadata, 0, 0)
}

// resolveLibraryMetadataSource is the canonical explicit-library resolver.
// mediaID identifies a Media and partID identifies its playable Part. A
// missing partID selects the first playable part of the requested Media.
func resolveLibraryMetadataSource(metadata *components.Metadata, mediaID, partID int64) (*components.Media, *components.Part, error) {
	if metadata == nil || (metadata.Type != "movie" && metadata.Type != "episode") || strings.TrimSpace(metadata.Title) == "" {
		return nil, nil, &sourceValidationError{message: "requested media is not clip-capable"}
	}
	for i := range metadata.Media {
		media := &metadata.Media[i]
		if media.ID <= 0 || mediaID > 0 && media.ID != mediaID {
			continue
		}
		if media.VideoProfile != nil && *media.VideoProfile == "main 10" {
			continue
		}
		for j := range media.Part {
			part := &media.Part[j]
			if partID > 0 && part.ID != partID {
				continue
			}
			if part.Key == "" || part.ID <= 0 || part.Accessible != nil && !*part.Accessible || part.Exists != nil && !*part.Exists {
				continue
			}
			return media, part, nil
		}
	}
	return nil, nil, &sourceValidationError{message: "requested media has no playable part"}
}

func mediaDurationFromSource(media *components.Media, part *components.Part) int64 {
	if part != nil && part.Duration != nil && *part.Duration > 0 {
		return int64(*part.Duration)
	}
	if media != nil && len(media.Part) > 1 {
		return 0
	}
	if media != nil && media.Duration != nil && *media.Duration > 0 {
		return int64(*media.Duration)
	}
	return 0
}

func selectedSourceDuration(media *components.Media, part *components.Part) (int64, error) {
	if part != nil && part.Duration != nil && *part.Duration > 0 {
		return int64(*part.Duration), nil
	}
	if media != nil && len(media.Part) > 1 {
		return 0, &sourceValidationError{message: "multipart media requires a part duration"}
	}
	if media != nil && media.Duration != nil && *media.Duration > 0 {
		return int64(*media.Duration), nil
	}
	return 0, nil
}

func findVisiblePartKey(sessions []sessionMetadata, ratingKey string, requestedID int64) string {
	for _, session := range sessions {
		key := session.Key
		if session.RatingKey != nil {
			key = *session.RatingKey
		}
		if key != ratingKey {
			continue
		}
		for _, media := range session.Media {
			if !sessionValueMatchesID(media.ID, requestedID) {
				matchedPart := false
				for _, part := range media.Part {
					if sessionValueMatchesID(part.ID, requestedID) {
						matchedPart = true
						if part.Key != "" {
							return part.Key
						}
					}
				}
				if !matchedPart {
					continue
				}
			}
			for _, part := range media.Part {
				if part.Key != "" {
					return part.Key
				}
			}
		}
	}
	return ""
}

func sessionIDAsInt64(value any) (int64, bool) {
	switch value := value.(type) {
	case int:
		return int64(value), true
	case int64:
		return value, true
	case float64:
		return int64(value), value == float64(int64(value))
	case json.Number:
		valueInt, err := value.Int64()
		return valueInt, err == nil
	case string:
		valueInt, err := strconv.ParseInt(value, 10, 64)
		return valueInt, err == nil
	default:
		return 0, false
	}
}

func (a *API) previewStream(ctx fiber.Ctx) error {
	user := UserFromContext(ctx.UserContext())
	if user == nil || user.Uuid == "" {
		return errors.New("authentication required")
	}
	operationCtx, cancelOperation := a.app.operationContext(ctx.UserContext())
	streamTransferred := false
	defer func() {
		if !streamTransferred {
			cancelOperation()
		}
	}()

	ratingKeyStr := ctx.Params("ratingKey")
	if ratingKeyStr == "" || len(ratingKeyStr) > 512 {
		return fmt.Errorf("ratingKey not specified")
	}

	from := ctx.Params("from")
	if from == "" {
		return fmt.Errorf("from not specified")
	}

	to := ctx.Params("to")
	if to == "" {
		return fmt.Errorf("to not specified")
	}
	fromMs, err := ParseTimestampToMs(from)
	if err != nil || fromMs < 0 {
		return errors.New("invalid preview start range")
	}
	toMs, err := ParseTimestampToMs(to)
	if err != nil || toMs < 0 || toMs <= fromMs {
		return errors.New("invalid preview end range")
	}
	if toMs-fromMs > renderMaxDurationMs {
		return errors.New("preview duration exceeds the maximum allowed duration")
	}
	audioMode, err := parseAudioMode(ctx.Query("audioMode", ""))
	if err != nil {
		return errors.New("audioMode is invalid")
	}

	subtitleIndexStr := ctx.Query("subtitle", "-1")
	subtitleIndex, err := strconv.Atoi(subtitleIndexStr)
	if err != nil {
		return fmt.Errorf("subtitle not an integer")
	}

	previewMediaIdStr := ctx.Query("mediaId")
	previewPartIdStr := ctx.Query("partId")
	var previewMediaId int64
	if previewMediaIdStr != "" {
		previewMediaId, err = strconv.ParseInt(previewMediaIdStr, 10, 64)
		if err != nil || previewMediaId <= 0 {
			return errors.New("could not parse media id")
		}
	}
	var previewPartId int64
	if previewPartIdStr != "" {
		previewPartId, err = strconv.ParseInt(previewPartIdStr, 10, 64)
		if err != nil || previewPartId <= 0 {
			return errors.New("could not parse part id")
		}
	}
	if subtitleIndex < -1 {
		return errors.New("subtitle not available")
	}
	subtitleOffsetMs, err := parseSubtitleOffsetMs(ctx.Query("subtitleOffsetMs", "0"))
	if err != nil {
		return errors.New("subtitleOffsetMs is invalid")
	}

	var sessions []sessionMetadata
	var selection previewSessionSelection
	userScoped := previewPartIdStr != ""
	var metadataItem *components.Metadata
	if userScoped {
		metadataItem, err = a.app.getMetadataItem(operationCtx, ratingKeyStr, true)
		if err != nil {
			return normalizeSourceError(err)
		}
		media, part, sourceErr := resolveLibraryMetadataSource(metadataItem, previewMediaId, previewPartId)
		if sourceErr != nil {
			return sourceErr
		}
		duration := mediaDurationFromSource(media, part)
		if _, durationErr := selectedSourceDuration(media, part); durationErr != nil {
			return durationErr
		}
		selection = previewSessionSelection{mediaID: media.ID, partID: part.ID, duration: duration, selected: part.ID}
	} else {
		// No partId is the legacy active-session path. Explicit library sources
		// must never depend on /status/sessions being available.
		sessions, err = a.app.GetSessions(operationCtx)
		if err != nil {
			return newPreviewUpstreamFailure("could not validate caller-visible preview source", err)
		}
		selection, err = selectPreviewSessionSource(sessions, ratingKeyStr, previewMediaId, previewMediaIdStr != "")
		if err != nil {
			return err
		}
	}
	if selection.duration > 0 && toMs > selection.duration {
		return errors.New("requested range exceeds the selected media duration")
	}

	if metadataItem == nil {
		metadataItem, err = a.app.getMetadataItem(operationCtx, ratingKeyStr, userScoped)
	}
	if err != nil {
		return newPreviewUpstreamFailure("could not get library metadata", err)
	}
	metadata := *metadataItem

	previewMedia, previewPart, err := resolvePreviewMetadataSource(metadata.Media, selection)
	if err != nil {
		return err
	}
	visibleMediaID := previewMedia.ID
	mediaDuration, durationErr := selectedSourceDuration(previewMedia, previewPart)
	if durationErr != nil {
		return durationErr
	}
	if mediaDuration > 0 && toMs > mediaDuration {
		return errors.New("requested range exceeds the selected media duration")
	}

	fileURL := fmt.Sprintf("%s%s?X-Plex-Token=%s",
		a.config.Plex.Host,
		previewPart.Key,
		a.app.plexSourceToken(operationCtx, userScoped),
	)

	// Extract subtitle to temp file if requested. For text subtitles, either use
	// cached entries via WriteClipSRT or extract via FFmpeg. For PGS, pass
	// subtitleIndex directly to FFmpeg overlay filter.
	var subtitleFile string
	defer func() {
		if subtitleFile != "" && !streamTransferred {
			_ = os.Remove(subtitleFile)
		}
	}()
	var subtitleIdxForFFmpeg int = -1
	if subtitleIndex >= 0 {
		source, sourceErr := selectSubtitleSource(previewPart.Stream, subtitleIndex)
		if sourceErr != nil {
			return errors.New("requested subtitle is unavailable")
		}
		if source.PGS && source.External {
			return errors.New("subtitle codec is not supported")
		}

		if source.PGS {
			subtitleIdxForFFmpeg = source.EmbeddedIndex
		} else {
			cacheMediaID := strconv.FormatInt(visibleMediaID, 10)
			cachePartID := strconv.FormatInt(previewPart.ID, 10)
			if entries, ok := a.app.GetCachedSubtitleEntries(operationCtx, ratingKeyStr, cacheMediaID, cachePartID, subtitleIndex); ok {
				subtitleFile, err = WriteClipSRT(entries, fromMs, toMs, subtitleOffsetMs)
				if err != nil {
					return fmt.Errorf("could not write clip subtitle: %w", err)
				}
			} else if source.External {
				if !isSupportedTextSubtitle(source) {
					return errors.New("subtitle codec is not supported")
				}
				subtitleFile, err = a.app.prepareExternalSubtitleWithToken(operationCtx, source, fromMs, toMs, []int64{subtitleOffsetMs}, a.app.plexSourceToken(operationCtx, userScoped))
				if err != nil {
					return newPreviewUpstreamFailure("preview subtitle source unavailable", err)
				}
			} else {
				acquireCtx, cancelAcquire := context.WithTimeout(operationCtx, renderFFmpegAcquireTimeout)
				release, acquireErr := a.app.acquireFFmpeg(acquireCtx)
				cancelAcquire()
				if acquireErr != nil {
					return newPreviewUpstreamFailure("preview encoder capacity is unavailable", acquireErr)
				}
				subtitleCtx, cancelSubtitle := context.WithTimeout(operationCtx, renderPreviewTimeout)
				subtitleFile, err = func() (string, error) {
					defer release()
					return ExtractSubtitleContext(subtitleCtx, fileURL, from, to, source.EmbeddedIndex, subtitleOffsetMs)
				}()
				cancelSubtitle()
				if err != nil {
					return newPreviewUpstreamFailure("preview subtitle preparation failed", err)
				}
			}
		}
	}

	// Fiber contexts are request-scoped and may be recycled as soon as this
	// handler returns. The stream writer therefore owns a standalone context
	// with an explicit bound instead of capturing ctx or ctx.UserContext().
	streamCtx, streamCancel := context.WithTimeout(operationCtx, renderPreviewTimeout)
	acquireCtx, cancelAcquire := context.WithTimeout(streamCtx, renderFFmpegAcquireTimeout)
	release, err := a.app.acquireFFmpeg(acquireCtx)
	cancelAcquire()
	if err != nil {
		streamCancel()
		return newPreviewUpstreamFailure("preview encoder capacity is unavailable", err)
	}
	codec := a.config.Ffmpeg.Codec
	streamSubtitleFile := subtitleFile
	subtitleFile = ""
	ctx.Set("Cache-Control", "no-store")
	ctx.Set("Content-Type", "video/mp4")
	// SetBodyStreamWriter starts the callback after the handler returns. Mark
	// the lease and operation as transferred now; waiting for the callback here
	// deadlocks because the callback cannot start until this handler returns.
	streamLeaseTransferred := false
	defer func() {
		if !streamLeaseTransferred {
			release()
		}
	}()
	previewRunner := a.previewRunner
	previewRunnerWithOffset := a.previewRunnerWithSubtitleOffset
	if previewRunner == nil && previewRunnerWithOffset == nil {
		previewRunnerWithOffset = func(ctx context.Context, fileURL, from, to, subtitleFile string, subtitleIndex int, codec Codec, writer io.Writer, audioMode AudioMode, offsetMs int64) error {
			return DoFfmpegPreviewContextWithSubtitleOffset(ctx, fileURL, from, to, subtitleFile, subtitleIndex, codec, writer, audioMode, offsetMs)
		}
	}
	if previewRunner == nil {
		previewRunner = func(ctx context.Context, fileURL, from, to, subtitleFile string, subtitleIndex int, codec Codec, writer io.Writer, audioMode AudioMode) error {
			return previewRunnerWithOffset(ctx, fileURL, from, to, subtitleFile, subtitleIndex, codec, writer, audioMode, subtitleOffsetMs)
		}
	}
	ctx.Response().SetBodyStreamWriter(func(w *bufio.Writer) {
		if streamSubtitleFile != "" {
			defer os.Remove(streamSubtitleFile)
		}
		runPreviewStream(streamCtx, w, release, streamCancel, cancelOperation, fileURL, from, to, streamSubtitleFile, subtitleIdxForFFmpeg, codec,
			func(ctx context.Context, fileURL, from, to, subtitleFile string, subtitleIndex int, codec Codec, writer io.Writer) error {
				return previewRunner(ctx, fileURL, from, to, subtitleFile, subtitleIndex, codec, writer, audioMode)
			})
	})
	streamLeaseTransferred = true
	streamTransferred = true

	return nil
}

// plexPinResponse represents the response from POST /api/v2/pins.json
type plexPinResponse struct {
	ID   int64  `json:"id"`
	Code string `json:"code"`
}

// plexTokenResponse represents the response from GET /api/v2/pins/{id}.json
type plexTokenResponse struct {
	AuthToken string `json:"authToken"`
}

// plexAPIBase allows test injection of a mock server URL.
var plexAPIBase = "https://plex.tv"

// plexGetPin creates a Plex authentication PIN via direct HTTP call
func plexGetPin(ctx context.Context, product string, strong bool, clientID string) (*plexPinResponse, error) {
	reqUrl := plexAPIBase + "/api/v2/pins.json"
	data := url.Values{}
	data.Set("strong", strconv.FormatBool(strong))

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, reqUrl, strings.NewReader(data.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Plex-Client-Identifier", clientID)
	req.Header.Set("X-Plex-Product", product)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1024))
	if err != nil {
		return nil, err
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("plex PIN request returned status %d: %s", resp.StatusCode, string(body))
	}

	var result plexPinResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, err
	}

	if result.ID == 0 || result.Code == "" {
		return nil, fmt.Errorf("plex PIN response missing required fields (id/code)")
	}

	return &result, nil
}

// plexGetToken retrieves the auth token for a completed PIN authentication via direct HTTP call
func plexGetToken(ctx context.Context, pinID int64, clientID string) (*plexTokenResponse, error) {
	reqUrl := fmt.Sprintf("%s/api/v2/pins/%d.json", plexAPIBase, pinID)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqUrl, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Plex-Client-Identifier", clientID)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1024))
	if err != nil {
		return nil, err
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("plex token request returned status %d: %s", resp.StatusCode, string(body))
	}

	var result plexTokenResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, err
	}

	return &result, nil
}
