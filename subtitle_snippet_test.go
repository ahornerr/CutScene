package main

import (
	"strings"
	"testing"
)

// TestSubtitleSnippetFromEntries covers the excerpt used to name downloads.
func TestSubtitleSnippetFromEntries(t *testing.T) {
	tests := []struct {
		name    string
		entries []SubtitleEntry
		want    string
	}{
		{name: "no entries", entries: nil, want: ""},
		{name: "single cue", entries: []SubtitleEntry{{Text: "We never told the police"}}, want: "We never told the police"},
		{
			name:    "multiple cues are joined",
			entries: []SubtitleEntry{{Text: "First line"}, {Text: "second line"}},
			want:    "First line second line",
		},
		{
			name:    "blank cues are skipped",
			entries: []SubtitleEntry{{Text: "   "}, {Text: "Real dialogue"}},
			want:    "Real dialogue",
		},
		{
			name:    "markup is stripped",
			entries: []SubtitleEntry{{Text: "{\\an8}Hello <i>there</i>"}},
			want:    "Hello there",
		},
		{
			name:    "entities are decoded",
			entries: []SubtitleEntry{{Text: "Tom &amp; Jerry"}},
			want:    "Tom & Jerry",
		},
		{
			name:    "newlines collapse to spaces",
			entries: []SubtitleEntry{{Text: "line one\nline two"}},
			want:    "line one line two",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := subtitleSnippetFromEntries(test.entries); got != test.want {
				t.Errorf("subtitleSnippetFromEntries() = %q, want %q", got, test.want)
			}
		})
	}
}

// TestSubtitleSnippetIsBounded keeps the excerpt short enough for a filename.
func TestSubtitleSnippetIsBounded(t *testing.T) {
	long := strings.Repeat("word ", 100)
	got := subtitleSnippetFromEntries([]SubtitleEntry{{Text: long}})
	if len([]rune(got)) > maxSubtitleSnippetRunes {
		t.Errorf("snippet length = %d runes, want at most %d", len([]rune(got)), maxSubtitleSnippetRunes)
	}
	if strings.HasSuffix(got, " ") {
		t.Errorf("snippet %q has a trailing space", got)
	}
	// Truncation should not cut mid-word.
	if !strings.HasSuffix(got, "word") {
		t.Errorf("snippet %q does not end on a word boundary", got)
	}
}

// TestSubtitleSnippetSkipsMarkupOnlyCues ensures a cue made entirely of
// styling produces no excerpt rather than garbage.
func TestSubtitleSnippetSkipsMarkupOnlyCues(t *testing.T) {
	got := subtitleSnippetFromEntries([]SubtitleEntry{{Text: "{\\an8}{\\i1}"}, {Text: "spoken"}})
	if got != "spoken" {
		t.Errorf("snippet = %q, want %q", got, "spoken")
	}
}

// TestBuildDownloadFilenamePrefersSnippet covers the feature: a clip with a
// subtitle excerpt is named by its dialogue, and one without keeps timestamps.
func TestBuildDownloadFilenamePrefersSnippet(t *testing.T) {
	base := renderJobSpec{Title: "The Movie", FromMs: 75000, ToMs: 45000}

	withSnippet := base
	withSnippet.SubtitleSnippet = "We never told the police"
	if got, want := buildDownloadFilename(withSnippet, false), "The_Movie_We_never_told_the_police.mp4"; got != want {
		t.Errorf("filename with snippet = %q, want %q", got, want)
	}

	if got, want := buildDownloadFilename(base, false), "The_Movie_00-01-15_to_00-00-45.mp4"; got != want {
		t.Errorf("filename without snippet = %q, want %q", got, want)
	}

	// A whitespace-only snippet is no snippet.
	blank := base
	blank.SubtitleSnippet = "   "
	if got, want := buildDownloadFilename(blank, false), "The_Movie_00-01-15_to_00-00-45.mp4"; got != want {
		t.Errorf("filename with blank snippet = %q, want %q", got, want)
	}
}

// TestBuildDownloadFilenameSnippetIsSanitised ensures hostile subtitle text
// cannot break out of the filename or inject header content.
func TestBuildDownloadFilenameSnippetIsSanitised(t *testing.T) {
	spec := renderJobSpec{Title: "Movie", FromMs: 0, ToMs: 1000, SubtitleSnippet: "a/b\\c\"d\r\nX-Injected: 1"}
	got := buildDownloadFilename(spec, false)
	if strings.ContainsAny(got, "/\\\"\r\n") {
		t.Errorf("filename %q contains unsafe characters", got)
	}
	if strings.Contains(got, ":") {
		t.Errorf("filename %q retained a header separator", got)
	}
	if !strings.HasSuffix(got, ".mp4") {
		t.Errorf("filename %q lost its extension", got)
	}
}
