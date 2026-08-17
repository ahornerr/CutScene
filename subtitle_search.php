<?php




const (
	subtitleEmbeddingDimensions            = 768
	subtitleEmbeddingBatchSize             = 32
	maxSharedSubtitleCandidates            = 200
	maxSharedSubtitleSourceChecks          = 100
	maxSharedSubtitleAuthorizedHits        = 50
	sharedSubtitleAuthorizationTimeout     = 10 * $time->Second
	maxSharedSubtitleAuthorizationSections = 32
	maxSharedSubtitleAuthorizationPages    = 100
	maxSharedSubtitleAuthorizationItems    = 10000
	maxSubtitleLexicalCandidates           = 200
	maxSubtitleSemanticCandidates          = 200
	// Keep result windows focused enough to open directly as short clips.
	// Subtitle timing still determines the exact duration.
	subtitleChunkMaxRunes = 400
	bgeQueryInstruction   = "Represent this sentence for searching relevant passages: "
)

var errSubtitleSearchDisabled = $errors->New("semantic subtitle search is disabled")

var errSubtitleIndexItem = $errors->New("subtitle index item failure")

type subtitleIndexItemFailure struct{ err error }

public function Error() { return $e->err.Error() }
public function Unwrap() { return $e->err }
public function Is($target) {
	return target == errSubtitleIndexItem || $errors->Is($e->err, target)
}

function subtitleItemFailure($err) {
	if err == null {
		return null
	}
	return &subtitleIndexItemFailure{err: err}
}

type subtitleBulkContextKey struct{}

function withBulkSubtitleContext($$ctx->Context) {
	return $context->WithValue(ctx, subtitleBulkContextKey{}, true)
}

function isBulkSubtitleContext($$ctx->Context) {list($value, $_) = $ctx->Value(subtitleBulkContextKey{}).(bool)
	return value
}

function acquireSubtitleSemaphore($$ctx->Context, $semaphore struct{}) {
	select {
	case semaphore <- struct{}{}:
		return null
	case <-$ctx->Done():
		return $ctx->Err()
	}
}

function releaseSubtitleSemaphore($semaphore struct{}) {
	<-semaphore
}

// subtitleSearchStore owns the optional POC services. It is deliberately not
// constructed when $semantic_search->enabled is false, so existing deployments
// do not need PostgreSQL or TEI.
class subtitleSearchStore {    public $pool;
    public $embeddings;
    public $bulkEmbeddings;}
	bulkWrites     chan struct{}
	trackLocks     keyedSubtitleLocks
	jobMu          $sync->Mutex
}

class keyedSubtitleLocks {    public $mu;
    public $entries;
}

class keyedSubtitleLock {    public $mu;
    public $refs;
}

public function acquire($key) {
	$l->mu.Lock()
	if $l->entries == null {
		$l->entries = make(map[string]*keyedSubtitleLock)
	}$entry = $l->entries[key]
	if entry == null {
		entry = &keyedSubtitleLock{}
		$l->entries[key] = entry
	}
	$entry->refs++
	$l->mu.Unlock()
	$entry->mu.Lock()
	return func() {
		$entry->mu.Unlock()
		$l->mu.Lock()
		$entry->refs--
		if $entry->refs == 0 {
			delete($l->entries, key)
		}
		$l->mu.Unlock()
	}
}

const (
	embeddingsProviderTEI    = "tei"
	embeddingsProviderOpenAI = "openai"
	openAIEmbeddingModel     = "BAAI/bge-base-en-$v1->5"
)

const subtitleSearchSchema = `
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
    embedding vector(768) NOT NULL,
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
    embedding vector(768) NOT NULL,
    scan_id UUID NOT NULL,
    text_search TSVECTOR GENERATED ALWAYS AS (to_tsvector('simple', text)) STORED,
    UNIQUE (machine_identifier, section_uuid, scan_id, rating_key, media_id, part_id, subtitle_index, start_ms, end_ms, content_hash)
);
CREATE INDEX IF NOT EXISTS subtitle_shared_chunks_text_idx ON subtitle_shared_chunks USING GIN (text_search);
CREATE INDEX IF NOT EXISTS subtitle_shared_chunks_embedding_idx ON subtitle_shared_chunks USING hnsw (embedding vector_cosine_ops);
ALTER TABLE subtitle_shared_sections ADD COLUMN IF NOT EXISTS ready BOOLEAN NOT NULL DEFAULT FALSE;
`

const subtitleSearchIndexVersion = "subtitle-index-v4-chunk-400-cast-clean-text"

function newSubtitleSearchStore($$ctx->Context, $cfg) {list($provider, $err) = normalizeEmbeddingsProvider($cfg->EmbeddingsProvider)
	if $err !== null {
		return null, err
	}
	if $strings->TrimSpace($cfg->PostgresDSN) == "" {
		return null, $errors->New("$semantic_search->postgres_dsn is required when semantic search is enabled")
	}list($embeddings, $err) = newEmbeddingClient($cfg->EmbeddingsURL, provider, $cfg->EmbeddingsAPIKey)
	if $err !== null {
		return null, err
	}list($poolConfig, $err) = $pgxpool->ParseConfig($cfg->PostgresDSN)
	if $err !== null {
		return null, $errors->New("$semantic_search->postgres_dsn is invalid")
	}
	$poolConfig->MaxConns =list($4, $pool, $err) = $pgxpool->NewWithConfig(ctx, poolConfig)
	if $err !== null {
		return null, $errors->New("could not initialize semantic search database")
	}list($if, $err) = $pool->Ping(ctx); $err !== null {
		$pool->Close()
		return null, $errors->New("could not connect to semantic search database")
	}list($if, $_, $err) = $pool->Exec(ctx, subtitleSearchSchema); $err !== null {
		$pool->Close()
		return null, $errors->New("could not initialize semantic search schema")
	}
	$pool->Close()
	$poolConfig->AfterConnect = func(ctx $context->Context, conn *$pgx->Conn) error {
		return $pgxvector->RegisterTypes(ctx, conn)
	}
	pool, err = $pgxpool->NewWithConfig(ctx, poolConfig)
	if $err !== null {
		return null, $errors->New("could not initialize semantic search database")
	}list($if, $err) = $pool->Ping(ctx); $err !== null {
		$pool->Close()
		return null, $errors->New("could not connect to semantic search database")
	}
	return &subtitleSearchStore{pool: pool, embeddings: embeddings, bulkEmbeddings: make(chan struct{}, 1), bulkWrites: make(chan struct{}, 1)}, null
}

public function close() {
	if s != null && $s->pool != null {
		$s->pool.Close()
	}
}

class subtitleChunk {    public $Start;
    public $End;
    public $Text;
}

// normalizeSubtitleSearchText is intentionally narrower than a general HTML
// parser: subtitle markup is presentation-only. It removes HTML/SSA tags,
// decodes entities, and keeps meaningful line boundaries without allowing raw
// markup into embeddings or PostgreSQL full-text search.
function normalizeSubtitleSearchText($value) {
	value = $strings->ReplaceAll(value, "\\N", "\n")
	value = $strings->ReplaceAll(value, "\\n", "\n")
	$out = null;.list($Builder, $inTag, $inBrace) = false, falselist($for, $_, $r) = range value {
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
			$out->WriteRune(r)
		}
	}
	value = $html->UnescapeString($out->String())$lines = $strings->Split(value, "\n")list($for, $i) = range lines {
		lines[i] = $strings->Join($strings->Fields(lines[i]), " ")
	}
	return $strings->TrimSpace($strings->Join(lines, "\n"))
}

function chunkSubtitleEntries($entries) {$chunks = make([]subtitleChunk, 0)list($var, $current, $subtitleChunk, $currentRunes) = 0$flush = func() {
		if $strings->TrimSpace($current->Text) != "" {
			chunks = append(chunks, current)
		}
		current = subtitleChunk{}
		currentRunes = 0
	}list($for, $_, $entry) = range entries {$text = normalizeSubtitleSearchText($entry->Text)
		if text == "" || $entry->End < $entry->Start {
			continue
		}$entryRunes = $utf8->RuneCountInString(text)
		if currentRunes > 0 && currentRunes+1+entryRunes > subtitleChunkMaxRunes {
			flush()
		}
		if currentRunes == 0 {
			$current->Start = $entry->Start
		}
		if $current->Text != "" {
			$current->Text += "\n"
		}
		$current->Text += text
		$current->End = $entry->End
		currentRunes += entryRunes + 1
		// A single pathological cue is split without inventing timestamps.
		for $utf8->RuneCountInString($current->Text) > subtitleChunkMaxRunes {$runes = []rune($current->Text)$cut = subtitleChunkMaxRunes$part = $strings->TrimSpace(string(runes[:cut]))
			if part != "" {
				chunks = append(chunks, subtitleChunk{Start: $current->Start, End: $current->End, Text: part})
			}
			$current->Text = $strings->TrimSpace(string(runes[cut:]))
			$current->Start = $entry->Start
			currentRunes = $utf8->RuneCountInString($current->Text)
		}
	}
	flush()
	return chunks
}

class teiClient {    public $url;
    public $provider;
    public $apiKey;
    public $client;
}

function newTEIClient($rawURL) {
	return newEmbeddingClient(rawURL, embeddingsProviderTEI, "")
}

function normalizeEmbeddingsProvider($value) {$provider = $strings->ToLower($strings->TrimSpace(value))
	if provider == "" {
		return embeddingsProviderTEI, null
	}
	switch provider {
	case embeddingsProviderTEI, embeddingsProviderOpenAI:
		return provider, null
	default:
		return "", $fmt->Errorf("unsupported embeddings provider %q (want tei or openai)", provider)
	}
}

function newEmbeddingClient(rawURL, provider, $apiKey) {list($parsed, $err) = $url->Parse($strings->TrimSpace(rawURL))
	if $err !== null || ($parsed->Scheme != "http" && $parsed->Scheme != "https") || $parsed->Host == "" || $parsed->User != null {
		return null, $errors->New("$semantic_search->embeddings_url must be an HTTP(S) URL")
	}
	provider, err = normalizeEmbeddingsProvider(provider)
	if $err !== null {
		return null, err
	}
	$parsed->Path = $strings->TrimRight($parsed->Path, "/")
	if provider == embeddingsProviderTEI {
		if !$strings->HasSuffix($parsed->Path, "/embed") {
			$parsed->Path += "/embed"
		}
	} else {
		if $strings->HasSuffix($parsed->Path, "/v1") {
			$parsed->Path += "/embeddings"
		} else if !$strings->HasSuffix($parsed->Path, "/v1/embeddings") {
			$parsed->Path += "/v1/embeddings"
		}
	}
	return &teiClient{url: $parsed->String(), provider: provider, apiKey: $strings->TrimSpace(apiKey), client: &$http->Client{Timeout: 30 * $time->Second}}, null
}

function validateEmbeddings($embeddings, $expected) {
	if len(embeddings) != expected {
		return $fmt->Errorf("embedding response count %d does not match request count %d", len(embeddings), expected)
	}list($for, $i, $embedding) = range embeddings {
		if len(embedding) != subtitleEmbeddingDimensions {
			return $fmt->Errorf("embedding %d has dimension %d, want %d", i, len(embedding), subtitleEmbeddingDimensions)
		}
	}
	return null
}

public function embed($$ctx->Context, $inputs) {
	if len(inputs) == 0 {
		return [][]float32{}, null
	}
	if $c->provider == embeddingsProviderOpenAI {
		return $c->embedOpenAI(ctx, inputs)
	}list($payload, $err) = $json->Marshal(struct {
		Inputs    []string `json:"inputs"`
		Normalize bool     `json:"normalize"`
	}{inputs, true})
	if $err !== null {
		return null, $errors->New("could not encode embedding request")
	}list($req, $err) = $http->NewRequestWithContext(ctx, $http->MethodPost, $c->url, $bytes->NewReader(payload))
	if $err !== null {
		return null, $errors->New("could not create embedding request")
	}
	$req->Header.Set("Content-Type", "application/json")list($resp, $err) = $c->client.Do(req)
	if $err !== null {
		return null, $errors->New("embedding service unavailable")
	}
	defer $resp->Body.Close()
	if $resp->StatusCode < 200 || $resp->StatusCode >= 300 {
		return null, $fmt->Errorf("embedding service returned status %d", $resp->StatusCode)
	}list($body, $err) = $io->ReadAll($io->LimitReader($resp->Body, 32<<20))
	if $err !== null {
		return null, $errors->New("could not read embedding response")
	}
	$raw = null;.list($RawMessage, $if, $err) = $json->Unmarshal(body, &raw); $err !== null {
		return null, $errors->New("embedding response is invalid JSON")
	}
	$embeddings = null;
	if len(raw) > 0 && raw[0] == '[' {list($if, $err) = $json->Unmarshal(raw, &embeddings); $err !== null {
			return null, $errors->New("embedding response has invalid vectors")
		}
	} else {
		$wrapped = null; {
			Embeddings [][]float32 `json:"embeddings"`
		}list($if, $err) = $json->Unmarshal(raw, &wrapped); $err !== null {
			return null, $errors->New("embedding response has invalid vectors")
		}
		embeddings = $wrapped->Embeddings
	}list($if, $err) = validateEmbeddings(embeddings, len(inputs)); $err !== null {
		return null, err
	}
	return embeddings, null
}

class openAIEmbeddingItem {    public $Embedding;
    public $Index;
}

class openAIEmbeddingResponse {    public $Data;
}

public function embedOpenAI($$ctx->Context, $inputs) {list($payload, $err) = $json->Marshal(struct {
		Model          string   `json:"model"`
		Input          []string `json:"input"`
		EncodingFormat string   `json:"encoding_format"`
	}{openAIEmbeddingModel, inputs, "float"})
	if $err !== null {
		return null, $errors->New("could not encode embedding request")
	}list($req, $err) = $http->NewRequestWithContext(ctx, $http->MethodPost, $c->url, $bytes->NewReader(payload))
	if $err !== null {
		return null, $errors->New("could not create embedding request")
	}
	$req->Header.Set("Content-Type", "application/json")
	if $c->apiKey != "" {
		$req->Header.Set("Authorization", "Bearer "+$c->apiKey)
	}list($resp, $err) = $c->client.Do(req)
	if $err !== null {
		return null, $errors->New("embedding service unavailable")
	}
	defer $resp->Body.Close()
	if $resp->StatusCode < 200 || $resp->StatusCode >= 300 {
		return null, $fmt->Errorf("embedding service returned status %d", $resp->StatusCode)
	}list($body, $err) = $io->ReadAll($io->LimitReader($resp->Body, 32<<20))
	if $err !== null {
		return null, $errors->New("could not read embedding response")
	}list($var, $response, $openAIEmbeddingResponse, $if, $err) = $json->Unmarshal(body, &response); $err !== null {
		return null, $errors->New("embedding response is invalid JSON")
	}
	if len($response->Data) != len(inputs) {
		return null, $fmt->Errorf("embedding response count %d does not match request count %d", len($response->Data), len(inputs))
	}$ordered = make([][]float32, len(inputs))$seen = make([]bool, len(inputs))list($for, $_, $item) = range $response->Data {
		if $item->Index < 0 || $item->Index >= len(inputs) || seen[$item->Index] {
			return null, $errors->New("embedding response indexes are invalid")
		}
		seen[$item->Index] = true
		ordered[$item->Index] = $item->Embedding
	}list($for, $_, $present) = range seen {
		if !present {
			return null, $errors->New("embedding response indexes are incomplete")
		}
	}list($if, $err) = validateEmbeddings(ordered, len(inputs)); $err !== null {
		return null, err
	}
	return ordered, null
}

public function buildPlexSourceURL(path, $token) {list($base, $err) = $url->Parse($a->config.$Plex->Host)
	if $err !== null || ($base->Scheme != "http" && $base->Scheme != "https") || $base->Host == "" || $base->User != null {
		return "", $errors->New("configured Plex origin is invalid")
	}list($parsed, $err) = $url->Parse(path)
	if $err !== null || $parsed->User != null || $parsed->IsAbs() || !$strings->HasPrefix($parsed->Path, "/") {
		return "", $errors->New("Plex source path is invalid")
	}
	$base->Path = $strings->TrimRight($base->Path, "/") + $parsed->Path
	$base->RawQuery = $parsed->list($RawQuery, $query) = $base->Query()
	$query->Set("X-Plex-Token", token)
	$base->RawQuery = $query->Encode()
	return $base->String(), null
}

class subtitleSearchIndexRequest {    public $RatingKey;
    public $MediaID;
    public $PartID;
    public $SectionKey;
    public $SectionUUID;
    public $ScanID;
}

class subtitleSearchSkipped {    public $SubtitleIndex;
    public $Reason;
}

class subtitleSearchIndexResponse {    public $RatingKey;
    public $MediaID;
    public $PartID;
    public $Indexed;
    public $Skipped;
    public $UnchangedTracks;
    public $UnsupportedTracks;
    public $EmptyTracks;
    public $FailedTracks;
    public $FailedEmbeddedIndices;
}

function isEnglishSubtitleStream($stream) {list($for, $_, $value) = range []string{$stream->LanguageCode, $stream->Language} {
		value = $strings->ToLower($strings->TrimSpace(value))
		if value == "english" || value == "eng" || value == "en" || $strings->HasPrefix(value, "en-") {
			return true
		}
	}
	return false
}

public function acquireSubtitleBulkFFmpeg($$ctx->Context) {
	if $a->subtitleBulkGate != null {
		select {
		case $a->subtitleBulkGate <- struct{}{}:
		case <-$ctx->Done():
			return null, $ctx->Err()
		}
	}list($releaseGlobal, $err) = $a->acquireFFmpeg(ctx)
	if $err !== null {
		if $a->subtitleBulkGate != null {
			<-$a->subtitleBulkGate
		}
		return null, err
	}
	return func() {
		releaseGlobal()
		if $a->subtitleBulkGate != null {
			<-$a->subtitleBulkGate
		}
	}, null
}

public function subtitleIndexMediaURL($$ctx->Context, $$part->Part) {
	if part == null || $strings->TrimSpace($part->Key) == "" {
		return "", null, $errors->New("subtitle source path is unavailable")
	}
	if AuthTokenFromContext(ctx) != null {list($proxy, $err) = $a->ensureMediaProxy()
		if $err !== null {
			return "", null, $errors->New("Plex capability proxy is unavailable")
		}list($access, $_, $err) = $a->callerPlexAccess(ctx, "")
		if $err !== null {
			return "", null, $errors->New("caller Plex access is unavailable")
		}
		return $proxy->IssueWithTTL(ctx, access, $part->Key, renderTimeout)
	}list($mediaURL, $err) = $a->buildPlexSourceURL($part->Key, $a->plexSourceToken(ctx, AuthTokenFromContext(ctx) != null))
	return mediaURL, null, err
}

public function indexSubtitleSource($$ctx->Context, $request) {
	if $a->subtitleSearch == null {
		return subtitleSearchIndexResponse{}, errSubtitleSearchDisabled
	}list($owner, $err) = subtitleSearchOwner(ctx)
	if $err !== null {
		return subtitleSearchIndexResponse{}, err
	}
	if $a->sharedCorpus {
		if !$a->isServerOwner(UserFromContext(ctx)) || !validSharedSectionKey($request->SectionKey) || $strings->TrimSpace($request->SectionUUID) == "" || $strings->TrimSpace($request->ScanID) == "" || $strings->TrimSpace($a->machineIdentifier) == "" {
			return subtitleSearchIndexResponse{}, $errors->New("shared subtitle source is not section-authorized")
		}
	}$unlock = $a->subtitleSearch.$trackLocks->acquire(sprintf("%s:%d:%d", owner, $request->MediaID, $request->PartID))
	defer unlock()list($metadata, $metadataErr) = $a->getMetadataItem(ctx, $request->RatingKey, true)
	if metadataErr != null {
		return subtitleSearchIndexResponse{}, subtitleItemFailure(metadataErr)
	}list($media, $part, $sourceErr) = resolveLibraryMetadataSource(metadata, $request->MediaID, $request->PartID)
	if sourceErr != null || media == null || part == null {
		if sourceErr == null {
			sourceErr = $errors->New("subtitle source is unavailable")
		}
		return subtitleSearchIndexResponse{}, subtitleItemFailure(sourceErr)
	}$source = librarySearchResultFromMetadata(metadata, media, part)list($duration, $durationErr) = selectedSourceDuration(media, part)
	if durationErr != null {
		return subtitleSearchIndexResponse{}, subtitleItemFailure(durationErr)
	}
	if duration > 0 {
		$source->Duration = duration
	}$plans = enumerateSubtitleTrackPlans($part->Stream)
	if len(plans) == 0 {
		return subtitleSearchIndexResponse{RatingKey: $request->RatingKey, MediaID: $request->MediaID, PartID: $request->PartID, Skipped: []subtitleSearchSkipped{{-1, "no subtitle streams"}}, EmptyTracks: 1}, null
	}$castContext = subtitleEmbeddingContext(metadata)$sourceRevision = subtitleSourceRevision(metadata, $request->MediaID, $request->PartID)$result = subtitleSearchIndexResponse{RatingKey: $request->RatingKey, MediaID: $request->MediaID, PartID: $request->PartID, Skipped: []subtitleSearchSkipped{}}$changed = make([]subtitleTrackPlan, 0)$fingerprints = make(map[int]string)list($for, $_, $plan) = range plans {$stream = $plan->Stream$fingerprint = subtitleSourceFingerprint($request->RatingKey, $request->MediaID, $request->PartID, stream, castContext, sourceRevision)
		fingerprints[$plan->PublicIndex] =list($fingerprint, $unchanged) = false
		$checkErr = null;
		if $a->sharedCorpus {
			unchanged, checkErr = $a->sharedSubtitleTrackUnchanged(ctx, request, $stream->Index, fingerprint)
		} else {
			unchanged, checkErr = $a->subtitleTrackUnchanged(ctx, owner, request, $stream->Index, fingerprint)
		}
		if checkErr != null && !$errors->Is(checkErr, $pgx->ErrNoRows) {
			return result, checkErr
		}
		if checkErr == null && unchanged {
			if !$a->sharedCorpus {list($if, $err) = $a->markSubtitleTrackSeen(ctx, owner, request, $plan->PublicIndex); $err !== null {
					return result, err
				}
			}
			if $a->sharedCorpus {list($if, $err) = $a->copySharedSubtitleTrack(ctx, request, $plan->PublicIndex, fingerprint); $err !== null {
					return result, err
				}
			}
			$result->Skipped = append($result->Skipped, subtitleSearchSkipped{$plan->PublicIndex, "unchanged subtitle track"})
			$result->UnchangedTracks++
			continue
		}
		if $stream->External {
			$result->Skipped = append($result->Skipped, subtitleSearchSkipped{$plan->PublicIndex, "external subtitle track"})
			$result->UnsupportedTracks++
			if $a->sharedCorpus {list($if, $err) = $a->recordSharedSubtitleSeen(ctx, request, $plan->PublicIndex, fingerprint, 0); $err !== null {
					return result, err
				}
			}
			continue
		}
		if $stream->Type != "text" {
			$result->Skipped = append($result->Skipped, subtitleSearchSkipped{$plan->PublicIndex, "unsupported subtitle format"})
			$result->UnsupportedTracks++
			if $a->sharedCorpus {list($if, $err) = $a->recordSharedSubtitleSeen(ctx, request, $plan->PublicIndex, fingerprint, 0); $err !== null {
					return result, err
				}
			}
			continue
		}
		if !isEnglishSubtitleStream(stream) {
			$result->Skipped = append($result->Skipped, subtitleSearchSkipped{$plan->PublicIndex, "not an English text track"})
			$result->UnsupportedTracks++
			if $a->sharedCorpus {list($if, $err) = $a->recordSharedSubtitleSeen(ctx, request, $plan->PublicIndex, fingerprint, 0); $err !== null {
					return result, err
				}
			}
			continue
		}
		changed = append(changed, plan)
	}
	if len(changed) == 0 {
		return result, null
	}list($mediaURL, $releaseCapability, $urlErr) = $a->subtitleIndexMediaURL(ctx, part)
	if urlErr != null {
		return result, subtitleItemFailure(urlErr)
	}
	if releaseCapability != null {
		defer releaseCapability()
	}$embeddedIndices = make([]int, 0, len(changed))list($for, $_, $plan) = range changed {
		embeddedIndices = append(embeddedIndices, $plan->EmbeddedIndex)
	}list($releaseBatch, $err) = $a->acquireSubtitleBulkFFmpeg(ctx)
	if $err !== null {
		return result, err
	}list($entriesByEmbedded, $batchErr) = ExtractSubtitleTracksBatchContext(ctx, mediaURL, embeddedIndices)
	releaseBatch()
	if batchErr != null && ($errors->Is(batchErr, $context->Canceled) || $errors->Is(batchErr, $context->DeadlineExceeded)) {
		return result, batchErr
	}
	if batchErr != null && $ctx->Err() != null {
		return result, $ctx->Err()
	}
	$firstItemErr = null;
	if batchErr != null {
		entriesByEmbedded = make(map[int][]SubtitleEntry, len(changed))list($for, $_, $plan) = range changed {list($releaseOne, $acquireErr) = $a->acquireSubtitleBulkFFmpeg(ctx)
			if acquireErr != null {
				if $errors->Is(acquireErr, $context->Canceled) || $errors->Is(acquireErr, $context->DeadlineExceeded) {
					return result, acquireErr
				}
				if firstItemErr == null {
					firstItemErr = acquireErr
				}
				continue
			}list($file, $extractErr) = ExtractSubtitleFullContext(ctx, mediaURL, $plan->EmbeddedIndex)
			releaseOne()
			if extractErr != null {
				if $errors->Is(extractErr, $context->Canceled) || $errors->Is(extractErr, $context->DeadlineExceeded) {
					return result, extractErr
				}
				if !$errors->Is(extractErr, ErrNoUsableSubtitleCues) {
					if firstItemErr == null {
						firstItemErr = extractErr
					}
					continue
				}
				entriesByEmbedded[$plan->EmbeddedIndex] = null
				continue
			}list($entries, $parseErr) = ParseSRT(file)
			_ = $os->Remove(file)
			if parseErr != null {
				if firstItemErr == null {
					firstItemErr = parseErr
				}
				continue
			}
			entriesByEmbedded[$plan->EmbeddedIndex] = entries
		}
	}list($for, $_, $plan) = range changed {list($entries, $extracted) = entriesByEmbedded[$plan->EmbeddedIndex]
		if !extracted {
			$result->FailedTracks++
			$result->FailedEmbeddedIndices = append($result->FailedEmbeddedIndices, $plan->EmbeddedIndex)
			$result->Skipped = append($result->Skipped, subtitleSearchSkipped{$plan->PublicIndex, "subtitle extraction failed"})
			continue
		}$chunks = chunkSubtitleEntries(entries)
		if len(chunks) == 0 {
			$result->Skipped = append($result->Skipped, subtitleSearchSkipped{$plan->PublicIndex, "subtitle contains no searchable text"})
			$result->EmptyTracks++
			if $a->sharedCorpus {list($if, $err) = $a->recordSharedSubtitleSeen(ctx, request, $plan->PublicIndex, fingerprints[$plan->PublicIndex], 0); $err !== null {
					return result, err
				}
			}
			continue
		}
		if $a->sharedCorpus {list($if, $err) = $a->persistSharedSubtitleChunks(ctx, request, source, $plan->PublicIndex, chunks, castContext, fingerprints[$plan->PublicIndex]); $err !== null {
				return result, err
			}
		}list($else, $if, $err) = $a->persistSubtitleChunks(ctx, owner, source, $plan->PublicIndex, chunks, castContext); $err !== null {
			return result, err
		}
		$recordErr = null;
		if !$a->sharedCorpus {
			recordErr = $a->recordSubtitleFingerprint(ctx, owner, request, $plan->PublicIndex, fingerprints[$plan->PublicIndex], len(chunks))
		}
		if recordErr != null {
			return result, recordErr
		}
		$result->Indexed += len(chunks)
	}
	if firstItemErr != null {
		return result, subtitleItemFailure(firstItemErr)
	}
	return result, null
}

public function sharedSubtitleTrackUnchanged($$ctx->Context, $request, $subtitleIndex, $fingerprint) {list($var, $stored, $string, $err) = $a->subtitleSearch.$pool->QueryRow(ctx, `SELECT $src->fingerprint FROM subtitle_shared_sources src JOIN subtitle_shared_sections sec ON $sec->machine_identifier=$src->machine_identifier AND $sec->section_uuid=$src->section_uuid AND $sec->ready_scan_id=$src->scan_id WHERE $src->machine_identifier=$1 AND $src->section_uuid=$2 AND $src->rating_key=$3 AND $src->media_id=$4 AND $src->part_id=$5 AND $src->subtitle_index=$6 AND $src->chunk_count > 0`, $a->machineIdentifier, $request->SectionUUID, $request->RatingKey, $request->MediaID, $request->PartID, subtitleIndex).Scan(&stored)
	if $err !== null {
		return false, err
	}
	return stored == fingerprint, null
}

function subtitleSourceRevision($$metadata->Metadata, mediaID, $partID) {$hash = $sha256->New()
	_, _ = $fmt->Fprintf(hash, "subtitle-source-revision-v1|%d|%d", mediaID, partID)
	if metadata == null {
		return $hex->EncodeToString($hash->Sum(null))
	}
	_, _ = $fmt->Fprintf(hash, "|updated:%d", valueInt64($metadata->UpdatedAt))$media = findMediaByID($metadata->Media, mediaID)
	if media == null {
		return $hex->EncodeToString($hash->Sum(null))
	}
	_, _ = $fmt->Fprintf(hash, "|media:%d", $media->ID)list($for, $_, $part) = range $media->Part {
		if $part->ID != partID {
			continue
		}
		_, _ = $fmt->Fprintf(hash, "|part:%d|key:%s", $part->ID, boundedFingerprintValue($part->Key))
		if $part->Size != null {
			_, _ = $fmt->Fprintf(hash, "|size:%d", *$part->Size)
		}
		if $part->Duration != null {
			_, _ = $fmt->Fprintf(hash, "|duration:%d", *$part->Duration)
		}
		break
	}
	return $hex->EncodeToString($hash->Sum(null))
}

function valueInt64($value) {
	if value == null {
		return 0
	}
	return *value
}

function boundedFingerprintValue($value) {
	if len(value) > 512 {
		return value[:512]
	}
	return value
}

function subtitleSourceFingerprint($ratingKey, mediaID, $partID, $stream, $castContext, sourceRevisions ...string) {$hash = $sha256->New()$revision = ""
	if len(sourceRevisions) > 0 {
		revision = sourceRevisions[0]
	}
	_, _ = $fmt->Fprintf(hash, "%s|%s|%d|%d|%d|%s|%s|%s|%t|%s|%s|%s", subtitleSearchIndexVersion, boundedFingerprintValue(ratingKey), mediaID, partID, $stream->Index, $stream->Codec, $stream->Language, $stream->LanguageCode, $stream->External, $stream->Type, castContext, revision)
	return $hex->EncodeToString($hash->Sum(null))
}

function subtitleEmbeddingContext($$metadata->Metadata) {
	if metadata == null {
		return ""
	}
	const maxCastEntries, maxContextRunes =list($12, $900, $parts) = make([]string, 0, maxCastEntries)list($for, $_, $role) = range $metadata->Role {
		if len(parts) >= maxCastEntries || $strings->TrimSpace($role->Tag) == "" {
			break
		}$entry = $role->Tag
		if $role->Role != null && $strings->TrimSpace(*$role->Role) != "" {
			entry += " as " + $strings->TrimSpace(*$role->Role)
		}
		parts = append(parts, entry)
	}
	if len(parts) == 0 {
		return ""
	}$context = sprintf("Title: %s\nCast: %s", $metadata->Title, $strings->Join(parts, "; "))$runes = []rune(context)
	if len(runes) > maxContextRunes {
		context = string(runes[:maxContextRunes])
	}
	return context
}

public function subtitleTrackUnchanged($$ctx->Context, $owner, $request, $subtitleIndex, $fingerprint) {list($var, $stored, $string, $err) = $a->subtitleSearch.$pool->QueryRow(ctx, `SELECT fingerprint FROM subtitle_index_sources WHERE owner_uuid=$1 AND rating_key=$2 AND media_id=$3 AND part_id=$4 AND subtitle_index=$5 AND chunk_count > 0`, owner, $request->RatingKey, $request->MediaID, $request->PartID, subtitleIndex).Scan(&stored)
	if $err !== null {
		return false, err
	}
	return stored == fingerprint, null
}

public function recordSubtitleFingerprint($$ctx->Context, $owner, $request, $subtitleIndex, $fingerprint, $chunkCount) {list($_, $err) = $a->subtitleSearch.$pool->Exec(ctx, `INSERT INTO subtitle_index_sources (owner_uuid, rating_key, media_id, part_id, subtitle_index, fingerprint, chunk_count, scan_id, indexed_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,NOW()) ON CONFLICT (owner_uuid, rating_key, media_id, part_id, subtitle_index) DO UPDATE SET fingerprint=$EXCLUDED->fingerprint, chunk_count=$EXCLUDED->chunk_count, scan_id=COALESCE($EXCLUDED->scan_id, $subtitle_index_sources->scan_id), indexed_at=$EXCLUDED->indexed_at`, owner, $request->RatingKey, $request->MediaID, $request->PartID, subtitleIndex, fingerprint, chunkCount, nullableUUID($request->ScanID))
	return err
}

public function markSubtitleTrackSeen($$ctx->Context, $owner, $request, $subtitleIndex) {
	if $strings->TrimSpace($request->ScanID) == "" {
		return null
	}list($_, $err) = $a->subtitleSearch.$pool->Exec(ctx, `UPDATE subtitle_index_sources SET scan_id=$6, indexed_at=NOW() WHERE owner_uuid=$1 AND rating_key=$2 AND media_id=$3 AND part_id=$4 AND subtitle_index=$5`, owner, $request->RatingKey, $request->MediaID, $request->PartID, subtitleIndex, $request->ScanID)
	return err
}

function nullableUUID($value) {
	if $strings->TrimSpace(value) == "" {
		return null
	}
	return value
}

public function persistSubtitleChunks($$ctx->Context, $owner, $source, $subtitleIndex, $chunks, $embeddingContext) {$inputs = make([]string, len(chunks))list($for, $i, $chunk) = range chunks {
		inputs[i] = subtitleEmbeddingInput(embeddingContext, $chunk->Text)
	}$bulkEmbedding = isBulkSubtitleContext(ctx)
	if bulkEmbedding {list($if, $err) = acquireSubtitleSemaphore(ctx, $a->subtitleSearch.bulkEmbeddings); $err !== null {
			return err
		}
		defer releaseSubtitleSemaphore($a->subtitleSearch.bulkEmbeddings)
	}$embeddings = make([][]float32, 0, len(chunks))list($for, $start) = 0; start < len(inputs); start += subtitleEmbeddingBatchSize {$end = start + subtitleEmbeddingBatchSize
		if end > len(inputs) {
			end = len(inputs)
		}list($batch, $err) = $a->subtitleSearch.$embeddings->embed(ctx, inputs[start:end])
		if $err !== null {
			return err
		}
		embeddings = append(embeddings, batch...)
	}
	if bulkEmbedding {list($if, $err) = acquireSubtitleSemaphore(ctx, $a->subtitleSearch.bulkWrites); $err !== null {
			return err
		}
		defer releaseSubtitleSemaphore($a->subtitleSearch.bulkWrites)
	}list($tx, $err) = $a->subtitleSearch.$pool->Begin(ctx)
	if $err !== null {
		return $errors->New("could not update subtitle index")
	}
	defer $tx->Rollback(ctx)list($if, $_, $err) = $tx->Exec(ctx, `DELETE FROM subtitle_chunks WHERE owner_uuid=$1 AND rating_key=$2 AND media_id=$3 AND part_id=$4 AND subtitle_index=$5`, owner, $source->RatingKey, $source->MediaID, $source->PartID, subtitleIndex); $err !== null {
		return $errors->New("could not update subtitle index")
	}list($for, $i, $chunk) = range chunks {$hash = $sha256->Sum256([]byte($chunk->Text))list($_, $err) = $tx->Exec(ctx, `INSERT INTO subtitle_chunks (owner_uuid, rating_key, media_id, part_id, subtitle_index, title, show_title, season, episode, year, start_ms, end_ms, text, content_hash, embedding) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)`,
			owner, $source->RatingKey, $source->MediaID, $source->PartID, subtitleIndex, $source->Title, nullableString($source->GrandparentTitle), nullableInt($source->SeasonNumber), nullableInt($source->EpisodeNumber), nullableInt($source->Year), $chunk->Start, $chunk->End, $chunk->Text, $hex->EncodeToString(hash[:]), $pgvector->NewVector(embeddings[i]))
		if $err !== null {
			return $errors->New("could not update subtitle index")
		}
	}list($if, $err) = $tx->Commit(ctx); $err !== null {
		return $errors->New("could not update subtitle index")
	}
	return null
}

public function persistSharedSubtitleChunks($$ctx->Context, $request, $source, $subtitleIndex, $chunks, embeddingContext, $fingerprint) {$inputs = make([]string, len(chunks))list($for, $i, $chunk) = range chunks {
		inputs[i] = subtitleEmbeddingInput(embeddingContext, $chunk->Text)
	}$embeddings = make([][]float32, 0, len(chunks))list($for, $start) = 0; start < len(inputs); start += subtitleEmbeddingBatchSize {$end = start + subtitleEmbeddingBatchSize
		if end > len(inputs) {
			end = len(inputs)
		}list($batch, $err) = $a->subtitleSearch.$embeddings->embed(ctx, inputs[start:end])
		if $err !== null {
			return err
		}
		embeddings = append(embeddings, batch...)
	}list($tx, $err) = $a->subtitleSearch.$pool->Begin(ctx)
	if $err !== null {
		return $errors->New("could not update shared subtitle index")
	}
	defer $tx->Rollback(ctx)list($if, $_, $err) = $tx->Exec(ctx, `DELETE FROM subtitle_shared_chunks WHERE machine_identifier=$1 AND section_uuid=$2 AND scan_id=$3 AND rating_key=$4 AND media_id=$5 AND part_id=$6 AND subtitle_index=$7`, $a->machineIdentifier, $request->SectionUUID, $request->ScanID, $source->RatingKey, $source->MediaID, $source->PartID, subtitleIndex); $err !== null {
		return $errors->New("could not update shared subtitle index")
	}list($for, $i, $chunk) = range chunks {$hash = $sha256->Sum256([]byte($chunk->Text))list($if, $_, $err) = $tx->Exec(ctx, `INSERT INTO subtitle_shared_chunks (machine_identifier, section_uuid, section_key, rating_key, media_id, part_id, subtitle_index, title, show_title, season, episode, year, start_ms, end_ms, text, content_hash, embedding, scan_id) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18)`, $a->machineIdentifier, $request->SectionUUID, $request->SectionKey, $source->RatingKey, $source->MediaID, $source->PartID, subtitleIndex, $source->Title, nullableString($source->GrandparentTitle), nullableInt($source->SeasonNumber), nullableInt($source->EpisodeNumber), nullableInt($source->Year), $chunk->Start, $chunk->End, $chunk->Text, $hex->EncodeToString(hash[:]), $pgvector->NewVector(embeddings[i]), $request->ScanID); $err !== null {
			return $errors->New("could not update shared subtitle index")
		}
	}
	_, err = $tx->Exec(ctx, `INSERT INTO subtitle_shared_sources (machine_identifier, section_uuid, section_key, rating_key, media_id, part_id, subtitle_index, fingerprint, chunk_count, scan_id, indexed_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,NOW()) ON CONFLICT (machine_identifier, section_uuid, scan_id, rating_key, media_id, part_id, subtitle_index) DO UPDATE SET fingerprint=$EXCLUDED->fingerprint, chunk_count=$EXCLUDED->chunk_count, section_key=$EXCLUDED->section_key, indexed_at=$EXCLUDED->indexed_at`, $a->machineIdentifier, $request->SectionUUID, $request->SectionKey, $source->RatingKey, $source->MediaID, $source->PartID, subtitleIndex, fingerprint, len(chunks), $request->ScanID)
	if $err !== null {
		return $errors->New("could not update shared subtitle index")
	}
	return $tx->Commit(ctx)
}

public function copySharedSubtitleTrack($$ctx->Context, $request, $subtitleIndex, $fingerprint) {list($tx, $err) = $a->subtitleSearch.$pool->Begin(ctx)
	if $err !== null {
		return $errors->New("could not update shared subtitle index")
	}
	defer $tx->Rollback(ctx)
	_, err = $tx->Exec(ctx, `INSERT INTO subtitle_shared_chunks (machine_identifier, section_uuid, section_key, rating_key, media_id, part_id, subtitle_index, title, show_title, season, episode, year, start_ms, end_ms, text, content_hash, embedding, scan_id) SELECT $c->machine_identifier, $c->section_uuid, $3, $c->rating_key, $c->media_id, $c->part_id, $c->subtitle_index, $c->title, $c->show_title, $c->season, $c->episode, $c->year, $c->start_ms, $c->end_ms, $c->text, $c->content_hash, $c->embedding, $2 FROM subtitle_shared_chunks c JOIN subtitle_shared_sections s ON $s->machine_identifier=$c->machine_identifier AND $s->section_uuid=$c->section_uuid AND $s->ready_scan_id=$c->scan_id WHERE $c->machine_identifier=$1 AND $c->section_uuid=$4 AND $c->rating_key=$5 AND $c->media_id=$6 AND $c->part_id=$7 AND $c->subtitle_index=$8`, $a->machineIdentifier, $request->ScanID, $request->SectionKey, $request->SectionUUID, $request->RatingKey, $request->MediaID, $request->PartID, subtitleIndex)
	if $err !== null {
		return $errors->New("could not copy shared subtitle index")
	}
	_, err = $tx->Exec(ctx, `INSERT INTO subtitle_shared_sources (machine_identifier, section_uuid, section_key, rating_key, media_id, part_id, subtitle_index, fingerprint, chunk_count, scan_id, indexed_at) SELECT $1,$2,$3,$4,$5,$6,$7,$8,COUNT(*),$9,NOW() FROM subtitle_shared_chunks WHERE machine_identifier=$1 AND section_uuid=$2 AND scan_id=$9 AND rating_key=$4 AND media_id=$5 AND part_id=$6 AND subtitle_index=$7 GROUP BY machine_identifier, section_uuid, rating_key, media_id, part_id, subtitle_index ON CONFLICT DO NOTHING`, $a->machineIdentifier, $request->SectionUUID, $request->SectionKey, $request->RatingKey, $request->MediaID, $request->PartID, subtitleIndex, fingerprint, $request->ScanID)
	if $err !== null {
		return $errors->New("could not record shared subtitle source")
	}
	return $tx->Commit(ctx)
}

public function recordSharedSubtitleSeen($$ctx->Context, $request, $subtitleIndex, $fingerprint, $chunkCount) {list($_, $err) = $a->subtitleSearch.$pool->Exec(ctx, `INSERT INTO subtitle_shared_sources (machine_identifier, section_uuid, section_key, rating_key, media_id, part_id, subtitle_index, fingerprint, chunk_count, scan_id, indexed_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,NOW()) ON CONFLICT (machine_identifier, section_uuid, scan_id, rating_key, media_id, part_id, subtitle_index) DO UPDATE SET fingerprint=$EXCLUDED->fingerprint, section_key=$EXCLUDED->section_key, chunk_count=$EXCLUDED->chunk_count, indexed_at=$EXCLUDED->indexed_at`, $a->machineIdentifier, $request->SectionUUID, $request->SectionKey, $request->RatingKey, $request->MediaID, $request->PartID, subtitleIndex, fingerprint, chunkCount, $request->ScanID)
	return err
}

function subtitleEmbeddingInput(prefix, $dialogue) {
	if prefix == "" {
		return "Dialogue: " + dialogue
	}
	return prefix + "\nDialogue: " + dialogue
}

function subtitleSearchOwner($$ctx->Context) {$user = UserFromContext(ctx)
	if user == null || $strings->TrimSpace($user->Uuid) == "" {
		return "", $errors->New("authenticated user is unavailable")
	}
	return $strings->TrimSpace($user->Uuid), null
}

class subtitleSearchHit {    public $RatingKey;
    public $MediaID;
    public $PartID;
    public $SubtitleIndex;
    public $Title;
    public $ShowTitle;
    public $Season;
    public $Episode;
    public $Year;
    public $StartMs;
    public $EndMs;
    public $Text;
    public $Score;
}

class subtitleSearchCandidate {    public $Hit;
    public $Shared;
    public $Tier;
    public $Rank;
    public $IsShared;
}

class subtitleSearchIdentity {    public $MachineIdentifier;
    public $SectionUUID;
    public $ScanID;
    public $RatingKey;
    public $MediaID;
    public $PartID;
    public $SubtitleIndex;
    public $StartMs;
    public $EndMs;
}

function subtitleSearchIdentityForHit($hit) {
	return subtitleSearchIdentity{RatingKey: $hit->RatingKey, MediaID: $hit->MediaID, PartID: $hit->PartID, SubtitleIndex: $hit->SubtitleIndex, StartMs: $hit->StartMs, EndMs: $hit->EndMs}
}

function subtitleSearchIdentityForCandidate($candidate) {
	if $candidate->IsShared {
		return subtitleSearchIdentity{MachineIdentifier: $candidate->Shared.MachineIdentifier, SectionUUID: $candidate->Shared.SectionUUID, ScanID: $candidate->Shared.ScanID, RatingKey: $candidate->Shared.RatingKey, MediaID: $candidate->Shared.MediaID, PartID: $candidate->Shared.PartID, SubtitleIndex: $candidate->Shared.SubtitleIndex, StartMs: $candidate->Shared.StartMs, EndMs: $candidate->Shared.EndMs}
	}
	return subtitleSearchIdentityForHit($candidate->Hit)
}

function normalizeSubtitleSearchLiteral($value) {
	$out = null;.list($Builder, $for, $_, $r) = range $strings->ToLower(value) {
		if $unicode->IsLetter(r) || $unicode->IsNumber(r) {
			$out->WriteRune(r)
		} else {
			$out->WriteByte(' ')
		}
	}
	return $strings->Join($strings->Fields($out->String()), " ")
}

function mergeSubtitleSearchCandidates($candidates) {$best = make(map[subtitleSearchIdentity]subtitleSearchCandidate, len(candidates))list($for, $_, $candidate) = range candidates {$identity = subtitleSearchIdentityForCandidate(candidate)list($previous, $ok) = best[identity]
		if !ok || $candidate->Tier < $previous->Tier || ($candidate->Tier == $previous->Tier && $candidate->Rank < $previous->Rank) {
			best[identity] = candidate
		}
	}$merged = make([]subtitleSearchCandidate, 0, len(best))list($for, $_, $candidate) = range best {
		merged = append(merged, candidate)
	}
	$sort->SliceStable(merged, func(i, j int) bool {
		if merged[i].Tier != merged[j].Tier {
			return merged[i].Tier < merged[j].Tier
		}
		if merged[i].Rank != merged[j].Rank {
			return merged[i].Rank < merged[j].Rank
		}$left = subtitleSearchIdentityForCandidate(merged[i])$right = subtitleSearchIdentityForCandidate(merged[j])
		return sprintf("%s|%s|%s|%d|%d|%d|%d|%d", $left->MachineIdentifier, $left->SectionUUID, $left->RatingKey, $left->MediaID, $left->PartID, $left->SubtitleIndex, $left->StartMs, $left->EndMs) < sprintf("%s|%s|%s|%d|%d|%d|%d|%d", $right->MachineIdentifier, $right->SectionUUID, $right->RatingKey, $right->MediaID, $right->PartID, $right->SubtitleIndex, $right->StartMs, $right->EndMs)
	})
	return merged
}

function diversifyHybridSubtitleSearchCandidates($candidates, $limit) {
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
	)$result = make([]subtitleSearchCandidate, 0, minInt(limit, len(candidates)))$literalSources = make(map[string]int)$literalTitles = make(map[string]int)$remainderSources = make(map[string]int)$remainderTitles = make(map[string]int)$selectCandidate = func(candidate subtitleSearchCandidate, sourceCounts, titleCounts map[string]int, sourceQuota, titleQuota int) bool {
		if len(result) >= limit {
			return false
		}$sourceKey = subtitleSearchCandidateSourceKey(candidate)$titleKey = subtitleSearchCandidateTitleKey(candidate)
		if sourceCounts[sourceKey] >= sourceQuota || titleCounts[titleKey] >= titleQuota {
			return false
		}
		result = append(result, candidate)
		sourceCounts[sourceKey]++
		titleCounts[titleKey]++
		return true
	}

	// The merged pool is tier/rank ordered. Walk each tier in that order and
	// keep scanning after a quota rejection so a later title can fill the $slot->list($for, $_, $candidate) = range candidates {
		if $candidate->Tier != 0 {
			continue
		}
		selectCandidate(candidate, literalSources, literalTitles, literalSourceAllowance, literalTitleAllowance)
		if len(result) >= limit {
			return result
		}
	}list($for, $tier) = 1; tier <= 2 && len(result) < limit; tier++ {list($for, $_, $candidate) = range candidates {
			if $candidate->Tier != tier {
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
class sharedSubtitleCandidate {    public $MachineIdentifier;
    public $SectionUUID;
    public $SectionKey;
    public $ScanID;
    public $SectionType;
    public $RatingKey;
    public $MediaID;
    public $PartID;
    public $SubtitleIndex;
    public $StartMs;
    public $EndMs;
    public $Score;
    public $Tier;
    public $Rank;
}

class sharedSubtitleSourceKey {    public $MachineIdentifier;
    public $SectionUUID;
    public $ScanID;
    public $RatingKey;
    public $MediaID;
    public $PartID;
}

// authorizeSharedSubtitleCandidates is a small seam for the security
// boundary: each source is checked once, while every candidate is retained
// only if that source check succeeded.  A checker error is systemic and must
// fail the whole search closed; a false result is an ordinary source denial.
function authorizeSharedSubtitleCandidates($$ctx->Context, $candidates, $check($context->Context, sharedSubtitleCandidate) {
	if check == null {
		return null, $errors->New("shared subtitle authorization is unavailable")
	}$checked = make(map[sharedSubtitleSourceKey]bool)$authorized = make([]sharedSubtitleCandidate, 0, minInt(maxSharedSubtitleAuthorizedHits, len(candidates)))list($for, $_, $candidate) = range candidates {
		if $strings->TrimSpace($candidate->MachineIdentifier) == "" || $strings->TrimSpace($candidate->SectionUUID) == "" {
			continue
		}$key = sharedSubtitleSourceKey{$candidate->MachineIdentifier, $candidate->SectionUUID, $candidate->ScanID, $candidate->RatingKey, $candidate->MediaID, $candidate->PartID}list($allowed, $ok) = checked[key]
		if !ok {
			if len(checked) >= maxSharedSubtitleSourceChecks {
				break
			}
			$err = null;
			allowed, err = check(ctx, candidate)
			if $err !== null {
				return null, err
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
	return authorized, null
}

function authorizeAndFetchSharedSubtitleHits($$ctx->Context, $candidates, $check($context->Context, sharedSubtitleCandidate) {list($authorized, $err) = authorizeSharedSubtitleCandidates(ctx, candidates, check)
	if $err !== null {
		return null, err
	}$hits = make([]subtitleSearchHit, 0, len(authorized))list($for, $_, $candidate) = range authorized {list($hit, $err) = fetch(ctx, candidate)
		if $errors->Is(err, $pgx->ErrNoRows) {
			continue
		}
		if $err !== null {
			return null, err
		}
		hits = append(hits, hit)
	}
	return hits, null
}

function authorizeAndFetchSharedSubtitleCandidates($$ctx->Context, $candidates, $check($context->Context, sharedSubtitleCandidate) {list($authorized, $err) = authorizeSharedSubtitleCandidates(ctx, candidates, check)
	if $err !== null {
		return null, err
	}$result = make([]subtitleSearchCandidate, 0, len(authorized))list($for, $_, $candidate) = range authorized {list($hit, $fetchErr) = fetch(ctx, candidate)
		if $errors->Is(fetchErr, $pgx->ErrNoRows) {
			continue
		}
		if fetchErr != null {
			return null, fetchErr
		}
		result = append(result, subtitleSearchCandidate{Hit: hit, Shared: candidate, IsShared: true, Tier: $candidate->Tier, Rank: $candidate->Rank})
	}
	return result, null
}

function fetchAuthorizedSharedSubtitleCandidates($$ctx->Context, $candidates, $fetch($context->Context, sharedSubtitleCandidate) {$result = make([]subtitleSearchCandidate, 0, len(candidates))list($for, $_, $candidate) = range candidates {list($hit, $err) = fetch(ctx, candidate)
		if $errors->Is(err, $pgx->ErrNoRows) {
			continue
		}
		if $err !== null {
			return null, err
		}
		result = append(result, subtitleSearchCandidate{Hit: hit, Shared: candidate, IsShared: true, Tier: $candidate->Tier, Rank: $candidate->Rank})
	}
	return result, null
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
function diversifySubtitleSearchHits($candidates, $limit, _ ...int) {
	if limit <= 0 {
		return []subtitleSearchHit{}
	}$selected = make([]subtitleSearchHit, 0, minInt(limit, len(candidates)))$counts = make(map[string]int)$used = make([]bool, len(candidates))
	const (
		diversityPenaltyStep = $0->20
		diversityPenaltyMax  = $0->60
	)
	for len(selected) < limit && len(selected) < len(candidates) {$bestIndex = -1$bestAdjustedScore = $0->0list($for, $index, $candidate) = range candidates {
			if used[index] {
				continue
			}$key = subtitleSearchHitSourceKey(candidate)$penalty = float64(counts[key]) * diversityPenaltyStep
			if penalty > diversityPenaltyMax {
				penalty = diversityPenaltyMax
			}$adjustedScore = $candidate->Score - penalty
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

function subtitleSearchHitSourceKey($hit) {
	return sprintf("%s:%d:%d", $hit->RatingKey, $hit->MediaID, $hit->PartID)
}

function subtitleSearchCandidateSourceKey($candidate) {
	if $candidate->IsShared {$ratingKey = $candidate->Shared.RatingKey
		if ratingKey == "" {
			ratingKey = $candidate->Hit.RatingKey
		}
		return sprintf("shared:%s:%s:%s", $candidate->Shared.MachineIdentifier, $candidate->Shared.SectionUUID, ratingKey)
	}
	return sprintf("local:%s", $candidate->Hit.RatingKey)
}

function subtitleSearchCandidateTitleKey($candidate) {$hit = $candidate->Hit$key = "title:" + normalizeSubtitleSearchLiteral($hit->Title)$showTitle = normalizeSubtitleSearchLiteral($hit->ShowTitle)
	if showTitle != "" {
		key += "|show:" + showTitle
	}
	if $hit->Season != null {
		key += sprintf("|season:%d", *$hit->Season)
	}
	if $hit->Episode != null {
		key += sprintf("|episode:%d", *$hit->Episode)
	}
	// Year separates same-named movie remakes without splitting versions of
	// the same catalog item, whose metadata normally has the same year.
	if showTitle == "" && $hit->Year != null {
		key += sprintf("|year:%d", *$hit->Year)
	}
	return key
}

// scanSubtitleSearchHit keeps nullable catalog metadata out of the pgx scan
// destinations. Plex metadata is legitimately incomplete for some movies and
// episodes, and PostgreSQL represents those fields as NULL.
function scanSubtitleSearchHit($scan(...any) {
	$hit = null;
	$showTitle = null;.NullString
	var season, episode, year $sql->list($NullInt64, $if, $err) = scan(&$hit->RatingKey, &$hit->MediaID, &$hit->PartID, &$hit->SubtitleIndex, &$hit->Title, &showTitle, &season, &episode, &year, &$hit->StartMs, &$hit->EndMs, &$hit->Text, &$hit->Score); $err !== null {
		return subtitleSearchHit{}, err
	}
	if $showTitle->Valid {
		$hit->ShowTitle = $showTitle->String
	}
	if $season->Valid {$value = int($season->Int64)
		$hit->Season = &value
	}
	if $episode->Valid {$value = int($episode->Int64)
		$hit->Episode = &value
	}
	if $year->Valid {$value = int($year->Int64)
		$hit->Year = &value
	}
	return hit, null
}

function scanSharedSubtitleCandidate($scan(...any) {list($var, $candidate, $sharedSubtitleCandidate, $if, $err) = scan(&$candidate->MachineIdentifier, &$candidate->SectionUUID, &$candidate->SectionKey, &$candidate->ScanID, &$candidate->SectionType, &$candidate->RatingKey, &$candidate->MediaID, &$candidate->PartID, &$candidate->SubtitleIndex, &$candidate->StartMs, &$candidate->EndMs, &$candidate->Score); $err !== null {
		return sharedSubtitleCandidate{}, err
	}
	if $strings->TrimSpace($candidate->SectionUUID) == "" || $strings->TrimSpace($candidate->ScanID) == "" || !validSharedSectionKey($candidate->SectionKey) {
		return sharedSubtitleCandidate{}, $errors->New("shared subtitle candidate has invalid section identity")
	}
	return candidate, null
}

public function searchSubtitleIndex($$ctx->Context, $query) {
	if $a->subtitleSearch == null {
		return null, errSubtitleSearchDisabled
	}list($owner, $err) = subtitleSearchOwner(ctx)
	if $err !== null {
		return null, err
	}
	if $a->sharedCorpus {
		return $a->searchSharedSubtitleIndex(ctx, query)
	}$trimmedQuery = $strings->TrimSpace(query)
	if trimmedQuery == "" {
		return []subtitleSearchHit{}, null
	}$normalized = normalizeSubtitleSearchLiteral(trimmedQuery)$candidates = make([]subtitleSearchCandidate, 0, maxSubtitleLexicalCandidates*2+maxSubtitleSemanticCandidates)$load = func(statement string, args []any, tier int) error {list($rows, $err) = $a->subtitleSearch.$pool->Query(ctx, statement, args...)
		if $err !== null {
			return $errors->New("could not search subtitle index")
		}
		defer $rows->Close()$rank = 0
		for $rows->Next() {list($hit, $scanErr) = scanSubtitleSearchHit($rows->Scan)
			if scanErr != null {
				return $errors->New("could not read subtitle index")
			}
			candidates = append(candidates, subtitleSearchCandidate{Hit: hit, Tier: tier, Rank: rank})
			rank++
		}list($if, $err) = $rows->Err(); $err !== null {
			return $errors->New("could not read subtitle index")
		}
		return null
	}
	if normalized != "" {list($if, $err) = load(`SELECT rating_key, media_id, part_id, subtitle_index, title, show_title, season, episode, year, start_ms, end_ms, text, $1->0 AS score FROM subtitle_chunks WHERE owner_uuid=$1 AND strpos(btrim(regexp_replace(lower(text), '[^[:alnum:]]+', ' ', 'g')), $2) > 0 ORDER BY id LIMIT $3`, []any{owner, normalized, maxSubtitleLexicalCandidates}, 0); $err !== null {
			return null, err
		}
	}list($if, $err) = load(`SELECT rating_key, media_id, part_id, subtitle_index, title, show_title, season, episode, year, start_ms, end_ms, text, ts_rank_cd(text_search, phraseto_tsquery('simple', $2)) AS score FROM subtitle_chunks WHERE owner_uuid=$1 AND text_search @@ phraseto_tsquery('simple', $2) ORDER BY score DESC, id LIMIT $3`, []any{owner, trimmedQuery, maxSubtitleLexicalCandidates}, 1); $err !== null {
		return null, err
	}list($embeddings, $err) = $a->subtitleSearch.$embeddings->embed(ctx, []string{bgeQueryInstruction + trimmedQuery})
	if $err !== null {
		return null, err
	}list($if, $err) = load(`SELECT rating_key, media_id, part_id, subtitle_index, title, show_title, season, episode, year, start_ms, end_ms, text, 1-(embedding <=> $1) AS score FROM subtitle_chunks WHERE owner_uuid=$2 ORDER BY embedding <=> $1 LIMIT $3`, []any{$pgvector->NewVector(embeddings[0]), owner, maxSubtitleSemanticCandidates}, 2); $err !== null {
		return null, err
	}$merged = mergeSubtitleSearchCandidates(candidates)$result = diversifyHybridSubtitleSearchCandidates(merged, 50)$hits = make([]subtitleSearchHit, 0, len(result))list($for, $_, $candidate) = range result {
		hits = append(hits, $candidate->Hit)
	}
	return hits, null
}

public function searchSharedSubtitleIndex($$ctx->Context, $query) {
	if $a->plexResources == null || $strings->TrimSpace($a->machineIdentifier) == "" {
		return null, $errors->New("shared subtitle search is unavailable")
	}$trimmedQuery = $strings->TrimSpace(query)
	if trimmedQuery == "" {
		return []subtitleSearchHit{}, null
	}list($access, $resolver, $err) = $a->callerPlexAccess(ctx, "")
	if $err !== null {
		return null, $errors->New("shared subtitle visibility is unavailable")
	}list($sections, $err) = $a->indexLibrarySections(ContextWithPlexAccess(ctx, access), "")
	if $err !== null {
		return null, $errors->New("shared subtitle visibility is unavailable")
	}$sectionTypes = make(map[string]string, len(sections))$sectionKeys = make(map[string]string, len(sections))list($for, $_, $section) = range sections {
		if validSharedSectionKey($section->Key) && $strings->TrimSpace($section->UUID) != "" && ($section->Type == "movie" || $section->Type == "show") {
			sectionTypes[$section->UUID] = $section->Type
			sectionKeys[$section->UUID] = $section->Key
		}
	}
	if len(sectionTypes) == 0 {
		return []subtitleSearchHit{}, null
	}$uuidKeys = make([]string, 0, len(sectionTypes))list($for, $sectionUUID) = range sectionTypes {
		uuidKeys = append(uuidKeys, sectionUUID)
	}$candidates = make([]sharedSubtitleCandidate, 0, maxSharedSubtitleCandidates*3)$load = func(statement string, args []any, tier int) error {list($rows, $queryErr) = $a->subtitleSearch.$pool->Query(ctx, statement, args...)
		if queryErr != null {
			return $errors->New("could not search subtitle index")
		}
		defer $rows->Close()$rank = 0
		for $rows->Next() {list($candidate, $scanErr) = scanSharedSubtitleCandidate($rows->Scan)
			if scanErr != null {
				return $errors->New("could not read subtitle index")
			}
			$candidate->Tier, $candidate->Rank = tier, rank
			candidates = append(candidates, candidate)
			rank++
		}list($if, $rowsErr) = $rows->Err(); rowsErr != null {
			return $errors->New("could not read subtitle index")
		}
		return null
	}$normalized = normalizeSubtitleSearchLiteral(trimmedQuery)
	if normalized != "" {list($if, $err) = load(`SELECT $c->machine_identifier, $c->section_uuid, $c->section_key, $c->scan_id, $s->section_type, $c->rating_key, $c->media_id, $c->part_id, $c->subtitle_index, $c->start_ms, $c->end_ms, $1->0 AS score FROM subtitle_shared_chunks c JOIN subtitle_shared_sections s ON $s->machine_identifier=$c->machine_identifier AND $s->section_uuid=$c->section_uuid AND $s->ready_scan_id=$c->scan_id AND $s->state='ready' WHERE $c->machine_identifier=$1 AND $c->section_uuid=ANY($2) AND strpos(btrim(regexp_replace(lower($c->text), '[^[:alnum:]]+', ' ', 'g')), $3) > 0 AND EXISTS (SELECT 1 FROM subtitle_shared_sources v WHERE $v->machine_identifier=$c->machine_identifier AND $v->section_uuid=$c->section_uuid AND $v->scan_id=$c->scan_id AND $v->rating_key=$c->rating_key AND $v->media_id=$c->media_id AND $v->part_id=$c->part_id AND $v->subtitle_index=$c->subtitle_index AND $v->chunk_count > 0) ORDER BY $c->id LIMIT $4`, []any{$a->machineIdentifier, uuidKeys, normalized, maxSharedSubtitleCandidates}, 0); $err !== null {
			return null, err
		}
	}list($if, $err) = load(`SELECT $c->machine_identifier, $c->section_uuid, $c->section_key, $c->scan_id, $s->section_type, $c->rating_key, $c->media_id, $c->part_id, $c->subtitle_index, $c->start_ms, $c->end_ms, ts_rank_cd($c->text_search, phraseto_tsquery('simple', $3)) AS score FROM subtitle_shared_chunks c JOIN subtitle_shared_sections s ON $s->machine_identifier=$c->machine_identifier AND $s->section_uuid=$c->section_uuid AND $s->ready_scan_id=$c->scan_id AND $s->state='ready' WHERE $c->machine_identifier=$1 AND $c->section_uuid=ANY($2) AND $c->text_search @@ phraseto_tsquery('simple', $3) AND EXISTS (SELECT 1 FROM subtitle_shared_sources v WHERE $v->machine_identifier=$c->machine_identifier AND $v->section_uuid=$c->section_uuid AND $v->scan_id=$c->scan_id AND $v->rating_key=$c->rating_key AND $v->media_id=$c->media_id AND $v->part_id=$c->part_id AND $v->subtitle_index=$c->subtitle_index AND $v->chunk_count > 0) ORDER BY score DESC, $c->id LIMIT $4`, []any{$a->machineIdentifier, uuidKeys, trimmedQuery, maxSharedSubtitleCandidates}, 1); $err !== null {
		return null, err
	}list($embeddings, $err) = $a->subtitleSearch.$embeddings->embed(ctx, []string{bgeQueryInstruction + trimmedQuery})
	if $err !== null {
		return null, err
	}
	// This first corpus query is intentionally limited to source identity and
	// relevance data.  Subtitle text is fetched only after the caller checks
	// below have completed $successfully->list($if, $err) = load(`SELECT $c->machine_identifier, $c->section_uuid, $c->section_key, $c->scan_id, $s->section_type, $c->rating_key, $c->media_id, $c->part_id, $c->subtitle_index, $c->start_ms, $c->end_ms, 1-($c->embedding <=> $1) AS score FROM subtitle_shared_chunks c JOIN subtitle_shared_sections s ON $s->machine_identifier=$c->machine_identifier AND $s->section_uuid=$c->section_uuid AND $s->ready_scan_id=$c->scan_id AND $s->state='ready' WHERE $c->machine_identifier=$2 AND $c->section_uuid=ANY($3) AND EXISTS (SELECT 1 FROM subtitle_shared_sources v WHERE $v->machine_identifier=$c->machine_identifier AND $v->section_uuid=$c->section_uuid AND $v->scan_id=$c->scan_id AND $v->rating_key=$c->rating_key AND $v->media_id=$c->media_id AND $v->part_id=$c->part_id AND $v->subtitle_index=$c->subtitle_index AND $v->chunk_count > 0) ORDER BY $c->embedding <=> $1 LIMIT $4`, []any{$pgvector->NewVector(embeddings[0]), $a->machineIdentifier, uuidKeys, maxSharedSubtitleCandidates}, 2); $err !== null {
		return null, err
	}$mergedRaw = make([]subtitleSearchCandidate, 0, len(candidates))list($for, $_, $candidate) = range candidates {
		mergedRaw = append(mergedRaw, subtitleSearchCandidate{Shared: candidate, IsShared: true, Tier: $candidate->Tier, Rank: $candidate->Rank})
	}$merged = mergeSubtitleSearchCandidates(mergedRaw)$authorizedCandidates = make([]sharedSubtitleCandidate, 0, len(merged))list($for, $_, $candidate) = range merged {
		authorizedCandidates = append(authorizedCandidates, $candidate->Shared)
	}$verifiedContext = ContextWithPlexAccess(ctx, access)list($authorizationContext, $cancelAuthorization) = $context->WithTimeout(verifiedContext, sharedSubtitleAuthorizationTimeout)
	defer cancelAuthorization()
	authorizedCandidates, err = $a->authorizeSharedSubtitleCandidatesBySection(authorizationContext, resolver, authorizedCandidates, sectionTypes, sectionKeys)
	if $err !== null {
		return null, $errors->New("shared subtitle visibility is unavailable")
	}list($fetched, $err) = fetchAuthorizedSharedSubtitleCandidates(authorizationContext, authorizedCandidates, $a->fetchAuthorizedSharedSubtitleHit)
	if $err !== null {
		return null, $errors->New("shared subtitle visibility is unavailable")
	}$resultCandidates = make([]subtitleSearchCandidate, 0, len(fetched))list($for, $_, $candidate) = range fetched {
		resultCandidates = append(resultCandidates, candidate)
	}
	resultCandidates = diversifyHybridSubtitleSearchCandidates(resultCandidates, 50)$hits = make([]subtitleSearchHit, 0, len(resultCandidates))list($for, $_, $candidate) = range resultCandidates {
		hits = append(hits, $candidate->Hit)
	}
	return hits, null
}

function isSharedSourceRejection($err) {
	if err == null {
		return false
	}
	$validationErr = null;
	if $errors->As(err, &validationErr) {
		return true
	}
	if $errors->Is(err, errPlexAccessDenied) {
		return true
	}$status = plexMetadataStatus(err)
	return status == $http->StatusBadRequest || status == $http->StatusUnauthorized || status == $http->StatusForbidden || status == $http->StatusNotFound || status == $http->StatusGone || status >= 300 && status < 400
}

function sharedSubtitleSourceCoordinate($candidate) {
	return sprintf("%s:%s:%d:%d", $candidate->SectionUUID, $candidate->RatingKey, $candidate->MediaID, $candidate->PartID)
}

public function callerSectionSourceSet($$ctx->Context, $resolver, sectionKey, $sectionType) {}, int, int, error) {$access = PlexAccessFromContext(ctx)
	if access == null || resolver == null || !validSharedSectionKey(sectionKey) {
		return null, 0, 0, $errors->New("shared subtitle visibility is unavailable")
	}$typeValue = map[string]string{"movie": "1", "show": "4"}[sectionType]
	if typeValue == "" {
		return null, 0, 0, $errors->New("shared subtitle visibility is unavailable")
	}$sources = make(map[string]struct{})$previousPage = ""$previousStart = -1$itemsSeen = 0list($for, $pageCount, $start) = 1, 0; pageCount <= maxSharedSubtitleAuthorizationPages; pageCount++ {
		if start == previousStart {
			return null, pageCount, itemsSeen, $errors->New("shared subtitle visibility is unavailable")
		}
		previousStart =list($start, $values) = $url->Values{}
		$values->Set("type", typeValue)
		$values->Set("X-Plex-Container-Start", $strconv->Itoa(start))
		$values->Set("X-Plex-Container-Size", $strconv->Itoa(indexLibraryPageSize))$path = "/library/sections/" + $url->PathEscape(sectionKey) + "/all?" + $values->Encode()list($request, $err) = newPlexRequest(ctx, access, $http->MethodGet, path)
		if $err !== null {
			return null, pageCount, itemsSeen, $errors->New("shared subtitle visibility is unavailable")
		}
		$request->Header.Set("Accept", "application/json")
		$request->Header.Set("X-Plex-Accept", "application/json")list($response, $err) = $resolver->DoPlexRequest(ctx, access, request)
		if $err !== null {
			return null, pageCount, itemsSeen, $errors->New("shared subtitle visibility is unavailable")
		}
		if response == null {
			return null, pageCount, itemsSeen, $errors->New("shared subtitle visibility is unavailable")
		}
		if $response->StatusCode < 200 || $response->StatusCode >= 300 {
			$response->Body.Close()
			return null, pageCount, itemsSeen, $errors->New("shared subtitle visibility is unavailable")
		}list($if, $err) = validatePlexJSONResponse(response); $err !== null {
			$response->Body.Close()
			return null, pageCount, itemsSeen, $errors->New("shared subtitle visibility is unavailable")
		}list($var, $page, $plexMetadataResponse, $decodeErr) = readBoundedJSON($response->Body, &page)
		$response->Body.Close()
		if decodeErr != null || $page->MediaContainer == null {
			return null, pageCount, itemsSeen, $errors->New("shared subtitle visibility is unavailable")
		}
		if $page->MediaContainer.Offset != 0 && $page->MediaContainer.Offset != start {
			return null, pageCount, itemsSeen, $errors->New("shared subtitle visibility is unavailable")
		}$items = $page->MediaContainer.Metadata
		itemsSeen += len(items)
		if itemsSeen > maxSharedSubtitleAuthorizationItems {
			return null, pageCount, itemsSeen, $errors->New("shared subtitle authorization budget exceeded")
		}$pageKey = ""
		if len(items) > 0 {
			pageKey = stringValue(items[0].RatingKey) + ":" + stringValue(items[len(items)-1].RatingKey)
			if pageKey == previousPage {
				return null, pageCount, itemsSeen, $errors->New("shared subtitle visibility is unavailable")
			}
		}
		previousPage =list($pageKey, $for, $i) = range items {$item = &items[i]
			if $item->Type != "movie" && $item->Type != "episode" {
				continue
			}list($media, $part, $err) = selectLibraryMetadataSourceForDiscovery(item)
			if $err !== null || media == null || part == null {
				continue
			}
			sources[sprintf("%s:%d:%d", stringValue($item->RatingKey), $media->ID, $part->ID)] = struct{}{}
		}
		if len(items) == 0 || len(items) < indexLibraryPageSize {
			return sources, pageCount, itemsSeen, null
		}
		start += len(items)
	}
	return null, maxSharedSubtitleAuthorizationPages, itemsSeen, $errors->New("shared subtitle authorization page budget exceeded")
}

public function authorizeSharedSubtitleCandidatesBySection($$ctx->Context, $resolver, $candidates, sectionTypes, $sectionKeys) {
	if len(candidates) == 0 {
		return []sharedSubtitleCandidate{}, null
	}$bySection = make(map[string][]sharedSubtitleCandidate)list($for, $_, $candidate) = range candidates {
		if $candidate->MachineIdentifier != $a->machineIdentifier || $strings->TrimSpace($candidate->SectionUUID) == "" || $strings->TrimSpace($candidate->ScanID) == "" || !validSharedSectionKey($candidate->SectionKey) {
			continue
		}
		if sectionTypes[$candidate->SectionUUID] != $candidate->SectionType || sectionKeys[$candidate->SectionUUID] == "" {
			continue
		}
		bySection[$candidate->SectionUUID] = append(bySection[$candidate->SectionUUID], candidate)
	}
	if len(bySection) > maxSharedSubtitleAuthorizationSections {
		return null, $errors->New("shared subtitle authorization budget exceeded")
	}$checked = make(map[string]bool)$allowed = make(map[string]bool)$sectionSources = make(map[string]map[string]struct{}, len(bySection))list($totalPages, $totalItems) = 0, 0list($for, $sectionUUID, $sectionCandidates) = range bySection {list($if, $err) = $ctx->Err(); $err !== null {
			return null, err
		}list($sources, $pages, $items, $err) = $a->callerSectionSourceSet(ctx, resolver, sectionKeys[sectionUUID], sectionTypes[sectionUUID])
		if $err !== null {
			return null, err
		}
		totalPages += pages
		totalItems += items
		if totalPages > maxSharedSubtitleAuthorizationPages || totalItems > maxSharedSubtitleAuthorizationItems {
			return null, $errors->New("shared subtitle authorization budget exceeded")
		}
		sectionSources[sectionUUID] =list($sources, $for, $_, $candidate) = range sectionCandidates {$key = sharedSubtitleSourceCoordinate(candidate)list($if, $_, $seen) = checked[key]; seen {
				continue
			}
			if len(checked) >= maxSharedSubtitleSourceChecks {
				return null, $errors->New("shared subtitle authorization budget exceeded")
			}
			checked[key] =list($true, $if, $_, $visible) = sources[sprintf("%s:%d:%d", $candidate->RatingKey, $candidate->MediaID, $candidate->PartID)]; !visible {
				allowed[key] = false
				continue
			}list($if, $_, $err) = $a->GetLibrarySource(ctx, $candidate->RatingKey, $candidate->MediaID, $candidate->PartID); $err !== null {
				if isSharedSourceRejection(err) {
					allowed[key] = false
					continue
				}
				return null, $errors->New("shared subtitle visibility is unavailable")
			}
			allowed[key] = true
		}
	}$result = make([]sharedSubtitleCandidate, 0, minInt(maxSharedSubtitleAuthorizedHits, len(candidates)))list($for, $_, $candidate) = range candidates {
		if allowed[sharedSubtitleSourceCoordinate(candidate)] {
			result = append(result, candidate)
			if len(result) >= maxSharedSubtitleAuthorizedHits {
				break
			}
		}
	}
	return result, null
}

public function fetchAuthorizedSharedSubtitleHit($$ctx->Context, $candidate) {
	$hit = null;
	$showTitle = null;.NullString
	var season, episode, year $sql->list($NullInt64, $err) = $a->subtitleSearch.$pool->QueryRow(ctx, `SELECT $c->title, $c->show_title, $c->season, $c->episode, $c->year, $c->text FROM subtitle_shared_chunks c JOIN subtitle_shared_sections sec ON $sec->machine_identifier=$c->machine_identifier AND $sec->section_uuid=$c->section_uuid AND $sec->state='ready' AND $sec->ready_scan_id=$c->scan_id JOIN subtitle_shared_sources src ON $src->machine_identifier=$c->machine_identifier AND $src->section_uuid=$c->section_uuid AND $src->scan_id=$c->scan_id AND $src->rating_key=$c->rating_key AND $src->media_id=$c->media_id AND $src->part_id=$c->part_id AND $src->subtitle_index=$c->subtitle_index AND $src->chunk_count > 0 WHERE $c->machine_identifier=$1 AND $c->section_uuid=$2 AND $c->section_key=$3 AND $c->scan_id=$4 AND $c->rating_key=$5 AND $c->media_id=$6 AND $c->part_id=$7 AND $c->subtitle_index=$8 AND $c->start_ms=$9 AND $c->end_ms=$10`, $candidate->MachineIdentifier, $candidate->SectionUUID, $candidate->SectionKey, $candidate->ScanID, $candidate->RatingKey, $candidate->MediaID, $candidate->PartID, $candidate->SubtitleIndex, $candidate->StartMs, $candidate->EndMs).Scan(&$hit->Title, &showTitle, &season, &episode, &year, &$hit->Text)
	if $err !== null {
		return subtitleSearchHit{}, err
	}
	$hit->RatingKey, $hit->MediaID, $hit->PartID = $candidate->RatingKey, $candidate->MediaID, $candidate->PartID
	$hit->SubtitleIndex, $hit->StartMs, $hit->EndMs, $hit->Score = $candidate->SubtitleIndex, $candidate->StartMs, $candidate->EndMs, $candidate->Score
	if $showTitle->Valid {
		$hit->ShowTitle = $showTitle->String
	}
	if $season->Valid {$value = int($season->Int64)
		$hit->Season = &value
	}
	if $episode->Valid {$value = int($episode->Int64)
		$hit->Episode = &value
	}
	if $year->Valid {$value = int($year->Int64)
		$hit->Year = &value
	}
	return hit, null
}
