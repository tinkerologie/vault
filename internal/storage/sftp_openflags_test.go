package storage

import (
	"bytes"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pkg/sftp"
)

type writeOnlyGateway struct {
	sftp.FileWriter
	t *testing.T
}

func (g writeOnlyGateway) Filewrite(r *sftp.Request) (io.WriterAt, error) {
	flags := r.Pflags()
	if flags != (sftp.FileOpenFlags{Write: true, Creat: true, Trunc: true}) {
		g.t.Errorf("upload OPEN flags = %+v, want write, create and truncate only", flags)
	}
	if flags.Read {
		// Accept the OPEN, then return SSH_FX_FAILURE on the first WRITE.
		return rejectedGatewayHandle{}, nil
	}
	return g.FileWriter.Filewrite(r)
}

type rejectedGatewayHandle struct{}

func (rejectedGatewayHandle) WriteAt([]byte, int64) (int, error) {
	return 0, sftp.ErrSSHFxFailure
}

func newGatewayClient(t *testing.T, handlers sftp.Handlers) *sftp.Client {
	t.Helper()
	local, remote := net.Pipe()
	server := sftp.NewRequestServer(remote, handlers)
	go func() { _ = server.Serve() }()
	t.Cleanup(func() { _ = server.Close() })
	client, err := sftp.NewClientPipe(local, local, sftpClientOptions()...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client
}

func TestSFTPUploadToWriteOnlyGateway(t *testing.T) {
	t.Parallel()
	const packet = 32 * 1024
	const batch = 2 * 1024 * 1024
	for _, size := range []int{331, packet - 1, packet + 1, batch - 1, batch + 1, 2*batch + 71} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			payload := make([]byte, size)
			for i := range payload {
				payload[i] = byte(i ^ i>>8 ^ i>>16 ^ i>>24)
			}
			for _, method := range []string{"Write", "WriteFrom"} {
				t.Run(method, func(t *testing.T) {
					handlers := sftp.InMemHandler()
					handlers.FilePut = writeOnlyGateway{FileWriter: handlers.FilePut, t: t}
					client := newGatewayClient(t, handlers)
					adapter := &SFTPAdapter{
						config: SFTPConfig{BasePath: "/"},
						pool: newSFTPPool(1, func() (sftpConn, error) {
							return &sftpConnection{sftp: client}, nil
						}),
					}
					t.Cleanup(func() { _ = adapter.Close() })
					check := newGatewayClient(t, handlers)
					name := method + ".bin"
					var err error
					if method == "Write" {
						err = adapter.Write(name, bytes.NewReader(payload))
					} else {
						err = adapter.WriteFrom(name, func() (io.ReadCloser, error) {
							return io.NopCloser(bytes.NewReader(payload)), nil
						})
					}
					if err != nil {
						t.Fatal(err)
					}
					f, err := check.Open("/" + name)
					if err != nil {
						t.Fatal(err)
					}
					defer func() { _ = f.Close() }()
					got, err := io.ReadAll(f)
					if err != nil {
						t.Fatal(err)
					}
					if !bytes.Equal(got, payload) {
						t.Fatal("remote contents differ from uploaded bytes")
					}
				})
			}
		})
	}
}

func TestSFTPOverwriteTruncatesExistingFile(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	adapter := startSFTP458Server(t).adapter(t, root)
	for _, content := range []string{"original longer content", "short"} {
		if err := adapter.Write("replace.bin", strings.NewReader(content)); err != nil {
			t.Fatal(err)
		}
	}
	got, err := os.ReadFile(filepath.Join(root, "replace.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "short" {
		t.Fatalf("overwritten file contains %q, want %q", got, "short")
	}
}
