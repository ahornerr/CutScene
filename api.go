package main

import (
	"bufio"
	"context"
	"fmt"
	"github.com/gofiber/fiber/v3/middleware/static"
	"path/filepath"
	"strconv"

	"github.com/gofiber/fiber/v3"
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

	api.http.Get("/sessions", api.getSessions)
	api.http.Get("/session/:user", api.getUserSession)
	api.http.Get("/clip/:ratingKey/:from/:to", api.clip)
	api.http.Get("/preview/:ratingKey/:from/:to", api.preview)

	api.http.Get("/*", static.New("./frontend/build"))

	return api, nil
}

func (a *API) Start() error {
	return a.http.Listen(a.config.API.ListenAddr)
}

func (a *API) getSessions(ctx fiber.Ctx) error {
	sessions, err := a.app.GetSessions()
	if err != nil {
		return err
	}

	return ctx.JSON(sessions)
}

func (a *API) getUserSession(ctx fiber.Ctx) error {
	user := ctx.Params("user")
	if user == "" {
		return fmt.Errorf("user not specified")
	}

	sessions, err := a.app.GetUserSessions(user)
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

	filePath, err := a.app.Clip(ratingKeyStr, from, to, height, qp)
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

	libraryMetadata, err := a.app.plex.Library.GetMetadata(context.Background(), ratingKey)
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
