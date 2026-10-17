//go:build !linux

package diagnostics

import "testing"

func TestProcReadersAreZeroOffLinux(t *testing.T) {
	if rss, peak := readProcRSS(); rss != 0 || peak != 0 {
		t.Fatalf("readProcRSS() = %d, %d; want zeros", rss, peak)
	}
	if got := readHostMemTotal(); got != 0 {
		t.Fatalf("readHostMemTotal() = %d, want 0", got)
	}
	if got := readProcCPU(); got != 0 {
		t.Fatalf("readProcCPU() = %v, want 0", got)
	}
}
