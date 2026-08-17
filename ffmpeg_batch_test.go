package main

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestSubtitleBatchHelperProcess(t *testing.T) {
	if os.Getenv("CUTSCENE_BATCH_HELPER") != "1" {
		return
	}
	if os.Getenv("CUTSCENE_BATCH_HELPER_FAIL") == "1" {
		os.Exit(1)
	}
	args := os.Args
	for i := 0; i+2 < len(args); i++ {
		if args[i] == "-c:s" && args[i+1] == "srt" {
			if err := os.WriteFile(args[i+2], []byte("1\n00:00:01,000 --> 00:00:02,000\nhello\n"), 0600); err != nil {
				os.Exit(2)
			}
		}
	}
	os.Exit(0)
}

func TestExtractSubtitleTracksBatchUsesMappedOrdinalsAndCleansOutputs(t *testing.T) {
	original := subtitleBatchFFmpegCommand
	t.Cleanup(func() { subtitleBatchFFmpegCommand = original })
	var captured []string
	subtitleBatchFFmpegCommand = func(ctx context.Context, args ...string) *exec.Cmd {
		captured = append([]string(nil), args...)
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=TestSubtitleBatchHelperProcess", "--")
		cmd.Env = append(os.Environ(), "CUTSCENE_BATCH_HELPER=1")
		cmd.Args = append(cmd.Args, args...)
		return cmd
	}

	entries, err := ExtractSubtitleTracksBatchContext(context.Background(), "http://127.0.0.1/media", []int{2, 5})
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 || len(entries[2]) != 1 || len(entries[5]) != 1 {
		t.Fatalf("unexpected batch entries: %+v", entries)
	}
	joined := strings.Join(captured, " ")
	if !strings.Contains(joined, "-map 0:s:2") || !strings.Contains(joined, "-map 0:s:5") {
		t.Fatalf("batch argv lost embedded ordinals: %v", captured)
	}
	for i, arg := range captured {
		if arg == "-c:s" && i+2 < len(captured) {
			if _, err := os.Stat(captured[i+2]); !os.IsNotExist(err) {
				t.Fatalf("batch output %q was not cleaned up: %v", captured[i+2], err)
			}
		}
	}
}

func TestExtractSubtitleTracksBatchCleansOutputsOnCommandFailure(t *testing.T) {
	original := subtitleBatchFFmpegCommand
	t.Cleanup(func() { subtitleBatchFFmpegCommand = original })
	var outputPath string
	subtitleBatchFFmpegCommand = func(ctx context.Context, args ...string) *exec.Cmd {
		for i := range args {
			if args[i] == "-c:s" && i+2 < len(args) {
				outputPath = args[i+2]
			}
		}
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=TestSubtitleBatchHelperProcess", "--")
		cmd.Env = append(os.Environ(), "CUTSCENE_BATCH_HELPER=1", "CUTSCENE_BATCH_HELPER_FAIL=1")
		cmd.Args = append(cmd.Args, args...)
		return cmd
	}

	if _, err := ExtractSubtitleTracksBatchContext(context.Background(), "http://127.0.0.1/media", []int{3}); err == nil {
		t.Fatal("expected batch command failure")
	}
	if outputPath == "" {
		t.Fatal("batch output path was not captured")
	}
	if _, err := os.Stat(outputPath); !os.IsNotExist(err) {
		t.Fatalf("failed batch output was not cleaned up: %v", err)
	}
}
