package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRenderJobRecordsSubtitleSnippetForDownload walks the feature end to end:
// the encode captures the clip's dialogue, the manager records it on the job,
// and the download filename uses it.
//
// This is the behaviour the user asked for -- several clips of the same scene
// should be distinguishable in a folder -- so it is asserted through the real
// queue rather than only on the filename helper.
func TestRenderJobRecordsSubtitleSnippetForDownload(t *testing.T) {
	const dialogue = "We never told the police anything"

	root := t.TempDir()
	store, err := newClipStore(root, "")
	if err != nil {
		t.Fatal(err)
	}
	defer store.close()

	originalExtract := extractSubtitleContextFn
	originalDoFfmpeg := doFfmpegFn
	t.Cleanup(func() {
		extractSubtitleContextFn = originalExtract
		doFfmpegFn = originalDoFfmpeg
	})

	// The encode produces the clip's SRT exactly as the real path does, so the
	// snippet is derived from genuine extracted cues.
	extractSubtitleContextFn = func(ctx context.Context, url, from, to string, subtitleIndex int, offsets ...int64) (string, error) {
		return WriteClipSRT([]SubtitleEntry{{Start: 1000, End: 2000, Text: dialogue}}, 0, 5000)
	}
	doFfmpegFn = func(params FfmpegParams) (string, error) {
		// The queue requires a real output file before promoting a clip.
		if err := os.WriteFile(params.OutputPath, []byte("rendered mp4"), 0o600); err != nil {
			return "", err
		}
		return params.OutputPath, nil
	}

	var promoted *Clip
	manager, err := newRenderJobManagerWithContextAndPromotion(
		context.Background(),
		filepath.Join(root, "transient"),
		func(ctx context.Context, spec renderJobSpec, outputPartial string) (string, error) {
			return (&Application{}).executeRenderSpec(ctx, spec, outputPartial)
		},
		func(job *renderJob, output string) error {
			clip, err := store.promote(job.spec, output)
			if err != nil {
				return err
			}
			promoted = clip
			job.mu.Lock()
			job.clipID = clip.ID
			job.mu.Unlock()
			return nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer manager.stopAndWait()

	spec := renderTestSpec("owner-a")
	spec.Title = "The Movie"
	spec.SubtitleIndex = 1
	spec.SubtitleEmbeddedIndex = 1

	job, err := manager.enqueue("owner-a", spec)
	if err != nil {
		t.Fatal(err)
	}
	status := waitRenderStatus(t, manager, job.id, "owner-a", renderSucceeded)
	if status.Error != nil {
		t.Fatalf("render failed: code=%s message=%s", status.Error.Code, status.Error.Message)
	}

	_, downloadSpec, release, err := manager.acquireDownloadWithSpec(job.id, "owner-a")
	if err != nil {
		t.Fatalf("acquiring download: %v", err)
	}
	defer release()

	if downloadSpec.SubtitleSnippet != dialogue {
		t.Errorf("job snippet = %q, want %q", downloadSpec.SubtitleSnippet, dialogue)
	}
	filename := buildDownloadFilename(downloadSpec, false)
	if !strings.Contains(filename, "We_never_told_the_police") {
		t.Errorf("filename %q does not contain the clip dialogue", filename)
	}

	if promoted == nil {
		t.Fatal("clip was not promoted")
	}
	if promoted.SubtitleSnippet != dialogue {
		t.Errorf("promoted clip snippet = %q, want %q", promoted.SubtitleSnippet, dialogue)
	}

	// The excerpt must survive a restart, since clips are downloaded later.
	if err := store.close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := newClipStore(root, "")
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.close()
	restored, err := reopened.get(promoted.ID)
	if err != nil {
		t.Fatal(err)
	}
	if restored.SubtitleSnippet != dialogue {
		t.Errorf("snippet after reopen = %q, want %q", restored.SubtitleSnippet, dialogue)
	}
}

// TestClipDownloadWithoutSubtitleKeepsTimestamps ensures clips rendered with no
// subtitle track still get a usable, unique name.
func TestClipDownloadWithoutSubtitleKeepsTimestamps(t *testing.T) {
	clip := &Clip{Title: "The Movie", FromMs: 75000, ToMs: 45000}
	got := buildDownloadFilename(renderJobSpec{
		Title: clip.Title, FromMs: clip.FromMs, ToMs: clip.ToMs, SubtitleSnippet: clip.SubtitleSnippet,
	}, false)
	if !strings.Contains(got, "00-01-15") {
		t.Errorf("filename %q lost the timestamps", got)
	}
}
