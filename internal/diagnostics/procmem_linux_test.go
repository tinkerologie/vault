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
