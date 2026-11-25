package restorerequest

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/tinkerologie/vault/internal/db"
	"github.com/tinkerologie/vault/internal/engine"
	"github.com/tinkerologie/vault/internal/runner"
)

// writeTargetFixture is one Job on local storage with a single full Restore
// Point whose item directories the test fills in.
type writeTargetFixture struct {
	database *db.DB
	root     string
	job      db.Job
	point    db.RestorePoint
}

func newWriteTargetFixture(t *testing.T, metadata string) writeTargetFixture {
	t.Helper()
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
	job := db.Job{Name: "write targets", Enabled: true, StorageDestID: dest.ID}
	if job.ID, err = database.CreateJob(job); err != nil {
		t.Fatal(err)
	}
	runID, err := database.CreateJobRun(db.JobRun{JobID: job.ID, Status: "completed"})
	if err != nil {
		t.Fatal(err)
	}
	point := db.RestorePoint{JobRunID: runID, JobID: job.ID, BackupType: "full", StoragePath: "points/full", Metadata: metadata}
	if point.ID, err = database.CreateRestorePoint(point); err != nil {
		t.Fatal(err)
	}
	return writeTargetFixture{database: database, root: root, job: job, point: point}
}

func (f writeTargetFixture) writeItem(t *testing.T, item string, files map[string]string) {
	t.Helper()
	dir := filepath.Join(f.root, f.point.StoragePath, item)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, data := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

// volumeArchive mirrors the engine's stable per-source archive name.
func volumeArchive(source string) string {
	sum := sha256.Sum256([]byte(source))
	return fmt.Sprintf("volume_%x.tar", sum[:8])
}

func containerFixtureFiles(t *testing.T, appdata, hook string) map[string]string {
	t.Helper()
	inspect, err := json.Marshal(map[string]any{
		"Name":   "/app",
		"Config": map[string]any{"Image": "fixture/app:backup"},
		"State":  map[string]any{"Running": true},
		"Mounts": []map[string]string{
			{"Type": "bind", "Source": appdata, "Destination": "/config"},
			{"Type": "bind", "Source": hook, "Destination": "/hook.sh"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	volumes, err := json.Marshal([]map[string]any{
		{"index": 0, "source": appdata, "backed_up": true, "archive": volumeArchive(appdata)},
		{"index": 1, "source": hook, "backed_up": true, "is_file": true, "archive": volumeArchive(hook)},
	})
	if err != nil {
		t.Fatal(err)
	}
	return map[string]string{
		"config.json": string(inspect), "volumes.json": string(volumes), "image.tar": "image", "template.xml": "<Container/>",
		volumeArchive(appdata): "data", volumeArchive(hook): "data",
	}
}

func TestPreviewWriteTargetsContainer(t *testing.T) {
	host := realTempDir(t)
	appdata := filepath.Join(host, "appdata", "app", "config")
	hook := filepath.Join(host, "appdata", "app", "hook.sh")
	custom := filepath.Join(realTempDir(t), "restored")
	template := engine.ContainerTemplatePath("app")

	cases := []struct {
		name        string
		destination string
		want        []WriteTarget
	}{
		{
			name: "original location writes each saved volume source",
			want: []WriteTarget{
				{Kind: "volume", Path: appdata, Mount: "/config", Clearable: true},
				{Kind: "file", Path: hook, Mount: "/hook.sh"},
				{Kind: "template", Path: template},
			},
		},
		{
			name:        "custom destination remaps each volume under it",
			destination: custom,
			want: []WriteTarget{
				{Kind: "volume", Path: filepath.Join(custom, "config"), Mount: "/config", Clearable: true},
				{Kind: "file", Path: filepath.Join(custom, "hook.sh"), Mount: "/hook.sh"},
				{Kind: "template", Path: template},
			},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newWriteTargetFixture(t, `{"item_sizes":{"app":20},"item_types":{"app":"container"}}`)
			f.writeItem(t, "app", containerFixtureFiles(t, appdata, hook))
			got := PreviewWriteTargets(t.Context(), f.database, runner.New(f.database, nil, nil), Input{
				JobID: f.job.ID, RestorePointID: f.point.ID, ItemName: "app", ItemType: "container", Destination: c.destination,
			}, "")
			if got.Status != "resolved" || got.Reason != "" {
				t.Fatalf("status = %q reason = %q", got.Status, got.Reason)
			}
			if got.Name != "app" || got.Type != "container" {
				t.Fatalf("identity = %q/%q", got.Name, got.Type)
			}
			if !slices.Equal(got.WriteTargets, c.want) {
				t.Fatalf("write targets = %+v\nwant %+v", got.WriteTargets, c.want)
			}
		})
	}
}

func TestPreviewWriteTargetsUnavailable(t *testing.T) {
	cases := []struct {
		name   string
		in     func(f writeTargetFixture) Input
		files  map[string]string
		reason string
	}{
		{
			name: "item absent from the Restore Point",
			in: func(f writeTargetFixture) Input {
				return Input{JobID: f.job.ID, RestorePointID: f.point.ID, ItemName: "other", ItemType: "container"}
			},
			reason: "not_in_restore_point",
		},
		{
			name: "custom destination outside the restore policy",
			in: func(f writeTargetFixture) Input {
				return Input{JobID: f.job.ID, RestorePointID: f.point.ID, ItemName: "app", ItemType: "container", Destination: "/etc/vault"}
			},
			reason: "destination_disallowed",
		},
		{
			name: "saved configuration that cannot be read",
			in: func(f writeTargetFixture) Input {
				return Input{JobID: f.job.ID, RestorePointID: f.point.ID, ItemName: "app", ItemType: "container"}
			},
			files:  map[string]string{"config.json": "not json", "image.tar": "image"},
			reason: "container_metadata_unusable",
		},
		{
			name: "unsupported item type",
			in: func(f writeTargetFixture) Input {
				return Input{JobID: f.job.ID, RestorePointID: f.point.ID, ItemName: "app", ItemType: "plugin"}
			},
			reason: "unsupported_item_type",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newWriteTargetFixture(t, `{"item_sizes":{"app":20},"item_types":{"app":"container"}}`)
			if c.files != nil {
				f.writeItem(t, "app", c.files)
			}
			in := c.in(f)
			got := PreviewWriteTargets(t.Context(), f.database, runner.New(f.database, nil, nil), in, "")
			if got.Status != "unavailable" || got.Reason != c.reason || len(got.WriteTargets) != 0 {
				t.Fatalf("preview = %+v, want unavailable with reason %q", got, c.reason)
			}
			if got.WriteTargets == nil {
				t.Fatal("write_targets must encode as an empty list, not null")
			}
			if got.Name != in.ItemName || got.Type != in.ItemType {
				t.Fatalf("identity = %q/%q", got.Name, got.Type)
			}
		})
	}
}

// fakeVMHost reviews a saved VM the way the libvirt host does, without one.
type fakeVMHost struct {
	disk, nvram string
}

func (h fakeVMHost) ReviewVMRestore(_ context.Context, in engine.VMRestoreReviewInput) (engine.VMRestoreEffects, string, error) {
	backup, err := engine.InspectVMBackup(in.XML, in.Metadata, in.Artifacts)
	if err != nil {
		return engine.VMRestoreEffects{}, "", err
	}
	disk, nvram := h.disk, h.nvram
	if in.Destination != "" {
		disk, nvram = filepath.Join(in.Destination, filepath.Base(disk)), filepath.Join(in.Destination, filepath.Base(nvram))
	}
	return engine.VMRestoreEffects{
		Name: in.Name, UUID: backup.UUID, Destination: in.Destination,
		Disks:  []engine.VMRestoreDiskEffect{{Target: "vda", Format: "qcow2", Destination: disk, Replace: true}},
		NVRAM:  &engine.VMRestoreDiskEffect{Destination: nvram, Replace: true},
		Domain: engine.VMRestoreDomainEffect{Replace: true}, ReadinessMode: "running",
	}, "expectation", nil
}

func (fakeVMHost) Restore(context.Context, engine.BackupItem, string, engine.ProgressFunc) error {
	return nil
}

func (fakeVMHost) Close() error { return nil }

func TestPreviewWriteTargetsVM(t *testing.T) {
	disk := filepath.Join(realTempDir(t), "domains", "guest", "vdisk1.img")
	nvram := filepath.Join(realTempDir(t), "nvram", "guest_VARS.fd")
	custom := filepath.Join(realTempDir(t), "restored")
	cases := []struct {
		name        string
		destination string
		want        []WriteTarget
	}{
		{
			name: "original location",
			want: []WriteTarget{
				{Kind: "disk", Path: disk, Mount: "vda"},
				{Kind: "nvram", Path: nvram},
				{Kind: "domain", Name: "guest"},
			},
		},
		{
			name:        "custom destination",
			destination: custom,
			want: []WriteTarget{
				{Kind: "disk", Path: filepath.Join(custom, "vdisk1.img"), Mount: "vda"},
				{Kind: "nvram", Path: filepath.Join(custom, "guest_VARS.fd")},
				{Kind: "domain", Name: "guest"},
			},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newWriteTargetFixture(t, `{"item_sizes":{"guest":20},"item_types":{"guest":"vm"}}`)
			f.writeItem(t, "guest", map[string]string{
				"domain.xml":   fmt.Sprintf(`<domain><name>guest</name><uuid>22222222-3333-4444-aaaa-555555555555</uuid><devices><disk device="disk" type="file"><driver type="qcow2"/><source file=%q/><target dev="vda"/></disk></devices></domain>`, disk),
				"vm_meta.json": `{"state":"shutoff","backup_type":"full","disks":[{"target":"vda","backup_file":"vdisk0.qcow2","format":"qcow2"}]}`,
				"vdisk0.qcow2": "fixture-data",
			})
			host := fakeVMHost{disk: disk, nvram: nvram}
			execution := runner.New(f.database, nil, []byte("server-key"), func() (runner.VMRestoreHost, error) { return host, nil })
			got := PreviewWriteTargets(t.Context(), f.database, execution, Input{
				JobID: f.job.ID, RestorePointID: f.point.ID, ItemName: "guest", ItemType: "vm", Destination: c.destination,
			}, "")
			if got.Status != "resolved" {
				t.Fatalf("preview = %+v", got)
			}
			if !slices.Equal(got.WriteTargets, c.want) {
				t.Fatalf("write targets = %+v\nwant %+v", got.WriteTargets, c.want)
			}
		})
	}
}

func TestPreviewWriteTargetsVMHostUnavailable(t *testing.T) {
	f := newWriteTargetFixture(t, `{"item_sizes":{"guest":20},"item_types":{"guest":"vm"}}`)
	f.writeItem(t, "guest", map[string]string{
		"domain.xml":   `<domain><name>guest</name><uuid>22222222-3333-4444-aaaa-555555555555</uuid><devices><disk device="disk" type="file"><driver type="qcow2"/><source file="/mnt/user/domains/guest/vdisk1.img"/><target dev="vda"/></disk></devices></domain>`,
		"vm_meta.json": `{"state":"shutoff","backup_type":"full","disks":[{"target":"vda","backup_file":"vdisk0.qcow2","format":"qcow2"}]}`,
		"vdisk0.qcow2": "fixture-data",
	})
	execution := runner.New(f.database, nil, []byte("server-key"), func() (runner.VMRestoreHost, error) {
		return nil, os.ErrNotExist
	})
	got := PreviewWriteTargets(t.Context(), f.database, execution, Input{
		JobID: f.job.ID, RestorePointID: f.point.ID, ItemName: "guest", ItemType: "vm",
	}, "")
	if got.Status != "unavailable" || got.Reason != "vm_host_unavailable" || strings.Contains(fmt.Sprint(got), "/mnt/user/domains") {
		t.Fatalf("preview = %+v", got)
	}
}

func TestPreviewErrorReason(t *testing.T) {
	cases := []struct {
		err  error
		want string
	}{
		{Invalid("the selected Restore Point does not contain that VM"), "not_in_restore_point"},
		{fmt.Errorf("capturing: %w", Invalid("write_mode must be replace or merge")), "invalid_selection"},
		{fmt.Errorf("capturing: %w", runner.ErrRestorePassphrase), "passphrase_required"},
		{runner.ErrRunnerBusy, "busy"},
		{fmt.Errorf("reviewing: %w", context.DeadlineExceeded), "cancelled"},
		{runner.ErrApprovedStorageUnavailable, "storage_unavailable"},
	}
	for _, c := range cases {
		if got := previewErrorReason(c.err); got != c.want {
			t.Errorf("previewErrorReason(%v) = %q, want %q", c.err, got, c.want)
		}
	}
}

// realTempDir resolves platform symlinks such as macOS /var, as the
// restore's path normalization does.
func realTempDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}
