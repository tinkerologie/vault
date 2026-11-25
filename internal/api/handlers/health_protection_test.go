package handlers

import (
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/tinkerologie/vault/internal/db"
	"github.com/tinkerologie/vault/internal/health"
)

// seedProtectedItem adds an item to the job and a Restore Point of the given
// age holding it.
func seedProtectedItem(t *testing.T, d *db.DB, jobID int64, name string, age time.Duration) {
	t.Helper()
	if _, err := d.AddJobItem(db.JobItem{JobID: jobID, ItemType: "container", ItemName: name}); err != nil {
		t.Fatalf("add item: %v", err)
	}
	runID, err := d.CreateJobRun(db.JobRun{JobID: jobID, Status: "success", BackupType: "full"})
	if err != nil {
		t.Fatalf("create run: %v", err)
	}
	rpID, err := d.CreateRestorePoint(db.RestorePoint{
		JobRunID: runID, JobID: jobID, BackupType: "full", StoragePath: name,
		Metadata: `{"item_sizes":{"` + name + `":1}}`,
	})
	if err != nil {
		t.Fatalf("create restore point: %v", err)
	}
	if _, err := d.Exec(`UPDATE restore_points SET created_at = ? WHERE id = ?`, time.Now().UTC().Add(-age), rpID); err != nil {
		t.Fatalf("age restore point: %v", err)
	}
}

func stringList(t *testing.T, resp map[string]any, key string) []string {
	t.Helper()
	raw, ok := resp[key].([]any)
	if !ok {
		t.Fatalf("%s = %v, want a list", key, resp[key])
	}
	out := make([]string, 0, len(raw))
	for _, v := range raw {
		out = append(out, v.(string))
	}
	return out
}

func TestHealthSummary_OutOfDateItems(t *testing.T) {
	d := newTestDB(t)
	jobID := seedHealthJob(t, d, db.Job{Name: "daily", Enabled: true, Schedule: "0 2 * * *"})
	seedProtectedItem(t, d, jobID, "fresh", time.Hour)
	seedProtectedItem(t, d, jobID, "stale", 9*healthDay)
	if _, err := d.AddJobItem(db.JobItem{JobID: jobID, ItemType: "container", ItemName: "new"}); err != nil {
		t.Fatalf("add item: %v", err)
	}

	resp := fetchHealthSummary(t, d)
	assertFloat(t, resp, "total_items", 3)
	// protected_items keeps its meaning: in a retained Restore Point.
	assertFloat(t, resp, "protected_items", 2)
	assertFloat(t, resp, "up_to_date_items", 1)
	assertFloat(t, resp, "out_of_date_items", 1)
	assertFloat(t, resp, "pending_items", 1)
	if got := stringList(t, resp, "out_of_date_keys"); !slices.Equal(got, []string{"container:stale"}) {
		t.Errorf("out_of_date_keys = %v", got)
	}
	if got := stringList(t, resp, "protected_keys"); !slices.Equal(got, []string{"container:fresh", "container:stale"}) {
		t.Errorf("protected_keys = %v", got)
	}
	if got := stringList(t, resp, "pending_keys"); !slices.Equal(got, []string{"container:new"}) {
		t.Errorf("pending_keys = %v", got)
	}
	last, ok := resp["item_last_backup_at"].(map[string]any)
	if !ok || len(last) != 2 || last["container:stale"] == nil || last["container:new"] != nil {
		t.Errorf("item_last_backup_at = %v, want fresh and stale only", resp["item_last_backup_at"])
	}
}

func TestHealthSummary_OutOfDateItemsLowerTheScore(t *testing.T) {
	d := newTestDB(t)
	jobID := seedHealthJob(t, d, db.Job{Name: "daily", Enabled: true, Schedule: "0 2 * * *"})
	seedProtectedItem(t, d, jobID, "stale", 9*healthDay)

	resp := fetchHealthSummary(t, d)
	assertFloat(t, resp, "protection_pct", 100)
	// One successful run (100% of 60) and no up-to-date item (0% of 40).
	assertFloat(t, resp, "health_score", 60)
}

func TestHealthSummary_EmptyListsAreNotNull(t *testing.T) {
	resp := fetchHealthSummary(t, newTestDB(t))
	for _, key := range []string{"out_of_date_keys", "protected_keys", "pending_keys"} {
		if got := stringList(t, resp, key); len(got) != 0 {
			t.Errorf("%s = %v, want empty", key, got)
		}
	}
	if resp["server_key_at_risk"] != false {
		t.Errorf("server_key_at_risk = %v, want false", resp["server_key_at_risk"])
	}
}

// A backup passphrase protects deduplicated backups only once each destination
// in use has a confirmed escrow for that passphrase (#714, #451).
func TestHealthSummary_ServerKeyAtRiskFollowsEscrow(t *testing.T) {
	d := newTestDB(t)
	destID, err := d.CreateStorageDestination(db.StorageDestination{Name: "dest", Type: "local", Config: `{"path":"/tmp"}`})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(`UPDATE storage_destinations SET dedup_enabled = 1 WHERE id = ?`, destID); err != nil {
		t.Fatal(err)
	}
	if _, err := d.CreateJob(db.Job{Name: "job", Enabled: true, StorageDestID: destID, BackupTypeChain: "full"}); err != nil {
		t.Fatal(err)
	}
	if err := d.SetSetting("encryption_passphrase_hash", "current"); err != nil {
		t.Fatal(err)
	}
	key := db.DedupEscrowConfirmedPrefix + strconv.FormatInt(destID, 10)
	for _, tc := range []struct {
		name, recorded string
		want           bool
	}{
		{"no escrow confirmed yet", "", true},
		{"escrow confirmed for an older passphrase", "previous", true},
		{"escrow confirmed for this passphrase", "current", false},
	} {
		if err := d.SetSetting(key, tc.recorded); err != nil {
			t.Fatal(err)
		}
		if got := fetchHealthSummary(t, d)["server_key_at_risk"]; got != tc.want {
			t.Errorf("%s: server_key_at_risk = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestHealthSummary_ServerKeyAtRisk(t *testing.T) {
	cases := []struct {
		name       string
		dedup      bool
		passphrase bool
		saved      string // server_key_saved_at value; "" = none
		current    string // fingerprint of the key the daemon loaded; "" = unknown
		jobUsesIt  bool
		want       bool
	}{
		{"dedup in use, no passphrase, no saved copy", true, false, "", "aaaa", true, true},
		{"a passphrase whose escrow is not written yet", true, true, "", "aaaa", true, true},
		{"a copy of this key was saved", true, false, "2026-11-25T10:00:00Z aaaa", "aaaa", true, false},
		{"the saved copy is of an older key", true, false, "2026-11-25T10:00:00Z bbbb", "aaaa", true, true},
		{"a saved copy with no fingerprint while the key is known", true, false, "2026-11-25T10:00:00Z", "aaaa", true, true},
		{"a value that is not a timestamp", true, false, "yes", "", true, true},
		{"legacy record before fingerprints, key unknown", true, false, "2026-11-25T10:00:00Z", "", true, false},
		{"no deduplicated destination", false, false, "", "aaaa", true, false},
		{"dedup destination no job uses", true, false, "", "aaaa", false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := newTestDB(t)
			destID, err := d.CreateStorageDestination(db.StorageDestination{Name: "dest", Type: "local", Config: `{"path":"/tmp"}`, DedupEnabled: tc.dedup})
			if err != nil {
				t.Fatalf("create dest: %v", err)
			}
			if tc.dedup {
				if _, err := d.Exec(`UPDATE storage_destinations SET dedup_enabled = 1 WHERE id = ?`, destID); err != nil {
					t.Fatalf("enable dedup: %v", err)
				}
			}
			if tc.jobUsesIt {
				if _, err := d.CreateJob(db.Job{Name: "job", Enabled: true, StorageDestID: destID, BackupTypeChain: "full"}); err != nil {
					t.Fatalf("create job: %v", err)
				}
			}
			if tc.passphrase {
				if err := d.SetSetting("encryption_passphrase_hash", "hash"); err != nil {
					t.Fatal(err)
				}
			}
			if tc.saved != "" {
				if err := d.SetSetting(health.ServerKeySavedSetting, tc.saved); err != nil {
					t.Fatal(err)
				}
			}
			if tc.current != "" {
				if err := d.SetSetting(db.ServerKeyFingerprintSetting, tc.current); err != nil {
					t.Fatal(err)
				}
			}
			if got := fetchHealthSummary(t, d)["server_key_at_risk"]; got != tc.want {
				t.Errorf("server_key_at_risk = %v, want %v", got, tc.want)
			}
		})
	}
}
