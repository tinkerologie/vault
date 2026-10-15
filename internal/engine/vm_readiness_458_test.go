//go:build linux

package engine

import (
	"context"
	"errors"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	libvirt "github.com/digitalocean/go-libvirt"
)

// listenLoopback opens a loopback listener that accepts and drops
// connections, returning its port.
func listenLoopback(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
		}
	}()
	return ln.Addr().(*net.TCPAddr).Port
}

// closedLoopbackPort returns a loopback port with nothing listening on it.
func closedLoopbackPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	return port
}

func TestVerifyRestoredVMReady458(t *testing.T) {
	fastVMPolling(t, time.Minute)
	port := listenLoopback(t)

	tests := []struct {
		name    string
		config  vmRestoreVerifyConfig
		fake    *fakeLibvirt
		wantErr string
		wantMsg string
	}{
		{name: "running only", config: vmRestoreVerifyConfig{Mode: vmRestoreVerifyModeRunning}, fake: &fakeLibvirt{}},
		{
			name:    "guest agent answers",
			config:  vmRestoreVerifyConfig{Mode: vmRestoreVerifyModeGuestAgent, TimeoutSeconds: 5},
			fake:    &fakeLibvirt{},
			wantMsg: "verified QEMU guest agent",
		},
		{
			name:   "guest agent gone",
			config: vmRestoreVerifyConfig{Mode: vmRestoreVerifyModeGuestAgent, TimeoutSeconds: 5},
			fake: &fakeLibvirt{QEMUDomainAgentCommandFn: func(libvirt.Domain, string) (libvirt.OptString, error) {
				return nil, noDomainErr
			}},
			wantErr: "waiting for guest agent: domain disappeared during guest agent verification",
		},
		{
			name:    "tcp explicit host",
			config:  vmRestoreVerifyConfig{Mode: vmRestoreVerifyModeTCP, TCPHost: "127.0.0.1", TCPPort: port, TimeoutSeconds: 5},
			fake:    &fakeLibvirt{},
			wantMsg: "verified 127.0.0.1:" + strconv.Itoa(port),
		},
		{
			name:   "tcp detected host is loopback",
			config: vmRestoreVerifyConfig{Mode: vmRestoreVerifyModeTCP, TCPPort: port, TimeoutSeconds: 5},
			fake: &fakeLibvirt{DomainInterfaceAddressesFn: func(libvirt.Domain, uint32) ([]libvirt.DomainInterface, error) {
				return []libvirt.DomainInterface{{Addrs: []libvirt.DomainIPAddr{{Type: int32(libvirt.IPAddrTypeIpv4), Addr: "127.0.0.1"}}}}, nil
			}},
			// Address selection skips loopback answers, so with no other
			// address the endpoint cannot be resolved.
			wantErr: "libvirt did not report a reachable guest address",
		},
		{
			name:    "tcp invalid port",
			config:  vmRestoreVerifyConfig{Mode: vmRestoreVerifyModeTCP, TCPHost: "127.0.0.1", TCPPort: 0},
			fake:    &fakeLibvirt{},
			wantErr: "tcp restore verify mode requires a port between 1 and 65535",
		},
		{
			name:    "unsupported mode",
			config:  vmRestoreVerifyConfig{Mode: "ping"},
			fake:    &fakeLibvirt{},
			wantErr: `unsupported restore verify mode "ping"`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			progress := &progressLog{}
			err := (&VMHandler{conn: tt.fake}).verifyRestoredVMReady(context.Background(), libvirt.Domain{}, "vm1", tt.config, progress.fn)
			if tt.wantErr == "" && err != nil {
				t.Fatalf("err = %v", err)
			}
			if tt.wantErr != "" && (err == nil || !strings.HasPrefix(err.Error(), tt.wantErr)) {
				t.Fatalf("err = %v, want prefix %q", err, tt.wantErr)
			}
			if tt.wantMsg != "" && !progress.has(tt.wantMsg) {
				t.Fatalf("progress = %+v, want %q", progress.entries, tt.wantMsg)
			}
		})
	}
}

func TestVerifyRestoredVMReadyTCPUnreachable458(t *testing.T) {
	fastVMPolling(t, time.Minute)
	port := closedLoopbackPort(t)
	config := vmRestoreVerifyConfig{Mode: vmRestoreVerifyModeTCP, TCPHost: "127.0.0.1", TCPPort: port, TimeoutSeconds: 1}
	err := (&VMHandler{conn: &fakeLibvirt{}}).verifyRestoredVMReady(context.Background(), libvirt.Domain{}, "vm1", config, noProgress)
	if err == nil || !strings.HasPrefix(err.Error(), "waiting for restored VM service 127.0.0.1:") {
		t.Fatalf("err = %v", err)
	}
}

func TestWaitForVMGuestAgent458(t *testing.T) {
	fastVMPolling(t, time.Minute)
	agentErr := errors.New("agent not connected")

	t.Run("succeeds after retries", func(t *testing.T) {
		n := 0
		fake := &fakeLibvirt{QEMUDomainAgentCommandFn: func(_ libvirt.Domain, cmd string) (libvirt.OptString, error) {
			if cmd != `{"execute":"guest-ping"}` {
				t.Errorf("agent command = %q", cmd)
			}
			n++
			if n < 3 {
				return nil, agentErr
			}
			return nil, nil
		}}
		if err := (&VMHandler{conn: fake}).waitForVMGuestAgent(context.Background(), libvirt.Domain{}, "vm1", time.Minute); err != nil {
			t.Fatal(err)
		}
		if n != 3 {
			t.Fatalf("agent calls = %d", n)
		}
	})

	t.Run("zero timeout", func(t *testing.T) {
		fake := &fakeLibvirt{}
		err := (&VMHandler{conn: fake}).waitForVMGuestAgent(context.Background(), libvirt.Domain{}, "vm1", 0)
		if err == nil || err.Error() != "timed out waiting for guest agent on domain vm1" || fake.count("QEMUDomainAgentCommand") != 0 {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("times out with last error", func(t *testing.T) {
		fake := &fakeLibvirt{QEMUDomainAgentCommandFn: func(libvirt.Domain, string) (libvirt.OptString, error) { return nil, agentErr }}
		err := (&VMHandler{conn: fake}).waitForVMGuestAgent(context.Background(), libvirt.Domain{}, "vm1", 20*time.Millisecond)
		if err == nil || err.Error() != "timed out waiting for guest agent on domain vm1: agent not connected" {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("cancelled", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		fake := &fakeLibvirt{QEMUDomainAgentCommandFn: func(libvirt.Domain, string) (libvirt.OptString, error) { return nil, agentErr }}
		if err := (&VMHandler{conn: fake}).waitForVMGuestAgent(ctx, libvirt.Domain{}, "vm1", time.Minute); !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestResolveVMReadyEndpoint458(t *testing.T) {
	t.Parallel()
	h := &VMHandler{conn: &fakeLibvirt{DomainInterfaceAddressesFn: func(libvirt.Domain, uint32) ([]libvirt.DomainInterface, error) {
		return []libvirt.DomainInterface{{Addrs: []libvirt.DomainIPAddr{{Type: int32(libvirt.IPAddrTypeIpv4), Addr: "192.0.2.10"}}}}, nil
	}}}
	for _, tc := range []struct {
		config  vmRestoreVerifyConfig
		want    string
		wantErr bool
	}{
		{config: vmRestoreVerifyConfig{TCPPort: 0}, wantErr: true},
		{config: vmRestoreVerifyConfig{TCPPort: 70000}, wantErr: true},
		{config: vmRestoreVerifyConfig{TCPHost: "db.local", TCPPort: 5432}, want: "db.local:5432"},
		{config: vmRestoreVerifyConfig{TCPPort: 22}, want: "192.0.2.10:22"},
	} {
		got, err := h.resolveVMReadyEndpoint(libvirt.Domain{}, tc.config)
		if (err != nil) != tc.wantErr || got != tc.want {
			t.Errorf("resolveVMReadyEndpoint(%+v) = %q, %v", tc.config, got, err)
		}
	}
	failing := &VMHandler{conn: &fakeLibvirt{DomainInterfaceAddressesFn: func(libvirt.Domain, uint32) ([]libvirt.DomainInterface, error) {
		return nil, errors.New("rpc")
	}}}
	if _, err := failing.resolveVMReadyEndpoint(libvirt.Domain{}, vmRestoreVerifyConfig{TCPPort: 22}); err == nil {
		t.Fatal("expected detection error")
	}
}

func TestDetectVMReadyHost458(t *testing.T) {
	t.Parallel()
	ipv4 := []libvirt.DomainInterface{{Addrs: []libvirt.DomainIPAddr{{Type: int32(libvirt.IPAddrTypeIpv4), Addr: "192.0.2.10"}}}}
	ipv6 := []libvirt.DomainInterface{{Addrs: []libvirt.DomainIPAddr{{Type: int32(libvirt.IPAddrTypeIpv6), Addr: "2001:db8::1"}}}}
	agentDown := libvirt.Error{Code: uint32(libvirt.ErrAgentUnresponsive), Message: "agent down"}

	tests := []struct {
		name    string
		answer  func(source uint32) ([]libvirt.DomainInterface, error)
		want    string
		wantErr string
	}{
		{
			name:   "agent reports ipv4",
			answer: func(uint32) ([]libvirt.DomainInterface, error) { return ipv4, nil },
			want:   "192.0.2.10",
		},
		{
			name: "agent fails, lease reports ipv6",
			answer: func(source uint32) ([]libvirt.DomainInterface, error) {
				if source == uint32(libvirt.DomainInterfaceAddressesSrcAgent) {
					return nil, agentDown
				}
				return ipv6, nil
			},
			want: "2001:db8::1",
		},
		{
			name:    "agent unresponsive everywhere",
			answer:  func(uint32) ([]libvirt.DomainInterface, error) { return nil, agentDown },
			wantErr: "libvirt could not determine a guest address; configure an explicit TCP host or enable guest network reporting: agent down",
		},
		{
			name:    "generic rpc failure",
			answer:  func(uint32) ([]libvirt.DomainInterface, error) { return nil, errors.New("rpc") },
			wantErr: "detecting guest address via libvirt: rpc",
		},
		{
			name:    "no usable addresses",
			answer:  func(uint32) ([]libvirt.DomainInterface, error) { return nil, nil },
			wantErr: "libvirt did not report a reachable guest address; configure an explicit TCP host",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			fake := &fakeLibvirt{DomainInterfaceAddressesFn: func(_ libvirt.Domain, source uint32) ([]libvirt.DomainInterface, error) {
				return tt.answer(source)
			}}
			got, err := (&VMHandler{conn: fake}).detectVMReadyHost(libvirt.Domain{})
			if tt.wantErr != "" {
				if err == nil || err.Error() != tt.wantErr {
					t.Fatalf("err = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil || got != tt.want {
				t.Fatalf("got %q, %v; want %q", got, err, tt.want)
			}
		})
	}
}

func TestWaitForTCPEndpoint458(t *testing.T) {
	fastVMPolling(t, time.Minute)
	open := listenLoopback(t)
	closed := closedLoopbackPort(t)

	if err := waitForTCPEndpoint(context.Background(), net.JoinHostPort("127.0.0.1", strconv.Itoa(open)), time.Minute); err != nil {
		t.Fatalf("open endpoint: %v", err)
	}
	if err := waitForTCPEndpoint(context.Background(), net.JoinHostPort("127.0.0.1", strconv.Itoa(closed)), -time.Second); err == nil {
		t.Fatal("closed endpoint reported reachable")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := waitForTCPEndpoint(ctx, net.JoinHostPort("127.0.0.1", strconv.Itoa(closed)), time.Minute); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled wait = %v", err)
	}
}
