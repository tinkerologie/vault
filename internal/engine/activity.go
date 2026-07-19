package engine

import (
	"context"
	"log"
	"os"
	"path/filepath"
	"time"
)

// ActivitySample is the uniform "how busy is this workload?" probe result
// shared by container, VM, and folder sensing (issue #240). Known=false
// means the probe could not measure — callers treat that as idle
// (fail-open, matching the autothrottle precedent) so a stats hiccup never
// blocks backups.
type ActivitySample struct {
	CPUPercent     float64
	NetBytesPerSec float64
	Known          bool
}

// IdleThresholds is the resolved "idle means below this" configuration.
type IdleThresholds struct {
	CPUPercent float64
	NetKbps    float64 // kilobits per second
}

// Active reports whether the sample exceeds the idle thresholds.
func (s ActivitySample) Active(th IdleThresholds) bool {
	if !s.Known {
		return false
	}
	return s.CPUPercent > th.CPUPercent || s.NetBytesPerSec > th.NetKbps*125 // 1 kbps = 125 B/s
}

// folderActivityScanCap bounds the mtime scan so a huge tree cannot stall
// the pre-run gate; an unfinished scan that found nothing recent reports
// Known=false (fail-open).
const folderActivityScanCap = 50_000

// ProbeFolderActivity reports a folder as active when any file beneath it
// (minus exclusions) was modified within idleWindow — the only usable idle
// signal for sources with no process to query. Context cancellation and
// scan-cap exhaustion both degrade to an unknown (fail-open) sample.
func ProbeFolderActivity(ctx context.Context, srcPath string, exclusions []string, idleWindow time.Duration) ActivitySample {
	cutoff := time.Now().Add(-idleWindow)
	visited := 0
	active := false
	complete := true
	_ = filepath.Walk(srcPath, func(p string, info os.FileInfo, err error) error {
		if ctx.Err() != nil {
			complete = false
			return filepath.SkipAll
		}
		if err != nil {
			return nil // unreadable entries can't count as activity
		}
		visited++
		if visited > folderActivityScanCap {
			log.Printf("engine: folder activity scan for %s hit the %d-entry cap — treating as idle (fail-open); recent writes beyond the cap are not seen", srcPath, folderActivityScanCap)
			complete = false
			return filepath.SkipAll
		}
		rel, relErr := filepath.Rel(srcPath, p)
		if relErr != nil || rel == "." {
			return nil
		}
		if shouldExcludePath(rel, exclusions) {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if !info.IsDir() && info.ModTime().After(cutoff) {
			active = true
			return filepath.SkipAll
		}
		return nil
	})
	if active {
		// Represent "recently written" as a definitive active verdict.
		return ActivitySample{CPUPercent: 100, Known: true}
	}
	return ActivitySample{Known: complete}
}
