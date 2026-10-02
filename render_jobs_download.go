package main

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Download naming for rendered clips.
//
// Builds the Content-Disposition filename from the job spec, honouring title
// byte limits and emitting both an ASCII fallback and an RFC 5987 encoded form.

const maxDownloadTitleBytes = 96

func cleanDownloadTitle(title string) (string, string) {
	var safe, ascii []rune
	for _, r := range title {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' {
			safe = append(safe, r)
			if r < utf8.RuneSelf {
				ascii = append(ascii, r)
			} else {
				ascii = append(ascii, '_')
			}
			continue
		}
		// Convert spaces, punctuation, separators, and control characters to a
		// harmless separator rather than carrying them into a header or path.
		safe = append(safe, '_')
		ascii = append(ascii, '_')
	}

	return boundDownloadTitle(normalizeDownloadTitle(safe)), boundDownloadTitle(normalizeDownloadTitle(ascii))
}

func normalizeDownloadTitle(runes []rune) string {
	var normalized []rune
	separator := false
	for _, r := range runes {
		if r == '_' {
			separator = true
			continue
		}
		if separator && len(normalized) > 0 {
			normalized = append(normalized, '_')
		}
		normalized = append(normalized, r)
		separator = false
	}
	return strings.Trim(string(normalized), "_-")
}

func boundDownloadTitle(title string) string {
	if len(title) <= maxDownloadTitleBytes {
		return title
	}
	var bounded []rune
	bytes := 0
	for _, r := range title {
		runeBytes := utf8.RuneLen(r)
		if bytes+runeBytes > maxDownloadTitleBytes {
			break
		}
		bounded = append(bounded, r)
		bytes += runeBytes
	}
	return strings.Trim(string(bounded), "_-")
}

func downloadRangeTimestamp(ms int64) string {
	if ms < 0 {
		ms = 0
	}
	hours := ms / 3600000
	minutes := (ms / 60000) % 60
	seconds := (ms / 1000) % 60
	return fmt.Sprintf("%02d-%02d-%02d", hours, minutes, seconds)
}

func buildDownloadFilename(spec renderJobSpec, ascii bool) string {
	title, asciiTitle := cleanDownloadTitle(spec.Title)
	if ascii {
		title = asciiTitle
	}
	if title == "" {
		title = "clip"
	}
	return fmt.Sprintf("%s_%s_to_%s.mp4", title, downloadRangeTimestamp(spec.FromMs), downloadRangeTimestamp(spec.ToMs))
}

func renderDownloadContentDisposition(spec renderJobSpec) string {
	filename := buildDownloadFilename(spec, false)
	fallback := buildDownloadFilename(spec, true)
	quote := func(value string) string {
		value = strings.ReplaceAll(value, `\`, `\\`)
		return strings.ReplaceAll(value, `"`, `\"`)
	}
	if filename == fallback {
		return fmt.Sprintf(`attachment; filename="%s"`, quote(fallback))
	}
	return fmt.Sprintf(`attachment; filename="%s"; filename*=UTF-8''%s`, quote(fallback), encodeRFC5987(filename))
}

func encodeRFC5987(value string) string {
	const hex = "0123456789ABCDEF"
	var encoded strings.Builder
	for i := 0; i < len(value); i++ {
		b := value[i]
		if (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') ||
			(b >= '0' && b <= '9') || strings.ContainsRune("!#$&+-.^_`|~", rune(b)) {
			encoded.WriteByte(b)
		} else {
			encoded.WriteByte('%')
			encoded.WriteByte(hex[b>>4])
			encoded.WriteByte(hex[b&0x0f])
		}
	}
	return encoded.String()
}

func (m *renderJobManager) releaseDownload(job *renderJob) {
	job.mu.Lock()
	if job.leaseCount > 0 {
		job.leaseCount--
	}
	remove := job.leaseCount == 0 && job.cleanupWait
	job.mu.Unlock()
	if remove {
		m.deleteJobStorage(job)
	}
}
