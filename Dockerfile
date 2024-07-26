FROM golang:latest AS build_go

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o /cutscene

FROM node:alpine AS build_react

WORKDIR /app

ENV PATH /app/node_modules/.bin:$PATH

COPY frontend/package.json ./
COPY frontend/package-lock.json ./

RUN npm install --silent

COPY frontend/. ./
RUN npm run build

FROM debian:stable

ENV NVIDIA_VISIBLE_DEVICES=all
ENV NVIDIA_DRIVER_CAPABILITIES=all

# https://github.com/blakeblackshear/frigate/issues/3858#issuecomment-1256832591
RUN echo 'deb http://deb.debian.org/debian testing main non-free' >> /etc/apt/sources.list
RUN apt update && apt install -y -t testing mesa-va-drivers libva-drm2 wget xz-utils

RUN mkdir -p /usr/lib/btbn-ffmpeg && \
    wget -qO btbn-ffmpeg.tar.xz "https://github.com/NickM-27/FFmpeg-Builds/releases/download/autobuild-2022-07-31-12-37/ffmpeg-n5.1-2-g915ef932a3-linux64-gpl-5.1.tar.xz" && \
    tar -xf btbn-ffmpeg.tar.xz -C /usr/lib/btbn-ffmpeg --strip-components 1 && \
    rm -rf btbn-ffmpeg.tar.xz /usr/lib/btbn-ffmpeg/doc /usr/lib/btbn-ffmpeg/bin/ffplay && \
    chown -R root:root /usr/lib/btbn-ffmpeg && \
    chmod -R +x /usr/lib/btbn-ffmpeg

ENV PATH="/usr/lib/btbn-ffmpeg/bin:${PATH}"

WORKDIR /
COPY --from=build_go /cutscene /cutscene
COPY --from=build_react /app/build /frontend/build

ENTRYPOINT ["/cutscene"]
