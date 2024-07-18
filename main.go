package main

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/LukeHagar/plexgo"
	"github.com/LukeHagar/plexgo/models/operations"
	"github.com/spf13/viper"
	ffmpeg "github.com/u2takey/ffmpeg-go"
	"log"
	"strconv"
)

type Config struct {
	Plex struct {
		Host  string
		Token string
	}
}

func main() {
	var cfg Config
	viper.SetConfigName("config")
	viper.AddConfigPath(".")
	if err := viper.ReadInConfig(); err != nil {
		log.Fatalf("Error reading config file, %s", err)
	}
	err := viper.Unmarshal(&cfg)
	if err != nil {
		log.Fatalf("unable to decode config into struct, %v", err)
	}

	plex := plexgo.New(
		plexgo.WithServerURL(cfg.Plex.Host),
		plexgo.WithSecurity(cfg.Plex.Token),
	)

	capabilities, err := plex.Server.GetServerCapabilities(context.Background())
	if err != nil {
		log.Fatalf("Error getting server capabilities, %s", err)
	}

	_ = capabilities

	user := "ahorner"
	session := getUserSession(plex, user)

	if session == nil {
		log.Fatalf("User session not found")
	}

	ratingKey, err := strconv.ParseFloat(*session.RatingKey, 0)
	if err != nil {
		log.Fatalf("Error parsing rating key, %s", err)
	}

	metadata, err := plex.Library.GetMetadata(context.Background(), ratingKey)
	if err != nil {
		log.Fatalf("Error getting metadata, %s", err)
	}

	// TODO: what if there are multiple of any of these?
	//  I think we can take the media ID and part ID from the session data to find the specific file in that case
	key := *metadata.Object.MediaContainer.Metadata[0].Media[0].Part[0].Key

	fileUrl := fmt.Sprintf("%s%s?X-Plex-Token=%s", cfg.Plex.Host, key, cfg.Plex.Token)

	fmt.Printf("%s is watching %s %s Episode %d at %s\n", user, *session.GrandparentTitle, *session.ParentTitle, *session.Index, fileUrl)

	probeJson, err := ffmpeg.Probe(fileUrl)
	if err != nil {
		log.Fatalf("Error getting ffprobe result, %s", err)
	}

	var probed ffProbeResult
	if err := json.Unmarshal([]byte(probeJson), &probed); err != nil {
		log.Fatalf("Error parsing ffprobe result, %s", err)
	}

	duration := 30

	outputArgs := ffmpeg.KwArgs{
		"t":            duration,
		"map_metadata": -1,
		"acodec":       "libvorbis",
	}

	if probed.Streams[0].CodecName == "h264" {
		outputArgs["vcodec"] = "copy"
	} else {
		outputArgs["vcodec"] = "libx264"
		outputArgs["pix_fmt"] = "yuv420p"
		outputArgs["crf"] = 23
		outputArgs["video_bitrate"] = 0
	}

	startTime := 123
	outputFile := "./test.mp4"

	fmt.Println("Starting clip creation")

	err = ffmpeg.
		Input(fileUrl, ffmpeg.KwArgs{"ss": startTime}).
		Output(outputFile, outputArgs).
		OverWriteOutput().
		Run()

	if err != nil {
		log.Fatalf("Error creating clip: %s", err)
	}

	fmt.Println("Successfully created clip!")
}

func getUserSession(plex *plexgo.PlexAPI, user string) *operations.GetSessionsMetadata {
	sessions, err := plex.Sessions.GetSessions(context.Background())
	if err != nil {
		log.Fatalf("Error getting sessions, %s", err)
	}

	for _, session := range sessions.Object.MediaContainer.Metadata {
		if *session.User.Title == user {
			return &session
		}
	}

	return nil
}

type ffProbeResult struct {
	Streams []struct {
		CodecName string `json:"codec_name"`
	} `json:"streams"`
}
