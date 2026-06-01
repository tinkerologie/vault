package storage

import (
	"context"
	"errors"
	"io"
	"net"
	"syscall"
	"testing"
)

func TestClassify(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"user cancel", context.Canceled, false},
		{"deadline", context.DeadlineExceeded, true},
		{"conn reset", syscall.ECONNRESET, true},
		{"conn refused", syscall.ECONNREFUSED, true},
		{"broken pipe", syscall.EPIPE, true},
		{"unexpected eof", io.ErrUnexpectedEOF, true},
		{"net timeout", timeoutErr{}, true},
		{"http 503", &httpStatusError{code: 503}, true},
		{"http 429", &httpStatusError{code: 429}, true},
		{"http 408", &httpStatusError{code: 408}, true},
		{"http 403", &httpStatusError{code: 403}, false},
		{"http 404", &httpStatusError{code: 404}, false},
		{"forced retryable", &retryableError{err: errors.New("x")}, true},
		{"plain error", errors.New("boom"), false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := classify(tc.err); got != tc.want {
				t.Errorf("classify(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

type timeoutErr struct{}

func (timeoutErr) Error() string   { return "i/o timeout" }
func (timeoutErr) Timeout() bool   { return true }
func (timeoutErr) Temporary() bool { return true }

var _ net.Error = timeoutErr{}
