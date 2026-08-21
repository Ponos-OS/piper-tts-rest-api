#!/usr/bin/env bash

rm -f hello-world.mp3

docker compose up --build -d

# Wait for API readiness
until curl -sf http://localhost:3000/health >/dev/null; do
	sleep 1
done

# ── Fabricate a W3C `traceparent` header ────────────────────────────────────
# Format:  00-<32-hex-trace-id>-<16-hex-parent-span-id>-<flags>
#   - "00"      : version
#   - trace-id  : 16 random bytes (32 lowercase hex chars), MUST NOT be all zeros
#   - parent-id : 8 random bytes  (16 lowercase hex chars), MUST NOT be all zeros
#   - flags     : "01" = sampled (so the collector/Jaeger actually keeps it),
#                 "00" = not sampled
#
# Reading 16 / 8 random bytes from /dev/urandom and hex-encoding them via `xxd -p -c <N>` gives us a single contiguous hex string with no whitespace.
# The non-zero check is a spec requirement -- vanishingly unlikely to fail in practice, but cheap to assert.
TRACE_ID="$(head -c 16 /dev/urandom | xxd -p -c 16)"
PARENT_SPAN_ID="$(head -c 8 /dev/urandom | xxd -p -c 8)"
TRACEPARENT="00-${TRACE_ID}-${PARENT_SPAN_ID}-01"

echo "Sending request with:"
echo "  trace-id        = ${TRACE_ID}"
echo "  parent-span-id  = ${PARENT_SPAN_ID}"
echo "  traceparent     = ${TRACEPARENT}"
echo "Open the trace and logs dashboard at http://localhost:3001/d/piper-tts-observability."

curl -f \
	-X POST http://localhost:3000/speak \
	-H "traceparent: ${TRACEPARENT}" \
	-H 'content-type: application/json' \
	-d '{"text":"Hello from a single command test"}' \
	-o hello-world.mp3

# curl -f \
# 	-X POST http://localhost:3000/speak \
# 	-H "traceparent: ${TRACEPARENT}" \
# 	-H 'content-type: text/plain; charset=utf-8' \
# 	--data-binary @input/half-maximum-allowed-size.txt \
# 	-o hello-world.mp3

ls -lh hello-world.mp3

echo "Sign in with GRAFANA_ADMIN_USER / GRAFANA_ADMIN_PASSWORD (admin / admin by default). BUT BEFORE YOU HAVE TO COMMENT THE LAST LINE."

docker compose down
