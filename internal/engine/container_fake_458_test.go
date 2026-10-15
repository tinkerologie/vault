package engine

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"iter"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/moby/moby/api/types/jsonstream"
	"github.com/moby/moby/client"
)

// fakeDocker458 layers per-test overrides on top of mockDockerClient. Each
// nil hook falls through to the embedded mock, so the defaults every other
// test relies on (sentinel errors for exec, canned inspect, and so on) stay
// exactly as they are; a test opts into a behaviour by setting a hook.
type fakeDocker458 struct {
	mockDockerClient

	execCreate       func(ctx context.Context, containerID string, opts client.ExecCreateOptions) (client.ExecCreateResult, error)
	execAttach       func(ctx context.Context, execID string, opts client.ExecAttachOptions) (client.ExecAttachResult, error)
	execInspect      func(ctx context.Context, execID string) (client.ExecInspectResult, error)
	containerInspect func(ctx context.Context, id string) (client.ContainerInspectResult, error)
	containerCreate  func(ctx context.Context, opts client.ContainerCreateOptions) (client.ContainerCreateResult, error)
	containerStart   func(ctx context.Context, id string) (client.ContainerStartResult, error)
	containerStop    func(ctx context.Context, id string) (client.ContainerStopResult, error)
	containerRemove  func(ctx context.Context, id string) (client.ContainerRemoveResult, error)
	containerStats   func(ctx context.Context, id string) (client.ContainerStatsResult, error)
	imageInspect     func(ctx context.Context, ref string) (client.ImageInspectResult, error)
	imageLoad        func(ctx context.Context, r io.Reader) (client.ImageLoadResult, error)
	imagePull        func(ctx context.Context, ref string) (client.ImagePullResponse, error)
	imageSave        func(ctx context.Context, images []string) (client.ImageSaveResult, error)
}

var _ dockerClient = (*fakeDocker458)(nil)

func (f *fakeDocker458) ExecCreate(ctx context.Context, id string, o client.ExecCreateOptions) (client.ExecCreateResult, error) {
	if f.execCreate != nil {
		return f.execCreate(ctx, id, o)
	}
	return f.mockDockerClient.ExecCreate(ctx, id, o)
}

func (f *fakeDocker458) ExecAttach(ctx context.Context, id string, o client.ExecAttachOptions) (client.ExecAttachResult, error) {
	if f.execAttach != nil {
		return f.execAttach(ctx, id, o)
	}
	return f.mockDockerClient.ExecAttach(ctx, id, o)
}

func (f *fakeDocker458) ExecInspect(ctx context.Context, id string, o client.ExecInspectOptions) (client.ExecInspectResult, error) {
	if f.execInspect != nil {
		return f.execInspect(ctx, id)
	}
	return f.mockDockerClient.ExecInspect(ctx, id, o)
}

func (f *fakeDocker458) ContainerInspect(ctx context.Context, id string, o client.ContainerInspectOptions) (client.ContainerInspectResult, error) {
	if f.containerInspect != nil {
		return f.containerInspect(ctx, id)
	}
	return f.mockDockerClient.ContainerInspect(ctx, id, o)
}

func (f *fakeDocker458) ContainerCreate(ctx context.Context, o client.ContainerCreateOptions) (client.ContainerCreateResult, error) {
	if f.containerCreate != nil {
		return f.containerCreate(ctx, o)
	}
	return f.mockDockerClient.ContainerCreate(ctx, o)
}

func (f *fakeDocker458) ContainerStart(ctx context.Context, id string, o client.ContainerStartOptions) (client.ContainerStartResult, error) {
	if f.containerStart != nil {
		return f.containerStart(ctx, id)
	}
	return f.mockDockerClient.ContainerStart(ctx, id, o)
}

func (f *fakeDocker458) ContainerStop(ctx context.Context, id string, o client.ContainerStopOptions) (client.ContainerStopResult, error) {
	if f.containerStop != nil {
		return f.containerStop(ctx, id)
	}
	return f.mockDockerClient.ContainerStop(ctx, id, o)
}

func (f *fakeDocker458) ContainerRemove(ctx context.Context, id string, o client.ContainerRemoveOptions) (client.ContainerRemoveResult, error) {
	if f.containerRemove != nil {
		return f.containerRemove(ctx, id)
	}
	return f.mockDockerClient.ContainerRemove(ctx, id, o)
}

func (f *fakeDocker458) ContainerStats(ctx context.Context, id string, o client.ContainerStatsOptions) (client.ContainerStatsResult, error) {
	if f.containerStats != nil {
		return f.containerStats(ctx, id)
	}
	return f.mockDockerClient.ContainerStats(ctx, id, o)
}

func (f *fakeDocker458) ImageInspect(ctx context.Context, ref string, opts ...client.ImageInspectOption) (client.ImageInspectResult, error) {
	if f.imageInspect != nil {
		return f.imageInspect(ctx, ref)
	}
	return f.mockDockerClient.ImageInspect(ctx, ref, opts...)
}

func (f *fakeDocker458) ImageLoad(ctx context.Context, r io.Reader, opts ...client.ImageLoadOption) (client.ImageLoadResult, error) {
	if f.imageLoad != nil {
		return f.imageLoad(ctx, r)
	}
	return f.mockDockerClient.ImageLoad(ctx, r, opts...)
}

func (f *fakeDocker458) ImagePull(ctx context.Context, ref string, o client.ImagePullOptions) (client.ImagePullResponse, error) {
	if f.imagePull != nil {
		return f.imagePull(ctx, ref)
	}
	return f.mockDockerClient.ImagePull(ctx, ref, o)
}

func (f *fakeDocker458) ImageSave(ctx context.Context, images []string, opts ...client.ImageSaveOption) (client.ImageSaveResult, error) {
	if f.imageSave != nil {
		return f.imageSave(ctx, images)
	}
	return f.mockDockerClient.ImageSave(ctx, images, opts...)
}

// okImageLoad is an ImageLoad hook that drains the input and succeeds.
func okImageLoad(_ context.Context, r io.Reader) (client.ImageLoadResult, error) {
	_, _ = io.Copy(io.Discard, r)
	return io.NopCloser(strings.NewReader(`{"stream":"Loaded image"}`)), nil
}

// fakePullResponse is an in-memory client.ImagePullResponse.
type fakePullResponse struct {
	io.Reader
	waitErr error
}

func (p *fakePullResponse) Close() error { return nil }
func (p *fakePullResponse) Wait(context.Context) error {
	return p.waitErr
}
func (p *fakePullResponse) JSONMessages(context.Context) iter.Seq2[jsonstream.Message, error] {
	return func(func(jsonstream.Message, error) bool) {}
}

// muxFrame encodes one Docker stdcopy frame: an 8-byte header carrying the
// stream type and big-endian payload length, then the payload.
func muxFrame(stream byte, payload string) []byte {
	hdr := make([]byte, 8)
	hdr[0] = stream
	binary.BigEndian.PutUint32(hdr[4:], uint32(len(payload))) // #nosec G115 -- test payloads are tiny
	return append(hdr, payload...)
}

// muxStream concatenates stdout (1) and stderr (2) frames.
func muxStream(frames ...[]byte) []byte {
	return bytes.Join(frames, nil)
}

// fakeExecConn is an in-memory net.Conn standing in for the hijacked exec
// connection. Output is served from attached.Reader (built separately by
// newFakeExec); this type records what the command received on stdin and
// whether, and how, the connection was closed.
type fakeExecConn struct {
	mu         sync.Mutex
	stdin      bytes.Buffer
	closed     bool
	closeCount int
	halfClosed bool
	closedCh   chan struct{}
	// blockWrites makes Write wait until Close, modelling a command that has
	// stopped draining stdin.
	blockWrites bool
	writeErr    error
}

func newFakeExecConn() *fakeExecConn {
	return &fakeExecConn{closedCh: make(chan struct{})}
}

func (c *fakeExecConn) Read([]byte) (int, error) {
	<-c.closedCh
	return 0, net.ErrClosed
}

func (c *fakeExecConn) Write(p []byte) (int, error) {
	if c.blockWrites {
		<-c.closedCh
		return 0, net.ErrClosed
	}
	if c.writeErr != nil {
		return 0, c.writeErr
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.stdin.Write(p)
}

func (c *fakeExecConn) Close() error {
	c.mu.Lock()
	c.closeCount++
	first := !c.closed
	c.closed = true
	c.mu.Unlock()
	if first {
		close(c.closedCh)
	}
	return nil
}

func (c *fakeExecConn) LocalAddr() net.Addr              { return &net.UnixAddr{Name: "fake", Net: "unix"} }
func (c *fakeExecConn) RemoteAddr() net.Addr             { return &net.UnixAddr{Name: "fake", Net: "unix"} }
func (c *fakeExecConn) SetDeadline(time.Time) error      { return nil }
func (c *fakeExecConn) SetReadDeadline(time.Time) error  { return nil }
func (c *fakeExecConn) SetWriteDeadline(time.Time) error { return nil }
func (c *fakeExecConn) stdinString() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.stdin.String()
}
func (c *fakeExecConn) wasHalfClosed() bool         { c.mu.Lock(); defer c.mu.Unlock(); return c.halfClosed }
func (c *fakeExecConn) closes() int                 { c.mu.Lock(); defer c.mu.Unlock(); return c.closeCount }
func (c *fakeExecConn) markHalfClosed()             { c.mu.Lock(); c.halfClosed = true; c.mu.Unlock() }
func (c *fakeExecConn) isClosed() bool              { c.mu.Lock(); defer c.mu.Unlock(); return c.closed }
func (c *fakeExecConn) waitClosed() <-chan struct{} { return c.closedCh }

// halfCloseConn adds CloseWrite, the half-close the exec path uses to
// deliver EOF on stdin while keeping the read side open.
type halfCloseConn struct{ *fakeExecConn }

func (c halfCloseConn) CloseWrite() error {
	c.markHalfClosed()
	return nil
}

// fakeExecOutput builds the reader the exec output is served from. With
// holdOpen, the stream does not end after the canned frames: it blocks until
// the connection is closed, as a real hijacked connection does while the
// command is still running, and then ends with endErr (io.EOF when nil).
func fakeExecOutput(conn *fakeExecConn, payload []byte, holdOpen bool, endErr error) *bufio.Reader {
	if !holdOpen {
		return bufio.NewReader(bytes.NewReader(payload))
	}
	pr, pw := io.Pipe()
	go func() {
		if _, err := pw.Write(payload); err != nil {
			return
		}
		<-conn.waitClosed()
		if endErr == nil {
			_ = pw.Close()
			return
		}
		_ = pw.CloseWithError(endErr)
	}()
	return bufio.NewReader(pr)
}

// execScript describes one simulated exec: what the command prints, how it
// exits, and how the connection behaves.
type execScript struct {
	output     []byte
	exitCode   int
	holdOpen   bool  // output stream stays open until the conn is closed
	endErr     error // error the held-open stream ends with
	halfClose  bool  // conn supports CloseWrite
	blockStdin bool  // stdin writes block until the conn is closed
	stdinErr   error // stdin writes fail immediately
	createErr  error
	attachErr  error
	inspectErr error
	// running reports Running=true from ExecInspect until the conn closes.
	running bool
}

// execRecorder captures what the fake saw, for assertions.
type execRecorder struct {
	mu      sync.Mutex
	creates []client.ExecCreateOptions
	conns   []*fakeExecConn
}

func (r *execRecorder) lastCreate() client.ExecCreateOptions {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.creates) == 0 {
		return client.ExecCreateOptions{}
	}
	return r.creates[len(r.creates)-1]
}

func (r *execRecorder) lastConn() *fakeExecConn {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.conns) == 0 {
		return nil
	}
	return r.conns[len(r.conns)-1]
}

func (r *execRecorder) createCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.creates)
}

// installExec wires the exec hooks of f to run the given scripts in order;
// the last script repeats once the list is exhausted.
func installExec(f *fakeDocker458, scripts ...execScript) *execRecorder {
	rec := &execRecorder{}
	var mu sync.Mutex
	idx := -1
	current := func() execScript {
		mu.Lock()
		defer mu.Unlock()
		if idx < 0 {
			return scripts[0]
		}
		if idx >= len(scripts) {
			return scripts[len(scripts)-1]
		}
		return scripts[idx]
	}
	f.execCreate = func(_ context.Context, _ string, o client.ExecCreateOptions) (client.ExecCreateResult, error) {
		mu.Lock()
		idx++
		mu.Unlock()
		rec.mu.Lock()
		rec.creates = append(rec.creates, o)
		rec.mu.Unlock()
		s := current()
		if s.createErr != nil {
			return client.ExecCreateResult{}, s.createErr
		}
		return client.ExecCreateResult{ID: "exec-1"}, nil
	}
	f.execAttach = func(context.Context, string, client.ExecAttachOptions) (client.ExecAttachResult, error) {
		s := current()
		if s.attachErr != nil {
			return client.ExecAttachResult{}, s.attachErr
		}
		conn := newFakeExecConn()
		conn.blockWrites = s.blockStdin
		conn.writeErr = s.stdinErr
		rec.mu.Lock()
		rec.conns = append(rec.conns, conn)
		rec.mu.Unlock()
		var nc net.Conn = conn
		if s.halfClose {
			nc = halfCloseConn{conn}
		}
		res := client.ExecAttachResult{}
		res.Conn = nc
		res.Reader = fakeExecOutput(conn, s.output, s.holdOpen, s.endErr)
		return res, nil
	}
	f.execInspect = func(context.Context, string) (client.ExecInspectResult, error) {
		s := current()
		if s.inspectErr != nil {
			return client.ExecInspectResult{}, s.inspectErr
		}
		running := false
		if s.running {
			if c := rec.lastConn(); c != nil && !c.isClosed() {
				running = true
			}
		}
		return client.ExecInspectResult{ID: "exec-1", Running: running, ExitCode: s.exitCode}, nil
	}
	return rec
}

var errFake458 = errors.New("fake458: boom")
