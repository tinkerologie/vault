//go:build linux

package engine

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	libvirt "github.com/digitalocean/go-libvirt"
)

// restoreFixture lays out a VM backup directory whose domain XML points the
// disk and NVRAM at paths under a temp target directory.
type restoreFixture struct {
	src, diskTarget, missingTarget, nvramTarget string
}

func newRestoreFixture(t *testing.T, meta string) restoreFixture {
	t.Helper()
	src := t.TempDir()
	target := t.TempDir()
	f := restoreFixture{
		src:           src,
		diskTarget:    filepath.Join(target, "vm", "vdisk1.img"),
		missingTarget: filepath.Join(target, "vm", "vdisk2.img"),
		nvramTarget:   filepath.Join(target, "nvram", "vm_VARS.fd"),
	}
	writeTestFile(t, filepath.Join(src, "domain.xml"), testDomainXML("raw", f.nvramTarget, f.diskTarget, f.missingTarget))
	writeTestFile(t, filepath.Join(src, "vdisk0.img"), "disk-bytes")
	writeTestFile(t, filepath.Join(src, "vm_VARS.fd"), "nvram-bytes")
	if meta != "" {
		writeTestFile(t, filepath.Join(src, vmMetadataFileName), meta)
	}
	return f
}

// lookupMissingFirst reports the domain as undefined on the first lookup (the
// pre-restore reconcile) and as present afterwards.
func lookupMissingFirst() func(string) (libvirt.Domain, error) {
	var mu sync.Mutex
	n := 0
	return func(name string) (libvirt.Domain, error) {
		mu.Lock()
		defer mu.Unlock()
		n++
		if n == 1 {
			return libvirt.Domain{}, noDomainErr
		}
		return libvirt.Domain{Name: name}, nil
	}
}

func TestVMRestoreDefinesWithoutStarting458(t *testing.T) {
	fastVMPolling(t, time.Minute)
	f := newRestoreFixture(t, `{"state":"shutoff"}`)
	var defined string
	fake := &fakeLibvirt{
		DomainLookupByNameFn:   func(string) (libvirt.Domain, error) { return libvirt.Domain{}, noDomainErr },
		DomainDefineXMLFlagsFn: func(xml string) (libvirt.Domain, error) { defined = xml; return libvirt.Domain{}, nil },
	}
	progress := &progressLog{}
	if err := (&VMHandler{conn: fake}).Restore(context.Background(), BackupItem{Name: "vm1"}, f.src, progress.fn); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if data, _ := os.ReadFile(f.diskTarget); string(data) != "disk-bytes" {
		t.Fatalf("disk = %q", data)
	}
	if data, _ := os.ReadFile(f.nvramTarget); string(data) != "nvram-bytes" {
		t.Fatalf("nvram = %q", data)
	}
	if _, err := os.Stat(f.missingTarget); !os.IsNotExist(err) {
		t.Fatal("disk without a backup file was created")
	}
	if !strings.Contains(defined, "vdisk1.img") || fake.count("DomainCreate") != 0 {
		t.Fatalf("defined=%q calls=%v", defined, fake.calls)
	}
	if !progress.has("restore complete") || !progress.has("restoring NVRAM") {
		t.Fatalf("progress = %+v", progress.entries)
	}
}

func TestVMRestoreToDestination458(t *testing.T) {
	fastVMPolling(t, time.Minute)
	f := newRestoreFixture(t, "")
	dest := t.TempDir()
	var defined string
	fake := &fakeLibvirt{
		DomainLookupByNameFn:   func(string) (libvirt.Domain, error) { return libvirt.Domain{}, noDomainErr },
		DomainDefineXMLFlagsFn: func(xml string) (libvirt.Domain, error) { defined = xml; return libvirt.Domain{}, nil },
	}
	item := BackupItem{Name: "vm1", Settings: map[string]any{"restore_destination": dest}}
	if err := (&VMHandler{conn: fake}).Restore(context.Background(), item, f.src, noProgress); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	resolvedDest, err := filepath.EvalSymlinks(dest)
	if err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(filepath.Join(resolvedDest, "vdisk1.img")); string(data) != "disk-bytes" {
		t.Fatalf("disk at destination = %q", data)
	}
	if data, _ := os.ReadFile(filepath.Join(resolvedDest, "vm_VARS.fd")); string(data) != "nvram-bytes" {
		t.Fatalf("nvram at destination = %q", data)
	}
	if !strings.Contains(defined, filepath.Join(resolvedDest, "vdisk1.img")) || strings.Contains(defined, f.diskTarget) {
		t.Fatalf("defined XML not rewritten: %s", defined)
	}
	if fake.count("DomainCreate") != 0 {
		t.Fatal("restore without metadata started the VM")
	}
}

func TestVMRestoreStartsAndVerifies458(t *testing.T) {
	fastVMPolling(t, time.Minute)
	f := newRestoreFixture(t, `{"state":"running"}`)
	fake := &fakeLibvirt{DomainLookupByNameFn: lookupMissingFirst()}
	progress := &progressLog{}
	if err := (&VMHandler{conn: fake}).Restore(context.Background(), BackupItem{Name: "vm1"}, f.src, progress.fn); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if fake.count("DomainCreate") != 1 || !progress.has("verified restored VM running") {
		t.Fatalf("calls=%v progress=%+v", fake.calls, progress.entries)
	}
}

func TestVMRestoreStartFailures458(t *testing.T) {
	fastVMPolling(t, time.Minute)
	boom := errors.New("boom")
	rpc := errors.New("rpc")

	tests := []struct {
		name string
		meta string
		fake func() *fakeLibvirt
		want string
	}{
		{
			name: "start fails and guest never came up",
			meta: `{"state":"running"}`,
			fake: func() *fakeLibvirt {
				return &fakeLibvirt{
					DomainLookupByNameFn: lookupMissingFirst(),
					DomainCreateFn:       func(libvirt.Domain) error { return boom },
					DomainGetStateFn:     stateSeq(libvirt.DomainShutoff),
				}
			},
			want: "starting restored VM: boom",
		},
		{
			name: "start fails and cleanup lookup fails",
			meta: `{"state":"running"}`,
			fake: func() *fakeLibvirt {
				return &fakeLibvirt{
					DomainLookupByNameFn: func(string) (libvirt.Domain, error) { return libvirt.Domain{}, noDomainErr },
					DomainCreateFn:       func(libvirt.Domain) error { return boom },
				}
			},
			want: "starting restored VM: boom; cleanup started restored VM: looking up restored VM for cleanup: domain not found",
		},
		{
			name: "running check fails",
			meta: `{"state":"running"}`,
			fake: func() *fakeLibvirt {
				return &fakeLibvirt{
					DomainLookupByNameFn: lookupMissingFirst(),
					DomainGetStateFn:     func(libvirt.Domain) (int32, int32, error) { return 0, 0, rpc },
				}
			},
			want: "verifying restored VM is running: getting domain state: rpc; cleanup started restored VM: checking restored VM state: rpc",
		},
		{
			name: "readiness fails and guest is stopped",
			meta: `{"state":"running","restore_verify":{"mode":"guest_agent","timeout_seconds":5}}`,
			fake: func() *fakeLibvirt {
				return &fakeLibvirt{
					DomainLookupByNameFn: lookupMissingFirst(),
					// running: start wait, running: cleanup check, shutoff: stop wait.
					DomainGetStateFn: stateSeq(libvirt.DomainRunning, libvirt.DomainRunning, libvirt.DomainShutoff),
					QEMUDomainAgentCommandFn: func(libvirt.Domain, string) (libvirt.OptString, error) {
						return nil, noDomainErr
					},
				}
			},
			want: "verifying restored VM readiness: waiting for guest agent: domain disappeared during guest agent verification",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newRestoreFixture(t, tt.meta)
			fake := tt.fake()
			err := (&VMHandler{conn: fake}).Restore(context.Background(), BackupItem{Name: "vm1"}, f.src, noProgress)
			if err == nil || !strings.HasPrefix(err.Error(), tt.want) {
				t.Fatalf("err = %v, want prefix %q", err, tt.want)
			}
		})
	}
}

func TestVMRestoreErrors458(t *testing.T) {
	fastVMPolling(t, time.Minute)
	boom := errors.New("boom")
	gone := func() *fakeLibvirt {
		return &fakeLibvirt{DomainLookupByNameFn: func(string) (libvirt.Domain, error) { return libvirt.Domain{}, noDomainErr }}
	}

	t.Run("missing domain xml", func(t *testing.T) {
		err := (&VMHandler{conn: gone()}).Restore(context.Background(), BackupItem{Name: "vm1"}, t.TempDir(), noProgress)
		if err == nil || !strings.HasPrefix(err.Error(), "reading domain XML") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("bad metadata", func(t *testing.T) {
		f := newRestoreFixture(t, "{not json")
		err := (&VMHandler{conn: gone()}).Restore(context.Background(), BackupItem{Name: "vm1"}, f.src, noProgress)
		if err == nil || !strings.HasPrefix(err.Error(), "reading VM metadata") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("destination outside allowed roots", func(t *testing.T) {
		f := newRestoreFixture(t, "")
		item := BackupItem{Name: "vm1", Settings: map[string]any{"restore_destination": "/nonexistent-vault-458/vms"}}
		err := (&VMHandler{conn: gone()}).Restore(context.Background(), item, f.src, noProgress)
		if err == nil || !strings.Contains(err.Error(), "invalid restore path") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("unparsable domain xml", func(t *testing.T) {
		src := t.TempDir()
		writeTestFile(t, filepath.Join(src, "domain.xml"), "<domain")
		err := (&VMHandler{conn: gone()}).Restore(context.Background(), BackupItem{Name: "vm1"}, src, noProgress)
		if err == nil || !strings.HasPrefix(err.Error(), "building restore plan") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("disk target outside allowed roots", func(t *testing.T) {
		src := t.TempDir()
		writeTestFile(t, filepath.Join(src, "domain.xml"), testDomainXML("raw", "", "/nonexistent-vault-458/vdisk1.img"))
		err := (&VMHandler{conn: gone()}).Restore(context.Background(), BackupItem{Name: "vm1"}, src, noProgress)
		if err == nil || !strings.HasPrefix(err.Error(), "validating restore plan") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("reconcile fails", func(t *testing.T) {
		f := newRestoreFixture(t, "")
		fake := &fakeLibvirt{DomainLookupByNameFn: func(string) (libvirt.Domain, error) { return libvirt.Domain{}, boom }}
		err := (&VMHandler{conn: fake}).Restore(context.Background(), BackupItem{Name: "vm1"}, f.src, noProgress)
		if err == nil || err.Error() != "cleaning up existing domain: looking up existing domain vm1: boom" {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("define fails", func(t *testing.T) {
		f := newRestoreFixture(t, "")
		fake := gone()
		fake.DomainDefineXMLFlagsFn = func(string) (libvirt.Domain, error) { return libvirt.Domain{}, boom }
		err := (&VMHandler{conn: fake}).Restore(context.Background(), BackupItem{Name: "vm1"}, f.src, noProgress)
		if err == nil || err.Error() != "defining domain: boom" {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("cancelled before copying disks", func(t *testing.T) {
		f := newRestoreFixture(t, "")
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		err := (&VMHandler{conn: gone()}).Restore(ctx, BackupItem{Name: "vm1"}, f.src, noProgress)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v", err)
		}
		if _, statErr := os.Stat(f.diskTarget); !os.IsNotExist(statErr) {
			t.Fatal("disk copied after cancellation")
		}
	})
}

func TestReconcileExistingDomainForRestore458(t *testing.T) {
	boom := errors.New("boom")
	busy := errors.New("busy")

	// destroyedState reports running until DomainDestroy has been called and
	// afterState from then on.
	destroyedState := func(fake *fakeLibvirt, after func() (int32, int32, error)) {
		var mu sync.Mutex
		destroyed := false
		fake.DomainDestroyFn = func(libvirt.Domain) error {
			mu.Lock()
			destroyed = true
			mu.Unlock()
			return nil
		}
		fake.DomainGetStateFn = func(libvirt.Domain) (int32, int32, error) {
			mu.Lock()
			defer mu.Unlock()
			if destroyed {
				return after()
			}
			return int32(libvirt.DomainRunning), 0, nil
		}
	}

	tests := []struct {
		name      string
		timeout   time.Duration
		fake      func() *fakeLibvirt
		cancelled bool
		want      string // "" = success; otherwise error prefix
		check     func(t *testing.T, f *fakeLibvirt)
	}{
		{
			name:    "domain does not exist",
			timeout: time.Minute,
			fake: func() *fakeLibvirt {
				return &fakeLibvirt{DomainLookupByNameFn: func(string) (libvirt.Domain, error) { return libvirt.Domain{}, noDomainErr }}
			},
			check: func(t *testing.T, f *fakeLibvirt) {
				if f.count("DomainGetState") != 0 {
					t.Fatal("reconcile touched a domain that does not exist")
				}
			},
		},
		{
			name:    "graceful stop, managed save removed",
			timeout: time.Minute,
			fake: func() *fakeLibvirt {
				return &fakeLibvirt{
					DomainGetStateFn:            stateSeq(libvirt.DomainRunning, libvirt.DomainShutoff),
					DomainHasManagedSaveImageFn: func(libvirt.Domain) (int32, error) { return 1, nil },
				}
			},
			check: func(t *testing.T, f *fakeLibvirt) {
				if f.count("DomainShutdownFlags") != 1 || f.count("DomainManagedSaveRemove") != 1 || f.count("DomainUndefineFlags") != 1 || f.count("DomainDestroy") != 0 {
					t.Fatalf("calls = %v", f.calls)
				}
			},
		},
		{
			name:    "state error",
			timeout: time.Minute,
			fake: func() *fakeLibvirt {
				return &fakeLibvirt{DomainGetStateFn: func(libvirt.Domain) (int32, int32, error) { return 0, 0, boom }}
			},
			want: "getting existing domain state: boom",
		},
		{
			name:    "managed save check fails",
			timeout: time.Minute,
			fake: func() *fakeLibvirt {
				return &fakeLibvirt{
					DomainGetStateFn:            stateSeq(libvirt.DomainShutoff),
					DomainHasManagedSaveImageFn: func(libvirt.Domain) (int32, error) { return 0, boom },
				}
			},
			want: "checking managed save: boom",
		},
		{
			name:    "managed save removal fails",
			timeout: time.Minute,
			fake: func() *fakeLibvirt {
				return &fakeLibvirt{
					DomainGetStateFn:            stateSeq(libvirt.DomainShutoff),
					DomainHasManagedSaveImageFn: func(libvirt.Domain) (int32, error) { return 1, nil },
					DomainManagedSaveRemoveFn:   func(libvirt.Domain) error { return boom },
				}
			},
			want: "removing managed save: boom",
		},
		{
			name:    "undefine fails",
			timeout: time.Minute,
			fake: func() *fakeLibvirt {
				return &fakeLibvirt{
					DomainGetStateFn:      stateSeq(libvirt.DomainShutoff),
					DomainUndefineFlagsFn: func(libvirt.Domain, libvirt.DomainUndefineFlagsValues) error { return boom },
					DomainUndefineFn:      func(libvirt.Domain) error { return boom },
				}
			},
			want: "undefining existing domain vm1: boom",
		},
		{
			name:    "shutdown refused, forced stop",
			timeout: time.Minute,
			fake: func() *fakeLibvirt {
				f := &fakeLibvirt{DomainShutdownFlagsFn: func(libvirt.Domain) error { return boom }}
				destroyedState(f, func() (int32, int32, error) { return int32(libvirt.DomainShutoff), 0, nil })
				return f
			},
			check: func(t *testing.T, f *fakeLibvirt) {
				if f.count("DomainDestroy") != 1 || f.count("DomainUndefineFlags") != 1 {
					t.Fatalf("calls = %v", f.calls)
				}
			},
		},
		{
			name:    "shutdown ignored until timeout, forced stop",
			timeout: 5 * time.Millisecond,
			fake: func() *fakeLibvirt {
				f := &fakeLibvirt{}
				destroyedState(f, func() (int32, int32, error) { return int32(libvirt.DomainShutoff), 0, nil })
				return f
			},
			check: func(t *testing.T, f *fakeLibvirt) {
				if f.count("DomainShutdownFlags") != 1 || f.count("DomainDestroy") != 1 {
					t.Fatalf("calls = %v", f.calls)
				}
			},
		},
		{
			name:    "post-destroy wait fails",
			timeout: time.Minute,
			fake: func() *fakeLibvirt {
				f := &fakeLibvirt{DomainShutdownFlagsFn: func(libvirt.Domain) error { return boom }}
				destroyedState(f, func() (int32, int32, error) { return 0, 0, errors.New("rpc") })
				return f
			},
			want: "waiting for domain vm1 to shut off: rpc",
		},
		{
			name:    "shutdown and destroy both fail",
			timeout: 5 * time.Millisecond,
			fake: func() *fakeLibvirt {
				return &fakeLibvirt{
					DomainShutdownFlagsFn: func(libvirt.Domain) error { return boom },
					DomainDestroyFn:       func(libvirt.Domain) error { return busy },
				}
			},
			want: "stopping existing domain: shutdown failed: boom; destroy failed: destroying domain vm1: giving up",
		},
		{
			name:    "graceful timeout then destroy fails",
			timeout: 5 * time.Millisecond,
			fake: func() *fakeLibvirt {
				return &fakeLibvirt{DomainDestroyFn: func(libvirt.Domain) error { return busy }}
			},
			want: "forcing existing domain stop: destroying domain vm1: giving up",
		},
		{
			name:      "cancelled while stopping",
			timeout:   time.Minute,
			cancelled: true,
			fake:      func() *fakeLibvirt { return &fakeLibvirt{} },
			want:      "context canceled",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fastVMPolling(t, tt.timeout)
			ctx := context.Background()
			if tt.cancelled {
				c, cancel := context.WithCancel(ctx)
				cancel()
				ctx = c
			}
			fake := tt.fake()
			err := (&VMHandler{conn: fake}).reconcileExistingDomainForRestore(ctx, "vm1", noProgress)
			if tt.want == "" && err != nil {
				t.Fatalf("err = %v", err)
			}
			if tt.want != "" && (err == nil || !strings.HasPrefix(err.Error(), tt.want)) {
				t.Fatalf("err = %v, want prefix %q", err, tt.want)
			}
			if tt.check != nil {
				tt.check(t, fake)
			}
		})
	}
}

func TestPrepareDomainForBackup458(t *testing.T) {
	boom := errors.New("boom")
	busy := errors.New("busy")
	dom := libvirt.Domain{Name: "vm1"}

	t.Run("shutoff start fails", func(t *testing.T) {
		fastVMPolling(t, time.Minute)
		fake := &fakeLibvirt{DomainCreateWithFlagsFn: func(libvirt.Domain, uint32) (libvirt.Domain, error) { return libvirt.Domain{}, boom }}
		_, _, err := (&VMHandler{conn: fake}).prepareDomainForBackup(context.Background(), "vm1", dom, libvirt.DomainShutoff, "cold", noProgress)
		if err == nil || err.Error() != "starting domain paused for backup: boom" {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("shutoff starts paused and tears down", func(t *testing.T) {
		fastVMPolling(t, time.Minute)
		var startFlags uint32
		fake := &fakeLibvirt{DomainCreateWithFlagsFn: func(d libvirt.Domain, flags uint32) (libvirt.Domain, error) {
			startFlags = flags
			return d, nil
		}}
		_, restore, err := (&VMHandler{conn: fake}).prepareDomainForBackup(context.Background(), "vm1", dom, libvirt.DomainShutoff, "cold", noProgress)
		if err != nil {
			t.Fatal(err)
		}
		if startFlags != uint32(libvirt.DomainStartPaused) {
			t.Fatalf("start flags = %d", startFlags)
		}
		if err := restore(); err != nil || fake.count("DomainDestroy") != 1 {
			t.Fatalf("restore = %v calls=%v", err, fake.calls)
		}
	})

	for _, tc := range []struct {
		name  string
		state libvirt.DomainState
		mode  string
	}{
		{name: "paused cold", state: libvirt.DomainPaused, mode: "cold"},
		{name: "running snapshot", state: libvirt.DomainRunning, mode: "snapshot"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fastVMPolling(t, time.Minute)
			fake := &fakeLibvirt{}
			got, restore, err := (&VMHandler{conn: fake}).prepareDomainForBackup(context.Background(), "vm1", dom, tc.state, tc.mode, noProgress)
			if err != nil || got != dom || restore() != nil || len(fake.calls) != 0 {
				t.Fatalf("got=%v err=%v calls=%v", got, err, fake.calls)
			}
		})
	}

	coldTests := []struct {
		name       string
		timeout    time.Duration
		fake       func() *fakeLibvirt
		wantErr    string // prepare error prefix
		restoreErr string // restore error prefix ("" = success)
		check      func(t *testing.T, f *fakeLibvirt)
	}{
		{
			name:    "graceful stop, resumes paused session",
			timeout: time.Minute,
			fake: func() *fakeLibvirt {
				return &fakeLibvirt{DomainGetStateFn: stateSeq(libvirt.DomainShutoff, libvirt.DomainPaused)}
			},
			check: func(t *testing.T, f *fakeLibvirt) {
				if f.count("DomainResume") != 1 || f.count("DomainCreate") != 0 {
					t.Fatalf("calls = %v", f.calls)
				}
			},
		},
		{
			name:    "resume fails",
			timeout: time.Minute,
			fake: func() *fakeLibvirt {
				return &fakeLibvirt{
					DomainGetStateFn: stateSeq(libvirt.DomainShutoff, libvirt.DomainPaused),
					DomainResumeFn:   func(libvirt.Domain) error { return boom },
				}
			},
			restoreErr: "resuming domain after cold backup: boom",
		},
		{
			name:    "restart fails",
			timeout: time.Minute,
			fake: func() *fakeLibvirt {
				return &fakeLibvirt{
					DomainGetStateFn: stateSeq(libvirt.DomainShutoff),
					DomainCreateFn:   func(libvirt.Domain) error { return boom },
				}
			},
			restoreErr: "restarting domain after cold backup: boom",
		},
		{
			name:    "session teardown fails but guest restarted",
			timeout: 5 * time.Millisecond,
			fake: func() *fakeLibvirt {
				return &fakeLibvirt{
					DomainGetStateFn: stateSeq(libvirt.DomainShutoff, libvirt.DomainRunning),
					DomainDestroyFn:  func(libvirt.Domain) error { return busy },
				}
			},
			restoreErr: "stopping paused backup session: destroying domain vm1: giving up",
			check: func(t *testing.T, f *fakeLibvirt) {
				if f.count("DomainCreate") != 1 {
					t.Fatalf("guest not restarted: %v", f.calls)
				}
			},
		},
		{
			name:    "shutdown refused, forced stop",
			timeout: time.Minute,
			fake: func() *fakeLibvirt {
				return &fakeLibvirt{
					DomainShutdownFlagsFn: func(libvirt.Domain) error { return boom },
					DomainGetStateFn:      stateSeq(libvirt.DomainShutoff),
				}
			},
			check: func(t *testing.T, f *fakeLibvirt) {
				if f.count("DomainDestroy") != 2 { // forced stop + session teardown
					t.Fatalf("calls = %v", f.calls)
				}
			},
		},
		{
			name:    "shutdown and destroy fail",
			timeout: 5 * time.Millisecond,
			fake: func() *fakeLibvirt {
				return &fakeLibvirt{
					DomainShutdownFlagsFn: func(libvirt.Domain) error { return boom },
					DomainDestroyFn:       func(libvirt.Domain) error { return busy },
				}
			},
			wantErr: "stopping domain for cold backup: shutdown failed: boom; destroy failed: destroying domain vm1: giving up",
		},
		{
			name:    "graceful timeout then destroy fails",
			timeout: 5 * time.Millisecond,
			fake: func() *fakeLibvirt {
				return &fakeLibvirt{DomainDestroyFn: func(libvirt.Domain) error { return busy }}
			},
			wantErr: "forcing domain stop for cold backup: destroying domain vm1: giving up",
		},
		{
			name:    "post-destroy wait fails",
			timeout: time.Minute,
			fake: func() *fakeLibvirt {
				return &fakeLibvirt{
					DomainShutdownFlagsFn: func(libvirt.Domain) error { return boom },
					DomainGetStateFn:      func(libvirt.Domain) (int32, int32, error) { return 0, 0, errors.New("rpc") },
				}
			},
			wantErr: "waiting for domain vm1 to shut off: rpc",
		},
		{
			name:    "paused session start fails",
			timeout: time.Minute,
			fake: func() *fakeLibvirt {
				return &fakeLibvirt{
					DomainGetStateFn:        stateSeq(libvirt.DomainShutoff),
					DomainCreateWithFlagsFn: func(libvirt.Domain, uint32) (libvirt.Domain, error) { return libvirt.Domain{}, boom },
				}
			},
			wantErr: "starting paused domain for cold backup: boom",
		},
	}
	for _, tt := range coldTests {
		t.Run("cold running: "+tt.name, func(t *testing.T) {
			fastVMPolling(t, tt.timeout)
			fake := tt.fake()
			_, restore, err := (&VMHandler{conn: fake}).prepareDomainForBackup(context.Background(), "vm1", dom, libvirt.DomainRunning, "cold", noProgress)
			if tt.wantErr != "" {
				if err == nil || !strings.HasPrefix(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want prefix %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("prepare: %v", err)
			}
			rerr := restore()
			if tt.restoreErr == "" && rerr != nil {
				t.Fatalf("restore = %v", rerr)
			}
			if tt.restoreErr != "" && (rerr == nil || !strings.HasPrefix(rerr.Error(), tt.restoreErr)) {
				t.Fatalf("restore = %v, want prefix %q", rerr, tt.restoreErr)
			}
			if tt.check != nil {
				tt.check(t, fake)
			}
		})
	}

	t.Run("cancelled during cold stop", func(t *testing.T) {
		fastVMPolling(t, time.Minute)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		fake := &fakeLibvirt{}
		_, _, err := (&VMHandler{conn: fake}).prepareDomainForBackup(ctx, "vm1", dom, libvirt.DomainRunning, "cold", noProgress)
		if !errors.Is(err, context.Canceled) || fake.count("DomainDestroy") != 0 {
			t.Fatalf("err=%v calls=%v", err, fake.calls)
		}
	})
}
