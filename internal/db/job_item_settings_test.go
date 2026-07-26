package db

import (
	"path/filepath"
	"testing"
)

func newSettingsTestDB(t *testing.T) *DB {
	t.Helper()
	d, err := Open(filepath.Join(t.TempDir(), "vault.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

// TestAddJobItemNormalisesBlankSettings covers the write boundary. The column
// declares DEFAULT '{}', but that only applies when the column is omitted —
// a caller that simply left settings unset passes "" explicitly, which stored
// an empty string and made every later read log a malformed-JSON warning.
func TestAddJobItemNormalisesBlankSettings(t *testing.T) {
	d := newSettingsTestDB(t)
	jobID, err := d.CreateJob(Job{Name: "j", Schedule: "0 2 * * *"})
	if err != nil {
		t.Fatal(err)
	}

	for _, blank := range []string{"", "   ", "\n"} {
		id, err := d.AddJobItem(JobItem{JobID: jobID, ItemType: "vm", ItemName: "Fedora", ItemID: "Fedora", Settings: blank})
		if err != nil {
			t.Fatal(err)
		}
		items, err := d.GetJobItems(jobID)
		if err != nil {
			t.Fatal(err)
		}
		var stored string
		for _, it := range items {
			if it.ID == id {
				stored = it.Settings
			}
		}
		if stored != "{}" {
			t.Fatalf("settings %q stored as %q, want \"{}\"", blank, stored)
		}
	}
}

func TestAddJobItemPreservesRealSettings(t *testing.T) {
	d := newSettingsTestDB(t)
	jobID, err := d.CreateJob(Job{Name: "j", Schedule: "0 2 * * *"})
	if err != nil {
		t.Fatal(err)
	}
	const want = `{"uuid":"abc","state":"running"}`
	id, err := d.AddJobItem(JobItem{JobID: jobID, ItemType: "vm", ItemName: "Fedora", ItemID: "Fedora", Settings: want})
	if err != nil {
		t.Fatal(err)
	}
	items, _ := d.GetJobItems(jobID)
	for _, it := range items {
		if it.ID == id && it.Settings != want {
			t.Fatalf("settings mangled: got %q, want %q", it.Settings, want)
		}
	}
}

// TestParsedSettings covers the read boundary, including rows written before
// normalisation existed.
func TestParsedSettings(t *testing.T) {
	cases := []struct {
		name    string
		raw     string
		wantErr bool
		wantLen int
	}{
		{"blank is not corruption", "", false, 0},
		{"whitespace is not corruption", "   ", false, 0},
		{"empty object", "{}", false, 0},
		{"literal null yields a usable map", "null", false, 0},
		{"real settings", `{"uuid":"abc","state":"running"}`, false, 2},
		{"genuine corruption still errors", `{"uuid":`, true, 0},
		{"wrong shape still errors", `["a"]`, true, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := JobItem{Settings: tc.raw}.ParsedSettings()
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr = %v", err, tc.wantErr)
			}
			if tc.wantErr {
				return
			}
			if got == nil {
				t.Fatal("returned a nil map; callers index into it")
			}
			if len(got) != tc.wantLen {
				t.Fatalf("len = %d, want %d", len(got), tc.wantLen)
			}
		})
	}
}
