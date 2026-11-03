package engine

import (
	"strings"
	"testing"
)

func TestZFSRestoreTarget(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name     string
		settings map[string]any
		want     string
		wantErr  string
	}{
		{name: "nothing authorised", wantErr: "no target dataset"},
		{name: "configured dataset", settings: map[string]any{"dataset": "tank/data"}, want: "tank/data"},
		{name: "chosen destination wins", settings: map[string]any{"dataset": "tank/a", "restore_destination": "tank/b"}, want: "tank/b"},
		{name: "explicit pool root", settings: map[string]any{"restore_destination": "tank"}, want: "tank"},
		{name: "implicit pool root", settings: map[string]any{"dataset": "tank"}, wantErr: "pool root"},
		{name: "option injection", settings: map[string]any{"dataset": "-o/x"}, wantErr: "invalid"},
		{name: "snapshot", settings: map[string]any{"dataset": "tank/a@s"}, wantErr: "invalid"},
		{name: "path", settings: map[string]any{"restore_destination": "/mnt/user/x"}, wantErr: "invalid"},
		{name: "dots", settings: map[string]any{"dataset": "tank/../x"}, wantErr: "invalid"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := zfsRestoreTarget(tt.settings)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil || got != tt.want {
				t.Fatalf("got %q, %v, want %q", got, err, tt.want)
			}
		})
	}
}
