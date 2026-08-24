package engine

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ruaan-deysel/vault/internal/dedup"
)

// TestContainerBackup_ProgressMilestones: container backups narrate every
// phase through the progress callback — these are the messages the runner
// mirrors into the run log (#328 QA). Table-driven over the classic and
// chunked (dedup) paths, which share the narration contract (#328 r3 #13).
func TestContainerBackup_ProgressMilestones(t *testing.T) {
	cases := []struct {
		name string
		want []string
		run  func(t *testing.T, h *ContainerHandler, progress func(string, int, string)) error
	}{
		{
			name: "classic",
			want: []string{"inspecting container", "saving image", "backing up volumes", "backup complete"},
			run: func(t *testing.T, h *ContainerHandler, progress func(string, int, string)) error {
				item := BackupItem{Name: "test", Type: "container", Settings: map[string]any{"id": "abc123"}}
				_, err := h.Backup(context.Background(), item, t.TempDir(), progress)
				return err
			},
		},
		{
			name: "chunked",
			want: []string{"inspecting container", "backing up volumes", "manifest written"},
			run: func(t *testing.T, h *ContainerHandler, progress func(string, int, string)) error {
				r, _, cleanup := dedup.NewTestRepoForEngine(t)
				defer cleanup()
				item := BackupItem{Name: "test", Type: "container", Settings: map[string]any{"id": "abc123"}}
				if _, err := h.BackupChunked(context.Background(), item, r, nil, progress); err != nil {
					return err
				}
				return r.Flush()
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			volSrc := t.TempDir()
			if err := os.WriteFile(filepath.Join(volSrc, "data.txt"), []byte("hello"), 0o644); err != nil {
				t.Fatal(err)
			}
			mock := newClassicMock(t, false, volSrc, time.Now().Add(-48*time.Hour).UTC().Format(time.RFC3339Nano))
			h := &ContainerHandler{cli: mock}

			var msgs []string
			progress := func(_ string, _ int, msg string) { msgs = append(msgs, msg) }

			if err := tc.run(t, h, progress); err != nil {
				t.Fatalf("backup: %v", err)
			}

			joined := strings.Join(msgs, "\n")
			for _, want := range tc.want {
				if !strings.Contains(joined, want) {
					t.Errorf("%s backup milestone missing %q; got:\n%s", tc.name, want, joined)
				}
			}
		})
	}
}
