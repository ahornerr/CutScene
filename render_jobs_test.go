package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/LukeHagar/plexgo/models/components"
	ffmpeg "github.com/u2takey/ffmpeg-go"
)

func TestDecodeRenderJobRequestIsStrictAndBoundedByCaller(t *testing.T) {
	request, err := decodeRenderJobRequest([]byte(`{"ratingKey":"movie-1","mediaId":42,"fromMs":0,"toMs":1000}`))
	if err != nil || request.SubtitleIndex != -1 {
		t.Fatalf("valid request decode = %+v, %v", request, err)
	}
	if _, err := decodeRenderJobRequest([]byte(`{"ratingKey":"movie-1","mediaId":42,"fromMs":0,"toMs":1000,"url":"https://bad"}`)); err == nil {
		t.Fatal("unknown fields must be rejected")
	}
	if _, err := decodeRenderJobRequest([]byte(`{"ratingKey":"movie-1"} {}`)); err == nil {
		t.Fatal("trailing JSON must be rejected")
	}
	request, err = decodeRenderJobRequest([]byte(`{"ratingKey":"movie-1","mediaId":42,"fromMs":0,"toMs":1000,"subtitleOffsetMs":-250}`))
	if err != nil || request.SubtitleOffsetMs != -250 {
		t.Fatalf("signed subtitle offset decode = %+v, %v", request, err)
	}
}

func TestRenderJobValidationRejectsSubtitleOffsetOutsideRange(t *testing.T) {
	request := RenderJobCreateRequest{
		RatingKey: "movie-1", MediaID: 42, FromMs: 0, ToMs: 1000,
		SubtitleIndex: -1, SubtitleOffsetMs: maxSubtitleOffsetMs + 1,
	}
	if _, err := validateRenderJobRequest(request, User{Uuid: "owner-a"}, nil); err == nil {
		t.Fatal("out-of-range subtitle offset was accepted")
	}
}

func TestFfmpegRenderOutputForcesMP4Muxer(t *testing.T) {
	args := ffmpeg.KwArgs{}
	configureMP4Output(args)
	commandArgs := ffmpeg.ConvertKwargsToCmdLineArgs(args)
	if len(commandArgs) != 2 || commandArgs[0] != "-f" || commandArgs[1] != "mp4" {
		t.Fatalf("unexpected output construction: %v", commandArgs)
	}
}

func renderTestSpec(owner string) renderJobSpec {
	return renderJobSpec{
		OwnerUUID:     owner,
		RatingKey:     "movie-1",
		MediaID:       42,
		PartKey:       "/library/parts/99/file",
		Title:         "Test movie",
		FromMs:        0,
		ToMs:          1000,
		Height:        720,
		QP:            23,
		SubtitleIndex: -1,
	}
}

func writeRenderOutput(_ context.Context, _ renderJobSpec, output string) error {
	return os.WriteFile(output, []byte("valid mp4 bytes"), 0600)
}

func waitRenderStatus(t *testing.T, manager *renderJobManager, id, owner string, state renderJobState) renderJobResponse {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		status, err := manager.status(id, owner)
		if err == nil && status.Status == state {
			return status
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("job %s did not reach %s", id, state)
	return renderJobResponse{}
}

func TestRenderJobValidationBindsVisibleMediaAndPart(t *testing.T) {
	codec := "srt"
	duration := 10_000
	sessions := []sessionMetadata{{
		Key:   "movie-1",
		Title: "Test movie",
		Media: []sessionMedia{{
			ID:       float64(42),
			Duration: &duration,
			Part: []sessionPart{{
				ID:     float64(99),
				Key:    "/library/parts/99/file",
				Stream: []sessionStream{{Type: 3, Codec: codec}},
			}},
		}},
	}}
	user := User{Uuid: "owner-a"}
	request := RenderJobCreateRequest{RatingKey: "movie-1", MediaID: 99, FromMs: 0, ToMs: 1000, SubtitleIndex: 0, Height: 720, QP: 23}

	spec, err := validateRenderJobRequest(request, user, sessions)
	if err != nil {
		t.Fatalf("unexpected validation error: %v", err)
	}
	if spec.PartKey != "/library/parts/99/file" || spec.OwnerUUID != "owner-a" {
		t.Fatalf("unexpected immutable spec: %+v", spec)
	}

	request.MediaID = 404
	if _, err := validateRenderJobRequest(request, user, sessions); err == nil {
		t.Fatal("expected invisible media to be rejected")
	}
	request.MediaID = 99
	request.ToMs = renderMaxDurationMs + 1
	if _, err := validateRenderJobRequest(request, user, sessions); err == nil {
		t.Fatal("expected excessive duration to be rejected")
	}
	request.ToMs = 1000
	request.ToMs = int64(duration + 1)
	if _, err := validateRenderJobRequest(request, user, sessions); err == nil {
		t.Fatal("expected range beyond media duration to be rejected")
	}
	request.ToMs = 1000
	request.SubtitleIndex = 1
	if _, err := validateRenderJobRequest(request, user, sessions); err == nil {
		t.Fatal("expected unavailable subtitle to be rejected")
	}
}

func TestRenderValidationSnapshotsExternalSubtitleSource(t *testing.T) {
	format := "srt"
	metadata := []components.Media{{
		ID: 42,
		Part: []components.Part{{
			ID:  99,
			Key: "/library/parts/video",
			Stream: []components.Stream{{
				StreamType: 3,
				Key:        "/library/streams/external",
				Codec:      "srt",
				Format:     &format,
			}},
		}},
	}}
	request := RenderJobCreateRequest{RatingKey: "movie-1", MediaID: 99, FromMs: 1000, ToMs: 3000, SubtitleIndex: 0, Height: 720, QP: 23}
	selection := previewSessionSelection{mediaID: 42, partID: 99, selected: 99}
	sessions := []sessionMetadata{{Key: "movie-1", Title: "Test movie"}}

	spec, err := validateRenderJobRequestWithMetadata(request, User{Uuid: "owner-a"}, sessions, metadata, selection)
	if err != nil {
		t.Fatal(err)
	}
	if !spec.SubtitleExternal || spec.SubtitleStreamKey != "/library/streams/external" || spec.SubtitleCodec != "srt" || spec.SubtitleFormat != "srt" {
		t.Fatalf("external source was not snapshotted: %+v", spec)
	}
	if spec.SubtitleEmbeddedIndex != -1 {
		t.Fatalf("external subtitle received embedded ordinal %d", spec.SubtitleEmbeddedIndex)
	}
}

func TestFFmpegLimiterReleaseIsIdempotent(t *testing.T) {
	limiter := newFFmpegLimiter(1)
	release, err := limiter.acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	release()
	release()

	nextRelease, err := limiter.acquire(context.Background())
	if err != nil {
		t.Fatalf("limiter was not released: %v", err)
	}
	nextRelease()
}

func TestRenderJobOwnershipTerminalStateAndDownload(t *testing.T) {
	manager, err := newRenderJobManager(t.TempDir(), writeRenderOutput)
	if err != nil {
		t.Fatal(err)
	}
	defer manager.stopAndWait()

	job, err := manager.enqueue("owner-a", renderTestSpec("owner-a"))
	if err != nil {
		t.Fatal(err)
	}
	if mode := mustFileMode(t, job.dir); mode != 0700 {
		t.Fatalf("job directory mode = %o, want 700", mode)
	}
	status := waitRenderStatus(t, manager, job.id, "owner-a", renderSucceeded)
	if status.DownloadURL == "" || status.Error != nil {
		t.Fatalf("unexpected successful status: %+v", status)
	}
	if _, err := manager.status(job.id, "owner-b"); !errors.Is(err, errRenderNotFound) {
		t.Fatalf("other owner status error = %v, want not found", err)
	}
	if _, _, err := manager.acquireDownload(job.id, "owner-b"); !errors.Is(err, errRenderNotFound) {
		t.Fatalf("other owner download error = %v, want not found", err)
	}

	path, release, err := manager.acquireDownload(job.id, "owner-a")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(path, filepath.Join(job.id, "output.mp4")) || mustFileSize(t, path) == 0 {
		t.Fatalf("invalid completed output path %q", path)
	}
	if _, err := os.Stat(filepath.Join(job.dir, "output.partial")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("partial output still exists: %v", err)
	}
	release()
}

func TestRenderJobExpirationRespectsDownloadLease(t *testing.T) {
	manager, err := newRenderJobManager(t.TempDir(), writeRenderOutput)
	if err != nil {
		t.Fatal(err)
	}
	defer manager.stopAndWait()

	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	manager.now = func() time.Time { return now }
	job, err := manager.enqueue("owner-a", renderTestSpec("owner-a"))
	if err != nil {
		t.Fatal(err)
	}
	waitRenderStatus(t, manager, job.id, "owner-a", renderSucceeded)
	path, release, err := manager.acquireDownload(job.id, "owner-a")
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(renderRetention + time.Second)
	manager.cleanupExpired()
	status, err := manager.status(job.id, "owner-a")
	if err != nil || status.Status != renderExpired {
		t.Fatalf("expired status = %+v, %v", status, err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("leased output removed during download: %v", err)
	}
	release()
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expired output remains after lease release: %v", err)
	}
	if _, _, err := manager.acquireDownload(job.id, "owner-a"); !errors.Is(err, errRenderExpired) {
		t.Fatalf("expired download error = %v, want expired", err)
	}
}

func TestRenderJobQueueAndOwnerSaturation(t *testing.T) {
	release := make(chan struct{})
	started := make(chan struct{}, 1)
	executor := func(_ context.Context, _ renderJobSpec, output string) error {
		started <- struct{}{}
		<-release
		return os.WriteFile(output, []byte("ok"), 0600)
	}
	manager, err := newRenderJobManager(t.TempDir(), executor)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		close(release)
		manager.stopAndWait()
	}()

	first, err := manager.enqueue("owner-0", renderTestSpec("owner-0"))
	if err != nil {
		t.Fatal(err)
	}
	<-started
	if _, err := manager.enqueue(first.ownerUUID, renderTestSpec(first.ownerUUID)); err != nil {
		t.Fatalf("second owner job: %v", err)
	}
	for i := 1; i < renderQueueCapacity; i++ {
		if _, err := manager.enqueue(fmt.Sprintf("owner-%d", i), renderTestSpec(fmt.Sprintf("owner-%d", i))); err != nil {
			t.Fatalf("queue fill %d: %v", i, err)
		}
	}
	if _, err := manager.enqueue("owner-overflow", renderTestSpec("owner-overflow")); !errors.Is(err, errRenderQueueFull) {
		t.Fatalf("queue overflow error = %v, want queue full", err)
	}
	if _, err := manager.enqueue(first.ownerUUID, renderTestSpec(first.ownerUUID)); !errors.Is(err, errRenderOwnerLimit) {
		t.Fatalf("owner overflow error = %v, want owner limit", err)
	}
}

func TestRenderJobFailureIsSanitizedAndCleansOutput(t *testing.T) {
	manager, err := newRenderJobManager(t.TempDir(), func(_ context.Context, _ renderJobSpec, output string) error {
		if err := os.WriteFile(output, []byte("partial"), 0600); err != nil {
			return err
		}
		return newRenderFailure("source_unavailable", errors.New("GET https://plex.test/file?X-Plex-Token=secret stderr=raw"))
	})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.stopAndWait()

	job, err := manager.enqueue("owner-a", renderTestSpec("owner-a"))
	if err != nil {
		t.Fatal(err)
	}
	status := waitRenderStatus(t, manager, job.id, "owner-a", renderFailed)
	if status.Error == nil || status.Error.Code != "source_unavailable" || status.Error.Message == "" {
		t.Fatalf("unexpected public failure: %+v", status.Error)
	}
	if strings.Contains(status.Error.Message, "secret") || strings.Contains(status.Error.Message, "plex.test") {
		t.Fatalf("failure exposed diagnostic: %+v", status.Error)
	}
	if _, err := os.Stat(job.dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed job directory remains: %v", err)
	}
}

func TestRenderFailureClassificationAndRedaction(t *testing.T) {
	classified := classifyRenderError(context.DeadlineExceeded)
	public := publicRenderFailure(classified)
	if public.Code != "render_timeout" || !public.Retryable {
		t.Fatalf("unexpected timeout classification: %+v", public)
	}
	if got := publicRenderFailure(classifyRenderError(errors.New("unknown encoder h264_nvenc"))); got.Code != "encoder_unavailable" {
		t.Fatalf("encoder classification = %+v", got)
	}
	if got := publicRenderFailure(classifyRenderError(errors.New("HTTP error 404 reading source"))); got.Code != "source_unavailable" {
		t.Fatalf("source classification = %+v", got)
	}
	if got := publicRenderFailure(classifyRenderStageError("subtitle", context.Canceled)); got.Code != "render_timeout" {
		t.Fatalf("subtitle cancellation classification = %+v", got)
	}
	if got := publicRenderFailure(classifyRenderStageError("subtitle", syscall.ENOSPC)); got.Code != "storage_full" {
		t.Fatalf("subtitle ENOSPC classification = %+v", got)
	}
	if diagnostic := redactedDiagnostic(errors.New("https://plex.test/a?X-Plex-Token=secret" + strings.Repeat("x", 600))); strings.Contains(diagnostic, "secret") || len(diagnostic) > 514 {
		t.Fatalf("diagnostic was not safely redacted/bounded: %q", diagnostic)
	}
	for _, raw := range []string{
		`{"X-Plex-Token":"secret"}`,
		`Authorization: Bearer secret`,
		`token=secret&other=value`,
	} {
		if diagnostic := redactedDiagnostic(errors.New(raw)); strings.Contains(diagnostic, "secret") {
			t.Fatalf("credential leaked in diagnostic %q -> %q", raw, diagnostic)
		}
	}
}

func TestRenderMissingOutputIsRenderFailure(t *testing.T) {
	manager, err := newRenderJobManager(t.TempDir(), func(context.Context, renderJobSpec, string) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	defer manager.stopAndWait()
	job, err := manager.enqueue("owner-a", renderTestSpec("owner-a"))
	if err != nil {
		t.Fatal(err)
	}
	status := waitRenderStatus(t, manager, job.id, "owner-a", renderFailed)
	if status.Error == nil || status.Error.Code != "render_failed" {
		t.Fatalf("missing output status = %+v", status)
	}
}

func TestFFmpegLimiterTimeoutAndApplicationLifetimeCancellation(t *testing.T) {
	limiter := newFFmpegLimiter(1)
	hold, err := limiter.acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	if _, err := limiter.acquire(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("limiter error = %v", err)
	}
	hold()

	lifetime, cancelLifetime := context.WithCancel(context.Background())
	app := &Application{lifetime: lifetime, cancelLifetime: cancelLifetime}
	operation, stopOperation := app.operationContext(context.Background())
	app.Close()
	select {
	case <-operation.Done():
	case <-time.After(time.Second):
		t.Fatal("application close did not cancel operation context")
	}
	stopOperation()
}

func TestRenderManagerParentCancellationCancelsRunningJob(t *testing.T) {
	parent, cancelParent := context.WithCancel(context.Background())
	started := make(chan struct{})
	manager, err := newRenderJobManagerWithContext(parent, t.TempDir(), func(ctx context.Context, _ renderJobSpec, _ string) error {
		close(started)
		<-ctx.Done()
		return ctx.Err()
	})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.stopAndWait()
	job, err := manager.enqueue("owner-a", renderTestSpec("owner-a"))
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("render job did not start")
	}
	cancelParent()
	status := waitRenderStatus(t, manager, job.id, "owner-a", renderFailed)
	if status.Error == nil || status.Error.Code != "render_timeout" {
		t.Fatalf("parent cancellation status = %+v", status)
	}
}

func TestRenderJobByteBudgetAndShutdownCancellation(t *testing.T) {
	manager, err := newRenderJobManager(t.TempDir(), func(ctx context.Context, _ renderJobSpec, output string) error {
		<-ctx.Done()
		return ctx.Err()
	})
	if err != nil {
		t.Fatal(err)
	}
	manager.timeout = 5 * time.Millisecond
	job, err := manager.enqueue("owner-a", renderTestSpec("owner-a"))
	if err != nil {
		t.Fatal(err)
	}
	status := waitRenderStatus(t, manager, job.id, "owner-a", renderFailed)
	if status.Error == nil || status.Error.Code != "render_timeout" {
		t.Fatalf("timeout status = %+v", status)
	}
	manager.stopAndWait()

	manager, err = newRenderJobManager(t.TempDir(), func(_ context.Context, _ renderJobSpec, output string) error {
		return os.WriteFile(output, []byte("12345"), 0600)
	})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.stopAndWait()
	manager.maxBytes = 4
	job, err = manager.enqueue("owner-a", renderTestSpec("owner-a"))
	if err != nil {
		t.Fatal(err)
	}
	status = waitRenderStatus(t, manager, job.id, "owner-a", renderFailed)
	if status.Error == nil || status.Error.Code != "storage_full" {
		t.Fatalf("byte budget status = %+v", status)
	}
}

func mustFileMode(t *testing.T, path string) os.FileMode {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info.Mode().Perm()
}

func mustFileSize(t *testing.T, path string) int64 {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info.Size()
}
