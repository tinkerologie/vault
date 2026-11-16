package storage

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"testing"
)

// boundedListContract checks the review-time listing contract shared by every
// adapter that supports it: complete listings within the limit, fail-closed
// overflow, proof of absence, and cancellation.
func boundedListContract(t *testing.T, root string, open func(context.Context) Adapter) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	a := open(ctx)
	if _, err := a.List("missing"); !IsNotExist(err) {
		t.Fatalf("missing directory: %v", err)
	}
	if _, err := a.List("../outside"); err == nil {
		t.Fatal("traversal was accepted")
	}
	sub := filepath.Join(root, "point")
	if err := os.MkdirAll(sub, 0o700); err != nil {
		t.Fatal(err)
	}
	write := func(name string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(sub, name), []byte("fixture"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("archive.tar")
	list, err := a.List("point")
	if err != nil || len(list) != 1 || path.Clean(filepath.ToSlash(list[0].Path)) != "point/archive.tar" || list[0].Size != 7 {
		t.Fatalf("one-entry listing: %+v, %v", list, err)
	}
	write("second.tar")
	write("third.tar")
	if list, err := a.List("point"); !errors.Is(err, errBoundedMetadataUnavailable) || len(list) != 0 {
		t.Fatalf("overflow returned a partial result: %+v, %v", list, err)
	}
	cancel()
	if _, err := a.List("point"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled list: %v", err)
	}
}

func TestBoundedMetadataSFTPList(t *testing.T) {
	t.Parallel()
	srv := startSFTP458Server(t)
	root := t.TempDir()
	boundedListContract(t, root, func(ctx context.Context) Adapter {
		return newMetadataAdapter(srv.adapter(t, root), Options{MetadataProbeLimit: 2, MetadataProbeContext: ctx})
	})
}

// An intact classic folder backup on SFTP is reviewed from its listing even
// when no checksums were saved; a directory over the limit, or one holding a
// link, is refused.
func TestBoundedMetadataSFTPListsClassicFolderBackup(t *testing.T) {
	t.Parallel()
	srv := startSFTP458Server(t)
	root := t.TempDir()
	item := filepath.Join(root, "folder-backup", "full", "documents")
	if err := os.MkdirAll(item, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"data.tar", "folder_meta.json"} {
		if err := os.WriteFile(filepath.Join(item, name), []byte("fixture"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	probe := newMetadataAdapter(srv.adapter(t, root), Options{MetadataProbeLimit: 128, MetadataProbeContext: t.Context()})
	list, err := probe.List("folder-backup/full/documents")
	if err != nil || len(list) != 2 {
		t.Fatalf("classic folder listing: %+v, %v", list, err)
	}
	for _, info := range list {
		if info.Size != 7 || info.IsDir || path.Dir(filepath.ToSlash(info.Path)) != "folder-backup/full/documents" {
			t.Fatalf("listed object: %+v", info)
		}
	}

	crowded := filepath.Join(root, "crowded")
	if err := os.MkdirAll(crowded, 0o700); err != nil {
		t.Fatal(err)
	}
	for i := range 129 {
		if err := os.WriteFile(filepath.Join(crowded, fmt.Sprintf("archive-%03d.tar", i)), []byte("fixture"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if list, err := probe.List("crowded"); !errors.Is(err, errBoundedMetadataUnavailable) || len(list) != 0 {
		t.Fatalf("over-limit listing: %d entries, %v", len(list), err)
	}

	if err := os.Symlink(filepath.Join(item, "data.tar"), filepath.Join(item, "linked.tar")); err != nil {
		t.Fatal(err)
	}
	if list, err := probe.List("folder-backup/full/documents"); !errors.Is(err, errBoundedMetadataUnavailable) || len(list) != 0 {
		t.Fatalf("listing with a link: %+v, %v", list, err)
	}
}

func TestBoundedMetadataSMBList(t *testing.T) {
	t.Parallel()
	a, share, _ := newFakeSMB(t, "")
	boundedListContract(t, share.root, func(ctx context.Context) Adapter {
		return newMetadataAdapter(a, Options{MetadataProbeLimit: 2, MetadataProbeContext: ctx})
	})
}

func TestBoundedMetadataSMBListStopsAfterLimit(t *testing.T) {
	t.Parallel()
	a, share, _ := newFakeSMB(t, "")
	sub := filepath.Join(share.root, "point")
	if err := os.MkdirAll(sub, 0o700); err != nil {
		t.Fatal(err)
	}
	for i := range 50 {
		if err := os.WriteFile(filepath.Join(sub, fmt.Sprintf("archive-%02d.tar", i)), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	const limit = 2
	probe := newMetadataAdapter(a, Options{MetadataProbeLimit: limit, MetadataProbeContext: t.Context()})
	if list, err := probe.List("point"); !errors.Is(err, errBoundedMetadataUnavailable) || len(list) != 0 {
		t.Fatalf("overflow: %+v, %v", list, err)
	}
	if share.entriesRead > limit+1 {
		t.Fatalf("read %d entries of an over-limit directory, want at most %d", share.entriesRead, limit+1)
	}
}
