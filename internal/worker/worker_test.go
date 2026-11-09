package worker_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	oteltrace "go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"

	"github.com/kasir-barati/piper-tts-rest-api/internal/amqp"
	"github.com/kasir-barati/piper-tts-rest-api/internal/config"
	"github.com/kasir-barati/piper-tts-rest-api/internal/logging"
	"github.com/kasir-barati/piper-tts-rest-api/internal/synthesis"
	"github.com/kasir-barati/piper-tts-rest-api/internal/worker"
)

var errPiper = errors.New("piper crashed")

type fakeSynth struct {
	audio    []byte
	startErr error
	readErr  error
	texts    []string
}

func (f *fakeSynth) Synthesize(_ context.Context, text string) (io.ReadCloser, error) {
	f.texts = append(f.texts, text)
	if f.startErr != nil {
		return nil, f.startErr
	}
	var r io.Reader = bytes.NewReader(f.audio)
	if f.readErr != nil {
		r = io.MultiReader(r, &failingReader{err: f.readErr})
	}

	return io.NopCloser(r), nil
}

type failingReader struct{ err error }

func (r *failingReader) Read([]byte) (int, error) { return 0, r.err }

type recorded struct {
	header http.Header
	body   []byte
}

// caller plays the API client: the genUploadUrl endpoint and the object storage the URL points at.
type caller struct {
	t               *testing.T
	server          *httptest.Server
	uploadURLStatus int
	uploadURLBody   string
	retryAfter      string
	uploadStatus    int
	onUpload        http.HandlerFunc
	onStatus        http.HandlerFunc

	mu          sync.Mutex
	urlRequests []recorded
	uploads     []recorded
	statuses    []recorded
}

func newCaller(t *testing.T) *caller {
	t.Helper()

	c := &caller{t: t, uploadURLStatus: http.StatusOK, uploadStatus: http.StatusOK}
	mux := http.NewServeMux()
	mux.HandleFunc("/upload-url", c.handleUploadURL)
	mux.HandleFunc("/file.mp3", c.handleUpload)
	mux.HandleFunc("/status", c.handleStatus)
	c.server = httptest.NewServer(mux)
	t.Cleanup(c.server.Close)

	return c
}

func (c *caller) handleUploadURL(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	assert.NoError(c.t, err)
	c.mu.Lock()
	c.urlRequests = append(c.urlRequests, recorded{header: r.Header.Clone(), body: body})
	c.mu.Unlock()

	if c.uploadURLStatus != http.StatusOK {
		if c.retryAfter != "" {
			w.Header().Set("Retry-After", c.retryAfter)
		}
		w.WriteHeader(c.uploadURLStatus)

		return
	}
	reply := c.uploadURLBody
	if reply == "" {
		reply = `{"uploadUrl":"` + c.server.URL + `/file.mp3?X-Amz-Signature=secret"}`
	}
	_, err = io.WriteString(w, reply)
	assert.NoError(c.t, err)
}

func (c *caller) handleUpload(w http.ResponseWriter, r *http.Request) {
	if c.onUpload != nil {
		c.onUpload(w, r)

		return
	}
	body, err := io.ReadAll(r.Body)
	assert.NoError(c.t, err)
	c.mu.Lock()
	c.uploads = append(c.uploads, recorded{header: r.Header.Clone(), body: body})
	c.mu.Unlock()
	w.WriteHeader(c.uploadStatus)
}

func (c *caller) handleStatus(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	assert.NoError(c.t, err)
	c.mu.Lock()
	c.statuses = append(c.statuses, recorded{header: r.Header.Clone(), body: body})
	c.mu.Unlock()

	if c.onStatus != nil {
		c.onStatus(w, r)
	}
}

// statusLog lists the statuses received so far as "status" or "status:percent" or "completed:bytes".
func (c *caller) statusLog(t *testing.T) []string {
	t.Helper()

	c.mu.Lock()
	defer c.mu.Unlock()

	log := make([]string, 0, len(c.statuses))
	for _, r := range c.statuses {
		var p struct {
			Status        string `json:"status"`
			Percent       *int   `json:"percent"`
			FileSizeBytes *int64 `json:"fileSizeBytes"`
			Error         *struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		require.NoError(t, json.Unmarshal(r.body, &p))
		entry := p.Status
		if p.Percent != nil {
			entry += ":" + strconv.Itoa(*p.Percent)
		}
		if p.FileSizeBytes != nil {
			entry += ":" + strconv.FormatInt(*p.FileSizeBytes, 10)
		}
		if p.Error != nil {
			entry += ":" + p.Error.Code
		}
		log = append(log, entry)
	}

	return log
}

func (c *caller) uploadCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()

	return len(c.uploads)
}

func testConfig() config.Synthesize {
	return config.Synthesize{
		GenUploadURLTimeout:   5 * time.Second,
		UploadIdleTimeout:     5 * time.Second,
		UploadTimeout:         10 * time.Second,
		StatusCallbackTimeout: 5 * time.Second,
		MaxAttempts:           3,
		RetryDelay:            retryDelay,
		WordsPerMinute:        150,
		Callback:              config.Callback{AllowedHosts: []string{"127.0.0.1"}},
	}
}

// publicOnlyConfig has no allow-list, so every non-public address is refused.
func publicOnlyConfig() config.Synthesize {
	cfg := testConfig()
	cfg.Callback = config.Callback{}

	return cfg
}

const (
	retryDelay = 30 * time.Second
	// jobMessage is the second attempt, so the delay without Retry-After is twice the configured one.
	delayAfterAttempt2 = 2 * retryDelay
)

// logBuffer collects log lines; the worker also logs from background goroutines.
type logBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *logBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	return l.b.Write(p)
}

func (l *logBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()

	return l.b.String()
}

func newLoggedWorker(cfg config.Synthesize, synth worker.Synthesizer) (*worker.Worker, *logBuffer) {
	logs := &logBuffer{}
	logger := logging.New(config.Log{Mode: config.LogModeJSON, Level: config.LogLevelInfo}, "svc", logs)

	return worker.New(cfg, synth, logger), logs
}

func last(entries []string) string {
	return entries[len(entries)-1]
}

func newWorker(cfg config.Synthesize, synth worker.Synthesizer) *worker.Worker {
	logger := logging.New(config.Log{Mode: config.LogModeJSON, Level: config.LogLevelError}, "svc", io.Discard)

	return worker.New(cfg, synth, logger)
}

func jobMessage(t *testing.T, genUploadURL string) amqp.Message {
	t.Helper()

	body, err := json.Marshal(synthesis.Job{
		ID:      "job-1",
		Request: synthesis.Request{Text: "Hello", GenUploadURL: genUploadURL, StatusCallbackURL: strings.Replace(genUploadURL, "/upload-url", "/status", 1)},
	})
	require.NoError(t, err)

	return amqp.Message{Body: body, Attempt: 2, Authorization: "Bearer token"}
}

func TestProcess_GeneratesAndUploadsTheMP3(t *testing.T) {
	api := newCaller(t)
	synth := &fakeSynth{audio: []byte("mp3-bytes")}
	w := newWorker(testConfig(), synth)

	got := w.Process(t.Context(), jobMessage(t, api.server.URL+"/upload-url"))

	assert.Equal(t, amqp.Ack(), got)
	assert.Equal(t, []string{"Hello"}, synth.texts)
	require.Len(t, api.uploads, 1)
	assert.Equal(t, []byte("mp3-bytes"), api.uploads[0].body)
	assert.Equal(t, "audio/mpeg", api.uploads[0].header.Get("Content-Type"))
	assert.Equal(t, strconv.Itoa(len("mp3-bytes")), api.uploads[0].header.Get("Content-Length"))
}

// slowSynth produces its chunks with a pause before each but the first.
type slowSynth struct {
	chunks [][]byte
	pause  time.Duration
}

func (f *slowSynth) Synthesize(context.Context, string) (io.ReadCloser, error) {
	pr, pw := io.Pipe()
	go func() {
		for i, chunk := range f.chunks {
			if i > 0 {
				time.Sleep(f.pause)
			}
			if _, err := pw.Write(chunk); err != nil {
				return
			}
		}
		pw.Close() //nolint:errcheck,gosec // PipeWriter.Close always returns nil.
	}()

	return pr, nil
}

func TestProcess_ReportsEachStageToTheStatusCallback(t *testing.T) {
	api := newCaller(t)
	w := newWorker(testConfig(), &fakeSynth{audio: []byte("mp3-bytes")})

	got := w.Process(t.Context(), jobMessage(t, api.server.URL+"/upload-url"))

	assert.Equal(t, amqp.Ack(), got)
	assert.Equal(t, []string{"queued", "generating:0", "uploading:0", "uploading:99", "completed:9"}, api.statusLog(t))
}

func TestProcess_StatusCallbacksCarryTheIdempotencyKeyAndAuthorization(t *testing.T) {
	api := newCaller(t)
	w := newWorker(testConfig(), &fakeSynth{audio: []byte("x")})

	got := w.Process(t.Context(), jobMessage(t, api.server.URL+"/upload-url"))

	assert.Equal(t, amqp.Ack(), got)
	require.NotEmpty(t, api.statuses)
	for _, r := range api.statuses {
		assert.Equal(t, "job-1:2", r.header.Get("Idempotency-Key"))
		assert.Equal(t, "Bearer token", r.header.Get("Authorization"))
		assert.Equal(t, "application/json", r.header.Get("Content-Type"))
	}
}

func TestProcess_GenerationProgressFollowsTheEstimatedSize(t *testing.T) {
	api := newCaller(t)
	// "Hello" is one word: about 6400 bytes at 150 words per minute. Both chunks together reach the estimate.
	synth := &slowSynth{chunks: [][]byte{make([]byte, 3200), make([]byte, 3200)}, pause: 700 * time.Millisecond}
	w := newWorker(testConfig(), synth)

	got := w.Process(t.Context(), jobMessage(t, api.server.URL+"/upload-url"))

	assert.Equal(t, amqp.Ack(), got)
	assert.Contains(t, api.statusLog(t), "generating:99")
}

func TestProcess_StatusCallbackFailuresDoNotChangeTheJob(t *testing.T) {
	tests := []struct {
		name     string
		onStatus http.HandlerFunc
	}{
		{"error response", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusInternalServerError) }},
		{"never answers", func(_ http.ResponseWriter, r *http.Request) { <-r.Context().Done() }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			api := newCaller(t)
			api.onStatus = tt.onStatus
			cfg := testConfig()
			cfg.StatusCallbackTimeout = 200 * time.Millisecond
			w := newWorker(cfg, &fakeSynth{audio: []byte("mp3-bytes")})

			got := w.Process(t.Context(), jobMessage(t, api.server.URL+"/upload-url"))

			assert.Equal(t, amqp.Ack(), got)
			assert.Equal(t, 1, api.uploadCount())
		})
	}
}

func TestProcess_AsksForTheUploadURLOncePerAttempt(t *testing.T) {
	api := newCaller(t)
	w := newWorker(testConfig(), &fakeSynth{audio: []byte("x")})

	got := w.Process(t.Context(), jobMessage(t, api.server.URL+"/upload-url"))

	assert.Equal(t, amqp.Ack(), got)
	require.Len(t, api.urlRequests, 1)
	req := api.urlRequests[0]
	assert.JSONEq(t, `{"jobId":"job-1","attempt":2,"contentType":"audio/mpeg"}`, string(req.body))
	assert.Equal(t, "job-1:2", req.header.Get("Idempotency-Key"))
	assert.Equal(t, "Bearer token", req.header.Get("Authorization"))
	assert.Equal(t, "application/json", req.header.Get("Content-Type"))
}

func TestProcess_OmitsAuthorizationWhenTheCallerSentNone(t *testing.T) {
	api := newCaller(t)
	w := newWorker(testConfig(), &fakeSynth{audio: []byte("x")})
	msg := jobMessage(t, api.server.URL+"/upload-url")
	msg.Authorization = ""

	got := w.Process(t.Context(), msg)

	assert.Equal(t, amqp.Ack(), got)
	assert.Empty(t, api.urlRequests[0].header.Values("Authorization"))
}

func TestProcess_LeavesNoTemporaryFilesBehind(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	api := newCaller(t)
	w := newWorker(testConfig(), &fakeSynth{audio: []byte("x")})

	got := w.Process(t.Context(), jobMessage(t, api.server.URL+"/upload-url"))

	assert.Equal(t, amqp.Ack(), got)
	entries, err := os.ReadDir(tmp)
	require.NoError(t, err)
	assert.Empty(t, entries)
}

func TestProcess_RejectsAnInvalidMessage(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{"not json", `not json`, "invalid-job-message: Message is not valid JSON"},
		{"missing text", `{"jobId":"j","genUploadUrl":"https://a.example/u","statusCallbackUrl":"https://a.example/s"}`, "invalid-job-message: Message has an invalid job shape"},
		{"missing job id", `{"text":"t","genUploadUrl":"https://a.example/u","statusCallbackUrl":"https://a.example/s"}`, "invalid-job-message: Message has an invalid job shape"},
		{"missing upload url endpoint", `{"jobId":"j","text":"t","statusCallbackUrl":"https://a.example/s"}`, "invalid-job-message: Message has an invalid job shape"},
		{"missing status callback", `{"jobId":"j","text":"t","genUploadUrl":"https://a.example/u"}`, "invalid-job-message: Message has an invalid job shape"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			synth := &fakeSynth{}
			w, logs := newLoggedWorker(testConfig(), synth)

			got := w.Process(t.Context(), amqp.Message{Body: []byte(tt.body), Attempt: 1})

			assert.Equal(t, amqp.Reject(), got)
			assert.Contains(t, logs.String(), tt.want)
			assert.Empty(t, synth.texts)
		})
	}
}

func TestProcess_GenerationFailureStopsBeforeAskingForAnUploadURL(t *testing.T) {
	tests := []struct {
		name  string
		synth *fakeSynth
	}{
		{"cannot start", &fakeSynth{startErr: errPiper}},
		{"fails while producing audio", &fakeSynth{audio: []byte("partial"), readErr: errPiper}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			api := newCaller(t)
			w, logs := newLoggedWorker(testConfig(), tt.synth)

			got := w.Process(t.Context(), jobMessage(t, api.server.URL+"/upload-url"))

			assert.Equal(t, amqp.Retry(delayAfterAttempt2), got)
			assert.Contains(t, logs.String(), "generation-failed: Audio generation failed")
			assert.Contains(t, logs.String(), "piper crashed")
			assert.Empty(t, api.urlRequests)
		})
	}
}

func TestProcess_UploadURLFailures(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*caller)
		want  string
	}{
		{"non-2xx", func(c *caller) { c.uploadURLStatus = http.StatusServiceUnavailable }, "upload-url-failed: HTTP 503"},
		{"empty upload url", func(c *caller) { c.uploadURLBody = `{"uploadUrl":"  "}` }, "upload-url-failed: Upload URL callback returned an invalid response"},
		{"not json", func(c *caller) { c.uploadURLBody = `nope` }, "upload-url-failed: Upload URL callback returned an invalid response"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			api := newCaller(t)
			tt.setup(api)
			w, logs := newLoggedWorker(testConfig(), &fakeSynth{audio: []byte("x")})

			got := w.Process(t.Context(), jobMessage(t, api.server.URL+"/upload-url"))

			assert.Equal(t, amqp.Retry(delayAfterAttempt2), got)
			assert.Contains(t, logs.String(), tt.want)
			assert.Zero(t, api.uploadCount())
		})
	}
}

func TestProcess_UploadRejectedByStorage(t *testing.T) {
	api := newCaller(t)
	api.uploadStatus = http.StatusForbidden
	w, logs := newLoggedWorker(testConfig(), &fakeSynth{audio: []byte("x")})

	got := w.Process(t.Context(), jobMessage(t, api.server.URL+"/upload-url"))

	assert.Equal(t, amqp.Ack(), got, "a 403 will not get better: the client is told and the job ends")
	assert.Contains(t, logs.String(), "upload-failed: HTTP 403")
	assert.Equal(t, "failed:upload-failed", last(api.statusLog(t)))
}

func TestProcess_StorageServerErrorIsRetried(t *testing.T) {
	api := newCaller(t)
	api.uploadStatus = http.StatusServiceUnavailable
	w := newWorker(testConfig(), &fakeSynth{audio: []byte("x")})

	got := w.Process(t.Context(), jobMessage(t, api.server.URL+"/upload-url"))

	assert.Equal(t, amqp.Retry(delayAfterAttempt2), got)
	assert.NotContains(t, api.statusLog(t), "failed:upload-failed")
}

func TestProcess_DoesNotFollowRedirects(t *testing.T) {
	target := newCaller(t)
	api := newCaller(t)
	api.onUpload = func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.server.URL+"/file.mp3", http.StatusFound)
	}
	w, logs := newLoggedWorker(testConfig(), &fakeSynth{audio: []byte("x")})

	got := w.Process(t.Context(), jobMessage(t, api.server.URL+"/upload-url"))

	assert.Equal(t, amqp.Ack(), got)
	assert.Contains(t, logs.String(), "upload-failed: HTTP 302")
	assert.Zero(t, target.uploadCount())
}

func TestProcess_UploadErrorsDoNotLeakThePresignedURL(t *testing.T) {
	api := newCaller(t)
	api.onUpload = func(w http.ResponseWriter, _ *http.Request) {
		hijacker, ok := w.(http.Hijacker)
		assert.True(t, ok)
		conn, _, err := hijacker.Hijack()
		assert.NoError(t, err)
		assert.NoError(t, conn.Close())
	}
	w, logs := newLoggedWorker(testConfig(), &fakeSynth{audio: []byte("x")})

	got := w.Process(t.Context(), jobMessage(t, api.server.URL+"/upload-url"))

	assert.Equal(t, amqp.Retry(delayAfterAttempt2), got)
	assert.Contains(t, logs.String(), "upload-failed: MP3 upload failed")
	assert.NotContains(t, logs.String(), "secret", "the signature must not reach the logs")
}

func TestProcess_UploadThatStopsMakingProgressIsAborted(t *testing.T) {
	const audioSize = 64 << 20 // far more than the socket buffers hold, so the sender blocks.
	stalled := make(chan struct{})
	api := newCaller(t)
	api.onUpload = func(_ http.ResponseWriter, r *http.Request) {
		select {
		case <-stalled:
		case <-r.Context().Done():
		}
	}
	t.Cleanup(func() { close(stalled) })
	cfg := testConfig()
	cfg.UploadIdleTimeout = 300 * time.Millisecond
	w, logs := newLoggedWorker(cfg, &fakeSynth{audio: make([]byte, audioSize)})

	got := w.Process(t.Context(), jobMessage(t, api.server.URL+"/upload-url"))

	assert.Equal(t, amqp.Retry(delayAfterAttempt2), got)
	assert.Contains(t, logs.String(), "upload became idle")
}

func TestProcess_SlowButSteadyUploadIsNotAborted(t *testing.T) {
	const (
		audioSize = 48 << 20
		chunk     = 8 << 20
	)
	api := newCaller(t)
	api.onUpload = func(w http.ResponseWriter, r *http.Request) {
		for {
			_, err := io.CopyN(io.Discard, r.Body, chunk)
			if err != nil {
				break
			}
			time.Sleep(100 * time.Millisecond) // the whole upload outlasts the idle timeout, no single pause does.
		}
		w.WriteHeader(http.StatusOK)
	}
	cfg := testConfig()
	cfg.UploadIdleTimeout = 500 * time.Millisecond
	w := newWorker(cfg, &fakeSynth{audio: make([]byte, audioSize)})

	got := w.Process(t.Context(), jobMessage(t, api.server.URL+"/upload-url"))

	assert.Equal(t, amqp.Ack(), got)
}

func TestProcess_UploadIsAbortedAtTheAbsoluteDeadline(t *testing.T) {
	stalled := make(chan struct{})
	api := newCaller(t)
	api.onUpload = func(_ http.ResponseWriter, r *http.Request) {
		_, err := io.Copy(io.Discard, r.Body)
		assert.NoError(t, err)
		select {
		case <-stalled:
		case <-r.Context().Done():
		}
	}
	t.Cleanup(func() { close(stalled) })
	cfg := testConfig()
	cfg.UploadTimeout = 300 * time.Millisecond
	w, logs := newLoggedWorker(cfg, &fakeSynth{audio: []byte("x")})

	got := w.Process(t.Context(), jobMessage(t, api.server.URL+"/upload-url"))

	assert.Equal(t, amqp.Retry(delayAfterAttempt2), got)
	assert.Contains(t, logs.String(), "upload timed out")
}

func TestProcess_RefusesCallbacksToNonPublicAddresses(t *testing.T) {
	tests := []struct {
		name string
		url  string
	}{
		{"loopback", "http://127.0.0.1:1/upload-url"},
		{"loopback v6", "http://[::1]:1/upload-url"},
		{"loopback as v4-mapped v6", "http://[::ffff:127.0.0.1]:1/upload-url"},
		{"private 10/8", "http://10.0.0.1:1/upload-url"},
		{"private 192.168/16", "http://192.168.1.1:1/upload-url"},
		{"unique local v6", "http://[fd00::1]:1/upload-url"},
		{"cloud metadata", "http://169.254.169.254/upload-url"},
		{"unspecified", "http://0.0.0.0:1/upload-url"},
		{"multicast", "http://224.0.0.1:1/upload-url"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w, logs := newLoggedWorker(publicOnlyConfig(), &fakeSynth{audio: []byte("x")})

			w.Process(t.Context(), jobMessage(t, tt.url))

			assert.Contains(t, logs.String(), "upload-url-failed")
			assert.Contains(t, logs.String(), "non-public address")
		})
	}
}

func TestProcess_RefusesAPublicNameThatResolvesToALoopbackAddress(t *testing.T) {
	api := newCaller(t)
	w, logs := newLoggedWorker(publicOnlyConfig(), &fakeSynth{audio: []byte("x")})
	localhostURL := "http://localhost:" + portOf(t, api.server.URL) + "/upload-url"

	w.Process(t.Context(), jobMessage(t, localhostURL))

	assert.Contains(t, logs.String(), "non-public address")
	assert.Empty(t, api.urlRequests)
}

func TestProcess_ListedHostMayBeAPrivateAddress(t *testing.T) {
	api := newCaller(t)
	cfg := testConfig()
	cfg.Callback.AllowedHosts = []string{"127.0.0.1", "other.example.com"}
	w := newWorker(cfg, &fakeSynth{audio: []byte("x")})

	got := w.Process(t.Context(), jobMessage(t, api.server.URL+"/upload-url"))

	assert.Equal(t, amqp.Ack(), got)
}

func TestProcess_ListingOneNameDoesNotAllowAnotherNameForTheSameAddress(t *testing.T) {
	api := newCaller(t)
	cfg := testConfig()
	cfg.Callback.AllowedHosts = []string{"127.0.0.1"}
	w, logs := newLoggedWorker(cfg, &fakeSynth{audio: []byte("x")})
	localhostURL := "http://localhost:" + portOf(t, api.server.URL) + "/upload-url"

	w.Process(t.Context(), jobMessage(t, localhostURL))

	assert.Contains(t, logs.String(), "non-public address")
	assert.Empty(t, api.urlRequests)
}

func TestProcess_ShutdownMidJobDoesNotTellTheClientItFailed(t *testing.T) {
	api := newCaller(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	w := newWorker(testConfig(), &fakeSynth{audio: []byte("x")})

	w.Process(ctx, jobMessage(t, api.server.URL+"/upload-url"))

	assert.Empty(t, api.statusLog(t))
	assert.Zero(t, api.uploadCount())
}

func portOf(t *testing.T, rawURL string) string {
	t.Helper()

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, rawURL, http.NoBody)
	require.NoError(t, err)

	return req.URL.Port()
}

func TestProcess_RetriesWhatMayWorkNextTime(t *testing.T) {
	for _, status := range []int{http.StatusRequestTimeout, http.StatusTooManyRequests, http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			api := newCaller(t)
			api.uploadURLStatus = status
			w := newWorker(testConfig(), &fakeSynth{audio: []byte("x")})

			got := w.Process(t.Context(), jobMessage(t, api.server.URL+"/upload-url"))

			assert.Equal(t, amqp.Retry(delayAfterAttempt2), got)
			assert.NotContains(t, api.statusLog(t), "failed:upload-url-failed", "the client hears about a failure only when it is final")
		})
	}
}

func TestProcess_DoesNotRetryWhatWillNotChange(t *testing.T) {
	for _, status := range []int{http.StatusBadRequest, http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusGone} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			api := newCaller(t)
			api.uploadURLStatus = status
			w := newWorker(testConfig(), &fakeSynth{audio: []byte("x")})

			got := w.Process(t.Context(), jobMessage(t, api.server.URL+"/upload-url"))

			assert.Equal(t, amqp.Ack(), got)
			assert.Equal(t, "failed:upload-url-failed", last(api.statusLog(t)))
		})
	}
}

func TestProcess_ARateLimitedServerChoosesTheRetryDelay(t *testing.T) {
	tests := []struct {
		name       string
		status     int
		retryAfter string
		want       time.Duration
	}{
		{"seconds", http.StatusTooManyRequests, "7", 7 * time.Second},
		{"retry immediately", http.StatusTooManyRequests, "0", 0},
		{"no header", http.StatusTooManyRequests, "", delayAfterAttempt2},
		{"unreadable header", http.StatusTooManyRequests, "soon", delayAfterAttempt2},
		{"only a 429 is trusted with the delay", http.StatusServiceUnavailable, "7", delayAfterAttempt2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			api := newCaller(t)
			api.uploadURLStatus = tt.status
			api.retryAfter = tt.retryAfter
			w := newWorker(testConfig(), &fakeSynth{audio: []byte("x")})

			got := w.Process(t.Context(), jobMessage(t, api.server.URL+"/upload-url"))

			assert.Equal(t, amqp.Retry(tt.want), got)
		})
	}
}

func TestProcess_AJobThatUsedAllItsAttemptsIsDeadLetteredAndTheClientIsTold(t *testing.T) {
	api := newCaller(t)
	w := newWorker(testConfig(), &fakeSynth{startErr: errPiper})
	msg := jobMessage(t, api.server.URL+"/upload-url")
	msg.Attempt = 3

	got := w.Process(t.Context(), msg)

	assert.Equal(t, amqp.Reject(), got)
	assert.Equal(t, "failed:generation-failed", last(api.statusLog(t)))
}

func TestProcess_TheFailedStatusCarriesTheTimeAndTheError(t *testing.T) {
	api := newCaller(t)
	api.uploadURLStatus = http.StatusBadRequest
	w := newWorker(testConfig(), &fakeSynth{audio: []byte("x")})
	before := time.Now().Add(-time.Second)

	w.Process(t.Context(), jobMessage(t, api.server.URL+"/upload-url"))

	var failed struct {
		JobID    string    `json:"jobId"`
		Status   string    `json:"status"`
		Attempt  int       `json:"attempt"`
		FailedAt time.Time `json:"failedAt"`
		Error    struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	require.NoError(t, json.Unmarshal(api.statuses[len(api.statuses)-1].body, &failed))
	assert.Equal(t, "job-1", failed.JobID)
	assert.Equal(t, "failed", failed.Status)
	assert.Equal(t, 2, failed.Attempt)
	assert.Equal(t, "upload-url-failed", failed.Error.Code)
	assert.Equal(t, "HTTP 400", failed.Error.Message)
	assert.WithinRange(t, failed.FailedAt, before, time.Now().Add(time.Second))
	assert.Equal(t, time.UTC, failed.FailedAt.Location())
}

func TestProcess_PassesTheTraceOnToEveryCallAsClientSpans(t *testing.T) {
	spans := tracetest.NewSpanRecorder()
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spans)))
	otel.SetTextMapPropagator(propagation.TraceContext{})
	t.Cleanup(func() {
		otel.SetTracerProvider(noop.NewTracerProvider())
		otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator())
	})
	api := newCaller(t)
	w := newWorker(testConfig(), &fakeSynth{audio: []byte("x")})
	ctx, parent := otel.Tracer("test").Start(t.Context(), "parent")

	got := w.Process(ctx, jobMessage(t, api.server.URL+"/upload-url"))
	parent.End()

	require.Equal(t, amqp.Ack(), got)
	traceID := parent.SpanContext().TraceID().String()
	api.mu.Lock()
	defer api.mu.Unlock()
	sent := slices.Concat(api.urlRequests, api.statuses, api.uploads)
	require.NotEmpty(t, sent)
	for _, r := range sent {
		assert.Contains(t, r.header.Get("traceparent"), traceID)
	}
	clients := 0
	for _, span := range spans.Ended() {
		if span.SpanKind() != oteltrace.SpanKindClient {
			continue
		}
		clients++
		assert.Equal(t, traceID, span.SpanContext().TraceID().String())
		assert.NotContains(t, span.Name(), "secret")
	}
	assert.Len(t, sent, clients, "one client span per call")
}
