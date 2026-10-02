package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

func mustURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parsing %q: %v", raw, err)
	}
	return u
}

// TestSamePlexOriginComparesOnlyOrigin pins the contract: "same origin" means
// the same scheme, host and port. Paths, queries and fragments must not take
// part in the comparison, because every caller passes a path-less configured
// origin against a resource URL that necessarily has a path.
//
// Comparing full URL strings made every absolute Plex URL look foreign,
// including the legitimate ones this function exists to allow.
func TestSamePlexOriginComparesOnlyOrigin(t *testing.T) {
	tests := []struct {
		name  string
		left  string
		right string
		want  bool
	}{
		{name: "same host with path", left: "https://plex.example", right: "https://plex.example/thumb/1", want: true},
		{name: "same host with deep path", left: "https://plex.example", right: "https://plex.example/library/metadata/9/children", want: true},
		{name: "same host with query", left: "https://plex.example", right: "https://plex.example/photo?width=320", want: true},
		{name: "identical", left: "https://plex.example", right: "https://plex.example", want: true},
		{name: "host case insensitive", left: "https://PLEX.example", right: "https://plex.example/thumb/1", want: true},
		{name: "default port equivalent", left: "https://plex.example:443", right: "https://plex.example/thumb/1", want: true},
		{name: "http default port equivalent", left: "http://plex.example:80", right: "http://plex.example/thumb/1", want: true},
		{name: "scheme mismatch", left: "https://plex.example", right: "http://plex.example/thumb/1", want: false},
		{name: "different host", left: "https://plex.example", right: "https://evil.example/thumb/1", want: false},
		{name: "suffix confusion", left: "https://plex.example", right: "https://plex.example.evil.example/thumb/1", want: false},
		{name: "different port", left: "https://plex.example:32400", right: "https://plex.example:32401/thumb/1", want: false},
		{name: "non default port kept", left: "https://plex.example:32400", right: "https://plex.example:32400/thumb/1", want: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := samePlexOrigin(mustURL(t, test.left), mustURL(t, test.right))
			if got != test.want {
				t.Errorf("samePlexOrigin(%q, %q) = %v, want %v", test.left, test.right, got, test.want)
			}
		})
	}
}

// TestSamePlexOriginRejectsForeignOrigins keeps the SSRF guard honest: a
// redirect or media URL on any other host must still be refused.
func TestSamePlexOriginRejectsForeignOrigins(t *testing.T) {
	origin := mustURL(t, "https://plex.example")
	for _, hostile := range []string{
		"http://169.254.169.254/latest/meta-data/",
		"http://127.0.0.1:8080/",
		"http://[::1]:8080/",
		"https://user:pass@plex.example/x",
		"https://plex.example.evil.example/x",
		"http://plex.example@evil.example/x",
	} {
		if samePlexOrigin(origin, mustURL(t, hostile)) {
			t.Errorf("samePlexOrigin accepted %q", hostile)
		}
	}
}

// TestThumbAcceptsAbsolutePlexURLs covers the user-visible symptom: the
// thumbnail proxy refused every absolute Plex URL, so callers passing the URL
// exactly as Plex reports it always failed.
func TestThumbAcceptsAbsolutePlexURLs(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write([]byte("PNGDATA"))
	}))
	defer upstream.Close()

	app := &Application{}
	app.config.Plex.Host = upstream.URL

	body, contentType, err := app.Thumb(context.Background(), upstream.URL+"/library/metadata/9/thumb/1")
	if err != nil {
		t.Fatalf("absolute thumbnail URL on the configured Plex origin was refused: %v", err)
	}
	defer body.Close()
	if contentType != "image/png" {
		t.Errorf("content type = %q, want image/png", contentType)
	}
}

// TestThumbStillRefusesForeignOrigins is the SSRF guard that must survive the
// origin-comparison fix.
func TestThumbStillRefusesForeignOrigins(t *testing.T) {
	app := &Application{}
	app.config.Plex.Host = "https://plex.example"

	for _, hostile := range []string{
		"http://169.254.169.254/latest/meta-data/",
		"http://127.0.0.1:8080/",
		"http://[::1]:8080/",
		"https://evil.example/thumb",
		"https://plex.example.evil.example/thumb",
	} {
		if _, _, err := app.Thumb(context.Background(), hostile); err == nil {
			t.Errorf("Thumb accepted %q", hostile)
		}
	}
}
