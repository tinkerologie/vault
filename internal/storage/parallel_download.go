package storage

import (
	"context"
	"fmt"
	"io"
	"log"
	"sync"
	"time"
)

// defaultRestorePartSize is the per-part size for parallel ranged downloads.
// Peak memory during a download is roughly partSize * concurrency.
const defaultRestorePartSize = 32 * 1024 * 1024 // 32 MiB

// RestorePartSize is the exported part size so the runner can reference it.
const RestorePartSize = defaultRestorePartSize

// parallelDownloadPolicy bounds per-part retries on transient mid-stream errors.
// It mirrors DefaultRetryPolicy's bounds today but is kept as a separate name so
// the parallel-download path can be tuned independently in the future.
var parallelDownloadPolicy = RetryPolicy{MaxAttempts: 5, BaseDelay: time.Second, MaxDelay: 30 * time.Second}

// ParallelRangeDownload downloads [0,size) of path concurrently in parts of
// partSize, writing each part at its offset via out.WriteAt. It is intended for
// PLAIN objects only (no encryption, no transport compression) — where the
// stored bytes equal the final file bytes — because parts are written
// out-of-order and cannot flow through a sequential decrypt/decompress pipeline.
//
// onBytes is called as bytes are written, to drive progress and the stall
// watchdog heartbeat. A transient per-part failure restarts that part from its
// offset (WriteAt is idempotent at fixed offsets); onBytes may therefore
// slightly over-count on a retried part, which is acceptable for progress.
func ParallelRangeDownload(ctx context.Context, adapter Adapter, path string, out io.WriterAt, size, partSize int64, concurrency int, onBytes func(int64)) error {
	if size <= 0 {
		return nil
	}
	if partSize <= 0 {
		partSize = defaultRestorePartSize
	}
	if concurrency < 1 {
		concurrency = 1
	}

	type part struct{ off, length int64 }
	var parts []part
	for off := int64(0); off < size; off += partSize {
		length := partSize
		if off+length > size {
			length = size - off
		}
		parts = append(parts, part{off, length})
	}

	var (
		sem      = make(chan struct{}, concurrency)
		wg       sync.WaitGroup
		mu       sync.Mutex
		firstErr error
	)
	setErr := func(e error) {
		mu.Lock()
		if firstErr == nil {
			firstErr = e
		}
		mu.Unlock()
	}
	failed := func() bool {
		mu.Lock()
		defer mu.Unlock()
		return firstErr != nil
	}

	for _, pt := range parts {
		if failed() || ctx.Err() != nil {
			break
		}
		// One extra part may start if a failure is recorded (or ctx is
		// cancelled) while this send is blocked on a full semaphore; that part
		// aborts quickly on its own ctx check in downloadPart. Deliberate
		// trade-off to keep the dispatch loop simple.
		sem <- struct{}{}
		wg.Add(1)
		go func(pt part) {
			defer wg.Done()
			defer func() { <-sem }()
			// A part-worker panic must fail the download, not kill the
			// daemon (issue #239).
			defer func() {
				if rec := recover(); rec != nil {
					setErr(fmt.Errorf("download part worker at offset %d panicked: %v", pt.off, rec))
				}
			}()
			if err := downloadPart(ctx, adapter, path, out, pt.off, pt.length, onBytes); err != nil {
				setErr(err)
			}
		}(pt)
	}
	wg.Wait()

	if firstErr != nil {
		return firstErr
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return nil
}

// downloadPart fetches [off, off+length) and writes it at off, retrying the
// whole part from its offset on a transient error (bounded).
func downloadPart(ctx context.Context, adapter Adapter, path string, out io.WriterAt, off, length int64, onBytes func(int64)) error {
	for attempt := 1; ; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		rc, err := adapter.ReadRange(path, off, length)
		if err != nil {
			if classify(err) && attempt < parallelDownloadPolicy.MaxAttempts {
				log.Printf("storage: parallel download part %d-%d failed (attempt %d/%d), retrying: %v",
					off, off+length, attempt, parallelDownloadPolicy.MaxAttempts, err)
				time.Sleep(jitteredBackoff(parallelDownloadPolicy, attempt))
				continue
			}
			return fmt.Errorf("range %d-%d: %w", off, off+length, err)
		}
		w := io.MultiWriter(&offsetWriter{out: out, off: off}, &countingWriter{onBytes: onBytes})
		_, copyErr := io.Copy(w, rc)
		_ = rc.Close()
		if copyErr == nil {
			return nil
		}
		if classify(copyErr) && attempt < parallelDownloadPolicy.MaxAttempts {
			log.Printf("storage: parallel download part %d-%d failed (attempt %d/%d), retrying: %v",
				off, off+length, attempt, parallelDownloadPolicy.MaxAttempts, copyErr)
			time.Sleep(jitteredBackoff(parallelDownloadPolicy, attempt))
			continue // restart whole part from off; WriteAt overwrites idempotently
		}
		return fmt.Errorf("copy range %d-%d: %w", off, off+length, copyErr)
	}
}

// offsetWriter adapts an io.WriterAt to a sequential io.Writer anchored at off.
type offsetWriter struct {
	out io.WriterAt
	off int64
}

func (w *offsetWriter) Write(p []byte) (int, error) {
	n, err := w.out.WriteAt(p, w.off)
	w.off += int64(n)
	return n, err
}

// countingWriter reports bytes via onBytes and discards them (used in a
// MultiWriter alongside the real sink).
type countingWriter struct{ onBytes func(int64) }

func (c *countingWriter) Write(p []byte) (int, error) {
	if c.onBytes != nil && len(p) > 0 {
		c.onBytes(int64(len(p)))
	}
	return len(p), nil
}
