package discovery

import (
	"bytes"
	"context"
	"errors"
	"log"
	"maps"
	"net"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"

	"github.com/brutella/dnssd"
)

type fakeServer struct{ shutdown atomic.Bool }

func (f *fakeServer) Shutdown() { f.shutdown.Store(true) }

// stubDiscovery swaps the package registrar and interface lister for the
// duration of a test. Tests using it must not run in parallel.
func stubDiscovery(t *testing.T, err error) (*[]registration, *fakeServer) {
	t.Helper()
	origReg, origList, origResolve := registrar, listInterfaces, resolveHost
	t.Cleanup(func() { registrar, listInterfaces, resolveHost = origReg, origList, origResolve })

	var calls []registration
	srv := &fakeServer{}
	registrar = func(reg registration) (shutdowner, error) {
		calls = append(calls, reg)
		if err != nil {
			return nil, err
		}
		return srv, nil
	}
	listInterfaces = fakeInterfaces
	resolveHost = fakeResolver
	return &calls, srv
}

func TestStartSkipsLoopbackAndInvalid(t *testing.T) {
	for _, addr := range []string{"127.0.0.1:24085", "[::1]:24085", "localhost:24085", "noport", ":0"} {
		t.Run(addr, func(t *testing.T) {
			calls, _ := stubDiscovery(t, nil)
			New(addr, "1.0.0", false).Start(t.Context())
			if len(*calls) != 0 {
				t.Fatalf("registrar called %d times for %q, want 0", len(*calls), addr)
			}
		})
	}
}

func TestStartSpecificBindUsesVaultHostAndBindIP(t *testing.T) {
	calls, _ := stubDiscovery(t, nil)
	svc := New("192.0.2.10:24085", "2026.10.00", true)
	svc.Start(t.Context())

	if len(*calls) != 1 {
		t.Fatalf("registrar called %d times, want 1", len(*calls))
	}
	got := (*calls)[0]
	if got.host != svc.hostLabel || !strings.HasPrefix(got.host, "vault") {
		t.Errorf("host = %q, want Vault host label %q", got.host, svc.hostLabel)
	}
	if !got.adv.ip.Equal(net.ParseIP("192.0.2.10")) {
		t.Errorf("ip = %v, want 192.0.2.10", got.adv.ip)
	}
	if !slices.Equal(got.adv.ifaces, []string{"br0"}) {
		t.Errorf("ifaces = %v, want [br0]", got.adv.ifaces)
	}
	if got.port != 24085 || got.instance != svc.instance {
		t.Errorf("service identity changed: %+v", got)
	}
	wantTXT := map[string]string{"version": "2026.10.00", "tls": "true", "path": "/api/v1"}
	if !maps.Equal(got.text, wantTXT) {
		t.Errorf("text = %v, want %v", got.text, wantTXT)
	}
}

func TestStartWildcardAdvertisesPerInterface(t *testing.T) {
	calls, _ := stubDiscovery(t, nil)
	New(":24085", "1.0.0", false).Start(t.Context())
	if len(*calls) != 1 {
		t.Fatalf("registrar called %d times, want 1", len(*calls))
	}
	got := (*calls)[0].adv
	if got.ip != nil || !slices.Equal(got.ifaces, []string{"br0", "tailscale1"}) {
		t.Errorf("wildcard advertisement = %+v, want per-interface addresses on [br0 tailscale1]", got)
	}
}

func TestStartRegistrarErrorIsNonFatal(t *testing.T) {
	calls, _ := stubDiscovery(t, errors.New("no multicast"))
	New("192.0.2.10:24085", "1.0.0", false).Start(t.Context())
	if len(*calls) != 1 {
		t.Fatalf("registrar called %d times, want 1", len(*calls))
	}
}

func TestStartSkipsWhenNoAddressOrListerFails(t *testing.T) {
	t.Run("no eligible address", func(t *testing.T) {
		calls, _ := stubDiscovery(t, nil)
		listInterfaces = func() ([]interfaceAddrs, error) { return nil, nil }
		New(":24085", "1.0.0", false).Start(t.Context())
		if len(*calls) != 0 {
			t.Fatalf("registrar called %d times, want 0", len(*calls))
		}
	})
	t.Run("lister error", func(t *testing.T) {
		calls, _ := stubDiscovery(t, nil)
		listInterfaces = func() ([]interfaceAddrs, error) { return nil, errors.New("boom") }
		New(":24085", "1.0.0", false).Start(t.Context())
		if len(*calls) != 0 {
			t.Fatalf("registrar called %d times, want 0", len(*calls))
		}
	})
}

func TestStartCancelledContextSkipsRegistration(t *testing.T) {
	calls, _ := stubDiscovery(t, nil)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	New("192.0.2.10:24085", "1.0.0", false).Start(ctx)
	if len(*calls) != 0 {
		t.Fatalf("registrar called %d times, want 0", len(*calls))
	}
}

func TestStartShutsDownOnCancel(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		_, srv := stubDiscovery(t, nil)
		ctx, cancel := context.WithCancel(t.Context())
		New("192.0.2.10:24085", "1.0.0", false).Start(ctx)
		synctest.Wait()
		if srv.shutdown.Load() {
			t.Fatal("Shutdown called before context cancellation")
		}
		cancel()
		synctest.Wait()
		if !srv.shutdown.Load() {
			t.Fatal("Shutdown not called after context cancellation")
		}
	})
}

func TestAdvertisementString(t *testing.T) {
	ip := net.ParseIP("192.0.2.10")
	cases := []struct {
		adv  advertisement
		want string
	}{
		{advertisement{ip: ip, ifaces: []string{"br0"}}, "address 192.0.2.10 on [br0]"},
		{advertisement{ifaces: []string{"br0", "eth1"}}, "interface addresses on [br0 eth1]"},
	}
	for _, tc := range cases {
		if got := tc.adv.String(); got != tc.want {
			t.Errorf("String() = %q, want %q", got, tc.want)
		}
	}
}

func TestShutdownFunc(t *testing.T) {
	called := false
	shutdownFunc(func() { called = true }).Shutdown()
	if !called {
		t.Fatal("shutdownFunc did not call its function")
	}
}

// TestDNSSDRegisterRejectsInvalidService exercises the production registrar's
// validation path without opening sockets: dnssd rejects a zero port before
// creating a responder.
func TestDNSSDRegisterRejectsInvalidService(t *testing.T) {
	if _, err := dnssdRegister(registration{instance: "Vault (test)", host: "vault-test"}); err == nil {
		t.Fatal("dnssdRegister with port 0 succeeded, want error")
	}
}

type fakeResponder struct {
	addErr     error
	respondErr error
	added      []dnssd.Service
	responding chan struct{}
}

func (f *fakeResponder) Add(srv dnssd.Service) (dnssd.ServiceHandle, error) {
	f.added = append(f.added, srv)
	return nil, f.addErr
}

func (f *fakeResponder) Respond(ctx context.Context) error {
	close(f.responding)
	if f.respondErr != nil {
		return f.respondErr
	}
	<-ctx.Done()
	return ctx.Err()
}

func stubResponder(t *testing.T, fake *fakeResponder, openErr error) {
	t.Helper()
	orig := newResponder
	t.Cleanup(func() { newResponder = orig })
	newResponder = func() (mdnsResponder, error) {
		if openErr != nil {
			return nil, openErr
		}
		return fake, nil
	}
}

func TestDNSSDRegisterPublishesAndStops(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		fake := &fakeResponder{responding: make(chan struct{})}
		stubResponder(t, fake, nil)

		stop, err := dnssdRegister(registration{
			instance: "Vault (Cube)",
			host:     "vault-cube",
			port:     24085,
			text:     map[string]string{"path": "/api/v1"},
			adv:      advertisement{ip: net.ParseIP("192.0.2.10"), ifaces: []string{"br0"}},
		})
		if err != nil {
			t.Fatalf("dnssdRegister: %v", err)
		}
		<-fake.responding
		if len(fake.added) != 1 {
			t.Fatalf("added %d services, want 1", len(fake.added))
		}
		svc := fake.added[0]
		if svc.Host != "vault-cube" || svc.Type != serviceType || svc.Port != 24085 {
			t.Errorf("service = %+v, want host vault-cube, type %s, port 24085", svc, serviceType)
		}
		if len(svc.IPs) != 1 || !svc.IPs[0].Equal(net.ParseIP("192.0.2.10")) || !slices.Equal(svc.Ifaces, []string{"br0"}) { //nolint:staticcheck // asserting the deprecated field we set
			t.Errorf("service scope = IPs %v Ifaces %v, want [192.0.2.10] on [br0]", svc.IPs, svc.Ifaces) //nolint:staticcheck // as above
		}
		stop.Shutdown()
		synctest.Wait()
	})
}

func TestDNSSDRegisterWildcardPublishesInterfaceAddresses(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		fake := &fakeResponder{responding: make(chan struct{})}
		stubResponder(t, fake, nil)
		stop, err := dnssdRegister(registration{instance: "Vault", host: "vault", port: 24085, adv: advertisement{ifaces: []string{"br0"}}})
		if err != nil {
			t.Fatalf("dnssdRegister: %v", err)
		}
		<-fake.responding
		if svc := fake.added[0]; len(svc.IPs) != 0 { //nolint:staticcheck // asserting the deprecated field stays unset
			t.Errorf("wildcard service IPs = %v, want none (per-interface addresses)", svc.IPs) //nolint:staticcheck // as above
		}
		stop.Shutdown()
		synctest.Wait()
	})
}

func TestDNSSDRegisterErrors(t *testing.T) {
	reg := registration{instance: "Vault", host: "vault", port: 24085}
	t.Run("responder open fails", func(t *testing.T) {
		stubResponder(t, nil, errors.New("bind 5353"))
		if _, err := dnssdRegister(reg); err == nil || !strings.Contains(err.Error(), "open mDNS responder") {
			t.Fatalf("err = %v, want open error", err)
		}
	})
	t.Run("add fails", func(t *testing.T) {
		stubResponder(t, &fakeResponder{addErr: errors.New("dup")}, nil)
		if _, err := dnssdRegister(reg); err == nil || !strings.Contains(err.Error(), "add mDNS service") {
			t.Fatalf("err = %v, want add error", err)
		}
	})
	t.Run("respond fails asynchronously", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			var buf bytes.Buffer
			orig := log.Writer()
			log.SetOutput(&buf)
			t.Cleanup(func() { log.SetOutput(orig) })

			fake := &fakeResponder{respondErr: errors.New("probe failed"), responding: make(chan struct{})}
			stubResponder(t, fake, nil)
			stop, err := dnssdRegister(reg)
			if err != nil {
				t.Fatalf("dnssdRegister: %v", err)
			}
			synctest.Wait()
			stop.Shutdown()
			if !strings.Contains(buf.String(), "mDNS responder stopped: probe failed") {
				t.Errorf("log = %q, want responder failure", buf.String())
			}
		})
	})
}
