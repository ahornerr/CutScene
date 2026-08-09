package main

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

func TestExpectedPreviewTerminationClassification(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "request cancellation", err: context.Canceled, want: true},
		{name: "closed pipe", err: errors.New("write: broken pipe"), want: true},
		{name: "connection reset", err: errors.New("connection reset by peer"), want: true},
		{name: "deadline is an actual error", err: context.DeadlineExceeded, want: false},
		{name: "ffmpeg failure is an actual error", err: errors.New("ffmpeg exited with error"), want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := isExpectedPreviewTermination(test.err, nil); got != test.want {
				t.Fatalf("isExpectedPreviewTermination(%v) = %v, want %v", test.err, got, test.want)
			}
		})
	}

	disconnected := &previewClientDisconnectWriter{}
	disconnected.disconnected.Store(true)
	if !isExpectedPreviewTermination(fmt.Errorf("ffmpeg exited with error"), disconnected) {
		t.Fatal("disconnected writer should suppress expected stream termination errors")
	}
}
