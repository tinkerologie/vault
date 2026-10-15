package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/tinkerologie/vault/internal/crypto"
	"github.com/tinkerologie/vault/internal/logx"
)

func runReplica458(t *testing.T, dbPath string, tls bool) error {
	t.Helper()
	t.Cleanup(func() { logx.SetLevelString("info") })
	cmd := &cobra.Command{Use: "replica"}
	cmd.SetContext(context.Background())
	cert, key := "", ""
	if tls {
		dir := t.TempDir()
		cert, key = filepath.Join(dir, "missing.crt"), filepath.Join(dir, "missing.key")
	}
	cmd.Flags().String("db", dbPath, "")
	cmd.Flags().String("addr", "127.0.0.1:0", "")
	cmd.Flags().String("tls-cert", cert, "")
	cmd.Flags().String("tls-key", key, "")
	return replicaCmd.RunE(cmd, nil)
}

func TestReplica458_GracefulShutdown(t *testing.T) {
	prev := shutdownNotifyContext
	t.Cleanup(func() { shutdownNotifyContext = prev })
	gracefulShutdown()

	dbPath := filepath.Join(t.TempDir(), "data", "vault.db")
	if err := runReplica458(t, dbPath, false); err != nil {
		t.Fatalf("replica RunE: %v", err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(dbPath), "vault.key")); err != nil {
		t.Errorf("server key not created: %v", err)
	}
}

func TestReplica458_ListenError(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "vault.db")
	if err := runReplica458(t, dbPath, true); err == nil {
		t.Fatal("expected TLS listen error")
	}
}

func TestReplica458_StartupErrors(t *testing.T) {
	dir := t.TempDir()
	blocker := filepath.Join(dir, "blocker")
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := runReplica458(t, filepath.Join(blocker, "sub", "vault.db"), true); err == nil ||
		!strings.Contains(err.Error(), "creating database directory") {
		t.Errorf("mkdir err = %v", err)
	}

	dbDir := filepath.Join(dir, "isdir.db")
	if err := os.MkdirAll(dbDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := runReplica458(t, dbDir, true); err == nil {
		t.Error("expected db.Open error for directory path")
	}

	keyDir := filepath.Join(dir, "keyerr")
	if err := os.MkdirAll(filepath.Join(keyDir, "vault.key"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := runReplica458(t, filepath.Join(keyDir, "vault.db"), true); err == nil ||
		!strings.Contains(err.Error(), "server key") {
		t.Errorf("key err = %v", err)
	}
}

func TestRecover458_SessionAndLookupErrors(t *testing.T) {
	root := writeRecoverFixture(t)
	dest := t.TempDir()
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"contents no location", []string{"contents", "--point", "Docs/latest", "--item", "src"}, "--path <folder>"},
		{"extract no location", []string{"extract", "--point", "Docs/latest", "--to", dest}, "--path <folder>"},
		{"extract unknown point", []string{"extract", "--path", root, "--point", "Nope/latest", "--to", dest}, "no backups for job"},
		{"contents unknown item", []string{"contents", "--path", root, "--point", "Docs/latest", "--item", "nope"}, "nope"},
		{"missing key file", []string{"list", "--path", root, "--key", filepath.Join(dest, "absent.key")}, "read --key"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := runRecover(t, tc.args...)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want it to contain %q", err, tc.want)
			}
		})
	}
}

func TestRecover458_ListFiltersAndJSON(t *testing.T) {
	root := writeRecoverFixture(t)
	out, _, err := runRecover(t, "list", "--path", root, "--job", "Other")
	if err != nil || !strings.Contains(out, "No Vault backups found") {
		t.Fatalf("filtered list = %q, %v", out, err)
	}
	key := filepath.Join(t.TempDir(), "vault.key")
	if err := os.WriteFile(key, make([]byte, crypto.ServerKeySize), 0o600); err != nil {
		t.Fatal(err)
	}
	out, _, err = runRecover(t, "list", "--path", root, "--key", key)
	if err != nil || !strings.Contains(out, "Docs/") {
		t.Fatalf("list with --key = %q, %v", out, err)
	}
	if _, _, err := runRecover(t, "list", "--path", filepath.Join(root, "does-not-exist")); err == nil {
		t.Error("listing a missing folder should fail")
	}
	out, _, err = runRecover(t, "list", "--path", root, "--json")
	if err != nil || !strings.HasPrefix(strings.TrimSpace(out), "[") {
		t.Fatalf("json list = %q, %v", out, err)
	}
	out, _, err = runRecover(t, "contents", "--path", root, "--point", "Docs/latest", "--item", "src", "--json")
	if err != nil || !strings.Contains(out, "notes/a.txt") {
		t.Fatalf("json contents = %q, %v", out, err)
	}
}

type failWriter458 struct{}

func (failWriter458) Write([]byte) (int, error) { return 0, errors.New("write failed") }

func TestRecover458_ListOutputWriteError(t *testing.T) {
	root := writeRecoverFixture(t)
	recoverType, recoverPath, recoverConfigFile, recoverKeyFile, recoverPassphraseFile = "local", root, "", "", ""
	recoverJSON, recoverVerbose, recoverListJob = false, false, ""
	t.Cleanup(func() { recoverPath = "" })
	cmd := &cobra.Command{}
	cmd.SetOut(failWriter458{})
	cmd.SetErr(failWriter458{})
	if err := runRecoverList(cmd, nil); err == nil {
		t.Fatal("expected tabwriter flush error")
	}
}

func TestValidateConfiguredPaths458_StagingIsFile(t *testing.T) {
	d := openTestDB(t)
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := d.SetSetting("staging_dir_override", file); err != nil {
		t.Fatal(err)
	}
	if err := d.SetSetting("snapshot_path_override", filepath.Join(t.TempDir(), "absent.db")); err != nil {
		t.Fatal(err)
	}
	validateConfiguredPaths(d) // logs warnings only
}

func TestNewestRotatedSnapshots458(t *testing.T) {
	dir := t.TempDir()
	snap := filepath.Join(dir, "vault.db")
	rot := filepath.Join(dir, "rotated")
	if err := os.MkdirAll(filepath.Join(rot, "subdir"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"a.db", "b.db", "c.db", "d.db"} {
		if err := os.WriteFile(filepath.Join(rot, n), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	got := newestRotatedSnapshots("", snap, snap)
	if len(got) != maxRotatedCandidates {
		t.Errorf("got %d candidates, want %d", len(got), maxRotatedCandidates)
	}
}
