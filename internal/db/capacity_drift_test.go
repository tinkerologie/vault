package db

import (
	"reflect"
	"sort"
	"testing"

	"github.com/ruaan-deysel/vault/internal/storage"
)

// TestCapacityRecordMirrorsStorageCapacity is a drift guard between
// storage.Capacity and db.CapacityRecord.
//
// db.CapacityRecord exists as a flat mirror of storage.Capacity solely
// to prevent internal/db from importing internal/storage. The mirror
// will become unnecessary once we are confident no future change will
// re-introduce a partial-adapter-compile state (e.g. adding another
// adapter via stubs-first). Until then, this test asserts the two
// structs have identical field sets so a field added to one is never
// silently dropped by the other.
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
