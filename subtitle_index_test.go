package main

import (
	"context"
	"strings"
	"testing"
)

// TestIsEnglishSubtitleStream covers language detection for the indexer.
func TestIsEnglishSubtitleStream(t *testing.T) {
	tests := []struct {
		name   string
		stream SubtitleStream
		want   bool
	}{
		{name: "language english", stream: SubtitleStream{Language: "English"}, want: true},
		{name: "language eng", stream: SubtitleStream{Language: "eng"}, want: true},
		{name: "language en", stream: SubtitleStream{Language: "en"}, want: true},
		{name: "language code en", stream: SubtitleStream{LanguageCode: "en"}, want: true},
		{name: "regional variant", stream: SubtitleStream{LanguageCode: "en-US"}, want: true},
		{name: "mixed case with space", stream: SubtitleStream{Language: "  ENGLISH  "}, want: true},
		{name: "code wins over language", stream: SubtitleStream{Language: "German", LanguageCode: "eng"}, want: true},
		{name: "german", stream: SubtitleStream{Language: "German", LanguageCode: "de"}, want: false},
		{name: "empty", stream: SubtitleStream{}, want: false},
		{name: "english substring is not enough", stream: SubtitleStream{Language: "Englished"}, want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := isEnglishSubtitleStream(test.stream); got != test.want {
				t.Errorf("isEnglishSubtitleStream(%+v) = %v, want %v", test.stream, got, test.want)
			}
		})
	}
}

// TestSubtitleSourceFingerprintChangesWithEveryInput is the core invariant
// behind incremental indexing: if any input that can change the indexed content
// is omitted, a changed track would keep a stale fingerprint and never be
// re-indexed.
func TestSubtitleSourceFingerprintChangesWithEveryInput(t *testing.T) {
	base := SubtitleStream{Index: 2, Codec: "srt", Language: "English", LanguageCode: "en", Type: "text", External: true}
	baseFP := subtitleSourceFingerprint("movie-1", 42, 99, base, "cast", "rev-1")

	variants := map[string]SubtitleStream{
		"base":         base,
		"index":        {Index: 3, Codec: "srt", Language: "English", LanguageCode: "en", Type: "text", External: true},
		"codec":        {Index: 2, Codec: "ass", Language: "English", LanguageCode: "en", Type: "text", External: true},
		"language":     {Index: 2, Codec: "srt", Language: "German", LanguageCode: "en", Type: "text", External: true},
		"languageCode": {Index: 2, Codec: "srt", Language: "English", LanguageCode: "de", Type: "text", External: true},
		"type":         {Index: 2, Codec: "srt", Language: "English", LanguageCode: "en", Type: "pgs", External: true},
		"not external": {Index: 2, Codec: "srt", Language: "English", LanguageCode: "en", Type: "text", External: false},
	}

	for name, stream := range variants {
		t.Run(name, func(t *testing.T) {
			got := subtitleSourceFingerprint("movie-1", 42, 99, stream, "cast", "rev-1")
			if name == "base" {
				if got != baseFP {
					t.Errorf("identical inputs produced different fingerprints")
				}
				return
			}
			if got == baseFP {
				t.Errorf("changing %s did not change the fingerprint; the track would not be re-indexed", name)
			}
		})
	}

	// Scalar inputs outside the stream must matter too.
	if subtitleSourceFingerprint("movie-2", 42, 99, base, "cast", "rev-1") == baseFP {
		t.Error("ratingKey is not part of the fingerprint")
	}
	if subtitleSourceFingerprint("movie-1", 43, 99, base, "cast", "rev-1") == baseFP {
		t.Error("mediaID is not part of the fingerprint")
	}
	if subtitleSourceFingerprint("movie-1", 42, 100, base, "cast", "rev-1") == baseFP {
		t.Error("partID is not part of the fingerprint")
	}
	if subtitleSourceFingerprint("movie-1", 42, 99, base, "other-cast", "rev-1") == baseFP {
		t.Error("cast context is not part of the fingerprint")
	}
	if subtitleSourceFingerprint("movie-1", 42, 99, base, "cast", "rev-2") == baseFP {
		t.Error("source revision is not part of the fingerprint")
	}
}

// TestSubtitleFingerprintIncludesDimensions ensures embeddings of different
// sizes are never treated as interchangeable.
func TestSubtitleFingerprintIncludesDimensions(t *testing.T) {
	stream := SubtitleStream{Index: 1, Codec: "srt", Language: "English"}
	if subtitleSourceFingerprintWithDimensions("m", 1, 2, stream, "cast", 768) ==
		subtitleSourceFingerprintWithDimensions("m", 1, 2, stream, "cast", 1024) {
		t.Error("fingerprint ignores the embedding dimensions")
	}
	// Dimension 0 keeps the plain fingerprint so callers that do not know the
	// dimensions still produce the historical value.
	if subtitleSourceFingerprintWithDimensions("m", 1, 2, stream, "cast", 0) !=
		subtitleSourceFingerprint("m", 1, 2, stream, "cast") {
		t.Error("dimension-less fingerprint differs from subtitleSourceFingerprint")
	}
}

// TestBoundedFingerprintValue keeps unbounded metadata from inflating hashes.
func TestBoundedFingerprintValue(t *testing.T) {
	long := strings.Repeat("a", 600)
	if got := boundedFingerprintValue(long); len(got) != 512 {
		t.Errorf("length = %d, want 512", len(got))
	}
	short := "abc"
	if got := boundedFingerprintValue(short); got != short {
		t.Errorf("value = %q, want %q", got, short)
	}
}

// TestNullableUUID ensures an absent owner is stored as SQL NULL rather than an
// empty string, which would group unrelated rows together.
func TestNullableUUID(t *testing.T) {
	if got := nullableUUID("   "); got != nil {
		t.Errorf("blank value produced %v, want nil", got)
	}
	if got := nullableUUID("abc"); got != "abc" {
		t.Errorf("value = %v, want abc", got)
	}
}

// TestSubtitleEmbeddingInput pins the text sent to the embedding model.
func TestSubtitleEmbeddingInput(t *testing.T) {
	if got := subtitleEmbeddingInput("", "hello"); got != "Dialogue: hello" {
		t.Errorf("without prefix got %q", got)
	}
	if got := subtitleEmbeddingInput("Show S01", "hello"); got != "Show S01\nDialogue: hello" {
		t.Errorf("with prefix got %q", got)
	}
}

// TestSubtitleSearchOwnerRequiresAuthenticatedUser ensures indexing cannot run
// without a stable owner identity to attribute rows to.
func TestSubtitleSearchOwnerRequiresAuthenticatedUser(t *testing.T) {
	if _, err := subtitleSearchOwner(context.Background()); err == nil {
		t.Error("expected an error without a user")
	}
	if _, err := subtitleSearchOwner(ContextWithUser(context.Background(), User{Uuid: "   "})); err == nil {
		t.Error("expected an error for a user without an id")
	}
	owner, err := subtitleSearchOwner(ContextWithUser(context.Background(), User{Uuid: " user-1 "}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if owner != "user-1" {
		t.Errorf("owner = %q, want the trimmed uuid", owner)
	}
}
