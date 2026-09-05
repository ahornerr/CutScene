package main

import (
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/viper"
)

type Config struct {
	Plex struct {
		Host         string            `mapstructure:"host"`
		Token        string            `mapstructure:"token"`
		PathMappings map[string]string `mapstructure:"path_mappings"`
	}
	API struct {
		ListenAddr string `mapstructure:"listen_addr"`
		Domain     string `mapstructure:"domain"`
	}
	Ffmpeg struct {
		Codec       Codec `mapstructure:"codec"`
		Concurrency int   `mapstructure:"concurrency"`
	} `mapstructure:"ffmpeg"`
	Storage struct {
		// Root contains durable application data, including saved clips and the
		// clip metadata database. It is deliberately separate from the transient
		// render-job directory.
		Root     string `mapstructure:"root"`
		Database string `mapstructure:"database"`
	} `mapstructure:"storage"`
	SemanticSearch SemanticSearchConfig `mapstructure:"semantic_search"`
}

type SemanticSearchConfig struct {
	PostgresDSN        string  `mapstructure:"postgres_dsn"`
	EmbeddingsURL      string  `mapstructure:"embeddings_url"`
	EmbeddingsProvider string  `mapstructure:"embeddings_provider"`
	EmbeddingsAPIKey   string  `mapstructure:"embeddings_api_key"`
	EmbeddingsModel    string  `mapstructure:"embeddings_model"`
	Dimensions         int     `mapstructure:"dimensions"`
	BatchSize          int     `mapstructure:"batch_size"`
	Concurrency        int     `mapstructure:"concurrency"`
	QueryInstruction   *string `mapstructure:"query_instruction"`
	Enabled            bool    `mapstructure:"enabled"`
	SharedCorpus       bool    `mapstructure:"shared_corpus"`
}

func loadConfig() (*Config, error) {
	var cfg Config
	viper.SetConfigName("config")
	viper.AddConfigPath(".")
	viper.SetDefault("ffmpeg.concurrency", defaultFFmpegConcurrency)
	viper.SetDefault("semantic_search.dimensions", 0)
	viper.SetDefault("semantic_search.batch_size", defaultSubtitleEmbeddingBatchSize)
	viper.SetDefault("semantic_search.concurrency", defaultSubtitleEmbeddingConcurrency)
	if err := viper.ReadInConfig(); err != nil {
		return nil, fmt.Errorf("reading config file: %w", err)
	}
	err := viper.Unmarshal(&cfg)
	if err != nil {
		return nil, fmt.Errorf("unmarshal config file: %w", err)
	}
	cfg.Ffmpeg.Concurrency = normalizeFFmpegConcurrency(cfg.Ffmpeg.Concurrency)
	cfg.SemanticSearch.BatchSize = normalizeSubtitleEmbeddingBatchSize(cfg.SemanticSearch.BatchSize)
	cfg.SemanticSearch.Concurrency = normalizeSubtitleEmbeddingConcurrency(cfg.SemanticSearch.Concurrency)
	cfg.SemanticSearch.EmbeddingsProvider, err = normalizeEmbeddingsProvider(cfg.SemanticSearch.EmbeddingsProvider)
	if err != nil {
		return nil, fmt.Errorf("semantic_search.embeddings_provider: %w", err)
	}

	return &cfg, nil
}

func main() {
	cfg, err := loadConfig()
	if err != nil {
		log.Fatal(err)
	}

	app, err := NewApplication(*cfg)
	if err != nil {
		log.Fatal(err)
	}

	api, err := NewAPI(*cfg, app)
	if err != nil {
		log.Fatal(err)
	}

	if err := serveAPI(api); err != nil {
		log.Fatal(err)
	}
}

func runWithSignals(start, shutdown func() error, signals <-chan os.Signal) error {
	serverErr := make(chan error, 1)
	go func() { serverErr <- start() }()
	select {
	case err := <-serverErr:
		return err
	case <-signals:
		return shutdown()
	}
}

func serveAPI(api *API) error {
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(signals)
	return runWithSignals(api.Start, api.Shutdown, signals)
}
