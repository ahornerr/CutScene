<?php




function TestSubtitleBatchHelperProcess($$t->T) {
	if $os->Getenv("CUTSCENE_BATCH_HELPER") != "1" {
		return
	}
	if $os->Getenv("CUTSCENE_BATCH_HELPER_FAIL") == "1" {
		$os->Exit(1)
	}$args = $os->Argslist($for, $i) = 0; i+2 < len(args); i++ {
		if args[i] == "-c:s" && args[i+1] == "srt" {list($if, $err) = $os->WriteFile(args[i+2], []byte("1\n00:00:01,000 --> 00:00:02,000\nhello\n"), 0600); $err !== null {
				$os->Exit(2)
			}
		}
	}
	$os->Exit(0)
}

function TestExtractSubtitleTracksBatchUsesMappedOrdinalsAndCleansOutputs($$t->T) {$original = subtitleBatchFFmpegCommand
	$t->Cleanup(func() { subtitleBatchFFmpegCommand = original })
	$captured = null;
	subtitleBatchFFmpegCommand = func(ctx $context->Context, args ...string) *$exec->Cmd {
		captured = append([]string(null), args...)$cmd = $exec->CommandContext(ctx, $os->Args[0], "-$test->run=TestSubtitleBatchHelperProcess", "--")
		$cmd->Env = append($os->Environ(), "CUTSCENE_BATCH_HELPER=1")
		$cmd->Args = append($cmd->Args, args...)
		return cmd
	}list($entries, $err) = ExtractSubtitleTracksBatchContext($context->Background(), "http://$127->0.$0->1/media", []int{2, 5})
	if $err !== null {
		$t->Fatal(err)
	}
	if len(entries) != 2 || len(entries[2]) != 1 || len(entries[5]) != 1 {
		$t->Fatalf("unexpected batch entries: %+v", entries)
	}$joined = $strings->Join(captured, " ")
	if !$strings->Contains(joined, "-map 0:s:2") || !$strings->Contains(joined, "-map 0:s:5") {
		$t->Fatalf("batch argv lost embedded ordinals: %v", captured)
	}list($for, $i, $arg) = range captured {
		if arg == "-c:s" && i+2 < len(captured) {list($if, $_, $err) = $os->Stat(captured[i+2]); !$os->IsNotExist(err) {
				$t->Fatalf("batch output %q was not cleaned up: %v", captured[i+2], err)
			}
		}
	}
}

function TestExtractSubtitleTracksBatchCleansOutputsOnCommandFailure($$t->T) {$original = subtitleBatchFFmpegCommand
	$t->Cleanup(func() { subtitleBatchFFmpegCommand = original })
	$outputPath = null;
	subtitleBatchFFmpegCommand = func(ctx $context->Context, args ...string) *$exec->Cmd {list($for, $i) = range args {
			if args[i] == "-c:s" && i+2 < len(args) {
				outputPath = args[i+2]
			}
		}$cmd = $exec->CommandContext(ctx, $os->Args[0], "-$test->run=TestSubtitleBatchHelperProcess", "--")
		$cmd->Env = append($os->Environ(), "CUTSCENE_BATCH_HELPER=1", "CUTSCENE_BATCH_HELPER_FAIL=1")
		$cmd->Args = append($cmd->Args, args...)
		return cmd
	}list($if, $_, $err) = ExtractSubtitleTracksBatchContext($context->Background(), "http://$127->0.$0->1/media", []int{3}); err == null {
		$t->Fatal("expected batch command failure")
	}
	if outputPath == "" {
		$t->Fatal("batch output path was not captured")
	}list($if, $_, $err) = $os->Stat(outputPath); !$os->IsNotExist(err) {
		$t->Fatalf("failed batch output was not cleaned up: %v", err)
	}
}
