package engine

import (
	"context"
	"errors"
	"fmt"
	"log"
	"slices"

	"github.com/distribution/reference"
	"github.com/moby/moby/client"
)

// backupImageRef returns the immutable reference for the image a chunked backup
// captured: the config's own digest when it has one, otherwise the recorded
// digest from the same repository. A digest from any other repository is ignored
// so backup metadata cannot redirect the pull.
func backupImageRef(image string, repoDigests []string) (string, error) {
	named, err := reference.ParseNormalizedNamed(image)
	if err != nil {
		return "", fmt.Errorf("parsing image %q: %w", image, err)
	}
	if canonical, ok := named.(reference.Canonical); ok {
		return canonical.String(), nil
	}
	for _, rd := range repoDigests {
		parsed, parseErr := reference.ParseNormalizedNamed(rd)
		if parseErr != nil {
			continue
		}
		if canonical, ok := parsed.(reference.Canonical); ok && canonical.Name() == named.Name() {
			return canonical.String(), nil
		}
	}
	return "", errors.New("the backup recorded no digest for this image")
}

// pinBackupImage makes image resolve to the backup-time digest. A copy already
// on this server is used as is, so an offline restore still works; otherwise the
// digest is pulled. The tag is then pointed at the digest, because Unraid's Docker
// page reads the tag from the container config, and re-read to prove it landed.
func pinBackupImage(ctx context.Context, cli dockerClient, image, digestRef string) error {
	pinned, err := cli.ImageInspect(ctx, digestRef)
	if err != nil {
		resp, pullErr := cli.ImagePull(ctx, digestRef, client.ImagePullOptions{})
		if pullErr != nil {
			return fmt.Errorf("pulling %s: %w", digestRef, pullErr)
		}
		waitErr := resp.Wait(ctx)
		_ = resp.Close()
		if waitErr != nil {
			return fmt.Errorf("pulling %s: %w", digestRef, waitErr)
		}
		if pinned, err = cli.ImageInspect(ctx, digestRef); err != nil {
			return fmt.Errorf("inspecting %s after pull: %w", digestRef, err)
		}
		if !slices.Contains(pinned.RepoDigests, digestRef) && !slices.Contains(pinned.RepoDigests, familiarRef(digestRef)) {
			return fmt.Errorf("pulled image does not carry digest %s", digestRef)
		}
	}
	// A config that already names a digest is immutable and cannot be a tag
	// target, whatever form the digest was written in.
	if named, err := reference.ParseNormalizedNamed(image); err == nil {
		if _, isDigest := named.(reference.Canonical); isDigest {
			return nil
		}
	}
	priorID := tagTarget(ctx, cli, image)
	if _, err := cli.ImageTag(ctx, client.ImageTagOptions{Source: digestRef, Target: image}); err != nil {
		return fmt.Errorf("pointing %s at %s: %w", image, digestRef, err)
	}
	// From here the tag may have moved, so a failed check puts it back.
	tagged, err := cli.ImageInspect(ctx, image)
	if err != nil {
		restoreImageTag(ctx, cli, image, priorID)
		return fmt.Errorf("inspecting %s: %w", image, err)
	}
	if tagged.ID != pinned.ID {
		restoreImageTag(ctx, cli, image, priorID)
		return fmt.Errorf("%s resolves to %s, not the backup-time image %s", image, tagged.ID, pinned.ID)
	}
	return nil
}

// tagTarget returns the image ID that the tag image names now, or "" when
// image is a digest reference or the tag does not exist on this server.
func tagTarget(ctx context.Context, cli dockerClient, image string) string {
	named, err := reference.ParseNormalizedNamed(image)
	if err != nil {
		return ""
	}
	if _, isDigest := named.(reference.Canonical); isDigest {
		return ""
	}
	res, err := cli.ImageInspect(ctx, image)
	if err != nil {
		return ""
	}
	return res.ID
}

// restoreImageTag points image back at priorID, the image it named before a
// restore pinned it, so a restore that fails leaves later pulls and
// recreations of the tag where they were (#599). It runs even after the
// restore's context is cancelled. A tag that did not exist before (priorID
// "") is left on the backup-time image, which is still on this server.
func restoreImageTag(ctx context.Context, cli dockerClient, image, priorID string) {
	if priorID == "" {
		return
	}
	if _, err := cli.ImageTag(context.WithoutCancel(ctx), client.ImageTagOptions{Source: priorID, Target: image}); err != nil {
		log.Printf("engine: chunked restore: could not point %s back at %s: %v", image, priorID, err)
		return
	}
	log.Printf("engine: chunked restore: pointed %s back at %s", image, priorID)
}

// familiarRef renders a canonical reference the way Docker lists RepoDigests for
// Docker Hub images ("nginx@sha256:…" rather than "docker.io/library/nginx@…").
func familiarRef(ref string) string {
	named, err := reference.ParseNormalizedNamed(ref)
	if err != nil {
		return ref
	}
	return reference.FamiliarString(named)
}

// resolveChunkedImage decides which image a chunked restore runs and returns the
// digest reference it pinned the tag to, or "" when it did not pin. A chunked backup holds no image layers, so the tag alone
// could now name different code. Without the digest the restore stops before
// touching anything, unless the person accepted the current tag.
func (h *ContainerHandler) resolveChunkedImage(ctx context.Context, item BackupItem, image string, repoDigests []string, progress ProgressFunc) (string, error) {
	if image == "" {
		return "", nil
	}
	digestRef, err := backupImageRef(image, repoDigests)
	if err == nil {
		if progress != nil {
			progress(item.Name, 20, "pulling backup-time image "+digestRef)
		}
		err = pinBackupImage(ctx, h.cli, image, digestRef)
		if err == nil {
			return digestRef, nil
		}
	}
	if !RestoreTrustFrom(ctx).AllowUnpinnedImage {
		return "", fmt.Errorf("%w: cannot restore %s with the image it was backed up with: %w; nothing was changed. If the registry is reachable and %q is acceptable as it is today, restart the restore with \"Use the image's current tag\" enabled (API: allow_unpinned_image)", ErrRestoreNeedsApproval, item.Name, err, image)
	}
	log.Printf("engine: chunked restore: %s is using the current image for %q as approved: %v", item.Name, image, err)
	if progress != nil {
		progress(item.Name, 20, "pulling image "+image)
	}
	pullResp, pullErr := h.cli.ImagePull(ctx, image, client.ImagePullOptions{})
	if pullErr != nil {
		log.Printf("engine: chunked restore: image pull %q: %v (continuing — container may fail to start)", image, pullErr)
		return "", nil
	}
	if waitErr := pullResp.Wait(ctx); waitErr != nil {
		log.Printf("engine: chunked restore: image pull wait %q: %v", image, waitErr)
	}
	_ = pullResp.Close()
	return "", nil
}
