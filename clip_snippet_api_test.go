package main

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestClipAPIExposesSubtitleSnippet pins the contract the clip library relies
// on: the dialogue excerpt stored with a clip is served to the frontend so it
// can be displayed and searched.
//
// The field was deliberately absent when the excerpt was introduced, because it
// was only used to name downloads.
func TestClipAPIExposesSubtitleSnippet(t *testing.T) {
	store, err := newClipStore(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	defer store.close()

	source := filepath.Join(t.TempDir(), "clip.mp4")
	if err := os.WriteFile(source, []byte("mp4"), 0o600); err != nil {
		t.Fatal(err)
	}

	const dialogue = "We never told the police"
	spec := renderTestSpec("owner-a")
	spec.Title = "The Movie"
	spec.SubtitleSnippet = dialogue
	clip, err := store.promote(spec, source)
	if err != nil {
		t.Fatal(err)
	}

	api := &API{config: Config{}, app: &Application{clipStore: store}}
	httpApp := testAuthenticatedParamRoute("GET", "/clips/:id", api.getClip)
	resp, err := httpApp.Test(httptest.NewRequest("GET", "/clips/"+clip.ID, nil))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	var payload struct {
		SubtitleSnippet string `json:"subtitleSnippet"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if payload.SubtitleSnippet != dialogue {
		t.Errorf("subtitleSnippet = %q, want %q", payload.SubtitleSnippet, dialogue)
	}
}

// TestClipAPIWithoutSubtitleSnippet stays omitted so the frontend can tell
// "no dialogue captured" from an empty string.
func TestClipAPIWithoutSubtitleSnippet(t *testing.T) {
	store, err := newClipStore(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	defer store.close()

	source := filepath.Join(t.TempDir(), "clip.mp4")
	if err := os.WriteFile(source, []byte("mp4"), 0o600); err != nil {
		t.Fatal(err)
	}
	clip, err := store.promote(renderTestSpec("owner-a"), source)
	if err != nil {
		t.Fatal(err)
	}

	api := &API{config: Config{}, app: &Application{clipStore: store}}
	response, err := api.clipResponse(clip, true, false, true, false, false)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) == "" {
		t.Fatal("empty response")
	}
	var fields map[string]any
	if err := json.Unmarshal(encoded, &fields); err != nil {
		t.Fatal(err)
	}
	if _, present := fields["subtitleSnippet"]; present {
		t.Errorf("subtitleSnippet should be omitted when no dialogue was captured: %s", encoded)
	}
}

// TestRenderJobStatusReportsSubtitleSnippet pins that a finished render tells
// the caller what dialogue it captured, so the UI can show it without opening
// the library.
func TestRenderJobStatusReportsSubtitleSnippet(t *testing.T) {
	manager, err := newRenderJobManager(t.TempDir(), writeRenderOutput)
	if err != nil {
		t.Fatal(err)
	}
	defer manager.stopAndWait()

	const dialogue = "We never told the police"
	job, err := manager.enqueue("owner-a", renderTestSpec("owner-a"))
	if err != nil {
		t.Fatal(err)
	}
	// The encode records the excerpt, as executeRenderSpec does after a
	// successful subtitle pass.
	job.mu.Lock()
	job.spec.SubtitleSnippet = dialogue
	job.mu.Unlock()

	waitRenderStatus(t, manager, job.id, "owner-a", renderSucceeded)

	response, err := manager.status(job.id, "owner-a")
	if err != nil {
		t.Fatal(err)
	}
	if response.SubtitleSnippet != dialogue {
		t.Errorf("status snippet = %q, want %q", response.SubtitleSnippet, dialogue)
	}
}

// TestRenderJobStatusOmitsSnippetWhenNoneCaptured keeps the field absent for
// renders that carried no subtitle track.
func TestRenderJobStatusOmitsSnippetWhenNoneCaptured(t *testing.T) {
	manager, err := newRenderJobManager(t.TempDir(), writeRenderOutput)
	if err != nil {
		t.Fatal(err)
	}
	defer manager.stopAndWait()

	job, err := manager.enqueue("owner-a", renderTestSpec("owner-a"))
	if err != nil {
		t.Fatal(err)
	}
	waitRenderStatus(t, manager, job.id, "owner-a", renderSucceeded)

	response, err := manager.status(job.id, "owner-a")
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(encoded); strings.Contains(got, "subtitleSnippet") {
		t.Errorf("subtitleSnippet should be omitted when nothing was captured: %s", got)
	}
}
