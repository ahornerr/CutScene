package main

import (
	"os"
	"path/filepath"
	"strings"
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

func TestDurableStorageConfig(t *testing.T) {
	config := loadConfigForTest(t, "plex:\n  host: http://plex\n"+
		"storage:\n  root: /srv/cutscene\n  database: metadata.sqlite3\n")
	if config.Storage.Root != "/srv/cutscene" || config.Storage.Database != "metadata.sqlite3" {
		t.Fatalf("storage config = root %q database %q", config.Storage.Root, config.Storage.Database)
	}
}

func TestComposeMountsDurableStorageWithoutTransientRenderRoot(t *testing.T) {
	compose, err := os.ReadFile("docker-compose.yaml")
	if err != nil {
		t.Fatal(err)
	}
	composeText := string(compose)
	if !strings.Contains(composeText, "cutscene-data:/data") || !strings.Contains(composeText, "volumes:\n  cutscene-data:") {
		t.Fatalf("Compose does not declare the durable /data volume:\n%s", composeText)
	}
	if strings.Contains(composeText, "      - /tmp") {
		t.Fatal("Compose must not persist transient /tmp render jobs")
	}

	sample, err := os.ReadFile("config.example.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(sample), "root: /data") {
		t.Fatal("config.example.yaml does not document the Compose durable root")
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

func TestSemanticSearchConfigDefaultsAndOverrides(t *testing.T) {
	t.Run("defaults", func(t *testing.T) {
		cfg := loadConfigForTest(t, "plex:\n  host: http://plex\n")
		if cfg.SemanticSearch.BatchSize != defaultSubtitleEmbeddingBatchSize {
			t.Fatalf("expected default batch size %d, got %d", defaultSubtitleEmbeddingBatchSize, cfg.SemanticSearch.BatchSize)
		}
		if cfg.SemanticSearch.Concurrency != defaultSubtitleEmbeddingConcurrency {
			t.Fatalf("expected default concurrency %d, got %d", defaultSubtitleEmbeddingConcurrency, cfg.SemanticSearch.Concurrency)
		}
		if cfg.SemanticSearch.QueryInstruction != nil {
			t.Fatalf("expected nil QueryInstruction by default, got %v", *cfg.SemanticSearch.QueryInstruction)
		}
	})

	t.Run("overrides", func(t *testing.T) {
		cfg := loadConfigForTest(t, `plex:
  host: http://plex
semantic_search:
  batch_size: 64
  concurrency: 4
  query_instruction: "Instruct: search\nQuery: "
`)
		if cfg.SemanticSearch.BatchSize != 64 {
			t.Fatalf("expected batch size 64, got %d", cfg.SemanticSearch.BatchSize)
		}
		if cfg.SemanticSearch.Concurrency != 4 {
			t.Fatalf("expected concurrency 4, got %d", cfg.SemanticSearch.Concurrency)
		}
		if cfg.SemanticSearch.QueryInstruction == nil || *cfg.SemanticSearch.QueryInstruction != "Instruct: search\nQuery: " {
			t.Fatalf("unexpected query instruction: %v", cfg.SemanticSearch.QueryInstruction)
		}
	})
}
