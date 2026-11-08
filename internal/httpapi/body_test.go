package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type sample struct {
	Text  string `json:"text" validate:"required,notblank"`
	Count int    `json:"count" validate:"omitempty,gte=1"`
}

func decodeSample(t *testing.T, body string, maxBytes int) (sample, error) {
	t.Helper()
	r := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/", strings.NewReader(body))

	return decodeJSON[sample](httptest.NewRecorder(), r, maxBytes)
}

func TestDecodeJSON_Valid(t *testing.T) {
	tests := []struct {
		name string
		body string
		want sample
	}{
		{"text only", `{"text":"hello"}`, sample{Text: "hello"}},
		{"text and count", `{"text":"hello","count":2}`, sample{Text: "hello", Count: 2}},
		{"surrounding whitespace", " \n{\"text\":\"hello\"}\n ", sample{Text: "hello"}},
		{"exactly the size limit", `{"text":"hello"}`, sample{Text: "hello"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := decodeSample(t, tt.body, len(tt.body))

			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestDecodeJSON_ValidationErrors(t *testing.T) {
	tests := []struct {
		name string
		body string
		want []fieldError
	}{
		{"empty body", ``, []fieldError{{"text", "required"}}},
		{"empty object", `{}`, []fieldError{{"text", "required"}}},
		{"null text", `{"text":null}`, []fieldError{{"text", "required"}}},
		{"blank text", `{"text":"  \n"}`, []fieldError{{"text", "notblank"}}},
		{"number as text", `{"text":42}`, []fieldError{{"text", "type"}}},
		{"object as text", `{"text":{"a":1}}`, []fieldError{{"text", "type"}}},
		{"string as count", `{"text":"hi","count":"2"}`, []fieldError{{"count", "type"}}},
		{"count below minimum", `{"text":"hi","count":-1}`, []fieldError{{"count", "gte"}}},
		{"several problems", `{"text":"","count":-1}`, []fieldError{{"text", "required"}, {"count", "gte"}}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := decodeSample(t, tt.body, 1000)

			var got *validationError
			require.ErrorAs(t, err, &got)
			assert.Equal(t, tt.want, got.Fields)
		})
	}
}

func TestDecodeJSON_InvalidJSON(t *testing.T) {
	tests := []struct{ name, body string }{
		{"not json", `hello world`},
		{"truncated", `{"text":"hi"`},
		{"array instead of object", `["hi"]`},
		{"unknown field", `{"text":"hi","extra":1}`},
		{"two objects", `{"text":"hi"}{"text":"again"}`},
		{"trailing garbage", `{"text":"hi"} nope`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := decodeSample(t, tt.body, 1000)

			require.ErrorIs(t, err, errInvalidJSON)
		})
	}
}

func TestDecodeJSON_BodyTooLarge(t *testing.T) {
	body := `{"text":"hello"}`

	_, err := decodeSample(t, body, len(body)-1)

	require.ErrorIs(t, err, errBodyTooLarge)
}
