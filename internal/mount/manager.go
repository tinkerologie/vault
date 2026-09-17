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

// Manager orchestrates FUSE mounts, SQLite persistence, and WebSocket notifications.
type Manager struct {
	db           *db.DB
	hub          *ws.Hub
	serverKey    []byte
	baseMountDir string

	mu           sync.Mutex
	activeMounts map[int64]activeMount
	stopSweeper  chan struct{}
	stopOnce     sync.Once
}

// NewManager creates a mount lifecycle manager.
func NewManager(d *db.DB, hub *ws.Hub, serverKey []byte) *Manager {
	baseDir := defaultBaseMountDir()
	if d != nil {
		if custom, err := d.GetSetting("fuse_mount_base_dir", docsmeta.DefaultFor("fuse_mount_base_dir")); err == nil && custom != "" {
			baseDir = custom
		}
	}
	return &Manager{
		db:           d,
		hub:          hub,
		serverKey:    append([]byte(nil), serverKey...),
		baseMountDir: baseDir,
		activeMounts: make(map[int64]activeMount),
		stopSweeper:  make(chan struct{}),
	}
}

// SetBaseMountDir overrides the base mount directory.
func (m *Manager) SetBaseMountDir(dir string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.baseMountDir = dir
}

func defaultBaseMountDir() string {
	if _, err := os.Stat("/mnt"); err == nil {
		testDir := "/mnt/vault-fuse"
		if err := os.MkdirAll(testDir, 0755); err == nil {
			return testDir
		}
	}
	fallback := filepath.Join(os.TempDir(), "vault-fuse")
	_ = os.MkdirAll(fallback, 0755)
	return fallback
}

// Start launches the periodic idle sweeper and cleans up stale mounts from previous daemon runs.
func (m *Manager) Start(ctx context.Context) {
	m.CleanupStale(ctx)
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
	baseDir := m.baseMountDir
	m.mu.Unlock()

	for _, s := range stale {
		log.Printf("INFO mount: cleaning up stale mount %s (session %d)", s.MountPath, s.ID)
		_ = unmountPath(s.MountPath)
		if rel, err := filepath.Rel(baseDir, s.MountPath); err == nil && !strings.HasPrefix(rel, "..") && rel != "." {
			_ = os.Remove(s.MountPath)
		}
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

	repo, err := dedup.OpenRepo(m.db, adapter, dest.ID, m.serverKey)
	if err != nil {
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

	mountDir := targetDir
	if mountDir == "" {
		m.mu.Lock()
		base := m.baseMountDir
		m.mu.Unlock()
		mountDir = filepath.Join(base, fmt.Sprintf("mount-%d", sessID))
	}
	_ = os.MkdirAll(mountDir, 0755)

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

func unmountPath(path string) error {
	return unmountPlatform(path)
}
