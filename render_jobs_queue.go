package main

import (
	"context"
	"errors"
	"log"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"
)

// Render-job queue and lifecycle.
//
// Owns job admission (owner limits, queue depth, caller capability leases),
// the worker pool that runs encodes, retention and cleanup, and the status and
// download-lease accessors used by the HTTP handlers.

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
		var subtitleSnippet string
		subtitleSnippet, err = m.execute(ctx, job.spec, filepath.Join(job.dir, "output.partial"))
		if err == nil && subtitleSnippet != "" {
			// Record the excerpt under the job lock: the worker mutates the spec
			// while status and download handlers may be reading it concurrently.
			job.mu.Lock()
			job.spec.SubtitleSnippet = subtitleSnippet
			job.mu.Unlock()
		}
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
	response.SubtitleSnippet = job.spec.SubtitleSnippet
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
