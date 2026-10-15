package runner

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/tinkerologie/vault/internal/db"
)

// a458ProcHeader mimics the two header lines of /proc/net/dev.
const a458ProcHeader = "Inter-|   Receive                                                |  Transmit\n" +
	" face |bytes    packets errs drop fifo frame compressed multicast|bytes    packets errs drop fifo colls carrier compressed\n"

// a458ProcLine renders one /proc/net/dev interface row with the given tx bytes.
func a458ProcLine(name, tx string) string {
	return "  " + name + ": 100 1 0 0 0 0 0 0 " + tx + " 1 0 0 0 0 0 0\n"
}

// a458StubProcNetDev points busiestInterfaceTxBytes at a temp file and
// returns a writer that replaces its content.
func a458StubProcNetDev(t *testing.T) func(content string) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "dev")
	orig := procNetDevPath
	procNetDevPath = p
	t.Cleanup(func() { procNetDevPath = orig })
	return func(content string) {
		if content == "" {
			_ = os.Remove(p)
			return
		}
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatalf("write proc stub: %v", err)
		}
	}
}

func TestBusiestInterfaceTxBytesA458(t *testing.T) {
	write := a458StubProcNetDev(t)

	// Missing file.
	write("")
	if _, _, err := busiestInterfaceTxBytes(); err == nil {
		t.Fatal("missing file: want error")
	}

	// Only virtual / malformed rows: no usable interface.
	write(a458ProcHeader +
		a458ProcLine("lo", "999999") +
		a458ProcLine("veth123", "999999") +
		a458ProcLine("docker0", "999999") +
		a458ProcLine("virbr0", "999999") +
		a458ProcLine("vnet0", "999999") +
		a458ProcLine("tun0", "999999") +
		a458ProcLine("wg0", "999999") +
		"  short: 1 2 3\n" +
		a458ProcLine("bad", "notanumber"))
	if _, _, err := busiestInterfaceTxBytes(); err == nil {
		t.Fatal("no usable interface: want error")
	}

	// The busiest physical interface wins.
	write(a458ProcHeader +
		a458ProcLine("lo", "999999") +
		a458ProcLine("eth0", "100") +
		a458ProcLine("br0", "5000") +
		a458ProcLine("eth1", "20"))
	name, tx, err := busiestInterfaceTxBytes()
	if err != nil || name != "br0" || tx != 5000 {
		t.Fatalf("got (%q, %d, %v), want (br0, 5000, nil)", name, tx, err)
	}
}

// a458LimitRecorder captures every limit the loop applies.
type a458LimitRecorder struct {
	mu     sync.Mutex
	limits []float64
}

func (l *a458LimitRecorder) set(v float64) {
	l.mu.Lock()
	l.limits = append(l.limits, v)
	l.mu.Unlock()
}

func (l *a458LimitRecorder) last() (float64, int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.limits) == 0 {
		return -1, 0
	}
	return l.limits[len(l.limits)-1], len(l.limits)
}

func TestRunAutoThrottleLoopA458(t *testing.T) {
	write := a458StubProcNetDev(t)
	rec := &a458LimitRecorder{}
	origSet := setAutoThrottleLimit
	setAutoThrottleLimit = rec.set
	t.Cleanup(func() { setAutoThrottleLimit = origSet })

	database, err := db.Open(filepath.Join(t.TempDir(), "vault.db"))
	if err != nil {
		t.Fatalf("db open: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	r := New(database, nil, nil)

	set := func(k, v string) {
		t.Helper()
		if err := database.SetSetting(k, v); err != nil {
			t.Fatalf("set %s: %v", k, err)
		}
	}

	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() {
			r.RunAutoThrottleLoop(ctx)
			close(done)
		}()
		tick := func() {
			time.Sleep(5 * time.Second)
			synctest.Wait()
		}

		// Tick 1: disabled → limit cleared.
		set("auto_throttle_enabled", "false")
		tick()
		if v, _ := rec.last(); v != 0 {
			t.Fatalf("disabled: limit %v, want 0", v)
		}

		// Tick 2: enabled but /proc missing → read error, limit cleared.
		set("auto_throttle_enabled", "true")
		set("auto_throttle_link_mbps", "40")
		set("auto_throttle_floor_mbps", "5")
		write("")
		tick()
		if v, _ := rec.last(); v != 0 {
			t.Fatalf("read error: limit %v, want 0", v)
		}

		// Tick 3: first sample establishes the baseline, no new limit.
		write(a458ProcHeader + a458ProcLine("eth0", "1000"))
		_, before := rec.last()
		tick()
		if _, n := rec.last(); n != before {
			t.Fatal("baseline tick must not set a limit")
		}

		// Tick 4: delta on the same interface → limit applied.
		write(a458ProcHeader + a458ProcLine("eth0", "1001000"))
		tick()
		if v, _ := rec.last(); v <= 0 {
			t.Fatalf("delta tick: limit %v, want > 0", v)
		}

		// Tick 5: counter reset (smaller tx) → baseline dropped.
		write(a458ProcHeader + a458ProcLine("eth0", "10"))
		_, before = rec.last()
		tick()
		if _, n := rec.last(); n != before {
			t.Fatal("counter reset must not set a limit")
		}

		// Tick 6: interface change → baseline dropped again.
		write(a458ProcHeader + a458ProcLine("br0", "99999999"))
		_, before = rec.last()
		tick()
		if _, n := rec.last(); n != before {
			t.Fatal("interface change must not set a limit")
		}

		// Tick 7: zero external traffic is clamped at 0 and the
		// limit is capacity minus headroom.
		tick()
		if v, _ := rec.last(); v != autoThrottleTarget(40, 5, 0) {
			t.Fatalf("idle tick: limit %v, want %v", v, autoThrottleTarget(40, 5, 0))
		}

		// Tick 8: link <= 0 while enabled → treated as disabled (logs the
		// transition) and clears the limit.
		set("auto_throttle_link_mbps", "0")
		tick()
		if v, _ := rec.last(); v != 0 {
			t.Fatalf("link 0: limit %v, want 0", v)
		}

		cancel()
		<-done
		if v, _ := rec.last(); v != 0 {
			t.Fatalf("cancel: limit %v, want 0", v)
		}
	})
}
