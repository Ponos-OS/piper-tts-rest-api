//go:build integration

package amqp

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	amqp091 "github.com/rabbitmq/amqp091-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kasir-barati/piper-tts-rest-api/internal/config"
	"github.com/kasir-barati/piper-tts-rest-api/internal/synthesis"
)

var errHandler = errors.New("handler failed")

// consume starts a consumer and returns a function that stops it and waits until it is gone.
func consume(t *testing.T, cfg config.RabbitMQ, handle func(context.Context, Message) error) (stop func()) {
	t.Helper()

	c, err := NewConsumer(cfg)
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- c.Run(ctx, handle) }()

	var once sync.Once
	stop = func() {
		once.Do(func() {
			cancel()
			require.NoError(t, <-done)
			require.NoError(t, c.Close())
		})
	}
	t.Cleanup(stop)

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

	stop := consume(t, cfg, func(_ context.Context, m Message) error {
		messages <- m

		return nil
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

	consume(t, cfg, func(context.Context, Message) error { return errHandler })

	assert.Eventually(t, func() bool { return ready(t, cfg, cfg.DeadLetterQueue) == 1 }, 10*time.Second, 200*time.Millisecond)
	assert.Zero(t, ready(t, cfg, cfg.WorkQueue))
}

func TestConsumer_LeavesTheJobForRedeliveryWhenShutdownInterruptsIt(t *testing.T) {
	cfg, _ := startRabbit(t)
	job := publish(t, cfg, "")
	started := make(chan struct{}, 1)
	stop := consume(t, cfg, func(ctx context.Context, _ Message) error {
		started <- struct{}{}
		<-ctx.Done()

		return ctx.Err()
	})
	receiveSignal(t, started)

	stop()
	again := make(chan Message, 1)
	consume(t, cfg, func(_ context.Context, m Message) error {
		again <- m

		return nil
	})

	assert.Contains(t, string(receive(t, again).Body), job.ID)
	assert.Zero(t, ready(t, cfg, cfg.DeadLetterQueue))
}

func TestConsumer_TakesOneJobAtATime(t *testing.T) {
	cfg, _ := startRabbit(t)
	first := publish(t, cfg, "")
	started := make(chan struct{}, 1)
	consume(t, cfg, func(ctx context.Context, _ Message) error {
		started <- struct{}{}
		<-ctx.Done() // never finishes the job, so the broker has to hold the next one back.

		return ctx.Err()
	})
	receiveSignal(t, started)
	second := publish(t, cfg, "")
	others := make(chan Message, 1)

	consume(t, cfg, func(_ context.Context, m Message) error {
		others <- m

		return nil
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
