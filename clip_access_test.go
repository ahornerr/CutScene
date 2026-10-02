package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/gofiber/fiber/v3"
)

// newClipAccessFixture builds an API backed by a clip store holding one clip
// owned by "creator-a".
func newClipAccessFixture(t *testing.T) (*API, *Clip) {
	t.Helper()
	root := t.TempDir()
	store, err := newClipStore(root, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.close() })

	source := filepath.Join(t.TempDir(), "clip.mp4")
	if err := os.WriteFile(source, []byte("clip-bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	clip, err := store.promote(renderTestSpec("creator-a"), source)
	if err != nil {
		t.Fatal(err)
	}

	api := &API{config: Config{}, app: &Application{clipStore: store}}
	return api, clip
}

// clipDownloadApp registers the download route with a fixed authenticated user,
// bypassing session handling, which is covered separately.
func clipDownloadApp(api *API, user *User) *fiber.App {
	app := fiber.New()
	handler := func(ctx fiber.Ctx) error {
		base := context.Background()
		if user != nil {
			base = ContextWithUser(base, *user)
		}
		ctx.SetUserContext(base)
		return api.downloadClip(ctx)
	}
	app.Get("/clips/:id/download", handler)
	return app
}

// TestDownloadClipRequiresAuthentication covers the unauthenticated path.
func TestDownloadClipRequiresAuthentication(t *testing.T) {
	api, clip := newClipAccessFixture(t)

	resp, err := clipDownloadApp(api, nil).Test(httptest.NewRequest("GET", "/clips/"+clip.ID+"/download", nil))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusUnauthorized)
	}
}

// TestDownloadClipDeniesOtherOwnersWithoutRevealingExistence pins the
// deliberate choice to answer 404 rather than 403 for a clip the caller does
// not own: a 403 would confirm that the clip id exists.
func TestDownloadClipDeniesOtherOwnersWithoutRevealingExistence(t *testing.T) {
	api, clip := newClipAccessFixture(t)
	stranger := &User{Uuid: "someone-else", Email: "someone@example.com"}

	ownedResp, err := clipDownloadApp(api, stranger).Test(httptest.NewRequest("GET", "/clips/"+clip.ID+"/download", nil))
	if err != nil {
		t.Fatal(err)
	}
	defer ownedResp.Body.Close()

	missingResp, err := clipDownloadApp(api, stranger).Test(httptest.NewRequest("GET", "/clips/does-not-exist/download", nil))
	if err != nil {
		t.Fatal(err)
	}
	defer missingResp.Body.Close()

	if ownedResp.StatusCode != http.StatusNotFound {
		t.Errorf("foreign clip status = %d, want %d", ownedResp.StatusCode, http.StatusNotFound)
	}
	if missingResp.StatusCode != ownedResp.StatusCode {
		t.Errorf("status for a foreign clip (%d) differs from a missing clip (%d); existence is leaked",
			ownedResp.StatusCode, missingResp.StatusCode)
	}
}

// TestDownloadClipServesOwnedClip covers the happy path.
func TestDownloadClipServesOwnedClip(t *testing.T) {
	api, clip := newClipAccessFixture(t)
	owner := &User{Uuid: "creator-a", Email: "creator@example.com"}

	resp, err := clipDownloadApp(api, owner).Test(httptest.NewRequest("GET", "/clips/"+clip.ID+"/download", nil))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	body := make([]byte, 64)
	n, _ := resp.Body.Read(body)
	if string(body[:n]) != "clip-bytes" {
		t.Errorf("body = %q, want the stored clip bytes", string(body[:n]))
	}
}

// TestDownloadClipMissingStoreFailsClosed ensures a nil store cannot be used to
// bypass authorization.
func TestDownloadClipMissingStoreFailsClosed(t *testing.T) {
	api := &API{config: Config{}, app: &Application{}}
	owner := &User{Uuid: "creator-a"}

	resp, err := clipDownloadApp(api, owner).Test(httptest.NewRequest("GET", "/clips/any/download", nil))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusServiceUnavailable)
	}
}
