package main

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/gofiber/fiber/v3"
)

func TestApplicationThumbPathValidation(t *testing.T) {
	app := &Application{}
	app.config.Plex.Host = "http://127.0.0.1:32400"
	app.config.Plex.Token = "test-token"

	invalidPaths := []struct {
		name string
		path string
	}{
		{"empty", ""},
		{"whitespace", "   "},
		{"scheme-relative", "//evil.com/thumb.jpg"},
		{"userinfo", "http://user:pass@127.0.0.1:32400/thumb.jpg"},
		{"external origin", "http://evil.com/thumb.jpg"},
		{"relative without slash", "library/metadata/1/thumb"},
		{"double slash relative", "//library/metadata/1/thumb"},
	}

	for _, tc := range invalidPaths {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := app.Thumb(context.Background(), tc.path)
			if err == nil {
				t.Fatalf("path %q was unexpectedly accepted", tc.path)
			}
			if !errors.Is(err, errThumbValidation) {
				t.Fatalf("expected errThumbValidation, got: %v", err)
			}
		})
	}
}

func TestApplicationThumbPlexTranscode(t *testing.T) {
	var capturedToken atomic.Pointer[string]
	var capturedURL atomic.Pointer[string]

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tok := r.Header.Get("X-Plex-Token")
		capturedToken.Store(&tok)
		u := r.URL.String()
		capturedURL.Store(&u)

		if r.URL.Path != "/photo/:/transcode" {
			http.NotFound(w, r)
			return
		}
		if r.URL.Query().Get("url") == "/fail" {
			http.Error(w, "transcode failed", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write([]byte("mock-image-data"))
	}))
	defer server.Close()

	app := &Application{}
	app.config.Plex.Host = server.URL
	app.config.Plex.Token = "server-admin-token"

	t.Run("successful transcode with header token", func(t *testing.T) {
		body, contentType, err := app.Thumb(context.Background(), "/library/metadata/100/thumb")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		defer body.Close()

		data, _ := io.ReadAll(body)
		if string(data) != "mock-image-data" {
			t.Fatalf("body = %q, want 'mock-image-data'", string(data))
		}
		if contentType != "image/jpeg" {
			t.Fatalf("contentType = %q, want 'image/jpeg'", contentType)
		}

		tok := capturedToken.Load()
		if tok == nil || *tok != "server-admin-token" {
			t.Fatalf("header token = %v, want 'server-admin-token'", tok)
		}
		rawURL := capturedURL.Load()
		if rawURL != nil && strings.Contains(*rawURL, "X-Plex-Token") {
			t.Fatalf("URL must not contain X-Plex-Token query param: %s", *rawURL)
		}
	})

	t.Run("uses caller token if present", func(t *testing.T) {
		ctx := ContextWithAuthToken(context.Background(), "caller-user-token")
		body, _, err := app.Thumb(ctx, "/library/metadata/100/thumb")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		defer body.Close()

		tok := capturedToken.Load()
		if tok == nil || *tok != "caller-user-token" {
			t.Fatalf("header token = %v, want 'caller-user-token'", tok)
		}
	})

	t.Run("transcode failure returns error", func(t *testing.T) {
		_, _, err := app.Thumb(context.Background(), "/fail")
		if err == nil {
			t.Fatal("expected error on 500 status")
		}
	})
}

func TestAPIThumbEndpoint(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("url") == "/fail" {
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write([]byte("image-png-content"))
	}))
	defer server.Close()

	app := &Application{}
	app.config.Plex.Host = server.URL
	app.config.Plex.Token = "token"

	api := &API{
		config: Config{},
		app:    app,
	}

	httpApp := fiber.New()
	httpApp.Use(func(ctx fiber.Ctx) error {
		ctx.SetUserContext(ContextWithUser(ContextWithAuthToken(context.Background(), "token"), User{Uuid: "caller"}))
		return ctx.Next()
	})
	httpApp.Get("/thumb", api.thumb, api.authMiddleware)

	t.Run("missing path returns 400 validation error", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/thumb", nil)
		resp, err := httpApp.Test(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400", resp.StatusCode)
		}
	})

	t.Run("invalid path returns 400 validation error", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/thumb?path=//evil.com/bad", nil)
		resp, err := httpApp.Test(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400", resp.StatusCode)
		}
	})

	t.Run("successful thumb returns 200 with content", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/thumb?path=/library/metadata/10/thumb", nil)
		resp, err := httpApp.Test(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200", resp.StatusCode)
		}
		body, _ := io.ReadAll(resp.Body)
		if string(body) != "image-png-content" {
			t.Fatalf("body = %q, want image-png-content", string(body))
		}
	})

	t.Run("upstream transcode failure returns 503", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/thumb?path=/fail", nil)
		resp, err := httpApp.Test(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusServiceUnavailable {
			t.Fatalf("status = %d, want 503", resp.StatusCode)
		}
	})
}
