package mount

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ruaan-deysel/vault/internal/db"
	"github.com/ruaan-deysel/vault/internal/dedup"
	"github.com/ruaan-deysel/vault/internal/docsmeta"
	"github.com/ruaan-deysel/vault/internal/runner"
	"github.com/ruaan-deysel/vault/internal/storage"
	"github.com/ruaan-deysel/vault/internal/ws"
)

// DefaultIdleTimeout is the default duration after which an inactive mount is auto-unmounted (30 minutes).
const DefaultIdleTimeout = 30 * time.Minute

type activeMount struct {
	handle  MountHandle
	adapter storage.Adapter
}

func closeAdapter(a storage.Adapter) {
	if closer, ok := a.(io.Closer); ok {
		_ = closer.Close()
	}
}

// Mount roots. Variables so tests can redirect them away from the host /mnt.
var (
	// addonsRoot is Unraid's namespace for plugin and remote mounts. Anything
	// else created directly under /mnt is flagged by Fix Common Problems as an
	// invalid folder (#446).
	addonsRoot = "/mnt/addons"
	// legacyMountRoot is where releases up to v2026.09.01 put FUSE mounts.
	legacyMountRoot = "/mnt/vault-fuse"
	// tempRoot returns the parent for the fallback mount root.
	tempRoot = os.TempDir
)

const mountRootName = "vault-fuse"

// DefaultMountRoots lists every root Vault may have used for default FUSE
// mounts, for cleanup that must not leave Vault directories under /mnt.
func DefaultMountRoots() []string {
	return []string{legacyMountRoot, filepath.Join(addonsRoot, mountRootName)}
}

// Manager orchestrates FUSE mounts, SQLite persistence, and WebSocket notifications.
type Manager struct {
	db           *db.DB
	hub          *ws.Hub
	serverKey    []byte
	baseMountDir string
	// baseIsDefault is false when the base came from fuse_mount_base_dir or
	// SetBaseMountDir; only the default base falls back to the temp dir.
	baseIsDefault bool

	mu           sync.Mutex
	activeMounts map[int64]activeMount
	stopSweeper  chan struct{}
	stopOnce     sync.Once
}

// NewManager creates a mount lifecycle manager. It creates no directories;
// a mount's directory is created only when that mount starts.
func NewManager(d *db.DB, hub *ws.Hub, serverKey []byte) *Manager {
	baseDir, isDefault := "", true
	if d != nil {
		if custom, err := d.GetSetting("fuse_mount_base_dir", docsmeta.DefaultFor("fuse_mount_base_dir")); err == nil && custom != "" {
			baseDir, isDefault = custom, false
		}
	}
	if isDefault {
		baseDir = defaultBaseMountDir()
	}
	return &Manager{
		db:            d,
		hub:           hub,
		serverKey:     append([]byte(nil), serverKey...),
		baseMountDir:  baseDir,
		baseIsDefault: isDefault,
		activeMounts:  make(map[int64]activeMount),
		stopSweeper:   make(chan struct{}),
	}
}

// SetBaseMountDir overrides the base mount directory.
func (m *Manager) SetBaseMountDir(dir string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.baseMountDir = dir
	m.baseIsDefault = false
}

// defaultBaseMountDir resolves the default mount root without creating it:
// /mnt/addons/vault-fuse when Unraid's /mnt/addons exists, otherwise a
// directory under the system temp dir.
func defaultBaseMountDir() string {
	if info, err := os.Stat(addonsRoot); err == nil && info.IsDir() {
		return filepath.Join(addonsRoot, mountRootName)
	}
	return fallbackBaseMountDir()
}

func fallbackBaseMountDir() string {
	return filepath.Join(tempRoot(), mountRootName)
}

// sessionMountDir creates and returns the directory for a mount session. An
// explicit target is used as given. Otherwise the directory goes under the
// base, and when the default base cannot be created (for example a read-only
// /mnt/addons) it retries once under the temp-dir fallback.
func (m *Manager) sessionMountDir(targetDir string, sessID int64) (string, error) {
	if targetDir != "" {
		if err := os.MkdirAll(targetDir, 0o755); err != nil {
			return "", fmt.Errorf("mount: create target %s: %w", targetDir, err)
		}
		return targetDir, nil
	}
	m.mu.Lock()
	base, isDefault := m.baseMountDir, m.baseIsDefault
	m.mu.Unlock()

	name := fmt.Sprintf("mount-%d", sessID)
	dir := filepath.Join(base, name)
	err := os.MkdirAll(dir, 0o755)
	if err == nil {
		return dir, nil
	}
	if fallback := fallbackBaseMountDir(); isDefault && fallback != base {
		log.Printf("WARN mount: cannot create %s (%v); using %s", dir, err, fallback)
		dir = filepath.Join(fallback, name)
		if err = os.MkdirAll(dir, 0o755); err == nil {
			return dir, nil
		}
	}
	return "", fmt.Errorf("mount: create mount directory %s: %w", dir, err)
}

// Start launches the periodic idle sweeper and cleans up stale mounts from previous daemon runs.
func (m *Manager) Start(ctx context.Context) {
	m.CleanupStale(ctx)
	m.cleanupOrphanedSessionDirs()
	m.cleanupLegacyRoot()
	go m.runIdleSweeper(ctx)
}

// Stop terminates active mounts and stops background routines.
func (m *Manager) Stop(ctx context.Context) {
	m.stopOnce.Do(func() {
		close(m.stopSweeper)
	})
	m.mu.Lock()
	ids := make([]int64, 0, len(m.activeMounts))
	for id := range m.activeMounts {
		ids = append(ids, id)
	}
	m.mu.Unlock()

	for _, id := range ids {
		_ = m.Unmount(ctx, id)
	}
}

// CleanupStale cleans up orphaned mounts from crashes or restarts.
func (m *Manager) CleanupStale(ctx context.Context) {
	if m.db == nil {
		return
	}
	stale, err := m.db.CleanupStaleMountSessions()
	if err != nil {
		log.Printf("WARN mount: cleanup stale sessions query: %v", err)
		return
	}

	m.mu.Lock()
	roots := []string{m.baseMountDir, legacyMountRoot}
	m.mu.Unlock()

	for _, s := range stale {
		log.Printf("INFO mount: cleaning up stale mount %s (session %d)", s.MountPath, s.ID)
		if err := unmountPath(s.MountPath); err != nil {
			log.Printf("WARN mount: keeping %s, unmount failed: %v", s.MountPath, err)
			continue
		}
		if !withinAnyRoot(s.MountPath, roots) {
			continue
		}
		removeEmptyDir(s.MountPath)
	}
}

// cleanupLegacyRoot removes the pre-#446 /mnt/vault-fuse root once nothing is
// left in it. Only empty mount-<id> directories and the root itself are
// removed, never recursively, so nothing that is still mounted or holds data
// can be deleted.
func (m *Manager) cleanupLegacyRoot() {
	m.mu.Lock()
	base := m.baseMountDir
	m.mu.Unlock()
	if filepath.Clean(base) == filepath.Clean(legacyMountRoot) {
		return
	}
	if !sweepSessionDirs(legacyMountRoot) {
		return
	}
	if err := os.Remove(legacyMountRoot); err != nil {
		log.Printf("INFO mount: keeping legacy mount root %s: %v", legacyMountRoot, err)
		return
	}
	log.Printf("INFO mount: removed legacy mount root %s", legacyMountRoot)
}

// cleanupOrphanedSessionDirs removes empty mount-<id> directories left under
// the current base by an earlier run. A session whose unmount failed is marked
// stopped by CleanupStale, so without this sweep its directory would never be
// retried. Live mount points are kept: os.Remove refuses a busy or non-empty
// directory, and nothing is unmounted here because a `vault mount` CLI
// session may share the base.
func (m *Manager) cleanupOrphanedSessionDirs() {
	m.mu.Lock()
	base := m.baseMountDir
	m.mu.Unlock()
	if base != "" {
		sweepSessionDirs(base)
	}
}

// sweepSessionDirs removes the empty mount-<id> directories directly inside
// root. It reports false when root is not a real directory or cannot be read.
func sweepSessionDirs(root string) bool {
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() {
		return false
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		log.Printf("WARN mount: read mount root %s: %v", root, err)
		return false
	}
	for _, e := range entries {
		if !e.IsDir() || !isSessionDirName(e.Name()) {
			continue
		}
		removeEmptyDir(filepath.Join(root, e.Name()))
	}
	return true
}

// RemoveEmptyMountRoot removes root when it is a real directory holding
// nothing but empty mount-<id> session directories. It never follows symlinks
// or removes other content; a missing root is not an error.
func RemoveEmptyMountRoot(root string) error {
	info, err := os.Lstat(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return nil
	}
	sweepSessionDirs(root)
	return os.Remove(root)
}

func isSessionDirName(name string) bool {
	id, ok := strings.CutPrefix(name, "mount-")
	if !ok || id == "" {
		return false
	}
	_, err := strconv.ParseInt(id, 10, 64)
	return err == nil
}

// withinAnyRoot reports whether p sits strictly below one of roots.
func withinAnyRoot(p string, roots []string) bool {
	for _, root := range roots {
		if root == "" {
			continue
		}
		rel, err := filepath.Rel(filepath.Clean(root), filepath.Clean(p))
		if err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// removeEmptyDir removes p only when it is a real directory (not a symlink);
// os.Remove refuses non-empty directories and busy mount points.
func removeEmptyDir(p string) {
	info, err := os.Lstat(p)
	if err != nil || !info.IsDir() {
		return
	}
	if err := os.Remove(p); err != nil {
		log.Printf("INFO mount: keeping %s: %v", p, err)
	}
}

func (m *Manager) broadcast(eventType string, data any) {
	if m.hub == nil {
		return
	}
	payload := map[string]any{
		"type": eventType,
		"data": data,
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return
	}
	m.hub.Broadcast(b)
}

func (m *Manager) getIdleTimeout() time.Duration {
	if m.db == nil {
		return DefaultIdleTimeout
	}
	val, err := m.db.GetSetting("fuse_mount_idle_minutes", docsmeta.DefaultFor("fuse_mount_idle_minutes"))
	if err != nil || val == "" {
		return DefaultIdleTimeout
	}
	mins, err := strconv.Atoi(val)
	if err != nil || mins < 0 {
		return DefaultIdleTimeout
	}
	if mins == 0 {
		return 0 // Disabled
	}
	return time.Duration(mins) * time.Minute
}

func (m *Manager) runIdleSweeper(ctx context.Context) {
	ticker := time.NewTicker(1 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-m.stopSweeper:
			return
		case <-ticker.C:
			timeout := m.getIdleTimeout()
			if timeout <= 0 {
				continue
			}
			active, err := m.ListActive()
			if err != nil {
				continue
			}
			now := time.Now()
			for _, s := range active {
				if now.Sub(s.LastActivityAt) > timeout {
					log.Printf("INFO mount: session %d (%s) inactive for %v; unmounting", s.ID, s.MountPath, timeout)
					_ = m.Unmount(ctx, s.ID)
				}
			}
		}
	}
}

// MountRestorePoint mounts a deduplicated restore point at a new mount directory.
func (m *Manager) MountRestorePoint(ctx context.Context, jobID, rpID int64) (*db.MountSession, error) {
	return m.MountRestorePointTo(ctx, jobID, rpID, "")
}

// MountRestorePointTo mounts a deduplicated restore point at targetDir (or default if targetDir is empty).
func (m *Manager) MountRestorePointTo(ctx context.Context, jobID, rpID int64, targetDir string) (*db.MountSession, error) {
	if m.db == nil {
		return nil, errors.New("mount: nil db")
	}

	job, err := m.db.GetJob(jobID)
	if err != nil {
		return nil, fmt.Errorf("mount: get job %d: %w", jobID, err)
	}

	dest, err := m.db.GetStorageDestination(job.StorageDestID)
	if err != nil {
		return nil, fmt.Errorf("mount: get storage %d: %w", job.StorageDestID, err)
	}

	if !dest.DedupEnabled {
		return nil, errors.New("mount: only deduplicated backups support random-access FUSE mounting")
	}

	rp, err := m.db.GetRestorePoint(rpID)
	if err != nil {
		return nil, fmt.Errorf("mount: get restore point %d: %w", rpID, err)
	}

	adapter, err := storage.NewAdapter(dest.Type, dest.Config)
	if err != nil {
		return nil, fmt.Errorf("mount: init storage adapter: %w", err)
	}
	adapterClosed := false
	defer func() {
		if !adapterClosed {
			closeAdapter(adapter)
		}
	}()

	// Server key first; when it does not match (a recovered server), the
	// configured backup passphrase opens the repository through its escrow.
	repo, err := runner.OpenDedupRepoWithFallback(m.db, adapter, dest.ID, m.serverKey, runner.ResolvePassphraseFrom(m.db, m.serverKey))
	if err != nil {
		if errors.Is(err, dedup.ErrServerKeyMismatch) {
			return nil, fmt.Errorf("mount: open dedup repo: %s: %w", dedup.KeyMismatchHint, err)
		}
		return nil, fmt.Errorf("mount: open dedup repo: %w", err)
	}

	manifests := make(map[string]dedup.Manifest)
	// Check for multi-item item_manifests in metadata
	if rp.Metadata != "" {
		var meta struct {
			ItemManifests map[string]string `json:"item_manifests"`
		}
		if jerr := json.Unmarshal([]byte(rp.Metadata), &meta); jerr == nil && len(meta.ItemManifests) > 0 {
			for itemName, hexID := range meta.ItemManifests {
				decoded, err := hex.DecodeString(hexID)
				if err != nil || len(decoded) != 32 {
					continue
				}
				var cid dedup.ID
				copy(cid[:], decoded)
				manifest, err := repo.GetManifest(cid)
				if err != nil {
					return nil, fmt.Errorf("mount: get manifest %s for item %s: %w", hexID[:8], itemName, err)
				}
				manifests[itemName] = manifest
			}
		}
	}

	// Fallback to single-item restore point manifest_id
	if len(manifests) == 0 && len(rp.ManifestID) == 32 {
		var cid dedup.ID
		copy(cid[:], rp.ManifestID)
		manifest, err := repo.GetManifest(cid)
		if err != nil {
			return nil, fmt.Errorf("mount: get manifest %x: %w", cid[:8], err)
		}
		name := job.Name
		if manifest.Item != "" {
			name = manifest.Item
		}
		manifests[name] = manifest
	}

	if len(manifests) == 0 {
		return nil, errors.New("mount: no manifests found in restore point")
	}

	// Create a preliminary session row in DB to obtain a unique ID
	sessID, err := m.db.CreateMountSession(db.MountSession{
		JobID:          jobID,
		RestorePointID: &rpID,
		StorageDestID:  dest.ID,
		MountPath:      "",
	})
	if err != nil {
		return nil, fmt.Errorf("mount: create session record: %w", err)
	}

	mountDir, err := m.sessionMountDir(targetDir, sessID)
	if err != nil {
		_ = m.db.UpdateMountSessionStatus(sessID, "failed", err.Error())
		m.broadcast("mount.failed", map[string]any{
			"session_id": sessID,
			"error":      err.Error(),
		})
		return nil, err
	}

	// Update session with final mount directory
	if err := m.db.UpdateMountSessionPath(sessID, mountDir); err != nil {
		_ = os.Remove(mountDir)
		return nil, fmt.Errorf("mount: update session path: %w", err)
	}

	m.broadcast("mount.started", map[string]any{
		"session_id": sessID,
		"mount_path": mountDir,
		"job_id":     jobID,
	})

	var lastPersisted atomic.Int64
	activityCallback := func() {
		now := time.Now().Unix()
		prev := lastPersisted.Load()
		if now-prev < 15 {
			return
		}
		if !lastPersisted.CompareAndSwap(prev, now) {
			return
		}
		_ = m.db.UpdateMountSessionActivity(sessID)
	}

	handle, err := MountManifests(ctx, repo, manifests, mountDir, activityCallback)
	if err != nil {
		_ = m.db.UpdateMountSessionStatus(sessID, "failed", err.Error())
		_ = os.Remove(mountDir)
		m.broadcast("mount.failed", map[string]any{
			"session_id": sessID,
			"error":      err.Error(),
		})
		return nil, fmt.Errorf("mount: start FUSE mount: %w", err)
	}

	m.mu.Lock()
	m.activeMounts[sessID] = activeMount{handle: handle, adapter: adapter}
	m.mu.Unlock()
	adapterClosed = true

	session, err := m.db.GetMountSession(sessID)
	if err != nil {
		session = db.MountSession{
			ID:             sessID,
			JobID:          jobID,
			JobName:        job.Name,
			RestorePointID: &rpID,
			StorageDestID:  dest.ID,
			StorageName:    dest.Name,
			MountPath:      mountDir,
			Status:         "active",
			StartedAt:      time.Now(),
			LastActivityAt: time.Now(),
		}
	}

	m.broadcast("mount.active", session)
	return &session, nil
}

// Unmount cleanly unmounts an active or orphaned mount session.
func (m *Manager) Unmount(ctx context.Context, sessionID int64) error {
	m.mu.Lock()
	entry, hasEntry := m.activeMounts[sessionID]
	delete(m.activeMounts, sessionID)
	m.mu.Unlock()

	var mountPath string
	var unmountErr error
	if hasEntry && entry.handle != nil {
		mountPath = entry.handle.MountPath()
		unmountErr = entry.handle.Unmount()
	}

	var sessionRowFound bool
	if m.db != nil {
		if s, err := m.db.GetMountSession(sessionID); err == nil {
			sessionRowFound = true
			if mountPath == "" {
				mountPath = s.MountPath
			}
		}
	}

	if !hasEntry && !sessionRowFound {
		return fmt.Errorf("mount: session %d not found: %w", sessionID, db.ErrNotFound)
	}

	if mountPath != "" {
		// Only orphaned sessions (no in-process handle) need external platform unmount.
		if !hasEntry || entry.handle == nil {
			if err := unmountPath(mountPath); err != nil && unmountErr == nil {
				unmountErr = err
			}
		}
		_ = os.Remove(mountPath)
	}

	if unmountErr != nil {
		if hasEntry {
			m.mu.Lock()
			m.activeMounts[sessionID] = entry
			m.mu.Unlock()
		}
		return fmt.Errorf("mount: unmount session %d: %w", sessionID, unmountErr)
	}

	if hasEntry && entry.adapter != nil {
		closeAdapter(entry.adapter)
	}

	if m.db != nil && sessionRowFound {
		_ = m.db.UpdateMountSessionStatus(sessionID, "stopped", "")
	}

	m.broadcast("mount.unmounted", map[string]any{
		"session_id": sessionID,
		"mount_path": mountPath,
	})
	return nil
}

// ListActive returns all active mount sessions from the database.
func (m *Manager) ListActive() ([]db.MountSession, error) {
	if m.db == nil {
		return nil, nil
	}
	return m.db.ListMountSessions(true)
}

// Get returns details for a single mount session.
func (m *Manager) Get(sessionID int64) (db.MountSession, error) {
	if m.db == nil {
		return db.MountSession{}, errors.New("mount: nil db")
	}
	return m.db.GetMountSession(sessionID)
}

// unmountPath detaches a mount point. A variable so tests can simulate the
// platform outcome: unprivileged Linux reports EPERM even for a plain,
// unmounted directory.
var unmountPath = unmountPlatform
