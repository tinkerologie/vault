package discovery

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"strconv"

	"github.com/brutella/dnssd"
	dnssdlog "github.com/brutella/dnssd/log"
)

// serviceType is the DNS-SD service type Vault advertises. The Home Assistant
// Vault integration (ha-vault) matches this exact type in its manifest's
// `zeroconf` block to auto-discover running daemons.
const serviceType = "_vault._tcp"

// serviceDomain is the standard mDNS domain.
const serviceDomain = "local"

func init() {
	// dnssd's info logger reports malformed packets from other hosts on
	// stdout, which would flood vault.log on a busy LAN.
	dnssdlog.Info.Disable()
}

// registration is everything Vault publishes for one service instance.
type registration struct {
	instance string
	host     string
	port     int
	text     map[string]string
	adv      advertisement
}

// shutdowner stops a running advertisement.
type shutdowner interface {
	Shutdown()
}

// shutdownFunc adapts a cancel function to shutdowner.
type shutdownFunc func()

func (f shutdownFunc) Shutdown() { f() }

// mdnsResponder is the part of dnssd.Responder Vault uses.
type mdnsResponder interface {
	Add(srv dnssd.Service) (dnssd.ServiceHandle, error)
	Respond(ctx context.Context) error
}

// newResponder opens the mDNS responder. It is a package variable so tests
// can exercise dnssdRegister without binding UDP port 5353.
var newResponder = func() (mdnsResponder, error) { return dnssd.NewResponder() }

// registrar starts publishing reg and returns a handle that stops it. It is
// a package variable so tests can verify registration without multicast
// networking.
var registrar = dnssdRegister

// dnssdRegister publishes reg with an RFC 6762 responder. Unlike a
// proxy-only registration, the responder also answers A/AAAA queries for the
// Vault host name, so clients that resolve the SRV target separately can
// connect, and it probes the name so a clash is renamed instead of
// contested.
func dnssdRegister(reg registration) (shutdowner, error) {
	cfg := dnssd.Config{
		Name:   reg.instance,
		Type:   serviceType,
		Domain: serviceDomain,
		Host:   reg.host,
		Text:   reg.text,
		Port:   reg.port,
		Ifaces: reg.adv.ifaces,
	}
	if reg.adv.ip != nil {
		// Config.IPs is the only way to publish one specific address rather
		// than every address on the interface; a specific-IP bind must not
		// advertise sibling addresses the API does not listen on.
		cfg.IPs = []net.IP{reg.adv.ip} //nolint:staticcheck // see comment above
	}
	svc, err := dnssd.NewService(cfg)
	if err != nil {
		return nil, fmt.Errorf("build mDNS service: %w", err)
	}
	rp, err := newResponder()
	if err != nil {
		return nil, fmt.Errorf("open mDNS responder: %w", err)
	}
	if _, err := rp.Add(svc); err != nil {
		return nil, fmt.Errorf("add mDNS service: %w", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		if err := rp.Respond(ctx); err != nil && !errors.Is(err, context.Canceled) {
			log.Printf("discovery: mDNS responder stopped: %v", err)
		}
	}()
	return shutdownFunc(cancel), nil
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

	adv, ok, err := advertisedAddrs(ctx, s.bindHost, listInterfaces, resolveHost)
	if err != nil {
		log.Printf("discovery: mDNS advertisement skipped: %v", err)
		return
	}
	if !ok {
		log.Printf("discovery: mDNS advertisement skipped: no advertisable address for bind address %q", s.addr)
		return
	}

	server, err := registrar(registration{
		instance: s.instance,
		host:     s.hostLabel,
		port:     s.port,
		text: map[string]string{
			"version": s.version,
			"tls":     strconv.FormatBool(s.tlsEnabled),
			"path":    "/api/v1",
		},
		adv: adv,
	})
	if err != nil {
		// A failure here (e.g. no multicast-capable interface) must not be
		// fatal — discovery is a convenience, not a requirement for backups.
		log.Printf("discovery: failed to register mDNS service: %v", err)
		return
	}

	log.Printf("discovery: zeroconf advertisement enabled (%s %s on %s.local port %d, %s)",
		s.instance, serviceType, s.hostLabel, s.port, adv)

	go func() {
		<-ctx.Done()
		server.Shutdown()
		log.Printf("discovery: zeroconf advertisement stopped")
	}()
}

// String describes the advertisement for logs.
func (a advertisement) String() string {
	switch {
	case a.ip != nil && len(a.ifaces) > 0:
		return fmt.Sprintf("address %s on %v", a.ip, a.ifaces)
	case a.ip != nil:
		return fmt.Sprintf("address %s on all multicast interfaces", a.ip)
	default:
		return fmt.Sprintf("interface addresses on %v", a.ifaces)
	}
}
