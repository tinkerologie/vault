package storage

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/ruaandeysel/vault/internal/safepath"
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
	if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
		return fmt.Errorf("create directories: %w", err)
	}
	f, err := os.Create(full)
	if err != nil {
		return fmt.Errorf("create file: %w", err)
	}
	defer f.Close()
	if _, err := io.Copy(f, reader); err != nil {
		return fmt.Errorf("write file: %w", err)
	}
	return nil
}

func (l *LocalAdapter) Read(path string) (io.ReadCloser, error) {
	fullPath, err := l.fullPath(path, false)
	if err != nil {
		return nil, err
	}
	return os.Open(fullPath)
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
	if err := os.WriteFile(testFile, []byte("test"), 0644); err != nil {
		return fmt.Errorf("not writable: %w", err)
	}
	_ = os.Remove(testFile)
	return nil
}

var _ Adapter = (*LocalAdapter)(nil)
