//go:build integration

package amqp

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	amqp091 "github.com/rabbitmq/amqp091-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go/modules/rabbitmq"

	"github.com/kasir-barati/piper-tts-rest-api/internal/config"
	"github.com/kasir-barati/piper-tts-rest-api/internal/synthesis"
)

const rabbitImage = "rabbitmq:4.4-management-alpine"

// startRabbit runs a throwaway RabbitMQ and returns the broker config and the management API base URL.
func startRabbit(t *testing.T) (config.RabbitMQ, string) {
	t.Helper()

	container, err := rabbitmq.Run(t.Context(), rabbitImage)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, container.Terminate(context.WithoutCancel(t.Context()))) })

	amqpURL, err := container.AmqpURL(t.Context())
	require.NoError(t, err)
	httpURL, err := container.HttpURL(t.Context())
	require.NoError(t, err)

	return config.RabbitMQ{
		URL:                amqpURL,
		Exchange:           "synthesize",
		WorkQueue:          "synthesize.work",
		DeadLetterExchange: "synthesize.dlx",
		DeadLetterQueue:    "synthesize.dlq",
	}, httpURL
}

func connect(t *testing.T, cfg config.RabbitMQ) *Broker {
	t.Helper()

	b, err := Connect(cfg)
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, b.Close()) })

	return b
}

// managementJSON reads a management API resource. Statistics lag a little behind declarations, so it waits for the resource to exist.
func managementJSON(t *testing.T, baseURL, path string, into any) {
	t.Helper()

	require.Eventually(t, func() bool {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, baseURL+path, http.NoBody)
		require.NoError(t, err)
		req.SetBasicAuth("guest", "guest")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return false
		}
		defer resp.Body.Close() //nolint:errcheck // a test.

		return resp.StatusCode == http.StatusOK && json.NewDecoder(resp.Body).Decode(into) == nil
	}, 30*time.Second, 200*time.Millisecond, "management API never returned %s", path)
}

type queueInfo struct {
	Type       string         `json:"type"`
	Durable    bool           `json:"durable"`
	AutoDelete bool           `json:"auto_delete"`
	Exclusive  bool           `json:"exclusive"`
	Arguments  map[string]any `json:"arguments"`
}

type exchangeInfo struct {
	Type       string `json:"type"`
	Durable    bool   `json:"durable"`
	AutoDelete bool   `json:"auto_delete"`
}

type bindingInfo struct {
	Source     string `json:"source"`
	RoutingKey string `json:"routing_key"`
}

func TestConnect_DeclaresTheTopology(t *testing.T) {
	cfg, api := startRabbit(t)
	connect(t, cfg)

	for _, name := range []string{"synthesize", "synthesize.dlx"} {
		var exchange exchangeInfo
		managementJSON(t, api, "/api/exchanges/%2F/"+name, &exchange)
		assert.Equal(t, exchangeInfo{Type: "direct", Durable: true}, exchange, name)
	}

	wantArguments := map[string]map[string]any{
		"synthesize.work": {
			"x-queue-type":              "quorum",
			"x-delivery-limit":          float64(5),
			"x-dead-letter-exchange":    "synthesize.dlx",
			"x-dead-letter-routing-key": "synthesize",
		},
		"synthesize.dlq": {"x-queue-type": "quorum"},
	}
	for name, arguments := range wantArguments {
		var queue queueInfo
		managementJSON(t, api, "/api/queues/%2F/"+name, &queue)
		assert.Equal(t, queueInfo{Type: "quorum", Durable: true, Arguments: arguments}, queue, name)
	}

	var work, dlq []bindingInfo
	managementJSON(t, api, "/api/queues/%2F/synthesize.work/bindings", &work)
	managementJSON(t, api, "/api/queues/%2F/synthesize.dlq/bindings", &dlq)
	assert.Contains(t, work, bindingInfo{Source: "synthesize", RoutingKey: "synthesize"})
	assert.Contains(t, dlq, bindingInfo{Source: "synthesize.dlx", RoutingKey: "synthesize"})
}

func TestConnect_IsRepeatable(t *testing.T) {
	cfg, _ := startRabbit(t)
	connect(t, cfg)

	second, err := Connect(cfg)

	require.NoError(t, err)
	require.NoError(t, second.Close())
}

// get reads one message from the work queue over a separate connection.
func get(t *testing.T, cfg config.RabbitMQ) amqp091.Delivery {
	t.Helper()

	conn, err := amqp091.Dial(cfg.URL)
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, conn.Close()) })
	ch, err := conn.Channel()
	require.NoError(t, err)

	var delivery amqp091.Delivery
	require.Eventually(t, func() bool {
		var ok bool
		var err error
		delivery, ok, err = ch.Get(cfg.WorkQueue, true)

		return err == nil && ok
	}, 10*time.Second, 100*time.Millisecond, "no message in the work queue")

	return delivery
}

func TestPublishJob_DeliversAPersistentMessageToTheWorkQueue(t *testing.T) {
	cfg, _ := startRabbit(t)
	b := connect(t, cfg)
	job := synthesis.Job{
		ID:      uuid.NewString(),
		Request: synthesis.Request{Text: "Hello", GenUploadURL: "https://a.example/u", StatusCallbackURL: "https://a.example/s"},
	}

	require.NoError(t, b.PublishJob(t.Context(), job, "Bearer abc"))
	got := get(t, cfg)

	assert.Equal(t, "application/json", got.ContentType)
	assert.Equal(t, amqp091.Persistent, got.DeliveryMode)
	assert.Equal(t, job.ID, got.MessageId)
	assert.Equal(t, "Bearer abc", got.Headers["authorization"])
	assert.IsType(t, int64(0), got.Headers["timestamp"])
	assert.JSONEq(t,
		`{"jobId":"`+job.ID+`","text":"Hello","genUploadUrl":"https://a.example/u","statusCallbackUrl":"https://a.example/s"}`,
		string(got.Body))
}

func TestPublishJob_ReconnectsAfterTheConnectionDropped(t *testing.T) {
	cfg, _ := startRabbit(t)
	b := connect(t, cfg)
	require.NoError(t, b.conn.Close())
	job := synthesis.Job{ID: uuid.NewString()}

	require.NoError(t, b.PublishJob(t.Context(), job, ""))

	assert.Equal(t, job.ID, get(t, cfg).MessageId)
}

func TestPublishJob_FailsAfterClose(t *testing.T) {
	cfg, _ := startRabbit(t)
	b, err := Connect(cfg)
	require.NoError(t, err)
	require.NoError(t, b.Close())

	err = b.PublishJob(t.Context(), synthesis.Job{ID: uuid.NewString()}, "")

	require.ErrorIs(t, err, errClosed)
	require.NoError(t, b.Close(), "closing twice is fine")
}
