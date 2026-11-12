package runner

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/tinkerologie/vault/internal/dedup"
	"github.com/tinkerologie/vault/internal/docsmeta"
	"github.com/tinkerologie/vault/internal/engine"
	"github.com/tinkerologie/vault/internal/safepath"
	"github.com/tinkerologie/vault/internal/storage"
	"github.com/tinkerologie/vault/internal/tempdir"
)

type FolderRestoreWork struct {
	Target engine.ReviewedFolderTarget
}

func (FolderRestoreWork) RequiresPassphrase() bool { return false }

func (work FolderRestoreWork) restore(ctx context.Context, r *Runner, approved *ApprovedRestore, passphrase string, reporter restoreProgressReporter) error {
	plan := approved.Plan
	stageOverride, _ := r.db.GetSetting("staging_dir_override", docsmeta.DefaultFor("staging_dir_override"))
	stage, cleanup, err := tempdir.CreateRestoreDirOutside(stageOverride, work.Target.Resolved)
	if err != nil {
		return err
	}
	defer cleanup()

	folder, err := engine.NewFolderHandler()
	if err != nil {
		return err
	}
	item := engine.BackupItem{
		Name: plan.Item.ItemName, Type: "folder",
		Settings: map[string]any{"path": work.Target.Configured, engine.SettingCleanDestination: work.Target.Clean},
	}
	steps := make([]preparedFolderStep, 0, len(plan.Steps))
	for i, step := range plan.Steps {
		manifestID, chunked, err := lookupItemManifest(step.Point, plan.Item.ItemName)
		if err != nil {
			return changedApprovedRestore(err)
		}
		if chunked {
			start := i * 40 / len(plan.Steps)
			r.reportRestoreProgress(reporter, start, "Staging required backup chunks")
			prepared, err := r.stageApprovedFolderChunks(ctx, plan, work.Target.Resolved, manifestID, stage, passphrase)
			if err != nil {
				return err
			}
			defer prepared.cache.Close()
			steps = append(steps, preparedFolderStep{chunks: prepared})
			continue
		}

		dir := filepath.Join(stage, fmt.Sprintf("step-%d", i))
		if err := os.Mkdir(dir, 0o700); err != nil {
			return err
		}
		if len(step.Objects) == 0 {
			return ErrReviewedRestoreChanged
		}
		start, end := i*40/len(plan.Steps), (i+1)*40/len(plan.Steps)
		if err := r.stageRestorePointItemFrom(ctx, plan.Job, plan.Destination, step.Point, plan.Item.ItemName, dir, passphrase, start, end, reporter, step.Objects); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return changedApprovedRestore(err)
		}
		if err := engine.ValidateStagedFolder(ctx, dir, work.Target.Resolved); err != nil {
			return changedApprovedRestore(err)
		}
		steps = append(steps, preparedFolderStep{dir: dir})
	}
	if len(steps) == 0 {
		return ErrReviewedRestoreChanged
	}
	approved.checks = append(approved.checks, FolderDataStaged)
	if err := approved.Revalidate(ctx); err != nil {
		return err
	}
	approved.checks = append(approved.checks, FolderReviewRevalidated)

	writes := make(map[string]os.FileInfo)
	ctx = engine.WithRestoreWrites(engine.WithReviewedFolderTarget(ctx, work.Target), writes)
	for i, step := range steps {
		start, end := 40+i*60/len(steps), 40+(i+1)*60/len(steps)
		target := work.Target
		target.Clean = i == 0 && work.Target.Clean
		item.Settings[engine.SettingCleanDestination] = target.Clean
		progress := r.approvedFolderProgress(reporter, start, end)
		var err error
		if step.chunks != nil {
			err = folder.RestorePreparedChunked(ctx, item, target, step.chunks.manifest, step.chunks.get, progress)
		} else {
			err = folder.Restore(ctx, item, step.dir, progress)
		}
		if errors.Is(err, engine.ErrReviewedFolderTargetChanged) {
			return ErrReviewedRestoreChanged
		}
		if err != nil {
			return err
		}
	}
	if len(steps) > 1 {
		if err := work.Target.Check(); err != nil {
			return ErrReviewedRestoreChanged
		}
		pruneStagedFolder(plan.Point.ID, work.Target.Resolved, steps, writes)
	}
	approved.checks = append(approved.checks, FolderRestoreCompleted)
	return nil
}

func (r *Runner) approvedFolderProgress(reporter restoreProgressReporter, start, end int) engine.ProgressFunc {
	return func(name string, percent int, _ string) {
		if percent < 0 {
			return
		}
		reporter.ItemName = name
		r.reportRestoreProgress(reporter, start+percent*(end-start)/100, "Restoring reviewed folder")
	}
}

type preparedFolderStep struct {
	dir    string
	chunks *preparedFolderChunks
}

func pruneStagedFolder(pointID int64, target string, steps []preparedFolderStep, writes map[string]os.FileInfo) {
	listing, ok := stagedFolderIndex(steps[len(steps)-1].dir, engine.ListingSuffix)
	if !ok {
		return
	}
	indexes := make([]engine.TarIndex, 0, len(steps)-1)
	for _, step := range steps[:len(steps)-1] {
		index, ok := stagedFolderIndex(step.dir, engine.IndexSuffix)
		if !ok {
			return
		}
		indexes = append(indexes, index)
	}
	root, err := safepath.OpenPinnedDir(target)
	if err != nil {
		return
	}
	defer root.Close()
	pruneRestoreWrites(root, pointID, listing, indexes, writes)
}

func stagedFolderIndex(dir, suffix string) (engine.TarIndex, bool) {
	if dir == "" {
		return engine.TarIndex{}, false
	}
	files, err := os.ReadDir(dir)
	if err != nil {
		return engine.TarIndex{}, false
	}
	for _, file := range files {
		if file.IsDir() || !isSidecarBase(file.Name(), suffix) {
			continue
		}
		body, err := os.Open(filepath.Join(dir, file.Name())) // #nosec G304 -- entries are enumerated in private staging
		if err != nil {
			return engine.TarIndex{}, false
		}
		index, err := engine.ReadTarIndex(body)
		_ = body.Close()
		return index, err == nil
	}
	return engine.TarIndex{}, false
}

type stagedChunk struct {
	offset int64
	length int
}

type preparedFolderChunks struct {
	manifest dedup.Manifest
	cache    *os.File
	chunks   map[dedup.ID]stagedChunk
}

func (p *preparedFolderChunks) get(id dedup.ID) ([]byte, error) {
	chunk, found := p.chunks[id]
	if !found {
		return nil, ErrReviewedRestoreChanged
	}
	data := make([]byte, chunk.length)
	_, err := p.cache.ReadAt(data, chunk.offset)
	return data, err
}

func (r *Runner) stageApprovedFolderChunks(ctx context.Context, plan ApprovedRestorePlan, target string, manifestID dedup.ID, stage, passphrase string) (*preparedFolderChunks, error) {
	adapter, err := newRunAdapter(ctx, plan.Destination)
	if err != nil {
		return nil, changedApprovedRestore(err)
	}
	defer storage.CloseAdapter(adapter)
	stopOnCancel := context.AfterFunc(ctx, func() { storage.CloseAdapter(adapter) })
	defer stopOnCancel()
	repo, err := OpenDedupRepoWithFallback(r.db, adapter, plan.Destination.ID, r.serverKey, passphrase)
	if err != nil {
		return nil, changedApprovedRestore(err)
	}
	manifest, err := repo.GetManifest(manifestID)
	if err != nil {
		return nil, changedApprovedRestore(err)
	}
	cache, err := os.CreateTemp(stage, "chunks-")
	if err != nil {
		return nil, err
	}
	var complete bool
	defer func() {
		if !complete {
			_ = cache.Close()
		}
	}()
	chunks := make(map[dedup.ID]stagedChunk)
	var offset int64
	for name, entry := range manifest.Files {
		if _, err := safepath.JoinUnderBase(target, name, entry.IsDir); err != nil {
			return nil, changedApprovedRestore(err)
		}
		var size int64
		for _, id := range entry.Chunks {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			chunk, found := chunks[id]
			if !found {
				data, err := repo.Get(id)
				if err != nil {
					return nil, changedApprovedRestore(err)
				}
				if _, err := cache.Write(data); err != nil {
					return nil, err
				}
				chunk = stagedChunk{offset: offset, length: len(data)}
				offset += int64(len(data))
				chunks[id] = chunk
			}
			size += int64(chunk.length)
			r.lastProgressMu.Lock()
			r.lastProgress = time.Now()
			r.lastProgressMu.Unlock()
		}
		if !entry.IsDir && size != entry.Size {
			return nil, ErrReviewedRestoreChanged
		}
	}
	storage.DiagStatsFrom(ctx).AddBytesRead(repo.SessionStats().ReadBytes)
	complete = true
	return &preparedFolderChunks{manifest: manifest, cache: cache, chunks: chunks}, nil
}
