package main

import (
	"strings"
)

// maxSubtitleSnippetRunes bounds the excerpt that ends up in a download
// filename. Long enough to identify a scene, short enough that the resulting
// name stays readable in a file listing and well under common filesystem
// filename limits once the title and range are added.
const maxSubtitleSnippetRunes = 72

// subtitleSnippetFromEntries builds a short, human-readable excerpt of the
// dialogue covered by a clip.
//
// Downloaded clips are otherwise identified only by title and timestamp, so
// several clips of the same scene are hard to tell apart in a folder. The
// excerpt gives each clip a recognisable name.
//
// Subtitle markup is presentation-only and is stripped using the same routine
// the search indexer uses, so override tags and HTML entities never reach a
// filename.
func subtitleSnippetFromEntries(entries []SubtitleEntry) string {
	var builder strings.Builder
	for _, entry := range entries {
		text := strings.Join(strings.Fields(normalizeSubtitleSearchText(entry.Text)), " ")
		if text == "" {
			continue
		}
		if builder.Len() > 0 {
			builder.WriteByte(' ')
		}
		builder.WriteString(text)
		if builder.Len() >= maxSubtitleSnippetRunes*2 {
			break
		}
	}
	return truncateSubtitleSnippet(builder.String())
}

// truncateSubtitleSnippet trims to maxSubtitleSnippetRunes, preferring to cut
// at a word boundary so the excerpt does not end mid-word.
func truncateSubtitleSnippet(value string) string {
	runes := []rune(value)
	if len(runes) <= maxSubtitleSnippetRunes {
		return strings.TrimSpace(value)
	}
	cut := runes[:maxSubtitleSnippetRunes]
	if idx := strings.LastIndexByte(string(cut), ' '); idx > 0 {
		cut = cut[:idx]
	}
	return strings.TrimSpace(string(cut))
}
