package main

import (
	"strings"
	"testing"
)

func TestBuildDownloadFilename(t *testing.T) {
	tests := []struct {
		name  string
		title string
		want  string
	}{
		{
			name:  "normal title and range",
			title: "Movie Title",
			want:  "Movie_Title_00-05-39_to_00-06-39.mp4",
		},
		{
			name:  "unsafe title",
			title: "../Movie/Title\r\n\\bad",
			want:  "Movie_Title_bad_00-00-00_to_00-00-01.mp4",
		},
		{
			name:  "blank title",
			title: " ./.. ",
			want:  "clip_00-00-00_to_00-00-01.mp4",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := buildDownloadFilename(renderJobSpec{
				Title: test.title, FromMs: 339_000, ToMs: 399_000,
			}, false)
			if test.name != "normal title and range" {
				got = buildDownloadFilename(renderJobSpec{Title: test.title, ToMs: 1_000}, false)
			}
			if got != test.want {
				t.Fatalf("download filename = %q, want %q", got, test.want)
			}
			if strings.ContainsAny(got, "\\/\r\n\t\x00") {
				t.Fatalf("download filename contains unsafe characters: %q", got)
			}
		})
	}
}

func TestRenderDownloadContentDispositionSupportsUnicodeSafely(t *testing.T) {
	header := renderDownloadContentDisposition(renderJobSpec{Title: "映画"})
	if !strings.HasPrefix(header, `attachment; filename="clip_`) {
		t.Fatalf("missing safe ASCII fallback: %q", header)
	}
	if !strings.Contains(header, `filename*=UTF-8''%E6%98%A0%E7%94%BB_`) {
		t.Fatalf("missing RFC 5987 filename encoding: %q", header)
	}
	if strings.ContainsAny(header, "\r\n") {
		t.Fatalf("content disposition contains header injection characters: %q", header)
	}
}
