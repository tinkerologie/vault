package health

import (
	"slices"
	"testing"
	"time"

	"github.com/tinkerologie/vault/internal/db"
)

func protectionItems(jobID int64, names ...string) []db.JobItem {
	items := make([]db.JobItem, 0, len(names))
	for _, name := range names {
		items = append(items, db.JobItem{JobID: jobID, ItemType: "container", ItemName: name})
	}
	return items
}

func protectionRP(at time.Time, metadata string) db.RestorePoint {
	return db.RestorePoint{CreatedAt: at, Metadata: metadata}
}

func TestProtectionClassifier(t *testing.T) {
	now := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)
	daily := db.Job{ID: 1, Enabled: true, Schedule: "0 2 * * *"}
	recent := now.Add(-12 * time.Hour)
	old := now.Add(-9 * 24 * time.Hour)

	cases := []struct {
		name          string
		jobs          []db.Job
		items         [][]db.JobItem
		rps           [][]db.RestorePoint
		wantBacked    []string
		wantOutOfDate []string
		wantPending   []string
	}{
		{
			name:        "recent restore point protects the items it holds",
			jobs:        []db.Job{daily},
			items:       [][]db.JobItem{protectionItems(1, "plex", "sonarr")},
			rps:         [][]db.RestorePoint{{protectionRP(recent, `{"item_sizes":{"plex":1}}`)}},
			wantBacked:  []string{"container:plex"},
			wantPending: []string{"container:sonarr"},
		},
		{
			name:          "an old restore point is out of date",
			jobs:          []db.Job{daily},
			items:         [][]db.JobItem{protectionItems(1, "plex")},
			rps:           [][]db.RestorePoint{{protectionRP(old, `{"item_sizes":{"plex":1}}`)}},
			wantBacked:    []string{"container:plex"},
			wantOutOfDate: []string{"container:plex"},
		},
		{
			name:  "newer runs that dropped an item leave it out of date",
			jobs:  []db.Job{daily},
			items: [][]db.JobItem{protectionItems(1, "plex", "sonarr")},
			rps: [][]db.RestorePoint{{
				protectionRP(recent, `{"item_sizes":{"plex":1}}`),
				protectionRP(old, `{"item_sizes":{"plex":1,"sonarr":1}}`),
			}},
			wantBacked:    []string{"container:plex", "container:sonarr"},
			wantOutOfDate: []string{"container:sonarr"},
		},
		{
			name:       "a legacy restore point covers every item in its job",
			jobs:       []db.Job{daily},
			items:      [][]db.JobItem{protectionItems(1, "plex", "sonarr")},
			rps:        [][]db.RestorePoint{{protectionRP(recent, "")}},
			wantBacked: []string{"container:plex", "container:sonarr"},
		},
		{
			name:  "one job with a recent backup is enough",
			jobs:  []db.Job{daily, {ID: 2, Enabled: true, Schedule: "0 3 * * *"}},
			items: [][]db.JobItem{protectionItems(1, "plex"), protectionItems(2, "plex")},
			rps: [][]db.RestorePoint{
				{protectionRP(old, `{"item_sizes":{"plex":1}}`)},
				{protectionRP(recent, `{"item_sizes":{"plex":1}}`)},
			},
			wantBacked: []string{"container:plex"},
		},
		{
			name:       "a replicated job's age says nothing",
			jobs:       []db.Job{{ID: 1, Enabled: true, Schedule: "0 2 * * *", SourceID: 7}},
			items:      [][]db.JobItem{protectionItems(1, "plex")},
			rps:        [][]db.RestorePoint{{protectionRP(old, `{"item_sizes":{"plex":1}}`)}},
			wantBacked: []string{"container:plex"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := NewProtectionClassifier(now)
			for i, job := range tc.jobs {
				c.AddJob(job, tc.items[i], tc.rps[i])
			}
			check := func(label string, got, want []string) {
				t.Helper()
				if want == nil {
					want = []string{}
				}
				if !slices.Equal(got, want) {
					t.Errorf("%s = %v, want %v", label, got, want)
				}
			}
			check("backed", c.backedKeys(), tc.wantBacked)
			check("out of date", c.outOfDateKeys(), tc.wantOutOfDate)
			check("pending", c.pendingKeys(), tc.wantPending)
		})
	}
}

func TestProtectionClassifier_NewestBackup(t *testing.T) {
	now := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)
	older, newer := now.Add(-48*time.Hour), now.Add(-time.Hour)
	c := NewProtectionClassifier(now)
	job := db.Job{ID: 1, Enabled: true, Schedule: "0 2 * * *"}
	c.AddJob(job, protectionItems(1, "plex"), []db.RestorePoint{protectionRP(older, "")})
	c.AddJob(db.Job{ID: 2}, protectionItems(2, "plex"), []db.RestorePoint{protectionRP(newer, "")})
	if at, ok := c.NewestBackup("container:plex"); !ok || !at.Equal(newer) {
		t.Errorf("newestBackup = %v, %v; want %v", at, ok, newer)
	}
	if _, ok := c.NewestBackup("container:none"); ok {
		t.Error("newestBackup reported a backup for an unknown item")
	}
}
