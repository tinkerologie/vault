package storage

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
)

func TestBoundedMetadataLocal(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	a, err := NewAdapterWithOptions("local", fmt.Sprintf(`{"path":%q}`, root), Options{MetadataProbeLimit: 2})
	if err != nil {
		t.Fatal(err)
	}
	defer CloseAdapter(a)
	if _, err := a.List("missing"); !IsNotExist(err) {
		t.Fatalf("missing directory: %v", err)
	}
	if _, err := a.List("../outside"); err == nil {
		t.Fatal("traversal was accepted")
	}
	sub := filepath.Join(root, "point")
	if err := os.Mkdir(sub, 0o700); err != nil {
		t.Fatal(err)
	}
	write := func(name string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(sub, name), []byte("fixture"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("archive.tar")
	if _, err := a.List("point/archive.tar"); !errors.Is(err, errBoundedMetadataUnavailable) {
		t.Fatalf("regular file treated as directory: %v", err)
	}
	list, err := a.List("point")
	if err != nil || len(list) != 1 || filepath.ToSlash(list[0].Path) != "point/archive.tar" || list[0].Size != 7 {
		t.Fatalf("one-entry listing: %+v, %v", list, err)
	}
	t.Run("links are not complete artifact listings", func(t *testing.T) {
		link := filepath.Join(sub, "link.tar")
		if err := os.Symlink("archive.tar", link); err != nil {
			t.Skipf("test platform cannot create symlinks: %v", err)
		}
		t.Cleanup(func() { _ = os.Remove(link) })
		if list, err := a.List("point"); !errors.Is(err, errBoundedMetadataUnavailable) || len(list) != 0 {
			t.Fatalf("listing treated link as a backup file: %+v, %v", list, err)
		}
	})
	write("second.tar")
	write("third.tar")
	if list, err := a.List("point"); !errors.Is(err, errBoundedMetadataUnavailable) || len(list) != 0 {
		t.Fatalf("overflow returned a partial result: %+v, %v", list, err)
	}
}

func TestBoundedMetadataWebDAVDoesNotReadSplitFile(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	for p, contents := range map[string]string{
		"archive.tar": "regular",
		filepath.Base(webDAVManifestPath("/split.tar")): "sidecar content must not be read",
	} {
		if err := os.WriteFile(filepath.Join(root, p), []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	var props, other atomic.Int64
	handler := testWebDAVHandler(root)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "PROPFIND" || r.Header.Get("Depth") != "0" {
			other.Add(1)
			http.Error(w, "only single object metadata permitted", http.StatusForbidden)
			return
		}
		props.Add(1)
		if r.URL.Path == "/unavailable.tar" {
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
			return
		}
		handler.ServeHTTP(w, r)
	}))
	defer server.Close()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	a, err := NewAdapterWithOptions("webdav", fmt.Sprintf(`{"url":%q}`, server.URL), Options{
		MetadataProbeLimit: 2, MetadataProbeContext: ctx,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer CloseAdapter(a)
	if info, err := a.Stat("archive.tar"); err != nil || info.Size != 7 {
		t.Fatalf("ordinary archive: %+v, %v", info, err)
	}
	if _, err := a.Stat("split.tar"); !errors.Is(err, errBoundedMetadataUnavailable) {
		t.Fatalf("split archive should have unknown logical metadata: %v", err)
	}
	if _, err := a.Stat("missing.tar"); !IsNotExist(err) {
		t.Fatalf("archive and split sidecar both missing: %v", err)
	}
	if _, err := a.Stat("../outside"); err == nil {
		t.Fatal("WebDAV traversal was accepted")
	}
	if _, err := a.Stat("unavailable.tar"); err == nil || IsNotExist(err) {
		t.Fatalf("server failure treated as proof of absence: %v", err)
	}
	if _, err := a.List("point"); !errors.Is(err, errBoundedMetadataUnavailable) {
		t.Fatalf("WebDAV listing should fail closed: %v", err)
	}
	requestCount := props.Load()
	cancel()
	if _, err := a.Stat("archive.tar"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled stat: %v", err)
	}
	if _, err := a.List("point"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled list: %v", err)
	}
	// Some DAV servers need a second metadata lookup after a 404.
	if got := props.Load(); got != requestCount || got < 6 || got > 12 || other.Load() != 0 {
		t.Fatalf("requests: single-object PROPFIND=%d, other=%d", got, other.Load())
	}
}

func TestBoundedMetadataNFSUsesReadOnlyMount(t *testing.T) {
	originalMount, originalUnmount := mountExport, unmountExport
	t.Cleanup(func() { mountExport, unmountExport = originalMount, originalUnmount })
	var mounted, unmounted string
	mountExport = func(args ...string) ([]byte, error) {
		index := slices.Index(args, "-o")
		if index < 0 || index+1 >= len(args) || !strings.HasSuffix(args[index+1], ",ro") {
			return nil, fmt.Errorf("metadata mount did not request final read-only mode: %v", args)
		}
		mounted = args[len(args)-1]
		dir := filepath.Join(mounted, "point")
		if err := os.Mkdir(dir, 0o700); err != nil {
			return nil, err
		}
		return nil, os.WriteFile(filepath.Join(dir, "archive.tar"), []byte("fixture"), 0o600)
	}
	unmountExport = func(dir string) error {
		unmounted = dir
		if err := os.Remove(filepath.Join(dir, "point", "archive.tar")); err != nil {
			return err
		}
		return os.Remove(filepath.Join(dir, "point"))
	}
	a, err := NewAdapterWithOptions("nfs", `{"host":"fixture","export":"/exports","options":"rw,soft"}`, Options{MetadataProbeLimit: 2})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { CloseAdapter(a) })
	if list, err := a.List("point"); err != nil || len(list) != 1 || list[0].Size != 7 {
		t.Fatalf("NFS metadata: %+v, %v", list, err)
	}
	CloseAdapter(a)
	if mounted == "" || mounted != unmounted {
		t.Fatalf("metadata mount was not closed: mounted %q, unmounted %q", mounted, unmounted)
	}
}
