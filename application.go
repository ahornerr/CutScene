package main

import (
	"context"
	"fmt"
	"github.com/LukeHagar/plexgo/models/components"
	"strconv"

	"github.com/LukeHagar/plexgo"
	"github.com/LukeHagar/plexgo/models/operations"
)

type Application struct {
	config Config
	plex   *plexgo.PlexAPI
}

func NewApplication(config Config) (*Application, error) {
	app := &Application{
		config: config,
	}

	app.plex = plexgo.New(
		plexgo.WithServerURL(config.Plex.Host),
		plexgo.WithSecuritySource(app.security),
	)

	// TODO: Only test this if there's a configured token, as opposed to user auth
	//_, err := app.plex.Server.GetServerCapabilities(context.Background())
	//if err != nil {
	//	return nil, fmt.Errorf("could not get server capabilities: %w", err)
	//}

	return app, nil
}

func (a *Application) security(ctx context.Context) (components.Security, error) {
	authToken := AuthTokenFromContext(ctx)
	if authToken == nil {
		return components.Security{}, fmt.Errorf("missing auth token")
	}

	// TODO: Fallback to config.Plex.Token? Maybe only if desirable

	return components.Security{
		AccessToken: *authToken,
	}, nil
}

func (a *Application) TestAuth(ctx context.Context) error {
	_, err := a.plex.Server.GetServerCapabilities(ctx)
	return err
}

func (a *Application) GetSessions(ctx context.Context) ([]operations.GetSessionsMetadata, error) {
	sessions, err := a.plex.Sessions.GetSessions(ctx)
	if err != nil {
		return nil, fmt.Errorf("could not get sessions: %w", err)
	}

	return sessions.Object.MediaContainer.Metadata, nil
}

func (a *Application) Clip(ctx context.Context, ratingKeyStr, from, to string, height, qp int) (string, error) {
	ratingKey, err := strconv.ParseFloat(ratingKeyStr, 0)
	if err != nil {
		return "", fmt.Errorf("could not parse rating key: %w", err)
	}

	libraryMetadata, err := a.plex.Library.GetMetadata(ctx, ratingKey)
	if err != nil {
		return "", fmt.Errorf("could not get library metadata: %w", err)
	}

	metadata := libraryMetadata.Object.MediaContainer.Metadata[0]

	fileURL := fmt.Sprintf("%s%s?X-Plex-Token=%s",
		a.config.Plex.Host,
		*metadata.Media[0].Part[0].Key,
		a.config.Plex.Token,
	)

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
		URL:      fileURL,
		From:     from,
		To:       to,
		Filename: fileName,
		Codec:    a.config.Ffmpeg.Codec,
		Height:   height,
		QP:       qp,
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
