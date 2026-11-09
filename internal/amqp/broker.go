// Package amqp talks to RabbitMQ: it declares the topology, publishes jobs and consumes them.
package amqp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	amqp091 "github.com/rabbitmq/amqp091-go"

	"github.com/kasir-barati/piper-tts-rest-api/internal/config"
	"github.com/kasir-barati/piper-tts-rest-api/internal/synthesis"
)

const (
	dialTimeout = 10 * time.Second

	// deliveryLimit stops a message that keeps crashing its consumer. It is not the retry count.
	deliveryLimit = 5
)

var (
	errClosed       = errors.New("broker is closed")
	errNotConfirmed = errors.New("broker did not confirm the message")
)

// Broker publishes jobs to the work queue. It is safe for concurrent use.
// The mutex guards conn, pub and closed: a reconnect replaces the connection and channel, and concurrent requests must not do that twice or publish on a channel that was just closed.
type Broker struct {
	cfg config.RabbitMQ

	mu     sync.Mutex
	conn   *amqp091.Connection
	pub    *amqp091.Channel
	closed bool
}

// Connect opens a connection, declares the topology and turns on publisher confirms.
func Connect(cfg config.RabbitMQ) (*Broker, error) {
	b := &Broker{cfg: cfg}
	if err := b.connect(); err != nil {
		return nil, err
	}

	return b, nil
}

// PublishJob enqueues job and returns once the broker confirmed it.
// authorization is the caller's Authorization header, kept for the worker's callbacks; it is omitted when empty.
// A lost connection is re-established on the next call.
func (b *Broker) PublishJob(ctx context.Context, job synthesis.Job, authorization string) error {
	body, err := json.Marshal(job)
	if err != nil {
		return fmt.Errorf("encode job: %w", err)
	}

	confirmation, err := b.publish(ctx, newPublishing(job.ID, body, authorization, time.Now()))
	if err != nil {
		return err
	}
	acked, err := confirmation.WaitContext(ctx) // RabbitMQ telling our publisher that it accepted responsibility for the message
	if err != nil {
		return fmt.Errorf("wait for confirm: %w", err)
	}
	if !acked {
		return errNotConfirmed
	}

	return nil
}

// Close closes the connection, and with it every channel.
func (b *Broker) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.closed = true
	if b.conn == nil {
		return nil
	}
	if err := b.conn.Close(); err != nil && !errors.Is(err, amqp091.ErrClosed) {
		return fmt.Errorf("close connection: %w", err)
	}

	return nil
}

func (b *Broker) publish(ctx context.Context, p amqp091.Publishing) (*amqp091.DeferredConfirmation, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.closed {
		return nil, errClosed
	}
	if b.conn.IsClosed() || b.pub.IsClosed() {
		if err := b.reconnect(); err != nil {
			return nil, err
		}
	}

	confirmation, err := b.pub.PublishWithDeferredConfirmWithContext(ctx, b.cfg.Exchange, synthesis.RoutingKey, false, false, p)
	if err != nil {
		return nil, fmt.Errorf("publish: %w", err)
	}

	return confirmation, nil
}

// reconnect replaces a lost connection and channel. The caller holds b.mu.
func (b *Broker) reconnect() error {
	_ = b.conn.Close() //nolint:errcheck // it is being replaced; closing an already closed connection fails.

	return b.connect()
}

// connect replaces b.conn and b.pub. The caller holds b.mu, except in Connect.
func (b *Broker) connect() error {
	conn, err := amqp091.DialConfig(b.cfg.URL, amqp091.Config{Dial: amqp091.DefaultDial(dialTimeout)})
	if err != nil {
		return fmt.Errorf("connect to RabbitMQ: %w", err)
	}

	ch, err := conn.Channel()
	if err == nil {
		err = declareTopology(ch, b.cfg)
	}
	if err == nil {
		err = ch.Confirm(false)
	}
	if err != nil {
		_ = conn.Close() //nolint:errcheck // the setup error is the one to report.

		return fmt.Errorf("set up RabbitMQ: %w", err)
	}

	b.conn, b.pub = conn, ch

	return nil
}

func newPublishing(jobID string, body []byte, authorization string, now time.Time) amqp091.Publishing {
	headers := amqp091.Table{"timestamp": now.UnixMilli()}
	if authorization != "" {
		headers["authorization"] = authorization
	}

	return amqp091.Publishing{
		ContentType:  "application/json",
		DeliveryMode: amqp091.Persistent,
		MessageId:    jobID,
		Timestamp:    now,
		Headers:      headers,
		Body:         body,
	}
}
