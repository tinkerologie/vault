//go:build linux

package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/alchemillahq/gzfs"
)

const (
	snapshotPrefix = "vault_backup_"
	zfsMetaFile    = "zfs_meta.json"
	zfsSendFile    = "send.zfs"
)

// zfsMeta stores backup metadata alongside the ZFS send stream file.
type zfsMeta struct {
	Dataset        string `json:"dataset"`
	Snapshot       string `json:"snapshot"`
	ParentSnapshot string `json:"parent_snapshot,omitempty"`
	BackupType     string `json:"backup_type"`
	Timestamp      string `json:"timestamp"`
}

// ZFSHandler implements Handler for ZFS dataset backup/restore.
type ZFSHandler struct {
	client *gzfs.Client
	cmd    gzfs.Cmd
}

// NewZFSHandler creates a new ZFSHandler after verifying ZFS availability.
func NewZFSHandler() (*ZFSHandler, error) {
	client := gzfs.NewClient(gzfs.Options{})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if _, err := client.ZFS.List(ctx, false); err != nil {
		return nil, fmt.Errorf("zfs not available: %w", err)
	}

	return &ZFSHandler{
		client: client,
		cmd:    gzfs.Cmd{Bin: "zfs"},
	}, nil
}

// ListItems returns all ZFS filesystems and volumes as backup items.
func (h *ZFSHandler) ListItems() ([]BackupItem, error) {
	ctx := context.Background()

	filesystems, err := h.client.ZFS.ListByType(ctx, gzfs.DatasetTypeFilesystem, true)
	if err != nil {
		return nil, fmt.Errorf("listing ZFS filesystems: %w", err)
	}

	volumes, err := h.client.ZFS.ListByType(ctx, gzfs.DatasetTypeVolume, true)
	if err != nil {
		return nil, fmt.Errorf("listing ZFS volumes: %w", err)
	}

	allDatasets := append(filesystems, volumes...)
	items := make([]BackupItem, 0, len(allDatasets))

	for _, ds := range allDatasets {
		pool := ds.Pool
		if pool == "" {
			parts := strings.SplitN(ds.Name, "/", 2)
			pool = parts[0]
		}

		dsType := strings.ToLower(string(ds.Type))
		mounted := ds.Mountpoint != "" && ds.Mountpoint != "none" && ds.Mountpoint != "-"

		items = append(items, BackupItem{
			Name: ds.Name,
			Type: "zfs",
			Settings: map[string]any{
				"dataset":    ds.Name,
				"mountpoint": ds.Mountpoint,
				"pool":       pool,
				"used":       ds.Used,
				"available":  ds.Available,
				"type":       dsType,
				"mounted":    mounted,
			},
		})
	}

	return items, nil
}

// Backup creates a ZFS snapshot and sends it as a stream file.
func (h *ZFSHandler) Backup(ctx context.Context, item BackupItem, destDir string, progress ProgressFunc) (*BackupResult, error) {
	result := &BackupResult{ItemName: item.Name}

	dataset, _ := item.Settings["dataset"].(string)
	if dataset == "" {
		dataset = item.Name
	}

	if err := os.MkdirAll(destDir, 0750); err != nil {
		return nil, fmt.Errorf("creating dest dir: %w", err)
	}

	progress(item.Name, 5, "creating snapshot")

	snapName := snapshotPrefix + time.Now().Format("20060102_150405")
	snap, err := h.client.ZFS.Snapshot(ctx, dataset, snapName, false)
	if err != nil {
		return nil, fmt.Errorf("creating snapshot %s@%s: %w", dataset, snapName, err)
	}

	fullSnapName := snap.Name

	// Determine backup type (full or incremental).
	backupType := "full"
	if bt, ok := item.Settings["backup_type"].(string); ok && bt != "" {
		backupType = bt
	}

	// For incremental/differential, find the most recent vault snapshot as the parent.
	parentSnapshot := ""
	if ps, ok := item.Settings["parent_snapshot"].(string); ok && ps != "" {
		parentSnapshot = ps
	}
	if parentSnapshot == "" && backupType != "full" {
		parentSnapshot = h.findLatestVaultSnapshot(ctx, dataset)
		if parentSnapshot == "" {
			// No parent found — fall back to full backup.
			backupType = "full"
		}
	}

	progress(item.Name, 10, "sending ZFS stream")

	sendPath := filepath.Join(destDir, zfsSendFile)
	f, err := os.Create(sendPath) // #nosec G304 — sendPath is destDir + fixed filename
	if err != nil {
		return nil, fmt.Errorf("creating send file: %w", err)
	}

	// Estimate total size for progress reporting.
	usedBytes, _ := item.Settings["used"].(float64)
	if usedBytes == 0 {
		if u, ok := item.Settings["used"].(uint64); ok {
			usedBytes = float64(u)
		}
	}

	cw := &countingWriter{
		w:        f,
		total:    int64(usedBytes),
		name:     item.Name,
		progress: progress,
	}

	var stderr bytes.Buffer
	var sendArgs []string

	if backupType != "full" && parentSnapshot != "" {
		sendArgs = []string{"send", "-i", parentSnapshot, fullSnapName}
	} else {
		backupType = "full"
		sendArgs = []string{"send", fullSnapName}
	}

	if err := h.cmd.RunStream(ctx, nil, cw, &stderr, sendArgs...); err != nil {
		_ = f.Close()
		_ = os.Remove(sendPath)
		// Clean up the snapshot we just created to avoid orphaned snapshots.
		var destroyStderr bytes.Buffer
		_ = h.cmd.RunStream(ctx, nil, io.Discard, &destroyStderr, "destroy", fullSnapName)
		return nil, fmt.Errorf("zfs send (%s): %w: %s", fullSnapName, err, stderr.String())
	}

	if err := f.Close(); err != nil {
		return nil, fmt.Errorf("closing send file: %w", err)
	}

	result.Files = append(result.Files, backupFileInfo(sendPath))

	progress(item.Name, 90, "writing metadata")

	meta := zfsMeta{
		Dataset:        dataset,
		Snapshot:       fullSnapName,
		ParentSnapshot: parentSnapshot,
		BackupType:     backupType,
		Timestamp:      time.Now().UTC().Format(time.RFC3339),
	}

	metaJSON, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshalling metadata: %w", err)
	}

	metaPath := filepath.Join(destDir, zfsMetaFile)
	if err := os.WriteFile(metaPath, metaJSON, 0600); err != nil {
		return nil, fmt.Errorf("writing metadata: %w", err)
	}
	result.Files = append(result.Files, backupFileInfo(metaPath))

	// Clean up old vault snapshots, keeping only this one.
	h.cleanupOldSnapshots(ctx, dataset, fullSnapName)

	progress(item.Name, 100, "backup complete")
	result.Success = true
	return result, nil
}

// Restore receives a ZFS stream from a backup into a dataset.
func (h *ZFSHandler) Restore(ctx context.Context, item BackupItem, sourceDir string, progress ProgressFunc) error {
	progress(item.Name, 5, "reading metadata")

	metaPath := filepath.Join(sourceDir, zfsMetaFile)
	metaBytes, err := os.ReadFile(metaPath) // #nosec G304 — metaPath is sourceDir + fixed filename
	if err != nil {
		return fmt.Errorf("reading zfs metadata: %w", err)
	}

	var meta zfsMeta
	if err := json.Unmarshal(metaBytes, &meta); err != nil {
		return fmt.Errorf("parsing zfs metadata: %w", err)
	}

	// Determine restore destination.
	destDataset := meta.Dataset
	if rd, ok := item.Settings["restore_destination"].(string); ok && rd != "" {
		destDataset = rd
	} else if ds, ok := item.Settings["dataset"].(string); ok && ds != "" {
		destDataset = ds
	}

	// Validate the destination pool exists.
	pool := strings.SplitN(destDataset, "/", 2)[0]
	pools, err := h.client.Zpool.List(ctx)
	if err != nil {
		return fmt.Errorf("listing pools: %w", err)
	}

	poolFound := false
	for _, p := range pools {
		if p.Name == pool {
			poolFound = true
			break
		}
	}
	if !poolFound {
		return fmt.Errorf("destination pool %q does not exist", pool)
	}

	progress(item.Name, 10, "receiving ZFS stream")

	sendPath := filepath.Join(sourceDir, zfsSendFile)
	f, err := os.Open(sendPath) // #nosec G304 — sendPath is sourceDir + fixed filename
	if err != nil {
		return fmt.Errorf("opening send file: %w", err)
	}
	defer f.Close()

	// Get file size for progress reporting.
	fi, err := f.Stat()
	if err != nil {
		return fmt.Errorf("stat send file: %w", err)
	}

	cr := &countingReader{
		r:        f,
		total:    fi.Size(),
		name:     item.Name,
		progress: progress,
	}

	var stderr bytes.Buffer
	recvArgs := []string{"recv", "-F", destDataset}

	if err := h.cmd.RunStream(ctx, cr, nil, &stderr, recvArgs...); err != nil {
		return fmt.Errorf("zfs receive: %w", err)
	}

	progress(item.Name, 100, "restore complete")
	return nil
}

// findLatestVaultSnapshot returns the most recent vault-created snapshot on
// the given dataset, or an empty string if none exist.
func (h *ZFSHandler) findLatestVaultSnapshot(ctx context.Context, dataset string) string {
	snapshots, err := h.client.ZFS.ListByType(ctx, gzfs.DatasetTypeSnapshot, false, dataset)
	if err != nil {
		return ""
	}

	var latest string
	for _, snap := range snapshots {
		parts := strings.SplitN(snap.Name, "@", 2)
		if len(parts) != 2 {
			continue
		}
		if strings.HasPrefix(parts[1], snapshotPrefix) {
			latest = snap.Name // Snapshots are listed in creation order; last wins.
		}
	}
	return latest
}

// cleanupOldSnapshots removes vault-created snapshots except the one to keep.
func (h *ZFSHandler) cleanupOldSnapshots(ctx context.Context, dataset string, keepSnapshot string) {
	snapshots, err := h.client.ZFS.ListByType(ctx, gzfs.DatasetTypeSnapshot, false, dataset)
	if err != nil {
		return
	}

	for _, snap := range snapshots {
		// Only remove snapshots created by vault.
		parts := strings.SplitN(snap.Name, "@", 2)
		if len(parts) != 2 {
			continue
		}
		snapSuffix := parts[1]
		if !strings.HasPrefix(snapSuffix, snapshotPrefix) {
			continue
		}
		if snap.Name == keepSnapshot {
			continue
		}
		_ = snap.Destroy(ctx, false, false)
	}
}

// countingWriter wraps an io.Writer and reports progress.
type countingWriter struct {
	w        io.Writer
	written  int64
	total    int64
	name     string
	progress ProgressFunc
}

func (cw *countingWriter) Write(p []byte) (int, error) {
	n, err := cw.w.Write(p)
	cw.written += int64(n)
	if cw.total > 0 && cw.progress != nil {
		pct := int(float64(cw.written) / float64(cw.total) * 80)
		pct = min(pct+10, 90) // Scale to 10-90%
		mb := cw.written / (1024 * 1024)
		cw.progress(cw.name, pct, fmt.Sprintf("sending: %d MB written", mb))
	}
	return n, err
}

// countingReader wraps an io.Reader and reports progress.
type countingReader struct {
	r        io.Reader
	read     int64
	total    int64
	name     string
	progress ProgressFunc
}

func (cr *countingReader) Read(p []byte) (int, error) {
	n, err := cr.r.Read(p)
	cr.read += int64(n)
	if cr.total > 0 && cr.progress != nil {
		pct := int(float64(cr.read) / float64(cr.total) * 80)
		pct = min(pct+10, 90) // Scale to 10-90%
		mb := cr.read / (1024 * 1024)
		cr.progress(cr.name, pct, fmt.Sprintf("receiving: %d MB read", mb))
	}
	return n, err
}

// ZFSPoolInfo describes a ZFS zpool with its root dataset mountpoint.
type ZFSPoolInfo struct {
	Name       string `json:"name"`
	Mountpoint string `json:"mountpoint"`
}

// ListNVMePools discovers ZFS zpools where every data vdev is backed by NVMe
// devices. It returns only pools with valid, accessible mountpoints.
func (h *ZFSHandler) ListNVMePools() ([]ZFSPoolInfo, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	pools, err := h.client.Zpool.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing zpools: %w", err)
	}

	var result []ZFSPoolInfo
	for _, pool := range pools {
		leaves := collectLeafVdevPaths(pool.Vdevs)
		if len(leaves) == 0 {
			continue
		}
		if !allNVMe(leaves) {
			continue
		}

		mountpoint, err := h.poolMountpoint(ctx, pool.Name)
		if err != nil {
			log.Printf("zfs: skipping pool %s: %v", pool.Name, err)
			continue
		}
		mountpoint = filepath.Clean(mountpoint)
		if !isValidMountpoint(mountpoint) {
			continue
		}
		if _, statErr := os.Stat(mountpoint); statErr != nil {
			continue
		}

		result = append(result, ZFSPoolInfo{
			Name:       pool.Name,
			Mountpoint: mountpoint,
		})
	}

	return result, nil
}

// ListZFSMountpoints returns mountpoints for all accessible ZFS pool root
// datasets. Used by the browse API to discover ZFS locations for database or
// staging configuration.
func (h *ZFSHandler) ListZFSMountpoints() ([]ZFSPoolInfo, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	pools, err := h.client.Zpool.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing zpools: %w", err)
	}

	var result []ZFSPoolInfo
	for _, pool := range pools {
		mountpoint, err := h.poolMountpoint(ctx, pool.Name)
		if err != nil {
			continue
		}
		mountpoint = filepath.Clean(mountpoint)
		if !isValidMountpoint(mountpoint) {
			continue
		}
		if _, statErr := os.Stat(mountpoint); statErr != nil {
			continue
		}

		result = append(result, ZFSPoolInfo{
			Name:       pool.Name,
			Mountpoint: mountpoint,
		})
	}

	return result, nil
}

// poolMountpoint retrieves the mountpoint of a pool's root dataset.
func (h *ZFSHandler) poolMountpoint(ctx context.Context, poolName string) (string, error) {
	datasets, err := h.client.ZFS.ListByType(ctx, gzfs.DatasetTypeFilesystem, false, poolName)
	if err != nil {
		return "", fmt.Errorf("listing datasets for pool %s: %w", poolName, err)
	}
	for _, ds := range datasets {
		if ds.Name == poolName {
			return ds.Mountpoint, nil
		}
	}
	return "", fmt.Errorf("root dataset for pool %s not found", poolName)
}

// collectLeafVdevPaths recursively traverses vdev trees and returns device
// paths of leaf vdevs (those with no children).
func collectLeafVdevPaths(vdevs map[string]*gzfs.ZPoolVDEV) []string {
	var paths []string
	for _, v := range vdevs {
		if len(v.Vdevs) > 0 {
			paths = append(paths, collectLeafVdevPaths(v.Vdevs)...)
		} else if v.Path != "" {
			paths = append(paths, v.Path)
		}
	}
	return paths
}

// allNVMe returns true if every path contains "nvme" (case-insensitive).
func allNVMe(paths []string) bool {
	for _, p := range paths {
		if !strings.Contains(strings.ToLower(p), "nvme") {
			return false
		}
	}
	return true
}

// isValidMountpoint returns true if the mountpoint is a usable filesystem path.
// The root filesystem ("/") is explicitly rejected to prevent broadening browse
// access to the entire host.
func isValidMountpoint(mp string) bool {
	switch mp {
	case "", "none", "-", "legacy":
		return false
	}
	cleaned := filepath.Clean(mp)
	if !filepath.IsAbs(cleaned) {
		return false
	}
	return cleaned != string(filepath.Separator)
}
