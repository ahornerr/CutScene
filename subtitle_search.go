package main

import (
	"context"
	"errors"
	"fmt"
	"html"
	"log"
	"net/url"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	pgxvector "github.com/pgvector/pgvector-go/pgx"
)

// Package-level subtitle semantic search support.
//
// This file holds the shared foundations: tuning constants, the semaphore and
// context helpers used by bulk work, the pgvector-backed store and its schema
// migrations, subtitle chunking, and the Plex source URL builder used by both
// indexing and preview.
//
// The embedding client, the indexing pipeline, and the query path live in
// subtitle_search_embeddings.go, subtitle_search_index.go, and
// subtitle_search_query.go respectively.
const (
	defaultSubtitleEmbeddingDimensions     = 768
	subtitleEmbeddingDimensions            = defaultSubtitleEmbeddingDimensions
	minSubtitleEmbeddingDimensions         = 1
	maxSubtitleEmbeddingDimensions         = 2000
	defaultSubtitleEmbeddingBatchSize      = 32
	minSubtitleEmbeddingBatchSize          = 1
	maxSubtitleEmbeddingBatchSize          = 512
	defaultSubtitleEmbeddingConcurrency    = 2
	minSubtitleEmbeddingConcurrency        = 1
	maxSubtitleEmbeddingConcurrency        = 32
	subtitleEmbeddingBatchSize             = defaultSubtitleEmbeddingBatchSize
	maxSharedSubtitleCandidates            = 200
	maxSharedSubtitleSourceChecks          = 100
	maxSharedSubtitleAuthorizedHits        = 50
	sharedSubtitleAuthorizationTimeout     = 10 * time.Second
	maxSharedSubtitleAuthorizationSections = 32
	maxSharedSubtitleAuthorizationPages    = 100
	maxSharedSubtitleAuthorizationItems    = 10000
	maxSubtitleLexicalCandidates           = 200
	maxSubtitleSemanticCandidates          = 200
	// Keep result windows focused enough to open directly as short clips.
	// Subtitle timing still determines the exact duration.
	subtitleChunkMaxRunes            = 400
	subtitleChunkMaxGapMs      int64 = 15 * 1000
	subtitleChunkMaxDurationMs int64 = 60 * 1000
	bgeQueryInstruction              = "Represent this sentence for searching relevant passages: "
)

var errSubtitleSearchDisabled = errors.New("semantic subtitle search is disabled")

var errSubtitleIndexItem = errors.New("subtitle index item failure")

type subtitleIndexItemFailure struct{ err error }

func (e *subtitleIndexItemFailure) Error() string { return e.err.Error() }
func (e *subtitleIndexItemFailure) Unwrap() error { return e.err }
func (e *subtitleIndexItemFailure) Is(target error) bool {
	return target == errSubtitleIndexItem || errors.Is(e.err, target)
}

func subtitleItemFailure(err error) error {
	if err == nil {
		return nil
	}
	return &subtitleIndexItemFailure{err: err}
}

type subtitleBulkContextKey struct{}

func withBulkSubtitleContext(ctx context.Context) context.Context {
	return context.WithValue(ctx, subtitleBulkContextKey{}, true)
}

func isBulkSubtitleContext(ctx context.Context) bool {
	value, _ := ctx.Value(subtitleBulkContextKey{}).(bool)
	return value
}

func acquireSubtitleSemaphore(ctx context.Context, semaphore chan struct{}) error {
	select {
	case semaphore <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func releaseSubtitleSemaphore(semaphore chan struct{}) {
	<-semaphore
}

// subtitleSearchStore owns the optional POC services. It is deliberately not
// constructed when semantic_search.enabled is false, so existing deployments
// do not need PostgreSQL or TEI.
type subtitleSearchStore struct {
	pool             *pgxpool.Pool
	embeddings       *teiClient
	dimensions       int
	batchSize        int
	concurrency      int
	queryInstruction string
	bulkEmbeddings   chan struct{}
	bulkWrites       chan struct{}
	trackLocks       keyedSubtitleLocks
	jobMu            sync.Mutex
}

type keyedSubtitleLocks struct {
	mu      sync.Mutex
	entries map[string]*keyedSubtitleLock
}

type keyedSubtitleLock struct {
	mu   sync.Mutex
	refs int
}

func (l *keyedSubtitleLocks) acquire(key string) func() {
	l.mu.Lock()
	if l.entries == nil {
		l.entries = make(map[string]*keyedSubtitleLock)
	}
	entry := l.entries[key]
	if entry == nil {
		entry = &keyedSubtitleLock{}
		l.entries[key] = entry
	}
	entry.refs++
	l.mu.Unlock()
	entry.mu.Lock()
	return func() {
		entry.mu.Unlock()
		l.mu.Lock()
		entry.refs--
		if entry.refs == 0 {
			delete(l.entries, key)
		}
		l.mu.Unlock()
	}
}

const (
	embeddingsProviderTEI    = "tei"
	embeddingsProviderOpenAI = "openai"
	openAIEmbeddingModel     = "BAAI/bge-base-en-v1.5"
)

const subtitleSearchIndexMigrationLockKey int64 = 748238519

type subtitleSearchIndexMigrationStatement struct {
	name string
	sql  string
}

var subtitleSearchIndexMigrationStatements = []subtitleSearchIndexMigrationStatement{
	{sql: "CREATE EXTENSION IF NOT EXISTS pg_trgm"},
	{name: "subtitle_chunks_owner_idx", sql: "CREATE INDEX CONCURRENTLY IF NOT EXISTS subtitle_chunks_owner_idx ON subtitle_chunks (owner_uuid)"},
	{name: "subtitle_chunks_normalized_text_trgm_idx", sql: "CREATE INDEX CONCURRENTLY IF NOT EXISTS subtitle_chunks_normalized_text_trgm_idx ON subtitle_chunks USING GIN (btrim(regexp_replace(lower(text), '[^[:alnum:]]+', ' ', 'g')) gin_trgm_ops)"},
	{name: "subtitle_shared_chunks_machine_section_scan_idx", sql: "CREATE INDEX CONCURRENTLY IF NOT EXISTS subtitle_shared_chunks_machine_section_scan_idx ON subtitle_shared_chunks (machine_identifier, section_uuid, scan_id)"},
	{name: "subtitle_shared_chunks_normalized_text_trgm_idx", sql: "CREATE INDEX CONCURRENTLY IF NOT EXISTS subtitle_shared_chunks_normalized_text_trgm_idx ON subtitle_shared_chunks USING GIN (btrim(regexp_replace(lower(text), '[^[:alnum:]]+', ' ', 'g')) gin_trgm_ops)"},
}

const subtitleSearchInvalidIndexQuery = `
SELECT EXISTS (
    SELECT 1
    FROM pg_index
    WHERE indexrelid = to_regclass($1)
      AND NOT indisvalid
)
`

func subtitleSearchDropInvalidIndexSQL(name string) string {
	return "DROP INDEX CONCURRENTLY IF EXISTS " + name
}

func subtitleSearchSchema(dimensions int) string {
	if dimensions <= 0 {
		dimensions = defaultSubtitleEmbeddingDimensions
	}
	return fmt.Sprintf(`
CREATE EXTENSION IF NOT EXISTS vector;
CREATE TABLE IF NOT EXISTS subtitle_chunks (
    id BIGSERIAL PRIMARY KEY,
    owner_uuid TEXT NOT NULL,
    rating_key TEXT NOT NULL,
    media_id BIGINT NOT NULL,
    part_id BIGINT NOT NULL,
    subtitle_index INTEGER NOT NULL,
    title TEXT NOT NULL,
    show_title TEXT,
    season INTEGER,
    episode INTEGER,
    year INTEGER,
    start_ms BIGINT NOT NULL,
    end_ms BIGINT NOT NULL,
    text TEXT NOT NULL,
    content_hash TEXT NOT NULL,
    embedding vector(%d) NOT NULL,
    text_search TSVECTOR GENERATED ALWAYS AS (to_tsvector('simple', text)) STORED,
    UNIQUE (owner_uuid, rating_key, media_id, part_id, subtitle_index, start_ms, end_ms, content_hash)
);
CREATE INDEX IF NOT EXISTS subtitle_chunks_text_search_idx ON subtitle_chunks USING GIN (text_search);
CREATE INDEX IF NOT EXISTS subtitle_chunks_embedding_hnsw_idx ON subtitle_chunks USING hnsw (embedding vector_cosine_ops);
CREATE TABLE IF NOT EXISTS subtitle_index_jobs (
    id UUID PRIMARY KEY,
    owner_uuid TEXT NOT NULL,
    state TEXT NOT NULL,
    discovered INTEGER NOT NULL DEFAULT 0,
    processed INTEGER NOT NULL DEFAULT 0,
    indexed_chunks INTEGER NOT NULL DEFAULT 0,
    skipped INTEGER NOT NULL DEFAULT 0,
    failed INTEGER NOT NULL DEFAULT 0,
    unchanged_tracks INTEGER NOT NULL DEFAULT 0,
    unsupported_tracks INTEGER NOT NULL DEFAULT 0,
    empty_tracks INTEGER NOT NULL DEFAULT 0,
    failed_tracks INTEGER NOT NULL DEFAULT 0,
    current_title TEXT,
    started_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    finished_at TIMESTAMPTZ,
    error TEXT
);
ALTER TABLE subtitle_index_jobs ADD COLUMN IF NOT EXISTS unchanged_tracks INTEGER NOT NULL DEFAULT 0;
ALTER TABLE subtitle_index_jobs ADD COLUMN IF NOT EXISTS unsupported_tracks INTEGER NOT NULL DEFAULT 0;
ALTER TABLE subtitle_index_jobs ADD COLUMN IF NOT EXISTS empty_tracks INTEGER NOT NULL DEFAULT 0;
ALTER TABLE subtitle_index_jobs ADD COLUMN IF NOT EXISTS failed_tracks INTEGER NOT NULL DEFAULT 0;
CREATE INDEX IF NOT EXISTS subtitle_index_jobs_owner_idx ON subtitle_index_jobs (owner_uuid, updated_at DESC);
CREATE TABLE IF NOT EXISTS subtitle_index_sources (
    owner_uuid TEXT NOT NULL,
    rating_key TEXT NOT NULL,
    media_id BIGINT NOT NULL,
    part_id BIGINT NOT NULL,
    subtitle_index INTEGER NOT NULL,
    fingerprint TEXT NOT NULL,
    chunk_count INTEGER NOT NULL DEFAULT 0,
    scan_id UUID,
    indexed_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (owner_uuid, rating_key, media_id, part_id, subtitle_index)
);
ALTER TABLE subtitle_index_sources ADD COLUMN IF NOT EXISTS scan_id UUID;
CREATE INDEX IF NOT EXISTS subtitle_index_sources_owner_scan_idx ON subtitle_index_sources (owner_uuid, scan_id);
CREATE TABLE IF NOT EXISTS subtitle_shared_sections (
    machine_identifier TEXT NOT NULL,
    section_uuid TEXT NOT NULL,
    section_key TEXT NOT NULL,
    section_type TEXT NOT NULL,
    state TEXT NOT NULL,
    scan_id UUID NOT NULL,
    ready_scan_id UUID,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    ready_at TIMESTAMPTZ,
    ready BOOLEAN NOT NULL DEFAULT FALSE,
    PRIMARY KEY (machine_identifier, section_uuid)
);
CREATE TABLE IF NOT EXISTS subtitle_shared_sources (
    machine_identifier TEXT NOT NULL,
    section_uuid TEXT NOT NULL,
    section_key TEXT NOT NULL,
    rating_key TEXT NOT NULL,
    media_id BIGINT NOT NULL,
    part_id BIGINT NOT NULL,
    subtitle_index INTEGER NOT NULL,
    fingerprint TEXT NOT NULL,
    chunk_count INTEGER NOT NULL DEFAULT 0,
    scan_id UUID NOT NULL,
    indexed_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (machine_identifier, section_uuid, scan_id, rating_key, media_id, part_id, subtitle_index)
);
CREATE TABLE IF NOT EXISTS subtitle_shared_chunks (
    id BIGSERIAL PRIMARY KEY,
    machine_identifier TEXT NOT NULL,
    section_uuid TEXT NOT NULL,
    section_key TEXT NOT NULL,
    rating_key TEXT NOT NULL,
    media_id BIGINT NOT NULL,
    part_id BIGINT NOT NULL,
    subtitle_index INTEGER NOT NULL,
    title TEXT NOT NULL,
    show_title TEXT,
    season INTEGER,
    episode INTEGER,
    year INTEGER,
    start_ms BIGINT NOT NULL,
    end_ms BIGINT NOT NULL,
    text TEXT NOT NULL,
    content_hash TEXT NOT NULL,
    embedding vector(%d) NOT NULL,
    scan_id UUID NOT NULL,
    text_search TSVECTOR GENERATED ALWAYS AS (to_tsvector('simple', text)) STORED,
    UNIQUE (machine_identifier, section_uuid, scan_id, rating_key, media_id, part_id, subtitle_index, start_ms, end_ms, content_hash)
);
CREATE INDEX IF NOT EXISTS subtitle_shared_chunks_text_idx ON subtitle_shared_chunks USING GIN (text_search);
CREATE INDEX IF NOT EXISTS subtitle_shared_chunks_embedding_idx ON subtitle_shared_chunks USING hnsw (embedding vector_cosine_ops);
ALTER TABLE subtitle_shared_sections ADD COLUMN IF NOT EXISTS ready BOOLEAN NOT NULL DEFAULT FALSE;
`, dimensions, dimensions)
}

func migrateSubtitleSearchIndexes(ctx context.Context, pool *pgxpool.Pool) error {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("could not acquire subtitle search index migration connection: %w", err)
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock($1)", subtitleSearchIndexMigrationLockKey); err != nil {
		return fmt.Errorf("could not acquire subtitle search index migration lock: %w", err)
	}
	defer func() {
		if _, err := conn.Exec(context.Background(), "SELECT pg_advisory_unlock($1)", subtitleSearchIndexMigrationLockKey); err != nil {
			log.Printf("subtitle search index migration lock release failed: %s", redactedDiagnostic(err))
		}
	}()

	for _, statement := range subtitleSearchIndexMigrationStatements {
		if statement.name != "" {
			var invalid bool
			if err := conn.QueryRow(ctx, subtitleSearchInvalidIndexQuery, statement.name).Scan(&invalid); err != nil {
				return fmt.Errorf("could not inspect subtitle search index %s: %w", statement.name, err)
			}
			if invalid {
				if _, err := conn.Exec(ctx, subtitleSearchDropInvalidIndexSQL(statement.name)); err != nil {
					return fmt.Errorf("could not drop invalid subtitle search index %s: %w", statement.name, err)
				}
			}
		}
		if _, err := conn.Exec(ctx, statement.sql); err != nil {
			return fmt.Errorf("could not create subtitle search index: %w", err)
		}
	}
	return nil
}

func migrateSubtitleEmbeddingDimensions(ctx context.Context, pool *pgxpool.Pool, targetDimensions int) error {
	var currentDim int32 = -1
	_ = pool.QueryRow(ctx, `
		SELECT COALESCE((
			SELECT atttypmod
			FROM pg_attribute
			WHERE attrelid = to_regclass('subtitle_chunks')
			  AND attname = 'embedding'
			  AND NOT attisdropped
		), -1)
	`).Scan(&currentDim)

	if currentDim > 0 && int(currentDim) != targetDimensions {
		log.Printf("semantic search embedding dimension changed from %d to %d; migrating subtitle_chunks schema", currentDim, targetDimensions)
		migrationSQL := fmt.Sprintf(`
			DROP INDEX IF EXISTS subtitle_chunks_embedding_hnsw_idx;
			TRUNCATE TABLE subtitle_chunks;
			TRUNCATE TABLE subtitle_index_sources;
			ALTER TABLE subtitle_chunks ALTER COLUMN embedding TYPE vector(%d);
			CREATE INDEX IF NOT EXISTS subtitle_chunks_embedding_hnsw_idx ON subtitle_chunks USING hnsw (embedding vector_cosine_ops);
		`, targetDimensions)
		if _, err := pool.Exec(ctx, migrationSQL); err != nil {
			return fmt.Errorf("could not migrate subtitle_chunks embedding dimensions: %w", err)
		}
	}

	var sharedDim int32 = -1
	_ = pool.QueryRow(ctx, `
		SELECT COALESCE((
			SELECT atttypmod
			FROM pg_attribute
			WHERE attrelid = to_regclass('subtitle_shared_chunks')
			  AND attname = 'embedding'
			  AND NOT attisdropped
		), -1)
	`).Scan(&sharedDim)

	if sharedDim > 0 && int(sharedDim) != targetDimensions {
		log.Printf("semantic search embedding dimension changed from %d to %d; migrating subtitle_shared_chunks schema", sharedDim, targetDimensions)
		sharedMigrationSQL := fmt.Sprintf(`
			DROP INDEX IF EXISTS subtitle_shared_chunks_embedding_idx;
			TRUNCATE TABLE subtitle_shared_chunks;
			TRUNCATE TABLE subtitle_shared_sources;
			UPDATE subtitle_shared_sections SET state='building', ready=FALSE, ready_scan_id=NULL;
			ALTER TABLE subtitle_shared_chunks ALTER COLUMN embedding TYPE vector(%d);
			CREATE INDEX IF NOT EXISTS subtitle_shared_chunks_embedding_idx ON subtitle_shared_chunks USING hnsw (embedding vector_cosine_ops);
		`, targetDimensions)
		if _, err := pool.Exec(ctx, sharedMigrationSQL); err != nil {
			return fmt.Errorf("could not migrate subtitle_shared_chunks embedding dimensions: %w", err)
		}
	}

	return nil
}

const subtitleSearchIndexVersion = "subtitle-index-v5-chunk-400-gap-15s-cast-clean-text"

func newSubtitleSearchStore(ctx context.Context, cfg SemanticSearchConfig) (*subtitleSearchStore, error) {
	provider, err := normalizeEmbeddingsProvider(cfg.EmbeddingsProvider)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(cfg.PostgresDSN) == "" {
		return nil, errors.New("semantic_search.postgres_dsn is required when semantic search is enabled")
	}
	embeddings, err := newEmbeddingClient(cfg.EmbeddingsURL, provider, cfg.EmbeddingsAPIKey, cfg.EmbeddingsModel, cfg.Dimensions)
	if err != nil {
		return nil, err
	}
	poolConfig, err := pgxpool.ParseConfig(cfg.PostgresDSN)
	if err != nil {
		return nil, errors.New("semantic_search.postgres_dsn is invalid")
	}
	poolConfig.MaxConns = 4
	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		return nil, errors.New("could not initialize semantic search database")
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, errors.New("could not connect to semantic search database")
	}

	dimensions := cfg.Dimensions
	if dimensions > 0 {
		if dimensions < minSubtitleEmbeddingDimensions || dimensions > maxSubtitleEmbeddingDimensions {
			pool.Close()
			return nil, fmt.Errorf("semantic_search.dimensions must be between %d and %d", minSubtitleEmbeddingDimensions, maxSubtitleEmbeddingDimensions)
		}
	} else {
		probedDim, probeErr := embeddings.probeDimensions(ctx)
		if probeErr == nil && probedDim > 0 {
			if probedDim < minSubtitleEmbeddingDimensions || probedDim > maxSubtitleEmbeddingDimensions {
				pool.Close()
				return nil, fmt.Errorf("probed embedding dimension %d exceeds supported range (%d-%d)", probedDim, minSubtitleEmbeddingDimensions, maxSubtitleEmbeddingDimensions)
			}
			dimensions = probedDim
			log.Printf("semantic search auto-detected embedding dimension %d from %s", dimensions, cfg.EmbeddingsURL)
		} else {
			var existingDim int32 = -1
			_ = pool.QueryRow(ctx, `
				SELECT COALESCE((
					SELECT atttypmod
					FROM pg_attribute
					WHERE attrelid = to_regclass('subtitle_chunks')
					  AND attname = 'embedding'
					  AND NOT attisdropped
				), -1)
			`).Scan(&existingDim)
			if existingDim > 0 {
				dimensions = int(existingDim)
				log.Printf("semantic search embedding probe unavailable (%v); using existing database dimension %d", probeErr, dimensions)
			} else {
				dimensions = defaultSubtitleEmbeddingDimensions
				log.Printf("semantic search embedding probe unavailable (%v); falling back to default dimension %d", probeErr, dimensions)
			}
		}
	}
	embeddings.dimensions = dimensions

	// Ensure vector extension is enabled before checking attributes
	if _, err := pool.Exec(ctx, "CREATE EXTENSION IF NOT EXISTS vector;"); err != nil {
		pool.Close()
		return nil, errors.New("could not initialize vector extension")
	}

	if err := migrateSubtitleEmbeddingDimensions(ctx, pool, dimensions); err != nil {
		pool.Close()
		return nil, err
	}

	if _, err := pool.Exec(ctx, subtitleSearchSchema(dimensions)); err != nil {
		pool.Close()
		return nil, errors.New("could not initialize semantic search schema")
	}
	pool.Close()
	poolConfig.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
		return pgxvector.RegisterTypes(ctx, conn)
	}
	pool, err = pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		return nil, errors.New("could not initialize semantic search database")
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, errors.New("could not connect to semantic search database")
	}
	batchSize := normalizeSubtitleEmbeddingBatchSize(cfg.BatchSize)
	concurrency := normalizeSubtitleEmbeddingConcurrency(cfg.Concurrency)
	queryInstruction := resolveQueryInstruction(cfg, dimensions)

	return &subtitleSearchStore{
		pool:             pool,
		embeddings:       embeddings,
		dimensions:       dimensions,
		batchSize:        batchSize,
		concurrency:      concurrency,
		queryInstruction: queryInstruction,
		bulkEmbeddings:   make(chan struct{}, concurrency),
		bulkWrites:       make(chan struct{}, 1),
	}, nil
}

func (s *subtitleSearchStore) close() {
	if s != nil && s.pool != nil {
		s.pool.Close()
	}
}

type subtitleChunk struct {
	Start int64
	End   int64
	Text  string
}

// normalizeSubtitleSearchText is intentionally narrower than a general HTML
// parser: subtitle markup is presentation-only. It removes HTML/SSA tags,
// decodes entities, and keeps meaningful line boundaries without allowing raw
// markup into embeddings or PostgreSQL full-text search.
func normalizeSubtitleSearchText(value string) string {
	value = strings.ReplaceAll(value, "\\N", "\n")
	value = strings.ReplaceAll(value, "\\n", "\n")
	var out strings.Builder
	inTag, inBrace := false, false
	for _, r := range value {
		switch {
		case r == '<':
			inTag = true
		case r == '>' && inTag:
			inTag = false
		case r == '{':
			inBrace = true
		case r == '}' && inBrace:
			inBrace = false
		case !inTag && !inBrace:
			out.WriteRune(r)
		}
	}
	value = html.UnescapeString(out.String())
	lines := strings.Split(value, "\n")
	for i := range lines {
		lines[i] = strings.Join(strings.Fields(lines[i]), " ")
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

func chunkSubtitleEntries(entries []SubtitleEntry) []subtitleChunk {
	chunks := make([]subtitleChunk, 0)
	var current subtitleChunk
	currentRunes := 0
	flush := func() {
		if strings.TrimSpace(current.Text) != "" {
			chunks = append(chunks, current)
		}
		current = subtitleChunk{}
		currentRunes = 0
	}
	for _, entry := range entries {
		text := normalizeSubtitleSearchText(entry.Text)
		if text == "" || entry.End < entry.Start {
			continue
		}
		entryRunes := utf8.RuneCountInString(text)
		if currentRunes > 0 {
			isRuneLimit := currentRunes+1+entryRunes > subtitleChunkMaxRunes
			isGapLimit := entry.Start > current.End && (entry.Start-current.End) > subtitleChunkMaxGapMs
			candidateEnd := current.End
			if entry.End > candidateEnd {
				candidateEnd = entry.End
			}
			isDurationLimit := (candidateEnd - current.Start) > subtitleChunkMaxDurationMs
			isOutOfOrder := entry.Start < current.Start
			if isRuneLimit || isGapLimit || isDurationLimit || isOutOfOrder {
				flush()
			}
		}
		if currentRunes == 0 {
			current.Start = entry.Start
			current.End = entry.End
		} else if entry.End > current.End {
			current.End = entry.End
		}
		if current.Text != "" {
			current.Text += "\n"
		}
		current.Text += text
		currentRunes += entryRunes + 1
		// A single pathological cue is split without inventing timestamps.
		for utf8.RuneCountInString(current.Text) > subtitleChunkMaxRunes {
			runes := []rune(current.Text)
			cut := subtitleChunkMaxRunes
			part := strings.TrimSpace(string(runes[:cut]))
			if part != "" {
				chunks = append(chunks, subtitleChunk{Start: current.Start, End: current.End, Text: part})
			}
			current.Text = strings.TrimSpace(string(runes[cut:]))
			current.Start = entry.Start
			currentRunes = utf8.RuneCountInString(current.Text)
		}
	}
	flush()
	return chunks
}

func (a *Application) buildPlexSourceURL(path, token string) (string, error) {
	base, err := url.Parse(a.config.Plex.Host)
	if err != nil || (base.Scheme != "http" && base.Scheme != "https") || base.Host == "" || base.User != nil {
		return "", errors.New("configured Plex origin is invalid")
	}
	if strings.TrimSpace(path) == "" {
		return "", errors.New("Plex source path is required")
	}
	if strings.HasPrefix(path, "//") {
		return "", errors.New("scheme-relative Plex source path is not allowed")
	}
	parsed, err := url.Parse(path)
	if err != nil || parsed.User != nil || parsed.IsAbs() || parsed.Host != "" || !strings.HasPrefix(parsed.Path, "/") {
		return "", errors.New("Plex source path is invalid")
	}
	base.Path = strings.TrimRight(base.Path, "/") + parsed.Path
	base.RawQuery = parsed.RawQuery
	query := base.Query()
	query.Set("X-Plex-Token", token)
	base.RawQuery = query.Encode()
	return base.String(), nil
}
