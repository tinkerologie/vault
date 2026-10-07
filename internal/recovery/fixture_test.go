package recovery

// End-to-end tests: real backups made by the runner, recovered through this
// package with no daemon database.
//
// The same scenarios run in two modes:
//   - TestRecoverRunnerBackups builds the backups in a temp dir and recovers
//     them on the same machine (every non-Windows run).
//   - In CI the backups are built on Linux (TestWriteRecoveryFixture, with
//     VAULT_RECOVERY_FIXTURE_OUT set), archived, and recovered on a Windows
//     runner (TestRecoverFixture, with VAULT_RECOVERY_FIXTURE set). That is
//     the only way to check Linux-only names (colons, CON, case twins)
//     really come out right on Windows.

import (
	"archive/tar"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/ruaan-deysel/vault/internal/crypto"
	"github.com/ruaan-deysel/vault/internal/db"
	"github.com/ruaan-deysel/vault/internal/runner"
	"github.com/ruaan-deysel/vault/internal/ws"
)

const fixturePassphrase = "correct horse battery staple"

// scenario is one recovery check. Paths are item-relative backup paths.
type scenario struct {
	Name    string            `json:"name"`
	Storage string            `json:"storage"` // directory under the fixture root
	Point   string            `json:"point"`   // point reference, e.g. "<job>/latest"
	Item    string            `json:"item"`
	Want    map[string]string `json:"want"`   // path -> content
	Absent  []string          `json:"absent"` // paths that must not be recovered
	// LinuxOnly lists Want paths that only Linux can create, which a Windows
	// run must see renamed.
	LinuxOnly []string `json:"linux_only,omitempty"`
	// Metadata lists engine files recovered beside the tree (under
	// _vault-metadata) that must exist too.
	Metadata []string `json:"metadata,omitempty"`
	// RelLinks are relative symlinks inside the item (path -> target) that
	// must be recreated where the OS allows; AbsLinks must always be skipped.
	RelLinks map[string]string `json:"rel_links,omitempty"`
	AbsLinks []string          `json:"abs_links,omitempty"`
}

type fixture struct {
	Scenarios []scenario `json:"scenarios"`
}

func fixtureServerKey() []byte {
	k := make([]byte, crypto.ServerKeySize)
	for i := range k {
		k[i] = byte(i*7 + 3)
	}
	return k
}

// sourceTree is the first backup's content. It includes names Windows cannot
// store; the case twin is only added where the filesystem can hold both.
func sourceTree(dir string) (files map[string]string, linuxOnly []string) {
	files = map[string]string{
		"plain.txt":        "version one\n",
		"dir/nested.txt":   "deleted before the second backup\n",
		"dir/keep.txt":     "kept\n",
		"unicode-ü.txt":    "ünïcödé\n",
		"empty.txt":        "",
		"deep/a/b/c/d.bin": strings.Repeat("0123456789abcdef", 4096),
		"Mixed/Upper.txt":  "upper\n",
		"logs/run:01.log":  "colon in name\n",
		"logs/what?.txt":   "question mark\n",
		"CON.txt":          "device name\n",
		"trailing-dot.":    "trailing dot\n",
	}
	linuxOnly = []string{"logs/run:01.log", "logs/what?.txt", "CON.txt", "trailing-dot."}
	// A case twin of Mixed/Upper.txt. Which of the pair keeps its name
	// depends on extraction order, so checkScenario asserts they land on
	// different paths rather than that a particular one was renamed.
	if runtime.GOOS == "linux" {
		files["mixed/upper.txt"] = "lower twin\n"
	}
	return files, linuxOnly
}

func writeTree(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for p, content := range files {
		full := filepath.Join(dir, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// backupJob creates a folder job on a local destination and runs it once per
// step, mutating the source between runs.
func backupJob(t *testing.T, storageDir, jobName, chain, encryption string, dedupDest bool, steps []func(src string)) {
	t.Helper()
	if err := os.MkdirAll(storageDir, 0o755); err != nil {
		t.Fatal(err)
	}
	database, err := db.Open(filepath.Join(t.TempDir(), "vault.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if encryption == "age" {
		if err := database.SetSetting("encryption_passphrase", fixturePassphrase); err != nil {
			t.Fatal(err)
		}
	}
	hub := ws.NewHub()
	go hub.Run()
	r := runner.New(database, hub, fixtureServerKey())

	cfg, _ := json.Marshal(map[string]string{"path": storageDir})
	destID, err := database.CreateStorageDestination(db.StorageDestination{
		Name: "fixture", Type: "local", Config: string(cfg), DedupEnabled: dedupDest,
	})
	if err != nil {
		t.Fatal(err)
	}
	jobID, err := database.CreateJob(db.Job{
		Name: jobName, StorageDestID: destID, BackupTypeChain: chain,
		Encryption: encryption, Compression: "zstd", Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	src := t.TempDir()
	settings, _ := json.Marshal(map[string]any{"path": src})
	if _, err := database.AddJobItem(db.JobItem{JobID: jobID, ItemType: "folder", ItemName: "src", Settings: string(settings)}); err != nil {
		t.Fatal(err)
	}
	for i, step := range steps {
		if i > 0 {
			time.Sleep(1100 * time.Millisecond) // run folders are named by the second
		}
		step(src)
		r.RunJob(jobID)
		runs, err := database.GetJobRuns(jobID, 1)
		if err != nil || len(runs) == 0 || runs[0].Status != "completed" {
			t.Fatalf("backup run %d of %s did not complete: %+v, %v", i+1, jobName, runs, err)
		}
	}
}

// buildFixture makes every scenario's backups under root.
func buildFixture(t *testing.T, root string) fixture {
	t.Helper()
	first, linuxOnly := sourceTree("")
	final := map[string]string{}
	for k, v := range first {
		final[k] = v
	}
	final["plain.txt"] = "version two, after the incremental\n"
	final["new.txt"] = "added in the incremental\n"
	delete(final, "dir/nested.txt")

	steps := []func(string){
		func(src string) {
			writeTree(t, src, first)
			if runtime.GOOS != "windows" {
				_ = os.MkdirAll(filepath.Join(src, "links"), 0o755)
				if err := os.Symlink("../plain.txt", filepath.Join(src, "links", "rel")); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink("/etc/hosts", filepath.Join(src, "links", "abs")); err != nil {
					t.Fatal(err)
				}
			}
		},
		func(src string) {
			writeTree(t, src, map[string]string{"plain.txt": final["plain.txt"], "new.txt": final["new.txt"]})
			// Make sure the incremental sees the change even on coarse mtimes.
			future := time.Now().Add(2 * time.Second)
			_ = os.Chtimes(filepath.Join(src, "plain.txt"), future, future)
			if err := os.Remove(filepath.Join(src, "dir", "nested.txt")); err != nil {
				t.Fatal(err)
			}
		},
	}

	classic := filepath.Join(root, "classic")
	dedupDir := filepath.Join(root, "dedup")
	for _, d := range []string{classic, dedupDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	backupJob(t, classic, "Encrypted Chain", "incremental", "age", false, steps)
	backupJob(t, dedupDir, "Dedup Folder", "incremental", "none", true, steps)

	return fixture{Scenarios: []scenario{
		{Name: "classic encrypted incremental chain", Storage: "classic", Point: "Encrypted Chain/latest",
			Item: "src", Want: final, Absent: []string{"dir/nested.txt"}, LinuxOnly: linuxOnly,
			Metadata: []string{metadataDir + "/folder_meta.json"},
			RelLinks: map[string]string{"links/rel": "../plain.txt"}, AbsLinks: []string{"links/abs"}},
		{Name: "dedup", Storage: "dedup", Point: "Dedup Folder/latest",
			Item: "src", Want: final, Absent: []string{"dir/nested.txt"}, LinuxOnly: linuxOnly},
	}}
}

// TestWriteRecoveryFixture writes the fixture for a Windows CI job to recover.
func TestWriteRecoveryFixture(t *testing.T) {
	out := os.Getenv("VAULT_RECOVERY_FIXTURE_OUT")
	if out == "" {
		t.Skip("set VAULT_RECOVERY_FIXTURE_OUT to write the cross-platform fixture")
	}
	fx := buildFixture(t, out)
	body, _ := json.MarshalIndent(fx, "", "  ")
	if err := os.WriteFile(filepath.Join(out, "fixture.json"), body, 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestRecoverFixture recovers a fixture built elsewhere (Linux, in CI).
func TestRecoverFixture(t *testing.T) {
	root := os.Getenv("VAULT_RECOVERY_FIXTURE")
	if root == "" {
		t.Skip("set VAULT_RECOVERY_FIXTURE to recover a pre-built fixture")
	}
	body, err := os.ReadFile(filepath.Join(root, "fixture.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fx fixture
	if err := json.Unmarshal(body, &fx); err != nil {
		t.Fatal(err)
	}
	for _, sc := range fx.Scenarios {
		t.Run(sc.Name, func(t *testing.T) { checkScenario(t, root, sc, runtime.GOOS == "windows") })
	}
}

// TestRecoverRunnerBackups is the single-machine version of the above.
func TestRecoverRunnerBackups(t *testing.T) {
	if testing.Short() {
		t.Skip("makes real backups")
	}
	if runtime.GOOS == "windows" {
		t.Skip("Windows recovers a Linux-built fixture instead (TestRecoverFixture)")
	}
	root := t.TempDir()
	fx := buildFixture(t, root)
	for _, sc := range fx.Scenarios {
		t.Run(sc.Name, func(t *testing.T) { checkScenario(t, root, sc, false) })
		// Windows naming rules, exercised here too so they are covered on
		// every platform and not only in the Windows CI job.
		t.Run(sc.Name+" with Windows-safe names", func(t *testing.T) { checkScenario(t, root, sc, true) })
	}

	// --raw on a chain keeps every step's archives, one folder per run.
	t.Run("raw chain keeps every step", func(t *testing.T) {
		s := openFixture(t, filepath.Join(root, "classic"))
		p, err := s.FindPoint("Encrypted Chain/latest")
		if err != nil {
			t.Fatal(err)
		}
		chain, _ := s.chain(p)
		rep, err := s.Extract(context.Background(), p, ExtractOptions{Dest: t.TempDir(), Raw: true})
		if err != nil || rep.Failed() {
			t.Fatalf("raw: %+v, %v", rep, err)
		}
		if len(chain) < 2 {
			t.Fatalf("chain has %d steps, want a full plus an incremental", len(chain))
		}
		for _, step := range chain {
			archive := filepath.Join(rep.Items[0].Dir, path.Base(step.StoragePath), "data.tar")
			if _, err := os.Stat(archive); err != nil {
				t.Errorf("raw chain: %s missing: %v", archive, err)
			}
		}
	})
}

func openFixture(t *testing.T, storageDir string) *Session {
	t.Helper()
	cfg, _ := json.Marshal(map[string]string{"path": storageDir})
	s, err := Open(Options{StorageType: "local", StorageConfig: string(cfg), ServerKey: fixtureServerKey(), Passphrase: fixturePassphrase})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	return s
}

// checkScenario recovers one scenario and checks contents, file data, deleted
// files, the report, and that storage was not modified. safeNames forces the
// Windows naming rules, which must then rename every Linux-only path.
func checkScenario(t *testing.T, root string, sc scenario, safeNames bool) {
	storageDir := filepath.Join(root, sc.Storage)
	before := snapshotTree(t, storageDir)
	s := openFixture(t, storageDir)

	p, err := s.FindPoint(sc.Point)
	if err != nil {
		t.Fatal(err)
	}

	entries, err := s.Contents(p, sc.Item)
	if err != nil {
		t.Fatalf("Contents: %v", err)
	}
	listed := map[string]bool{}
	for _, e := range entries {
		listed[e.Path] = true
	}
	for want := range sc.Want {
		if !listed[want] {
			t.Errorf("Contents is missing %q", want)
		}
	}
	for _, gone := range sc.Absent {
		if listed[gone] {
			t.Errorf("Contents lists %q, which was deleted before this backup", gone)
		}
	}

	dest := t.TempDir()
	rep, err := s.Extract(context.Background(), p, ExtractOptions{Items: []string{sc.Item}, Dest: dest, SafeNames: &safeNames})
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if rep.Failed() {
		t.Fatalf("Extract reported a failure: %+v", rep)
	}
	ir := rep.Items[0]
	for src, want := range sc.Want {
		local := filepath.Join(ir.Dir, filepath.FromSlash(applyRenames(src, ir.Renamed)))
		got, err := os.ReadFile(local)
		if err != nil {
			t.Errorf("%s: expected at %s: %v", src, local, err)
			continue
		}
		if string(got) != want {
			t.Errorf("%s: content %q, want %q", src, got, want)
		}
	}
	for _, gone := range sc.Absent {
		if _, err := os.Lstat(filepath.Join(ir.Dir, filepath.FromSlash(gone))); err == nil {
			t.Errorf("%s was recovered but had been deleted before this backup", gone)
		}
	}
	for _, meta := range sc.Metadata {
		if _, err := os.Stat(filepath.Join(ir.Dir, filepath.FromSlash(meta))); err != nil {
			t.Errorf("metadata file %s was not recovered: %v", meta, err)
		}
	}
	skipped := map[string]bool{}
	for _, sk := range ir.Skipped {
		skipped[sk.Path] = true
	}
	for link, target := range sc.RelLinks {
		if runtime.GOOS == "windows" {
			if !skipped[link] {
				t.Errorf("%s: symlink should be reported as skipped on Windows", link)
			}
			continue
		}
		got, err := os.Readlink(filepath.Join(ir.Dir, filepath.FromSlash(applyRenames(link, ir.Renamed))))
		if err != nil || got != target {
			t.Errorf("%s: symlink = %q, %v; want -> %q", link, got, err, target)
		}
	}
	for _, link := range sc.AbsLinks {
		if !skipped[link] {
			t.Errorf("%s: absolute symlink was not reported as skipped", link)
		}
		if _, err := os.Lstat(filepath.Join(ir.Dir, filepath.FromSlash(link))); err == nil {
			t.Errorf("%s: absolute symlink was recreated", link)
		}
	}
	if want := len(sc.Want) + len(sc.Metadata); ir.Files != want {
		t.Errorf("report says %d files, want %d (skipped: %+v)", ir.Files, want, ir.Skipped)
	}
	if safeNames {
		seen := map[string]string{}
		for src := range sc.Want {
			local := strings.ToLower(applyRenames(src, ir.Renamed))
			if other, dup := seen[local]; dup {
				t.Errorf("%s and %s both map to %s, which Windows treats as one name", src, other, local)
			}
			seen[local] = src
		}
		renamed := map[string]bool{}
		for _, r := range ir.Renamed {
			renamed[r.From] = true
		}
		for _, p := range sc.LinuxOnly {
			if !renamed[p] {
				t.Errorf("%s cannot exist on Windows but was not reported as renamed", p)
			}
		}
	}

	if after := snapshotTree(t, storageDir); after != before {
		t.Fatal("recovery changed the backup storage")
	}
}

// applyRenames maps a backup path to its local path using an extraction
// report, independently of the namer that produced the report.
func applyRenames(src string, renames []Rename) string {
	to := make(map[string]string, len(renames))
	for _, r := range renames {
		to[r.From] = r.To
	}
	local := ""
	for i, part := range strings.Split(src, "/") {
		prefix := strings.Join(strings.Split(src, "/")[:i+1], "/")
		if r, ok := to[prefix]; ok {
			local = r
			continue
		}
		local = path.Join(local, part)
	}
	return local
}

// snapshotTree fingerprints every file under dir (path, size, content hash).
func snapshotTree(t *testing.T, dir string) string {
	t.Helper()
	h := sha256.New()
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		h.Write([]byte(filepath.ToSlash(rel) + "\n"))
		if d.Type().IsRegular() {
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			sum := sha256.Sum256(b)
			h.Write(sum[:])
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// TestExtractRawAndInclude covers --raw (stored archives copied out, decrypted
// and decompressed) and --include (only the chosen subtree), on one full
// classic encrypted backup and one dedup backup.
func TestExtractRawAndInclude(t *testing.T) {
	if testing.Short() {
		t.Skip("makes real backups")
	}
	if runtime.GOOS == "windows" {
		t.Skip("backups are made on the Unraid server; Windows recovers the Linux-built fixture (TestRecoverFixture)")
	}
	root := t.TempDir()
	files := map[string]string{"dir/keep.txt": "kept\n", "dir/sub/deep.txt": "deep\n", "other.txt": "other\n"}
	write := []func(string){func(src string) { writeTree(t, src, files) }}
	backupJob(t, filepath.Join(root, "classic"), "Raw Job", "full", "age", false, write)
	backupJob(t, filepath.Join(root, "dedup"), "Dedup Job", "full", "none", true, write)

	for _, storageName := range []string{"classic", "dedup"} {
		s := openFixture(t, filepath.Join(root, storageName))
		points, err := s.Points()
		if err != nil || len(points) != 1 {
			t.Fatalf("%s: points = %+v, %v", storageName, points, err)
		}
		dest := t.TempDir()
		rep, err := s.Extract(context.Background(), points[0], ExtractOptions{Dest: dest, Include: []string{"dir"}})
		if err != nil || rep.Failed() {
			t.Fatalf("%s include: %+v, %v", storageName, rep, err)
		}
		ir := rep.Items[0]
		if ir.Files != 2 {
			t.Errorf("%s include: %d files, want 2 (dir/keep.txt, dir/sub/deep.txt)", storageName, ir.Files)
		}
		if _, err := os.Stat(filepath.Join(ir.Dir, "other.txt")); err == nil {
			t.Errorf("%s include: other.txt was extracted outside the included path", storageName)
		}
	}

	s := openFixture(t, filepath.Join(root, "classic"))
	points, _ := s.Points()
	dest := t.TempDir()
	rep, err := s.Extract(context.Background(), points[0], ExtractOptions{Dest: dest, Raw: true})
	if err != nil || rep.Failed() {
		t.Fatalf("raw: %+v, %v", rep, err)
	}
	f, err := os.Open(filepath.Join(rep.Items[0].Dir, "data.tar"))
	if err != nil {
		t.Fatalf("raw extract did not produce a plain data.tar: %v", err)
	}
	defer f.Close()
	if _, err := tar.NewReader(f).Next(); err != nil {
		t.Fatalf("data.tar is not a readable tar (still encrypted or compressed?): %v", err)
	}

	// --raw is meaningless for dedup items and must say so rather than
	// silently writing nothing.
	ds := openFixture(t, filepath.Join(root, "dedup"))
	dp, _ := ds.Points()
	rep, err = ds.Extract(context.Background(), dp[0], ExtractOptions{Dest: t.TempDir(), Raw: true})
	if err != nil || !rep.Failed() {
		t.Fatalf("raw on dedup: %+v, %v; want an item error", rep, err)
	}
}
