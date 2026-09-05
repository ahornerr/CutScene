package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/LukeHagar/plexgo/models/components"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const (
	indexLibraryPageSize  = 100
	maxIndexLibraryPages  = 10000
	subtitleBulkWorkers   = 3
	subtitleBulkQueueSize = 6
)

var (
	errStopPagination       = errors.New("stop pagination")
	errPaginationIncomplete = errors.New("section pagination ended prematurely")
)

type subtitleIndexJobManager struct {
	app      *Application
	mu       sync.Mutex
	active   *subtitleIndexJob
	jobs     map[string]*subtitleIndexJob
	cancel   context.CancelFunc
	lifetime context.Context
}

type subtitleIndexJob struct {
	mu                                                            sync.RWMutex
	persistMu                                                     sync.Mutex
	id                                                            string
	owner                                                         string
	state                                                         string
	discovered, processed, indexed, skipped, failed               int
	unchangedTracks, unsupportedTracks, emptyTracks, failedTracks int
	sourceValidationSkips                                         int
	currentTitle                                                  string
	startedAt, updatedAt                                          time.Time
	finishedAt                                                    *time.Time
	err                                                           string
	sourceErrorSamples                                            []string
	sourceElapsedNanos                                            int64
	completedSources                                              int
	scanID                                                        string
}

type subtitleIndexJobStatus struct {
	ID                    string     `json:"id"`
	State                 string     `json:"state"`
	Discovered            int        `json:"discovered"`
	Processed             int        `json:"processed"`
	IndexedChunks         int        `json:"indexedChunks"`
	Skipped               int        `json:"skipped"`
	Failed                int        `json:"failed"`
	DiscoveredSources     int        `json:"discoveredSources"`
	ProcessedSources      int        `json:"processedSources"`
	UnchangedTracks       int        `json:"unchangedTracks"`
	UnsupportedTracks     int        `json:"unsupportedTracks"`
	EmptyTracks           int        `json:"emptyTracks"`
	FailedTracks          int        `json:"failedTracks"`
	SourceValidationSkips int        `json:"sourceValidationSkips"`
	CurrentTitle          string     `json:"currentTitle,omitempty"`
	StartedAt             time.Time  `json:"startedAt"`
	UpdatedAt             time.Time  `json:"updatedAt"`
	FinishedAt            *time.Time `json:"finishedAt,omitempty"`
	Error                 string     `json:"error,omitempty"`
}

func newSubtitleIndexJobManager(app *Application) *subtitleIndexJobManager {
	parent := context.Background()
	if app != nil && app.lifetime != nil {
		parent = app.lifetime
	}
	lifetime, cancel := context.WithCancel(parent)
	return &subtitleIndexJobManager{app: app, jobs: make(map[string]*subtitleIndexJob), cancel: cancel, lifetime: lifetime}
}

func (j *subtitleIndexJob) status() subtitleIndexJobStatus {
	j.mu.RLock()
	defer j.mu.RUnlock()
	return subtitleIndexJobStatus{ID: j.id, State: j.state, Discovered: j.discovered, Processed: j.processed, DiscoveredSources: j.discovered, ProcessedSources: j.processed, IndexedChunks: j.indexed, Skipped: j.skipped, Failed: j.failed, UnchangedTracks: j.unchangedTracks, UnsupportedTracks: j.unsupportedTracks, EmptyTracks: j.emptyTracks, FailedTracks: j.failedTracks, SourceValidationSkips: j.sourceValidationSkips, CurrentTitle: j.currentTitle, StartedAt: j.startedAt, UpdatedAt: j.updatedAt, FinishedAt: j.finishedAt, Error: j.err}
}

func (j *subtitleIndexJob) recordSourceFailure(source LibrarySearchResult, err error, failedTracks int) {
	diagnostic := redactedDiagnostic(err)
	if diagnostic == "" {
		diagnostic = "source processing failed"
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	j.failed++
	if failedTracks < 1 {
		failedTracks = 1
	}
	j.failedTracks += failedTracks
	if len(j.sourceErrorSamples) < 3 {
		j.sourceErrorSamples = append(j.sourceErrorSamples, diagnostic)
	}
	if len(j.sourceErrorSamples) > 0 {
		message := fmt.Sprintf("source failures (%d): %s", j.failed, strings.Join(j.sourceErrorSamples, " | "))
		if len(message) > 1200 {
			message = message[:1200]
		}
		j.err = message
	}
	_ = source
}

func (j *subtitleIndexJob) recordIndexResult(result subtitleSearchIndexResponse) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.indexed += result.Indexed
	j.skipped += len(result.Skipped)
	j.unchangedTracks += result.UnchangedTracks
	j.unsupportedTracks += result.UnsupportedTracks
	j.emptyTracks += result.EmptyTracks
}

var errSubtitleIndexActive = errors.New("a whole-library subtitle index job is already active")

func (m *subtitleIndexJobManager) start(ctx context.Context, owner string) (*subtitleIndexJob, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.active != nil {
		return nil, errSubtitleIndexActive
	}
	job, err := m.loadOrCreatePersistedJob(owner)
	if err != nil {
		return nil, err
	}
	m.active, m.jobs[job.id] = job, job
	go m.run(ctx, job)
	return job, nil
}

func (m *subtitleIndexJobManager) loadOrCreatePersistedJob(owner string) (*subtitleIndexJob, error) {
	now := time.Now().UTC()
	if m.app.subtitleSearch == nil {
		return &subtitleIndexJob{id: uuid.NewString(), owner: owner, state: "queued", startedAt: now, updatedAt: now}, nil
	}
	var job subtitleIndexJob
	var current, safeError sql.NullString
	var finished sql.NullTime
	err := m.app.subtitleSearch.pool.QueryRow(context.Background(), `SELECT id::text, state, discovered, processed, indexed_chunks, skipped, failed, unchanged_tracks, unsupported_tracks, empty_tracks, failed_tracks, current_title, started_at, updated_at, finished_at, error FROM subtitle_index_jobs WHERE owner_uuid=$1 ORDER BY updated_at DESC LIMIT 1`, owner).Scan(&job.id, &job.state, &job.discovered, &job.processed, &job.indexed, &job.skipped, &job.failed, &job.unchangedTracks, &job.unsupportedTracks, &job.emptyTracks, &job.failedTracks, &current, &job.startedAt, &job.updatedAt, &finished, &safeError)
	if errors.Is(err, pgx.ErrNoRows) || errors.Is(err, sql.ErrNoRows) {
		job.id, job.owner, job.state, job.startedAt, job.updatedAt = uuid.NewString(), owner, "queued", now, now
		if _, err := m.app.subtitleSearch.pool.Exec(context.Background(), `INSERT INTO subtitle_index_jobs (id, owner_uuid, state, started_at, updated_at) VALUES ($1,$2,$3,$4,$5)`, job.id, owner, job.state, job.startedAt, job.updatedAt); err != nil {
			return nil, errors.New("could not create subtitle index job")
		}
		return &job, nil
	}
	if err != nil {
		return nil, errors.New("could not load subtitle index job")
	}
	job.owner = owner
	if current.Valid {
		job.currentTitle = current.String
	}
	if finished.Valid {
		job.finishedAt = &finished.Time
	}
	if safeError.Valid {
		job.err = safeError.String
	}
	if job.state != "interrupted" {
		job.id, job.state, job.startedAt, job.updatedAt = uuid.NewString(), "queued", now, now
		job.discovered, job.processed, job.indexed, job.skipped, job.failed = 0, 0, 0, 0, 0
		job.unchangedTracks, job.unsupportedTracks, job.emptyTracks, job.failedTracks = 0, 0, 0, 0
		job.sourceValidationSkips = 0
		job.currentTitle, job.err = "", ""
		job.finishedAt = nil
		if _, err := m.app.subtitleSearch.pool.Exec(context.Background(), `INSERT INTO subtitle_index_jobs (id, owner_uuid, state, started_at, updated_at) VALUES ($1,$2,$3,$4,$5)`, job.id, owner, job.state, job.startedAt, job.updatedAt); err != nil {
			return nil, errors.New("could not create subtitle index job")
		}
	}
	job.state = "queued"
	job.finishedAt = nil
	job.updatedAt = now
	return &job, nil
}

func (m *subtitleIndexJobManager) recoverPersistedJobs() error {
	if m == nil || m.app == nil || m.app.subtitleSearch == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := m.app.subtitleSearch.pool.Exec(ctx, `UPDATE subtitle_index_jobs SET state='interrupted', updated_at=NOW(), error='indexing interrupted; rescan resumes completed sources' WHERE state IN ('queued','running')`)
	if err != nil {
		return errors.New("could not recover subtitle index jobs")
	}
	if m.app.sharedCorpus && strings.TrimSpace(m.app.machineIdentifier) != "" {
		_, _ = m.app.subtitleSearch.pool.Exec(ctx, `UPDATE subtitle_shared_sections SET state=CASE WHEN ready THEN 'ready' ELSE 'failed' END, scan_id=COALESCE(ready_scan_id, scan_id) WHERE machine_identifier=$1 AND state='building'`, m.app.machineIdentifier)
		_, _ = m.app.subtitleSearch.pool.Exec(ctx, `DELETE FROM subtitle_shared_chunks WHERE machine_identifier=$1 AND scan_id NOT IN (SELECT ready_scan_id FROM subtitle_shared_sections WHERE machine_identifier=$1 AND ready=TRUE AND ready_scan_id IS NOT NULL)`, m.app.machineIdentifier)
		_, _ = m.app.subtitleSearch.pool.Exec(ctx, `DELETE FROM subtitle_shared_sources WHERE machine_identifier=$1 AND scan_id NOT IN (SELECT ready_scan_id FROM subtitle_shared_sections WHERE machine_identifier=$1 AND ready=TRUE AND ready_scan_id IS NOT NULL)`, m.app.machineIdentifier)
	}
	return nil
}

func (m *subtitleIndexJobManager) get(id, owner string) (*subtitleIndexJob, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	job, ok := m.jobs[id]
	if ok && job.owner == owner {
		return job, true
	}
	if m.app.subtitleSearch == nil {
		return nil, false
	}
	var persisted subtitleIndexJob
	var current, safeError sql.NullString
	var finished sql.NullTime
	err := m.app.subtitleSearch.pool.QueryRow(context.Background(), `SELECT id::text, state, discovered, processed, indexed_chunks, skipped, failed, unchanged_tracks, unsupported_tracks, empty_tracks, failed_tracks, current_title, started_at, updated_at, finished_at, error FROM subtitle_index_jobs WHERE id=$1 AND owner_uuid=$2`, id, owner).Scan(&persisted.id, &persisted.state, &persisted.discovered, &persisted.processed, &persisted.indexed, &persisted.skipped, &persisted.failed, &persisted.unchangedTracks, &persisted.unsupportedTracks, &persisted.emptyTracks, &persisted.failedTracks, &current, &persisted.startedAt, &persisted.updatedAt, &finished, &safeError)
	if err != nil {
		return nil, false
	}
	persisted.owner = owner
	if current.Valid {
		persisted.currentTitle = current.String
	}
	if finished.Valid {
		persisted.finishedAt = &finished.Time
	}
	if safeError.Valid {
		persisted.err = safeError.String
	}
	m.jobs[id] = &persisted
	return &persisted, true
}

func (m *subtitleIndexJobManager) current(owner string) (*subtitleIndexJob, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.app.subtitleSearch == nil {
		return nil, false, nil
	}
	var job subtitleIndexJob
	var current, safeError sql.NullString
	var finished sql.NullTime
	err := m.app.subtitleSearch.pool.QueryRow(context.Background(), `SELECT id::text, state, discovered, processed, indexed_chunks, skipped, failed, unchanged_tracks, unsupported_tracks, empty_tracks, failed_tracks, current_title, started_at, updated_at, finished_at, error FROM subtitle_index_jobs WHERE owner_uuid=$1 ORDER BY CASE WHEN state IN ('queued','running') THEN 0 ELSE 1 END, updated_at DESC LIMIT 1`, owner).Scan(&job.id, &job.state, &job.discovered, &job.processed, &job.indexed, &job.skipped, &job.failed, &job.unchangedTracks, &job.unsupportedTracks, &job.emptyTracks, &job.failedTracks, &current, &job.startedAt, &job.updatedAt, &finished, &safeError)
	if errors.Is(err, pgx.ErrNoRows) || errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, errors.New("could not load subtitle index job")
	}
	job.owner = owner
	if current.Valid {
		job.currentTitle = current.String
	}
	if finished.Valid {
		job.finishedAt = &finished.Time
	}
	if safeError.Valid {
		job.err = safeError.String
	}
	if active, ok := m.jobs[job.id]; ok {
		return active, true, nil
	}
	m.jobs[job.id] = &job
	return &job, true, nil
}

func (m *subtitleIndexJobManager) run(parent context.Context, job *subtitleIndexJob) {
	jobContext, cancelJob := context.WithCancel(parent)
	stopLifetime := context.AfterFunc(m.lifetime, cancelJob)
	defer stopLifetime()
	defer cancelJob()
	ctx, cancel := m.app.operationContext(jobContext)
	defer cancel()
	ctx = withBulkSubtitleContext(ctx)
	job.mu.Lock()
	job.scanID = uuid.NewString()
	job.mu.Unlock()
	if UserFromContext(ctx) != nil && AuthTokenFromContext(ctx) != nil && m.app.plexResources != nil {
		access, accessErr := m.app.resolvePlexAccess(ctx)
		if accessErr != nil {
			m.finish(job, "failed", "could not resolve caller Plex access")
			return
		}
		ctx = ContextWithPlexAccess(ctx, access)
	}
	job.mu.Lock()
	job.state = "running"
	job.updatedAt = time.Now().UTC()
	job.mu.Unlock()
	_ = m.persistJob(job)
	log.Printf("subtitle index-all job started owner=%s", job.owner)
	err := m.app.discoverAndIndexSources(ctx, job, func(work subtitleIndexWork) error {
		source := work.source
		if ctx.Err() != nil {
			return ctx.Err()
		}
		started := time.Now()
		job.mu.Lock()
		job.currentTitle = source.Title
		job.updatedAt = time.Now().UTC()
		job.mu.Unlock()
		_ = m.persistJob(job)
		request := subtitleSearchIndexRequest{RatingKey: source.RatingKey, MediaID: source.MediaID, PartID: source.PartID, SectionUUID: work.sectionUUID, SectionKey: work.sectionKey, ScanID: work.scanID}
		result, indexErr := m.app.indexSubtitleSource(ctx, request)
		if indexErr != nil {
			if errors.Is(indexErr, context.Canceled) || errors.Is(indexErr, context.DeadlineExceeded) || ctx.Err() != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				return indexErr
			}
			job.recordSourceFailure(source, indexErr, result.FailedTracks)
			job.recordIndexResult(result)
			job.mu.Lock()
			failed := job.failed
			job.processed++
			job.completedSources++
			job.sourceElapsedNanos += time.Since(started).Nanoseconds()
			job.mu.Unlock()
			_ = m.persistJob(job)
			if failed <= 3 || failed%100 == 0 {
				log.Printf("subtitle index-all source failed owner=%s title=%q: %s", job.owner, source.Title, redactedDiagnostic(indexErr))
			}
			if m.app.sharedCorpus {
				return indexErr
			}
			return nil
		}
		job.recordIndexResult(result)
		job.mu.Lock()
		job.processed++
		job.completedSources++
		job.sourceElapsedNanos += time.Since(started).Nanoseconds()
		job.updatedAt = time.Now().UTC()
		job.mu.Unlock()
		_ = m.persistJob(job)
		return nil
	})
	if err != nil {
		log.Printf("subtitle index-all discovery failed owner=%s: %s", job.owner, redactedDiagnostic(err))
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || ctx.Err() != nil {
			m.finish(job, "cancelled", "indexing cancelled")
		} else {
			m.finish(job, "failed", "could not discover Plex libraries")
		}
		return
	}
	m.finish(job, "succeeded", "")
}

func (m *subtitleIndexJobManager) finish(job *subtitleIndexJob, state, safeError string) {
	now := time.Now().UTC()
	job.mu.Lock()
	job.state = state
	job.finishedAt = &now
	job.updatedAt = now
	job.currentTitle = ""
	if safeError != "" {
		job.err = safeError
	}
	job.mu.Unlock()
	_ = m.persistJob(job)
	m.mu.Lock()
	if m.active == job {
		m.active = nil
	}
	m.mu.Unlock()
	status := job.status()
	job.mu.RLock()
	completed, elapsed := job.completedSources, job.sourceElapsedNanos
	job.mu.RUnlock()
	averageMS := float64(0)
	if completed > 0 {
		averageMS = float64(elapsed) / float64(completed) / float64(time.Millisecond)
	}
	log.Printf("subtitle index-all job finished owner=%s state=%s discovered=%d processed=%d indexed_chunks=%d skipped=%d failed=%d average_source_ms=%.1f", job.owner, status.State, status.Discovered, status.Processed, status.IndexedChunks, status.Skipped, status.Failed, averageMS)
}

func (m *subtitleIndexJobManager) persistJob(job *subtitleIndexJob) error {
	if m == nil || m.app == nil || m.app.subtitleSearch == nil {
		return nil
	}
	job.persistMu.Lock()
	defer job.persistMu.Unlock()
	job.mu.RLock()
	defer job.mu.RUnlock()
	_, err := m.app.subtitleSearch.pool.Exec(context.Background(), `UPDATE subtitle_index_jobs SET state=$2, discovered=$3, processed=$4, indexed_chunks=$5, skipped=$6, failed=$7, unchanged_tracks=$8, unsupported_tracks=$9, empty_tracks=$10, failed_tracks=$11, current_title=$12, started_at=$13, updated_at=$14, finished_at=$15, error=$16 WHERE id=$1`, job.id, job.state, job.discovered, job.processed, job.indexed, job.skipped, job.failed, job.unchangedTracks, job.unsupportedTracks, job.emptyTracks, job.failedTracks, nullableString(job.currentTitle), job.startedAt, job.updatedAt, job.finishedAt, nullableString(job.err))
	return err
}

type plexSection struct {
	Key  string `json:"key"`
	Type string `json:"type"`
	UUID string `json:"uuid"`
}

type subtitleIndexWork struct {
	source      LibrarySearchResult
	sectionUUID string
	sectionKey  string
	scanID      string
}

func validSharedSectionKey(key string) bool {
	key = strings.TrimSpace(key)
	return key != "" && len(key) <= 128 && !strings.ContainsAny(key, "\r\n/\\")
}

func readBoundedJSON(body io.Reader, target any) error {
	data, err := io.ReadAll(io.LimitReader(body, maxLibraryResponseBytes+1))
	if err != nil || len(data) > maxLibraryResponseBytes {
		return errors.New("response exceeds size limit")
	}
	// Use the same PMS compatibility boundary as the existing library paths.
	// Plex commonly quotes numeric attributes in paginated library responses.
	if err := unmarshalPlexLibraryJSON(data, target); err != nil {
		return err
	}
	return nil
}

func logPlexDecodeFailure(stage string, response *http.Response, err error) {
	contentType := strings.TrimSpace(response.Header.Get("Content-Type"))
	log.Printf("subtitle index-all %s JSON decode failed content_type=%q error=%s", stage, contentType, redactedDiagnostic(err))
}

func validatePlexJSONResponse(resp *http.Response) error {
	contentType := strings.TrimSpace(resp.Header.Get("Content-Type"))
	if contentType == "" {
		return nil
	}
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil || !strings.EqualFold(mediaType, "application/json") {
		return fmt.Errorf("Plex returned unexpected content type %q", mediaType)
	}
	return nil
}

func (a *Application) indexLibrarySections(ctx context.Context, token string) ([]plexSection, error) {
	access, resolver, err := a.callerPlexAccess(ctx, token)
	if err != nil {
		return nil, errors.New("caller Plex access is unavailable")
	}
	req, err := newPlexRequest(ctx, access, http.MethodGet, "/library/sections")
	if err != nil {
		return nil, errors.New("could not create Plex sections request")
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Plex-Accept", "application/json")
	resp, err := resolver.DoPlexRequest(ctx, access, req)
	if err != nil {
		return nil, errors.New("could not load Plex sections")
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("Plex sections returned status %d", resp.StatusCode)
	}
	if err := validatePlexJSONResponse(resp); err != nil {
		return nil, err
	}
	var payload struct {
		MediaContainer struct {
			Directory []plexSection `json:"Directory"`
		} `json:"MediaContainer"`
	}
	if err := readBoundedJSON(resp.Body, &payload); err != nil {
		logPlexDecodeFailure("section discovery", resp, err)
		return nil, errors.New("could not decode Plex sections")
	}
	log.Printf("subtitle index-all discovered Plex sections count=%d", len(payload.MediaContainer.Directory))
	return payload.MediaContainer.Directory, nil
}

func (a *Application) indexLibrarySectionSources(ctx context.Context, token, key, sectionType string) ([]components.Metadata, error) {
	all := make([]components.Metadata, 0)
	err := a.streamIndexLibrarySectionSources(ctx, token, key, sectionType, func(items []components.Metadata) error {
		all = append(all, items...)
		return nil
	})
	return all, err
}

func (a *Application) discoverIndexSources(ctx context.Context, job *subtitleIndexJob) ([]LibrarySearchResult, error) {
	result := make([]LibrarySearchResult, 0)
	err := a.discoverAndIndexSources(ctx, job, func(work subtitleIndexWork) error { result = append(result, work.source); return nil })
	return result, err
}

func (a *Application) streamIndexLibrarySectionSources(ctx context.Context, token, key, sectionType string, consume func([]components.Metadata) error) error {
	access, resolver, err := a.callerPlexAccess(ctx, token)
	if err != nil {
		return errors.New("caller Plex access is unavailable")
	}
	start, previous, pageCount := 0, -1, 0
	seenPages := make(map[string]bool)
	typeValue := map[string]string{"movie": "1", "show": "4"}[sectionType]
	if typeValue == "" {
		return errors.New("unsupported Plex section type")
	}
	log.Printf("subtitle index-all section started key=%s type=%s", key, sectionType)
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		pageCount++
		if pageCount > maxIndexLibraryPages {
			return errors.New("Plex section exceeded pagination safety limit")
		}
		if start == previous {
			return errors.New("Plex section pagination made no progress")
		}
		previous = start
		query := url.Values{}
		query.Set("type", typeValue)
		query.Set("X-Plex-Container-Start", strconv.Itoa(start))
		query.Set("X-Plex-Container-Size", strconv.Itoa(indexLibraryPageSize))
		path := "/library/sections/" + url.PathEscape(key) + "/all?" + query.Encode()
		req, err := newPlexRequest(ctx, access, http.MethodGet, path)
		if err != nil {
			return errors.New("could not create Plex section request")
		}
		req.Header.Set("Accept", "application/json")
		req.Header.Set("X-Plex-Accept", "application/json")
		resp, err := resolver.DoPlexRequest(ctx, access, req)
		if err != nil {
			return errors.New("could not load Plex section")
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			resp.Body.Close()
			return fmt.Errorf("Plex section returned status %d", resp.StatusCode)
		}
		if err := validatePlexJSONResponse(resp); err != nil {
			resp.Body.Close()
			return err
		}
		var page plexMetadataResponse
		decodeErr := readBoundedJSON(resp.Body, &page)
		resp.Body.Close()
		if decodeErr != nil {
			logPlexDecodeFailure("section page", resp, decodeErr)
			return errors.New("could not decode Plex section")
		}
		if page.MediaContainer == nil {
			return errors.New("Plex section response is missing media container")
		}
		items := page.MediaContainer.Metadata
		if page.MediaContainer.Offset != 0 && page.MediaContainer.Offset != start {
			return errors.New("Plex section pagination returned an unexpected offset")
		}
		if len(items) == 0 {
			log.Printf("subtitle index-all section finished key=%s type=%s pages=%d (empty page)", key, sectionType, pageCount)
			return nil
		}
		pageKey := stringValue(items[0].RatingKey) + ":" + stringValue(items[len(items)-1].RatingKey)
		if seenPages[pageKey] {
			log.Printf("subtitle index-all section detected pagination cycle key=%s type=%s page_key=%s", key, sectionType, pageKey)
			if page.MediaContainer.TotalSize > 0 && start >= page.MediaContainer.TotalSize {
				return nil
			}
			return errPaginationIncomplete
		}
		seenPages[pageKey] = true
		if err := consume(items); err != nil {
			if errors.Is(err, errStopPagination) {
				log.Printf("subtitle index-all section stopped key=%s type=%s pages=%d (stop requested)", key, sectionType, pageCount)
				if page.MediaContainer.TotalSize > 0 && start+len(items) >= page.MediaContainer.TotalSize {
					return nil
				}
				return errPaginationIncomplete
			}
			return err
		}
		if pageCount == 1 || pageCount%10 == 0 {
			log.Printf("subtitle index-all section progress key=%s type=%s pages=%d page_items=%d", key, sectionType, pageCount, len(items))
		}
		if len(items) < indexLibraryPageSize {
			log.Printf("subtitle index-all section finished key=%s type=%s pages=%d (last page len=%d)", key, sectionType, pageCount, len(items))
			return nil
		}
		if page.MediaContainer.TotalSize > 0 && start+len(items) >= page.MediaContainer.TotalSize {
			log.Printf("subtitle index-all section reached total size key=%s type=%s total=%d pages=%d", key, sectionType, page.MediaContainer.TotalSize, pageCount)
			return nil
		}
		start += len(items)
	}
}

func (a *Application) discoverAndIndexSources(ctx context.Context, job *subtitleIndexJob, index func(subtitleIndexWork) error) error {
	job.mu.Lock()
	if strings.TrimSpace(job.scanID) == "" {
		job.scanID = uuid.NewString()
	}
	privateScanID := job.scanID
	job.mu.Unlock()
	token := AuthTokenFromContext(ctx)
	if token == nil || strings.TrimSpace(*token) == "" {
		return errors.New("missing auth token")
	}
	sections, err := a.indexLibrarySections(ctx, *token)
	if err != nil {
		return fmt.Errorf("could not discover Plex libraries: %w", err)
	}
	hasIncompleteSection := false
	for _, section := range sections {
		if section.Type != "movie" && section.Type != "show" {
			continue
		}
		scanID := privateScanID
		if a.sharedCorpus {
			if !validSharedSectionKey(section.Key) || strings.TrimSpace(section.UUID) == "" {
				continue
			}
			scanID = uuid.NewString()
			if _, err := a.subtitleSearch.pool.Exec(ctx, `INSERT INTO subtitle_shared_sections (machine_identifier, section_uuid, section_key, section_type, state, scan_id, ready) VALUES ($1,$2,$3,$4,'building',$5,FALSE) ON CONFLICT (machine_identifier, section_uuid) DO UPDATE SET section_key=EXCLUDED.section_key, section_type=EXCLUDED.section_type, state=CASE WHEN subtitle_shared_sections.ready THEN 'ready' ELSE 'building' END, scan_id=EXCLUDED.scan_id, ready_at=subtitle_shared_sections.ready_at, ready=subtitle_shared_sections.ready`, a.machineIdentifier, section.UUID, section.Key, section.Type, scanID); err != nil {
				return errors.New("could not start shared subtitle section scan")
			}
		}
		sectionCtx, cancelSection := context.WithCancel(ctx)
		seen := make(map[string]bool)
		queue := make(chan subtitleIndexWork, subtitleBulkQueueSize)
		var workers sync.WaitGroup
		var workerMu sync.Mutex
		var workerErr error
		itemFailed := false
		for i := 0; i < subtitleBulkWorkers; i++ {
			workers.Add(1)
			go func() {
				defer workers.Done()
				for source := range queue {
					if sectionCtx.Err() != nil {
						continue
					}
					if err := index(source); err != nil {
						workerMu.Lock()
						if errors.Is(err, errSubtitleIndexItem) {
							itemFailed = true
						} else if workerErr == nil {
							workerErr = err
							cancelSection()
						}
						workerMu.Unlock()
					}
				}
			}()
		}
		consecutiveDuplicatePages := 0
		pageErr := a.streamIndexLibrarySectionSources(sectionCtx, *token, section.Key, section.Type, func(items []components.Metadata) error {
			newItemsOnPage := 0
			for _, item := range items {
				if item.Type != "movie" && item.Type != "episode" {
					continue
				}
				media, part, resolveErr := selectLibraryMetadataSourceForDiscovery(&item)
				if resolveErr != nil || media == nil || part == nil {
					job.mu.Lock()
					job.skipped++
					job.sourceValidationSkips++
					job.mu.Unlock()
					continue
				}
				value := librarySearchResultFromMetadata(&item, media, part)
				// Keep the routing identity attached to each worker item. The Plex
				// section key remains mutable metadata and is never an identity.
				value.sectionKey = section.Key
				identity := fmt.Sprintf("%s:%d:%d", value.RatingKey, value.MediaID, value.PartID)
				if seen[identity] {
					continue
				}
				seen[identity] = true
				newItemsOnPage++
				select {
				case queue <- subtitleIndexWork{source: value, sectionUUID: section.UUID, sectionKey: section.Key, scanID: scanID}:
					job.mu.Lock()
					job.discovered++
					job.updatedAt = time.Now().UTC()
					job.mu.Unlock()
				case <-sectionCtx.Done():
					return sectionCtx.Err()
				}
			}
			if len(items) > 0 && newItemsOnPage == 0 {
				consecutiveDuplicatePages++
				if consecutiveDuplicatePages >= 3 {
					log.Printf("subtitle index-all section detected 3 consecutive duplicate pages key=%s, terminating section pagination", section.Key)
					return errStopPagination
				}
			} else {
				consecutiveDuplicatePages = 0
			}
			return nil
		})
		close(queue)
		workers.Wait()
		cancelSection()
		workerMu.Lock()
		currentWorkerErr := workerErr
		currentItemFailed := itemFailed
		workerMu.Unlock()
		if currentWorkerErr != nil {
			if a.sharedCorpus {
				a.abortSharedSubtitleScan(ctx, section.UUID, scanID)
			}
			return currentWorkerErr
		}
		if pageErr != nil {
			if errors.Is(pageErr, errPaginationIncomplete) {
				log.Printf("subtitle index-all section ended prematurely key=%s; skipping publication/pruning to protect index", section.Key)
				hasIncompleteSection = true
				if a.sharedCorpus {
					_ = a.abortSharedSubtitleScan(ctx, section.UUID, scanID)
				}
				continue
			}
			if a.sharedCorpus {
				a.abortSharedSubtitleScan(ctx, section.UUID, scanID)
			}
			return pageErr
		}
		if currentItemFailed {
			if a.sharedCorpus {
				if err := a.abortSharedSubtitleScan(ctx, section.UUID, scanID); err != nil {
					return err
				}
			}
			continue
		}
		if a.sharedCorpus {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if _, err := a.subtitleSearch.pool.Exec(ctx, `UPDATE subtitle_shared_sections SET state='ready', ready_at=NOW(), ready=TRUE, ready_scan_id=scan_id WHERE machine_identifier=$1 AND section_uuid=$2 AND scan_id=$3`, a.machineIdentifier, section.UUID, scanID); err != nil {
				return errors.New("could not publish shared subtitle section")
			}
			if _, err := a.subtitleSearch.pool.Exec(ctx, `DELETE FROM subtitle_shared_chunks c WHERE c.machine_identifier=$1 AND c.section_uuid=$2 AND c.scan_id=$3 AND NOT EXISTS (SELECT 1 FROM subtitle_shared_sources s WHERE s.machine_identifier=c.machine_identifier AND s.section_uuid=c.section_uuid AND s.scan_id=c.scan_id AND s.rating_key=c.rating_key AND s.media_id=c.media_id AND s.part_id=c.part_id AND s.subtitle_index=c.subtitle_index)`, a.machineIdentifier, section.UUID, scanID); err != nil {
				return errors.New("could not prune shared subtitle chunks")
			}
			if _, err := a.subtitleSearch.pool.Exec(ctx, `DELETE FROM subtitle_shared_chunks WHERE machine_identifier=$1 AND section_uuid=$2 AND scan_id <> $3`, a.machineIdentifier, section.UUID, scanID); err != nil {
				return errors.New("could not prune shared subtitle chunks")
			}
			if _, err := a.subtitleSearch.pool.Exec(ctx, `DELETE FROM subtitle_shared_sources WHERE machine_identifier=$1 AND section_uuid=$2 AND scan_id <> $3`, a.machineIdentifier, section.UUID, scanID); err != nil {
				return errors.New("could not prune shared subtitle sources")
			}
		}
	}
	if !a.sharedCorpus {
		job.mu.RLock()
		failed, owner, scanID := job.failed, job.owner, job.scanID
		job.mu.RUnlock()
		if failed == 0 && !hasIncompleteSection {
			if err := a.prunePrivateSubtitleSources(ctx, owner, scanID); err != nil {
				return err
			}
		} else if hasIncompleteSection {
			log.Printf("subtitle index-all private pruning skipped due to incomplete section traversal owner=%s scan_id=%s", owner, scanID)
		}
	}
	if hasIncompleteSection {
		return errPaginationIncomplete
	}
	return nil
}

func (a *Application) prunePrivateSubtitleSources(ctx context.Context, owner, scanID string) error {
	if a.subtitleSearch == nil || strings.TrimSpace(owner) == "" || strings.TrimSpace(scanID) == "" {
		return errors.New("private subtitle scan identity is unavailable")
	}
	tx, err := a.subtitleSearch.pool.Begin(ctx)
	if err != nil {
		return errors.New("could not prune private subtitle index")
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `DELETE FROM subtitle_chunks c WHERE c.owner_uuid=$1 AND NOT EXISTS (SELECT 1 FROM subtitle_index_sources s WHERE s.owner_uuid=c.owner_uuid AND s.rating_key=c.rating_key AND s.media_id=c.media_id AND s.part_id=c.part_id AND s.subtitle_index=c.subtitle_index AND s.scan_id=$2)`, owner, scanID); err != nil {
		return errors.New("could not prune private subtitle chunks")
	}
	if _, err := tx.Exec(ctx, `DELETE FROM subtitle_index_sources WHERE owner_uuid=$1 AND scan_id IS DISTINCT FROM $2`, owner, scanID); err != nil {
		return errors.New("could not prune private subtitle sources")
	}
	if err := tx.Commit(ctx); err != nil {
		return errors.New("could not prune private subtitle index")
	}
	return nil
}

// abortSharedSubtitleScan removes only the unpublished scan. Previously ready
// rows are deliberately left untouched, so an item failure cannot publish or
// prune a partial shared section.
func (a *Application) abortSharedSubtitleScan(ctx context.Context, sectionUUID, scanID string) error {
	if !a.sharedCorpus || a.subtitleSearch == nil || strings.TrimSpace(sectionUUID) == "" || strings.TrimSpace(scanID) == "" {
		return nil
	}
	cleanupCtx := ctx
	if cleanupCtx == nil || cleanupCtx.Err() != nil {
		cleanupCtx = context.Background()
	}
	if _, err := a.subtitleSearch.pool.Exec(cleanupCtx, `DELETE FROM subtitle_shared_chunks WHERE machine_identifier=$1 AND section_uuid=$2 AND scan_id=$3`, a.machineIdentifier, sectionUUID, scanID); err != nil {
		return errors.New("could not discard shared subtitle scan")
	}
	if _, err := a.subtitleSearch.pool.Exec(cleanupCtx, `DELETE FROM subtitle_shared_sources WHERE machine_identifier=$1 AND section_uuid=$2 AND scan_id=$3`, a.machineIdentifier, sectionUUID, scanID); err != nil {
		return errors.New("could not discard shared subtitle scan")
	}
	if _, err := a.subtitleSearch.pool.Exec(cleanupCtx, `UPDATE subtitle_shared_sections SET state=CASE WHEN ready THEN 'ready' ELSE 'failed' END, scan_id=COALESCE(ready_scan_id, scan_id) WHERE machine_identifier=$1 AND section_uuid=$2 AND scan_id=$3`, a.machineIdentifier, sectionUUID, scanID); err != nil {
		return errors.New("could not restore shared subtitle section")
	}
	return nil
}
