package mcpserver

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/tinkerologie/vault/internal/runner"
)

// An agent must not approve its own privileged restore: approval keys in the
// tool input are ignored and the runner options have nowhere to carry them.
func TestRestoreItemInputCannotApprove(t *testing.T) {
	var in restoreItemInput
	if err := json.Unmarshal([]byte(`{"allow_host_privileges":true,"allow_unpinned_image":true,"clean_destination":false}`), &in); err != nil {
		t.Fatal(err)
	}
	opts := in.options()
	if opts.CleanDestination == nil || *opts.CleanDestination {
		t.Fatalf("clean_destination lost: %+v", opts)
	}
	for _, typ := range []reflect.Type{reflect.TypeOf(in), reflect.TypeOf(runner.RestoreItemOptions{})} {
		for i := range typ.NumField() {
			f := typ.Field(i)
			if f.Name == "AllowHostPrivileges" || f.Name == "AllowUnpinnedImage" {
				t.Errorf("%s has approval field %s", typ.Name(), f.Name)
			}
		}
	}
}
