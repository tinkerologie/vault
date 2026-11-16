//go:build linux

package engine

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	libvirt "github.com/digitalocean/go-libvirt"
)

var errVMCovLibvirt = errors.New("libvirt connection lost")

type vmcovReviewFixture struct {
	dir, disk, nvram string
	doc, meta        []byte
	uuid             libvirt.UUID
}

func newVMCovReviewFixture(t *testing.T) vmcovReviewFixture {
	t.Helper()
	dir := t.TempDir()
	f := vmcovReviewFixture{dir: dir, disk: filepath.Join(dir, "disk.img"), nvram: filepath.Join(dir, "vars.fd")}
	f.doc = []byte(fmt.Sprintf(`<domain><name>fixture</name><uuid>%s</uuid><os><nvram>%s</nvram></os><devices><disk type="file" device="disk"><driver type="raw"/><source file=%q/><target dev="vda"/></disk></devices></domain>`, vmcovUUID, f.nvram, f.disk))
	f.meta = []byte(vmcovMeta)
	raw, err := vmUUIDBytes(vmcovUUID)
	if err != nil {
		t.Fatal(err)
	}
	f.uuid = libvirt.UUID(raw)
	return f
}

func (f vmcovReviewFixture) review(ctx context.Context, conn *fakeLibvirt, key []byte) (VMRestoreEffects, string, error) {
	return (&VMHandler{conn: conn}).ReviewVMRestore(ctx, VMRestoreReviewInput{
		Name: "fixture", XML: f.doc, Metadata: f.meta, Artifacts: []string{"vdisk0.img", "vars.fd"}, Key: key,
	})
}

func vmcovNoDomain(string) (libvirt.Domain, error) { return libvirt.Domain{}, noDomainErr }

func TestVMCovReviewVMRestoreReportsExistingDomainEffects(t *testing.T) {
	f := newVMCovReviewFixture(t)
	writeTestFile(t, f.disk, "live disk")
	previousNVRAM := filepath.Join(f.dir, "old-vars.fd")
	previous := fmt.Sprintf(`<domain><name>fixture</name><os><nvram><source file=%q/></nvram></os></domain>`, previousNVRAM)
	conn := &fakeLibvirt{
		DomainLookupByNameFn: func(name string) (libvirt.Domain, error) {
			return libvirt.Domain{Name: name, UUID: f.uuid, ID: 3}, nil
		},
		DomainHasManagedSaveImageFn: func(libvirt.Domain) (int32, error) { return 1, nil },
		DomainGetXMLDescFn: func(dom libvirt.Domain, _ libvirt.DomainXMLFlags) (string, error) {
			if dom.Name == "fixture" {
				return previous, nil
			}
			return `<domain><name>other</name><devices><disk><source file="relative.img"/></disk><disk><source file="/mnt/user/elsewhere.img"/></disk></devices></domain>`, nil
		},
		ConnectListAllDomainsFn: func() ([]libvirt.Domain, uint32, error) {
			return []libvirt.Domain{{Name: "fixture", UUID: f.uuid, ID: 3}, {Name: "other", ID: -1}}, 2, nil
		},
	}
	key := []byte("fixture review key")
	effects, digest, err := f.review(t.Context(), conn, key)
	if err != nil || digest == "" {
		t.Fatalf("review = %v, digest %q", err, digest)
	}
	domain := effects.Domain
	if !domain.Replace || !domain.Stop || !domain.RemoveManagedSave || domain.State != "running" ||
		len(domain.MayRemoveNVRAM) != 1 || domain.MayRemoveNVRAM[0] != previousNVRAM {
		t.Fatalf("domain effects = %+v", domain)
	}
	if !effects.Disks[0].Replace || effects.NVRAM == nil || effects.NVRAM.Replace || effects.NVRAM.Destination != f.nvram {
		t.Fatalf("target effects = %+v, nvram %+v", effects.Disks, effects.NVRAM)
	}
	// The same live state produces the same expectation; a changed key does not.
	if _, again, err := f.review(t.Context(), conn, key); err != nil || again != digest {
		t.Fatalf("repeat review = %q, %v; want %q", again, err, digest)
	}
	if _, other, err := f.review(t.Context(), conn, []byte("another key")); err != nil || other == digest {
		t.Fatalf("review with another key = %q, %v", other, err)
	}
}

func TestVMCovReviewVMRestoreRefusesUncheckedHostState(t *testing.T) {
	f := newVMCovReviewFixture(t)
	existing := func(name string) (libvirt.Domain, error) {
		return libvirt.Domain{Name: name, UUID: f.uuid, ID: 1}, nil
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	for _, tt := range []struct {
		name string
		ctx  context.Context
		key  []byte
		doc  []byte
		conn *fakeLibvirt
		want error
	}{
		{name: "no review key", conn: &fakeLibvirt{}, want: ErrVMRestoreHostUnavailable},
		{name: "cancelled", ctx: cancelled, conn: &fakeLibvirt{}, want: ErrVMRestoreHostUnavailable},
		{name: "unreadable saved XML", doc: []byte("<domain>"), conn: &fakeLibvirt{}, want: ErrVMBackupEvidenceUnknown},
		{name: "lookup failure", conn: &fakeLibvirt{DomainLookupByNameFn: func(string) (libvirt.Domain, error) {
			return libvirt.Domain{}, errVMCovLibvirt
		}}, want: ErrVMRestoreHostUnavailable},
		{name: "state failure", conn: &fakeLibvirt{DomainLookupByNameFn: existing, DomainGetStateFn: func(libvirt.Domain) (int32, int32, error) {
			return 0, 0, errVMCovLibvirt
		}}, want: ErrVMRestoreHostUnavailable},
		{name: "managed save failure", conn: &fakeLibvirt{DomainLookupByNameFn: existing, DomainHasManagedSaveImageFn: func(libvirt.Domain) (int32, error) {
			return 0, errVMCovLibvirt
		}}, want: ErrVMRestoreHostUnavailable},
		{name: "current XML failure", conn: &fakeLibvirt{DomainLookupByNameFn: existing, DomainGetXMLDescFn: func(libvirt.Domain, libvirt.DomainXMLFlags) (string, error) {
			return "", errVMCovLibvirt
		}}, want: ErrVMRestoreHostUnavailable},
		{name: "inactive XML failure", conn: &fakeLibvirt{DomainLookupByNameFn: existing, DomainGetXMLDescFn: func(_ libvirt.Domain, flags libvirt.DomainXMLFlags) (string, error) {
			if flags == libvirt.DomainXMLInactive {
				return "", errVMCovLibvirt
			}
			return "<domain/>", nil
		}}, want: ErrVMRestoreHostUnavailable},
		{name: "unreadable existing XML", conn: &fakeLibvirt{DomainLookupByNameFn: existing, DomainGetXMLDescFn: func(libvirt.Domain, libvirt.DomainXMLFlags) (string, error) {
			return "<domain>", nil
		}}, want: ErrVMRestoreHostUnavailable},
		{name: "existing NVRAM outside policy", conn: &fakeLibvirt{DomainLookupByNameFn: existing, DomainGetXMLDescFn: func(libvirt.Domain, libvirt.DomainXMLFlags) (string, error) {
			return "<domain><os><nvram>/root/vars.fd</nvram></os></domain>", nil
		}}, want: ErrVMRestoreTargetDisallowed},
		{name: "existing NVRAM through a parent directory", conn: &fakeLibvirt{DomainLookupByNameFn: existing, DomainGetXMLDescFn: func(libvirt.Domain, libvirt.DomainXMLFlags) (string, error) {
			return fmt.Sprintf("<domain><os><nvram>%s/x/../vars.fd</nvram></os></domain>", f.dir), nil
		}}, want: ErrVMRestoreTargetDisallowed},
		{name: "domain list failure", conn: &fakeLibvirt{DomainLookupByNameFn: vmcovNoDomain, ConnectListAllDomainsFn: func() ([]libvirt.Domain, uint32, error) {
			return nil, 0, errVMCovLibvirt
		}}, want: ErrVMRestoreHostUnavailable},
		{name: "domain appeared after lookup", conn: &fakeLibvirt{DomainLookupByNameFn: vmcovNoDomain, ConnectListAllDomainsFn: func() ([]libvirt.Domain, uint32, error) {
			return []libvirt.Domain{{Name: "fixture", UUID: f.uuid}}, 1, nil
		}}, want: ErrVMRestoreIdentityChanged},
		{name: "UUID used by another domain", conn: &fakeLibvirt{DomainLookupByNameFn: vmcovNoDomain, ConnectListAllDomainsFn: func() ([]libvirt.Domain, uint32, error) {
			return []libvirt.Domain{{Name: "clone", UUID: f.uuid}}, 1, nil
		}}, want: ErrVMRestoreIdentityChanged},
		{name: "other domain XML failure", conn: &fakeLibvirt{DomainLookupByNameFn: vmcovNoDomain,
			ConnectListAllDomainsFn: func() ([]libvirt.Domain, uint32, error) { return []libvirt.Domain{{Name: "other", ID: -1}}, 1, nil },
			DomainGetXMLDescFn:      func(libvirt.Domain, libvirt.DomainXMLFlags) (string, error) { return "", errVMCovLibvirt },
		}, want: ErrVMRestoreHostUnavailable},
		{name: "other domain uses the firmware", conn: &fakeLibvirt{DomainLookupByNameFn: vmcovNoDomain,
			ConnectListAllDomainsFn: func() ([]libvirt.Domain, uint32, error) { return []libvirt.Domain{{Name: "other", ID: -1}}, 1, nil },
			DomainGetXMLDescFn: func(libvirt.Domain, libvirt.DomainXMLFlags) (string, error) {
				return fmt.Sprintf("<domain><os><nvram>%s</nvram></os></domain>", f.nvram), nil
			},
		}, want: ErrVMRestoreTargetInUse},
		{name: "other domain XML truncated", conn: &fakeLibvirt{DomainLookupByNameFn: vmcovNoDomain,
			ConnectListAllDomainsFn: func() ([]libvirt.Domain, uint32, error) { return []libvirt.Domain{{Name: "other", ID: -1}}, 1, nil },
			DomainGetXMLDescFn:      func(libvirt.Domain, libvirt.DomainXMLFlags) (string, error) { return "<domain/><devices>", nil },
		}, want: ErrVMRestoreHostUnavailable},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ctx, key, review := tt.ctx, tt.key, f
			if ctx == nil {
				ctx = t.Context()
			}
			if key == nil && tt.name != "no review key" {
				key = []byte("fixture review key")
			}
			if tt.doc != nil {
				review.doc = tt.doc
			}
			if _, _, err := review.review(ctx, tt.conn, key); !errors.Is(err, tt.want) {
				t.Fatalf("review error = %v, want %v", err, tt.want)
			}
		})
	}
}

func TestVMCovCheckVMSharedPathResolvesLinks(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "disk.img")
	writeTestFile(t, target, "disk")
	link := filepath.Join(dir, "alias.img")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	resolved, err := resolveRestorePath(target)
	if err != nil {
		t.Fatal(err)
	}
	targets := map[string]bool{resolved: true}
	if err := checkVMSharedPath(link, targets); !errors.Is(err, ErrVMRestoreTargetInUse) {
		t.Fatalf("link to target = %v, want %v", err, ErrVMRestoreTargetInUse)
	}
	if err := checkVMSharedPath("relative.img", targets); err != nil {
		t.Fatalf("relative path = %v", err)
	}
	if err := checkVMSharedPath(filepath.Join(dir, "unrelated.img"), targets); err != nil {
		t.Fatalf("unrelated path = %v", err)
	}
	loop := filepath.Join(dir, "loop")
	if err := os.Symlink(loop, loop); err != nil {
		t.Fatal(err)
	}
	if err := checkVMSharedPath(filepath.Join(loop, "disk.img"), targets); !errors.Is(err, ErrVMRestoreHostUnavailable) {
		t.Fatalf("unresolvable path = %v, want %v", err, ErrVMRestoreHostUnavailable)
	}
}

func TestVMCovReviewVMTargetRefusesUnsafeFiles(t *testing.T) {
	dir := t.TempDir()
	linked := filepath.Join(dir, "linked.img")
	writeTestFile(t, linked, "disk")
	if err := os.Link(linked, filepath.Join(dir, "second-name.img")); err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(dir, "directory.img")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "file")
	writeTestFile(t, file, "not a directory")
	realDir := filepath.Join(dir, "real")
	if err := os.Mkdir(realDir, 0o700); err != nil {
		t.Fatal(err)
	}
	linkedDir := filepath.Join(dir, "linked-dir")
	if err := os.Symlink(realDir, linkedDir); err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name, path string
		want       error
	}{
		{name: "hard-linked disk", path: linked, want: ErrVMRestoreTargetDisallowed},
		{name: "directory", path: directory, want: ErrVMRestoreTargetDisallowed},
		{name: "below a regular file", path: filepath.Join(file, "disk.img"), want: ErrVMRestoreHostUnavailable},
		{name: "parent is a symlink", path: filepath.Join(linkedDir, "disk.img"), want: ErrVMRestoreTargetDisallowed},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := reviewVMTarget(tt.path, tt.path); !errors.Is(err, tt.want) {
				t.Fatalf("reviewVMTarget error = %v, want %v", err, tt.want)
			}
		})
	}

	fresh := filepath.Join(dir, "new", "disk.img")
	got, err := reviewVMTarget(fresh, fresh)
	if err != nil || got.File.Inode != 0 || got.Parent.Path != dir || got.Parent.Inode == 0 {
		t.Fatalf("new target = %+v, %v", got, err)
	}
}

func TestVMCovReadApprovedVMSource(t *testing.T) {
	dir := t.TempDir()
	if _, _, _, err := readApprovedVMSource(dir); err == nil || !strings.Contains(err.Error(), "opening staged VM domain.xml") {
		t.Fatalf("missing domain = %v", err)
	}
	writeTestFile(t, filepath.Join(dir, "domain.xml"), "<domain/>")
	writeTestFile(t, filepath.Join(dir, vmMetadataFileName), strings.Repeat("x", VMRestoreSidecarLimit+1))
	if _, _, _, err := readApprovedVMSource(dir); err == nil || !strings.Contains(err.Error(), "reading staged VM "+vmMetadataFileName) {
		t.Fatalf("oversized metadata = %v", err)
	}
	writeTestFile(t, filepath.Join(dir, vmMetadataFileName), "{}")
	writeTestFile(t, filepath.Join(dir, "vdisk0.img"), "disk")
	if err := os.Mkdir(filepath.Join(dir, "nested"), 0o700); err != nil {
		t.Fatal(err)
	}
	xmlData, metadata, artifacts, err := readApprovedVMSource(dir)
	if err != nil || string(xmlData) != "<domain/>" || string(metadata) != "{}" || strings.Join(artifacts, ",") != "domain.xml,vdisk0.img,"+vmMetadataFileName {
		t.Fatalf("source = %q, %q, %v, %v", xmlData, metadata, artifacts, err)
	}
}
