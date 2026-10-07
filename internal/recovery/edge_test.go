package recovery

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// tarEntry is one header (and body, for regular files) of a test archive.
type tarEntry struct {
	name, link, body string
	typ              byte
}

func buildTar(t *testing.T, entries []tarEntry) string {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, e := range entries {
		h := &tar.Header{Name: e.name, Linkname: e.link, Typeflag: e.typ, Mode: 0o644, Size: int64(len(e.body))}
		if e.typ != tar.TypeReg {
			h.Size = 0
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if e.typ == tar.TypeReg {
			_, _ = tw.Write([]byte(e.body))
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}

// extractOne writes one classic run holding item, opens it and extracts.
func extractOne(t *testing.T, item Item, files map[string]string, opts ExtractOptions) (Report, error) {
	t.Helper()
	root := t.TempDir()
	writeClassicRun(t, root, "Job", "2026-10-01_020000", "full", "2026-10-01T02:00:00Z", item, files)
	s := openFixture(t, root)
	p, err := s.FindPoint("Job/latest")
	if err != nil {
		t.Fatal(err)
	}
	if opts.Dest == "" {
		opts.Dest = t.TempDir()
	}
	return s.Extract(context.Background(), p, opts)
}

func skippedPaths(ir ItemReport) map[string]string {
	out := map[string]string{}
	for _, s := range ir.Skipped {
		out[s.Path] = s.Reason
	}
	return out
}

// TestExtractHostileArchiveEntries feeds an archive with entries a backup
// should never contain and checks each is skipped with a reason while the
// legitimate file is still recovered.
func TestExtractHostileArchiveEntries(t *testing.T) {
	archive := buildTar(t, []tarEntry{
		{name: "./ok.txt", body: "ok", typ: tar.TypeReg},
		{name: "../evil.txt", body: "evil", typ: tar.TypeReg},
		{name: "./dev", typ: tar.TypeChar},
		{name: "./hl-out", link: "../../etc/passwd", typ: tar.TypeLink},
		{name: "./hl-missing", link: "./nope", typ: tar.TypeLink},
		{name: "./ok.txt", link: "elsewhere", typ: tar.TypeSymlink},
	})
	rep, err := extractOne(t, Item{Name: "src", Type: "folder"}, map[string]string{"data.tar": archive}, ExtractOptions{})
	if err != nil || rep.Failed() {
		t.Fatalf("Extract: %+v, %v", rep, err)
	}
	ir := rep.Items[0]
	if got, _ := os.ReadFile(filepath.Join(ir.Dir, "ok.txt")); string(got) != "ok" {
		t.Fatalf("ok.txt = %q", got)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(ir.Dir), "evil.txt")); err == nil {
		t.Fatal("../evil.txt was written outside the item")
	}
	skipped := skippedPaths(ir)
	for p, want := range map[string]string{
		"../evil.txt": "escapes",
		"dev":         "special file",
		"hl-out":      "hard link",
		"hl-missing":  "was not extracted",
	} {
		if !strings.Contains(skipped[p], want) {
			t.Errorf("skip reason for %s = %q, want it to mention %q", p, skipped[p], want)
		}
	}
	if runtime.GOOS != "windows" && !strings.Contains(skipped["ok.txt"], "already exists") {
		t.Errorf("symlink over an extracted file: reason = %q", skipped["ok.txt"])
	}
}

// TestExtractClassicIntegrityFailures checks a corrupted or tampered stored
// object fails the item with a clear error.
func TestExtractClassicIntegrityFailures(t *testing.T) {
	item := Item{Name: "src", Type: "folder"}
	t.Run("corrupt archive", func(t *testing.T) {
		rep, err := extractOne(t, item, map[string]string{"data.tar": strings.Repeat("not a tar archive ", 64)}, ExtractOptions{})
		if err != nil || !strings.Contains(rep.Items[0].Error, "read archive") {
			t.Fatalf("rep = %+v, %v; want a read archive error", rep, err)
		}
	})
	t.Run("checksum mismatch", func(t *testing.T) {
		root := t.TempDir()
		writeClassicRun(t, root, "Job", "2026-10-01_020000", "full", "2026-10-01T02:00:00Z", item,
			map[string]string{"data.tar": buildTar(t, []tarEntry{{name: "a.txt", body: "a", typ: tar.TypeReg}})})
		// Record a checksum the stored object does not have.
		mp := filepath.Join(root, "Job", "2026-10-01_020000", "manifest.json")
		var m map[string]any
		body, _ := os.ReadFile(mp)
		_ = json.Unmarshal(body, &m)
		m["checksums"] = map[string]any{"src": map[string]any{"data.tar": strings.Repeat("0", 64)}}
		body, _ = json.Marshal(m)
		_ = os.WriteFile(mp, body, 0o644)

		s := openFixture(t, root)
		p, _ := s.FindPoint("Job/latest")
		rep, err := s.Extract(context.Background(), p, ExtractOptions{Dest: t.TempDir()})
		if err != nil || !strings.Contains(rep.Items[0].Error, "checksum mismatch") {
			t.Fatalf("rep = %+v, %v; want a checksum mismatch", rep, err)
		}
	})
}

// TestExtractDestinationSafety checks extraction refuses to overwrite things
// that are not its own files, even with --overwrite.
func TestExtractDestinationSafety(t *testing.T) {
	item := Item{Name: "src", Type: "folder"}
	archive := buildTar(t, []tarEntry{
		{name: "notes/a.txt", body: "a", typ: tar.TypeReg},
		{name: "ok.txt", body: "ok", typ: tar.TypeReg},
	})
	files := map[string]string{"data.tar": archive}

	t.Run("item path is a file", func(t *testing.T) {
		dest := t.TempDir()
		_ = os.WriteFile(filepath.Join(dest, "src"), nil, 0o600)
		rep, _ := extractOne(t, item, files, ExtractOptions{Dest: dest})
		if !strings.Contains(rep.Items[0].Error, "is not a folder") {
			t.Fatalf("error = %q", rep.Items[0].Error)
		}
	})
	t.Run("write through a symlink", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("creating the test symlink needs elevated rights on Windows")
		}
		dest, outside := t.TempDir(), t.TempDir()
		_ = os.MkdirAll(filepath.Join(dest, "src"), 0o750)
		if err := os.Symlink(outside, filepath.Join(dest, "src", "notes")); err != nil {
			t.Fatal(err)
		}
		rep, _ := extractOne(t, item, files, ExtractOptions{Dest: dest, Overwrite: true})
		if !strings.Contains(rep.Items[0].Error, "symlink") {
			t.Fatalf("error = %q, want a refusal to write through the symlink", rep.Items[0].Error)
		}
		if entries, _ := os.ReadDir(outside); len(entries) != 0 {
			t.Fatal("a file was written through the symlink")
		}
	})
	t.Run("replace a directory with a file", func(t *testing.T) {
		dest := t.TempDir()
		_ = os.MkdirAll(filepath.Join(dest, "src", "ok.txt"), 0o750)
		rep, _ := extractOne(t, item, files, ExtractOptions{Dest: dest, Overwrite: true})
		if !strings.Contains(rep.Items[0].Error, "not a regular file") {
			t.Fatalf("error = %q", rep.Items[0].Error)
		}
	})
	t.Run("item name with a separator", func(t *testing.T) {
		rep, _ := extractOne(t, Item{Name: "a/b", Type: "vm"}, map[string]string{}, ExtractOptions{})
		if !strings.Contains(rep.Items[0].Error, "single folder name") {
			t.Fatalf("error = %q", rep.Items[0].Error)
		}
	})
	t.Run("item with no stored data", func(t *testing.T) {
		root := t.TempDir()
		writeClassicRun(t, root, "Job", "2026-10-01_020000", "full", "2026-10-01T02:00:00Z", Item{Name: "gone", Type: "vm"}, nil)
		_ = os.RemoveAll(filepath.Join(root, "Job", "2026-10-01_020000", "gone"))
		s := openFixture(t, root)
		p, _ := s.FindPoint("Job/latest")
		rep, err := s.Extract(context.Background(), p, ExtractOptions{Dest: t.TempDir()})
		if err != nil || rep.Failed() || !strings.Contains(skippedPaths(rep.Items[0])["gone"], "no data stored") {
			t.Fatalf("rep = %+v, %v", rep, err)
		}
	})
}

// TestExtractOptionErrors covers the up-front refusals.
func TestExtractOptionErrors(t *testing.T) {
	s := &Session{}
	if _, err := s.Extract(context.Background(), Point{Locked: true, LockedBy: "dedup", StoragePath: "J/r"}, ExtractOptions{Dest: t.TempDir()}); err == nil || !strings.Contains(err.Error(), "--key") {
		t.Fatalf("locked point: %v", err)
	}
	if _, err := s.Extract(context.Background(), Point{StoragePath: "J/r"}, ExtractOptions{}); err == nil || !strings.Contains(err.Error(), "destination") {
		t.Fatalf("no destination: %v", err)
	}
	if _, err := s.Extract(context.Background(), Point{StoragePath: "J/r"}, ExtractOptions{Dest: t.TempDir(), Items: []string{"x"}}); err == nil {
		t.Fatal("unknown item accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.Extract(ctx, Point{StoragePath: "J/r", Items: []Item{{Name: "x", Type: "vm"}}}, ExtractOptions{Dest: t.TempDir()}); err == nil {
		t.Fatal("cancelled context did not stop the extraction")
	}
}

// TestExtractChainWithoutListingWarns checks a classic chain whose newest
// run has no effective listing is still replayed, with a note that deleted
// files may reappear, and that a single-file volume lands at its path.
func TestExtractChainWithoutListingWarns(t *testing.T) {
	root := t.TempDir()
	c := Item{Name: "app", Type: "container"}
	vols := `[{"destination":"/etc/app.conf","backed_up":true,"archive":"volume_1.tar","is_file":true}]`
	for i, run := range []string{"2026-10-01_020000", "2026-10-02_020000"} {
		typ, created := "full", "2026-10-01T02:00:00Z"
		if i == 1 {
			typ, created = "incremental", "2026-10-02T02:00:00Z"
		}
		writeClassicRun(t, root, "Apps", run, typ, created, c, map[string]string{
			"volumes.json": vols,
			"volume_1.tar": buildTar(t, []tarEntry{{name: "app.conf", body: "v" + run, typ: tar.TypeReg}}),
		})
	}
	s := openFixture(t, root)
	p, _ := s.FindPoint("Apps/latest")
	var notes []string
	rep, err := s.Extract(context.Background(), p, ExtractOptions{Dest: t.TempDir(), Progress: func(l string) { notes = append(notes, l) }})
	if err != nil || rep.Failed() {
		t.Fatalf("Extract: %+v, %v", rep, err)
	}
	got, err := os.ReadFile(filepath.Join(rep.Items[0].Dir, "etc", "app.conf"))
	if err != nil || string(got) != "v2026-10-02_020000" {
		t.Fatalf("etc/app.conf = %q, %v; want the newest run's content", got, err)
	}
	if !strings.Contains(strings.Join(notes, "\n"), "no file listing") {
		t.Fatalf("notes = %q, want the missing-listing note", notes)
	}
}
