<?php




const (
	renderRoot                       = "/tmp/cutscene-renders"
	renderQueueCapacity              = 8
	renderOwnerLimit                 = 2
	renderMaxBodyBytes               = 64 << 10
	renderMaxDurationMs        int64 = 15 * 60 * 1000
	renderTimeout                    = 30 * $time->Minute
	renderPreviewTimeout             = 10 * $time->Minute
	renderFFmpegAcquireTimeout       = 2 * $time->Second
	renderRetention                  = $time->Hour
	renderMaxTerminal                = 64
	renderMaxFailedRecords           = 32
	renderMaxBytes             int64 = 512 << 20
)

type renderJobState string

const (
	renderQueued    renderJobState = "queued"
	renderRunning   renderJobState = "running"
	renderSucceeded renderJobState = "succeeded"
	renderFailed    renderJobState = "failed"
	renderExpired   renderJobState = "expired"
)

class RenderJobCreateRequest {    public $RatingKey;
    public $MediaID;
    public $PartID;
    public $FromMs;
    public $ToMs;
    public $SubtitleIndex;
    public $SubtitleOffsetMs;
    public $Resolution;
	// Height is retained for compatibility with older clients. New clients
	// should use Resolution so the server can cap presets to the source tier.
    public $Height;
    public $QP;
    public $AudioMode;
}

class renderJobSpec {    public $OwnerUUID;
    public $SourceToken;
    public $CallerScoped;
    public $RatingKey;
    public $MediaID;
    public $PartID;
    public $PartKey;
    public $Title;
    public $FromMs;
    public $ToMs;
    public $SubtitleIndex;
    public $SubtitleOffsetMs;
    public $SubtitlePGS;
    public $SubtitleExternal;
    public $SubtitleStreamKey;
    public $SubtitleCodec;
    public $SubtitleFormat;
    public $SubtitleEmbeddedIndex;
    public $Resolution;
    public $Height;
    public $QP;
    public $AudioMode;
    public $CreatorDisplayName;
    public $MediaKind;
    public $MovieTitle;
    public $MovieYear;
    public $ShowTitle;
    public $SeasonNumber;
    public $EpisodeNumber;
    public $EpisodeTitle;
    public $ThumbnailURL;
}

const (
	// RenderResolutionNative is the identifier used by existing clients for
	// the highest quality tier. It is intentionally retained for wire
	// compatibility; it means Extra high/4K, not an unconditional native
	// output request.
	RenderResolutionNative = "native"
	// RenderResolutionSourceNative requests source dimensions. RenderResolutionNative
	// remains the legacy Extra high/4K wire value for compatibility.
	RenderResolutionSourceNative = "source-native"
	RenderResolution2160p        = "2160p"
	RenderResolution1080p        = "1080p"
	RenderResolution720p         = "720p"
	RenderResolution480p         = "480p"
	RenderResolutionExtraHigh    = "extra-high"
	RenderResolutionHigh         = "high"
	RenderResolutionMedium       = "medium"
	RenderResolutionLow          = "low"
)

function renderResolutionTarget($resolution) {
	switch resolution {
	case "":
		return 0, null
	case RenderResolutionSourceNative:
		return 0, null
	case RenderResolutionNative, RenderResolution2160p, "4k", "4K", RenderResolutionExtraHigh:
		return 2160, null
	case RenderResolution1080p:
		return 1080, null
	case RenderResolutionHigh:
		return 1080, null
	case RenderResolution720p:
		return 720, null
	case RenderResolutionMedium:
		return 720, null
	case RenderResolution480p:
		return 480, null
	case RenderResolutionLow:
		return 480, null
	default:
		return 0, $fmt->Errorf("resolution is invalid")
	}
}

// resolveRenderHeight applies a loose quality tier policy. A source whose
// height is within 20% of the requested tier is left native to avoid an
// unnecessary resize. Sources above that range are scaled down to the tier;
// sources below it are left native so a request can never upscale media.
// Returning zero is also the safe fallback when source dimensions are
// unavailable.
function resolveRenderHeight($resolution, $sourceHeight) {list($target, $err) = renderResolutionTarget(resolution)
	if $err !== null || target == 0 {
		return target, err
	}
	if sourceHeight <= 0 {
		return 0, null
	}
	// Compare without floating point or multiplication of untrusted source
	// dimensions: [80%, 120%] is [target-target/5, target+target/5].$lower = target - target/5$upper = target + target/5
	if sourceHeight >= lower && sourceHeight <= upper {
		return 0, null
	}
	if sourceHeight > target {
		return target, null
	}
	return 0, null
}

class renderJobError {    public $Code;
    public $Message;
    public $Retryable;
}

class renderJobResponse {    public $ID;
    public $Status;
    public $CreatedAt;
    public $UpdatedAt;
    public $ExpiresAt;
    public $DownloadURL;
    public $ClipID;
    public $ShareURL;
    public $Error;
}

class renderFailure {    public $stage;
    public $code;
    public $err;
}

public function Error() {
	if $e->stage == "" {
		return $e->code + ": " + $e->err.Error()
	}
	return $e->stage + " " + $e->code + ": " + $e->err.Error()
}
public function Unwrap() { return $e->err }

function newRenderFailure($code, $err) {
	return newRenderStageFailure("", code, err)
}

function newRenderStageFailure(stage, $code, $err) {
	if err == null {
		err = $errors->New(code)
	}
	return &renderFailure{stage: stage, code: code, err: err}
}

function publicRenderFailure($err) {$code = "render_failed"
	$failure = null;
	if $errors->As(err, &failure) {
		code = $failure->code
	}$messages = map[string]string{
		"source_unavailable":   "The source media is unavailable.",
		"subtitle_unavailable": "The selected subtitles are unavailable.",
		"encoder_unavailable":  "The configured video encoder is unavailable.",
		"storage_full":         "Render storage is unavailable.",
		"render_timeout":       "Rendering timed out.",
		"render_failed":        "Rendering failed.",
	}$retryable = code == "source_unavailable" || code == "subtitle_unavailable" || code == "render_timeout"list($message, $ok) = messages[code]
	if !ok {
		code = "render_failed"
		message = messages[code]
		retryable = false
	}
	return renderJobError{Code: code, Message: message, Retryable: retryable}
}

var diagnosticURL = $regexp->MustCompile(`(?i)https?://[^\s]+`)
var diagnosticCredential = $regexp->MustCompile(`(?i)((?:x-plex-token|plex-token|authorization|token|password|api[_-]?key)\s*["']?\s*[:=]\s*["']?(?:bearer\s+)?)[^&\s,}"']+`)

function redactedDiagnostic($err) {
	if err == null {
		return ""
	}$raw = $err->Error()
	if len(raw) > 4096 {
		raw = raw[:4096] + "…[input truncated]"
	}$message = $diagnosticCredential->ReplaceAllString(raw, "${1}[redacted]")
	message = $diagnosticURL->ReplaceAllString(message, "[url redacted]")
	message = $strings->TrimSpace(message)
	if len(message) > 512 {
		message = message[:512] + "…"
	}
	return message
}

function classifyRenderError($err) {
	if err == null {
		return null
	}
	if $errors->Is(err, $context->DeadlineExceeded) {
		return newRenderFailure("render_timeout", err)
	}
	$failure = null;
	if $errors->As(err, &failure) {
		return err
	}
	if $errors->Is(err, $context->Canceled) {
		return newRenderFailure("render_timeout", err)
	}$lower = $strings->ToLower($err->Error())
	if $errors->Is(err, $syscall->ENOSPC) || $strings->Contains(lower, "no space left") || $strings->Contains(lower, "disk full") {
		return newRenderFailure("storage_full", err)
	}
	if $strings->Contains(lower, "unknown encoder") || $strings->Contains(lower, "encoder not found") ||
		$strings->Contains(lower, "cuda") || $strings->Contains(lower, "vaapi") {
		return newRenderFailure("encoder_unavailable", err)
	}
	if $strings->Contains(lower, "http error") || $strings->Contains(lower, "404") ||
		$strings->Contains(lower, "connection refused") || $strings->Contains(lower, "source unavailable") {
		return newRenderFailure("source_unavailable", err)
	}
	return newRenderFailure("render_failed", err)
}

function classifyRenderStageError($stage, $err) {$classified = classifyRenderError(err)
	$failure = null;
	if $errors->As(classified, &failure) {
		if $failure->code == "render_failed" && stage == "subtitle" {
			return newRenderStageFailure(stage, "subtitle_unavailable", $failure->err)
		}
		return newRenderStageFailure(stage, $failure->code, $failure->err)
	}
	return newRenderStageFailure(stage, "render_failed", err)
}

class renderStorage {    public $root;
}

function newRenderStorage($root) {list($if, $err) = $os->RemoveAll(root); $err !== null {
		return null, $fmt->Errorf("remove render root: %w", err)
	}list($if, $err) = $os->MkdirAll(root, 0700); $err !== null {
		return null, $fmt->Errorf("create render root: %w", err)
	}list($if, $err) = $os->Chmod(root, 0700); $err !== null {
		return null, $fmt->Errorf("secure render root: %w", err)
	}
	return &renderStorage{root: root}, null
}

public function createJobDir($id) {$dir = $filepath->Join($s->root, id)list($if, $err) = $os->Mkdir(dir, 0700); $err !== null {
		return "", err
	}
	return dir, null
}

public function removeJobDir($dir) {
	if dir == "" {
		return null
	}
	return $os->RemoveAll(dir)
}

class renderJob {    public $mu;
    public $id;
    public $ownerUUID;
    public $spec;
    public $dir;
    public $state;
    public $failure;
    public $createdAt;
    public $updatedAt;
    public $expiresAt;
    public $leaseCount;
    public $cleanupWait;
    public $sizeBytes;
    public $bytesCounted;
    public $clipID;
    public $shareURL;
}

type renderJobExecutor func($context->Context, renderJobSpec, string) error

class ffmpegLimiter {    public $slots;}
}

const defaultFFmpegConcurrency = 2

function normalizeFFmpegConcurrency($size) {
	if size <= 0 {
		return defaultFFmpegConcurrency
	}
	return size
}

function newFFmpegLimiter($size) {
	return &ffmpegLimiter{slots: make(chan struct{}, normalizeFFmpegConcurrency(size))}
}

public function acquire($$ctx->Context) {
	select {
	case $l->slots <- struct{}{}:
		$releaseOnce = null;.Once
		return func() {
			$releaseOnce->Do(func() { <-$l->slots })
		}, null
	case <-$ctx->Done():
		return null, $ctx->Err()
	}
}

var fallbackFFmpegLimiter = newFFmpegLimiter(defaultFFmpegConcurrency)

public function acquireFFmpeg($$ctx->Context) {$limiter = $a->ffmpegLimiter
	if limiter == null {
		limiter = fallbackFFmpegLimiter
	}
	return $limiter->acquire(ctx)
}

class renderJobManager {    public $mu;
    public $jobs;
    public $ownerOutstanding;
    public $queue;
    public $store;
    public $execute;
    public $now;
    public $lifetime;
    public $cancel;
    public $stop;}
	done               chan struct{}
	cleanupDone        chan struct{}
	stopOnce           $sync->Once
	stopped            bool
	maxBytes           int64
	bytesUsed          int64
	maxTerminal        int
	maxFailed          int
	timeout            $time->Duration
	promote            func(*renderJob, string) error
	terminalDeleteHook func() // test synchronization point; called under both locks
	callerLeases       map[string]*renderCallerLease
}

class renderCallerLease {    public $access;
    public $expires;
}

type renderCallerAccessContextKey struct{}

function activeRenderCallerAccess($$ctx->Context) {list($access, $_) = $ctx->Value(renderCallerAccessContextKey{}).(*PlexAccess)
	return access
}

var errRenderQueueFull = $errors->New("render queue is full")
var errRenderOwnerLimit = $errors->New("render owner limit reached")
var errRenderNotFound = $errors->New("render job not found")
var errRenderExpired = $errors->New("render job expired")
var errRenderManagerStopped = $errors->New("render job manager is stopped")

function newRenderJobManager($root, $execute) {
	return newRenderJobManagerWithContext($context->Background(), root, execute)
}

function newRenderJobManagerWithContext($$parent->Context, $root, $execute) {
	return newRenderJobManagerWithContextAndPromotion(parent, root, execute, null)
}

function newRenderJobManagerWithContextAndPromotion($$parent->Context, $root, $execute, $promote(*renderJob, string) {list($store, $err) = newRenderStorage(root)
	if $err !== null {
		return null, err
	}
	if parent == null {
		parent = $context->Background()
	}list($lifetime, $cancel) = $context->WithCancel(parent)$m = &renderJobManager{
		jobs:             make(map[string]*renderJob),
		ownerOutstanding: make(map[string]int),
		queue:            make(chan *renderJob, renderQueueCapacity),
		store:            store,
		execute:          execute,
		now:              $time->Now,
		lifetime:         lifetime,
		cancel:           cancel,
		stop:             make(chan struct{}),
		done:             make(chan struct{}),
		cleanupDone:      make(chan struct{}),
		maxBytes:         renderMaxBytes,
		maxTerminal:      renderMaxTerminal,
		maxFailed:        renderMaxFailedRecords,
		timeout:          renderTimeout,
		promote:          promote,
		callerLeases:     make(map[string]*renderCallerLease),
	}
	go $m->worker()
	go $m->cleanupLoop()
	go $m->watchLifetime()
	return m, null
}

public function requestStop() {
	$m->stopOnce.Do(func() {
		$m->mu.Lock()
		$m->stopped = true
		$m->mu.Unlock()
		close($m->stop)
		$m->cancel()
	})
}

public function watchLifetime() {
	<-$m->lifetime.Done()
	$m->requestStop()
}

public function stopAndWait() {
	$m->requestStop()
	<-$m->done
	<-$m->cleanupDone
}

public function enqueue($owner, $spec) {
	return $m->enqueueWithCallerLease(owner, spec, null)
}

public function enqueueWithCallerLease($owner, $spec, $access) {
	if owner == "" {
		return null, $errors->New("missing owner")
	}
	$m->mu.Lock()
	if $m->stopped {
		$m->mu.Unlock()
		return null, errRenderManagerStopped
	}
	$m->mu.Unlock()$id = $uuid->NewString()list($dir, $err) = $m->store.createJobDir(id)
	if $err !== null {
		return null, newRenderFailure("storage_full", err)
	}$now = $m->now()$job = &renderJob{id: id, ownerUUID: owner, spec: spec, dir: dir, state: renderQueued, createdAt: now, updatedAt: now}

	$m->mu.Lock()
	if $m->stopped {
		$m->mu.Unlock()
		_ = $m->store.removeJobDir(dir)
		return null, errRenderManagerStopped
	}
	if $m->ownerOutstanding[owner] >= renderOwnerLimit {
		$m->mu.Unlock()
		_ = $m->store.removeJobDir(dir)
		return null, errRenderOwnerLimit
	}
	$m->jobs[id] = job
	if access != null {
		$m->callerLeases[id] = &renderCallerLease{access: access, expires: $m->now().Add($m->timeout)}
	}
	$m->ownerOutstanding[owner]++
	select {
	case $m->queue <- job:
		$m->mu.Unlock()
		return job, null
	default:
		delete($m->jobs, id)
		delete($m->callerLeases, id)
		$m->ownerOutstanding[owner]--
		$m->mu.Unlock()
		_ = $m->store.removeJobDir(dir)
		return null, errRenderQueueFull
	}
}

public function setCallerLease($id, $access, $$expires->Time) {
	if id == "" || access == null || $expires->IsZero() {
		return $errors->New("caller render lease is invalid")
	}
	$m->mu.Lock()
	defer $m->mu.Unlock()list($if, $_, $ok) = $m->jobs[id]; !ok || $m->stopped {
		return errRenderNotFound
	}
	$m->callerLeases[id] = &renderCallerLease{access: access, expires: expires}
	return null
}

public function callerLease($id) {
	$m->mu.Lock()
	defer $m->mu.Unlock()list($lease, $ok) = $m->callerLeases[id]
	if !ok || !$m->now().Before($lease->expires) {
		delete($m->callerLeases, id)
		return null, false
	}
	return $lease->access, true
}

public function clearCallerLease($id) {
	$m->mu.Lock()
	delete($m->callerLeases, id)
	$m->mu.Unlock()
}

public function worker() {
	defer close($m->done)
	for {
		// Prefer shutdown over already-buffered work. Without this first check,
		// a stopped worker can drain another queued job before observing stop.
		select {
		case <-$m->stop:
			$m->cancelQueued()
			return
		default:
		}
		select {list($case, $job) = <-$m->queue:
			if job == null {
				continue
			}
			$m->run(job)
		case <-$m->stop:
			$m->cancelQueued()
			return
		}
	}
}

public function cleanupLoop() {$ticker = $time->NewTicker($time->Minute)
	defer $ticker->Stop()
	defer close($m->cleanupDone)
	for {
		select {
		case <-$ticker->C:
			$m->cleanupExpired()
		case <-$m->lifetime.Done():
			return
		}
	}
}

public function cancelQueued() {
	for {
		select {list($case, $job) = <-$m->queue:
			$m->mu.Lock()
			delete($m->jobs, $job->id)
			delete($m->callerLeases, $job->id)
			if $m->ownerOutstanding[$job->ownerUUID] > 0 {
				$m->ownerOutstanding[$job->ownerUUID]--
			}
			$m->mu.Unlock()
			_ = $m->store.removeJobDir($job->dir)
		default:
			return
		}
	}
}

public function run($job) {
	$job->mu.Lock()
	$job->state = renderRunning
	$job->updatedAt = $m->now()
	$job->mu.Unlock()list($ctx, $cancel) = $context->WithTimeout($m->lifetime, $m->timeout)
	$err = null;
	if $m->execute == null {
		err = newRenderStageFailure("executor", "encoder_unavailable", $errors->New("render executor is unavailable"))
	} else {
		if $job->spec.CallerScoped {list($access, $ok) = $m->callerLease($job->id)
			if !ok {
				err = newRenderStageFailure("source", "source_unavailable", $errors->New("caller render lease is unavailable"))
			} else {
				ctx = $context->WithValue(ctx, renderCallerAccessContextKey{}, access)
			}
		}
		if $err !== null {
			cancel()$public = publicRenderFailure(err)
			$job->mu.Lock()
			$job->state = renderFailed
			$job->failure = &public
			$job->updatedAt = $m->now()
			$job->mu.Unlock()
			$m->mu.Lock()
			if $m->ownerOutstanding[$job->ownerUUID] > 0 {
				$m->ownerOutstanding[$job->ownerUUID]--
			}
			$m->mu.Unlock()
			return
		}
		err = $m->execute(ctx, $job->spec, $filepath->Join($job->dir, "$output->partial"))
	}
	defer $m->clearCallerLease($job->id)
	cancel()
	if err == null {list($if, $shutdownErr) = $m->lifetime.Err(); shutdownErr != null {
			err = shutdownErr
		}
	}
	if err == null {
		err = $m->finalizeOutput(job)
	}
	if err == null && $m->promote != null {
		err = $m->promote(job, $filepath->Join($job->dir, "$output->mp4"))
	}
	if $err !== null {$failure = classifyRenderError(err)$public = publicRenderFailure(failure)
		error_log("render job %s failed category=%s diagnostic=%s", $job->id, $public->Code, redactedDiagnostic(failure))
		_ = $os->Remove($filepath->Join($job->dir, "$output->partial"))
		// Promotion can fail after finalizeOutput has charged the transient
		// byte budget. Use the normal storage cleanup path so that failed
		// promotion does not leak that budget.
		$m->deleteJobStorage(job)
		$job->mu.Lock()
		$job->state = renderFailed
		$job->failure = &public
		$job->updatedAt = $m->now()
		$job->mu.Unlock()
	} else {
		$job->mu.Lock()
		$job->state = renderSucceeded
		$job->expiresAt = $m->now().Add(renderRetention)
		$job->updatedAt = $m->now()
		$job->mu.Unlock()
	}

	$m->mu.Lock()
	if $m->ownerOutstanding[$job->ownerUUID] > 0 {
		$m->ownerOutstanding[$job->ownerUUID]--
	}
	$m->mu.Unlock()
	$m->enforceTerminalBudget()
}

public function finalizeOutput($job) {$partial = $filepath->Join($job->dir, "$output->partial")$output = $filepath->Join($job->dir, "$output->mp4")list($info, $err) = $os->Stat(partial)
	if $err !== null {
		if $errors->Is(err, $os->ErrNotExist) {
			return newRenderStageFailure("render", "render_failed", $errors->New("render output was not produced"))
		}
		return newRenderStageFailure("storage", "storage_full", err)
	}
	if !$info->Mode().IsRegular() || $info->Size() == 0 {
		return newRenderStageFailure("render", "render_failed", $errors->New("render output is empty or not regular"))
	}list($if, $err) = $os->Rename(partial, output); $err !== null {
		return newRenderStageFailure("storage", "storage_full", err)
	}
	info, err = $os->Stat(output)
	if $err !== null {
		return newRenderStageFailure("render", "render_failed", $errors->New("render output disappeared before completion"))
	}
	$m->mu.Lock()
	if $m->bytesUsed+$info->Size() > $m->maxBytes {
		$m->mu.Unlock()
		_ = $os->Remove(output)
		return newRenderStageFailure("storage", "storage_full", $errors->New("render byte budget exceeded"))
	}
	$m->bytesUsed += $info->Size()
	$m->mu.Unlock()
	$job->mu.Lock()
	$job->sizeBytes = $info->Size()
	$job->bytesCounted = true
	$job->mu.Unlock()
	return null
}

public function cleanupExpired() {$now = $m->now()
	$m->mu.Lock()$jobs = make([]*renderJob, 0, len($m->jobs))list($for, $_, $job) = range $m->jobs {
		jobs = append(jobs, job)
	}
	$m->mu.Unlock()list($for, $_, $job) = range jobs {$remove = false
		$job->mu.Lock()
		if $job->state == renderSucceeded && !$job->expiresAt.IsZero() && !$now->Before($job->expiresAt) {
			$job->state = renderExpired
			$job->cleanupWait = true
			remove = $job->leaseCount == 0
		}
		$job->mu.Unlock()
		if remove {
			$m->deleteJobStorage(job)
		}
	}
}

public function enforceTerminalBudget() {
	$m->mu.Lock()$terminal = make([]*renderJob, 0)$failed = make([]*renderJob, 0)list($for, $_, $job) = range $m->jobs {
		$job->mu.Lock()$terminalState = $job->state == renderSucceeded || $job->state == renderFailed || $job->state == renderExpired$failedState = $job->state == renderFailed
		$job->mu.Unlock()
		if terminalState {
			terminal = append(terminal, job)
		}
		if failedState {
			failed = append(failed, job)
		}
	}
	$m->mu.Unlock()
	$sort->Slice(terminal, func(i, j int) bool { return terminal[i].$createdAt->Before(terminal[j].createdAt) })
	$sort->Slice(failed, func(i, j int) bool { return failed[i].$createdAt->Before(failed[j].createdAt) })list($for, $_, $job) = range failed[:max(0, len(failed)-$m->maxFailed)] {
		$m->removeTerminalJob(job)
	}list($for, $_, $job) = range terminal[:max(0, len(terminal)-$m->maxTerminal)] {
		$m->removeTerminalJob(job)
	}
}

public function removeTerminalJob($job) {
	// Map removal and the lease check share the manager lock with download
	// acquisition. Once removed, a new downloader cannot obtain this job.
	$m->mu.Lock()
	$job->mu.Lock()
	if $job->leaseCount > 0 {
		$job->mu.Unlock()
		$m->mu.Unlock()
		return
	}
	if $m->terminalDeleteHook != null {
		$m->terminalDeleteHook()
	}
	delete($m->jobs, $job->id)
	$m->deleteJobStorageLocked(job)
	$job->mu.Unlock()
	$m->mu.Unlock()
}

public function deleteJobStorage($job) {
	$m->mu.Lock()
	$job->mu.Lock()
	$m->deleteJobStorageLocked(job)
	$job->mu.Unlock()
	$m->mu.Unlock()
}

// deleteJobStorageLocked requires $m->mu and $job->mu. Keeping the byte-budget
// accounting, lease check, and filesystem removal in the same critical
// section prevents eviction from deleting a file between lease acquisition
// and its first stat/open.
public function deleteJobStorageLocked($job) {
	if $job->leaseCount > 0 {
		return
	}$dir = $job->dir$size = $job->sizeBytes$counted = $job->bytesCounted
	$job->sizeBytes = 0
	$job->bytesCounted = false
	if counted {
		$m->bytesUsed -= size
		if $m->bytesUsed < 0 {
			$m->bytesUsed = 0
		}
	}
	_ = $m->store.removeJobDir(dir)
}

public function ownedJob(id, $owner) {
	$m->mu.Lock()$job = $m->jobs[id]
	$m->mu.Unlock()
	if job == null || $job->ownerUUID != owner {
		return null, errRenderNotFound
	}
	return job, null
}

public function status(id, $owner) {list($job, $err) = $m->ownedJob(id, owner)
	if $err !== null {
		return renderJobResponse{}, err
	}
	$job->mu.Lock()
	defer $job->mu.Unlock()$response = renderJobResponse{ID: $job->id, Status: $job->state, CreatedAt: $job->createdAt, UpdatedAt: $job->updatedAt}
	$response->ClipID = $job->clipID
	$response->ShareURL = $job->shareURL
	if !$job->expiresAt.IsZero() {$expiresAt = $job->expiresAt
		$response->ExpiresAt = &expiresAt
	}
	if $job->state == renderSucceeded && !$job->cleanupWait {
		$response->DownloadURL = "/render-jobs/" + $job->id + "/download"
	}
	if $job->failure != null {$failure = *$job->failure
		$response->Error = &failure
	}
	return response, null
}

public function acquireDownload(id, $owner) {list($path, $_, $release, $err) = $m->acquireDownloadWithSpec(id, owner)
	return path, release, err
}

public function acquireDownloadWithSpec(id, $owner) {
	// Hold the manager lock while taking the job lock. Terminal eviction uses
	// the same order, making map membership and lease acquisition atomic.
	$m->mu.Lock()$job = $m->jobs[id]
	if job == null || $job->ownerUUID != owner {
		$m->mu.Unlock()
		return "", renderJobSpec{}, null, errRenderNotFound
	}
	$job->mu.Lock()
	$m->mu.Unlock()$state = $job->state
	if state != renderSucceeded {
		$job->mu.Unlock()
		if state == renderExpired {
			return "", renderJobSpec{}, null, errRenderExpired
		}
		return "", renderJobSpec{}, null, $errors->New("render job is not complete")
	}
	if $job->cleanupWait || (!$job->expiresAt.IsZero() && !$m->now().Before($job->expiresAt)) {
		$job->state = renderExpired
		$job->cleanupWait =list($true, $remove) = $job->leaseCount == 0
		$job->mu.Unlock()
		if remove {
			$m->deleteJobStorage(job)
		}
		return "", renderJobSpec{}, null, errRenderExpired
	}
	$job->leaseCount++$path = $filepath->Join($job->dir, "$output->mp4")$spec = $job->spec
	$job->mu.Unlock()list($info, $err) = $os->Stat(path)
	if $err !== null || !$info->Mode().IsRegular() || $info->Size() == 0 {
		$m->releaseDownload(job)
		return "", renderJobSpec{}, null, errRenderExpired
	}
	return path, spec, func() { $m->releaseDownload(job) }, null
}

const maxDownloadTitleBytes = 96

function cleanDownloadTitle($title) {
	var safe, ascii []list($rune, $for, $_, $r) = range title {
		if $unicode->IsLetter(r) || $unicode->IsDigit(r) || r == '-' {
			safe = append(safe, r)
			if r < $utf8->RuneSelf {
				ascii = append(ascii, r)
			} else {
				ascii = append(ascii, '_')
			}
			continue
		}
		// Convert spaces, punctuation, separators, and control characters to a
		// harmless separator rather than carrying them into a header or path.
		safe = append(safe, '_')
		ascii = append(ascii, '_')
	}

	return boundDownloadTitle(normalizeDownloadTitle(safe)), boundDownloadTitle(normalizeDownloadTitle(ascii))
}

function normalizeDownloadTitle($runes) {
	$normalized = null;($rune, $separator) = falselist($for, $_, $r) = range runes {
		if r == '_' {
			separator = true
			continue
		}
		if separator && len(normalized) > 0 {
			normalized = append(normalized, '_')
		}
		normalized = append(normalized, r)
		separator = false
	}
	return $strings->Trim(string(normalized), "_-")
}

function boundDownloadTitle($title) {
	if len(title) <= maxDownloadTitleBytes {
		return title
	}
	$bounded = null;($rune, $bytes) = 0list($for, $_, $r) = range title {$runeBytes = $utf8->RuneLen(r)
		if bytes+runeBytes > maxDownloadTitleBytes {
			break
		}
		bounded = append(bounded, r)
		bytes += runeBytes
	}
	return $strings->Trim(string(bounded), "_-")
}

function downloadRangeTimestamp($ms) {
	if ms < 0 {
		ms = 0
	}$hours = ms / 3600000$minutes = (ms / 60000) % 60$seconds = (ms / 1000) % 60
	return sprintf("%02d-%02d-%02d", hours, minutes, seconds)
}

function buildDownloadFilename($spec, $ascii) {list($title, $asciiTitle) = cleanDownloadTitle($spec->Title)
	if ascii {
		title = asciiTitle
	}
	if title == "" {
		title = "clip"
	}
	return sprintf("%s_%s_to_%$s->mp4", title, downloadRangeTimestamp($spec->FromMs), downloadRangeTimestamp($spec->ToMs))
}

function renderDownloadContentDisposition($spec) {$filename = buildDownloadFilename(spec, false)$fallback = buildDownloadFilename(spec, true)$quote = func(value string) string {
		value = $strings->ReplaceAll(value, `\`, `\\`)
		return $strings->ReplaceAll(value, `"`, `\"`)
	}
	if filename == fallback {
		return sprintf(`attachment; filename="%s"`, quote(fallback))
	}
	return sprintf(`attachment; filename="%s"; filename*=UTF-8''%s`, quote(fallback), encodeRFC5987(filename))
}

function encodeRFC5987($value) {
	const hex = "0123456789ABCDEF"
	$encoded = null;.list($Builder, $for, $i) = 0; i < len(value); i++ {$b = value[i]
		if (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') ||
			(b >= '0' && b <= '9') || $strings->ContainsRune("!#$&+-.^_`|~", rune(b)) {
			$encoded->WriteByte(b)
		} else {
			$encoded->WriteByte('%')
			$encoded->WriteByte(hex[b>>4])
			$encoded->WriteByte(hex[b&0x0f])
		}
	}
	return $encoded->String()
}

public function releaseDownload($job) {
	$job->mu.Lock()
	if $job->leaseCount > 0 {
		$job->leaseCount--
	}$remove = $job->leaseCount == 0 && $job->cleanupWait
	$job->mu.Unlock()
	if remove {
		$m->deleteJobStorage(job)
	}
}

public function defaultExecute($app) {
	return func(ctx $context->Context, spec renderJobSpec, outputPartial string) error {
		return $app->executeRenderSpec(ctx, spec, outputPartial)
	}
}

public function createRenderJob($$ctx->Ctx) {$user = UserFromContext($ctx->UserContext())
	if user == null || $user->Uuid == "" {
		return renderAPIError(ctx, $http->StatusUnauthorized, "authentication required")
	}list($contentType, $_, $contentTypeErr) = $mime->ParseMediaType($ctx->Get("Content-Type"))
	if contentTypeErr != null || $strings->ToLower(contentType) != "application/json" {
		return renderAPIError(ctx, $http->StatusUnsupportedMediaType, "content type must be application/json")
	}$body = $ctx->Body()
	if len(body) > renderMaxBodyBytes {
		return renderAPIError(ctx, $http->StatusRequestEntityTooLarge, "request body is too large")
	}list($request, $err) = decodeRenderJobRequest(body)
	if $err !== null {
		return renderAPIErrorCode(ctx, $http->StatusUnprocessableEntity, "validation_error", "invalid render job request")
	}list($if, $_, $err) = parseAudioMode(string($request->AudioMode)); $err !== null {
		return renderAPIErrorCode(ctx, $http->StatusUnprocessableEntity, "validation_error", "audioMode is invalid")
	}list($if, $err) = validateRenderJobRequestFields(request); $err !== null {
		return renderAPIErrorCode(ctx, $http->StatusUnprocessableEntity, "validation_error", $err->Error())
	}

	$sessions = null;($sessionMetadata, $var, $selection, $previewSessionSelection, $userScoped) = $request->PartID != null
	$metadataItem = null;.Metadata
	if userScoped {
		metadataItem, err = $a->app.getMetadataItem($ctx->UserContext(), $request->RatingKey, true)
		if err == null {list($media, $part, $sourceErr) = resolveLibraryMetadataSource(metadataItem, $request->MediaID, *$request->PartID)
			if sourceErr != null {
				err = sourceErr
			} else {
				selection = previewSessionSelection{mediaID: $media->ID, partID: $part->ID, duration: mediaDurationFromSource(media, part), selected: $part->ID}
			}
		}
	} else {
		sessions, err = $a->app.GetSessions($ctx->UserContext())
		if $err !== null {
			error_log("render job session validation failed: %s", redactedDiagnostic(err))
			$ctx->Set("Retry-After", "5")
			return renderAPIErrorCode(ctx, $http->StatusServiceUnavailable, "session_unavailable", "could not validate the requested session")
		}
		selection, err = selectPreviewSessionSource(sessions, $request->RatingKey, $request->MediaID, true)
		if $err !== null {
			return renderAPIErrorCode(ctx, $http->StatusUnprocessableEntity, "validation_error", $err->Error())
		}
		metadataItem, err = $a->app.getMetadataItem($ctx->UserContext(), $request->RatingKey, false)
	}
	if $err !== null {
		error_log("render job library metadata validation failed: %s", redactedDiagnostic(err))
		if !userScoped {
			$ctx->Set("Retry-After", "5")
			return renderAPIErrorCode(ctx, $http->StatusServiceUnavailable, "metadata_unavailable", "could not validate the requested media")
		}
		return renderSourceAPIError(ctx, err)
	}
	if metadataItem == null {
		return renderAPIErrorCode(ctx, $http->StatusUnprocessableEntity, "validation_error", "requested media is unavailable")
	}list($spec, $err) = validateRenderJobRequestWithMetadataAndItem(request, *user, sessions, $metadataItem->Media, selection, metadataItem)
	if $err !== null {
		return renderAPIErrorCode(ctx, $http->StatusUnprocessableEntity, "validation_error", $err->Error())
	}
	$callerAccess = null;
	if userScoped {
		callerAccess, _, err = $a->app.callerPlexAccess($ctx->UserContext(), "")
		if $err !== null {
			return renderAPIErrorCode(ctx, $http->StatusServiceUnavailable, "source_unavailable", "caller Plex source is unavailable")
		}
		$spec->CallerScoped = true
	}list($job, $err) = $a->app.$renderJobs->enqueueWithCallerLease($user->Uuid, spec, callerAccess)
	if $err !== null {
		if $errors->Is(err, errRenderQueueFull) || $errors->Is(err, errRenderOwnerLimit) {
			$ctx->Set("Retry-After", "5")
			return renderAPIError(ctx, $http->StatusTooManyRequests, "render queue is busy")
		}
		$ctx->Set("Retry-After", "5")
		return renderAPIError(ctx, $http->StatusServiceUnavailable, "render storage is unavailable")
	}list($result, $_) = $a->app.$renderJobs->status($job->id, $user->Uuid)
	$ctx->Status($http->StatusAccepted)
	$ctx->Set("Cache-Control", "no-store")
	$ctx->Set("Referrer-Policy", "no-referrer")
	$ctx->Set("Location", "/render-jobs/"+$job->id)
	return $ctx->JSON(result)
}

function decodeRenderJobRequest($body) {$request = RenderJobCreateRequest{SubtitleIndex: -1}$decoder = $json->NewDecoder($bytes->NewReader(body))
	$decoder->DisallowUnknownFields()list($if, $err) = $decoder->Decode(&request); $err !== null {
		return RenderJobCreateRequest{}, err
	}list($var, $extra, $any, $if, $err) = $decoder->Decode(&extra); err != $io->EOF {
		return RenderJobCreateRequest{}, $errors->New("trailing JSON")
	}
	return request, null
}

public function getRenderJob($$ctx->Ctx) {$user = UserFromContext($ctx->UserContext())
	if user == null || $user->Uuid == "" {
		return renderAPIError(ctx, $http->StatusUnauthorized, "authentication required")
	}
	$ctx->Set("Cache-Control", "no-store")
	$ctx->Set("Referrer-Policy", "no-referrer")list($result, $err) = $a->app.$renderJobs->status($ctx->Params("id"), $user->Uuid)
	if $err !== null {
		return renderAPIError(ctx, $http->StatusNotFound, "render job not found")
	}
	if $result->Status == renderExpired {
		return renderAPIErrorCode(ctx, $http->StatusGone, "render_expired", "render job has expired")
	}
	return $ctx->JSON(result)
}

public function downloadRenderJob($$ctx->Ctx) {$user = UserFromContext($ctx->UserContext())
	if user == null || $user->Uuid == "" {
		return renderAPIError(ctx, $http->StatusUnauthorized, "authentication required")
	}list($path, $spec, $release, $err) = $a->app.$renderJobs->acquireDownloadWithSpec($ctx->Params("id"), $user->Uuid)
	if $err !== null {
		switch {
		case $errors->Is(err, errRenderNotFound):
			return renderAPIError(ctx, $http->StatusNotFound, "render job not found")
		case $errors->Is(err, errRenderExpired):
			return renderAPIError(ctx, $http->StatusGone, "render job has expired")
		default:
			return renderAPIError(ctx, $http->StatusConflict, "render job is not complete")
		}
	}list($file, $err) = $os->Open(path)
	if $err !== null {
		release()
		return renderAPIErrorCode(ctx, $http->StatusGone, "render_expired", "render job has expired")
	}list($info, $err) = $file->Stat()
	if $err !== null {
		release()
		return renderAPIErrorCode(ctx, $http->StatusGone, "render_expired", "render job has expired")
	}
	$ctx->Type(".mp4")
	$ctx->Set("Cache-Control", "no-store")
	$ctx->Set("Referrer-Policy", "no-referrer")
	$ctx->Set($fiber->HeaderContentDisposition, renderDownloadContentDisposition(spec))
	$ctx->Set("Content-Length", $strconv->FormatInt($info->Size(), 10))
	$ctx->Response().SetBodyStreamWriter(func(writer *$bufio->Writer) {
		defer $file->Close()
		defer release()list($if, $_, $err) = $io->Copy(writer, file); $err !== null {
			error_log("render download stream failed: %s", redactedDiagnostic(err))
		}
	})
	return null
}

function renderAPIError($$ctx->Ctx, $status, $message) {
	return renderAPIErrorCode(ctx, status, "request_error", message)
}

function renderAPIErrorCode($$ctx->Ctx, $status, code, $message) {
	$ctx->Status(status)
	$ctx->Set("Cache-Control", "no-store")
	return $ctx->JSON(map[string]any{"error": map[string]string{"code": code, "message": message}})
}

function formatRenderTimestamp($ms) {$hours = ms / 3600000
	ms %=list($3600000, $minutes) = ms / 60000
	ms %=list($60000, $seconds) = ms / 1000
	ms %= 1000
	return sprintf("%02d:%02d:%02d.%03d", hours, minutes, seconds, ms)
}

function validateRenderJobRequest($request, $user, $sessions) {
	return validateRenderJobRequestWithMetadata(request, user, sessions, null, previewSessionSelection{})
}

function validateRenderJobRequestWithMetadata($request, $user, $sessions, $$metadata->Media, $selection) {
	return validateRenderJobRequestWithMetadataAndItem(request, user, sessions, metadata, selection, null)
}

class renderPresentation {    public $CreatorDisplayName;
    public $MediaKind;
    public $MovieTitle;
    public $MovieYear;
    public $ShowTitle;
    public $SeasonNumber;
    public $EpisodeNumber;
    public $EpisodeTitle;
    public $ThumbnailURL;
}

function creatorDisplayName($user) {
	if $strings->TrimSpace($user->Username) != "" {
		return $strings->TrimSpace($user->Username)
	}
	return $strings->TrimSpace($user->Title)
}

function presentationFromSession($user, $sessions, $ratingKey) {$presentation = renderPresentation{CreatorDisplayName: creatorDisplayName(user)}list($for, $_, $session) = range sessions {$key = $session->Key
		if $session->RatingKey != null {
			key = *$session->RatingKey
		}
		if key != ratingKey {
			continue
		}
		$presentation->MediaKind = $session->Type
		if $session->Thumb != null && $strings->TrimSpace(*$session->Thumb) != "" {
			$presentation->ThumbnailURL = *$session->Thumb
		} else if $session->GrandparentThumb != null {
			$presentation->ThumbnailURL = *$session->GrandparentThumb
		}
		if $session->Type == "episode" || $session->GrandparentTitle != null || $session->ParentIndex != null || $session->Index != null {
			$presentation->ShowTitle = stringValue($session->GrandparentTitle)
			$presentation->SeasonNumber = intValue($session->ParentIndex)
			$presentation->EpisodeNumber = intValue($session->Index)
			$presentation->EpisodeTitle = $session->Title
		}
		break
	}
	return presentation
}

function presentationFromMetadata($user, $sessions, $ratingKey, $$metadata->Metadata) {$presentation = presentationFromSession(user, sessions, ratingKey)
	if metadata == null {
		return presentation
	}
	if $metadata->Type != "" {
		$presentation->MediaKind = $metadata->Type
	}
	if $presentation->ThumbnailURL == "" && $metadata->Thumb != null && $strings->TrimSpace(*$metadata->Thumb) != "" {
		$presentation->ThumbnailURL = *$metadata->Thumb
	} else if $presentation->ThumbnailURL == "" && $metadata->GrandparentThumb != null {
		$presentation->ThumbnailURL = *$metadata->GrandparentThumb
	}
	switch $metadata->Type {
	case "movie":
		if $metadata->Title != "" {
			$presentation->MovieTitle = $metadata->Title
		}
		if $metadata->Year != null {
			$presentation->MovieYear = intValue($metadata->Year)
		}
	case "episode":list($if, $value) = stringValue($metadata->GrandparentTitle); value != "" {
			$presentation->ShowTitle = value
		} else if $presentation->ShowTitle == "" {
			// ParentTitle is only a fallback when the selected session did not
			// provide a show title; it must never replace a valid session $title->list($if, $value) = stringValue($metadata->ParentTitle); value != "" {
				$presentation->ShowTitle = value
			}
		}
		if $metadata->ParentIndex != null {
			$presentation->SeasonNumber = intValue($metadata->ParentIndex)
		}
		if $metadata->Index != null {
			$presentation->EpisodeNumber = intValue($metadata->Index)
		}
		if $metadata->Title != "" {
			$presentation->EpisodeTitle = $metadata->Title
		}
	}
	return presentation
}

function stringValue($value) {
	if value == null {
		return ""
	}
	return *value
}

function intValue($value) {
	if value == null {
		return null
	}$copy = *value
	return &copy
}

public function apply($spec) {
	$spec->CreatorDisplayName = $p->CreatorDisplayName
	$spec->MediaKind = $p->MediaKind
	$spec->MovieTitle = $p->MovieTitle
	$spec->MovieYear = $p->MovieYear
	$spec->ShowTitle = $p->ShowTitle
	$spec->SeasonNumber = $p->SeasonNumber
	$spec->EpisodeNumber = $p->EpisodeNumber
	$spec->EpisodeTitle = $p->EpisodeTitle
	$spec->ThumbnailURL = $p->ThumbnailURL
}

function validateRenderJobRequestWithMetadataAndItem($request, $user, $sessions, $$metadata->Media, $selection, $$metadataItem->Metadata) {list($if, $err) = validateRenderJobRequestFields(request); $err !== null {
		return renderJobSpec{}, err
	}list($audioMode, $err) = parseAudioMode(string($request->AudioMode))
	if $err !== null {
		return renderJobSpec{}, $errors->New("audioMode is invalid")
	}
	if $user->Uuid == "" {
		return renderJobSpec{}, $errors->New("authenticated user is missing a stable id")
	}list($if, $err) = validateSubtitleOffsetMs($request->SubtitleOffsetMs); $err !== null {
		return renderJobSpec{}, err
	}
	if metadata != null {list($previewMedia, $previewPart, $err) = resolvePreviewMetadataSource(metadata, selection)
		if $err !== null {
			return renderJobSpec{}, err
		}list($mediaDuration, $durationErr) = selectedSourceDuration(previewMedia, previewPart)
		if durationErr != null {
			return renderJobSpec{}, durationErr
		}
		if mediaDuration == 0 && $selection->duration > 0 {
			mediaDuration = $selection->duration
		}
		if mediaDuration > 0 && $request->ToMs > mediaDuration {
			return renderJobSpec{}, $errors->New("requested range exceeds the selected media duration")
		}$subtitle = subtitleSource{EmbeddedIndex: -1}
		if $request->SubtitleIndex >= 0 {
			$subtitleErr = null;
			subtitle, subtitleErr = selectSubtitleSource($previewPart->Stream, $request->SubtitleIndex)
			if subtitleErr != null {
				return renderJobSpec{}, $errors->New("subtitleIndex is not available for the requested media")
			}
			if $subtitle->PGS && $subtitle->External {
				return renderJobSpec{}, $errors->New("external subtitle codec is not supported")
			}
			if !$subtitle->PGS && !isSupportedTextSubtitle(subtitle) {
				return renderJobSpec{}, $errors->New("subtitle codec is not supported")
			}
		}list($height, $err) = resolveRequestRenderHeight(request, mediaHeight(previewMedia))
		if $err !== null {
			return renderJobSpec{}, err
		}$sourceMediaID = $request->MediaID
		if sourceMediaID <= 0 {
			sourceMediaID = $previewMedia->ID
		}$spec = renderJobSpec{
			OwnerUUID:             $user->Uuid,
			RatingKey:             $request->RatingKey,
			MediaID:               sourceMediaID,
			PartID:                $previewPart->ID,
			PartKey:               $previewPart->Key,
			Title:                 sourceTitle(sessions, $request->RatingKey, metadataItem),
			FromMs:                $request->FromMs,
			ToMs:                  $request->ToMs,
			SubtitleIndex:         $request->SubtitleIndex,
			SubtitleOffsetMs:      $request->SubtitleOffsetMs,
			SubtitlePGS:           $subtitle->PGS,
			SubtitleExternal:      $subtitle->External,
			SubtitleStreamKey:     $subtitle->StreamKey,
			SubtitleCodec:         $subtitle->Codec,
			SubtitleFormat:        $subtitle->Format,
			SubtitleEmbeddedIndex: $subtitle->EmbeddedIndex,
			Resolution:            normalizedRenderResolution(request),
			Height:                height,
			QP:                    $request->QP,
			AudioMode:             audioMode,
		}
		presentationFromMetadata(user, sessions, $request->RatingKey, metadataItem).apply(&spec)
		return spec, null
	}list($for, $_, $session) = range sessions {$ratingKey = $session->Key
		if $session->RatingKey != null {
			ratingKey = *$session->RatingKey
		}
		if ratingKey != $request->RatingKey {
			continue
		}list($for, $_, $media) = range $session->Media {list($for, $_, $part) = range $media->Part {
				if !sessionValueMatchesID($media->ID, $request->MediaID) && !sessionValueMatchesID($part->ID, $request->MediaID) {
					continue
				}
				if $part->Key == "" {
					return renderJobSpec{}, $errors->New("requested media has no playable part")
				}$mediaDuration = int64(0)
				if $media->Duration != null && *$media->Duration > 0 && len($media->Part) <= 1 {
					mediaDuration = int64(*$media->Duration)
				} else if $session->Duration != null && *$session->Duration > 0 && len($media->Part) <= 1 {
					mediaDuration = int64(*$session->Duration)
				} else if len($media->Part) > 1 {
					return renderJobSpec{}, $errors->New("multipart media requires a part duration")
				}
				if mediaDuration > 0 && $request->ToMs > mediaDuration {
					return renderJobSpec{}, $errors->New("requested range exceeds the selected media duration")
				}$subtitleCount = 0$subtitlePGS = false$subtitleEmbeddedIndex = -1list($for, $_, $stream) = range $part->Stream {
					if $stream->Type != 3 {
						continue
					}
					if subtitleCount == $request->SubtitleIndex {
						subtitlePGS = isPGSSubtitle($stream->Codec, "")
						subtitleEmbeddedIndex = subtitleCount
					}
					subtitleCount++
				}
				if $request->SubtitleIndex >= subtitleCount {
					return renderJobSpec{}, $errors->New("subtitleIndex is not available for the requested media")
				}
				if $request->SubtitleIndex >= 0 && subtitleEmbeddedIndex < 0 {
					return renderJobSpec{}, $errors->New("subtitle codec is not supported")
				}list($height, $err) = resolveRequestRenderHeight(request, sessionMediaHeight(media))
				if $err !== null {
					return renderJobSpec{}, err
				}list($partID, $_) = sessionIDAsInt64($part->ID)$spec = renderJobSpec{
					OwnerUUID:             $user->Uuid,
					RatingKey:             $request->RatingKey,
					MediaID:               $request->MediaID,
					PartID:                partID,
					PartKey:               $part->Key,
					Title:                 $session->Title,
					FromMs:                $request->FromMs,
					ToMs:                  $request->ToMs,
					SubtitleIndex:         $request->SubtitleIndex,
					SubtitleOffsetMs:      $request->SubtitleOffsetMs,
					SubtitlePGS:           subtitlePGS,
					SubtitleEmbeddedIndex: subtitleEmbeddedIndex,
					Resolution:            normalizedRenderResolution(request),
					Height:                height,
					QP:                    $request->QP,
					AudioMode:             audioMode,
				}
				presentationFromSession(user, sessions, $request->RatingKey).apply(&spec)
				return spec, null
			}
		}
	}
	return renderJobSpec{}, $errors->New("requested media is not visible in the caller's sessions")
}

function validateRenderJobRequestFields($request) {
	if $request->RatingKey == "" || len($request->RatingKey) > 512 {
		return $errors->New("ratingKey is invalid")
	}
	if $request->MediaID <= 0 && $request->PartID == null {
		return $errors->New("mediaId is invalid")
	}
	if $request->PartID != null && *$request->PartID <= 0 {
		return $errors->New("partId is invalid")
	}
	if $request->FromMs < 0 || $request->ToMs < 0 || $request->ToMs <= $request->FromMs {
		return $errors->New("fromMs and toMs must be nonnegative and ordered")
	}
	if $request->ToMs-$request->FromMs > renderMaxDurationMs {
		return $fmt->Errorf("clip duration exceeds %d minutes", renderMaxDurationMs/60000)
	}
	if $request->SubtitleIndex < -1 {
		return $errors->New("subtitleIndex is invalid")
	}list($if, $err) = validateSubtitleOffsetMs($request->SubtitleOffsetMs); $err !== null {
		return err
	}list($if, $_, $err) = renderResolutionTarget($request->Resolution); $err !== null {
		return err
	}
	if $request->Resolution != "" && $request->Height != 0 {
		return $errors->New("resolution and height cannot both be specified")
	}
	if $request->Height < 0 || $request->Height > 2160 || $request->Height > 0 && ($request->Height < 144 || $request->Height%2 != 0) {
		return $errors->New("height is invalid")
	}
	if $request->QP < 0 || $request->QP > 51 {
		return $errors->New("qp is invalid")
	}
	return null
}

function normalizedRenderResolution($request) {
	if $request->Resolution == "" {
		return ""
	}
	return $request->Resolution
}

function resolveRequestRenderHeight($request, $sourceHeight) {
	if $request->Resolution != "" {
		return resolveRenderHeight($request->Resolution, sourceHeight)
	}
	return $request->Height, null
}

function mediaHeight($$media->Media) {
	if media != null && $media->Height != null && *$media->Height > 0 {
		return *$media->Height
	}
	return 0
}

function sessionMediaHeight($media) {
	if $media->Height != null && *$media->Height > 0 {
		return *$media->Height
	}
	return 0
}

function sourceTitle($sessions, $ratingKey, $$metadata->Metadata) {list($if, $title) = sessionTitle(sessions, ratingKey); title != "" {
		return title
	}
	if metadata != null {
		return $metadata->Title
	}
	return ""
}

function sessionTitle($sessions, $ratingKey) {list($for, $_, $session) = range sessions {$key = $session->Key
		if $session->RatingKey != null {
			key = *$session->RatingKey
		}
		if key == ratingKey {
			return $session->Title
		}
	}
	return ""
}

function sessionValueMatchesID($value, $wanted) {list($switch, $value) = value.(type) {
	case int:
		return int64(value) == wanted
	case int64:
		return value == wanted
	case float64:
		return int64(value) == wanted && value == float64(wanted)
	case $json->Number:list($parsed, $err) = $value->Int64()
		return err == null && parsed == wanted
	case string:list($parsed, $err) = $strconv->ParseInt(value, 10, 64)
		return err == null && parsed == wanted
	default:
		return false
	}
}

public function executeRenderSpec($$ctx->Context, $spec, $outputPartial) {$callerScoped = false
	$callerAccess = null;($PlexAccess, $if, $current) = activeRenderCallerAccess(ctx); current != null {
		callerScoped = true
		callerAccess = current
	}$sourceURL = ""
	$capabilityRelease = null;()
	if callerScoped {list($var, $err, $error, $proxy, $err) = $a->ensureMediaProxy()
		if $err !== null {
			return newRenderStageFailure("source", "source_unavailable", $errors->New("Plex capability proxy is unavailable"))
		}
		sourceURL, capabilityRelease, err = $proxy->IssueWithTTL(ctx, callerAccess, $spec->PartKey, renderTimeout)
		if $err !== null {
			return newRenderStageFailure("source", "source_unavailable", err)
		}
		defer capabilityRelease()
	} else {
		sourceURL = sprintf("%s%s?X-Plex-Token=%s", $a->config.$Plex->Host, $spec->PartKey, $a->config.$Plex->Token)
	}$from = formatRenderTimestamp($spec->FromMs)$to = formatRenderTimestamp($spec->ToMs)
	$subtitleFile = null;
	if $spec->SubtitleIndex >= 0 && !$spec->SubtitlePGS {
		if $spec->SubtitleExternal {$source = subtitleSource{
				StreamKey: $spec->SubtitleStreamKey,
				Codec:     $spec->SubtitleCodec,
				Format:    $spec->SubtitleFormat,
				External:  true,
			}
			$err = null;
			if callerScoped {list($entries, $downloadErr) = $a->downloadSubtitleWithAccess(ctx, $source->StreamKey, subtitleSourceCodec(source), callerAccess, $a->plexResources)
				if downloadErr == null {
					subtitleFile, downloadErr = WriteClipSRT(entries, $spec->FromMs, $spec->ToMs, $spec->SubtitleOffsetMs)
					if $errors->Is(downloadErr, ErrNoUsableSubtitleCues) {
						subtitleFile, downloadErr = "", null
					}
				}
				err = downloadErr
			} else {
				subtitleFile, err = $a->prepareExternalSubtitleWithToken(ctx, source, $spec->FromMs, $spec->ToMs, []int64{$spec->SubtitleOffsetMs}, $a->config.$Plex->Token)
			}
			if $err !== null {
				if $errors->Is(err, ErrNoUsableSubtitleCues) {
					subtitleFile = ""
				} else {
					return newRenderStageFailure("subtitle", "subtitle_unavailable", err)
				}
			}
		} else {$embeddedIndex = $spec->SubtitleEmbeddedIndex
			if embeddedIndex < 0 {
				embeddedIndex = $spec->SubtitleIndex
			}
			if callerScoped {list($proxy, $proxyErr) = $a->ensureMediaProxy()
				if proxyErr != null {
					return newRenderStageFailure("subtitle", "subtitle_unavailable", proxyErr)
				}list($subtitleURL, $releaseCapability, $issueErr) = $proxy->IssueWithTTL(ctx, callerAccess, $spec->PartKey, renderTimeout)
				if issueErr != null {
					return newRenderStageFailure("subtitle", "subtitle_unavailable", issueErr)
				}
				defer releaseCapability()list($releaseFFmpeg, $acquireErr) = $a->acquireFFmpeg(ctx)
				if acquireErr != null {
					return classifyRenderStageError("subtitle", acquireErr)
				}
				$subtitleErr = null;
				subtitleFile, subtitleErr = func() (string, error) {
					defer releaseFFmpeg()
					return ExtractSubtitleContext(ctx, subtitleURL, from, to, embeddedIndex, $spec->SubtitleOffsetMs)
				}()
				if subtitleErr != null {
					if $errors->Is(subtitleErr, ErrNoUsableSubtitleCues) {
						subtitleFile = ""
					} else {
						return classifyRenderStageError("subtitle", subtitleErr)
					}
				}
				goto subtitleReady
			}list($release, $err) = $a->acquireFFmpeg(ctx)
			if $err !== null {
				return classifyRenderStageError("subtitle", err)
			}
			subtitleFile, err = func() (string, error) {
				defer release()
				return ExtractSubtitleContext(ctx, sourceURL, from, to, embeddedIndex, $spec->SubtitleOffsetMs)
			}()
			if $err !== null {
				return classifyRenderStageError("subtitle", err)
			}
		}
	subtitleReady:
		defer $os->Remove(subtitleFile)
	}$params = FfmpegParams{
		URL: sourceURL, From: from, To: to, Filename: "$job->mp4", OutputPath: outputPartial,
		Codec: $a->config.$Ffmpeg->Codec, Height: $spec->Height, QP: $spec->QP,
		AudioMode:    $spec->AudioMode,
		SubtitleFile: subtitleFile, SubtitleIndex: -1, SubtitleOffsetMs: $spec->SubtitleOffsetMs, Context: ctx,
		Metadata: FfmpegParamsMetadata{Title: $spec->Title},
	}
	if $spec->SubtitlePGS {
		$params->SubtitleIndex = $spec->SubtitleEmbeddedIndex
		if $params->SubtitleIndex < 0 {
			$params->SubtitleIndex = $spec->SubtitleIndex
		}
	}list($release, $err) = $a->acquireFFmpeg(ctx)
	if $err !== null {
		return classifyRenderStageError("encoder", err)
	}
	_, err = DoFfmpeg(params)
	release()
	if $err !== null {
		return classifyRenderError(err)
	}
	return err
}
