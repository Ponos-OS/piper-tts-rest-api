package amqp

import (
	"testing"

	amqp091 "github.com/rabbitmq/amqp091-go"
	"github.com/stretchr/testify/assert"
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
