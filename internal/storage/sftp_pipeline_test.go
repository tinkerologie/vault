package storage

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pkg/sftp"
)

// writePipelineGate withholds WRITE replies until a second WRITE reaches the
// server. A sequential uploader cannot open the gate. Other requests, including
// the handshake, path checks, and file creation, pass through normally.
type writePipelineGate struct {
	net.Conn
	ctx       context.Context
	opened    chan struct{}
	writes    atomic.Int32
	header    [5]byte
	headerLen int
	remaining uint32
}

type failingSFTPFileWriter struct {
	sftp.FileWriter
	failAt    int64
	onFailure func()
}

func (w failingSFTPFileWriter) Filewrite(r *sftp.Request) (io.WriterAt, error) {
	file, err := w.FileWriter.Filewrite(r)
	if err != nil {
		return nil, err
	}
	return failSFTPWriteAt{WriterAt: file, failAt: w.failAt, onFailure: w.onFailure}, nil
}

type failSFTPWriteAt struct {
	io.WriterAt
	failAt    int64
	onFailure func()
}

func (w failSFTPWriteAt) WriteAt(p []byte, off int64) (int, error) {
	if off >= w.failAt {
		if w.onFailure != nil {
			w.onFailure()
		}
		return 0, errors.New("injected remote write failure")
	}
	return w.WriterAt.WriteAt(p, off)
}

func (w failSFTPWriteAt) Close() error {
	if closer, ok := w.WriterAt.(io.Closer); ok {
		return closer.Close()
	}
	return nil
}

func TestSFTPWriteRemoteFailureRemovesPartialFile(t *testing.T) {
	t.Parallel()
	handlers := sftp.InMemHandler()
	handlers.FilePut = failingSFTPFileWriter{FileWriter: handlers.FilePut, failAt: 2 * 32768}
	cConn, sConn := net.Pipe()
	server := sftp.NewRequestServer(sConn, handlers)
	go func() { _ = server.Serve() }()
	t.Cleanup(func() { _ = server.Close() })
	client, err := sftp.NewClientPipe(cConn, cConn, sftpClientOptions()...)
	if err != nil {
		t.Fatal(err)
	}
	adapter := &SFTPAdapter{
		config: SFTPConfig{BasePath: "/"},
		pool: newSFTPPool(1, func() (sftpConn, error) {
			return &sftpConnection{sftp: client}, nil
		}),
	}
	t.Cleanup(func() { _ = adapter.Close() })
	err = adapter.Write("partial.bin", struct{ io.Reader }{bytes.NewReader(make([]byte, 512*1024))})
	if err == nil {
		t.Fatal("expected remote WRITE failure")
	}
	// Write discards the pooled connection on error, so inspect the server's
	// filesystem through an independent client with the same handlers.
	otherClient, otherServer := net.Pipe()
	verifier := sftp.NewRequestServer(otherServer, handlers)
	go func() { _ = verifier.Serve() }()
	t.Cleanup(func() { _ = verifier.Close() })
	check, err := sftp.NewClientPipe(otherClient, otherClient)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = check.Close() })
	if _, err := check.Stat("/partial.bin"); !os.IsNotExist(err) {
		t.Fatalf("partial file remains after remote failure: %v", err)
	}
}

// stalledSFTPSource exposes no closer: the adapter must not depend on being
// able to cancel an arbitrary caller-owned reader to surface a remote error.
type stalledSFTPSource struct {
	remaining int
	release   chan struct{}
}

func (r *stalledSFTPSource) Read(p []byte) (int, error) {
	if r.remaining > 0 {
		n := min(len(p), r.remaining)
		clear(p[:n])
		r.remaining -= n
		return n, nil
	}
	<-r.release
	return 0, io.EOF
}

func TestSFTPWriteRemoteErrorDoesNotWaitForStalledSource(t *testing.T) {
	t.Parallel()
	const windowBytes = 64 * 32768
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	rejected := make(chan struct{})
	var rejectedOnce sync.Once
	handlers := sftp.InMemHandler()
	handlers.FilePut = failingSFTPFileWriter{
		FileWriter: handlers.FilePut,
		failAt:     windowBytes - 32768,
		onFailure:  func() { rejectedOnce.Do(func() { close(rejected) }) },
	}
	cConn, sConn := net.Pipe()
	server := sftp.NewRequestServer(sConn, handlers)
	go func() { _ = server.Serve() }()
	t.Cleanup(func() { _ = server.Close() })
	client, err := sftp.NewClientPipe(cConn, cConn, sftpClientOptions()...)
	if err != nil {
		t.Fatal(err)
	}
	adapter := &SFTPAdapter{
		config: SFTPConfig{BasePath: "/"},
		pool: newSFTPPool(1, func() (sftpConn, error) {
			return &sftpConnection{sftp: client}, nil
		}),
	}
	t.Cleanup(func() { _ = adapter.Close() })
	source := &stalledSFTPSource{remaining: windowBytes, release: make(chan struct{})}
	defer close(source.release)
	finished := make(chan error, 1)
	go func() { finished <- adapter.Write("stalled.bin", source) }()
	select {
	case <-rejected:
	case <-ctx.Done():
		t.Fatal("server did not reject the final WRITE in the window")
	}
	select {
	case err := <-finished:
		if err == nil || !strings.Contains(err.Error(), "write:") {
			t.Fatalf("expected remote write error, got %v", err)
		}
	case <-ctx.Done():
		t.Fatal("remote WRITE failure is waiting for the stalled source")
	}
	if _, err := handlers.FileList.Filelist(sftp.NewRequest("Stat", "/stalled.bin")); !os.IsNotExist(err) {
		t.Fatalf("partial file remains after remote error: %v", err)
	}
}

func (g *writePipelineGate) Read(p []byte) (int, error) {
	n, err := g.Conn.Read(p)
	data := p[:n]
	for len(data) > 0 {
		if g.remaining > 0 {
			skip := min(len(data), int(g.remaining))
			g.remaining -= uint32(skip)
			data = data[skip:]
			continue
		}
		copied := copy(g.header[g.headerLen:], data)
		g.headerLen += copied
		data = data[copied:]
		if g.headerLen == len(g.header) {
			length := binary.BigEndian.Uint32(g.header[:4])
			if length == 0 {
				return n, io.ErrUnexpectedEOF
			}
			if g.header[4] == 6 && g.writes.Add(1) == 2 { // SSH_FXP_WRITE
				close(g.opened)
			}
			g.remaining = length - 1
			g.headerLen = 0
		}
	}
	return n, err
}

func (g *writePipelineGate) Write(p []byte) (int, error) {
	if g.writes.Load() > 0 {
		select {
		case <-g.opened:
		case <-g.ctx.Done():
			return 0, g.ctx.Err()
		}
	}
	return g.Conn.Write(p)
}

func TestSFTPWritePipelinesUnknownSizeStream(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	var gate *writePipelineGate
	root := t.TempDir()
	adapter := newInProcessSFTP(t, root, func(conn net.Conn) net.Conn {
		gate = &writePipelineGate{Conn: conn, ctx: ctx, opened: make(chan struct{})}
		stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
		t.Cleanup(func() { stop() })
		return gate
	})
	// Hide Len, Size, Stat and WriterTo, as the runner's wrapped stream does.
	payload := bytes.Repeat([]byte("pipeline-integrity\x00"), 32*1024)
	reader := struct{ io.Reader }{bytes.NewReader(payload)}
	if err := adapter.Write("upload.bin", reader); err != nil {
		t.Fatalf("upload failed (WRITE replies require two requests in flight): %v", err)
	}
	select {
	case <-gate.opened:
	default:
		t.Fatal("upload did not pipeline two WRITE requests")
	}
	got, err := os.ReadFile(filepath.Join(root, "upload.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if sha256.Sum256(got) != sha256.Sum256(payload) {
		t.Fatal("pipelined upload content differs from source")
	}
}
