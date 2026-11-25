package db

import (
	"reflect"
	"testing"
)

// A Restore Point names the type of each item it holds, so an item that has
// since left its job can still be browsed and restored (#669).
func TestRestorePointRecordedItemTypes(t *testing.T) {
	tests := []struct {
		name     string
		metadata string
		want     map[string]string
	}{
		{"item_types", `{"item_types":{"plex":"container","media":"folder"}}`, map[string]string{"plex": "container", "media": "folder"}},
		{"ambiguous name is left out", `{"item_types":{"plex":""}}`, map[string]string{}},
		{"vm checkpoints imply a VM", `{"vm_checkpoints":{"win11":"cp1"}}`, map[string]string{"win11": "vm"}},
		{"manifest items", `{"manifest_items":[{"name":"plex","type":"container"},{"name":"x","type":""}]}`, map[string]string{"plex": "container"}},
		{"item_types wins over other evidence", `{"item_types":{"a":"folder"},"manifest_items":[{"name":"a","type":"container"}]}`, map[string]string{"a": "folder"}},
		{"no metadata", ``, map[string]string{}},
		{"malformed metadata", `{`, map[string]string{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := RestorePoint{Metadata: tt.metadata}.RecordedItemTypes()
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("RecordedItemTypes() = %v, want %v", got, tt.want)
			}
		})
	}
}
