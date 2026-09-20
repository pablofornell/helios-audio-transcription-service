# syntax=docker/dockerfile:1

# ---- go builder ---------------------------------------------------------
FROM golang:1.27 AS builder
WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 go build -o /out/helios-transcription-api ./cmd/helios-transcription-api

# ---- whisper.cpp builder -------------------------------------------------
# Built in its own stage, independent of the Go builder above, since it
# shares nothing with it but the final COPY into runtime: a change to the
# Go source never invalidates this stage's (multi-minute) cmake build, and
# vice versa.
FROM debian:bookworm-slim AS whispercpp-builder

# Pinned to the current stable release tag (verified against
# https://github.com/ggml-org/whisper.cpp/releases/tag/v1.9.4, published
# 2026-09-11), the same way ARG YTDLP_VERSION pins BLUEPRINT's yt-dlp.
ARG WHISPER_CPP_VERSION=v1.9.4

RUN apt-get update \
    && apt-get install -y --no-install-recommends \
        build-essential cmake git ca-certificates \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /src
RUN git clone --depth 1 --branch ${WHISPER_CPP_VERSION} \
        https://github.com/ggml-org/whisper.cpp.git .

# -DBUILD_SHARED_LIBS=OFF statically links whisper.cpp's own libraries
# (libggml*, libwhisper) into the whisper-cli binary -- upstream's CMake
# default on Linux is ON, which instead scatters several .so files across
# build/bin/ that would all have to be tracked down and copied into the
# runtime stage alongside the binary itself; static keeps this Dockerfile
# to copying a single file. -DGGML_NATIVE=OFF disables baking in this
# build machine's specific CPU features (upstream's default, GGML_NATIVE=ON
# on native Linux builds, targets -march=native): without it, a binary
# built on one host's CPU can illegal-instruction-crash on another's, which
# is exactly the situation a Docker image built once and run anywhere is
# in. Neither flag makes the binary fully static: it still dynamically
# links libgomp (ggml's OpenMP threading, GGML_OPENMP=ON by default) and
# libstdc++ (whisper.cpp is C++) from the runtime image -- see the runtime
# stage's apt-get install below.
RUN cmake -B build -DCMAKE_BUILD_TYPE=Release -DBUILD_SHARED_LIBS=OFF -DGGML_NATIVE=OFF \
    && cmake --build build -j --config Release --target whisper-cli

# ---- runtime --------------------------------------------------------------
FROM debian:bookworm-slim AS runtime

RUN apt-get update \
    && apt-get install -y --no-install-recommends \
        ca-certificates ffmpeg curl \
        # libgomp1 (OpenMP runtime) and libstdc++6 (the C++ standard
        # library) are what whisper-cli, built in the previous stage,
        # actually needs beyond libc even with its own libraries linked in
        # statically -- see whispercpp-builder's own comment on why
        # neither is baked in by the static build. ffmpeg above typically
        # pulls libstdc++6 in already; it is still listed explicitly so
        # this image does not depend on that happening to remain true.
        libgomp1 libstdc++6 \
    && rm -rf /var/lib/apt/lists/*

COPY --from=builder /out/helios-transcription-api /usr/local/bin/helios-transcription-api
COPY --from=whispercpp-builder /src/build/bin/whisper-cli /usr/local/bin/whisper-cli

RUN groupadd --system helios \
    && useradd --system --gid helios --home-dir /data --shell /usr/sbin/nologin helios \
    && mkdir -p /data /models \
    && chown -R helios:helios /data /models

USER helios
VOLUME ["/data", "/models"]
EXPOSE 7462

# /readyz, not /healthz: /healthz is unconditionally 200 the instant the
# process can answer HTTP at all (see internal/httpapi/health.go's own
# doc), so a HEALTHCHECK against it would report "healthy" forever even
# with a dead database connection or no model weights on disk -- exactly
# the states an operator most needs this to catch. /readyz aggregates the
# database, storage and transcriber checks (plus the worker pool itself)
# and only 200s when all of them do.
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
    CMD curl -fsS http://localhost:7462/readyz || exit 1

ENTRYPOINT ["/usr/local/bin/helios-transcription-api"]
