FROM golang:1.26.8-bookworm AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/server ./cmd/server

FROM debian:bookworm-slim

# Use bash and pipefail for safer builds
SHELL ["/bin/bash", "-o", "pipefail", "-c"]

# Common paths and settings
ENV PIP_NO_CACHE_DIR=1 \
    VENV_DIR=/opt/piper-venv \
    APP_DIR=/app \
    MODELS_DIR=/app/models \
    HF_BASE="https://huggingface.co/rhasspy/piper-voices/resolve/main/en/en_US" \
    VOICES="lessac ryan libritts_r ljspeech amy joe john kristin"

WORKDIR $APP_DIR

# System deps, Python venv, piper packages, and voice models
RUN set -eux; \
    apt-get update; \
    apt-get install -y --no-install-recommends \
            ca-certificates curl ffmpeg \
            python3 \
            python3-pip \
            python3-venv; \
    rm -rf /var/lib/apt/lists/*; \
    python3 -m venv "$VENV_DIR"; \
    "$VENV_DIR/bin/pip" install --upgrade pip; \
    "$VENV_DIR/bin/pip" install piper-tts pathvalidate; \
    mkdir -p "$MODELS_DIR"; \
    for v in $VOICES; do \
      for ext in onnx "onnx.json"; do \
        f="en_US-${v}-medium.${ext}"; \
        curl -fsSL "$HF_BASE/$v/medium/$f" -o "$MODELS_DIR/$f"; \
      done; \
    done; \
    groupadd --gid 1000 app; \
    useradd --uid 1000 --gid 1000 --no-create-home app

# Export venv PATH and default model
ENV PATH="$VENV_DIR/bin:${PATH}" \
    PIPER_MODEL=/app/models/en_US-lessac-medium.onnx

COPY --from=build /out/server /app/server

# The release version, reported as service.version. Declared last so a new version only rebuilds this layer.
ARG SERVICE_VERSION=unknown
ENV SERVICE_VERSION=$SERVICE_VERSION

USER app
EXPOSE 3000
CMD ["/app/server"]
