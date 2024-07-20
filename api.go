package main

import (
	"fmt"
	"path/filepath"

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

	api.http.Get("/session/:user", api.getUserSession)
	api.http.Get("/clip/:user/:from/:to", api.clip)

	return api, nil
}

func (a *API) Start() error {
	return a.http.Listen(a.config.API.ListenAddr)
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
	user := ctx.Params("user")
	if user == "" {
		return fmt.Errorf("user not specified")
	}

	from := ctx.Params("from")
	if from == "" {
		return fmt.Errorf("from not specified")
	}

	to := ctx.Params("to")
	if to == "" {
		return fmt.Errorf("to not specified")
	}

	sessions, err := a.app.GetUserSessions(user)
	if err != nil {
		return err
	}

	// TODO: Handle multiple user sessions
	filePath, err := a.app.Clip(sessions[0], from, to)
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
