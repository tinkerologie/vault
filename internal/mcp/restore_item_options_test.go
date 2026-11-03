package mcpserver

import (
	"encoding/json"
	"testing"
)

func TestRestoreItemInputCarriesApprovals(t *testing.T) {
	var in restoreItemInput
	if err := json.Unmarshal([]byte(`{"allow_host_privileges":true,"allow_unpinned_image":true,"clean_destination":false}`), &in); err != nil {
		t.Fatal(err)
	}
	opts := in.options()
	if !opts.AllowHostPrivileges || !opts.AllowUnpinnedImage || opts.CleanDestination == nil || *opts.CleanDestination {
		t.Fatalf("options = %+v", opts)
	}
	if def := (restoreItemInput{}).options(); def.AllowHostPrivileges || def.AllowUnpinnedImage {
		t.Fatalf("defaults approve: %+v", def)
	}
}
