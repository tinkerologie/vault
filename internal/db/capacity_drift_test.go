//go:build capacity_drift_check

package db

import (
	"reflect"
	"sort"
	"testing"

	"github.com/ruaan-deysel/vault/internal/storage"
)

// TestCapacityRecordMirrorsStorageCapacity is a TEMPORARY drift guard.
//
// CapacityRecord exists in this package as a flat mirror of
// storage.Capacity solely to break the build-time import cycle while
// Tasks 2-7 of the storage-capacity feature land (every adapter
// currently fails to compile because GetCapacity isn't implemented yet,
// which would cascade to internal/db if we imported internal/storage
// directly). Once those tasks ship and internal/storage compiles
// reliably, we can replace CapacityRecord with storage.Capacity directly
// and delete this test.
//
// Until then, this test asserts the two structs have identical field
// sets so a field added to one is never silently dropped by the other.
//
// Build tag rationale: this file imports internal/storage, which does
// not compile while the adapters are missing GetCapacity. Including the
// test by default would poison the whole internal/db test binary build.
// Run manually with:
//   go test -tags=capacity_drift_check ./internal/db/...
// Before Tasks 8-9 land, AND after every change to storage.Capacity or
// db.CapacityRecord. Drop the build tag once internal/storage compiles
// cleanly (i.e. after Task 7), then delete this file once internal/db
// imports internal/storage directly.
func TestCapacityRecordMirrorsStorageCapacity(t *testing.T) {
	t.Parallel()
	fields := func(v any) []string {
		rt := reflect.TypeOf(v)
		out := make([]string, rt.NumField())
		for i := 0; i < rt.NumField(); i++ {
			f := rt.Field(i)
			out[i] = f.Name + " " + f.Type.String()
		}
		sort.Strings(out)
		return out
	}
	got := fields(CapacityRecord{})
	want := fields(storage.Capacity{})
	if !reflect.DeepEqual(got, want) {
		t.Errorf("CapacityRecord drifted from storage.Capacity:\n got:  %v\n want: %v", got, want)
	}
}
