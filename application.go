package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
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

func (a *Application) Clip(sessionMetadata operations.GetSessionsMetadata, from, to string) (string, error) {
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

	probed, err := a.probe(fileURL)
	if err != nil {
		return "", err
	}

	outputMetadata := map[string]string{
		"title":   *metadata.Title,
		"comment": from,
		"artist":  *sessionMetadata.User.Title,
	}

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

		outputMetadata["show"] = *metadata.GrandparentTitle
		outputMetadata["season_number"] = strconv.Itoa(*metadata.ParentIndex)
		outputMetadata["episode_id"] = strconv.Itoa(*metadata.Index)
	} else {
		fileName = fmt.Sprintf("%s (%d) (%s - %s).mp4",
			*metadata.Title,
			*metadata.Year,
			from,
			to,
		)

		outputMetadata["year"] = strconv.Itoa(*metadata.Year)
	}

	var metadataArr []string
	for k, v := range outputMetadata {
		metadataArr = append(metadataArr, fmt.Sprintf("%s=%s", k, v))
	}

	tmpFile := filepath.Join("/tmp", fileName)

	inputArgs := ffmpeg.KwArgs{
		"ss": from,
		// TODO: Make these two configurable, we don't want them when trying to troubleshoot
		"hide_banner": "",
		"loglevel":    "error",
	}

	// TODO: Might be a good idea to make these configurable or add support for presets
	outputArgs := ffmpeg.KwArgs{
		"to":           to,
		"acodec":       "libvorbis",
		"copyts":       "",
		"map_chapters": -1,
		"map_metadata": 0,
		"movflags":     "use_metadata_tags",
		"metadata":     metadataArr,
	}

	//for _, m := range metadataArr {
	//outputArgs = append(outputArgs, ffmpeg.KwArgs{"metadata": m})
	//}

	// TODO: How to handle multiple streams?
	if probed.Streams[0].CodecName == "h264" {
		outputArgs["vcodec"] = "copy"
	} else {
		outputArgs["vcodec"] = "libx264"
		outputArgs["pix_fmt"] = "yuv420p"
		outputArgs["crf"] = 23
		outputArgs["video_bitrate"] = 0
		// TODO: I'm not sure if this does anything useful
		outputArgs["tune"] = "film"
	}

	errBuff := &bytes.Buffer{}
	err = ffmpeg.
		Input(fileURL, inputArgs).
		Output(tmpFile, outputArgs).
		OverWriteOutput().
		WithErrorOutput(errBuff).
		Run()

	// Capture the ffmpeg process stderr if it exits unsuccessfully
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		err = fmt.Errorf("ffmpeg exited with error:\n%s", errBuff.String())
	}

	return tmpFile, err
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
