// Package recovery reads Vault backups straight from storage, without the
// daemon or its database, and extracts files to a local folder. It powers the
// `vault recover` command, which also runs on Windows (issue #313).
//
// Recovery is read-only by construction: the storage adapter is wrapped so
// every write and delete fails. Metadata lives in a throwaway SQLite database
// in a temporary directory that Close removes.
package recovery

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/ruaan-deysel/vault/internal/db"
	"github.com/ruaan-deysel/vault/internal/dedup"
	"github.com/ruaan-deysel/vault/internal/storage"
)

// repoConfigPath is where a dedup destination keeps its repository header
// (and the sealed master key).
const repoConfigPath = "_vault/repo.json"

// Options configures a recovery session.
type Options struct {
	// StorageType and StorageConfig are the destination's type ("local",
	// "s3", "sftp", "smb", "webdav") and its JSON config, in the same form
	// Vault stores for a storage destination.
	StorageType   string
	StorageConfig string
	// ServerKey is the contents of vault.key. Required for dedup
	// destinations, whose master key is sealed with it.
	ServerKey []byte
	// Passphrase decrypts age-encrypted (classic) backups.
	Passphrase string
}

// Session is an open connection to one backup destination.
type Session struct {
	adapter    storage.Adapter
	db         *db.DB
	tmpDir     string
	destID     int64
	serverKey  []byte
	passphrase string

	points []Point
	repo   *dedup.Repo
}

// Open connects to the destination read-only and prepares a scratch
// database. Call Close to disconnect and delete the scratch files.
func Open(opts Options) (*Session, error) {
	if opts.StorageType == "nfs" {
		return nil, errors.New("recover does not mount NFS exports: mount the export yourself and use --type local with --path pointing at the mount")
	}
	inner, err := storage.NewAdapter(opts.StorageType, opts.StorageConfig)
	if err != nil {
		return nil, fmt.Errorf("connect to %s storage: %w", opts.StorageType, err)
	}
	adapter := readOnlyAdapter{inner: inner}

	tmpDir, err := os.MkdirTemp("", "vault-recover-")
	if err != nil {
		storage.CloseAdapter(adapter)
		return nil, fmt.Errorf("create scratch directory: %w", err)
	}
	s := &Session{
		adapter:    adapter,
		tmpDir:     tmpDir,
		serverKey:  opts.ServerKey,
		passphrase: opts.Passphrase,
	}
	s.db, err = db.Open(filepath.Join(tmpDir, "recover.db"))
	if err != nil {
		s.Close()
		return nil, fmt.Errorf("open scratch database: %w", err)
	}
	// The dedup index tables reference a destination row. Its config is left
	// empty on purpose so storage credentials are never written to disk.
	s.destID, err = s.db.CreateStorageDestination(db.StorageDestination{
		Name: "recovery", Type: opts.StorageType, Config: "{}",
	})
	if err != nil {
		s.Close()
		return nil, fmt.Errorf("prepare scratch database: %w", err)
	}
	return s, nil
}

// Close disconnects from storage and removes the scratch database.
func (s *Session) Close() {
	if s.db != nil {
		_ = s.db.Close()
	}
	storage.CloseAdapter(s.adapter)
	if s.tmpDir != "" {
		_ = os.RemoveAll(s.tmpDir)
	}
}

// IsDedup reports whether the destination holds a dedup repository.
func (s *Session) IsDedup() bool {
	_, err := s.adapter.Stat(repoConfigPath)
	return err == nil
}

// dedupRepo opens the destination's dedup repository on first use and
// rebuilds the chunk index from the index blobs kept on storage. The
// repository is opened first: unsealing its master key is cheap and proves
// --key is right before scanning a possibly large index.
func (s *Session) dedupRepo() (*dedup.Repo, error) {
	if s.repo != nil {
		return s.repo, nil
	}
	if len(s.serverKey) == 0 {
		return nil, errors.New("this is a deduplicated backup: pass --key with the vault.key from the original server")
	}
	repo, err := dedup.OpenRepo(s.db, s.adapter, s.destID, s.serverKey)
	if err != nil {
		return nil, fmt.Errorf("open dedup repository (is --key the vault.key from the server that made these backups?): %w", err)
	}
	// A repository that has never stored a pack has no index folder yet;
	// that is an empty index, not an error.
	if err := dedup.NewIndex(s.db, s.adapter, s.destID).RebuildFromStorage(); err != nil && !storage.IsNotExist(err) {
		return nil, fmt.Errorf("rebuild dedup index from storage: %w", err)
	}
	s.repo = repo
	return repo, nil
}

// defaultSafeNames is whether extraction rewrites names Windows cannot store.
func defaultSafeNames() bool { return runtime.GOOS == "windows" }

// caseInsensitiveFS reports whether names differing only by case must be
// kept apart on this platform's default filesystem.
func caseInsensitiveFS() bool { return runtime.GOOS == "windows" || runtime.GOOS == "darwin" }
