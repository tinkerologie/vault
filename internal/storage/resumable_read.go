package storage

import (
	"context"
	"io"
	"time"
)

// resumableReader is an io.ReadCloser over [0,size) of a single object that
// transparently recovers from transient mid-stream failures. It reads from an
// inner stream obtained via adapter.ReadRange(path, offset, size-offset); on a
// transient read error (classify) or an unexpectedly short stream, it closes
// the inner stream, backs off, and reopens ReadRange from the byte offset
// already delivered to the caller. EOF at `size` and non-transient errors
// propagate unchanged.
//
// It composes with the retry middleware: the chain already retries establishing
// each ReadRange call; resumableReader adds recovery from a drop that occurs
// while bytes are flowing, which the middleware cannot provide.
type resumableReader struct {
	ctx     context.Context
	adapter Adapter
	path    string
	size    int64
	policy  RetryPolicy
	sleep   func(time.Duration)

	offset   int64         // bytes successfully delivered to the caller
	inner    io.ReadCloser // current underlying stream, nil until (re)opened
	failures int           // consecutive failures with zero forward progress
}

// NewResumableReader returns an io.ReadCloser over [0,size) of path that resumes
// on transient mid-stream errors. It honours ctx: a cancelled ctx aborts the
// next read or reopen.
func NewResumableReader(ctx context.Context, adapter Adapter, path string, size int64, policy RetryPolicy) io.ReadCloser {
	if policy.MaxAttempts < 1 {
		policy.MaxAttempts = 1
	}
	return &resumableReader{ctx: ctx, adapter: adapter, path: path, size: size, policy: policy, sleep: time.Sleep}
}

func (r *resumableReader) Read(p []byte) (int, error) {
	if r.offset >= r.size {
		return 0, io.EOF
	}
	for {
		if err := r.ctx.Err(); err != nil {
			return 0, err
		}
		if r.inner == nil {
			rc, err := r.adapter.ReadRange(r.path, r.offset, r.size-r.offset)
			if err != nil {
				if !classify(err) || r.failures+1 >= r.policy.MaxAttempts {
					return 0, err
				}
				r.failures++
				r.sleep(jitteredBackoff(r.policy, r.failures))
				continue
			}
			r.inner = rc
		}

		n, err := r.inner.Read(p)
		if n > 0 {
			r.offset += int64(n)
			r.failures = 0
		}
		if err == nil {
			return n, nil
		}

		// Stream ended or errored. Close it and decide whether to resume.
		_ = r.inner.Close()
		r.inner = nil

		if err == io.EOF {
			if r.offset >= r.size {
				return n, io.EOF // genuine end of object
			}
			err = io.ErrUnexpectedEOF // short read → resume
		}

		if !classify(err) || r.failures+1 >= r.policy.MaxAttempts {
			return n, err
		}
		// Any bytes read this call are valid; deliver them, resume next call.
		if n > 0 {
			return n, nil
		}
		r.failures++
		r.sleep(jitteredBackoff(r.policy, r.failures))
	}
}

func (r *resumableReader) Close() error {
	if r.inner != nil {
		err := r.inner.Close()
		r.inner = nil
		return err
	}
	return nil
}
