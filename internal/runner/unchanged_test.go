package runner

import (
	"testing"

	"github.com/ruaan-deysel/vault/internal/dedup"
	"github.com/ruaan-deysel/vault/internal/engine"
	"github.com/ruaan-deysel/vault/internal/storage"
)

// TestMarkUnchanged covers the run-log side of issue #326. The flag is
// additive on purpose: status must stay "ok" so nothing that reads it starts
// treating an idle container as a failure.
func TestMarkUnchanged(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		result *engine.BackupResult
		want   bool
	}{
		{
			name:   "engine reported nothing changed",
			result: &engine.BackupResult{Meta: map[string]any{engine.MetaUnchanged: true}},
			want:   true,
		},
		{
			name:   "engine reported content was captured",
			result: &engine.BackupResult{Meta: map[string]any{engine.MetaUnchanged: false}},
			want:   false,
		},
		{
			name:   "engine said nothing either way",
			result: &engine.BackupResult{Meta: map[string]any{"manifest_id": []byte{0x01}}},
			want:   false,
		},
		{
			name:   "handler set no metadata at all",
			result: &engine.BackupResult{},
			want:   false,
		},
		{
			// A non-boolean under the key is a handler bug, not a licence to
			// mislabel the item — treat it as changed.
			name:   "the flag is not a boolean",
			result: &engine.BackupResult{Meta: map[string]any{engine.MetaUnchanged: "yes"}},
			want:   false,
		},
		{
			// Items that failed before producing a result still get a run-log
			// entry, so nil must not panic.
			name:   "no result at all",
			result: nil,
			want:   false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			resEntry := map[string]any{"name": "plex", "status": "ok"}
			markUnchanged(resEntry, tc.result)
			got, set := resEntry["unchanged"]
			if tc.want {
				if !set || got != true {
					t.Errorf(`resEntry["unchanged"] = %v (set=%v), want true`, got, set)
				}
			} else if set {
				t.Errorf(`resEntry["unchanged"] = %v, want the key to be absent`, got)
			}
			if resEntry["status"] != "ok" {
				t.Errorf(`status = %v, want it left as "ok"`, resEntry["status"])
			}
		})
	}
}

// TestChunkedItemUnchanged runs against a real dedup repo: the flag is only
// trustworthy if the manifest can actually be read back, and the failure path
// (a manifest ID the repo does not hold) must degrade to "changed" rather than
// mislabel the item or fail the backup.
func TestChunkedItemUnchanged(t *testing.T) {
	t.Parallel()
	r, database, storageDir := setupTestRunner(t)
	r.serverKey = testServerKey()

	dest := makeDedupDest(t, database, storageDir)
	adapter, err := storage.NewAdapter(dest.Type, dest.Config)
	if err != nil {
		t.Fatalf("NewAdapter: %v", err)
	}
	defer storage.CloseAdapter(adapter)
	repo, err := dedup.InitRepo(database, adapter, dest.ID, r.serverKey)
	if err != nil {
		t.Fatalf("InitRepo: %v", err)
	}

	body := dedup.Manifest{Version: 1, Item: "plex", Files: map[string]dedup.ManifestEntry{
		"__vol__/config": {Chunks: []dedup.ID{{0x01}}},
	}}
	id, err := repo.PutManifest("plex", body)
	if err != nil {
		t.Fatalf("PutManifest: %v", err)
	}
	if err := repo.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	t.Run("the written manifest matches its parent", func(t *testing.T) {
		if !chunkedItemUnchanged(repo, id, &body, "plex") {
			t.Error("a manifest identical to its parent reported as changed")
		}
	})

	t.Run("the written manifest differs from its parent", func(t *testing.T) {
		other := dedup.Manifest{Version: 1, Item: "plex", Files: map[string]dedup.ManifestEntry{
			"__vol__/config": {Chunks: []dedup.ID{{0x02}}},
		}}
		if chunkedItemUnchanged(repo, id, &other, "plex") {
			t.Error("a manifest with a different chunk reported as unchanged")
		}
	})

	t.Run("a full backup has no parent to compare against", func(t *testing.T) {
		if chunkedItemUnchanged(repo, id, nil, "plex") {
			t.Error("an item with no parent reported as unchanged")
		}
	})

	t.Run("the manifest cannot be read back", func(t *testing.T) {
		var missing dedup.ID
		missing[0] = 0xff
		if chunkedItemUnchanged(repo, missing, &body, "plex") {
			t.Error("an unreadable manifest must report as changed, not unchanged")
		}
	})

	t.Run("no repo at all", func(t *testing.T) {
		if chunkedItemUnchanged(nil, id, &body, "plex") {
			t.Error("a nil repo must report as changed")
		}
	})
}
