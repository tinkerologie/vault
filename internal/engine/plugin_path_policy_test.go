//go:build linux

package engine

import (
	"strings"
	"testing"

	"github.com/tinkerologie/vault/internal/dedup"
)

// Issue #545: plugin names and the path setting must not reach arbitrary trees.
func TestPluginPathPolicy(t *testing.T) {
	t.Parallel()
	h := &PluginHandler{}
	for _, name := range []string{"../../etc", "a/b", ".."} {
		if _, err := h.BackupChunked(t.Context(), BackupItem{Name: name, Settings: map[string]any{}}, nil, nil, noopProgress); err == nil || !strings.Contains(err.Error(), "invalid plugin name") {
			t.Errorf("name %q: err = %v", name, err)
		}
	}
	if err := h.RestoreChunked(t.Context(), BackupItem{Name: "../../etc", Settings: map[string]any{}}, nil, dedup.ID{}, "", noopProgress); err == nil || !strings.Contains(err.Error(), "invalid plugin name") {
		t.Errorf("restore traversal name: err = %v", err)
	}
	item := BackupItem{Name: "ok", Settings: map[string]any{"path": "/etc/ssh"}}
	if _, err := h.BackupChunked(t.Context(), item, nil, nil, noopProgress); err == nil {
		t.Error("path setting in a system tree allowed")
	}
	if err := h.RestoreChunked(t.Context(), BackupItem{Name: "ok", Settings: map[string]any{"restore_destination": "/etc/x"}}, nil, dedup.ID{}, "/etc/x", noopProgress); err == nil {
		t.Error("custom destination in a system tree allowed")
	}
	if err := h.RestoreChunked(t.Context(), BackupItem{Name: "ok", Settings: map[string]any{}}, nil, dedup.ID{}, "/etc/x", noopProgress); err == nil {
		t.Error("destination in a system tree allowed")
	}
}
