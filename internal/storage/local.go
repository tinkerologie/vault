package storage

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

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

var _ Adapter = (*LocalAdapter)(nil)
