package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tinkerologie/vault/internal/maintlock"
)

// Each test binary gets its own lock, so CLI tests never contend with a real
// daemon or with another package's tests on the same machine.
func init() {
	dir, err := os.MkdirTemp("", "vault-cli-lock")
	if err != nil {
		panic(err)
	}
	maintlock.SetDirForTest(dir)
}

// CLI GC and repair refuse while a daemon holds the host lock, before opening
// anything, and tell the user what to do instead.
func TestDedupCommandsRefuseWhileDaemonRuns(t *testing.T) {
	saved := dedupDestID
	dedupDestID = 42
	t.Cleanup(func() { dedupDestID = saved })

	release := holdMaintenanceLock(context.Background())
	defer release()
	for name, run := range map[string]func() error{
		"gc":     func() error { return runDedupGC(nil, nil) },
		"repair": func() error { return runDedupRepair(nil, nil) },
	} {
		err := run()
		if err == nil || !strings.Contains(err.Error(), "service") {
			t.Errorf("%s = %v, want a refusal naming the running service", name, err)
		}
	}
}

// With no usable lock the CLI cannot show nothing else is writing, so it stops;
// a daemon starts regardless.
func TestDedupLockFailsClosed(t *testing.T) {
	blocked := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocked, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	maintlock.SetDirForTest(blocked)
	t.Cleanup(func() { maintlock.SetDirForTest(t.TempDir()) })

	if _, err := lockForDedupMaintenance(); err == nil || !strings.Contains(err.Error(), "cannot confirm") {
		t.Fatalf("lock = %v", err)
	}
	holdMaintenanceLock(nil)() //nolint:staticcheck // a nil context is what cobra hands a direct Execute
}

// Uninstall callers no longer pin the snapshot path, so the same pool detection
// as daemon startup finds the database, and an explicit path still wins.
func TestUninstallSnapshotPathNotHardCoded(t *testing.T) {
	t.Parallel()
	for _, p := range []string{"../../plugin/vault.plg", "../../ansible/roles/uninstall/tasks/main.yml"} {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(b), "--snapshot-path") {
			t.Errorf("%s passes --snapshot-path", p)
		}
	}
	flag := cleanupUninstallCmd.Flags().Lookup("snapshot-path")
	if flag == nil || flag.DefValue != "" {
		t.Fatalf("snapshot-path default = %+v", flag)
	}
}
