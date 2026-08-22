package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestValidateYouTubeURLAndDownloadArguments(t *testing.T) {
	valid, err := validateYouTubeURL("https://youtu.be/dQw4w9WgXcQ")
	if err != nil || valid.ID != "dQw4w9WgXcQ" || valid.URL != "https://www.youtube.com/watch?v=dQw4w9WgXcQ" {
		t.Fatalf("validated video = %+v, %v", valid, err)
	}
	for _, raw := range []string{
		"http://www.youtube.com/watch?v=dQw4w9WgXcQ",
		"https://evil.example/watch?v=dQw4w9WgXcQ",
		"https://www.youtube.com/playlist?list=PL123456",
		"https://www.youtube.com/watch?v=dQw4w9WgXcQ&list=PL123456",
		"https://www.youtube.com/watch?v=bad.id",
	} {
		if _, err := validateYouTubeURL(raw); err == nil {
			t.Errorf("URL %q was accepted", raw)
		}
	}
	args := youtubeDownloadArgs(valid.URL, "/tmp/source")
	want := []string{"--ignore-config", "--no-playlist", "--no-warnings", "--format", "bv*+ba/b", "--output", filepath.Join("/tmp/source", "%(id)s.%(ext)s"), "--print", "after_move:filepath", valid.URL}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("download args = %#v, want %#v", args, want)
	}
	if strings.Contains(strings.Join(args, " "), "--config") {
		t.Fatal("download args allowed a config option")
	}
}

func TestYouTubeIngestionAndRegistryOwnership(t *testing.T) {
	app := &Application{}
	app.config.YouTube.Retention = time.Hour
	var calls [][]string
	app.youtubeRunner = func(_ context.Context, _ string, args []string, dir string) ([]byte, error) {
		calls = append(calls, append([]string(nil), args...))
		if len(calls) == 1 {
			return []byte(`{"id":"dQw4w9WgXcQ","title":"Test video","duration":12.5,"thumbnail":"https://img.example/thumb.jpg"}`), nil
		}
		path := filepath.Join(dir, "dQw4w9WgXcQ.mp4")
		if err := os.WriteFile(path, []byte("media"), 0600); err != nil {
			return nil, err
		}
		return []byte(path + "\n"), nil
	}
	source, err := app.ingestYouTubeSource(context.Background(), "owner-a", "https://www.youtube.com/watch?v=dQw4w9WgXcQ")
	if err != nil {
		t.Fatal(err)
	}
	if source.SourceID == "" || source.SourceType != "youtube" || source.Duration != 12500 || source.Thumb == "" || source.RatingKey == "" || len(source.Media) != 1 {
		t.Fatalf("unexpected source response: %+v", source)
	}
	if len(calls) != 2 || calls[0][0] != "--ignore-config" || calls[1][0] != "--ignore-config" {
		t.Fatalf("unexpected yt-dlp calls: %#v", calls)
	}
	if _, ok := app.youtubeSources.get(source.SourceID, "other-user"); ok {
		t.Fatal("another owner could read the source")
	}
	if got, ok := app.youtubeSources.get(source.SourceID, "owner-a"); !ok || got.Path == "" {
		t.Fatal("owner could not read the source")
	}
	record, ok := app.youtubeSources.get(source.SourceID, "owner-a")
	if !ok {
		t.Fatal("source disappeared before close")
	}
	app.youtubeSources.close()
	if _, err := os.Stat(record.TempDir); !os.IsNotExist(err) {
		t.Fatalf("source temp directory was not cleaned: %v", err)
	}
}

func TestMediaSourceEndpointContract(t *testing.T) {
	app := &Application{}
	app.config.YouTube.Retention = time.Hour
	app.youtubeRunner = func(_ context.Context, _ string, args []string, dir string) ([]byte, error) {
		if strings.Contains(strings.Join(args, " "), "--dump-single-json") {
			return []byte(`{"id":"dQw4w9WgXcQ","title":"Endpoint video","duration":2}`), nil
		}
		path := filepath.Join(dir, "dQw4w9WgXcQ.mp4")
		_ = os.WriteFile(path, []byte("media"), 0600)
		return []byte(path), nil
	}
	api := &API{app: app}
	httpApp := testAuthenticatedRoute(api.createMediaSource)
	request := httptest.NewRequest(http.MethodPost, "/media-sources", strings.NewReader(`{"url":"https://youtu.be/dQw4w9WgXcQ"}`))
	request.Header.Set("Content-Type", "application/json")
	response, err := httpApp.Test(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d", response.StatusCode)
	}
	var source youtubeSourceResponse
	if err := json.NewDecoder(response.Body).Decode(&source); err != nil {
		t.Fatal(err)
	}
	if source.SourceID == "" || source.SourceType != "youtube" || source.RatingKey == "" || source.MediaID == 0 || source.PartID == 0 {
		t.Fatalf("source contract is incomplete: %+v", source)
	}
	if response.Header.Get("Location") != "/media-sources/"+source.SourceID {
		t.Fatalf("Location = %q", response.Header.Get("Location"))
	}
	app.youtubeSources.close()
}

func TestExternalRenderSnapshotsAndExecutesLocalSource(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "source.mp4")
	if err := os.WriteFile(path, []byte("source"), 0600); err != nil {
		t.Fatal(err)
	}
	registry := newYouTubeSourceRegistry(context.Background(), time.Hour)
	defer registry.close()
	registry.put(youtubeSourceRecord{
		Response:  youtubeSourceResponse{SourceID: "source-1", SourceType: "youtube", RatingKey: "youtube:video", Type: "movie", Title: "External", Duration: 10000, MediaID: 9, PartID: 9},
		OwnerUUID: "owner-a", Path: path, TempDir: dir, ExpiresAt: time.Now().Add(time.Hour),
	})
	app := &Application{youtubeSources: registry}
	from := RenderJobCreateRequest{ExternalSourceID: "source-1", FromMs: 100, ToMs: 1000, SubtitleIndex: -1}
	spec, err := app.validateExternalRenderJobRequest(from, User{Uuid: "owner-a"})
	if err != nil {
		t.Fatal(err)
	}
	if spec.ExternalPath != path || spec.Title != "External" || spec.RatingKey != "youtube:video" {
		t.Fatalf("external spec did not snapshot source: %+v", spec)
	}
	var gotURL string
	app.ffmpegRunner = func(params FfmpegParams) (string, error) {
		gotURL = params.URL
		if err := os.WriteFile(params.OutputPath, []byte("rendered"), 0600); err != nil {
			return params.OutputPath, err
		}
		return params.OutputPath, nil
	}
	output := filepath.Join(t.TempDir(), "output.partial")
	if err := app.executeRenderSpec(context.Background(), spec, output); err != nil {
		t.Fatal(err)
	}
	if gotURL != path {
		t.Fatalf("FFmpeg source = %q, want local snapshot path %q", gotURL, path)
	}
}
