package storage

import (
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"strings"
	"sync"

	"github.com/ruaan-deysel/vault/internal/safepath"
)

// NFSConfig holds configuration for an NFS storage adapter.
type NFSConfig struct {
	Host     string `json:"host"`
	Export   string `json:"export"`    // Remote export path, e.g. "/mnt/user/backups".
	BasePath string `json:"base_path"` // Sub-directory within the export.
	Version  string `json:"version"`   // NFS version: "3" or "4" (default "4").
	Options  string `json:"options"`   // Extra mount options, e.g. "nolock,soft".
}

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

	if out, err := exec.Command("mount", args...).CombinedOutput(); err != nil { // #nosec G204 //nolint:gosec // args validated in NewNFSAdapter
		_ = os.Remove(dir)
		return fmt.Errorf("nfs: mount %s failed: %w\n%s", source, err, strings.TrimSpace(string(out)))
	}

	basePath := dir
	if n.config.BasePath != "" {
		basePath, err = safepath.JoinUnderBase(dir, n.config.BasePath, true)
		if err != nil {
			_ = exec.Command("umount", dir).Run() // #nosec G204 //nolint:gosec // dir is vault-controlled temp dir
			_ = os.Remove(dir)
			return fmt.Errorf("nfs: invalid base path %q: %w", n.config.BasePath, err)
		}
	}

	n.mountDir = dir
	n.local = NewLocalAdapter(basePath)
	n.mounted = true
	return nil
}

// unmount removes the NFS mount and cleans up the temporary directory.
func (n *NFSAdapter) unmount() {
	n.mu.Lock()
	defer n.mu.Unlock()
	if !n.mounted {
		return
	}
	if err := exec.Command("umount", n.mountDir).Run(); err != nil { // #nosec G204 //nolint:gosec // mountDir is vault-controlled temp dir
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

func (n *NFSAdapter) Read(path string) (io.ReadCloser, error) {
	if err := n.mount(); err != nil {
		return nil, err
	}
	return n.local.Read(path)
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

var _ Adapter = (*NFSAdapter)(nil)
