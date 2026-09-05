package main

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/LukeHagar/plexgo"
)

func TestValidateSubtitleFileForBurn(t *testing.T) {
	tempDir := t.TempDir()

	t.Run("zero-byte file returns ErrNoUsableSubtitleCues", func(t *testing.T) {
		emptyFile := filepath.Join(tempDir, "empty.srt")
		if err := os.WriteFile(emptyFile, []byte(""), 0600); err != nil {
			t.Fatal(err)
		}
		err := validateSubtitleFileForBurn(emptyFile)
		if !errors.Is(err, ErrNoUsableSubtitleCues) {
			t.Fatalf("expected ErrNoUsableSubtitleCues, got: %v", err)
		}
	})

	t.Run("cue with empty or whitespace text returns ErrNoUsableSubtitleCues", func(t *testing.T) {
		blankFile := filepath.Join(tempDir, "blank.srt")
		content := "1\n00:00:01,000 --> 00:00:02,000\n   \n\n"
		if err := os.WriteFile(blankFile, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
		err := validateSubtitleFileForBurn(blankFile)
		if !errors.Is(err, ErrNoUsableSubtitleCues) {
			t.Fatalf("expected ErrNoUsableSubtitleCues, got: %v", err)
		}
	})

	t.Run("valid cues pass validation", func(t *testing.T) {
		validFile := filepath.Join(tempDir, "valid.srt")
		content := "1\n00:00:01,000 --> 00:00:02,000\nHello World\n\n"
		if err := os.WriteFile(validFile, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
		if err := validateSubtitleFileForBurn(validFile); err != nil {
			t.Fatalf("expected nil error, got: %v", err)
		}
	})
}

func TestExecuteRenderSpecNonScopedEmbeddedEmptySubtitle(t *testing.T) {
	origExtract := extractSubtitleContextFn
	origDoFfmpeg := doFfmpegFn
	t.Cleanup(func() {
		extractSubtitleContextFn = origExtract
		doFfmpegFn = origDoFfmpeg
	})

	app := &Application{}
	app.config.Plex.Host = "http://127.0.0.1:32400"
	app.config.Plex.Token = "token"

	spec := renderJobSpec{
		RatingKey:             "movie-1",
		PartKey:               "/library/parts/1/file.mp4",
		FromMs:                0,
		ToMs:                  5000,
		SubtitleIndex:         1,
		SubtitleEmbeddedIndex: 0,
		SubtitleExternal:      false,
		SubtitlePGS:           false,
	}

	t.Run("empty subtitle succeeds with cleared subtitleFile", func(t *testing.T) {
		extractSubtitleContextFn = func(ctx context.Context, url, from, to string, subtitleIndex int, subtitleOffsets ...int64) (string, error) {
			return "", ErrNoUsableSubtitleCues
		}

		var capturedParams FfmpegParams
		doFfmpegFn = func(params FfmpegParams) (string, error) {
			capturedParams = params
			return "/out/job.mp4", nil
		}

		err := app.executeRenderSpec(context.Background(), spec, "/out/job.mp4")
		if err != nil {
			t.Fatalf("executeRenderSpec failed: %v", err)
		}
		if capturedParams.SubtitleFile != "" {
			t.Fatalf("expected empty SubtitleFile, got: %q", capturedParams.SubtitleFile)
		}
	})

	t.Run("other subtitle error fails with subtitle stage failure", func(t *testing.T) {
		extractSubtitleContextFn = func(ctx context.Context, url, from, to string, subtitleIndex int, subtitleOffsets ...int64) (string, error) {
			return "", errors.New("ffmpeg extraction failed")
		}

		err := app.executeRenderSpec(context.Background(), spec, "/out/job.mp4")
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		var failure *renderFailure
		if !errors.As(err, &failure) || failure.stage != "subtitle" || failure.code != "subtitle_unavailable" {
			t.Fatalf("expected subtitle_unavailable stage failure, got: %v", err)
		}
	})
}

func TestGetSubtitleEntriesForSourceEmptySubtitle(t *testing.T) {
	origExtract := extractSubtitleFullContextFn
	t.Cleanup(func() {
		extractSubtitleFullContextFn = origExtract
	})

	metadataJSON := `{
		"MediaContainer": {
			"Metadata": [{
				"ratingKey": "movie-1",
				"type": "movie",
				"title": "Test Movie",
				"Media": [{
					"id": 10,
					"Part": [{
						"id": 20,
						"key": "/library/parts/20/file.mp4",
						"Stream": [{
							"id": 30,
							"streamType": 3,
							"codec": "srt",
							"index": 2
						}]
					}]
				}]
			}]
		}
	}`

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/library/metadata/movie-1":
			_, _ = io.WriteString(w, metadataJSON)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	cfg := Config{}
	cfg.Plex.Host = server.URL
	cfg.Plex.Token = "admin-token"

	app := &Application{
		config:        cfg,
		plexAdmin:     plexgo.New(plexgo.WithServerURL(server.URL), plexgo.WithSecurity("admin-token")),
		subtitleCache: newSubtitleCache(100),
	}

	var extractionCount atomic.Int32
	extractSubtitleFullContextFn = func(ctx context.Context, url string, subtitleIndex int) (string, error) {
		extractionCount.Add(1)
		return "", ErrNoUsableSubtitleCues
	}

	ctx := context.Background()

	// First call should return empty slice and nil error
	entries, err := app.GetSubtitleEntriesForSource(ctx, "movie-1", "10", "20", 0)
	if err != nil {
		t.Fatalf("unexpected error for empty subtitle: %v", err)
	}
	if entries == nil || len(entries) != 0 {
		t.Fatalf("expected empty slice, got: %+v", entries)
	}
	if extractionCount.Load() != 1 {
		t.Fatalf("expected 1 extraction attempt, got: %d", extractionCount.Load())
	}

	// Second call should return from cache without re-extracting
	cachedEntries, err := app.GetSubtitleEntriesForSource(ctx, "movie-1", "10", "20", 0)
	if err != nil {
		t.Fatalf("unexpected error from cached empty subtitle: %v", err)
	}
	if cachedEntries == nil || len(cachedEntries) != 0 {
		t.Fatalf("expected empty slice from cache, got: %+v", cachedEntries)
	}
	if extractionCount.Load() != 1 {
		t.Fatalf("expected cache hit (count remaining 1), got: %d", extractionCount.Load())
	}
}
