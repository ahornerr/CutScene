package main

import (
	"bufio"
	"errors"
	"fmt"
	"github.com/LukeHagar/plexgo"
	"github.com/LukeHagar/plexgo/models/operations"
	"github.com/gofiber/fiber/v3/middleware/session"
	"github.com/gofiber/fiber/v3/middleware/static"
	"github.com/google/uuid"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strconv"

	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/storage/sqlite3"
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

	sessKeyAuthToken = "authToken"
	sessKeyUser      = "user"
	sessKeyClientID  = "clientID"
	sessKeyPinID     = "pinID"
	sessKeyAuthUrl   = "authURL"
)

type API struct {
	config Config
	app    *Application
	http   *fiber.App
}

func NewAPI(config Config, app *Application) (*API, error) {
	api := &API{
		config: config,
		app:    app,
		http:   fiber.New(),
	}

	api.http.Get("/sessions", api.getSessions, api.authMiddleware)
	api.http.Get("/thumb", api.thumb, api.authMiddleware)
	api.http.Get("/streams/:ratingKey", api.getStreams, api.authMiddleware)
	api.http.Get("/subtitles/:ratingKey", api.getSubtitleEntries, api.authMiddleware)
	api.http.Get("/clip/:ratingKey/:from/:to", api.clip, api.authMiddleware)
	api.http.Get("/preview/:ratingKey/:from/:to", api.preview, api.authMiddleware)

	api.http.Get("/authUrl", api.authUrl).Name(routeNameAuthUrl)

	api.http.Get("/*", static.New("./frontend/build"))

	return api, nil
}

func (a *API) validateAuthToken(ctx fiber.Ctx, sess *session.Session, authToken string) error {
	sess.Set(sessKeyAuthToken, authToken)

	newCtx := ContextWithAuthToken(ctx.UserContext(), authToken)

	// TODO: Probably not necessary to do auth so frequently
	user, err := a.app.GetValidatedUser(newCtx)
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

func (a *API) authMiddleware(ctx fiber.Ctx) error {
	// TODO: If configured to not use user auth, skip all of this

	if AuthTokenFromContext(ctx.UserContext()) != nil {
		// Short circuit for when the auth token is already in the context
		return ctx.Next()
	}

	sess, err := store.Get(ctx)
	if err != nil {
		return err
	}

	authToken, ok := sess.Get(sessKeyAuthToken).(string)
	if ok {
		return a.validateAuthToken(ctx, sess, authToken)
	}

	clientID, clientIDOk := sess.Get(sessKeyClientID).(string)
	pinID, pinIDOk := sess.Get(sessKeyPinID).(string)
	if clientIDOk && pinIDOk {
		// User has likely completed first stage of auth. Attempt to get the token from Plex and check its validity
		tokenResp, err := plexgo.New().Plex.GetToken(ctx.UserContext(), pinID, plexgo.String(clientID))
		if err != nil {
			return err
		}

		// If this auth token is nil, the user hasn't finished authenticating with Plex.
		// Send them back to the auth URL.
		authToken := tokenResp.Object.AuthToken
		if authToken == nil {
			// If we have a saved authURL in the session we can just send them there
			authUrl, authUrlOk := sess.Get(sessKeyAuthUrl).(string)
			if authUrlOk {
				return ctx.Redirect().To(authUrl)
			}

			// Otherwise we have to initiate the auth flow from the beginning
			return ctx.Redirect().Route(routeNameAuthUrl)
		}

		// If Plex does give us an auth token, test it.
		// If it's successful then store it in the session and context and continue on.
		return a.validateAuthToken(ctx, sess, *authToken)
	}

	// User has not started the auth flow. Redirect them to the beginning of it.
	return ctx.Redirect().Route(routeNameAuthUrl)
}

func (a *API) authUrl(ctx fiber.Ctx) error {
	sess, err := store.Get(ctx)
	if err != nil {
		return err
	}

	clientId := uuid.New().String()
	pinResp, err := plexgo.New().Plex.GetPin(ctx.UserContext(), productCutScene, plexgo.Bool(true), plexgo.String(clientId))
	if err != nil {
		return err
	}

	pinID := pinResp.Object.ID
	code := pinResp.Object.Code

	values := url.Values{}
	values.Set("clientID", clientId)
	values.Set("code", *code)
	values.Set("forwardUrl", a.config.API.Domain)
	values.Set("context[device][product]", productCutScene)

	authUrl := fmt.Sprintf("https://app.plex.tv/auth#?%s", values.Encode())

	sess.Set(sessKeyClientID, clientId)
	sess.Set(sessKeyPinID, strconv.FormatFloat(*pinID, 'f', -1, 64))
	sess.Set(sessKeyAuthUrl, authUrl)

	if err := sess.Save(); err != nil {
		return err
	}

	return ctx.Redirect().To(authUrl)
}

func (a *API) Start() error {
	return a.http.Listen(a.config.API.ListenAddr)
}

func (a *API) getSessions(ctx fiber.Ctx) error {
	sessions, err := a.app.GetSessions(ctx.UserContext())
	if err != nil {
		return err
	}

	return ctx.JSON(sessions)
}

func (a *API) getStreams(ctx fiber.Ctx) error {
	ratingKeyStr := ctx.Params("ratingKey")
	if ratingKeyStr == "" {
		return fmt.Errorf("ratingKey not specified")
	}

	streams, err := a.app.GetSubtitleStreams(ctx.UserContext(), ratingKeyStr)
	if err != nil {
		return err
	}

	return ctx.JSON(streams)
}

func (a *API) getSubtitleEntries(ctx fiber.Ctx) error {
	ratingKeyStr := ctx.Params("ratingKey")
	if ratingKeyStr == "" {
		return fmt.Errorf("ratingKey not specified")
	}

	mediaIdStr := ctx.Query("mediaId")

	subtitleIndexStr := ctx.Query("subtitle", "-1")
	subtitleIndex, err := strconv.Atoi(subtitleIndexStr)
	if err != nil {
		return fmt.Errorf("subtitle not an integer")
	}

	if subtitleIndex < 0 {
		return ctx.JSON([]SubtitleEntry{})
	}

	entries, err := a.app.GetSubtitleEntries(ctx.UserContext(), ratingKeyStr, mediaIdStr, subtitleIndex)
	if err != nil {
		return err
	}

	return ctx.JSON(entries)
}

func (a *API) clip(ctx fiber.Ctx) error {
	ratingKeyStr := ctx.Params("ratingKey")
	if ratingKeyStr == "" {
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

	respBody, err := a.app.Thumb(ctx.UserContext(), path)
	if err != nil {
		return err
	}

	defer respBody.Close()

	_, _ = io.Copy(ctx.Response().BodyWriter(), respBody)

	return nil
}

func (a *API) preview(ctx fiber.Ctx) error {
	ratingKeyStr := ctx.Params("ratingKey")
	if ratingKeyStr == "" {
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

	subtitleIndexStr := ctx.Query("subtitle", "-1")
	subtitleIndex, err := strconv.Atoi(subtitleIndexStr)
	if err != nil {
		return fmt.Errorf("subtitle not an integer")
	}

	previewMediaIdStr := ctx.Query("mediaId")
	var previewMediaId int
	if previewMediaIdStr != "" {
		previewMediaId, err = strconv.Atoi(previewMediaIdStr)
		if err != nil {
			return fmt.Errorf("could not parse media id: %w", err)
		}
	}

	ratingKey, err := strconv.ParseFloat(ratingKeyStr, 0)
	if err != nil {
		return fmt.Errorf("could not parse rating key: %w", err)
	}

	libraryMetadata, err := a.app.plexAdmin.Library.GetMetadata(ctx.UserContext(), ratingKey)
	if err != nil {
		return fmt.Errorf("could not get library metadata: %w", err)
	}

	metadata := libraryMetadata.Object.MediaContainer.Metadata[0]

	var previewMedia *operations.GetMetadataMedia
	if previewMediaIdStr != "" {
		for _, m := range metadata.Media {
			if m.ID != nil && *m.ID == previewMediaId {
				previewMedia = &m
				break
			}
		}
	}

	if previewMedia == nil {
		for _, m := range metadata.Media {
			// 10 bit encoding doesn't work correctly on NVIDIA hardware (and maybe others)
			if m.VideoProfile != nil && *m.VideoProfile == "main 10" {
				continue
			}
			previewMedia = &m
			break
		}
	}

	if previewMedia == nil {
		return fmt.Errorf("could not find suitable media for rating key")
	}

	fileURL := fmt.Sprintf("%s%s?X-Plex-Token=%s",
		a.config.Plex.Host,
		*previewMedia.Part[0].Key,
		a.config.Plex.Token,
	)

	// Extract subtitle to temp file if requested
	var subtitleFile string
	if subtitleIndex >= 0 {
		subtitleFile, err = ExtractSubtitle(fileURL, from, to, subtitleIndex)
		if err != nil {
			return fmt.Errorf("could not extract subtitle: %w", err)
		}
	}

	ctx.Response().SetBodyStreamWriter(func(w *bufio.Writer) {
		if subtitleFile != "" {
			defer os.Remove(subtitleFile)
		}
		_ = DoFfmpegPreview(fileURL, from, to, subtitleFile, a.config.Ffmpeg.Codec, w)
	})

	ctx.Set("Content-Type", "video/mp4")

	return nil
}
