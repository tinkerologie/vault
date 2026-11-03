package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/tinkerologie/vault/internal/maintlock"
)

var testLockDir string

// Each test binary gets its own lock, so CLI tests never contend with a real
// daemon or with another package's tests on the same machine.
func init() {
	var err error
	testLockDir, err = os.MkdirTemp("", "vault-cli-lock")
	if err != nil {
		panic(err)
	}
	maintlock.SetDirForTest(testLockDir)
}

// CLI GC and repair refuse while a daemon holds the host lock, before opening
// anything, and tell the user what to do instead.
func TestDedupCommandsRefuseWhileDaemonRuns(t *testing.T) {
	saved := dedupDestID
	dedupDestID = 42
	t.Cleanup(func() { dedupDestID = saved })

	release, err := holdMaintenanceLock(lockTestCmd(false))
	if err != nil {
		t.Fatal(err)
	}
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

// With no usable lock nothing can show the other side is excluded: the CLI stops
// and a daemon refuses to start unless explicitly opted out.
func TestDedupLockFailsClosed(t *testing.T) {
	blocked := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocked, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	maintlock.SetDirForTest(blocked)
	t.Cleanup(func() { maintlock.SetDirForTest(testLockDir) })

	if _, err := lockForDedupMaintenance(); err == nil || !strings.Contains(err.Error(), "cannot confirm") {
		t.Fatalf("lock = %v", err)
	}
	if _, err := holdMaintenanceLock(lockTestCmd(false)); err == nil {
		t.Fatal("daemon started without the lock and without opting out")
	}
	release, err := holdMaintenanceLock(lockTestCmd(true))
	if err != nil {
		t.Fatalf("opt-out = %v", err)
	}
	release()
}

func lockTestCmd(skip bool) *cobra.Command {
	cmd := &cobra.Command{}
	cmd.Flags().Bool(noMaintenanceLockFlag, skip, "")
	return cmd
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
