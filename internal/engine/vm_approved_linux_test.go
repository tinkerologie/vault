//go:build linux

package engine

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	libvirt "github.com/digitalocean/go-libvirt"
)

func TestApprovedVMRestoreRechecksEffectsBeforeMutation(t *testing.T) {
	for _, tt := range []struct {
		name   string
		change func(t *testing.T, stage, target string)
	}{
		{name: "saved effects execute"},
		{name: "new destination disk", change: func(t *testing.T, _, target string) {
			writeTestFile(t, target, "another disk")
		}},
		{name: "disk data removed", change: func(t *testing.T, stage, _ string) {
			if err := os.Remove(filepath.Join(stage, "recorded.img")); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			target := filepath.Join(t.TempDir(), "disk.img")
			stage := t.TempDir()
			doc := fmt.Sprintf(`<domain><name>fixture</name><uuid>22222222-3333-4444-aaaa-555555555555</uuid><devices><disk type="file" device="disk"><source file=%q/><target dev="vda"/></disk><graphics passwd="private"/></devices></domain>`, target)
			meta := `{"backup_type":"full","state":"running","restore_verify":{"mode":"guest_agent"},"disks":[{"target":"vda","backup_file":"recorded.img","format":"raw"}]}`
			writeTestFile(t, filepath.Join(stage, "domain.xml"), doc)
			writeTestFile(t, filepath.Join(stage, "vm_meta.json"), meta)
			writeTestFile(t, filepath.Join(stage, "recorded.img"), "saved contents")

			var defined bool
			fake := &fakeLibvirt{
				DomainLookupByNameFn: func(name string) (libvirt.Domain, error) {
					if !defined {
						return libvirt.Domain{}, noDomainErr
					}
					return libvirt.Domain{Name: name}, nil
				},
				DomainDefineXMLFlagsFn: func(xml string) (libvirt.Domain, error) {
					if xml != doc {
						t.Error("XML for the original target was not preserved")
					}
					defined = true
					return libvirt.Domain{Name: "fixture"}, nil
				},
			}
			handler := &VMHandler{conn: fake}
			key := []byte("fixture review key")
			effects, expected, err := handler.ReviewVMRestore(t.Context(), "fixture", "", []byte(doc), []byte(meta), []string{"recorded.img"}, key)
			if err != nil || len(effects.Disks) != 1 || effects.Disks[0].Destination != target || effects.Disks[0].Replace {
				t.Fatalf("review = %+v, err = %v", effects, err)
			}
			projected, err := json.Marshal(effects)
			if err != nil || strings.Contains(string(projected), "private") {
				t.Fatalf("effects contain the graphics password: %s", projected)
			}

			if tt.change != nil {
				tt.change(t, stage, target)
			}
			var readiness VMReadinessResult
			ctx := WithVMReadinessResult(WithVMRestoreExpectation(t.Context(), key, expected), &readiness)
			err = handler.Restore(ctx, BackupItem{Name: "fixture"}, stage, noProgress)
			if tt.change != nil {
				if !errors.Is(err, ErrVMReviewedEffectsChanged) || defined || fake.count("DomainShutdownFlags") != 0 ||
					fake.count("DomainUndefineFlags") != 0 || fake.count("DomainCreate") != 0 {
					t.Fatalf("restore = %v, calls = %v", err, fake.calls)
				}
				if data, _ := os.ReadFile(target); string(data) == "saved contents" {
					t.Fatal("restore wrote the disk after invalidation")
				}
				return
			}
			if err != nil || !defined {
				t.Fatalf("restore = %v, calls = %v", err, fake.calls)
			}
			if data, err := os.ReadFile(target); err != nil || string(data) != "saved contents" {
				t.Fatalf("restored disk = %q, err = %v", data, err)
			}
			if readiness.Startup != "passed" || readiness.Running != "passed" || readiness.GuestCheck != "guest_agent" ||
				readiness.GuestOutcome != "passed" || readiness.ApplicationHealth != "unchecked" {
				t.Fatalf("readiness = %+v", readiness)
			}
		})
	}
}

func TestApprovedVMReviewChecksTargetOwnershipAndIdentity(t *testing.T) {
	target := filepath.Join(t.TempDir(), "disk.img")
	doc := []byte(fmt.Sprintf(`<domain><name>fixture</name><uuid>22222222-3333-4444-aaaa-555555555555</uuid><devices><disk device="disk"><source file=%q/><target dev="vda"/></disk></devices></domain>`, target))
	meta := []byte(`{"backup_type":"full","state":"shutoff","restore_verify":{"mode":"tcp","tcp_host":"secret.invalid","tcp_port":8080},"disks":[{"target":"vda","backup_file":"disk.img","format":"raw"}]}`)
	key := []byte("fixture review key")
	for _, tt := range []struct {
		name, vmName string
		fake         *fakeLibvirt
		want         error
	}{
		{
			name: "safe", vmName: "fixture",
			fake: &fakeLibvirt{DomainLookupByNameFn: func(string) (libvirt.Domain, error) { return libvirt.Domain{}, noDomainErr }},
		},
		{
			name: "name mismatch", vmName: "wrong",
			fake: &fakeLibvirt{}, want: ErrVMRestoreIdentityChanged,
		},
		{
			name: "existing UUID mismatch", vmName: "fixture",
			fake: &fakeLibvirt{}, want: ErrVMRestoreIdentityChanged,
		},
		{
			name: "target used by a different domain", vmName: "fixture",
			fake: &fakeLibvirt{
				DomainLookupByNameFn: func(string) (libvirt.Domain, error) { return libvirt.Domain{}, noDomainErr },
				ConnectListAllDomainsFn: func() ([]libvirt.Domain, uint32, error) {
					return []libvirt.Domain{{Name: "another", ID: -1}}, 1, nil
				},
				DomainGetXMLDescFn: func(libvirt.Domain, libvirt.DomainXMLFlags) (string, error) {
					return string(doc), nil
				},
			},
			want: ErrVMRestoreTargetInUse,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			effects, _, err := (&VMHandler{conn: tt.fake}).ReviewVMRestore(t.Context(), tt.vmName, "", doc, meta, []string{"disk.img"}, key)
			if !errors.Is(err, tt.want) {
				t.Fatalf("review error = %v, want %v", err, tt.want)
			}
			if tt.want == nil {
				data, err := json.Marshal(effects)
				if err != nil || strings.Contains(string(data), "secret.invalid") || effects.ReadinessAddress != "explicit" || effects.ReadinessPort != 8080 {
					t.Fatalf("effects = %s, err = %v", data, err)
				}
			}
		})
	}
}
