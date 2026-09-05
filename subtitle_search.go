package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/LukeHagar/plexgo/models/components"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	pgvector "github.com/pgvector/pgvector-go"
	pgxvector "github.com/pgvector/pgvector-go/pgx"
)

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
	subtitleChunkMaxRunes      = 400
	subtitleChunkMaxGapMs      int64 = 15 * 1000
	subtitleChunkMaxDurationMs int64 = 60 * 1000
	bgeQueryInstruction        = "Represent this sentence for searching relevant passages: "
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

type teiClient struct {
	url        string
	provider   string
	apiKey     string
	model      string
	dimensions int
	client     *http.Client
}

func newTEIClient(rawURL string) (*teiClient, error) {
	return newEmbeddingClient(rawURL, embeddingsProviderTEI, "", "", defaultSubtitleEmbeddingDimensions)
}

func normalizeEmbeddingsProvider(value string) (string, error) {
	provider := strings.ToLower(strings.TrimSpace(value))
	if provider == "" {
		return embeddingsProviderTEI, nil
	}
	switch provider {
	case embeddingsProviderTEI, embeddingsProviderOpenAI:
		return provider, nil
	default:
		return "", fmt.Errorf("unsupported embeddings provider %q (want tei or openai)", provider)
	}
}

func normalizeSubtitleEmbeddingBatchSize(value int) int {
	if value <= 0 {
		return defaultSubtitleEmbeddingBatchSize
	}
	if value < minSubtitleEmbeddingBatchSize {
		return minSubtitleEmbeddingBatchSize
	}
	if value > maxSubtitleEmbeddingBatchSize {
		return maxSubtitleEmbeddingBatchSize
	}
	return value
}

func normalizeSubtitleEmbeddingConcurrency(value int) int {
	if value <= 0 {
		return defaultSubtitleEmbeddingConcurrency
	}
	if value < minSubtitleEmbeddingConcurrency {
		return minSubtitleEmbeddingConcurrency
	}
	if value > maxSubtitleEmbeddingConcurrency {
		return maxSubtitleEmbeddingConcurrency
	}
	return value
}

func resolveQueryInstruction(cfg SemanticSearchConfig, dimensions int) string {
	if cfg.QueryInstruction != nil {
		return *cfg.QueryInstruction
	}
	modelLower := strings.ToLower(strings.TrimSpace(cfg.EmbeddingsModel))
	if strings.Contains(modelLower, "bge") {
		return bgeQueryInstruction
	}
	if modelLower == "" && cfg.EmbeddingsProvider != embeddingsProviderOpenAI && dimensions == defaultSubtitleEmbeddingDimensions {
		return bgeQueryInstruction
	}
	return ""
}

func (s *subtitleSearchStore) queryInput(query string) string {
	if s != nil && s.queryInstruction != "" {
		return s.queryInstruction + query
	}
	return query
}

func newEmbeddingClient(rawURL, provider, apiKey, model string, dimensions int) (*teiClient, error) {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil {
		return nil, errors.New("semantic_search.embeddings_url must be an HTTP(S) URL")
	}
	provider, err = normalizeEmbeddingsProvider(provider)
	if err != nil {
		return nil, err
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/")
	if provider == embeddingsProviderTEI {
		if !strings.HasSuffix(parsed.Path, "/embed") {
			parsed.Path += "/embed"
		}
	} else {
		if strings.HasSuffix(parsed.Path, "/v1") {
			parsed.Path += "/embeddings"
		} else if !strings.HasSuffix(parsed.Path, "/v1/embeddings") {
			parsed.Path += "/v1/embeddings"
		}
	}
	if dimensions <= 0 {
		dimensions = defaultSubtitleEmbeddingDimensions
	}
	trimmedModel := strings.TrimSpace(model)
	if trimmedModel == "" && provider == embeddingsProviderOpenAI {
		trimmedModel = openAIEmbeddingModel
	}
	return &teiClient{
		url:        parsed.String(),
		provider:   provider,
		apiKey:     strings.TrimSpace(apiKey),
		model:      trimmedModel,
		dimensions: dimensions,
		client:     &http.Client{Timeout: 30 * time.Second},
	}, nil
}

func validateEmbeddings(embeddings [][]float32, expected int, dimensions int) error {
	if len(embeddings) != expected {
		return fmt.Errorf("embedding response count %d does not match request count %d", len(embeddings), expected)
	}
	if dimensions <= 0 {
		dimensions = defaultSubtitleEmbeddingDimensions
	}
	for i, embedding := range embeddings {
		if len(embedding) != dimensions {
			return fmt.Errorf("embedding %d has dimension %d, want %d", i, len(embedding), dimensions)
		}
	}
	return nil
}

func (c *teiClient) embed(ctx context.Context, inputs []string) ([][]float32, error) {
	if len(inputs) == 0 {
		return [][]float32{}, nil
	}
	vecs, err := c.embedRaw(ctx, inputs)
	if err != nil {
		return nil, err
	}
	expectedDim := c.dimensions
	if expectedDim <= 0 {
		expectedDim = defaultSubtitleEmbeddingDimensions
	}
	if err := validateEmbeddings(vecs, len(inputs), expectedDim); err != nil {
		return nil, err
	}
	return vecs, nil
}

func (c *teiClient) probeDimensions(ctx context.Context) (int, error) {
	probeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	vecs, err := c.embedRaw(probeCtx, []string{"probe"})
	if err != nil {
		return 0, err
	}
	if len(vecs) == 0 || len(vecs[0]) == 0 {
		return 0, errors.New("embedding probe returned empty vector")
	}
	return len(vecs[0]), nil
}

func (c *teiClient) embedRaw(ctx context.Context, inputs []string) ([][]float32, error) {
	if len(inputs) == 0 {
		return [][]float32{}, nil
	}
	if c.provider == embeddingsProviderOpenAI {
		return c.embedRawOpenAI(ctx, inputs)
	}
	return c.embedRawTEI(ctx, inputs)
}

func (c *teiClient) embedRawTEI(ctx context.Context, inputs []string) ([][]float32, error) {
	payload, err := json.Marshal(struct {
		Inputs    []string `json:"inputs"`
		Normalize bool     `json:"normalize"`
	}{inputs, true})
	if err != nil {
		return nil, errors.New("could not encode embedding request")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(payload))
	if err != nil {
		return nil, errors.New("could not create embedding request")
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, errors.New("embedding service unavailable")
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("embedding service returned status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return nil, errors.New("could not read embedding response")
	}
	var raw json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, errors.New("embedding response is invalid JSON")
	}
	var embeddings [][]float32
	if len(raw) > 0 && raw[0] == '[' {
		if err := json.Unmarshal(raw, &embeddings); err != nil {
			return nil, errors.New("embedding response has invalid vectors")
		}
	} else {
		var wrapped struct {
			Embeddings [][]float32 `json:"embeddings"`
		}
		if err := json.Unmarshal(raw, &wrapped); err != nil {
			return nil, errors.New("embedding response has invalid vectors")
		}
		embeddings = wrapped.Embeddings
	}
	return embeddings, nil
}

type openAIEmbeddingItem struct {
	Embedding []float32 `json:"embedding"`
	Index     int       `json:"index"`
}

type openAIEmbeddingResponse struct {
	Data []openAIEmbeddingItem `json:"data"`
}

func (c *teiClient) embedRawOpenAI(ctx context.Context, inputs []string) ([][]float32, error) {
	model := c.model
	if strings.TrimSpace(model) == "" {
		model = openAIEmbeddingModel
	}
	payload, err := json.Marshal(struct {
		Model          string   `json:"model"`
		Input          []string `json:"input"`
		EncodingFormat string   `json:"encoding_format"`
	}{model, inputs, "float"})
	if err != nil {
		return nil, errors.New("could not encode embedding request")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(payload))
	if err != nil {
		return nil, errors.New("could not create embedding request")
	}
	req.Header.Set("Content-Type", "application/json")
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, errors.New("embedding service unavailable")
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("embedding service returned status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return nil, errors.New("could not read embedding response")
	}
	var response openAIEmbeddingResponse
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, errors.New("embedding response is invalid JSON")
	}
	if len(response.Data) != len(inputs) {
		return nil, fmt.Errorf("embedding response count %d does not match request count %d", len(response.Data), len(inputs))
	}
	ordered := make([][]float32, len(inputs))
	seen := make([]bool, len(inputs))
	for _, item := range response.Data {
		if item.Index < 0 || item.Index >= len(inputs) || seen[item.Index] {
			return nil, errors.New("embedding response indexes are invalid")
		}
		seen[item.Index] = true
		ordered[item.Index] = item.Embedding
	}
	for _, present := range seen {
		if !present {
			return nil, errors.New("embedding response indexes are incomplete")
		}
	}
	return ordered, nil
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

type subtitleSearchIndexRequest struct {
	RatingKey   string `json:"ratingKey"`
	MediaID     int64  `json:"mediaId"`
	PartID      int64  `json:"partId"`
	SectionKey  string `json:"-"`
	SectionUUID string `json:"-"`
	ScanID      string `json:"-"`
}

type subtitleSearchSkipped struct {
	SubtitleIndex int    `json:"subtitleIndex"`
	Reason        string `json:"reason"`
}

type subtitleSearchIndexResponse struct {
	RatingKey             string                  `json:"ratingKey"`
	MediaID               int64                   `json:"mediaId"`
	PartID                int64                   `json:"partId"`
	Indexed               int                     `json:"indexed"`
	Skipped               []subtitleSearchSkipped `json:"skipped"`
	UnchangedTracks       int                     `json:"-"`
	UnsupportedTracks     int                     `json:"-"`
	EmptyTracks           int                     `json:"-"`
	FailedTracks          int                     `json:"-"`
	FailedEmbeddedIndices []int                   `json:"-"`
}

func isEnglishSubtitleStream(stream SubtitleStream) bool {
	for _, value := range []string{stream.LanguageCode, stream.Language} {
		value = strings.ToLower(strings.TrimSpace(value))
		if value == "english" || value == "eng" || value == "en" || strings.HasPrefix(value, "en-") {
			return true
		}
	}
	return false
}

func (a *Application) acquireSubtitleBulkFFmpeg(ctx context.Context) (func(), error) {
	if a.subtitleBulkGate != nil {
		select {
		case a.subtitleBulkGate <- struct{}{}:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	releaseGlobal, err := a.acquireFFmpeg(ctx)
	if err != nil {
		if a.subtitleBulkGate != nil {
			<-a.subtitleBulkGate
		}
		return nil, err
	}
	return func() {
		releaseGlobal()
		if a.subtitleBulkGate != nil {
			<-a.subtitleBulkGate
		}
	}, nil
}

func (a *Application) subtitleIndexMediaURL(ctx context.Context, part *components.Part) (string, func(), error) {
	if part == nil || strings.TrimSpace(part.Key) == "" {
		return "", nil, errors.New("subtitle source path is unavailable")
	}
	if localPath, ok := a.resolveLocalPartFile(part); ok {
		return localPath, nil, nil
	}
	if AuthTokenFromContext(ctx) != nil {
		proxy, err := a.ensureMediaProxy()
		if err != nil {
			return "", nil, errors.New("Plex capability proxy is unavailable")
		}
		access, _, err := a.callerPlexAccess(ctx, "")
		if err != nil {
			return "", nil, errors.New("caller Plex access is unavailable")
		}
		return proxy.IssueWithTTL(ctx, access, part.Key, renderTimeout)
	}
	mediaURL, err := a.buildPlexSourceURL(part.Key, a.plexSourceToken(ctx, AuthTokenFromContext(ctx) != nil))
	return mediaURL, nil, err
}

func (a *Application) indexSubtitleSource(ctx context.Context, request subtitleSearchIndexRequest) (subtitleSearchIndexResponse, error) {
	if a.subtitleSearch == nil {
		return subtitleSearchIndexResponse{}, errSubtitleSearchDisabled
	}
	owner, err := subtitleSearchOwner(ctx)
	if err != nil {
		return subtitleSearchIndexResponse{}, err
	}
	if a.sharedCorpus {
		if !a.isServerOwner(UserFromContext(ctx)) || !validSharedSectionKey(request.SectionKey) || strings.TrimSpace(request.SectionUUID) == "" || strings.TrimSpace(request.ScanID) == "" || strings.TrimSpace(a.machineIdentifier) == "" {
			return subtitleSearchIndexResponse{}, errors.New("shared subtitle source is not section-authorized")
		}
	}
	unlock := a.subtitleSearch.trackLocks.acquire(fmt.Sprintf("%s:%d:%d", owner, request.MediaID, request.PartID))
	defer unlock()
	metadata, metadataErr := a.getMetadataItem(ctx, request.RatingKey, true)
	if metadataErr != nil {
		return subtitleSearchIndexResponse{}, subtitleItemFailure(metadataErr)
	}
	media, part, sourceErr := resolveLibraryMetadataSource(metadata, request.MediaID, request.PartID)
	if sourceErr != nil || media == nil || part == nil {
		if sourceErr == nil {
			sourceErr = errors.New("subtitle source is unavailable")
		}
		return subtitleSearchIndexResponse{}, subtitleItemFailure(sourceErr)
	}
	source := librarySearchResultFromMetadata(metadata, media, part)
	duration, durationErr := selectedSourceDuration(media, part)
	if durationErr != nil {
		return subtitleSearchIndexResponse{}, subtitleItemFailure(durationErr)
	}
	if duration > 0 {
		source.Duration = duration
	}
	plans := enumerateSubtitleTrackPlans(part.Stream)
	if len(plans) == 0 {
		return subtitleSearchIndexResponse{RatingKey: request.RatingKey, MediaID: request.MediaID, PartID: request.PartID, Skipped: []subtitleSearchSkipped{{-1, "no subtitle streams"}}, EmptyTracks: 1}, nil
	}
	castContext := subtitleEmbeddingContext(metadata)
	sourceRevision := subtitleSourceRevision(metadata, request.MediaID, request.PartID)
	result := subtitleSearchIndexResponse{RatingKey: request.RatingKey, MediaID: request.MediaID, PartID: request.PartID, Skipped: []subtitleSearchSkipped{}}
	changed := make([]subtitleTrackPlan, 0)
	changedExternal := make([]subtitleTrackPlan, 0)
	fingerprints := make(map[int]string)
	dim := 0
	if a.subtitleSearch != nil {
		dim = a.subtitleSearch.dimensions
	}
	for _, plan := range plans {
		stream := plan.Stream
		fingerprint := subtitleSourceFingerprintWithDimensions(request.RatingKey, request.MediaID, request.PartID, stream, castContext, dim, sourceRevision)
		fingerprints[plan.PublicIndex] = fingerprint
		unchanged := false
		var checkErr error
		if a.sharedCorpus {
			unchanged, checkErr = a.sharedSubtitleTrackUnchanged(ctx, request, stream.Index, fingerprint)
		} else {
			unchanged, checkErr = a.subtitleTrackUnchanged(ctx, owner, request, stream.Index, fingerprint)
		}
		if checkErr != nil && !errors.Is(checkErr, pgx.ErrNoRows) {
			return result, checkErr
		}
		if checkErr == nil && unchanged {
			if !a.sharedCorpus {
				if err := a.markSubtitleTrackSeen(ctx, owner, request, plan.PublicIndex); err != nil {
					return result, err
				}
			}
			if a.sharedCorpus {
				if err := a.copySharedSubtitleTrack(ctx, request, plan.PublicIndex, fingerprint); err != nil {
					return result, err
				}
			}
			result.Skipped = append(result.Skipped, subtitleSearchSkipped{plan.PublicIndex, "unchanged subtitle track"})
			result.UnchangedTracks++
			continue
		}
		if stream.External {
			if !isEnglishSubtitleStream(stream) {
				result.Skipped = append(result.Skipped, subtitleSearchSkipped{plan.PublicIndex, "not an English text track"})
				result.UnsupportedTracks++
				if a.sharedCorpus {
					if err := a.recordSharedSubtitleSeen(ctx, request, plan.PublicIndex, fingerprint, 0); err != nil {
						return result, err
					}
				}
				continue
			}
			if stream.Type != "text" {
				result.Skipped = append(result.Skipped, subtitleSearchSkipped{plan.PublicIndex, "unsupported subtitle format"})
				result.UnsupportedTracks++
				if a.sharedCorpus {
					if err := a.recordSharedSubtitleSeen(ctx, request, plan.PublicIndex, fingerprint, 0); err != nil {
						return result, err
					}
				}
				continue
			}
			changedExternal = append(changedExternal, plan)
			continue
		}
		if stream.Type != "text" {
			result.Skipped = append(result.Skipped, subtitleSearchSkipped{plan.PublicIndex, "unsupported subtitle format"})
			result.UnsupportedTracks++
			if a.sharedCorpus {
				if err := a.recordSharedSubtitleSeen(ctx, request, plan.PublicIndex, fingerprint, 0); err != nil {
					return result, err
				}
			}
			continue
		}
		if !isEnglishSubtitleStream(stream) {
			result.Skipped = append(result.Skipped, subtitleSearchSkipped{plan.PublicIndex, "not an English text track"})
			result.UnsupportedTracks++
			if a.sharedCorpus {
				if err := a.recordSharedSubtitleSeen(ctx, request, plan.PublicIndex, fingerprint, 0); err != nil {
					return result, err
				}
			}
			continue
		}
		changed = append(changed, plan)
	}
	for _, plan := range changedExternal {
		token := a.plexSourceToken(ctx, AuthTokenFromContext(ctx) != nil)
		entries, downloadErr := a.downloadSubtitleWithToken(ctx, plan.Raw.Key, plan.Stream.Codec, token)
		if downloadErr != nil {
			result.FailedTracks++
			result.Skipped = append(result.Skipped, subtitleSearchSkipped{plan.PublicIndex, "subtitle download failed"})
			continue
		}
		chunks := chunkSubtitleEntries(entries)
		if len(chunks) == 0 {
			result.Skipped = append(result.Skipped, subtitleSearchSkipped{plan.PublicIndex, "subtitle contains no searchable text"})
			result.EmptyTracks++
			if a.sharedCorpus {
				if err := a.recordSharedSubtitleSeen(ctx, request, plan.PublicIndex, fingerprints[plan.PublicIndex], 0); err != nil {
					return result, err
				}
			}
			continue
		}
		if a.sharedCorpus {
			if err := a.persistSharedSubtitleChunks(ctx, request, source, plan.PublicIndex, chunks, castContext, fingerprints[plan.PublicIndex]); err != nil {
				return result, err
			}
		} else if err := a.persistSubtitleChunks(ctx, owner, source, plan.PublicIndex, chunks, castContext); err != nil {
			return result, err
		}
		if !a.sharedCorpus {
			if err := a.recordSubtitleFingerprint(ctx, owner, request, plan.PublicIndex, fingerprints[plan.PublicIndex], len(chunks)); err != nil {
				return result, err
			}
		}
		result.Indexed += len(chunks)
	}
	if len(changed) == 0 {
		return result, nil
	}
	mediaURL, releaseCapability, urlErr := a.subtitleIndexMediaURL(ctx, part)
	if urlErr != nil {
		return result, subtitleItemFailure(urlErr)
	}
	cleanCapability := func() {
		if releaseCapability != nil {
			releaseCapability()
			releaseCapability = nil
		}
	}
	defer cleanCapability()
	embeddedIndices := make([]int, 0, len(changed))
	for _, plan := range changed {
		embeddedIndices = append(embeddedIndices, plan.EmbeddedIndex)
	}
	releaseBatch, err := a.acquireSubtitleBulkFFmpeg(ctx)
	if err != nil {
		return result, err
	}
	entriesByEmbedded, batchErr := ExtractSubtitleTracksBatchContext(ctx, mediaURL, embeddedIndices)
	releaseBatch()
	if batchErr != nil && (errors.Is(batchErr, context.Canceled) || errors.Is(batchErr, context.DeadlineExceeded)) {
		return result, batchErr
	}
	if batchErr != nil && ctx.Err() != nil {
		return result, ctx.Err()
	}
	var firstItemErr error
	if batchErr != nil {
		entriesByEmbedded = make(map[int][]SubtitleEntry, len(changed))
		for _, plan := range changed {
			releaseOne, acquireErr := a.acquireSubtitleBulkFFmpeg(ctx)
			if acquireErr != nil {
				if errors.Is(acquireErr, context.Canceled) || errors.Is(acquireErr, context.DeadlineExceeded) {
					return result, acquireErr
				}
				if firstItemErr == nil {
					firstItemErr = acquireErr
				}
				continue
			}
			file, extractErr := ExtractSubtitleFullContext(ctx, mediaURL, plan.EmbeddedIndex)
			releaseOne()
			if extractErr != nil {
				if errors.Is(extractErr, context.Canceled) || errors.Is(extractErr, context.DeadlineExceeded) {
					return result, extractErr
				}
				if !errors.Is(extractErr, ErrNoUsableSubtitleCues) {
					if firstItemErr == nil {
						firstItemErr = extractErr
					}
					continue
				}
				entriesByEmbedded[plan.EmbeddedIndex] = nil
				continue
			}
			entries, parseErr := ParseSRT(file)
			_ = os.Remove(file)
			if parseErr != nil {
				if firstItemErr == nil {
					firstItemErr = parseErr
				}
				continue
			}
			entriesByEmbedded[plan.EmbeddedIndex] = entries
		}
	}
	cleanCapability()
	for _, plan := range changed {
		entries, extracted := entriesByEmbedded[plan.EmbeddedIndex]
		if !extracted {
			result.FailedTracks++
			result.FailedEmbeddedIndices = append(result.FailedEmbeddedIndices, plan.EmbeddedIndex)
			result.Skipped = append(result.Skipped, subtitleSearchSkipped{plan.PublicIndex, "subtitle extraction failed"})
			continue
		}
		chunks := chunkSubtitleEntries(entries)
		if len(chunks) == 0 {
			result.Skipped = append(result.Skipped, subtitleSearchSkipped{plan.PublicIndex, "subtitle contains no searchable text"})
			result.EmptyTracks++
			if a.sharedCorpus {
				if err := a.recordSharedSubtitleSeen(ctx, request, plan.PublicIndex, fingerprints[plan.PublicIndex], 0); err != nil {
					return result, err
				}
			}
			continue
		}
		if a.sharedCorpus {
			if err := a.persistSharedSubtitleChunks(ctx, request, source, plan.PublicIndex, chunks, castContext, fingerprints[plan.PublicIndex]); err != nil {
				return result, err
			}
		} else if err := a.persistSubtitleChunks(ctx, owner, source, plan.PublicIndex, chunks, castContext); err != nil {
			return result, err
		}
		var recordErr error
		if !a.sharedCorpus {
			recordErr = a.recordSubtitleFingerprint(ctx, owner, request, plan.PublicIndex, fingerprints[plan.PublicIndex], len(chunks))
		}
		if recordErr != nil {
			return result, recordErr
		}
		result.Indexed += len(chunks)
	}
	if firstItemErr != nil {
		return result, subtitleItemFailure(firstItemErr)
	}
	return result, nil
}

func (a *Application) sharedSubtitleTrackUnchanged(ctx context.Context, request subtitleSearchIndexRequest, subtitleIndex int, fingerprint string) (bool, error) {
	var stored string
	err := a.subtitleSearch.pool.QueryRow(ctx, `SELECT src.fingerprint FROM subtitle_shared_sources src JOIN subtitle_shared_sections sec ON sec.machine_identifier=src.machine_identifier AND sec.section_uuid=src.section_uuid AND (sec.scan_id=src.scan_id OR (sec.ready_scan_id IS NOT NULL AND sec.ready_scan_id=src.scan_id)) WHERE src.machine_identifier=$1 AND src.section_uuid=$2 AND src.rating_key=$3 AND src.media_id=$4 AND src.part_id=$5 AND src.subtitle_index=$6 AND src.chunk_count > 0 LIMIT 1`, a.machineIdentifier, request.SectionUUID, request.RatingKey, request.MediaID, request.PartID, subtitleIndex).Scan(&stored)
	if err != nil {
		return false, err
	}
	return stored == fingerprint, nil
}

func subtitleSourceRevision(metadata *components.Metadata, mediaID, partID int64) string {
	hash := sha256.New()
	_, _ = fmt.Fprintf(hash, "subtitle-source-revision-v1|%d|%d", mediaID, partID)
	if metadata == nil {
		return hex.EncodeToString(hash.Sum(nil))
	}
	_, _ = fmt.Fprintf(hash, "|updated:%d", valueInt64(metadata.UpdatedAt))
	media := findMediaByID(metadata.Media, mediaID)
	if media == nil {
		return hex.EncodeToString(hash.Sum(nil))
	}
	_, _ = fmt.Fprintf(hash, "|media:%d", media.ID)
	for _, part := range media.Part {
		if part.ID != partID {
			continue
		}
		_, _ = fmt.Fprintf(hash, "|part:%d|key:%s", part.ID, boundedFingerprintValue(part.Key))
		if part.Size != nil {
			_, _ = fmt.Fprintf(hash, "|size:%d", *part.Size)
		}
		if part.Duration != nil {
			_, _ = fmt.Fprintf(hash, "|duration:%d", *part.Duration)
		}
		break
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func valueInt64(value *int64) int64 {
	if value == nil {
		return 0
	}
	return *value
}

func boundedFingerprintValue(value string) string {
	if len(value) > 512 {
		return value[:512]
	}
	return value
}

func subtitleSourceFingerprint(ratingKey string, mediaID, partID int64, stream SubtitleStream, castContext string, sourceRevisions ...string) string {
	return subtitleSourceFingerprintWithDimensions(ratingKey, mediaID, partID, stream, castContext, 0, sourceRevisions...)
}

func subtitleSourceFingerprintWithDimensions(ratingKey string, mediaID, partID int64, stream SubtitleStream, castContext string, dimensions int, sourceRevisions ...string) string {
	hash := sha256.New()
	revision := ""
	if len(sourceRevisions) > 0 {
		revision = sourceRevisions[0]
	}
	version := subtitleSearchIndexVersion
	if dimensions > 0 {
		version = fmt.Sprintf("%s-dim-%d", subtitleSearchIndexVersion, dimensions)
	}
	_, _ = fmt.Fprintf(hash, "%s|%s|%d|%d|%d|%s|%s|%s|%t|%s|%s|%s", version, boundedFingerprintValue(ratingKey), mediaID, partID, stream.Index, stream.Codec, stream.Language, stream.LanguageCode, stream.External, stream.Type, castContext, revision)
	return hex.EncodeToString(hash.Sum(nil))
}

func subtitleEmbeddingContext(metadata *components.Metadata) string {
	if metadata == nil {
		return ""
	}
	const maxCastEntries, maxContextRunes = 12, 900
	parts := make([]string, 0, maxCastEntries)
	for _, role := range metadata.Role {
		if len(parts) >= maxCastEntries || strings.TrimSpace(role.Tag) == "" {
			break
		}
		entry := role.Tag
		if role.Role != nil && strings.TrimSpace(*role.Role) != "" {
			entry += " as " + strings.TrimSpace(*role.Role)
		}
		parts = append(parts, entry)
	}
	if len(parts) == 0 {
		return ""
	}
	context := fmt.Sprintf("Title: %s\nCast: %s", metadata.Title, strings.Join(parts, "; "))
	runes := []rune(context)
	if len(runes) > maxContextRunes {
		context = string(runes[:maxContextRunes])
	}
	return context
}

func (a *Application) subtitleTrackUnchanged(ctx context.Context, owner string, request subtitleSearchIndexRequest, subtitleIndex int, fingerprint string) (bool, error) {
	var stored string
	err := a.subtitleSearch.pool.QueryRow(ctx, `SELECT fingerprint FROM subtitle_index_sources WHERE owner_uuid=$1 AND rating_key=$2 AND media_id=$3 AND part_id=$4 AND subtitle_index=$5 AND chunk_count > 0`, owner, request.RatingKey, request.MediaID, request.PartID, subtitleIndex).Scan(&stored)
	if err != nil {
		return false, err
	}
	return stored == fingerprint, nil
}

func (a *Application) recordSubtitleFingerprint(ctx context.Context, owner string, request subtitleSearchIndexRequest, subtitleIndex int, fingerprint string, chunkCount int) error {
	_, err := a.subtitleSearch.pool.Exec(ctx, `INSERT INTO subtitle_index_sources (owner_uuid, rating_key, media_id, part_id, subtitle_index, fingerprint, chunk_count, scan_id, indexed_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,NOW()) ON CONFLICT (owner_uuid, rating_key, media_id, part_id, subtitle_index) DO UPDATE SET fingerprint=EXCLUDED.fingerprint, chunk_count=EXCLUDED.chunk_count, scan_id=COALESCE(EXCLUDED.scan_id, subtitle_index_sources.scan_id), indexed_at=EXCLUDED.indexed_at`, owner, request.RatingKey, request.MediaID, request.PartID, subtitleIndex, fingerprint, chunkCount, nullableUUID(request.ScanID))
	return err
}

func (a *Application) markSubtitleTrackSeen(ctx context.Context, owner string, request subtitleSearchIndexRequest, subtitleIndex int) error {
	if strings.TrimSpace(request.ScanID) == "" {
		return nil
	}
	_, err := a.subtitleSearch.pool.Exec(ctx, `UPDATE subtitle_index_sources SET scan_id=$6, indexed_at=NOW() WHERE owner_uuid=$1 AND rating_key=$2 AND media_id=$3 AND part_id=$4 AND subtitle_index=$5`, owner, request.RatingKey, request.MediaID, request.PartID, subtitleIndex, request.ScanID)
	return err
}

func nullableUUID(value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return value
}

func (a *Application) persistSubtitleChunks(ctx context.Context, owner string, source LibrarySearchResult, subtitleIndex int, chunks []subtitleChunk, embeddingContext string) error {
	inputs := make([]string, len(chunks))
	for i, chunk := range chunks {
		inputs[i] = subtitleEmbeddingInput(embeddingContext, chunk.Text)
	}
	bulkEmbedding := isBulkSubtitleContext(ctx)
	if bulkEmbedding && a.subtitleSearch != nil {
		if err := acquireSubtitleSemaphore(ctx, a.subtitleSearch.bulkEmbeddings); err != nil {
			return err
		}
	}
	batchSize := defaultSubtitleEmbeddingBatchSize
	if a.subtitleSearch != nil && a.subtitleSearch.batchSize > 0 {
		batchSize = a.subtitleSearch.batchSize
	}
	embeddings := make([][]float32, 0, len(chunks))
	for start := 0; start < len(inputs); start += batchSize {
		end := start + batchSize
		if end > len(inputs) {
			end = len(inputs)
		}
		batch, err := a.subtitleSearch.embeddings.embed(ctx, inputs[start:end])
		if err != nil {
			if bulkEmbedding && a.subtitleSearch != nil {
				releaseSubtitleSemaphore(a.subtitleSearch.bulkEmbeddings)
			}
			return err
		}
		embeddings = append(embeddings, batch...)
	}
	if bulkEmbedding && a.subtitleSearch != nil {
		releaseSubtitleSemaphore(a.subtitleSearch.bulkEmbeddings)
		if err := acquireSubtitleSemaphore(ctx, a.subtitleSearch.bulkWrites); err != nil {
			return err
		}
		defer releaseSubtitleSemaphore(a.subtitleSearch.bulkWrites)
	}
	tx, err := a.subtitleSearch.pool.Begin(ctx)
	if err != nil {
		return errors.New("could not update subtitle index")
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `DELETE FROM subtitle_chunks WHERE owner_uuid=$1 AND rating_key=$2 AND media_id=$3 AND part_id=$4 AND subtitle_index=$5`, owner, source.RatingKey, source.MediaID, source.PartID, subtitleIndex); err != nil {
		return errors.New("could not update subtitle index")
	}

	batch := &pgx.Batch{}
	for i, chunk := range chunks {
		hash := sha256.Sum256([]byte(chunk.Text))
		batch.Queue(`INSERT INTO subtitle_chunks (owner_uuid, rating_key, media_id, part_id, subtitle_index, title, show_title, season, episode, year, start_ms, end_ms, text, content_hash, embedding) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)`,
			owner, source.RatingKey, source.MediaID, source.PartID, subtitleIndex, source.Title, nullableString(source.GrandparentTitle), nullableInt(source.SeasonNumber), nullableInt(source.EpisodeNumber), nullableInt(source.Year), chunk.Start, chunk.End, chunk.Text, hex.EncodeToString(hash[:]), pgvector.NewVector(embeddings[i]))
	}
	br := tx.SendBatch(ctx, batch)
	defer br.Close()
	for range chunks {
		if _, err := br.Exec(); err != nil {
			return errors.New("could not update subtitle index")
		}
	}
	if err := br.Close(); err != nil {
		return errors.New("could not update subtitle index")
	}
	if err := tx.Commit(ctx); err != nil {
		return errors.New("could not update subtitle index")
	}
	return nil
}

func (a *Application) persistSharedSubtitleChunks(ctx context.Context, request subtitleSearchIndexRequest, source LibrarySearchResult, subtitleIndex int, chunks []subtitleChunk, embeddingContext, fingerprint string) error {
	inputs := make([]string, len(chunks))
	for i, chunk := range chunks {
		inputs[i] = subtitleEmbeddingInput(embeddingContext, chunk.Text)
	}
	bulkEmbedding := isBulkSubtitleContext(ctx)
	if bulkEmbedding && a.subtitleSearch != nil {
		if err := acquireSubtitleSemaphore(ctx, a.subtitleSearch.bulkEmbeddings); err != nil {
			return err
		}
	}
	batchSize := defaultSubtitleEmbeddingBatchSize
	if a.subtitleSearch != nil && a.subtitleSearch.batchSize > 0 {
		batchSize = a.subtitleSearch.batchSize
	}
	embeddings := make([][]float32, 0, len(chunks))
	for start := 0; start < len(inputs); start += batchSize {
		end := start + batchSize
		if end > len(inputs) {
			end = len(inputs)
		}
		batch, err := a.subtitleSearch.embeddings.embed(ctx, inputs[start:end])
		if err != nil {
			if bulkEmbedding && a.subtitleSearch != nil {
				releaseSubtitleSemaphore(a.subtitleSearch.bulkEmbeddings)
			}
			return err
		}
		embeddings = append(embeddings, batch...)
	}
	if bulkEmbedding && a.subtitleSearch != nil {
		releaseSubtitleSemaphore(a.subtitleSearch.bulkEmbeddings)
		if err := acquireSubtitleSemaphore(ctx, a.subtitleSearch.bulkWrites); err != nil {
			return err
		}
		defer releaseSubtitleSemaphore(a.subtitleSearch.bulkWrites)
	}
	tx, err := a.subtitleSearch.pool.Begin(ctx)
	if err != nil {
		return errors.New("could not update shared subtitle index")
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `DELETE FROM subtitle_shared_chunks WHERE machine_identifier=$1 AND section_uuid=$2 AND scan_id=$7 AND rating_key=$3 AND media_id=$4 AND part_id=$5 AND subtitle_index=$6`, a.machineIdentifier, request.SectionUUID, source.RatingKey, source.MediaID, source.PartID, subtitleIndex, request.ScanID); err != nil {
		return errors.New("could not update shared subtitle index")
	}
	if _, err := tx.Exec(ctx, `DELETE FROM subtitle_shared_sources WHERE machine_identifier=$1 AND section_uuid=$2 AND scan_id=$7 AND rating_key=$3 AND media_id=$4 AND part_id=$5 AND subtitle_index=$6`, a.machineIdentifier, request.SectionUUID, source.RatingKey, source.MediaID, source.PartID, subtitleIndex, request.ScanID); err != nil {
		return errors.New("could not update shared subtitle index")
	}

	batch := &pgx.Batch{}
	for i, chunk := range chunks {
		hash := sha256.Sum256([]byte(chunk.Text))
		batch.Queue(`INSERT INTO subtitle_shared_chunks (machine_identifier, section_uuid, section_key, rating_key, media_id, part_id, subtitle_index, title, show_title, season, episode, year, start_ms, end_ms, text, content_hash, embedding, scan_id) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18)`, a.machineIdentifier, request.SectionUUID, request.SectionKey, source.RatingKey, source.MediaID, source.PartID, subtitleIndex, source.Title, nullableString(source.GrandparentTitle), nullableInt(source.SeasonNumber), nullableInt(source.EpisodeNumber), nullableInt(source.Year), chunk.Start, chunk.End, chunk.Text, hex.EncodeToString(hash[:]), pgvector.NewVector(embeddings[i]), request.ScanID)
	}
	br := tx.SendBatch(ctx, batch)
	defer br.Close()
	for range chunks {
		if _, err := br.Exec(); err != nil {
			return errors.New("could not update shared subtitle index")
		}
	}
	if err := br.Close(); err != nil {
		return errors.New("could not update shared subtitle index")
	}
	_, err = tx.Exec(ctx, `INSERT INTO subtitle_shared_sources (machine_identifier, section_uuid, section_key, rating_key, media_id, part_id, subtitle_index, fingerprint, chunk_count, scan_id, indexed_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,NOW()) ON CONFLICT (machine_identifier, section_uuid, scan_id, rating_key, media_id, part_id, subtitle_index) DO UPDATE SET fingerprint=EXCLUDED.fingerprint, chunk_count=EXCLUDED.chunk_count, section_key=EXCLUDED.section_key, indexed_at=EXCLUDED.indexed_at`, a.machineIdentifier, request.SectionUUID, request.SectionKey, source.RatingKey, source.MediaID, source.PartID, subtitleIndex, fingerprint, len(chunks), request.ScanID)
	if err != nil {
		return errors.New("could not update shared subtitle index")
	}
	return tx.Commit(ctx)
}

func (a *Application) copySharedSubtitleTrack(ctx context.Context, request subtitleSearchIndexRequest, subtitleIndex int, fingerprint string) error {
	tx, err := a.subtitleSearch.pool.Begin(ctx)
	if err != nil {
		return errors.New("could not update shared subtitle index")
	}
	defer tx.Rollback(ctx)

	// Avoid duplicate inserts if destination scan already has these chunks (e.g. from prior interrupted scan)
	var alreadyCopied bool
	err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM subtitle_shared_chunks WHERE machine_identifier=$1 AND section_uuid=$2 AND scan_id=$3 AND rating_key=$4 AND media_id=$5 AND part_id=$6 AND subtitle_index=$7)`, a.machineIdentifier, request.SectionUUID, request.ScanID, request.RatingKey, request.MediaID, request.PartID, subtitleIndex).Scan(&alreadyCopied)
	if err != nil {
		return errors.New("could not update shared subtitle index")
	}

	if !alreadyCopied {
		// Copy from the published ready scan (or prior scan) into the in-progress scan without modifying the ready scan's rows
		_, err = tx.Exec(ctx, `INSERT INTO subtitle_shared_chunks (machine_identifier, section_uuid, section_key, rating_key, media_id, part_id, subtitle_index, title, show_title, season, episode, year, start_ms, end_ms, text, content_hash, embedding, scan_id) SELECT c.machine_identifier, c.section_uuid, $3, c.rating_key, c.media_id, c.part_id, c.subtitle_index, c.title, c.show_title, c.season, c.episode, c.year, c.start_ms, c.end_ms, c.text, c.content_hash, c.embedding, $2 FROM subtitle_shared_chunks c JOIN subtitle_shared_sections s ON s.machine_identifier=c.machine_identifier AND s.section_uuid=c.section_uuid AND (s.ready_scan_id=c.scan_id OR s.scan_id=c.scan_id) WHERE c.machine_identifier=$1 AND c.section_uuid=$4 AND c.rating_key=$5 AND c.media_id=$6 AND c.part_id=$7 AND c.subtitle_index=$8 AND c.scan_id <> $2`, a.machineIdentifier, request.ScanID, request.SectionKey, request.SectionUUID, request.RatingKey, request.MediaID, request.PartID, subtitleIndex)
		if err != nil {
			return errors.New("could not copy shared subtitle index")
		}
	}
	_, err = tx.Exec(ctx, `INSERT INTO subtitle_shared_sources (machine_identifier, section_uuid, section_key, rating_key, media_id, part_id, subtitle_index, fingerprint, chunk_count, scan_id, indexed_at) SELECT $1,$2,$3,$4,$5,$6,$7,$8,COUNT(*),$9,NOW() FROM subtitle_shared_chunks WHERE machine_identifier=$1 AND section_uuid=$2 AND scan_id=$9 AND rating_key=$4 AND media_id=$5 AND part_id=$6 AND subtitle_index=$7 GROUP BY machine_identifier, section_uuid, rating_key, media_id, part_id, subtitle_index ON CONFLICT (machine_identifier, section_uuid, scan_id, rating_key, media_id, part_id, subtitle_index) DO UPDATE SET fingerprint=EXCLUDED.fingerprint, chunk_count=EXCLUDED.chunk_count, section_key=EXCLUDED.section_key, indexed_at=EXCLUDED.indexed_at`, a.machineIdentifier, request.SectionUUID, request.SectionKey, request.RatingKey, request.MediaID, request.PartID, subtitleIndex, fingerprint, request.ScanID)
	if err != nil {
		return errors.New("could not record shared subtitle source")
	}
	return tx.Commit(ctx)
}

func (a *Application) recordSharedSubtitleSeen(ctx context.Context, request subtitleSearchIndexRequest, subtitleIndex int, fingerprint string, chunkCount int) error {
	_, err := a.subtitleSearch.pool.Exec(ctx, `INSERT INTO subtitle_shared_sources (machine_identifier, section_uuid, section_key, rating_key, media_id, part_id, subtitle_index, fingerprint, chunk_count, scan_id, indexed_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,NOW()) ON CONFLICT (machine_identifier, section_uuid, scan_id, rating_key, media_id, part_id, subtitle_index) DO UPDATE SET fingerprint=EXCLUDED.fingerprint, section_key=EXCLUDED.section_key, chunk_count=EXCLUDED.chunk_count, indexed_at=EXCLUDED.indexed_at`, a.machineIdentifier, request.SectionUUID, request.SectionKey, request.RatingKey, request.MediaID, request.PartID, subtitleIndex, fingerprint, chunkCount, request.ScanID)
	return err
}

func subtitleEmbeddingInput(prefix, dialogue string) string {
	if prefix == "" {
		return "Dialogue: " + dialogue
	}
	return prefix + "\nDialogue: " + dialogue
}

func subtitleSearchOwner(ctx context.Context) (string, error) {
	user := UserFromContext(ctx)
	if user == nil || strings.TrimSpace(user.Uuid) == "" {
		return "", errors.New("authenticated user is unavailable")
	}
	return strings.TrimSpace(user.Uuid), nil
}

type subtitleSearchHit struct {
	RatingKey     string  `json:"ratingKey"`
	MediaID       int64   `json:"mediaId"`
	PartID        int64   `json:"partId"`
	SubtitleIndex int     `json:"subtitleIndex"`
	Title         string  `json:"title"`
	ShowTitle     string  `json:"showTitle,omitempty"`
	Season        *int    `json:"season,omitempty"`
	Episode       *int    `json:"episode,omitempty"`
	Year          *int    `json:"year,omitempty"`
	StartMs       int64   `json:"startMs"`
	EndMs         int64   `json:"endMs"`
	Text          string  `json:"text"`
	Score         float64 `json:"score"`
}

type subtitleSearchCandidate struct {
	Hit      subtitleSearchHit
	Shared   sharedSubtitleCandidate
	Tier     int // 0 literal, 1 phrase, 2 semantic
	Rank     int
	IsShared bool
}

type subtitleSearchIdentity struct {
	MachineIdentifier string
	SectionUUID       string
	ScanID            string
	RatingKey         string
	MediaID           int64
	PartID            int64
	SubtitleIndex     int
	StartMs           int64
	EndMs             int64
}

func subtitleSearchIdentityForHit(hit subtitleSearchHit) subtitleSearchIdentity {
	return subtitleSearchIdentity{RatingKey: hit.RatingKey, MediaID: hit.MediaID, PartID: hit.PartID, SubtitleIndex: hit.SubtitleIndex, StartMs: hit.StartMs, EndMs: hit.EndMs}
}

func subtitleSearchIdentityForCandidate(candidate subtitleSearchCandidate) subtitleSearchIdentity {
	if candidate.IsShared {
		return subtitleSearchIdentity{MachineIdentifier: candidate.Shared.MachineIdentifier, SectionUUID: candidate.Shared.SectionUUID, ScanID: candidate.Shared.ScanID, RatingKey: candidate.Shared.RatingKey, MediaID: candidate.Shared.MediaID, PartID: candidate.Shared.PartID, SubtitleIndex: candidate.Shared.SubtitleIndex, StartMs: candidate.Shared.StartMs, EndMs: candidate.Shared.EndMs}
	}
	return subtitleSearchIdentityForHit(candidate.Hit)
}

func normalizeSubtitleSearchLiteral(value string) string {
	var out strings.Builder
	for _, r := range strings.ToLower(value) {
		if unicode.IsLetter(r) || unicode.IsNumber(r) {
			out.WriteRune(r)
		} else {
			out.WriteByte(' ')
		}
	}
	return strings.Join(strings.Fields(out.String()), " ")
}

func mergeSubtitleSearchCandidates(candidates []subtitleSearchCandidate) []subtitleSearchCandidate {
	best := make(map[subtitleSearchIdentity]subtitleSearchCandidate, len(candidates))
	for _, candidate := range candidates {
		identity := subtitleSearchIdentityForCandidate(candidate)
		previous, ok := best[identity]
		if !ok || candidate.Tier < previous.Tier || (candidate.Tier == previous.Tier && candidate.Rank < previous.Rank) {
			best[identity] = candidate
		}
	}
	merged := make([]subtitleSearchCandidate, 0, len(best))
	for _, candidate := range best {
		merged = append(merged, candidate)
	}
	sort.SliceStable(merged, func(i, j int) bool {
		if merged[i].Tier != merged[j].Tier {
			return merged[i].Tier < merged[j].Tier
		}
		if merged[i].Rank != merged[j].Rank {
			return merged[i].Rank < merged[j].Rank
		}
		left := subtitleSearchIdentityForCandidate(merged[i])
		right := subtitleSearchIdentityForCandidate(merged[j])
		return fmt.Sprintf("%s|%s|%s|%d|%d|%d|%d|%d", left.MachineIdentifier, left.SectionUUID, left.RatingKey, left.MediaID, left.PartID, left.SubtitleIndex, left.StartMs, left.EndMs) < fmt.Sprintf("%s|%s|%s|%d|%d|%d|%d|%d", right.MachineIdentifier, right.SectionUUID, right.RatingKey, right.MediaID, right.PartID, right.SubtitleIndex, right.StartMs, right.EndMs)
	})
	return merged
}

func diversifyHybridSubtitleSearchCandidates(candidates []subtitleSearchCandidate, limit int) []subtitleSearchCandidate {
	if limit <= 0 {
		return []subtitleSearchCandidate{}
	}
	// Keep exact literal results protected from semantic diversity. The small
	// literal allowances prevent a broken/duplicated index from consuming the
	// entire result window, while still allowing more than one matching cue.
	const (
		literalSourceAllowance = 3
		literalTitleAllowance  = 5
		remainderSourceQuota   = 3
		remainderTitleQuota    = 5
	)
	result := make([]subtitleSearchCandidate, 0, minInt(limit, len(candidates)))
	literalSources := make(map[string]int)
	literalTitles := make(map[string]int)
	remainderSources := make(map[string]int)
	remainderTitles := make(map[string]int)

	selectCandidate := func(candidate subtitleSearchCandidate, sourceCounts, titleCounts map[string]int, sourceQuota, titleQuota int) bool {
		if len(result) >= limit {
			return false
		}
		sourceKey := subtitleSearchCandidateSourceKey(candidate)
		titleKey := subtitleSearchCandidateTitleKey(candidate)
		if sourceCounts[sourceKey] >= sourceQuota || titleCounts[titleKey] >= titleQuota {
			return false
		}
		result = append(result, candidate)
		sourceCounts[sourceKey]++
		titleCounts[titleKey]++
		return true
	}

	// The merged pool is tier/rank ordered. Walk each tier in that order and
	// keep scanning after a quota rejection so a later title can fill the slot.
	for _, candidate := range candidates {
		if candidate.Tier != 0 {
			continue
		}
		selectCandidate(candidate, literalSources, literalTitles, literalSourceAllowance, literalTitleAllowance)
		if len(result) >= limit {
			return result
		}
	}
	for tier := 1; tier <= 2 && len(result) < limit; tier++ {
		for _, candidate := range candidates {
			if candidate.Tier != tier {
				continue
			}
			selectCandidate(candidate, remainderSources, remainderTitles, remainderSourceQuota, remainderTitleQuota)
			if len(result) >= limit {
				return result
			}
		}
	}
	return result
}

// sharedSubtitleCandidate deliberately contains no presentation fields.  The
// shared corpus is untrusted with respect to the current caller, so the vector
// query may disclose only the coordinates needed to authorize a candidate.
type sharedSubtitleCandidate struct {
	MachineIdentifier string
	SectionUUID       string
	SectionKey        string
	ScanID            string
	SectionType       string
	RatingKey         string
	MediaID           int64
	PartID            int64
	SubtitleIndex     int
	StartMs           int64
	EndMs             int64
	Score             float64
	Tier              int
	Rank              int
}

type sharedSubtitleSourceKey struct {
	MachineIdentifier string
	SectionUUID       string
	ScanID            string
	RatingKey         string
	MediaID           int64
	PartID            int64
}

// authorizeSharedSubtitleCandidates is a small seam for the security
// boundary: each source is checked once, while every candidate is retained
// only if that source check succeeded.  A checker error is systemic and must
// fail the whole search closed; a false result is an ordinary source denial.
func authorizeSharedSubtitleCandidates(ctx context.Context, candidates []sharedSubtitleCandidate, check func(context.Context, sharedSubtitleCandidate) (bool, error)) ([]sharedSubtitleCandidate, error) {
	if check == nil {
		return nil, errors.New("shared subtitle authorization is unavailable")
	}
	checked := make(map[sharedSubtitleSourceKey]bool)
	authorized := make([]sharedSubtitleCandidate, 0, minInt(maxSharedSubtitleAuthorizedHits, len(candidates)))
	for _, candidate := range candidates {
		if strings.TrimSpace(candidate.MachineIdentifier) == "" || strings.TrimSpace(candidate.SectionUUID) == "" {
			continue
		}
		key := sharedSubtitleSourceKey{candidate.MachineIdentifier, candidate.SectionUUID, candidate.ScanID, candidate.RatingKey, candidate.MediaID, candidate.PartID}
		allowed, ok := checked[key]
		if !ok {
			if len(checked) >= maxSharedSubtitleSourceChecks {
				break
			}
			var err error
			allowed, err = check(ctx, candidate)
			if err != nil {
				return nil, err
			}
			checked[key] = allowed
		}
		if allowed {
			authorized = append(authorized, candidate)
			if len(authorized) >= maxSharedSubtitleAuthorizedHits {
				break
			}
		}
	}
	return authorized, nil
}

func authorizeAndFetchSharedSubtitleHits(ctx context.Context, candidates []sharedSubtitleCandidate, check func(context.Context, sharedSubtitleCandidate) (bool, error), fetch func(context.Context, sharedSubtitleCandidate) (subtitleSearchHit, error)) ([]subtitleSearchHit, error) {
	authorized, err := authorizeSharedSubtitleCandidates(ctx, candidates, check)
	if err != nil {
		return nil, err
	}
	hits := make([]subtitleSearchHit, 0, len(authorized))
	for _, candidate := range authorized {
		hit, err := fetch(ctx, candidate)
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, err
		}
		hits = append(hits, hit)
	}
	return hits, nil
}

func authorizeAndFetchSharedSubtitleCandidates(ctx context.Context, candidates []sharedSubtitleCandidate, check func(context.Context, sharedSubtitleCandidate) (bool, error), fetch func(context.Context, sharedSubtitleCandidate) (subtitleSearchHit, error)) ([]subtitleSearchCandidate, error) {
	authorized, err := authorizeSharedSubtitleCandidates(ctx, candidates, check)
	if err != nil {
		return nil, err
	}
	result := make([]subtitleSearchCandidate, 0, len(authorized))
	for _, candidate := range authorized {
		hit, fetchErr := fetch(ctx, candidate)
		if errors.Is(fetchErr, pgx.ErrNoRows) {
			continue
		}
		if fetchErr != nil {
			return nil, fetchErr
		}
		result = append(result, subtitleSearchCandidate{Hit: hit, Shared: candidate, IsShared: true, Tier: candidate.Tier, Rank: candidate.Rank})
	}
	return result, nil
}

func fetchAuthorizedSharedSubtitleCandidates(ctx context.Context, candidates []sharedSubtitleCandidate, fetch func(context.Context, sharedSubtitleCandidate) (subtitleSearchHit, error)) ([]subtitleSearchCandidate, error) {
	result := make([]subtitleSearchCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		hit, err := fetch(ctx, candidate)
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, err
		}
		result = append(result, subtitleSearchCandidate{Hit: hit, Shared: candidate, IsShared: true, Tier: candidate.Tier, Rank: candidate.Rank})
	}
	return result, nil
}

// diversifySubtitleSearchHits applies a soft, deterministic source-diversity
// preference to the already relevance-ordered candidate window. A source is
// never capped: each previously selected hit from that source adds a bounded
// penalty to its next selection, so sufficiently relevant hits can still
// occupy as much of the result window as warranted.
//
// The variadic argument is retained for source compatibility with the initial
// implementation, where it was a hard per-source limit. It is intentionally
// ignored; diversity is a ranking preference, not a filter.
func diversifySubtitleSearchHits(candidates []subtitleSearchHit, limit int, _ ...int) []subtitleSearchHit {
	if limit <= 0 {
		return []subtitleSearchHit{}
	}
	selected := make([]subtitleSearchHit, 0, minInt(limit, len(candidates)))
	counts := make(map[string]int)
	used := make([]bool, len(candidates))
	const (
		diversityPenaltyStep = 0.20
		diversityPenaltyMax  = 0.60
	)
	for len(selected) < limit && len(selected) < len(candidates) {
		bestIndex := -1
		bestAdjustedScore := 0.0
		for index, candidate := range candidates {
			if used[index] {
				continue
			}
			key := subtitleSearchHitSourceKey(candidate)
			penalty := float64(counts[key]) * diversityPenaltyStep
			if penalty > diversityPenaltyMax {
				penalty = diversityPenaltyMax
			}
			adjustedScore := candidate.Score - penalty
			// Keeping the first candidate on ties makes the ranking stable
			// relative to the database's relevance ordering.
			if bestIndex < 0 || adjustedScore > bestAdjustedScore {
				bestIndex = index
				bestAdjustedScore = adjustedScore
			}
		}
		if bestIndex < 0 {
			break
		}
		used[bestIndex] = true
		selected = append(selected, candidates[bestIndex])
		counts[subtitleSearchHitSourceKey(candidates[bestIndex])]++
	}
	return selected
}

func subtitleSearchHitSourceKey(hit subtitleSearchHit) string {
	return fmt.Sprintf("%s:%d:%d", hit.RatingKey, hit.MediaID, hit.PartID)
}

func subtitleSearchCandidateSourceKey(candidate subtitleSearchCandidate) string {
	if candidate.IsShared {
		ratingKey := candidate.Shared.RatingKey
		if ratingKey == "" {
			ratingKey = candidate.Hit.RatingKey
		}
		return fmt.Sprintf("shared:%s:%s:%s", candidate.Shared.MachineIdentifier, candidate.Shared.SectionUUID, ratingKey)
	}
	return fmt.Sprintf("local:%s", candidate.Hit.RatingKey)
}

func subtitleSearchCandidateTitleKey(candidate subtitleSearchCandidate) string {
	hit := candidate.Hit
	key := "title:" + normalizeSubtitleSearchLiteral(hit.Title)
	showTitle := normalizeSubtitleSearchLiteral(hit.ShowTitle)
	if showTitle != "" {
		key += "|show:" + showTitle
	}
	if hit.Season != nil {
		key += fmt.Sprintf("|season:%d", *hit.Season)
	}
	if hit.Episode != nil {
		key += fmt.Sprintf("|episode:%d", *hit.Episode)
	}
	// Year separates same-named movie remakes without splitting versions of
	// the same catalog item, whose metadata normally has the same year.
	if showTitle == "" && hit.Year != nil {
		key += fmt.Sprintf("|year:%d", *hit.Year)
	}
	return key
}

// scanSubtitleSearchHit keeps nullable catalog metadata out of the pgx scan
// destinations. Plex metadata is legitimately incomplete for some movies and
// episodes, and PostgreSQL represents those fields as NULL.
func scanSubtitleSearchHit(scan func(...any) error) (subtitleSearchHit, error) {
	var hit subtitleSearchHit
	var showTitle sql.NullString
	var season, episode, year sql.NullInt64
	if err := scan(&hit.RatingKey, &hit.MediaID, &hit.PartID, &hit.SubtitleIndex, &hit.Title, &showTitle, &season, &episode, &year, &hit.StartMs, &hit.EndMs, &hit.Text, &hit.Score); err != nil {
		return subtitleSearchHit{}, err
	}
	if showTitle.Valid {
		hit.ShowTitle = showTitle.String
	}
	if season.Valid {
		value := int(season.Int64)
		hit.Season = &value
	}
	if episode.Valid {
		value := int(episode.Int64)
		hit.Episode = &value
	}
	if year.Valid {
		value := int(year.Int64)
		hit.Year = &value
	}
	return hit, nil
}

func scanSharedSubtitleCandidate(scan func(...any) error) (sharedSubtitleCandidate, error) {
	var candidate sharedSubtitleCandidate
	if err := scan(&candidate.MachineIdentifier, &candidate.SectionUUID, &candidate.SectionKey, &candidate.ScanID, &candidate.SectionType, &candidate.RatingKey, &candidate.MediaID, &candidate.PartID, &candidate.SubtitleIndex, &candidate.StartMs, &candidate.EndMs, &candidate.Score); err != nil {
		return sharedSubtitleCandidate{}, err
	}
	if strings.TrimSpace(candidate.SectionUUID) == "" || strings.TrimSpace(candidate.ScanID) == "" || !validSharedSectionKey(candidate.SectionKey) {
		return sharedSubtitleCandidate{}, errors.New("shared subtitle candidate has invalid section identity")
	}
	return candidate, nil
}

func (a *Application) searchSubtitleIndex(ctx context.Context, query string) ([]subtitleSearchHit, error) {
	if a.subtitleSearch == nil {
		return nil, errSubtitleSearchDisabled
	}
	owner, err := subtitleSearchOwner(ctx)
	if err != nil {
		return nil, err
	}
	if a.sharedCorpus {
		return a.searchSharedSubtitleIndex(ctx, query)
	}
	trimmedQuery := strings.TrimSpace(query)
	if trimmedQuery == "" {
		return []subtitleSearchHit{}, nil
	}
	normalized := normalizeSubtitleSearchLiteral(trimmedQuery)
	candidates := make([]subtitleSearchCandidate, 0, maxSubtitleLexicalCandidates*2+maxSubtitleSemanticCandidates)
	load := func(statement string, args []any, tier int) error {
		rows, err := a.subtitleSearch.pool.Query(ctx, statement, args...)
		if err != nil {
			return errors.New("could not search subtitle index")
		}
		defer rows.Close()
		rank := 0
		for rows.Next() {
			hit, scanErr := scanSubtitleSearchHit(rows.Scan)
			if scanErr != nil {
				return errors.New("could not read subtitle index")
			}
			candidates = append(candidates, subtitleSearchCandidate{Hit: hit, Tier: tier, Rank: rank})
			rank++
		}
		if err := rows.Err(); err != nil {
			return errors.New("could not read subtitle index")
		}
		return nil
	}
	if normalized != "" {
		if err := load(`SELECT rating_key, media_id, part_id, subtitle_index, title, show_title, season, episode, year, start_ms, end_ms, text, 1.0 AS score FROM subtitle_chunks WHERE owner_uuid=$1 AND strpos(btrim(regexp_replace(lower(text), '[^[:alnum:]]+', ' ', 'g')), $2) > 0 ORDER BY id LIMIT $3`, []any{owner, normalized, maxSubtitleLexicalCandidates}, 0); err != nil {
			return nil, err
		}
	}
	if err := load(`SELECT rating_key, media_id, part_id, subtitle_index, title, show_title, season, episode, year, start_ms, end_ms, text, ts_rank_cd(text_search, phraseto_tsquery('simple', $2)) AS score FROM subtitle_chunks WHERE owner_uuid=$1 AND text_search @@ phraseto_tsquery('simple', $2) ORDER BY score DESC, id LIMIT $3`, []any{owner, trimmedQuery, maxSubtitleLexicalCandidates}, 1); err != nil {
		return nil, err
	}
	embeddings, err := a.subtitleSearch.embeddings.embed(ctx, []string{a.subtitleSearch.queryInput(trimmedQuery)})
	if err != nil {
		return nil, err
	}
	if err := load(`SELECT rating_key, media_id, part_id, subtitle_index, title, show_title, season, episode, year, start_ms, end_ms, text, 1-(embedding <=> $1) AS score FROM subtitle_chunks WHERE owner_uuid=$2 ORDER BY embedding <=> $1 LIMIT $3`, []any{pgvector.NewVector(embeddings[0]), owner, maxSubtitleSemanticCandidates}, 2); err != nil {
		return nil, err
	}
	merged := mergeSubtitleSearchCandidates(candidates)
	result := diversifyHybridSubtitleSearchCandidates(merged, 50)
	hits := make([]subtitleSearchHit, 0, len(result))
	for _, candidate := range result {
		hits = append(hits, candidate.Hit)
	}
	return hits, nil
}

func (a *Application) searchSharedSubtitleIndex(ctx context.Context, query string) ([]subtitleSearchHit, error) {
	if a.subtitleSearch == nil {
		return nil, errSubtitleSearchDisabled
	}
	if a.plexResources == nil || strings.TrimSpace(a.machineIdentifier) == "" {
		return nil, errors.New("shared subtitle search is unavailable")
	}
	trimmedQuery := strings.TrimSpace(query)
	if trimmedQuery == "" {
		return []subtitleSearchHit{}, nil
	}
	access, resolver, err := a.callerPlexAccess(ctx, "")
	if err != nil {
		return nil, errors.New("shared subtitle visibility is unavailable")
	}
	sections, err := a.indexLibrarySections(ContextWithPlexAccess(ctx, access), "")
	if err != nil {
		return nil, errors.New("shared subtitle visibility is unavailable")
	}
	sectionTypes := make(map[string]string, len(sections))
	sectionKeys := make(map[string]string, len(sections))
	for _, section := range sections {
		if validSharedSectionKey(section.Key) && strings.TrimSpace(section.UUID) != "" && (section.Type == "movie" || section.Type == "show") {
			sectionTypes[section.UUID] = section.Type
			sectionKeys[section.UUID] = section.Key
		}
	}
	if len(sectionTypes) == 0 {
		return []subtitleSearchHit{}, nil
	}
	uuidKeys := make([]string, 0, len(sectionTypes))
	for sectionUUID := range sectionTypes {
		uuidKeys = append(uuidKeys, sectionUUID)
	}
	candidates := make([]sharedSubtitleCandidate, 0, maxSharedSubtitleCandidates*3)
	load := func(statement string, args []any, tier int) error {
		rows, queryErr := a.subtitleSearch.pool.Query(ctx, statement, args...)
		if queryErr != nil {
			return errors.New("could not search subtitle index")
		}
		defer rows.Close()
		rank := 0
		for rows.Next() {
			candidate, scanErr := scanSharedSubtitleCandidate(rows.Scan)
			if scanErr != nil {
				return errors.New("could not read subtitle index")
			}
			candidate.Tier, candidate.Rank = tier, rank
			candidates = append(candidates, candidate)
			rank++
		}
		if rowsErr := rows.Err(); rowsErr != nil {
			return errors.New("could not read subtitle index")
		}
		return nil
	}
	normalized := normalizeSubtitleSearchLiteral(trimmedQuery)
	const sharedChunksJoin = `FROM subtitle_shared_chunks c JOIN subtitle_shared_sections s ON s.machine_identifier=c.machine_identifier AND s.section_uuid=c.section_uuid AND s.state<>'failed' AND (c.scan_id=s.scan_id OR (s.ready_scan_id IS NOT NULL AND c.scan_id=s.ready_scan_id AND NOT EXISTS (SELECT 1 FROM subtitle_shared_chunks n WHERE n.machine_identifier=c.machine_identifier AND n.section_uuid=c.section_uuid AND n.scan_id=s.scan_id AND n.rating_key=c.rating_key AND n.media_id=c.media_id AND n.part_id=c.part_id AND n.subtitle_index=c.subtitle_index)))`
	if normalized != "" {
		if err := load(`SELECT c.machine_identifier, c.section_uuid, c.section_key, c.scan_id, s.section_type, c.rating_key, c.media_id, c.part_id, c.subtitle_index, c.start_ms, c.end_ms, 1.0 AS score `+sharedChunksJoin+` WHERE c.machine_identifier=$1 AND c.section_uuid=ANY($2) AND strpos(btrim(regexp_replace(lower(c.text), '[^[:alnum:]]+', ' ', 'g')), $3) > 0 AND EXISTS (SELECT 1 FROM subtitle_shared_sources v WHERE v.machine_identifier=c.machine_identifier AND v.section_uuid=c.section_uuid AND v.scan_id=c.scan_id AND v.rating_key=c.rating_key AND v.media_id=c.media_id AND v.part_id=c.part_id AND v.subtitle_index=c.subtitle_index AND v.chunk_count > 0) ORDER BY c.id LIMIT $4`, []any{a.machineIdentifier, uuidKeys, normalized, maxSharedSubtitleCandidates}, 0); err != nil {
			return nil, err
		}
	}
	if err := load(`SELECT c.machine_identifier, c.section_uuid, c.section_key, c.scan_id, s.section_type, c.rating_key, c.media_id, c.part_id, c.subtitle_index, c.start_ms, c.end_ms, ts_rank_cd(c.text_search, phraseto_tsquery('simple', $3)) AS score `+sharedChunksJoin+` WHERE c.machine_identifier=$1 AND c.section_uuid=ANY($2) AND c.text_search @@ phraseto_tsquery('simple', $3) AND EXISTS (SELECT 1 FROM subtitle_shared_sources v WHERE v.machine_identifier=c.machine_identifier AND v.section_uuid=c.section_uuid AND v.scan_id=c.scan_id AND v.rating_key=c.rating_key AND v.media_id=c.media_id AND v.part_id=c.part_id AND v.subtitle_index=c.subtitle_index AND v.chunk_count > 0) ORDER BY score DESC, c.id LIMIT $4`, []any{a.machineIdentifier, uuidKeys, trimmedQuery, maxSharedSubtitleCandidates}, 1); err != nil {
		return nil, err
	}
	embeddings, err := a.subtitleSearch.embeddings.embed(ctx, []string{a.subtitleSearch.queryInput(trimmedQuery)})
	if err != nil {
		return nil, err
	}
	// This first corpus query is intentionally limited to source identity and
	// relevance data.  Subtitle text is fetched only after the caller checks
	// below have completed successfully.
	if err := load(`SELECT c.machine_identifier, c.section_uuid, c.section_key, c.scan_id, s.section_type, c.rating_key, c.media_id, c.part_id, c.subtitle_index, c.start_ms, c.end_ms, 1-(c.embedding <=> $1) AS score `+sharedChunksJoin+` WHERE c.machine_identifier=$2 AND c.section_uuid=ANY($3) AND EXISTS (SELECT 1 FROM subtitle_shared_sources v WHERE v.machine_identifier=c.machine_identifier AND v.section_uuid=c.section_uuid AND v.scan_id=c.scan_id AND v.rating_key=c.rating_key AND v.media_id=c.media_id AND v.part_id=c.part_id AND v.subtitle_index=c.subtitle_index AND v.chunk_count > 0) ORDER BY c.embedding <=> $1 LIMIT $4`, []any{pgvector.NewVector(embeddings[0]), a.machineIdentifier, uuidKeys, maxSharedSubtitleCandidates}, 2); err != nil {
		return nil, err
	}
	mergedRaw := make([]subtitleSearchCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		mergedRaw = append(mergedRaw, subtitleSearchCandidate{Shared: candidate, IsShared: true, Tier: candidate.Tier, Rank: candidate.Rank})
	}
	merged := mergeSubtitleSearchCandidates(mergedRaw)
	authorizedCandidates := make([]sharedSubtitleCandidate, 0, len(merged))
	for _, candidate := range merged {
		authorizedCandidates = append(authorizedCandidates, candidate.Shared)
	}
	verifiedContext := ContextWithPlexAccess(ctx, access)
	authorizationContext, cancelAuthorization := context.WithTimeout(verifiedContext, sharedSubtitleAuthorizationTimeout)
	defer cancelAuthorization()
	authorizedCandidates, err = a.authorizeSharedSubtitleCandidatesBySection(authorizationContext, resolver, authorizedCandidates, sectionTypes, sectionKeys)
	if err != nil {
		return nil, errors.New("shared subtitle visibility is unavailable")
	}
	fetched, err := fetchAuthorizedSharedSubtitleCandidates(authorizationContext, authorizedCandidates, a.fetchAuthorizedSharedSubtitleHit)
	if err != nil {
		return nil, errors.New("shared subtitle visibility is unavailable")
	}
	resultCandidates := make([]subtitleSearchCandidate, 0, len(fetched))
	for _, candidate := range fetched {
		resultCandidates = append(resultCandidates, candidate)
	}
	resultCandidates = diversifyHybridSubtitleSearchCandidates(resultCandidates, 50)
	hits := make([]subtitleSearchHit, 0, len(resultCandidates))
	for _, candidate := range resultCandidates {
		hits = append(hits, candidate.Hit)
	}
	return hits, nil
}

func isSharedSourceRejection(err error) bool {
	if err == nil {
		return false
	}
	var validationErr *sourceValidationError
	if errors.As(err, &validationErr) {
		return true
	}
	if errors.Is(err, errPlexAccessDenied) {
		return true
	}
	status := plexMetadataStatus(err)
	return status == http.StatusBadRequest || status == http.StatusUnauthorized || status == http.StatusForbidden || status == http.StatusNotFound || status == http.StatusGone || status >= 300 && status < 400
}

func sharedSubtitleSourceCoordinate(candidate sharedSubtitleCandidate) string {
	return fmt.Sprintf("%s:%s:%d:%d", candidate.SectionUUID, candidate.RatingKey, candidate.MediaID, candidate.PartID)
}

func (a *Application) callerSectionSourceSet(ctx context.Context, resolver *PlexResourceResolver, sectionKey, sectionType string) (map[string]struct{}, int, int, error) {
	access := PlexAccessFromContext(ctx)
	if access == nil || resolver == nil || !validSharedSectionKey(sectionKey) {
		return nil, 0, 0, errors.New("shared subtitle visibility is unavailable")
	}
	typeValue := map[string]string{"movie": "1", "show": "4"}[sectionType]
	if typeValue == "" {
		return nil, 0, 0, errors.New("shared subtitle visibility is unavailable")
	}
	sources := make(map[string]struct{})
	seenPages := make(map[string]bool)
	previousStart := -1
	itemsSeen := 0
	for pageCount, start := 1, 0; pageCount <= maxSharedSubtitleAuthorizationPages; pageCount++ {
		if start == previousStart {
			return nil, pageCount, itemsSeen, errors.New("shared subtitle visibility is unavailable")
		}
		previousStart = start
		values := url.Values{}
		values.Set("type", typeValue)
		values.Set("X-Plex-Container-Start", strconv.Itoa(start))
		values.Set("X-Plex-Container-Size", strconv.Itoa(indexLibraryPageSize))
		path := "/library/sections/" + url.PathEscape(sectionKey) + "/all?" + values.Encode()
		request, err := newPlexRequest(ctx, access, http.MethodGet, path)
		if err != nil {
			return nil, pageCount, itemsSeen, errors.New("shared subtitle visibility is unavailable")
		}
		request.Header.Set("Accept", "application/json")
		request.Header.Set("X-Plex-Accept", "application/json")
		response, err := resolver.DoPlexRequest(ctx, access, request)
		if err != nil {
			return nil, pageCount, itemsSeen, errors.New("shared subtitle visibility is unavailable")
		}
		if response == nil {
			return nil, pageCount, itemsSeen, errors.New("shared subtitle visibility is unavailable")
		}
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			response.Body.Close()
			return nil, pageCount, itemsSeen, errors.New("shared subtitle visibility is unavailable")
		}
		if err := validatePlexJSONResponse(response); err != nil {
			response.Body.Close()
			return nil, pageCount, itemsSeen, errors.New("shared subtitle visibility is unavailable")
		}
		var page plexMetadataResponse
		decodeErr := readBoundedJSON(response.Body, &page)
		response.Body.Close()
		if decodeErr != nil || page.MediaContainer == nil {
			return nil, pageCount, itemsSeen, errors.New("shared subtitle visibility is unavailable")
		}
		if page.MediaContainer.Offset != 0 && page.MediaContainer.Offset != start {
			return nil, pageCount, itemsSeen, errors.New("shared subtitle visibility is unavailable")
		}
		items := page.MediaContainer.Metadata
		itemsSeen += len(items)
		if itemsSeen > maxSharedSubtitleAuthorizationItems {
			return nil, pageCount, itemsSeen, errors.New("shared subtitle authorization budget exceeded")
		}
		if len(items) > 0 {
			pageKey := stringValue(items[0].RatingKey) + ":" + stringValue(items[len(items)-1].RatingKey)
			if seenPages[pageKey] {
				return sources, pageCount, itemsSeen, nil
			}
			seenPages[pageKey] = true
		}
		for i := range items {
			item := &items[i]
			if item.Type != "movie" && item.Type != "episode" {
				continue
			}
			media, part, err := selectLibraryMetadataSourceForDiscovery(item)
			if err != nil || media == nil || part == nil {
				continue
			}
			sources[fmt.Sprintf("%s:%d:%d", stringValue(item.RatingKey), media.ID, part.ID)] = struct{}{}
		}
		if len(items) == 0 || len(items) < indexLibraryPageSize {
			return sources, pageCount, itemsSeen, nil
		}
		if page.MediaContainer.TotalSize > 0 && start+len(items) >= page.MediaContainer.TotalSize {
			return sources, pageCount, itemsSeen, nil
		}
		start += len(items)
	}
	return nil, maxSharedSubtitleAuthorizationPages, itemsSeen, errors.New("shared subtitle authorization page budget exceeded")
}

func (a *Application) authorizeSharedSubtitleCandidatesBySection(ctx context.Context, resolver *PlexResourceResolver, candidates []sharedSubtitleCandidate, sectionTypes, sectionKeys map[string]string) ([]sharedSubtitleCandidate, error) {
	if len(candidates) == 0 {
		return []sharedSubtitleCandidate{}, nil
	}
	bySection := make(map[string][]sharedSubtitleCandidate)
	for _, candidate := range candidates {
		if candidate.MachineIdentifier != a.machineIdentifier || strings.TrimSpace(candidate.SectionUUID) == "" || strings.TrimSpace(candidate.ScanID) == "" || !validSharedSectionKey(candidate.SectionKey) {
			continue
		}
		if sectionTypes[candidate.SectionUUID] != candidate.SectionType || sectionKeys[candidate.SectionUUID] == "" {
			continue
		}
		bySection[candidate.SectionUUID] = append(bySection[candidate.SectionUUID], candidate)
	}
	if len(bySection) > maxSharedSubtitleAuthorizationSections {
		return nil, errors.New("shared subtitle authorization budget exceeded")
	}
	checked := make(map[string]bool)
	allowed := make(map[string]bool)
	sectionSources := make(map[string]map[string]struct{}, len(bySection))
	totalPages, totalItems := 0, 0
	for sectionUUID, sectionCandidates := range bySection {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		sources, pages, items, err := a.callerSectionSourceSet(ctx, resolver, sectionKeys[sectionUUID], sectionTypes[sectionUUID])
		if err != nil {
			return nil, err
		}
		totalPages += pages
		totalItems += items
		if totalPages > maxSharedSubtitleAuthorizationPages || totalItems > maxSharedSubtitleAuthorizationItems {
			return nil, errors.New("shared subtitle authorization budget exceeded")
		}
		sectionSources[sectionUUID] = sources
		for _, candidate := range sectionCandidates {
			key := sharedSubtitleSourceCoordinate(candidate)
			if _, seen := checked[key]; seen {
				continue
			}
			if len(checked) >= maxSharedSubtitleSourceChecks {
				return nil, errors.New("shared subtitle authorization budget exceeded")
			}
			checked[key] = true
			if _, visible := sources[fmt.Sprintf("%s:%d:%d", candidate.RatingKey, candidate.MediaID, candidate.PartID)]; !visible {
				allowed[key] = false
				continue
			}
			if _, err := a.GetLibrarySource(ctx, candidate.RatingKey, candidate.MediaID, candidate.PartID); err != nil {
				if isSharedSourceRejection(err) {
					allowed[key] = false
					continue
				}
				return nil, errors.New("shared subtitle visibility is unavailable")
			}
			allowed[key] = true
		}
	}
	result := make([]sharedSubtitleCandidate, 0, minInt(maxSharedSubtitleAuthorizedHits, len(candidates)))
	for _, candidate := range candidates {
		if allowed[sharedSubtitleSourceCoordinate(candidate)] {
			result = append(result, candidate)
			if len(result) >= maxSharedSubtitleAuthorizedHits {
				break
			}
		}
	}
	return result, nil
}

func (a *Application) fetchAuthorizedSharedSubtitleHit(ctx context.Context, candidate sharedSubtitleCandidate) (subtitleSearchHit, error) {
	var hit subtitleSearchHit
	var showTitle sql.NullString
	var season, episode, year sql.NullInt64
	err := a.subtitleSearch.pool.QueryRow(ctx, `SELECT c.title, c.show_title, c.season, c.episode, c.year, c.text FROM subtitle_shared_chunks c JOIN subtitle_shared_sections sec ON sec.machine_identifier=c.machine_identifier AND sec.section_uuid=c.section_uuid AND sec.state<>'failed' AND (sec.scan_id=c.scan_id OR (sec.ready_scan_id IS NOT NULL AND sec.ready_scan_id=c.scan_id)) JOIN subtitle_shared_sources src ON src.machine_identifier=c.machine_identifier AND src.section_uuid=c.section_uuid AND src.scan_id=c.scan_id AND src.rating_key=c.rating_key AND src.media_id=c.media_id AND src.part_id=c.part_id AND src.subtitle_index=c.subtitle_index AND src.chunk_count > 0 WHERE c.machine_identifier=$1 AND c.section_uuid=$2 AND c.section_key=$3 AND c.scan_id=$4 AND c.rating_key=$5 AND c.media_id=$6 AND c.part_id=$7 AND c.subtitle_index=$8 AND c.start_ms=$9 AND c.end_ms=$10`, candidate.MachineIdentifier, candidate.SectionUUID, candidate.SectionKey, candidate.ScanID, candidate.RatingKey, candidate.MediaID, candidate.PartID, candidate.SubtitleIndex, candidate.StartMs, candidate.EndMs).Scan(&hit.Title, &showTitle, &season, &episode, &year, &hit.Text)
	if err != nil {
		return subtitleSearchHit{}, err
	}
	hit.RatingKey, hit.MediaID, hit.PartID = candidate.RatingKey, candidate.MediaID, candidate.PartID
	hit.SubtitleIndex, hit.StartMs, hit.EndMs, hit.Score = candidate.SubtitleIndex, candidate.StartMs, candidate.EndMs, candidate.Score
	if showTitle.Valid {
		hit.ShowTitle = showTitle.String
	}
	if season.Valid {
		value := int(season.Int64)
		hit.Season = &value
	}
	if episode.Valid {
		value := int(episode.Int64)
		hit.Episode = &value
	}
	if year.Valid {
		value := int(year.Int64)
		hit.Year = &value
	}
	return hit, nil
}
