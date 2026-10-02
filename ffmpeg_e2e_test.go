package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestMain makes a missing FFmpeg a hard failure in CI while still allowing a
// bare `go test` on a developer machine without FFmpeg to pass.
//
// Without this, the guard below skips silently: CI reported the suite green and
// "ok" while this test never executed, because the GitHub runner image does not
// ship FFmpeg. Skipping must be visible.
func TestMain(m *testing.M) {
	if os.Getenv("CI") != "" {
		if _, err := exec.LookPath("ffmpeg"); err != nil {
			fmt.Fprintln(os.Stderr, "CI is set but ffmpeg is not installed; the end-to-end preview/render tests would be skipped.")
			fmt.Fprintln(os.Stderr, "Install ffmpeg (apt-get install -y ffmpeg) so CI exercises them.")
			os.Exit(1)
		}
	}

	// Initialise the session store for the whole package. main() does this
	// after loading the configuration because the location depends on
	// storage.root; tests construct API values directly and bypass main, so
	// they must initialise it here.
	sessionDir, err := os.MkdirTemp("", "cutscene-test-session-*")
	if err != nil {
		fmt.Fprintln(os.Stderr, "could not create test session directory:", err)
		os.Exit(1)
	}
	if err := configureSessionStore(sessionDir, false); err != nil {
		fmt.Fprintln(os.Stderr, "could not initialise test session store:", err)
		os.RemoveAll(sessionDir)
		os.Exit(1)
	}

	code := m.Run()
	os.RemoveAll(sessionDir)
	os.Exit(code)
}

// TestPreviewAndRenderProduceEquivalentVideo exercises both pipelines for real
// against the same source and compares decoded pixels. Argument assertions
// cannot prove the refactor kept a preview faithful to the render it precedes.
//
// Render is given the preview height because previews are fixed at
// defaultPreviewHeight while renders are user-selectable; comparing a native
// resolution render against a 720p preview would compare different requests.
func TestPreviewAndRenderProduceEquivalentVideo(t *testing.T) {
	if _, lookErr := exec.LookPath("ffmpeg"); lookErr != nil {
		t.Skip("ffmpeg is not installed; skipping end-to-end encode comparison (set CI=true to make this fatal)")
	}
	dir := t.TempDir()
	source := filepath.Join(dir, "source.mp4")
	if out, err := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", "testsrc2=d=2:s=320x240", "-t", "2",
		"-c:v", "libx264", "-crf", "23", "-pix_fmt", "yuv420p", "-y", source).CombinedOutput(); err != nil {
		t.Fatalf("creating the source clip failed: %v\n%s", err, out)
	}

	rendered := filepath.Join(dir, "render.mp4")
	if _, err := DoFfmpeg(FfmpegParams{
		URL:        source,
		From:       "00:00:00",
		To:         "00:00:02",
		Filename:   "render.mp4",
		OutputPath: rendered,
		Codec:      CodecLibx264,
		Height:     defaultPreviewHeight,
		AudioMode:  AudioModeStandard,
		// -1 so no subtitle handling is involved; subtitle paths are covered
		// by the argument-level tests.
		SubtitleIndex: -1,
		Context:       context.Background(),
	}); err != nil {
		t.Fatalf("render failed: %v", err)
	}

	var streamed bytes.Buffer
	if err := DoFfmpegPreviewContextWithSubtitleOffset(
		context.Background(), source, "00:00:00", "00:00:02", "", -1,
		CodecLibx264, &streamed, AudioModeStandard, 0,
	); err != nil {
		t.Fatalf("preview failed: %v", err)
	}
	if streamed.Len() == 0 {
		t.Fatal("preview produced no output")
	}
	previewFile := filepath.Join(dir, "preview.mp4")
	if err := os.WriteFile(previewFile, streamed.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}

	renderPixels := decodeGrayFrames(t, rendered)
	previewPixels := decodeGrayFrames(t, previewFile)
	if len(renderPixels) == 0 {
		t.Fatal("render produced no decodable frames")
	}
	if len(renderPixels) != len(previewPixels) {
		t.Fatalf("decoded sample counts differ: render=%d preview=%d",
			len(renderPixels), len(previewPixels))
	}
	worst := 0
	total := 0
	for i := range renderPixels {
		diff := int(renderPixels[i]) - int(previewPixels[i])
		if diff < 0 {
			diff = -diff
		}
		if diff > worst {
			worst = diff
		}
		total += diff
	}
	// A tiny threshold absorbs encoder nondeterminism while still failing if
	// the two pipelines genuinely diverge (different filters, scaling, or
	// subtitle handling produce large differences).
	if worst > 2 {
		t.Errorf("preview and render decoded pixels differ (max delta %d, mean %.4f); the preview no longer represents the render",
			worst, float64(total)/float64(len(renderPixels)))
	}
}

// decodeGrayFrames decodes path to small grayscale raw frames for comparison.
func decodeGrayFrames(t *testing.T, path string) []byte {
	t.Helper()
	out, err := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error",
		"-i", path, "-vf", "scale=32:24,format=gray", "-f", "rawvideo", "-").Output()
	if err != nil {
		t.Fatalf("decoding %s failed: %v", filepath.Base(path), err)
	}
	return out
}
