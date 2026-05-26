package storage

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
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
	return fullPath, nil
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
	bsize := int64(s.Bsize)            //nolint:gosec // Bsize is platform-determined, fits int64
	total := int64(s.Blocks) * bsize   //nolint:gosec
	free := int64(s.Bavail) * bsize    //nolint:gosec
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

var _ Adapter = (*LocalAdapter)(nil)
