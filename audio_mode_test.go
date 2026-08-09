package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	ffmpeg "github.com/u2takey/ffmpeg-go"
)

func TestParseAudioModeStrictlyAcceptsContractValues(t *testing.T) {
	tests := []struct {
		input string
		want  AudioMode
		ok    bool
	}{
		{input: "", want: AudioModeStandard, ok: true},
		{input: "standard", want: AudioModeStandard, ok: true},
		{input: "dialogue", want: AudioModeDialogue, ok: true},
		{input: "dialogue_normalized", want: AudioModeDialogueNormalized, ok: true},
		{input: "pan=stereo", ok: false},
		{input: "Dialogue", ok: false},
		{input: "unsupported", ok: false},
	}

	for _, test := range tests {
		got, err := parseAudioMode(test.input)
		if test.ok {
			if err != nil || got != test.want {
				t.Errorf("parseAudioMode(%q) = %q, %v; want %q", test.input, got, err, test.want)
			}
		} else if err == nil {
			t.Errorf("parseAudioMode(%q) unexpectedly accepted %q", test.input, got)
		}
	}
}

func TestRenderJobSpecPreservesAudioMode(t *testing.T) {
	duration := 10_000
	sessions := []sessionMetadata{{
		Key:   "movie-1",
		Title: "Test movie",
		Media: []sessionMedia{{
			ID:       float64(42),
			Duration: &duration,
			Part:     []sessionPart{{ID: float64(99), Key: "/library/parts/99/file"}},
		}},
	}}
	request := RenderJobCreateRequest{
		RatingKey: "movie-1", MediaID: 99, FromMs: 0, ToMs: 1000,
		SubtitleIndex: -1, AudioMode: AudioModeDialogueNormalized,
	}

	spec, err := validateRenderJobRequest(request, User{Uuid: "owner-a"}, sessions)
	if err != nil {
		t.Fatal(err)
	}
	if spec.AudioMode != AudioModeDialogueNormalized {
		t.Fatalf("render spec audio mode = %q, want %q", spec.AudioMode, AudioModeDialogueNormalized)
	}
}

func TestPreviewRejectsUnsupportedAudioModeBeforeUpstreamCalls(t *testing.T) {
	api := &API{app: &Application{}}
	app := testAuthenticatedParamRoute(http.MethodGet, "/preview/:ratingKey/:from/:to", api.preview)
	response, err := app.Test(httptest.NewRequest(http.MethodGet,
		"/preview/movie/00:00:00/00:00:01?audioMode=pan%3Dstereo", nil))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("preview status = %d, want %d", response.StatusCode, http.StatusUnprocessableEntity)
	}
}

func TestAudioModeFFmpegArgsForFinalAndPreview(t *testing.T) {
	tests := []struct {
		name       string
		mode       AudioMode
		wantFilter string
		wantRate   string
	}{
		{name: "standard", mode: AudioModeStandard},
		{name: "dialogue", mode: AudioModeDialogue, wantFilter: dialogueAudioFilter},
		{
			name: "dialogue normalized", mode: AudioModeDialogueNormalized,
			wantFilter: dialogueAudioFilter + ",loudnorm=I=-16:LRA=11:TP=-1.5:linear=false:print_format=none,aresample=48000",
			wantRate:   "48000",
		},
	}

	for _, path := range []string{"final", "preview"} {
		for _, test := range tests {
			t.Run(path+"/"+test.name, func(t *testing.T) {
				args := ffmpeg.KwArgs{"ac": 2}
				if err := configureAudioOutput(args, test.mode); err != nil {
					t.Fatal(err)
				}
				commandArgs := ffmpeg.ConvertKwargsToCmdLineArgs(args)
				if !containsConsecutiveArgs(commandArgs, "-ac", "2") {
					t.Fatalf("audio args missing stereo layout: %v", commandArgs)
				}
				if test.wantFilter == "" {
					if containsArg(commandArgs, "-af") {
						t.Fatalf("standard mode unexpectedly has an audio filter: %v", commandArgs)
					}
					return
				}
				if !containsConsecutiveArgs(commandArgs, "-af", test.wantFilter) {
					t.Fatalf("audio filter missing from args: %v", commandArgs)
				}
				if test.wantRate != "" && !containsConsecutiveArgs(commandArgs, "-ar", test.wantRate) {
					t.Fatalf("normalized mode missing output rate: %v", commandArgs)
				}
				if strings.Contains(strings.Join(commandArgs, " "), "filter") {
					t.Fatalf("unexpected user filter expression in args: %v", commandArgs)
				}
			})
		}
	}
}

func TestDoFfmpegRejectsUnsupportedAudioModeWithoutRunningFFmpeg(t *testing.T) {
	_, err := DoFfmpeg(FfmpegParams{
		URL: "source", Filename: "clip.mp4", AudioMode: AudioMode("pan=stereo"),
		Context: context.Background(),
	})
	if err == nil {
		t.Fatal("unsupported audio mode was accepted")
	}
}
