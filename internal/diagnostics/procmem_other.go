//go:build !linux

package diagnostics

// readProcRSS is Linux-only; other platforms report zeros.
func readProcRSS() (rss, peak uint64) { return 0, 0 }

// readHostMemTotal is Linux-only; other platforms report 0.
func readHostMemTotal() uint64 { return 0 }

// readProcCPU is Linux-only; other platforms report 0.
func readProcCPU() float64 { return 0 }
