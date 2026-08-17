<?php




// ---------------------------------------------------------------------------
// Timestamp / SRT utility tests
// ---------------------------------------------------------------------------

function TestParseTimestampToMs($$t->T) {$tests = []struct {
		input string
		want  int64
	}{
		{"0:00:$00->000", 0},
		{"0:00:$01->000", 1000},
		{"0:01:$00->000", 60000},
		{"1:00:$00->000", 3600000},
		{"1:30:$45->500", 3600000 + 30*60000 + 45*1000 + 500},
		{"1:30:45,500", 3600000 + 30*60000 + 45*1000 + 500}, // SRT format (comma)
		{"0:00:$00->050", 50},
		{"0:00:$00->5", 500},  // short ms
		{"0:00:$00->05", 50},  // short ms
		{"$123->456", 123456}, // seconds float
		{"0", 0},            // zero
		{"$10->5", 10500},     // float seconds
	}list($for, $_, $tc) = range tests {list($got, $err) = ParseTimestampToMs($tc->input)
		if $err !== null {
			$t->Errorf("ParseTimestampToMs(%q) unexpected error: %v", $tc->input, err)
			continue
		}
		if got != $tc->want {
			$t->Errorf("ParseTimestampToMs(%q) = %d, want %d", $tc->input, got, $tc->want)
		}
	}
}

function TestParseTimestampToMs_Errors($$t->T) {$inputs = []string{
		"",
		"abc",
		"not-a-number",
		"1:2",     // partial
		"1:2:3:4", // too many colons
	}list($for, $_, $input) = range inputs {list($_, $err) = ParseTimestampToMs(input)
		if err == null {
			$t->Errorf("ParseTimestampToMs(%q) expected error, got null", input)
		}
	}
}

function TestFormatSRTTimestamp($$t->T) {$tests = []struct {
		ms   int64
		want string
	}{
		{0, "00:00:00,000"},
		{1000, "00:00:01,000"},
		{60000, "00:01:00,000"},
		{3600000, "01:00:00,000"},
		{3661500, "01:01:01,500"},
	}list($for, $_, $tc) = range tests {$got = formatSRTTimestamp($tc->ms)
		if got != $tc->want {
			$t->Errorf("formatSRTTimestamp(%d) = %q, want %q", $tc->ms, got, $tc->want)
		}
	}
}

function TestTimestampRoundtrip($$t->T) {$values = []int64{0, 1, 1000, 60000, 3600000, 3661500, 123456}list($for, $_, $v) = range values {$s = formatSRTTimestamp(v)list($got, $err) = parseSRTTimestamp(s)
		if $err !== null {
			$t->Errorf("parseSRTTimestamp(%q) error: %v", s, err)
			continue
		}
		if got != v {
			$t->Errorf("roundtrip %d: format -> %q -> parse -> %d", v, s, got)
		}
	}
}

function TestParseSRTTimestamp($$t->T) {$tests = []struct {
		input string
		want  int64
	}{
		{"00:00:00,000", 0},
		{"00:00:01,000", 1000},
		{"01:30:45,500", 3600000 + 30*60000 + 45*1000 + 500},
		{"00:00:00,050", 50},
	}list($for, $_, $tc) = range tests {list($got, $err) = parseSRTTimestamp($tc->input)
		if $err !== null {
			$t->Errorf("parseSRTTimestamp(%q) error: %v", $tc->input, err)
			continue
		}
		if got != $tc->want {
			$t->Errorf("parseSRTTimestamp(%q) = %d, want %d", $tc->input, got, $tc->want)
		}
	}
}

function TestParseSRTTimestamp_WithPosition($$t->T) {
	//list($SRT, $timestamps, $can, $have, $position, $tags, $after, $the, $timestamp, $got, $err) = parseSRTTimestamp("00:01:23,456 X1:0 Y1:0")
	if $err !== null {
		$t->Fatalf("unexpected error: %v", err)
	}
	if got != 83456 {
		$t->Errorf("got %d, want %d", got, 83456)
	}
}

function TestParseSRT($$t->T) {$srtContent = `1
00:00:01,000 --> 00:00:02,500
Hello world

2
00:00:03,000 --> 00:00:04,000
First line
Second line

3
00:00:05,000 --> 00:00:06,000
No trailing newline`list($tmpFile, $err) = $os->CreateTemp("", "cutscene_test_*.srt")
	if $err !== null {
		$t->Fatal(err)
	}
	defer $os->Remove($tmpFile->Name())list($if, $_, $err) = $tmpFile->WriteString(srtContent); $err !== null {
		$t->Fatal(err)
	}
	$tmpFile->Close()list($entries, $err) = ParseSRT($tmpFile->Name())
	if $err !== null {
		$t->Fatalf("ParseSRT error: %v", err)
	}

	if len(entries) != 3 {
		$t->Fatalf("expected 3 entries, got %d", len(entries))
	}

	// Entry 1
	if entries[0].Start != 1000 || entries[0].End != 2500 || entries[0].Text != "Hello world" {
		$t->Errorf("entry 0 mismatch: %+v", entries[0])
	}
	// Entry 2 (multi-line)
	if entries[1].Start != 3000 || entries[1].End != 4000 || entries[1].Text != "First line\nSecond line" {
		$t->Errorf("entry 1 mismatch: %+v", entries[1])
	}
	// Entry 3 (no trailing newline)
	if entries[2].Start != 5000 || entries[2].End != 6000 || entries[2].Text != "No trailing newline" {
		$t->Errorf("entry 2 mismatch: %+v", entries[2])
	}
}

function TestParseSRT_NonSRT($$t->T) {
	//list($ASS, $format, $should, $produce, $empty, $results, $not, $error, $assContent) = `[Script Info]
Title: Test
ScriptType: $v4->00+

[Events]
Format: Layer, Start, End, Style, Name, MarginL, MarginR, MarginV, Effect, Text
Dialogue: 0,0:00:01,00,0:00:02,00,Default,,0,0,0,,Hello`list($tmpFile, $err) = $os->CreateTemp("", "cutscene_test_*.ass")
	if $err !== null {
		$t->Fatal(err)
	}
	defer $os->Remove($tmpFile->Name())list($if, $_, $err) = $tmpFile->WriteString(assContent); $err !== null {
		$t->Fatal(err)
	}
	$tmpFile->Close()list($entries, $err) = ParseSRT($tmpFile->Name())
	if $err !== null {
		$t->Fatalf("ParseSRT on ASS expected no error, got: %v", err)
	}
	if len(entries) != 0 {
		$t->Errorf("ParseSRT on ASS expected 0 entries, got %d", len(entries))
	}
}

function TestParseWebVTT($$t->T) {$data = []byte(`WEBVTT - example

NOTE
ignored metadata

cue-one
00:00:$01->000 --> 00:00:$03->500 align:start position:10%
<$c->green>Hello &amp; <b>world</b></c>
second line

00:$01->250 --> 00:$02->000
plain text`)list($entries, $err) = ParseWebVTT(data)
	if $err !== null {
		$t->Fatalf("parseWebVTT error: %v", err)
	}
	if len(entries) != 2 {
		$t->Fatalf("expected 2 WebVTT entries, got %d", len(entries))
	}

	if entries[0].Start != 1000 || entries[0].End != 3500 || entries[0].Text != "Hello & world\nsecond line" {
		$t->Errorf("unexpected first WebVTT entry: %+v", entries[0])
	}
	if entries[1].Start != 1250 || entries[1].End != 2000 || entries[1].Text != "plain text" {
		$t->Errorf("unexpected second WebVTT entry: %+v", entries[1])
	}
}

function TestParseWebVTTMalformed($$t->T) {$tests = []struct {
		name string
		data string
	}{
		{"missing header", "00:00:$00->000 --> 00:00:$01->000\ntext"},
		{"invalid timing", "WEBVTT\n\n00:00:$00->000 --> bad\ntext"},
		{"missing text", "WEBVTT\n\n00:00:$00->000 --> 00:00:$01->000"},
	}list($for, $_, $tt) = range tests {
		$t->Run($tt->name, func(t *$testing->T) {list($if, $_, $err) = ParseWebVTT([]byte($tt->data)); err == null {
				$t->Fatal("expected malformed WebVTT error")
			}
		})
	}
}

function TestParseASS($$t->T) {$data = []byte(`[Script Info]
Title: Example

[Events]
Format: Layer, End, Start, Style, Name, MarginL, MarginR, MarginV, Effect, Text
Dialogue: 0,0:01:$04->50,0:01:$02->34,Default,,0,0,0,,Hello, world{\i1}!\NNext\nLine\hspace`)list($entries, $err) = ParseASS(data)
	if $err !== null {
		$t->Fatalf("parseASS error: %v", err)
	}
	if len(entries) != 1 {
		$t->Fatalf("expected 1 ASS entry, got %d", len(entries))
	}$entry = entries[0]
	if $entry->Start != 62340 || $entry->End != 64500 {
		$t->Errorf("unexpected ASS timing: %+v", entry)
	}
	if $entry->Text != "Hello, world!\nNext\nLine space" {
		$t->Errorf("unexpected ASS text: %q", $entry->Text)
	}
}

function TestParseASS_ReorderedTextPreservesCommas($$t->T) {$data = []byte(`[Events]
Format: Layer, Start, Text, End, Style
Dialogue: 0,0:00:$01->00,Hello, world, with commas{\i1}!,0:00:$03->00,Default`)list($entries, $err) = ParseASS(data)
	if $err !== null {
		$t->Fatalf("ParseASS error: %v", err)
	}
	if len(entries) != 1 {
		$t->Fatalf("expected 1 ASS entry, got %d", len(entries))
	}$entry = entries[0]
	if $entry->Start != 1000 || $entry->End != 3000 {
		$t->Errorf("unexpected ASS timing: %+v", entry)
	}
	if $entry->Text != "Hello, world, with commas!" {
		$t->Errorf("unexpected ASS text: %q", $entry->Text)
	}
}

function TestParseASSMalformed($$t->T) {$tests = []struct {
		name string
		data string
	}{
		{
			"missing format",
			"[Events]\nDialogue: 0,0:00:$00->00,0:00:$01->00,Default,,0,0,0,,text",
		},
		{
			"invalid timestamp",
			"[Events]\nFormat: Layer, Start, End, Text\nDialogue: 0,bad,0:00:$01->00,text",
		},
		{
			"unterminated override",
			"[Events]\nFormat: Layer, Start, End, Text\nDialogue: 0,0:00:$00->00,0:00:$01->00,{\\i1text",
		},
	}list($for, $_, $tt) = range tests {
		$t->Run($tt->name, func(t *$testing->T) {list($if, $_, $err) = ParseASS([]byte($tt->data)); err == null {
				$t->Fatal("expected malformed ASS error")
			}
		})
	}
}

function TestWriteClipSRT($$t->T) {$entries = []SubtitleEntry{
		{Start: 5000, End: 10000, Text: "Full entry"},
		{Start: 15000, End: 20000, Text: "Outside range"},
		{Start: 2000, End: 6000, Text: "Clipped start"},
		{Start: 8000, End: 12000, Text: "Clipped end"},
		{Start: 0, End: 3000, Text: "Before range"},
	}$fromMs = int64(4000)$toMs = int64(11000)list($path, $err) = WriteClipSRT(entries, fromMs, toMs)
	if $err !== null {
		$t->Fatalf("WriteClipSRT error: %v", err)
	}
	defer $os->Remove(path)list($content, $err) = $os->ReadFile(path)
	if $err !== null {
		$t->Fatal(err)
	}

	// Expected entries (after clipping):
	// 1) Full entry: 5000-10000 -> relative 1000-6000
	// 2) Clipped start: 2000-6000 -> relative 0-2000
	// 3) Clipped end: 8000-12000 -> relative 4000-7000 (but end clipped to 11000-4000=7000)
	// Entries "Outside range" and "Before range"list($should, $be, $excluded, $reparsed, $err) = ParseSRT(path)
	if $err !== null {
		$t->Fatal(err)
	}

	if len(reparsed) != 3 {
		$t->Fatalf("expected 3 clipped entries, got %d: %+v", len(reparsed), reparsed)
	}

	_ = content
	_ = reparsed

	// Entry 0: Full entry, relative start: 1000ms, end: 6000ms
	if reparsed[0].Start != 1000 || reparsed[0].End != 6000 || reparsed[0].Text != "Full entry" {
		$t->Errorf("entry 0 mismatch: %+v", reparsed[0])
	}
	// Entry 1: Clipped start, relative start: 0ms (clamped), end: 2000ms
	if reparsed[1].Start != 0 || reparsed[1].End != 2000 || reparsed[1].Text != "Clipped start" {
		$t->Errorf("entry 1 mismatch: %+v", reparsed[1])
	}
	// Entry 2: Clipped end, relative start: 4000ms, end: 7000ms (clipped to toMs-fromMs)
	if reparsed[2].Start != 4000 || reparsed[2].End != 7000 || reparsed[2].Text != "Clipped end" {
		$t->Errorf("entry 2 mismatch: %+v", reparsed[2])
	}
}

function TestWriteClipSRTSubtitleOffsetShiftsWithoutMutatingEntries($$t->T) {$entries = []SubtitleEntry{{Start: 1000, End: 3000, Text: "shift me"}}list($positive, $err) = WriteClipSRT(entries, 0, 2500, 500)
	if $err !== null {
		$t->Fatal(err)
	}
	defer $os->Remove(positive)list($positiveEntries, $err) = ParseSRT(positive)
	if $err !== null {
		$t->Fatal(err)
	}
	if len(positiveEntries) != 1 || positiveEntries[0].Start != 1500 || positiveEntries[0].End != 2500 {
		$t->Fatalf("positive offset entries = %+v", positiveEntries)
	}list($negative, $err) = WriteClipSRT(entries, 0, 2500, -500)
	if $err !== null {
		$t->Fatal(err)
	}
	defer $os->Remove(negative)list($negativeEntries, $err) = ParseSRT(negative)
	if $err !== null {
		$t->Fatal(err)
	}
	if len(negativeEntries) != 1 || negativeEntries[0].Start != 500 || negativeEntries[0].End != 2500 {
		$t->Fatalf("negative offset entries = %+v", negativeEntries)
	}
	if entries[0].Start != 1000 || entries[0].End != 3000 {
		$t->Fatalf("source entries were mutated: %+v", entries)
	}
}

function TestSubtitleOffsetParsingAndRange($$t->T) {list($for, $_, $test) = range []struct {
		value string
		want  int64
		valid bool
	}{
		{value: "250", want: 250, valid: true},
		{value: "-250", want: -250, valid: true},
		{value: "", want: 0, valid: true},
		{value: "not-a-number", valid: false},
		{value: "900001", valid: false},
	} {list($got, $err) = parseSubtitleOffsetMs($test->value)
		if $test->valid && ($err !== null || got != $test->want) {
			$t->Errorf("parseSubtitleOffsetMs(%q) = %d, %v; want %d", $test->value, got, err, $test->want)
		}
		if !$test->valid && err == null {
			$t->Errorf("parseSubtitleOffsetMs(%q) unexpectedly succeeded", $test->value)
		}
	}
}

function TestSubtitleOverlayFilterAppliesSignedOffset($$t->T) {list($if, $got) = subtitleOverlayFilter(2, 500, ",scale[out]"); !$strings->Contains(got, "setpts=PTS+500/1000/TB") {
		$t->Fatalf("positive overlay offset filter = %q", got)
	}list($if, $got) = subtitleOverlayFilter(2, -500, ",scale[out]"); !$strings->Contains(got, "setpts=PTS-500/1000/TB") {
		$t->Fatalf("negative overlay offset filter = %q", got)
	}
}

// ---------------------------------------------------------------------------
// Subtitle cache tests
// ---------------------------------------------------------------------------

function TestSubtitleCacheBoundedEviction($$t->T) {$cache = newSubtitleCache(3)

	// Insert 3 entries
	$cache->set(subtitleCacheKey{"a", 1, 0, ""}, []SubtitleEntry{{Start: 100, End: 200, Text: "A"}})
	$cache->set(subtitleCacheKey{"b", 1, 0, ""}, []SubtitleEntry{{Start: 300, End: 400, Text: "B"}})
	$cache->set(subtitleCacheKey{"c", 1, 0, ""}, []SubtitleEntry{{Start: 500, End: 600, Text: "C"}})

	if $cache->len() != 3 {
		$t->Fatalf("expected cache len 3, got %d", $cache->len())
	}

	// Insert 4th entry - should evict oldest ("a")
	$cache->set(subtitleCacheKey{"d", 1, 0, ""}, []SubtitleEntry{{Start: 700, End: 800, Text: "D"}})

	if $cache->len() != 3 {
		$t->Fatalf("expected cache len 3 after eviction, got %d", $cache->len())
	}

	// "a"list($should, $be, $gone, $if, $_, $ok) = $cache->get(subtitleCacheKey{"a", 1, 0, ""}); ok {
		$t->Error("expected entry 'a' to be evicted, but it's still present")
	}

	// "b", "c", "d"list($should, $be, $present, $for, $_, $key) = range []subtitleCacheKey{{"b", 1, 0, ""}, {"c", 1, 0, ""}, {"d", 1, 0, ""}} {list($if, $_, $ok) = $cache->get(key); !ok {
			$t->Errorf("expected entry %q to be present", $key->ratingKey)
		}
	}
}

function TestSubtitleCacheDefensiveCopy($$t->T) {$cache = newSubtitleCache(10)$original = []SubtitleEntry{{Start: 100, End: 200, Text: "test"}}

	$cache->set(subtitleCacheKey{"x", 1, 0, ""}, original)

	// Mutate the original slice (should not affect cache)
	original[0].Text = "mutated"list($got, $ok) = $cache->get(subtitleCacheKey{"x", 1, 0, ""})
	if !ok {
		$t->Fatal("expected entry to be present")
	}
	if got[0].Text == "mutated" {
		$t->Error("cache returned mutated data; defensive copy failed")
	}
	if got[0].Text != "test" {
		$t->Errorf("expected 'test', got %q", got[0].Text)
	}

	// Mutate the returned slice (should not affect cache)
	got[0].Text = "changed again"list($retry, $ok) = $cache->get(subtitleCacheKey{"x", 1, 0, ""})
	if !ok {
		$t->Fatal("expected entry still present")
	}
	if retry[0].Text != "test" {
		$t->Error("second retrieval returned mutated data; defensive copy failed")
	}
}

function TestSubtitleCacheConcurrency($$t->T) {$cache = newSubtitleCache(100)
	$wg = null;.WaitGroup

	//list($Concurrent, $writers, $for, $i) = 0; i < 50; i++ {
		$wg->Add(1)
		go func(n int) {
			defer $wg->Done()$key = subtitleCacheKey{sprintf("key-%d", n), 1, 0, ""}
			$cache->set(key, []SubtitleEntry{{Start: int64(n), End: int64(n + 100), Text: sprintf("entry-%d", n)}})
		}(i)
	}

	//list($Concurrent, $readers, $for, $i) = 0; i < 50; i++ {
		$wg->Add(1)
		go func(n int) {
			defer $wg->Done()$key = subtitleCacheKey{sprintf("key-%d", n), 1, 0, ""}
			_, _ = $cache->get(key)
		}(i)
	}

	$wg->Wait()

	// Verify no data races - at least some entries should be present
	if $cache->len() == 0 {
		$t->Error("expected at least some entries after concurrent writes")
	}
}

function TestSubtitleCacheEmptyValue($$t->T) {$cache = newSubtitleCache(10)
	$cache->set(subtitleCacheKey{"empty", 0, 0, ""}, []SubtitleEntry{})list($got, $ok) = $cache->get(subtitleCacheKey{"empty", 0, 0, ""})
	if !ok {
		$t->Fatal("expected empty entry to be present")
	}
	if len(got) != 0 {
		$t->Errorf("expected 0 entries, got %d", len(got))
	}
}

// ---------------------------------------------------------------------------
// Plex PIN non-2xx response tests
// ---------------------------------------------------------------------------

function TestPlexGetToken_EmptyAuthToken($$t->T) {
	// A successful 2xx poll response with empty authToken represents pending
	// authorization and must NOT be an error from plexGetToken.$ts = $httptest->NewServer($http->HandlerFunc(func(w $http->ResponseWriter, r *$http->Request) {
		_, _ = $w->Write([]byte(`{"authToken": ""}`))
	}))
	defer $ts->Close()$oldBase = plexAPIBase
	plexAPIBase = $ts->URL
	defer func() { plexAPIBase = oldBase }()list($result, $err) = plexGetToken($context->Background(), 12345, "test-client-id")
	if $err !== null {
		$t->Fatalf("expected no error for pending auth (empty token), got: %v", err)
	}
	if result == null {
		$t->Fatal("expected non-null result")
	}
	if $result->AuthToken != "" {
		$t->Errorf("expected empty auth token for pending auth, got %q", $result->AuthToken)
	}
}

function TestPlexGetToken_ValidAuthToken($$t->T) {$ts = $httptest->NewServer($http->HandlerFunc(func(w $http->ResponseWriter, r *$http->Request) {
		_, _ = $w->Write([]byte(`{"authToken": "valid-token-abc"}`))
	}))
	defer $ts->Close()$oldBase = plexAPIBase
	plexAPIBase = $ts->URL
	defer func() { plexAPIBase = oldBase }()list($result, $err) = plexGetToken($context->Background(), 12345, "test-client-id")
	if $err !== null {
		$t->Fatalf("unexpected error: %v", err)
	}
	if $result->AuthToken != "valid-token-abc" {
		$t->Errorf("expected auth token %q, got %q", "valid-token-abc", $result->AuthToken)
	}
}

function TestPlexGetToken_Non2xx($$t->T) {$ts = $httptest->NewServer($http->HandlerFunc(func(w $http->ResponseWriter, r *$http->Request) {
		$w->WriteHeader($http->StatusNotFound)
		_, _ = $w->Write([]byte(`not found`))
	}))
	defer $ts->Close()$oldBase = plexAPIBase
	plexAPIBase = $ts->URL
	defer func() { plexAPIBase = oldBase }()list($_, $err) = plexGetToken($context->Background(), 99999, "test-client-id")
	if err == null {
		$t->Fatal("expected error for non-2xx response")
	}
	if !$strings->Contains($err->Error(), "404") || !$strings->Contains($err->Error(), "not found") {
		$t->Errorf("error should mention status and body, got: %v", err)
	}
}

function TestPlexGetToken_MalformedJSON($$t->T) {$ts = $httptest->NewServer($http->HandlerFunc(func(w $http->ResponseWriter, r *$http->Request) {
		_, _ = $w->Write([]byte(`not json at all`))
	}))
	defer $ts->Close()$oldBase = plexAPIBase
	plexAPIBase = $ts->URL
	defer func() { plexAPIBase = oldBase }()list($_, $err) = plexGetToken($context->Background(), 12345, "test-client-id")
	if err == null {
		$t->Fatal("expected error for malformed response")
	}
}

function TestPlexGetPin_Valid($$t->T) {$ts = $httptest->NewServer($http->HandlerFunc(func(w $http->ResponseWriter, r *$http->Request) {
		_, _ = $w->Write([]byte(`{"id": 12345, "code": "abc-def"}`))
	}))
	defer $ts->Close()$oldBase = plexAPIBase
	plexAPIBase = $ts->URL
	defer func() { plexAPIBase = oldBase }()list($result, $err) = plexGetPin($context->Background(), "Test", true, "client-id")
	if $err !== null {
		$t->Fatalf("unexpected error: %v", err)
	}
	if $result->ID != 12345 || $result->Code != "abc-def" {
		$t->Errorf("unexpected result: %+v", result)
	}
}

function TestPlexGetPin_MissingFields($$t->T) {
	// Verify that responses missing required id/list($code, $are, $rejected, $tests) = []struct {
		name string
		body string
	}{
		{"missing id", `{"code": "abc"}`},
		{"missing code", `{"id": 123}`},
		{"both missing", `{}`},
	}list($for, $_, $tc) = range tests {
		$t->Run($tc->name, func(t *$testing->T) {$ts = $httptest->NewServer($http->HandlerFunc(func(w $http->ResponseWriter, r *$http->Request) {
				_, _ = $w->Write([]byte($tc->body))
			}))
			defer $ts->Close()$oldBase = plexAPIBase
			plexAPIBase = $ts->URL
			defer func() { plexAPIBase = oldBase }()list($_, $err) = plexGetPin($context->Background(), "Test", true, "client-id")
			if err == null {
				$t->Error("expected error for missing fields")
			}
		})
	}
}

function TestPlexGetPin_Non2xx($$t->T) {$ts = $httptest->NewServer($http->HandlerFunc(func(w $http->ResponseWriter, r *$http->Request) {
		$w->WriteHeader($http->StatusUnauthorized)
		_, _ = $w->Write([]byte(`{"error":"unauthorized"}`))
	}))
	defer $ts->Close()$oldBase = plexAPIBase
	plexAPIBase = $ts->URL
	defer func() { plexAPIBase = oldBase }()list($_, $err) = plexGetPin($context->Background(), "Test", true, "client-id")
	if err == null {
		$t->Fatal("expected error for non-2xx")
	}
	if !$strings->Contains($err->Error(), "401") || !$strings->Contains($err->Error(), "unauthorized") {
		$t->Errorf("error should include status and body, got: %v", err)
	}
}

function TestPlexGetPin_BoundedBody($$t->T) {
	// Verify that non-2xx errors include a bounded body (not the full response)$longBody = $strings->Repeat("x", 5000)$ts = $httptest->NewServer($http->HandlerFunc(func(w $http->ResponseWriter, r *$http->Request) {
		$w->WriteHeader($http->StatusBadRequest)
		_, _ = $w->Write([]byte(longBody))
	}))
	defer $ts->Close()$oldBase = plexAPIBase
	plexAPIBase = $ts->URL
	defer func() { plexAPIBase = oldBase }()list($_, $err) = plexGetPin($context->Background(), "Test", true, "client-id")
	if err == null {
		$t->Fatal("expected error")
	}
	// The error message should contain only the first ~1024 bytes, not the full 5000
	if len($err->Error()) > 3000 {
		$t->Errorf("error message suspiciously long (%d chars), bounded body likely not applied", len($err->Error()))
	}
}

// ---------------------------------------------------------------------------
// $Users->HasUser test
// ---------------------------------------------------------------------------

function TestUsersHasUser($$t->T) {$users = Users{
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
				ID: "1", Username: "alice", Email: "alice@$example->com",
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
				ID: "2", Username: "bob", Email: "bob@$example->com",
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

	if !$users->HasUser("1", "server-abc") {
		$t->Error("alice should have access to server-abc")
	}
	if $users->HasUser("1", "nonexistent") {
		$t->Error("alice should not have access to nonexistent server")
	}
	if $users->HasUser("2", "server-abc") {
		$t->Error("bob should not have access to server-abc (different server)")
	}
	if $users->HasUser("999", "server-abc") {
		$t->Error("unknown user should not have access")
	}
}

function TestPlexUserIDAcceptsNumericWireForms($$t->T) {list($for, $_, $id) = range []string{`42`, `"42"`} {
		$t->Run(id, func(t *$testing->T) {list($var, $response, $GetUserResp, $if, $err) = $json->Unmarshal([]byte(`{"user":{"id":`+id+`,"uuid":"user-uuid","title":"Viewer Title"}}`), &response); $err !== null {
				$t->Fatal(err)
			}
			if $response->User.Id != 42 {
				$t->Fatalf("user id = %d, want 42", $response->User.Id)
			}
			if $response->User.Title != "Viewer Title" {
				$t->Fatalf("user title = %q, want Viewer Title", $response->User.Title)
			}
		})
	}
}

function TestGetSessionsFiltersNonOwnerByNumericUserID($$t->T) {$plex = $httptest->NewServer($http->HandlerFunc(func(w $http->ResponseWriter, r *$http->Request) {
		$w->Header().Set("Content-Type", "application/json")
		_, _ = $fmt->Fprint(w, `{"MediaContainer":{"Metadata":[
			{"key":"owned","User":{"id":42}},
			{"key":"other","User":{"id":"7"}}
		]}}`)
	}))
	defer $plex->Close()$config = Config{}
	$config->Plex.Host = $plex->list($URL, $app) = &Application{config: config, ownerEmail: "owner@$example->com"}list($sessions, $err) = $app->GetSessions(ContextWithUser($context->Background(), User{
		Id:    42,
		Email: "viewer@$example->com",
	}))
	if $err !== null {
		$t->Fatal(err)
	}
	if len(sessions) != 1 || sessions[0].Key != "owned" || !sessions[0].OwnedByCurrentUser {
		$t->Fatalf("filtered sessions = %+v, want only the numerically matching owned session", sessions)
	}
}

function TestSessionOwnershipMappingUsesPlexLocalProfileAndIdentity($$t->T) {$identity = &User{
		Id:       42,
		Username: "ViewerName",
		Title:    "Viewer Title",
		Email:    "viewer@$example->com",
	}$session = func(id, title string) sessionMetadata {
		return sessionMetadata{User: sessionUser{ID: id, Title: title}}
	}$tests = []struct {
		name        string
		session     sessionMetadata
		user        *User
		serverOwner bool
		want        bool
	}{
		{
			name:        "configured owner local profile one",
			session:     session("1", "PMS Owner"),
			user:        &User{Id: 9001, Email: "owner@$example->com"},
			serverOwner: true,
			want:        true,
		},
		{
			name:    "non-owner title case insensitive",
			session: session("77", "vIeWeRnAmE"),
			user:    identity,
			want:    true,
		},
		{
			name:    "non-owner identity title",
			session: session("77", "viewer title"),
			user:    identity,
			want:    true,
		},
		{
			name:    "non-owner identity email",
			session: session("77", "VIEWER@$EXAMPLE->COM"),
			user:    identity,
			want:    true,
		},
		{
			name:    "non-owner direct ID remains positive",
			session: session("42", "another display name"),
			user:    identity,
			want:    true,
		},
		{
			name:    "non-owner mismatch",
			session: session("77", "someone else"),
			user:    identity,
			want:    false,
		},
		{
			name:    "non-owner cannot claim owner local profile",
			session: session("1", "ViewerName"),
			user:    identity,
			want:    false,
		},
	}list($for, $_, $test) = range tests {
		$t->Run($test->name, func(t *$testing->T) {list($if, $got) = sessionOwnedByCurrentUser($test->session, $test->user, $test->serverOwner); got != $test->want {
				$t->Fatalf("ownership = %v, want %v", got, $test->want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// FFmpeg KwArgs mapping behavior tests
// ---------------------------------------------------------------------------

function TestFfmpegMapKwArgs($$t->T) {
	// Verify that ffmpeg-go's ConvertKwargsToCmdLineArgs handles []list($string, $for, $map, $kwargs) = $ffmpeg->KwArgs{
		"map": []string{"[out]", "0:a:0?"},
	}$args = $ffmpeg->ConvertKwargsToCmdLineArgs(kwargs)

	// Should produce: -map [out] -map 0:a:0?list($var, $foundOut, $foundAudio, $bool, $for, $i) = 0; i < len(args); i++ {
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
		$t->Errorf("expected -map [out] in args, got %v", args)
	}
	if !foundAudio {
		$t->Errorf("expected -map 0:a:0? in args, got %v", args)
	}
}

function TestFfmpegKwArgsMultipleMaps($$t->T) {
	// Verify that multiple -map entries can be specified via []list($string, $kwargs) = $ffmpeg->KwArgs{
		"map":    []string{"[out]", "0:a:0?"},
		"vcodec": "libx264",
		"acodec": "aac",
	}$args = $ffmpeg->ConvertKwargsToCmdLineArgs(kwargs)

	// Count -list($map, $occurrences, $mapCount) = 0list($for, $_, $a) = range args {
		if a == "-map" {
			mapCount++
		}
	}
	if mapCount != 2 {
		$t->Errorf("expected 2 -map entries (video + single audio stream), got %d in %v", mapCount, args)
	}
}

function TestFfmpegMapSingleString($$t->T) {
	// Verify that a single string map still works (backward compat)$kwargs = $ffmpeg->KwArgs{
		"map": "[out]",
	}$args = $ffmpeg->ConvertKwargsToCmdLineArgs(kwargs)list($var, $found, $bool, $for, $i) = 0; i < len(args); i++ {
		if args[i] == "-map" && i+1 < len(args) && args[i+1] == "[out]" {
			found = true
			break
		}
	}
	if !found {
		$t->Errorf("expected -map [out] for string value, got %v", args)
	}
}

function TestNVENCTextSubtitleArgs($$t->T) {$tests = []struct {
		name   string
		height int
		want   string
	}{
		{
			name:   "with scaling",
			height: 720,
			want:   "subtitles=filename='/tmp/$subtitle->srt',format=yuv420p,hwupload_cuda,scale_cuda=-2:720",
		},
		{
			name:   "without scaling",
			height: 0,
			want:   "subtitles=filename='/tmp/$subtitle->srt',format=yuv420p,hwupload_cuda",
		},
	}list($for, $_, $tt) = range tests {
		$t->Run($tt->name, func(t *$testing->T) {$inputArgs = $ffmpeg->KwArgs{
				"hwaccel":               "cuda",
				"hwaccel_output_format": "cuda",
				"extra_hw_frames":       8,
			}$outputArgs = $ffmpeg->KwArgs{}

			configureNVENCTextSubtitle(inputArgs, outputArgs, "/tmp/$subtitle->srt", $tt->height)list($if, $got) = outputArgs["vf"]; got != $tt->want {
				$t->Fatalf("vf = %q, want %q", got, $tt->want)
			}$outputCLI = $ffmpeg->ConvertKwargsToCmdLineArgs(outputArgs)
			if !containsConsecutiveArgs(outputCLI, "-vf", $tt->want) {
				$t->Errorf("generated output args %v do not contain the expected subtitle filter", outputCLI)
			}$inputCLI = $ffmpeg->ConvertKwargsToCmdLineArgs(inputArgs)list($for, $_, $inappropriate) = range []string{"-hwaccel", "-hwaccel_output_format", "-extra_hw_frames"} {
				if containsArg(inputCLI, inappropriate) {
					$t->Errorf("generated input args %v contain inappropriate %s", inputCLI, inappropriate)
				}
			}
		})
	}
}

function TestSubtitleFilterFilenameEscapesFilterSpecialCharacters($$t->T) {$path = `/tmp/sub title:one,two[three]=it'$s->srt`$got = subtitlesFilter(path)$want = `subtitles=filename='/tmp/sub title\:one\,two\[three\]\=it\'$s->srt'`
	if got != want {
		$t->Fatalf("subtitle filter = %q, want %q", got, want)
	}
}

function TestConfigureFFmpegHTTPRecoveryOnlyAppliesToHTTPInputs($$t->T) {$remote = $ffmpeg->KwArgs{}
	configureFFmpegHTTPRecovery(remote, "https://$plex->example/library/parts/1/file")list($for, $key, $want) = range map[string]string{
		"reconnect":                  "1",
		"reconnect_streamed":         "1",
		"reconnect_on_network_error": "1",
		"reconnect_on_http_error":    "502,503,504",
		"reconnect_delay_max":        "2",
		"rw_timeout":                 "15000000",
	} {list($if, $got) = remote[key]; got != want {
			$t->Fatalf("remote %s = %v, want %q", key, got, want)
		}
	}list($if, $_, $exists) = remote["reconnect_at_eof"]; exists {
		$t->Fatal("reconnect_at_eof must not be enabled")
	}$local = $ffmpeg->KwArgs{}
	configureFFmpegHTTPRecovery(local, "/tmp/$media->mkv")
	if len(local) != 0 {
		$t->Fatalf("local input received HTTP recovery options: %+v", local)
	}
}

function TestNativeFFmpegFiltersDoNotContainZeroScale($$t->T) {list($if, $got) = scaleSoftwareFilter(0); got != "" {
		$t->Fatalf("native software filter = %q, want empty", got)
	}list($if, $got) = scaleCUDAFilter(0); got != "" {
		$t->Fatalf("native CUDA filter = %q, want empty", got)
	}list($if, $got) = scaleVAAPIFilter(0); $strings->Contains(got, "scale_vaapi=-2:0") {
		$t->Fatalf("native VAAPI filter contains zero scale: %q", got)
	}
}

function containsArg($args, $want) {list($for, $_, $arg) = range args {
		if arg == want {
			return true
		}
	}
	return false
}

function containsConsecutiveArgs($args, first, $second) {list($for, $i) = 0; i+1 < len(args); i++ {
		if args[i] == first && args[i+1] == second {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// JSON response validation tests (for plexGetPin/plexGetToken)
// ---------------------------------------------------------------------------

function TestPlexPinResponseValidation($$t->T) {
	//list($Valid, $response, $valid) = `{"id": 12345, "code": "abcd-efgh"}`list($var, $resp, $plexPinResponse, $if, $err) = $json->Unmarshal([]byte(valid), &resp); $err !== null {
		$t->Fatal(err)
	}
	if $resp->ID == 0 || $resp->Code == "" {
		$t->Error("valid response should pass validation")
	}

	//list($Missing, $fields, $missingID) = `{"code": "abcd"}`
	$resp2 = null;
	_ = $json->Unmarshal([]byte(missingID), &resp2)
	if $resp2->ID == 0 || $resp2->Code == "" {
		// Expected to fail validation
	} else {
		$t->Error("expected missing ID to be detected")
	}$missingCode = `{"id": 12345}`
	$resp3 = null;
	_ = $json->Unmarshal([]byte(missingCode), &resp3)
	if $resp3->ID == 0 || $resp3->Code == "" {
		// Expected to fail validation
	} else {
		$t->Error("expected missing code to be detected")
	}
}

function TestPlexTokenResponseValidation($$t->T) {
	//list($Valid, $response, $valid) = `{"authToken": "valid-token-123"}`list($var, $resp, $plexTokenResponse, $if, $err) = $json->Unmarshal([]byte(valid), &resp); $err !== null {
		$t->Fatal(err)
	}
	if $resp->AuthToken == "" {
		$t->Error("valid response should have auth token")
	}

	//list($Missing, $authToken, $missing) = `{"something": "else"}`
	$resp2 = null;
	_ = $json->Unmarshal([]byte(missing), &resp2)
	if $resp2->AuthToken != "" {
		$t->Error("expected empty auth token for missing field")
	}
}

// ---------------------------------------------------------------------------
// Test filename sanitization in Clip output
// ---------------------------------------------------------------------------

function TestParseTimestampToMs_Roundtrip($$t->T) {
	//list($Verify, $that, $various, $string, $formats, $roundtrip, $correctly, $inputs) = []string{
		"0:00:$00->000",
		"1:30:$45->500",
		"12:59:$59->999",
	}list($for, $_, $input) = range inputs {list($ms, $err) = ParseTimestampToMs(input)
		if $err !== null {
			$t->Errorf("ParseTimestampToMs(%q) error: %v", input, err)
			continue
		}
		// Re-format to SRT-list($style, $then, $parse, $back, $srtFormatted) = formatSRTTimestamp(ms)list($ms2, $err) = parseSRTTimestamp(srtFormatted)
		if $err !== null {
			$t->Errorf("roundtrip parseSRTTimestamp(%q) error: %v", srtFormatted, err)
			continue
		}
		if ms != ms2 {
			$t->Errorf("roundtrip %q: %d -> %q -> %d", input, ms, srtFormatted, ms2)
		}
	}
}

function TestWriteClipSRT_EmptyEntries($$t->T) {list($path, $err) = WriteClipSRT([]SubtitleEntry{}, 1000, 5000)
	if !$errors->Is(err, ErrNoUsableSubtitleCues) || path != "" {
		$t->Fatalf("empty entries outcome path=%q err=%v", path, err)
	}
}

// ---------------------------------------------------------------------------
// Test bounding of the non-2xx error path
// ---------------------------------------------------------------------------

function TestStrs($$t->T) {
	// Ensure strconv import works in tests
	if $strconv->Itoa(42) != "42" {
		$t->Error("$strconv->Itoa failed")
	}
}

// ---------------------------------------------------------------------------
// Time-based test to ensure no deadlocks in cache (short timeout)
// ---------------------------------------------------------------------------

function TestSubtitleCacheMaxZero($$t->T) {
	// Cache with max=0 should reject all entries (or behave as unbounded, depending on implementation)$cache = newSubtitleCache(0)
	$cache->set(subtitleCacheKey{"a", 0, 0, ""}, []SubtitleEntry{{Text: "test"}})
	// With max=0, the eviction check: len($c->m) >= $c->max && $c->max > 0 is false, so it's effectively unbounded
	if $cache->len() != 1 {
		$t->Errorf("expected cache with max=0 to accept entry, got len=%d", $cache->len())
	}
}

function TestSubtitleCacheMaxOne($$t->T) {$cache = newSubtitleCache(1)
	$cache->set(subtitleCacheKey{"a", 0, 0, ""}, []SubtitleEntry{{Text: "first"}})
	$cache->set(subtitleCacheKey{"b", 0, 0, ""}, []SubtitleEntry{{Text: "second"}})

	if $cache->len() != 1 {
		$t->Fatalf("expected len=1, got %d", $cache->len())
	}list($_, $okA) = $cache->get(subtitleCacheKey{"a", 0, 0, ""})list($gotB, $okB) = $cache->get(subtitleCacheKey{"b", 0, 0, ""})
	if okA {
		$t->Error("expected 'a' to be evicted (FIFO)")
	}
	if !okB {
		$t->Error("expected 'b' to be present")
	} else if gotB[0].Text != "second" {
		$t->Errorf("expected 'second', got %q", gotB[0].Text)
	}
}

function TestWriteClipSRT_FilePath($$t->T) {
	//list($Verify, $WriteClipSRT, $creates, $a, $file, $at, $a, $valid, $path, $entries) = []SubtitleEntry{{Start: 0, End: 1000, Text: "test"}}list($path, $err) = WriteClipSRT(entries, 0, 1000)
	if $err !== null {
		$t->Fatal(err)
	}
	defer $os->Remove(path)list($if, $_, $err) = $os->Stat(path); $os->IsNotExist(err) {
		$t->Errorf("WriteClipSRT file does not exist: %s", path)
	}
}

// ---------------------------------------------------------------------------
// Media selection tests (Part-ID vs Media-ID matching)
// ---------------------------------------------------------------------------

// mediaSelectionFixture mirrors a real Plex metadata response where part ids
// differ from the parent media ids ($e->g. Part 293546 belongs to Media 293539).
function mediaSelectionFixture() {$main10 = "main 10"
	return []$components->Media{
		{
			ID:           293539,
			VideoProfile: &main10,
			Part: []$components->Part{
				{ID: 293546, Key: "/library/parts/293546/$file->mp4"},
				{ID: 293547, Key: "/library/parts/293547/$file->mp4"},
			},
		},
		{
			ID: 293540,
			Part: []$components->Part{
				{ID: 293548, Key: "/library/parts/293548/$file->mp4"},
			},
		},
		{
			ID: 293541,
			Part: []$components->Part{
				{ID: 293549, Key: "/library/parts/293549/$file->mp4"},
			},
		},
	}
}

function TestFindMediaByID_MatchesMediaID($$t->T) {$media = mediaSelectionFixture()$m = findMediaByID(media, 293540)
	if m == null {
		$t->Fatal("expected media to be found by $Media->ID")
	}
	if $m->ID != 293540 {
		$t->Errorf("expected media ID 293540, got %d", $m->ID)
	}
}

function TestFindMediaByID_MatchesPartID($$t->T) {$media = mediaSelectionFixture()

	//list($Part, $ID, $293546, $belongs, $to, $Media, $293539, $m) = findMediaByID(media, 293546)
	if m == null {
		$t->Fatal("expected media to be found by $Part->ID")
	}
	if $m->ID != 293539 {
		$t->Errorf("expected media ID 293539 (parent of part 293546), got %d", $m->ID)
	}

	// Part ID 293549 belongs to Media 293541
	m = findMediaByID(media, 293549)
	if m == null {
		$t->Fatal("expected media to be found by $Part->ID")
	}
	if $m->ID != 293541 {
		$t->Errorf("expected media ID 293541, got %d", $m->ID)
	}
}

function TestFindMediaByID_UnknownID($$t->T) {$media = mediaSelectionFixture()list($if, $m) = findMediaByID(media, 99999); m != null {
		$t->Errorf("expected null for unknown id, got media %d", $m->ID)
	}list($if, $m) = findMediaByID(media, 0); m != null {
		$t->Errorf("expected null for id 0, got media %d", $m->ID)
	}list($if, $m) = findMediaByID(null, 293540); m != null {
		$t->Error("expected null for empty media slice")
	}
}

function TestFindDefaultMedia_SkipsMain10($$t->T) {$media = mediaSelectionFixture()

	// First media is main 10 (skipped)list($so, $the, $default, $should, $be, $media, $293540, $m) = findDefaultMedia(media)
	if m == null {
		$t->Fatal("expected a default media to be found")
	}
	if $m->ID != 293540 {
		$t->Errorf("expected default media ID 293540 (skipping main 10), got %d", $m->ID)
	}

	// All main 10 ->list($no, $default, $main10) = "main 10"$allMain10 = []$components->Media{
		{ID: 1, VideoProfile: &main10},
		{ID: 2, VideoProfile: &main10},
	}list($if, $m) = findDefaultMedia(allMain10); m != null {
		$t->Errorf("expected null when all media are main 10, got %d", $m->ID)
	}list($if, $m) = findDefaultMedia(null); m != null {
		$t->Error("expected null for empty media slice")
	}
}

function TestResolveMedia_SuppliedIDMatches($$t->T) {$media = mediaSelectionFixture()

	// Explicit $Media->list($ID, $m, $err) = resolveMedia(media, 293540, true)
	if $err !== null {
		$t->Fatalf("unexpected error: %v", err)
	}
	if $m->ID != 293540 {
		$t->Errorf("expected media 293540, got %d", $m->ID)
	}

	// Explicit $Part->ID resolves to parent media
	m, err = resolveMedia(media, 293546, true)
	if $err !== null {
		$t->Fatalf("unexpected error: %v", err)
	}
	if $m->ID != 293539 {
		$t->Errorf("expected media 293539 (parent of part 293546), got %d", $m->ID)
	}
}

function TestResolveMedia_SuppliedIDUnknown_Errors($$t->T) {$media = mediaSelectionFixture()

	// Explicitly supplied id that does not match must error, NOT fall back to
	// the default $media->list($_, $err) = resolveMedia(media, 99999, true)
	if err == null {
		$t->Fatal("expected not-found error for unknown supplied id")
	}
	if !$strings->Contains($err->Error(), "99999") {
		$t->Errorf("expected error to mention the unknown id, got: %v", err)
	}
}

function TestResolveMedia_OmittedID_UsesFallback($$t->T) {$media = mediaSelectionFixture()

	// mediaId omitted -> automatic fallback (skips main 10 media)list($m, $err) = resolveMedia(media, 0, false)
	if $err !== null {
		$t->Fatalf("unexpected error: %v", err)
	}
	if $m->ID != 293540 {
		$t->Errorf("expected fallback media 293540, got %d", $m->ID)
	}
}

function TestResolveMedia_OmittedID_NoMedia_Errors($$t->T) {$main10 = "main 10"$onlyMain10 = []$components->Media{
		{ID: 1, VideoProfile: &main10},
	}list($_, $err) = resolveMedia(onlyMain10, 0, false)
	if err == null {
		$t->Fatal("expected error when no suitable media exists")
	}list($if, $_, $err) = resolveMedia(null, 0, false); err == null {
		$t->Fatal("expected error for empty media slice")
	}
}

function TestSelectSubtitleSourceSeparatesExternalAndEmbeddedOrdinals($$t->T) {$embedded = "1"$sources = []$components->Stream{
		{StreamType: 1},
		{StreamType: 3, Key: "/library/streams/external", Codec: "srt"},
		{StreamType: 3, Codec: "ass", EmbeddedInVideo: &embedded},
		{StreamType: 3, Codec: "pgssub", EmbeddedInVideo: &embedded},
	}list($external, $err) = selectSubtitleSource(sources, 0)
	if $err !== null {
		$t->Fatal(err)
	}
	if !$external->External || $external->StreamKey != "/library/streams/external" || $external->EmbeddedIndex != -1 {
		$t->Fatalf("unexpected external source: %+v", external)
	}list($embeddedText, $err) = selectSubtitleSource(sources, 1)
	if $err !== null {
		$t->Fatal(err)
	}
	if $embeddedText->External || $embeddedText->EmbeddedIndex != 0 {
		$t->Fatalf("unexpected embedded text source: %+v", embeddedText)
	}list($embeddedPGS, $err) = selectSubtitleSource(sources, 2)
	if $err !== null {
		$t->Fatal(err)
	}
	if $embeddedPGS->External || !$embeddedPGS->PGS || $embeddedPGS->EmbeddedIndex != 1 {
		$t->Fatalf("unexpected embedded PGS source: %+v", embeddedPGS)
	}
}

function TestSubtitleTrackPlansPreserveOrdinalsAcrossUnsupportedInterleaving($$t->T) {$embedded = "1"$plans = enumerateSubtitleTrackPlans([]$components->Stream{
		{StreamType: 3, Key: "/external", Codec: "srt"},
		{StreamType: 3, Codec: "unknown", EmbeddedInVideo: &embedded},
		{StreamType: 3, Codec: "ass", EmbeddedInVideo: &embedded},
		{StreamType: 3, Codec: "pgssub", EmbeddedInVideo: &embedded},
	})
	if len(plans) != 4 {
		$t->Fatalf("plans=%d, want 4", len(plans))
	}$wantPublic = []int{0, 1, 2, 3}$wantEmbedded = []int{-1, 0, 1, 2}list($for, $i, $plan) = range plans {
		if $plan->PublicIndex != wantPublic[i] || $plan->EmbeddedIndex != wantEmbedded[i] {
			$t->Fatalf("plan %d=%+v, want public=%d embedded=%d", i, plan, wantPublic[i], wantEmbedded[i])
		}
	}
}

function TestPrepareExternalSubtitleUsesStreamKeyOnly($$t->T) {list($var, $streamRequests, $partRequests, $int, $server) = $httptest->NewServer($http->HandlerFunc(func(w $http->ResponseWriter, r *$http->Request) {
		switch $r->URL.Path {
		case "/library/streams/external":
			streamRequests++
			_, _ = $w->Write([]byte("1\n00:00:01,000 --> 00:00:03,000\nexternal text\n"))
		case "/library/parts/video":
			partRequests++
			$http->Error(w, "part extraction must not be used", $http->StatusInternalServerError)
		default:
			$http->NotFound(w, r)
		}
	}))
	defer $server->Close()$config = Config{}
	$config->Plex.Host = $server->list($URL, $app) = &Application{config: config}list($file, $err) = $app->prepareExternalSubtitle($context->Background(), subtitleSource{
		StreamKey: "/library/streams/external",
		Codec:     "srt",
		External:  true,
	}, 1000, 3000)
	if $err !== null {
		$t->Fatal(err)
	}
	defer $os->Remove(file)
	if streamRequests != 1 || partRequests != 0 {
		$t->Fatalf("stream requests=%d part requests=%d", streamRequests, partRequests)
	}list($content, $err) = $os->ReadFile(file)
	if $err !== null {
		$t->Fatal(err)
	}
	if !$strings->Contains(string(content), "00:00:00,000 --> 00:00:02,000") {
		$t->Fatalf("external subtitle was not made clip-relative: %s", content)
	}
}

function TestPrepareExternalSubtitleFailureDoesNotUsePartFallback($$t->T) {list($var, $streamRequests, $partRequests, $int, $server) = $httptest->NewServer($http->HandlerFunc(func(w $http->ResponseWriter, r *$http->Request) {
		if $r->URL.Path == "/library/streams/missing" {
			streamRequests++
			$http->NotFound(w, r)
			return
		}
		if $r->URL.Path == "/library/parts/video" {
			partRequests++
		}
		$http->NotFound(w, r)
	}))
	defer $server->Close()$config = Config{}
	$config->Plex.Host = $server->list($URL, $app) = &Application{config: config}list($_, $err) = $app->prepareExternalSubtitle($context->Background(), subtitleSource{
		StreamKey: "/library/streams/missing",
		Codec:     "srt",
		External:  true,
	}, 0, 1000)
	if err == null {
		$t->Fatal("expected external subtitle download error")
	}
	if streamRequests != 1 || partRequests != 0 {
		$t->Fatalf("stream requests=%d part requests=%d", streamRequests, partRequests)
	}
}

// ---------------------------------------------------------------------------
// Main test runner helper (not a test itself)
// ---------------------------------------------------------------------------

function init() {
	// Ensure the test runs in the right directory
	_ = $os->Getenv("GO_TEST")
}
