package worker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/kasir-barati/piper-tts-rest-api/internal/synthesis"
)

var (
	errUploadTimeout = errors.New("upload timed out")
	errUploadIdle    = errors.New("upload became idle")
)

// upload PUTs the MP3 to the presigned URL.
// UploadTimeout bounds the whole call and UploadIdleTimeout the time without any bytes being sent.
func (w *Worker) upload(ctx context.Context, uploadURL string, a audio) error {
	if err := w.put(ctx, uploadURL, a); err != nil {
		return newJobError(synthesis.CodeUploadFailed, "MP3 upload failed", err)
	}

	return nil
}

func (w *Worker) put(ctx context.Context, uploadURL string, a audio) error {
	file, err := os.Open(a.path)
	if err != nil {
		return fmt.Errorf("open audio: %w", err)
	}
	defer file.Close() //nolint:errcheck // read-only file.

	ctx, cancelDeadline := context.WithTimeoutCause(ctx, w.cfg.UploadTimeout, errUploadTimeout)
	defer cancelDeadline()
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)

	body := newIdleReader(file, w.cfg.UploadIdleTimeout, func() { cancel(errUploadIdle) })
	defer body.stop()

	req, err := http.NewRequestWithContext(ctx, http.MethodPut, uploadURL, body)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.ContentLength = a.size
	req.Header.Set("Content-Type", contentTypeMP3)
	req.Header.Set("Content-Length", strconv.FormatInt(a.size, 10))

	resp, err := w.client.Do(req)
	if err != nil {
		return fmt.Errorf("send: %w", errors.Join(withoutURL(err), context.Cause(ctx)))
	}
	defer closeResponse(resp)

	if !isSuccess(resp.StatusCode) {
		return httpStatusError(synthesis.CodeUploadFailed, resp.StatusCode)
	}

	return nil
}

// idleReader calls onIdle when no Read happens for the idle duration.
// It stops watching at EOF: from then on only the response is awaited, which the overall deadline bounds.
type idleReader struct {
	r     io.Reader
	idle  time.Duration
	timer *time.Timer
}

func newIdleReader(r io.Reader, idle time.Duration, onIdle func()) *idleReader {
	return &idleReader{r: r, idle: idle, timer: time.AfterFunc(idle, onIdle)}
}

func (i *idleReader) Read(b []byte) (int, error) {
	n, err := i.r.Read(b)
	if errors.Is(err, io.EOF) {
		i.timer.Stop()
	} else {
		i.timer.Reset(i.idle)
	}

	return n, err //nolint:wrapcheck // io.Reader contract: io.EOF must come back unwrapped.
}

func (i *idleReader) stop() {
	i.timer.Stop()
}
