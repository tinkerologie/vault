package engine

import (
	"bytes"
	"context"
	"fmt"
	"io"

	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/client"
)

// execLimitStderr caps how much of a failed command's stderr is retained for
// the error message. Enough to carry a real diagnostic, bounded so a command
// that fails by emitting megabytes cannot be turned into an unreadable error.
const execLimitStderr = 8 << 10

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
	defer attached.Close()

	// StdCopy and the stdin copy both block directly on the hijacked
	// connection, which nothing else closes. Without this a database command
	// that hangs — or one that exits without draining stdin — would wedge the
	// run past any watchdog, because cancelling the context alone does not
	// interrupt a blocking read on a net.Conn.
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			attached.Close()
		case <-done:
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
	if _, err := stdcopy.StdCopy(stdout, limitWriter(&stderr, execLimitStderr), attached.Reader); err != nil {
		return fmt.Errorf("reading exec output: %w", err)
	}
	// Deliberately NOT gating on stdin here: a command that exits early stops
	// draining stdin, so the write fails with a broken pipe. Its own exit
	// status below is the more useful diagnosis, so stdin's error is only
	// consulted when the command otherwise looks successful.
	stdinCopyErr := <-stdinErr

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
