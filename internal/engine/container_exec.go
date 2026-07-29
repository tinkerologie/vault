package engine

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/client"
)

// execLimitStderr caps how much of a failed command's stderr is retained for
// the error message. Enough to carry a real diagnostic, bounded so a command
// that fails by emitting megabytes cannot be turned into an unreadable error.
const execLimitStderr = 8 << 10

// execStdinDrainTimeout bounds how long the stdin writer is given to unwind
// after the command has finished and the connection has been closed.
const execStdinDrainTimeout = 10 * time.Second

// execExitPollInterval is how often the exec is checked for having finished,
// so a command that exits while stdin is still attached cannot hold the
// connection — and the run — open indefinitely.
const execExitPollInterval = time.Second

// execInContainer runs cmd inside a running container, streaming stdout to
// stdout and returning the command's exit status.
//
// Docker multiplexes stdout and stderr over one connection unless a TTY is
// allocated, so no TTY is requested and stdcopy demultiplexes them. A TTY
// would interleave the two into a single stream — fatal here, because stdout
// is a database dump being written verbatim to a file and any stderr mixed
// into it would silently corrupt the backup.
//
// stdin, when non-nil, is streamed to the command and the write side is closed
// afterwards so tools that read until EOF (psql, mysql) terminate.
func (h *ContainerHandler) execInContainer(ctx context.Context, containerID string, cmd []string, env []string, stdin io.Reader, stdout io.Writer) error {
	created, err := h.cli.ExecCreate(ctx, containerID, client.ExecCreateOptions{
		Cmd:          cmd,
		Env:          env,
		AttachStdout: true,
		AttachStderr: true,
		AttachStdin:  stdin != nil,
	})
	if err != nil {
		return fmt.Errorf("creating exec: %w", err)
	}

	attached, err := h.cli.ExecAttach(ctx, created.ID, client.ExecAttachOptions{})
	if err != nil {
		return fmt.Errorf("attaching to exec: %w", err)
	}
	// Closed exactly once, from whichever of the paths below gets there first.
	var closeOnce sync.Once
	defer closeOnce.Do(attached.Close)

	// Nothing else closes the hijacked connection, and both StdCopy and the
	// stdin copy block directly on it. Two things therefore have to force it
	// shut, or the exec wedges the run:
	//
	//  - cancellation, which does not by itself interrupt a blocking net.Conn
	//    read; and
	//  - the command exiting while stdin is still attached. Docker keeps the
	//    connection open until it sees EOF on stdin, but the writer only sends
	//    EOF after its copy finishes — and that copy is blocked precisely
	//    because the command stopped reading. StdCopy then waits forever on a
	//    command that is already gone. Observed wedging a restore for minutes
	//    with psql long since exited.
	//
	// Polling the exec's own state breaks that circle without guessing at a
	// duration, so a legitimately long dump is never cut short.
	done := make(chan struct{})
	defer close(done)
	go func() {
		ticker := time.NewTicker(execExitPollInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				closeOnce.Do(attached.Close)
				return
			case <-done:
				return
			case <-ticker.C:
				st, err := h.cli.ExecInspect(ctx, created.ID, client.ExecInspectOptions{})
				if err == nil && !st.Running {
					closeOnce.Do(attached.Close)
					return
				}
			}
		}
	}()

	// Feed stdin on its own goroutine: a large restore would otherwise deadlock
	// against the command's output filling the connection buffer while we are
	// still writing.
	stdinErr := make(chan error, 1)
	if stdin != nil {
		go func() {
			_, copyErr := io.Copy(attached.Conn, stdin)
			// Half-close so the command sees EOF; the read side stays open for
			// its remaining output.
			if cw, ok := attached.Conn.(interface{ CloseWrite() error }); ok {
				_ = cw.CloseWrite()
			}
			stdinErr <- copyErr
		}()
	} else {
		stdinErr <- nil
	}

	var stderr bytes.Buffer
	_, copyErr := stdcopy.StdCopy(stdout, limitWriter(&stderr, execLimitStderr), attached.Reader)

	// Close BEFORE waiting on the stdin goroutine. A command that exits early —
	// psql aborting on a conflict, say — stops draining stdin, leaving the
	// writer blocked on a socket nobody reads. Waiting on it first therefore
	// hangs forever even though the command is already gone: observed wedging a
	// restore with psql long since exited. Closing here makes that copy return.
	closeOnce.Do(attached.Close)

	var stdinCopyErr error
	select {
	case stdinCopyErr = <-stdinErr:
	case <-time.After(execStdinDrainTimeout):
		// Backstop: never let a stuck writer outlive the command it fed.
		stdinCopyErr = fmt.Errorf("timed out waiting for stdin to finish")
	}

	if copyErr != nil {
		return fmt.Errorf("reading exec output: %w", copyErr)
	}

	inspected, err := h.cli.ExecInspect(ctx, created.ID, client.ExecInspectOptions{})
	if err != nil {
		return fmt.Errorf("inspecting exec: %w", err)
	}
	if inspected.ExitCode != 0 {
		msg := bytes.TrimSpace(stderr.Bytes())
		if len(msg) == 0 {
			return fmt.Errorf("command %q exited %d", cmd[0], inspected.ExitCode)
		}
		return fmt.Errorf("command %q exited %d: %s", cmd[0], inspected.ExitCode, msg)
	}
	if stdinCopyErr != nil {
		return fmt.Errorf("writing exec input: %w", stdinCopyErr)
	}
	return nil
}

// limitWriter returns a writer that discards everything past n bytes, so
// captured stderr cannot grow without bound.
func limitWriter(w io.Writer, n int) io.Writer {
	return &limitedWriter{w: w, remaining: n}
}

type limitedWriter struct {
	w         io.Writer
	remaining int
}

func (l *limitedWriter) Write(p []byte) (int, error) {
	if l.remaining <= 0 {
		return len(p), nil // silently drop, but report success so the copy continues
	}
	if len(p) > l.remaining {
		if _, err := l.w.Write(p[:l.remaining]); err != nil {
			return 0, err
		}
		l.remaining = 0
		return len(p), nil
	}
	l.remaining -= len(p)
	return l.w.Write(p)
}
