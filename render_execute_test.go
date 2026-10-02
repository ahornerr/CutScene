package main

import (
	"context"
	"strings"
	"testing"
)

// captureRenderParams runs executeRenderSpec with FFmpeg and subtitle extraction
// stubbed out, returning the FfmpegParams the render pipeline produced.
func captureRenderParams(t *testing.T, app *Application, spec renderJobSpec) (FfmpegParams, error) {
	t.Helper()
	originalDoFfmpeg := doFfmpegFn
	originalExtract := extractSubtitleContextFn
	t.Cleanup(func() {
		doFfmpegFn = originalDoFfmpeg
		extractSubtitleContextFn = originalExtract
	})

	extractSubtitleContextFn = func(ctx context.Context, url, from, to string, subtitleIndex int, subtitleOffsets ...int64) (string, error) {
		return "", ErrNoUsableSubtitleCues
	}
	var captured FfmpegParams
	doFfmpegFn = func(params FfmpegParams) (string, error) {
		captured = params
		return "/out/job.mp4", nil
	}
	_, err := app.executeRenderSpec(context.Background(), spec, "/out/job.mp4")
	return captured, err
}

// TestExecuteRenderSpecPrefersLocalFileOverPlexURL pins the local-filesystem
// optimisation: when Plex reports a directly readable file, FFmpeg must read it
// from disk instead of streaming over HTTP.
func TestExecuteRenderSpecPrefersLocalFileOverPlexURL(t *testing.T) {
	app := &Application{}
	app.config.Plex.Host = "https://plex.example"
	app.config.Plex.Token = "server-token"

	base := renderJobSpec{
		PartKey:       "/library/parts/1/file.mkv",
		FromMs:        0,
		ToMs:          2000,
		SubtitleIndex: -1,
	}

	t.Run("plex URL is used without a local file", func(t *testing.T) {
		params, err := captureRenderParams(t, app, base)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(params.URL, "https://plex.example/library/parts/1/file.mkv") {
			t.Errorf("URL = %q, want the Plex HTTP source", params.URL)
		}
		if !strings.Contains(params.URL, "X-Plex-Token=server-token") {
			t.Errorf("URL = %q, want the server token", params.URL)
		}
	})

	t.Run("local file wins when available", func(t *testing.T) {
		withFile := base
		withFile.PartFile = "/media/movies/film.mkv"
		params, err := captureRenderParams(t, app, withFile)
		if err != nil {
			t.Fatal(err)
		}
		if params.URL != "/media/movies/film.mkv" {
			t.Errorf("URL = %q, want the local file path", params.URL)
		}
		if strings.Contains(params.URL, "X-Plex-Token") {
			t.Errorf("URL = %q leaks the Plex token on a local path", params.URL)
		}
	})
}

// TestExecuteRenderSpecPassesEncodeSettings pins the settings that flow from a
// render job into FFmpeg.
func TestExecuteRenderSpecPassesEncodeSettings(t *testing.T) {
	app := &Application{}
	app.config.Plex.Host = "https://plex.example"
	app.config.Plex.Token = "token"
	app.config.Ffmpeg.Codec = CodecH264NVENC

	spec := renderJobSpec{
		PartKey:          "/library/parts/1/file.mkv",
		FromMs:           15000,
		ToMs:             45000,
		Height:           720,
		QP:               21,
		AudioMode:        AudioModeDialogueNormalized,
		SubtitleIndex:    -1,
		Title:            "Episode One",
		SubtitleOffsetMs: -2000,
	}

	params, err := captureRenderParams(t, app, spec)
	if err != nil {
		t.Fatal(err)
	}
	if params.Codec != CodecH264NVENC {
		t.Errorf("codec = %q, want %q", params.Codec, CodecH264NVENC)
	}
	if params.Height != 720 {
		t.Errorf("height = %d, want 720", params.Height)
	}
	if params.QP != 21 {
		t.Errorf("qp = %d, want 21", params.QP)
	}
	if params.AudioMode != AudioModeDialogueNormalized {
		t.Errorf("audio mode = %q, want %q", params.AudioMode, AudioModeDialogueNormalized)
	}
	if params.From != "00:00:15.000" || params.To != "00:00:45.000" {
		t.Errorf("range = %q..%q, want 00:00:15.000..00:00:45.000", params.From, params.To)
	}
	if params.SubtitleOffsetMs != -2000 {
		t.Errorf("subtitle offset = %d, want -2000", params.SubtitleOffsetMs)
	}
	if params.OutputPath != "/out/job.mp4" {
		t.Errorf("output path = %q, want the partial output path", params.OutputPath)
	}
	if params.Metadata.Title != "Episode One" {
		t.Errorf("metadata title = %q, want the job title", params.Metadata.Title)
	}
}

// TestExecuteRenderSpecUsesOverlayForPGSSubtitles pins that bitmap subtitles
// are burned in via the overlay filter rather than an extracted SRT file.
func TestExecuteRenderSpecUsesOverlayForPGSSubtitles(t *testing.T) {
	app := &Application{}
	app.config.Plex.Host = "https://plex.example"
	app.config.Plex.Token = "token"

	spec := renderJobSpec{
		PartKey:               "/library/parts/1/file.mkv",
		FromMs:                0,
		ToMs:                  3000,
		SubtitleIndex:         4,
		SubtitlePGS:           true,
		SubtitleEmbeddedIndex: 2,
	}

	params, err := captureRenderParams(t, app, spec)
	if err != nil {
		t.Fatal(err)
	}
	if params.SubtitleIndex != 2 {
		t.Errorf("subtitle index = %d, want the embedded index 2", params.SubtitleIndex)
	}
	if params.SubtitleFile != "" {
		t.Errorf("subtitle file = %q, want none for a PGS overlay", params.SubtitleFile)
	}

	// When no embedded index is recorded the public index is the fallback.
	fallback := spec
	fallback.SubtitleEmbeddedIndex = -1
	params, err = captureRenderParams(t, app, fallback)
	if err != nil {
		t.Fatal(err)
	}
	if params.SubtitleIndex != 4 {
		t.Errorf("fallback subtitle index = %d, want the public index 4", params.SubtitleIndex)
	}
}
