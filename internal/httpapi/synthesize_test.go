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

	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/synthesize", strings.NewReader(body))
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
	const urls = `"genUploadUrl":"https://a.example/u","statusCallbackUrl":"https://a.example/s"`
	tests := []struct {
		name   string
		body   string
		status int
	}{
		{"empty body", "", http.StatusBadRequest},
		{"missing urls", `{"text":"a"}`, http.StatusUnprocessableEntity},
		{"blank text", `{"text":" ",` + urls + `}`, http.StatusUnprocessableEntity},
		{"non-string text", `{"text":1,` + urls + `}`, http.StatusUnprocessableEntity},
		{"unknown field", `{"text":"a",` + urls + `,"voice":"x"}`, http.StatusUnprocessableEntity},
		{"malformed JSON", `{"text":`, http.StatusBadRequest},
		{"plain text", `hello`, http.StatusBadRequest},
		{"array", `["a"]`, http.StatusUnprocessableEntity},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			jobs := &fakePublisher{}

			rec := postSynthesize(t, newSynthesizeRouter(jobs, config.Callback{}, 1000), tt.body, nil)

			assert.Equal(t, tt.status, rec.Code)
			assert.Equal(t, "application/problem+json", rec.Header().Get("Content-Type"))
			assert.Empty(t, jobs.published)
		})
	}
}

func TestSynthesize_RejectsCallbackURLsBreakingThePolicy(t *testing.T) {
	jobs := &fakePublisher{}
	callback := config.Callback{RequireHTTPS: true, AllowedHosts: []string{"callbacks.example.com"}}
	body := `{"text":"Hello","genUploadUrl":"http://callbacks.example.com/u","statusCallbackUrl":"https://other.example.com/s"}`

	rec := postSynthesize(t, newSynthesizeRouter(jobs, callback, 1000), body, nil)

	assert.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	var problem struct {
		Errors []struct {
			Message  string `json:"message"`
			Location string `json:"location"`
		} `json:"errors"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &problem))
	require.Len(t, problem.Errors, 2)
	assert.Equal(t, "body.genUploadUrl", problem.Errors[0].Location)
	assert.Equal(t, "https", problem.Errors[0].Message)
	assert.Equal(t, "body.statusCallbackUrl", problem.Errors[1].Location)
	assert.Equal(t, "host", problem.Errors[1].Message)
	assert.Empty(t, jobs.published)
}

func TestSynthesize_RejectsOversizedBody(t *testing.T) {
	jobs := &fakePublisher{}

	rec := postSynthesize(t, newSynthesizeRouter(jobs, config.Callback{}, 20), validSynthesizeBody, nil)

	assert.Equal(t, http.StatusRequestEntityTooLarge, rec.Code)
	assert.Empty(t, jobs.published)
}

func TestSynthesize_AnswersTheBrokerFailureWith503(t *testing.T) {
	jobs := &fakePublisher{err: errors.New("amqp://user:secret@rabbit: connection refused")} //nolint:err113 // a one-off test error.

	rec := postSynthesize(t, newSynthesizeRouter(jobs, config.Callback{}, 1000), validSynthesizeBody, nil)

	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
	assert.Contains(t, rec.Body.String(), "Unable to accept synthesize job")
	assert.NotContains(t, rec.Body.String(), "secret")
}
