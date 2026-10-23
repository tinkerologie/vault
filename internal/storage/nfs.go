package storage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"github.com/tinkerologie/vault/internal/safepath"
)

// NFSConfig holds configuration for an NFS storage adapter.
type NFSConfig struct {
	Host     string `json:"host"`
	Export   string `json:"export"`    // Remote export path, e.g. "/mnt/user/backups".
	BasePath string `json:"base_path"` // Sub-directory within the export.
	Version  string `json:"version"`   // NFS version: "3" or "4" (default "4").
	Options  string `json:"options"`   // Extra mount options, e.g. "nolock,soft".
}

// mountExport and unmountExport run the system mount commands. They are
// variables only so tests can stand in for a real NFS server.
var (
	mountExport = func(args ...string) ([]byte, error) {
		return exec.Command("mount", args...).CombinedOutput() // #nosec G204 //nolint:gosec // args validated in NewNFSAdapter
	}
	unmountExport = func(dir string) error {
		return exec.Command("umount", dir).Run() // #nosec G204 //nolint:gosec // dir is vault-controlled temp dir
	}
)

// NFSAdapter implements Adapter for NFS shares.
// It mounts the export to a temporary directory on first use and delegates
// file operations to a LocalAdapter. The mount is reference-counted and
// unmounted when the adapter is garbage-collected or explicitly closed.
type NFSAdapter struct {
	config   NFSConfig
	mountDir string
	local    *LocalAdapter
	mu       sync.Mutex
	mounted  bool
	// stale holds mount points a refused mount could not unmount; unmount
	// (and so Close) retries them so a failed cleanup is never forgotten.
	stale []string
}

// NewNFSAdapter creates a new NFS adapter. The share is not mounted until
// the first operation or TestConnection.
func NewNFSAdapter(config NFSConfig) (*NFSAdapter, error) {
	if config.Host == "" {
		return nil, fmt.Errorf("nfs: host is required")
	}
	if config.Export == "" {
		return nil, fmt.Errorf("nfs: export is required")
	}
	// Reject shell metacharacters in mount arguments to prevent command injection.
	for _, s := range []string{config.Host, config.Export, config.Version, config.Options} {
		if strings.ContainsAny(s, ";|&$`\\\"'(){}[]<>!\n\r") {
			return nil, fmt.Errorf("nfs: config contains invalid characters")
		}
	}
	if config.Version == "" {
		config.Version = "4"
	}
	return &NFSAdapter{config: config}, nil
}

// mount ensures the NFS share is mounted. Safe to call multiple times.
func (n *NFSAdapter) mount() error {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.mounted {
		return nil
	}

	// The share is mounted at a fresh directory under the OS temp dir, and
	// the destination root lies inside it. Validate that location before
	// creating or mounting anything, so a TMPDIR inside a protected system
	// tree fails closed (#514).
	if _, err := safepath.ValidateLocalStorageRoot(filepath.Join(os.TempDir(), "vault-nfs")); err != nil {
		return fmt.Errorf("nfs: unsafe mount location: %w", err)
	}
	dir, err := os.MkdirTemp("", "vault-nfs-*")
	if err != nil {
		return fmt.Errorf("nfs: creating mount dir: %w", err)
	}

	source := fmt.Sprintf("%s:%s", n.config.Host, n.config.Export)
	args := []string{"-t", "nfs"}

	// Build mount options string.
	opts := []string{fmt.Sprintf("vers=%s", n.config.Version)}
	if n.config.Options != "" {
		opts = append(opts, n.config.Options)
	}
	args = append(args, "-o", strings.Join(opts, ","))
	args = append(args, source, dir)

	if out, err := mountExport(args...); err != nil {
		_ = os.Remove(dir)
		return fmt.Errorf("nfs: mount %s failed: %w\n%s", source, err, strings.TrimSpace(string(out)))
	}

	basePath := dir
	if n.config.BasePath != "" {
		basePath, err = safepath.JoinUnderBase(dir, n.config.BasePath, true)
		if err != nil {
			return n.abortMount(dir, fmt.Errorf("nfs: invalid base path %q: %w", n.config.BasePath, err))
		}
	}

	// Symlinks inside the export resolve on this host, so a base path that
	// names one could point at a protected directory. Validate the resolved
	// root and refuse the mount if it is unsafe (#514).
	local, err := NewValidatedLocalAdapter(basePath)
	if err != nil {
		return n.abortMount(dir, fmt.Errorf("nfs: invalid base path %q: %w", n.config.BasePath, err))
	}

	n.mountDir = dir
	n.local = local
	n.mounted = true
	return nil
}

// abortMount undoes a mount that mount() refused after it succeeded. When
// the unmount itself fails, dir is kept in n.stale for unmount to retry and
// the failure is added to cause. Called with n.mu held.
func (n *NFSAdapter) abortMount(dir string, cause error) error {
	if err := unmountExport(dir); err != nil {
		n.stale = append(n.stale, dir)
		return errors.Join(cause, fmt.Errorf("nfs: unmount %s after refused mount: %w", dir, err))
	}
	_ = os.Remove(dir)
	return cause
}

// unmount removes the NFS mount and cleans up the temporary directory. It
// also retries mount points a refused mount could not unmount.
func (n *NFSAdapter) unmount() {
	n.mu.Lock()
	defer n.mu.Unlock()
	var still []string
	for _, dir := range n.stale {
		if err := unmountExport(dir); err != nil {
			log.Printf("Warning: nfs unmount %s failed: %v", dir, err)
			still = append(still, dir)
			continue
		}
		_ = os.Remove(dir)
	}
	n.stale = still
	if !n.mounted {
		return
	}
	if err := unmountExport(n.mountDir); err != nil {
		log.Printf("Warning: nfs unmount %s failed: %v", n.mountDir, err)
	}
	if err := os.Remove(n.mountDir); err != nil && !os.IsNotExist(err) {
		log.Printf("Warning: nfs cleanup %s failed: %v", n.mountDir, err)
	}
	n.mounted = false
	n.local = nil
}

func (n *NFSAdapter) Write(path string, reader io.Reader) error {
	if err := n.mount(); err != nil {
		return err
	}
	return n.local.Write(path, reader)
}

func (n *NFSAdapter) WriteFrom(path string, open func() (io.ReadCloser, error)) error {
	return streamWriteFrom(n, path, open)
}

func (n *NFSAdapter) Read(path string) (io.ReadCloser, error) {
	if err := n.mount(); err != nil {
		return nil, err
	}
	return n.local.Read(path)
}

func (n *NFSAdapter) ReadRange(path string, offset, length int64) (io.ReadCloser, error) {
	if err := n.mount(); err != nil {
		return nil, err
	}
	return n.local.ReadRange(path, offset, length)
}

func (n *NFSAdapter) Delete(path string) error {
	if err := n.mount(); err != nil {
		return err
	}
	return n.local.Delete(path)
}

func (n *NFSAdapter) List(prefix string) ([]FileInfo, error) {
	if err := n.mount(); err != nil {
		return nil, err
	}
	return n.local.List(prefix)
}

func (n *NFSAdapter) Stat(path string) (FileInfo, error) {
	if err := n.mount(); err != nil {
		return FileInfo{}, err
	}
	return n.local.Stat(path)
}

func (n *NFSAdapter) TestConnection() error {
	if err := n.mount(); err != nil {
		return err
	}
	defer n.unmount()
	return n.local.TestConnection()
}

// Close unmounts the NFS share and cleans up the temporary mount point.
// It is safe to call if the share is not currently mounted.
func (n *NFSAdapter) Close() error {
	n.unmount()
	return nil
}

// GetCapacity returns the underlying NFS mount's filesystem usage.
// NFS mounts behave as local paths on the host, so this delegates
// directly to the wrapped LocalAdapter, which calls unix.Statfs on
// the mount point. The share is mounted on demand if it is not already.
func (n *NFSAdapter) GetCapacity(ctx context.Context) (Capacity, error) {
	if err := n.mount(); err != nil {
		return Capacity{}, err
	}
	return n.local.GetCapacity(ctx)
}

// RemoveEmptyDir removes dir if it is empty by delegating to the underlying
// LocalAdapter. The NFS share is mounted on demand if not already mounted.
// os.Remove fails on a non-empty directory, which is the desired guard.
func (n *NFSAdapter) RemoveEmptyDir(dir string) error {
	if err := n.mount(); err != nil {
		return err
	}
	return n.local.RemoveEmptyDir(dir)
}

var _ Adapter = (*NFSAdapter)(nil)
