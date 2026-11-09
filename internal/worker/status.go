package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sync"

	"github.com/kasir-barati/piper-tts-rest-api/internal/amqp"
	"github.com/kasir-barati/piper-tts-rest-api/internal/synthesis"
)

const keyStatus = "status"

// reporter sends one job's status callbacks. Delivery is best effort: a failure is logged and never changes the job.
type reporter struct {
	w             *Worker
	job           synthesis.Job
	attempt       int
	authorization string

	mu      sync.Mutex
	pending chan struct{} // closed when the progress update in flight has been delivered; nil when none is.
}

func (w *Worker) newReporter(job synthesis.Job, m amqp.Message) *reporter {
	return &reporter{w: w, job: job, attempt: m.Attempt, authorization: m.Authorization}
}

// send delivers p and waits for it, after any progress update that is still in flight, so the order is kept.
func (r *reporter) send(ctx context.Context, p synthesis.StatusPayload) {
	r.wait()
	r.post(ctx, p)
}

// progress delivers p in the background so a slow client cannot slow the job down.
// It is dropped while a previous update is still in flight: only the latest progress matters.
func (r *reporter) progress(ctx context.Context, p synthesis.StatusPayload) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.pending != nil {
		return
	}
	done := make(chan struct{})
	r.pending = done

	go func() {
		r.post(ctx, p)

		r.mu.Lock()
		r.pending = nil
		r.mu.Unlock()
		close(done)
	}()
}

// wait returns once no progress update is in flight.
func (r *reporter) wait() {
	r.mu.Lock()
	done := r.pending
	r.mu.Unlock()

	if done != nil {
		<-done
	}
}

func (r *reporter) post(ctx context.Context, p synthesis.StatusPayload) {
	err := r.w.postStatus(ctx, r.job, r.attempt, r.authorization, p)
	if err == nil || ctx.Err() != nil {
		return
	}

	r.w.log.WarnContext(ctx, "Status callback failed; it is best effort and does not change the job",
		slog.String(keyJobID, r.job.ID), slog.Int(keyAttempt, r.attempt), slog.String(keyStatus, string(p.Status)),
		slog.String(keyURL, synthesis.RedactUploadURL(r.job.StatusCallbackURL)), slog.Any(keyError, err))
}

func (w *Worker) postStatus(ctx context.Context, job synthesis.Job, attempt int, authorization string, p synthesis.StatusPayload) error {
	ctx, cancel := context.WithTimeout(ctx, w.cfg.StatusCallbackTimeout)
	defer cancel()

	body, err := json.Marshal(p)
	if err != nil {
		return fmt.Errorf("encode status: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, job.StatusCallbackURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	setCallbackHeaders(req, job.ID, attempt, authorization)

	resp, err := w.client.Do(req)
	if err != nil {
		return fmt.Errorf("send: %w", withoutURL(err))
	}
	defer closeResponse(resp)

	if !isSuccess(resp.StatusCode) {
		return fmt.Errorf("%w: HTTP %d", errStatusRejected, resp.StatusCode)
	}

	return nil
}
