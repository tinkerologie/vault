package runner

import (
	"fmt"
	"io/fs"
	"testing"

	"github.com/ruaan-deysel/vault/internal/storage"
)

// notFoundListAdapter is a storage.Adapter whose List reports a missing path.
// Only List is exercised by DeleteStorageDir's not-found short-circuit, so the
// embedded nil Adapter (which would panic on any other call) is never reached.
type notFoundListAdapter struct{ storage.Adapter }

func (notFoundListAdapter) List(string) ([]storage.FileInfo, error) {
	return nil, fmt.Errorf("webdav: list ghost: %w", fs.ErrNotExist)
}

// TestDeleteStorageDirNotFoundIsSuccess ensures that cleaning up a directory
// that no longer exists on storage is treated as success rather than a hard
// failure. Previously a 404/not-found from List aborted cleanup with
// "files may remain on storage" (issue #143).
func TestDeleteStorageDirNotFoundIsSuccess(t *testing.T) {
	t.Parallel()
	r := &Runner{}
	if err := r.DeleteStorageDir(notFoundListAdapter{}, "ghost/path"); err != nil {
		t.Errorf("DeleteStorageDir on missing dir = %v, want nil", err)
	}
}
