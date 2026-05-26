//go:build !linux

package cli

import (
	"testing"

	"github.com/ruaan-deysel/vault/internal/engine"
)

// TestZFSBrowseAdapter_StubReturnsEmpty wraps the platform stub
// engine.ZFSHandler (empty struct on non-Linux) and confirms the adapter
// propagates the empty list without error.
//
// Only meaningful when compiled with the !linux build tag — on Linux the
// real engine.ZFSHandler requires a non-nil zfs client and would panic
// on a zero-value handler. The equivalent Linux coverage would need a
// real ZFS environment and lives outside unit tests.
func TestZFSBrowseAdapter_StubReturnsEmpty(t *testing.T) {
	t.Parallel()
	// engine.ZFSHandler is an empty struct on non-Linux; we can construct
	// it directly to bypass NewZFSHandler's "not supported" error.
	adapter := &zfsBrowseAdapter{handler: &engine.ZFSHandler{}}
	got, err := adapter.ListZFSMountpoints()
	if err != nil {
		t.Fatalf("ListZFSMountpoints err = %v, want nil", err)
	}
	if len(got) != 0 {
		t.Errorf("expected empty slice on stub, got %d entries", len(got))
	}
}
