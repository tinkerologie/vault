package safepath

import (
	"fmt"
	"path/filepath"
	"strings"
)

// NormalizeRelative cleans an untrusted relative path and rejects paths that
// escape their eventual base directory.
func NormalizeRelative(path string, allowRoot bool) (string, error) {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		if allowRoot {
			return ".", nil
		}
		return "", fmt.Errorf("path is required")
	}

	if !filepath.IsLocal(trimmed) {
		return "", fmt.Errorf("path must stay within the configured base directory")
	}

	rel := filepath.Clean(trimmed)
	if rel == "." {
		if allowRoot {
			return rel, nil
		}
		return "", fmt.Errorf("path must not point to the root")
	}
	return rel, nil
}

// JoinUnderBase joins an untrusted relative path to a trusted base directory.
func JoinUnderBase(basePath, path string, allowRoot bool) (string, error) {
	rel, err := NormalizeRelative(path, allowRoot)
	if err != nil {
		return "", err
	}
	if rel == "." {
		return filepath.Clean(basePath), nil
	}
	return filepath.Join(filepath.Clean(basePath), rel), nil
}

// NormalizeAbsoluteUnderRoots validates an absolute path against a fixed set of
// trusted root directories and returns the normalized path anchored to one of
// those roots.
func NormalizeAbsoluteUnderRoots(path string, roots []string) (string, error) {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return "", fmt.Errorf("path is required")
	}

	cleaned := filepath.Clean(trimmed)
	if !filepath.IsAbs(cleaned) {
		return "", fmt.Errorf("path must be absolute")
	}

	for _, root := range roots {
		cleanRoot := filepath.Clean(root)
		rel, err := filepath.Rel(cleanRoot, cleaned)
		if err != nil {
			continue
		}
		if rel == "." {
			return cleanRoot, nil
		}
		if filepath.IsLocal(rel) {
			return filepath.Join(cleanRoot, rel), nil
		}
	}

	return "", fmt.Errorf("path must stay within approved roots")
}

// NormalizeComponent validates a single filename component.
func NormalizeComponent(name string) (string, error) {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return "", fmt.Errorf("name is required")
	}
	if strings.Contains(trimmed, "/") || strings.Contains(trimmed, `\\`) {
		return "", fmt.Errorf("name must not contain path separators")
	}
	if trimmed != filepath.Base(trimmed) || !filepath.IsLocal(trimmed) {
		return "", fmt.Errorf("name must be a single local path component")
	}
	return trimmed, nil
}
