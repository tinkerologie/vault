package storage

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/unix"

	"github.com/ruaan-deysel/vault/internal/safepath"
)

type LocalAdapter struct {
	basePath string
}

func NewLocalAdapter(basePath string) *LocalAdapter {
	return &LocalAdapter{basePath: basePath}
}

func (l *LocalAdapter) fullPath(path string, allowRoot bool) (string, error) {
	fullPath, err := safepath.JoinUnderBase(l.basePath, path, allowRoot)
	if err != nil {
		return "", fmt.Errorf("invalid path %q: %w", path, err)
	}
	// JoinUnderBase is lexical only — a symlink planted under the base could
	// still redirect the operation (including Delete) outside the
	// destination root. Verify the resolved target stays under the resolved
	// base (resolving both sides tolerates bases that legitimately live
	// behind symlinks, e.g. /mnt/user fuse paths).
	if err := verifyNoSymlinkEscape(l.basePath, fullPath); err != nil {
		return "", err
	}
	return fullPath, nil
}

// verifyNoSymlinkEscape resolves the deepest existing ancestor of fullPath
// and rejects the operation when it lands outside the resolved base.
//
// This is static containment, not a TOCTOU-proof boundary: a same-host
// attacker who can swap a checked directory for a symlink between this check
// and the operation can still race it. Closing that would require an
// openat2/RESOLVE_BENEATH rework of every file op; for a single-admin LAN
// daemon where such an attacker already holds filesystem write access, the
// static check is the proportionate defence.
func verifyNoSymlinkEscape(basePath, fullPath string) error {
	resolvedBase, err := filepath.EvalSymlinks(basePath)
	if err != nil {
		// Base missing/unreadable — let the operation itself surface the
		// real error rather than masking it with a containment failure.
		return nil
	}
	p := fullPath
	for {
		resolved, err := filepath.EvalSymlinks(p)
		if err == nil {
			rel, relErr := filepath.Rel(resolvedBase, resolved)
			if relErr != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				return fmt.Errorf("path %q resolves outside the destination root via symlink", fullPath)
			}
			return nil
		}
		parent := filepath.Dir(p)
		if parent == p {
			return nil // nothing of the path exists yet — a fresh write
		}
		p = parent
	}
}

func (l *LocalAdapter) Write(path string, reader io.Reader) error {
	full, err := l.fullPath(path, false)
	if err != nil {
		return err
	}
	dir := filepath.Dir(full)
	if err := os.MkdirAll(dir, 0750); err != nil {
		return fmt.Errorf("create directories: %w", err)
	}
	// Write to a sibling temp file, then atomically rename into place. This
	// avoids leaving a partial file at `full` if Close() or Sync() fails, and
	// avoids the silent no-op of os.Remove on Windows when the file is still
	// open.
	tmp, err := os.CreateTemp(dir, ".vault-tmp-*")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpPath := tmp.Name()
	cleanupTmp := func() { _ = os.Remove(tmpPath) }
	if _, err := io.Copy(tmp, reader); err != nil {
		_ = tmp.Close()
		cleanupTmp()
		return fmt.Errorf("write file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		cleanupTmp()
		return fmt.Errorf("sync file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		cleanupTmp()
		return fmt.Errorf("close file: %w", err)
	}
	if err := os.Rename(tmpPath, full); err != nil {
		cleanupTmp()
		return fmt.Errorf("rename file: %w", err)
	}
	return nil
}

func (l *LocalAdapter) WriteFrom(path string, open func() (io.ReadCloser, error)) error {
	return streamWriteFrom(l, path, open)
}

func (l *LocalAdapter) Read(path string) (io.ReadCloser, error) {
	fullPath, err := l.fullPath(path, false)
	if err != nil {
		return nil, err
	}
	return os.Open(fullPath) // #nosec G304 — fullPath validated by safepath.JoinUnderBase in fullPath()
}

func (l *LocalAdapter) ReadRange(p string, offset, length int64) (io.ReadCloser, error) {
	full, err := l.fullPath(p, false)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(full)
	if err != nil {
		return nil, err
	}
	if offset < 0 || length < 0 {
		return nil, fmt.Errorf("invalid range offset=%d length=%d", offset, length)
	}
	if offset >= info.Size() {
		return nil, fmt.Errorf("offset %d at or past EOF (size=%d)", offset, info.Size())
	}
	f, err := os.Open(full) // #nosec G304 — fullPath validated by safepath.JoinUnderBase in fullPath()
	if err != nil {
		return nil, err
	}
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		_ = f.Close()
		return nil, err
	}
	return &rangeReader{Reader: io.LimitReader(f, length), closer: f}, nil
}

func (l *LocalAdapter) Delete(path string) error {
	fullPath, err := l.fullPath(path, false)
	if err != nil {
		return err
	}
	return os.Remove(fullPath)
}

func (l *LocalAdapter) List(prefix string) ([]FileInfo, error) {
	dir, err := l.fullPath(prefix, true)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var files []FileInfo
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			continue
		}
		files = append(files, FileInfo{
			Path:    filepath.Join(prefix, e.Name()),
			Size:    info.Size(),
			ModTime: info.ModTime(),
			IsDir:   e.IsDir(),
		})
	}
	return files, nil
}

func (l *LocalAdapter) Stat(path string) (FileInfo, error) {
	fullPath, err := l.fullPath(path, false)
	if err != nil {
		return FileInfo{}, err
	}
	info, err := os.Stat(fullPath)
	if err != nil {
		return FileInfo{}, err
	}
	return FileInfo{
		Path:    path,
		Size:    info.Size(),
		ModTime: info.ModTime(),
		IsDir:   info.IsDir(),
	}, nil
}

func (l *LocalAdapter) TestConnection() error {
	info, err := os.Stat(l.basePath)
	if err != nil {
		return fmt.Errorf("path not accessible: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("%s is not a directory", l.basePath)
	}
	testFile := filepath.Join(l.basePath, ".vault_test")
	if err := os.WriteFile(testFile, []byte("test"), 0600); err != nil {
		return fmt.Errorf("not writable: %w", err)
	}
	_ = os.Remove(testFile)
	return nil
}

// GetCapacity reports the filesystem usage of the configured basePath
// via unix.Statfs. Source is always "statfs" — local mounts always
// expose a real quota. The context is honoured before the syscall so
// a caller that has already cancelled its deadline does not pay for
// the kernel call.
//
// The Bsize field is platform-determined and never exceeds int64 in
// practice; the //nolint:gosec annotations match the same pattern in
// internal/diagnostics/collector.go's probeDisk.
func (l *LocalAdapter) GetCapacity(ctx context.Context) (Capacity, error) {
	if err := ctx.Err(); err != nil {
		return Capacity{}, err
	}
	var s unix.Statfs_t
	if err := unix.Statfs(l.basePath, &s); err != nil {
		return Capacity{}, fmt.Errorf("local: statfs %s: %w", l.basePath, err)
	}
	bsize := int64(s.Bsize)          //nolint:gosec,unconvert // Bsize varies (uint32 on Darwin, int64 on Linux); cast is required on Darwin, redundant on Linux
	total := int64(s.Blocks) * bsize //nolint:gosec,unconvert
	free := int64(s.Bavail) * bsize  //nolint:gosec,unconvert
	used := total - free
	if used < 0 {
		used = 0
	}
	return Capacity{
		TotalBytes: total,
		UsedBytes:  used,
		FreeBytes:  free,
		ProbedAt:   time.Now().UTC(),
		Source:     "statfs",
	}, nil
}

// Usage returns the free and total bytes on the filesystem containing
// basePath by delegating to GetCapacity. The context used internally
// is background with no deadline — Usage is a fast syscall.
func (l *LocalAdapter) Usage() (free, total int64, err error) {
	info, err := l.GetCapacity(context.Background())
	if err != nil {
		return 0, 0, err
	}
	return info.FreeBytes, info.TotalBytes, nil
}

// RemoveEmptyDir removes dir if it is empty. os.Remove fails on a non-empty
// directory, which is the desired guard — callers use this to sweep parent
// directories left vacant after the last object under them is deleted.
func (l *LocalAdapter) RemoveEmptyDir(dir string) error {
	full, err := l.fullPath(dir, false)
	if err != nil {
		return err
	}
	return os.Remove(full)
}

var _ Adapter = (*LocalAdapter)(nil)
