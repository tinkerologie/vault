package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/ruaan-deysel/vault/internal/db"
	"github.com/ruaan-deysel/vault/internal/engine"
)

// volumeManifestFixture mirrors the on-disk volumes.json the container engine
// writes next to a classic backup's per-volume archives.
type volumeManifestFixture struct {
	Index       int    `json:"index"`
	Source      string `json:"source"`
	Destination string `json:"destination"`
	BackedUp    bool   `json:"backed_up"`
	SkipReason  string `json:"skip_reason,omitempty"`
	Archive     string `json:"archive,omitempty"`
	IsFile      bool   `json:"is_file,omitempty"`
}

// classicContainerPoint builds a job with one container item plus a classic
// full restore point, and returns the handler, the item directory on disk, and
// the URL to browse its contents.
func classicContainerPoint(t *testing.T, itemName string) (*JobHandler, string, string, int64, int64) {
	t.Helper()
	h, d := newJobHandlerDB(t)

	storageRoot := t.TempDir()
	cfg, _ := json.Marshal(map[string]string{"path": storageRoot})
	destID, err := d.CreateStorageDestination(db.StorageDestination{
		Name: "cvi-" + nextUnique(), Type: "local", Config: string(cfg),
	})
	if err != nil {
		t.Fatalf("create dest: %v", err)
	}
	jobID, err := d.CreateJob(db.Job{Name: "cvi-job-" + nextUnique(), StorageDestID: destID})
	if err != nil {
		t.Fatalf("create job: %v", err)
	}
	if _, err := d.AddJobItem(db.JobItem{
		JobID: jobID, ItemType: "container", ItemName: itemName, ItemID: itemName,
	}); err != nil {
		t.Fatalf("add job item: %v", err)
	}
	runID, err := d.CreateJobRun(db.JobRun{JobID: jobID, Status: "success"})
	if err != nil {
		t.Fatalf("create run: %v", err)
	}
	rpID, err := d.CreateRestorePoint(db.RestorePoint{
		JobRunID: runID, JobID: jobID, BackupType: "full",
		StoragePath: "rp-cvi", Metadata: "{}",
	})
	if err != nil {
		t.Fatalf("create restore point: %v", err)
	}

	itemDir := filepath.Join(storageRoot, "rp-cvi", itemName)
	if err := os.MkdirAll(itemDir, 0o755); err != nil {
		t.Fatalf("mkdir item dir: %v", err)
	}
	url := fmt.Sprintf("/api/v1/jobs/%d/restore-points/%d/contents?item=%s", jobID, rpID, itemName)
	return h, itemDir, url, jobID, rpID
}

func writeJSONFile(t *testing.T, path string, v any) {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal %s: %v", path, err)
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func writeVolumeIndex(t *testing.T, itemDir, archive string, paths ...string) {
	t.Helper()
	idx := engine.TarIndex{Version: 1, Archive: archive}
	for _, p := range paths {
		idx.Files = append(idx.Files, engine.TarIndexEntry{
			Path: p, Size: 10, Mode: "0644", ModTime: "2026-01-01T00:00:00Z",
		})
	}
	writeJSONFile(t, filepath.Join(itemDir, archive+engine.IndexSuffix), idx)
}

func browseContentsAt(t *testing.T, h *JobHandler, url string, jobID, rpID int64) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, url, nil)
	req = withURLParams(req, "id", strconv.FormatInt(jobID, 10), "rpid", strconv.FormatInt(rpID, 10))
	w := httptest.NewRecorder()
	h.RestorePointContents(w, req)
	return w
}

func decodeIndex(t *testing.T, w *httptest.ResponseRecorder) engine.TarIndex {
	t.Helper()
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	var got engine.TarIndex
	if err := json.NewDecoder(w.Body).Decode(&got); err != nil {
		t.Fatalf("decode resp: %v", err)
	}
	return got
}

func indexPaths(idx engine.TarIndex) []string {
	paths := make([]string, 0, len(idx.Files))
	for _, f := range idx.Files {
		paths = append(paths, f.Path)
	}
	return paths
}

func assertPaths(t *testing.T, got []string, want ...string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("paths = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("paths = %v, want %v", got, want)
		}
	}
}

// TestContainerVolumeIndex_MergesEveryVolume is the core of issue #275: a
// multi-volume container must list every backed-up volume at once, in
// container-internal absolute paths, rather than one arbitrary volume's files
// under bare volume-relative names.
func TestContainerVolumeIndex_MergesEveryVolume(t *testing.T) {
	t.Parallel()
	h, itemDir, url, jobID, rpID := classicContainerPoint(t, "plex")

	writeJSONFile(t, filepath.Join(itemDir, "volumes.json"), []volumeManifestFixture{
		{Index: 0, Source: "/mnt/user/appdata/plex", Destination: "/config", BackedUp: true, Archive: "vol-0.tar"},
		{Index: 1, Source: "/mnt/cache/transcode", Destination: "/transcode", BackedUp: true, Archive: "vol-1.tar"},
		{Index: 2, Source: "/mnt/user/media", Destination: "/media", BackedUp: false, SkipReason: "excluded"},
	})
	writeVolumeIndex(t, itemDir, "vol-0.tar", "settings.yml", "logs/plex.log")
	writeVolumeIndex(t, itemDir, "vol-1.tar", "tmp.bin")

	got := decodeIndex(t, browseContentsAt(t, h, url, jobID, rpID))
	assertPaths(t, indexPaths(got),
		"/config/logs/plex.log",
		"/config/settings.yml",
		"/transcode/tmp.bin",
	)
}

// TestContainerVolumeIndex_FileMount covers a single-file bind mount, where the
// mount destination IS the file and the archive holds it under its base name.
func TestContainerVolumeIndex_FileMount(t *testing.T) {
	t.Parallel()
	h, itemDir, url, jobID, rpID := classicContainerPoint(t, "nginx")

	writeJSONFile(t, filepath.Join(itemDir, "volumes.json"), []volumeManifestFixture{
		{Index: 0, Source: "/boot/config/localtime", Destination: "/etc/localtime", BackedUp: true, Archive: "vol-0.tar", IsFile: true},
	})
	writeVolumeIndex(t, itemDir, "vol-0.tar", "localtime")

	got := decodeIndex(t, browseContentsAt(t, h, url, jobID, rpID))
	assertPaths(t, indexPaths(got), "/etc/localtime")
}

// TestContainerVolumeIndex_OmitsUnreadableVolume degrades the listing by one
// volume rather than failing the whole request when a sidecar is missing or
// corrupt.
func TestContainerVolumeIndex_OmitsUnreadableVolume(t *testing.T) {
	t.Parallel()
	h, itemDir, url, jobID, rpID := classicContainerPoint(t, "sonarr")

	writeJSONFile(t, filepath.Join(itemDir, "volumes.json"), []volumeManifestFixture{
		{Index: 0, Destination: "/config", BackedUp: true, Archive: "vol-0.tar"},
		// Backed up, but its index sidecar was never written.
		{Index: 1, Destination: "/downloads", BackedUp: true, Archive: "vol-1.tar"},
		// Present but corrupt.
		{Index: 2, Destination: "/tv", BackedUp: true, Archive: "vol-2.tar"},
	})
	writeVolumeIndex(t, itemDir, "vol-0.tar", "settings.yml")
	if err := os.WriteFile(filepath.Join(itemDir, "vol-2.tar"+engine.IndexSuffix), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}

	got := decodeIndex(t, browseContentsAt(t, h, url, jobID, rpID))
	assertPaths(t, indexPaths(got), "/config/settings.yml")
}

// TestContainerVolumeIndex_FallsBackWithoutManifest keeps containers backed up
// before volumes.json existed browsable: without a manifest there is nothing to
// attribute paths to, so the pre-#275 single-archive listing still applies.
func TestContainerVolumeIndex_FallsBackWithoutManifest(t *testing.T) {
	t.Parallel()
	h, itemDir, url, jobID, rpID := classicContainerPoint(t, "radarr")

	writeVolumeIndex(t, itemDir, "backup.tar", "settings.yml")

	got := decodeIndex(t, browseContentsAt(t, h, url, jobID, rpID))
	assertPaths(t, indexPaths(got), "settings.yml")
	if got.Archive != "backup.tar" {
		t.Errorf("archive = %q, want backup.tar", got.Archive)
	}
}

// TestContainerVolumeIndex_ExplicitArchiveBypassesMerge: addressing one archive
// directly still returns exactly that archive's listing.
func TestContainerVolumeIndex_ExplicitArchiveBypassesMerge(t *testing.T) {
	t.Parallel()
	h, itemDir, url, jobID, rpID := classicContainerPoint(t, "bazarr")

	writeJSONFile(t, filepath.Join(itemDir, "volumes.json"), []volumeManifestFixture{
		{Index: 0, Destination: "/config", BackedUp: true, Archive: "vol-0.tar"},
	})
	writeVolumeIndex(t, itemDir, "vol-0.tar", "settings.yml")

	got := decodeIndex(t, browseContentsAt(t, h, url+"&file=vol-0.tar", jobID, rpID))
	assertPaths(t, indexPaths(got), "settings.yml")
}

// TestContainerVolumeIndex_DifferentialChainPrune guards the interaction the
// merge created: a chain restore point prunes its merged contents against the
// newest step's effective listing, and a container's listing sidecars are
// per-volume and volume-relative. Comparing those directly against the merged
// container-absolute paths keeps nothing and empties the picker, so the
// keep-set must go through the same volume merge.
func TestContainerVolumeIndex_DifferentialChainPrune(t *testing.T) {
	t.Parallel()
	h, d := newJobHandlerDB(t)

	storageRoot := t.TempDir()
	cfg, _ := json.Marshal(map[string]string{"path": storageRoot})
	destID, err := d.CreateStorageDestination(db.StorageDestination{
		Name: "cvi-chain-" + nextUnique(), Type: "local", Config: string(cfg),
	})
	if err != nil {
		t.Fatalf("create dest: %v", err)
	}
	jobID, err := d.CreateJob(db.Job{Name: "cvi-chain-job-" + nextUnique(), StorageDestID: destID})
	if err != nil {
		t.Fatalf("create job: %v", err)
	}
	if _, err := d.AddJobItem(db.JobItem{
		JobID: jobID, ItemType: "container", ItemName: "plex", ItemID: "plex",
	}); err != nil {
		t.Fatalf("add job item: %v", err)
	}
	runID, err := d.CreateJobRun(db.JobRun{JobID: jobID, Status: "success"})
	if err != nil {
		t.Fatalf("create run: %v", err)
	}

	fullID, err := d.CreateRestorePoint(db.RestorePoint{
		JobRunID: runID, JobID: jobID, BackupType: "full",
		StoragePath: "rp-full", Metadata: "{}",
	})
	if err != nil {
		t.Fatalf("create full point: %v", err)
	}
	diffID, err := d.CreateRestorePoint(db.RestorePoint{
		JobRunID: runID, JobID: jobID, BackupType: "differential",
		StoragePath: "rp-diff", Metadata: "{}", ParentRestorePointID: fullID,
	})
	if err != nil {
		t.Fatalf("create differential point: %v", err)
	}

	// The full captured two files; the differential re-captured one of them
	// and its effective listing says both are still live.
	fullDir := filepath.Join(storageRoot, "rp-full", "plex")
	diffDir := filepath.Join(storageRoot, "rp-diff", "plex")
	for _, dir := range []string{fullDir, diffDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
		writeJSONFile(t, filepath.Join(dir, "volumes.json"), []volumeManifestFixture{
			{Index: 0, Destination: "/config", BackedUp: true, Archive: "vol-0.tar"},
		})
	}
	writeVolumeIndex(t, fullDir, "vol-0.tar", "settings.yml", "stale.yml")
	writeVolumeIndex(t, diffDir, "vol-0.tar", "settings.yml")

	// The listing sidecar is volume-relative, exactly as the engine writes it.
	listing := engine.TarIndex{Version: 1, Archive: "vol-0.tar", Files: []engine.TarIndexEntry{
		{Path: "settings.yml", Size: 10, Mode: "0644", ModTime: "2026-01-01T00:00:00Z"},
	}}
	writeJSONFile(t, filepath.Join(diffDir, "vol-0.tar"+engine.ListingSuffix), listing)

	url := fmt.Sprintf("/api/v1/jobs/%d/restore-points/%d/contents?item=plex", jobID, diffID)
	got := decodeIndex(t, browseContentsAt(t, h, url, jobID, diffID))

	// stale.yml is absent from the newest listing, so the prune drops it —
	// but settings.yml must survive, in container-absolute form.
	assertPaths(t, indexPaths(got), "/config/settings.yml")
}

// TestContainerVolumeIndex_IncompleteListingSkipsPrune guards the other half of
// the chain prune: when a volume's listing sidecar is missing, the merged
// listing under-reports, and pruning against it would delete every inherited
// path for that volume. The union stays instead.
func TestContainerVolumeIndex_IncompleteListingSkipsPrune(t *testing.T) {
	t.Parallel()
	h, d := newJobHandlerDB(t)

	storageRoot := t.TempDir()
	cfg, _ := json.Marshal(map[string]string{"path": storageRoot})
	destID, err := d.CreateStorageDestination(db.StorageDestination{
		Name: "cvi-part-" + nextUnique(), Type: "local", Config: string(cfg),
	})
	if err != nil {
		t.Fatalf("create dest: %v", err)
	}
	jobID, err := d.CreateJob(db.Job{Name: "cvi-part-job-" + nextUnique(), StorageDestID: destID})
	if err != nil {
		t.Fatalf("create job: %v", err)
	}
	if _, err := d.AddJobItem(db.JobItem{
		JobID: jobID, ItemType: "container", ItemName: "plex", ItemID: "plex",
	}); err != nil {
		t.Fatalf("add job item: %v", err)
	}
	runID, err := d.CreateJobRun(db.JobRun{JobID: jobID, Status: "success"})
	if err != nil {
		t.Fatalf("create run: %v", err)
	}
	fullID, err := d.CreateRestorePoint(db.RestorePoint{
		JobRunID: runID, JobID: jobID, BackupType: "full",
		StoragePath: "rp-full", Metadata: "{}",
	})
	if err != nil {
		t.Fatalf("create full point: %v", err)
	}
	diffID, err := d.CreateRestorePoint(db.RestorePoint{
		JobRunID: runID, JobID: jobID, BackupType: "differential",
		StoragePath: "rp-diff", Metadata: "{}", ParentRestorePointID: fullID,
	})
	if err != nil {
		t.Fatalf("create differential point: %v", err)
	}

	// Two volumes in both steps; the differential has a listing sidecar for
	// only one of them.
	fullDir := filepath.Join(storageRoot, "rp-full", "plex")
	diffDir := filepath.Join(storageRoot, "rp-diff", "plex")
	for _, dir := range []string{fullDir, diffDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
		writeJSONFile(t, filepath.Join(dir, "volumes.json"), []volumeManifestFixture{
			{Index: 0, Destination: "/config", BackedUp: true, Archive: "vol-0.tar"},
			{Index: 1, Destination: "/transcode", BackedUp: true, Archive: "vol-1.tar"},
		})
		writeVolumeIndex(t, dir, "vol-0.tar", "settings.yml")
		writeVolumeIndex(t, dir, "vol-1.tar", "tmp.bin")
	}
	writeJSONFile(t, filepath.Join(diffDir, "vol-0.tar"+engine.ListingSuffix),
		engine.TarIndex{Version: 1, Archive: "vol-0.tar", Files: []engine.TarIndexEntry{
			{Path: "settings.yml", Size: 10, Mode: "0644", ModTime: "2026-01-01T00:00:00Z"},
		}})

	url := fmt.Sprintf("/api/v1/jobs/%d/restore-points/%d/contents?item=plex", jobID, diffID)
	got := decodeIndex(t, browseContentsAt(t, h, url, jobID, diffID))

	// /transcode/tmp.bin has no listing to vouch for it, so the prune is
	// skipped entirely rather than dropping it.
	assertPaths(t, indexPaths(got), "/config/settings.yml", "/transcode/tmp.bin")
}
