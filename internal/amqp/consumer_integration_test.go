//go:build integration

package amqp

import (
	"context"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	amqp091 "github.com/rabbitmq/amqp091-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kasir-barati/piper-tts-rest-api/internal/config"
	"github.com/kasir-barati/piper-tts-rest-api/internal/logging"
	"github.com/kasir-barati/piper-tts-rest-api/internal/synthesis"
)

// startConsumer starts a consumer and returns it with a function that stops it and waits until it is gone.
func startConsumer(t *testing.T, cfg config.RabbitMQ, handle func(context.Context, Message) Outcome) (*Consumer, func()) {
	t.Helper()

	logger := logging.New(config.Log{Mode: config.LogModeJSON, Level: config.LogLevelError}, "svc", io.Discard)
	c, err := NewConsumer(cfg, logger)
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		c.Run(ctx, handle)
		close(done)
	}()

	var once sync.Once
	stop := func() {
		once.Do(func() {
			cancel()
			<-done
			require.NoError(t, c.Close())
		})
	}
	t.Cleanup(stop)

	return c, stop
}

func consume(t *testing.T, cfg config.RabbitMQ, handle func(context.Context, Message) Outcome) (stop func()) {
	t.Helper()

	_, stop = startConsumer(t, cfg, handle)

	return stop
}

// ready returns how many messages wait in the queue; delivered but unacknowledged ones are not counted.
func ready(t *testing.T, cfg config.RabbitMQ, queue string) int {
	t.Helper()

	conn, err := amqp091.Dial(cfg.URL)
	require.NoError(t, err)
	defer conn.Close() //nolint:errcheck // test helper.
	ch, err := conn.Channel()
	require.NoError(t, err)
	q, err := ch.QueueDeclarePassive(queue, true, false, false, false, nil)
	require.NoError(t, err)

	return q.Messages
}

func publish(t *testing.T, cfg config.RabbitMQ, authorization string) synthesis.Job {
	t.Helper()

	b := connect(t, cfg)
	job := synthesis.Job{
		ID:      uuid.NewString(),
		Request: synthesis.Request{Text: "Hello", GenUploadURL: "https://a.example/u", StatusCallbackURL: "https://a.example/s"},
	}
	require.NoError(t, b.PublishJob(t.Context(), job, authorization))

	return job
}

func receive(t *testing.T, messages <-chan Message) Message {
	t.Helper()

	select {
	case m := <-messages:
		return m
	case <-time.After(10 * time.Second):
		require.FailNow(t, "no message was delivered")

		return Message{}
	}
}

func TestConsumer_HandsOverTheJobAndAcknowledgesIt(t *testing.T) {
	cfg, _ := startRabbit(t)
	job := publish(t, cfg, "Bearer abc")
	messages := make(chan Message, 1)

	stop := consume(t, cfg, func(_ context.Context, m Message) Outcome {
		messages <- m

		return Ack()
	})
	got := receive(t, messages)
	stop()

	assert.Equal(t, 1, got.Attempt)
	assert.Equal(t, "Bearer abc", got.Authorization)
	assert.Contains(t, string(got.Body), job.ID)
	assert.Never(t, func() bool { return ready(t, cfg, cfg.WorkQueue) > 0 }, time.Second, 200*time.Millisecond, "an acknowledged job must not come back")
	assert.Zero(t, ready(t, cfg, cfg.DeadLetterQueue))
}

func TestConsumer_DeadLettersAJobWhoseHandlerFailed(t *testing.T) {
	cfg, _ := startRabbit(t)
	publish(t, cfg, "")

	consume(t, cfg, func(context.Context, Message) Outcome { return Reject() })

	assert.Eventually(t, func() bool { return ready(t, cfg, cfg.DeadLetterQueue) == 1 }, 10*time.Second, 200*time.Millisecond)
	assert.Zero(t, ready(t, cfg, cfg.WorkQueue))
}

func TestConsumer_LeavesTheJobForRedeliveryWhenShutdownInterruptsIt(t *testing.T) {
	cfg, _ := startRabbit(t)
	job := publish(t, cfg, "")
	started := make(chan struct{}, 1)
	stop := consume(t, cfg, func(ctx context.Context, _ Message) Outcome {
		started <- struct{}{}
		<-ctx.Done()

		return Reject() // ignored: the context is cancelled.
	})
	receiveSignal(t, started)

	stop()
	again := make(chan Message, 1)
	consume(t, cfg, func(_ context.Context, m Message) Outcome {
		again <- m

		return Ack()
	})

	assert.Contains(t, string(receive(t, again).Body), job.ID)
	assert.Zero(t, ready(t, cfg, cfg.DeadLetterQueue))
}

func TestConsumer_TakesOneJobAtATime(t *testing.T) {
	cfg, _ := startRabbit(t)
	first := publish(t, cfg, "")
	started := make(chan struct{}, 1)
	consume(t, cfg, func(ctx context.Context, _ Message) Outcome {
		started <- struct{}{}
		<-ctx.Done() // never finishes the job, so the broker has to hold the next one back.

		return Reject() // ignored: the context is cancelled.
	})
	receiveSignal(t, started)
	second := publish(t, cfg, "")
	others := make(chan Message, 1)

	consume(t, cfg, func(_ context.Context, m Message) Outcome {
		others <- m

		return Ack()
	})

	got := receive(t, others)
	assert.Contains(t, string(got.Body), second.ID)
	assert.NotContains(t, string(got.Body), first.ID)
}

func receiveSignal(t *testing.T, signal <-chan struct{}) {
	t.Helper()

	select {
	case <-signal:
	case <-time.After(10 * time.Second):
		require.FailNow(t, "the handler never started")
	}
}

func TestConsumer_RetryReturnsTheJobAfterTheDelayWithTheNextAttempt(t *testing.T) {
	cfg, _ := startRabbit(t)
	job := publish(t, cfg, "Bearer abc")
	const delay = 300 * time.Millisecond
	type delivery struct {
		m  Message
		at time.Time
	}
	deliveries := make(chan delivery, 2)
	consume(t, cfg, func(_ context.Context, m Message) Outcome {
		deliveries <- delivery{m: m, at: time.Now()}
		if m.Attempt == 1 {
			return Retry(delay)
		}

		return Ack()
	})

	first := receiveDelivery(t, deliveries)
	second := receiveDelivery(t, deliveries)

	assert.Equal(t, 1, first.m.Attempt)
	assert.Equal(t, 2, second.m.Attempt)
	assert.Equal(t, "Bearer abc", second.m.Authorization)
	assert.Equal(t, first.m.Body, second.m.Body)
	assert.Contains(t, string(second.m.Body), job.ID)
	assert.GreaterOrEqual(t, second.at.Sub(first.at), delay)
	assert.Zero(t, ready(t, cfg, cfg.DeadLetterQueue))
}

func TestConsumer_DeadLettersTheJobWhenTheRetryCannotBePublished(t *testing.T) {
	cfg, _ := startRabbit(t)
	broker := connect(t, cfg)
	var calls atomic.Int32
	consume(t, cfg, func(context.Context, Message) Outcome {
		calls.Add(1)
		unbindWorkQueue(t, cfg) // the exchange now has no queue to take the retry.

		return Retry(0)
	})

	require.NoError(t, broker.PublishJob(t.Context(), synthesis.Job{ID: uuid.NewString()}, ""))

	assert.Eventually(t, func() bool { return ready(t, cfg, cfg.DeadLetterQueue) == 1 }, 10*time.Second, 200*time.Millisecond)
	assert.Equal(t, int32(1), calls.Load(), "the job is not handed over again")
	assert.Zero(t, ready(t, cfg, cfg.WorkQueue))
}

func TestConsumer_ReconnectsAfterTheConnectionDropped(t *testing.T) {
	cfg, _ := startRabbit(t)
	messages := make(chan Message, 2)
	c, _ := startConsumer(t, cfg, func(_ context.Context, m Message) Outcome {
		messages <- m

		return Ack()
	})
	require.NoError(t, c.conn.Close())

	job := publish(t, cfg, "")

	assert.Contains(t, string(receive(t, messages).Body), job.ID)
}

func receiveDelivery[T any](t *testing.T, deliveries <-chan T) T {
	t.Helper()

	select {
	case d := <-deliveries:
		return d
	case <-time.After(15 * time.Second):
		require.FailNow(t, "no message was delivered")

		var zero T

		return zero
	}
}

func unbindWorkQueue(t *testing.T, cfg config.RabbitMQ) {
	t.Helper()

	conn, err := amqp091.Dial(cfg.URL)
	require.NoError(t, err)
	defer conn.Close() //nolint:errcheck // test helper.
	ch, err := conn.Channel()
	require.NoError(t, err)
	require.NoError(t, ch.QueueUnbind(cfg.WorkQueue, synthesis.RoutingKey, cfg.Exchange, nil))
}
