package restorerequest

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/tinkerologie/vault/internal/db"
	"github.com/tinkerologie/vault/internal/runner"
)

// legacyContainerPoint records a classic container backup made before
// item_types was written, with no Verification run, so neither the catalog
// nor a verification result can confirm the item type. config.json is the
// saved configuration the review reads; an empty value omits it.
func legacyContainerPoint(t *testing.T, config string) (*Service, Input) {
	t.Helper()
	database, err := db.Open(filepath.Join(t.TempDir(), "vault.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	root := t.TempDir()
	destConfig, err := json.Marshal(map[string]string{"path": root})
	if err != nil {
		t.Fatal(err)
	}
	dest := db.StorageDestination{Name: "local", Type: "local", Config: string(destConfig)}
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
		Metadata: `{"item_sizes":{"app":20}}`}
	if point.ID, err = database.CreateRestorePoint(point); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, point.StoragePath, "app")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{"image.tar": "image"}
	if config != "" {
		files["config.json"] = config
	}
	for name, data := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	service := New(database, nil, nil, runner.New(database, nil, nil), nil, nil)
	return service, Input{JobID: job.ID, RestorePointID: point.ID, ItemName: "app", ItemType: "container", WriteMode: "replace"}
}

// Older container backups have no recorded item type, so their type and
// volume coverage stay Unknown until the saved configuration is reviewed.
// That review must run instead of being skipped by the unknown-type reasons.
func TestLegacyContainerBackupReachesConfigurationReview(t *testing.T) {
	service, in := legacyContainerPoint(t, `{"Name":"/app","Config":{"Image":"fixture/app:backup"},"State":{"Running":false}}`)

	op, err := service.capture(t.Context(), in, "")
	if err != nil {
		t.Fatalf("capture: %v", err)
	}
	if !op.Eligible || len(op.Reasons) != 0 {
		t.Fatalf("eligible = %v, reasons = %v; want the configuration review to settle the type", op.Eligible, op.Reasons)
	}
	if op.ItemTypeEvidence != runner.Confirmed || op.Container == nil {
		t.Fatalf("item type evidence = %q, container = %v", op.ItemTypeEvidence, op.Container)
	}
	for _, step := range op.Chain.Steps {
		if step.DataRequired && (step.ItemTypeEvidence != runner.Confirmed || step.Artifacts != runner.Confirmed) {
			t.Fatalf("step %+v left unconfirmed after review", step)
		}
	}
}

// When the saved configuration cannot be reviewed, nothing establishes the
// type, so the request stays ineligible and says why.
func TestLegacyContainerBackupWithoutUsableConfigurationStaysIneligible(t *testing.T) {
	service, in := legacyContainerPoint(t, "")

	op, err := service.capture(t.Context(), in, "")
	if err != nil {
		t.Fatalf("capture: %v", err)
	}
	if op.Eligible {
		t.Fatal("a request whose type review failed must not be eligible")
	}
	if !slices.Contains(op.Reasons, "item_type_unconfirmed") || op.ItemTypeEvidence == runner.Confirmed {
		t.Fatalf("reasons = %v, evidence = %q; want the unconfirmed type stated", op.Reasons, op.ItemTypeEvidence)
	}
}
