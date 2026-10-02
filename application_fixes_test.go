package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/gofiber/fiber/v3"
)

type testAPIErrorResponse struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func TestDownloadSubtitleWithTokenValidation(t *testing.T) {
	app := &Application{}
	app.config.Plex.Host = "http://127.0.0.1:32400"
	app.config.Plex.Token = "token"

	tests := []struct {
		name      string
		streamKey string
		errMsg    string
	}{
		{"empty", "", "subtitle stream key is required"},
		{"whitespace", "   ", "subtitle stream key is required"},
		{"scheme-relative", "//evil.com/sub.srt", "scheme-relative subtitle stream path is not allowed"},
		{"absolute url", "http://evil.com/sub.srt", "subtitle stream path is invalid"},
		{"missing leading slash", "sub.srt", "subtitle stream path is invalid"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := app.downloadSubtitleWithToken(context.Background(), tc.streamKey, "srt", "test-token")
			if err == nil {
				t.Fatalf("expected error for streamKey %q, got nil", tc.streamKey)
			}
			if !strings.Contains(err.Error(), tc.errMsg) {
				t.Fatalf("expected error %q, got %q", tc.errMsg, err.Error())
			}
		})
	}
}

func TestDownloadSubtitleWithTokenExecution(t *testing.T) {
	var capturedToken atomic.Pointer[string]
	var capturedURL atomic.Pointer[string]

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tok := r.Header.Get("X-Plex-Token")
		capturedToken.Store(&tok)
		u := r.URL.String()
		capturedURL.Store(&u)

		switch r.URL.Path {
		case "/subtitles/valid.srt":
			w.Header().Set("Content-Type", "text/plain")
			_, _ = io.WriteString(w, "1\n00:00:01,000 --> 00:00:02,000\nHello World\n\n")
		case "/subtitles/huge.srt":
			w.Header().Set("Content-Type", "text/plain")
			// Write more than maxLibraryResponseBytes (8MB)
			largeBuf := make([]byte, 1024*1024)
			for i := range largeBuf {
				largeBuf[i] = 'A'
			}
			for i := 0; i < 9; i++ {
				_, _ = w.Write(largeBuf)
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	app := &Application{}
	app.config.Plex.Host = server.URL
	app.config.Plex.Token = "default-token"

	t.Run("successful download sends token in header", func(t *testing.T) {
		entries, err := app.downloadSubtitleWithToken(context.Background(), "/subtitles/valid.srt", "srt", "custom-token")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(entries) != 1 || entries[0].Text != "Hello World" {
			t.Fatalf("unexpected entries: %+v", entries)
		}
		tok := capturedToken.Load()
		if tok == nil || *tok != "custom-token" {
			t.Fatalf("captured token = %v, want 'custom-token'", tok)
		}
		rawURL := capturedURL.Load()
		if rawURL != nil && strings.Contains(*rawURL, "X-Plex-Token") {
			t.Fatalf("X-Plex-Token should not be in query: %s", *rawURL)
		}
	})

	t.Run("response exceeding size limit returns error", func(t *testing.T) {
		_, err := app.downloadSubtitleWithToken(context.Background(), "/subtitles/huge.srt", "srt", "token")
		if err == nil {
			t.Fatal("expected size limit error")
		}
		if !strings.Contains(err.Error(), "exceeds size limit") {
			t.Fatalf("expected size limit error, got: %v", err)
		}
	})
}

func TestGetSessionsValidationAndLimit(t *testing.T) {
	t.Run("invalid configured origin fails", func(t *testing.T) {
		app := &Application{}
		app.config.Plex.Host = "http://evil.com:invalid-port"
		_, err := app.GetSessions(context.Background())
		if err == nil {
			t.Fatal("expected origin error")
		}
	})

	t.Run("response exceeding size limit fails", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			largeBuf := make([]byte, 1024*1024)
			for i := 0; i < 9; i++ {
				_, _ = w.Write(largeBuf)
			}
		}))
		defer server.Close()

		app := &Application{}
		app.config.Plex.Host = server.URL
		app.config.Plex.Token = "token"

		_, err := app.GetSessions(context.Background())
		if err == nil {
			t.Fatal("expected size limit error")
		}
		if !strings.Contains(err.Error(), "exceeds size limit") {
			t.Fatalf("expected size limit error, got: %v", err)
		}
	})

	t.Run("successful sessions parse", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"MediaContainer":{"size":1,"Metadata":[{"ratingKey":"1","title":"Session 1"}]}}`)
		}))
		defer server.Close()

		app := &Application{}
		app.config.Plex.Host = server.URL
		app.config.Plex.Token = "token"

		sessions, err := app.GetSessions(context.Background())
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(sessions) != 1 || sessions[0].RatingKey == nil || *sessions[0].RatingKey != "1" {
			t.Fatalf("expected 1 session with ratingKey 1, got %+v", sessions)
		}
	})
}

func TestAPISessionsAndSubtitleStructuredErrors(t *testing.T) {
	app := &Application{}
	app.config.Plex.Host = "http://127.0.0.1:1" // unconnectable host
	app.config.Plex.Token = "token"

	api := &API{
		config: Config{},
		app:    app,
	}

	httpApp := fiber.New()
	httpApp.Use(func(ctx fiber.Ctx) error {
		ctx.SetUserContext(ContextWithUser(ContextWithAuthToken(context.Background(), "tok"), User{Uuid: "user-1"}))
		return ctx.Next()
	})
	httpApp.Get("/sessions", api.getSessions)
	httpApp.Get("/subtitles/:ratingKey", api.getSubtitleEntries)
	httpApp.Get("/subtitles", api.getSubtitleEntries)

	t.Run("getSessions failure returns 503 structured error", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/sessions", nil)
		resp, err := httpApp.Test(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusServiceUnavailable {
			t.Fatalf("status = %d, want 503", resp.StatusCode)
		}
		var errResp testAPIErrorResponse
		if err := json.NewDecoder(resp.Body).Decode(&errResp); err != nil {
			t.Fatalf("failed to decode JSON error: %v", err)
		}
		if errResp.Error.Code != "sessions_unavailable" {
			t.Fatalf("error code = %q, want 'sessions_unavailable'", errResp.Error.Code)
		}
	})

	t.Run("getSubtitleEntries missing ratingKey returns 422 structured error", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/subtitles", nil)
		resp, err := httpApp.Test(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusUnprocessableEntity {
			t.Fatalf("status = %d, want 422", resp.StatusCode)
		}
		var errResp testAPIErrorResponse
		if err := json.NewDecoder(resp.Body).Decode(&errResp); err != nil {
			t.Fatalf("failed to decode JSON error: %v", err)
		}
		if errResp.Error.Code != "validation_error" {
			t.Fatalf("error code = %q, want 'validation_error'", errResp.Error.Code)
		}
	})

	t.Run("getSubtitleEntries non-integer subtitle returns 422 structured error", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/subtitles/100?subtitle=notanint", nil)
		resp, err := httpApp.Test(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusUnprocessableEntity {
			t.Fatalf("status = %d, want 422", resp.StatusCode)
		}
		var errResp testAPIErrorResponse
		if err := json.NewDecoder(resp.Body).Decode(&errResp); err != nil {
			t.Fatalf("failed to decode JSON error: %v", err)
		}
		if errResp.Error.Code != "validation_error" || !strings.Contains(errResp.Error.Message, "not an integer") {
			t.Fatalf("unexpected error response: %+v", errResp)
		}
	})
}
