package amqp

import (
	"context"
	"errors"
	"fmt"

	amqp091 "github.com/rabbitmq/amqp091-go"

	"github.com/kasir-barati/piper-tts-rest-api/internal/config"
)

// Message is what a consumer hands to its handler.
type Message struct {
	Body []byte
	// Attempt starts at 1; the retry flow raises it through the x-attempt header.
	Attempt int
	// Authorization is the caller's Authorization header, empty when there was none.
	Authorization string
}

// Consumer reads jobs from the work queue, one at a time.
type Consumer struct {
	cfg  config.RabbitMQ
	conn *amqp091.Connection
	ch   *amqp091.Channel
}

// NewConsumer opens its own connection (publishing and consuming do not share one) and sets prefetch to 1, so the broker sends the next job only after the current one is acknowledged.
func NewConsumer(cfg config.RabbitMQ) (*Consumer, error) {
	conn, err := amqp091.DialConfig(cfg.URL, amqp091.Config{Dial: amqp091.DefaultDial(dialTimeout)})
	if err != nil {
		return nil, fmt.Errorf("connect to RabbitMQ: %w", err)
	}

	ch, err := conn.Channel()
	if err == nil {
		err = ch.Qos(1, 0, false)
	}
	if err != nil {
		_ = conn.Close() //nolint:errcheck // the setup error is the one to report.

		return nil, fmt.Errorf("set up RabbitMQ consumer: %w", err)
	}

	return &Consumer{cfg: cfg, conn: conn, ch: ch}, nil
}

// Run calls handle for each job until ctx is cancelled or the connection is lost.
// A nil result acknowledges the job; an error rejects it without requeue, so it is dead-lettered.
// When ctx is cancelled while a job is running, the job is left unacknowledged and RabbitMQ delivers it again.
func (c *Consumer) Run(ctx context.Context, handle func(context.Context, Message) error) error {
	deliveries, err := c.ch.ConsumeWithContext(ctx, c.cfg.WorkQueue, "", false, false, false, false, nil)
	if err != nil {
		return fmt.Errorf("consume %s: %w", c.cfg.WorkQueue, err)
	}

	for {
		select {
		case <-ctx.Done():
			return nil
		case d, ok := <-deliveries:
			if !ok {
				return errConsumerClosed
			}
			if err := settle(ctx, d, handle); err != nil {
				return err
			}
		}
	}
}

func settle(ctx context.Context, d amqp091.Delivery, handle func(context.Context, Message) error) error {
	err := handle(ctx, newMessage(d))
	if ctx.Err() != nil {
		return nil //nolint:nilerr // shutting down: leave the job unacknowledged.
	}
	if err != nil {
		return wrapSettle(d.Reject(false))
	}

	return wrapSettle(d.Ack(false))
}

// Close closes the connection, and with it the channel.
func (c *Consumer) Close() error {
	if err := c.conn.Close(); err != nil && !errors.Is(err, amqp091.ErrClosed) {
		return fmt.Errorf("close consumer connection: %w", err)
	}

	return nil
}

func newMessage(d amqp091.Delivery) Message {
	m := Message{Body: d.Body, Attempt: 1}
	if attempt := headerInt(d.Headers["x-attempt"]); attempt > 0 {
		m.Attempt = attempt
	}
	if authorization, ok := d.Headers["authorization"].(string); ok {
		m.Authorization = authorization
	}

	return m
}

// headerInt reads a header the broker decoded as any of its integer types; other values give 0.
func headerInt(v any) int {
	switch n := v.(type) {
	case int32:
		return int(n)
	case int64:
		return int(n)
	case int:
		return n
	default:
		return 0
	}
}

func wrapSettle(err error) error {
	if err != nil {
		return fmt.Errorf("settle message: %w", err)
	}

	return nil
}
