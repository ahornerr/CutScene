package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/LukeHagar/plexgo/models/components"
	"github.com/gofiber/fiber/v3"
	"github.com/google/uuid"
)

const (
	renderRoot                       = "/tmp/cutscene-renders"
	renderQueueCapacity              = 8
	renderOwnerLimit                 = 2
	renderMaxBodyBytes               = 64 << 10
	renderMaxDurationMs        int64 = 15 * 60 * 1000
	renderTimeout                    = 30 * time.Minute
	renderPreviewTimeout             = 10 * time.Minute
	renderFFmpegAcquireTimeout       = 2 * time.Second
	renderRetention                  = time.Hour
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

type RenderJobCreateRequest struct {
	RatingKey        string `json:"ratingKey"`
	MediaID          int64  `json:"mediaId"`
	PartID           *int64 `json:"partId,omitempty"`
	FromMs           int64  `json:"fromMs"`
	ToMs             int64  `json:"toMs"`
	SubtitleIndex    int    `json:"subtitleIndex"`
	SubtitleOffsetMs int64  `json:"subtitleOffsetMs"`
	Resolution       string `json:"resolution"`
	// Height is retained for compatibility with older clients. New clients
	// should use Resolution so the server can cap presets to the source tier.
	Height    int       `json:"height"`
	QP        int       `json:"qp"`
	AudioMode AudioMode `json:"audioMode"`
}

type renderJobSpec struct {
	OwnerUUID             string
	SourceToken           string
	CallerScoped          bool
	RatingKey             string
	MediaID               int64
	PartID                int64
	PartKey               string
	Title                 string
	FromMs                int64
	ToMs                  int64
	SubtitleIndex         int
	SubtitleOffsetMs      int64
	SubtitlePGS           bool
	SubtitleExternal      bool
	SubtitleStreamKey     string
	SubtitleCodec         string
	SubtitleFormat        string
	SubtitleEmbeddedIndex int
	Resolution            string
	Height                int
	QP                    int
	AudioMode             AudioMode
	CreatorDisplayName    string
	MediaKind             string
	MovieTitle            string
	MovieYear             *int
	ShowTitle             string
	SeasonNumber          *int
	EpisodeNumber         *int
	EpisodeTitle          string
	ThumbnailURL          string
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

func renderResolutionTarget(resolution string) (int, error) {
	switch resolution {
	case "":
		return 0, nil
	case RenderResolutionSourceNative:
		return 0, nil
	case RenderResolutionNative, RenderResolution2160p, "4k", "4K", RenderResolutionExtraHigh:
		return 2160, nil
	case RenderResolution1080p:
		return 1080, nil
	case RenderResolutionHigh:
		return 1080, nil
	case RenderResolution720p:
		return 720, nil
	case RenderResolutionMedium:
		return 720, nil
	case RenderResolution480p:
		return 480, nil
	case RenderResolutionLow:
		return 480, nil
	default:
		return 0, fmt.Errorf("resolution is invalid")
	}
}

// resolveRenderHeight applies a loose quality tier policy. A source whose
// height is within 20% of the requested tier is left native to avoid an
// unnecessary resize. Sources above that range are scaled down to the tier;
// sources below it are left native so a request can never upscale media.
// Returning zero is also the safe fallback when source dimensions are
// unavailable.
func resolveRenderHeight(resolution string, sourceHeight int) (int, error) {
	target, err := renderResolutionTarget(resolution)
	if err != nil || target == 0 {
		return target, err
	}
	if sourceHeight <= 0 {
		return 0, nil
	}
	// Compare without floating point or multiplication of untrusted source
	// dimensions: [80%, 120%] is [target-target/5, target+target/5].
	lower := target - target/5
	upper := target + target/5
	if sourceHeight >= lower && sourceHeight <= upper {
		return 0, nil
	}
	if sourceHeight > target {
		return target, nil
	}
	return 0, nil
}

type renderJobError struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable"`
}

type renderJobResponse struct {
	ID          string          `json:"id"`
	Status      renderJobState  `json:"status"`
	CreatedAt   time.Time       `json:"createdAt"`
	UpdatedAt   time.Time       `json:"updatedAt"`
	ExpiresAt   *time.Time      `json:"expiresAt,omitempty"`
	DownloadURL string          `json:"downloadUrl,omitempty"`
	ClipID      string          `json:"clipId,omitempty"`
	ShareURL    string          `json:"shareUrl,omitempty"`
	Error       *renderJobError `json:"error,omitempty"`
}

type renderFailure struct {
	stage string
	code  string
	err   error
}

func (e *renderFailure) Error() string {
	if e.stage == "" {
		return e.code + ": " + e.err.Error()
	}
	return e.stage + " " + e.code + ": " + e.err.Error()
}
func (e *renderFailure) Unwrap() error { return e.err }

func newRenderFailure(code string, err error) error {
	return newRenderStageFailure("", code, err)
}

func newRenderStageFailure(stage, code string, err error) error {
	if err == nil {
		err = errors.New(code)
	}
	return &renderFailure{stage: stage, code: code, err: err}
}

func publicRenderFailure(err error) renderJobError {
	code := "render_failed"
	var failure *renderFailure
	if errors.As(err, &failure) {
		code = failure.code
	}
	messages := map[string]string{
		"source_unavailable":   "The source media is unavailable.",
		"subtitle_unavailable": "The selected subtitles are unavailable.",
		"encoder_unavailable":  "The configured video encoder is unavailable.",
		"storage_full":         "Render storage is unavailable.",
		"render_timeout":       "Rendering timed out.",
		"render_failed":        "Rendering failed.",
	}
	retryable := code == "source_unavailable" || code == "subtitle_unavailable" || code == "render_timeout"
	message, ok := messages[code]
	if !ok {
		code = "render_failed"
		message = messages[code]
		retryable = false
	}
	return renderJobError{Code: code, Message: message, Retryable: retryable}
}

var diagnosticURL = regexp.MustCompile(`(?i)https?://[^\s]+`)
var diagnosticCredential = regexp.MustCompile(`(?i)((?:x-plex-token|plex-token|authorization|token|password|api[_-]?key)\s*["']?\s*[:=]\s*["']?(?:bearer\s+)?)[^&\s,}"']+`)

func redactedDiagnostic(err error) string {
	if err == nil {
		return ""
	}
	raw := err.Error()
	if len(raw) > 4096 {
		raw = raw[:4096] + "…[input truncated]"
	}
	message := diagnosticCredential.ReplaceAllString(raw, "${1}[redacted]")
	message = diagnosticURL.ReplaceAllString(message, "[url redacted]")
	message = strings.TrimSpace(message)
	if len(message) > 512 {
		message = message[:512] + "…"
	}
	return message
}

func classifyRenderError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return newRenderFailure("render_timeout", err)
	}
	var failure *renderFailure
	if errors.As(err, &failure) {
		return err
	}
	if errors.Is(err, context.Canceled) {
		return newRenderFailure("render_timeout", err)
	}
	lower := strings.ToLower(err.Error())
	if errors.Is(err, syscall.ENOSPC) || strings.Contains(lower, "no space left") || strings.Contains(lower, "disk full") {
		return newRenderFailure("storage_full", err)
	}
	if strings.Contains(lower, "unknown encoder") || strings.Contains(lower, "encoder not found") ||
		strings.Contains(lower, "cuda") || strings.Contains(lower, "vaapi") {
		return newRenderFailure("encoder_unavailable", err)
	}
	if strings.Contains(lower, "http error") || strings.Contains(lower, "404") ||
		strings.Contains(lower, "connection refused") || strings.Contains(lower, "source unavailable") {
		return newRenderFailure("source_unavailable", err)
	}
	return newRenderFailure("render_failed", err)
}

func classifyRenderStageError(stage string, err error) error {
	classified := classifyRenderError(err)
	var failure *renderFailure
	if errors.As(classified, &failure) {
		if failure.code == "render_failed" && stage == "subtitle" {
			return newRenderStageFailure(stage, "subtitle_unavailable", failure.err)
		}
		return newRenderStageFailure(stage, failure.code, failure.err)
	}
	return newRenderStageFailure(stage, "render_failed", err)
}

type renderStorage struct {
	root string
}

func newRenderStorage(root string) (*renderStorage, error) {
	if err := os.RemoveAll(root); err != nil {
		return nil, fmt.Errorf("remove render root: %w", err)
	}
	if err := os.MkdirAll(root, 0700); err != nil {
		return nil, fmt.Errorf("create render root: %w", err)
	}
	if err := os.Chmod(root, 0700); err != nil {
		return nil, fmt.Errorf("secure render root: %w", err)
	}
	return &renderStorage{root: root}, nil
}

func (s *renderStorage) createJobDir(id string) (string, error) {
	dir := filepath.Join(s.root, id)
	if err := os.Mkdir(dir, 0700); err != nil {
		return "", err
	}
	return dir, nil
}

func (s *renderStorage) removeJobDir(dir string) error {
	if dir == "" {
		return nil
	}
	return os.RemoveAll(dir)
}

type renderJob struct {
	mu           sync.Mutex
	id           string
	ownerUUID    string
	spec         renderJobSpec
	dir          string
	state        renderJobState
	failure      *renderJobError
	createdAt    time.Time
	updatedAt    time.Time
	expiresAt    time.Time
	leaseCount   int
	cleanupWait  bool
	sizeBytes    int64
	bytesCounted bool
	clipID       string
	shareURL     string
}

type renderJobExecutor func(context.Context, renderJobSpec, string) error

type ffmpegLimiter struct {
	slots chan struct{}
}

const defaultFFmpegConcurrency = 2

func normalizeFFmpegConcurrency(size int) int {
	if size <= 0 {
		return defaultFFmpegConcurrency
	}
	return size
}

func newFFmpegLimiter(size int) *ffmpegLimiter {
	return &ffmpegLimiter{slots: make(chan struct{}, normalizeFFmpegConcurrency(size))}
}

func (l *ffmpegLimiter) acquire(ctx context.Context) (func(), error) {
	select {
	case l.slots <- struct{}{}:
		var releaseOnce sync.Once
		return func() {
			releaseOnce.Do(func() { <-l.slots })
		}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

var fallbackFFmpegLimiter = newFFmpegLimiter(defaultFFmpegConcurrency)

func (a *Application) acquireFFmpeg(ctx context.Context) (func(), error) {
	limiter := a.ffmpegLimiter
	if limiter == nil {
		limiter = fallbackFFmpegLimiter
	}
	return limiter.acquire(ctx)
}

type renderJobManager struct {
	mu                 sync.Mutex
	jobs               map[string]*renderJob
	ownerOutstanding   map[string]int
	queue              chan *renderJob
	store              *renderStorage
	execute            renderJobExecutor
	now                func() time.Time
	lifetime           context.Context
	cancel             context.CancelFunc
	stop               chan struct{}
	done               chan struct{}
	cleanupDone        chan struct{}
	stopOnce           sync.Once
	stopped            bool
	maxBytes           int64
	bytesUsed          int64
	maxTerminal        int
	maxFailed          int
	timeout            time.Duration
	promote            func(*renderJob, string) error
	terminalDeleteHook func() // test synchronization point; called under both locks
	callerLeases       map[string]*renderCallerLease
}

type renderCallerLease struct {
	access  *PlexAccess
	expires time.Time
}

type renderCallerAccessContextKey struct{}

func activeRenderCallerAccess(ctx context.Context) *PlexAccess {
	access, _ := ctx.Value(renderCallerAccessContextKey{}).(*PlexAccess)
	return access
}

var errRenderQueueFull = errors.New("render queue is full")
var errRenderOwnerLimit = errors.New("render owner limit reached")
var errRenderNotFound = errors.New("render job not found")
var errRenderExpired = errors.New("render job expired")
var errRenderManagerStopped = errors.New("render job manager is stopped")

func newRenderJobManager(root string, execute renderJobExecutor) (*renderJobManager, error) {
	return newRenderJobManagerWithContext(context.Background(), root, execute)
}

func newRenderJobManagerWithContext(parent context.Context, root string, execute renderJobExecutor) (*renderJobManager, error) {
	return newRenderJobManagerWithContextAndPromotion(parent, root, execute, nil)
}

func newRenderJobManagerWithContextAndPromotion(parent context.Context, root string, execute renderJobExecutor, promote func(*renderJob, string) error) (*renderJobManager, error) {
	store, err := newRenderStorage(root)
	if err != nil {
		return nil, err
	}
	if parent == nil {
		parent = context.Background()
	}
	lifetime, cancel := context.WithCancel(parent)
	m := &renderJobManager{
		jobs:             make(map[string]*renderJob),
		ownerOutstanding: make(map[string]int),
		queue:            make(chan *renderJob, renderQueueCapacity),
		store:            store,
		execute:          execute,
		now:              time.Now,
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
	go m.worker()
	go m.cleanupLoop()
	go m.watchLifetime()
	return m, nil
}

func (m *renderJobManager) requestStop() {
	m.stopOnce.Do(func() {
		m.mu.Lock()
		m.stopped = true
		m.mu.Unlock()
		close(m.stop)
		m.cancel()
	})
}

func (m *renderJobManager) watchLifetime() {
	<-m.lifetime.Done()
	m.requestStop()
}

func (m *renderJobManager) stopAndWait() {
	m.requestStop()
	<-m.done
	<-m.cleanupDone
}

func (m *renderJobManager) enqueue(owner string, spec renderJobSpec) (*renderJob, error) {
	return m.enqueueWithCallerLease(owner, spec, nil)
}

func (m *renderJobManager) enqueueWithCallerLease(owner string, spec renderJobSpec, access *PlexAccess) (*renderJob, error) {
	if owner == "" {
		return nil, errors.New("missing owner")
	}
	m.mu.Lock()
	if m.stopped {
		m.mu.Unlock()
		return nil, errRenderManagerStopped
	}
	m.mu.Unlock()
	id := uuid.NewString()
	dir, err := m.store.createJobDir(id)
	if err != nil {
		return nil, newRenderFailure("storage_full", err)
	}
	now := m.now()
	job := &renderJob{id: id, ownerUUID: owner, spec: spec, dir: dir, state: renderQueued, createdAt: now, updatedAt: now}

	m.mu.Lock()
	if m.stopped {
		m.mu.Unlock()
		_ = m.store.removeJobDir(dir)
		return nil, errRenderManagerStopped
	}
	if m.ownerOutstanding[owner] >= renderOwnerLimit {
		m.mu.Unlock()
		_ = m.store.removeJobDir(dir)
		return nil, errRenderOwnerLimit
	}
	m.jobs[id] = job
	if access != nil {
		m.callerLeases[id] = &renderCallerLease{access: access, expires: m.now().Add(m.timeout)}
	}
	m.ownerOutstanding[owner]++
	select {
	case m.queue <- job:
		m.mu.Unlock()
		return job, nil
	default:
		delete(m.jobs, id)
		delete(m.callerLeases, id)
		m.ownerOutstanding[owner]--
		m.mu.Unlock()
		_ = m.store.removeJobDir(dir)
		return nil, errRenderQueueFull
	}
}

func (m *renderJobManager) setCallerLease(id string, access *PlexAccess, expires time.Time) error {
	if id == "" || access == nil || expires.IsZero() {
		return errors.New("caller render lease is invalid")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.jobs[id]; !ok || m.stopped {
		return errRenderNotFound
	}
	m.callerLeases[id] = &renderCallerLease{access: access, expires: expires}
	return nil
}

func (m *renderJobManager) callerLease(id string) (*PlexAccess, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	lease, ok := m.callerLeases[id]
	if !ok || !m.now().Before(lease.expires) {
		delete(m.callerLeases, id)
		return nil, false
	}
	return lease.access, true
}

func (m *renderJobManager) clearCallerLease(id string) {
	m.mu.Lock()
	delete(m.callerLeases, id)
	m.mu.Unlock()
}

func (m *renderJobManager) worker() {
	defer close(m.done)
	for {
		// Prefer shutdown over already-buffered work. Without this first check,
		// a stopped worker can drain another queued job before observing stop.
		select {
		case <-m.stop:
			m.cancelQueued()
			return
		default:
		}
		select {
		case job := <-m.queue:
			if job == nil {
				continue
			}
			m.run(job)
		case <-m.stop:
			m.cancelQueued()
			return
		}
	}
}

func (m *renderJobManager) cleanupLoop() {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	defer close(m.cleanupDone)
	for {
		select {
		case <-ticker.C:
			m.cleanupExpired()
		case <-m.lifetime.Done():
			return
		}
	}
}

func (m *renderJobManager) cancelQueued() {
	for {
		select {
		case job := <-m.queue:
			m.mu.Lock()
			delete(m.jobs, job.id)
			delete(m.callerLeases, job.id)
			if m.ownerOutstanding[job.ownerUUID] > 0 {
				m.ownerOutstanding[job.ownerUUID]--
			}
			m.mu.Unlock()
			_ = m.store.removeJobDir(job.dir)
		default:
			return
		}
	}
}

func (m *renderJobManager) run(job *renderJob) {
	job.mu.Lock()
	job.state = renderRunning
	job.updatedAt = m.now()
	job.mu.Unlock()

	ctx, cancel := context.WithTimeout(m.lifetime, m.timeout)
	var err error
	if m.execute == nil {
		err = newRenderStageFailure("executor", "encoder_unavailable", errors.New("render executor is unavailable"))
	} else {
		if job.spec.CallerScoped {
			access, ok := m.callerLease(job.id)
			if !ok {
				err = newRenderStageFailure("source", "source_unavailable", errors.New("caller render lease is unavailable"))
			} else {
				ctx = context.WithValue(ctx, renderCallerAccessContextKey{}, access)
			}
		}
		if err != nil {
			cancel()
			public := publicRenderFailure(err)
			job.mu.Lock()
			job.state = renderFailed
			job.failure = &public
			job.updatedAt = m.now()
			job.mu.Unlock()
			m.mu.Lock()
			if m.ownerOutstanding[job.ownerUUID] > 0 {
				m.ownerOutstanding[job.ownerUUID]--
			}
			m.mu.Unlock()
			return
		}
		err = m.execute(ctx, job.spec, filepath.Join(job.dir, "output.partial"))
	}
	defer m.clearCallerLease(job.id)
	cancel()
	if err == nil {
		if shutdownErr := m.lifetime.Err(); shutdownErr != nil {
			err = shutdownErr
		}
	}
	if err == nil {
		err = m.finalizeOutput(job)
	}
	if err == nil && m.promote != nil {
		err = m.promote(job, filepath.Join(job.dir, "output.mp4"))
	}
	if err != nil {
		failure := classifyRenderError(err)
		public := publicRenderFailure(failure)
		log.Printf("render job %s failed category=%s diagnostic=%s", job.id, public.Code, redactedDiagnostic(failure))
		_ = os.Remove(filepath.Join(job.dir, "output.partial"))
		// Promotion can fail after finalizeOutput has charged the transient
		// byte budget. Use the normal storage cleanup path so that failed
		// promotion does not leak that budget.
		m.deleteJobStorage(job)
		job.mu.Lock()
		job.state = renderFailed
		job.failure = &public
		job.updatedAt = m.now()
		job.mu.Unlock()
	} else {
		job.mu.Lock()
		job.state = renderSucceeded
		job.expiresAt = m.now().Add(renderRetention)
		job.updatedAt = m.now()
		job.mu.Unlock()
	}

	m.mu.Lock()
	if m.ownerOutstanding[job.ownerUUID] > 0 {
		m.ownerOutstanding[job.ownerUUID]--
	}
	m.mu.Unlock()
	m.enforceTerminalBudget()
}

func (m *renderJobManager) finalizeOutput(job *renderJob) error {
	partial := filepath.Join(job.dir, "output.partial")
	output := filepath.Join(job.dir, "output.mp4")
	info, err := os.Stat(partial)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return newRenderStageFailure("render", "render_failed", errors.New("render output was not produced"))
		}
		return newRenderStageFailure("storage", "storage_full", err)
	}
	if !info.Mode().IsRegular() || info.Size() == 0 {
		return newRenderStageFailure("render", "render_failed", errors.New("render output is empty or not regular"))
	}
	if err := os.Rename(partial, output); err != nil {
		return newRenderStageFailure("storage", "storage_full", err)
	}
	info, err = os.Stat(output)
	if err != nil {
		return newRenderStageFailure("render", "render_failed", errors.New("render output disappeared before completion"))
	}
	m.mu.Lock()
	if m.bytesUsed+info.Size() > m.maxBytes {
		m.mu.Unlock()
		_ = os.Remove(output)
		return newRenderStageFailure("storage", "storage_full", errors.New("render byte budget exceeded"))
	}
	m.bytesUsed += info.Size()
	m.mu.Unlock()
	job.mu.Lock()
	job.sizeBytes = info.Size()
	job.bytesCounted = true
	job.mu.Unlock()
	return nil
}

func (m *renderJobManager) cleanupExpired() {
	now := m.now()
	m.mu.Lock()
	jobs := make([]*renderJob, 0, len(m.jobs))
	for _, job := range m.jobs {
		jobs = append(jobs, job)
	}
	m.mu.Unlock()
	for _, job := range jobs {
		remove := false
		job.mu.Lock()
		if job.state == renderSucceeded && !job.expiresAt.IsZero() && !now.Before(job.expiresAt) {
			job.state = renderExpired
			job.cleanupWait = true
			remove = job.leaseCount == 0
		}
		job.mu.Unlock()
		if remove {
			m.deleteJobStorage(job)
		}
	}
}

func (m *renderJobManager) enforceTerminalBudget() {
	m.mu.Lock()
	terminal := make([]*renderJob, 0)
	failed := make([]*renderJob, 0)
	for _, job := range m.jobs {
		job.mu.Lock()
		terminalState := job.state == renderSucceeded || job.state == renderFailed || job.state == renderExpired
		failedState := job.state == renderFailed
		job.mu.Unlock()
		if terminalState {
			terminal = append(terminal, job)
		}
		if failedState {
			failed = append(failed, job)
		}
	}
	m.mu.Unlock()
	sort.Slice(terminal, func(i, j int) bool { return terminal[i].createdAt.Before(terminal[j].createdAt) })
	sort.Slice(failed, func(i, j int) bool { return failed[i].createdAt.Before(failed[j].createdAt) })
	for _, job := range failed[:max(0, len(failed)-m.maxFailed)] {
		m.removeTerminalJob(job)
	}
	for _, job := range terminal[:max(0, len(terminal)-m.maxTerminal)] {
		m.removeTerminalJob(job)
	}
}

func (m *renderJobManager) removeTerminalJob(job *renderJob) {
	// Map removal and the lease check share the manager lock with download
	// acquisition. Once removed, a new downloader cannot obtain this job.
	m.mu.Lock()
	job.mu.Lock()
	if job.leaseCount > 0 {
		job.mu.Unlock()
		m.mu.Unlock()
		return
	}
	if m.terminalDeleteHook != nil {
		m.terminalDeleteHook()
	}
	delete(m.jobs, job.id)
	m.deleteJobStorageLocked(job)
	job.mu.Unlock()
	m.mu.Unlock()
}

func (m *renderJobManager) deleteJobStorage(job *renderJob) {
	m.mu.Lock()
	job.mu.Lock()
	m.deleteJobStorageLocked(job)
	job.mu.Unlock()
	m.mu.Unlock()
}

// deleteJobStorageLocked requires m.mu and job.mu. Keeping the byte-budget
// accounting, lease check, and filesystem removal in the same critical
// section prevents eviction from deleting a file between lease acquisition
// and its first stat/open.
func (m *renderJobManager) deleteJobStorageLocked(job *renderJob) {
	if job.leaseCount > 0 {
		return
	}
	dir := job.dir
	size := job.sizeBytes
	counted := job.bytesCounted
	job.sizeBytes = 0
	job.bytesCounted = false
	if counted {
		m.bytesUsed -= size
		if m.bytesUsed < 0 {
			m.bytesUsed = 0
		}
	}
	_ = m.store.removeJobDir(dir)
}

func (m *renderJobManager) ownedJob(id, owner string) (*renderJob, error) {
	m.mu.Lock()
	job := m.jobs[id]
	m.mu.Unlock()
	if job == nil || job.ownerUUID != owner {
		return nil, errRenderNotFound
	}
	return job, nil
}

func (m *renderJobManager) status(id, owner string) (renderJobResponse, error) {
	job, err := m.ownedJob(id, owner)
	if err != nil {
		return renderJobResponse{}, err
	}
	job.mu.Lock()
	defer job.mu.Unlock()
	response := renderJobResponse{ID: job.id, Status: job.state, CreatedAt: job.createdAt, UpdatedAt: job.updatedAt}
	response.ClipID = job.clipID
	response.ShareURL = job.shareURL
	if !job.expiresAt.IsZero() {
		expiresAt := job.expiresAt
		response.ExpiresAt = &expiresAt
	}
	if job.state == renderSucceeded && !job.cleanupWait {
		response.DownloadURL = "/render-jobs/" + job.id + "/download"
	}
	if job.failure != nil {
		failure := *job.failure
		response.Error = &failure
	}
	return response, nil
}

func (m *renderJobManager) acquireDownload(id, owner string) (string, func(), error) {
	path, _, release, err := m.acquireDownloadWithSpec(id, owner)
	return path, release, err
}

func (m *renderJobManager) acquireDownloadWithSpec(id, owner string) (string, renderJobSpec, func(), error) {
	// Hold the manager lock while taking the job lock. Terminal eviction uses
	// the same order, making map membership and lease acquisition atomic.
	m.mu.Lock()
	job := m.jobs[id]
	if job == nil || job.ownerUUID != owner {
		m.mu.Unlock()
		return "", renderJobSpec{}, nil, errRenderNotFound
	}
	job.mu.Lock()
	m.mu.Unlock()
	state := job.state
	if state != renderSucceeded {
		job.mu.Unlock()
		if state == renderExpired {
			return "", renderJobSpec{}, nil, errRenderExpired
		}
		return "", renderJobSpec{}, nil, errors.New("render job is not complete")
	}
	if job.cleanupWait || (!job.expiresAt.IsZero() && !m.now().Before(job.expiresAt)) {
		job.state = renderExpired
		job.cleanupWait = true
		remove := job.leaseCount == 0
		job.mu.Unlock()
		if remove {
			m.deleteJobStorage(job)
		}
		return "", renderJobSpec{}, nil, errRenderExpired
	}
	job.leaseCount++
	path := filepath.Join(job.dir, "output.mp4")
	spec := job.spec
	job.mu.Unlock()
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() == 0 {
		m.releaseDownload(job)
		return "", renderJobSpec{}, nil, errRenderExpired
	}
	return path, spec, func() { m.releaseDownload(job) }, nil
}

const maxDownloadTitleBytes = 96

func cleanDownloadTitle(title string) (string, string) {
	var safe, ascii []rune
	for _, r := range title {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' {
			safe = append(safe, r)
			if r < utf8.RuneSelf {
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

func normalizeDownloadTitle(runes []rune) string {
	var normalized []rune
	separator := false
	for _, r := range runes {
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
	return strings.Trim(string(normalized), "_-")
}

func boundDownloadTitle(title string) string {
	if len(title) <= maxDownloadTitleBytes {
		return title
	}
	var bounded []rune
	bytes := 0
	for _, r := range title {
		runeBytes := utf8.RuneLen(r)
		if bytes+runeBytes > maxDownloadTitleBytes {
			break
		}
		bounded = append(bounded, r)
		bytes += runeBytes
	}
	return strings.Trim(string(bounded), "_-")
}

func downloadRangeTimestamp(ms int64) string {
	if ms < 0 {
		ms = 0
	}
	hours := ms / 3600000
	minutes := (ms / 60000) % 60
	seconds := (ms / 1000) % 60
	return fmt.Sprintf("%02d-%02d-%02d", hours, minutes, seconds)
}

func buildDownloadFilename(spec renderJobSpec, ascii bool) string {
	title, asciiTitle := cleanDownloadTitle(spec.Title)
	if ascii {
		title = asciiTitle
	}
	if title == "" {
		title = "clip"
	}
	return fmt.Sprintf("%s_%s_to_%s.mp4", title, downloadRangeTimestamp(spec.FromMs), downloadRangeTimestamp(spec.ToMs))
}

func renderDownloadContentDisposition(spec renderJobSpec) string {
	filename := buildDownloadFilename(spec, false)
	fallback := buildDownloadFilename(spec, true)
	quote := func(value string) string {
		value = strings.ReplaceAll(value, `\`, `\\`)
		return strings.ReplaceAll(value, `"`, `\"`)
	}
	if filename == fallback {
		return fmt.Sprintf(`attachment; filename="%s"`, quote(fallback))
	}
	return fmt.Sprintf(`attachment; filename="%s"; filename*=UTF-8''%s`, quote(fallback), encodeRFC5987(filename))
}

func encodeRFC5987(value string) string {
	const hex = "0123456789ABCDEF"
	var encoded strings.Builder
	for i := 0; i < len(value); i++ {
		b := value[i]
		if (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') ||
			(b >= '0' && b <= '9') || strings.ContainsRune("!#$&+-.^_`|~", rune(b)) {
			encoded.WriteByte(b)
		} else {
			encoded.WriteByte('%')
			encoded.WriteByte(hex[b>>4])
			encoded.WriteByte(hex[b&0x0f])
		}
	}
	return encoded.String()
}

func (m *renderJobManager) releaseDownload(job *renderJob) {
	job.mu.Lock()
	if job.leaseCount > 0 {
		job.leaseCount--
	}
	remove := job.leaseCount == 0 && job.cleanupWait
	job.mu.Unlock()
	if remove {
		m.deleteJobStorage(job)
	}
}

func (m *renderJobManager) defaultExecute(app *Application) renderJobExecutor {
	return func(ctx context.Context, spec renderJobSpec, outputPartial string) error {
		return app.executeRenderSpec(ctx, spec, outputPartial)
	}
}

func (a *API) createRenderJob(ctx fiber.Ctx) error {
	user := UserFromContext(ctx.UserContext())
	if user == nil || user.Uuid == "" {
		return renderAPIError(ctx, http.StatusUnauthorized, "authentication required")
	}
	contentType, _, contentTypeErr := mime.ParseMediaType(ctx.Get("Content-Type"))
	if contentTypeErr != nil || strings.ToLower(contentType) != "application/json" {
		return renderAPIError(ctx, http.StatusUnsupportedMediaType, "content type must be application/json")
	}
	body := ctx.Body()
	if len(body) > renderMaxBodyBytes {
		return renderAPIError(ctx, http.StatusRequestEntityTooLarge, "request body is too large")
	}
	request, err := decodeRenderJobRequest(body)
	if err != nil {
		return renderAPIErrorCode(ctx, http.StatusUnprocessableEntity, "validation_error", "invalid render job request")
	}
	if _, err := parseAudioMode(string(request.AudioMode)); err != nil {
		return renderAPIErrorCode(ctx, http.StatusUnprocessableEntity, "validation_error", "audioMode is invalid")
	}
	if err := validateRenderJobRequestFields(request); err != nil {
		return renderAPIErrorCode(ctx, http.StatusUnprocessableEntity, "validation_error", err.Error())
	}

	var sessions []sessionMetadata
	var selection previewSessionSelection
	userScoped := request.PartID != nil
	var metadataItem *components.Metadata
	if userScoped {
		metadataItem, err = a.app.getMetadataItem(ctx.UserContext(), request.RatingKey, true)
		if err == nil {
			media, part, sourceErr := resolveLibraryMetadataSource(metadataItem, request.MediaID, *request.PartID)
			if sourceErr != nil {
				err = sourceErr
			} else {
				selection = previewSessionSelection{mediaID: media.ID, partID: part.ID, duration: mediaDurationFromSource(media, part), selected: part.ID}
			}
		}
	} else {
		sessions, err = a.app.GetSessions(ctx.UserContext())
		if err != nil {
			log.Printf("render job session validation failed: %s", redactedDiagnostic(err))
			ctx.Set("Retry-After", "5")
			return renderAPIErrorCode(ctx, http.StatusServiceUnavailable, "session_unavailable", "could not validate the requested session")
		}
		selection, err = selectPreviewSessionSource(sessions, request.RatingKey, request.MediaID, true)
		if err != nil {
			return renderAPIErrorCode(ctx, http.StatusUnprocessableEntity, "validation_error", err.Error())
		}
		metadataItem, err = a.app.getMetadataItem(ctx.UserContext(), request.RatingKey, false)
	}
	if err != nil {
		log.Printf("render job library metadata validation failed: %s", redactedDiagnostic(err))
		if !userScoped {
			ctx.Set("Retry-After", "5")
			return renderAPIErrorCode(ctx, http.StatusServiceUnavailable, "metadata_unavailable", "could not validate the requested media")
		}
		return renderSourceAPIError(ctx, err)
	}
	if metadataItem == nil {
		return renderAPIErrorCode(ctx, http.StatusUnprocessableEntity, "validation_error", "requested media is unavailable")
	}
	spec, err := validateRenderJobRequestWithMetadataAndItem(request, *user, sessions, metadataItem.Media, selection, metadataItem)
	if err != nil {
		return renderAPIErrorCode(ctx, http.StatusUnprocessableEntity, "validation_error", err.Error())
	}
	var callerAccess *PlexAccess
	if userScoped {
		callerAccess, _, err = a.app.callerPlexAccess(ctx.UserContext(), "")
		if err != nil {
			return renderAPIErrorCode(ctx, http.StatusServiceUnavailable, "source_unavailable", "caller Plex source is unavailable")
		}
		spec.CallerScoped = true
	}
	job, err := a.app.renderJobs.enqueueWithCallerLease(user.Uuid, spec, callerAccess)
	if err != nil {
		if errors.Is(err, errRenderQueueFull) || errors.Is(err, errRenderOwnerLimit) {
			ctx.Set("Retry-After", "5")
			return renderAPIError(ctx, http.StatusTooManyRequests, "render queue is busy")
		}
		ctx.Set("Retry-After", "5")
		return renderAPIError(ctx, http.StatusServiceUnavailable, "render storage is unavailable")
	}
	result, _ := a.app.renderJobs.status(job.id, user.Uuid)
	ctx.Status(http.StatusAccepted)
	ctx.Set("Cache-Control", "no-store")
	ctx.Set("Referrer-Policy", "no-referrer")
	ctx.Set("Location", "/render-jobs/"+job.id)
	return ctx.JSON(result)
}

func decodeRenderJobRequest(body []byte) (RenderJobCreateRequest, error) {
	request := RenderJobCreateRequest{SubtitleIndex: -1}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		return RenderJobCreateRequest{}, err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return RenderJobCreateRequest{}, errors.New("trailing JSON")
	}
	return request, nil
}

func (a *API) getRenderJob(ctx fiber.Ctx) error {
	user := UserFromContext(ctx.UserContext())
	if user == nil || user.Uuid == "" {
		return renderAPIError(ctx, http.StatusUnauthorized, "authentication required")
	}
	ctx.Set("Cache-Control", "no-store")
	ctx.Set("Referrer-Policy", "no-referrer")
	result, err := a.app.renderJobs.status(ctx.Params("id"), user.Uuid)
	if err != nil {
		return renderAPIError(ctx, http.StatusNotFound, "render job not found")
	}
	if result.Status == renderExpired {
		return renderAPIErrorCode(ctx, http.StatusGone, "render_expired", "render job has expired")
	}
	return ctx.JSON(result)
}

func (a *API) downloadRenderJob(ctx fiber.Ctx) error {
	user := UserFromContext(ctx.UserContext())
	if user == nil || user.Uuid == "" {
		return renderAPIError(ctx, http.StatusUnauthorized, "authentication required")
	}
	path, spec, release, err := a.app.renderJobs.acquireDownloadWithSpec(ctx.Params("id"), user.Uuid)
	if err != nil {
		switch {
		case errors.Is(err, errRenderNotFound):
			return renderAPIError(ctx, http.StatusNotFound, "render job not found")
		case errors.Is(err, errRenderExpired):
			return renderAPIError(ctx, http.StatusGone, "render job has expired")
		default:
			return renderAPIError(ctx, http.StatusConflict, "render job is not complete")
		}
	}
	file, err := os.Open(path)
	if err != nil {
		release()
		return renderAPIErrorCode(ctx, http.StatusGone, "render_expired", "render job has expired")
	}
	info, err := file.Stat()
	if err != nil {
		release()
		return renderAPIErrorCode(ctx, http.StatusGone, "render_expired", "render job has expired")
	}
	ctx.Type(".mp4")
	ctx.Set("Cache-Control", "no-store")
	ctx.Set("Referrer-Policy", "no-referrer")
	ctx.Set(fiber.HeaderContentDisposition, renderDownloadContentDisposition(spec))
	ctx.Set("Content-Length", strconv.FormatInt(info.Size(), 10))
	ctx.Response().SetBodyStreamWriter(func(writer *bufio.Writer) {
		defer file.Close()
		defer release()
		if _, err := io.Copy(writer, file); err != nil {
			log.Printf("render download stream failed: %s", redactedDiagnostic(err))
		}
	})
	return nil
}

func renderAPIError(ctx fiber.Ctx, status int, message string) error {
	return renderAPIErrorCode(ctx, status, "request_error", message)
}

func renderAPIErrorCode(ctx fiber.Ctx, status int, code, message string) error {
	ctx.Status(status)
	ctx.Set("Cache-Control", "no-store")
	return ctx.JSON(map[string]any{"error": map[string]string{"code": code, "message": message}})
}

func formatRenderTimestamp(ms int64) string {
	hours := ms / 3600000
	ms %= 3600000
	minutes := ms / 60000
	ms %= 60000
	seconds := ms / 1000
	ms %= 1000
	return fmt.Sprintf("%02d:%02d:%02d.%03d", hours, minutes, seconds, ms)
}

func validateRenderJobRequest(request RenderJobCreateRequest, user User, sessions []sessionMetadata) (renderJobSpec, error) {
	return validateRenderJobRequestWithMetadata(request, user, sessions, nil, previewSessionSelection{})
}

func validateRenderJobRequestWithMetadata(request RenderJobCreateRequest, user User, sessions []sessionMetadata, metadata []components.Media, selection previewSessionSelection) (renderJobSpec, error) {
	return validateRenderJobRequestWithMetadataAndItem(request, user, sessions, metadata, selection, nil)
}

type renderPresentation struct {
	CreatorDisplayName string
	MediaKind          string
	MovieTitle         string
	MovieYear          *int
	ShowTitle          string
	SeasonNumber       *int
	EpisodeNumber      *int
	EpisodeTitle       string
	ThumbnailURL       string
}

func creatorDisplayName(user User) string {
	if strings.TrimSpace(user.Username) != "" {
		return strings.TrimSpace(user.Username)
	}
	return strings.TrimSpace(user.Title)
}

func presentationFromSession(user User, sessions []sessionMetadata, ratingKey string) renderPresentation {
	presentation := renderPresentation{CreatorDisplayName: creatorDisplayName(user)}
	for _, session := range sessions {
		key := session.Key
		if session.RatingKey != nil {
			key = *session.RatingKey
		}
		if key != ratingKey {
			continue
		}
		presentation.MediaKind = session.Type
		if session.Thumb != nil && strings.TrimSpace(*session.Thumb) != "" {
			presentation.ThumbnailURL = *session.Thumb
		} else if session.GrandparentThumb != nil {
			presentation.ThumbnailURL = *session.GrandparentThumb
		}
		if session.Type == "episode" || session.GrandparentTitle != nil || session.ParentIndex != nil || session.Index != nil {
			presentation.ShowTitle = stringValue(session.GrandparentTitle)
			presentation.SeasonNumber = intValue(session.ParentIndex)
			presentation.EpisodeNumber = intValue(session.Index)
			presentation.EpisodeTitle = session.Title
		}
		break
	}
	return presentation
}

func presentationFromMetadata(user User, sessions []sessionMetadata, ratingKey string, metadata *components.Metadata) renderPresentation {
	presentation := presentationFromSession(user, sessions, ratingKey)
	if metadata == nil {
		return presentation
	}
	if metadata.Type != "" {
		presentation.MediaKind = metadata.Type
	}
	if presentation.ThumbnailURL == "" && metadata.Thumb != nil && strings.TrimSpace(*metadata.Thumb) != "" {
		presentation.ThumbnailURL = *metadata.Thumb
	} else if presentation.ThumbnailURL == "" && metadata.GrandparentThumb != nil {
		presentation.ThumbnailURL = *metadata.GrandparentThumb
	}
	switch metadata.Type {
	case "movie":
		if metadata.Title != "" {
			presentation.MovieTitle = metadata.Title
		}
		if metadata.Year != nil {
			presentation.MovieYear = intValue(metadata.Year)
		}
	case "episode":
		if value := stringValue(metadata.GrandparentTitle); value != "" {
			presentation.ShowTitle = value
		} else if presentation.ShowTitle == "" {
			// ParentTitle is only a fallback when the selected session did not
			// provide a show title; it must never replace a valid session title.
			if value := stringValue(metadata.ParentTitle); value != "" {
				presentation.ShowTitle = value
			}
		}
		if metadata.ParentIndex != nil {
			presentation.SeasonNumber = intValue(metadata.ParentIndex)
		}
		if metadata.Index != nil {
			presentation.EpisodeNumber = intValue(metadata.Index)
		}
		if metadata.Title != "" {
			presentation.EpisodeTitle = metadata.Title
		}
	}
	return presentation
}

func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func intValue(value *int) *int {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func (p renderPresentation) apply(spec *renderJobSpec) {
	spec.CreatorDisplayName = p.CreatorDisplayName
	spec.MediaKind = p.MediaKind
	spec.MovieTitle = p.MovieTitle
	spec.MovieYear = p.MovieYear
	spec.ShowTitle = p.ShowTitle
	spec.SeasonNumber = p.SeasonNumber
	spec.EpisodeNumber = p.EpisodeNumber
	spec.EpisodeTitle = p.EpisodeTitle
	spec.ThumbnailURL = p.ThumbnailURL
}

func validateRenderJobRequestWithMetadataAndItem(request RenderJobCreateRequest, user User, sessions []sessionMetadata, metadata []components.Media, selection previewSessionSelection, metadataItem *components.Metadata) (renderJobSpec, error) {
	if err := validateRenderJobRequestFields(request); err != nil {
		return renderJobSpec{}, err
	}
	audioMode, err := parseAudioMode(string(request.AudioMode))
	if err != nil {
		return renderJobSpec{}, errors.New("audioMode is invalid")
	}
	if user.Uuid == "" {
		return renderJobSpec{}, errors.New("authenticated user is missing a stable id")
	}
	if err := validateSubtitleOffsetMs(request.SubtitleOffsetMs); err != nil {
		return renderJobSpec{}, err
	}
	if metadata != nil {
		previewMedia, previewPart, err := resolvePreviewMetadataSource(metadata, selection)
		if err != nil {
			return renderJobSpec{}, err
		}
		mediaDuration, durationErr := selectedSourceDuration(previewMedia, previewPart)
		if durationErr != nil {
			return renderJobSpec{}, durationErr
		}
		if mediaDuration == 0 && selection.duration > 0 {
			mediaDuration = selection.duration
		}
		if mediaDuration > 0 && request.ToMs > mediaDuration {
			return renderJobSpec{}, errors.New("requested range exceeds the selected media duration")
		}

		subtitle := subtitleSource{EmbeddedIndex: -1}
		if request.SubtitleIndex >= 0 {
			var subtitleErr error
			subtitle, subtitleErr = selectSubtitleSource(previewPart.Stream, request.SubtitleIndex)
			if subtitleErr != nil {
				return renderJobSpec{}, errors.New("subtitleIndex is not available for the requested media")
			}
			if subtitle.PGS && subtitle.External {
				return renderJobSpec{}, errors.New("external subtitle codec is not supported")
			}
			if !subtitle.PGS && !isSupportedTextSubtitle(subtitle) {
				return renderJobSpec{}, errors.New("subtitle codec is not supported")
			}
		}
		height, err := resolveRequestRenderHeight(request, mediaHeight(previewMedia))
		if err != nil {
			return renderJobSpec{}, err
		}
		sourceMediaID := request.MediaID
		if sourceMediaID <= 0 {
			sourceMediaID = previewMedia.ID
		}
		spec := renderJobSpec{
			OwnerUUID:             user.Uuid,
			RatingKey:             request.RatingKey,
			MediaID:               sourceMediaID,
			PartID:                previewPart.ID,
			PartKey:               previewPart.Key,
			Title:                 sourceTitle(sessions, request.RatingKey, metadataItem),
			FromMs:                request.FromMs,
			ToMs:                  request.ToMs,
			SubtitleIndex:         request.SubtitleIndex,
			SubtitleOffsetMs:      request.SubtitleOffsetMs,
			SubtitlePGS:           subtitle.PGS,
			SubtitleExternal:      subtitle.External,
			SubtitleStreamKey:     subtitle.StreamKey,
			SubtitleCodec:         subtitle.Codec,
			SubtitleFormat:        subtitle.Format,
			SubtitleEmbeddedIndex: subtitle.EmbeddedIndex,
			Resolution:            normalizedRenderResolution(request),
			Height:                height,
			QP:                    request.QP,
			AudioMode:             audioMode,
		}
		presentationFromMetadata(user, sessions, request.RatingKey, metadataItem).apply(&spec)
		return spec, nil
	}

	for _, session := range sessions {
		ratingKey := session.Key
		if session.RatingKey != nil {
			ratingKey = *session.RatingKey
		}
		if ratingKey != request.RatingKey {
			continue
		}
		for _, media := range session.Media {
			for _, part := range media.Part {
				if !sessionValueMatchesID(media.ID, request.MediaID) && !sessionValueMatchesID(part.ID, request.MediaID) {
					continue
				}
				if part.Key == "" {
					return renderJobSpec{}, errors.New("requested media has no playable part")
				}
				mediaDuration := int64(0)
				if media.Duration != nil && *media.Duration > 0 && len(media.Part) <= 1 {
					mediaDuration = int64(*media.Duration)
				} else if session.Duration != nil && *session.Duration > 0 && len(media.Part) <= 1 {
					mediaDuration = int64(*session.Duration)
				} else if len(media.Part) > 1 {
					return renderJobSpec{}, errors.New("multipart media requires a part duration")
				}
				if mediaDuration > 0 && request.ToMs > mediaDuration {
					return renderJobSpec{}, errors.New("requested range exceeds the selected media duration")
				}
				subtitleCount := 0
				subtitlePGS := false
				subtitleEmbeddedIndex := -1
				for _, stream := range part.Stream {
					if stream.Type != 3 {
						continue
					}
					if subtitleCount == request.SubtitleIndex {
						subtitlePGS = isPGSSubtitle(stream.Codec, "")
						subtitleEmbeddedIndex = subtitleCount
					}
					subtitleCount++
				}
				if request.SubtitleIndex >= subtitleCount {
					return renderJobSpec{}, errors.New("subtitleIndex is not available for the requested media")
				}
				if request.SubtitleIndex >= 0 && subtitleEmbeddedIndex < 0 {
					return renderJobSpec{}, errors.New("subtitle codec is not supported")
				}
				height, err := resolveRequestRenderHeight(request, sessionMediaHeight(media))
				if err != nil {
					return renderJobSpec{}, err
				}
				partID, _ := sessionIDAsInt64(part.ID)
				spec := renderJobSpec{
					OwnerUUID:             user.Uuid,
					RatingKey:             request.RatingKey,
					MediaID:               request.MediaID,
					PartID:                partID,
					PartKey:               part.Key,
					Title:                 session.Title,
					FromMs:                request.FromMs,
					ToMs:                  request.ToMs,
					SubtitleIndex:         request.SubtitleIndex,
					SubtitleOffsetMs:      request.SubtitleOffsetMs,
					SubtitlePGS:           subtitlePGS,
					SubtitleEmbeddedIndex: subtitleEmbeddedIndex,
					Resolution:            normalizedRenderResolution(request),
					Height:                height,
					QP:                    request.QP,
					AudioMode:             audioMode,
				}
				presentationFromSession(user, sessions, request.RatingKey).apply(&spec)
				return spec, nil
			}
		}
	}
	return renderJobSpec{}, errors.New("requested media is not visible in the caller's sessions")
}

func validateRenderJobRequestFields(request RenderJobCreateRequest) error {
	if request.RatingKey == "" || len(request.RatingKey) > 512 {
		return errors.New("ratingKey is invalid")
	}
	if request.MediaID <= 0 && request.PartID == nil {
		return errors.New("mediaId is invalid")
	}
	if request.PartID != nil && *request.PartID <= 0 {
		return errors.New("partId is invalid")
	}
	if request.FromMs < 0 || request.ToMs < 0 || request.ToMs <= request.FromMs {
		return errors.New("fromMs and toMs must be nonnegative and ordered")
	}
	if request.ToMs-request.FromMs > renderMaxDurationMs {
		return fmt.Errorf("clip duration exceeds %d minutes", renderMaxDurationMs/60000)
	}
	if request.SubtitleIndex < -1 {
		return errors.New("subtitleIndex is invalid")
	}
	if err := validateSubtitleOffsetMs(request.SubtitleOffsetMs); err != nil {
		return err
	}
	if _, err := renderResolutionTarget(request.Resolution); err != nil {
		return err
	}
	if request.Resolution != "" && request.Height != 0 {
		return errors.New("resolution and height cannot both be specified")
	}
	if request.Height < 0 || request.Height > 2160 || request.Height > 0 && (request.Height < 144 || request.Height%2 != 0) {
		return errors.New("height is invalid")
	}
	if request.QP < 0 || request.QP > 51 {
		return errors.New("qp is invalid")
	}
	return nil
}

func normalizedRenderResolution(request RenderJobCreateRequest) string {
	if request.Resolution == "" {
		return ""
	}
	return request.Resolution
}

func resolveRequestRenderHeight(request RenderJobCreateRequest, sourceHeight int) (int, error) {
	if request.Resolution != "" {
		return resolveRenderHeight(request.Resolution, sourceHeight)
	}
	return request.Height, nil
}

func mediaHeight(media *components.Media) int {
	if media != nil && media.Height != nil && *media.Height > 0 {
		return *media.Height
	}
	return 0
}

func sessionMediaHeight(media sessionMedia) int {
	if media.Height != nil && *media.Height > 0 {
		return *media.Height
	}
	return 0
}

func sourceTitle(sessions []sessionMetadata, ratingKey string, metadata *components.Metadata) string {
	if title := sessionTitle(sessions, ratingKey); title != "" {
		return title
	}
	if metadata != nil {
		return metadata.Title
	}
	return ""
}

func sessionTitle(sessions []sessionMetadata, ratingKey string) string {
	for _, session := range sessions {
		key := session.Key
		if session.RatingKey != nil {
			key = *session.RatingKey
		}
		if key == ratingKey {
			return session.Title
		}
	}
	return ""
}

func sessionValueMatchesID(value any, wanted int64) bool {
	switch value := value.(type) {
	case int:
		return int64(value) == wanted
	case int64:
		return value == wanted
	case float64:
		return int64(value) == wanted && value == float64(wanted)
	case json.Number:
		parsed, err := value.Int64()
		return err == nil && parsed == wanted
	case string:
		parsed, err := strconv.ParseInt(value, 10, 64)
		return err == nil && parsed == wanted
	default:
		return false
	}
}

func (a *Application) executeRenderSpec(ctx context.Context, spec renderJobSpec, outputPartial string) error {
	callerScoped := false
	var callerAccess *PlexAccess
	if current := activeRenderCallerAccess(ctx); current != nil {
		callerScoped = true
		callerAccess = current
	}
	sourceURL := ""
	var capabilityRelease func()
	if callerScoped {
		var err error
		proxy, err := a.ensureMediaProxy()
		if err != nil {
			return newRenderStageFailure("source", "source_unavailable", errors.New("Plex capability proxy is unavailable"))
		}
		sourceURL, capabilityRelease, err = proxy.IssueWithTTL(ctx, callerAccess, spec.PartKey, renderTimeout)
		if err != nil {
			return newRenderStageFailure("source", "source_unavailable", err)
		}
		defer capabilityRelease()
	} else {
		sourceURL = fmt.Sprintf("%s%s?X-Plex-Token=%s", a.config.Plex.Host, spec.PartKey, a.config.Plex.Token)
	}
	from := formatRenderTimestamp(spec.FromMs)
	to := formatRenderTimestamp(spec.ToMs)
	var subtitleFile string
	if spec.SubtitleIndex >= 0 && !spec.SubtitlePGS {
		if spec.SubtitleExternal {
			source := subtitleSource{
				StreamKey: spec.SubtitleStreamKey,
				Codec:     spec.SubtitleCodec,
				Format:    spec.SubtitleFormat,
				External:  true,
			}
			var err error
			if callerScoped {
				entries, downloadErr := a.downloadSubtitleWithAccess(ctx, source.StreamKey, subtitleSourceCodec(source), callerAccess, a.plexResources)
				if downloadErr == nil {
					subtitleFile, downloadErr = WriteClipSRT(entries, spec.FromMs, spec.ToMs, spec.SubtitleOffsetMs)
					if errors.Is(downloadErr, ErrNoUsableSubtitleCues) {
						subtitleFile, downloadErr = "", nil
					}
				}
				err = downloadErr
			} else {
				subtitleFile, err = a.prepareExternalSubtitleWithToken(ctx, source, spec.FromMs, spec.ToMs, []int64{spec.SubtitleOffsetMs}, a.config.Plex.Token)
			}
			if err != nil {
				if errors.Is(err, ErrNoUsableSubtitleCues) {
					subtitleFile = ""
				} else {
					return newRenderStageFailure("subtitle", "subtitle_unavailable", err)
				}
			}
		} else {
			embeddedIndex := spec.SubtitleEmbeddedIndex
			if embeddedIndex < 0 {
				embeddedIndex = spec.SubtitleIndex
			}
			if callerScoped {
				proxy, proxyErr := a.ensureMediaProxy()
				if proxyErr != nil {
					return newRenderStageFailure("subtitle", "subtitle_unavailable", proxyErr)
				}
				subtitleURL, releaseCapability, issueErr := proxy.IssueWithTTL(ctx, callerAccess, spec.PartKey, renderTimeout)
				if issueErr != nil {
					return newRenderStageFailure("subtitle", "subtitle_unavailable", issueErr)
				}
				var subtitleErr error
				subtitleFile, subtitleErr = ExtractSubtitleContext(ctx, subtitleURL, from, to, embeddedIndex, spec.SubtitleOffsetMs)
				releaseCapability()
				if subtitleErr != nil {
					if errors.Is(subtitleErr, ErrNoUsableSubtitleCues) {
						subtitleFile = ""
					} else {
						return classifyRenderStageError("subtitle", subtitleErr)
					}
				}
				goto subtitleReady
			}
			release, err := a.acquireFFmpeg(ctx)
			if err != nil {
				if errors.Is(err, ErrNoUsableSubtitleCues) {
					subtitleFile = ""
				} else {
					return classifyRenderStageError("subtitle", err)
				}
			}
			subtitleFile, err = ExtractSubtitleContext(ctx, sourceURL, from, to, embeddedIndex, spec.SubtitleOffsetMs)
			release()
			if err != nil {
				return classifyRenderStageError("subtitle", err)
			}
		}
	subtitleReady:
		defer os.Remove(subtitleFile)
	}
	params := FfmpegParams{
		URL: sourceURL, From: from, To: to, Filename: "job.mp4", OutputPath: outputPartial,
		Codec: a.config.Ffmpeg.Codec, Height: spec.Height, QP: spec.QP,
		AudioMode:    spec.AudioMode,
		SubtitleFile: subtitleFile, SubtitleIndex: -1, SubtitleOffsetMs: spec.SubtitleOffsetMs, Context: ctx,
		Metadata: FfmpegParamsMetadata{Title: spec.Title},
	}
	if spec.SubtitlePGS {
		params.SubtitleIndex = spec.SubtitleEmbeddedIndex
		if params.SubtitleIndex < 0 {
			params.SubtitleIndex = spec.SubtitleIndex
		}
	}
	release, err := a.acquireFFmpeg(ctx)
	if err != nil {
		return classifyRenderStageError("encoder", err)
	}
	_, err = DoFfmpeg(params)
	release()
	if err != nil {
		return classifyRenderError(err)
	}
	return err
}
