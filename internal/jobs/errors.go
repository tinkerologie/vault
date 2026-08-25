package jobs

import (
	"errors"
	"fmt"
)

// ValidationError reports caller-supplied input that Job Intake refused. It is
// the only error class an adapter should translate into a client-fault
// response (HTTP 400, or an MCP tool error); every other error is a server
// fault and must not be blamed on the caller.
//
// Field names the offending Job field where one applies, and is empty for
// whole-request problems such as a source/destination overlap.
type ValidationError struct {
	Field   string
	Message string
	// Err is the underlying cause when the refusal came from another
	// validator (the cron parser, say). Message stays the client-facing
	// wording; Err lets a caller reach the original with errors.Is/As.
	Err error
}

func (e *ValidationError) Error() string { return e.Message }

func (e *ValidationError) Unwrap() error { return e.Err }

// invalid builds a ValidationError. Message wording is preserved verbatim from
// the REST handlers this module absorbed, so existing clients and tests see
// the same strings.
func invalid(field, format string, args ...any) error {
	return &ValidationError{Field: field, Message: fmt.Sprintf(format, args...)}
}

// invalidCause is invalid() with the underlying error preserved for
// errors.Is/As, without changing the client-facing message.
func invalidCause(field string, cause error, format string, args ...any) error {
	return &ValidationError{Field: field, Message: fmt.Sprintf(format, args...), Err: cause}
}

// IsValidation reports whether err is a caller-fault validation failure.
func IsValidation(err error) (*ValidationError, bool) {
	var ve *ValidationError
	if errors.As(err, &ve) {
		return ve, true
	}
	return nil, false
}

// ErrJobNotFound is returned by Update and Delete when the id does not exist.
// Adapters map it to 404 rather than 400: the caller's input was well-formed,
// the target simply is not there.
var ErrJobNotFound = errors.New("jobs: job not found")

// ErrRestoreInProgress refuses a Delete while a restore for that Job is
// running. Interrupting a restore mid-write leaves half-restored data on disk,
// and deleting the Job would cascade-delete the restore's own records. This is
// a safety invariant of Job Intake, not of any one adapter — it must hold no
// matter which caller asks.
var ErrRestoreInProgress = errors.New("a restore for this job is in progress — wait for it to finish before deleting the job")
