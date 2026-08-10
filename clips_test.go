package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/LukeHagar/plexgo"
	"github.com/LukeHagar/plexgo/models/components"
	"github.com/gofiber/fiber/v3"
)

func TestSuccessfulRenderPromotesToDurableClipAndSurvivesStoreReopen(t *testing.T) {
	root := t.TempDir()
	store, err := newClipStore(root, "")
	if err != nil {
		t.Fatal(err)
	}
	defer store.close()
	manager, err := newRenderJobManagerWithContextAndPromotion(context.Background(), filepath.Join(root, "transient"), func(_ context.Context, spec renderJobSpec, output string) error {
		return os.WriteFile(output, []byte("durable mp4"), 0600)
	}, func(job *renderJob, output string) error {
		clip, err := store.promote(job.spec, output)
		if err == nil {
			job.mu.Lock()
			job.clipID = clip.ID
			job.shareURL = clipShareURLForDomain("https://clips.example.test", clip.ShareToken)
			job.mu.Unlock()
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	job, err := manager.enqueue("creator-a", renderTestSpec("creator-a"))
	if err != nil {
		t.Fatal(err)
	}
	defer manager.stopAndWait()
	status := waitRenderStatus(t, manager, job.id, "creator-a", renderSucceeded)
	if status.ClipID == "" {
		t.Fatal("successful render did not return promoted clip id")
	}
	if status.ShareURL == "" {
		t.Fatalf("successful promotion did not emit a share URL: %q", status.ShareURL)
	}
	clip, err := store.get(status.ClipID)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(clip.FilePath); err != nil || string(got) != "durable mp4" {
		t.Fatalf("durable clip bytes = %q, %v", got, err)
	}

	if err := store.close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := newClipStore(root, "")
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.close()
	clips, err := reopened.list("creator-a", false)
	if err != nil || len(clips) != 1 || clips[0].ID != clip.ID {
		t.Fatalf("reopened clips = %+v, %v", clips, err)
	}
	if clips[0].ShareToken != "" {
		t.Fatal("reopened clip leaked a raw share token")
	}
	rows, err := reopened.db.Query("PRAGMA table_info(clips)")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, columnType string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			t.Fatal(err)
		}
		if name == "share_token" {
			t.Fatal("raw share token column remains in SQLite")
		}
	}
}

func TestClipStartupPreflightFailsClosedWithoutDeletingMismatchedDurableState(t *testing.T) {
	root := t.TempDir()
	store, err := newClipStore(root, "")
	if err != nil {
		t.Fatal(err)
	}
	missingSource := filepath.Join(t.TempDir(), "missing.mp4")
	if err := os.WriteFile(missingSource, []byte("missing"), 0600); err != nil {
		t.Fatal(err)
	}
	missing, err := store.promote(renderTestSpec("missing-owner"), missingSource)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(missing.FilePath); err != nil {
		t.Fatal(err)
	}
	if err := store.close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "clips", "orphan.mp4"), []byte("orphan"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "clips", ".crash.partial"), []byte("partial"), 0600); err != nil {
		t.Fatal(err)
	}

	if _, err := newClipStore(root, ""); err == nil {
		t.Fatal("mismatched database/files were reconciled destructively")
	}
	for _, path := range []string{filepath.Join(root, "clips", "orphan.mp4"), filepath.Join(root, "clips", ".crash.partial")} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("mismatched durable file was removed: %q: %v", path, err)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "clips.sqlite3")); err != nil {
		t.Fatalf("mismatched database was removed: %v", err)
	}
}

func TestClipStartupPreflightRejectsPartialMP4SetWithoutMutation(t *testing.T) {
	root := t.TempDir()
	store, err := newClipStore(root, "")
	if err != nil {
		t.Fatal(err)
	}
	makeClip := func(name string) *Clip {
		source := filepath.Join(t.TempDir(), name+".mp4")
		if err := os.WriteFile(source, []byte(name), 0600); err != nil {
			t.Fatal(err)
		}
		clip, err := store.promote(renderTestSpec(name), source)
		if err != nil {
			t.Fatal(err)
		}
		return clip
	}
	first, second := makeClip("first"), makeClip("second")
	if err := store.close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(second.FilePath); err != nil {
		t.Fatal(err)
	}
	if _, err := newClipStore(root, ""); err == nil {
		t.Fatal("partial MP4 set was accepted")
	}
	if _, err := os.Stat(first.FilePath); err != nil {
		t.Fatalf("preflight removed surviving MP4: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "clips.sqlite3")); err != nil {
		t.Fatalf("preflight removed database: %v", err)
	}
}

func TestClipStoreMigratesLegacyRawTokenColumnAway(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "clips"), 0700); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite3", filepath.Join(root, "clips.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TABLE clips (
		id TEXT PRIMARY KEY, owner_uuid TEXT NOT NULL, title TEXT NOT NULL,
		rating_key TEXT NOT NULL, media_id INTEGER NOT NULL, from_ms INTEGER NOT NULL,
		to_ms INTEGER NOT NULL, created_at TEXT NOT NULL, share_token TEXT NOT NULL UNIQUE,
		share_token_hash TEXT NOT NULL UNIQUE, file_path TEXT NOT NULL UNIQUE
	);
	INSERT INTO clips VALUES ('legacy', 'owner', 'Legacy', 'movie', 1, 0, 1000,
		'2026-01-01T00:00:00Z', 'raw-secret-token', 'hash-only', 'CLIP_PATH');`)
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "clips", "legacy.mp4"), []byte("legacy bytes"), 0600); err != nil {
		db.Close()
		t.Fatal(err)
	}
	_, err = db.Exec("UPDATE clips SET file_path = ? WHERE id = 'legacy'", filepath.Join(root, "clips", "legacy.mp4"))
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	store, err := newClipStore(root, "")
	if err != nil {
		t.Fatal(err)
	}
	defer store.close()
	if _, err := store.get("legacy"); err != nil {
		t.Fatalf("legacy clip was not retained after migration: %v", err)
	}
	var rawColumnCount int
	if err := store.db.QueryRow("SELECT count(*) FROM pragma_table_info('clips') WHERE name = 'share_token'").Scan(&rawColumnCount); err != nil {
		t.Fatal(err)
	}
	if rawColumnCount != 0 {
		t.Fatal("legacy raw share token column survived migration")
	}
}

func TestCurrentEncryptedSchemaAddsNullablePresentationColumns(t *testing.T) {
	root := t.TempDir()
	store, err := newClipStore(root, "")
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(t.TempDir(), "current.mp4")
	if err := os.WriteFile(source, []byte("current"), 0600); err != nil {
		store.close()
		t.Fatal(err)
	}
	clip, err := store.promote(renderJobSpec{OwnerUUID: "creator-a", Title: "Old current", RatingKey: "movie", MediaID: 1, FromMs: 0, ToMs: 1000}, source)
	if err != nil {
		store.close()
		t.Fatal(err)
	}
	var ciphertext []byte
	if err := store.db.QueryRow("SELECT share_token_ciphertext FROM clips WHERE id = ?", clip.ID).Scan(&ciphertext); err != nil {
		store.close()
		t.Fatal(err)
	}
	if err := store.close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(root, "clips.sqlite3")); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite3", filepath.Join(root, "clips.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TABLE clips (
		id TEXT PRIMARY KEY, owner_uuid TEXT NOT NULL, title TEXT NOT NULL,
		rating_key TEXT NOT NULL, media_id INTEGER NOT NULL, from_ms INTEGER NOT NULL,
		to_ms INTEGER NOT NULL, created_at TEXT NOT NULL, share_token_hash TEXT NOT NULL UNIQUE,
		share_token_ciphertext BLOB NOT NULL
	);
	INSERT INTO clips VALUES (?, 'creator-a', 'Old current', 'movie', 1, 0, 1000, ?, ?, ?);`, clip.ID, clip.CreatedAt.Format(time.RFC3339Nano), hashShareToken(clip.ShareToken), ciphertext)
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := newClipStore(root, "")
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.close()
	restored, err := reopened.get(clip.ID)
	if err != nil {
		t.Fatal(err)
	}
	if restored.CreatorDisplayName != "" || restored.ArtworkPath != "" {
		t.Fatalf("old current clip did not retain nullable presentation defaults: %+v", restored)
	}
	encoded, err := json.Marshal(restored)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "ownerUuid") || strings.Contains(string(encoded), "creator@example.test") {
		t.Fatalf("old clip JSON exposed private identity data: %s", encoded)
	}
	api := &API{app: &Application{clipStore: reopened}}
	var storedHash string
	if err := reopened.db.QueryRow("SELECT share_token_hash FROM clips WHERE id = ?", clip.ID).Scan(&storedHash); err != nil {
		t.Fatal(err)
	}
	if storedHash != hashShareToken(clip.ShareToken) {
		t.Fatalf("preserved hash = %q, want %q", storedHash, hashShareToken(clip.ShareToken))
	}
	public := fiber.New()
	public.Get("/shared/clips/:token/download", api.publicDownloadClip)
	response, err := public.Test(httptest.NewRequest(http.MethodGet, "/shared/clips/"+clip.ShareToken+"/download", nil))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusOK || string(body) != "current" {
		t.Fatalf("preserved old public URL = %d %q", response.StatusCode, body)
	}
}

func TestServerOwnerUsesUUIDBeforeNormalizedEmailFallback(t *testing.T) {
	app := &Application{ownerUUID: "plex-owner-uuid", ownerEmail: "owner@example.test"}
	if app.isServerOwner(&User{Uuid: "other", Email: "owner@example.test"}) {
		t.Fatal("matching email bypassed configured Plex account UUID")
	}
	if !app.isServerOwner(&User{Uuid: " PLEX-OWNER-UUID ", Email: "different@example.test"}) {
		t.Fatal("configured Plex account UUID was not normalized")
	}
	if !app.isServerOwner(&User{Email: " OWNER@EXAMPLE.TEST "}) {
		t.Fatal("owner email fallback did not apply when request UUID was unavailable")
	}
	app.ownerUUID = ""
	if !app.isServerOwner(&User{Email: " OWNER@EXAMPLE.TEST "}) {
		t.Fatal("normalized email fallback did not identify server owner")
	}
}

func TestClipStorageFailureIsNotReportedAsAuthenticationOrNotFound(t *testing.T) {
	store, err := newClipStore(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.close(); err != nil {
		t.Fatal(err)
	}
	api := &API{app: &Application{clipStore: store}}
	app := fiber.New()
	app.Use(func(ctx fiber.Ctx) error {
		ctx.SetUserContext(ContextWithUser(context.Background(), User{Uuid: "creator-a"}))
		return ctx.Next()
	})
	app.Get("/clips/:id", api.getClip)
	response, err := app.Test(httptest.NewRequest(http.MethodGet, "/clips/clip-1", nil))
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("closed clip storage status = %d, want 503", response.StatusCode)
	}
}

func TestConfiguredDurableStorageRootFailureIsSurfacedAtStartup(t *testing.T) {
	root := filepath.Join(t.TempDir(), "storage-root")
	if err := os.WriteFile(root, []byte("not a directory"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := newClipStore(root, ""); err == nil {
		t.Fatal("inaccessible configured durable root was accepted")
	}
}

func persistPhase3TestClip(t *testing.T) (string, *Clip) {
	t.Helper()
	root := t.TempDir()
	store, err := newClipStore(root, "")
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(t.TempDir(), "persisted.mp4")
	if err := os.WriteFile(source, []byte("persisted"), 0600); err != nil {
		store.close()
		t.Fatal(err)
	}
	clip, err := store.promote(renderTestSpec("creator-a"), source)
	if err != nil {
		store.close()
		t.Fatal(err)
	}
	if err := store.close(); err != nil {
		t.Fatal(err)
	}
	return root, clip
}

func TestClipStoreFailsClosedWhenDatabaseIsMissingBesideMP4s(t *testing.T) {
	root, clip := persistPhase3TestClip(t)
	if err := os.Remove(filepath.Join(root, "clips.sqlite3")); err != nil {
		t.Fatal(err)
	}
	if _, err := newClipStore(root, ""); err == nil {
		t.Fatal("missing database with durable MP4s was accepted")
	}
	if _, err := os.Stat(clip.FilePath); err != nil {
		t.Fatalf("ambiguous recovery removed durable MP4: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "clips.sqlite3")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("ambiguous recovery created a replacement database: %v", err)
	}
}

func TestClipStoreRejectsMissingOrWrongKeyWithoutReplacement(t *testing.T) {
	root, _ := persistPhase3TestClip(t)
	keyPath := filepath.Join(root, clipTokenKeyFile)
	keyBefore, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(keyPath); err != nil {
		t.Fatal(err)
	}
	if _, err := newClipStore(root, ""); err == nil {
		t.Fatal("missing key for encrypted records was accepted")
	}
	if _, err := os.Stat(keyPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing required key was regenerated: %v", err)
	}
	if err := os.WriteFile(keyPath, bytes.Repeat([]byte{0xA5}, len(keyBefore)), 0600); err != nil {
		t.Fatal(err)
	}
	wrongKey, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := newClipStore(root, ""); err == nil {
		t.Fatal("wrong key for encrypted records was accepted")
	}
	keyAfter, err := os.ReadFile(keyPath)
	if err != nil || !bytes.Equal(wrongKey, keyAfter) {
		t.Fatalf("wrong key was replaced: %v", err)
	}
}

func TestClipStoreRelocatedRootDerivesFilePathFromClipID(t *testing.T) {
	parent := t.TempDir()
	oldRoot := filepath.Join(parent, "old")
	newRoot := filepath.Join(parent, "restored")
	store, err := newClipStore(oldRoot, "")
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(t.TempDir(), "relocated.mp4")
	if err := os.WriteFile(source, []byte("relocated"), 0600); err != nil {
		t.Fatal(err)
	}
	clip, err := store.promote(renderTestSpec("creator-a"), source)
	if err != nil {
		t.Fatal(err)
	}
	oldPath := clip.FilePath
	if err := store.close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(oldRoot, newRoot); err != nil {
		t.Fatal(err)
	}
	restored, err := newClipStore(newRoot, "")
	if err != nil {
		t.Fatal(err)
	}
	defer restored.close()
	restoredClip, err := restored.get(clip.ID)
	if err != nil {
		t.Fatal(err)
	}
	if restoredClip.FilePath == oldPath || !strings.HasPrefix(restoredClip.FilePath, filepath.Join(newRoot, "clips")) {
		t.Fatalf("restored clip path = %q, old path %q", restoredClip.FilePath, oldPath)
	}
	if body, err := os.ReadFile(restoredClip.FilePath); err != nil || string(body) != "relocated" {
		t.Fatalf("restored MP4 = %q, %v", body, err)
	}
}

func TestClipStoreRejectsAbsoluteAndEscapingDatabasePaths(t *testing.T) {
	root := t.TempDir()
	for _, database := range []string{"/tmp/clips.sqlite3", "../outside.sqlite3", "nested/../../outside.sqlite3"} {
		if _, err := newClipStore(root, database); err == nil {
			t.Fatalf("database path %q escaped validation", database)
		}
	}
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.MkdirAll(outside, 0700); err != nil {
		t.Fatal(err)
	}
	componentLink := filepath.Join(root, "linked")
	if err := os.Symlink(outside, componentLink); err != nil {
		t.Skipf("symlink test unavailable: %v", err)
	}
	if _, err := newClipStore(root, "linked/clips.sqlite3"); err == nil {
		t.Fatal("database path through an escaping symlink component was accepted")
	}
	outsideDB := filepath.Join(outside, "outside.sqlite3")
	if err := os.WriteFile(outsideDB, []byte{}, 0600); err != nil {
		t.Fatal(err)
	}
	finalLink := filepath.Join(root, "linked.sqlite3")
	if err := os.Symlink(outsideDB, finalLink); err != nil {
		t.Fatal(err)
	}
	if _, err := newClipStore(root, "linked.sqlite3"); err == nil {
		t.Fatal("database path through an escaping final symlink was accepted")
	}
}

func TestDeployedPhase1HashOnlyMigrationIsIdempotentAndRetainsClip(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "clips"), 0700); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite3", filepath.Join(root, "clips.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	clipID := "deployed-phase1-clip"
	filePath := filepath.Join(root, "clips", clipID+".mp4")
	if err := os.WriteFile(filePath, []byte("hash-only media"), 0600); err != nil {
		db.Close()
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TABLE clips (
		id TEXT PRIMARY KEY, owner_uuid TEXT NOT NULL, title TEXT NOT NULL,
		rating_key TEXT NOT NULL, media_id INTEGER NOT NULL, from_ms INTEGER NOT NULL,
		to_ms INTEGER NOT NULL, created_at TEXT NOT NULL, share_token_hash TEXT NOT NULL UNIQUE,
		file_path TEXT NOT NULL UNIQUE
	);
	INSERT INTO clips VALUES (?, 'creator-a', 'Migrated', 'movie', 1, 0, 1000,
		'2026-01-01T00:00:00Z', ?, ?);`, clipID, hashShareToken("lost-phase1-token"), filePath)
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	store, err := newClipStore(root, "")
	if err != nil {
		t.Fatal(err)
	}
	api := &API{app: &Application{clipStore: store}}
	list := fiber.New()
	list.Use(func(ctx fiber.Ctx) error {
		ctx.SetUserContext(ContextWithUser(context.Background(), User{Uuid: "creator-a"}))
		return ctx.Next()
	})
	list.Get("/clips", api.listClips)
	response, err := list.Test(httptest.NewRequest(http.MethodGet, "/clips", nil))
	if err != nil {
		store.close()
		t.Fatal(err)
	}
	var first clipListResponse
	if err := json.NewDecoder(response.Body).Decode(&first); err != nil {
		response.Body.Close()
		store.close()
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK || len(first.Clips) != 1 || first.Clips[0].ShareURL == "" {
		store.close()
		t.Fatalf("migrated HTTP list = %d %+v", response.StatusCode, first)
	}
	shareURL := first.Clips[0].ShareURL
	if err := store.close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := newClipStore(root, "")
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.close()
	restartedAPI := &API{app: &Application{clipStore: reopened}}
	restartedList := fiber.New()
	restartedList.Use(func(ctx fiber.Ctx) error {
		ctx.SetUserContext(ContextWithUser(context.Background(), User{Uuid: "creator-a"}))
		return ctx.Next()
	})
	restartedList.Get("/clips", restartedAPI.listClips)
	response, err = restartedList.Test(httptest.NewRequest(http.MethodGet, "/clips", nil))
	if err != nil {
		t.Fatal(err)
	}
	var second clipListResponse
	if err := json.NewDecoder(response.Body).Decode(&second); err != nil {
		response.Body.Close()
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK || len(second.Clips) != 1 || second.Clips[0].ShareURL != shareURL {
		t.Fatalf("second migrated HTTP list = %d %+v, want URL %q", response.StatusCode, second, shareURL)
	}
	var pathColumns int
	if err := reopened.db.QueryRow("SELECT count(*) FROM pragma_table_info('clips') WHERE name = 'file_path'").Scan(&pathColumns); err != nil {
		t.Fatal(err)
	}
	if pathColumns != 0 {
		t.Fatal("migrated schema retained root-dependent file_path")
	}
	for _, column := range []string{"creator_display_name", "media_kind", "movie_title", "movie_year", "show_title", "season_number", "episode_number", "episode_title", "thumbnail_mime"} {
		var count int
		if err := reopened.db.QueryRow("SELECT count(*) FROM pragma_table_info('clips') WHERE name = ?", column).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatalf("migrated schema missing nullable presentation column %q", column)
		}
	}
}

func TestRenderPresentationClassifiesMovieAndEpisodeAndSnapshotsCreator(t *testing.T) {
	year := 1999
	season, episode := 3, 7
	show := "Example Show"
	movie := presentationFromMetadata(User{Username: "plex-user", Email: "user@example.test"}, nil, "movie", &components.Metadata{
		Type: "movie", Title: "Example Movie", Year: &year,
	})
	if movie.CreatorDisplayName != "plex-user" || movie.MediaKind != "movie" || movie.MovieTitle != "Example Movie" || movie.MovieYear == nil || *movie.MovieYear != year {
		t.Fatalf("movie presentation = %+v", movie)
	}
	episodePresentation := presentationFromMetadata(User{Title: "Plex display title", Email: "fallback@example.test"}, nil, "episode", &components.Metadata{
		Type: "episode", Title: "Episode Title", GrandparentTitle: &show, ParentIndex: &season, Index: &episode,
	})
	if episodePresentation.CreatorDisplayName != "Plex display title" || episodePresentation.MediaKind != "episode" || episodePresentation.ShowTitle != show || episodePresentation.SeasonNumber == nil || *episodePresentation.SeasonNumber != season || episodePresentation.EpisodeNumber == nil || *episodePresentation.EpisodeNumber != episode || episodePresentation.EpisodeTitle != "Episode Title" {
		t.Fatalf("episode presentation = %+v", episodePresentation)
	}
	if got := creatorDisplayName(User{Email: "fallback@example.test"}); got != "" {
		t.Fatalf("creator display name fell back to email: %q", got)
	}
}

func TestClipArtworkCopiesServesAuthenticatedAndDeletesWithClip(t *testing.T) {
	root := t.TempDir()
	store, err := newClipStore(root, "")
	if err != nil {
		t.Fatal(err)
	}
	defer store.close()
	video := filepath.Join(t.TempDir(), "video.mp4")
	artwork := filepath.Join(t.TempDir(), "poster.jpg")
	if err := os.WriteFile(video, []byte("video"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(artwork, []byte("poster bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	year := 2024
	clip, err := store.promoteWithArtwork(renderJobSpec{OwnerUUID: "creator-a", CreatorDisplayName: "plex-user", MediaKind: "movie", MovieTitle: "Artwork Movie", MovieYear: &year, Title: "Artwork clip", RatingKey: "movie", MediaID: 1, FromMs: 0, ToMs: 1000}, video, artwork, "image/jpeg")
	if err != nil {
		t.Fatal(err)
	}
	if clip.ArtworkPath == "" {
		t.Fatal("promoted clip has no durable artwork path")
	}
	api := &API{app: &Application{clipStore: store}}
	httpApp := fiber.New()
	httpApp.Use(func(ctx fiber.Ctx) error {
		ctx.SetUserContext(ContextWithUser(context.Background(), User{Uuid: "creator-a"}))
		return ctx.Next()
	})
	httpApp.Get("/clips/:id/artwork", api.downloadClipArtwork)
	httpApp.Get("/clips/:id", api.getClip)
	response, err := httpApp.Test(httptest.NewRequest(http.MethodGet, "/clips/"+clip.ID+"/artwork", nil))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusOK || string(body) != "poster bytes" || response.Header.Get("Content-Type") != "image/jpeg" || response.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("artwork response = %d %q type=%q nosniff=%q", response.StatusCode, body, response.Header.Get("Content-Type"), response.Header.Get("X-Content-Type-Options"))
	}
	response, err = httpApp.Test(httptest.NewRequest(http.MethodGet, "/clips/"+clip.ID, nil))
	if err != nil {
		t.Fatal(err)
	}
	detailBody, _ := io.ReadAll(response.Body)
	var detail clipAPIResponse
	if err := json.Unmarshal(detailBody, &detail); err != nil {
		response.Body.Close()
		t.Fatal(err)
	}
	response.Body.Close()
	for _, privateValue := range []string{"ownerUuid", "user@example.test"} {
		if strings.Contains(string(detailBody), privateValue) {
			t.Fatalf("authenticated detail exposed private value %q: %s", privateValue, detailBody)
		}
	}
	if detail.CreatorDisplayName != "plex-user" || detail.MediaKind != "movie" || detail.MovieTitle != "Artwork Movie" || detail.MovieYear == nil || *detail.MovieYear != year || detail.ArtworkURL != "/clips/"+clip.ID+"/artwork" {
		t.Fatalf("authenticated presentation detail = %+v", detail)
	}
	public := fiber.New()
	public.Get("/shared/clips/:token", api.publicClip)
	response, err = public.Test(httptest.NewRequest(http.MethodGet, "/shared/clips/"+clip.ShareToken, nil))
	if err != nil {
		t.Fatal(err)
	}
	publicBody, _ := io.ReadAll(response.Body)
	response.Body.Close()
	for _, privateField := range []string{"ownerUuid", "user@example.test", "creatorDisplayName", "mediaKind", "movieTitle", "movieYear", "showTitle", "seasonNumber", "episodeNumber", "episodeTitle", "artworkUrl", "/library/"} {
		if strings.Contains(string(publicBody), privateField) {
			t.Fatalf("public metadata exposed private presentation data: %s", publicBody)
		}
	}
	artworkPath := clip.ArtworkPath
	if err := store.delete(clip.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(artworkPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("clip artwork survived hard deletion: %v", err)
	}
}

func TestClipArtworkFetchFailureStillAllowsClipWithoutThumbnail(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, "upstream failure")
	}))
	defer server.Close()
	application := &Application{}
	application.config.Plex.Host = server.URL
	path, mimeType, err := application.fetchClipArtwork(context.Background(), "/library/metadata/1/thumb")
	if err == nil || path != "" || mimeType != "" {
		t.Fatalf("artwork failure = path %q mime %q err %v", path, mimeType, err)
	}
	root := t.TempDir()
	store, err := newClipStore(root, "")
	if err != nil {
		t.Fatal(err)
	}
	defer store.close()
	video := filepath.Join(t.TempDir(), "video.mp4")
	if err := os.WriteFile(video, []byte("video"), 0600); err != nil {
		t.Fatal(err)
	}
	clip, err := store.promoteWithArtwork(renderJobSpec{OwnerUUID: "creator-a", Title: "No poster", RatingKey: "movie", MediaID: 1, FromMs: 0, ToMs: 1000}, video, path, mimeType)
	if err != nil {
		t.Fatal(err)
	}
	if clip.ArtworkPath != "" || clip.ThumbnailMIME != "" {
		t.Fatalf("failed artwork was persisted: %+v", clip)
	}
}

func TestClipArtworkFetchRejectsUnsafeRedirectsMIMEsOversizeAndTimeout(t *testing.T) {
	validImage := func() []byte {
		var buffer bytes.Buffer
		imageValue := image.NewRGBA(image.Rect(0, 0, 1, 1))
		imageValue.Set(0, 0, color.RGBA{R: 255, A: 255})
		if err := png.Encode(&buffer, imageValue); err != nil {
			t.Fatal(err)
		}
		return buffer.Bytes()
	}
	otherRequests := 0
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		otherRequests++
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(validImage())
	}))
	defer other.Close()
	base := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/redirect":
			http.Redirect(w, r, other.URL+"/image", http.StatusFound)
		case "/text":
			w.Header().Set("Content-Type", "text/plain")
			_, _ = io.WriteString(w, "not an image")
		case "/svg":
			w.Header().Set("Content-Type", "image/svg+xml")
			_, _ = io.WriteString(w, "<svg></svg>")
		case "/large":
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(bytes.Repeat([]byte("x"), int(maxClipArtworkBytes)+1))
		case "/timeout":
			<-r.Context().Done()
		default:
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(validImage())
		}
	}))
	defer base.Close()
	application := &Application{}
	application.config.Plex.Host = base.URL
	checks := []string{
		"//evil.example/image",
		other.URL + "/image",
		"http://user@" + strings.TrimPrefix(base.URL, "http://") + "/image",
		"/redirect",
		"/text",
		"/svg",
		"/large",
	}
	for _, path := range checks {
		if temporary, _, err := application.fetchClipArtwork(context.Background(), path); err == nil {
			if temporary != "" {
				os.Remove(temporary)
			}
			t.Fatalf("unsafe/invalid artwork path %q was accepted", path)
		}
	}
	if otherRequests != 0 {
		t.Fatalf("cross-origin redirect reached destination %d times", otherRequests)
	}
	timeoutContext, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if temporary, _, err := application.fetchClipArtwork(timeoutContext, "/timeout"); err == nil || temporary != "" {
		t.Fatalf("artwork timeout = path %q err %v", temporary, err)
	}
	temporary, mimeType, err := application.fetchClipArtwork(context.Background(), "/image")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(temporary)
	if mimeType != "image/png" {
		t.Fatalf("valid artwork MIME = %q", mimeType)
	}
}

func TestClipPromotionRollsBackMP4AndArtworkAfterMetadataFailure(t *testing.T) {
	root := t.TempDir()
	store, err := newClipStore(root, "")
	if err != nil {
		t.Fatal(err)
	}
	video := filepath.Join(t.TempDir(), "video.mp4")
	artwork := filepath.Join(t.TempDir(), "poster.png")
	if err := os.WriteFile(video, []byte("video"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(artwork, []byte("poster"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := store.close(); err != nil {
		t.Fatal(err)
	}
	if _, err := store.promoteWithArtwork(renderJobSpec{OwnerUUID: "creator-a", Title: "rollback", RatingKey: "movie", MediaID: 1, FromMs: 0, ToMs: 1000}, video, artwork, "image/png"); err == nil {
		t.Fatal("promotion succeeded with closed metadata database")
	}
	entries, err := os.ReadDir(filepath.Join(root, "clips"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("promotion rollback left durable files: %+v", entries)
	}
}

func TestClipStartupPreflightRejectsMissingReferencedThumbnail(t *testing.T) {
	root := t.TempDir()
	store, err := newClipStore(root, "")
	if err != nil {
		t.Fatal(err)
	}
	video := filepath.Join(t.TempDir(), "video.mp4")
	artwork := filepath.Join(t.TempDir(), "poster.jpg")
	if err := os.WriteFile(video, []byte("video"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(artwork, []byte("poster"), 0600); err != nil {
		t.Fatal(err)
	}
	clip, err := store.promoteWithArtwork(renderJobSpec{OwnerUUID: "creator-a", Title: "Poster", RatingKey: "movie", MediaID: 1, FromMs: 0, ToMs: 1000}, video, artwork, "image/jpeg")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(clip.ArtworkPath); err != nil {
		t.Fatal(err)
	}
	if _, err := newClipStore(root, ""); err == nil {
		t.Fatal("missing referenced thumbnail was reconciled destructively")
	}
	if _, err := os.Stat(filepath.Join(root, "clips.sqlite3")); err != nil {
		t.Fatalf("thumbnail preflight removed database: %v", err)
	}
}

func TestHTTPRenderPromotionRestartAndPublicDownloadLifecycle(t *testing.T) {
	var poster bytes.Buffer
	posterImage := image.NewRGBA(image.Rect(0, 0, 1, 1))
	posterImage.Set(0, 0, color.RGBA{B: 255, A: 255})
	if err := png.Encode(&poster, posterImage); err != nil {
		t.Fatal(err)
	}
	plex := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/status/sessions":
			_, _ = io.WriteString(w, `{"MediaContainer":{"Metadata":[{"key":"movie-1","title":"Lifecycle movie","thumb":"/library/metadata/movie-1/thumb","Media":[{"id":293539,"duration":60000,"Part":[{"id":293546}]}]}]}}`)
		case "/library/metadata/movie-1":
			_, _ = io.WriteString(w, `{"MediaContainer":{"Metadata":[{"ratingKey":"movie-1","type":"movie","title":"Lifecycle movie","year":2024,"thumb":"/library/metadata/movie-1/thumb","Media":[{"id":293539,"duration":60000,"Part":[{"id":293546,"key":"/library/parts/293546/file.mp4","duration":60000}]}]}]}}`)
		case "/library/metadata/movie-1/thumb":
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(poster.Bytes())
		default:
			http.NotFound(w, r)
		}
	}))
	defer plex.Close()

	root := t.TempDir()
	store, err := newClipStore(root, "")
	if err != nil {
		t.Fatal(err)
	}
	var application *Application
	manager, err := newRenderJobManagerWithContextAndPromotion(context.Background(), filepath.Join(root, "transient"), writeRenderOutput, func(job *renderJob, output string) error {
		artworkPath, artworkMIME, _ := application.fetchClipArtwork(application.lifetime, job.spec.ThumbnailURL)
		if artworkPath != "" {
			defer os.Remove(artworkPath)
		}
		clip, err := store.promoteWithArtwork(job.spec, output, artworkPath, artworkMIME)
		if err != nil {
			return err
		}
		job.mu.Lock()
		job.clipID = clip.ID
		job.shareURL = clipShareURLForDomain("", clip.ShareToken)
		job.mu.Unlock()
		return nil
	})
	if err != nil {
		store.close()
		t.Fatal(err)
	}
	defer manager.stopAndWait()
	config := Config{}
	config.Plex.Host = plex.URL
	config.Plex.Token = "admin-token"
	lifetime, cancelLifetime := context.WithCancel(context.Background())
	defer cancelLifetime()
	application = &Application{
		config:     config,
		ownerEmail: "owner@example.com",
		plexAdmin:  plexgo.New(plexgo.WithServerURL(plex.URL), plexgo.WithSecurity(config.Plex.Token)),
		renderJobs: manager,
		clipStore:  store,
		lifetime:   lifetime,
	}
	api := &API{config: config, app: application}
	httpApp := fiber.New()
	httpApp.Use(func(ctx fiber.Ctx) error {
		ctx.SetUserContext(ContextWithUser(context.Background(), User{Uuid: "owner-a", Username: "plex-user", Title: "Plex display title", Email: "owner@example.com"}))
		return ctx.Next()
	})
	httpApp.Post("/render-jobs", api.createRenderJob)
	httpApp.Get("/render-jobs/:id", api.getRenderJob)
	httpApp.Get("/clips", api.listClips)

	request := httptest.NewRequest(http.MethodPost, "/render-jobs", strings.NewReader(`{"ratingKey":"movie-1","mediaId":293546,"fromMs":0,"toMs":1000}`))
	request.Header.Set("Content-Type", "application/json")
	response, err := httpApp.Test(request)
	if err != nil {
		t.Fatal(err)
	}
	var created renderJobResponse
	if err := json.NewDecoder(response.Body).Decode(&created); err != nil {
		response.Body.Close()
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusAccepted || created.ID == "" {
		t.Fatalf("render creation = %d %+v", response.StatusCode, created)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		response, err = httpApp.Test(httptest.NewRequest(http.MethodGet, "/render-jobs/"+created.ID, nil))
		if err != nil {
			t.Fatal(err)
		}
		var status renderJobResponse
		decodeErr := json.NewDecoder(response.Body).Decode(&status)
		response.Body.Close()
		if decodeErr != nil {
			t.Fatal(decodeErr)
		}
		if status.Status == renderSucceeded {
			created = status
			break
		}
		time.Sleep(time.Millisecond)
	}
	if created.Status != renderSucceeded || created.ClipID == "" || created.ShareURL == "" {
		t.Fatalf("render did not promote through HTTP lifecycle: %+v", created)
	}
	response, err = httpApp.Test(httptest.NewRequest(http.MethodGet, "/clips", nil))
	if err != nil {
		t.Fatal(err)
	}
	listBody, _ := io.ReadAll(response.Body)
	var listed clipListResponse
	if err := json.Unmarshal(listBody, &listed); err != nil {
		response.Body.Close()
		t.Fatal(err)
	}
	response.Body.Close()
	for _, privateValue := range []string{"ownerUuid", "user@example.test"} {
		if strings.Contains(string(listBody), privateValue) {
			t.Fatalf("authenticated list exposed private value %q: %s", privateValue, listBody)
		}
	}
	if response.StatusCode != http.StatusOK || len(listed.Clips) != 1 || listed.Clips[0].ID != created.ClipID || listed.Clips[0].ArtworkURL == "" || listed.Clips[0].MediaKind != "movie" || listed.Clips[0].MovieTitle != "Lifecycle movie" || listed.Clips[0].CreatorDisplayName != "plex-user" {
		t.Fatalf("promoted HTTP clip list = %d %+v", response.StatusCode, listed)
	}
	if err := store.close(); err != nil {
		t.Fatal(err)
	}
	restartedStore, err := newClipStore(root, "")
	if err != nil {
		t.Fatal(err)
	}
	defer restartedStore.close()
	restartedAPI := &API{app: &Application{clipStore: restartedStore}}
	public := fiber.New()
	public.Get("/shared/clips/:token/download", restartedAPI.publicDownloadClip)
	response, err = public.Test(httptest.NewRequest(http.MethodGet, created.ShareURL, nil))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusOK || string(body) != "valid mp4 bytes" {
		t.Fatalf("restarted public lifecycle download = %d %q", response.StatusCode, body)
	}
}

func TestClipAuthorizationAndPublicShareToken(t *testing.T) {
	store, err := newClipStore(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	defer store.close()
	source := filepath.Join(t.TempDir(), "output.mp4")
	if err := os.WriteFile(source, []byte("clip bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	clip, err := store.promote(renderTestSpec("creator-a"), source)
	if err != nil {
		t.Fatal(err)
	}
	if clip.ShareToken == "" || clip.ShareToken == clip.ID || len(clip.ShareToken) < 40 {
		t.Fatalf("share token is not sufficiently unguessable: %q", clip.ShareToken)
	}

	config := Config{}
	config.API.Domain = "https://clips.example.test"
	application := &Application{clipStore: store, ownerEmail: "owner@example.test"}
	api := &API{config: config, app: application}
	creator := func(ctx fiber.Ctx) error {
		ctx.SetUserContext(ContextWithUser(context.Background(), User{Uuid: "creator-a", Email: "creator@example.test"}))
		return ctx.Next()
	}
	authApp := fiber.New()
	authApp.Use(creator)
	authApp.Get("/clips/:id", api.getClip)
	authApp.Delete("/clips/:id", api.deleteClip)

	response, err := authApp.Test(httptest.NewRequest(http.MethodGet, "/clips/"+clip.ID, nil))
	if err != nil {
		t.Fatal(err)
	}
	var creatorDetail clipAPIResponse
	if err := json.NewDecoder(response.Body).Decode(&creatorDetail); err != nil {
		response.Body.Close()
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("creator detail status = %d", response.StatusCode)
	}
	if creatorDetail.IsAdmin == nil || *creatorDetail.IsAdmin || !creatorDetail.CanDelete {
		t.Fatalf("creator detail capabilities = %+v", creatorDetail)
	}

	adminAPI := &API{config: config, app: &Application{clipStore: store, ownerEmail: "owner@example.test"}}
	adminApp := fiber.New()
	adminApp.Use(func(ctx fiber.Ctx) error {
		ctx.SetUserContext(ContextWithUser(context.Background(), User{Uuid: "admin", Email: "OWNER@example.test"}))
		return ctx.Next()
	})
	adminApp.Get("/clips/:id", adminAPI.getClip)
	response, err = adminApp.Test(httptest.NewRequest(http.MethodGet, "/clips/"+clip.ID, nil))
	if err != nil {
		t.Fatal(err)
	}
	var adminDetail clipAPIResponse
	if err := json.NewDecoder(response.Body).Decode(&adminDetail); err != nil {
		response.Body.Close()
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK || adminDetail.IsAdmin == nil || !*adminDetail.IsAdmin || !adminDetail.CanDelete {
		t.Fatalf("admin detail capabilities = %d %+v", response.StatusCode, adminDetail)
	}

	otherApp := fiber.New()
	otherApp.Use(func(ctx fiber.Ctx) error {
		ctx.SetUserContext(ContextWithUser(context.Background(), User{Uuid: "creator-b", Email: "other@example.test"}))
		return ctx.Next()
	})
	otherApp.Get("/clips/:id", api.getClip)
	response, err = otherApp.Test(httptest.NewRequest(http.MethodGet, "/clips/"+clip.ID, nil))
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("other creator status = %d, want 404", response.StatusCode)
	}

	publicApp := fiber.New()
	publicApp.Get("/shared/clips/:token", api.publicClip)
	publicApp.Get("/shared/clips/:token/download", api.publicDownloadClip)
	response, err = publicApp.Test(httptest.NewRequest(http.MethodGet, "/shared/clips/"+clip.ShareToken, nil))
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("public detail status = %d", response.StatusCode)
	}
	if response.Header.Get("Cache-Control") != "no-store" || response.Header.Get("Referrer-Policy") != "no-referrer" {
		t.Fatalf("public detail capability headers = cache=%q referrer=%q", response.Header.Get("Cache-Control"), response.Header.Get("Referrer-Policy"))
	}
	response, err = publicApp.Test(httptest.NewRequest(http.MethodGet, "/shared/clips/"+clip.ShareToken+"/download", nil))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusOK || string(body) != "clip bytes" {
		t.Fatalf("public download = %d %q", response.StatusCode, body)
	}
	if response.Header.Get("Cache-Control") != "no-store" || response.Header.Get("Referrer-Policy") != "no-referrer" {
		t.Fatalf("public download capability headers = cache=%q referrer=%q", response.Header.Get("Cache-Control"), response.Header.Get("Referrer-Policy"))
	}
	response, err = publicApp.Test(httptest.NewRequest(http.MethodGet, "/shared/clips/not-the-token", nil))
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("guessed share token status = %d, want 404", response.StatusCode)
	}
}

func TestClipHTTPContractPersistsShareURLAndServesInlineMediaAfterRestart(t *testing.T) {
	root := t.TempDir()
	store, err := newClipStore(root, "")
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(t.TempDir(), "output.mp4")
	if err := os.WriteFile(source, []byte("persistent media"), 0600); err != nil {
		t.Fatal(err)
	}
	clip, err := store.promote(renderTestSpec("creator-a"), source)
	if err != nil {
		t.Fatal(err)
	}
	token := clip.ShareToken
	keyPath := filepath.Join(root, clipTokenKeyFile)
	keyInfo, err := os.Stat(keyPath)
	if err != nil || keyInfo.Mode().Perm() != 0600 {
		t.Fatalf("token key permissions = %v, want 0600", err)
	}
	keyBefore, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	api := &API{app: &Application{clipStore: store}}
	authApp := fiber.New()
	authApp.Use(func(ctx fiber.Ctx) error {
		ctx.SetUserContext(ContextWithUser(context.Background(), User{Uuid: "creator-a"}))
		return ctx.Next()
	})
	authApp.Get("/clips", api.listClips)
	response, err := authApp.Test(httptest.NewRequest(http.MethodGet, "/clips", nil))
	if err != nil {
		t.Fatal(err)
	}
	var listed clipListResponse
	if err := json.NewDecoder(response.Body).Decode(&listed); err != nil {
		response.Body.Close()
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK || len(listed.Clips) != 1 || listed.IsAdmin || !listed.Clips[0].CanDelete {
		t.Fatalf("initial clip list = %d %+v", response.StatusCode, listed)
	}
	wantURL := "/shared/clips/" + token + "/download"
	if listed.Clips[0].ShareURL != wantURL || listed.Clips[0].PublicDownloadURL != wantURL {
		t.Fatalf("initial share URLs = %+v, want %q", listed.Clips[0], wantURL)
	}
	if listed.Clips[0].ShareURL == "/shared/clips/"+token {
		t.Fatal("share URL points to public metadata instead of inline media")
	}

	if err := store.close(); err != nil {
		t.Fatal(err)
	}
	databaseBytes, err := os.ReadFile(filepath.Join(root, "clips.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(databaseBytes, []byte(token)) {
		t.Fatal("raw share token is present in SQLite database bytes")
	}
	restartedStore, err := newClipStore(root, "")
	if err != nil {
		t.Fatal(err)
	}
	defer restartedStore.close()
	keyAfter, err := os.ReadFile(keyPath)
	if err != nil || !bytes.Equal(keyBefore, keyAfter) {
		t.Fatalf("durable token key changed across restart: %v", err)
	}
	restartedAPI := &API{app: &Application{clipStore: restartedStore}}
	restartedAuth := fiber.New()
	restartedAuth.Use(func(ctx fiber.Ctx) error {
		ctx.SetUserContext(ContextWithUser(context.Background(), User{Uuid: "creator-a"}))
		return ctx.Next()
	})
	restartedAuth.Get("/clips", restartedAPI.listClips)
	response, err = restartedAuth.Test(httptest.NewRequest(http.MethodGet, "/clips", nil))
	if err != nil {
		t.Fatal(err)
	}
	listed = clipListResponse{}
	if err := json.NewDecoder(response.Body).Decode(&listed); err != nil {
		response.Body.Close()
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK || len(listed.Clips) != 1 || listed.IsAdmin || !listed.Clips[0].CanDelete || listed.Clips[0].ShareURL != wantURL || listed.Clips[0].PublicDownloadURL != wantURL {
		t.Fatalf("restarted clip list = %d %+v", response.StatusCode, listed)
	}

	public := fiber.New()
	public.Get("/shared/clips/:token", restartedAPI.publicClip)
	public.Get("/shared/clips/:token/download", restartedAPI.publicDownloadClip)
	response, err = public.Test(httptest.NewRequest(http.MethodGet, "/shared/clips/"+token, nil))
	if err != nil {
		t.Fatal(err)
	}
	var publicMetadata map[string]any
	if err := json.NewDecoder(response.Body).Decode(&publicMetadata); err != nil {
		response.Body.Close()
		t.Fatal(err)
	}
	response.Body.Close()
	if _, copied := publicMetadata["shareUrl"]; copied {
		t.Fatal("public metadata exposes a copied share URL")
	}
	if _, exposed := publicMetadata["isAdmin"]; exposed {
		t.Fatal("public metadata exposes authenticated role scope")
	}
	response, err = public.Test(httptest.NewRequest(http.MethodGet, wantURL, nil))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusOK || string(body) != "persistent media" || response.Header.Get("Content-Type") != "video/mp4" {
		t.Fatalf("restarted public media = %d %q type=%q", response.StatusCode, body, response.Header.Get("Content-Type"))
	}
}

func TestCiphertextRowSwapFailsSafelyWithoutIncorrectShareURL(t *testing.T) {
	store, err := newClipStore(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	defer store.close()
	makeClip := func(owner string) *Clip {
		source := filepath.Join(t.TempDir(), owner+".mp4")
		if err := os.WriteFile(source, []byte(owner), 0600); err != nil {
			t.Fatal(err)
		}
		clip, err := store.promote(renderTestSpec(owner), source)
		if err != nil {
			t.Fatal(err)
		}
		return clip
	}
	first, second := makeClip("first"), makeClip("second")
	var firstCipher, secondCipher []byte
	if err := store.db.QueryRow("SELECT share_token_ciphertext FROM clips WHERE id = ?", first.ID).Scan(&firstCipher); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRow("SELECT share_token_ciphertext FROM clips WHERE id = ?", second.ID).Scan(&secondCipher); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec("UPDATE clips SET share_token_ciphertext = ? WHERE id = ?", secondCipher, first.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec("UPDATE clips SET share_token_ciphertext = ? WHERE id = ?", firstCipher, second.ID); err != nil {
		t.Fatal(err)
	}

	api := &API{app: &Application{clipStore: store}}
	app := fiber.New()
	app.Use(func(ctx fiber.Ctx) error {
		ctx.SetUserContext(ContextWithUser(context.Background(), User{Uuid: "first"}))
		return ctx.Next()
	})
	app.Get("/clips/:id", api.getClip)
	response, err := app.Test(httptest.NewRequest(http.MethodGet, "/clips/"+first.ID, nil))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("swapped ciphertext status = %d, want 503", response.StatusCode)
	}
	if strings.Contains(string(body), "/shared/clips/") || strings.Contains(string(body), first.ShareToken) || strings.Contains(string(body), second.ShareToken) {
		t.Fatalf("swapped ciphertext response exposed a public URL/token: %s", body)
	}
}

func TestClipListAndDeleteAuthorizationHardDeletesBytesAndMetadata(t *testing.T) {
	store, err := newClipStore(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	defer store.close()
	makeClip := func(owner string) *Clip {
		source := filepath.Join(t.TempDir(), owner+".mp4")
		if err := os.WriteFile(source, []byte(owner), 0600); err != nil {
			t.Fatal(err)
		}
		clip, err := store.promote(renderTestSpec(owner), source)
		if err != nil {
			t.Fatal(err)
		}
		return clip
	}
	owned := makeClip("creator-a")
	other := makeClip("creator-b")
	api := &API{app: &Application{clipStore: store, ownerEmail: "owner@example.test"}}
	requestApp := func(user User, handler fiber.Handler) *fiber.App {
		app := fiber.New()
		app.Use(func(ctx fiber.Ctx) error {
			ctx.SetUserContext(ContextWithUser(context.Background(), user))
			return ctx.Next()
		})
		app.Get("/clips", handler)
		app.Delete("/clips/:id", handler)
		return app
	}
	creatorApp := requestApp(User{Uuid: "creator-a", Email: "creator@example.test"}, api.listClips)
	response, err := creatorApp.Test(httptest.NewRequest(http.MethodGet, "/clips", nil))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusOK || !strings.Contains(string(body), owned.ID) || strings.Contains(string(body), other.ID) {
		t.Fatalf("creator list = %d %s", response.StatusCode, body)
	}

	adminApp := requestApp(User{Uuid: "admin", Email: "OWNER@example.test"}, api.listClips)
	response, err = adminApp.Test(httptest.NewRequest(http.MethodGet, "/clips", nil))
	if err != nil {
		t.Fatal(err)
	}
	body, _ = io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusOK || !strings.Contains(string(body), owned.ID) || !strings.Contains(string(body), other.ID) {
		t.Fatalf("admin list = %d %s", response.StatusCode, body)
	}
	var adminList clipListResponse
	if err := json.Unmarshal(body, &adminList); err != nil {
		t.Fatal(err)
	}
	if !adminList.IsAdmin || len(adminList.Clips) != 2 {
		t.Fatalf("admin capability envelope = %+v", adminList)
	}
	for _, listedClip := range adminList.Clips {
		if !listedClip.CanDelete {
			t.Fatalf("administrator cannot delete listed clip: %+v", listedClip)
		}
	}

	deleteApp := fiber.New()
	deleteApp.Use(func(ctx fiber.Ctx) error {
		ctx.SetUserContext(ContextWithUser(context.Background(), User{Uuid: "creator-a", Email: "creator@example.test"}))
		return ctx.Next()
	})
	deleteApp.Delete("/clips/:id", api.deleteClip)
	response, err = deleteApp.Test(httptest.NewRequest(http.MethodDelete, "/clips/"+other.ID, nil))
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("unauthorized delete status = %d", response.StatusCode)
	}
	response, err = deleteApp.Test(httptest.NewRequest(http.MethodDelete, "/clips/"+owned.ID, nil))
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("creator delete status = %d", response.StatusCode)
	}
	if _, err := os.Stat(owned.FilePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("deleted clip bytes still exist: %v", err)
	}
	if _, err := store.get(owned.ID); !errors.Is(err, errClipNotFound) {
		t.Fatalf("deleted clip metadata lookup = %v", err)
	}
	adminDeleteApp := fiber.New()
	adminDeleteApp.Use(func(ctx fiber.Ctx) error {
		ctx.SetUserContext(ContextWithUser(context.Background(), User{Uuid: "admin", Email: "OWNER@example.test"}))
		return ctx.Next()
	})
	adminDeleteApp.Delete("/clips/:id", api.deleteClip)
	response, err = adminDeleteApp.Test(httptest.NewRequest(http.MethodDelete, "/clips/"+other.ID, nil))
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("administrator delete status = %d", response.StatusCode)
	}
	if _, err := os.Stat(other.FilePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("administrator-deleted bytes still exist: %v", err)
	}
}

func TestClipStoreUsesConfiguredDurableRoot(t *testing.T) {
	root := filepath.Join(t.TempDir(), "durable")
	config := Config{}
	config.Storage.Root = root
	store, err := newClipStore(durableStorageRoot(config), config.Storage.Database)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "clips.sqlite3")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "clips")); err != nil {
		t.Fatal(err)
	}
	if _, err := parseClipTime(time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
}
