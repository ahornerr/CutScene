package main

import (
	"net/http/httptest"
	"strings"
	"testing"
)

// TestUnauthenticatedRoutesDoNotLeakData exercises the real router built by
// NewAPI and asserts that the JSON API routes refuse unauthenticated callers.
//
// Routes are registered as api.http.Get(path, handler, middleware), so Fiber
// invokes the handler before the middleware. That ordering is currently benign
// only because each handler independently refuses an unauthenticated caller;
// this test locks in the observable behaviour so a future handler that forgets
// to do so cannot start leaking silently.
func TestUnauthenticatedRoutesDoNotLeakData(t *testing.T) {
	api, err := NewAPI(Config{}, &Application{})
	if err != nil {
		t.Fatal(err)
	}

	// Browser-facing routes (thumb, streams, subtitles, preview) redirect to the
	// login flow with 302; JSON API routes answer 401 with a structured error.
	// Neither may return data.
	for _, tc := range []struct {
		path       string
		wantStatus int
	}{
		{path: "/clips", wantStatus: 401},
		{path: "/library/search?q=movie", wantStatus: 401},
		{path: "/clips/some-clip-id", wantStatus: 401},
		{path: "/clips/some-clip-id/download", wantStatus: 401},
		{path: "/clips/some-clip-id/artwork", wantStatus: 401},
		{path: "/subtitle-search/index-jobs/current", wantStatus: 401},
		{path: "/subtitle-search/index-jobs/abc", wantStatus: 401},
		{path: "/thumb?path=/x", wantStatus: 302},
		{path: "/streams/1", wantStatus: 302},
		{path: "/subtitles/1", wantStatus: 302},
		{path: "/preview/movie/00:00:00/00:00:10", wantStatus: 302},
	} {
		t.Run(tc.path, func(t *testing.T) {
			resp, err := api.http.Test(httptest.NewRequest("GET", tc.path, nil))
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != tc.wantStatus {
				t.Errorf("status = %d, want %d", resp.StatusCode, tc.wantStatus)
			}
			buf := make([]byte, 512)
			n, _ := resp.Body.Read(buf)
			if body := strings.ToLower(string(buf[:n])); strings.Contains(body, "session") && strings.Contains(body, "plex-token") {
				t.Errorf("response body looks like it contains credentials: %q", body)
			}
		})
	}
}
