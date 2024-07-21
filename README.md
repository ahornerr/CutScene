# CutScene

CutScene is a tool that gives you the ability to create short clips from your Plex media.

Currently, CutScene exists as an HTTP API, but in the future it will be more user-friendly (UI, browser extension, etc).

## Setup

Copy [config.example.yaml]() to [config.yaml]() (in the same directory) and update values accordingly

Depending on your setup, the Plex host can either be a DNS record (e.g. https://my.plex.server) or an IP (http://10.0.0.100:32400). 

The Plex token can be found by following [these instructions](https://support.plex.tv/articles/204059436-finding-an-authentication-token-x-plex-token/).

> [!IMPORTANT]
> If you changed the listen address in the config, update the forwarded port in [docker-compose.yaml]()

The default [docker-compose.yaml]() file points to the [GitHub Container Registry image](https://github.com/ahornerr/CutScene/pkgs/container/cutscene). 

You can start this container by simply running `docker compose up`.

### Hardware acceleration
AMD GPU support on Linux is supported by setting the Ffmpeg codec config to `h264_vaapi`.

In Docker, the `/dev/dri/renderD*` device must also be mounted. This can be done by using the GPU Docker compose override:

```sh
docker compose -f docker-compose.yaml -f docker-compose.gpu.yaml up
```

This `h264_vaapi` codec and DRI device approach should theoretically also work for Intel Quicksync but is untested.

> [!NOTE]
> The `h264_nvenc` codec provides experimental hardware encoding for Nvidia GPUs. If running in Docker, the [Nvidia Container Toolkit](https://github.com/NVIDIA/nvidia-container-toolkit) should be installed.

## Usage

Hit the app over HTTP passing your plex username and the start/end times of the clip in the request

```sh
curl http://127.0.0.1:8080/clip/ahorner/00:05:00/00:05:05 -O -J
```

### Query parameters

Query parameters are used to modify the resulting file (quality, size, etc)

#### `height` (integer)
Determines the height in pixels of the clip. If not specified, original quality is used.
Width is scaled appropriately to retain original aspect ratio.

#### `qp` (integer)
Quantization parameter as an integer. https://slhck.info/video/2017/02/24/crf-guide.html

Higher values = worse quality but lower file size

A QP between 20 and 30 is typically ideal (tested with the h264_vaapi encoder, libx264 may be different)

## Development

A [docker-compose.build.yaml]() file is included which will build the Docker image from source.

It can be used via `docker compose -f docker-compose.yaml -f docker-compose.build.yaml up --build`.

Alternatively the source can be compiled directly with `go build ./...` or ran with `go run ./...` assuming the Go SDK is installed (recommend Go >= 1.22).
