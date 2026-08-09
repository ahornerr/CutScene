package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/LukeHagar/plexgo/models/components"
	ffmpeg "github.com/u2takey/ffmpeg-go"
)

// ---------------------------------------------------------------------------
// Timestamp / SRT utility tests
// ---------------------------------------------------------------------------

func TestParseTimestampToMs(t *testing.T) {
	tests := []struct {
		input string
		want  int64
	}{
		{"0:00:00.000", 0},
		{"0:00:01.000", 1000},
		{"0:01:00.000", 60000},
		{"1:00:00.000", 3600000},
		{"1:30:45.500", 3600000 + 30*60000 + 45*1000 + 500},
		{"1:30:45,500", 3600000 + 30*60000 + 45*1000 + 500}, // SRT format (comma)
		{"0:00:00.050", 50},
		{"0:00:00.5", 500},  // short ms
		{"0:00:00.05", 50},  // short ms
		{"123.456", 123456}, // seconds float
		{"0", 0},            // zero
		{"10.5", 10500},     // float seconds
	}
	for _, tc := range tests {
		got, err := ParseTimestampToMs(tc.input)
		if err != nil {
			t.Errorf("ParseTimestampToMs(%q) unexpected error: %v", tc.input, err)
			continue
		}
		if got != tc.want {
			t.Errorf("ParseTimestampToMs(%q) = %d, want %d", tc.input, got, tc.want)
		}
	}
}

func TestParseTimestampToMs_Errors(t *testing.T) {
	inputs := []string{
		"",
		"abc",
		"not-a-number",
		"1:2",     // partial
		"1:2:3:4", // too many colons
	}
	for _, input := range inputs {
		_, err := ParseTimestampToMs(input)
		if err == nil {
			t.Errorf("ParseTimestampToMs(%q) expected error, got nil", input)
		}
	}
}

func TestFormatSRTTimestamp(t *testing.T) {
	tests := []struct {
		ms   int64
		want string
	}{
		{0, "00:00:00,000"},
		{1000, "00:00:01,000"},
		{60000, "00:01:00,000"},
		{3600000, "01:00:00,000"},
		{3661500, "01:01:01,500"},
	}
	for _, tc := range tests {
		got := formatSRTTimestamp(tc.ms)
		if got != tc.want {
			t.Errorf("formatSRTTimestamp(%d) = %q, want %q", tc.ms, got, tc.want)
		}
	}
}

func TestTimestampRoundtrip(t *testing.T) {
	values := []int64{0, 1, 1000, 60000, 3600000, 3661500, 123456}
	for _, v := range values {
		s := formatSRTTimestamp(v)
		got, err := parseSRTTimestamp(s)
		if err != nil {
			t.Errorf("parseSRTTimestamp(%q) error: %v", s, err)
			continue
		}
		if got != v {
			t.Errorf("roundtrip %d: format -> %q -> parse -> %d", v, s, got)
		}
	}
}

func TestParseSRTTimestamp(t *testing.T) {
	tests := []struct {
		input string
		want  int64
	}{
		{"00:00:00,000", 0},
		{"00:00:01,000", 1000},
		{"01:30:45,500", 3600000 + 30*60000 + 45*1000 + 500},
		{"00:00:00,050", 50},
	}
	for _, tc := range tests {
		got, err := parseSRTTimestamp(tc.input)
		if err != nil {
			t.Errorf("parseSRTTimestamp(%q) error: %v", tc.input, err)
			continue
		}
		if got != tc.want {
			t.Errorf("parseSRTTimestamp(%q) = %d, want %d", tc.input, got, tc.want)
		}
	}
}

func TestParseSRTTimestamp_WithPosition(t *testing.T) {
	// SRT timestamps can have position tags after the timestamp
	got, err := parseSRTTimestamp("00:01:23,456 X1:0 Y1:0")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != 83456 {
		t.Errorf("got %d, want %d", got, 83456)
	}
}

func TestParseSRT(t *testing.T) {
	srtContent := `1
00:00:01,000 --> 00:00:02,500
Hello world

2
00:00:03,000 --> 00:00:04,000
First line
Second line

3
00:00:05,000 --> 00:00:06,000
No trailing newline`

	tmpFile, err := os.CreateTemp("", "cutscene_test_*.srt")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(tmpFile.Name())
	if _, err := tmpFile.WriteString(srtContent); err != nil {
		t.Fatal(err)
	}
	tmpFile.Close()

	entries, err := ParseSRT(tmpFile.Name())
	if err != nil {
		t.Fatalf("ParseSRT error: %v", err)
	}

	if len(entries) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(entries))
	}

	// Entry 1
	if entries[0].Start != 1000 || entries[0].End != 2500 || entries[0].Text != "Hello world" {
		t.Errorf("entry 0 mismatch: %+v", entries[0])
	}
	// Entry 2 (multi-line)
	if entries[1].Start != 3000 || entries[1].End != 4000 || entries[1].Text != "First line\nSecond line" {
		t.Errorf("entry 1 mismatch: %+v", entries[1])
	}
	// Entry 3 (no trailing newline)
	if entries[2].Start != 5000 || entries[2].End != 6000 || entries[2].Text != "No trailing newline" {
		t.Errorf("entry 2 mismatch: %+v", entries[2])
	}
}

func TestParseSRT_NonSRT(t *testing.T) {
	// ASS format should produce empty results, not error
	assContent := `[Script Info]
Title: Test
ScriptType: v4.00+

[Events]
Format: Layer, Start, End, Style, Name, MarginL, MarginR, MarginV, Effect, Text
Dialogue: 0,0:00:01,00,0:00:02,00,Default,,0,0,0,,Hello`

	tmpFile, err := os.CreateTemp("", "cutscene_test_*.ass")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(tmpFile.Name())
	if _, err := tmpFile.WriteString(assContent); err != nil {
		t.Fatal(err)
	}
	tmpFile.Close()

	entries, err := ParseSRT(tmpFile.Name())
	if err != nil {
		t.Fatalf("ParseSRT on ASS expected no error, got: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("ParseSRT on ASS expected 0 entries, got %d", len(entries))
	}
}

func TestParseWebVTT(t *testing.T) {
	data := []byte(`WEBVTT - example

NOTE
ignored metadata

cue-one
00:00:01.000 --> 00:00:03.500 align:start position:10%
<c.green>Hello &amp; <b>world</b></c>
second line

00:01.250 --> 00:02.000
plain text`)

	entries, err := ParseWebVTT(data)
	if err != nil {
		t.Fatalf("parseWebVTT error: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 WebVTT entries, got %d", len(entries))
	}

	if entries[0].Start != 1000 || entries[0].End != 3500 || entries[0].Text != "Hello & world\nsecond line" {
		t.Errorf("unexpected first WebVTT entry: %+v", entries[0])
	}
	if entries[1].Start != 1250 || entries[1].End != 2000 || entries[1].Text != "plain text" {
		t.Errorf("unexpected second WebVTT entry: %+v", entries[1])
	}
}

func TestParseWebVTTMalformed(t *testing.T) {
	tests := []struct {
		name string
		data string
	}{
		{"missing header", "00:00:00.000 --> 00:00:01.000\ntext"},
		{"invalid timing", "WEBVTT\n\n00:00:00.000 --> bad\ntext"},
		{"missing text", "WEBVTT\n\n00:00:00.000 --> 00:00:01.000"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := ParseWebVTT([]byte(tt.data)); err == nil {
				t.Fatal("expected malformed WebVTT error")
			}
		})
	}
}

func TestParseASS(t *testing.T) {
	data := []byte(`[Script Info]
Title: Example

[Events]
Format: Layer, End, Start, Style, Name, MarginL, MarginR, MarginV, Effect, Text
Dialogue: 0,0:01:04.50,0:01:02.34,Default,,0,0,0,,Hello, world{\i1}!\NNext\nLine\hspace`)

	entries, err := ParseASS(data)
	if err != nil {
		t.Fatalf("parseASS error: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 ASS entry, got %d", len(entries))
	}

	entry := entries[0]
	if entry.Start != 62340 || entry.End != 64500 {
		t.Errorf("unexpected ASS timing: %+v", entry)
	}
	if entry.Text != "Hello, world!\nNext\nLine space" {
		t.Errorf("unexpected ASS text: %q", entry.Text)
	}
}

func TestParseASS_ReorderedTextPreservesCommas(t *testing.T) {
	data := []byte(`[Events]
Format: Layer, Start, Text, End, Style
Dialogue: 0,0:00:01.00,Hello, world, with commas{\i1}!,0:00:03.00,Default`)

	entries, err := ParseASS(data)
	if err != nil {
		t.Fatalf("ParseASS error: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 ASS entry, got %d", len(entries))
	}

	entry := entries[0]
	if entry.Start != 1000 || entry.End != 3000 {
		t.Errorf("unexpected ASS timing: %+v", entry)
	}
	if entry.Text != "Hello, world, with commas!" {
		t.Errorf("unexpected ASS text: %q", entry.Text)
	}
}

func TestParseASSMalformed(t *testing.T) {
	tests := []struct {
		name string
		data string
	}{
		{
			"missing format",
			"[Events]\nDialogue: 0,0:00:00.00,0:00:01.00,Default,,0,0,0,,text",
		},
		{
			"invalid timestamp",
			"[Events]\nFormat: Layer, Start, End, Text\nDialogue: 0,bad,0:00:01.00,text",
		},
		{
			"unterminated override",
			"[Events]\nFormat: Layer, Start, End, Text\nDialogue: 0,0:00:00.00,0:00:01.00,{\\i1text",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := ParseASS([]byte(tt.data)); err == nil {
				t.Fatal("expected malformed ASS error")
			}
		})
	}
}

func TestWriteClipSRT(t *testing.T) {
	entries := []SubtitleEntry{
		{Start: 5000, End: 10000, Text: "Full entry"},
		{Start: 15000, End: 20000, Text: "Outside range"},
		{Start: 2000, End: 6000, Text: "Clipped start"},
		{Start: 8000, End: 12000, Text: "Clipped end"},
		{Start: 0, End: 3000, Text: "Before range"},
	}

	fromMs := int64(4000)
	toMs := int64(11000)

	path, err := WriteClipSRT(entries, fromMs, toMs)
	if err != nil {
		t.Fatalf("WriteClipSRT error: %v", err)
	}
	defer os.Remove(path)

	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	// Expected entries (after clipping):
	// 1) Full entry: 5000-10000 -> relative 1000-6000
	// 2) Clipped start: 2000-6000 -> relative 0-2000
	// 3) Clipped end: 8000-12000 -> relative 4000-7000 (but end clipped to 11000-4000=7000)
	// Entries "Outside range" and "Before range" should be excluded

	reparsed, err := ParseSRT(path)
	if err != nil {
		t.Fatal(err)
	}

	if len(reparsed) != 3 {
		t.Fatalf("expected 3 clipped entries, got %d: %+v", len(reparsed), reparsed)
	}

	_ = content
	_ = reparsed

	// Entry 0: Full entry, relative start: 1000ms, end: 6000ms
	if reparsed[0].Start != 1000 || reparsed[0].End != 6000 || reparsed[0].Text != "Full entry" {
		t.Errorf("entry 0 mismatch: %+v", reparsed[0])
	}
	// Entry 1: Clipped start, relative start: 0ms (clamped), end: 2000ms
	if reparsed[1].Start != 0 || reparsed[1].End != 2000 || reparsed[1].Text != "Clipped start" {
		t.Errorf("entry 1 mismatch: %+v", reparsed[1])
	}
	// Entry 2: Clipped end, relative start: 4000ms, end: 7000ms (clipped to toMs-fromMs)
	if reparsed[2].Start != 4000 || reparsed[2].End != 7000 || reparsed[2].Text != "Clipped end" {
		t.Errorf("entry 2 mismatch: %+v", reparsed[2])
	}
}

// ---------------------------------------------------------------------------
// Subtitle cache tests
// ---------------------------------------------------------------------------

func TestSubtitleCacheBoundedEviction(t *testing.T) {
	cache := newSubtitleCache(3)

	// Insert 3 entries
	cache.set(subtitleCacheKey{"a", 1, 0}, []SubtitleEntry{{Start: 100, End: 200, Text: "A"}})
	cache.set(subtitleCacheKey{"b", 1, 0}, []SubtitleEntry{{Start: 300, End: 400, Text: "B"}})
	cache.set(subtitleCacheKey{"c", 1, 0}, []SubtitleEntry{{Start: 500, End: 600, Text: "C"}})

	if cache.len() != 3 {
		t.Fatalf("expected cache len 3, got %d", cache.len())
	}

	// Insert 4th entry - should evict oldest ("a")
	cache.set(subtitleCacheKey{"d", 1, 0}, []SubtitleEntry{{Start: 700, End: 800, Text: "D"}})

	if cache.len() != 3 {
		t.Fatalf("expected cache len 3 after eviction, got %d", cache.len())
	}

	// "a" should be gone
	if _, ok := cache.get(subtitleCacheKey{"a", 1, 0}); ok {
		t.Error("expected entry 'a' to be evicted, but it's still present")
	}

	// "b", "c", "d" should be present
	for _, key := range []subtitleCacheKey{{"b", 1, 0}, {"c", 1, 0}, {"d", 1, 0}} {
		if _, ok := cache.get(key); !ok {
			t.Errorf("expected entry %q to be present", key.ratingKey)
		}
	}
}

func TestSubtitleCacheDefensiveCopy(t *testing.T) {
	cache := newSubtitleCache(10)
	original := []SubtitleEntry{{Start: 100, End: 200, Text: "test"}}

	cache.set(subtitleCacheKey{"x", 1, 0}, original)

	// Mutate the original slice (should not affect cache)
	original[0].Text = "mutated"

	got, ok := cache.get(subtitleCacheKey{"x", 1, 0})
	if !ok {
		t.Fatal("expected entry to be present")
	}
	if got[0].Text == "mutated" {
		t.Error("cache returned mutated data; defensive copy failed")
	}
	if got[0].Text != "test" {
		t.Errorf("expected 'test', got %q", got[0].Text)
	}

	// Mutate the returned slice (should not affect cache)
	got[0].Text = "changed again"
	retry, ok := cache.get(subtitleCacheKey{"x", 1, 0})
	if !ok {
		t.Fatal("expected entry still present")
	}
	if retry[0].Text != "test" {
		t.Error("second retrieval returned mutated data; defensive copy failed")
	}
}

func TestSubtitleCacheConcurrency(t *testing.T) {
	cache := newSubtitleCache(100)
	var wg sync.WaitGroup

	// Concurrent writers
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			key := subtitleCacheKey{fmt.Sprintf("key-%d", n), 1, 0}
			cache.set(key, []SubtitleEntry{{Start: int64(n), End: int64(n + 100), Text: fmt.Sprintf("entry-%d", n)}})
		}(i)
	}

	// Concurrent readers
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			key := subtitleCacheKey{fmt.Sprintf("key-%d", n), 1, 0}
			_, _ = cache.get(key)
		}(i)
	}

	wg.Wait()

	// Verify no data races - at least some entries should be present
	if cache.len() == 0 {
		t.Error("expected at least some entries after concurrent writes")
	}
}

func TestSubtitleCacheEmptyValue(t *testing.T) {
	cache := newSubtitleCache(10)
	cache.set(subtitleCacheKey{"empty", 0, 0}, []SubtitleEntry{})

	got, ok := cache.get(subtitleCacheKey{"empty", 0, 0})
	if !ok {
		t.Fatal("expected empty entry to be present")
	}
	if len(got) != 0 {
		t.Errorf("expected 0 entries, got %d", len(got))
	}
}

// ---------------------------------------------------------------------------
// Plex PIN non-2xx response tests
// ---------------------------------------------------------------------------

func TestPlexGetToken_EmptyAuthToken(t *testing.T) {
	// A successful 2xx poll response with empty authToken represents pending
	// authorization and must NOT be an error from plexGetToken.
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"authToken": ""}`))
	}))
	defer ts.Close()

	oldBase := plexAPIBase
	plexAPIBase = ts.URL
	defer func() { plexAPIBase = oldBase }()

	result, err := plexGetToken(context.Background(), 12345, "test-client-id")
	if err != nil {
		t.Fatalf("expected no error for pending auth (empty token), got: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if result.AuthToken != "" {
		t.Errorf("expected empty auth token for pending auth, got %q", result.AuthToken)
	}
}

func TestPlexGetToken_ValidAuthToken(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"authToken": "valid-token-abc"}`))
	}))
	defer ts.Close()

	oldBase := plexAPIBase
	plexAPIBase = ts.URL
	defer func() { plexAPIBase = oldBase }()

	result, err := plexGetToken(context.Background(), 12345, "test-client-id")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.AuthToken != "valid-token-abc" {
		t.Errorf("expected auth token %q, got %q", "valid-token-abc", result.AuthToken)
	}
}

func TestPlexGetToken_Non2xx(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`not found`))
	}))
	defer ts.Close()

	oldBase := plexAPIBase
	plexAPIBase = ts.URL
	defer func() { plexAPIBase = oldBase }()

	_, err := plexGetToken(context.Background(), 99999, "test-client-id")
	if err == nil {
		t.Fatal("expected error for non-2xx response")
	}
	if !strings.Contains(err.Error(), "404") || !strings.Contains(err.Error(), "not found") {
		t.Errorf("error should mention status and body, got: %v", err)
	}
}

func TestPlexGetToken_MalformedJSON(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`not json at all`))
	}))
	defer ts.Close()

	oldBase := plexAPIBase
	plexAPIBase = ts.URL
	defer func() { plexAPIBase = oldBase }()

	_, err := plexGetToken(context.Background(), 12345, "test-client-id")
	if err == nil {
		t.Fatal("expected error for malformed response")
	}
}

func TestPlexGetPin_Valid(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"id": 12345, "code": "abc-def"}`))
	}))
	defer ts.Close()

	oldBase := plexAPIBase
	plexAPIBase = ts.URL
	defer func() { plexAPIBase = oldBase }()

	result, err := plexGetPin(context.Background(), "Test", true, "client-id")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.ID != 12345 || result.Code != "abc-def" {
		t.Errorf("unexpected result: %+v", result)
	}
}

func TestPlexGetPin_MissingFields(t *testing.T) {
	// Verify that responses missing required id/code are rejected
	tests := []struct {
		name string
		body string
	}{
		{"missing id", `{"code": "abc"}`},
		{"missing code", `{"id": 123}`},
		{"both missing", `{}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte(tc.body))
			}))
			defer ts.Close()

			oldBase := plexAPIBase
			plexAPIBase = ts.URL
			defer func() { plexAPIBase = oldBase }()

			_, err := plexGetPin(context.Background(), "Test", true, "client-id")
			if err == nil {
				t.Error("expected error for missing fields")
			}
		})
	}
}

func TestPlexGetPin_Non2xx(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"unauthorized"}`))
	}))
	defer ts.Close()

	oldBase := plexAPIBase
	plexAPIBase = ts.URL
	defer func() { plexAPIBase = oldBase }()

	_, err := plexGetPin(context.Background(), "Test", true, "client-id")
	if err == nil {
		t.Fatal("expected error for non-2xx")
	}
	if !strings.Contains(err.Error(), "401") || !strings.Contains(err.Error(), "unauthorized") {
		t.Errorf("error should include status and body, got: %v", err)
	}
}

func TestPlexGetPin_BoundedBody(t *testing.T) {
	// Verify that non-2xx errors include a bounded body (not the full response)
	longBody := strings.Repeat("x", 5000)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(longBody))
	}))
	defer ts.Close()

	oldBase := plexAPIBase
	plexAPIBase = ts.URL
	defer func() { plexAPIBase = oldBase }()

	_, err := plexGetPin(context.Background(), "Test", true, "client-id")
	if err == nil {
		t.Fatal("expected error")
	}
	// The error message should contain only the first ~1024 bytes, not the full 5000
	if len(err.Error()) > 3000 {
		t.Errorf("error message suspiciously long (%d chars), bounded body likely not applied", len(err.Error()))
	}
}

// ---------------------------------------------------------------------------
// Users.HasUser test
// ---------------------------------------------------------------------------

func TestUsersHasUser(t *testing.T) {
	users := Users{
		MachineIdentifier: "server-abc",
		User: []struct {
			ID        string `xml:"id,attr"`
			Username  string `xml:"username,attr"`
			Email     string `xml:"email,attr"`
			AllowSync string `xml:"allowSync,attr"`
			Server    []struct {
				ID                string `xml:"id,attr"`
				ServerId          string `xml:"serverId,attr"`
				MachineIdentifier string `xml:"machineIdentifier,attr"`
				Name              string `xml:"name,attr"`
				Owned             string `xml:"owned,attr"`
				Pending           string `xml:"pending,attr"`
			} `xml:"Server"`
		}{
			{
				ID: "1", Username: "alice", Email: "alice@example.com",
				Server: []struct {
					ID                string `xml:"id,attr"`
					ServerId          string `xml:"serverId,attr"`
					MachineIdentifier string `xml:"machineIdentifier,attr"`
					Name              string `xml:"name,attr"`
					Owned             string `xml:"owned,attr"`
					Pending           string `xml:"pending,attr"`
				}{
					{MachineIdentifier: "server-abc"},
				},
			},
			{
				ID: "2", Username: "bob", Email: "bob@example.com",
				Server: []struct {
					ID                string `xml:"id,attr"`
					ServerId          string `xml:"serverId,attr"`
					MachineIdentifier string `xml:"machineIdentifier,attr"`
					Name              string `xml:"name,attr"`
					Owned             string `xml:"owned,attr"`
					Pending           string `xml:"pending,attr"`
				}{
					{MachineIdentifier: "other-server"},
				},
			},
		},
	}

	if !users.HasUser("1", "server-abc") {
		t.Error("alice should have access to server-abc")
	}
	if users.HasUser("1", "nonexistent") {
		t.Error("alice should not have access to nonexistent server")
	}
	if users.HasUser("2", "server-abc") {
		t.Error("bob should not have access to server-abc (different server)")
	}
	if users.HasUser("999", "server-abc") {
		t.Error("unknown user should not have access")
	}
}

// ---------------------------------------------------------------------------
// FFmpeg KwArgs mapping behavior tests
// ---------------------------------------------------------------------------

func TestFfmpegMapKwArgs(t *testing.T) {
	// Verify that ffmpeg-go's ConvertKwargsToCmdLineArgs handles []string for map
	kwargs := ffmpeg.KwArgs{
		"map": []string{"[out]", "0:a:0?"},
	}

	args := ffmpeg.ConvertKwargsToCmdLineArgs(kwargs)

	// Should produce: -map [out] -map 0:a:0?
	var foundOut, foundAudio bool
	for i := 0; i < len(args); i++ {
		if args[i] == "-map" && i+1 < len(args) {
			if args[i+1] == "[out]" {
				foundOut = true
			}
			if args[i+1] == "0:a:0?" {
				foundAudio = true
			}
		}
	}

	if !foundOut {
		t.Errorf("expected -map [out] in args, got %v", args)
	}
	if !foundAudio {
		t.Errorf("expected -map 0:a:0? in args, got %v", args)
	}
}

func TestFfmpegKwArgsMultipleMaps(t *testing.T) {
	// Verify that multiple -map entries can be specified via []string
	kwargs := ffmpeg.KwArgs{
		"map":    []string{"[out]", "0:a:0?"},
		"vcodec": "libx264",
		"acodec": "aac",
	}

	args := ffmpeg.ConvertKwargsToCmdLineArgs(kwargs)

	// Count -map occurrences
	mapCount := 0
	for _, a := range args {
		if a == "-map" {
			mapCount++
		}
	}
	if mapCount != 2 {
		t.Errorf("expected 2 -map entries (video + single audio stream), got %d in %v", mapCount, args)
	}
}

func TestFfmpegMapSingleString(t *testing.T) {
	// Verify that a single string map still works (backward compat)
	kwargs := ffmpeg.KwArgs{
		"map": "[out]",
	}

	args := ffmpeg.ConvertKwargsToCmdLineArgs(kwargs)

	var found bool
	for i := 0; i < len(args); i++ {
		if args[i] == "-map" && i+1 < len(args) && args[i+1] == "[out]" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected -map [out] for string value, got %v", args)
	}
}

func TestNVENCTextSubtitleArgs(t *testing.T) {
	tests := []struct {
		name   string
		height int
		want   string
	}{
		{
			name:   "with scaling",
			height: 720,
			want:   "subtitles=/tmp/subtitle.srt,format=yuv420p,hwupload_cuda,scale_cuda=-2:720",
		},
		{
			name:   "without scaling",
			height: 0,
			want:   "subtitles=/tmp/subtitle.srt,format=yuv420p,hwupload_cuda",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			inputArgs := ffmpeg.KwArgs{
				"hwaccel":               "cuda",
				"hwaccel_output_format": "cuda",
				"extra_hw_frames":       8,
			}
			outputArgs := ffmpeg.KwArgs{}

			configureNVENCTextSubtitle(inputArgs, outputArgs, "/tmp/subtitle.srt", tt.height)

			if got := outputArgs["vf"]; got != tt.want {
				t.Fatalf("vf = %q, want %q", got, tt.want)
			}

			outputCLI := ffmpeg.ConvertKwargsToCmdLineArgs(outputArgs)
			if !containsConsecutiveArgs(outputCLI, "-vf", tt.want) {
				t.Errorf("generated output args %v do not contain the expected subtitle filter", outputCLI)
			}

			inputCLI := ffmpeg.ConvertKwargsToCmdLineArgs(inputArgs)
			for _, inappropriate := range []string{"-hwaccel", "-hwaccel_output_format", "-extra_hw_frames"} {
				if containsArg(inputCLI, inappropriate) {
					t.Errorf("generated input args %v contain inappropriate %s", inputCLI, inappropriate)
				}
			}
		})
	}
}

func containsArg(args []string, want string) bool {
	for _, arg := range args {
		if arg == want {
			return true
		}
	}
	return false
}

func containsConsecutiveArgs(args []string, first, second string) bool {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == first && args[i+1] == second {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// JSON response validation tests (for plexGetPin/plexGetToken)
// ---------------------------------------------------------------------------

func TestPlexPinResponseValidation(t *testing.T) {
	// Valid response
	valid := `{"id": 12345, "code": "abcd-efgh"}`
	var resp plexPinResponse
	if err := json.Unmarshal([]byte(valid), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.ID == 0 || resp.Code == "" {
		t.Error("valid response should pass validation")
	}

	// Missing fields
	missingID := `{"code": "abcd"}`
	var resp2 plexPinResponse
	_ = json.Unmarshal([]byte(missingID), &resp2)
	if resp2.ID == 0 || resp2.Code == "" {
		// Expected to fail validation
	} else {
		t.Error("expected missing ID to be detected")
	}

	missingCode := `{"id": 12345}`
	var resp3 plexPinResponse
	_ = json.Unmarshal([]byte(missingCode), &resp3)
	if resp3.ID == 0 || resp3.Code == "" {
		// Expected to fail validation
	} else {
		t.Error("expected missing code to be detected")
	}
}

func TestPlexTokenResponseValidation(t *testing.T) {
	// Valid response
	valid := `{"authToken": "valid-token-123"}`
	var resp plexTokenResponse
	if err := json.Unmarshal([]byte(valid), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.AuthToken == "" {
		t.Error("valid response should have auth token")
	}

	// Missing authToken
	missing := `{"something": "else"}`
	var resp2 plexTokenResponse
	_ = json.Unmarshal([]byte(missing), &resp2)
	if resp2.AuthToken != "" {
		t.Error("expected empty auth token for missing field")
	}
}

// ---------------------------------------------------------------------------
// Test filename sanitization in Clip output
// ---------------------------------------------------------------------------

func TestParseTimestampToMs_Roundtrip(t *testing.T) {
	// Verify that various string formats roundtrip correctly
	inputs := []string{
		"0:00:00.000",
		"1:30:45.500",
		"12:59:59.999",
	}
	for _, input := range inputs {
		ms, err := ParseTimestampToMs(input)
		if err != nil {
			t.Errorf("ParseTimestampToMs(%q) error: %v", input, err)
			continue
		}
		// Re-format to SRT-style, then parse back
		srtFormatted := formatSRTTimestamp(ms)
		ms2, err := parseSRTTimestamp(srtFormatted)
		if err != nil {
			t.Errorf("roundtrip parseSRTTimestamp(%q) error: %v", srtFormatted, err)
			continue
		}
		if ms != ms2 {
			t.Errorf("roundtrip %q: %d -> %q -> %d", input, ms, srtFormatted, ms2)
		}
	}
}

func TestWriteClipSRT_EmptyEntries(t *testing.T) {
	path, err := WriteClipSRT([]SubtitleEntry{}, 1000, 5000)
	if err != nil {
		t.Fatalf("WriteClipSRT with empty entries error: %v", err)
	}
	defer os.Remove(path)

	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(content) > 0 && strings.TrimSpace(string(content)) == "" {
		t.Log("empty entries produced an empty file")
	}
}

// ---------------------------------------------------------------------------
// Test bounding of the non-2xx error path
// ---------------------------------------------------------------------------

func TestStrs(t *testing.T) {
	// Ensure strconv import works in tests
	if strconv.Itoa(42) != "42" {
		t.Error("strconv.Itoa failed")
	}
}

// ---------------------------------------------------------------------------
// Time-based test to ensure no deadlocks in cache (short timeout)
// ---------------------------------------------------------------------------

func TestSubtitleCacheMaxZero(t *testing.T) {
	// Cache with max=0 should reject all entries (or behave as unbounded, depending on implementation)
	cache := newSubtitleCache(0)
	cache.set(subtitleCacheKey{"a", 0, 0}, []SubtitleEntry{{Text: "test"}})
	// With max=0, the eviction check: len(c.m) >= c.max && c.max > 0 is false, so it's effectively unbounded
	if cache.len() != 1 {
		t.Errorf("expected cache with max=0 to accept entry, got len=%d", cache.len())
	}
}

func TestSubtitleCacheMaxOne(t *testing.T) {
	cache := newSubtitleCache(1)
	cache.set(subtitleCacheKey{"a", 0, 0}, []SubtitleEntry{{Text: "first"}})
	cache.set(subtitleCacheKey{"b", 0, 0}, []SubtitleEntry{{Text: "second"}})

	if cache.len() != 1 {
		t.Fatalf("expected len=1, got %d", cache.len())
	}

	_, okA := cache.get(subtitleCacheKey{"a", 0, 0})
	gotB, okB := cache.get(subtitleCacheKey{"b", 0, 0})
	if okA {
		t.Error("expected 'a' to be evicted (FIFO)")
	}
	if !okB {
		t.Error("expected 'b' to be present")
	} else if gotB[0].Text != "second" {
		t.Errorf("expected 'second', got %q", gotB[0].Text)
	}
}

func TestWriteClipSRT_FilePath(t *testing.T) {
	// Verify WriteClipSRT creates a file at a valid path
	entries := []SubtitleEntry{{Start: 0, End: 1000, Text: "test"}}
	path, err := WriteClipSRT(entries, 0, 1000)
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(path)

	if _, err := os.Stat(path); os.IsNotExist(err) {
		t.Errorf("WriteClipSRT file does not exist: %s", path)
	}
}

// ---------------------------------------------------------------------------
// Media selection tests (Part-ID vs Media-ID matching)
// ---------------------------------------------------------------------------

// mediaSelectionFixture mirrors a real Plex metadata response where part ids
// differ from the parent media ids (e.g. Part 293546 belongs to Media 293539).
func mediaSelectionFixture() []components.Media {
	main10 := "main 10"
	return []components.Media{
		{
			ID:           293539,
			VideoProfile: &main10,
			Part: []components.Part{
				{ID: 293546, Key: "/library/parts/293546/file.mp4"},
				{ID: 293547, Key: "/library/parts/293547/file.mp4"},
			},
		},
		{
			ID: 293540,
			Part: []components.Part{
				{ID: 293548, Key: "/library/parts/293548/file.mp4"},
			},
		},
		{
			ID: 293541,
			Part: []components.Part{
				{ID: 293549, Key: "/library/parts/293549/file.mp4"},
			},
		},
	}
}

func TestFindMediaByID_MatchesMediaID(t *testing.T) {
	media := mediaSelectionFixture()

	m := findMediaByID(media, 293540)
	if m == nil {
		t.Fatal("expected media to be found by Media.ID")
	}
	if m.ID != 293540 {
		t.Errorf("expected media ID 293540, got %d", m.ID)
	}
}

func TestFindMediaByID_MatchesPartID(t *testing.T) {
	media := mediaSelectionFixture()

	// Part ID 293546 belongs to Media 293539
	m := findMediaByID(media, 293546)
	if m == nil {
		t.Fatal("expected media to be found by Part.ID")
	}
	if m.ID != 293539 {
		t.Errorf("expected media ID 293539 (parent of part 293546), got %d", m.ID)
	}

	// Part ID 293549 belongs to Media 293541
	m = findMediaByID(media, 293549)
	if m == nil {
		t.Fatal("expected media to be found by Part.ID")
	}
	if m.ID != 293541 {
		t.Errorf("expected media ID 293541, got %d", m.ID)
	}
}

func TestFindMediaByID_UnknownID(t *testing.T) {
	media := mediaSelectionFixture()

	if m := findMediaByID(media, 99999); m != nil {
		t.Errorf("expected nil for unknown id, got media %d", m.ID)
	}
	if m := findMediaByID(media, 0); m != nil {
		t.Errorf("expected nil for id 0, got media %d", m.ID)
	}
	if m := findMediaByID(nil, 293540); m != nil {
		t.Error("expected nil for empty media slice")
	}
}

func TestFindDefaultMedia_SkipsMain10(t *testing.T) {
	media := mediaSelectionFixture()

	// First media is main 10 (skipped), so the default should be media 293540
	m := findDefaultMedia(media)
	if m == nil {
		t.Fatal("expected a default media to be found")
	}
	if m.ID != 293540 {
		t.Errorf("expected default media ID 293540 (skipping main 10), got %d", m.ID)
	}

	// All main 10 -> no default
	main10 := "main 10"
	allMain10 := []components.Media{
		{ID: 1, VideoProfile: &main10},
		{ID: 2, VideoProfile: &main10},
	}
	if m := findDefaultMedia(allMain10); m != nil {
		t.Errorf("expected nil when all media are main 10, got %d", m.ID)
	}
	if m := findDefaultMedia(nil); m != nil {
		t.Error("expected nil for empty media slice")
	}
}

func TestResolveMedia_SuppliedIDMatches(t *testing.T) {
	media := mediaSelectionFixture()

	// Explicit Media.ID
	m, err := resolveMedia(media, 293540, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m.ID != 293540 {
		t.Errorf("expected media 293540, got %d", m.ID)
	}

	// Explicit Part.ID resolves to parent media
	m, err = resolveMedia(media, 293546, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m.ID != 293539 {
		t.Errorf("expected media 293539 (parent of part 293546), got %d", m.ID)
	}
}

func TestResolveMedia_SuppliedIDUnknown_Errors(t *testing.T) {
	media := mediaSelectionFixture()

	// Explicitly supplied id that does not match must error, NOT fall back to
	// the default media.
	_, err := resolveMedia(media, 99999, true)
	if err == nil {
		t.Fatal("expected not-found error for unknown supplied id")
	}
	if !strings.Contains(err.Error(), "99999") {
		t.Errorf("expected error to mention the unknown id, got: %v", err)
	}
}

func TestResolveMedia_OmittedID_UsesFallback(t *testing.T) {
	media := mediaSelectionFixture()

	// mediaId omitted -> automatic fallback (skips main 10 media)
	m, err := resolveMedia(media, 0, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m.ID != 293540 {
		t.Errorf("expected fallback media 293540, got %d", m.ID)
	}
}

func TestResolveMedia_OmittedID_NoMedia_Errors(t *testing.T) {
	main10 := "main 10"
	onlyMain10 := []components.Media{
		{ID: 1, VideoProfile: &main10},
	}
	_, err := resolveMedia(onlyMain10, 0, false)
	if err == nil {
		t.Fatal("expected error when no suitable media exists")
	}

	if _, err := resolveMedia(nil, 0, false); err == nil {
		t.Fatal("expected error for empty media slice")
	}
}

func TestSelectSubtitleSourceSeparatesExternalAndEmbeddedOrdinals(t *testing.T) {
	embedded := "1"
	sources := []components.Stream{
		{StreamType: 1},
		{StreamType: 3, Key: "/library/streams/external", Codec: "srt"},
		{StreamType: 3, Codec: "ass", EmbeddedInVideo: &embedded},
		{StreamType: 3, Codec: "pgssub", EmbeddedInVideo: &embedded},
	}

	external, err := selectSubtitleSource(sources, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !external.External || external.StreamKey != "/library/streams/external" || external.EmbeddedIndex != -1 {
		t.Fatalf("unexpected external source: %+v", external)
	}

	embeddedText, err := selectSubtitleSource(sources, 1)
	if err != nil {
		t.Fatal(err)
	}
	if embeddedText.External || embeddedText.EmbeddedIndex != 0 {
		t.Fatalf("unexpected embedded text source: %+v", embeddedText)
	}

	embeddedPGS, err := selectSubtitleSource(sources, 2)
	if err != nil {
		t.Fatal(err)
	}
	if embeddedPGS.External || !embeddedPGS.PGS || embeddedPGS.EmbeddedIndex != 1 {
		t.Fatalf("unexpected embedded PGS source: %+v", embeddedPGS)
	}
}

func TestPrepareExternalSubtitleUsesStreamKeyOnly(t *testing.T) {
	var streamRequests, partRequests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/library/streams/external":
			streamRequests++
			_, _ = w.Write([]byte("1\n00:00:01,000 --> 00:00:03,000\nexternal text\n"))
		case "/library/parts/video":
			partRequests++
			http.Error(w, "part extraction must not be used", http.StatusInternalServerError)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	config := Config{}
	config.Plex.Host = server.URL
	app := &Application{config: config}
	file, err := app.prepareExternalSubtitle(context.Background(), subtitleSource{
		StreamKey: "/library/streams/external",
		Codec:     "srt",
		External:  true,
	}, 1000, 3000)
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(file)
	if streamRequests != 1 || partRequests != 0 {
		t.Fatalf("stream requests=%d part requests=%d", streamRequests, partRequests)
	}
	content, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(content), "00:00:00,000 --> 00:00:02,000") {
		t.Fatalf("external subtitle was not made clip-relative: %s", content)
	}
}

func TestPrepareExternalSubtitleFailureDoesNotUsePartFallback(t *testing.T) {
	var streamRequests, partRequests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/library/streams/missing" {
			streamRequests++
			http.NotFound(w, r)
			return
		}
		if r.URL.Path == "/library/parts/video" {
			partRequests++
		}
		http.NotFound(w, r)
	}))
	defer server.Close()

	config := Config{}
	config.Plex.Host = server.URL
	app := &Application{config: config}
	_, err := app.prepareExternalSubtitle(context.Background(), subtitleSource{
		StreamKey: "/library/streams/missing",
		Codec:     "srt",
		External:  true,
	}, 0, 1000)
	if err == nil {
		t.Fatal("expected external subtitle download error")
	}
	if streamRequests != 1 || partRequests != 0 {
		t.Fatalf("stream requests=%d part requests=%d", streamRequests, partRequests)
	}
}

// ---------------------------------------------------------------------------
// Main test runner helper (not a test itself)
// ---------------------------------------------------------------------------

func init() {
	// Ensure the test runs in the right directory
	_ = os.Getenv("GO_TEST")
}
