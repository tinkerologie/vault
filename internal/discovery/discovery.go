package discovery

import (
	"context"
	"fmt"
	"log"
	"net"
	"os"

	"github.com/grandcat/zeroconf"
)

// serviceType is the DNS-SD service type Vault advertises. The Home Assistant
// Vault integration (ha-vault) matches this exact type in its manifest's
// `zeroconf` block to auto-discover running daemons.
const serviceType = "_vault._tcp"

// serviceDomain is the standard mDNS domain.
const serviceDomain = "local."

// shutdowner is the part of *zeroconf.Server that Service depends on.
type shutdowner interface {
	Shutdown()
}

// registrar publishes a DNS-SD service under an explicit host name and
// address set. It is a package variable so tests can verify registration
// without multicast networking.
var registrar = func(instance, service, domain string, port int, host string, ips, text []string, ifaces []net.Interface) (shutdowner, error) {
	return zeroconf.RegisterProxy(instance, service, domain, port, host, ips, text, ifaces)
}

// listInterfaces enumerates host interfaces for address selection. It is a
// package variable so tests can supply a deterministic interface set.
var listInterfaces interfaceLister = systemInterfaces

// resolveHost resolves host-name binds. It is a package variable so tests can
// avoid real DNS lookups.
var resolveHost hostResolver = systemResolver

// Service manages an mDNS/zeroconf advertisement for the Vault daemon. It
// follows the same context-driven lifecycle as the other background services
// (e.g. runner.Heartbeat): Start launches a goroutine that tears the
// advertisement down when ctx is cancelled.
type Service struct {
	instance   string
	hostLabel  string
	bindHost   string
	port       int
	version    string
	tlsEnabled bool
	advertise  bool
	addr       string
}

// New constructs a discovery Service from the daemon's bind address. version
// and tlsEnabled are published as TXT records so discovering clients can show
// the daemon version and know whether to connect over HTTPS. Advertisement is
// disabled automatically when addr is a loopback bind or has no usable port.
func New(addr, version string, tlsEnabled bool) *Service {
	port, hasPort := portFromAddr(addr)
	osHost, _ := os.Hostname()
	instance := "Vault"
	if osHost != "" {
		instance = fmt.Sprintf("Vault (%s)", osHost)
	}
	return &Service{
		instance:   instance,
		hostLabel:  vaultHostLabel(osHost),
		bindHost:   hostFromAddr(addr),
		port:       port,
		version:    version,
		tlsEnabled: tlsEnabled,
		advertise:  hasPort && shouldAdvertise(addr),
		addr:       addr,
	}
}

// Start begins mDNS advertisement if the daemon is reachable off-host. When
// bound to loopback (or lacking a port) it logs the reason and returns without
// registering. The registration is shut down when ctx is cancelled, so callers
// need no explicit cleanup — pass the same signal context used by the daemon.
//
// The service is published under Vault's own host name (vault-<host>.local)
// rather than the OS host name, so it never contends with avahi-daemon for
// the host's A/AAAA records (#468). The instance name and TXT records are
// unchanged, keeping existing Home Assistant discoveries stable.
func (s *Service) Start(ctx context.Context) {
	if !s.advertise {
		log.Printf("discovery: mDNS advertisement disabled for bind address %q (loopback or no port) — Vault is local-only", s.addr)
		return
	}
	if ctx.Err() != nil {
		return
	}

	ips, ifaces, err := advertisedAddrs(ctx, s.bindHost, listInterfaces, resolveHost)
	if err != nil {
		log.Printf("discovery: mDNS advertisement skipped: %v", err)
		return
	}
	if len(ips) == 0 {
		log.Printf("discovery: mDNS advertisement skipped: no advertisable address for bind address %q", s.addr)
		return
	}

	txt := []string{
		"version=" + s.version,
		fmt.Sprintf("tls=%t", s.tlsEnabled),
		"path=/api/v1",
	}

	server, err := registrar(s.instance, serviceType, serviceDomain, s.port, s.hostLabel, ips, txt, ifaces)
	if err != nil {
		// A failure here (e.g. no multicast-capable interface) must not be
		// fatal — discovery is a convenience, not a requirement for backups.
		log.Printf("discovery: failed to register mDNS service: %v", err)
		return
	}

	log.Printf("discovery: zeroconf advertisement enabled (%s %s on %s.local port %d, addresses %v)",
		s.instance, serviceType, s.hostLabel, s.port, ips)

	go func() {
		<-ctx.Done()
		server.Shutdown()
		log.Printf("discovery: zeroconf advertisement stopped")
	}()
}
