package runner

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/tinkerologie/vault/internal/bounded"
	"github.com/tinkerologie/vault/internal/crypto"
	"github.com/tinkerologie/vault/internal/db"
	"github.com/tinkerologie/vault/internal/engine"
)

const (
	vmcovDomainXML = `<domain><name>test-vm</name><uuid>22222222-3333-4444-aaaa-555555555555</uuid><devices><disk device="disk"><driver type="qcow2"/><source file="/mnt/user/domains/test-vm/disk.img"/><target dev="vda"/></disk></devices></domain>`
	vmcovItemMeta  = `{"item_sizes":{"test-vm":50},"item_types":{"test-vm":"vm"}}`
)

// vmcovQCOW2 returns a QCOW2 header with the given version, backing file
// offset and incompatible feature bits.
func vmcovQCOW2(version uint32, backing, incompatible uint64) []byte {
	header := make([]byte, 104)
	copy(header, "QFI\xfb")
	binary.BigEndian.PutUint32(header[4:8], version)
	binary.BigEndian.PutUint64(header[8:16], backing)
	binary.BigEndian.PutUint64(header[72:80], incompatible)
	return header
}

func vmcovSidecar(backupType, checkpoint, parent string) string {
	return fmt.Sprintf(`{"state":"shutoff","backup_type":%q,"checkpoint":%q,"parent_checkpoint":%q,"disks":[{"target":"vda","backup_file":"vdisk0.qcow2","format":"qcow2"}]}`, backupType, checkpoint, parent)
}

func vmcovFiles(backupType, checkpoint, parent string) map[string][]byte {
	return map[string][]byte{
		"domain.xml":   []byte(vmcovDomainXML),
		"vm_meta.json": []byte(vmcovSidecar(backupType, checkpoint, parent)),
		"vdisk0.qcow2": vmcovQCOW2(3, 0, 0),
	}
}

type vmcovHost struct {
	mu          sync.Mutex
	openErr     error
	reviewErr   error
	restoreErr  error
	effects     engine.VMRestoreEffects
	reviews     []engine.VMRestoreReviewInput
	sources     [][]string
	restoreItem engine.BackupItem
	progress    []int
	closed      int
}

func (h *vmcovHost) open() (VMRestoreHost, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.openErr != nil {
		return nil, h.openErr
	}
	return h, nil
}

func (h *vmcovHost) ReviewVMRestore(_ context.Context, in engine.VMRestoreReviewInput) (engine.VMRestoreEffects, string, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.reviews = append(h.reviews, in)
	return h.effects, "reviewed-expectation", h.reviewErr
}

func (h *vmcovHost) Restore(_ context.Context, item engine.BackupItem, dir string, progress engine.ProgressFunc) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	var names []string
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	progress(item.Name, -1, "ignored")
	progress(item.Name, 50, "restoring")
	h.mu.Lock()
	defer h.mu.Unlock()
	h.sources = append(h.sources, names)
	h.restoreItem = item
	return h.restoreErr
}

func (h *vmcovHost) Close() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.closed++
	return nil
}

type vmcovRunner struct {
	r     *Runner
	d     *db.DB
	root  string
	job   db.Job
	runID int64
	host  *vmcovHost
}

func newVMCovRunner(t *testing.T) *vmcovRunner {
	t.Helper()
	r, d, root := setupTestRunner(t)
	r.serverKey = []byte("vm coverage review key")
	host := &vmcovHost{effects: engine.VMRestoreEffects{
		Name: "test-vm", Destination: "/mnt/user/restored",
		Disks: []engine.VMRestoreDiskEffect{{Target: "vda", Destination: "/mnt/user/restored/disk.img"}},
		NVRAM: &engine.VMRestoreDiskEffect{Destination: "/mnt/user/restored/vars.fd"},
	}}
	r.vmRestoreHost = host.open
	if err := d.SetSetting("staging_dir_override", t.TempDir()); err != nil {
		t.Fatal(err)
	}
	dest := createLocalDest(t, d, root)
	job := db.Job{Name: "vm-backup", Enabled: true, BackupTypeChain: "incremental", StorageDestID: dest.ID}
	var err error
	if job.ID, err = d.CreateJob(job); err != nil {
		t.Fatal(err)
	}
	runID, err := d.CreateJobRun(db.JobRun{JobID: job.ID, Status: "completed"})
	if err != nil {
		t.Fatal(err)
	}
	return &vmcovRunner{r: r, d: d, root: root, job: job, runID: runID, host: host}
}

// record saves files under <job>/<point>/test-vm and records the point.
func (f *vmcovRunner) record(t *testing.T, point string, files map[string][]byte, metadata string, parentID int64) db.RestorePoint {
	t.Helper()
	dir := filepath.Join(f.root, "vm-backup", point, "test-vm")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, payload := range files {
		if err := os.WriteFile(filepath.Join(dir, name), payload, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if metadata == "" {
		metadata = vmcovItemMeta
	}
	rp := db.RestorePoint{
		JobRunID: f.runID, JobID: f.job.ID, BackupType: "incremental",
		StoragePath: "vm-backup/" + point, ParentRestorePointID: parentID, Metadata: metadata,
	}
	id, err := f.d.CreateRestorePoint(rp)
	if err != nil {
		t.Fatal(err)
	}
	rp.ID = id
	return rp
}

func (f *vmcovRunner) stored(point, name string) string {
	return filepath.Join(f.root, "vm-backup", point, "test-vm", name)
}

func (f *vmcovRunner) inspect(t *testing.T, point db.RestorePoint, passphrase string) (VMRestoreReview, error) {
	t.Helper()
	plan := ApprovedRestorePlan{Job: f.job, Point: point, Item: ItemSelection{ItemType: "vm", ItemName: "test-vm"}}
	return f.r.InspectVMRestoreRequest(t.Context(), plan, "/mnt/user/restored", passphrase)
}

func TestVMCovInspectConfirmsFullBackupAndBindsReview(t *testing.T) {
	f := newVMCovRunner(t)
	point := f.record(t, "full", vmcovFiles("full", "base", ""), "", 0)
	review, err := f.inspect(t, point, "")
	if err != nil || review.Chain.Status != Confirmed || len(review.Reasons) != 0 {
		t.Fatalf("review = %+v, err = %v", review, err)
	}
	if review.Effects == nil || review.Effects.Name != "test-vm" || review.SourceDigest == "" || review.SourceAnchor == "" ||
		review.ItemTypeEvidence != Confirmed || !slices.Contains(review.Warnings, "unverified_backup") {
		t.Fatalf("review = %+v", review)
	}
	if len(review.Chain.Steps) != 1 || review.Chain.Steps[0].BackupType != "full" || review.Chain.Steps[0].Artifacts != Confirmed ||
		!slices.Equal(review.Chain.Steps[0].VMTargets, []string{"vda"}) {
		t.Fatalf("chain = %+v", review.Chain)
	}
	if len(f.host.reviews) != 1 || f.host.closed != 1 {
		t.Fatalf("host reviews = %d, closed = %d", len(f.host.reviews), f.host.closed)
	}
	in := f.host.reviews[0]
	if in.Name != "test-vm" || in.Destination != "/mnt/user/restored" || string(in.XML) != vmcovDomainXML ||
		!slices.Equal(in.Artifacts, []string{"domain.xml", "vdisk0.qcow2", "vm_meta.json"}) || !bytes.Equal(in.Key, f.r.serverKey) {
		t.Fatalf("review input = %+v", in)
	}
	work, ok := review.Plan.Work.(vmRestoreWork)
	if !ok || work.RequiresPassphrase() || work.expectation != "reviewed-expectation" || work.destination != "/mnt/user/restored" ||
		!slices.Equal(work.stagingAvoid, []string{"/mnt/user/restored", "/mnt/user/restored/disk.img", "/mnt/user/restored/vars.fd"}) ||
		len(work.steps) != 1 || len(review.Plan.Steps) != 1 {
		t.Fatalf("work = %+v", review.Plan.Work)
	}

	// The digest is stable for the same reviewed source and moves when the
	// saved sidecars change.
	again, err := f.inspect(t, point, "")
	if err != nil || again.SourceDigest != review.SourceDigest || again.SourceAnchor != review.SourceAnchor {
		t.Fatalf("repeat review digest = %q, err = %v", again.SourceDigest, err)
	}
	if err := os.WriteFile(f.stored("full", "vm_meta.json"), []byte(vmcovSidecar("full", "other", "")), 0o600); err != nil {
		t.Fatal(err)
	}
	changed, err := f.inspect(t, point, "")
	if err != nil || changed.SourceDigest == review.SourceDigest {
		t.Fatalf("changed sidecar digest = %q, err = %v", changed.SourceDigest, err)
	}
}

func TestVMCovInspectReportsLatestVerification(t *testing.T) {
	f := newVMCovRunner(t)
	point := f.record(t, "full", vmcovFiles("full", "base", ""), "", 0)
	if _, err := f.d.CreateVerifyRun(point.ID, "full"); err != nil {
		t.Fatal(err)
	}
	review, err := f.inspect(t, point, "")
	if err != nil || !slices.Contains(review.Warnings, "latest_verification_running") || slices.Contains(review.Warnings, "unverified_backup") {
		t.Fatalf("warnings = %v, err = %v", review.Warnings, err)
	}
}

func TestVMCovInspectStopsBeforeTheHost(t *testing.T) {
	t.Run("item not in the point", func(t *testing.T) {
		f := newVMCovRunner(t)
		point := f.record(t, "full", vmcovFiles("full", "base", ""), `{"item_sizes":{"other":1},"item_types":{"other":"vm"}}`, 0)
		review, err := f.inspect(t, point, "")
		if err != nil || review.Chain.Status != Missing || !slices.Equal(review.Reasons, []string{"vm_membership_missing"}) || len(f.host.reviews) != 0 {
			t.Fatalf("review = %+v, err = %v", review, err)
		}
	})
	t.Run("storage destination removed", func(t *testing.T) {
		f := newVMCovRunner(t)
		point := f.record(t, "full", vmcovFiles("full", "base", ""), "", 0)
		f.job.StorageDestID = 9999
		review, err := f.inspect(t, point, "")
		if err != nil || review.Chain.Status != Missing || !slices.Equal(review.Reasons, []string{"required_data_unusable"}) {
			t.Fatalf("review = %+v, err = %v", review, err)
		}
	})
	t.Run("host unavailable", func(t *testing.T) {
		f := newVMCovRunner(t)
		f.host.openErr = errors.New("libvirt socket missing")
		point := f.record(t, "full", vmcovFiles("full", "base", ""), "", 0)
		review, err := f.inspect(t, point, "")
		if err != nil || review.Effects != nil || !slices.Equal(review.Reasons, []string{"vm_host_unavailable"}) || review.SourceDigest != "" {
			t.Fatalf("review = %+v, err = %v", review, err)
		}
	})
	t.Run("host refuses the target", func(t *testing.T) {
		f := newVMCovRunner(t)
		f.host.reviewErr = fmt.Errorf("reviewing: %w", engine.ErrVMRestoreTargetInUse)
		point := f.record(t, "full", vmcovFiles("full", "base", ""), "", 0)
		review, err := f.inspect(t, point, "")
		if err != nil || review.Effects != nil || !slices.Equal(review.Reasons, []string{"vm_target_in_use"}) || review.Plan.Work != nil || f.host.closed != 1 {
			t.Fatalf("review = %+v, err = %v", review, err)
		}
	})
}

func TestVMCovInspectRefusesUnconfirmedChains(t *testing.T) {
	clashing := vmcovFiles("full", "base", "")
	clashing["domain.xml.gz"] = []byte("compressed copy")
	emptyDomain := vmcovFiles("full", "base", "")
	emptyDomain["domain.xml"] = nil
	noDisk := vmcovFiles("full", "base", "")
	delete(noDisk, "vdisk0.qcow2")
	badGzip := vmcovFiles("full", "base", "")
	delete(badGzip, "domain.xml")
	badGzip["domain.xml.gz"] = []byte("not gzip")
	tooLarge := vmcovFiles("full", "base", "")
	tooLarge["domain.xml"] = bytes.Repeat([]byte(" "), engine.VMRestoreSidecarLimit+1)
	unreadable := vmcovFiles("full", "base", "")
	unreadable["domain.xml"] = []byte("<domain>")
	renamed := vmcovFiles("full", "base", "")
	renamed["domain.xml"] = []byte(strings.ReplaceAll(vmcovDomainXML, "<name>test-vm</name>", "<name>other-vm</name>"))
	rawDisk := map[string][]byte{
		"domain.xml":   []byte(strings.ReplaceAll(vmcovDomainXML, `type="qcow2"`, `type="raw"`)),
		"vm_meta.json": []byte(`{"state":"shutoff","backup_type":"incremental","checkpoint":"selected","parent_checkpoint":"base","disks":[{"target":"vda","backup_file":"vdisk0.img","format":"raw"}]}`),
		"vdisk0.img":   []byte("raw disk"),
	}
	checksums := `{"item_sizes":{"test-vm":50},"item_types":{"test-vm":"vm"},"checksums":{"test-vm":{"domain.xml":"00","vm_meta.json":"","vdisk0.qcow2":""}}}`

	for _, tt := range []struct {
		name     string
		files    map[string][]byte
		metadata string
		parentID int64
		edit     func(*db.RestorePoint)
		want     Status
		reason   string
	}{
		{name: "item manifest", files: vmcovFiles("full", "base", ""), metadata: `{"item_sizes":{"test-vm":50},"item_types":{"test-vm":"vm"},"item_manifests":{"test-vm":5}}`, want: Broken, reason: "vm_manifest_unsupported"},
		{name: "absolute storage path", files: vmcovFiles("full", "base", ""), edit: func(p *db.RestorePoint) { p.StoragePath = "/vm-backup/abs" }, want: Unknown, reason: "vm_sidecar_unconfirmed"},
		{name: "item directory missing", files: vmcovFiles("full", "base", ""), edit: func(p *db.RestorePoint) { p.StoragePath = "vm-backup/gone" }, want: Missing, reason: "vm_required_artifact_missing"},
		{name: "objects restore to one name", files: clashing, want: Unknown, reason: "vm_sidecar_unconfirmed"},
		{name: "empty domain", files: emptyDomain, want: Missing, reason: "vm_required_artifact_missing"},
		{name: "disk missing", files: noDisk, want: Missing, reason: "vm_required_artifact_missing"},
		{name: "corrupt compressed sidecar", files: badGzip, want: Unknown, reason: "vm_sidecar_unconfirmed"},
		{name: "oversized sidecar", files: tooLarge, want: Unknown, reason: "vm_sidecar_too_large"},
		{name: "unreadable sidecar", files: unreadable, want: Unknown, reason: "vm_sidecar_unconfirmed"},
		{name: "checksum mismatch", files: vmcovFiles("full", "base", ""), metadata: checksums, want: Unknown, reason: "vm_sidecar_unconfirmed"},
		{name: "different VM", files: renamed, want: Broken, reason: "vm_identity_mismatch"},
		{name: "incremental without checkpoint", files: vmcovFiles("incremental", "selected", ""), parentID: 9999, want: Unknown, reason: "vm_checkpoint_unconfirmed"},
		{name: "incremental of itself", files: vmcovFiles("incremental", "same", "same"), parentID: 9999, want: Unknown, reason: "vm_checkpoint_unconfirmed"},
		{name: "unknown backup type", files: vmcovFiles("snapshot", "selected", "base"), parentID: 9999, want: Unknown, reason: "vm_checkpoint_unconfirmed"},
		{name: "raw incremental", files: rawDisk, parentID: 9999, want: Broken, reason: "vm_required_disk_layer_missing"},
		{name: "no recorded parent", files: vmcovFiles("incremental", "selected", "base"), want: Missing, reason: "vm_full_backup_missing"},
		{name: "self parent", files: vmcovFiles("incremental", "selected", "base"), edit: func(p *db.RestorePoint) { p.ParentRestorePointID = p.ID }, want: Broken, reason: "cycle"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := newVMCovRunner(t)
			point := f.record(t, "selected", tt.files, tt.metadata, tt.parentID)
			if tt.edit != nil {
				tt.edit(&point)
			}
			review, err := f.inspect(t, point, "")
			if err != nil || review.Chain.Status != tt.want || review.Chain.Reason != tt.reason || !slices.Contains(review.Reasons, tt.reason) {
				t.Fatalf("chain = %+v, reasons = %v, err = %v", review.Chain, review.Reasons, err)
			}
			if review.Effects != nil || len(f.host.reviews) != 0 {
				t.Fatalf("an unconfirmed chain reached the host: %+v", review.Effects)
			}
		})
	}
}

func TestVMCovInspectChecksEveryParent(t *testing.T) {
	otherDisk := vmcovFiles("full", "base", "")
	otherDisk["domain.xml"] = []byte(strings.ReplaceAll(vmcovDomainXML, `dev="vda"`, `dev="vdb"`))
	otherDisk["vm_meta.json"] = []byte(strings.ReplaceAll(vmcovSidecar("full", "base", ""), `"vda"`, `"vdb"`))
	for _, tt := range []struct {
		name       string
		parent     map[string][]byte
		parentMeta string
		foreign    bool
		want       Status
		reason     string
		steps      int
	}{
		{name: "parent lacks the disk", parent: otherDisk, want: Broken, reason: "vm_required_disk_layer_missing", steps: 2},
		{name: "parent membership unknown", parent: vmcovFiles("full", "base", ""), parentMeta: `{}`, want: Unknown, reason: "vm_chain_membership_unknown", steps: 2},
		{name: "parent skipped the VM", parent: vmcovFiles("full", "base", ""), parentMeta: `{"item_sizes":{"other":1},"item_types":{"test-vm":"vm"}}`, want: Missing, reason: "vm_full_backup_missing", steps: 2},
		{name: "parent from another job", parent: vmcovFiles("full", "base", ""), foreign: true, want: Broken, reason: "foreign_parent", steps: 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := newVMCovRunner(t)
			parentJob := f.job
			if tt.foreign {
				other := db.Job{Name: "other-backup", Enabled: true, StorageDestID: f.job.StorageDestID}
				id, err := f.d.CreateJob(other)
				if err != nil {
					t.Fatal(err)
				}
				f.job.ID, parentJob.ID = id, f.job.ID
			}
			parent := f.record(t, "base", tt.parent, tt.parentMeta, 0)
			f.job = parentJob
			point := f.record(t, "selected", vmcovFiles("incremental", "selected", "base"), "", parent.ID)
			review, err := f.inspect(t, point, "")
			if err != nil || review.Chain.Status != tt.want || review.Chain.Reason != tt.reason || len(review.Chain.Steps) != tt.steps {
				t.Fatalf("chain = %+v, err = %v", review.Chain, err)
			}
			if review.Chain.Steps[len(review.Chain.Steps)-1].RestorePointID != point.ID {
				t.Fatalf("selected point is not last: %+v", review.Chain.Steps)
			}
		})
	}
}

func TestVMCovInspectRequiresTheBackupPassphrase(t *testing.T) {
	f := newVMCovRunner(t)
	files := vmcovFiles("full", "base", "")
	encrypted, err := crypto.EncryptReader("correct horse", bytes.NewReader(files["domain.xml"]))
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := io.ReadAll(encrypted)
	if err != nil {
		t.Fatal(err)
	}
	delete(files, "domain.xml")
	files["domain.xml.age"] = sealed
	point := f.record(t, "full", files, "", 0)

	for _, passphrase := range []string{"", "wrong passphrase"} {
		if _, err := f.inspect(t, point, passphrase); !errors.Is(err, ErrRestorePassphrase) {
			t.Fatalf("passphrase %q: error = %v, want %v", passphrase, err, ErrRestorePassphrase)
		}
	}
	review, err := f.inspect(t, point, "correct horse")
	if err != nil || review.Chain.Status != Confirmed || review.Effects == nil {
		t.Fatalf("review = %+v, err = %v", review.Chain, err)
	}
}

func TestVMCovInspectReportsUnreadableStorage(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads files regardless of permissions")
	}
	f := newVMCovRunner(t)
	point := f.record(t, "full", vmcovFiles("full", "base", ""), "", 0)
	path := f.stored("full", "domain.xml")
	if err := os.Chmod(path, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0o600) })
	if _, err := f.inspect(t, point, ""); !errors.Is(err, ErrApprovedStorageUnavailable) {
		t.Fatalf("error = %v, want %v", err, ErrApprovedStorageUnavailable)
	}
}

func TestVMCovInspectHonoursReadSlotsAndCancellation(t *testing.T) {
	f := newVMCovRunner(t)
	point := f.record(t, "full", vmcovFiles("full", "base", ""), "", 0)
	for range cap(approvedReadSlots) {
		approvedReadSlots <- struct{}{}
	}
	_, err := f.inspect(t, point, "")
	for range cap(approvedReadSlots) {
		<-approvedReadSlots
	}
	if !errors.Is(err, ErrRunnerBusy) {
		t.Fatalf("busy error = %v, want %v", err, ErrRunnerBusy)
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	plan := ApprovedRestorePlan{Job: f.job, Point: point, Item: ItemSelection{ItemType: "vm", ItemName: "test-vm"}}
	if _, err := f.r.InspectVMRestoreRequest(ctx, plan, "", ""); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled error = %v", err)
	}
}

// vmcovApproved reviews point and returns the approved run for it.
func (f *vmcovRunner) vmcovApproved(t *testing.T, point db.RestorePoint, revalidate error) (*ApprovedRestore, vmRestoreWork) {
	t.Helper()
	review, err := f.inspect(t, point, "")
	if err != nil || review.Chain.Status != Confirmed {
		t.Fatalf("review = %+v, err = %v", review.Chain, err)
	}
	approved := &ApprovedRestore{Plan: review.Plan, Revalidate: func(context.Context) error { return revalidate }}
	return approved, review.Plan.Work.(vmRestoreWork)
}

func TestVMCovRestoreExecutesTheReviewedPoint(t *testing.T) {
	f := newVMCovRunner(t)
	point := f.record(t, "full", vmcovFiles("full", "base", ""), "", 0)
	approved, work := f.vmcovApproved(t, point, nil)
	reporter := restoreProgressReporter{JobID: f.job.ID, RunID: f.runID, ItemType: "vm", ItemsTotal: 1}
	if err := work.restore(t.Context(), f.r, approved, "", reporter); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if len(f.host.sources) != 1 || !slices.Equal(f.host.sources[0], []string{"domain.xml", "vdisk0.qcow2", "vm_meta.json"}) {
		t.Fatalf("restored sources = %v", f.host.sources)
	}
	item := f.host.restoreItem
	if item.Name != "test-vm" || item.Type != "vm" || item.Settings["restore_destination"] != "/mnt/user/restored" {
		t.Fatalf("restored item = %+v", item)
	}
	if !slices.Equal(approved.checks, []string{FolderDataStaged, FolderReviewRevalidated}) || f.host.closed != 2 {
		t.Fatalf("checks = %v, host closed %d times", approved.checks, f.host.closed)
	}
}

func TestVMCovRestoreRefusesChangedOrFailedExecution(t *testing.T) {
	for _, tt := range []struct {
		name    string
		mutate  func(t *testing.T, f *vmcovRunner)
		ctx     func(context.Context) context.Context
		recheck error
		want    error
		text    string
	}{
		{name: "sidecar changed in storage", mutate: func(t *testing.T, f *vmcovRunner) {
			vmcovWrite(t, f.stored("full", "vm_meta.json"), vmcovSidecar("full", "rewritten", ""))
		}, want: ErrReviewedRestoreChanged},
		{name: "disk removed from storage", mutate: func(t *testing.T, f *vmcovRunner) {
			if err := os.Remove(f.stored("full", "vdisk0.qcow2")); err != nil {
				t.Fatal(err)
			}
		}, want: ErrReviewedRestoreChanged},
		{name: "disk gained a backing file", mutate: func(t *testing.T, f *vmcovRunner) {
			vmcovWrite(t, f.stored("full", "vdisk0.qcow2"), string(vmcovQCOW2(3, 512, 0)))
		}, want: ErrReviewedRestoreChanged},
		{name: "cancelled", ctx: func(ctx context.Context) context.Context {
			ctx, cancel := context.WithCancel(ctx)
			cancel()
			return ctx
		}, want: context.Canceled},
		{name: "revalidation refused", recheck: ErrReviewedRestoreChanged, want: ErrReviewedRestoreChanged, text: "revalidating staged VM restore"},
		{name: "host gone", mutate: func(_ *testing.T, f *vmcovRunner) { f.host.openErr = errors.New("libvirt down") }, text: "opening VM restore host"},
		{name: "live effects changed", mutate: func(_ *testing.T, f *vmcovRunner) {
			f.host.restoreErr = fmt.Errorf("restoring: %w", engine.ErrVMReviewedEffectsChanged)
		}, want: ErrReviewedRestoreChanged},
		{name: "host restore failed", mutate: func(_ *testing.T, f *vmcovRunner) { f.host.restoreErr = errors.New("define failed") }, text: "restoring VM test-vm: define failed"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := newVMCovRunner(t)
			point := f.record(t, "full", vmcovFiles("full", "base", ""), "", 0)
			approved, work := f.vmcovApproved(t, point, tt.recheck)
			if tt.mutate != nil {
				tt.mutate(t, f)
			}
			ctx := t.Context()
			if tt.ctx != nil {
				ctx = tt.ctx(ctx)
			}
			err := work.restore(ctx, f.r, approved, "", restoreProgressReporter{})
			if err == nil || (tt.want != nil && !errors.Is(err, tt.want)) || !strings.Contains(err.Error(), tt.text) {
				t.Fatalf("restore error = %v, want %v containing %q", err, tt.want, tt.text)
			}
			if tt.name != "live effects changed" && tt.name != "host restore failed" && len(f.host.sources) != 0 {
				t.Fatal("the host restored after a refused check")
			}
		})
	}
}

func TestVMCovRestoreAssemblesIncrementalLayers(t *testing.T) {
	f := newVMCovRunner(t)
	base := f.record(t, "base", vmcovFiles("full", "base", ""), "", 0)
	point := f.record(t, "selected", vmcovFiles("incremental", "selected", "base"), "", base.ID)
	approved, work := f.vmcovApproved(t, point, nil)
	if len(work.steps) != 2 || len(approved.Plan.Steps) != 2 || approved.Plan.Steps[1].Point.ID != point.ID {
		t.Fatalf("work steps = %+v", work.steps)
	}
	err := work.restore(t.Context(), f.r, approved, "", restoreProgressReporter{})
	if _, lookErr := exec.LookPath("qemu-img"); lookErr != nil {
		if err == nil || !strings.Contains(err.Error(), "assembling VM disk layers") || len(f.host.sources) != 0 {
			t.Fatalf("restore without qemu-img = %v", err)
		}
		return
	}
	if err != nil && !strings.Contains(err.Error(), "assembling VM disk layers") {
		t.Fatalf("restore = %v", err)
	}
}

func vmcovWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestVMCovCheckStagedRequiresReviewedFiles(t *testing.T) {
	key := []byte("staged key")
	xmlData, metadata := []byte(vmcovDomainXML), []byte(vmcovSidecar("full", "base", ""))
	digest, err := vmSidecarDigest(key, xmlData, metadata)
	if err != nil {
		t.Fatal(err)
	}
	step := vmReviewedStep{digest: digest, evidence: engine.VMBackupEvidence{
		Disks:     []engine.VMBackupDisk{{Target: "vda", BackupFile: "vdisk0.qcow2", Format: "qcow2"}, {Target: "vdb", BackupFile: "vdisk1.img", Format: "raw"}},
		NVRAMFile: "vars.fd",
	}}
	stage := func(t *testing.T, skip string, override map[string]string) string {
		dir := t.TempDir()
		files := map[string]string{
			"domain.xml": string(xmlData), "vm_meta.json": string(metadata), "vdisk0.qcow2": string(vmcovQCOW2(2, 0, 0)),
			"vdisk1.img": "raw", "vars.fd": "firmware",
		}
		for name, content := range override {
			files[name] = content
		}
		for name, content := range files {
			if name != skip {
				vmcovWrite(t, filepath.Join(dir, name), content)
			}
		}
		return dir
	}
	if err := step.checkStaged(stage(t, "", nil), key); err != nil {
		t.Fatalf("checkStaged: %v", err)
	}
	if err := step.checkStaged(stage(t, "domain.xml", nil), key); err == nil || !strings.Contains(err.Error(), "reading staged VM domain") {
		t.Fatalf("missing domain = %v", err)
	}
	if err := step.checkStaged(stage(t, "vm_meta.json", nil), key); err == nil || !strings.Contains(err.Error(), "reading staged VM metadata") {
		t.Fatalf("missing metadata = %v", err)
	}
	for name, dir := range map[string]string{
		"different key":       "",
		"changed domain":      stage(t, "", map[string]string{"domain.xml": "<domain/>"}),
		"raw disk missing":    stage(t, "vdisk1.img", nil),
		"empty raw disk":      stage(t, "", map[string]string{"vdisk1.img": ""}),
		"external qcow2 data": stage(t, "", map[string]string{"vdisk0.qcow2": string(vmcovQCOW2(3, 0, 1<<2))}),
		"firmware missing":    stage(t, "vars.fd", nil),
	} {
		checkKey := key
		if dir == "" {
			dir, checkKey = stage(t, "", nil), []byte("another key")
		}
		if err := step.checkStaged(dir, checkKey); !errors.Is(err, ErrReviewedRestoreChanged) {
			t.Errorf("%s: error = %v, want %v", name, err, ErrReviewedRestoreChanged)
		}
	}
}

func TestVMCovIsSelfContainedQCOW2(t *testing.T) {
	dir := t.TempDir()
	for _, tt := range []struct {
		name    string
		content []byte
		want    bool
	}{
		{name: "version 2", content: vmcovQCOW2(2, 0, 0)[:72], want: true},
		{name: "version 3", content: vmcovQCOW2(3, 0, 1<<0), want: true},
		{name: "version 3 external data", content: vmcovQCOW2(3, 0, 1<<2)},
		{name: "version 3 corrupt", content: vmcovQCOW2(3, 0, 1<<1)},
		{name: "version 3 truncated", content: vmcovQCOW2(3, 0, 0)[:80]},
		{name: "backing file", content: vmcovQCOW2(2, 1024, 0)},
		{name: "future version", content: vmcovQCOW2(4, 0, 0)},
		{name: "raw data", content: bytes.Repeat([]byte{0}, 104)},
		{name: "short", content: []byte("QFI")},
	} {
		path := filepath.Join(dir, strings.ReplaceAll(tt.name, " ", "-"))
		vmcovWrite(t, path, string(tt.content))
		if got := isSelfContainedQCOW2(path); got != tt.want {
			t.Errorf("%s: isSelfContainedQCOW2 = %v, want %v", tt.name, got, tt.want)
		}
	}
	if isSelfContainedQCOW2(filepath.Join(dir, "absent")) {
		t.Error("a missing image is self-contained")
	}
}

func TestVMCovStagedChainDisksMapsLayersByTarget(t *testing.T) {
	layer := func(file string) vmReviewedStep {
		return vmReviewedStep{evidence: engine.VMBackupEvidence{Disks: []engine.VMBackupDisk{
			{Target: "vdb", BackupFile: "other-" + file, Format: "qcow2"}, {Target: "vda", BackupFile: file, Format: "qcow2"},
		}}}
	}
	work := vmRestoreWork{steps: []vmReviewedStep{layer("base.qcow2"), layer("delta.qcow2")}}
	work.steps[1].evidence.Disks = work.steps[1].evidence.Disks[1:]
	disks, err := work.stagedChainDisks([]string{"/stage/0", "/stage/1"})
	if err != nil || len(disks) != 1 || disks[0].Target != "vda" || disks[0].OutputName != "delta.qcow2" ||
		!slices.Equal(disks[0].Layers, []string{"/stage/0/base.qcow2", "/stage/1/delta.qcow2"}) {
		t.Fatalf("disks = %+v, err = %v", disks, err)
	}

	work.steps[0].evidence.Disks = work.steps[0].evidence.Disks[:1]
	if _, err := work.stagedChainDisks([]string{"/stage/0", "/stage/1"}); !errors.Is(err, ErrReviewedRestoreChanged) {
		t.Fatalf("missing layer = %v, want %v", err, ErrReviewedRestoreChanged)
	}
}

func TestVMCovReviewReasonsAndChainFailures(t *testing.T) {
	for err, want := range map[error]string{
		errVMManifest:                             "vm_manifest_unsupported",
		engine.ErrVMBackupArtifactMissing:         "vm_required_artifact_missing",
		bounded.ErrTooLarge:                       "vm_sidecar_too_large",
		engine.ErrVMRestoreTargetDisallowed:       "vm_target_disallowed",
		engine.ErrVMRestoreTargetCollision:        "vm_target_collision",
		engine.ErrVMRestoreIdentityChanged:        "vm_identity_mismatch",
		engine.ErrVMRestoreTargetInUse:            "vm_target_in_use",
		engine.ErrVMRestoreHostUnavailable:        "vm_host_unavailable",
		engine.ErrVMBackupEvidenceUnknown:         "vm_sidecar_unconfirmed",
		fmt.Errorf("wrapped: %w", errVMManifest):  "vm_manifest_unsupported",
		errors.New("anything else from the host"): "vm_sidecar_unconfirmed",
	} {
		if got := vmReviewReason(err); got != want {
			t.Errorf("vmReviewReason(%v) = %q, want %q", err, got, want)
		}
	}
	for err, want := range map[error]Status{
		engine.ErrVMBackupArtifactMissing:  Missing,
		errVMManifest:                      Broken,
		engine.ErrVMRestoreIdentityChanged: Broken,
		engine.ErrVMBackupEvidenceUnknown:  Unknown,
	} {
		var review VMRestoreReview
		step := RecoveryChainStep{Artifacts: Unknown}
		recordVMChainFailure(&review, &step, err)
		if step.Artifacts != want || review.Chain.Status != want || review.Chain.Reason != vmReviewReason(err) {
			t.Errorf("recordVMChainFailure(%v) = step %v, chain %+v", err, step.Artifacts, review.Chain)
		}
	}
}

func TestVMCovInspectBoundsTheObjectListing(t *testing.T) {
	// The storage probe stops at maxVMReviewObjects, so an oversized item
	// directory is reported as unchecked storage, never as a confirmed chain.
	f := newVMCovRunner(t)
	files := vmcovFiles("full", "base", "")
	for i := range maxVMReviewObjects {
		files[fmt.Sprintf("extra-%03d", i)] = []byte("x")
	}
	point := f.record(t, "full", files, "", 0)
	if review, err := f.inspect(t, point, ""); !errors.Is(err, ErrApprovedStorageUnavailable) || review.Effects != nil {
		t.Fatalf("review = %+v, err = %v", review.Chain, err)
	}
}

func TestVMCovInspectAcceptsPassedVerification(t *testing.T) {
	f := newVMCovRunner(t)
	point := f.record(t, "full", vmcovFiles("full", "base", ""), "", 0)
	id, err := f.d.CreateVerifyRun(point.ID, "full")
	if err != nil {
		t.Fatal(err)
	}
	if err := f.d.FinishVerifyRun(id, "passed", ""); err != nil {
		t.Fatal(err)
	}
	review, err := f.inspect(t, point, "")
	if err != nil || len(review.Warnings) != 0 || review.Verification.Status != "passed" {
		t.Fatalf("warnings = %v, verification = %+v, err = %v", review.Warnings, review.Verification, err)
	}
}

func TestVMCovInspectRefusesMissingOrTruncatedSidecars(t *testing.T) {
	var compressed bytes.Buffer
	zw := gzip.NewWriter(&compressed)
	if _, err := zw.Write([]byte(vmcovDomainXML)); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	truncated := vmcovFiles("full", "base", "")
	delete(truncated, "domain.xml")
	truncated["domain.xml.gz"] = compressed.Bytes()[:compressed.Len()/2]
	noMeta := vmcovFiles("full", "base", "")
	delete(noMeta, "vm_meta.json")
	for name, tt := range map[string]struct {
		files  map[string][]byte
		want   Status
		reason string
	}{
		"truncated compressed domain": {files: truncated, want: Unknown, reason: "vm_sidecar_unconfirmed"},
		"metadata missing":            {files: noMeta, want: Missing, reason: "vm_required_artifact_missing"},
	} {
		t.Run(name, func(t *testing.T) {
			f := newVMCovRunner(t)
			point := f.record(t, "full", tt.files, "", 0)
			review, err := f.inspect(t, point, "")
			if err != nil || review.Chain.Status != tt.want || review.Chain.Reason != tt.reason {
				t.Fatalf("chain = %+v, err = %v", review.Chain, err)
			}
		})
	}
}

func TestVMCovInspectReportsUnusableStorageConfig(t *testing.T) {
	f := newVMCovRunner(t)
	point := f.record(t, "full", vmcovFiles("full", "base", ""), "", 0)
	id, err := f.d.CreateStorageDestination(db.StorageDestination{Name: "broken", Type: "unsupported-type", Config: "{}"})
	if err != nil {
		t.Fatal(err)
	}
	f.job.StorageDestID = id
	if _, err := f.inspect(t, point, ""); !errors.Is(err, ErrApprovedStorageUnavailable) {
		t.Fatalf("error = %v, want %v", err, ErrApprovedStorageUnavailable)
	}
}

func TestVMCovInspectStopsAtTheChainDepthLimit(t *testing.T) {
	f := newVMCovRunner(t)
	skipped := `{"item_sizes":{"other":1},"item_types":{"test-vm":"vm"}}`
	var parentID int64
	for i := range maxEvidenceChainDepth {
		parentID = f.record(t, fmt.Sprintf("skipped-%03d", i), nil, skipped, parentID).ID
	}
	point := f.record(t, "selected", vmcovFiles("incremental", "selected", "base"), "", parentID)
	review, err := f.inspect(t, point, "")
	if err != nil || review.Chain.Reason != "depth_limit" || len(review.Chain.Steps) != maxEvidenceChainDepth || review.Effects != nil {
		t.Fatalf("chain = %s (%d steps), err = %v", review.Chain.Reason, len(review.Chain.Steps), err)
	}
}
