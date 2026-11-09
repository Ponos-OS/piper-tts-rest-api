package amqp

import (
	"testing"
	"time"

	amqp091 "github.com/rabbitmq/amqp091-go"
	"github.com/stretchr/testify/assert"
)

func TestNewPublishing_IsPersistentJSONWithMetadata(t *testing.T) {
	now := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)

	got := newPublishing("job-1", []byte(`{"jobId":"job-1"}`), "Bearer abc", now)

	assert.Equal(t, "application/json", got.ContentType)
	assert.Equal(t, amqp091.Persistent, got.DeliveryMode)
	assert.Equal(t, "job-1", got.MessageId)
	assert.Equal(t, now, got.Timestamp)
	assert.JSONEq(t, `{"jobId":"job-1"}`, string(got.Body))
	assert.Equal(t, amqp091.Table{"timestamp": now.UnixMilli(), "authorization": "Bearer abc"}, got.Headers)
}

func TestNewPublishing_OmitsAnEmptyAuthorization(t *testing.T) {
	got := newPublishing("job-1", nil, "", time.UnixMilli(1_700_000_000_000))

	assert.Equal(t, amqp091.Table{"timestamp": int64(1_700_000_000_000)}, got.Headers)
}
