// Package telemetry sets up OpenTelemetry tracing and log shipping.
package telemetry

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"slices"

	"go.opentelemetry.io/contrib/bridges/otelslog"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploghttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"

	"github.com/kasir-barati/piper-tts-rest-api/internal/config"
	"github.com/kasir-barati/piper-tts-rest-api/internal/redact"
)

const logShippingOTLP = "otlp"

// Telemetry owns the OpenTelemetry providers. The zero value is the disabled state.
type Telemetry struct {
	traces *sdktrace.TracerProvider
	logs   *sdklog.LoggerProvider
	logger slog.Handler
}

// Setup starts tracing and, when OTEL_LOG_SHIPPING=otlp, log shipping; with OTEL_ENABLED off it does nothing.
// The OTLP endpoint, the sampler and the resource attributes are read from the standard OTEL_* variables by the SDK itself.
func Setup(ctx context.Context, cfg config.Config) (*Telemetry, error) {
	if !cfg.Otel.Enabled {
		return &Telemetry{}, nil
	}

	res, err := resource.New(ctx,
		resource.WithFromEnv(),
		resource.WithAttributes(
			semconv.ServiceName(cfg.Service.Name),
			semconv.ServiceVersion(cfg.Service.Version),
			attribute.String("deployment.environment.name", cfg.Service.Env),
		),
	)
	if err != nil {
		return nil, fmt.Errorf("build resource: %w", err)
	}

	t := &Telemetry{}
	if err = t.startTraces(ctx, res, cfg.Otel.Traces); err != nil {
		return nil, err
	}
	if cfg.Otel.LogShipping == logShippingOTLP {
		if err = t.startLogs(ctx, res, cfg.Service.Name, cfg.Otel.Logs); err != nil {
			return nil, errors.Join(err, t.Shutdown(ctx))
		}
	}

	return t, nil
}

func (t *Telemetry) startTraces(ctx context.Context, res *resource.Resource, batch config.Batch) error {
	exporter, err := otlptracehttp.New(ctx)
	if err != nil {
		return fmt.Errorf("create trace exporter: %w", err)
	}

	t.traces = sdktrace.NewTracerProvider(
		sdktrace.WithResource(res),
		sdktrace.WithBatcher(redactingExporter{exporter},
			sdktrace.WithMaxQueueSize(batch.MaxQueueSize),
			sdktrace.WithMaxExportBatchSize(batch.MaxExportBatchSize),
			sdktrace.WithBatchTimeout(batch.FlushInterval),
			sdktrace.WithExportTimeout(batch.ExportTimeout),
		),
	)
	otel.SetTracerProvider(t.traces)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{}))

	return nil
}

func (t *Telemetry) startLogs(ctx context.Context, res *resource.Resource, service string, batch config.Batch) error {
	exporter, err := otlploghttp.New(ctx)
	if err != nil {
		return fmt.Errorf("create log exporter: %w", err)
	}

	t.logs = sdklog.NewLoggerProvider(
		sdklog.WithResource(res),
		sdklog.WithProcessor(sdklog.NewBatchProcessor(exporter,
			sdklog.WithMaxQueueSize(batch.MaxQueueSize),
			sdklog.WithExportMaxBatchSize(batch.MaxExportBatchSize),
			sdklog.WithExportInterval(batch.FlushInterval),
			sdklog.WithExportTimeout(batch.ExportTimeout),
		)),
	)
	t.logger = otelslog.NewHandler(service, otelslog.WithLoggerProvider(t.logs))

	return nil
}

// LogHandler returns the handler that ships records to the collector, or nil when logs are not shipped.
func (t *Telemetry) LogHandler() slog.Handler {
	return t.logger
}

// Shutdown flushes what is queued and stops the exporters.
func (t *Telemetry) Shutdown(ctx context.Context) error {
	var errs []error
	if t.traces != nil {
		errs = append(errs, t.traces.Shutdown(ctx))
	}
	if t.logs != nil {
		errs = append(errs, t.logs.Shutdown(ctx))
	}

	return errors.Join(errs...)
}

// redactingExporter keeps secrets out of exported spans: in url.full it drops the user info and the fragment, and hides the secret query parameters as the logs do.
// Presigned upload URLs carry their signature in the query, and callback URLs may carry tokens.
// It works on export because otelhttp sets url.full after the span started.
type redactingExporter struct{ sdktrace.SpanExporter }

func (e redactingExporter) ExportSpans(ctx context.Context, spans []sdktrace.ReadOnlySpan) error {
	stubs := tracetest.SpanStubsFromReadOnlySpans(spans)
	for i := range stubs {
		stubs[i].Attributes = slices.Clone(stubs[i].Attributes)
		for j, a := range stubs[i].Attributes {
			if a.Key == semconv.URLFullKey {
				stubs[i].Attributes[j] = semconv.URLFull(withoutSecrets(a.Value.AsString()))
			}
		}
	}

	return e.SpanExporter.ExportSpans(ctx, stubs.Snapshots()) //nolint:wrapcheck // the exporter's own result.
}

func withoutSecrets(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return ""
	}

	redacted := u.Scheme + "://" + u.Host + u.EscapedPath()
	if u.RawQuery != "" {
		redacted += "?" + redact.Query(u.RawQuery)
	}

	return redacted
}
