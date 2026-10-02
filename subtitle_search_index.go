package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"

	components "github.com/LukeHagar/plexgo/models/components"
	pgx "github.com/jackc/pgx/v5"
	pgvector "github.com/pgvector/pgvector-go"
)

// Subtitle indexing pipeline.
//
// Selects subtitle tracks, extracts and chunks them, embeds the chunks, and
// persists the results into the shared or per-user corpus. Track fingerprints
// are recorded so unchanged tracks are skipped on rescan.

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
