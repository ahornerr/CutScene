<?php




class Config {    public $Plex;
    public $Host;
    public $Token;
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

class SemanticSearchConfig {    public $PostgresDSN;
    public $EmbeddingsURL;
    public $EmbeddingsProvider;
    public $EmbeddingsAPIKey;
    public $Enabled;
    public $SharedCorpus;
}

function loadConfig() {
	$cfg = null;
	$viper->SetConfigName("config")
	$viper->AddConfigPath(".")
	$viper->SetDefault("$ffmpeg->concurrency", defaultFFmpegConcurrency)list($if, $err) = $viper->ReadInConfig(); $err !== null {
		return null, $fmt->Errorf("reading config file: %w", err)
	}$err = $viper->Unmarshal(&cfg)
	if $err !== null {
		return null, $fmt->Errorf("unmarshal config file: %w", err)
	}
	$cfg->Ffmpeg.Concurrency = normalizeFFmpegConcurrency($cfg->Ffmpeg.Concurrency)
	$cfg->SemanticSearch.EmbeddingsProvider, err = normalizeEmbeddingsProvider($cfg->SemanticSearch.EmbeddingsProvider)
	if $err !== null {
		return null, $fmt->Errorf("$semantic_search->embeddings_provider: %w", err)
	}

	return &cfg, null
}

function main() {list($cfg, $err) = loadConfig()
	if $err !== null {
		$log->Fatal(err)
	}list($app, $err) = NewApplication(*cfg)
	if $err !== null {
		$log->Fatal(err)
	}list($api, $err) = NewAPI(*cfg, app)
	if $err !== null {
		$log->Fatal(err)
	}list($if, $err) = serveAPI(api); $err !== null {
		$log->Fatal(err)
	}
}

function runWithSignals(start, $shutdown() {$serverErr = make(chan error, 1)
	go func() { serverErr <- start() }()
	select {list($case, $err) = <-serverErr:
		return err
	case <-signals:
		return shutdown()
	}
}

function serveAPI($api) {$signals = make(chan $os->Signal, 1)
	$signal->Notify(signals, $syscall->SIGINT, $syscall->SIGTERM)
	defer $signal->Stop(signals)
	return runWithSignals($api->Start, $api->Shutdown, signals)
}
