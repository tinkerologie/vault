package storage

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"syscall"
)

// httpStatusError carries an HTTP status code so the classifier can decide
// retryability. Providers that speak HTTP wrap transport errors in this type
// when they can extract a status.
type httpStatusError struct {
	code int
	err  error
}

func (e *httpStatusError) Error() string {
	if e.err != nil {
		return e.err.Error()
	}
	return http.StatusText(e.code)
}

func (e *httpStatusError) Unwrap() error { return e.err }

// retryableError forces the classifier to treat the wrapped error as
// retryable, for protocol-specific transient conditions that aren't otherwise
// detectable.
type retryableError struct{ err error }

func (e *retryableError) Error() string { return e.err.Error() }
func (e *retryableError) Unwrap() error { return e.err }

// classify reports whether err represents a transient failure worth retrying.
// The default is false: when in doubt, do not retry — a wrong retry wastes time
// and can amplify load on a struggling destination.
func classify(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var forced *retryableError
	if errors.As(err, &forced) {
		return true
	}
	var hse *httpStatusError
	if errors.As(err, &hse) {
		switch hse.code {
		case http.StatusRequestTimeout, http.StatusTooManyRequests: // 408, 429
			return true
		}
		return hse.code >= 500 && hse.code <= 599
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return true
	}
	if errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	if errors.Is(err, syscall.ECONNRESET) ||
		errors.Is(err, syscall.ECONNREFUSED) ||
		errors.Is(err, syscall.EPIPE) ||
		errors.Is(err, syscall.ETIMEDOUT) {
		return true
	}
	return false
}
