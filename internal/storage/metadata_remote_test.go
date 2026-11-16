package storage

import (
	"context"
	"errors"
	"fmt"
	"net"
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

// pkg/sftp can only read a whole directory, so review-time SFTP probes never
// list: they refuse before sending a request, and callers stat the objects
// the Restore Point recorded.
func TestBoundedMetadataSFTPNeverLists(t *testing.T) {
	t.Parallel()
	srv := startSFTP458Server(t)
	root := t.TempDir()
	sub := filepath.Join(root, "point")
	if err := os.MkdirAll(sub, 0o700); err != nil {
		t.Fatal(err)
	}
	for i := range 500 {
		if err := os.WriteFile(filepath.Join(sub, fmt.Sprintf("archive-%03d.tar", i)), []byte("fixture"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	probe := newMetadataAdapter(srv.adapter(t, root), Options{MetadataProbeLimit: 128, MetadataProbeContext: t.Context()})
	if list, err := probe.List("point"); !errors.Is(err, errBoundedMetadataUnavailable) || len(list) != 0 {
		t.Fatalf("SFTP list: %+v, %v", list, err)
	}
	info, err := probe.Stat("point/archive-042.tar")
	if err != nil || info.Size != 7 || info.IsDir {
		t.Fatalf("recorded object stat: %+v, %v", info, err)
	}

	// No request reaches the server: a probe of an unreachable host still
	// reports the missing listing, not a connection failure.
	closed, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := closed.Addr().(*net.TCPAddr).Port
	_ = closed.Close()
	offline, err := NewSFTPAdapter(SFTPConfig{Host: "127.0.0.1", Port: port, User: sftp458User, Password: sftp458Password, BasePath: root, HostKey: srv.fingerprint})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = offline.Close() }()
	unreachable := newMetadataAdapter(offline, Options{MetadataProbeLimit: 128, MetadataProbeContext: t.Context()})
	if _, err := unreachable.List("point"); !errors.Is(err, errBoundedMetadataUnavailable) {
		t.Fatalf("SFTP list contacted the server: %v", err)
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
