package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/moby/moby/client"
)

// (helpers in this file take the narrow dockerClient interface — defined
// in docker_client.go — rather than the concrete *client.Client, so the
// same code paths can be exercised by mocks in container_test.go.)

// unraidUpdateStatusPath is the Unraid Docker Manager's update status cache.
// It maps `<image>:<tag>` to {local, remote, status}. When `local` is null
// (which is what `docker load` produces for restored images, since RepoDigests
// is empty), the Unraid UI shows "not available" and "Check for Updates"
// cannot resolve the local digest.
//
// A var, not a const, only so tests can point it at a temporary file instead
// of the real Unraid path; production never reassigns it. Mirrors
// dockerTemplatesDir in container.go.
var unraidUpdateStatusPath = "/var/lib/docker/unraid-update-status.json"

// unraidUpdateStatusMu serialises Vault's read-modify-write of the
// update-status file.
var unraidUpdateStatusMu sync.Mutex

// imageMeta is the on-disk shape of `image_meta.json` placed alongside
// `image.tar` in each container backup. It captures the image's
// RepoDigests so restore can re-seed Unraid's update-status cache.
type imageMeta struct {
	ImageTag    string   `json:"image_tag"`
	RepoDigests []string `json:"repo_digests"`
}

// buildImageMeta inspects the source image and returns the metadata that
// restore needs to repopulate Unraid's docker update-status.json. Errors
// are logged but otherwise swallowed — image meta is best-effort and a
// missing file just means restore falls back to current (pre-fix) behaviour.
func buildImageMeta(ctx context.Context, cli dockerClient, imageRef string) imageMeta {
	meta := imageMeta{ImageTag: imageRef}
	if cli == nil || imageRef == "" {
		return meta
	}
	inspect, err := cli.ImageInspect(ctx, imageRef)
	if err != nil {
		log.Printf("engine: image_meta: inspecting %q: %v", imageRef, err)
		return meta
	}
	meta.RepoDigests = append(meta.RepoDigests, inspect.RepoDigests...)
	return meta
}

// ensureUnraidImageTag normalises a Docker image reference the same way
// Unraid's `DockerUtil::ensureImageTag` does. Without this we'd write keys
// like `influxdb` while Unraid stores `library/influxdb:latest`.
func ensureUnraidImageTag(image string) string {
	if image == "" {
		return image
	}
	// Strip any digest suffix — Unraid keys by tag, not digest.
	image, _, _ = strings.Cut(image, "@")
	// Add `:latest` if no tag is present in the final path component.
	lastComponent := image
	if _, after, found := strings.CutLast(image, "/"); found {
		lastComponent = after
	}
	if !strings.Contains(lastComponent, ":") {
		image += ":latest"
	}
	// Add `library/` prefix for docker official single-name images.
	if !strings.Contains(image, "/") {
		image = "library/" + image
	}
	return image
}

// extractSHA returns the `sha256:…` portion of a RepoDigest reference such as
// `nicolargo/glances@sha256:abc…`, or the empty string if none is found.
func extractSHA(repoDigest string) string {
	if at := strings.Index(repoDigest, "@sha256:"); at >= 0 {
		return repoDigest[at+1:]
	}
	return ""
}

// restoreUnraidUpdateStatus re-seeds Unraid's docker update-status.json with
// the local digest captured at backup time. The Unraid UI then shows
// "up-to-date" or "update available" instead of "not available", and the
// "Check for Updates" button can complete its compare-with-remote step.
//
// This function is best-effort: any missing file, parse error, or write
// failure is logged and swallowed so a non-Unraid host (where the file does
// not exist) is unaffected.
func restoreUnraidUpdateStatus(sourceDir, imageTag string) {
	if sourceDir == "" || imageTag == "" {
		return
	}
	metaPath := filepath.Join(sourceDir, "image_meta.json")
	metaBytes, err := os.ReadFile(metaPath) // #nosec G304 — sourceDir is vault-controlled temp directory
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			log.Printf("engine: update-status: reading %s: %v", metaPath, err)
		}
		return
	}
	var meta imageMeta
	if err := json.Unmarshal(metaBytes, &meta); err != nil {
		log.Printf("engine: update-status: parsing image_meta.json: %v", err)
		return
	}
	if len(meta.RepoDigests) == 0 {
		// Backup didn't capture digests (older backup or image was
		// loaded without ever being pulled). Nothing we can do.
		return
	}
	sha := extractSHA(meta.RepoDigests[0])
	if sha == "" {
		return
	}
	seedUnraidUpdateStatus(imageTag, sha)
}

// seedUnraidUpdateStatus writes the given local digest into Unraid's
// docker update-status.json for the given image tag. It is the shared
// write path used by both backup-time metadata seeding and post-pull
// fresh-digest seeding.
func seedUnraidUpdateStatus(imageTag, sha string) {
	if imageTag == "" || sha == "" {
		return
	}
	writeUnraidLocalDigest(imageTag, sha)
}

// forgetUnraidLocalDigest clears the recorded local digest for imageTag when
// Vault cannot tell which digest the restored image carries. Unraid keeps a
// recorded digest even through "Check for Updates", and only inspects the
// image when none is recorded, so a stale one would stick (#599).
func forgetUnraidLocalDigest(imageTag string) {
	if imageTag == "" {
		return
	}
	writeUnraidLocalDigest(imageTag, "")
}

// seedRestoredImageStatus records in Unraid's update-status cache the digest
// of the image the restored container was created from. It reads the image ID
// from the container itself, by the ID ContainerCreate returned, not from the
// tag, which another pull can move after the container is created, nor by
// name, which a replacement container could take. So the badge never
// describes an image the restored container is not running (#599). When no
// digest for the image's repository is known, the recorded one is cleared so
// Unraid inspects the image itself.
func seedRestoredImageStatus(ctx context.Context, cli dockerClient, image, containerID string) {
	if image == "" {
		return
	}
	var ref string
	if cli != nil && containerID != "" {
		if c, err := cli.ContainerInspect(ctx, containerID, client.ContainerInspectOptions{}); err == nil && c.Container.Image != "" {
			if res, err := cli.ImageInspect(ctx, c.Container.Image); err == nil {
				ref, _ = backupImageRef(image, res.RepoDigests)
			}
		}
	}
	if sha := extractSHA(ref); sha != "" {
		seedUnraidUpdateStatus(image, sha)
		return
	}
	forgetUnraidLocalDigest(image)
}

// writeUnraidLocalDigest sets the local digest for imageTag, or clears it when
// sha is empty, and re-derives the status. Clearing never creates an entry.
func writeUnraidLocalDigest(imageTag, sha string) {
	// Restores can finish side by side (an untracked MCP restore alongside a
	// tracked one), so the read-modify-write of the shared file is serialised.
	unraidUpdateStatusMu.Lock()
	defer unraidUpdateStatusMu.Unlock()
	// Only touch the status file if it already exists — i.e. we're on
	// Unraid with the dockerMan plugin active.
	statusBytes, err := os.ReadFile(unraidUpdateStatusPath) // #nosec G304 — fixed Unraid path
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			log.Printf("engine: update-status: reading %s: %v", unraidUpdateStatusPath, err)
		}
		return
	}

	status := map[string]map[string]any{}
	if len(statusBytes) > 0 {
		if err := json.Unmarshal(statusBytes, &status); err != nil {
			log.Printf("engine: update-status: parsing %s: %v", unraidUpdateStatusPath, err)
			return
		}
	}

	key := ensureUnraidImageTag(imageTag)
	entry, ok := status[key]
	if !ok {
		if sha == "" {
			return
		}
		entry = map[string]any{}
	}
	if sha == "" {
		entry["local"] = nil
	} else {
		entry["local"] = sha
	}
	if _, hasRemote := entry["remote"]; !hasRemote {
		entry["remote"] = nil
	}
	// Re-derive status from local/remote so the UI badge updates immediately
	// when both are present, matching Unraid's own reloadUpdateStatus() logic.
	remote, _ := entry["remote"].(string)
	switch {
	case remote == "" || sha == "":
		entry["status"] = "undef"
	case remote == sha:
		entry["status"] = "true"
	default:
		entry["status"] = "false"
	}
	status[key] = entry

	out, err := json.MarshalIndent(status, "", "    ")
	if err != nil {
		log.Printf("engine: update-status: marshalling: %v", err)
		return
	}
	if err := replaceFileAtomically(unraidUpdateStatusPath, out); err != nil {
		log.Printf("engine: update-status: writing %s: %v", unraidUpdateStatusPath, err)
		return
	}
	if sha == "" {
		log.Printf("engine: update-status: cleared local digest for %s", key)
		return
	}
	log.Printf("engine: update-status: seeded local digest for %s (%s)", key, shortSHA(sha))
}

// shortSHA returns the first 12 hex chars of a `sha256:…` digest for log lines.
func shortSHA(sha string) string {
	const prefix = "sha256:"
	s := strings.TrimPrefix(sha, prefix)
	if len(s) > 12 {
		s = s[:12]
	}
	return fmt.Sprintf("%s%s", prefix, s)
}

// populateRepoDigestsViaPull asks the Docker daemon to pull the given image
// after a restore. Because `docker load` does not preserve RepoDigests
// (only `docker pull` does), this is what makes the Unraid Docker Manager
// see a local digest for the restored image, which in turn unblocks the
// "Check for Updates" button and the "up-to-date" badge.
//
// The pull is non-destructive: if the layers already exist locally (which
// they will, since ImageLoad just placed them there), no data is transferred
// — Docker just records the manifest digest against the local image, which
// is exactly the metadata we need. If the registry has a newer manifest,
// only the differing layers are fetched. If the daemon is offline, the call
// fails and we fall back to image_meta.json seeding via
// restoreUnraidUpdateStatus.
//
// All errors are logged and swallowed — this is best-effort.
func populateRepoDigestsViaPull(ctx context.Context, cli dockerClient, imageRef string) bool {
	if cli == nil || imageRef == "" {
		return false
	}
	// Strip any digest suffix; `docker pull` of `image@sha256:…` would
	// pin a specific manifest and might not populate RepoDigests for the
	// tag-based reference Unraid stores in its update-status cache.
	ref := imageRef
	if at := strings.Index(ref, "@"); at >= 0 {
		ref = ref[:at]
	}
	resp, err := cli.ImagePull(ctx, ref, client.ImagePullOptions{})
	if err != nil {
		log.Printf("engine: image_pull: %q: %v (falling back to image_meta seeding)", ref, err)
		return false
	}
	defer func() { _ = resp.Close() }()
	// Wait for the pull to finish; this drains the JSON message stream.
	if err := resp.Wait(ctx); err != nil {
		log.Printf("engine: image_pull: waiting for %q: %v", ref, err)
		return false
	}
	// Confirm the pull actually populated RepoDigests; if not, the caller
	// can still seed from image_meta.json as a fallback.
	inspect, err := cli.ImageInspect(ctx, ref)
	if err != nil {
		log.Printf("engine: image_pull: post-pull inspect %q: %v", ref, err)
		return false
	}
	if len(inspect.RepoDigests) == 0 {
		return false
	}
	log.Printf("engine: image_pull: refreshed RepoDigests for %s", ref)
	// Seed Unraid's update-status cache with the freshly-pulled digest so
	// the UI badge flips from "not available" immediately, without waiting
	// for the user to click "Check for Updates".
	if sha := extractSHA(inspect.RepoDigests[0]); sha != "" {
		seedUnraidUpdateStatus(ref, sha)
	}
	return true
}

// createTempFile is os.CreateTemp; a var so tests can make it fail.
var createTempFile = os.CreateTemp

// replaceFileAtomically writes data to a uniquely named temporary file next to
// path and renames it over path, so concurrent writers never share a temporary
// file and readers see either the old or the new contents. The file keeps the
// 0644 mode Unraid gives it.
func replaceFileAtomically(path string, data []byte) error {
	tmp, err := createTempFile(filepath.Dir(path), filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }() // no-op once renamed
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpName, 0o644); err != nil { // #nosec G302 — match Unraid's existing perms (0644)
		return err
	}
	return os.Rename(tmpName, path)
}
