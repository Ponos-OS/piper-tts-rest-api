package worker

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/kasir-barati/piper-tts-rest-api/internal/synthesis"
)

var errStatusRejected = errors.New("status callback answered with a failure")

// jobError is a failed job with the code reported to the client.
type jobError struct {
	code    synthesis.ErrorCode
	message string
	cause   error
	// status is the HTTP status that caused the failure, 0 when there was no HTTP response.
	status     int
	retryAfter *time.Duration
}

func (e *jobError) Error() string {
	if e.cause == nil {
		return fmt.Sprintf("%s: %s", e.code, e.message)
	}

	return fmt.Sprintf("%s: %s: %v", e.code, e.message, e.cause)
}

func (e *jobError) Unwrap() error {
	return e.cause
}

func newJobError(code synthesis.ErrorCode, message string, cause error) *jobError {
	return &jobError{code: code, message: message, cause: withoutURL(cause)}
}

func httpStatusError(code synthesis.ErrorCode, resp *http.Response) *jobError {
	return &jobError{
		code:       code,
		message:    fmt.Sprintf("HTTP %d", resp.StatusCode),
		status:     resp.StatusCode,
		retryAfter: parseRetryAfter(resp.Header.Get("Retry-After"), time.Now()),
	}
}

// retryable says whether trying again can help: anything but an HTTP answer is retried, and of the HTTP answers only a timeout, a rate limit and server errors are. Other 4xx mean the request itself is wrong.
func (e *jobError) retryable() bool {
	return e.status == 0 ||
		e.status == http.StatusRequestTimeout ||
		e.status == http.StatusTooManyRequests ||
		e.status >= http.StatusInternalServerError
}

// retryDelay is how long the retry waits: a rate-limited server decides with Retry-After, otherwise it is the configured delay times the number of the attempt that failed.
func (e *jobError) retryDelay(configured time.Duration, attempt int) time.Duration {
	if e.status == http.StatusTooManyRequests && e.retryAfter != nil {
		return *e.retryAfter
	}

	return configured * time.Duration(attempt)
}

// maxRetryAfter caps what a callback server can ask for, so it cannot park a job in the work queue for years.
const maxRetryAfter = time.Hour

// parseRetryAfter reads the header as seconds or as an HTTP date, capped at maxRetryAfter; anything else gives nil.
func parseRetryAfter(value string, now time.Time) *time.Duration {
	if seconds, err := strconv.ParseUint(value, 10, 64); err == nil {
		d := time.Duration(min(seconds, uint64(maxRetryAfter/time.Second))) * time.Second

		return &d
	}
	if date, err := http.ParseTime(value); err == nil {
		d := min(max(0, date.Sub(now)), maxRetryAfter)

		return &d
	}

	return nil
}

// withoutURL drops the request URL that net/http puts in its errors: presigned URLs carry signatures that must not reach logs.
func withoutURL(err error) error {
	if urlErr, ok := errors.AsType[*url.Error](err); ok {
		return urlErr.Err
	}

	return err
}
