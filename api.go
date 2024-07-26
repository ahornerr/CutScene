package main

import (
	"bufio"
	"fmt"
	"github.com/LukeHagar/plexgo"
	"github.com/gofiber/fiber/v3/middleware/session"
	"github.com/gofiber/fiber/v3/middleware/static"
	"github.com/google/uuid"
	"net/url"
	"path/filepath"
	"strconv"

	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/storage/sqlite3"
)

var storage = sqlite3.New()

var store = session.New(session.Config{
	Storage: storage,
})

const (
	productCutScene = "CutScene"

	routeNameAuthUrl = "authUrl"

	sessKeyAuthToken = "authToken"
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
	api.http.Get("/clip/:ratingKey/:from/:to", api.clip, api.authMiddleware)
	api.http.Get("/preview/:ratingKey/:from/:to", api.preview, api.authMiddleware)

	api.http.Get("/authUrl", api.authUrl).Name(routeNameAuthUrl)

	api.http.Get("/*", static.New("./frontend/build"))

	return api, nil
}

func (a *API) authMiddleware(ctx fiber.Ctx) error {
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
		ctxWithAuth := ContextWithAuthToken(ctx.UserContext(), authToken)
		err = a.app.TestAuth(ctxWithAuth)
		if err == nil {
			// Session found, auth successful.
			// Set context with auth token as user context and continue onto next
			ctx.SetUserContext(ctxWithAuth)
			return ctx.Next()
		}

		// Auth token exists in session errored. Maybe want to check the error to see if it's a problem
		// communicating with the server (as opposed to an invalid token).
		// If it's a communication problem then we probably don't want to remove the auth token from the session.

		sess.Delete(sessKeyAuthToken)
		// TODO: Do we want to save the session here?
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
		ctxWithAuth := ContextWithAuthToken(ctx.UserContext(), *authToken)
		testAuthErr := a.app.TestAuth(ctxWithAuth)
		if testAuthErr == nil {
			sess.Set(sessKeyAuthToken, *authToken)
			sess.Delete(sessKeyClientID)
			sess.Delete(sessKeyPinID)
			sess.Delete(sessKeyAuthUrl)

			if err := sess.Save(); err != nil {
				return err
			}

			ctx.SetUserContext(ctxWithAuth)
			return ctx.Next()
		}

		// If it's unsuccessful, it means they don't have access to this server, or something went wrong
		// during the auth flow. In that case, reset the session and return an error.
		// TODO: We may want to be smarter about how we handle these errors to only conditionally delete session items.
		sess.Delete(sessKeyClientID)
		sess.Delete(sessKeyPinID)
		sess.Delete(sessKeyAuthUrl)

		if err := sess.Save(); err != nil {
			return err
		}

		return fmt.Errorf("verification of auth token failed: %w", testAuthErr)
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

func (a *API) clip(ctx fiber.Ctx) error {
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

	filePath, err := a.app.Clip(ctx.UserContext(), ratingKeyStr, from, to, height, qp)
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

func (a *API) preview(ctx fiber.Ctx) error {
	ratingKeyStr := ctx.Params("ratingKey")
	if ratingKeyStr == "" {
		return fmt.Errorf("ratingKey not specified")
	}

	ratingKey, err := strconv.ParseFloat(ratingKeyStr, 0)
	if err != nil {
		return fmt.Errorf("could not parse rating key: %w", err)
	}

	from := ctx.Params("from")
	if from == "" {
		return fmt.Errorf("from not specified")
	}

	to := ctx.Params("to")
	if to == "" {
		return fmt.Errorf("to not specified")
	}

	libraryMetadata, err := a.app.plex.Library.GetMetadata(ctx.UserContext(), ratingKey)
	if err != nil {
		return fmt.Errorf("could not get library metadata: %w", err)
	}

	metadata := libraryMetadata.Object.MediaContainer.Metadata[0]

	fileURL := fmt.Sprintf("%s%s?X-Plex-Token=%s",
		a.config.Plex.Host,
		*metadata.Media[0].Part[0].Key,
		a.config.Plex.Token,
	)

	ctx.Response().SetBodyStreamWriter(func(w *bufio.Writer) {
		_ = DoFfmpegPreview(fileURL, from, to, a.config.Ffmpeg.Codec, w)
	})

	ctx.Set("Content-Type", "video/mp4")

	return nil
}
