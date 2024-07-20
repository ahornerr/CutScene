# CutScene

## Setup

Copy [config.example.yaml]() to [config.yaml]() (in the same directory) and update values accordingly

Depending on your setup, the Plex host can either be a DNS record (e.g. https://my.plex.server) or an IP (http://10.0.0.100:32400). 

The Plex token can be found by following [these instructions](https://support.plex.tv/articles/204059436-finding-an-authentication-token-x-plex-token/).

> If you changed the listen address, update the port forward in [docker-compose.yaml]()

The default [docker-compose.yaml]() file points to the [GitHub Container Registry image](https://github.com/ahornerr/CutScene/pkgs/container/cutscene). 

You can start this container by simply running `docker compose up`.

## Usage

Hit the app over HTTP passing your plex username and the start/end times of the clip in the request

```sh
curl http://127.0.0.1:8080/clip/ahorner/00:05:00/00:05:05 -O -J
```

## Development

A [docker-compose.build.yaml]() file is included which will build the Docker image from source.

It can be used via `docker compose -f docker-compose.yaml -f docker-compose.build.yaml up --build`.

Alternatively the source can be compiled directly with `go build ./...` or ran with `go run ./...` assuming the Go SDK is installed (recommend Go >= 1.22).