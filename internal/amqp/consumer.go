package amqp

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	amqp091 "github.com/rabbitmq/amqp091-go"

	"github.com/kasir-barati/piper-tts-rest-api/internal/config"
	"github.com/kasir-barati/piper-tts-rest-api/internal/logging"
	"github.com/kasir-barati/piper-tts-rest-api/internal/synthesis"
)

const (
	settleTimeout  = 10 * time.Second
	initialBackoff = time.Second
	maxBackoff     = 30 * time.Second

	// deliveryTimeHeader holds the epoch milliseconds before which RabbitMQ 4.4 does not deliver the message.
	deliveryTimeHeader = "x-opt-delivery-time"

	backoffFactor = 2

	attemptHeader = "x-attempt"

	keyError = "error"
	keyQueue = "queue"
)

var (
	errUnroutable     = errors.New("no queue took the retry")
	errUnknownOutcome = errors.New("unknown outcome")
)

// Message is what a consumer hands to its handler.
type Message struct {
	Body []byte
	// Attempt starts at 1; the retry flow raises it through the x-attempt header.
	Attempt int
	// Authorization is the caller's Authorization header, empty when there was none.
	Authorization string
}

type outcomeKind int

const (
	outcomeAck outcomeKind = iota
	outcomeReject
	outcomeRetry
)

// Outcome tells the consumer what to do with a message once its handler returned.
type Outcome struct {
	kind  outcomeKind
	delay time.Duration
}

// Ack removes the message: the job is done or failed for good.
func Ack() Outcome { return Outcome{kind: outcomeAck} }

// Reject dead-letters the message: it cannot be processed.
func Reject() Outcome { return Outcome{kind: outcomeReject} }

// Retry republishes the message to the work queue with its attempt raised by one; the broker holds it back for delay.
func Retry(delay time.Duration) Outcome { return Outcome{kind: outcomeRetry, delay: delay} }

// Consumer reads jobs from the work queue, one at a time, and reconnects when the connection is lost.
type Consumer struct {
	cfg config.RabbitMQ
	log *slog.Logger

	conn    *amqp091.Connection
	ch      *amqp091.Channel
	pub     *amqp091.Channel // publishes retries with confirms.
	returns chan amqp091.Return
}

// NewConsumer opens its own connection (publishing and consuming do not share one) and sets prefetch to 1, so the broker sends the next job only after the current one is settled.
func NewConsumer(cfg config.RabbitMQ, logger *slog.Logger) (*Consumer, error) {
	c := &Consumer{cfg: cfg, log: logging.Named(logger, "Consumer")}
	if err := c.connect(); err != nil {
		return nil, err
	}

	return c, nil
}

// connect replaces the connection and both channels.
func (c *Consumer) connect() error {
	conn, err := amqp091.DialConfig(c.cfg.URL, amqp091.Config{Dial: amqp091.DefaultDial(dialTimeout)})
	if err != nil {
		return fmt.Errorf("connect to RabbitMQ: %w", err)
	}

	ch, pub, returns, err := openChannels(conn, c.cfg)
	if err != nil {
		_ = conn.Close() //nolint:errcheck // the setup error is the one to report.

		return fmt.Errorf("set up RabbitMQ consumer: %w", err)
	}
	c.conn, c.ch, c.pub, c.returns = conn, ch, pub, returns

	return nil
}

func openChannels(conn *amqp091.Connection, cfg config.RabbitMQ) (*amqp091.Channel, *amqp091.Channel, chan amqp091.Return, error) {
	ch, err := conn.Channel()
	if err != nil {
		return nil, nil, nil, fmt.Errorf("open channel: %w", err)
	}
	// Declaring again is harmless and keeps the consumer working after a broker that lost its queues.
	if err = declareTopology(ch, cfg); err != nil {
		return nil, nil, nil, err
	}
	if err = ch.Qos(1, 0, false); err != nil {
		return nil, nil, nil, fmt.Errorf("set prefetch: %w", err)
	}

	pub, err := conn.Channel()
	if err != nil {
		return nil, nil, nil, fmt.Errorf("open channel: %w", err)
	}
	if err = pub.Confirm(false); err != nil {
		return nil, nil, nil, fmt.Errorf("turn on confirms: %w", err)
	}

	return ch, pub, pub.NotifyReturn(make(chan amqp091.Return, 1)), nil
}

// Run calls handle for each job and settles it with the returned Outcome, until ctx is cancelled.
// When the connection is lost it reconnects with a growing pause; a job being processed at that moment is delivered again.
// When ctx is cancelled while a job is running, its Outcome is ignored and the job stays unacknowledged, so RabbitMQ delivers it again.
func (c *Consumer) Run(ctx context.Context, handle func(context.Context, Message) Outcome) {
	for {
		c.serve(ctx, handle)
		if ctx.Err() != nil {
			return
		}

		c.log.WarnContext(ctx, "Lost the RabbitMQ connection, reconnecting")
		if !c.reconnect(ctx) {
			return
		}
	}
}

// serve consumes until ctx is cancelled or the connection is lost.
func (c *Consumer) serve(ctx context.Context, handle func(context.Context, Message) Outcome) {
	deliveries, err := c.ch.ConsumeWithContext(ctx, c.cfg.WorkQueue, "", false, false, false, false, nil)
	if err != nil {
		c.log.ErrorContext(ctx, "Consuming failed", slog.String(keyQueue, c.cfg.WorkQueue), slog.Any(keyError, err))

		return
	}

	for {
		select {
		case <-ctx.Done():
			return
		case d, ok := <-deliveries:
			if !ok {
				return
			}

			m := newMessage(d)
			outcome := handle(ctx, m)
			if ctx.Err() != nil {
				return
			}
			if err := c.settle(ctx, d, m, outcome); err != nil {
				c.log.ErrorContext(ctx, "Settling the job failed", slog.Any(keyError, err))

				return
			}
		}
	}
}

// reconnect retries with a growing pause (the first attempt is immediate). It returns false when ctx ended first.
func (c *Consumer) reconnect(ctx context.Context) bool {
	var pause time.Duration

	for {
		if !sleep(ctx, pause) {
			return false
		}

		_ = c.conn.Close() //nolint:errcheck // it is being replaced; closing an already closed connection fails.
		err := c.connect()
		if err == nil {
			c.log.InfoContext(ctx, "Reconnected to RabbitMQ")

			return true
		}

		c.log.ErrorContext(ctx, "Reconnecting to RabbitMQ failed", slog.Any(keyError, err))
		pause = min(max(pause*backoffFactor, initialBackoff), maxBackoff)
	}
}

func sleep(ctx context.Context, d time.Duration) bool {
	if d == 0 {
		return ctx.Err() == nil
	}

	timer := time.NewTimer(d)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func (c *Consumer) settle(ctx context.Context, d amqp091.Delivery, m Message, o Outcome) error {
	switch o.kind {
	case outcomeAck:
		return wrapSettle(d.Ack(false))
	case outcomeReject:
		return wrapSettle(d.Reject(false))
	case outcomeRetry:
		if err := c.publishRetry(ctx, d, m.Attempt+1, o.delay); err != nil {
			// Giving the job back to the work queue would loop at full speed (x-delivery-limit does not count requeues),
			// so it goes to the dead-letter queue, where it is kept for an operator.
			c.log.ErrorContext(ctx, "Publishing the retry failed, dead-lettering the job", slog.Any(keyError, err))

			return wrapSettle(d.Reject(false))
		}

		return wrapSettle(d.Ack(false))
	default:
		return fmt.Errorf("%w: %d", errUnknownOutcome, o.kind)
	}
}

// publishRetry puts the job back on the work queue, due after delay, and returns once the broker confirmed it, so the original is acknowledged only after the copy is safe.
// Publishing is mandatory, because an exchange without a queue would otherwise confirm the message and drop it.
func (c *Consumer) publishRetry(ctx context.Context, d amqp091.Delivery, attempt int, delay time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, settleTimeout)
	defer cancel()

	now := time.Now()
	due := now.Add(max(delay, 0))
	confirmation, err := c.pub.PublishWithDeferredConfirmWithContext(ctx, c.cfg.Exchange, synthesis.RoutingKey, true, false, amqp091.Publishing{
		ContentType:  d.ContentType,
		DeliveryMode: amqp091.Persistent,
		MessageId:    d.MessageId,
		Timestamp:    now,
		Headers:      retryHeaders(d.Headers, attempt, due),
		Body:         d.Body,
	})
	if err != nil {
		return fmt.Errorf("publish: %w", err)
	}
	acked, err := confirmation.WaitContext(ctx)
	if err != nil {
		return fmt.Errorf("wait for confirm: %w", err)
	}
	if !acked {
		return errNotConfirmed
	}

	select {
	case <-c.returns: // a return arrives before the confirm of the same message.
		return errUnroutable
	default:
		return nil
	}
}

// retryHeaders copies the caller's headers (authorization, timestamp, trace context) except the broker's own x- headers, and sets the attempt and the time before which the broker must not deliver the message.
func retryHeaders(headers amqp091.Table, attempt int, due time.Time) amqp091.Table {
	out := amqp091.Table{attemptHeader: attempt, deliveryTimeHeader: due.UnixMilli()}
	for k, v := range headers {
		if !strings.HasPrefix(k, "x-") {
			out[k] = v
		}
	}

	return out
}

// Close closes the connection, and with it the channels.
func (c *Consumer) Close() error {
	if err := c.conn.Close(); err != nil && !errors.Is(err, amqp091.ErrClosed) {
		return fmt.Errorf("close consumer connection: %w", err)
	}

	return nil
}

func newMessage(d amqp091.Delivery) Message {
	m := Message{Body: d.Body, Attempt: 1}
	if attempt := headerInt(d.Headers[attemptHeader]); attempt > 0 {
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
