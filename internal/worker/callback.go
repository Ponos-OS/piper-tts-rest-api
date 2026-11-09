package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/kasir-barati/piper-tts-rest-api/internal/synthesis"
)

const (
	contentTypeMP3 = "audio/mpeg"

	// maxCallbackResponseBytes bounds what we read from a caller's server.
	maxCallbackResponseBytes = 1 << 20
)

type uploadURLRequest struct {
	JobID       string `json:"jobId"`
	Attempt     int    `json:"attempt"`
	ContentType string `json:"contentType"`
}

type uploadURLResponse struct {
	UploadURL string `json:"uploadUrl"`
}

// uploadURL asks the caller for a fresh presigned PUT URL, once per attempt.
func (w *Worker) uploadURL(ctx context.Context, job synthesis.Job, attempt int, authorization string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, w.cfg.GenUploadURLTimeout)
	defer cancel()

	body, err := json.Marshal(uploadURLRequest{JobID: job.ID, Attempt: attempt, ContentType: contentTypeMP3})
	if err != nil {
		return "", newJobError(synthesis.CodeUploadURLFailed, "Upload URL request failed", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, job.GenUploadURL, bytes.NewReader(body))
	if err != nil {
		return "", newJobError(synthesis.CodeUploadURLFailed, "Upload URL request failed", err)
	}
	req.Header.Set("Content-Type", "application/json")
	setCallbackHeaders(req, job.ID, attempt, authorization)

	resp, err := w.client.Do(req)
	if err != nil {
		return "", newJobError(synthesis.CodeUploadURLFailed, "Upload URL request failed", err)
	}
	defer closeResponse(resp)

	if !isSuccess(resp.StatusCode) {
		return "", httpStatusError(synthesis.CodeUploadURLFailed, resp)
	}

	var payload uploadURLResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxCallbackResponseBytes)).Decode(&payload); err != nil ||
		strings.TrimSpace(payload.UploadURL) == "" {
		return "", newJobError(synthesis.CodeUploadURLFailed, "Upload URL callback returned an invalid response", err)
	}

	return payload.UploadURL, nil
}

// setCallbackHeaders sets what every call to the caller's server carries.
func setCallbackHeaders(req *http.Request, jobID string, attempt int, authorization string) {
	req.Header.Set("Idempotency-Key", jobID+":"+strconv.Itoa(attempt))
	if authorization != "" {
		req.Header.Set("Authorization", authorization)
	}
}

// closeResponse reads what is left of the body before closing it, so the connection can be reused.
func closeResponse(resp *http.Response) {
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxCallbackResponseBytes)) //nolint:errcheck // best effort.
	_ = resp.Body.Close()                                                           //nolint:errcheck // best effort.
}

func isSuccess(status int) bool {
	return status >= http.StatusOK && status < http.StatusMultipleChoices
}
