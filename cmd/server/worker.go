package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"

	"go.uber.org/fx"

	"github.com/kasir-barati/piper-tts-rest-api/internal/amqp"
	"github.com/kasir-barati/piper-tts-rest-api/internal/config"
	"github.com/kasir-barati/piper-tts-rest-api/internal/piper"
	"github.com/kasir-barati/piper-tts-rest-api/internal/worker"
)

// StartWorker consumes jobs in the background until shutdown, then closes the consumer.
// The consumer reconnects by itself when RabbitMQ goes away.
func StartWorker(
	lc fx.Lifecycle,
	cfg config.Config,
	logger *slog.Logger,
	synth *piper.Synthesizer,
) error {
	consumer, err := amqp.NewConsumer(cfg.Synthesize.RabbitMQ, logger)
	if err != nil {
		return fmt.Errorf("amqp consumer: %w", err)
	}

	jobs := worker.New(cfg.Synthesize, pipeline{synth}, logger)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)

	lc.Append(fx.Hook{
		OnStart: func(context.Context) error {
			go func() {
				consumer.Run(ctx, jobs.Process)
				done <- consumer.Close()
			}()

			return nil
		},
		OnStop: func(stopCtx context.Context) error {
			cancel()

			select {
			case err := <-done:
				return err
			case <-stopCtx.Done():
				return stopCtx.Err()
			}
		},
	})

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
