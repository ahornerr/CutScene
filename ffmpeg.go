package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	ffmpeg "github.com/u2takey/ffmpeg-go"
	"io"
	"math"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

type Codec string

const (
	CodecH264VAAPI Codec = "h264_vaapi"
	CodecH264NVENC Codec = "h264_nvenc"
	CodecLibx264   Codec = "libx264"
)

type AudioMode string

const (
	AudioModeStandard           AudioMode = "standard"
	AudioModeDialogue           AudioMode = "dialogue"
	AudioModeDialogueNormalized AudioMode = "dialogue_normalized"
)

const dialogueAudioFilter = "pan=stereo|FL<FL+0.85*FC+0.50*BL+0.50*SL|FR<FR+0.85*FC+0.50*BR+0.50*SR,alimiter=limit=0.95:attack=5:release=50:latency=1:level=0"

var ErrNoUsableSubtitleCues = errors.New("no usable subtitle cues")

type subtitleSRTWriteError struct {
	operation string
	err       error
}

func (e *subtitleSRTWriteError) Error() string {
	return fmt.Sprintf("subtitle SRT %s failed: %v", e.operation, e.err)
}

func (e *subtitleSRTWriteError) Unwrap() error { return e.err }

func subtitleSRTIOError(operation string, err error) error {
	if err == nil {
		return nil
	}
	return &subtitleSRTWriteError{operation: operation, err: err}
}

func escapeFFmpegFilterFilename(path string) string {
	var escaped strings.Builder
	escaped.WriteByte('\'')
	for _, r := range path {
		switch r {
		case '\\', '\'', ':', ',', ';', '[', ']', '=':
			escaped.WriteByte('\\')
		}
		escaped.WriteRune(r)
	}
	escaped.WriteByte('\'')
	return escaped.String()
}

func subtitlesFilter(filename string) string {
	return "subtitles=filename=" + escapeFFmpegFilterFilename(filename)
}

// Subtitle offsets are deliberately bounded to keep timestamp arithmetic
// predictable and to match the maximum supported clip duration.
const maxSubtitleOffsetMs int64 = 15 * 60 * 1000

func validateSubtitleOffsetMs(offset int64) error {
	if offset < -maxSubtitleOffsetMs || offset > maxSubtitleOffsetMs {
		return fmt.Errorf("subtitleOffsetMs must be between -%d and %d", maxSubtitleOffsetMs, maxSubtitleOffsetMs)
	}
	return nil
}

func parseSubtitleOffsetMs(value string) (int64, error) {
	if value == "" {
		return 0, nil
	}
	offset, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid subtitleOffsetMs: %w", err)
	}
	if err := validateSubtitleOffsetMs(offset); err != nil {
		return 0, err
	}
	return offset, nil
}

func saturatingAddInt64(a, b int64) int64 {
	if b > 0 && a > math.MaxInt64-b {
		return math.MaxInt64
	}
	if b < 0 && a < math.MinInt64-b {
		return math.MinInt64
	}
	return a + b
}

func parseAudioMode(value string) (AudioMode, error) {
	if value == "" {
		return AudioModeStandard, nil
	}
	mode := AudioMode(value)
	switch mode {
	case AudioModeStandard, AudioModeDialogue, AudioModeDialogueNormalized:
		return mode, nil
	default:
		return "", fmt.Errorf("unsupported audio mode %q", value)
	}
}

func configureAudioOutput(outputArgs ffmpeg.KwArgs, mode AudioMode) error {
	parsedMode, err := parseAudioMode(string(mode))
	if err != nil {
		return err
	}
	if parsedMode == AudioModeStandard {
		return nil
	}

	filter := dialogueAudioFilter
	if parsedMode == AudioModeDialogueNormalized {
		filter += ",loudnorm=I=-16:LRA=11:TP=-1.5:linear=false:print_format=none,aresample=48000"
		outputArgs["ar"] = 48000
	}
	outputArgs["af"] = filter
	return nil
}

type boundedBuffer struct {
	buf       bytes.Buffer
	max       int
	truncated bool
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	if b.max <= 0 || b.buf.Len() >= b.max {
		b.truncated = true
		return len(p), nil
	}
	remaining := b.max - b.buf.Len()
	if len(p) > remaining {
		_, _ = b.buf.Write(p[:remaining])
		b.truncated = true
		return len(p), nil
	}
	return b.buf.Write(p)
}

func (b *boundedBuffer) String() string {
	value := b.buf.String()
	if b.truncated {
		value += "…[truncated]"
	}
	return value
}

func init() {
	// Compiled commands include authenticated source URLs; never log them.
	ffmpeg.LogCompiledCommand = false
}

type FfmpegParams struct {
	URL              string
	From             string
	To               string
	Filename         string
	Height           int
	QP               int
	Codec            Codec
	AudioMode        AudioMode
	SubtitleFile     string
	SubtitleIndex    int // >= 0 for PGS overlay via overlay filter
	SubtitleOffsetMs int64
	OutputPath       string
	Context          context.Context
	Metadata         FfmpegParamsMetadata
}

func subtitleOverlayFilter(index int, offsetMs int64, suffix string) string {
	if offsetMs == 0 {
		return fmt.Sprintf("[0:v][0:s:%d]overlay%s", index, suffix)
	}
	return fmt.Sprintf("[0:s:%d]setpts=PTS%+d/1000/TB[sub];[0:v][sub]overlay%s", index, offsetMs, suffix)
}

func configureFFmpegHTTPRecovery(inputArgs ffmpeg.KwArgs, rawURL string) {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return
	}
	inputArgs["reconnect"] = "1"
	inputArgs["reconnect_streamed"] = "1"
	inputArgs["reconnect_on_network_error"] = "1"
	inputArgs["reconnect_on_http_error"] = "502,503,504"
	inputArgs["reconnect_delay_max"] = "2"
	inputArgs["rw_timeout"] = "15000000"
}

type FfmpegParamsMetadata struct {
	Title        string
	Show         string
	SeasonNumber int
	EpisodeID    int
	Year         int
}

func configureNVENCTextSubtitle(inputArgs, outputArgs ffmpeg.KwArgs, subtitleFile string, height int) {
	// Text subtitles are rendered in software. Do not ask FFmpeg to decode the
	// source into CUDA frames before the subtitles filter runs; this also keeps
	// Main10 sources available to the software filter as 8-bit frames.
	delete(inputArgs, "hwaccel")
	delete(inputArgs, "hwaccel_output_format")
	delete(inputArgs, "extra_hw_frames")

	vf := fmt.Sprintf("%s,format=yuv420p,hwupload_cuda", subtitlesFilter(subtitleFile))
	if height > 0 {
		vf += fmt.Sprintf(",scale_cuda=-2:%d", height)
	}
	outputArgs["vf"] = vf
}

func scaleSoftwareFilter(height int) string {
	if height <= 0 {
		return ""
	}
	return fmt.Sprintf("scale=-2:%d", height)
}

func scaleCUDAFilter(height int) string {
	if height <= 0 {
		return ""
	}
	return fmt.Sprintf("scale_cuda=-2:%d", height)
}

func scaleVAAPIFilter(height int) string {
	if height <= 0 {
		return "scale_vaapi=format=nv12"
	}
	return fmt.Sprintf("scale_vaapi=format=nv12,scale_vaapi=-2:%d", height)
}

func configureMP4Output(outputArgs ffmpeg.KwArgs) {
	outputArgs["f"] = "mp4"
}

func DoFfmpeg(params FfmpegParams) (string, error) {
	if err := validateSubtitleOffsetMs(params.SubtitleOffsetMs); err != nil {
		return params.OutputPath, err
	}
	if params.SubtitleFile != "" {
		if err := validateSubtitleFileForBurn(params.SubtitleFile); err != nil {
			if errors.Is(err, ErrNoUsableSubtitleCues) {
				params.SubtitleFile = ""
			} else {
				return params.OutputPath, errors.New("subtitle file preflight failed")
			}
		}
	}
	outputMetadata := map[string]string{
		"title":   params.Metadata.Title,
		"comment": params.From,
	}

	if params.Metadata.Show != "" {
		outputMetadata["show"] = params.Metadata.Show
	}
	if params.Metadata.SeasonNumber != 0 {
		outputMetadata["season_number"] = strconv.Itoa(params.Metadata.SeasonNumber)
	}
	if params.Metadata.EpisodeID != 0 {
		outputMetadata["episode_id"] = strconv.Itoa(params.Metadata.EpisodeID)
	}
	if params.Metadata.Year != 0 {
		outputMetadata["year"] = strconv.Itoa(params.Metadata.Year)
	}

	var metadataArr []string
	for k, v := range outputMetadata {
		metadataArr = append(metadataArr, fmt.Sprintf("%s=%s", k, v))
	}

	tmpFile := params.OutputPath
	if tmpFile == "" {
		tmpFile = filepath.Join("/tmp", params.Filename)
	}

	inputArgs := ffmpeg.KwArgs{
		"ss":      params.From,
		"to":      params.To,
		"hwaccel": "auto",
		// TODO: Make these two configurable, we don't want them when trying to troubleshoot
		"hide_banner": "",
		"loglevel":    "error",
	}

	switch params.Codec {
	case CodecH264VAAPI:
		inputArgs["hwaccel"] = "vaapi"
		inputArgs["hwaccel_device"] = "/dev/dri/renderD128"
		inputArgs["hwaccel_output_format"] = "vaapi"
	case CodecH264NVENC:
		inputArgs["hwaccel"] = "cuda"
		inputArgs["extra_hw_frames"] = 8
	case CodecLibx264:
		fallthrough
	default:
	}

	// TODO: Might be a good idea to make these configurable or add support for presets
	outputArgs := ffmpeg.KwArgs{
		"acodec":       "aac",
		"ac":           2,
		"b:a":          "192k",
		"map_chapters": -1,
		"map_metadata": 0,
		"movflags":     "+use_metadata_tags+faststart",
		"metadata":     metadataArr,
		"qp":           params.QP,
	}
	if err := configureAudioOutput(outputArgs, params.AudioMode); err != nil {
		return tmpFile, err
	}
	configureMP4Output(outputArgs)

	outputArgs["vcodec"] = params.Codec

	switch params.Codec {
	case CodecH264VAAPI:
		if params.SubtitleIndex >= 0 {
			outputArgs["filter_complex"] = subtitleOverlayFilter(params.SubtitleIndex, params.SubtitleOffsetMs, ",format=nv12,hwupload,"+scaleVAAPIFilter(params.Height)+"[out]")
			outputArgs["map"] = []string{"[out]", "0:a:0?"}
			delete(inputArgs, "hwaccel_output_format")
		} else if params.SubtitleFile != "" {
			delete(inputArgs, "hwaccel_output_format")
			outputArgs["vf"] = fmt.Sprintf("%s,format=nv12,hwupload,%s", subtitlesFilter(params.SubtitleFile), scaleVAAPIFilter(params.Height))
		} else {
			outputArgs["vf"] = "hwupload," + scaleVAAPIFilter(params.Height)
		}
		outputArgs["compression_level"] = "0"
	case CodecH264NVENC:
		if params.SubtitleIndex >= 0 {
			if params.Height > 0 {
				outputArgs["filter_complex"] = subtitleOverlayFilter(params.SubtitleIndex, params.SubtitleOffsetMs, fmt.Sprintf(",hwupload_cuda,scale_cuda=-2:%d[out]", params.Height))
			} else {
				outputArgs["filter_complex"] = subtitleOverlayFilter(params.SubtitleIndex, params.SubtitleOffsetMs, ",hwupload_cuda[out]")
			}
			outputArgs["map"] = []string{"[out]", "0:a:0?"}
		} else if params.SubtitleFile != "" {
			configureNVENCTextSubtitle(inputArgs, outputArgs, params.SubtitleFile, params.Height)
		} else {
			inputArgs["hwaccel_output_format"] = "cuda"
			if params.Height > 0 {
				outputArgs["vf"] = "scale_cuda=-2:" + strconv.Itoa(params.Height)
			}
		}
		if params.QP == 0 {
			outputArgs["rc"] = "constqp"
			outputArgs["qp"] = 24
			outputArgs["b:v"] = "0K"
		}
	case CodecLibx264:
		fallthrough
	default:
		if params.SubtitleIndex >= 0 {
			suffix := "[out]"
			if filter := scaleSoftwareFilter(params.Height); filter != "" {
				suffix = "," + filter + suffix
			}
			outputArgs["filter_complex"] = subtitleOverlayFilter(params.SubtitleIndex, params.SubtitleOffsetMs, suffix)
			outputArgs["map"] = []string{"[out]", "0:a:0?"}
		} else {
			vf := scaleSoftwareFilter(params.Height)
			if params.SubtitleFile != "" {
				if vf != "" {
					vf += ","
				}
				vf += subtitlesFilter(params.SubtitleFile)
			}
			if vf != "" {
				outputArgs["vf"] = vf
			}
		}
		outputArgs["pix_fmt"] = "yuv420p"
		outputArgs["crf"] = 23
		outputArgs["video_bitrate"] = 0
		outputArgs["tune"] = "film"
	}

	configureFFmpegHTTPRecovery(inputArgs, params.URL)
	errBuff := &boundedBuffer{max: 64 << 10}
	input := ffmpeg.Input(params.URL, inputArgs)
	var output *ffmpeg.Stream
	if params.Context != nil {
		output = ffmpeg.OutputContext(params.Context, []*ffmpeg.Stream{input}, tmpFile, outputArgs)
	} else {
		output = input.Output(tmpFile, outputArgs)
	}
	err := output.
		OverWriteOutput().
		WithErrorOutput(errBuff).
		WithOutput(os.Stdout).
		Run()
	if params.Context != nil && params.Context.Err() != nil {
		err = params.Context.Err()
	}

	// Capture the ffmpeg process stderr if it exits unsuccessfully
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		err = fmt.Errorf("ffmpeg exited with error:\n%s", errBuff.String())
	}

	if diagnostic := redactedDiagnostic(errors.New(errBuff.String())); diagnostic != "" {
		fmt.Fprintln(os.Stderr, diagnostic)
	}

	return tmpFile, err
}

func DoFfmpegPreview(fileURL, from, to string, subtitleFile string, subtitleIndex int, codec Codec, writer io.Writer, audioModes ...AudioMode) error {
	return DoFfmpegPreviewContext(context.Background(), fileURL, from, to, subtitleFile, subtitleIndex, codec, writer, audioModes...)
}

// previewClientDisconnectWriter turns a response write failure into
// cancellation of the FFmpeg context. Without this, FFmpeg can keep running
// after fasthttp has closed the response pipe and hold the encoder limiter.
type previewClientDisconnectWriter struct {
	writer       *bufio.Writer
	cancel       context.CancelFunc
	disconnected atomic.Bool
}

func (w *previewClientDisconnectWriter) Write(p []byte) (int, error) {
	n, err := w.writer.Write(p)
	if err != nil {
		w.disconnected.Store(true)
		w.cancel()
	}
	return n, err
}

func (w *previewClientDisconnectWriter) Flush() error {
	err := w.writer.Flush()
	if err != nil {
		w.disconnected.Store(true)
		w.cancel()
	}
	return err
}

func (w *previewClientDisconnectWriter) clientDisconnected() bool {
	return w.disconnected.Load()
}

func previewWriterDisconnected(writer io.Writer) bool {
	disconnectAware, ok := writer.(interface{ clientDisconnected() bool })
	return ok && disconnectAware.clientDisconnected()
}

func isPreviewClientDisconnectError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, io.ErrClosedPipe) {
		return true
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "broken pipe") ||
		strings.Contains(message, "connection reset by peer") ||
		strings.Contains(message, "use of closed network connection")
}

func isExpectedPreviewTermination(err error, writer io.Writer) bool {
	return errors.Is(err, context.Canceled) ||
		previewWriterDisconnected(writer) ||
		isPreviewClientDisconnectError(err)
}

func DoFfmpegPreviewContext(ctx context.Context, fileURL, from, to string, subtitleFile string, subtitleIndex int, codec Codec, writer io.Writer, audioModes ...AudioMode) error {
	return doFfmpegPreviewContext(ctx, fileURL, from, to, subtitleFile, subtitleIndex, codec, writer, 0, audioModes...)
}

func DoFfmpegPreviewContextWithSubtitleOffset(ctx context.Context, fileURL, from, to string, subtitleFile string, subtitleIndex int, codec Codec, writer io.Writer, audioMode AudioMode, subtitleOffsetMs int64) error {
	return doFfmpegPreviewContext(ctx, fileURL, from, to, subtitleFile, subtitleIndex, codec, writer, subtitleOffsetMs, audioMode)
}

func doFfmpegPreviewContext(ctx context.Context, fileURL, from, to string, subtitleFile string, subtitleIndex int, codec Codec, writer io.Writer, subtitleOffsetMs int64, audioModes ...AudioMode) error {
	if err := validateSubtitleOffsetMs(subtitleOffsetMs); err != nil {
		return err
	}
	if subtitleFile != "" {
		if err := validateSubtitleFileForBurn(subtitleFile); err != nil {
			if errors.Is(err, ErrNoUsableSubtitleCues) {
				subtitleFile = ""
			} else {
				return errors.New("subtitle file preflight failed")
			}
		}
	}
	inputArgs := ffmpeg.KwArgs{
		"ss":          from,
		"to":          to,
		"hwaccel":     "auto",
		"hide_banner": "",
		"loglevel":    "error",
	}

	switch codec {
	case CodecH264VAAPI:
		inputArgs["hwaccel"] = "vaapi"
		inputArgs["hwaccel_device"] = "/dev/dri/renderD128"
		inputArgs["hwaccel_output_format"] = "vaapi"
	case CodecH264NVENC:
		inputArgs["hwaccel"] = "cuda"
	case CodecLibx264:
		fallthrough
	default:
	}

	outputArgs := ffmpeg.KwArgs{
		"acodec":   "aac",
		"ac":       2,
		"b:a":      "192k",
		"f":        "mp4",
		"movflags": "frag_keyframe+empty_moov",
	}
	audioMode := AudioModeStandard
	if len(audioModes) > 1 {
		return errors.New("multiple audio modes specified")
	} else if len(audioModes) == 1 {
		audioMode = audioModes[0]
	}
	if err := configureAudioOutput(outputArgs, audioMode); err != nil {
		return err
	}

	outputArgs["vcodec"] = codec

	height := 720

	switch codec {
	case CodecH264VAAPI:
		if subtitleIndex >= 0 {
			outputArgs["filter_complex"] = subtitleOverlayFilter(subtitleIndex, subtitleOffsetMs, ",format=nv12,hwupload,"+scaleVAAPIFilter(height)+"[out]")
			outputArgs["map"] = []string{"[out]", "0:a:0?"}
			delete(inputArgs, "hwaccel_output_format")
		} else if subtitleFile != "" {
			delete(inputArgs, "hwaccel_output_format")
			outputArgs["vf"] = fmt.Sprintf("%s,format=nv12,hwupload,%s", subtitlesFilter(subtitleFile), scaleVAAPIFilter(height))
		} else {
			outputArgs["vf"] = "hwupload," + scaleVAAPIFilter(height)
		}
		outputArgs["compression_level"] = "0"
	case CodecH264NVENC:
		if subtitleIndex >= 0 {
			if height > 0 {
				outputArgs["filter_complex"] = subtitleOverlayFilter(subtitleIndex, subtitleOffsetMs, ",hwupload_cuda,"+scaleCUDAFilter(height)+"[out]")
			} else {
				outputArgs["filter_complex"] = subtitleOverlayFilter(subtitleIndex, subtitleOffsetMs, ",hwupload_cuda[out]")
			}
			outputArgs["map"] = []string{"[out]", "0:a:0?"}
		} else if subtitleFile != "" {
			configureNVENCTextSubtitle(inputArgs, outputArgs, subtitleFile, height)
		} else {
			inputArgs["hwaccel_output_format"] = "cuda"
			if filter := scaleCUDAFilter(height); filter != "" {
				outputArgs["vf"] = filter
			}
		}
	case CodecLibx264:
		fallthrough
	default:
		if subtitleIndex >= 0 {
			suffix := "[out]"
			if filter := scaleSoftwareFilter(height); filter != "" {
				suffix = "," + filter + suffix
			}
			outputArgs["filter_complex"] = subtitleOverlayFilter(subtitleIndex, subtitleOffsetMs, suffix)
			outputArgs["map"] = []string{"[out]", "0:a:0?"}
		} else {
			vf := scaleSoftwareFilter(height)
			if subtitleFile != "" {
				if vf != "" {
					vf += ","
				}
				vf += subtitlesFilter(subtitleFile)
			}
			if vf != "" {
				outputArgs["vf"] = vf
			}
		}
		outputArgs["pix_fmt"] = "yuv420p"
		outputArgs["crf"] = 23
		outputArgs["video_bitrate"] = 0
		outputArgs["tune"] = "film"
	}

	configureFFmpegHTTPRecovery(inputArgs, fileURL)
	errBuff := &boundedBuffer{max: 64 << 10}
	input := ffmpeg.Input(fileURL, inputArgs)
	output := ffmpeg.OutputContext(ctx, []*ffmpeg.Stream{input}, "pipe:", outputArgs)
	err := output.
		WithOutput(writer, errBuff).
		Run()
	if ctx.Err() != nil {
		err = ctx.Err()
	}

	// Capture the ffmpeg process stderr if it exits unsuccessfully
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		err = fmt.Errorf("ffmpeg exited with error:\n%s", errBuff.String())
	}

	if diagnostic := redactedDiagnostic(errors.New(errBuff.String())); diagnostic != "" && !isExpectedPreviewTermination(err, writer) {
		fmt.Fprintln(os.Stderr, diagnostic)
	}

	return err
}

// ExtractSubtitleFull extracts the entire subtitle track at subtitleIndex into a
// temp SRT file with absolute timestamps (relative to the start of the video).
// Use this when you need all subtitle entries for browsing/searching.
func ExtractSubtitleFull(url string, subtitleIndex int) (string, error) {
	return ExtractSubtitleFullContext(context.Background(), url, subtitleIndex)
}

func ExtractSubtitleFullContext(ctx context.Context, url string, subtitleIndex int) (string, error) {
	tmpFile := fmt.Sprintf("/tmp/cutscene_subfull_%d.srt", time.Now().UnixNano())

	inputArgs := ffmpeg.KwArgs{
		"hide_banner": "",
		"loglevel":    "error",
	}

	outputArgs := ffmpeg.KwArgs{
		"map": fmt.Sprintf("0:s:%d", subtitleIndex),
		"c:s": "srt",
	}

	configureFFmpegHTTPRecovery(inputArgs, url)
	errBuff := &boundedBuffer{max: 64 << 10}
	input := ffmpeg.Input(url, inputArgs)
	output := ffmpeg.OutputContext(ctx, []*ffmpeg.Stream{input}, tmpFile, outputArgs)
	err := output.
		OverWriteOutput().
		WithErrorOutput(errBuff).
		Run()
	if ctx.Err() != nil {
		err = ctx.Err()
	}

	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		_ = os.Remove(tmpFile)
		return "", fmt.Errorf("subtitle extraction failed:\n%s", errBuff.String())
	}
	if err != nil {
		_ = os.Remove(tmpFile)
		return "", err
	}
	if err := validateSubtitleFileForBurn(tmpFile); err != nil {
		_ = os.Remove(tmpFile)
		return "", err
	}
	if diagnostic := redactedDiagnostic(errors.New(errBuff.String())); diagnostic != "" {
		fmt.Fprintln(os.Stderr, diagnostic)
	}

	return tmpFile, err
}

// ParseSRT parses a SubRip (.srt) file and returns the subtitle entries with
// millisecond timestamps. Multi-line text blocks are joined with newlines.
func ParseSRT(filename string) ([]SubtitleEntry, error) {
	f, err := os.Open(filename)
	if err != nil {
		return nil, subtitleSRTIOError("open", err)
	}
	defer f.Close()

	var entries []SubtitleEntry
	var start, end int64
	var textLines []string

	// state: 0 = awaiting sequence number, 1 = awaiting timestamp, 2 = collecting text
	state := 0

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimRight(scanner.Text(), "\r")

		switch state {
		case 0: // awaiting sequence number
			if strings.TrimSpace(line) != "" {
				state = 1
			}
		case 1: // awaiting timestamp line
			parts := strings.SplitN(line, " --> ", 2)
			if len(parts) == 2 {
				var startErr, endErr error
				start, startErr = parseSRTTimestamp(strings.TrimSpace(parts[0]))
				end, endErr = parseSRTTimestamp(strings.TrimSpace(parts[1]))
				if startErr != nil || endErr != nil || end <= start {
					return entries, errors.New("invalid SRT cue timestamp")
				}
				textLines = textLines[:0]
				state = 2
			}
		case 2: // collecting text lines
			if strings.TrimSpace(line) == "" {
				if len(textLines) > 0 {
					entries = append(entries, SubtitleEntry{
						Start: start,
						End:   end,
						Text:  strings.Join(textLines, "\n"),
					})
				}
				state = 0
			} else {
				textLines = append(textLines, line)
			}
		}
	}

	// flush any pending entry at EOF (files without trailing blank line)
	if state == 2 && len(textLines) > 0 {
		entries = append(entries, SubtitleEntry{
			Start: start,
			End:   end,
			Text:  strings.Join(textLines, "\n"),
		})
	}

	if err := scanner.Err(); err != nil {
		return entries, subtitleSRTIOError("read", err)
	}
	return entries, nil
}

func validateSubtitleFileForBurn(filename string) error {
	info, err := os.Stat(filename)
	if err != nil {
		return errors.New("subtitle file is unavailable")
	}
	if !info.Mode().IsRegular() || info.Size() == 0 {
		return ErrNoUsableSubtitleCues
	}
	entries, err := ParseSRT(filename)
	if err != nil {
		var ioErr *subtitleSRTWriteError
		if errors.As(err, &ioErr) {
			return errors.New("subtitle file is unavailable")
		}
		return ErrNoUsableSubtitleCues
	}
	for _, entry := range entries {
		if entry.End > entry.Start && strings.TrimSpace(entry.Text) != "" {
			return nil
		}
	}
	return ErrNoUsableSubtitleCues
}

// parseSRTTimestamp converts an SRT timestamp string "HH:MM:SS,mmm" to milliseconds.
func parseSRTTimestamp(s string) (int64, error) {
	// Strip anything after space (e.g. position tags: "00:01:23,456 X1:0 Y1:0")
	if i := strings.Index(s, " "); i >= 0 {
		s = s[:i]
	}
	comma := strings.SplitN(s, ",", 2)
	if len(comma) != 2 {
		return 0, fmt.Errorf("invalid SRT timestamp: %s", s)
	}
	hms := strings.SplitN(comma[0], ":", 3)
	if len(hms) != 3 {
		return 0, fmt.Errorf("invalid SRT timestamp: %s", s)
	}
	h, err1 := strconv.ParseInt(hms[0], 10, 64)
	m, err2 := strconv.ParseInt(hms[1], 10, 64)
	sec, err3 := strconv.ParseInt(hms[2], 10, 64)
	ms, err4 := strconv.ParseInt(comma[1], 10, 64)
	for _, e := range []error{err1, err2, err3, err4} {
		if e != nil {
			return 0, fmt.Errorf("invalid SRT timestamp %q: %w", s, e)
		}
	}
	return h*3600000 + m*60000 + sec*1000 + ms, nil
}

// ExtractSubtitle extracts the subtitle stream at subtitleIndex (0-based among
// subtitle streams) for the given time range and writes it to a temp SRT file.
// The timestamps in the output file are relative to from, so they align with
// the clip produced by DoFfmpeg for the same range.
func ExtractSubtitle(url, from, to string, subtitleIndex int, subtitleOffsets ...int64) (string, error) {
	return ExtractSubtitleContext(context.Background(), url, from, to, subtitleIndex, subtitleOffsets...)
}

func ExtractSubtitleContext(ctx context.Context, url, from, to string, subtitleIndex int, subtitleOffsets ...int64) (string, error) {
	offset, err := subtitleOffsetArgument(subtitleOffsets)
	if err != nil {
		return "", err
	}
	if offset == 0 {
		return extractSubtitleContextRaw(ctx, url, from, to, subtitleIndex)
	}

	fromMs, err := ParseTimestampToMs(from)
	if err != nil {
		return "", fmt.Errorf("invalid subtitle extraction start: %w", err)
	}
	toMs, err := ParseTimestampToMs(to)
	if err != nil || toMs <= fromMs {
		return "", fmt.Errorf("invalid subtitle extraction range")
	}
	// A shifted subtitle in the requested clip comes from this source range.
	// Avoid passing negative seek times to FFmpeg; entries before source zero
	// cannot exist and are consequently absent from the result.
	extractFrom := saturatingAddInt64(fromMs, -offset)
	extractTo := saturatingAddInt64(toMs, -offset)
	if extractFrom < 0 {
		extractFrom = 0
	}
	if extractTo <= extractFrom {
		return WriteClipSRT(nil, fromMs, toMs, offset)
	}

	rawFile, err := extractSubtitleContextRaw(ctx, url, formatRenderTimestamp(extractFrom), formatRenderTimestamp(extractTo), subtitleIndex)
	if err != nil {
		return "", err
	}
	defer os.Remove(rawFile)
	entries, err := ParseSRT(rawFile)
	if err != nil {
		return "", fmt.Errorf("could not parse extracted subtitle: %w", err)
	}
	// Raw extraction timestamps are relative to extractFrom. Convert them back
	// to source time, then apply the same clipping path used by external and
	// cached subtitles. This keeps all subtitle types semantically identical.
	for i := range entries {
		entries[i].Start = saturatingAddInt64(entries[i].Start, extractFrom)
		entries[i].End = saturatingAddInt64(entries[i].End, extractFrom)
	}
	return WriteClipSRT(entries, fromMs, toMs, offset)
}

func subtitleOffsetArgument(offsets []int64) (int64, error) {
	if len(offsets) > 1 {
		return 0, errors.New("multiple subtitle offsets specified")
	}
	if len(offsets) == 0 {
		return 0, nil
	}
	if err := validateSubtitleOffsetMs(offsets[0]); err != nil {
		return 0, err
	}
	return offsets[0], nil
}

func extractSubtitleContextRaw(ctx context.Context, url, from, to string, subtitleIndex int) (string, error) {
	tmpFile := fmt.Sprintf("/tmp/cutscene_sub_%d.srt", time.Now().UnixNano())

	inputArgs := ffmpeg.KwArgs{
		"hide_banner": "",
		"loglevel":    "error",
	}

	outputArgs := ffmpeg.KwArgs{
		"ss":  from,
		"to":  to,
		"map": fmt.Sprintf("0:s:%d", subtitleIndex),
		"c:s": "srt",
	}

	configureFFmpegHTTPRecovery(inputArgs, url)
	errBuff := &boundedBuffer{max: 64 << 10}
	input := ffmpeg.Input(url, inputArgs)
	output := ffmpeg.OutputContext(ctx, []*ffmpeg.Stream{input}, tmpFile, outputArgs)
	err := output.
		OverWriteOutput().
		WithErrorOutput(errBuff).
		Run()
	if ctx.Err() != nil {
		err = ctx.Err()
	}

	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		_ = os.Remove(tmpFile)
		return "", fmt.Errorf("subtitle extraction failed:\n%s", errBuff.String())
	}
	if err != nil {
		_ = os.Remove(tmpFile)
		return "", err
	}
	if err := validateSubtitleFileForBurn(tmpFile); err != nil {
		_ = os.Remove(tmpFile)
		return "", err
	}
	if diagnostic := redactedDiagnostic(errors.New(errBuff.String())); diagnostic != "" {
		fmt.Fprintln(os.Stderr, diagnostic)
	}

	return tmpFile, err
}

func ParseTimestampToMs(ts string) (int64, error) {
	if !strings.Contains(ts, ":") {
		s, err := strconv.ParseFloat(ts, 64)
		if err != nil || math.IsNaN(s) || math.IsInf(s, 0) || s < 0 || s > float64(math.MaxInt64)/1000 {
			return 0, fmt.Errorf("invalid timestamp format: %s", ts)
		}
		return int64(s * 1000), nil
	}
	parts := strings.Split(ts, ":")
	if len(parts) != 3 {
		return 0, fmt.Errorf("invalid timestamp format: %s", ts)
	}
	h, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid hours in timestamp: %w", err)
	}
	if h < 0 {
		return 0, fmt.Errorf("invalid hours in timestamp: %s", ts)
	}
	m, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid minutes in timestamp: %w", err)
	}
	if m < 0 || m >= 60 {
		return 0, fmt.Errorf("invalid minutes in timestamp: %s", ts)
	}
	secParts := strings.Split(parts[2], ",")
	if len(secParts) == 1 {
		secParts = strings.Split(parts[2], ".")
	}
	s, err := strconv.ParseInt(secParts[0], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid seconds in timestamp: %w", err)
	}
	if s < 0 || s >= 60 {
		return 0, fmt.Errorf("invalid seconds in timestamp: %s", ts)
	}
	ms := int64(0)
	if len(secParts) > 1 {
		msStr := secParts[1]
		if len(secParts) != 2 || msStr == "" || len(msStr) > 3 {
			return 0, fmt.Errorf("invalid milliseconds in timestamp: %s", ts)
		}
		for len(msStr) < 3 {
			msStr += "0"
		}
		ms, err = strconv.ParseInt(msStr, 10, 64)
		if err != nil {
			return 0, fmt.Errorf("invalid milliseconds in timestamp: %w", err)
		}
	}
	return h*3600000 + m*60000 + s*1000 + ms, nil
}

func formatSRTTimestamp(ms int64) string {
	h := ms / 3600000
	ms %= 3600000
	m := ms / 60000
	ms %= 60000
	s := ms / 1000
	ms %= 1000
	return fmt.Sprintf("%02d:%02d:%02d,%03d", h, m, s, ms)
}

func WriteClipSRT(entries []SubtitleEntry, fromMs, toMs int64, subtitleOffsets ...int64) (string, error) {
	offset, err := subtitleOffsetArgument(subtitleOffsets)
	if err != nil {
		return "", err
	}
	if fromMs < 0 || toMs <= fromMs {
		return "", errors.New("invalid subtitle clip range")
	}
	type clippedCue struct {
		start int64
		end   int64
		text  string
	}
	cues := make([]clippedCue, 0, len(entries))
	for _, entry := range entries {
		startMs := saturatingAddInt64(entry.Start, offset)
		endMs := saturatingAddInt64(entry.End, offset)
		if endMs <= fromMs || startMs >= toMs || endMs <= startMs || strings.TrimSpace(entry.Text) == "" {
			continue
		}
		start := startMs - fromMs
		if start < 0 {
			start = 0
		}
		end := endMs - fromMs
		if end > toMs-fromMs {
			end = toMs - fromMs
		}
		if end <= start {
			continue
		}
		cues = append(cues, clippedCue{start: start, end: end, text: entry.Text})
	}
	if len(cues) == 0 {
		return "", ErrNoUsableSubtitleCues
	}
	tmpFile, err := os.CreateTemp("", "cutscene_clip_*.srt")
	if err != nil {
		return "", err
	}
	tmpFilePath := tmpFile.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = tmpFile.Close()
			_ = os.Remove(tmpFilePath)
		}
	}()
	for seq, cue := range cues {
		if _, err := fmt.Fprintf(tmpFile, "%d\n%s --> %s\n%s\n\n", seq+1, formatSRTTimestamp(cue.start), formatSRTTimestamp(cue.end), cue.text); err != nil {
			return "", subtitleSRTIOError("write", err)
		}
	}
	if err := tmpFile.Sync(); err != nil {
		return "", subtitleSRTIOError("sync", err)
	}
	if err := tmpFile.Close(); err != nil {
		return "", subtitleSRTIOError("close", err)
	}
	cleanup = false
	if err := validateSubtitleFileForBurn(tmpFilePath); err != nil {
		_ = os.Remove(tmpFilePath)
		return "", err
	}
	return tmpFilePath, nil
}
