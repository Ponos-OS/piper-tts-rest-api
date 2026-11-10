package httpapi

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"
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

type synthesizeInput struct {
	Authorization string `header:"Authorization" doc:"Passed through to the callbacks. Optional."`
	Body          synthesis.Request
}

type synthesizeOutput struct {
	Body struct {
		JobID string `json:"jobId" doc:"Identifies the job in the callbacks." format:"uuid" example:"e52cbe80-17de-4204-a983-bb1c10c36341"`
	}
}

func registerSynthesize(api huma.API, s *synthesizer) {
	huma.Register(api, huma.Operation{
		OperationID:   "synthesize",
		Method:        http.MethodPost,
		Path:          "/v1/synthesize",
		Summary:       "Asynchronous text to speech",
		Description:   "Queues a job that generates the MP3 and uploads it to the presigned URL returned by genUploadUrl. Progress goes to statusCallbackUrl. A 202 means queued, not done.",
		Tags:          []string{"Speech"},
		DefaultStatus: http.StatusAccepted,
		MaxBodyBytes:  int64(s.maxBody),
		Errors:        []int{http.StatusRequestEntityTooLarge, http.StatusUnprocessableEntity, http.StatusServiceUnavailable},
	}, s.handleSynthesize)
}

// handleSynthesize validates the request and queues a job; the 202 means "queued", not "done".
func (s *synthesizer) handleSynthesize(ctx context.Context, in *synthesizeInput) (*synthesizeOutput, error) {
	req, err := in.Body.Normalize(s.callback)
	if err != nil {
		return nil, s.normalizeError(ctx, err)
	}

	job := synthesis.Job{ID: uuid.NewString(), Request: req}
	ctx, cancel := context.WithTimeout(ctx, publishTimeout)
	defer cancel()

	if err := s.jobs.PublishJob(ctx, job, in.Authorization); err != nil {
		s.log.ErrorContext(ctx, "Publishing the job failed", slog.String(keyJobID, job.ID), slog.Any(keyError, err))

		return nil, huma.Error503ServiceUnavailable("Unable to accept synthesize job")
	}

	out := &synthesizeOutput{}
	out.Body.JobID = job.ID

	return out, nil
}

// normalizeError answers 422 for any Normalize failure, so an unexpected error can never lead to a published job.
func (s *synthesizer) normalizeError(ctx context.Context, err error) error {
	var invalid *synthesis.ValidationError
	if !errors.As(err, &invalid) {
		s.log.ErrorContext(ctx, "Normalizing the request failed", slog.Any(keyError, err))

		return huma.Error422UnprocessableEntity("Invalid request")
	}

	details := make([]error, len(invalid.Violations))
	for i, v := range invalid.Violations {
		details[i] = &huma.ErrorDetail{Message: v.Rule, Location: "body." + v.Field}
	}

	return huma.Error422UnprocessableEntity("Invalid callback URL", details...)
}
