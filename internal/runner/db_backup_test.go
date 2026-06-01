package runner

import (
	"context"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/ruaan-deysel/vault/internal/storage"
)

// recordingAdapter captures every Write so tests can assert content/path.
type recordingAdapter struct {
	writes map[string][]byte
	err    error // injected error for the first write
}

func newRecordingAdapter() *recordingAdapter {
	return &recordingAdapter{writes: make(map[string][]byte)}
}

func (a *recordingAdapter) Write(path string, r io.Reader) error {
	if a.err != nil {
		e := a.err
		a.err = nil
		return e
	}
	b, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	a.writes[path] = b
	return nil
}

func (a *recordingAdapter) Read(string) (io.ReadCloser, error) { return nil, nil }
func (a *recordingAdapter) ReadRange(string, int64, int64) (io.ReadCloser, error) {
	return nil, nil
}
func (a *recordingAdapter) Delete(string) error                     { return nil }
func (a *recordingAdapter) List(string) ([]storage.FileInfo, error) { return nil, nil }
func (a *recordingAdapter) Stat(string) (storage.FileInfo, error)   { return storage.FileInfo{}, nil }
func (a *recordingAdapter) TestConnection() error                   { return nil }
func (a *recordingAdapter) GetCapacity(_ context.Context) (storage.Capacity, error) {
	return storage.Capacity{}, nil
}
func (a *recordingAdapter) Usage() (int64, int64, error) { return 0, 0, storage.ErrUsageNotSupported }
func (a *recordingAdapter) WriteFrom(path string, open func() (io.ReadCloser, error)) error {
	rc, err := open()
	if err != nil {
		return err
	}
	defer rc.Close() //nolint:errcheck
	return a.Write(path, rc)
}

var _ storage.Adapter = (*recordingAdapter)(nil)

func TestWriteDBOnceWritesPlaintext(t *testing.T) {
	dir := t.TempDir()
	dbPath := dir + "/vault.db"
	if err := writeFileSafe(t, dbPath, []byte("DBCONTENT")); err != nil {
		t.Fatal(err)
	}
	adapter := newRecordingAdapter()

	if err := writeDBOnce(adapter, dbPath, "_vault/vault.db.latest.db", ""); err != nil {
		t.Fatalf("write: %v", err)
	}
	got, ok := adapter.writes["_vault/vault.db.latest.db"]
	if !ok {
		t.Fatalf("write did not record path; got %v", keys(adapter.writes))
	}
	if string(got) != "DBCONTENT" {
		t.Errorf("plaintext mismatch: got %q", got)
	}
}

func TestWriteDBOnceEncryptsWithPassphrase(t *testing.T) {
	dir := t.TempDir()
	dbPath := dir + "/vault.db"
	if err := writeFileSafe(t, dbPath, []byte("DBCONTENT")); err != nil {
		t.Fatal(err)
	}
	adapter := newRecordingAdapter()

	if err := writeDBOnce(adapter, dbPath, "_vault/vault.db.latest.age", "secret"); err != nil {
		t.Fatalf("write: %v", err)
	}
	got, ok := adapter.writes["_vault/vault.db.latest.age"]
	if !ok {
		t.Fatalf("write did not record path; got %v", keys(adapter.writes))
	}
	// Encrypted blob must differ from plaintext.
	if string(got) == "DBCONTENT" {
		t.Errorf("write went out plaintext when passphrase was set")
	}
	// age format starts with "age-encryption.org/v1" header.
	if !strings.HasPrefix(string(got), "age-encryption.org/v1") {
		head := got
		if len(head) > 50 {
			head = head[:50]
		}
		t.Errorf("encrypted output missing age header: %q", head)
	}
}

func writeFileSafe(t *testing.T, path string, content []byte) error {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(content)
	return err
}

func keys(m map[string][]byte) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
