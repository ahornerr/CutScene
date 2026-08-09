package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/viper"
)

func loadConfigForTest(t *testing.T, contents string) *Config {
	t.Helper()

	configPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(configPath, []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}

	workingDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(filepath.Dir(configPath)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(workingDir); err != nil {
			t.Errorf("restore working directory: %v", err)
		}
	})
	viper.Reset()
	t.Cleanup(viper.Reset)

	config, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	return config
}

func TestFFmpegConcurrencyConfig(t *testing.T) {
	tests := []struct {
		name     string
		contents string
		want     int
	}{
		{
			name:     "default",
			contents: "plex:\n  host: http://plex\n",
			want:     defaultFFmpegConcurrency,
		},
		{
			name: "configured override",
			contents: "plex:\n  host: http://plex\nffmpeg:\n" +
				"  concurrency: 5\n",
			want: 5,
		},
		{
			name: "zero is normalized",
			contents: "plex:\n  host: http://plex\nffmpeg:\n" +
				"  concurrency: 0\n",
			want: defaultFFmpegConcurrency,
		},
		{
			name: "negative is normalized",
			contents: "plex:\n  host: http://plex\nffmpeg:\n" +
				"  concurrency: -1\n",
			want: defaultFFmpegConcurrency,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			config := loadConfigForTest(t, test.contents)
			if got := config.Ffmpeg.Concurrency; got != test.want {
				t.Fatalf("ffmpeg.concurrency = %d, want %d", got, test.want)
			}
		})
	}
}

func TestNewFFmpegLimiterNormalizesNonPositiveConcurrency(t *testing.T) {
	for _, size := range []int{0, -1} {
		limiter := newFFmpegLimiter(size)
		if got := cap(limiter.slots); got != defaultFFmpegConcurrency {
			t.Errorf("newFFmpegLimiter(%d) capacity = %d, want %d", size, got, defaultFFmpegConcurrency)
		}
	}
}
