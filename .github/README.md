# Piper TTS RESTful API

Dockerized Go RESTful API for offline text-to-speech using Piper TTS. Supports both synchronous streaming MP3 generation and asynchronous job-based synthesis with RabbitMQ, callbacks, and object‑storage uploads.

🐳📦🚢 [The image in Docker Hub](https://hub.docker.com/r/9109679196/piper-tts-rest-api).

- [API reference](https://kasir-barati.github.io/piper-tts-rest-api/latest/) (versioned OpenAPI docs).
- As for env variables and how to start it read [this docs](./docs/config.md).
- See [scaling](./docs/scaling.md) for deployment and KEDA guidance.
