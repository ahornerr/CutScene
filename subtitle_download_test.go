package main

import "testing"

func TestRenderSRTUsesCommaMilliseconds(t *testing.T) {
	got := string(renderSRT([]SubtitleEntry{{Start: 3723456, End: 3725000, Text: "Hello"}}))
	want := "1\n01:02:03,456 --> 01:02:05,000\nHello\n\n"
	if got != want {
		t.Fatalf("renderSRT = %q, want %q", got, want)
	}
}
