package discovery

import (
	"context"
	"errors"
	"net"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
)

type registration struct {
	instance, service, domain string
	port                      int
	host                      string
	ips, text                 []string
	ifaces                    []net.Interface
}

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
	registrar = func(instance, service, domain string, port int, host string, ips, text []string, ifaces []net.Interface) (shutdowner, error) {
		calls = append(calls, registration{instance, service, domain, port, host, ips, text, ifaces})
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
	if !slices.Equal(got.ips, []string{"192.0.2.10"}) {
		t.Errorf("ips = %v, want [192.0.2.10]", got.ips)
	}
	if len(got.ifaces) != 1 || got.ifaces[0].Name != "br0" {
		t.Errorf("ifaces = %v, want [br0]", got.ifaces)
	}
	if got.service != serviceType || got.domain != serviceDomain || got.port != 24085 || got.instance != svc.instance {
		t.Errorf("service identity changed: %+v", got)
	}
	wantTXT := []string{"version=2026.10.00", "tls=true", "path=/api/v1"}
	if !slices.Equal(got.text, wantTXT) {
		t.Errorf("text = %v, want %v", got.text, wantTXT)
	}
}

func TestStartWildcardAdvertisesEligibleAddresses(t *testing.T) {
	calls, _ := stubDiscovery(t, nil)
	New(":24085", "1.0.0", false).Start(t.Context())
	if len(*calls) != 1 {
		t.Fatalf("registrar called %d times, want 1", len(*calls))
	}
	if got := (*calls)[0]; len(got.ips) != 3 || got.ifaces != nil {
		t.Errorf("wildcard registration = ips %v ifaces %v, want 3 ips and no interface restriction", got.ips, got.ifaces)
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
