package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPlexTVGetUser(t *testing.T) {
	origClient := plexTVHTTPClient
	defer func() {
		plexTVHTTPClient = origClient
	}()

	t.Run("successful numeric id", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/users/account.json" {
				http.NotFound(w, r)
				return
			}
			if r.Header.Get("X-Plex-Token") != "test-token" {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"user":{"id":12345,"uuid":"u-123","username":"plexuser","title":"Plex User","email":"user@example.com"}}`))
		}))
		defer server.Close()

		plexTVHTTPClient = server.Client()
		p := &PlexTV{token: "test-token", identifier: "client-id"}

		// Test with mock URL by redirecting via roundtripper
		plexTVHTTPClient.Transport = plexRoundTripFunc(func(r *http.Request) (*http.Response, error) {
			r.URL.Scheme = "http"
			r.URL.Host = server.Listener.Addr().String()
			return http.DefaultTransport.RoundTrip(r)
		})

		user, err := p.getUserContext(context.Background())
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if user.Id != 12345 || user.Uuid != "u-123" || user.Username != "plexuser" {
			t.Fatalf("unexpected user: %+v", user)
		}
	})

	t.Run("successful quoted string id", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"user":{"id":"67890","uuid":"u-678","username":"plexuser2","title":"User 2","email":"user2@example.com"}}`))
		}))
		defer server.Close()

		plexTVHTTPClient = server.Client()
		plexTVHTTPClient.Transport = plexRoundTripFunc(func(r *http.Request) (*http.Response, error) {
			r.URL.Scheme = "http"
			r.URL.Host = server.Listener.Addr().String()
			return http.DefaultTransport.RoundTrip(r)
		})

		p := &PlexTV{token: "test-token", identifier: "client-id"}
		user, err := p.getUserContext(context.Background())
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if user.Id != 67890 {
			t.Fatalf("id = %d, want 67890", user.Id)
		}
	})

	t.Run("non-200 status returns descriptive error", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "invalid token supplied", http.StatusUnauthorized)
		}))
		defer server.Close()

		plexTVHTTPClient = server.Client()
		plexTVHTTPClient.Transport = plexRoundTripFunc(func(r *http.Request) (*http.Response, error) {
			r.URL.Scheme = "http"
			r.URL.Host = server.Listener.Addr().String()
			return http.DefaultTransport.RoundTrip(r)
		})

		p := &PlexTV{token: "bad-token", identifier: "client-id"}
		_, err := p.getUserContext(context.Background())
		if err == nil {
			t.Fatal("expected error on 401")
		}
		if !strings.Contains(err.Error(), "status 401") || !strings.Contains(err.Error(), "invalid token") {
			t.Fatalf("unexpected error message: %v", err)
		}
	})

	t.Run("context cancellation aborts request", func(t *testing.T) {
		p := &PlexTV{token: "token", identifier: "client-id"}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		_, err := p.getUserContext(ctx)
		if err == nil {
			t.Fatal("expected error with canceled context")
		}
	})
}

func TestPlexTVGetUsers(t *testing.T) {
	origClient := plexTVHTTPClient
	defer func() {
		plexTVHTTPClient = origClient
	}()

	t.Run("successful xml parse and hasUser check", func(t *testing.T) {
		xmlBody := `<?xml version="1.0" encoding="UTF-8"?>
<MediaContainer machineIdentifier="server-machine-id">
  <User id="101" username="friend1" email="friend1@example.com">
    <Server serverId="1" machineIdentifier="server-machine-id" name="MyServer" owned="1" pending="0"/>
  </User>
  <User id="102" username="friend2" email="friend2@example.com">
    <Server serverId="2" machineIdentifier="other-machine-id" name="OtherServer" owned="0" pending="0"/>
  </User>
</MediaContainer>`

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/api/users" {
				http.NotFound(w, r)
				return
			}
			if r.Header.Get("X-Plex-Token") != "admin-tok" {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			w.Header().Set("Content-Type", "application/xml")
			_, _ = w.Write([]byte(xmlBody))
		}))
		defer server.Close()

		plexTVHTTPClient = server.Client()
		plexTVHTTPClient.Transport = plexRoundTripFunc(func(r *http.Request) (*http.Response, error) {
			r.URL.Scheme = "http"
			r.URL.Host = server.Listener.Addr().String()
			return http.DefaultTransport.RoundTrip(r)
		})

		p := &PlexTV{token: "admin-tok", identifier: "my-app"}
		users, err := p.getUsersContext(context.Background())
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if len(users.User) != 2 {
			t.Fatalf("user count = %d, want 2", len(users.User))
		}
		if !users.HasUser("101", "server-machine-id") {
			t.Fatal("expected user 101 to have access to server-machine-id")
		}
		if users.HasUser("102", "server-machine-id") {
			t.Fatal("user 102 should NOT have access to server-machine-id")
		}
	})

	t.Run("non-200 status returns descriptive error", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "access denied", http.StatusForbidden)
		}))
		defer server.Close()

		plexTVHTTPClient = server.Client()
		plexTVHTTPClient.Transport = plexRoundTripFunc(func(r *http.Request) (*http.Response, error) {
			r.URL.Scheme = "http"
			r.URL.Host = server.Listener.Addr().String()
			return http.DefaultTransport.RoundTrip(r)
		})

		p := &PlexTV{token: "token", identifier: "app"}
		_, err := p.getUsersContext(context.Background())
		if err == nil {
			t.Fatal("expected error on 403")
		}
		if !strings.Contains(err.Error(), "status 403") {
			t.Fatalf("unexpected error message: %v", err)
		}
	})
}
