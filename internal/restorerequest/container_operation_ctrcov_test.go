package restorerequest

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/tinkerologie/vault/internal/db"
	"github.com/tinkerologie/vault/internal/engine"
	"github.com/tinkerologie/vault/internal/runner"
	"github.com/tinkerologie/vault/internal/storage"
)

func TestCtrcovContainerWriteMode(t *testing.T) {
	effect := func(mode string) engine.ContainerVolumeEffect { return engine.ContainerVolumeEffect{WriteMode: mode} }
	cases := []struct {
		name    string
		volumes []engine.ContainerVolumeEffect
		want    string
	}{
		{name: "no volumes", want: "recreate"},
		{name: "configuration only", volumes: []engine.ContainerVolumeEffect{effect("configuration_only")}, want: "recreate"},
		{name: "one mode", volumes: []engine.ContainerVolumeEffect{effect("merge"), effect("configuration_only"), effect("merge")}, want: "merge"},
		{name: "mixed", volumes: []engine.ContainerVolumeEffect{effect("merge"), effect("replace")}, want: "mixed"},
	}
	for _, c := range cases {
		if got := containerWriteMode(c.volumes); got != c.want {
			t.Errorf("%s: mode = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestCtrcovCaptureContainerStopsEarly(t *testing.T) {
	op := &Operation{Input: Input{Destination: "/etc/vault-restore"}}
	if err := op.captureContainer(t.Context(), nil, ""); err == nil {
		t.Fatal("a destination outside the container restore policy must be refused")
	}
	if op.TargetChoice != "" || op.Container != nil {
		t.Fatalf("refused operation = %+v", op.Review)
	}

	op = &Operation{Input: Input{Destination: t.TempDir()}, Review: Review{Reasons: []string{"chain_unconfirmed"}}}
	if err := op.captureContainer(t.Context(), nil, ""); err != nil {
		t.Fatalf("ineligible operation: %v", err)
	}
	if op.TargetChoice != "alternate" || op.Container != nil || len(op.Effects) != 0 {
		t.Fatalf("ineligible operation was reviewed: %+v", op.Review)
	}
}

func TestCtrcovCaptureContainerListsEveryReviewedEffect(t *testing.T) {
	database, err := db.Open(filepath.Join(t.TempDir(), "vault.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	root := t.TempDir()
	config, err := json.Marshal(map[string]string{"path": root})
	if err != nil {
		t.Fatal(err)
	}
	dest := db.StorageDestination{Name: "local", Type: "local", Config: string(config)}
	if dest.ID, err = database.CreateStorageDestination(dest); err != nil {
		t.Fatal(err)
	}
	job := db.Job{Name: "containers", Enabled: true, StorageDestID: dest.ID}
	if job.ID, err = database.CreateJob(job); err != nil {
		t.Fatal(err)
	}
	runID, err := database.CreateJobRun(db.JobRun{JobID: job.ID, Status: "completed"})
	if err != nil {
		t.Fatal(err)
	}
	point := db.RestorePoint{JobRunID: runID, JobID: job.ID, BackupType: "full", StoragePath: "containers/full",
		Metadata: `{"item_sizes":{"app":20},"item_types":{"app":"container"}}`}
	if point.ID, err = database.CreateRestorePoint(point); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, point.StoragePath, "app")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string]string{
		"config.json":               `{"Name":"/app","Config":{"Image":"fixture/app:backup"},"State":{"Running":true}}`,
		"image.tar":                 "image",
		"template.xml":              "<Container/>",
		engine.DatabaseDumpFile:     "dump",
		engine.DatabaseReplayMarker: "",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	adapter, err := storage.NewAdapter(dest.Type, dest.Config)
	if err != nil {
		t.Fatal(err)
	}
	objects, err := adapter.List(filepath.Join(point.StoragePath, "app"))
	storage.CloseAdapter(adapter)
	if err != nil {
		t.Fatal(err)
	}

	op := &Operation{Review: Review{Item: runner.ItemSelection{ItemType: "container", ItemName: "app"}, WriteMode: "replace",
		Warnings: []string{"artifact_evidence_unavailable"}}}
	op.plan = runner.ApprovedRestorePlan{Job: job, Destination: dest, Point: point, Item: op.Item,
		Steps: []runner.ApprovedRestoreStep{{Point: point, Objects: objects}}}
	if err := op.captureContainer(t.Context(), runner.New(database, nil, nil), ""); err != nil {
		t.Fatalf("captureContainer: %v", err)
	}
	want := []string{"stop_existing_container", "remove_existing_container", "recreate_container", "load_saved_image",
		"start_container", "restore_container_template", "replay_container_database_if_ready", "preserve_container_database_dump_if_not_replayed"}
	if !slices.Equal(op.Effects, want) {
		t.Fatalf("effects = %v, want %v", op.Effects, want)
	}
	if op.Container == nil || op.EffectiveWriteMode != "recreate" || op.WorkDigest == "" || len(op.Warnings) != 0 || op.plan.Work == nil {
		t.Fatalf("operation = %+v", op)
	}
}
