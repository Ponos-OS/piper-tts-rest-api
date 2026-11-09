package httpapi_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kasir-barati/piper-tts-rest-api/internal/config"
	"github.com/kasir-barati/piper-tts-rest-api/internal/httpapi"
	"github.com/kasir-barati/piper-tts-rest-api/internal/logging"
	"github.com/kasir-barati/piper-tts-rest-api/internal/synthesis"
)

const validSynthesizeBody = `{"text":"  Hello world  ","genUploadUrl":"https://Callbacks.Example.com/upload","statusCallbackUrl":"https://callbacks.example.com/status"}`

type publishedJob struct {
	job           synthesis.Job
	authorization string
	hasDeadline   bool
}

type fakePublisher struct {
	err       error
	published []publishedJob
}

func (f *fakePublisher) PublishJob(ctx context.Context, job synthesis.Job, authorization string) error {
	_, hasDeadline := ctx.Deadline()
	f.published = append(f.published, publishedJob{job: job, authorization: authorization, hasDeadline: hasDeadline})

	return f.err
}

func newSynthesizeRouter(jobs httpapi.JobPublisher, callback config.Callback, maxBody int) http.Handler {
	logger := logging.New(config.Log{Mode: config.LogModeJSON, Level: config.LogLevelError}, "svc", io.Discard)

	return httpapi.NewRouter(httpapi.Options{
		Logger:           logger,
		Jobs:             jobs,
		MaxConcurrency:   1,
		MaxBodySizeBytes: 1000,
		Synthesize:       httpapi.SynthesizeOptions{MaxBodySizeBytes: maxBody, Callback: callback},
	})
}

func postSynthesize(t *testing.T, h http.Handler, body string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/synthesize", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	return rec
}

func TestSynthesize_QueuesTheJobAndReturns202(t *testing.T) {
	jobs := &fakePublisher{}
	router := newSynthesizeRouter(jobs, config.Callback{RequireHTTPS: true}, 1000)

	rec := postSynthesize(t, router, validSynthesizeBody, map[string]string{"Authorization": "Bearer abc"})

	require.Equal(t, http.StatusAccepted, rec.Code)
	var resp struct {
		JobID string `json:"jobId"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Len(t, jobs.published, 1)
	got := jobs.published[0]
	assert.Regexp(t, `^[0-9a-f-]{36}$`, resp.JobID)
	assert.Equal(t, resp.JobID, got.job.ID)
	assert.Equal(t, synthesis.Request{
		Text:              "Hello world",
		GenUploadURL:      "https://callbacks.example.com/upload",
		StatusCallbackURL: "https://callbacks.example.com/status",
	}, got.job.Request)
	assert.Equal(t, "Bearer abc", got.authorization)
	assert.True(t, got.hasDeadline, "a stuck broker must not hold the request forever")
}

func TestSynthesize_WorksWithoutAnAuthorizationHeader(t *testing.T) {
	jobs := &fakePublisher{}
	router := newSynthesizeRouter(jobs, config.Callback{}, 1000)

	rec := postSynthesize(t, router, validSynthesizeBody, nil)

	require.Equal(t, http.StatusAccepted, rec.Code)
	assert.Empty(t, jobs.published[0].authorization)
}

func TestSynthesize_RejectsInvalidBodies(t *testing.T) {
	tests := []struct {
		name   string
		body   string
		errMsg string
		fields []map[string]any
	}{
		{"empty body", "", "Invalid request body", []map[string]any{
			{"field": "text", "rule": "required"},
			{"field": "genUploadUrl", "rule": "required"},
			{"field": "statusCallbackUrl", "rule": "required"},
		}},
		{"blank text", `{"text":" ","genUploadUrl":"https://a.example/u","statusCallbackUrl":"https://a.example/s"}`, "Invalid request body", []map[string]any{{"field": "text", "rule": "notblank"}}},
		{"malformed JSON", `{"text":`, "Invalid JSON body", nil},
		{"unknown field", `{"text":"a","genUploadUrl":"https://a.example/u","statusCallbackUrl":"https://a.example/s","voice":"x"}`, "Invalid JSON body", nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			jobs := &fakePublisher{}

			rec := postSynthesize(t, newSynthesizeRouter(jobs, config.Callback{}, 1000), tt.body, nil)

			assert.Equal(t, http.StatusBadRequest, rec.Code)
			var parsed struct {
				Error  string           `json:"error"`
				Fields []map[string]any `json:"fields"`
			}
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &parsed))
			assert.Equal(t, tt.errMsg, parsed.Error)
			assert.Equal(t, tt.fields, parsed.Fields)
			assert.Empty(t, jobs.published)
		})
	}
}

func TestSynthesize_RejectsCallbackURLsBreakingThePolicy(t *testing.T) {
	jobs := &fakePublisher{}
	callback := config.Callback{RequireHTTPS: true, AllowedHosts: []string{"callbacks.example.com"}}
	body := `{"text":"Hello","genUploadUrl":"http://callbacks.example.com/u","statusCallbackUrl":"https://other.example.com/s"}`

	rec := postSynthesize(t, newSynthesizeRouter(jobs, callback, 1000), body, nil)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.JSONEq(t, `{"error":"Invalid callback URL","fields":[{"field":"genUploadUrl","rule":"https"},{"field":"statusCallbackUrl","rule":"host"}]}`, rec.Body.String())
	assert.Empty(t, jobs.published)
}

func TestSynthesize_RejectsOversizedBody(t *testing.T) {
	jobs := &fakePublisher{}

	rec := postSynthesize(t, newSynthesizeRouter(jobs, config.Callback{}, 20), validSynthesizeBody, nil)

	assert.Equal(t, http.StatusRequestEntityTooLarge, rec.Code)
	assert.JSONEq(t, `{"error":"Request body too large"}`, rec.Body.String())
	assert.Empty(t, jobs.published)
}

func TestSynthesize_AnswersTheBrokerFailureWith503(t *testing.T) {
	jobs := &fakePublisher{err: errors.New("amqp://user:secret@rabbit: connection refused")} //nolint:err113 // a one-off test error.

	rec := postSynthesize(t, newSynthesizeRouter(jobs, config.Callback{}, 1000), validSynthesizeBody, nil)

	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
	assert.JSONEq(t, `{"error":"Unable to accept synthesize job"}`, rec.Body.String())
}
