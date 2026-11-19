package main

import (
	"context"

	"go.uber.org/fx"

	"github.com/kasir-barati/piper-tts-rest-api/internal/amqp"
	"github.com/kasir-barati/piper-tts-rest-api/internal/config"
)

// BrokerProvider connects the publisher used by /synthesize and closes it on shutdown, after the HTTP server has stopped.
func BrokerProvider(lc fx.Lifecycle, cfg config.Config) (*amqp.Broker, error) {
	broker, err := amqp.Connect(cfg.Synthesize.RabbitMQ)
	if err != nil {
		return nil, err
	}

	lc.Append(fx.Hook{
		OnStop: func(context.Context) error {
			return broker.Close()
		},
	})

	return broker, nil
}
