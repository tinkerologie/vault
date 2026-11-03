package cli

import (
	"archive/tar"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// writeRecoverFixture lays out one unencrypted classic folder backup the way
// the runner stores it, with a name Windows cannot store.
func writeRecoverFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	run := filepath.Join(root, "Docs", "2026-10-01_020000")
	if err := os.MkdirAll(filepath.Join(run, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	manifest, _ := json.Marshal(map[string]any{
		"version": 1, "job_name": "Docs", "backup_type": "full", "encryption": "none",
		"created_at": "2026-10-01T02:00:00Z",
		"items":      []map[string]string{{"name": "src", "type": "folder"}},
	})
	if err := os.WriteFile(filepath.Join(run, "manifest.json"), manifest, 0o644); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for name, body := range map[string]string{"./notes/a.txt": "alpha", "./notes/b:c.txt": "colon"} {
		_ = tw.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(len(body))})
		_, _ = tw.Write([]byte(body))
	}
	_ = tw.Close()
	if err := os.WriteFile(filepath.Join(run, "src", "data.tar"), buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	index, _ := json.Marshal(map[string]any{"version": 1, "archive": "data.tar", "files": []map[string]any{
		{"path": "notes/a.txt", "size": 5, "mode": "0644"},
		{"path": "notes/b:c.txt", "size": 5, "mode": "0644"},
	}})
	if err := os.WriteFile(filepath.Join(run, "src", "data.tar.index.json"), index, 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

// runRecover executes `vault recover <args>` and returns stdout, stderr and
// the error. Flag variables are package globals, so they are reset first.
func runRecover(t *testing.T, args ...string) (string, string, error) {
	t.Helper()
	prevLog := log.Writer()
	t.Cleanup(func() { log.SetOutput(prevLog) })
	recoverType, recoverPath, recoverConfigFile, recoverKeyFile, recoverPassphraseFile = "local", "", "", "", ""
	recoverJSON, recoverVerbose, recoverListJob = false, false, ""
	recoverPoint, recoverItems, recoverIncludes, recoverTo = "", nil, nil, ""
	recoverRaw, recoverOverwrite, recoverSafeNames = false, false, "auto"
	var out, errOut bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&errOut)
	rootCmd.SetArgs(append([]string{"recover"}, args...))
	t.Cleanup(func() {
		rootCmd.SetOut(nil)
		rootCmd.SetErr(nil)
		rootCmd.SetArgs(nil)
	})
	err := rootCmd.Execute()
	return out.String(), errOut.String(), err
}

func TestRecoverCommandsEndToEnd(t *testing.T) {
	root := writeRecoverFixture(t)

	out, _, err := runRecover(t, "list", "--path", root)
	if err != nil || !strings.Contains(out, "Docs/2026-10-01_020000") || !strings.Contains(out, "src (folder)") {
		t.Fatalf("list = %q, %v", out, err)
	}
	out, _, err = runRecover(t, "list", "--path", root, "--json", "--job", "Docs")
	var points []map[string]any
	if err != nil || json.Unmarshal([]byte(out), &points) != nil || len(points) != 1 {
		t.Fatalf("list --json = %q, %v", out, err)
	}
	out, _, err = runRecover(t, "list", "--path", root, "--job", "nope")
	if err != nil || !strings.Contains(out, "No Vault backups") {
		t.Fatalf("list --job nope = %q, %v", out, err)
	}

	out, _, err = runRecover(t, "contents", "--path", root, "--point", "Docs/latest", "--item", "src")
	if err != nil || !strings.Contains(out, "notes/b:c.txt") {
		t.Fatalf("contents = %q, %v", out, err)
	}
	out, _, err = runRecover(t, "contents", "--path", root, "--point", "Docs/latest", "--item", "src", "--json")
	if err != nil || !strings.Contains(out, `"path": "notes/a.txt"`) {
		t.Fatalf("contents --json = %q, %v", out, err)
	}

	dest := t.TempDir()
	out, _, err = runRecover(t, "extract", "--path", root, "--point", "Docs/latest", "--to", dest, "--safe-names", "on")
	if err != nil || !strings.Contains(out, "renamed (1)") || !strings.Contains(out, "notes/b:c.txt -> notes/b_c.txt") {
		t.Fatalf("extract = %q, %v", out, err)
	}
	if got, _ := os.ReadFile(filepath.Join(dest, "src", "notes", "b_c.txt")); string(got) != "colon" {
		t.Fatalf("renamed file content = %q", got)
	}
	reports, _ := filepath.Glob(filepath.Join(dest, "vault-recover-report-*.json"))
	if len(reports) != 1 {
		t.Fatalf("reports = %v, want one", reports)
	}

	// The item folder is not empty now: refused without --overwrite.
	out, _, err = runRecover(t, "extract", "--path", root, "--point", "Docs/latest", "--to", dest)
	if err == nil || !strings.Contains(out, "is not empty") {
		t.Fatalf("second extract = %q, %v; want a not-empty failure", out, err)
	}
	out, _, err = runRecover(t, "extract", "--path", root, "--point", "Docs/latest", "--to", dest,
		"--overwrite", "--include", "notes/a.txt", "--item", "src")
	if err != nil || !strings.Contains(out, "src: 1 files") {
		t.Fatalf("extract --overwrite --include = %q, %v", out, err)
	}
}

func TestRecoverCommandErrors(t *testing.T) {
	root := writeRecoverFixture(t)
	dir := t.TempDir()
	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	badJSON := write("bad.json", "{")
	localCfg, _ := json.Marshal(map[string]string{"path": root})
	goodCfg := write("local.json", string(localCfg))
	shortKey := write("vault.key", "short")
	pass := write("pass.txt", "secret\n")

	cases := []struct {
		name string
		args []string
		want string
	}{
		{"no location", []string{"list"}, "--path <folder> or --config-file"},
		{"path and config", []string{"list", "--path", root, "--config-file", goodCfg}, "not both"},
		{"path for s3", []string{"list", "--type", "s3", "--path", root}, "--path is for local storage"},
		{"invalid config", []string{"list", "--config-file", badJSON}, "not valid JSON"},
		{"missing config", []string{"list", "--config-file", filepath.Join(dir, "none.json")}, "read --config-file"},
		{"nfs", []string{"list", "--type", "nfs", "--config-file", goodCfg}, "does not mount NFS"},
		{"short key", []string{"list", "--path", root, "--key", shortKey}, "unexpected size"},
		{"missing passphrase file", []string{"list", "--path", root, "--passphrase-file", filepath.Join(dir, "none")}, "read --passphrase-file"},
		{"two items", []string{"contents", "--path", root, "--point", "Docs/latest", "--item", "a", "--item", "b"}, "exactly one --item"},
		{"unknown point", []string{"contents", "--path", root, "--point", "Nope/latest", "--item", "src"}, "no backups for job"},
		{"unknown item", []string{"extract", "--path", root, "--point", "Docs/latest", "--item", "nope", "--to", dir}, `an item named "nope"`},
		{"bad safe-names", []string{"extract", "--path", root, "--point", "Docs/latest", "--to", dir, "--safe-names", "maybe"}, "must be auto, on or off"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := runRecover(t, tc.args...)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want it to contain %q", err, tc.want)
			}
		})
	}

	// A config file and passphrase file that are fine work together.
	if out, _, err := runRecover(t, "list", "--config-file", goodCfg, "--passphrase-file", pass); err != nil || !strings.Contains(out, "Docs/") {
		t.Fatalf("list via --config-file = %q, %v", out, err)
	}
}

// TestRecoverCommandOutputDetails covers the display branches: a locked run
// in list, folders in contents, more than 20 renames, skipped entries, and
// --safe-names off.
// Backup metadata is untrusted: it must not reach the terminal raw (#569).
func TestRecoverEscapesBackupMetadata(t *testing.T) {
	const evil = "x\x1b]0;owned\x07\nFAKE"
	const shown = `x\x1b]0;owned\x07\x0aFAKE`
	root := t.TempDir()
	run := filepath.Join(root, "Docs", "2026-10-01_020000")
	_ = os.MkdirAll(filepath.Join(run, "src"), 0o755)
	manifest, _ := json.Marshal(map[string]any{
		"version": 1, "job_name": "Docs", "backup_type": "full", "encryption": "none",
		"created_at": "2026-10-01T02:00:00Z",
		"items":      []map[string]string{{"name": "src", "type": "folder"}, {"name": "item" + evil, "type": "folder"}},
	})
	_ = os.WriteFile(filepath.Join(run, "manifest.json"), manifest, 0o644)
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	name := "a" + evil + ".txt"
	_ = tw.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: 0o644, Size: 1})
	_, _ = tw.Write([]byte("x"))
	_ = tw.WriteHeader(&tar.Header{Name: "abs" + evil, Typeflag: tar.TypeSymlink, Linkname: "/etc/hosts"})
	_ = tw.Close()
	_ = os.WriteFile(filepath.Join(run, "src", "data.tar"), buf.Bytes(), 0o644)
	idx, _ := json.Marshal(map[string]any{"version": 1, "archive": "data.tar", "files": []map[string]any{{"path": name, "size": 1, "mode": "0644"}}})
	_ = os.WriteFile(filepath.Join(run, "src", "data.tar.index.json"), idx, 0o644)

	check := func(what, out, errOut string, err error) {
		t.Helper()
		text := out + errOut
		if err != nil {
			text += err.Error()
		}
		if strings.ContainsAny(text, "\x1b\x07") || strings.Contains(text, "\nFAKE") {
			t.Errorf("%s printed raw control bytes: %q", what, text)
		}
		if !strings.Contains(text, shown) {
			t.Errorf("%s does not show the escaped name: %q", what, text)
		}
	}
	out, errOut, err := runRecover(t, "list", "--path", root)
	check("list", out, errOut, err)
	out, errOut, err = runRecover(t, "contents", "--path", root, "--point", "Docs/latest", "--item", "src")
	check("contents", out, errOut, err)
	out, errOut, err = runRecover(t, "extract", "--path", root, "--point", "Docs/latest", "--to", t.TempDir(), "--safe-names", "on")
	check("extract", out, errOut, err)

	// JSON stays exact for machines: encoding/json already escapes controls.
	out, _, err = runRecover(t, "contents", "--path", root, "--point", "Docs/latest", "--item", "src", "--json")
	if err != nil || strings.ContainsRune(out, 0x1b) || !strings.Contains(out, `\u001b`) {
		t.Errorf("json contents = %q, %v", out, err)
	}
}

func TestSafeRecoverErrors(t *testing.T) {
	if err := safeRecoverErrors(func(*cobra.Command, []string) error { return nil })(nil, nil); err != nil {
		t.Fatal(err)
	}
	err := safeRecoverErrors(func(*cobra.Command, []string) error { return errors.New("no \x1b[2J job") })(nil, nil)
	if err == nil || err.Error() != `no \x1b[2J job` {
		t.Fatalf("err = %q", err)
	}
}

func TestTerminalLogWriter(t *testing.T) {
	var buf bytes.Buffer
	w := terminalLogWriter{&buf}
	in := "name \x1b[2J\nFAKE\n"
	if n, err := w.Write([]byte(in)); err != nil || n != len(in) {
		t.Fatalf("Write = %d, %v", n, err)
	}
	if got, want := buf.String(), `name \x1b[2J\x0aFAKE`+"\n"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	_, _ = w.Write([]byte("no newline"))
	if !strings.HasSuffix(buf.String(), "no newline") {
		t.Errorf("got %q", buf.String())
	}
	if _, err := (terminalLogWriter{failWriter{}}).Write([]byte("x")); err == nil {
		t.Error("write error lost")
	}
	if _, err := (terminalLogWriter{shortWriter{}}).Write([]byte("x")); !errors.Is(err, io.ErrShortWrite) {
		t.Errorf("short write: %v", err)
	}
}

type shortWriter struct{}

func (shortWriter) Write([]byte) (int, error) { return 0, nil }

type failWriter struct{}

func (failWriter) Write([]byte) (int, error) { return 0, errors.New("closed") }

func TestRecoverCommandOutputDetails(t *testing.T) {
	root := t.TempDir()
	run := filepath.Join(root, "Many", "2026-10-01_020000")
	_ = os.MkdirAll(filepath.Join(run, "src"), 0o755)
	manifest, _ := json.Marshal(map[string]any{
		"version": 1, "job_name": "Many", "backup_type": "full", "encryption": "none",
		"created_at": "2026-10-01T02:00:00Z", "items": []map[string]string{{"name": "src", "type": "folder"}},
	})
	_ = os.WriteFile(filepath.Join(run, "manifest.json"), manifest, 0o644)
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	var index []map[string]any
	_ = tw.WriteHeader(&tar.Header{Name: "dir/", Typeflag: tar.TypeDir, Mode: 0o755})
	index = append(index, map[string]any{"path": "dir", "is_dir": true, "mode": "0755"})
	for i := range 21 {
		name := "dir/f:" + string(rune('a'+i)) + ".txt"
		_ = tw.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: 0o644, Size: 1})
		_, _ = tw.Write([]byte("x"))
		index = append(index, map[string]any{"path": name, "size": 1, "mode": "0644"})
	}
	_ = tw.WriteHeader(&tar.Header{Name: "abs-link", Typeflag: tar.TypeSymlink, Linkname: "/etc/hosts"})
	_ = tw.Close()
	_ = os.WriteFile(filepath.Join(run, "src", "data.tar"), buf.Bytes(), 0o644)
	idx, _ := json.Marshal(map[string]any{"version": 1, "archive": "data.tar", "files": index})
	_ = os.WriteFile(filepath.Join(run, "src", "data.tar.index.json"), idx, 0o644)

	locked := filepath.Join(root, "Secret", "2026-10-01_020000")
	_ = os.MkdirAll(locked, 0o755)
	_ = os.WriteFile(filepath.Join(locked, "manifest.json"), []byte(`{"vault_manifest_enc":1,"key":"age","algo":"age","payload":"AAAA"}`), 0o644)

	out, errOut, err := runRecover(t, "list", "--path", root)
	if err != nil || !strings.Contains(out, "locked (needs passphrase)") || !strings.Contains(errOut, "1 backup(s) are locked") {
		t.Fatalf("list = %q / %q, %v", out, errOut, err)
	}
	out, _, err = runRecover(t, "contents", "--path", root, "--point", "Many/latest", "--item", "src")
	if err != nil || !strings.Contains(out, "dir/\n") {
		t.Fatalf("contents = %q, %v; want the folder listed with a trailing slash", out, err)
	}

	out, _, err = runRecover(t, "extract", "--path", root, "--point", "Many/latest", "--to", t.TempDir(), "--safe-names", "on")
	if err != nil || !strings.Contains(out, "renamed (21)") || !strings.Contains(out, "and 1 more") ||
		!strings.Contains(out, "not recreated (1)") || !strings.Contains(out, "abs-link") {
		t.Fatalf("extract = %q, %v", out, err)
	}

	if runtime.GOOS != "windows" {
		dest := t.TempDir()
		out, _, err = runRecover(t, "extract", "--path", root, "--point", "Many/latest", "--to", dest, "--safe-names", "off")
		if err != nil || strings.Contains(out, "renamed") {
			t.Fatalf("--safe-names off = %q, %v; want no renames", out, err)
		}
		if _, err := os.Stat(filepath.Join(dest, "src", "dir", "f:a.txt")); err != nil {
			t.Fatalf("original name not kept: %v", err)
		}
	}
}
