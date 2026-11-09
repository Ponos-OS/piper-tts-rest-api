package telemetry_test

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"

	"github.com/kasir-barati/piper-tts-rest-api/internal/config"
	"github.com/kasir-barati/piper-tts-rest-api/internal/telemetry"
)

const serviceName = "telemetry-test-service"

// collector plays the OTLP endpoint and keeps what it receives, by path.
type collector struct {
	mu     sync.Mutex
	bodies map[string][]byte
}

func newCollector(t *testing.T) *collector {
	t.Helper()

	c := &collector{bodies: map[string][]byte{}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		assert.NoError(t, err)
		c.mu.Lock()
		c.bodies[r.URL.Path] = append(c.bodies[r.URL.Path], body...)
		c.mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", srv.URL)

	return c
}

func (c *collector) received(path string) []byte {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.bodies[path]
}

func testConfig(enabled bool, logShipping string) config.Config {
	batch := config.Batch{MaxQueueSize: 10, MaxExportBatchSize: 10, FlushInterval: time.Hour, ExportTimeout: 5 * time.Second}

	return config.Config{
		Service: config.Service{Name: serviceName, Version: "9.8.7", Env: "test"},
		Otel:    config.Otel{Enabled: enabled, LogShipping: logShipping, Traces: batch, Logs: batch},
	}
}

func resetGlobals(t *testing.T) {
	t.Helper()

	t.Cleanup(func() {
		otel.SetTracerProvider(noop.NewTracerProvider())
		otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator())
	})
}

func TestSetup_DoesNothingWhenDisabled(t *testing.T) {
	resetGlobals(t)
	collect := newCollector(t)

	tel, err := telemetry.Setup(t.Context(), testConfig(false, "otlp"))
	require.NoError(t, err)
	_, span := otel.Tracer("test").Start(t.Context(), "work")
	span.End()

	assert.Nil(t, tel.LogHandler())
	require.NoError(t, tel.Shutdown(t.Context()))
	assert.False(t, span.SpanContext().IsValid())
	assert.Empty(t, collect.received("/v1/traces"))
}

func TestSetup_ExportsTracesAndLogsToTheConfiguredEndpoint(t *testing.T) {
	resetGlobals(t)
	collect := newCollector(t)

	tel, err := telemetry.Setup(t.Context(), testConfig(true, "otlp"))
	require.NoError(t, err)
	ctx, span := otel.Tracer("test").Start(t.Context(), "work")
	require.NotNil(t, tel.LogHandler())
	slog.New(tel.LogHandler()).InfoContext(ctx, "shipped message")
	span.End()
	require.NoError(t, tel.Shutdown(t.Context()))

	assert.True(t, span.SpanContext().IsSampled())
	traces := collect.received("/v1/traces")
	assert.True(t, bytes.Contains(traces, []byte("work")), "the span reached /v1/traces")
	assert.True(t, bytes.Contains(traces, []byte(serviceName)), "the resource names the service")
	assert.True(t, bytes.Contains(traces, []byte("9.8.7")), "the resource carries the version")
	logs := collect.received("/v1/logs")
	assert.True(t, bytes.Contains(logs, []byte("shipped message")), "the record reached /v1/logs")
	traceID := span.SpanContext().TraceID()
	assert.True(t, bytes.Contains(logs, traceID[:]), "the record is linked to its trace")
}

func TestSetup_ShipsNoLogsWhenTheyGoToStdout(t *testing.T) {
	resetGlobals(t)
	collect := newCollector(t)

	tel, err := telemetry.Setup(t.Context(), testConfig(true, "stdout"))
	require.NoError(t, err)
	_, span := otel.Tracer("test").Start(t.Context(), "work")
	span.End()
	require.NoError(t, tel.Shutdown(t.Context()))

	assert.Nil(t, tel.LogHandler())
	assert.NotEmpty(t, collect.received("/v1/traces"))
	assert.Empty(t, collect.received("/v1/logs"))
}

func TestSetup_HonorsTheStandardSamplerVariable(t *testing.T) {
	resetGlobals(t)
	collect := newCollector(t)
	t.Setenv("OTEL_TRACES_SAMPLER", "always_off")

	tel, err := telemetry.Setup(t.Context(), testConfig(true, "stdout"))
	require.NoError(t, err)
	_, span := otel.Tracer("test").Start(t.Context(), "work")
	span.End()
	require.NoError(t, tel.Shutdown(t.Context()))

	assert.False(t, span.SpanContext().IsSampled())
	assert.Empty(t, collect.received("/v1/traces"))
}

func TestSetup_ExportsRecordedURLsWithoutSecrets(t *testing.T) {
	resetGlobals(t)
	collect := newCollector(t)

	tel, err := telemetry.Setup(t.Context(), testConfig(true, "stdout"))
	require.NoError(t, err)
	_, span := otel.Tracer("test").Start(t.Context(), "call",
		trace.WithAttributes(semconv.URLFull("https://user:pw@bucket.example/key.mp3?X-Amz-Expires=300&X-Amz-Signature=secret#frag")))
	span.End()
	require.NoError(t, tel.Shutdown(t.Context()))

	traces := collect.received("/v1/traces")
	assert.True(t, bytes.Contains(traces, []byte("https://bucket.example/key.mp3?X-Amz-Expires=300&X-Amz-Signature=[REDACTED]")))
	assert.False(t, bytes.Contains(traces, []byte("secret")))
	assert.False(t, bytes.Contains(traces, []byte("pw@")))
}
