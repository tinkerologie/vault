//go:build linux

package engine

import (
	"context"
	"errors"
	"net"
	"net/url"
	"strings"
	"testing"
	"time"

	libvirt "github.com/digitalocean/go-libvirt"
)

// withConnectLibvirt swaps the libvirt constructor seam for one test.
func withConnectLibvirt(t *testing.T, fn func(*url.URL) (net.Conn, libvirtClient, error)) {
	t.Helper()
	old := connectLibvirt
	connectLibvirt = fn
	t.Cleanup(func() { connectLibvirt = old })
}

func TestNewVMHandler458(t *testing.T) {
	fake := &fakeLibvirt{}
	sock := &fakeSock{}
	var gotURI string
	withConnectLibvirt(t, func(u *url.URL) (net.Conn, libvirtClient, error) {
		gotURI = u.String()
		return sock, fake, nil
	})

	h, err := NewVMHandler()
	if err != nil {
		t.Fatalf("NewVMHandler: %v", err)
	}
	if h.conn != fake || h.sock != sock {
		t.Fatal("NewVMHandler did not keep the connection it was given")
	}
	if gotURI != string(libvirt.QEMUSystem) {
		t.Fatalf("connect URI = %q, want %q", gotURI, libvirt.QEMUSystem)
	}
}

func TestNewVMHandlerConnectError458(t *testing.T) {
	withConnectLibvirt(t, func(*url.URL) (net.Conn, libvirtClient, error) {
		return nil, nil, errors.New("no socket")
	})
	h, err := NewVMHandler()
	if err == nil || h != nil {
		t.Fatalf("NewVMHandler = %v, %v; want error", h, err)
	}
	if !strings.Contains(err.Error(), "connecting to libvirt: no socket") {
		t.Fatalf("error = %v", err)
	}
}

func TestVMHandlerClose458(t *testing.T) {
	t.Parallel()

	var nilHandler *VMHandler
	if err := nilHandler.Close(); err != nil {
		t.Fatalf("nil Close = %v", err)
	}
	if err := (&VMHandler{}).Close(); err != nil {
		t.Fatalf("Close without conn = %v", err)
	}

	disconnectErr := errors.New("disconnect failed")
	closeErr := errors.New("close failed")
	tests := []struct {
		name          string
		disconnectErr error
		sock          *fakeSock
		wantErr       error
	}{
		{name: "clean", sock: &fakeSock{}},
		{name: "disconnect error wins", disconnectErr: disconnectErr, sock: &fakeSock{closeErr: closeErr}, wantErr: disconnectErr},
		{name: "socket close error", sock: &fakeSock{closeErr: closeErr}, wantErr: closeErr},
		{name: "already closed socket ignored", sock: &fakeSock{closeErr: net.ErrClosed}},
		{name: "no socket", disconnectErr: disconnectErr, wantErr: disconnectErr},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			fake := &fakeLibvirt{DisconnectFn: func() error { return tt.disconnectErr }}
			h := &VMHandler{conn: fake}
			if tt.sock != nil {
				h.sock = tt.sock
			}
			err := h.Close()
			if !errors.Is(err, tt.wantErr) || (tt.wantErr == nil && err != nil) {
				t.Fatalf("Close = %v, want %v", err, tt.wantErr)
			}
			if fake.count("Disconnect") != 1 {
				t.Fatalf("Disconnect calls = %d", fake.count("Disconnect"))
			}
			if tt.sock != nil && tt.sock.closes.Load() != 1 {
				t.Fatalf("socket closes = %d, want 1", tt.sock.closes.Load())
			}
		})
	}
}

func TestDeriveCheckpointName458(t *testing.T) {
	t.Parallel()
	h := &VMHandler{}
	tests := []struct {
		name     string
		settings map[string]any
		want     string
	}{
		{name: "int64", settings: map[string]any{"backup_run_id": int64(5)}, want: "vault-run-5"},
		{name: "int", settings: map[string]any{"backup_run_id": 7}, want: "vault-run-7"},
		{name: "float64", settings: map[string]any{"backup_run_id": float64(9)}, want: "vault-run-9"},
		{name: "string", settings: map[string]any{"backup_run_id": " 12 "}, want: "vault-run-12"},
		{name: "zero int64", settings: map[string]any{"backup_run_id": int64(0)}},
		{name: "zero int", settings: map[string]any{"backup_run_id": 0}},
		{name: "zero float", settings: map[string]any{"backup_run_id": float64(0)}},
		{name: "zero string", settings: map[string]any{"backup_run_id": "0"}},
		{name: "blank string", settings: map[string]any{"backup_run_id": "  "}},
		{name: "unsupported type", settings: map[string]any{"backup_run_id": true}},
		{name: "missing", settings: map[string]any{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := h.deriveCheckpointName(BackupItem{Settings: tt.settings})
			if tt.want != "" {
				if got != tt.want {
					t.Fatalf("got %q, want %q", got, tt.want)
				}
				return
			}
			if !strings.HasPrefix(got, "vault-") || strings.HasPrefix(got, "vault-run-") {
				t.Fatalf("fallback name = %q, want vault-<timestamp>", got)
			}
		})
	}
}

func TestCheckpointExists458(t *testing.T) {
	t.Parallel()
	ok := &VMHandler{conn: &fakeLibvirt{}}
	if ok.checkpointExists(libvirt.Domain{}, "") {
		t.Fatal("empty name reported as existing")
	}
	if !ok.checkpointExists(libvirt.Domain{}, "cp") {
		t.Fatal("existing checkpoint not found")
	}
	missing := &VMHandler{conn: &fakeLibvirt{
		DomainCheckpointLookupByNameFn: func(libvirt.Domain, string) (libvirt.DomainCheckpoint, error) {
			return libvirt.DomainCheckpoint{}, errors.New("not found")
		},
	}}
	if missing.checkpointExists(libvirt.Domain{}, "cp") {
		t.Fatal("lookup error reported as existing")
	}
}

func TestCreateBackupCheckpoint458(t *testing.T) {
	t.Parallel()
	disks := []domainDisk{{Target: "vda", Path: "/x/vda.qcow2"}}

	t.Run("empty name is a no-op", func(t *testing.T) {
		t.Parallel()
		fake := &fakeLibvirt{}
		h := &VMHandler{conn: fake}
		if err := h.createBackupCheckpoint(libvirt.Domain{}, "", "", disks, noProgress); err != nil {
			t.Fatal(err)
		}
		if fake.count("DomainCheckpointCreateXML") != 0 {
			t.Fatal("checkpoint created for empty name")
		}
	})

	for _, parent := range []string{"", "vault-run-1"} {
		t.Run("parent="+parent, func(t *testing.T) {
			t.Parallel()
			var gotXML string
			fake := &fakeLibvirt{DomainCheckpointCreateXMLFn: func(_ libvirt.Domain, xml string) (libvirt.DomainCheckpoint, error) {
				gotXML = xml
				return libvirt.DomainCheckpoint{}, nil
			}}
			h := &VMHandler{conn: fake}
			if err := h.createBackupCheckpoint(libvirt.Domain{}, "vault-run-2", parent, disks, noProgress); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(gotXML, "<name>vault-run-2</name>") || !strings.Contains(gotXML, `name="vda"`) {
				t.Fatalf("checkpoint XML = %s", gotXML)
			}
			wantDesc := "Vault backup checkpoint"
			if parent != "" {
				wantDesc = "Vault incremental backup checkpoint (parent: vault-run-1)"
			}
			if !strings.Contains(gotXML, wantDesc) {
				t.Fatalf("checkpoint XML missing %q: %s", wantDesc, gotXML)
			}
		})
	}

	t.Run("create error", func(t *testing.T) {
		t.Parallel()
		h := &VMHandler{conn: &fakeLibvirt{DomainCheckpointCreateXMLFn: func(libvirt.Domain, string) (libvirt.DomainCheckpoint, error) {
			return libvirt.DomainCheckpoint{}, errors.New("boom")
		}}}
		err := h.createBackupCheckpoint(libvirt.Domain{}, "cp", "", disks, noProgress)
		if err == nil || !strings.Contains(err.Error(), "creating checkpoint cp: boom") {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestDeleteCheckpoint458(t *testing.T) {
	t.Parallel()
	boom := errors.New("boom")
	tests := []struct {
		name       string
		checkpoint string
		fake       *fakeLibvirt
		wantErr    string
		wantDelete int
	}{
		{name: "empty name", checkpoint: "", fake: &fakeLibvirt{}},
		{name: "domain gone", checkpoint: "cp", fake: &fakeLibvirt{DomainLookupByNameFn: func(string) (libvirt.Domain, error) {
			return libvirt.Domain{}, noDomainErr
		}}},
		{name: "lookup error", checkpoint: "cp", fake: &fakeLibvirt{DomainLookupByNameFn: func(string) (libvirt.Domain, error) {
			return libvirt.Domain{}, boom
		}}, wantErr: "looking up domain vm1: boom"},
		{name: "checkpoint missing", checkpoint: "cp", fake: &fakeLibvirt{DomainCheckpointLookupByNameFn: func(libvirt.Domain, string) (libvirt.DomainCheckpoint, error) {
			return libvirt.DomainCheckpoint{}, boom
		}}},
		{name: "delete error", checkpoint: "cp", fake: &fakeLibvirt{DomainCheckpointDeleteFn: func(libvirt.DomainCheckpoint) error {
			return boom
		}}, wantErr: "deleting checkpoint cp: boom", wantDelete: 1},
		{name: "deleted", checkpoint: "cp", fake: &fakeLibvirt{}, wantDelete: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			h := &VMHandler{conn: tt.fake}
			err := h.DeleteCheckpoint("vm1", tt.checkpoint)
			if tt.wantErr == "" && err != nil {
				t.Fatalf("err = %v", err)
			}
			if tt.wantErr != "" && (err == nil || err.Error() != tt.wantErr) {
				t.Fatalf("err = %v, want %q", err, tt.wantErr)
			}
			if got := tt.fake.count("DomainCheckpointDelete"); got != tt.wantDelete {
				t.Fatalf("delete calls = %d, want %d", got, tt.wantDelete)
			}
		})
	}
}

func TestStopStartedRestoreDomain458(t *testing.T) {
	boom := errors.New("boom")
	stateErr := func(libvirt.Domain) (int32, int32, error) { return 0, 0, boom }

	tests := []struct {
		name    string
		fake    *fakeLibvirt
		wantErr string
		destroy int
	}{
		{
			name: "graceful shutdown",
			fake: &fakeLibvirt{DomainGetStateFn: stateSeq(libvirt.DomainShutoff)},
		},
		{
			name: "domain already gone",
			fake: &fakeLibvirt{DomainShutdownFlagsFn: func(libvirt.Domain) error { return noDomainErr }},
		},
		{
			name: "shutdown error then destroy",
			fake: &fakeLibvirt{
				DomainShutdownFlagsFn: func(libvirt.Domain) error { return boom },
				DomainGetStateFn:      stateSeq(libvirt.DomainShutoff),
			},
			destroy: 1,
		},
		{
			name: "destroy reports no domain",
			fake: &fakeLibvirt{
				DomainShutdownFlagsFn: func(libvirt.Domain) error { return boom },
				DomainDestroyFn:       func(libvirt.Domain) error { return noDomainErr },
				DomainGetStateFn:      stateSeq(libvirt.DomainShutoff),
			},
			destroy: 1,
		},
		{
			name: "destroy fails",
			fake: &fakeLibvirt{
				DomainShutdownFlagsFn: func(libvirt.Domain) error { return boom },
				DomainDestroyFn:       func(libvirt.Domain) error { return errors.New("busy") },
			},
			wantErr: "shutdown failed: boom; destroy failed: busy",
			destroy: 1,
		},
		{
			name: "wait fails before and after destroy",
			fake: &fakeLibvirt{DomainGetStateFn: stateErr},
			wantErr: "shutdown failed: waiting for domain vm1 to shut off: boom; " +
				"post-destroy wait failed: waiting for domain vm1 to shut off: boom",
			destroy: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := &VMHandler{conn: tt.fake}
			err := h.stopStartedRestoreDomain(context.Background(), libvirt.Domain{Name: "vm1"}, "vm1")
			if tt.wantErr == "" && err != nil {
				t.Fatalf("err = %v", err)
			}
			if tt.wantErr != "" && (err == nil || err.Error() != tt.wantErr) {
				t.Fatalf("err = %v, want %q", err, tt.wantErr)
			}
			if got := tt.fake.count("DomainDestroy"); got != tt.destroy {
				t.Fatalf("destroy calls = %d, want %d", got, tt.destroy)
			}
		})
	}
}

func TestWaitForLibvirtDomainShutOff458(t *testing.T) {
	fastVMPolling(t, time.Minute)
	dom := libvirt.Domain{Name: "vm1"}

	t.Run("cancelled", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		h := &VMHandler{conn: &fakeLibvirt{}}
		if _, err := h.waitForLibvirtDomainShutOff(ctx, dom, "vm1", time.Minute); !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("state error", func(t *testing.T) {
		h := &VMHandler{conn: &fakeLibvirt{DomainGetStateFn: func(libvirt.Domain) (int32, int32, error) {
			return 0, 0, errors.New("rpc")
		}}}
		_, err := h.waitForLibvirtDomainShutOff(context.Background(), dom, "vm1", time.Minute)
		if err == nil || err.Error() != "waiting for domain vm1 to shut off: rpc" {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("reaches shutoff after polling", func(t *testing.T) {
		h := &VMHandler{conn: &fakeLibvirt{DomainGetStateFn: stateSeq(libvirt.DomainRunning, libvirt.DomainShutdown, libvirt.DomainShutoff)}}
		state, err := h.waitForLibvirtDomainShutOff(context.Background(), dom, "vm1", time.Minute)
		if err != nil || state != libvirt.DomainShutoff {
			t.Fatalf("state=%v err=%v", state, err)
		}
	})

	t.Run("timeout", func(t *testing.T) {
		h := &VMHandler{conn: &fakeLibvirt{}}
		state, err := h.waitForLibvirtDomainShutOff(context.Background(), dom, "vm1", -time.Second)
		if !errors.Is(err, errShutdownTimeout) || state != libvirt.DomainRunning {
			t.Fatalf("state=%v err=%v", state, err)
		}
		if !strings.Contains(err.Error(), "timed out with state running") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("cancelled during poll at deadline", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		h := &VMHandler{conn: &fakeLibvirt{DomainGetStateFn: func(libvirt.Domain) (int32, int32, error) {
			cancel()
			return int32(libvirt.DomainRunning), 0, nil
		}}}
		_, err := h.waitForLibvirtDomainShutOff(ctx, dom, "vm1", -time.Second)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("cancelled during sleep", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		h := &VMHandler{conn: &fakeLibvirt{DomainGetStateFn: func(libvirt.Domain) (int32, int32, error) {
			cancel()
			return int32(libvirt.DomainRunning), 0, nil
		}}}
		_, err := h.waitForLibvirtDomainShutOff(ctx, dom, "vm1", time.Minute)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestWaitForLibvirtDomainRunning458(t *testing.T) {
	fastVMPolling(t, time.Minute)
	dom := libvirt.Domain{Name: "vm1"}

	t.Run("running after poll", func(t *testing.T) {
		h := &VMHandler{conn: &fakeLibvirt{DomainGetStateFn: stateSeq(libvirt.DomainPaused, libvirt.DomainRunning)}}
		state, err := h.waitForLibvirtDomainRunning(context.Background(), dom, "vm1", time.Minute)
		if err != nil || state != libvirt.DomainRunning {
			t.Fatalf("state=%v err=%v", state, err)
		}
	})

	t.Run("state error", func(t *testing.T) {
		h := &VMHandler{conn: &fakeLibvirt{DomainGetStateFn: func(libvirt.Domain) (int32, int32, error) {
			return 0, 0, errors.New("rpc")
		}}}
		state, err := h.waitForLibvirtDomainRunning(context.Background(), dom, "vm1", time.Minute)
		if err == nil || err.Error() != "getting domain state: rpc" || state != libvirt.DomainNostate {
			t.Fatalf("state=%v err=%v", state, err)
		}
	})

	t.Run("timeout", func(t *testing.T) {
		h := &VMHandler{conn: &fakeLibvirt{DomainGetStateFn: stateSeq(libvirt.DomainPaused)}}
		_, err := h.waitForLibvirtDomainRunning(context.Background(), dom, "vm1", -time.Second)
		if err == nil || err.Error() != "waiting for domain vm1 to reach running state: timed out with state paused" {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("cancelled", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		h := &VMHandler{conn: &fakeLibvirt{DomainGetStateFn: stateSeq(libvirt.DomainPaused)}}
		_, err := h.waitForLibvirtDomainRunning(ctx, dom, "vm1", time.Minute)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestAbortLibvirtOnCancel458(t *testing.T) {
	t.Parallel()

	t.Run("fires after grace", func(t *testing.T) {
		t.Parallel()
		sock := &fakeSock{}
		h := &VMHandler{conn: &fakeLibvirt{}, sock: sock}
		ctx, cancel := context.WithCancel(context.Background())
		stop := h.abortLibvirtOnCancel(ctx, time.Millisecond)
		cancel()
		deadline := time.Now().Add(5 * time.Second)
		for sock.closes.Load() == 0 && time.Now().Before(deadline) {
			time.Sleep(time.Millisecond)
		}
		stop()
		stop() // idempotent
		if sock.closes.Load() != 1 {
			t.Fatalf("socket closes = %d, want 1", sock.closes.Load())
		}
		if !h.aborted {
			t.Fatal("aborted flag not set")
		}
	})

	t.Run("close error is logged", func(t *testing.T) {
		t.Parallel()
		sock := &fakeSock{closeErr: errors.New("close failed")}
		h := &VMHandler{conn: &fakeLibvirt{}, sock: sock}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		stop := h.abortLibvirtOnCancel(ctx, time.Millisecond)
		deadline := time.Now().Add(5 * time.Second)
		for sock.closes.Load() == 0 && time.Now().Before(deadline) {
			time.Sleep(time.Millisecond)
		}
		stop()
		if sock.closes.Load() != 1 || !h.aborted {
			t.Fatalf("closes=%d aborted=%v", sock.closes.Load(), h.aborted)
		}
	})

	t.Run("no socket", func(t *testing.T) {
		t.Parallel()
		h := &VMHandler{conn: &fakeLibvirt{}}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		stop := h.abortLibvirtOnCancel(ctx, time.Millisecond)
		time.Sleep(20 * time.Millisecond)
		stop()
		if h.aborted {
			t.Fatal("aborted set without a socket")
		}
	})

	t.Run("disarmed before cancel", func(t *testing.T) {
		t.Parallel()
		sock := &fakeSock{}
		h := &VMHandler{conn: &fakeLibvirt{}, sock: sock}
		stop := h.abortLibvirtOnCancel(context.Background(), time.Millisecond)
		stop()
		if sock.closes.Load() != 0 || h.aborted {
			t.Fatal("abort fired without cancellation")
		}
	})

	t.Run("disarmed during grace", func(t *testing.T) {
		t.Parallel()
		sock := &fakeSock{}
		h := &VMHandler{conn: &fakeLibvirt{}, sock: sock}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		stop := h.abortLibvirtOnCancel(ctx, time.Hour)
		time.Sleep(10 * time.Millisecond)
		stop()
		if sock.closes.Load() != 0 || h.aborted {
			t.Fatal("abort fired inside the grace period")
		}
	})
}

func TestRunCleanupBounded458(t *testing.T) {
	t.Run("not aborted runs cleanup", func(t *testing.T) {
		h := &VMHandler{conn: &fakeLibvirt{}}
		stopped := false
		want := errors.New("cleanup result")
		err := h.runCleanupBounded(func() { stopped = true }, func() error { return want })
		if !stopped || !errors.Is(err, want) {
			t.Fatalf("stopped=%v err=%v", stopped, err)
		}
	})

	t.Run("aborted reconnects first", func(t *testing.T) {
		newFake := &fakeLibvirt{}
		newSock := &fakeSock{}
		withConnectLibvirt(t, func(*url.URL) (net.Conn, libvirtClient, error) {
			return newSock, newFake, nil
		})
		h := &VMHandler{conn: &fakeLibvirt{}, sock: &fakeSock{}, aborted: true}
		var sawConn libvirtClient
		err := h.runCleanupBounded(func() {}, func() error {
			sawConn = h.conn
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		if sawConn != newFake || h.sock != newSock || h.aborted {
			t.Fatal("cleanup did not run on the reconnected client")
		}
	})

	t.Run("reconnect failure skips cleanup", func(t *testing.T) {
		withConnectLibvirt(t, func(*url.URL) (net.Conn, libvirtClient, error) {
			return nil, nil, errors.New("still down")
		})
		h := &VMHandler{conn: &fakeLibvirt{}, aborted: true}
		ran := false
		err := h.runCleanupBounded(func() {}, func() error { ran = true; return nil })
		if ran || err == nil || err.Error() != "reconnecting to libvirt after abort: still down" {
			t.Fatalf("ran=%v err=%v", ran, err)
		}
	})
}

func TestLookupDomainFresh458(t *testing.T) {
	t.Parallel()
	fallback := libvirt.Domain{Name: "old", ID: 1}
	h := &VMHandler{conn: &fakeLibvirt{DomainLookupByNameFn: func(name string) (libvirt.Domain, error) {
		return libvirt.Domain{Name: name, ID: 2}, nil
	}}}
	if got := h.lookupDomainFresh("vm", fallback); got.ID != 2 {
		t.Fatalf("got %+v, want fresh handle", got)
	}
	bad := &VMHandler{conn: &fakeLibvirt{DomainLookupByNameFn: func(string) (libvirt.Domain, error) {
		return libvirt.Domain{}, errors.New("gone")
	}}}
	if got := bad.lookupDomainFresh("vm", fallback); got != fallback {
		t.Fatalf("got %+v, want fallback", got)
	}
}

func TestSleepCtx458(t *testing.T) {
	t.Parallel()
	if err := sleepCtx(context.Background(), time.Millisecond); err != nil {
		t.Fatalf("sleepCtx = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := sleepCtx(ctx, time.Hour); !errors.Is(err, context.Canceled) {
		t.Fatalf("sleepCtx cancelled = %v", err)
	}
}

func TestLibvirtUndefineDomain458(t *testing.T) {
	t.Parallel()

	t.Run("first attempt succeeds", func(t *testing.T) {
		t.Parallel()
		fake := &fakeLibvirt{}
		if err := (&VMHandler{conn: fake}).libvirtUndefineDomain(libvirt.Domain{}); err != nil {
			t.Fatal(err)
		}
		if fake.count("DomainUndefineFlags") != 1 || fake.count("DomainUndefine") != 0 {
			t.Fatalf("calls = %v", fake.calls)
		}
	})

	t.Run("falls back to plain undefine", func(t *testing.T) {
		t.Parallel()
		fake := &fakeLibvirt{DomainUndefineFlagsFn: func(libvirt.Domain, libvirt.DomainUndefineFlagsValues) error {
			return errors.New("unsupported flag")
		}}
		if err := (&VMHandler{conn: fake}).libvirtUndefineDomain(libvirt.Domain{}); err != nil {
			t.Fatal(err)
		}
		if fake.count("DomainUndefineFlags") != 6 || fake.count("DomainUndefine") != 1 {
			t.Fatalf("calls = %v", fake.calls)
		}
	})

	t.Run("all attempts fail", func(t *testing.T) {
		t.Parallel()
		last := errors.New("last")
		fake := &fakeLibvirt{
			DomainUndefineFlagsFn: func(libvirt.Domain, libvirt.DomainUndefineFlagsValues) error { return errors.New("flags") },
			DomainUndefineFn:      func(libvirt.Domain) error { return last },
		}
		if err := (&VMHandler{conn: fake}).libvirtUndefineDomain(libvirt.Domain{}); !errors.Is(err, last) {
			t.Fatalf("err = %v, want last error", err)
		}
	})
}

func TestVMSmallHelpers458(t *testing.T) {
	t.Parallel()

	if !isLibvirtNoDomainError(noDomainErr) {
		t.Fatal("no-domain error not recognised")
	}
	if isLibvirtNoDomainError(libvirt.Error{Code: uint32(libvirt.ErrInternalError)}) || isLibvirtNoDomainError(errors.New("x")) {
		t.Fatal("unrelated error recognised as no-domain")
	}

	states := map[libvirt.DomainState]string{
		libvirt.DomainRunning:     "running",
		libvirt.DomainBlocked:     "blocked",
		libvirt.DomainPaused:      "paused",
		libvirt.DomainShutdown:    "shutdown",
		libvirt.DomainShutoff:     "shutoff",
		libvirt.DomainCrashed:     "crashed",
		libvirt.DomainPmsuspended: "pmsuspended",
		libvirt.DomainNostate:     "unknown",
	}
	for state, want := range states {
		if got := domainStateString(state); got != want {
			t.Errorf("domainStateString(%d) = %q, want %q", state, got, want)
		}
	}

	var uuid libvirt.UUID
	for i := range uuid {
		uuid[i] = byte(i)
	}
	if got := formatDomainUUID(uuid); got != "00010203-0405-0607-0809-0a0b0c0d0e0f" {
		t.Fatalf("formatDomainUUID = %q", got)
	}

	if got := stripSnapSuffix("/a/img.qcow2.snap.snap"); got != "/a/img.qcow2" {
		t.Fatalf("stripSnapSuffix = %q", got)
	}
	if got := stripSnapSuffix("/a/img.qcow2"); got != "/a/img.qcow2" {
		t.Fatalf("stripSnapSuffix no-op = %q", got)
	}

	if !domainBlockJobIsReady(0, 0) || !domainBlockJobIsReady(5, 5) || domainBlockJobIsReady(4, 5) {
		t.Fatal("domainBlockJobIsReady mismatch")
	}
	if !domainBlockJobIsCommit(libvirt.DomainBlockJobTypeCommit) || !domainBlockJobIsCommit(libvirt.DomainBlockJobTypeActiveCommit) ||
		domainBlockJobIsCommit(libvirt.DomainBlockJobTypeCopy) {
		t.Fatal("domainBlockJobIsCommit mismatch")
	}
}

func TestDestroyDomainWithRetry458(t *testing.T) {
	fastVMPolling(t, time.Minute)
	dom := libvirt.Domain{Name: "vm1"}
	busy := errors.New("device or resource busy")

	t.Run("cancelled before destroy", func(t *testing.T) {
		fake := &fakeLibvirt{}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if err := (&VMHandler{conn: fake}).destroyDomainWithRetry(ctx, dom, "vm1"); !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v", err)
		}
		if fake.count("DomainDestroy") != 0 {
			t.Fatal("destroy issued after cancellation")
		}
	})

	tests := []struct {
		name     string
		fake     *fakeLibvirt
		wantCall int
	}{
		{name: "destroy ok", fake: &fakeLibvirt{}, wantCall: 1},
		{name: "destroy no domain", fake: &fakeLibvirt{DomainDestroyFn: func(libvirt.Domain) error { return noDomainErr }}, wantCall: 1},
		{name: "state reports gone", fake: &fakeLibvirt{
			DomainDestroyFn:  func(libvirt.Domain) error { return busy },
			DomainGetStateFn: func(libvirt.Domain) (int32, int32, error) { return 0, 0, noDomainErr },
		}, wantCall: 1},
		{name: "state reports shutoff", fake: &fakeLibvirt{
			DomainDestroyFn:  func(libvirt.Domain) error { return busy },
			DomainGetStateFn: stateSeq(libvirt.DomainShutoff),
		}, wantCall: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := (&VMHandler{conn: tt.fake}).destroyDomainWithRetry(context.Background(), dom, "vm1"); err != nil {
				t.Fatalf("err = %v", err)
			}
			if got := tt.fake.count("DomainDestroy"); got != tt.wantCall {
				t.Fatalf("destroy calls = %d, want %d", got, tt.wantCall)
			}
		})
	}

	t.Run("retries until success", func(t *testing.T) {
		calls := 0
		fake := &fakeLibvirt{
			DomainDestroyFn: func(libvirt.Domain) error {
				calls++
				if calls < 3 {
					return busy
				}
				return nil
			},
			DomainGetStateFn: func(libvirt.Domain) (int32, int32, error) { return 0, 0, errors.New("rpc") },
		}
		if err := (&VMHandler{conn: fake}).destroyDomainWithRetry(context.Background(), dom, "vm1"); err != nil {
			t.Fatalf("err = %v", err)
		}
		if calls != 3 {
			t.Fatalf("destroy calls = %d, want 3", calls)
		}
	})

	t.Run("cancelled while retrying", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		fake := &fakeLibvirt{DomainDestroyFn: func(libvirt.Domain) error {
			cancel()
			return busy
		}}
		err := (&VMHandler{conn: fake}).destroyDomainWithRetry(ctx, dom, "vm1")
		if !errors.Is(err, context.Canceled) || !strings.Contains(err.Error(), "destroying domain vm1") {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestDestroyDomainWithRetryGivesUp458(t *testing.T) {
	fastVMPolling(t, 5*time.Millisecond)
	fake := &fakeLibvirt{DomainDestroyFn: func(libvirt.Domain) error { return errors.New("busy") }}
	err := (&VMHandler{conn: fake}).destroyDomainWithRetry(context.Background(), libvirt.Domain{}, "vm1")
	if err == nil || !strings.Contains(err.Error(), "destroying domain vm1: giving up after") || !strings.Contains(err.Error(), "busy") {
		t.Fatalf("err = %v", err)
	}
	if fake.count("DomainDestroy") < 2 {
		t.Fatalf("destroy calls = %d, want retries", fake.count("DomainDestroy"))
	}
}

func TestProbeActivity458(t *testing.T) {
	fastVMPolling(t, time.Minute)
	boom := errors.New("boom")

	unknown := []struct {
		name string
		fake *fakeLibvirt
	}{
		{name: "lookup error", fake: &fakeLibvirt{DomainLookupByNameFn: func(string) (libvirt.Domain, error) { return libvirt.Domain{}, boom }}},
		{name: "state error", fake: &fakeLibvirt{DomainGetStateFn: func(libvirt.Domain) (int32, int32, error) { return 0, 0, boom }}},
		{name: "first info error", fake: &fakeLibvirt{DomainGetInfoFn: func(libvirt.Domain) (uint8, uint64, uint64, uint16, uint64, error) {
			return 0, 0, 0, 0, 0, boom
		}}},
		{name: "second info error", fake: func() *fakeLibvirt {
			n := 0
			return &fakeLibvirt{DomainGetInfoFn: func(libvirt.Domain) (uint8, uint64, uint64, uint16, uint64, error) {
				n++
				if n == 2 {
					return 0, 0, 0, 0, 0, boom
				}
				return 0, 0, 0, 0, 100, nil
			}}
		}()},
		{name: "cpu counter went backwards", fake: func() *fakeLibvirt {
			cpu := []uint64{100, 50}
			n := 0
			return &fakeLibvirt{DomainGetInfoFn: func(libvirt.Domain) (uint8, uint64, uint64, uint16, uint64, error) {
				v := cpu[n]
				n++
				return 0, 0, 0, 0, v, nil
			}}
		}()},
	}
	for _, tt := range unknown {
		t.Run(tt.name, func(t *testing.T) {
			if got := (&VMHandler{conn: tt.fake}).ProbeActivity(context.Background(), "vm1"); got.Known {
				t.Fatalf("sample = %+v, want unknown", got)
			}
		})
	}

	t.Run("idle when not running", func(t *testing.T) {
		fake := &fakeLibvirt{DomainGetStateFn: stateSeq(libvirt.DomainShutoff)}
		got := (&VMHandler{conn: fake}).ProbeActivity(context.Background(), "vm1")
		if !got.Known || got.CPUPercent != 0 {
			t.Fatalf("sample = %+v", got)
		}
	})

	t.Run("cancelled while sampling", func(t *testing.T) {
		prev := vmActivitySampleInterval
		vmActivitySampleInterval = time.Hour
		// Restore even if the assertion fails, so later subtests never sample
		// for an hour.
		t.Cleanup(func() { vmActivitySampleInterval = prev })
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if got := (&VMHandler{conn: &fakeLibvirt{}}).ProbeActivity(ctx, "vm1"); got.Known {
			t.Fatalf("sample = %+v, want unknown", got)
		}
	})

	t.Run("busy", func(t *testing.T) {
		cpu := []uint64{0, 5_000_000}
		n := 0
		fake := &fakeLibvirt{DomainGetInfoFn: func(libvirt.Domain) (uint8, uint64, uint64, uint16, uint64, error) {
			v := cpu[n]
			n++
			return 0, 0, 0, 0, v, nil
		}}
		got := (&VMHandler{conn: fake}).ProbeActivity(context.Background(), "vm1")
		if !got.Known || got.CPUPercent <= 0 {
			t.Fatalf("sample = %+v", got)
		}
	})
}
