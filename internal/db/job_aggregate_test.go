package db

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

type jobSnapshot struct {
	job   Job
	items []JobItem
}

func readJobSnapshot(t *testing.T, d *DB, id int64) jobSnapshot {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	job, err := getJob(ctx, d, id)
	if err != nil {
		t.Fatal(err)
	}
	items, err := getJobItems(ctx, d, id)
	if err != nil {
		t.Fatal(err)
	}
	return jobSnapshot{job, items}
}

func aggregateItem(name string) JobItem {
	return JobItem{ItemName: name, ItemID: name, ItemType: "container", Settings: `{"included":true}`}
}

func TestJobAggregateRollsBack(t *testing.T) {
	for _, tt := range []struct{ name, trigger, wantError string }{
		{"insert", `CREATE TRIGGER reject_item BEFORE INSERT ON job_items
			WHEN NEW.item_name = 'fail' BEGIN SELECT RAISE(ABORT, 'item insert failed'); END`, "item insert failed"},
		{"delete", `CREATE TRIGGER reject_item BEFORE DELETE ON job_items
			BEGIN SELECT RAISE(ABORT, 'item delete failed'); END`, "item delete failed"},
		{"job read back", `CREATE TRIGGER break_readback AFTER INSERT ON job_items
			BEGIN UPDATE jobs SET description = NULL WHERE id = NEW.job_id; END`, "read saved job:"},
		{"item read back", `CREATE TRIGGER break_readback AFTER INSERT ON job_items
			BEGIN UPDATE job_items SET settings = NULL WHERE id = NEW.id; END`, "read saved job items:"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			d := setupTestDB(t)
			dest, err := d.CreateStorageDestination(StorageDestination{Name: "old", Type: "local", Config: "{}"})
			if err != nil {
				t.Fatal(err)
			}
			job, items, err := d.CreateJobWithItems(t.Context(), Job{Name: "before", Schedule: "@daily", StorageDestID: dest}, []JobItem{aggregateItem("keep"), aggregateItem("also keep")}, nil)
			if err != nil {
				t.Fatal(err)
			}
			if err := d.MarkJobItemsMissing([]int64{items[0].ID}, "2026-11-01 01:00:00"); err != nil {
				t.Fatal(err)
			}
			before := readJobSnapshot(t, d, job.ID)
			if _, err := d.Exec(tt.trigger); err != nil {
				t.Fatal(err)
			}
			update := job
			update.Name, update.Schedule, update.RetentionCount, update.StorageDestID = "after", "@weekly", 5, 0
			if _, _, err := d.UpdateJobWithItems(t.Context(), job.ID, update, []JobItem{aggregateItem("new"), aggregateItem("fail")}, nil); err == nil {
				t.Fatal("expected item write error")
			}
			if after := readJobSnapshot(t, d, job.ID); !reflect.DeepEqual(after, before) {
				t.Fatalf("failed mutation changed snapshot: %+v", after)
			}
		})
	}
}

func TestFailedAggregateCreateLeavesNoRows(t *testing.T) {
	d := setupTestDB(t)
	if _, err := d.Exec(`CREATE TRIGGER reject_item BEFORE INSERT ON job_items
		WHEN NEW.item_name = 'fail' BEGIN SELECT RAISE(ABORT, 'item insert failed'); END`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := d.CreateJobWithItems(t.Context(), Job{Name: "new"}, []JobItem{aggregateItem("first"), aggregateItem("fail")}, nil); err == nil || !strings.Contains(err.Error(), "item insert failed") {
		t.Fatalf("expected injected create failure, got %v", err)
	}
	jobs, err := d.ListJobs()
	if err != nil || len(jobs) != 0 {
		t.Fatalf("jobs after rollback = %v, err = %v", jobs, err)
	}
	items, err := d.ListJobItems(t.Context())
	if err != nil || len(items) != 0 {
		t.Fatalf("items after rollback = %v, err = %v", items, err)
	}
}

func TestJobAggregateCheckAndNotFound(t *testing.T) {
	d := setupTestDB(t)
	if _, _, err := d.UpdateJobWithItems(t.Context(), 99, Job{Name: "missing"}, nil, nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("update missing job = %v", err)
	}
	dest, err := d.CreateStorageDestination(StorageDestination{Name: "test", Type: "local", Config: `{"path":"/mnt/test"}`})
	if err != nil {
		t.Fatal(err)
	}
	job, items, err := d.CreateJobWithItems(t.Context(), Job{Name: "existing", StorageDestID: dest}, []JobItem{aggregateItem("keep")}, nil)
	if err != nil {
		t.Fatal(err)
	}
	wantErr := errors.New("policy refused mutation")
	if _, _, err := d.UpdateJobWithItems(t.Context(), job.ID, Job{Name: "edited", StorageDestID: dest}, nil, func(state JobMutationState) error {
		if state.Current == nil || !reflect.DeepEqual(*state.Current, job) || !reflect.DeepEqual(state.Items, items) ||
			!state.DestinationFound || state.Destination.ID != dest || state.Destination.Config != `{"path":"/mnt/test"}` {
			t.Errorf("unexpected policy state: %+v", state)
		}
		return wantErr
	}); err != wantErr {
		t.Fatalf("policy error = %v, want unchanged error", err)
	}
	if got := readJobSnapshot(t, d, job.ID); !reflect.DeepEqual(got, jobSnapshot{job, items}) {
		t.Fatalf("policy failure changed snapshot: %+v", got)
	}
}

// Pause a real transaction to check which rows a WAL reader sees.
func TestJobAggregateVisibility(t *testing.T) {
	for _, tt := range []struct {
		name  string
		items []JobItem
	}{
		{"preserve", nil},
		{"clear", []JobItem{}},
		{"replace", []JobItem{{ItemName: "replacement", ItemID: "new", ItemType: "container", JobID: 999}}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "vault.db")
			writer, err := Open(path)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = writer.Close() })
			reader, err := Open(path)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = reader.Close() })
			job, items, err := writer.CreateJobWithItems(t.Context(), Job{Name: "original"}, []JobItem{aggregateItem("one"), aggregateItem("two")}, nil)
			if err != nil {
				t.Fatal(err)
			}

			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			entered, release, finished := make(chan struct{}), make(chan struct{}), make(chan struct{})
			var releaseOnce sync.Once
			resume := func() { releaseOnce.Do(func() { close(release) }) }
			writer.beforeJobAggregateCommit = func() {
				close(entered)
				<-release
			}
			defer func() {
				resume()
				cancel()
				select {
				case <-finished:
				case <-time.After(2 * time.Second):
					t.Error("writer did not stop after cancellation")
				}
			}()
			var saved jobSnapshot
			var writeErr error
			go func() {
				defer close(finished)
				saved.job, saved.items, writeErr = writer.UpdateJobWithItems(ctx, job.ID, Job{Name: "edited"}, tt.items, nil)
			}()
			select {
			case <-entered:
			case <-finished:
				t.Fatalf("writer stopped before commit: %v", writeErr)
			case <-ctx.Done():
				t.Fatal("writer did not reach commit")
			}

			if got := readJobSnapshot(t, reader, job.ID); !reflect.DeepEqual(got, jobSnapshot{job, items}) {
				t.Fatalf("reader saw an uncommitted change: %+v", got)
			}
			resume()
			select {
			case <-finished:
			case <-ctx.Done():
				t.Fatal("writer did not finish after release")
			}
			if writeErr != nil {
				t.Fatal(writeErr)
			}
			if got := readJobSnapshot(t, reader, job.ID); !reflect.DeepEqual(got, saved) {
				t.Fatalf("response differs from committed snapshot: %+v", got)
			}
			if saved.job.Name != "edited" {
				t.Fatalf("saved job = %+v", saved.job)
			}
			switch tt.name {
			case "preserve":
				if !reflect.DeepEqual(saved.items, items) {
					t.Fatalf("omitted items changed selection: %+v", saved.items)
				}
			case "clear":
				if saved.items != nil {
					t.Fatalf("empty request should clear the selection: %+v", saved.items)
				}
			case "replace":
				if len(saved.items) != 1 || saved.items[0].JobID != job.ID || saved.items[0].ItemName != "replacement" || saved.items[0].Settings != "{}" {
					t.Fatalf("unexpected replacement: %+v", saved.items)
				}
			}
		})
	}
}

func TestJobAggregateRollsBackOnCommitFailure(t *testing.T) {
	d := setupTestDB(t)
	job, items, err := d.CreateJobWithItems(t.Context(), Job{Name: "original"}, []JobItem{aggregateItem("keep")}, nil)
	if err != nil {
		t.Fatal(err)
	}
	// An invalid deferred foreign key accepts the item write and fails at
	// commit, so this exercises SQLite's real commit and rollback behavior.
	if _, err := d.Exec(`CREATE TABLE commit_failure (
		job_id INTEGER REFERENCES jobs(id) DEFERRABLE INITIALLY DEFERRED);
		CREATE TRIGGER fail_job_commit AFTER INSERT ON job_items
		BEGIN INSERT INTO commit_failure (job_id) VALUES (-1); END;`); err != nil {
		t.Fatal(err)
	}
	got, gotItems, err := d.UpdateJobWithItems(t.Context(), job.ID, Job{Name: "edited"}, []JobItem{aggregateItem("new")}, nil)
	if err == nil || !strings.Contains(err.Error(), "commit job mutation") || !strings.Contains(err.Error(), "FOREIGN KEY constraint failed") {
		t.Fatalf("expected deferred foreign key failure at commit, got %v", err)
	}
	if !reflect.DeepEqual(got, Job{}) || gotItems != nil {
		t.Fatalf("failed commit returned records: %+v, %+v", got, gotItems)
	}
	if after := readJobSnapshot(t, d, job.ID); !reflect.DeepEqual(after, jobSnapshot{job, items}) {
		t.Fatalf("commit failure changed snapshot: %+v", after)
	}
}
