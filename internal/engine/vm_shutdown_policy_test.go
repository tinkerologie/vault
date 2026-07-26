package engine

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

func TestEscalateToForceStop(t *testing.T) {
	timedOut := fmt.Errorf("waiting for domain vm1 to shut off: timed out with state running: %w", errShutdownTimeout)

	cases := []struct {
		name        string
		ctxErr      error
		shutdownErr error
		waitErr     error
		shutOff     bool
		wantForce   bool
		wantFatal   bool
	}{
		{
			// A cancel can land during a state poll or as the deadline
			// expires, so both can be true at once. Cancellation must win, or
			// cancelling a backup hard-stops a running guest.
			name:      "cancelled at the same moment the wait timed out",
			ctxErr:    context.Canceled,
			waitErr:   timedOut,
			shutOff:   false,
			wantFatal: true,
		},
		{
			name:        "cancelled while the shutdown request was failing",
			ctxErr:      context.Canceled,
			shutdownErr: errors.New("libvirt: operation not supported"),
			shutOff:     false,
			wantFatal:   true,
		},
		{
			// Issue #255: the request was accepted, the guest ignored it, and
			// the backup failed instead of escalating.
			name:      "guest ignores an accepted shutdown request",
			waitErr:   timedOut,
			shutOff:   false,
			wantForce: true,
		},
		{
			name:    "guest shuts down cleanly",
			shutOff: true,
		},
		{
			// libvirt refused the request outright, so there was no wait.
			name:        "shutdown request itself failed",
			shutdownErr: errors.New("libvirt: operation not supported"),
			shutOff:     false,
			wantForce:   true,
		},
		{
			// Must NOT become a hard power-off of a running VM.
			name:      "context cancelled while waiting",
			waitErr:   context.Canceled,
			shutOff:   false,
			wantFatal: true,
		},
		{
			name:      "libvirt failed while polling state",
			waitErr:   errors.New("libvirt: connection reset"),
			shutOff:   false,
			wantFatal: true,
		},
		{
			// Timed out but the domain reached shut-off on the final poll.
			name:    "timeout raced a clean shutdown",
			waitErr: timedOut,
			shutOff: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			force, fatal := escalateToForceStop(tc.ctxErr, tc.shutdownErr, tc.waitErr, tc.shutOff)
			if (fatal != nil) != tc.wantFatal {
				t.Fatalf("fatal = %v, want fatal=%v", fatal, tc.wantFatal)
			}
			if force != tc.wantForce {
				t.Fatalf("force = %v, want %v", force, tc.wantForce)
			}
		})
	}
}

// TestShutdownTimeoutIsDistinguishable guards the sentinel: if the wrap is ever
// dropped, a timeout would read as a fatal error again and #255 would return.
func TestShutdownTimeoutIsDistinguishable(t *testing.T) {
	wrapped := fmt.Errorf("waiting for domain vm1 to shut off: timed out with state running: %w", errShutdownTimeout)
	if !errors.Is(wrapped, errShutdownTimeout) {
		t.Fatal("timeout sentinel lost through wrapping")
	}
	if errors.Is(errors.New("some other failure"), errShutdownTimeout) {
		t.Fatal("unrelated errors match the timeout sentinel")
	}
}
