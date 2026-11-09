package synthesis_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kasir-barati/piper-tts-rest-api/internal/synthesis"
)

func TestJob_MarshalsToTheFlatMessage(t *testing.T) {
	job := synthesis.Job{
		ID:      "e52cbe80-17de-4204-a983-bb1c10c36341",
		Request: synthesis.Request{Text: "Hello", GenUploadURL: "https://a.example/u", StatusCallbackURL: "https://a.example/s"},
	}

	got, err := json.Marshal(job)

	require.NoError(t, err)
	assert.JSONEq(t, `{"jobId":"e52cbe80-17de-4204-a983-bb1c10c36341","text":"Hello","genUploadUrl":"https://a.example/u","statusCallbackUrl":"https://a.example/s"}`, string(got))
}
