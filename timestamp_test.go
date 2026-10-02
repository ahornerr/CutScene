package main

import (
	"math"
	"strconv"
	"testing"
)

// TestParseTimestampToMsRejectsOverflow is a regression test: the colon form
// multiplied the parsed hours by 3600000 with no bound, so a large hours value
// silently wrapped around and returned a negative duration instead of an error.
//
// The plain-numeric form was already bounded; the two forms disagreed.
func TestParseTimestampToMsRejectsOverflow(t *testing.T) {
	tests := []string{
		"9223372036854775807:00:00",
		strconv.FormatInt(math.MaxInt64, 10) + ":00:00",
		strconv.FormatInt(math.MaxInt64/3600000+1, 10) + ":00:00",
		// Exactly one hour past the representable maximum.
		strconv.FormatInt(math.MaxInt64/3600000+1, 10) + ":30:00",
	}
	for _, ts := range tests {
		t.Run(ts, func(t *testing.T) {
			got, err := ParseTimestampToMs(ts)
			if err == nil {
				t.Fatalf("ParseTimestampToMs(%q) = %d, want an error for an out-of-range timestamp", ts, got)
			}
			if got != 0 {
				t.Errorf("ParseTimestampToMs(%q) returned %d alongside an error, want 0", ts, got)
			}
		})
	}
}

// TestParseTimestampToMsStaysWithinRange guards the general invariant: a parsed
// timestamp is never negative and never wraps.
func TestParseTimestampToMsStaysWithinRange(t *testing.T) {
	cases := []struct {
		in   string
		want int64
	}{
		{in: "0", want: 0},
		{in: "00:00:00.000", want: 0},
		{in: "00:00:01.500", want: 1500},
		{in: "01:02:03.004", want: 3723004},
		{in: "99:59:59.999", want: 359999999},
		{in: "1.5", want: 1500},
	}
	for _, c := range cases {
		got, err := ParseTimestampToMs(c.in)
		if err != nil {
			t.Errorf("ParseTimestampToMs(%q) returned %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("ParseTimestampToMs(%q) = %d, want %d", c.in, got, c.want)
		}
		if got < 0 {
			t.Errorf("ParseTimestampToMs(%q) = %d, want a non-negative value", c.in, got)
		}
	}
}

// TestParseTimestampToMsRejectsMalformedInput keeps the existing validation
// covered alongside the new bound.
func TestParseTimestampToMsRejectsMalformedInput(t *testing.T) {
	for _, ts := range []string{
		"", "abc", "-1", "00:00:60", "00:60:00", "00:00:00.1234567",
		"1:2", "1:2:3:4",
	} {
		if got, err := ParseTimestampToMs(ts); err == nil {
			t.Errorf("ParseTimestampToMs(%q) = %d, want an error", ts, got)
		}
	}
}
