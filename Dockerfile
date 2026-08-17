FROM node:24.18.1-bookworm-slim

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

# System deps, pnpm, Python venv, piper packages, and voice models
RUN set -eux; \
    apt-get update; \
    apt-get install -y --no-install-recommends \
            ca-certificates curl ffmpeg \
            python3 \
            python3-pip \
            python3-venv; \
    rm -rf /var/lib/apt/lists/*; \
    npm install -g pnpm; \
    python3 -m venv "$VENV_DIR"; \
    "$VENV_DIR/bin/pip" install --upgrade pip; \
    "$VENV_DIR/bin/pip" install piper-tts pathvalidate; \
    mkdir -p "$MODELS_DIR"; \
    for v in $VOICES; do \
      for ext in onnx "onnx.json"; do \
        f="en_US-${v}-medium.${ext}"; \
        curl -fsSL "$HF_BASE/$v/medium/$f" -o "$MODELS_DIR/$f"; \
      done; \
    done

# Export venv PATH and default model
ENV PATH="$VENV_DIR/bin:${PATH}" \
    PIPER_MODEL=/app/models/en_US-lessac-medium.onnx

# Disable husky in Docker builds
ENV HUSKY=0

# Install app dependencies as non-root and avoid a separate chown layer
COPY --chown=node:node package.json pnpm-lock.yaml tsconfig.json tsconfig.build.json ./
RUN pnpm install --frozen-lockfile

COPY --chown=node:node src ./src
RUN pnpm build && pnpm prune --prod

# Make /app and everything in it owned by `node` so pnpm can write its runtime temp files (and so bind-mounted dev workflows produce host-deletable files).
RUN chown -R node:node /app

USER node
CMD ["pnpm", "start"]
