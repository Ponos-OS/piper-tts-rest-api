package synthesis

import "time"

// Status is the state reported to the client's status callback.
type Status string

// Statuses sent to the status callback.
const (
	StatusQueued     Status = "queued"
	StatusGenerating Status = "generating"
	StatusUploading  Status = "uploading"
	StatusCompleted  Status = "completed"
	StatusFailed     Status = "failed"
)

// ErrorCode says why a job failed.
type ErrorCode string

// Codes sent in the "failed" status.
const (
	CodeInvalidJobMessage ErrorCode = "invalid-job-message"
	CodeGenerationFailed  ErrorCode = "generation-failed"
	CodeUploadURLFailed   ErrorCode = "upload-url-failed"
	CodeUploadFailed      ErrorCode = "upload-failed"
)

// maxProgressPercent: 100 is reported by the "completed" status instead.
const maxProgressPercent = 99

// JobError is the failure reported in the "failed" status.
type JobError struct {
	Code    ErrorCode `json:"code"`
	Message string    `json:"message"`
}

// StatusPayload is the body sent to the status callback.
// Build it with Queued, Generating, Uploading, Completed or Failed: each sets only the fields its status has.
type StatusPayload struct {
	JobID         string    `json:"jobId"`
	Status        Status    `json:"status"`
	Attempt       int       `json:"attempt"`
	Percent       *int      `json:"percent,omitempty"`
	FileSizeBytes *int64    `json:"fileSizeBytes,omitempty"`
	FailedAt      time.Time `json:"failedAt,omitzero"`
	Error         *JobError `json:"error,omitempty"`
}

// Queued reports that a worker received the job.
func Queued(jobID string, attempt int) StatusPayload {
	return StatusPayload{JobID: jobID, Status: StatusQueued, Attempt: attempt}
}

// Generating reports progress (0-99) of the MP3 generation.
func Generating(jobID string, attempt, percent int) StatusPayload {
	return StatusPayload{JobID: jobID, Status: StatusGenerating, Attempt: attempt, Percent: clampPercent(percent)}
}

// Uploading reports progress (0-99) of the upload.
func Uploading(jobID string, attempt, percent int) StatusPayload {
	return StatusPayload{JobID: jobID, Status: StatusUploading, Attempt: attempt, Percent: clampPercent(percent)}
}

// Completed reports that the upload succeeded.
func Completed(jobID string, attempt int, fileSizeBytes int64) StatusPayload {
	return StatusPayload{JobID: jobID, Status: StatusCompleted, Attempt: attempt, FileSizeBytes: &fileSizeBytes}
}

// Failed reports a terminal failure: no more retries.
func Failed(jobID string, attempt int, failedAt time.Time, jobErr JobError) StatusPayload {
	return StatusPayload{JobID: jobID, Status: StatusFailed, Attempt: attempt, FailedAt: failedAt.UTC(), Error: &jobErr}
}

func clampPercent(percent int) *int {
	percent = min(max(percent, 0), maxProgressPercent)

	return &percent
}
