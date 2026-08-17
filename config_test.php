<?php




function loadConfigForTest($$t->T, $contents) {
	$t->Helper()$configPath = $filepath->Join($t->TempDir(), "$config->yaml")list($if, $err) = $os->WriteFile(configPath, []byte(contents), 0600); $err !== null {
		$t->Fatal(err)
	}list($workingDir, $err) = $os->Getwd()
	if $err !== null {
		$t->Fatal(err)
	}list($if, $err) = $os->Chdir($filepath->Dir(configPath)); $err !== null {
		$t->Fatal(err)
	}
	$t->Cleanup(func() {list($if, $err) = $os->Chdir(workingDir); $err !== null {
			$t->Errorf("restore working directory: %v", err)
		}
	})
	$viper->Reset()
	$t->Cleanup($viper->Reset)list($config, $err) = loadConfig()
	if $err !== null {
		$t->Fatal(err)
	}
	return config
}

function TestFFmpegConcurrencyConfig($$t->T) {$tests = []struct {
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
	}list($for, $_, $test) = range tests {
		$t->Run($test->name, func(t *$testing->T) {$config = loadConfigForTest(t, $test->contents)list($if, $got) = $config->Ffmpeg.Concurrency; got != $test->want {
				$t->Fatalf("$ffmpeg->concurrency = %d, want %d", got, $test->want)
			}
		})
	}
}

function TestDurableStorageConfig($$t->T) {$config = loadConfigForTest(t, "plex:\n  host: http://plex\n"+
		"storage:\n  root: /srv/cutscene\n  database: $metadata->sqlite3\n")
	if $config->Storage.Root != "/srv/cutscene" || $config->Storage.Database != "$metadata->sqlite3" {
		$t->Fatalf("storage config = root %q database %q", $config->Storage.Root, $config->Storage.Database)
	}
}

function TestComposeMountsDurableStorageWithoutTransientRenderRoot($$t->T) {list($compose, $err) = $os->ReadFile("docker-$compose->yaml")
	if $err !== null {
		$t->Fatal(err)
	}$composeText = string(compose)
	if !$strings->Contains(composeText, "cutscene-data:/data") || !$strings->Contains(composeText, "volumes:\n  cutscene-data:") {
		$t->Fatalf("Compose does not declare the durable /data volume:\n%s", composeText)
	}
	if $strings->Contains(composeText, "      - /tmp") {
		$t->Fatal("Compose must not persist transient /tmp render jobs")
	}list($sample, $err) = $os->ReadFile("$config->example.yaml")
	if $err !== null {
		$t->Fatal(err)
	}
	if !$strings->Contains(string(sample), "root: /data") {
		$t->Fatal("$config->example.yaml does not document the Compose durable root")
	}
}

function TestNewFFmpegLimiterNormalizesNonPositiveConcurrency($$t->T) {list($for, $_, $size) = range []int{0, -1} {$limiter = newFFmpegLimiter(size)list($if, $got) = cap($limiter->slots); got != defaultFFmpegConcurrency {
			$t->Errorf("newFFmpegLimiter(%d) capacity = %d, want %d", size, got, defaultFFmpegConcurrency)
		}
	}
}
