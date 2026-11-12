package engine

import (
	"archive/tar"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"

	"github.com/tinkerologie/vault/internal/safepath"

	"github.com/tinkerologie/vault/internal/dedup"
)

var ErrReviewedFolderTargetChanged = errors.New("reviewed folder restore target changed")

// ReviewedFolderTarget retains the trust policy and both sides of path
// resolution. Check at the write boundary to reject retargeted symlinks.
type ReviewedFolderTarget struct {
	Configured string
	Resolved   string
	Original   bool
	Clean      bool
}

func (target ReviewedFolderTarget) Check() error {
	resolved, err := ResolveFolderRestoreTarget(target.Configured, target.Original)
	if err != nil || resolved != target.Resolved || (target.Clean && !CanClearRestoreTarget(resolved)) {
		return ErrReviewedFolderTargetChanged
	}
	return nil
}

// createReviewedFolder opens the deepest existing ancestor without following
// symlinks. The reviewed target was already resolved and must stay at that
// path even if another process replaces one of its components.
func createReviewedFolder(path string) error {
	var missing []string
	parent := path
	for {
		root, err := safepath.OpenPinnedDir(parent)
		if err == nil {
			return createReviewedChildren(root, parent, missing)
		}
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		missing = append(missing, filepath.Base(parent))
		parent = filepath.Dir(parent)
	}
}

func createReviewedChildren(root *os.Root, parent string, missing []string) error {
	for i := len(missing) - 1; i >= 0; i-- {
		name := missing[i]
		err := root.Mkdir(name, 0o750)
		_ = root.Close()
		if err != nil && !errors.Is(err, os.ErrExist) {
			return err
		}
		parent = filepath.Join(parent, name)
		root, err = safepath.OpenPinnedDir(parent)
		if err != nil {
			return err
		}
	}
	return root.Close()
}

type reviewedFolderTargetKey struct{}

func WithReviewedFolderTarget(ctx context.Context, target ReviewedFolderTarget) context.Context {
	return context.WithValue(ctx, reviewedFolderTargetKey{}, target)
}

// ValidateStagedFolder reads the entire tar stream before any chain step can
// clear or write the workload target. Extraction still enforces its own rules
// against the actual destination state.
func ValidateStagedFolder(ctx context.Context, dir, dest string) error {
	name, err := findArchive(dir, "data.tar")
	if err != nil {
		return err
	}
	file, err := os.Open(name) // #nosec G304 -- the name came from a fixed filename in private restore staging
	if err != nil {
		return err
	}
	defer file.Close()
	body, closeBody, err := detectingReader(file)
	if err != nil {
		return err
	}
	defer closeBody() //nolint:errcheck
	reader := tar.NewReader(body)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		entry, err := reader.Next()
		if err == io.EOF {
			// Also check the trailer of a compressed stream.
			_, err = io.Copy(io.Discard, body)
			return err
		}
		if err != nil {
			return err
		}
		if _, err := joinArchiveTarget(dest, entry.Name); err != nil {
			return err
		}
		if _, err := io.Copy(io.Discard, reader); err != nil {
			return err
		}
	}
}

// RestorePreparedChunked consumes the already staged manifest and chunk bytes.
func (h *FolderHandler) RestorePreparedChunked(ctx context.Context, item BackupItem, target ReviewedFolderTarget, manifest dedup.Manifest, get func(dedup.ID) ([]byte, error), progress ProgressFunc) error {
	if err := target.Check(); err != nil {
		return err
	}
	if err := createReviewedFolder(target.Resolved); err != nil {
		return err
	}
	if target.Clean {
		if err := clearPinnedRestoreTarget(target.Resolved); err != nil {
			return err
		}
	}
	return h.restoreChunkedEntries(ctx, item, manifest, get, target.Resolved, progress)
}
