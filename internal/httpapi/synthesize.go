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

// synthesizeDescription is Markdown shown in the API reference.
const synthesizeDescription = `Queues a job that generates the MP3 and uploads it to a presigned URL. A 202 means queued, not done.

**Callbacks.** Once per attempt the service calls ` + "`genUploadUrl`" + ` with ` + "`{\"jobId\", \"attempt\", \"contentType\": \"audio/mpeg\"}`" + `. It must reply 200 with ` + "`{\"uploadUrl\": \"...\"}`" + `, a short-lived presigned PUT URL. Progress goes to ` + "`statusCallbackUrl`" + ` with an ` + "`Idempotency-Key: <jobId>:<attempt>`" + ` header.

| Status | Extra fields | Meaning |
| --- | --- | --- |
| queued | | A worker received the job. |
| generating | percent (0-99) | Producing the MP3. |
| uploading | percent (0-99) | Uploading to object storage. |
| completed | fileSizeBytes | Upload succeeded. |
| failed | failedAt, error | Terminal failure, no more retries. |

Percentages are stage-local, approximate and monotonic. Status delivery is best effort: timeouts and non-2xx replies do not stop synthesis. Clients must tolerate duplicate, missing and out-of-order updates.

**Delivery and retries.** RabbitMQ delivery is at least once. A worker crash after the upload and before the acknowledgement can repeat the upload and the callbacks. A failure while generating, getting the upload URL or uploading is retried when it is not an HTTP answer (a network error, for example), or the answer is 408, 429 or 5xx. Other 4xx answers and redirects fail the job at once. A retry republishes the job with attempt + 1, up to ` + "`SYNTHESIZE_MAX_ATTEMPTS`" + `. The delay is the ` + "`Retry-After`" + ` of a 429 reply (at most one hour), otherwise ` + "`SYNTHESIZE_RETRY_DELAY_MS`" + ` times the number of the failed attempt. Delayed retries need RabbitMQ 4.4 or newer; older brokers redeliver at once. A job that fails for good gets the failed callback and its message goes to the dead-letter queue.

**Security.** Callback URLs must be HTTPS in production, and URL credentials and fragments are rejected. ` + "`SYNTHESIZE_CALLBACK_ALLOWED_HOSTS`" + ` restricts the hosts, and the worker refuses non-public addresses unless the host is listed. Presigned URLs are redacted in logs. The optional ` + "`Authorization`" + ` header is passed through to the callbacks.

**Operations.** The service declares durable exchanges and quorum queues (` + "`synthesize.work`" + `, ` + "`synthesize.dlq`" + `) with prefetch 1; give RabbitMQ persistent storage. See the scaling guide in the repository for KEDA and rollout guidance.`

func registerSynthesize(api huma.API, s *synthesizer) {
	huma.Register(api, huma.Operation{
		OperationID:   "synthesize",
		Method:        http.MethodPost,
		Path:          "/v1/synthesize",
		Summary:       "Asynchronous text to speech",
		Description:   synthesizeDescription,
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
