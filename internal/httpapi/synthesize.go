package httpapi

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/kasir-barati/piper-tts-rest-api/internal/config"
	"github.com/kasir-barati/piper-tts-rest-api/internal/synthesis"
)

// publishTimeout bounds waiting for the broker to confirm a job.
const publishTimeout = 10 * time.Second

// JobPublisher enqueues a synthesize job and returns once the broker confirmed it.
type JobPublisher interface {
	PublishJob(ctx context.Context, job synthesis.Job, authorization string) error
}

type synthesizer struct {
	jobs     JobPublisher
	maxBody  int
	callback config.Callback
	log      *slog.Logger
}

// handleSynthesize validates the request and queues a job; the 202 means "queued", not "done".
func (s *synthesizer) handleSynthesize(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	raw, err := readBody(w, r, s.maxBody)
	if err != nil {
		writeBodyError(w, r, s.log, err)

		return
	}
	req, err := decodeJSON[synthesis.Request](raw)
	if err != nil {
		writeBodyError(w, r, s.log, err)

		return
	}

	req, err = req.Normalize(s.callback)
	if err != nil {
		s.writeNormalizeError(w, r, err)

		return
	}

	job := synthesis.Job{ID: uuid.NewString(), Request: req}
	ctx, cancel := context.WithTimeout(ctx, publishTimeout)
	defer cancel()

	if err := s.jobs.PublishJob(ctx, job, r.Header.Get("Authorization")); err != nil {
		s.log.ErrorContext(ctx, "Publishing the job failed", slog.String(keyJobID, job.ID), slog.Any(keyError, err))
		writeJSON(w, http.StatusServiceUnavailable, errorBody{Error: "Unable to accept synthesize job"})

		return
	}

	writeJSON(w, http.StatusAccepted, map[string]string{"jobId": job.ID})
}

// writeNormalizeError answers 400 for any Normalize failure, so an unexpected error can never lead to a published job.
func (s *synthesizer) writeNormalizeError(w http.ResponseWriter, r *http.Request, err error) {
	var invalid *synthesis.ValidationError
	if !errors.As(err, &invalid) {
		s.log.ErrorContext(r.Context(), "Normalizing the request failed", slog.Any(keyError, err))
		writeJSON(w, http.StatusBadRequest, errorBody{Error: "Invalid request", Code: codeInvalidRequest})

		return
	}

	fields := make([]fieldError, len(invalid.Violations))
	for i, v := range invalid.Violations {
		fields[i] = fieldError{Field: v.Field, Rule: v.Rule}
	}
	writeJSON(w, http.StatusBadRequest, errorBody{Error: "Invalid callback URL", Code: codeInvalidCallbackURL, Fields: fields})
}
