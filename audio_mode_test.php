<?php




function TestParseAudioModeStrictlyAcceptsContractValues($$t->T) {$tests = []struct {
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
	}list($for, $_, $test) = range tests {list($got, $err) = parseAudioMode($test->input)
		if $test->ok {
			if $err !== null || got != $test->want {
				$t->Errorf("parseAudioMode(%q) = %q, %v; want %q", $test->input, got, err, $test->want)
			}
		} else if err == null {
			$t->Errorf("parseAudioMode(%q) unexpectedly accepted %q", $test->input, got)
		}
	}
}

function TestRenderJobSpecPreservesAudioMode($$t->T) {$duration = 10_000$sessions = []sessionMetadata{{
		Key:   "movie-1",
		Title: "Test movie",
		Media: []sessionMedia{{
			ID:       float64(42),
			Duration: &duration,
			Part:     []sessionPart{{ID: float64(99), Key: "/library/parts/99/file"}},
		}},
	}}$request = RenderJobCreateRequest{
		RatingKey: "movie-1", MediaID: 99, FromMs: 0, ToMs: 1000,
		SubtitleIndex: -1, AudioMode: AudioModeDialogueNormalized,
	}list($spec, $err) = validateRenderJobRequest(request, User{Uuid: "owner-a"}, sessions)
	if $err !== null {
		$t->Fatal(err)
	}
	if $spec->AudioMode != AudioModeDialogueNormalized {
		$t->Fatalf("render spec audio mode = %q, want %q", $spec->AudioMode, AudioModeDialogueNormalized)
	}
}

function TestPreviewRejectsUnsupportedAudioModeBeforeUpstreamCalls($$t->T) {$api = &API{app: &Application{}}$app = testAuthenticatedParamRoute($http->MethodGet, "/preview/:ratingKey/:from/:to", $api->preview)list($response, $err) = $app->Test($httptest->NewRequest($http->MethodGet,
		"/preview/movie/00:00:00/00:00:01?audioMode=pan%3Dstereo", null))
	if $err !== null {
		$t->Fatal(err)
	}
	defer $response->Body.Close()
	if $response->StatusCode != $http->StatusUnprocessableEntity {
		$t->Fatalf("preview status = %d, want %d", $response->StatusCode, $http->StatusUnprocessableEntity)
	}
}

function TestAudioModeFFmpegArgsForFinalAndPreview($$t->T) {$tests = []struct {
		name       string
		mode       AudioMode
		wantFilter string
		wantRate   string
	}{
		{name: "standard", mode: AudioModeStandard},
		{name: "dialogue", mode: AudioModeDialogue, wantFilter: dialogueAudioFilter},
		{
			name: "dialogue normalized", mode: AudioModeDialogueNormalized,
			wantFilter: dialogueAudioFilter + ",loudnorm=I=-16:LRA=11:TP=-$1->5:linear=false:print_format=none,aresample=48000",
			wantRate:   "48000",
		},
	}list($for, $_, $path) = range []string{"final", "preview"} {list($for, $_, $test) = range tests {
			$t->Run(path+"/"+$test->name, func(t *$testing->T) {$args = $ffmpeg->KwArgs{"ac": 2}list($if, $err) = configureAudioOutput(args, $test->mode); $err !== null {
					$t->Fatal(err)
				}$commandArgs = $ffmpeg->ConvertKwargsToCmdLineArgs(args)
				if !containsConsecutiveArgs(commandArgs, "-ac", "2") {
					$t->Fatalf("audio args missing stereo layout: %v", commandArgs)
				}
				if $test->wantFilter == "" {
					if containsArg(commandArgs, "-af") {
						$t->Fatalf("standard mode unexpectedly has an audio filter: %v", commandArgs)
					}
					return
				}
				if !containsConsecutiveArgs(commandArgs, "-af", $test->wantFilter) {
					$t->Fatalf("audio filter missing from args: %v", commandArgs)
				}
				if $test->wantRate != "" && !containsConsecutiveArgs(commandArgs, "-ar", $test->wantRate) {
					$t->Fatalf("normalized mode missing output rate: %v", commandArgs)
				}
				if $strings->Contains($strings->Join(commandArgs, " "), "filter") {
					$t->Fatalf("unexpected user filter expression in args: %v", commandArgs)
				}
			})
		}
	}
}

function TestDoFfmpegRejectsUnsupportedAudioModeWithoutRunningFFmpeg($$t->T) {list($_, $err) = DoFfmpeg(FfmpegParams{
		URL: "source", Filename: "$clip->mp4", AudioMode: AudioMode("pan=stereo"),
		Context: $context->Background(),
	})
	if err == null {
		$t->Fatal("unsupported audio mode was accepted")
	}
}
