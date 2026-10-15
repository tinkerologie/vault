package engine

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	imagetypes "github.com/moby/moby/api/types/image"
	"github.com/moby/moby/client"
)

const sha458 = "sha256:1111111111111111111111111111111111111111111111111111111111111111"

// withUpdateStatusPath458 points the Unraid update-status cache at a file in
// a temp dir for the duration of the test. It mutates a package variable, so
// callers must not run in parallel.
func withUpdateStatusPath458(t *testing.T, content *string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "unraid-update-status.json")
	if content != nil {
		writeFile458(t, path, *content)
	}
	orig := unraidUpdateStatusPath
	unraidUpdateStatusPath = path
	t.Cleanup(func() { unraidUpdateStatusPath = orig })
	return path
}

func readStatus458(t *testing.T, path string) map[string]map[string]any {
	t.Helper()
	out := map[string]map[string]any{}
	if err := json.Unmarshal([]byte(readFile458(t, path)), &out); err != nil {
		t.Fatalf("status file is not valid JSON: %v", err)
	}
	return out
}

func strPtr458(s string) *string { return &s }

func TestSeedUnraidUpdateStatus458(t *testing.T) {
	tests := []struct {
		name       string
		initial    *string
		image      string
		wantKey    string
		wantStatus string
		unchanged  bool
	}{
		{name: "empty cache gains an entry", initial: strPtr458(""), image: "nginx", wantKey: "library/nginx:latest", wantStatus: "undef"},
		{name: "remote matches local", initial: strPtr458(`{"library/nginx:latest":{"remote":"` + sha458 + `"}}`), image: "nginx:latest", wantKey: "library/nginx:latest", wantStatus: "true"},
		{name: "remote differs", initial: strPtr458(`{"linuxserver/plex:latest":{"remote":"sha256:2222"}}`), image: "linuxserver/plex", wantKey: "linuxserver/plex:latest", wantStatus: "false"},
		{name: "corrupt cache left alone", initial: strPtr458("{oops"), image: "nginx", unchanged: true},
		{name: "not on unraid", initial: nil, image: "nginx", unchanged: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := withUpdateStatusPath458(t, tt.initial)
			seedUnraidUpdateStatus(tt.image, sha458)
			if tt.unchanged {
				got, err := os.ReadFile(path) // #nosec G304 -- test path
				switch {
				case tt.initial == nil && err == nil:
					t.Fatal("status file was created on a non-Unraid host")
				case tt.initial != nil && string(got) != *tt.initial:
					t.Fatalf("status file changed: %q", got)
				}
				return
			}
			entry := readStatus458(t, path)[tt.wantKey]
			if entry == nil {
				t.Fatalf("no entry for %s", tt.wantKey)
			}
			if entry["local"] != sha458 || entry["status"] != tt.wantStatus {
				t.Errorf("entry = %v, want local=%s status=%s", entry, sha458, tt.wantStatus)
			}
		})
	}
}

func TestSeedUnraidUpdateStatusIOFailures458(t *testing.T) {
	t.Run("cache path unreadable", func(t *testing.T) {
		path := withUpdateStatusPath458(t, nil)
		if err := os.Mkdir(path, 0o750); err != nil {
			t.Fatal(err)
		}
		seedUnraidUpdateStatus("nginx", sha458) // logs and returns
	})
	t.Run("temp file cannot be written", func(t *testing.T) {
		path := withUpdateStatusPath458(t, strPtr458("{}"))
		if err := os.Mkdir(path+".tmp", 0o750); err != nil {
			t.Fatal(err)
		}
		seedUnraidUpdateStatus("nginx", sha458)
		if got := readFile458(t, path); got != "{}" {
			t.Errorf("status file changed despite the failed write: %q", got)
		}
	})
}

func TestRestoreUnraidUpdateStatusSeedsCache458(t *testing.T) {
	path := withUpdateStatusPath458(t, strPtr458("{}"))
	src := t.TempDir()
	meta, _ := json.Marshal(imageMeta{ImageTag: "nginx", RepoDigests: []string{"nginx@" + sha458}})
	writeFile458(t, filepath.Join(src, "image_meta.json"), string(meta))
	restoreUnraidUpdateStatus(src, "nginx")
	if readStatus458(t, path)["library/nginx:latest"]["local"] != sha458 {
		t.Error("digest from image_meta.json was not seeded")
	}

	// image_meta.json that cannot be read (a directory) is logged and ignored.
	bad := t.TempDir()
	if err := os.Mkdir(filepath.Join(bad, "image_meta.json"), 0o750); err != nil {
		t.Fatal(err)
	}
	restoreUnraidUpdateStatus(bad, "nginx")
}

func TestPopulateRepoDigestsViaPull458(t *testing.T) {
	tests := []struct {
		name       string
		ref        string
		waitErr    error
		inspectErr error
		digests    []string
		want       bool
		wantPulled string
		wantSeeded bool
	}{
		{name: "digest refreshed and seeded", ref: "nginx:latest@sha256:abcd", digests: []string{"nginx@" + sha458}, want: true, wantPulled: "nginx:latest", wantSeeded: true},
		{name: "digest without sha", ref: "nginx:latest", digests: []string{"nginx"}, want: true, wantPulled: "nginx:latest"},
		{name: "wait fails", ref: "nginx", waitErr: errFake458, wantPulled: "nginx"},
		{name: "post-pull inspect fails", ref: "nginx", inspectErr: errFake458, wantPulled: "nginx"},
		{name: "no digests after pull", ref: "nginx", wantPulled: "nginx"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := withUpdateStatusPath458(t, strPtr458("{}"))
			var pulled string
			f := &fakeDocker458{
				imagePull: func(_ context.Context, ref string) (client.ImagePullResponse, error) {
					pulled = ref
					return &fakePullResponse{Reader: strings.NewReader(""), waitErr: tt.waitErr}, nil
				},
				imageInspect: func(context.Context, string) (client.ImageInspectResult, error) {
					if tt.inspectErr != nil {
						return client.ImageInspectResult{}, tt.inspectErr
					}
					return client.ImageInspectResult{InspectResponse: imagetypes.InspectResponse{RepoDigests: tt.digests}}, nil
				},
			}
			if got := populateRepoDigestsViaPull(t.Context(), f, tt.ref); got != tt.want {
				t.Errorf("populateRepoDigestsViaPull = %v, want %v", got, tt.want)
			}
			if pulled != tt.wantPulled {
				t.Errorf("pulled %q, want %q", pulled, tt.wantPulled)
			}
			_, seeded := readStatus458(t, path)["library/nginx:latest"]
			if seeded != tt.wantSeeded {
				t.Errorf("seeded = %v, want %v", seeded, tt.wantSeeded)
			}
		})
	}
	if populateRepoDigestsViaPull(t.Context(), nil, "nginx") {
		t.Error("nil client must report false")
	}
}

func TestBuildImageMetaInspectError458(t *testing.T) {
	t.Parallel()
	f := &fakeDocker458{imageInspect: func(context.Context, string) (client.ImageInspectResult, error) {
		return client.ImageInspectResult{}, errFake458
	}}
	meta := buildImageMeta(t.Context(), f, "nginx:latest")
	if meta.ImageTag != "nginx:latest" || len(meta.RepoDigests) != 0 {
		t.Errorf("meta = %+v", meta)
	}
}
