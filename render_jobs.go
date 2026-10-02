package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Core render-job domain model.
//
// Job and spec types, the request payload, failure classification and log
// redaction, the per-job storage directory, and the FFmpeg concurrency limiter.
// The queue, validation, download naming, HTTP handlers, and the encode step
// live in the sibling render_jobs_*.go files.

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
	OwnerUUID        string
	SourceToken      string
	CallerScoped     bool
	RatingKey        string
	MediaID          int64
	PartID           int64
	PartKey          string
	PartFile         string // resolved local filesystem path, empty if not available
	Title            string
	FromMs           int64
	ToMs             int64
	SubtitleIndex    int
	SubtitleOffsetMs int64
	SubtitlePGS      bool
	// SubtitleSnippet is a short excerpt of the burned-in dialogue, filled in
	// by the encode once the subtitle has been extracted.
	SubtitleSnippet       string
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

// renderJobExecutor encodes one render. It returns a short excerpt of the
// clip's subtitle dialogue when a subtitle track was burned in, which the
// manager records on the spec so the download filename can identify the clip.
type renderJobExecutor func(context.Context, renderJobSpec, string) (string, error)

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
