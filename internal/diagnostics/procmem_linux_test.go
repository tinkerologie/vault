//go:build linux

package diagnostics

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReadProcRSSLive(t *testing.T) {
	rss, peak := readProcRSS()
	if rss == 0 || peak < rss {
		t.Fatalf("rss=%d peak=%d, want rss > 0 and peak >= rss", rss, peak)
	}
	if readHostMemTotal() == 0 {
		t.Fatal("MemTotal = 0")
	}
}

func TestReadProcCPUFixture(t *testing.T) {
	dir := t.TempDir()
	prev := procStatPath
	t.Cleanup(func() { procStatPath = prev })

	stat := filepath.Join(dir, "stat")
	// comm contains spaces and a parenthesis; utime=250, stime=50 ticks.
	body := "4242 (vault (x) d) S 1 4242 4242 0 -1 4194560 100 0 0 0 250 50 0 0 20 0 12 0 1 1 1\n"
	if err := os.WriteFile(stat, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	procStatPath = stat
	if got := readProcCPU(); got != 3 {
		t.Fatalf("readProcCPU() = %v, want 3", got)
	}
	for _, bad := range []string{"no paren", "1 (x) S 1 2", "1 (x) S 1 2 3 4 5 6 7 8 9 10 a b"} {
		if err := os.WriteFile(stat, []byte(bad), 0o600); err != nil {
			t.Fatal(err)
		}
		if got := readProcCPU(); got != 0 {
			t.Fatalf("readProcCPU(%q) = %v, want 0", bad, got)
		}
	}
	procStatPath = filepath.Join(dir, "missing")
	if got := readProcCPU(); got != 0 {
		t.Fatalf("missing file: %v", got)
	}
}

func TestReadKBFieldsFixture(t *testing.T) {
	dir := t.TempDir()
	status := filepath.Join(dir, "status")
	body := "Name:\tvault\nVmHWM:\t  204800 kB\nVmRSS:\t  102400 kB\nThreads:\t12\nVmSwap:\tbogus kB\n"
	if err := os.WriteFile(status, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	prev := procStatusPath
	procStatusPath = status
	t.Cleanup(func() { procStatusPath = prev })

	rss, peak := readProcRSS()
	if rss != 100<<20 || peak != 200<<20 {
		t.Fatalf("rss=%d peak=%d", rss, peak)
	}
	if got := readKBFields(status, "VmSwap", "Threads"); len(got) != 0 {
		t.Fatalf("malformed keys parsed: %v", got)
	}
	if got := readKBFields(filepath.Join(dir, "missing"), "VmRSS"); len(got) != 0 {
		t.Fatalf("missing file parsed: %v", got)
	}
}
