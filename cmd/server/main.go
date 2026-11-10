// Command server runs the Piper TTS REST API.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/kasir-barati/piper-tts-rest-api/internal/amqp"
	"github.com/kasir-barati/piper-tts-rest-api/internal/config"
	"github.com/kasir-barati/piper-tts-rest-api/internal/httpapi"
	"github.com/kasir-barati/piper-tts-rest-api/internal/logging"
	"github.com/kasir-barati/piper-tts-rest-api/internal/piper"
	"github.com/kasir-barati/piper-tts-rest-api/internal/telemetry"
	"github.com/kasir-barati/piper-tts-rest-api/internal/worker"
)

const (
	keyPort       = "port"
	keyPath       = "path"
	keySampleRate = "sampleRate"
	keyError      = "error"

	telemetryFlushTimeout = 10 * time.Second
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "piper-tts-rest-api:", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load(os.LookupEnv)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)

	// First SIGTERM/SIGINT makes httpapi.Serve start the graceful shutdown.
	// A second SIGTERM/SIGINT terminates the process immediately.
	defer stop()
	// Fire-and-forget/anonymous goroutine
	go func() {
		<-ctx.Done() // The goroutine waits for the first signal
		stop()       // Then goroutine calls stop()
	}()

	logger, stopTelemetry, err := startTelemetry(ctx, cfg)
	if err != nil {
		return err
	}
	defer stopTelemetry() // Runs last, so the spans and logs of the shutdown are flushed too.

	synth, err := piper.New(ctx, cfg.PiperModelPath)
	if err != nil {
		return fmt.Errorf("piper: %w", err)
	}

	broker, err := amqp.Connect(cfg.Synthesize.RabbitMQ)
	if err != nil {
		return fmt.Errorf("amqp: %w", err)
	}
	defer func() { // Cleanup: runs at the very end of the process's life.
		if closeErr := broker.Close(); closeErr != nil {
			logging.Named(logger, "Bootstrap").ErrorContext(context.WithoutCancel(ctx), "Closing RabbitMQ failed", slog.String(keyError, closeErr.Error()))
		}
	}()

	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp", ":"+strconv.Itoa(cfg.Service.Port))
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}

	boot := logging.Named(logger, "Bootstrap")
	boot.InfoContext(ctx, "TTS API running", slog.Int(keyPort, cfg.Service.Port))
	boot.InfoContext(ctx, "Piper model", slog.String(keyPath, cfg.PiperModelPath), slog.Int(keySampleRate, synth.SampleRate()))

	consumed, err := startWorker(ctx, cfg.Synthesize, logger, synth)
	if err != nil {
		return err
	}

	serveErr := httpapi.Serve(ctx, ln, httpapi.NewRouter(routerOptions(cfg, logger, synth, broker)))
	stop() // Guarantee the consumer is told to exit

	return errors.Join(
		wrap("serve", serveErr),
		wrap("consume", <-consumed), // Waits on <-consumed for the consumer to finish.
	)
}

func routerOptions(cfg config.Config, logger *slog.Logger, synth *piper.Synthesizer, jobs httpapi.JobPublisher) httpapi.Options {
	return httpapi.Options{
		Logger:           logger,
		Synthesizer:      synth,
		Jobs:             jobs,
		MaxConcurrency:   cfg.MaxConcurrency,
		MaxBodySizeBytes: cfg.MaxBodySizeBytes,
		Version:          cfg.Service.Version,
		DocsEnabled:      cfg.Service.DocsEnabled,
		Synthesize: httpapi.SynthesizeOptions{
			MaxBodySizeBytes: cfg.Synthesize.MaxBodySizeBytes,
			Callback:         cfg.Synthesize.Callback,
		},
	}
}

// startWorker consumes jobs in the background until ctx is cancelled, then closes the consumer.
// The consumer reconnects by itself when RabbitMQ goes away.
func startWorker(ctx context.Context, cfg config.Synthesize, logger *slog.Logger, synth *piper.Synthesizer) (<-chan error, error) {
	consumer, err := amqp.NewConsumer(cfg.RabbitMQ, logger)
	if err != nil {
		return nil, fmt.Errorf("amqp consumer: %w", err)
	}

	jobs := worker.New(cfg, pipeline{synth}, logger)
	done := make(chan error, 1)
	go func() {
		consumer.Run(ctx, jobs.Process)
		done <- consumer.Close()
	}()

	return done, nil
}

// startTelemetry starts OpenTelemetry and builds the logger, which ships its records when OTEL_LOG_SHIPPING asks for it.
// The returned function flushes what is still queued.
func startTelemetry(ctx context.Context, cfg config.Config) (*slog.Logger, func(), error) {
	tel, err := telemetry.Setup(ctx, cfg)
	if err != nil {
		return nil, nil, fmt.Errorf("telemetry: %w", err)
	}
	logger := logging.New(cfg.Log, cfg.Service.Name, os.Stdout, tel.LogHandler())

	return logger, func() {
		flush, cancel := context.WithTimeout(context.WithoutCancel(ctx), telemetryFlushTimeout)
		defer cancel()

		if shutdownErr := tel.Shutdown(flush); shutdownErr != nil {
			logging.Named(logger, "Bootstrap").ErrorContext(flush, "Flushing telemetry failed", slog.String(keyError, shutdownErr.Error()))
		}
	}, nil
}

func wrap(what string, err error) error {
	if err != nil {
		return fmt.Errorf("%s: %w", what, err)
	}

	return nil
}

// pipeline adapts the synthesizer, which returns the concrete *piper.Stream, to worker.Synthesizer.
type pipeline struct{ *piper.Synthesizer }

func (p pipeline) Synthesize(ctx context.Context, text string) (io.ReadCloser, error) {
	stream, err := p.Synthesizer.Synthesize(ctx, text)
	if err != nil {
		return nil, err
	}

	return stream, nil
}
