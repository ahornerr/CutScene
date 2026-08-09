package main

import (
	"fmt"
	"html"
	"strconv"
	"strings"
)

func isNativeSubtitleCodec(codec string) bool {
	switch strings.ToLower(strings.TrimSpace(codec)) {
	case "srt", "subrip", "webvtt", "ass", "ssa":
		return true
	default:
		return false
	}
}

func ParseWebVTT(data []byte) ([]SubtitleEntry, error) {
	text := normalizeSubtitleText(string(data))
	lines := strings.Split(text, "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "WEBVTT" && !strings.HasPrefix(strings.TrimSpace(lines[0]), "WEBVTT ") && !strings.HasPrefix(strings.TrimSpace(lines[0]), "WEBVTT\t") {
		return nil, fmt.Errorf("invalid WebVTT header")
	}

	// Header metadata ends at the first blank line.
	lineIndex := 1
	for lineIndex < len(lines) && strings.TrimSpace(lines[lineIndex]) != "" {
		lineIndex++
	}

	var entries []SubtitleEntry
	for lineIndex < len(lines) {
		for lineIndex < len(lines) && strings.TrimSpace(lines[lineIndex]) == "" {
			lineIndex++
		}
		if lineIndex >= len(lines) {
			break
		}

		blockStart := lineIndex
		for lineIndex < len(lines) && strings.TrimSpace(lines[lineIndex]) != "" {
			lineIndex++
		}
		block := lines[blockStart:lineIndex]
		if isWebVTTNonCueBlock(block[0]) {
			continue
		}

		timingIndex := 0
		if !strings.Contains(block[timingIndex], "-->") {
			timingIndex = 1 // optional cue identifier
		}
		if timingIndex >= len(block) {
			return nil, fmt.Errorf("WebVTT cue is missing timing")
		}

		start, end, err := parseWebVTTTiming(block[timingIndex])
		if err != nil {
			return nil, err
		}
		if timingIndex+1 >= len(block) {
			return nil, fmt.Errorf("WebVTT cue has no text")
		}

		textLines := make([]string, 0, len(block)-timingIndex-1)
		for _, line := range block[timingIndex+1:] {
			textLines = append(textLines, html.UnescapeString(stripWebVTTTags(line)))
		}
		entries = append(entries, SubtitleEntry{
			Start: start,
			End:   end,
			Text:  strings.TrimSpace(strings.Join(textLines, "\n")),
		})
	}

	return entries, nil
}

func parseWebVTTTiming(line string) (int64, int64, error) {
	parts := strings.Split(line, "-->")
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("invalid WebVTT timing line: %q", line)
	}

	start, err := parseWebVTTTimestamp(strings.TrimSpace(parts[0]))
	if err != nil {
		return 0, 0, fmt.Errorf("invalid WebVTT start timestamp: %w", err)
	}
	endFields := strings.Fields(strings.TrimSpace(parts[1]))
	if len(endFields) == 0 {
		return 0, 0, fmt.Errorf("invalid WebVTT end timestamp")
	}
	end, err := parseWebVTTTimestamp(endFields[0])
	if err != nil {
		return 0, 0, fmt.Errorf("invalid WebVTT end timestamp: %w", err)
	}
	if end < start {
		return 0, 0, fmt.Errorf("WebVTT cue ends before it starts")
	}
	return start, end, nil
}

func parseWebVTTTimestamp(value string) (int64, error) {
	parts := strings.Split(value, ":")
	if len(parts) != 2 && len(parts) != 3 {
		return 0, fmt.Errorf("invalid timestamp %q", value)
	}

	secondsParts := strings.Split(parts[len(parts)-1], ".")
	if len(secondsParts) != 2 || secondsParts[1] == "" || len(secondsParts[1]) > 3 {
		return 0, fmt.Errorf("invalid timestamp %q", value)
	}
	seconds, err := strconv.ParseInt(secondsParts[0], 10, 64)
	if err != nil || seconds < 0 || seconds > 59 {
		return 0, fmt.Errorf("invalid seconds in timestamp %q", value)
	}
	fraction := secondsParts[1]
	for len(fraction) < 3 {
		fraction += "0"
	}
	milliseconds, err := strconv.ParseInt(fraction, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid milliseconds in timestamp %q", value)
	}

	minutes, err := strconv.ParseInt(parts[len(parts)-2], 10, 64)
	if err != nil || minutes < 0 || (len(parts) == 3 && minutes > 59) {
		return 0, fmt.Errorf("invalid minutes in timestamp %q", value)
	}
	hours := int64(0)
	if len(parts) == 3 {
		hours, err = strconv.ParseInt(parts[0], 10, 64)
		if err != nil || hours < 0 {
			return 0, fmt.Errorf("invalid hours in timestamp %q", value)
		}
	}

	return hours*3600000 + minutes*60000 + seconds*1000 + milliseconds, nil
}

func stripWebVTTTags(value string) string {
	var builder strings.Builder
	inTag := false
	for _, r := range value {
		switch {
		case r == '<':
			inTag = true
		case r == '>' && inTag:
			inTag = false
		case !inTag:
			builder.WriteRune(r)
		}
	}
	return builder.String()
}

func isWebVTTNonCueBlock(firstLine string) bool {
	fields := strings.Fields(firstLine)
	if len(fields) == 0 {
		return false
	}
	switch strings.ToUpper(fields[0]) {
	case "NOTE", "STYLE", "REGION":
		return true
	default:
		return false
	}
}

func ParseASS(data []byte) ([]SubtitleEntry, error) {
	lines := strings.Split(normalizeSubtitleText(string(data)), "\n")
	section := ""
	inEvents := false
	format := []string(nil)
	startIndex, endIndex, textIndex := -1, -1, -1
	var entries []SubtitleEntry

	for _, rawLine := range lines {
		line := strings.TrimSpace(rawLine)
		if line == "" || strings.HasPrefix(line, ";") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = strings.ToLower(strings.TrimSpace(line[1 : len(line)-1]))
			inEvents = section == "events"
			continue
		}
		if !inEvents {
			continue
		}

		lowerLine := strings.ToLower(line)
		switch {
		case strings.HasPrefix(lowerLine, "format:"):
			format = splitASSFormat(line[len("format:"):])
			for i, field := range format {
				switch strings.ToLower(field) {
				case "start":
					startIndex = i
				case "end":
					endIndex = i
				case "text":
					textIndex = i
				}
			}
			if startIndex < 0 || endIndex < 0 || textIndex < 0 {
				return nil, fmt.Errorf("ASS Events Format must contain Start, End, and Text")
			}
		case strings.HasPrefix(lowerLine, "dialogue:"):
			if len(format) == 0 {
				return nil, fmt.Errorf("ASS Dialogue encountered before Events Format")
			}
			fields, err := splitASSDialogue(strings.TrimSpace(line[len("dialogue:"):]), len(format), textIndex)
			if err != nil {
				return nil, fmt.Errorf("malformed ASS Dialogue line")
			}
			start, err := parseASSTimestamp(strings.TrimSpace(fields[startIndex]))
			if err != nil {
				return nil, fmt.Errorf("invalid ASS start timestamp: %w", err)
			}
			end, err := parseASSTimestamp(strings.TrimSpace(fields[endIndex]))
			if err != nil {
				return nil, fmt.Errorf("invalid ASS end timestamp: %w", err)
			}
			if end < start {
				return nil, fmt.Errorf("ASS dialogue ends before it starts")
			}
			visibleText, err := cleanASSText(fields[textIndex])
			if err != nil {
				return nil, err
			}
			entries = append(entries, SubtitleEntry{Start: start, End: end, Text: visibleText})
		}
	}

	if section == "" || format == nil {
		return nil, fmt.Errorf("ASS Events Format not found")
	}
	return entries, nil
}

func splitASSFormat(value string) []string {
	parts := strings.Split(value, ",")
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	return parts
}

// splitASSDialogue parses fixed-width ASS fields around Text from the left and
// right. This preserves commas in Text even when Text is not the final format
// field; ASS's fixed fields surrounding Text do not contain commas.
func splitASSDialogue(value string, fieldCount, textIndex int) ([]string, error) {
	if fieldCount <= 0 || textIndex < 0 || textIndex >= fieldCount {
		return nil, fmt.Errorf("invalid ASS field layout")
	}

	fields := make([]string, fieldCount)
	remainder := value
	for i := 0; i < textIndex; i++ {
		separator := strings.IndexByte(remainder, ',')
		if separator < 0 {
			return nil, fmt.Errorf("missing ASS field separator")
		}
		fields[i] = remainder[:separator]
		remainder = remainder[separator+1:]
	}

	for i := fieldCount - 1; i > textIndex; i-- {
		separator := strings.LastIndexByte(remainder, ',')
		if separator < 0 {
			return nil, fmt.Errorf("missing ASS field separator")
		}
		fields[i] = remainder[separator+1:]
		remainder = remainder[:separator]
	}
	fields[textIndex] = remainder
	return fields, nil
}

func parseASSTimestamp(value string) (int64, error) {
	parts := strings.Split(value, ":")
	if len(parts) != 3 {
		return 0, fmt.Errorf("invalid timestamp %q", value)
	}
	secondsParts := strings.Split(parts[2], ".")
	if len(secondsParts) != 2 || secondsParts[1] == "" || len(secondsParts[1]) > 3 {
		return 0, fmt.Errorf("invalid timestamp %q", value)
	}
	hours, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || hours < 0 {
		return 0, fmt.Errorf("invalid hours in timestamp %q", value)
	}
	minutes, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || minutes < 0 || minutes > 59 {
		return 0, fmt.Errorf("invalid minutes in timestamp %q", value)
	}
	seconds, err := strconv.ParseInt(secondsParts[0], 10, 64)
	if err != nil || seconds < 0 || seconds > 59 {
		return 0, fmt.Errorf("invalid seconds in timestamp %q", value)
	}
	fraction := secondsParts[1]
	for len(fraction) < 3 {
		fraction += "0"
	}
	milliseconds, err := strconv.ParseInt(fraction, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid milliseconds in timestamp %q", value)
	}
	return hours*3600000 + minutes*60000 + seconds*1000 + milliseconds, nil
}

func cleanASSText(value string) (string, error) {
	var builder strings.Builder
	for i := 0; i < len(value); {
		if value[i] != '{' {
			builder.WriteByte(value[i])
			i++
			continue
		}
		end := strings.IndexByte(value[i+1:], '}')
		if end < 0 {
			return "", fmt.Errorf("unterminated ASS override tag")
		}
		i += end + 2
	}
	return strings.NewReplacer(`\N`, "\n", `\n`, "\n", `\h`, " ").Replace(builder.String()), nil
}

func normalizeSubtitleText(value string) string {
	value = strings.TrimPrefix(value, "\uFEFF")
	value = strings.ReplaceAll(value, "\r\n", "\n")
	return strings.ReplaceAll(value, "\r", "\n")
}
