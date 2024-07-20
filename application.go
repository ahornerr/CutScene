package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"

	"github.com/LukeHagar/plexgo"
	"github.com/LukeHagar/plexgo/models/operations"
	ffmpeg "github.com/u2takey/ffmpeg-go"
)

var (
	ErrNoUserSession = errors.New("no user session")
)

type Application struct {
	config Config
	plex   *plexgo.PlexAPI
}

func NewApplication(config Config) (*Application, error) {
	plex := plexgo.New(
		plexgo.WithServerURL(config.Plex.Host),
		plexgo.WithSecurity(config.Plex.Token),
	)

	_, err := plex.Server.GetServerCapabilities(context.Background())
	if err != nil {
		return nil, fmt.Errorf("could not get server capabilities: %w", err)
	}

	return &Application{
		config: config,
		plex:   plex,
	}, nil
}

func (a *Application) GetUserSessions(user string) ([]operations.GetSessionsMetadata, error) {
	sessions, err := a.plex.Sessions.GetSessions(context.Background())
	if err != nil {
		return nil, fmt.Errorf("could not get sessions: %w", err)
	}

	var userSessions []operations.GetSessionsMetadata
	for _, session := range sessions.Object.MediaContainer.Metadata {
		if *session.User.Title == user {
			userSessions = append(userSessions, session)
		}
	}

	// TODO: May be better to normalize each session into some human readable objects for
	//  better handling of multiple sessions

	if len(userSessions) == 0 {
		return nil, ErrNoUserSession
	}

	return userSessions, nil
}

func (a *Application) Clip(sessionMetadata operations.GetSessionsMetadata, from, to string, height, qp int) (string, error) {
	ratingKey, err := strconv.ParseFloat(*sessionMetadata.RatingKey, 0)
	if err != nil {
		return "", fmt.Errorf("could not parse rating key: %w", err)
	}

	libraryMetadata, err := a.plex.Library.GetMetadata(context.Background(), ratingKey)
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

	probed, err := a.probe(fileURL)
	if err != nil {
		return "", err
	}

	params := FfmpegParams{
		URL:         fileURL,
		From:        from,
		To:          to,
		Filename:    fileName,
		Codec:       a.config.Ffmpeg.Codec,
		Height:      height,
		QP:          qp,
		ProbedCodec: probed.Streams[0].CodecName, // TODO: How to handle multiple streams?
		Metadata: FfmpegParamsMetadata{
			Title: *metadata.Title,
			User:  *sessionMetadata.User.Title,
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

func (a *Application) probe(input string) (*ffProbeResult, error) {
	probeJson, err := ffmpeg.Probe(input)
	if err != nil {
		return nil, fmt.Errorf("could not get ffprobe: %w", err)
	}

	var probed ffProbeResult
	if err := json.Unmarshal([]byte(probeJson), &probed); err != nil {
		return nil, fmt.Errorf("could not unmarshal ffprobe: %w", err)
	}

	return &probed, nil
}

type ffProbeResult struct {
	Streams []struct {
		CodecName string `json:"codec_name"`
	} `json:"streams"`
}
