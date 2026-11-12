package engine

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/distribution/reference"
	"github.com/moby/moby/client"

	"github.com/tinkerologie/vault/internal/bounded"
	"github.com/tinkerologie/vault/internal/dedup"
	"github.com/tinkerologie/vault/internal/safepath"
)

const ContainerReviewMetadataLimit int64 = 4 << 20

// ContainerRestoreEffects describes the saved configuration's effects without
// exposing the inspect document, environment, labels, or template contents.
type ContainerRestoreEffects struct {
	Name           string                  `json:"name"`
	Image          string                  `json:"image"`
	ImageID        string                  `json:"image_id,omitempty"`
	ImageDigest    string                  `json:"image_digest,omitempty"`
	Destination    string                  `json:"destination,omitempty"`
	Start          bool                    `json:"start"`
	Template       bool                    `json:"template"`
	DatabaseDump   bool                    `json:"database_dump"`
	DatabaseReplay bool                    `json:"database_replay"`
	Binds          []string                `json:"binds,omitempty"`
	Volumes        []ContainerVolumeEffect `json:"volumes"`
}

type ContainerVolumeEffect struct {
	Mount     string `json:"mount"`
	Target    string `json:"target"`
	WriteMode string `json:"write_mode"`
}

// ContainerBackupMetadata carries the bounded sidecars and the archive names
// selected from one classic Restore Point.
type ContainerBackupMetadata struct {
	Inspect  []byte
	Volumes  []byte
	Template []byte
	Files    []string
}

// ContainerRestoreReview retains the reviewed paths and configuration identity
// for an independent comparison against the staged artifacts before execution.
type ContainerRestoreReview struct {
	Effects          ContainerRestoreEffects
	InspectDigest    [32]byte
	TemplateDigest   [32]byte
	VolumeMetaDigest [32]byte
}

var ErrContainerReviewChanged = errors.New("reviewed container configuration or location changed")

func (h *ContainerHandler) ReviewClassicRestore(ctx context.Context, item BackupItem, steps []ContainerBackupMetadata, destination string, clean bool) (ContainerRestoreReview, error) {
	if len(steps) == 0 {
		return ContainerRestoreReview{}, errors.New("missing required container backup data")
	}
	var config, volumes, template []byte
	files := map[string]bool{}
	for _, step := range steps {
		if len(step.Inspect) != 0 {
			config = step.Inspect
		}
		if step.Volumes != nil {
			volumes = step.Volumes
		}
		if step.Template != nil {
			template = step.Template
		}
		archives, err := reviewedContainerArchives(step, len(steps) > 1)
		if err != nil {
			return ContainerRestoreReview{}, err
		}
		for archive := range archives {
			files[archive] = true
		}
	}
	if !files["image.tar"] {
		return ContainerRestoreReview{}, errors.New("backup has no container image archive")
	}
	manifest, err := decodeContainerVolumes(volumes)
	if err != nil {
		return ContainerRestoreReview{}, err
	}
	hasDump := len(steps) == 1 && (files[DatabaseDumpFile] || files[DatabaseDumpFile+".gz"] || files[DatabaseDumpFile+".zst"])
	replay := hasDump && files[DatabaseReplayMarker]
	result, err := h.reviewContainerRestore(ctx, item, config, template, nil, destination, clean, hasDump, replay,
		func(mount mountInfo, i int) (string, error) {
			entry, found := findVolumeManifestEntry(manifest, mount.Source, i)
			for _, base := range volumeArchiveCandidates(entry.Archive, mount.Source, legacyVolumeIndex(entry, found, volumes != nil, i)) {
				if files[base] {
					if entry.IsFile {
						return "overwrite_file", nil
					}
					return "directory", nil
				}
			}
			if restorableVolume(mount.Type, mount.Name) &&
				(!found || entry.BackedUp || entry.SkipReason == "" || entry.SkipReason == "unchanged since reference") {
				return "", errors.New("required container volume archive is missing")
			}
			return "configuration_only", nil
		})
	result.VolumeMetaDigest = sha256.Sum256(volumes)
	return result, err
}

// ContainerRestoreRepository is the read interface used by a chunked restore.
// Approved restores supply the same manifests and chunks from private staging.
type ContainerRestoreRepository interface {
	Get(dedup.ID) ([]byte, error)
	GetManifest(dedup.ID) (dedup.Manifest, error)
}

func (h *ContainerHandler) ReviewChunkedRestore(ctx context.Context, item BackupItem, m dedup.Manifest, repo ContainerRestoreRepository, destination string, clean bool) (ContainerRestoreReview, error) {
	config, err := containerReviewBlob(repo, m, containerInspectKey)
	if err != nil || config == nil {
		return ContainerRestoreReview{}, errors.New("cannot read saved container inspect data")
	}
	template, err := containerReviewBlob(repo, m, containerTemplateKey)
	if err != nil {
		return ContainerRestoreReview{}, err
	}
	var meta imageMeta
	rawMeta, err := containerReviewBlob(repo, m, containerImageMetaKey)
	if err != nil {
		return ContainerRestoreReview{}, err
	}
	if rawMeta != nil {
		if err := json.Unmarshal(rawMeta, &meta); err != nil {
			return ContainerRestoreReview{}, err
		}
	}
	// A non-nil slice selects chunked image handling, including backups
	// that did not record a registry digest.
	digests := append([]string{}, meta.RepoDigests...)
	dump, hasDump := m.Files[ContainerDBDumpKey]
	hasDump = hasDump && len(dump.Chunks) > 0
	_, replay := m.Files[ContainerDBReplayKey]
	return h.reviewContainerRestore(ctx, item, config, template, digests, destination, clean, hasDump, hasDump && replay,
		func(mount mountInfo, _ int) (string, error) {
			if e, ok := m.Files[containerVolFilePrefix+mount.Destination]; ok && e.Size >= 0 && (e.Size == 0 || len(e.Chunks) > 0) {
				return "overwrite_file", nil
			}
			if e, ok := m.Files[containerVolPrefix+mount.Destination]; ok && e.Size != volumeSkippedSize && len(e.Chunks) > 0 {
				return "directory", nil
			}
			return "configuration_only", nil
		})
}

func containerReviewBlob(repo ContainerRestoreRepository, manifest dedup.Manifest, name string) ([]byte, error) {
	entry, ok := manifest.Files[name]
	if !ok {
		return nil, nil
	}
	if entry.Size < 0 || entry.Size > ContainerReviewMetadataLimit || len(entry.Chunks) != 1 {
		return nil, errors.New("invalid container restore metadata entry")
	}
	data, err := repo.Get(entry.Chunks[0])
	if err != nil {
		return nil, err
	}
	if int64(len(data)) != entry.Size {
		return nil, errors.New("invalid container restore metadata length")
	}
	return data, nil
}

func (h *ContainerHandler) reviewContainerRestore(ctx context.Context, item BackupItem, config, template []byte, digests []string, destination string, clean, hasDump, replay bool, volumeMode func(mountInfo, int) (string, error)) (ContainerRestoreReview, error) {
	var inspect restoreInspect
	if err := json.Unmarshal(config, &inspect); err != nil {
		return ContainerRestoreReview{}, err
	}
	trustErr := enforceRestorePrivileges(ctx, item, inspect)
	name, err := normalizeRestoreComponent(restoredContainerName(item, inspect))
	if err != nil {
		return ContainerRestoreReview{}, err
	}
	if _, err := reference.ParseNormalizedNamed(inspect.Config.Image); err != nil {
		return ContainerRestoreReview{}, errors.New("saved configuration has an invalid Docker image name")
	}
	result := ContainerRestoreReview{
		InspectDigest: sha256.Sum256(config), TemplateDigest: sha256.Sum256(template),
		Effects: ContainerRestoreEffects{Name: name, Image: inspect.Config.Image, ImageID: inspect.ImageID, Start: inspect.State.Running,
			Template: template != nil, DatabaseDump: hasDump, DatabaseReplay: replay && inspect.State.Running,
			Volumes: []ContainerVolumeEffect{}},
	}
	if digests != nil {
		digest, err := backupImageRef(inspect.Config.Image, digests)
		if err != nil {
			trustErr = fmt.Errorf("%w: backup-time image unavailable: %w", ErrRestoreNeedsApproval, err)
		}
		result.Effects.ImageDigest = digest
	}
	if destination != "" {
		if _, err := safepath.CheckCustomRestoreDestination(destination); err != nil {
			return ContainerRestoreReview{}, err
		}
		resolved, err := normalizeRestorePath(destination)
		if err != nil {
			return ContainerRestoreReview{}, err
		}
		destination = resolved
	}
	result.Effects.Destination = destination
	binds, _, err := containerRestoreBinds(inspect.HostConfig.Binds, destination)
	if err != nil {
		return ContainerRestoreReview{}, err
	}
	result.Effects.Binds = binds
	mounts := inspect.mountInfos()
	if err := checkVolumeTargetCollisions(destination, mounts); err != nil {
		return ContainerRestoreReview{}, err
	}
	for i, mount := range mounts {
		if !backupableMount(mount.Type) || mount.Source == "" || mount.Destination == "" {
			continue
		}
		mode, err := volumeMode(mount, i)
		if err != nil {
			return ContainerRestoreReview{}, err
		}
		target, err := volumeRestoreTarget(destination, mount.Type, mount.Name, mount.Source)
		if err != nil {
			return ContainerRestoreReview{}, err
		}
		if destination == "" && mode != "configuration_only" {
			if err := authorizeVolumeSources([]string{mount.Source}, nil); err != nil {
				if err := authorizeVolumeSources([]string{mount.Source}, h.liveMountSources(ctx, item.Name)); err != nil {
					return ContainerRestoreReview{}, err
				}
			}
		}
		// A mount with no saved data is still applied in the recreated
		// configuration. Preserve its path for review even when it would
		// not pass Vault's independent rules for extracting backup files.
		if mode != "configuration_only" || destination != "" {
			target, err = normalizeRestorePath(target)
			if err != nil {
				return ContainerRestoreReview{}, err
			}
		}
		if mode == "directory" {
			mode = "merge"
			if clean && CanClearRestoreTarget(target) {
				mode = "replace"
			}
		}
		result.Effects.Volumes = append(result.Effects.Volumes, ContainerVolumeEffect{Mount: mount.Destination, Target: target, WriteMode: mode})
	}
	return result, trustErr
}

func decodeContainerVolumes(body []byte) ([]volumeManifestEntry, error) {
	var manifest []volumeManifestEntry
	if body != nil {
		if err := json.Unmarshal(body, &manifest); err != nil {
			return nil, err
		}
	}
	return manifest, nil
}

// containerArchiveMapping reproduces the classic chain merge's stable volume
// keys from the same manifests without extracting archives during review.
func reviewedContainerArchives(step ContainerBackupMetadata, merged bool) (map[string]string, error) {
	manifest, err := decodeContainerVolumes(step.Volumes)
	if err != nil {
		return nil, err
	}
	sources := map[string]string{}
	for _, entry := range manifest {
		if entry.Archive != "" && entry.Source != "" {
			sources[TarBaseName(entry.Archive)] = entry.Source
		}
	}
	out := map[string]string{}
	for _, file := range step.Files {
		base := TarBaseName(filepath.Base(file))
		if (strings.HasPrefix(base, "volume_") || base == "image.tar") && !strings.HasSuffix(file, ".tar") &&
			!strings.HasSuffix(file, ".tar.gz") && !strings.HasSuffix(file, ".tar.zst") {
			continue
		}
		canonical := base
		if source := sources[base]; merged && source != "" {
			canonical = volumeArchiveBase(source)
		}
		out[canonical] = base
	}
	for _, entry := range manifest {
		if !entry.BackedUp {
			continue
		}
		available := false
		for _, base := range volumeArchiveCandidates(entry.Archive, entry.Source, entry.Index) {
			key := base
			if source := sources[base]; merged && source != "" {
				key = volumeArchiveBase(source)
			}
			if _, ok := out[key]; ok {
				available = true
				break
			}
		}
		if !available {
			return nil, errors.New("recorded container volume archive is missing")
		}
	}
	return out, nil
}

func (review ContainerRestoreReview) matches(other ContainerRestoreReview) bool {
	effects, fresh := review.Effects, other.Effects
	volumes, freshVolumes := effects.Volumes, fresh.Volumes
	effects.Volumes, fresh.Volumes = nil, nil
	return review.InspectDigest == other.InspectDigest && review.TemplateDigest == other.TemplateDigest &&
		review.VolumeMetaDigest == other.VolumeMetaDigest &&
		effects.Name == fresh.Name && effects.Image == fresh.Image && effects.ImageID == fresh.ImageID && effects.ImageDigest == fresh.ImageDigest &&
		effects.Destination == fresh.Destination && effects.Start == fresh.Start && effects.Template == fresh.Template &&
		effects.DatabaseDump == fresh.DatabaseDump && effects.DatabaseReplay == fresh.DatabaseReplay &&
		slices.Equal(effects.Binds, fresh.Binds) && slices.Equal(volumes, freshVolumes)
}

func readStagedContainerMetadata(dir string) (ContainerBackupMetadata, error) {
	var result ContainerBackupMetadata
	for name, target := range map[string]*[]byte{
		"config.json": &result.Inspect, "volumes.json": &result.Volumes, containerTemplateFile: &result.Template,
	} {
		file, err := os.Open(filepath.Join(dir, name)) // #nosec G304 -- fixed filenames within private restore staging
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return ContainerBackupMetadata{}, err
		}
		*target, err = bounded.ReadAll(file, ContainerReviewMetadataLimit)
		_ = file.Close()
		if err != nil {
			return ContainerBackupMetadata{}, err
		}
	}
	files, err := os.ReadDir(dir)
	if err != nil {
		return ContainerBackupMetadata{}, err
	}
	for _, file := range files {
		if file.Type().IsRegular() {
			result.Files = append(result.Files, file.Name())
		}
	}
	return result, nil
}

type reviewedContainerKey struct{}

type reviewedContainerRestore struct {
	review     ContainerRestoreReview
	record     func(string)
	existingID string
}

// WithReviewedContainerRestore applies the operator's reviewed configuration
// and targets at the engine boundary and collects completed readiness checks.
func WithReviewedContainerRestore(ctx context.Context, review ContainerRestoreReview, record func(string)) context.Context {
	return context.WithValue(ctx, reviewedContainerKey{}, &reviewedContainerRestore{review: review, record: record})
}

func reviewedContainerFrom(ctx context.Context) (*reviewedContainerRestore, bool) {
	reviewed, ok := ctx.Value(reviewedContainerKey{}).(*reviewedContainerRestore)
	return reviewed, ok
}

func recordContainerCheck(ctx context.Context, check string) {
	if reviewed, ok := reviewedContainerFrom(ctx); ok {
		reviewed.record(check)
	}
}

const (
	ContainerStarted              = "container_started"
	ContainerStartFailed          = "container_start_failed"
	ContainerDatabaseReady        = "container_database_ready"
	ContainerDatabaseNotReady     = "container_database_not_ready"
	ContainerDatabaseReplayed     = "container_database_replayed"
	ContainerDatabaseReplayFailed = "container_database_replay_failed"
)

func checkReviewedVolumeTarget(ctx context.Context, mount, target string) error {
	if reviewed, ok := reviewedContainerFrom(ctx); ok {
		for _, volume := range reviewed.review.Effects.Volumes {
			if volume.Mount == mount && volume.Target == target && volume.WriteMode != "configuration_only" {
				return nil
			}
		}
		return ErrContainerReviewChanged
	}
	return nil
}

// The daemon can report a load failure in a successful HTTP response. Stop
// before touching the live container or extracting volumes in that case.
func checkContainerImageLoad(body io.Reader) error {
	reader := bufio.NewScanner(body)
	reader.Buffer(make([]byte, 4096), 1<<20)
	for reader.Scan() {
		if len(reader.Bytes()) == 0 {
			continue
		}
		var message struct {
			Error       string `json:"error"`
			ErrorDetail *struct {
				Message string `json:"message"`
			} `json:"errorDetail"`
		}
		if err := json.Unmarshal(reader.Bytes(), &message); err != nil {
			return errors.New("Docker returned an invalid image load response")
		}
		if message.Error != "" || (message.ErrorDetail != nil && message.ErrorDetail.Message != "") {
			return errors.New("Docker rejected the saved image")
		}
	}
	return reader.Err()
}

func (h *ContainerHandler) checkReviewedClassicImage(ctx context.Context, inspect restoreInspect) error {
	if inspect.ImageID == "" {
		return nil
	}
	image, err := h.cli.ImageInspect(ctx, inspect.Config.Image)
	if err != nil {
		return fmt.Errorf("checking loaded backup image: %w", err)
	}
	if image.ID != inspect.ImageID {
		return errors.New("loaded image differs from the saved container image")
	}
	return nil
}

func (h *ContainerHandler) stopReviewedContainer(ctx context.Context) error {
	reviewed, ok := reviewedContainerFrom(ctx)
	if !ok {
		return nil
	}
	result, err := h.cli.ContainerInspect(ctx, reviewed.review.Effects.Name, client.ContainerInspectOptions{})
	if cerrdefs.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspecting the container before replacing its volumes: %w", err)
	}
	existing := result.Container
	if existing.ID == "" || existing.State == nil {
		return errors.New("Docker returned no identity or state for the existing container")
	}
	reviewed.existingID = existing.ID
	if existing.State.Running {
		if _, err := h.cli.ContainerStop(ctx, existing.ID, client.ContainerStopOptions{}); err != nil {
			return fmt.Errorf("stopping the container before replacing its volumes: %w", err)
		}
	}
	return nil
}

func (h *ContainerHandler) removeReviewedContainer(ctx context.Context, reviewed *reviewedContainerRestore) error {
	result, err := h.cli.ContainerInspect(ctx, reviewed.review.Effects.Name, client.ContainerInspectOptions{})
	if cerrdefs.IsNotFound(err) && reviewed.existingID == "" {
		return nil
	}
	if err != nil {
		return fmt.Errorf("rechecking the existing container before removal: %w", err)
	}
	if reviewed.existingID == "" || reviewed.existingID != result.Container.ID ||
		result.Container.State == nil || result.Container.State.Running {
		return ErrContainerReviewChanged
	}
	if _, err := h.cli.ContainerRemove(ctx, reviewed.existingID, client.ContainerRemoveOptions{Force: true}); err != nil {
		return fmt.Errorf("removing the existing container: %w", err)
	}
	return nil
}

func validateReviewedContainerArchives(ctx context.Context, dir string, inspect restoreInspect, review ContainerRestoreReview) error {
	manifest, present, err := readVolumeManifest(dir)
	if err != nil {
		return err
	}
	for i, mount := range inspect.Mounts {
		volumeIndex := slices.IndexFunc(review.Effects.Volumes, func(volume ContainerVolumeEffect) bool {
			return volume.Mount == mount.Destination && volume.WriteMode != "configuration_only"
		})
		if volumeIndex < 0 {
			continue
		}
		volume := review.Effects.Volumes[volumeIndex]
		entry, found := findVolumeManifestEntry(manifest, mount.Source, i)
		archive, err := resolveVolumeArchive(dir, entry.Archive, mount.Source, legacyVolumeIndex(entry, found, present, i))
		if err != nil {
			return err
		}
		if err := validateStagedVolumeArchive(ctx, archive, volume.Target); err != nil {
			return err
		}
	}
	return nil
}

func checkReviewedContainerPaths(ctx context.Context, inspect restoreInspect, restoreDest string) error {
	reviewed, ok := reviewedContainerFrom(ctx)
	if !ok {
		return nil
	}
	if restoreDest != reviewed.review.Effects.Destination {
		return ErrContainerReviewChanged
	}
	if restoreDest != "" {
		resolved, err := normalizeRestorePath(restoreDest)
		if err != nil || resolved != restoreDest {
			return ErrContainerReviewChanged
		}
	}
	for _, mount := range inspect.Mounts {
		for _, volume := range reviewed.review.Effects.Volumes {
			if volume.Mount != mount.Destination ||
				(restoreDest == "" && volume.WriteMode == "configuration_only") {
				continue
			}
			target, err := volumeRestoreTarget(restoreDest, mount.Type, mount.Name, mount.Source)
			if err != nil {
				return ErrContainerReviewChanged
			}
			resolved, err := normalizeRestorePath(target)
			if err != nil || resolved != volume.Target {
				return ErrContainerReviewChanged
			}
		}
	}
	return nil
}
