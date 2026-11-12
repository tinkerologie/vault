package runner

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/tinkerologie/vault/internal/bounded"
	"github.com/tinkerologie/vault/internal/crypto"
	"github.com/tinkerologie/vault/internal/dedup"
	"github.com/tinkerologie/vault/internal/docsmeta"
	"github.com/tinkerologie/vault/internal/engine"
	"github.com/tinkerologie/vault/internal/safepath"
	"github.com/tinkerologie/vault/internal/storage"
	"github.com/tinkerologie/vault/internal/tempdir"
)

type ContainerRestoreWork struct {
	Reviewed        engine.ContainerRestoreReview
	Destination     string
	Clean           bool
	Trusted         bool
	NeedsPassphrase bool
}

func (work ContainerRestoreWork) RequiresPassphrase() bool { return work.NeedsPassphrase }

func (r *Runner) ReviewContainerRestore(ctx context.Context, plan ApprovedRestorePlan, destination, passphrase string, clean bool) (ContainerRestoreWork, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	select {
	case approvedReadSlots <- struct{}{}:
	default:
		return ContainerRestoreWork{}, ErrRunnerBusy
	}
	type result struct {
		work ContainerRestoreWork
		err  error
	}
	done := make(chan result, 1)
	go func() {
		defer func() { <-approvedReadSlots }()
		defer func() {
			if recover() != nil {
				done <- result{err: ErrApprovedStorageUnavailable}
			}
		}()
		work, err := r.reviewContainerRestore(ctx, plan, destination, passphrase, clean)
		done <- result{work: work, err: err}
	}()
	select {
	case result := <-done:
		return result.work, result.err
	case <-ctx.Done():
		return ContainerRestoreWork{}, ErrApprovedStorageUnavailable
	}
}

func (r *Runner) reviewContainerRestore(ctx context.Context, plan ApprovedRestorePlan, destination, passphrase string, clean bool) (ContainerRestoreWork, error) {
	adapter, err := storage.NewAdapterWithOptions(plan.Destination.Type, plan.Destination.Config, storage.Options{MetadataProbeContext: ctx})
	if err != nil {
		return ContainerRestoreWork{}, ErrApprovedStorageUnavailable
	}
	defer storage.CloseAdapter(adapter)
	stop := context.AfterFunc(ctx, func() { storage.CloseAdapter(adapter) })
	defer stop()
	handler, err := engine.NewContainerHandler()
	if err != nil {
		return ContainerRestoreWork{}, ErrApprovedStorageUnavailable
	}
	work := ContainerRestoreWork{Destination: destination, Clean: clean, Trusted: true}
	for _, step := range plan.Steps {
		work.Trusted = work.Trusted && r.restorePointVouched(step.Point, plan.Item.ItemName)
	}
	ctx = engine.WithRestoreTrust(ctx, engine.RestoreTrust{ArtifactTrusted: work.Trusted})
	item := work.backupItem(plan.Item.ItemName)
	id, chunked, err := lookupItemManifest(plan.Point, plan.Item.ItemName)
	if err != nil {
		return ContainerRestoreWork{}, err
	}
	if chunked {
		repo, err := OpenDedupRepoWithFallback(r.db, adapter, plan.Destination.ID, r.serverKey)
		if errors.Is(err, dedup.ErrServerKeyMismatch) {
			work.NeedsPassphrase = true
			repo, err = OpenDedupRepoWithFallback(r.db, adapter, plan.Destination.ID, r.serverKey, passphrase)
		}
		if errors.Is(err, dedup.ErrServerKeyMismatch) {
			return work, ErrRestorePassphrase
		}
		if err != nil {
			return work, ErrApprovedStorageUnavailable
		}
		manifest, err := repo.GetManifest(id)
		if err != nil {
			return work, err
		}
		work.Reviewed, err = handler.ReviewChunkedRestore(ctx, item, manifest, repo, destination, clean)
		return work, err
	}

	remaining := 16 << 20
	var steps []engine.ContainerBackupMetadata
	for _, step := range plan.Steps {
		var metadata engine.ContainerBackupMetadata
		for _, object := range step.Objects {
			if err := ctx.Err(); err != nil {
				return work, ErrApprovedStorageUnavailable
			}
			name := localRestoreName(filepath.Base(object.Path))
			metadata.Files = append(metadata.Files, name)
			var target *[]byte
			switch name {
			case "config.json":
				target = &metadata.Inspect
			case "volumes.json":
				target = &metadata.Volumes
			case "template.xml":
				target = &metadata.Template
			default:
				continue
			}
			*target, err = readApprovedRestoreMetadata(adapter, object, passphrase, min(engine.ContainerReviewMetadataLimit, int64(remaining)))
			if err != nil {
				return work, err
			}
			remaining -= len(*target)
		}
		steps = append(steps, metadata)
	}
	work.Reviewed, err = handler.ReviewClassicRestore(ctx, item, steps, destination, clean)
	return work, err
}

func readApprovedRestoreMetadata(adapter storage.Adapter, file storage.FileInfo, passphrase string, limit int64) ([]byte, error) {
	name := filepath.Base(file.Path)
	if filepath.Ext(name) == ".age" && passphrase == "" {
		return nil, ErrRestorePassphrase
	}
	// Bound the transport stream as well as the decompressed output.
	if file.Size > 2*limit {
		return nil, bounded.ErrTooLarge
	}
	input, err := adapter.Read(file.Path)
	if err != nil {
		if storage.IsNotExist(err) {
			return nil, ErrReviewedRestoreChanged
		}
		return nil, ErrApprovedStorageUnavailable
	}
	defer input.Close()
	plain, closePlain, _, err := OpenStoredStream(io.LimitReader(input, 2*limit+1), name, passphrase)
	if crypto.IsWrongPassphrase(err) {
		return nil, ErrRestorePassphrase
	}
	if err != nil {
		return nil, err
	}
	defer closePlain() //nolint:errcheck
	return bounded.ReadAll(plain, limit)
}

func (work ContainerRestoreWork) backupItem(name string) engine.BackupItem {
	return engine.BackupItem{Name: name, Type: "container", Settings: map[string]any{
		"restore_destination": work.Destination, engine.SettingCleanDestination: work.Clean,
	}}
}

func (work ContainerRestoreWork) restore(ctx context.Context, r *Runner, approved *ApprovedRestore, passphrase string, reporter restoreProgressReporter) error {
	plan := approved.Plan
	stageOverride, _ := r.db.GetSetting("staging_dir_override", docsmeta.DefaultFor("staging_dir_override"))
	var targets []string
	if work.Reviewed.Effects.Destination != "" {
		targets = append(targets, work.Reviewed.Effects.Destination)
	}
	for _, volume := range work.Reviewed.Effects.Volumes {
		targets = append(targets, volume.Target)
	}
	stage, cleanup, err := tempdir.CreateRestoreDirOutside(stageOverride, targets...)
	if err != nil {
		return err
	}
	defer cleanup()
	handler, err := engine.NewContainerHandler()
	if err != nil {
		return err
	}
	ctx = engine.WithRestoreTrust(ctx, engine.RestoreTrust{ArtifactTrusted: work.Trusted})
	ctx = engine.WithReviewedContainerRestore(ctx, work.Reviewed, func(check string) {
		approved.checks = append(approved.checks, check)
	})
	item := work.backupItem(plan.Item.ItemName)
	progress := func(_ string, percent int, _ string) {
		if percent >= 0 {
			r.reportRestoreProgress(reporter, 40+percent*60/100, "Restoring reviewed container")
		}
	}

	id, chunked, err := lookupItemManifest(plan.Point, plan.Item.ItemName)
	if err != nil {
		return changedApprovedRestore(err)
	}
	if chunked {
		prepared, err := r.stageApprovedContainerChunks(ctx, plan, id, passphrase, stage, reporter)
		if err != nil {
			return err
		}
		defer prepared.cache.Close()
		if err := approved.revalidateStaged(ctx); err != nil {
			return err
		}
		return approvedContainerResult(handler.RestorePreparedChunked(ctx, item, prepared, id, work.Destination, progress))
	}

	stepDirs := make([]string, 0, len(plan.Steps))
	for i, step := range plan.Steps {
		dir := filepath.Join(stage, fmt.Sprintf("step-%d", i))
		if err := os.Mkdir(dir, 0o700); err != nil {
			return err
		}
		if len(step.Objects) == 0 {
			return ErrReviewedRestoreChanged
		}
		if err := r.stageRestorePointItemFrom(ctx, plan.Job, plan.Destination, step.Point, plan.Item.ItemName,
			dir, passphrase, i*40/len(plan.Steps), (i+1)*40/len(plan.Steps), reporter, step.Objects); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return changedApprovedRestore(err)
		}
		stepDirs = append(stepDirs, dir)
	}
	if len(stepDirs) == 0 {
		return ErrReviewedRestoreChanged
	}
	source := stepDirs[0]
	if len(stepDirs) > 1 {
		source = filepath.Join(stage, "merged")
		if err := engine.MergeContainerChainStaging(ctx, stepDirs, source); err != nil {
			return changedApprovedRestore(err)
		}
	}
	if err := approved.revalidateStaged(ctx); err != nil {
		return err
	}
	if err := approvedContainerResult(handler.Restore(ctx, item, source, progress)); err != nil {
		return err
	}
	if len(stepDirs) > 1 {
		r.pruneVouchedContainerChain(approvedRestorePoints(plan), plan.Item.ItemName, stepDirs, source,
			work.Reviewed.Effects.Destination, reporter)
	}
	return nil
}

func approvedContainerResult(err error) error {
	if errors.Is(err, engine.ErrContainerReviewChanged) {
		return ErrReviewedRestoreChanged
	}
	return err
}

type stagedContainerRepository struct {
	preparedFolderChunks
	manifests map[dedup.ID]dedup.Manifest
}

func (repo *stagedContainerRepository) Get(id dedup.ID) ([]byte, error) {
	return repo.get(id)
}

func (repo *stagedContainerRepository) GetManifest(id dedup.ID) (dedup.Manifest, error) {
	manifest, found := repo.manifests[id]
	if !found {
		return dedup.Manifest{}, ErrReviewedRestoreChanged
	}
	return manifest, nil
}

func (r *Runner) stageApprovedContainerChunks(ctx context.Context, plan ApprovedRestorePlan, id dedup.ID, passphrase, stage string, reporter restoreProgressReporter) (*stagedContainerRepository, error) {
	r.reportRestoreProgress(reporter, 0, "Staging required backup chunks")
	adapter, err := newRunAdapter(ctx, plan.Destination)
	if err != nil {
		return nil, changedApprovedRestore(err)
	}
	defer storage.CloseAdapter(adapter)
	stop := context.AfterFunc(ctx, func() { storage.CloseAdapter(adapter) })
	defer stop()
	repo, err := OpenDedupRepoWithFallback(r.db, adapter, plan.Destination.ID, r.serverKey, passphrase)
	if err != nil {
		return nil, changedApprovedRestore(err)
	}
	manifest, err := repo.GetManifest(id)
	if err != nil {
		return nil, changedApprovedRestore(err)
	}
	cache, err := os.CreateTemp(stage, "chunks-")
	if err != nil {
		return nil, err
	}
	prepared := &stagedContainerRepository{
		preparedFolderChunks: preparedFolderChunks{cache: cache, chunks: make(map[dedup.ID]stagedChunk)},
		manifests:            map[dedup.ID]dedup.Manifest{id: manifest},
	}
	var complete bool
	defer func() {
		if !complete {
			_ = cache.Close()
		}
	}()
	var offset int64
	stageEntry := func(entry dedup.ManifestEntry) error {
		var size int64
		for _, chunkID := range entry.Chunks {
			if err := ctx.Err(); err != nil {
				return err
			}
			chunk, found := prepared.chunks[chunkID]
			if !found {
				data, err := repo.Get(chunkID)
				if err != nil {
					return changedApprovedRestore(err)
				}
				if _, err := cache.Write(data); err != nil {
					return err
				}
				chunk = stagedChunk{offset: offset, length: len(data)}
				prepared.chunks[chunkID] = chunk
				offset += int64(chunk.length)
				r.lastProgressMu.Lock()
				r.lastProgress = time.Now()
				r.lastProgressMu.Unlock()
			}
			size += int64(chunk.length)
		}
		if !entry.IsDir && size != entry.Size {
			return ErrReviewedRestoreChanged
		}
		return nil
	}
	for name, entry := range manifest.Files {
		if _, volume := engine.ContainerVolumeDest(name); !volume {
			if entry.Size >= 0 {
				if err := stageEntry(entry); err != nil {
					return nil, err
				}
			}
			continue
		}
		if engine.IsSkippedVolumeEntry(entry) {
			continue
		}
		if len(entry.Chunks) != 1 {
			return nil, ErrReviewedRestoreChanged
		}
		sub, err := repo.GetManifest(entry.Chunks[0])
		if err != nil {
			return nil, changedApprovedRestore(err)
		}
		prepared.manifests[entry.Chunks[0]] = sub
		for path, file := range sub.Files {
			if _, err := safepath.NormalizeRelative(path, file.IsDir); err != nil {
				return nil, ErrReviewedRestoreChanged
			}
			if err := stageEntry(file); err != nil {
				return nil, err
			}
		}
	}
	storage.DiagStatsFrom(ctx).AddBytesRead(repo.SessionStats().ReadBytes)
	complete = true
	return prepared, nil
}
