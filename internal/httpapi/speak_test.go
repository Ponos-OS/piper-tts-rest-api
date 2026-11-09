package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kasir-barati/piper-tts-rest-api/internal/config"
	"github.com/kasir-barati/piper-tts-rest-api/internal/httpapi"
	"github.com/kasir-barati/piper-tts-rest-api/internal/logging"
	"github.com/kasir-barati/piper-tts-rest-api/internal/piper"
)

// Fake piper bodies. ffmpeg is always `cat`, so the "MP3" is whatever piper prints.
const (
	echoPiper = "printf 'pcm:%s' \"$(cat)\"\n"

	// Input "hang" never produces audio and records its pid; any other input is echoed.
	hangingPiper = `text=$(cat)
case "$text" in
hang) echo $$ > "$EVIDENCE/piper.pid"; exec sleep 60;;
esac
printf 'pcm:%s' "$text"
`
)

// Piper prints some audio, waits until ffmpeg has relayed it, then fails.
// Failing at once would kill ffmpeg (the first failure stops the other process) before it relays anything.
const partialThenFailingPiper = "cat >/dev/null\nprintf partial\nsleep 0.5\nexit 1\n"

type speakResult struct {
	status int
	body   string
	err    error
}

// newSpeakServer starts the real router on a test server, backed by fake piper and ffmpeg scripts.
func newSpeakServer(t *testing.T, piperBody string, opts httpapi.Options) (*httptest.Server, string, *syncBuffer) {
	t.Helper()

	binDir, evidence := t.TempDir(), t.TempDir()
	writeScript(t, filepath.Join(binDir, "piper"), "case \"$1\" in --help) exit 0;; esac\n"+piperBody)
	writeScript(t, filepath.Join(binDir, "ffmpeg"), "case \"$1\" in -version) exit 0;; esac\ncat\n")
	t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))
	t.Setenv("EVIDENCE", evidence)

	model := filepath.Join(t.TempDir(), "voice.onnx")
	require.NoError(t, os.WriteFile(model, nil, 0o600))
	require.NoError(t, os.WriteFile(model+".json", []byte(`{"audio":{"sample_rate":22050}}`), 0o600))
	synth, err := piper.New(t.Context(), model)
	require.NoError(t, err)

	logs := &syncBuffer{}
	logger := logging.New(config.Log{Mode: config.LogModeJSON, Level: config.LogLevelInfo}, "svc", logs)
	srv := httptest.NewServer(httpapi.NewRouter(logger, synth, opts))
	t.Cleanup(srv.Close)

	return srv, evidence, logs
}

// syncBuffer is a bytes.Buffer that the server goroutines and the test can use together.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.buf.Write(p) //nolint:wrapcheck // a test helper.
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.buf.String()
}

type completedLog struct {
	Message    string `json:"message"`
	StatusCode int    `json:"statusCode"`
	Aborted    bool   `json:"aborted"`
}

// completedLogs waits until n "Completed" log lines exist and returns them.
// The line is written after the response is sent, so the test may get here first.
func completedLogs(t *testing.T, logs *syncBuffer, n int) []completedLog {
	t.Helper()

	var found []completedLog
	require.Eventually(t, func() bool {
		found = nil
		for _, raw := range strings.Split(strings.TrimSpace(logs.String()), "\n") {
			var line completedLog
			if json.Unmarshal([]byte(raw), &line) == nil && strings.HasPrefix(line.Message, "Completed") {
				found = append(found, line)
			}
		}

		return len(found) == n
	}, 5*time.Second, 10*time.Millisecond, "expected %d Completed log lines", n)

	return found
}

func writeScript(t *testing.T, path, body string) {
	t.Helper()
	require.NoError(t, os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o700)) //nolint:gosec // an executable test script.
}

func defaultOpts() httpapi.Options {
	return httpapi.Options{MaxConcurrency: 2, MaxBodySizeBytes: 1000}
}

func postSpeak(ctx context.Context, srv *httptest.Server, body string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, srv.URL+"/speak", strings.NewReader(body))
	if err != nil {
		return nil, err //nolint:wrapcheck // a test helper.
	}
	req.Header.Set("Content-Type", "application/json")

	return srv.Client().Do(req) //nolint:wrapcheck,gosec // a test helper talking to a local test server.
}

func speak(ctx context.Context, srv *httptest.Server, body string) speakResult {
	resp, err := postSpeak(ctx, srv, body)
	if err != nil {
		return speakResult{err: err}
	}
	defer resp.Body.Close() //nolint:errcheck // a test helper.
	data, err := io.ReadAll(resp.Body)

	return speakResult{status: resp.StatusCode, body: string(data), err: err}
}

func waitForPid(t *testing.T, evidence string) int {
	t.Helper()

	var pid int
	require.Eventually(t, func() bool {
		data, err := os.ReadFile(filepath.Join(evidence, "piper.pid")) //nolint:gosec // a test file.
		if err != nil || strings.TrimSpace(string(data)) == "" {
			return false
		}
		pid, err = strconv.Atoi(strings.TrimSpace(string(data)))

		return err == nil
	}, 5*time.Second, 10*time.Millisecond, "piper never started")

	return pid
}

func TestSpeak_StreamsMP3WithHeaders(t *testing.T) {
	srv, _, logs := newSpeakServer(t, echoPiper, defaultOpts())

	resp, err := postSpeak(t.Context(), srv, `{"text":"hello world"}`)
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, resp.Body.Close()) })
	body, err := io.ReadAll(resp.Body)

	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "audio/mpeg", resp.Header.Get("Content-Type"))
	assert.Equal(t, "no-store", resp.Header.Get("Cache-Control"))
	assert.Regexp(t, `^inline; filename="\d+-[A-Z2-7]+\.mp3"$`, resp.Header.Get("Content-Disposition"))
	assert.Equal(t, []string{"chunked"}, resp.TransferEncoding)
	assert.Equal(t, "pcm:hello world", string(body))
	done := completedLogs(t, logs, 1)[0]
	assert.Equal(t, http.StatusOK, done.StatusCode)
	assert.False(t, done.Aborted)
}

func TestSpeak_TrimsText(t *testing.T) {
	srv, _, _ := newSpeakServer(t, echoPiper, defaultOpts())

	got := speak(t.Context(), srv, `{"text":"  hello  "}`)

	require.NoError(t, got.err)
	assert.Equal(t, "pcm:hello", got.body)
}

func TestSpeak_RejectsInvalidBodies(t *testing.T) {
	tests := []struct {
		name   string
		body   string
		errMsg string
		fields []map[string]any
	}{
		{"empty body", "", "Invalid request body", []map[string]any{{"field": "text", "rule": "required"}}},
		{"blank text", `{"text":"  "}`, "Invalid request body", []map[string]any{{"field": "text", "rule": "notblank"}}},
		{"non-string text", `{"text":1}`, "Invalid request body", []map[string]any{{"field": "text", "rule": "type"}}},
		{"malformed JSON", `{"text":`, "Invalid JSON body", nil},
		{"unknown field", `{"text":"a","voice":"b"}`, "Invalid JSON body", nil},
		{"plain text", "hello", "Invalid JSON body", nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv, _, _ := newSpeakServer(t, echoPiper, defaultOpts())

			got := speak(t.Context(), srv, tt.body)

			require.NoError(t, got.err)
			assert.Equal(t, http.StatusBadRequest, got.status)
			var parsed struct {
				Error  string           `json:"error"`
				Fields []map[string]any `json:"fields"`
			}
			require.NoError(t, json.Unmarshal([]byte(got.body), &parsed))
			assert.Equal(t, tt.errMsg, parsed.Error)
			assert.Equal(t, tt.fields, parsed.Fields)
		})
	}
}

func TestSpeak_RejectsOversizedBody(t *testing.T) {
	srv, _, _ := newSpeakServer(t, echoPiper, httpapi.Options{MaxConcurrency: 1, MaxBodySizeBytes: 10})

	got := speak(t.Context(), srv, `{"text":"this is longer than ten bytes"}`)

	require.NoError(t, got.err)
	assert.Equal(t, http.StatusRequestEntityTooLarge, got.status)
	assert.JSONEq(t, `{"error":"Request body too large"}`, got.body)
}

func TestSpeak_PiperFailingBeforeAnyAudioIs500(t *testing.T) {
	srv, _, logs := newSpeakServer(t, "cat >/dev/null\nexit 1\n", defaultOpts())

	got := speak(t.Context(), srv, `{"text":"hello"}`)

	require.NoError(t, got.err)
	assert.Equal(t, http.StatusInternalServerError, got.status)
	assert.JSONEq(t, `{"error":"Synthesis failed"}`, got.body)
	done := completedLogs(t, logs, 1)[0]
	assert.Equal(t, http.StatusInternalServerError, done.StatusCode)
	assert.False(t, done.Aborted)
}

func TestSpeak_FailureAfterAudioStartedAbortsTheConnection(t *testing.T) {
	srv, _, logs := newSpeakServer(t, partialThenFailingPiper, defaultOpts())

	got := speak(t.Context(), srv, `{"text":"hello"}`)

	require.Error(t, got.err, "the client must not mistake a truncated MP3 for a complete one")
	assert.Equal(t, http.StatusOK, got.status)
	done := completedLogs(t, logs, 1)[0]
	assert.Equal(t, http.StatusOK, done.StatusCode, "headers were already sent")
	assert.True(t, done.Aborted)
}

func TestSpeak_ClientDisconnectKillsPiperAndFreesTheSlot(t *testing.T) {
	srv, evidence, logs := newSpeakServer(t, hangingPiper, httpapi.Options{MaxConcurrency: 1, MaxBodySizeBytes: 1000})
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	first := make(chan speakResult, 1)
	go func() { first <- speak(ctx, srv, `{"text":"hang"}`) }()
	pid := waitForPid(t, evidence)

	second := make(chan speakResult, 1)
	go func() { second <- speak(t.Context(), srv, `{"text":"ok"}`) }()
	select {
	case got := <-second:
		require.Failf(t, "the second request ran while the only slot was taken", "%+v", got)
	case <-time.After(200 * time.Millisecond):
	}

	cancel()

	select {
	case got := <-second:
		require.NoError(t, got.err)
		assert.Equal(t, "pcm:ok", got.body)
	case <-time.After(10 * time.Second):
		require.Fail(t, "the slot was never freed")
	}
	require.Error(t, (<-first).err)
	require.Error(t, syscall.Kill(pid, 0), "piper is still running")
	lines := completedLogs(t, logs, 2)
	aborted := make([]bool, len(lines))
	for i, line := range lines {
		aborted[i] = line.Aborted
	}
	assert.ElementsMatch(t, []bool{true, false}, aborted, "only the cancelled request is aborted")
}
