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
		Host  string `mapstructure:"host"`
		Token string `mapstructure:"token"`
	}
	API struct {
		ListenAddr string `mapstructure:"listen_addr"`
		Domain     string `mapstructure:"domain"`
	}
	Ffmpeg struct {
		Codec       Codec `mapstructure:"codec"`
		Concurrency int   `mapstructure:"concurrency"`
	} `mapstructure:"ffmpeg"`
}

func loadConfig() (*Config, error) {
	var cfg Config
	viper.SetConfigName("config")
	viper.AddConfigPath(".")
	viper.SetDefault("ffmpeg.concurrency", defaultFFmpegConcurrency)
	if err := viper.ReadInConfig(); err != nil {
		return nil, fmt.Errorf("reading config file: %w", err)
	}
	err := viper.Unmarshal(&cfg)
	if err != nil {
		return nil, fmt.Errorf("unmarshal config file: %w", err)
	}
	cfg.Ffmpeg.Concurrency = normalizeFFmpegConcurrency(cfg.Ffmpeg.Concurrency)

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
