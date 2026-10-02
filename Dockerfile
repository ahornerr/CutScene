FROM golang:1.22-bookworm AS build_go

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN GOOS=linux go build -o /cutscene

FROM node:22-alpine AS build_react

WORKDIR /app

ENV PATH /app/node_modules/.bin:$PATH

COPY frontend/package.json ./
COPY frontend/package-lock.json ./

# npm ci honours the committed lockfile so the image is reproducible; npm
# install would resolve versions afresh on every build.
RUN npm ci --silent

COPY frontend/. ./
RUN npm run build

FROM debian:stable-slim

ENV NVIDIA_VISIBLE_DEVICES=all
ENV NVIDIA_DRIVER_CAPABILITIES=all

RUN apt-get update && apt-get install -y --no-install-recommends \
        mesa-va-drivers \
        libva-drm2 \
        ca-certificates \
        wget \
        xz-utils \
        fontconfig \
        fonts-dejavu-core \
    && fc-cache -f \
    && rm -rf /var/lib/apt/lists/*

# FFmpeg is fetched over the network, so verify the download against a pinned
# digest rather than trusting whatever the URL serves at build time.
ARG FFMPEG_URL="https://github.com/NickM-27/FFmpeg-Builds/releases/download/autobuild-2022-07-31-12-37/ffmpeg-n5.1-2-g915ef932a3-linux64-gpl-5.1.tar.xz"
ARG FFMPEG_SHA256="377abec133f9d9e8014dee1b91c9684ac8bb0b5b7d80100a57116ff837c4c0d4"

RUN mkdir -p /usr/lib/btbn-ffmpeg && \
    wget -qO /tmp/btbn-ffmpeg.tar.xz "${FFMPEG_URL}" && \
    echo "${FFMPEG_SHA256}  /tmp/btbn-ffmpeg.tar.xz" | sha256sum -c - && \
    tar -xf /tmp/btbn-ffmpeg.tar.xz -C /usr/lib/btbn-ffmpeg --strip-components 1 && \
    rm -rf /tmp/btbn-ffmpeg.tar.xz /usr/lib/btbn-ffmpeg/doc /usr/lib/btbn-ffmpeg/bin/ffplay && \
    chown -R root:root /usr/lib/btbn-ffmpeg && \
    chmod -R +x /usr/lib/btbn-ffmpeg

ENV PATH="/usr/lib/btbn-ffmpeg/bin:${PATH}"
ENV FONTCONFIG_FILE=/etc/fonts/fonts.conf
ENV FONTCONFIG_PATH=/etc/fonts

# Run unprivileged. The service needs no root capability: it reads
# /config.yaml, keeps durable clips and its session database under /data, and
# uses /tmp for transient render jobs. GPU access comes from device
# passthrough rather than elevated privileges.
#
# The entrypoint starts as root purely to adopt a storage volume left
# root-owned by earlier releases, then drops to this user for the process
# lifetime. See docker-entrypoint.sh.
RUN groupadd --gid 10001 cutscene && \
    useradd --uid 10001 --gid 10001 --home-dir /home/cutscene --create-home --shell /usr/sbin/nologin cutscene && \
    apt-get update && apt-get install -y --no-install-recommends util-linux && \
    rm -rf /var/lib/apt/lists/*

WORKDIR /
COPY --from=build_go /cutscene /cutscene
COPY --from=build_react /app/build /frontend/build
COPY docker-entrypoint.sh /usr/local/bin/docker-entrypoint.sh

# The unprivileged user only needs write access to storage.root. The working
# directory stays / and keeps root ownership: it holds config.yaml, the binary,
# and the frontend assets, and a writable / would let a compromised process
# replace the binary.
RUN mkdir -p /data && chown -R cutscene:cutscene /data /frontend && \
    chmod +x /usr/local/bin/docker-entrypoint.sh

# No USER directive: the entrypoint must start as root to adopt a storage
# volume left root-owned by earlier releases, then exec setpriv to drop to the
# unprivileged cutscene user. Supplemental groups for VAAPI (the host `render`
# gid, commonly 989) are supplied by docker-compose.gpu.yaml via group_add and
# are preserved across the privilege drop.

ENTRYPOINT ["/usr/local/bin/docker-entrypoint.sh"]
