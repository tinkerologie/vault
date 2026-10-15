//go:build linux

package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/alchemillahq/gzfs"
)

// fakeZFSDataset is one dataset/volume/snapshot known to fakeZFS.
type fakeZFSDataset struct {
	name, typ, pool, mountpoint string
}

// fakeZFSPool is one zpool known to fakeZFS, with its leaf device paths.
type fakeZFSPool struct {
	name   string
	leaves []string
}

// fakeZFS is a gzfs.Runner that emulates the handful of zfs/zpool commands
// the ZFS engine issues, entirely in memory (#458). No binary is executed.
type fakeZFS struct {
	mu       sync.Mutex
	datasets []fakeZFSDataset
	pools    []fakeZFSPool
	sendData string

	listErr     map[string]error // keyed by "-t" value ("fs", "vol", "snap"; "" for untyped)
	snapshotErr error
	sendErr     error
	recvErr     error
	zpoolErr    error

	calls     [][]string
	destroyed []string
	received  bytes.Buffer
}

var _ gzfs.Runner = (*fakeZFS)(nil)

func (f *fakeZFS) Run(_ context.Context, stdin io.Reader, stdout, stderr io.Writer, name string, args ...string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, append([]string{name}, args...))

	if name == "zpool" {
		if f.zpoolErr != nil {
			return f.zpoolErr
		}
		return f.writePools(stdout)
	}
	if name != "zfs" || len(args) == 0 {
		return fmt.Errorf("fakeZFS: unexpected command %s %v", name, args)
	}

	switch args[0] {
	case "list":
		typ, target := parseZFSListArgs(args[1:])
		if err := f.listErr[typ]; err != nil {
			return err
		}
		return f.writeDatasets(stdout, typ, target)
	case "snapshot":
		if f.snapshotErr != nil {
			return f.snapshotErr
		}
		full := args[len(args)-1]
		ds, _, _ := strings.Cut(full, "@")
		pool, _, _ := strings.Cut(ds, "/")
		f.datasets = append(f.datasets, fakeZFSDataset{name: full, typ: "SNAPSHOT", pool: pool})
		return nil
	case "send":
		if f.sendErr != nil {
			_, _ = io.WriteString(stderr, "cannot send")
			return f.sendErr
		}
		_, err := io.WriteString(stdout, f.sendData)
		return err
	case "destroy":
		f.destroyed = append(f.destroyed, args[len(args)-1])
		return nil
	case "recv":
		if _, err := io.Copy(&f.received, stdin); err != nil {
			return err
		}
		return f.recvErr
	}
	return fmt.Errorf("fakeZFS: unexpected zfs %v", args)
}

// parseZFSListArgs extracts the -t type and positional target from the
// arguments gzfs passes to "zfs list" (after the "list" verb).
func parseZFSListArgs(args []string) (typ, target string) {
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-o", "-t":
			if args[i] == "-t" && i+1 < len(args) {
				typ = args[i+1]
			}
			i++
		case "-p", "-r", "-j":
		default:
			target = args[i]
		}
	}
	return typ, target
}

var zfsTypeFlag = map[string]string{"FILESYSTEM": "fs", "VOLUME": "vol", "SNAPSHOT": "snap"}

func (f *fakeZFS) writeDatasets(w io.Writer, typ, target string) error {
	type prop struct {
		Value string `json:"value"`
	}
	type ds struct {
		Name       string          `json:"name"`
		Type       string          `json:"type"`
		Pool       string          `json:"pool"`
		Properties map[string]prop `json:"properties"`
	}
	out := map[string]ds{}
	for _, d := range f.datasets {
		if typ != "" && zfsTypeFlag[d.typ] != typ {
			continue
		}
		if target != "" && d.name != target && !strings.HasPrefix(d.name, target+"/") &&
			!strings.HasPrefix(d.name, target+"@") {
			continue
		}
		mp := d.mountpoint
		if mp == "" {
			mp = "-"
		}
		out[d.name] = ds{Name: d.name, Type: d.typ, Pool: d.pool, Properties: map[string]prop{
			"mountpoint": {Value: mp},
			"used":       {Value: "1024"},
			"available":  {Value: "2048"},
		}}
	}
	return json.NewEncoder(w).Encode(map[string]any{"datasets": out})
}

func (f *fakeZFS) writePools(w io.Writer) error {
	type vdev struct {
		Name  string          `json:"name"`
		Path  string          `json:"path,omitempty"`
		Vdevs map[string]vdev `json:"vdevs,omitempty"`
	}
	type pool struct {
		Name  string          `json:"name"`
		Vdevs map[string]vdev `json:"vdevs"`
	}
	out := map[string]pool{}
	for _, p := range f.pools {
		leaves := map[string]vdev{}
		for _, l := range p.leaves {
			leaves[l] = vdev{Name: l, Path: l}
		}
		vdevs := map[string]vdev{}
		if len(leaves) > 0 {
			vdevs["mirror-0"] = vdev{Name: "mirror-0", Vdevs: leaves}
		}
		out[p.name] = pool{Name: p.name, Vdevs: vdevs}
	}
	return json.NewEncoder(w).Encode(map[string]any{"pools": out})
}

func (f *fakeZFS) commands(verb string) [][]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out [][]string
	for _, c := range f.calls {
		if len(c) > 1 && c[0] == "zfs" && c[1] == verb {
			out = append(out, c)
		}
	}
	return out
}

func newFakeZFSHandler(f *fakeZFS) *ZFSHandler {
	return &ZFSHandler{
		client: gzfs.NewClient(gzfs.Options{Runner: f}),
		cmd:    gzfs.Cmd{Bin: "zfs", Runner: f},
	}
}

const oldVaultSnap = "tank/data@vault_backup_20200101_000000"

func fakeZFSWorld() *fakeZFS {
	return &fakeZFS{
		datasets: []fakeZFSDataset{
			{name: "tank", typ: "FILESYSTEM", pool: "tank", mountpoint: "/mnt/tank"},
			{name: "tank/data", typ: "FILESYSTEM", pool: "tank", mountpoint: "/mnt/tank/data"},
			{name: oldVaultSnap, typ: "SNAPSHOT", pool: "tank"},
			{name: "tank/data@manual", typ: "SNAPSHOT", pool: "tank"},
		},
		pools:    []fakeZFSPool{{name: "tank", leaves: []string{"/dev/sda1"}}},
		sendData: "zfs-stream-bytes",
	}
}

func TestNewZFSHandler458(t *testing.T) {
	f := fakeZFSWorld()
	old := zfsRunner
	zfsRunner = f
	t.Cleanup(func() { zfsRunner = old })

	h, err := NewZFSHandler()
	if err != nil {
		t.Fatalf("NewZFSHandler: %v", err)
	}
	if h.cmd.Bin != "zfs" || h.cmd.Runner != f {
		t.Fatalf("cmd = %+v", h.cmd)
	}

	f.listErr = map[string]error{"": errors.New("zfs: command not found")}
	if _, err := NewZFSHandler(); err == nil || !strings.HasPrefix(err.Error(), "zfs not available") {
		t.Fatalf("err = %v", err)
	}
}

func TestZFSListItems458(t *testing.T) {
	t.Parallel()
	f := &fakeZFS{datasets: []fakeZFSDataset{
		{name: "tank/data", typ: "FILESYSTEM", pool: "tank", mountpoint: "/mnt/tank/data"},
		{name: "tank/vm", typ: "VOLUME"},
		{name: "tank/data@snap", typ: "SNAPSHOT", pool: "tank"},
	}}
	items, err := newFakeZFSHandler(f).ListItems()
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("items = %+v", items)
	}
	byName := map[string]BackupItem{}
	for _, it := range items {
		byName[it.Name] = it
	}
	fs, vol := byName["tank/data"].Settings, byName["tank/vm"].Settings
	if fs["type"] != "filesystem" || fs["mounted"] != true || fs["pool"] != "tank" || fs["mountpoint"] != "/mnt/tank/data" {
		t.Fatalf("filesystem settings = %+v", fs)
	}
	if vol["type"] != "volume" || vol["mounted"] != false || vol["pool"] != "tank" {
		t.Fatalf("volume settings = %+v", vol)
	}

	for _, typ := range []string{"fs", "vol"} {
		f := &fakeZFS{listErr: map[string]error{typ: errors.New("boom")}}
		if _, err := newFakeZFSHandler(f).ListItems(); err == nil {
			t.Fatalf("list %s error not reported", typ)
		}
	}
}

func TestZFSBackupFull458(t *testing.T) {
	t.Parallel()
	f := fakeZFSWorld()
	dest := filepath.Join(t.TempDir(), "dest")
	progress := &progressLog{}
	item := BackupItem{Name: "data", Settings: map[string]any{"dataset": "tank/data", "used": float64(8)}}

	res, err := newFakeZFSHandler(f).Backup(context.Background(), item, dest, progress.fn)
	if err != nil {
		t.Fatalf("Backup: %v", err)
	}
	snap, _ := res.Meta["zfs_snapshot"].(string)
	if !res.Success || !strings.HasPrefix(snap, "tank/data@vault_backup_") || res.Meta["zfs_backup_type"] != "full" {
		t.Fatalf("result = %+v", res)
	}
	if _, ok := res.Meta["zfs_parent_snapshot"]; ok {
		t.Fatal("full backup reported a parent")
	}
	if data, _ := os.ReadFile(filepath.Join(dest, zfsSendFile)); string(data) != "zfs-stream-bytes" {
		t.Fatalf("send file = %q", data)
	}
	var meta zfsMeta
	raw, _ := os.ReadFile(filepath.Join(dest, zfsMetaFile))
	if err := json.Unmarshal(raw, &meta); err != nil || meta.Dataset != "tank/data" || meta.Snapshot != snap || meta.BackupType != "full" {
		t.Fatalf("meta = %+v (%v)", meta, err)
	}
	sends := f.commands("send")
	if len(sends) != 1 || !slices.Equal(sends[0][1:], []string{"send", snap}) {
		t.Fatalf("send commands = %v", sends)
	}
	if !slices.Equal(f.destroyed, []string{oldVaultSnap}) {
		t.Fatalf("destroyed = %v, want only the old vault snapshot", f.destroyed)
	}
	if !progress.hasPct(90) || !progress.has("backup complete") {
		t.Fatalf("progress = %+v", progress.entries)
	}
}

func TestZFSBackupIncremental458(t *testing.T) {
	t.Parallel()
	f := fakeZFSWorld()
	item := BackupItem{Name: "tank/data", Settings: map[string]any{
		"backup_type":     "incremental",
		"parent_snapshot": oldVaultSnap,
		"used":            uint64(8),
	}}
	res, err := newFakeZFSHandler(f).Backup(context.Background(), item, t.TempDir(), noProgress)
	if err != nil {
		t.Fatalf("Backup: %v", err)
	}
	if res.Meta["zfs_backup_type"] != "incremental" || res.Meta["zfs_parent_snapshot"] != oldVaultSnap {
		t.Fatalf("meta = %+v", res.Meta)
	}
	sends := f.commands("send")
	if len(sends) != 1 || sends[0][2] != "-i" || sends[0][3] != oldVaultSnap {
		t.Fatalf("send commands = %v", sends)
	}
	if len(f.destroyed) != 0 {
		t.Fatalf("parent snapshot destroyed: %v", f.destroyed)
	}
}

func TestZFSBackupFallsBackToFull458(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		parent  string
		snapErr bool
	}{
		{name: "no parent recorded"},
		{name: "parent vanished", parent: "tank/data@vault_backup_19990101_000000"},
		{name: "snapshot listing fails", parent: oldVaultSnap, snapErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := fakeZFSWorld()
			if tt.snapErr {
				f.listErr = map[string]error{"snap": errors.New("boom")}
			}
			item := BackupItem{Name: "tank/data", Settings: map[string]any{"backup_type": "differential", "parent_snapshot": tt.parent}}
			res, err := newFakeZFSHandler(f).Backup(context.Background(), item, t.TempDir(), noProgress)
			if err != nil {
				t.Fatalf("Backup: %v", err)
			}
			if res.Meta["zfs_backup_type"] != "full" {
				t.Fatalf("type = %v", res.Meta["zfs_backup_type"])
			}
			if sends := f.commands("send"); len(sends) != 1 || len(sends[0]) != 3 {
				t.Fatalf("send = %v, want a full send", sends)
			}
		})
	}
}

func TestZFSBackupErrors458(t *testing.T) {
	t.Parallel()

	t.Run("dest dir", func(t *testing.T) {
		t.Parallel()
		file := filepath.Join(t.TempDir(), "file")
		writeTestFile(t, file, "x")
		_, err := newFakeZFSHandler(fakeZFSWorld()).Backup(context.Background(), BackupItem{Name: "tank/data"}, filepath.Join(file, "sub"), noProgress)
		if err == nil || !strings.HasPrefix(err.Error(), "creating dest dir") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("snapshot", func(t *testing.T) {
		t.Parallel()
		f := fakeZFSWorld()
		f.snapshotErr = errors.New("out of space")
		_, err := newFakeZFSHandler(f).Backup(context.Background(), BackupItem{Name: "tank/data"}, t.TempDir(), noProgress)
		if err == nil || !strings.HasPrefix(err.Error(), "creating snapshot tank/data@vault_backup_") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("send file cannot be created", func(t *testing.T) {
		t.Parallel()
		dest := t.TempDir()
		if err := os.Mkdir(filepath.Join(dest, zfsSendFile), 0o755); err != nil {
			t.Fatal(err)
		}
		_, err := newFakeZFSHandler(fakeZFSWorld()).Backup(context.Background(), BackupItem{Name: "tank/data"}, dest, noProgress)
		if err == nil || !strings.HasPrefix(err.Error(), "creating send file") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("send fails and snapshot is cleaned up", func(t *testing.T) {
		t.Parallel()
		f := fakeZFSWorld()
		f.sendErr = errors.New("exit status 1")
		dest := t.TempDir()
		_, err := newFakeZFSHandler(f).Backup(context.Background(), BackupItem{Name: "tank/data"}, dest, noProgress)
		if err == nil || !strings.HasPrefix(err.Error(), "zfs send (tank/data@vault_backup_") || !strings.HasSuffix(err.Error(), "cannot send") {
			t.Fatalf("err = %v", err)
		}
		if _, statErr := os.Stat(filepath.Join(dest, zfsSendFile)); !os.IsNotExist(statErr) {
			t.Fatal("partial send file left behind")
		}
		if len(f.destroyed) != 1 || !strings.HasPrefix(f.destroyed[0], "tank/data@vault_backup_2") || f.destroyed[0] == oldVaultSnap {
			t.Fatalf("destroyed = %v, want the new snapshot", f.destroyed)
		}
	})

	t.Run("metadata cannot be written", func(t *testing.T) {
		t.Parallel()
		dest := t.TempDir()
		if err := os.Mkdir(filepath.Join(dest, zfsMetaFile), 0o755); err != nil {
			t.Fatal(err)
		}
		_, err := newFakeZFSHandler(fakeZFSWorld()).Backup(context.Background(), BackupItem{Name: "tank/data"}, dest, noProgress)
		if err == nil || !strings.HasPrefix(err.Error(), "writing metadata") {
			t.Fatalf("err = %v", err)
		}
	})
}

func writeZFSBackup(t *testing.T, dataset string) string {
	t.Helper()
	dir := t.TempDir()
	meta, _ := json.Marshal(zfsMeta{Dataset: dataset, Snapshot: dataset + "@vault_backup_x", BackupType: "full"})
	writeTestFile(t, filepath.Join(dir, zfsMetaFile), string(meta))
	writeTestFile(t, filepath.Join(dir, zfsSendFile), "stream-to-receive")
	return dir
}

func TestZFSRestore458(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		settings map[string]any
		want     string
	}{
		{name: "original dataset", want: "tank/data"},
		{name: "restore destination", settings: map[string]any{"restore_destination": "tank/restored", "dataset": "tank/other"}, want: "tank/restored"},
		{name: "dataset override", settings: map[string]any{"dataset": "tank/other"}, want: "tank/other"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := fakeZFSWorld()
			progress := &progressLog{}
			src := writeZFSBackup(t, "tank/data")
			if err := newFakeZFSHandler(f).Restore(context.Background(), BackupItem{Name: "data", Settings: tt.settings}, src, progress.fn); err != nil {
				t.Fatalf("Restore: %v", err)
			}
			recvs := f.commands("recv")
			if len(recvs) != 1 || !slices.Equal(recvs[0][1:], []string{"recv", "-F", tt.want}) {
				t.Fatalf("recv = %v", recvs)
			}
			if f.received.String() != "stream-to-receive" {
				t.Fatalf("received = %q", f.received.String())
			}
			if !progress.hasPct(90) || !progress.has("restore complete") {
				t.Fatalf("progress = %+v", progress.entries)
			}
		})
	}
}

func TestZFSRestoreErrors458(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		setup func(t *testing.T, f *fakeZFS) string
		want  string
	}{
		{
			name:  "missing metadata",
			setup: func(t *testing.T, _ *fakeZFS) string { return t.TempDir() },
			want:  "reading zfs metadata",
		},
		{
			name: "bad metadata",
			setup: func(t *testing.T, _ *fakeZFS) string {
				dir := t.TempDir()
				writeTestFile(t, filepath.Join(dir, zfsMetaFile), "{")
				return dir
			},
			want: "parsing zfs metadata",
		},
		{
			name: "pool listing fails",
			setup: func(t *testing.T, f *fakeZFS) string {
				f.zpoolErr = errors.New("boom")
				return writeZFSBackup(t, "tank/data")
			},
			want: "listing pools",
		},
		{
			name:  "pool missing",
			setup: func(t *testing.T, _ *fakeZFS) string { return writeZFSBackup(t, "nope/data") },
			want:  `destination pool "nope" does not exist`,
		},
		{
			name: "send file missing",
			setup: func(t *testing.T, _ *fakeZFS) string {
				dir := writeZFSBackup(t, "tank/data")
				if err := os.Remove(filepath.Join(dir, zfsSendFile)); err != nil {
					t.Fatal(err)
				}
				return dir
			},
			want: "opening send file",
		},
		{
			name: "receive fails",
			setup: func(t *testing.T, f *fakeZFS) string {
				f.recvErr = errors.New("exit status 1")
				return writeZFSBackup(t, "tank/data")
			},
			want: "zfs receive",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := fakeZFSWorld()
			src := tt.setup(t, f)
			err := newFakeZFSHandler(f).Restore(context.Background(), BackupItem{Name: "data"}, src, noProgress)
			if err == nil || !strings.HasPrefix(err.Error(), tt.want) {
				t.Fatalf("err = %v, want prefix %q", err, tt.want)
			}
		})
	}
}

func TestZFSPoolDiscovery458(t *testing.T) {
	t.Parallel()
	fastMP := t.TempDir()
	slowMP := t.TempDir()
	f := &fakeZFS{
		pools: []fakeZFSPool{
			{name: "fast", leaves: []string{"/dev/nvme0n1p1", "/dev/NVMe1n1p1"}},
			{name: "slow", leaves: []string{"/dev/sda1"}},
			{name: "empty"},
			{name: "noroot", leaves: []string{"/dev/nvme2n1"}},
			{name: "unmounted", leaves: []string{"/dev/nvme3n1"}},
			{name: "vanished", leaves: []string{"/dev/nvme4n1"}},
		},
		datasets: []fakeZFSDataset{
			{name: "fast", typ: "FILESYSTEM", pool: "fast", mountpoint: fastMP},
			{name: "slow", typ: "FILESYSTEM", pool: "slow", mountpoint: slowMP},
			{name: "empty", typ: "FILESYSTEM", pool: "empty", mountpoint: "none"},
			{name: "noroot/child", typ: "FILESYSTEM", pool: "noroot", mountpoint: "/mnt/x"},
			{name: "unmounted", typ: "FILESYSTEM", pool: "unmounted", mountpoint: "legacy"},
			{name: "vanished", typ: "FILESYSTEM", pool: "vanished", mountpoint: filepath.Join(fastMP, "missing")},
		},
	}
	h := newFakeZFSHandler(f)

	nvme, err := h.ListNVMePools()
	if err != nil {
		t.Fatal(err)
	}
	if len(nvme) != 1 || nvme[0].Name != "fast" || nvme[0].Mountpoint != filepath.Clean(fastMP) {
		t.Fatalf("NVMe pools = %+v", nvme)
	}

	all, err := h.ListZFSMountpoints()
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(all))
	for _, p := range all {
		names = append(names, p.Name)
	}
	slices.Sort(names)
	if !slices.Equal(names, []string{"fast", "slow"}) {
		t.Fatalf("mountpoints = %+v", all)
	}

	broken := newFakeZFSHandler(&fakeZFS{zpoolErr: errors.New("boom")})
	if _, err := broken.ListNVMePools(); err == nil || !strings.HasPrefix(err.Error(), "listing zpools") {
		t.Fatalf("ListNVMePools err = %v", err)
	}
	if _, err := broken.ListZFSMountpoints(); err == nil || !strings.HasPrefix(err.Error(), "listing zpools") {
		t.Fatalf("ListZFSMountpoints err = %v", err)
	}

	failing := newFakeZFSHandler(&fakeZFS{listErr: map[string]error{"fs": errors.New("boom")}})
	if _, err := failing.poolMountpoint(context.Background(), "tank"); err == nil || !strings.HasPrefix(err.Error(), "listing datasets for pool tank") {
		t.Fatalf("poolMountpoint err = %v", err)
	}
}

func TestZFSHelpers458(t *testing.T) {
	t.Parallel()

	nested := map[string]*gzfs.ZPoolVDEV{
		"mirror-0": {Vdevs: map[string]*gzfs.ZPoolVDEV{
			"a": {Path: "/dev/nvme0n1"},
			"b": {Path: "/dev/nvme1n1"},
		}},
		"c":     {Path: "/dev/sdc"},
		"blank": {},
	}
	paths := collectLeafVdevPaths(nested)
	slices.Sort(paths)
	if !slices.Equal(paths, []string{"/dev/nvme0n1", "/dev/nvme1n1", "/dev/sdc"}) {
		t.Fatalf("leaf paths = %v", paths)
	}
	if !allNVMe([]string{"/dev/NVME0n1"}) || allNVMe([]string{"/dev/nvme0n1", "/dev/sda"}) {
		t.Fatal("allNVMe mismatch")
	}
	for mp, want := range map[string]bool{
		"": false, "none": false, "-": false, "legacy": false, "relative/path": false, "/": false, "/mnt/tank": true,
	} {
		if got := isValidMountpoint(mp); got != want {
			t.Errorf("isValidMountpoint(%q) = %v, want %v", mp, got, want)
		}
	}

	var buf bytes.Buffer
	var calls int
	cw := &countingWriter{w: &buf, total: 0, name: "x", progress: func(string, int, string) { calls++ }}
	if _, err := cw.Write([]byte("abc")); err != nil || calls != 0 || cw.written != 3 {
		t.Fatalf("countingWriter without total: calls=%d written=%d err=%v", calls, cw.written, err)
	}
	cw.total = 3
	if _, err := cw.Write([]byte("abc")); err != nil || calls != 1 {
		t.Fatalf("countingWriter with total: calls=%d err=%v", calls, err)
	}
	cr := &countingReader{r: strings.NewReader("abc"), name: "x", progress: func(string, int, string) { calls++ }}
	if _, err := io.ReadAll(cr); err != nil || cr.read != 3 || calls != 1 {
		t.Fatalf("countingReader without total: calls=%d read=%d err=%v", calls, cr.read, err)
	}
}

func TestZFSSnapshotHousekeeping458(t *testing.T) {
	t.Parallel()
	f := fakeZFSWorld()
	f.datasets = append(f.datasets, fakeZFSDataset{name: "tank/data/odd", typ: "SNAPSHOT", pool: "tank"})
	h := newFakeZFSHandler(f)

	if h.snapshotExists(context.Background(), "tank/data", "") {
		t.Fatal("empty snapshot name reported as existing")
	}
	if !h.snapshotExists(context.Background(), "tank/data", oldVaultSnap) {
		t.Fatal("existing snapshot not found")
	}

	h.cleanupOldSnapshots(context.Background(), "tank/data", map[string]struct{}{})
	if !slices.Equal(f.destroyed, []string{oldVaultSnap}) {
		t.Fatalf("destroyed = %v, want only the vault snapshot", f.destroyed)
	}
}
