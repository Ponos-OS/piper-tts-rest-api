// Package worker turns queued jobs into uploaded MP3 files.
package worker

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/kasir-barati/piper-tts-rest-api/internal/amqp"
	"github.com/kasir-barati/piper-tts-rest-api/internal/config"
	"github.com/kasir-barati/piper-tts-rest-api/internal/logging"
	"github.com/kasir-barati/piper-tts-rest-api/internal/synthesis"
)

const (
	keyJobID   = "jobId"
	keyAttempt = "attempt"
	keyError   = "error"
	keyCode    = "code"
	keyTextLen = "textCharacterCount"
	keySize    = "fileSizeBytes"
	keyURL     = "uploadUrl"
	keyTotal   = "totalDuration"
)

// Synthesizer turns text into an MP3 stream; the caller must close it.
type Synthesizer interface {
	Synthesize(ctx context.Context, text string) (io.ReadCloser, error)
}

// Worker processes jobs: it generates the MP3, asks the caller for an upload URL and uploads the file.
type Worker struct {
	cfg    config.Synthesize
	synth  Synthesizer
	client *http.Client
	log    *slog.Logger
}

// New returns a Worker. It connects to non-public addresses only for hosts listed in cfg.Callback.AllowedHosts.
func New(cfg config.Synthesize, synth Synthesizer, logger *slog.Logger) *Worker {
	return &Worker{
		cfg:    cfg,
		synth:  synth,
		client: newClient(cfg.Callback.AllowedHosts),
		log:    logging.Named(logger, "Worker"),
	}
}

// Process runs one job. A nil result means the file is uploaded.
func (w *Worker) Process(ctx context.Context, m amqp.Message) error {
	job, err := parseJob(m.Body)
	if err != nil {
		w.logFailure(ctx, "", m.Attempt, err)

		return err
	}

	started := time.Now()
	w.log.InfoContext(ctx, "Processing job", slog.String(keyJobID, job.ID), slog.Int(keyAttempt, m.Attempt), slog.Int(keyTextLen, len(job.Text)))

	status := w.newReporter(job, m)
	defer status.wait()
	status.send(ctx, synthesis.Queued(job.ID, m.Attempt))

	size, err := w.run(ctx, job, m, status)
	if err != nil {
		w.logFailure(ctx, job.ID, m.Attempt, err)

		return err
	}
	status.send(ctx, synthesis.Completed(job.ID, m.Attempt, size))

	w.log.InfoContext(ctx, "Job uploaded", slog.String(keyJobID, job.ID), slog.Int(keyAttempt, m.Attempt),
		slog.Int64(keySize, size), slog.Duration(keyTotal, time.Since(started)))

	return nil
}

func (w *Worker) run(ctx context.Context, job synthesis.Job, m amqp.Message, status *reporter) (int64, error) {
	dir, err := os.MkdirTemp("", "piper-tts-")
	if err != nil {
		return 0, newJobError(synthesis.CodeGenerationFailed, "Audio generation failed", err)
	}
	defer os.RemoveAll(dir) //nolint:errcheck // a leftover temp directory cannot change the outcome.

	generating := newThrottle(progressInterval, func(percent int) {
		status.progress(ctx, synthesis.Generating(job.ID, m.Attempt, percent))
	})
	a, err := w.generate(ctx, dir, job.Text, generating.report)
	if err != nil {
		return 0, err
	}
	uploadURL, err := w.uploadURL(ctx, job, m.Attempt, m.Authorization)
	if err != nil {
		return 0, err
	}

	status.send(ctx, synthesis.Uploading(job.ID, m.Attempt, 0))
	uploading := newThrottle(progressInterval, func(percent int) {
		status.progress(ctx, synthesis.Uploading(job.ID, m.Attempt, percent))
	})
	if err := w.upload(ctx, uploadURL, a, uploading.report); err != nil {
		w.log.ErrorContext(ctx, "Upload failed", slog.String(keyJobID, job.ID), slog.String(keyURL, synthesis.RedactUploadURL(uploadURL)))

		return 0, err
	}

	return a.size, nil
}

func (w *Worker) logFailure(ctx context.Context, jobID string, attempt int, err error) {
	if ctx.Err() != nil {
		return // shutting down: the job goes back to the queue, it did not fail.
	}

	attrs := []any{slog.String(keyJobID, jobID), slog.Int(keyAttempt, attempt), slog.Any(keyError, err)}
	if je, ok := errors.AsType[*jobError](err); ok {
		attrs = append(attrs, slog.String(keyCode, string(je.code)))
	}
	w.log.ErrorContext(ctx, "Job failed", attrs...)
}

func parseJob(body []byte) (synthesis.Job, error) {
	var job synthesis.Job
	if err := json.Unmarshal(body, &job); err != nil {
		return synthesis.Job{}, newJobError(synthesis.CodeInvalidJobMessage, "Message is not valid JSON", err)
	}
	if job.ID == "" || job.Text == "" || job.GenUploadURL == "" || job.StatusCallbackURL == "" {
		return synthesis.Job{}, newJobError(synthesis.CodeInvalidJobMessage, "Message has an invalid job shape", nil)
	}

	return job, nil
}
