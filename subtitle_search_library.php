<?php




const (
	indexLibraryPageSize  = 100
	maxIndexLibraryPages  = 200000
	subtitleBulkWorkers   = 3
	subtitleBulkQueueSize = 6
)

class subtitleIndexJobManager {    public $app;
    public $mu;
    public $active;
    public $jobs;
    public $cancel;
    public $lifetime;
}

class subtitleIndexJob {    public $mu;
    public $persistMu;
    public $id;
    public $owner;
    public $state;
	discovered, processed, indexed, skipped, failed               int
	unchangedTracks, unsupportedTracks, emptyTracks, failedTracks int
    public $sourceValidationSkips;
    public $currentTitle;
	startedAt, updatedAt                                          $time->Time
    public $finishedAt;
    public $err;
    public $sourceErrorSamples;
    public $sourceElapsedNanos;
    public $completedSources;
    public $scanID;
}

class subtitleIndexJobStatus {    public $ID;
    public $State;
    public $Discovered;
    public $Processed;
    public $IndexedChunks;
    public $Skipped;
    public $Failed;
    public $DiscoveredSources;
    public $ProcessedSources;
    public $UnchangedTracks;
    public $UnsupportedTracks;
    public $EmptyTracks;
    public $FailedTracks;
    public $SourceValidationSkips;
    public $CurrentTitle;
    public $StartedAt;
    public $UpdatedAt;
    public $FinishedAt;
    public $Error;
}

function newSubtitleIndexJobManager($app) {list($lifetime, $cancel) = $context->WithCancel($app->lifetime)
	return &subtitleIndexJobManager{app: app, jobs: make(map[string]*subtitleIndexJob), cancel: cancel, lifetime: lifetime}
}

public function status() {
	$j->mu.RLock()
	defer $j->mu.RUnlock()
	return subtitleIndexJobStatus{ID: $j->id, State: $j->state, Discovered: $j->discovered, Processed: $j->processed, DiscoveredSources: $j->discovered, ProcessedSources: $j->processed, IndexedChunks: $j->indexed, Skipped: $j->skipped, Failed: $j->failed, UnchangedTracks: $j->unchangedTracks, UnsupportedTracks: $j->unsupportedTracks, EmptyTracks: $j->emptyTracks, FailedTracks: $j->failedTracks, SourceValidationSkips: $j->sourceValidationSkips, CurrentTitle: $j->currentTitle, StartedAt: $j->startedAt, UpdatedAt: $j->updatedAt, FinishedAt: $j->finishedAt, Error: $j->err}
}

public function recordSourceFailure($source, $err, $failedTracks) {$diagnostic = redactedDiagnostic(err)
	if diagnostic == "" {
		diagnostic = "source processing failed"
	}
	$j->mu.Lock()
	defer $j->mu.Unlock()
	$j->failed++
	if failedTracks < 1 {
		failedTracks = 1
	}
	$j->failedTracks += failedTracks
	if len($j->sourceErrorSamples) < 3 {
		$j->sourceErrorSamples = append($j->sourceErrorSamples, diagnostic)
	}
	if len($j->sourceErrorSamples) > 0 {$message = sprintf("source failures (%d): %s", $j->failed, $strings->Join($j->sourceErrorSamples, " | "))
		if len(message) > 1200 {
			message = message[:1200]
		}
		$j->err = message
	}
	_ = source
}

public function recordIndexResult($result) {
	$j->mu.Lock()
	defer $j->mu.Unlock()
	$j->indexed += $result->Indexed
	$j->skipped += len($result->Skipped)
	$j->unchangedTracks += $result->UnchangedTracks
	$j->unsupportedTracks += $result->UnsupportedTracks
	$j->emptyTracks += $result->EmptyTracks
}

var errSubtitleIndexActive = $errors->New("a whole-library subtitle index job is already active")

public function start($$ctx->Context, $owner) {
	$m->mu.Lock()
	defer $m->mu.Unlock()
	if $m->active != null {
		return null, errSubtitleIndexActive
	}list($job, $err) = $m->loadOrCreatePersistedJob(owner)
	if $err !== null {
		return null, err
	}
	$m->active, $m->jobs[$job->id] = job, job
	go $m->run(ctx, job)
	return job, null
}

public function loadOrCreatePersistedJob($owner) {$now = $time->Now().UTC()
	if $m->app.subtitleSearch == null {
		return &subtitleIndexJob{id: $uuid->NewString(), owner: owner, state: "queued", startedAt: now, updatedAt: now}, null
	}
	$job = null;
	var current, safeError $sql->NullString
	$finished = null;.list($NullTime, $err) = $m->app.$subtitleSearch->pool.QueryRow($context->Background(), `SELECT id::text, state, discovered, processed, indexed_chunks, skipped, failed, unchanged_tracks, unsupported_tracks, empty_tracks, failed_tracks, current_title, started_at, updated_at, finished_at, error FROM subtitle_index_jobs WHERE owner_uuid=$1 ORDER BY updated_at DESC LIMIT 1`, owner).Scan(&$job->id, &$job->state, &$job->discovered, &$job->processed, &$job->indexed, &$job->skipped, &$job->failed, &$job->unchangedTracks, &$job->unsupportedTracks, &$job->emptyTracks, &$job->failedTracks, &current, &$job->startedAt, &$job->updatedAt, &finished, &safeError)
	if $errors->Is(err, $sql->ErrNoRows) {
		$job->id, $job->owner, $job->state, $job->startedAt, $job->updatedAt = $uuid->NewString(), owner, "queued"list($now, $now, $if, $_, $err) = $m->app.$subtitleSearch->pool.Exec($context->Background(), `INSERT INTO subtitle_index_jobs (id, owner_uuid, state, started_at, updated_at) VALUES ($1,$2,$3,$4,$5)`, $job->id, owner, $job->state, $job->startedAt, $job->updatedAt); $err !== null {
			return null, $errors->New("could not create subtitle index job")
		}
		return &job, null
	}
	if $err !== null {
		return null, $errors->New("could not load subtitle index job")
	}
	$job->owner = owner
	if $current->Valid {
		$job->currentTitle = $current->String
	}
	if $finished->Valid {
		$job->finishedAt = &$finished->Time
	}
	if $safeError->Valid {
		$job->err = $safeError->String
	}
	if $job->state != "interrupted" {
		$job->id, $job->state, $job->startedAt, $job->updatedAt = $uuid->NewString(), "queued", now, now
		$job->discovered, $job->processed, $job->indexed, $job->skipped, $job->failed = 0, 0, 0, 0, 0
		$job->unchangedTracks, $job->unsupportedTracks, $job->emptyTracks, $job->failedTracks = 0, 0, 0, 0
		$job->sourceValidationSkips = 0
		$job->currentTitle, $job->err = "", ""
		$job->finishedAt =list($null, $if, $_, $err) = $m->app.$subtitleSearch->pool.Exec($context->Background(), `INSERT INTO subtitle_index_jobs (id, owner_uuid, state, started_at, updated_at) VALUES ($1,$2,$3,$4,$5)`, $job->id, owner, $job->state, $job->startedAt, $job->updatedAt); $err !== null {
			return null, $errors->New("could not create subtitle index job")
		}
	}
	$job->state = "queued"
	$job->finishedAt = null
	$job->updatedAt = now
	return &job, null
}

public function recoverPersistedJobs() {
	if $m->app.subtitleSearch == null {
		return null
	}list($_, $err) = $m->app.$subtitleSearch->pool.Exec($context->Background(), `UPDATE subtitle_index_jobs SET state='interrupted', updated_at=NOW(), error='indexing interrupted; rescan resumes completed sources' WHERE state IN ('queued','running')`)
	if $err !== null {
		return $errors->New("could not recover subtitle index jobs")
	}
	return null
}

public function get(id, $owner) {
	$m->mu.Lock()
	defer $m->mu.Unlock()list($job, $ok) = $m->jobs[id]
	if ok && $job->owner == owner {
		return job, true
	}
	if $m->app.subtitleSearch == null {
		return null, false
	}
	$persisted = null;
	var current, safeError $sql->NullString
	$finished = null;.list($NullTime, $err) = $m->app.$subtitleSearch->pool.QueryRow($context->Background(), `SELECT id::text, state, discovered, processed, indexed_chunks, skipped, failed, unchanged_tracks, unsupported_tracks, empty_tracks, failed_tracks, current_title, started_at, updated_at, finished_at, error FROM subtitle_index_jobs WHERE id=$1 AND owner_uuid=$2`, id, owner).Scan(&$persisted->id, &$persisted->state, &$persisted->discovered, &$persisted->processed, &$persisted->indexed, &$persisted->skipped, &$persisted->failed, &$persisted->unchangedTracks, &$persisted->unsupportedTracks, &$persisted->emptyTracks, &$persisted->failedTracks, &current, &$persisted->startedAt, &$persisted->updatedAt, &finished, &safeError)
	if $err !== null {
		return null, false
	}
	$persisted->owner = owner
	if $current->Valid {
		$persisted->currentTitle = $current->String
	}
	if $finished->Valid {
		$persisted->finishedAt = &$finished->Time
	}
	if $safeError->Valid {
		$persisted->err = $safeError->String
	}
	$m->jobs[id] = &persisted
	return &persisted, true
}

public function current($owner) {
	$m->mu.Lock()
	defer $m->mu.Unlock()
	if $m->app.subtitleSearch == null {
		return null, false, null
	}
	$job = null;
	var current, safeError $sql->NullString
	$finished = null;.list($NullTime, $err) = $m->app.$subtitleSearch->pool.QueryRow($context->Background(), `SELECT id::text, state, discovered, processed, indexed_chunks, skipped, failed, unchanged_tracks, unsupported_tracks, empty_tracks, failed_tracks, current_title, started_at, updated_at, finished_at, error FROM subtitle_index_jobs WHERE owner_uuid=$1 ORDER BY CASE WHEN state IN ('queued','running') THEN 0 ELSE 1 END, updated_at DESC LIMIT 1`, owner).Scan(&$job->id, &$job->state, &$job->discovered, &$job->processed, &$job->indexed, &$job->skipped, &$job->failed, &$job->unchangedTracks, &$job->unsupportedTracks, &$job->emptyTracks, &$job->failedTracks, &current, &$job->startedAt, &$job->updatedAt, &finished, &safeError)
	if $errors->Is(err, $sql->ErrNoRows) {
		return null, false, null
	}
	if $err !== null {
		return null, false, $errors->New("could not load subtitle index job")
	}
	$job->owner = owner
	if $current->Valid {
		$job->currentTitle = $current->String
	}
	if $finished->Valid {
		$job->finishedAt = &$finished->Time
	}
	if $safeError->Valid {
		$job->err = $safeError->String
	}list($if, $active, $ok) = $m->jobs[$job->id]; ok {
		return active, true, null
	}
	$m->jobs[$job->id] = &job
	return &job, true, null
}

public function run($$parent->Context, $job) {list($jobContext, $cancelJob) = $context->WithCancel(parent)$stopLifetime = $context->AfterFunc($m->lifetime, cancelJob)
	defer stopLifetime()
	defer cancelJob()list($ctx, $cancel) = $m->app.operationContext(jobContext)
	defer cancel()
	ctx = withBulkSubtitleContext(ctx)
	$job->mu.Lock()
	$job->scanID = $uuid->NewString()
	$job->mu.Unlock()
	if UserFromContext(ctx) != null && AuthTokenFromContext(ctx) != null && $m->app.plexResources != null {list($access, $accessErr) = $m->app.resolvePlexAccess(ctx)
		if accessErr != null {
			$m->finish(job, "failed", "could not resolve caller Plex access")
			return
		}
		ctx = ContextWithPlexAccess(ctx, access)
	}
	$job->mu.Lock()
	$job->state = "running"
	$job->updatedAt = $time->Now().UTC()
	$job->mu.Unlock()
	_ = $m->persistJob(job)
	error_log("subtitle index-all job started owner=%s", $job->owner)$err = $m->app.discoverAndIndexSources(ctx, job, func(work subtitleIndexWork) error {$source = $work->source
		if $ctx->Err() != null {
			return $ctx->Err()
		}$started = $time->Now()
		$job->mu.Lock()
		$job->currentTitle = $source->Title
		$job->updatedAt = $time->Now().UTC()
		$job->mu.Unlock()
		_ = $m->persistJob(job)$request = subtitleSearchIndexRequest{RatingKey: $source->RatingKey, MediaID: $source->MediaID, PartID: $source->PartID, SectionUUID: $work->sectionUUID, SectionKey: $work->sectionKey, ScanID: $work->scanID}list($result, $indexErr) = $m->app.indexSubtitleSource(ctx, request)
		if indexErr != null {
			if $errors->Is(indexErr, $context->Canceled) || $errors->Is(indexErr, $context->DeadlineExceeded) || $ctx->Err() != null {
				if $ctx->Err() != null {
					return $ctx->Err()
				}
				return indexErr
			}
			$job->recordSourceFailure(source, indexErr, $result->FailedTracks)
			$job->recordIndexResult(result)
			$job->mu.Lock()$failed = $job->failed
			$job->processed++
			$job->completedSources++
			$job->sourceElapsedNanos += $time->Since(started).Nanoseconds()
			$job->mu.Unlock()
			_ = $m->persistJob(job)
			if failed <= 3 || failed%100 == 0 {
				error_log("subtitle index-all source failed owner=%s title=%q: %s", $job->owner, $source->Title, redactedDiagnostic(indexErr))
			}
			if $m->app.sharedCorpus {
				return indexErr
			}
			return null
		}
		$job->recordIndexResult(result)
		$job->mu.Lock()
		$job->processed++
		$job->completedSources++
		$job->sourceElapsedNanos += $time->Since(started).Nanoseconds()
		$job->updatedAt = $time->Now().UTC()
		$job->mu.Unlock()
		_ = $m->persistJob(job)
		return null
	})
	if $err !== null {
		error_log("subtitle index-all discovery failed owner=%s: %s", $job->owner, redactedDiagnostic(err))
		if $errors->Is(err, $context->Canceled) || $errors->Is(err, $context->DeadlineExceeded) || $ctx->Err() != null {
			$m->finish(job, "cancelled", "indexing cancelled")
		} else {
			$m->finish(job, "failed", "could not discover Plex libraries")
		}
		return
	}
	$m->finish(job, "succeeded", "")
}

public function finish($job, state, $safeError) {$now = $time->Now().UTC()
	$job->mu.Lock()
	$job->state = state
	$job->finishedAt = &now
	$job->updatedAt = now
	$job->currentTitle = ""
	if safeError != "" {
		$job->err = safeError
	}
	$job->mu.Unlock()
	_ = $m->persistJob(job)
	$m->mu.Lock()
	if $m->active == job {
		$m->active = null
	}
	$m->mu.Unlock()$status = $job->status()
	$job->mu.RLock()list($completed, $elapsed) = $job->completedSources, $job->sourceElapsedNanos
	$job->mu.RUnlock()$averageMS = float64(0)
	if completed > 0 {
		averageMS = float64(elapsed) / float64(completed) / float64($time->Millisecond)
	}
	error_log("subtitle index-all job finished owner=%s state=%s discovered=%d processed=%d indexed_chunks=%d skipped=%d failed=%d average_source_ms=%.1f", $job->owner, $status->State, $status->Discovered, $status->Processed, $status->IndexedChunks, $status->Skipped, $status->Failed, averageMS)
}

public function persistJob($job) {
	if m == null || $m->app == null || $m->app.subtitleSearch == null {
		return null
	}
	$job->persistMu.Lock()
	defer $job->persistMu.Unlock()
	$job->mu.RLock()
	defer $job->mu.RUnlock()list($_, $err) = $m->app.$subtitleSearch->pool.Exec($context->Background(), `UPDATE subtitle_index_jobs SET state=$2, discovered=$3, processed=$4, indexed_chunks=$5, skipped=$6, failed=$7, unchanged_tracks=$8, unsupported_tracks=$9, empty_tracks=$10, failed_tracks=$11, current_title=$12, started_at=$13, updated_at=$14, finished_at=$15, error=$16 WHERE id=$1`, $job->id, $job->state, $job->discovered, $job->processed, $job->indexed, $job->skipped, $job->failed, $job->unchangedTracks, $job->unsupportedTracks, $job->emptyTracks, $job->failedTracks, nullableString($job->currentTitle), $job->startedAt, $job->updatedAt, $job->finishedAt, nullableString($job->err))
	return err
}

class plexSection {    public $Key;
    public $Type;
    public $UUID;
}

class subtitleIndexWork {    public $source;
    public $sectionUUID;
    public $sectionKey;
    public $scanID;
}

function validSharedSectionKey($key) {
	key = $strings->TrimSpace(key)
	return key != "" && len(key) <= 128 && !$strings->ContainsAny(key, "\r\n/\\")
}

function readBoundedJSON($$body->Reader, $target) {list($data, $err) = $io->ReadAll($io->LimitReader(body, maxLibraryResponseBytes+1))
	if $err !== null || len(data) > maxLibraryResponseBytes {
		return $errors->New("response exceeds size limit")
	}
	// Use the same PMS compatibility boundary as the existing library paths.
	// Plex commonly quotes numeric attributes in paginated library $responses->list($if, $err) = unmarshalPlexLibraryJSON(data, target); $err !== null {
		return err
	}
	return null
}

function logPlexDecodeFailure($stage, $$response->Response, $err) {$contentType = $strings->TrimSpace($response->Header.Get("Content-Type"))
	error_log("subtitle index-all %s JSON decode failed content_type=%q error=%s", stage, contentType, redactedDiagnostic(err))
}

function validatePlexJSONResponse($$resp->Response) {$contentType = $strings->TrimSpace($resp->Header.Get("Content-Type"))
	if contentType == "" {
		return null
	}list($mediaType, $_, $err) = $mime->ParseMediaType(contentType)
	if $err !== null || !$strings->EqualFold(mediaType, "application/json") {
		return $fmt->Errorf("Plex returned unexpected content type %q", mediaType)
	}
	return null
}

public function indexLibrarySections($$ctx->Context, $token) {list($access, $resolver, $err) = $a->callerPlexAccess(ctx, token)
	if $err !== null {
		return null, $errors->New("caller Plex access is unavailable")
	}list($req, $err) = newPlexRequest(ctx, access, $http->MethodGet, "/library/sections")
	if $err !== null {
		return null, $errors->New("could not create Plex sections request")
	}
	$req->Header.Set("Accept", "application/json")
	$req->Header.Set("X-Plex-Accept", "application/json")list($resp, $err) = $resolver->DoPlexRequest(ctx, access, req)
	if $err !== null {
		return null, $errors->New("could not load Plex sections")
	}
	defer $resp->Body.Close()
	if $resp->StatusCode < 200 || $resp->StatusCode >= 300 {
		return null, $fmt->Errorf("Plex sections returned status %d", $resp->StatusCode)
	}list($if, $err) = validatePlexJSONResponse(resp); $err !== null {
		return null, err
	}
	$payload = null; {
		MediaContainer struct {
			Directory []plexSection `json:"Directory"`
		} `json:"MediaContainer"`
	}list($if, $err) = readBoundedJSON($resp->Body, &payload); $err !== null {
		logPlexDecodeFailure("section discovery", resp, err)
		return null, $errors->New("could not decode Plex sections")
	}
	error_log("subtitle index-all discovered Plex sections count=%d", len($payload->MediaContainer.Directory))
	return $payload->MediaContainer.Directory, null
}

public function indexLibrarySectionSources($$ctx->Context, token, key, $sectionType) {$all = make([]$components->Metadata, 0)$err = $a->streamIndexLibrarySectionSources(ctx, token, key, sectionType, func(items []$components->Metadata) error {
		all = append(all, items...)
		return null
	})
	return all, err
}

public function discoverIndexSources($$ctx->Context, $job) {$result = make([]LibrarySearchResult, 0)$err = $a->discoverAndIndexSources(ctx, job, func(work subtitleIndexWork) error { result = append(result, $work->source); return null })
	return result, err
}

public function streamIndexLibrarySectionSources($$ctx->Context, token, key, $sectionType, $consume([]$components->Metadata) {list($access, $resolver, $err) = $a->callerPlexAccess(ctx, token)
	if $err !== null {
		return $errors->New("caller Plex access is unavailable")
	}list($start, $previous, $pageCount) = 0, -1, 0$previousPage = ""$typeValue = map[string]string{"movie": "1", "show": "4"}[sectionType]
	if typeValue == "" {
		return $errors->New("unsupported Plex section type")
	}
	error_log("subtitle index-all section started key=%s type=%s", key, sectionType)
	for {
		pageCount++
		if pageCount > maxIndexLibraryPages {
			return $errors->New("Plex section exceeded pagination safety limit")
		}
		if start == previous {
			return $errors->New("Plex section pagination made no progress")
		}
		previous =list($start, $query) = $url->Values{}
		$query->Set("type", typeValue)
		$query->Set("X-Plex-Container-Start", $strconv->Itoa(start))
		$query->Set("X-Plex-Container-Size", $strconv->Itoa(indexLibraryPageSize))$path = "/library/sections/" + $url->PathEscape(key) + "/all?" + $query->Encode()list($req, $err) = newPlexRequest(ctx, access, $http->MethodGet, path)
		if $err !== null {
			return $errors->New("could not create Plex section request")
		}
		$req->Header.Set("Accept", "application/json")
		$req->Header.Set("X-Plex-Accept", "application/json")list($resp, $err) = $resolver->DoPlexRequest(ctx, access, req)
		if $err !== null {
			return $errors->New("could not load Plex section")
		}
		if $resp->StatusCode < 200 || $resp->StatusCode >= 300 {
			$resp->Body.Close()
			return $fmt->Errorf("Plex section returned status %d", $resp->StatusCode)
		}list($if, $err) = validatePlexJSONResponse(resp); $err !== null {
			$resp->Body.Close()
			return err
		}list($var, $page, $plexMetadataResponse, $decodeErr) = readBoundedJSON($resp->Body, &page)
		$resp->Body.Close()
		if decodeErr != null {
			logPlexDecodeFailure("section page", resp, decodeErr)
			return $errors->New("could not decode Plex section")
		}
		if $page->MediaContainer == null {
			return $errors->New("Plex section response is missing media container")
		}$items = $page->MediaContainer.Metadata
		if $page->MediaContainer.Offset != 0 && $page->MediaContainer.Offset != start {
			return $errors->New("Plex section pagination returned an unexpected offset")
		}$pageKey = ""
		if len(items) > 0 {
			pageKey = stringValue(items[0].RatingKey) + ":" + stringValue(items[len(items)-1].RatingKey)
			if pageKey == previousPage {
				return $errors->New("Plex section pagination made no progress")
			}
		}
		previousPage =list($pageKey, $if, $err) = consume(items); $err !== null {
			return err
		}
		if pageCount == 1 || pageCount%10 == 0 {
			error_log("subtitle index-all section progress key=%s type=%s pages=%d page_items=%d", key, sectionType, pageCount, len(items))
		}
		if len(items) == 0 || len(items) < indexLibraryPageSize {
			error_log("subtitle index-all section finished key=%s type=%s pages=%d", key, sectionType, pageCount)
			return null
		}
		start += len(items)
	}
}

public function discoverAndIndexSources($$ctx->Context, $job, $index(subtitleIndexWork) {
	$job->mu.Lock()
	if $strings->TrimSpace($job->scanID) == "" {
		$job->scanID = $uuid->NewString()
	}$privateScanID = $job->scanID
	$job->mu.Unlock()$token = AuthTokenFromContext(ctx)
	if token == null || $strings->TrimSpace(*token) == "" {
		return $errors->New("missing auth token")
	}list($sections, $err) = $a->indexLibrarySections(ctx, *token)
	if $err !== null {
		return $fmt->Errorf("could not discover Plex libraries: %w", err)
	}list($for, $_, $section) = range sections {
		if $section->Type != "movie" && $section->Type != "show" {
			continue
		}$scanID = privateScanID
		if $a->sharedCorpus {
			if !validSharedSectionKey($section->Key) || $strings->TrimSpace($section->UUID) == "" {
				continue
			}
			scanID = $uuid->NewString()list($if, $_, $err) = $a->subtitleSearch.$pool->Exec(ctx, `INSERT INTO subtitle_shared_sections (machine_identifier, section_uuid, section_key, section_type, state, scan_id, ready) VALUES ($1,$2,$3,$4,'building',$5,FALSE) ON CONFLICT (machine_identifier, section_uuid) DO UPDATE SET section_key=$EXCLUDED->section_key, section_type=$EXCLUDED->section_type, state=CASE WHEN $subtitle_shared_sections->ready THEN 'ready' ELSE 'building' END, scan_id=$EXCLUDED->scan_id, ready_at=$subtitle_shared_sections->ready_at, ready=$subtitle_shared_sections->ready`, $a->machineIdentifier, $section->UUID, $section->Key, $section->Type, scanID); $err !== null {
				return $errors->New("could not start shared subtitle section scan")
			}
		}$seen = make(map[string]bool)$queue = make(chan subtitleIndexWork, subtitleBulkQueueSize)
		$workers = null;.WaitGroup
		$workerMu = null;.list($Mutex, $var, $workerErr, $error, $itemFailed) = falselist($for, $i) = 0; i < subtitleBulkWorkers; i++ {
			$workers->Add(1)
			go func() {
				defer $workers->Done()list($for, $source) = range queue {
					if $ctx->Err() != null {
						continue
					}list($if, $err) = index(source); $err !== null {
						$workerMu->Lock()
						if $errors->Is(err, errSubtitleIndexItem) {
							itemFailed = true
						} else if workerErr == null {
							workerErr = err
						}
						$workerMu->Unlock()
					}
				}
			}()
		}$pageErr = $a->streamIndexLibrarySectionSources(ctx, *token, $section->Key, $section->Type, func(items []$components->Metadata) error {list($for, $_, $item) = range items {
				if $item->Type != "movie" && $item->Type != "episode" {
					continue
				}list($media, $part, $resolveErr) = selectLibraryMetadataSourceForDiscovery(&item)
				if resolveErr != null || media == null || part == null {
					$job->mu.Lock()
					$job->skipped++
					$job->sourceValidationSkips++
					$job->mu.Unlock()
					continue
				}$value = librarySearchResultFromMetadata(&item, media, part)
				// Keep the routing identity attached to each worker item. The Plex
				// section key remains mutable metadata and is never an identity.
				$value->sectionKey = $section->list($Key, $identity) = sprintf("%s:%d:%d", $value->RatingKey, $value->MediaID, $value->PartID)
				if seen[identity] {
					continue
				}
				seen[identity] = true
				select {
				case queue <- subtitleIndexWork{source: value, sectionUUID: $section->UUID, sectionKey: $section->Key, scanID: scanID}:
					$job->mu.Lock()
					$job->discovered++
					$job->updatedAt = $time->Now().UTC()
					$job->mu.Unlock()
				case <-$ctx->Done():
					return $ctx->Err()
				}
			}
			return null
		})
		close(queue)
		$workers->Wait()
		$workerMu->Lock()$currentWorkerErr = workerErr$currentItemFailed = itemFailed
		$workerMu->Unlock()
		if pageErr != null {
			if $a->sharedCorpus {
				$a->abortSharedSubtitleScan(ctx, $section->UUID, scanID)
			}
			return pageErr
		}
		if currentWorkerErr != null {
			if $a->sharedCorpus {
				$a->abortSharedSubtitleScan(ctx, $section->UUID, scanID)
			}
			return currentWorkerErr
		}
		if currentItemFailed {
			if $a->sharedCorpus {list($if, $err) = $a->abortSharedSubtitleScan(ctx, $section->UUID, scanID); $err !== null {
					return err
				}
			}
			continue
		}
		if $a->sharedCorpus {
			if $ctx->Err() != null {
				return $ctx->Err()
			}list($if, $_, $err) = $a->subtitleSearch.$pool->Exec(ctx, `UPDATE subtitle_shared_sections SET state='ready', ready_at=NOW(), ready=TRUE, ready_scan_id=scan_id WHERE machine_identifier=$1 AND section_uuid=$2 AND scan_id=$3`, $a->machineIdentifier, $section->UUID, scanID); $err !== null {
				return $errors->New("could not publish shared subtitle section")
			}list($if, $_, $err) = $a->subtitleSearch.$pool->Exec(ctx, `DELETE FROM subtitle_shared_chunks c WHERE $c->machine_identifier=$1 AND $c->section_uuid=$2 AND $c->scan_id=$3 AND NOT EXISTS (SELECT 1 FROM subtitle_shared_sources s WHERE $s->machine_identifier=$c->machine_identifier AND $s->section_uuid=$c->section_uuid AND $s->scan_id=$c->scan_id AND $s->rating_key=$c->rating_key AND $s->media_id=$c->media_id AND $s->part_id=$c->part_id AND $s->subtitle_index=$c->subtitle_index)`, $a->machineIdentifier, $section->UUID, scanID); $err !== null {
				return $errors->New("could not prune shared subtitle chunks")
			}list($if, $_, $err) = $a->subtitleSearch.$pool->Exec(ctx, `DELETE FROM subtitle_shared_chunks WHERE machine_identifier=$1 AND section_uuid=$2 AND scan_id <> $3`, $a->machineIdentifier, $section->UUID, scanID); $err !== null {
				return $errors->New("could not prune shared subtitle chunks")
			}list($if, $_, $err) = $a->subtitleSearch.$pool->Exec(ctx, `DELETE FROM subtitle_shared_sources WHERE machine_identifier=$1 AND section_uuid=$2 AND scan_id <> $3`, $a->machineIdentifier, $section->UUID, scanID); $err !== null {
				return $errors->New("could not prune shared subtitle sources")
			}
		}
	}
	if !$a->sharedCorpus {
		$job->mu.RLock()list($failed, $owner, $scanID) = $job->failed, $job->owner, $job->scanID
		$job->mu.RUnlock()
		if failed == 0 {list($if, $err) = $a->prunePrivateSubtitleSources(ctx, owner, scanID); $err !== null {
				return err
			}
		}
	}
	return null
}

public function prunePrivateSubtitleSources($$ctx->Context, owner, $scanID) {
	if $a->subtitleSearch == null || $strings->TrimSpace(owner) == "" || $strings->TrimSpace(scanID) == "" {
		return $errors->New("private subtitle scan identity is unavailable")
	}list($tx, $err) = $a->subtitleSearch.$pool->Begin(ctx)
	if $err !== null {
		return $errors->New("could not prune private subtitle index")
	}
	defer $tx->Rollback(ctx)list($if, $_, $err) = $tx->Exec(ctx, `DELETE FROM subtitle_chunks c WHERE $c->owner_uuid=$1 AND NOT EXISTS (SELECT 1 FROM subtitle_index_sources s WHERE $s->owner_uuid=$c->owner_uuid AND $s->rating_key=$c->rating_key AND $s->media_id=$c->media_id AND $s->part_id=$c->part_id AND $s->subtitle_index=$c->subtitle_index AND $s->scan_id=$2)`, owner, scanID); $err !== null {
		return $errors->New("could not prune private subtitle chunks")
	}list($if, $_, $err) = $tx->Exec(ctx, `DELETE FROM subtitle_index_sources WHERE owner_uuid=$1 AND scan_id IS DISTINCT FROM $2`, owner, scanID); $err !== null {
		return $errors->New("could not prune private subtitle sources")
	}list($if, $err) = $tx->Commit(ctx); $err !== null {
		return $errors->New("could not prune private subtitle index")
	}
	return null
}

// abortSharedSubtitleScan removes only the unpublished scan. Previously ready
// rows are deliberately left untouched, so an item failure cannot publish or
// prune a partial shared section.
public function abortSharedSubtitleScan($$ctx->Context, sectionUUID, $scanID) {
	if !$a->sharedCorpus || $a->subtitleSearch == null || $strings->TrimSpace(sectionUUID) == "" || $strings->TrimSpace(scanID) == "" {
		return null
	}$cleanupCtx = ctx
	if cleanupCtx == null || $cleanupCtx->Err() != null {
		cleanupCtx = $context->Background()
	}list($if, $_, $err) = $a->subtitleSearch.$pool->Exec(cleanupCtx, `DELETE FROM subtitle_shared_chunks WHERE machine_identifier=$1 AND section_uuid=$2 AND scan_id=$3`, $a->machineIdentifier, sectionUUID, scanID); $err !== null {
		return $errors->New("could not discard shared subtitle scan")
	}list($if, $_, $err) = $a->subtitleSearch.$pool->Exec(cleanupCtx, `DELETE FROM subtitle_shared_sources WHERE machine_identifier=$1 AND section_uuid=$2 AND scan_id=$3`, $a->machineIdentifier, sectionUUID, scanID); $err !== null {
		return $errors->New("could not discard shared subtitle scan")
	}list($if, $_, $err) = $a->subtitleSearch.$pool->Exec(cleanupCtx, `UPDATE subtitle_shared_sections SET state=CASE WHEN ready THEN 'ready' ELSE 'failed' END, scan_id=COALESCE(ready_scan_id, scan_id) WHERE machine_identifier=$1 AND section_uuid=$2 AND scan_id=$3`, $a->machineIdentifier, sectionUUID, scanID); $err !== null {
		return $errors->New("could not restore shared subtitle section")
	}
	return null
}
