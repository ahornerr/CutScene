package main

import (
	"testing"
)

func strPtr(v string) *string { return &v }
func intPtr(v int) *int       { return &v }

func previewSessions() []sessionMetadata {
	return []sessionMetadata{{
		Key:       "movie-1",
		RatingKey: strPtr("movie-1"),
		Duration:  intPtr(120000),
		Media: []sessionMedia{{
			ID:       float64(500),
			Duration: intPtr(60000),
			Part:     []sessionPart{{ID: float64(700)}},
		}},
	}}
}

// TestSelectPreviewSessionSourceDefaultsWhenNoIDSupplied covers the legacy
// active-session path, where the caller sends no mediaId and the playing part
// is inferred. This branch had no coverage.
func TestSelectPreviewSessionSourceDefaultsWhenNoIDSupplied(t *testing.T) {
	selection, err := selectPreviewSessionSource(previewSessions(), "movie-1", 0, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if selection.mediaID != 500 {
		t.Errorf("mediaID = %d, want 500", selection.mediaID)
	}
	if selection.partID != 700 {
		t.Errorf("partID = %d, want the playing part 700", selection.partID)
	}
	if selection.selected != 700 {
		t.Errorf("selected = %d, want 700", selection.selected)
	}
	// Media duration wins over the session-level duration when present.
	if selection.duration != 60000 {
		t.Errorf("duration = %d, want the media duration 60000", selection.duration)
	}
}

// TestSelectPreviewSessionSourceFallsBackToSessionDuration pins the duration
// precedence when the media entry omits one.
func TestSelectPreviewSessionSourceFallsBackToSessionDuration(t *testing.T) {
	sessions := []sessionMetadata{{
		Key:      "movie-1",
		Duration: intPtr(90000),
		Media:    []sessionMedia{{ID: float64(500), Part: []sessionPart{{ID: float64(700)}}}},
	}}
	selection, err := selectPreviewSessionSource(sessions, "movie-1", 0, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if selection.duration != 90000 {
		t.Errorf("duration = %d, want the session duration 90000", selection.duration)
	}
}

// TestSelectPreviewSessionSourceRejectsOtherSessions ensures a caller cannot
// preview a rating key they are not watching.
func TestSelectPreviewSessionSourceRejectsOtherSessions(t *testing.T) {
	if _, err := selectPreviewSessionSource(previewSessions(), "movie-2", 0, false); err == nil {
		t.Error("expected an error for a rating key outside the caller's sessions")
	}
	if _, err := selectPreviewSessionSource(nil, "movie-1", 0, false); err == nil {
		t.Error("expected an error when there are no active sessions")
	}
}

// TestPreviewSessionMediaIDIsAuthorizationChecked ensures the helper used by
// callers resolves through the same visibility rules.
func TestPreviewSessionMediaIDIsAuthorizationChecked(t *testing.T) {
	got, err := previewSessionMediaID(previewSessions(), "movie-1", 700, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != 700 {
		t.Errorf("mediaID = %d, want 700", got)
	}
	if _, err := previewSessionMediaID(previewSessions(), "movie-1", 4242, true); err == nil {
		t.Error("expected an error for an id that is not visible in the caller's sessions")
	}
}
