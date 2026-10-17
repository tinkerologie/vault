package cli

import (
	"path/filepath"
	"testing"

	"github.com/tinkerologie/vault/internal/db"
	"github.com/tinkerologie/vault/internal/memlimit"
)

func TestApplyMemoryLimit(t *testing.T) {
	d, err := db.Open(filepath.Join(t.TempDir(), "vault.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	t.Cleanup(func() { memlimit.Apply(0) })

	applyMemoryLimit(nil) // nil database is a no-op

	applyMemoryLimit(d)
	if st := memlimit.Current(); st.SettingMB != 0 || st.Source == memlimit.SourceSetting {
		t.Fatalf("default: %+v, want off", st)
	}

	if err := d.SetSetting("memory_limit_mb", "1024"); err != nil {
		t.Fatal(err)
	}
	applyMemoryLimit(d)
	if st := memlimit.Current(); st.SettingMB != 1024 || (!st.EnvironmentOverride && st.LimitBytes != 1024<<20) {
		t.Fatalf("1024: %+v", st)
	}

	// A corrupt stored value falls back to off instead of failing start-up.
	if err := d.SetSetting("memory_limit_mb", "12"); err != nil {
		t.Fatal(err)
	}
	applyMemoryLimit(d)
	if st := memlimit.Current(); st.SettingMB != 0 {
		t.Fatalf("invalid: %+v, want off", st)
	}

	// An unreadable database leaves the current limit alone.
	_ = d.Close()
	applyMemoryLimit(d)
	if st := memlimit.Current(); st.SettingMB != 0 {
		t.Fatalf("closed db: %+v", st)
	}
}
