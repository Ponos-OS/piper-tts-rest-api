package worker

import (
	"errors"
	"fmt"
	"net/url"

	"github.com/kasir-barati/piper-tts-rest-api/internal/synthesis"
)

var errStatusRejected = errors.New("status callback answered with a failure")

// jobError is a failed job with the code reported to the client.
type jobError struct {
	code    synthesis.ErrorCode
	message string
	cause   error
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

func httpStatusError(code synthesis.ErrorCode, status int) *jobError {
	return &jobError{code: code, message: fmt.Sprintf("HTTP %d", status)}
}

// withoutURL drops the request URL that net/http puts in its errors: presigned URLs carry signatures that must not reach logs.
func withoutURL(err error) error {
	if urlErr, ok := errors.AsType[*url.Error](err); ok {
		return urlErr.Err
	}

	return err
}
