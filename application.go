package main

import (
	"context"
	"errors"
	"fmt"
	"github.com/LukeHagar/plexgo/models/components"
	"io"
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
		for _, session := range filteredSessions {
			if strconv.Itoa(user.Id) == *session.User.ID {
				filteredSessions = append(filteredSessions, session)
			}
		}
	} else {
		filteredSessions = sessions.Object.MediaContainer.Metadata
	}

	return filteredSessions, nil
}

func (a *Application) Clip(ctx context.Context, ratingKeyStr, from, to string, height, qp int) (string, error) {
	ratingKey, err := strconv.ParseFloat(ratingKeyStr, 0)
	if err != nil {
		return "", fmt.Errorf("could not parse rating key: %w", err)
	}

	libraryMetadata, err := a.plexAdmin.Library.GetMetadata(ctx, ratingKey)
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
