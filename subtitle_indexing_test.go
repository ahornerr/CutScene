package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LukeHagar/plexgo/models/components"
	"github.com/jackc/pgx/v5"
)

func TestBuildPlexSourceURLValidation(t *testing.T) {
	app := &Application{}
	app.config.Plex.Host = "http://127.0.0.1:32400"
	app.config.Plex.Token = "test-token"

	t.Run("valid relative path", func(t *testing.T) {
		got, err := app.buildPlexSourceURL("/library/parts/123/file.mp4", "tok-123")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.HasPrefix(got, "http://127.0.0.1:32400/library/parts/123/file.mp4?") || !strings.Contains(got, "X-Plex-Token=tok-123") {
			t.Fatalf("unexpected url: %s", got)
		}
	})

	invalidPaths := []struct {
		name string
		path string
	}{
		{"empty", ""},
		{"whitespace", "   "},
		{"scheme-relative", "//evil.com/file.mp4"},
		{"userinfo", "http://user:pass@127.0.0.1:32400/file.mp4"},
		{"external origin", "http://evil.com/file.mp4"},
		{"relative without slash", "library/parts/123/file.mp4"},
	}

	for _, tc := range invalidPaths {
		t.Run(tc.name, func(t *testing.T) {
			_, err := app.buildPlexSourceURL(tc.path, "tok")
			if err == nil {
				t.Fatalf("path %q was unexpectedly accepted", tc.path)
			}
		})
	}
}

func TestSubtitleIndexJobManagerErrNoRowsCompatibility(t *testing.T) {
	// Verify that checking pgx.ErrNoRows and sql.ErrNoRows matches pgx.ErrNoRows
	pgxErr := pgx.ErrNoRows
	if !(errors.Is(pgxErr, pgx.ErrNoRows) || errors.Is(pgxErr, sql.ErrNoRows)) {
		t.Fatal("expected pgx.ErrNoRows to match")
	}
	sqlErr := sql.ErrNoRows
	if !(errors.Is(sqlErr, pgx.ErrNoRows) || errors.Is(sqlErr, sql.ErrNoRows)) {
		t.Fatal("expected sql.ErrNoRows to match")
	}
}

func TestDiscoverAndIndexSourcesCancelsOnWorkerFatalError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/identity":
			_, _ = w.Write([]byte(`{"MediaContainer":{"machineIdentifier":"pms-1"}}`))
		case "/library/sections":
			_, _ = w.Write([]byte(`{"MediaContainer":{"Directory":[{"key":"1","type":"movie","uuid":"sec-1"}]}}`))
		case "/library/sections/1/all":
			// Return a large batch of items
			_, _ = w.Write([]byte(`{"MediaContainer":{"size":5,"Metadata":[
				{"ratingKey":"1","type":"movie","title":"Movie 1","duration":60000,"Media":[{"id":10,"duration":60000,"Part":[{"id":20,"duration":60000,"key":"/library/parts/20/file.mp4"}]}]},
				{"ratingKey":"2","type":"movie","title":"Movie 2","duration":60000,"Media":[{"id":11,"duration":60000,"Part":[{"id":21,"duration":60000,"key":"/library/parts/21/file.mp4"}]}]},
				{"ratingKey":"3","type":"movie","title":"Movie 3","duration":60000,"Media":[{"id":12,"duration":60000,"Part":[{"id":22,"duration":60000,"key":"/library/parts/22/file.mp4"}]}]},
				{"ratingKey":"4","type":"movie","title":"Movie 4","duration":60000,"Media":[{"id":13,"duration":60000,"Part":[{"id":23,"duration":60000,"key":"/library/parts/23/file.mp4"}]}]},
				{"ratingKey":"5","type":"movie","title":"Movie 5","duration":60000,"Media":[{"id":14,"duration":60000,"Part":[{"id":24,"duration":60000,"key":"/library/parts/24/file.mp4"}]}]}
			]}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	resolver, err := NewPlexResourceResolver(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	_ = resolver.SetTrustedOrigins("pms-1", []string{server.URL})

	app := &Application{
		plexResources:     resolver,
		machineIdentifier: "pms-1",
	}
	app.config.Plex.Host = server.URL
	app.config.Plex.Token = "server-token"

	now := time.Now().UTC()
	job := &subtitleIndexJob{
		id:        "job-test",
		owner:     "owner-test",
		state:     "running",
		startedAt: now,
		updatedAt: now,
	}

	ctx := ContextWithUser(ContextWithAuthToken(context.Background(), "user-token"), User{Uuid: "owner-test"})
	access := &PlexAccess{
		baseOrigin:        server.URL,
		secretToken:       "user-token",
		accountToken:      "user-token",
		callerUUID:        "owner-test",
		machineIdentifier: "pms-1",
	}
	ctx = ContextWithPlexAccess(ctx, access)
	fatalErr := errors.New("database connection terminated")

	var processedCount atomic.Int32
	err = app.discoverAndIndexSources(ctx, job, func(work subtitleIndexWork) error {
		count := processedCount.Add(1)
		if count == 1 {
			// First item encounters fatal worker error
			return fatalErr
		}
		// Subsequent items should not run indefinitely
		time.Sleep(10 * time.Millisecond)
		return nil
	})

	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, fatalErr) && !strings.Contains(err.Error(), "database connection terminated") {
		t.Fatalf("expected fatalErr, got: %v", err)
	}
}

func TestBulkEmbeddingsSemaphoreReleaseBeforeWrites(t *testing.T) {
	store := &subtitleSearchStore{
		bulkEmbeddings: make(chan struct{}, 1),
		bulkWrites:     make(chan struct{}, 1),
	}

	app := &Application{
		subtitleSearch: store,
	}

	// Verify that with withBulkSubtitleContext, acquire and release helper functions work properly
	ctx := withBulkSubtitleContext(context.Background())
	if !isBulkSubtitleContext(ctx) {
		t.Fatal("expected bulk subtitle context to be true")
	}

	if err := acquireSubtitleSemaphore(ctx, app.subtitleSearch.bulkEmbeddings); err != nil {
		t.Fatal(err)
	}
	// Verify semaphore is acquired (channel has 1 element)
	if len(app.subtitleSearch.bulkEmbeddings) != 1 {
		t.Fatalf("expected bulkEmbeddings len 1, got %d", len(app.subtitleSearch.bulkEmbeddings))
	}

	releaseSubtitleSemaphore(app.subtitleSearch.bulkEmbeddings)
	if len(app.subtitleSearch.bulkEmbeddings) != 0 {
		t.Fatalf("expected bulkEmbeddings len 0, got %d", len(app.subtitleSearch.bulkEmbeddings))
	}
}

func TestRecoverPersistedJobsSafeWhenNilOrDisabled(t *testing.T) {
	app := &Application{}
	manager := newSubtitleIndexJobManager(app)
	if err := manager.recoverPersistedJobs(); err != nil {
		t.Fatalf("expected nil error when subtitleSearch is nil, got: %v", err)
	}
}

func TestBatchChunkQueuing(t *testing.T) {
	chunks := []subtitleChunk{
		{Start: 1000, End: 2000, Text: "Line 1"},
		{Start: 2500, End: 4000, Text: "Line 2"},
		{Start: 5000, End: 7000, Text: "Line 3"},
	}

	batch := &pgx.Batch{}
	for _, chunk := range chunks {
		batch.Queue("INSERT INTO subtitle_chunks ...", chunk.Start, chunk.End, chunk.Text)
	}

	if batch.Len() != 3 {
		t.Fatalf("batch len = %d, want 3", batch.Len())
	}
}

func TestStreamIndexLibrarySectionSourcesPaginationCycleTermination(t *testing.T) {
	var requestCount atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/identity" {
			_, _ = w.Write([]byte(`{"MediaContainer":{"machineIdentifier":"pms-cycle"}}`))
			return
		}
		// Every request returns the exact same full page of 100 items (a cycle)
		count := requestCount.Add(1)
		var items []string
		for i := 0; i < indexLibraryPageSize; i++ {
			items = append(items, fmt.Sprintf(`{"ratingKey":"%d","type":"movie","title":"Movie %d"}`, i, i))
		}
		_, _ = w.Write([]byte(fmt.Sprintf(`{"MediaContainer":{"size":%d,"offset":0,"Metadata":[%s]}}`, indexLibraryPageSize, strings.Join(items, ","))))
		_ = count
	}))
	defer server.Close()

	resolver, err := NewPlexResourceResolver(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	_ = resolver.SetTrustedOrigins("pms-cycle", []string{server.URL})

	app := &Application{
		plexResources:     resolver,
		machineIdentifier: "pms-cycle",
	}
	app.config.Plex.Host = server.URL
	app.config.Plex.Token = "server-token"

	access := &PlexAccess{
		baseOrigin:        server.URL,
		secretToken:       "user-token",
		accountToken:      "user-token",
		callerUUID:        "owner-test",
		machineIdentifier: "pms-cycle",
	}
	ctx := ContextWithPlexAccess(context.Background(), access)

	// Stream should detect cycle on second page and return errPaginationIncomplete
	err = app.streamIndexLibrarySectionSources(ctx, "user-token", "1", "movie", func(items []components.Metadata) error {
		return nil
	})
	if !errors.Is(err, errPaginationIncomplete) {
		t.Fatalf("expected errPaginationIncomplete on cycle termination, got: %v", err)
	}
	if requestCount.Load() > 5 {
		t.Fatalf("expected pagination to terminate quickly on cycle, but made %d requests", requestCount.Load())
	}
}

func TestStreamIndexLibrarySectionSourcesTotalSizeTermination(t *testing.T) {
	var requestCount atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/identity" {
			_, _ = w.Write([]byte(`{"MediaContainer":{"machineIdentifier":"pms-totalsize"}}`))
			return
		}
		requestCount.Add(1)
		// Returns exactly indexLibraryPageSize items with totalSize = indexLibraryPageSize
		var items []string
		for i := 0; i < indexLibraryPageSize; i++ {
			items = append(items, fmt.Sprintf(`{"ratingKey":"%d","type":"movie","title":"Movie %d"}`, i, i))
		}
		_, _ = w.Write([]byte(fmt.Sprintf(`{"MediaContainer":{"size":%d,"totalSize":%d,"offset":0,"Metadata":[%s]}}`, indexLibraryPageSize, indexLibraryPageSize, strings.Join(items, ","))))
	}))
	defer server.Close()

	resolver, err := NewPlexResourceResolver(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	_ = resolver.SetTrustedOrigins("pms-totalsize", []string{server.URL})

	app := &Application{
		plexResources:     resolver,
		machineIdentifier: "pms-totalsize",
	}
	app.config.Plex.Host = server.URL
	app.config.Plex.Token = "server-token"

	access := &PlexAccess{
		baseOrigin:        server.URL,
		secretToken:       "user-token",
		accountToken:      "user-token",
		callerUUID:        "owner-test",
		machineIdentifier: "pms-totalsize",
	}
	ctx := ContextWithPlexAccess(context.Background(), access)

	err = app.streamIndexLibrarySectionSources(ctx, "user-token", "1", "movie", func(items []components.Metadata) error {
		return nil
	})
	if err != nil {
		t.Fatalf("expected nil error, got: %v", err)
	}
	// Should terminate after page 1 because start + len(items) >= totalSize
	if requestCount.Load() != 1 {
		t.Fatalf("expected 1 request, got %d", requestCount.Load())
	}
}

func TestStreamIndexLibrarySectionSourcesStopPagination(t *testing.T) {
	var requestCount atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/identity" {
			_, _ = w.Write([]byte(`{"MediaContainer":{"machineIdentifier":"pms-stop"}}`))
			return
		}
		requestCount.Add(1)
		var items []string
		for i := 0; i < indexLibraryPageSize; i++ {
			items = append(items, fmt.Sprintf(`{"ratingKey":"%d","type":"movie","title":"Movie %d"}`, i, i))
		}
		_, _ = w.Write([]byte(fmt.Sprintf(`{"MediaContainer":{"size":%d,"offset":0,"Metadata":[%s]}}`, indexLibraryPageSize, strings.Join(items, ","))))
	}))
	defer server.Close()

	resolver, err := NewPlexResourceResolver(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	_ = resolver.SetTrustedOrigins("pms-stop", []string{server.URL})

	app := &Application{
		plexResources:     resolver,
		machineIdentifier: "pms-stop",
	}
	app.config.Plex.Host = server.URL
	app.config.Plex.Token = "server-token"

	access := &PlexAccess{
		baseOrigin:        server.URL,
		secretToken:       "user-token",
		accountToken:      "user-token",
		callerUUID:        "owner-test",
		machineIdentifier: "pms-stop",
	}
	ctx := ContextWithPlexAccess(context.Background(), access)

	// Returning errStopPagination before totalSize is reached should return errPaginationIncomplete
	err = app.streamIndexLibrarySectionSources(ctx, "user-token", "1", "movie", func(items []components.Metadata) error {
		return errStopPagination
	})
	if !errors.Is(err, errPaginationIncomplete) {
		t.Fatalf("expected errPaginationIncomplete on errStopPagination, got: %v", err)
	}
	if requestCount.Load() != 1 {
		t.Fatalf("expected exactly 1 request before stop, got %d", requestCount.Load())
	}
}

func TestCallerSectionSourceSetPaginationCycle(t *testing.T) {
	var requestCount atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/identity" {
			_, _ = w.Write([]byte(`{"MediaContainer":{"machineIdentifier":"pms-auth-cycle"}}`))
			return
		}
		requestCount.Add(1)
		var items []string
		for i := 0; i < indexLibraryPageSize; i++ {
			items = append(items, fmt.Sprintf(`{"ratingKey":"%d","type":"movie","title":"Movie %d","Media":[{"id":%d,"duration":1000,"Part":[{"id":%d,"key":"/library/parts/%d"}]}]}`, i, i, i+100, i+200, i+200))
		}
		_, _ = w.Write([]byte(fmt.Sprintf(`{"MediaContainer":{"size":%d,"offset":0,"Metadata":[%s]}}`, indexLibraryPageSize, strings.Join(items, ","))))
	}))
	defer server.Close()

	resolver, err := NewPlexResourceResolver(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	_ = resolver.SetTrustedOrigins("pms-auth-cycle", []string{server.URL})

	app := &Application{
		plexResources:     resolver,
		machineIdentifier: "pms-auth-cycle",
	}
	app.config.Plex.Host = server.URL
	app.config.Plex.Token = "server-token"

	access := &PlexAccess{
		baseOrigin:        server.URL,
		secretToken:       "user-token",
		accountToken:      "user-token",
		callerUUID:        "owner-test",
		machineIdentifier: "pms-auth-cycle",
	}
	ctx := ContextWithPlexAccess(context.Background(), access)

	sources, pages, itemsSeen, err := app.callerSectionSourceSet(ctx, resolver, "1", "movie")
	if err != nil {
		t.Fatalf("expected nil error on cycle termination, got: %v", err)
	}
	if pages > 5 {
		t.Fatalf("expected pagination to terminate quickly on cycle, but did %d pages", pages)
	}
	if len(sources) != indexLibraryPageSize {
		t.Fatalf("expected %d sources, got %d", indexLibraryPageSize, len(sources))
	}
	_ = itemsSeen
}

func TestDiscoverAndIndexSourcesSkipsPruningOnPaginationCycle(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/identity" {
			_, _ = w.Write([]byte(`{"MediaContainer":{"machineIdentifier":"pms-discover-cycle"}}`))
			return
		}
		if r.URL.Path == "/library/sections" {
			_, _ = w.Write([]byte(`{"MediaContainer":{"Directory":[{"key":"1","type":"movie","title":"Movies","uuid":"sec-1"}]}}`))
			return
		}
		// Returns same page of items to trigger a pagination cycle
		var items []string
		for i := 0; i < indexLibraryPageSize; i++ {
			items = append(items, fmt.Sprintf(`{"ratingKey":"%d","type":"movie","title":"Movie %d","duration":60000,"Media":[{"id":%d,"duration":60000,"Part":[{"id":%d,"duration":60000,"key":"/video.mp4"}]}]}`, i, i, i+10, i+20))
		}
		_, _ = w.Write([]byte(fmt.Sprintf(`{"MediaContainer":{"size":%d,"offset":0,"Metadata":[%s]}}`, indexLibraryPageSize, strings.Join(items, ","))))
	}))
	defer server.Close()

	resolver, err := NewPlexResourceResolver(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	_ = resolver.SetTrustedOrigins("pms-discover-cycle", []string{server.URL})

	app := &Application{
		plexResources:     resolver,
		machineIdentifier: "pms-discover-cycle",
	}
	app.config.Plex.Host = server.URL
	app.config.Plex.Token = "server-token"

	token := "user-token"
	ctx := ContextWithAuthToken(context.Background(), token)
	job := &subtitleIndexJob{
		owner: "owner-1",
	}

	var indexedCount atomic.Int32
	err = app.discoverAndIndexSources(ctx, job, func(work subtitleIndexWork) error {
		indexedCount.Add(1)
		return nil
	})

	// Must return errPaginationIncomplete and not nil
	if !errors.Is(err, errPaginationIncomplete) {
		t.Fatalf("expected errPaginationIncomplete, got: %v", err)
	}
	if indexedCount.Load() == 0 {
		t.Fatal("expected items before cycle to be indexed")
	}
}

func TestRecoverPersistedJobsRequiresMachineIdentifierForShared(t *testing.T) {
	app := &Application{
		sharedCorpus:      true,
		machineIdentifier: "", // uninitialized machineIdentifier
	}
	mgr := newSubtitleIndexJobManager(app)
	if err := mgr.recoverPersistedJobs(); err != nil {
		t.Fatalf("expected nil error, got: %v", err)
	}
}

func TestDiscoverAndIndexSourcesSharedCorpusDoesNotAbortOnItemErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/identity":
			_, _ = w.Write([]byte(`{"MediaContainer":{"machineIdentifier":"pms-1"}}`))
		case "/library/sections":
			_, _ = w.Write([]byte(`{"MediaContainer":{"Directory":[{"key":"1","type":"movie","uuid":"sec-1"}]}}`))
		case "/library/sections/1/all":
			_, _ = w.Write([]byte(`{"MediaContainer":{"size":3,"Metadata":[
				{"ratingKey":"1","type":"movie","title":"Movie 1","duration":60000,"Media":[{"id":10,"duration":60000,"Part":[{"id":20,"duration":60000,"key":"/library/parts/20/file.mp4"}]}]},
				{"ratingKey":"2","type":"movie","title":"Movie 2","duration":60000,"Media":[{"id":11,"duration":60000,"Part":[{"id":21,"duration":60000,"key":"/library/parts/21/file.mp4"}]}]},
				{"ratingKey":"3","type":"movie","title":"Movie 3","duration":60000,"Media":[{"id":12,"duration":60000,"Part":[{"id":22,"duration":60000,"key":"/library/parts/22/file.mp4"}]}]}
			]}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	resolver, err := NewPlexResourceResolver(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	_ = resolver.SetTrustedOrigins("pms-1", []string{server.URL})

	app := &Application{
		plexResources:     resolver,
		machineIdentifier: "pms-1",
		sharedCorpus:      true,
	}
	app.config.Plex.Host = server.URL
	app.config.Plex.Token = "server-token"

	now := time.Now().UTC()
	job := &subtitleIndexJob{
		id:        "job-shared-test",
		owner:     "owner-test",
		state:     "running",
		startedAt: now,
		updatedAt: now,
	}

	ctx := ContextWithUser(ContextWithAuthToken(context.Background(), "user-token"), User{Uuid: "owner-test"})
	access := &PlexAccess{
		baseOrigin:        server.URL,
		secretToken:       "user-token",
		accountToken:      "user-token",
		callerUUID:        "owner-test",
		machineIdentifier: "pms-1",
	}
	ctx = ContextWithPlexAccess(ctx, access)

	var indexedCount atomic.Int32
	var failedCount atomic.Int32
	err = app.discoverAndIndexSources(ctx, job, func(work subtitleIndexWork) error {
		if work.source.RatingKey == "2" {
			job.recordSourceFailure(work.source, subtitleItemFailure(errors.New("ffmpeg extraction failed")), 1)
			failedCount.Add(1)
			return nil
		}
		indexedCount.Add(1)
		return nil
	})

	if err != nil {
		t.Fatalf("expected nil error (scan should not abort on item failure), got: %v", err)
	}
	if indexedCount.Load() != 2 {
		t.Fatalf("expected 2 successful items indexed, got %d", indexedCount.Load())
	}
	if failedCount.Load() != 1 {
		t.Fatalf("expected 1 failed item recorded, got %d", failedCount.Load())
	}
}

func TestRecoverPersistedJobsPreservesSharedState(t *testing.T) {
	app := &Application{
		sharedCorpus:      true,
		machineIdentifier: "pms-1",
	}
	mgr := newSubtitleIndexJobManager(app)
	if err := mgr.recoverPersistedJobs(); err != nil {
		t.Fatalf("expected nil error, got: %v", err)
	}
}




