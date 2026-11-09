package amqp

import (
	"testing"
	"time"

	amqp091 "github.com/rabbitmq/amqp091-go"
	"github.com/stretchr/testify/assert"
	"go.opentelemetry.io/otel/propagation"
)

func TestNewMessage_ReadsAttemptAndAuthorizationFromHeaders(t *testing.T) {
	tests := []struct {
		name        string
		headers     amqp091.Table
		wantAttempt int
		wantAuth    string
	}{
		{"no headers", nil, 1, ""},
		{"attempt as int32", amqp091.Table{"x-attempt": int32(3)}, 3, ""},
		{"attempt as int64", amqp091.Table{"x-attempt": int64(4)}, 4, ""},
		{"attempt as int", amqp091.Table{"x-attempt": 5}, 5, ""},
		{"zero attempt", amqp091.Table{"x-attempt": int32(0)}, 1, ""},
		{"negative attempt", amqp091.Table{"x-attempt": int32(-2)}, 1, ""},
		{"attempt of another type", amqp091.Table{"x-attempt": "2"}, 1, ""},
		{"authorization", amqp091.Table{"authorization": "Bearer abc"}, 1, "Bearer abc"},
		{"authorization of another type", amqp091.Table{"authorization": 7}, 1, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := amqp091.Delivery{Body: []byte("body"), Headers: tt.headers}

			got := newMessage(d)

			assert.Equal(t, []byte("body"), got.Body)
			assert.Equal(t, tt.wantAttempt, got.Attempt)
			assert.Equal(t, tt.wantAuth, got.Authorization)
		})
	}
}

func TestRetryHeaders_KeepsTheCallersHeadersDropsTheBrokersAndSetsTheAttemptAndTheDeliveryTime(t *testing.T) {
	headers := amqp091.Table{
		"authorization":       "Bearer abc",
		"timestamp":           int64(1700000000000),
		"traceparent":         "00-abc-def-01",
		"x-attempt":           int32(1),
		"x-opt-delivery-time": int64(1),
		"x-death":             []any{"anything"},
		"x-delivery-count":    int64(2),
	}

	due := time.UnixMilli(1700000005000)

	got := retryHeaders(headers, 3, due)

	assert.Equal(t, amqp091.Table{
		"authorization":       "Bearer abc",
		"timestamp":           int64(1700000000000),
		"traceparent":         "00-abc-def-01",
		"x-attempt":           3,
		"x-opt-delivery-time": int64(1700000005000),
	}, got)
}

func TestHeaderCarrier_CarriesTheTraceContextThroughMessageHeaders(t *testing.T) {
	const traceparent = "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"
	propagator := propagation.TraceContext{}

	ctx := propagator.Extract(t.Context(), headerCarrier(amqp091.Table{"traceparent": traceparent}))
	out := amqp091.Table{}
	propagator.Inject(ctx, headerCarrier(out))

	assert.Equal(t, traceparent, out["traceparent"])
}

func TestHeaderCarrier_IgnoresHeadersThatAreNotStrings(t *testing.T) {
	carrier := headerCarrier(amqp091.Table{"x-attempt": int32(2)})

	assert.Empty(t, carrier.Get("x-attempt"))
	assert.Equal(t, []string{"x-attempt"}, carrier.Keys())
}
