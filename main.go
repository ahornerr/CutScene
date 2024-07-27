package main

import (
	"fmt"
	"log"

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
		Codec Codec `mapstructure:"codec"`
	}
}

func loadConfig() (*Config, error) {
	var cfg Config
	viper.SetConfigName("config")
	viper.AddConfigPath(".")
	if err := viper.ReadInConfig(); err != nil {
		return nil, fmt.Errorf("reading config file: %w", err)
	}
	err := viper.Unmarshal(&cfg)
	if err != nil {
		return nil, fmt.Errorf("unmarshal config file: %w", err)
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

	log.Fatal(api.Start())
}
