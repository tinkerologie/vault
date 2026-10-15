package engine

import (
	"bytes"
	"context"
	"errors"
	"net"
	"slices"
	"strings"
	"testing"

	"github.com/moby/moby/client"
)

func TestExecInContainer458(t *testing.T) {
	t.Parallel()

	bigStderr := strings.Repeat("e", execLimitStderr+500)

	tests := []struct {
		name       string
		script     execScript
		stdin      string
		withStdin  bool
		wantErr    string // substring; empty means success
		wantStdout string
		check      func(t *testing.T, rec *execRecorder, err error)
	}{
		{
			name: "demultiplexes stdout from stderr",
			script: execScript{output: muxStream(
				muxFrame(1, "line one\n"),
				muxFrame(2, "a warning\n"),
				muxFrame(1, "line two\n"),
			)},
			wantStdout: "line one\nline two\n",
			check: func(t *testing.T, rec *execRecorder, _ error) {
				o := rec.lastCreate()
				if o.AttachStdin {
					t.Error("AttachStdin = true without stdin")
				}
				if !o.AttachStdout || !o.AttachStderr {
					t.Errorf("stdout/stderr not attached: %+v", o)
				}
				if !slices.Equal(o.Cmd, []string{"pg_dumpall", "-U", "postgres"}) {
					t.Errorf("Cmd = %v", o.Cmd)
				}
				if !slices.Equal(o.Env, []string{"PGPASSWORD=x"}) {
					t.Errorf("Env = %v", o.Env)
				}
				if c := rec.lastConn(); c == nil || c.closes() != 1 {
					t.Errorf("connection should be closed exactly once")
				}
			},
		},
		{
			name: "non-zero exit carries trimmed stderr",
			script: execScript{
				output:   muxStream(muxFrame(1, "partial"), muxFrame(2, "  FATAL: role missing \n")),
				exitCode: 2,
			},
			wantErr:    `command "pg_dumpall" exited 2: FATAL: role missing`,
			wantStdout: "partial",
		},
		{
			name:    "non-zero exit with no stderr",
			script:  execScript{exitCode: 3},
			wantErr: `command "pg_dumpall" exited 3`,
			check: func(t *testing.T, _ *execRecorder, err error) {
				if strings.Contains(err.Error(), "exited 3:") {
					t.Errorf("no stderr should mean no trailing colon: %v", err)
				}
			},
		},
		{
			name:    "stderr is capped",
			script:  execScript{output: muxFrame(2, bigStderr), exitCode: 1},
			wantErr: "exited 1",
			check: func(t *testing.T, _ *execRecorder, err error) {
				if n := strings.Count(err.Error(), "e"); n > execLimitStderr+10 {
					t.Errorf("stderr not capped: %d bytes of it in the error", n)
				}
			},
		},
		{
			name:      "stdin delivered and half-closed",
			script:    execScript{output: muxFrame(1, "ok"), halfClose: true},
			stdin:     "SELECT 1;\n",
			withStdin: true,
			check: func(t *testing.T, rec *execRecorder, _ error) {
				if !rec.lastCreate().AttachStdin {
					t.Error("AttachStdin = false with stdin")
				}
				c := rec.lastConn()
				if got := c.stdinString(); got != "SELECT 1;\n" {
					t.Errorf("stdin = %q", got)
				}
				if !c.wasHalfClosed() {
					t.Error("stdin was not half-closed")
				}
			},
			wantStdout: "ok",
		},
		{
			name:      "stdin delivered on a conn without CloseWrite",
			script:    execScript{},
			stdin:     "data",
			withStdin: true,
			check: func(t *testing.T, rec *execRecorder, _ error) {
				if got := rec.lastConn().stdinString(); got != "data" {
					t.Errorf("stdin = %q", got)
				}
			},
		},
		{
			name:      "stdin write failure surfaces after a clean exit",
			script:    execScript{stdinErr: errFake458},
			stdin:     "data",
			withStdin: true,
			wantErr:   "writing exec input",
		},
		{
			name:      "command exits while stdin is blocked",
			script:    execScript{blockStdin: true, halfClose: true},
			stdin:     strings.Repeat("x", 1<<16),
			withStdin: true,
			wantErr:   "writing exec input",
			check: func(t *testing.T, _ *execRecorder, err error) {
				if !errors.Is(err, net.ErrClosed) {
					t.Errorf("want net.ErrClosed in chain, got %v", err)
				}
			},
		},
		{
			name:      "exit code wins over a blocked stdin",
			script:    execScript{blockStdin: true, exitCode: 1, output: muxFrame(2, "duplicate key")},
			stdin:     "data",
			withStdin: true,
			wantErr:   "exited 1: duplicate key",
		},
		{
			name:    "create error",
			script:  execScript{createErr: errFake458},
			wantErr: "creating exec",
		},
		{
			name:    "attach error",
			script:  execScript{attachErr: errFake458},
			wantErr: "attaching to exec",
		},
		{
			name:    "inspect error",
			script:  execScript{output: muxFrame(1, "x"), inspectErr: errFake458},
			wantErr: "inspecting exec",
		},
		{
			name:    "malformed output stream",
			script:  execScript{output: muxFrame(9, "garbage")},
			wantErr: "reading exec output",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := &fakeDocker458{}
			rec := installExec(f, tt.script)
			h := &ContainerHandler{cli: f}
			var stdout bytes.Buffer
			var err error
			if tt.withStdin {
				err = h.execInContainer(t.Context(), "cid", []string{"pg_dumpall", "-U", "postgres"}, []string{"PGPASSWORD=x"}, strings.NewReader(tt.stdin), &stdout)
			} else {
				err = h.execInContainer(t.Context(), "cid", []string{"pg_dumpall", "-U", "postgres"}, []string{"PGPASSWORD=x"}, nil, &stdout)
			}
			switch {
			case tt.wantErr == "" && err != nil:
				t.Fatalf("unexpected error: %v", err)
			case tt.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tt.wantErr)):
				t.Fatalf("error = %v, want containing %q", err, tt.wantErr)
			}
			if tt.wantStdout != "" && stdout.String() != tt.wantStdout {
				t.Errorf("stdout = %q, want %q", stdout.String(), tt.wantStdout)
			}
			if tt.check != nil {
				tt.check(t, rec, err)
			}
		})
	}
}

// Cancelling the context must force the hijacked connection shut: a blocking
// read on it does not observe cancellation by itself.
func TestExecInContainerCancellationClosesConnection458(t *testing.T) {
	t.Parallel()

	f := &fakeDocker458{}
	rec := installExec(f, execScript{output: muxFrame(1, "started"), holdOpen: true, endErr: net.ErrClosed, running: true})
	ctx, cancel := context.WithCancel(t.Context())
	attach := f.execAttach
	f.execAttach = func(ctx context.Context, id string, o client.ExecAttachOptions) (client.ExecAttachResult, error) {
		res, err := attach(ctx, id, o)
		cancel() // the command is now "running"; cancel the run
		return res, err
	}
	h := &ContainerHandler{cli: f}
	var stdout bytes.Buffer
	err := h.execInContainer(ctx, "cid", []string{"mysqldump"}, nil, nil, &stdout)
	if err == nil || !strings.Contains(err.Error(), "reading exec output") {
		t.Fatalf("error = %v, want reading exec output", err)
	}
	if stdout.String() != "started" {
		t.Errorf("stdout = %q", stdout.String())
	}
	if c := rec.lastConn(); c.closes() != 1 {
		t.Errorf("conn closed %d times, want 1", c.closes())
	}
}

// A command that exits while its output connection is still held open is
// detected by polling ExecInspect, which closes the connection so the run
// is not wedged. This waits one poll interval (1s) by design.
func TestExecInContainerExitedCommandUnwedges458(t *testing.T) {
	t.Parallel()

	f := &fakeDocker458{}
	rec := installExec(f, execScript{output: muxFrame(1, "dump"), holdOpen: true})
	h := &ContainerHandler{cli: f}
	var stdout bytes.Buffer
	if err := h.execInContainer(t.Context(), "cid", []string{"psql"}, nil, nil, &stdout); err != nil {
		t.Fatalf("execInContainer: %v", err)
	}
	if stdout.String() != "dump" {
		t.Errorf("stdout = %q", stdout.String())
	}
	if !rec.lastConn().isClosed() {
		t.Error("connection left open")
	}
}

type errWriter458 struct{}

func (errWriter458) Write([]byte) (int, error) { return 0, errFake458 }

func TestLimitWriter458(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	w := limitWriter(&buf, 5)
	steps := []struct {
		in    string
		wantN int
	}{
		{"abc", 3},  // fits
		{"defg", 4}, // spans the limit: only "de" kept
		{"hij", 3},  // past the limit: dropped, reported as written
	}
	for _, s := range steps {
		n, err := w.Write([]byte(s.in))
		if err != nil || n != s.wantN {
			t.Fatalf("Write(%q) = %d, %v; want %d, nil", s.in, n, err, s.wantN)
		}
	}
	if buf.String() != "abcde" {
		t.Errorf("kept %q, want abcde", buf.String())
	}

	// Underlying write errors propagate on both the spanning and the
	// in-limit path.
	if _, err := limitWriter(errWriter458{}, 2).Write([]byte("abc")); !errors.Is(err, errFake458) {
		t.Errorf("spanning write err = %v", err)
	}
	if _, err := limitWriter(errWriter458{}, 10).Write([]byte("abc")); !errors.Is(err, errFake458) {
		t.Errorf("in-limit write err = %v", err)
	}
}
