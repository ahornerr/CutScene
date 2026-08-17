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

func TestEmbeddingContractConfigDefaultsAndOverrides(t *testing.T) {
	defaults := loadConfigForTest(t, "plex:\n  host: http://plex\n")
	if defaults.SemanticSearch.EmbeddingsModel != openAIEmbeddingModel || defaults.SemanticSearch.EmbeddingsDimensions != subtitleEmbeddingDimensions || defaults.SemanticSearch.EmbeddingsProfile != embeddingProfileBGE {
		t.Fatalf("embedding defaults = %+v", defaults.SemanticSearch)
	}
	configured := loadConfigForTest(t, "plex:\n  host: http://plex\nsemantic_search:\n  embeddings_model: custom/model\n  embeddings_dimensions: 1024\n  embeddings_profile: qwen\n  embeddings_model_revision: rev-1\n  embeddings_rebuild: true\n")
	if configured.SemanticSearch.EmbeddingsModel != "custom/model" || configured.SemanticSearch.EmbeddingsDimensions != 1024 || configured.SemanticSearch.EmbeddingsProfile != embeddingProfileQwen || configured.SemanticSearch.EmbeddingsModelRevision != "rev-1" || !configured.SemanticSearch.EmbeddingsRebuild {
		t.Fatalf("configured embedding contract = %+v", configured.SemanticSearch)
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
