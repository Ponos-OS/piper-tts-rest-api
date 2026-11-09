package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kasir-barati/piper-tts-rest-api/internal/config"
	"github.com/kasir-barati/piper-tts-rest-api/internal/httpapi"
	"github.com/kasir-barati/piper-tts-rest-api/internal/logging"
)

func newRouter() (http.Handler, *bytes.Buffer) {
	out := &bytes.Buffer{}
	logger := logging.New(config.Log{Mode: config.LogModeJSON, Level: config.LogLevelInfo}, "svc", out)

	// The tests using this router never reach /speak, so no synthesizer is needed.
	return httpapi.NewRouter(logger, nil, httpapi.Options{MaxConcurrency: 1, MaxBodySizeBytes: 1000}), out
}

func do(t *testing.T, h http.Handler, method, target string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequestWithContext(t.Context(), method, target, http.NoBody)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	return rec
}

func logLines(t *testing.T, out *bytes.Buffer) []map[string]any {
	t.Helper()

	raws := strings.Split(strings.TrimSpace(out.String()), "\n")
	lines := make([]map[string]any, 0, len(raws))
	for _, raw := range raws {
		var line map[string]any
		require.NoError(t, json.Unmarshal([]byte(raw), &line))
		lines = append(lines, line)
	}

	return lines
}

func TestHealth_ReturnsPlainTextOK(t *testing.T) {
	router, _ := newRouter()

	rec := do(t, router, http.MethodGet, "/health", nil)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "text/plain; charset=utf-8", rec.Header().Get("Content-Type"))
	assert.Equal(t, "OK", rec.Body.String())
}

func TestHealth_IsNotLogged(t *testing.T) {
	router, out := newRouter()

	do(t, router, http.MethodGet, "/health", nil)

	assert.Empty(t, out.String())
}

func TestUnknownRoutes_ReturnPlainText404(t *testing.T) {
	tests := []struct{ method, target string }{
		{http.MethodGet, "/nope"},
		{http.MethodPost, "/health"},
	}

	for _, tt := range tests {
		t.Run(tt.method+" "+tt.target, func(t *testing.T) {
			router, _ := newRouter()

			rec := do(t, router, tt.method, tt.target, nil)

			assert.Equal(t, http.StatusNotFound, rec.Code)
			assert.Equal(t, "text/plain; charset=utf-8", rec.Header().Get("Content-Type"))
			assert.Equal(t, "Not Found", rec.Body.String())
		})
	}
}

func TestRequestLogging_LogsIncomingAndCompleted(t *testing.T) {
	router, out := newRouter()

	do(t, router, http.MethodPost, "/other?x=1", map[string]string{"X-Request-Id": "abc"})
	lines := logLines(t, out)

	require.Len(t, lines, 2)
	assert.Equal(t, "Incoming POST /other?x=1", lines[0]["message"])
	assert.Equal(t, "HttpMiddleware", lines[0]["context"])
	assert.Equal(t, "POST", lines[0]["method"])
	assert.Equal(t, "/other?x=1", lines[0]["url"])
	assert.Equal(t, map[string]any{"X-Request-Id": []any{"abc"}}, lines[0]["headers"])
	assert.Equal(t, "Completed POST /other?x=1", lines[1]["message"])
	assert.InDelta(t, http.StatusNotFound, lines[1]["statusCode"], 0)
	assert.Contains(t, lines[1], "duration")
	assert.Equal(t, false, lines[1]["aborted"])
}

func TestRequestLogging_RedactsSecretHeaders(t *testing.T) {
	headers := map[string]string{
		"Authorization": "Bearer abc123",
		"Cookie":        "session=abc123",
		"X-Api-Key":     "abc123",
		"User-Agent":    "curl/8",
	}
	router, out := newRouter()

	do(t, router, http.MethodGet, "/x", headers)
	lines := logLines(t, out)

	assert.Equal(t, map[string]any{
		"Authorization": []any{"Bearer [REDACTED]"},
		"Cookie":        []any{"[REDACTED]"},
		"X-Api-Key":     []any{"[REDACTED]"},
		"User-Agent":    []any{"curl/8"},
	}, lines[0]["headers"])
	assert.NotContains(t, out.String(), "abc123")
}

func TestRequestLogging_RedactsSecretQueryParameters(t *testing.T) {
	tests := []struct{ target, logged string }{
		{"/x", "/x"},
		{"/x?lang=en", "/x?lang=en"},
		{"/x?token=abc123&lang=en", "/x?token=[REDACTED]&lang=en"},
		{"/x?lang=en&Access_Token=abc123", "/x?lang=en&Access_Token=[REDACTED]"},
		{"/x?X-Amz-Signature=abc123&a=1", "/x?X-Amz-Signature=[REDACTED]&a=1"},
		{"/x?api%5Fkey=abc123", "/x?api%5Fkey=[REDACTED]"},
	}

	for _, tt := range tests {
		t.Run(tt.target, func(t *testing.T) {
			router, out := newRouter()

			do(t, router, http.MethodGet, tt.target, nil)
			lines := logLines(t, out)

			assert.Equal(t, tt.logged, lines[0]["url"])
			assert.Equal(t, tt.logged, lines[1]["url"])
			assert.NotContains(t, out.String(), "abc123")
		})
	}
}

func TestServe_DrainsInFlightRequestsOnShutdown(t *testing.T) {
	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)
	started := make(chan struct{})
	release := make(chan struct{})
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(started)
		<-release
		w.WriteHeader(http.StatusAccepted)
	})
	ctx, cancel := context.WithCancel(t.Context())
	served := make(chan error, 1)
	returned := make(chan struct{})
	go func() {
		served <- httpapi.Serve(ctx, ln, handler)
		close(returned)
	}()
	response := make(chan fetched, 1)
	go func() { response <- fetch(t.Context(), "http://"+ln.Addr().String()) }()
	<-started

	cancel()
	returnedEarly := func() bool {
		select {
		case <-returned:
			return true
		default:
			return false
		}
	}
	assert.Never(t, returnedEarly, 200*time.Millisecond, 10*time.Millisecond)
	close(release)

	require.NoError(t, <-served)
	got := <-response
	require.NoError(t, got.err)
	assert.Equal(t, http.StatusAccepted, got.status)
}

type fetched struct {
	status int
	err    error
}

func fetch(ctx context.Context, url string) fetched {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, http.NoBody)
	if err != nil {
		return fetched{err: err}
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fetched{err: err}
	}
	_, drainErr := io.Copy(io.Discard, resp.Body)

	return fetched{status: resp.StatusCode, err: errors.Join(drainErr, resp.Body.Close())}
}
