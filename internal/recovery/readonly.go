package recovery

import (
	"context"
	"errors"
	"io"

	"github.com/ruaan-deysel/vault/internal/storage"
)

// ErrReadOnly is returned by every mutating call on the recovery adapter.
// Recovery must never change the backup storage it reads from.
var ErrReadOnly = errors.New("recovery: backup storage is opened read-only")

// readOnlyAdapter forwards reads to a storage adapter and refuses writes and
// deletes, so no recovery code path — including shared runner and dedup code
// — can modify the backups being recovered.
type readOnlyAdapter struct {
	inner storage.Adapter
}

var _ storage.Adapter = readOnlyAdapter{}

func (a readOnlyAdapter) Write(string, io.Reader) error { return ErrReadOnly }

func (a readOnlyAdapter) WriteFrom(string, func() (io.ReadCloser, error)) error {
	return ErrReadOnly
}

func (a readOnlyAdapter) Delete(string) error { return ErrReadOnly }

func (a readOnlyAdapter) Read(p string) (io.ReadCloser, error) { return a.inner.Read(p) }

func (a readOnlyAdapter) ReadRange(p string, offset, length int64) (io.ReadCloser, error) {
	return a.inner.ReadRange(p, offset, length)
}

func (a readOnlyAdapter) List(prefix string) ([]storage.FileInfo, error) {
	return a.inner.List(prefix)
}

func (a readOnlyAdapter) Stat(p string) (storage.FileInfo, error) { return a.inner.Stat(p) }

func (a readOnlyAdapter) TestConnection() error { return a.inner.TestConnection() }

func (a readOnlyAdapter) GetCapacity(ctx context.Context) (storage.Capacity, error) {
	return a.inner.GetCapacity(ctx)
}

// Close releases the wrapped adapter's connections, if it holds any.
func (a readOnlyAdapter) Close() error {
	storage.CloseAdapter(a.inner)
	return nil
}
