FROM golang:latest AS build

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o /cutscene

FROM debian:stable

ENV NVIDIA_VISIBLE_DEVICES=all
ENV NVIDIA_DRIVER_CAPABILITIES=all

RUN apt update && apt install -y mesa-va-drivers libva-drm2 wget xz-utils

RUN mkdir -p /usr/lib/btbn-ffmpeg && \
    wget -qO btbn-ffmpeg.tar.xz "https://github.com/NickM-27/FFmpeg-Builds/releases/download/autobuild-2022-07-31-12-37/ffmpeg-n5.1-2-g915ef932a3-linux64-gpl-5.1.tar.xz" && \
    tar -xf btbn-ffmpeg.tar.xz -C /usr/lib/btbn-ffmpeg --strip-components 1 && \
    rm -rf btbn-ffmpeg.tar.xz /usr/lib/btbn-ffmpeg/doc /usr/lib/btbn-ffmpeg/bin/ffplay && \
    chown -R root:root /usr/lib/btbn-ffmpeg && \
    chmod -R +x /usr/lib/btbn-ffmpeg

ENV PATH="/usr/lib/btbn-ffmpeg/bin:${PATH}"

WORKDIR /
COPY --from=build /cutscene /cutscene

ENTRYPOINT ["/cutscene"]
