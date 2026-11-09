package amqp

import (
	"fmt"
	"maps"

	amqp091 "github.com/rabbitmq/amqp091-go"

	"github.com/kasir-barati/piper-tts-rest-api/internal/config"
	"github.com/kasir-barati/piper-tts-rest-api/internal/synthesis"
)

// declareTopology declares durable direct exchanges and quorum queues. Declaring is idempotent as long as the arguments match what is already there.
//
//	synthesize (exchange) --synthesize--> synthesize.work   (failed after x-delivery-limit: dead-lettered)
//	                                          |
//	synthesize.dlx (exchange) --synthesize--> synthesize.dlq
func declareTopology(ch *amqp091.Channel, cfg config.RabbitMQ) error {
	steps := []struct {
		name string
		do   func() error
	}{
		{"exchange " + cfg.Exchange, func() error { return declareExchange(ch, cfg.Exchange) }},
		{"exchange " + cfg.DeadLetterExchange, func() error { return declareExchange(ch, cfg.DeadLetterExchange) }},
		{"queue " + cfg.DeadLetterQueue, func() error { return declareQueue(ch, cfg.DeadLetterQueue, quorum(nil)) }},
		{"bind " + cfg.DeadLetterQueue, func() error { return bind(ch, cfg.DeadLetterQueue, cfg.DeadLetterExchange) }},
		{"queue " + cfg.WorkQueue, func() error {
			return declareQueue(ch, cfg.WorkQueue, quorum(amqp091.Table{
				"x-delivery-limit":          deliveryLimit,
				"x-dead-letter-exchange":    cfg.DeadLetterExchange,
				"x-dead-letter-routing-key": synthesis.RoutingKey,
			}))
		}},
		{"bind " + cfg.WorkQueue, func() error { return bind(ch, cfg.WorkQueue, cfg.Exchange) }},
	}

	for _, step := range steps {
		if err := step.do(); err != nil {
			return fmt.Errorf("declare %s: %w", step.name, err)
		}
	}

	return nil
}

// quorum returns the arguments of a quorum queue plus extra.
func quorum(extra amqp091.Table) amqp091.Table {
	args := amqp091.Table{"x-queue-type": "quorum"}
	maps.Copy(args, extra)

	return args
}

func declareExchange(ch *amqp091.Channel, name string) error {
	return ch.ExchangeDeclare(name, amqp091.ExchangeDirect, true, false, false, false, nil) //nolint:wrapcheck // wrapped by declareTopology.
}

func declareQueue(ch *amqp091.Channel, name string, args amqp091.Table) error {
	_, err := ch.QueueDeclare(name, true, false, false, false, args)

	return err //nolint:wrapcheck // wrapped by declareTopology.
}

func bind(ch *amqp091.Channel, queue, exchange string) error {
	return ch.QueueBind(queue, synthesis.RoutingKey, exchange, false, nil) //nolint:wrapcheck // wrapped by declareTopology.
}
