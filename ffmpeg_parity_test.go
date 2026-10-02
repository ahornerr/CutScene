package main

import (
	"fmt"
	"sort"
	"testing"

	ffmpeg "github.com/u2takey/ffmpeg-go"
)

// keys intentionally allowed to differ between preview and render.
var intentionalPreviewRenderDifferences = map[string]bool{
	// Output container/muxing strategy and source-metadata handling are the
	// structural reasons the two pipelines cannot be fully identical.
	"movflags":     true,
	"map_chapters": true,
	"map_metadata": true,
	"metadata":     true,
	// Height is caller-chosen for renders; previews are fixed at 720p.
	"vf":             true,
	"filter_complex": true,
}

// TestPreviewAndRenderProduceIdenticalArgs is the regression guard for
// preview/render drift. The two pipelines historically carried near-duplicate
// copies of the codec switch, and two NVENC settings (constqp and
// extra_hw_frames) were applied to renders but not previews, so a preview did
// not faithfully represent the file it preceded.
func TestPreviewAndRenderProduceIdenticalArgs(t *testing.T) {
	codecs := []Codec{CodecLibx264, CodecH264VAAPI, CodecH264NVENC}
	subs := []struct {
		name  string
		file  string
		index int
	}{
		{name: "none"},
		{name: "embedded", index: 2},
		{name: "text", file: "/tmp/sub.srt"},
	}

	for _, codec := range codecs {
		for _, sub := range subs {
			// QP is compared at the same value on both sides: preview has no
			// user-selectable QP, so comparing a non-zero render QP against the
			// preview default would compare different intents, not drift.
			for _, qp := range []int{0, 20} {
				name := fmt.Sprintf("%s/%s/qp%d", codec, sub.name, qp)
				t.Run(name, func(t *testing.T) {
					renderIn, renderOut, err := buildArgsForTest(transcodeSpec{
						Codec:              codec,
						Height:             720,
						QP:                 qp,
						AudioMode:          AudioModeStandard,
						SubtitleFile:       sub.file,
						SubtitleIndex:      sub.index,
						SubtitleOffsetMs:   0,
						MetadataTags:       []string{"title=Example"},
						StripInputChapters: true,
						StripInputMetadata: true,
					})
					if err != nil {
						t.Fatalf("render args: %v", err)
					}
					previewIn, previewOut, err := buildArgsForTest(transcodeSpec{
						Codec:            codec,
						Height:           defaultPreviewHeight,
						QP:               qp,
						AudioMode:        AudioModeStandard,
						SubtitleFile:     sub.file,
						SubtitleIndex:    sub.index,
						SubtitleOffsetMs: 0,
						FragmentedOutput: true,
					})
					if err != nil {
						t.Fatalf("preview args: %v", err)
					}
					assertNoDrift(t, "input", renderIn, previewIn)
					assertNoDrift(t, "output", renderOut, previewOut)
				})
			}
		}
	}
}

func buildArgsForTest(spec transcodeSpec) (ffmpeg.KwArgs, ffmpeg.KwArgs, error) {
	input := ffmpeg.KwArgs{"ss": "00:00:00", "to": "00:01:00"}
	output := ffmpeg.KwArgs{}
	if err := buildTranscodeArgs(spec, input, output); err != nil {
		return nil, nil, err
	}
	return input, output, nil
}

func assertNoDrift(t *testing.T, which string, render, preview ffmpeg.KwArgs) {
	t.Helper()
	var keys []string
	for k := range render {
		keys = append(keys, k)
	}
	for k := range preview {
		if _, ok := render[k]; !ok {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	for _, k := range keys {
		if intentionalPreviewRenderDifferences[k] {
			continue
		}
		rv, rok := render[k]
		pv, pok := preview[k]
		if rok != pok {
			t.Errorf("%s arg %q: render=%v (present=%v) preview=%v (present=%v); drift is not intentional",
				which, k, rv, rok, pv, pok)
			continue
		}
		if fmt.Sprint(rv) != fmt.Sprint(pv) {
			t.Errorf("%s arg %q: render=%v preview=%v; drift is not intentional", which, k, rv, pv)
		}
	}
}

// TestTranscodeArgsNVENCDefaults pins the NVENC decode/encode arguments. The
// preview path previously omitted extra_hw_frames and the constqp rate control
// that the render path applied, so an NVENC preview did not represent the
// render it preceded. These assertions exist to catch that class of removal,
// which a preview-vs-render comparison cannot detect once both share one
// builder.
func TestTranscodeArgsNVENCDefaults(t *testing.T) {
	input, output, err := buildArgsForTest(transcodeSpec{
		Codec:            CodecH264NVENC,
		Height:           defaultPreviewHeight,
		FragmentedOutput: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := input["extra_hw_frames"]; got != 8 {
		t.Errorf("input extra_hw_frames = %v, want 8 (NVENC previews need spare hardware frames)", got)
	}
	if got := output["rc"]; got != "constqp" {
		t.Errorf("output rc = %v, want constqp when no QP is requested", got)
	}
	if got := output["qp"]; got != defaultNVENCQP {
		t.Errorf("output qp = %v, want %d", got, defaultNVENCQP)
	}
	if got := output["b:v"]; got != "0K" {
		t.Errorf("output b:v = %v, want 0K under constqp", got)
	}
}

// TestTranscodeArgsNVENCExplicitQPSkipsConstqp ensures an explicit QP is
// honoured rather than being overwritten by the default.
func TestTranscodeArgsNVENCExplicitQPSkipsConstqp(t *testing.T) {
	_, output, err := buildArgsForTest(transcodeSpec{
		Codec: CodecH264NVENC,
		QP:    20,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := output["qp"]; got != 20 {
		t.Errorf("output qp = %v, want the requested 20", got)
	}
	if _, ok := output["rc"]; ok {
		t.Errorf("output rc = %v, want unset when an explicit QP is requested", output["rc"])
	}
}

// TestTranscodeArgsDoesNotEmitQPForLibx264 guards against emitting a qp that
// silently conflicts with (and is ignored by) crf.
func TestTranscodeArgsDoesNotEmitQPForLibx264(t *testing.T) {
	_, output, err := buildArgsForTest(transcodeSpec{Codec: CodecLibx264, QP: 20})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := output["qp"]; ok {
		t.Errorf("output qp = %v, want no qp for libx264 (crf is authoritative)", output["qp"])
	}
	if got := output["crf"]; got != 23 {
		t.Errorf("output crf = %v, want 23", got)
	}
}

// TestTranscodeArgsHonoursQPForHardwareCodecs pins QP as part of the public
// render request contract: the VAAPI and NVENC encoders act on it, so an
// explicitly requested QP must reach them for either codec.
func TestTranscodeArgsHonoursQPForHardwareCodecs(t *testing.T) {
	for _, codec := range []Codec{CodecH264VAAPI, CodecH264NVENC} {
		_, output, err := buildArgsForTest(transcodeSpec{Codec: codec, QP: 20})
		if err != nil {
			t.Fatal(err)
		}
		if got := output["qp"]; got != 20 {
			t.Errorf("%s: output qp = %v, want the requested 20", codec, got)
		}
	}
}

// TestTranscodeArgsPreviewStreamsRenderSeeks pins the container difference that
// justifies keeping the two entry points separate.
func TestTranscodeArgsPreviewStreamsRenderSeeks(t *testing.T) {
	_, previewOut, err := buildArgsForTest(transcodeSpec{Codec: CodecLibx264, FragmentedOutput: true})
	if err != nil {
		t.Fatal(err)
	}
	if got := previewOut["movflags"]; got != "frag_keyframe+empty_moov" {
		t.Errorf("preview movflags = %v, want fragmented streaming flags", got)
	}
	_, renderOut, err := buildArgsForTest(transcodeSpec{Codec: CodecLibx264, StripInputChapters: true, StripInputMetadata: true})
	if err != nil {
		t.Fatal(err)
	}
	if got := renderOut["movflags"]; got != "+use_metadata_tags+faststart" {
		t.Errorf("render movflags = %v, want seekable faststart flags", got)
	}
	if got := renderOut["map_chapters"]; got != -1 {
		t.Errorf("render map_chapters = %v, want -1", got)
	}
}

// TestTranscodeSpecZeroAudioModeIsStandard pins the zero-value AudioMode to
// standard playback. The preview entry points used to take a variadic
// audioModes argument whose zero-argument form defaulted to standard; they now
// take a single AudioMode, so the zero value must keep that meaning.
func TestTranscodeSpecZeroAudioModeIsStandard(t *testing.T) {
	_, output, err := buildArgsForTest(transcodeSpec{Codec: CodecLibx264})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := output["af"]; ok {
		t.Errorf("output af = %v, want no audio filter for the zero-value audio mode", output["af"])
	}
	if _, ok := output["ar"]; ok {
		t.Errorf("output ar = %v, want no output rate for the zero-value audio mode", output["ar"])
	}
}
