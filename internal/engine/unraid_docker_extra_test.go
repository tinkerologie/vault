package engine

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	imagetypes "github.com/moby/moby/api/types/image"
	"github.com/moby/moby/client"
)

// buildImageMeta short-circuits to a bare ImageTag-only result when either
// the docker client is nil or the imageRef is empty. Both guards prevent
// SDK calls on dev hosts where the daemon may be unreachable.
func TestBuildImageMetaShortCircuits(t *testing.T) {
	t.Parallel()

	t.Run("nil client returns tag-only meta", func(t *testing.T) {
		t.Parallel()
		meta := buildImageMeta(context.Background(), nil, "nicolargo/glances:latest")
		if meta.ImageTag != "nicolargo/glances:latest" {
			t.Fatalf("ImageTag = %q, want %q", meta.ImageTag, "nicolargo/glances:latest")
		}
		if len(meta.RepoDigests) != 0 {
			t.Fatalf("RepoDigests should be empty when client is nil, got %v", meta.RepoDigests)
		}
	})

	t.Run("empty imageRef returns empty meta", func(t *testing.T) {
		t.Parallel()
		meta := buildImageMeta(context.Background(), nil, "")
		if meta.ImageTag != "" {
			t.Fatalf("ImageTag = %q, want empty", meta.ImageTag)
		}
		if len(meta.RepoDigests) != 0 {
			t.Fatalf("RepoDigests should be empty, got %v", meta.RepoDigests)
		}
	})
}

// restoreUnraidUpdateStatus must silently no-op on the empty-input guard
// (both sourceDir and imageTag must be present) and on a corrupt
// image_meta.json file. The function is best-effort — the only contract is
// "do not panic, do not create the status file".
func TestRestoreUnraidUpdateStatusEmptyArgsAndCorrupt(t *testing.T) {
	t.Parallel()

	t.Run("empty source dir is a no-op", func(t *testing.T) {
		t.Parallel()
		restoreUnraidUpdateStatus("", "nicolargo/glances:latest")
	})

	t.Run("empty image tag is a no-op", func(t *testing.T) {
		t.Parallel()
		restoreUnraidUpdateStatus(t.TempDir(), "")
	})

	t.Run("corrupt image_meta.json is a no-op", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "image_meta.json"), []byte("not json"), 0o600); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
		// The function logs and returns; we just need to ensure it doesn't
		// crash or write anywhere we'd notice.
		restoreUnraidUpdateStatus(dir, "nicolargo/glances:latest")
	})

	t.Run("image_meta.json with empty digest in slice is a no-op", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		// Digest field present but lacks the `@sha256:` substring, so
		// extractSHA returns "" and the function returns before writing.
		body := `{"image_tag":"nicolargo/glances:latest","repo_digests":["nicolargo/glances:latest"]}`
		if err := os.WriteFile(filepath.Join(dir, "image_meta.json"), []byte(body), 0o600); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
		restoreUnraidUpdateStatus(dir, "nicolargo/glances:latest")
	})
}

// buildImageMeta on the success path appends the inspect.RepoDigests
// onto the meta. We use the existing mockDockerClient whose ImageInspect
// returns its imageResp field — supplying RepoDigests there exercises the
// "merge digests into meta" branch.
func TestBuildImageMetaMergesRepoDigests(t *testing.T) {
	t.Parallel()

	mock := &mockDockerClient{
		imageResp: client.ImageInspectResult{
			InspectResponse: imagetypes.InspectResponse{
				RepoDigests: []string{"redis@sha256:abc123"},
			},
		},
	}
	meta := buildImageMeta(context.Background(), mock, "redis:7")
	if meta.ImageTag != "redis:7" {
		t.Fatalf("ImageTag = %q, want redis:7", meta.ImageTag)
	}
	if len(meta.RepoDigests) != 1 || meta.RepoDigests[0] != "redis@sha256:abc123" {
		t.Fatalf("RepoDigests = %v, want [redis@sha256:abc123]", meta.RepoDigests)
	}
}

// populateRepoDigestsViaPull surfaces ImagePull errors as "false" so the
// caller can fall back to image_meta.json seeding. We use the existing
// mockDockerClient (whose ImagePull stub returns an error) to drive that
// path, including the digest-stripping branch for image@sha256:... refs.
func TestPopulateRepoDigestsViaPullReturnsFalseOnPullError(t *testing.T) {
	t.Parallel()

	mock := &mockDockerClient{}
	// Plain tag — exercises the pull-error log branch.
	if populateRepoDigestsViaPull(context.Background(), mock, "redis:7") {
		t.Fatal("expected false when ImagePull fails")
	}
	// Digest-pinned ref — exercises the @-stripping branch before pull.
	if populateRepoDigestsViaPull(context.Background(), mock, "redis@sha256:abc123") {
		t.Fatal("expected false when ImagePull fails with digest-pinned ref")
	}
}

// seedUnraidUpdateStatus has two early-return guards: empty tag and empty
// sha. Both must short-circuit before the file read. On a non-Unraid host
// the missing /var/lib/docker/unraid-update-status.json path is the third
// guard — the function logs the not-exist case at info level and returns.
func TestSeedUnraidUpdateStatusGuards(t *testing.T) {
	t.Parallel()

	// All three guards: no-op behaviour. The contract is "don't panic".
	seedUnraidUpdateStatus("", "sha256:abc")
	seedUnraidUpdateStatus("nicolargo/glances:latest", "")
	// Non-Unraid host: status file does not exist, function logs and returns.
	seedUnraidUpdateStatus("nicolargo/glances:latest", "sha256:abc123def4567890abc123def4567890abc123def4567890abc123def456789a")
}
