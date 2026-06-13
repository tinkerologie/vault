package discovery

import (
	"context"
	"fmt"
	"log"
	"os"

	"github.com/grandcat/zeroconf"
)

// serviceType is the DNS-SD service type Vault advertises. The Home Assistant
// Vault integration (ha-vault) matches this exact type in its manifest's
// `zeroconf` block to auto-discover running daemons.
const serviceType = "_vault._tcp"

// serviceDomain is the standard mDNS domain.
const serviceDomain = "local."

// Service manages an mDNS/zeroconf advertisement for the Vault daemon. It
// follows the same context-driven lifecycle as the other background services
// (e.g. runner.Heartbeat): Start launches a goroutine that tears the
// advertisement down when ctx is cancelled.
type Service struct {
	instance   string
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
	instance := "Vault"
	if host, err := os.Hostname(); err == nil && host != "" {
		instance = fmt.Sprintf("Vault (%s)", host)
	}
	return &Service{
		instance:   instance,
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
func (s *Service) Start(ctx context.Context) {
	if !s.advertise {
		log.Printf("discovery: mDNS advertisement disabled for bind address %q (loopback or no port) — Vault is local-only", s.addr)
		return
	}

	txt := []string{
		"version=" + s.version,
		fmt.Sprintf("tls=%t", s.tlsEnabled),
		"path=/api/v1",
	}

	server, err := zeroconf.Register(s.instance, serviceType, serviceDomain, s.port, txt, nil)
	if err != nil {
		// A failure here (e.g. no multicast-capable interface) must not be
		// fatal — discovery is a convenience, not a requirement for backups.
		log.Printf("discovery: failed to register mDNS service: %v", err)
		return
	}

	log.Printf("discovery: zeroconf advertisement enabled (%s %s on port %d)", s.instance, serviceType, s.port)

	go func() {
		<-ctx.Done()
		server.Shutdown()
		log.Printf("discovery: zeroconf advertisement stopped")
	}()
}
