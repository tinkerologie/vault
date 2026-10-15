package engine

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"
)

// Engine-side progress counting (#456). An itemProgress travels on the
// context of one item's backup or restore; the shared copy loop
// (contextCopy) adds bytes as they move and the archive walkers add files
// as they finish, so a long archive or extract reports real work instead of
// sitting on a milestone percentage.

type itemProgress struct {
	ctx        context.Context
	item       string
	phase      Phase
	bytesTotal int64
	filesTotal int64
	bytes      atomic.Int64
	files      atomic.Int64
	lastReport atomic.Int64 // unix nanos of the last byte-driven report
}

// byteReportInterval coalesces byte reports from tight copy loops (every
// 32 KiB) before they reach the runner. File completions, the phase start,
// and the final report always go through.
const byteReportInterval = 250 * time.Millisecond

type itemProgressKey struct{}

// withItemProgress starts counting phase work for item against the given
// totals (0 = unknown) and returns a context carrying the counter.
func withItemProgress(ctx context.Context, item string, phase Phase, bytesTotal, filesTotal int64) (context.Context, *itemProgress) {
	p := &itemProgress{item: item, phase: phase, bytesTotal: bytesTotal, filesTotal: filesTotal}
	ctx = context.WithValue(ctx, itemProgressKey{}, p)
	p.ctx = ctx
	p.report()
	return ctx, p
}

// ensureItemProgress returns the counter already on ctx — a container that
// backs up several volumes through the folder engine counts them all on one
// counter under the container's name — or starts one with the totals that
// sizeFn reports. sizeFn runs only when a new counter is needed.
func ensureItemProgress(ctx context.Context, item string, phase Phase, sizeFn func() (files, bytes int64)) (context.Context, *itemProgress) {
	if p := itemProgressFrom(ctx); p != nil {
		return ctx, p
	}
	var files, bytes int64
	if sizeFn != nil {
		files, bytes = sizeFn()
	}
	return withItemProgress(ctx, item, phase, bytes, files)
}

// itemProgressFrom returns the counter on ctx, or nil. Methods on a nil
// counter are no-ops, so copy loops can call them unconditionally.
func itemProgressFrom(ctx context.Context) *itemProgress {
	p, _ := ctx.Value(itemProgressKey{}).(*itemProgress)
	return p
}

func (p *itemProgress) addBytes(n int64) {
	if p == nil || n <= 0 {
		return
	}
	done := p.bytes.Add(n)
	now := time.Now().UnixNano()
	final := p.bytesTotal > 0 && done >= p.bytesTotal
	if !final && now-p.lastReport.Load() < int64(byteReportInterval) {
		return
	}
	p.lastReport.Store(now)
	p.report()
}

func (p *itemProgress) addFile() {
	if p == nil {
		return
	}
	p.files.Add(1)
	p.report()
}

func (p *itemProgress) report() {
	ReportDetail(p.ctx, p.item, ProgressDetail{
		Phase:      p.phase,
		BytesDone:  p.bytes.Load(),
		BytesTotal: p.bytesTotal,
		FilesDone:  p.files.Load(),
		FilesTotal: p.filesTotal,
	})
}

// scanTotals walks root with the archive walker's rules — the same
// exclusions, regular files only, symlinks and special files skipped — and
// returns the number and total size of the files a backup would read. With
// a non-zero changedSince, only files modified after it or missing from
// prevPaths count, matching the incremental archive walk's rule. It reports
// a growing file count in the scanning phase and honours ctx cancellation.
// A scan error leaves progress indeterminate but never fails the backup, so
// callers ignore it.
func scanTotals(ctx context.Context, item, root string, exclusions []string, changedSince time.Time, prevPaths map[string]struct{}) (files, bytes int64, err error) {
	ReportDetail(ctx, item, ProgressDetail{Phase: PhaseScanning})
	err = filepath.Walk(root, func(path string, info os.FileInfo, walkErr error) error {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		if walkErr != nil {
			return nil // the archive walk skips these too
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil || rel == "." {
			return nil
		}
		if shouldExcludePath(rel, exclusions) {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		// Same rule as the incremental archive walk: an old mtime means
		// unchanged, unless the file is absent from the previous listing —
		// a new file with a preserved timestamp is still archived (#320).
		if !changedSince.IsZero() && !info.ModTime().After(changedSince) {
			if prevPaths == nil {
				return nil
			}
			if _, existed := prevPaths[rel]; existed {
				return nil
			}
		}
		files++
		bytes += info.Size()
		ReportDetail(ctx, item, ProgressDetail{Phase: PhaseScanning, FilesDone: files, BytesDone: bytes})
		return nil
	})
	return files, bytes, err
}
