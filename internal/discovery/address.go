// Package discovery provides mDNS/zeroconf service announcement so that
// integrations such as the Home Assistant Vault integration can auto-discover
// a running Vault daemon on the local network. Discovery is automatically
// disabled when the daemon is bound to a loopback address (the local-only
// default), since a service reachable only on 127.0.0.1 cannot be reached by
// other hosts and must not be advertised.
package discovery

import (
	"context"
	"fmt"
	"net"
	"slices"
	"strconv"
	"strings"
)

// shouldAdvertise reports whether mDNS advertisement should be enabled for the
// given bind address. It returns false only when the host portion is an
// explicit loopback address (e.g. 127.0.0.1, ::1) — in every other case
// (all-interfaces binds like ":24085", "0.0.0.0", "::", or a specific LAN IP)
// the daemon is reachable by other hosts and should advertise.
//
// The loopback detection mirrors internal/api/middleware.go's isLoopback so
// the "is this local-only?" decision is consistent across the daemon.
func shouldAdvertise(addr string) bool {
	host := hostFromAddr(addr)
	// Empty host means all interfaces (e.g. ":24085") — advertise.
	if host == "" {
		return true
	}
	ip := net.ParseIP(host)
	if ip == nil {
		// Not an IP literal (e.g. a hostname). Advertise unless it is the
		// textual loopback name.
		return !strings.EqualFold(host, "localhost")
	}
	// Unspecified (0.0.0.0 / ::) means all interfaces — advertise.
	if ip.IsUnspecified() {
		return true
	}
	return !ip.IsLoopback()
}

// hostFromAddr extracts the host portion of a "host:port" bind address,
// tolerating a missing port and IPv6 bracket notation.
func hostFromAddr(addr string) string {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	return strings.Trim(host, "[]")
}

// portFromAddr extracts the numeric port from a "host:port" bind address.
// Returns ok=false when no valid port is present.
func portFromAddr(addr string) (int, bool) {
	_, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		// Tolerate a bare ":port" that SplitHostPort accepts, plus the
		// no-colon case which simply has no port.
		return 0, false
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || port <= 0 || port > 65535 {
		return 0, false
	}
	return port, true
}

// maxDNSLabel is the RFC 1035 limit for a single DNS label.
const maxDNSLabel = 63

// vaultHostLabel derives the mDNS host label Vault advertises its service
// under, in the form "vault-<sanitised-hostname>". Vault must never publish
// A/AAAA records for the OS host name: on Unraid that name is owned by
// avahi-daemon, which treats a second responder as a conflict and renames the
// host (e.g. TheVault.local -> TheVault-2.local, issue #468).
//
// The result is lower case, contains only letters, digits, and hyphens, and
// fits in one DNS label. It falls back to "vault" when osHost is empty or
// sanitises to nothing.
func vaultHostLabel(osHost string) string {
	// Only the first label matters; "nas.example.lan" becomes "nas".
	osHost, _, _ = strings.Cut(osHost, ".")

	var b strings.Builder
	for _, r := range strings.ToLower(osHost) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-':
			b.WriteRune(r)
		case r == '_' || r == ' ':
			b.WriteByte('-')
		}
	}
	sanitised := strings.Trim(b.String(), "-")
	if sanitised == "" {
		return "vault"
	}

	label := "vault-" + sanitised
	if len(label) > maxDNSLabel {
		label = strings.TrimRight(label[:maxDNSLabel], "-")
	}
	// Truncating a pathological 63+ character name such as "vault-vault-…"
	// can reproduce the OS label; "vault" cannot equal a name that long.
	if strings.EqualFold(label, osHost) {
		return "vault"
	}
	return label
}

// interfaceAddrs pairs a network interface with its assigned addresses.
type interfaceAddrs struct {
	iface net.Interface
	addrs []net.Addr
}

// interfaceLister enumerates local interfaces and their addresses. Production
// uses systemInterfaces; tests substitute a fake so address selection can be
// verified without touching the host network.
type interfaceLister func() ([]interfaceAddrs, error)

// systemInterfaces lists the host's interfaces via the net package.
func systemInterfaces() ([]interfaceAddrs, error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, fmt.Errorf("list network interfaces: %w", err)
	}
	out := make([]interfaceAddrs, 0, len(ifaces))
	for _, iface := range ifaces {
		addrs, err := iface.Addrs()
		if err != nil {
			// One unreadable interface should not hide the others.
			continue
		}
		out = append(out, interfaceAddrs{iface: iface, addrs: addrs})
	}
	return out, nil
}

// hostResolver resolves a host name to IP addresses. Production uses
// net.DefaultResolver; tests substitute a fake.
type hostResolver func(ctx context.Context, host string) ([]net.IP, error)

// systemResolver resolves host through the default resolver.
func systemResolver(ctx context.Context, host string) ([]net.IP, error) {
	return net.DefaultResolver.LookupIP(ctx, "ip", host)
}

// listenerIP mirrors how net.Listen picks one address for a host-name bind:
// the first IPv4 address, otherwise the first address of any family.
func listenerIP(ips []net.IP) net.IP {
	for _, ip := range ips {
		if ip.To4() != nil {
			return ip
		}
	}
	return ips[0]
}

// advertisement is where and what Vault publishes for its bind address.
type advertisement struct {
	// ip, when set, is the only address published (specific-IP or resolved
	// host-name binds). When nil, each interface publishes its own addresses.
	ip net.IP
	// ifaces restricts publication to these interface names. Empty means
	// every multicast-capable interface.
	ifaces []string
}

// advertisedAddrs decides what to publish for the given bind host, so the
// advertised records match where the API actually listens and every
// interface only carries addresses reachable on its own link:
//
//   - Specific IP: only that IP, on the interface that owns it. When no
//     interface owns the IP it is published on every multicast interface.
//   - Host name: resolved the way the listener resolves it, then treated as
//     a specific IP. A name that resolves to loopback publishes nothing.
//   - Wildcard ("", 0.0.0.0, ::): every up, multicast-capable, non-loopback
//     interface with a routable unicast address, each publishing its own
//     addresses. Both families are included because Go serves a wildcard
//     "tcp" listen from a dual-stack socket.
//
// ok is false when there is nothing reachable to advertise.
func advertisedAddrs(ctx context.Context, bindHost string, list interfaceLister, resolve hostResolver) (adv advertisement, ok bool, err error) {
	ip := net.ParseIP(bindHost)
	if ip == nil && bindHost != "" {
		resolved, err := resolve(ctx, bindHost)
		if err != nil {
			return advertisement{}, false, fmt.Errorf("resolve bind host %q: %w", bindHost, err)
		}
		if len(resolved) == 0 {
			return advertisement{}, false, nil
		}
		ip = listenerIP(resolved)
		if ip.IsLoopback() {
			return advertisement{}, false, nil
		}
	}

	entries, err := list()
	if err != nil {
		return advertisement{}, false, err
	}

	if ip != nil && !ip.IsUnspecified() {
		for _, e := range entries {
			for _, a := range e.addrs {
				if addrIP(a).Equal(ip) {
					return advertisement{ip: ip, ifaces: []string{e.iface.Name}}, true, nil
				}
			}
		}
		return advertisement{ip: ip}, true, nil
	}

	for _, e := range entries {
		if e.iface.Flags&net.FlagUp == 0 || e.iface.Flags&net.FlagMulticast == 0 || e.iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		if slices.ContainsFunc(e.addrs, routable) {
			adv.ifaces = append(adv.ifaces, e.iface.Name)
		}
	}
	return adv, len(adv.ifaces) > 0, nil
}

// routable reports whether a is a non-loopback, non-link-local unicast
// address worth advertising an interface for.
func routable(a net.Addr) bool {
	ip := addrIP(a)
	return ip != nil && ip.IsGlobalUnicast() && !ip.IsLinkLocalUnicast()
}

// addrIP extracts the IP from the net.Addr implementations returned by
// net.Interface.Addrs.
func addrIP(a net.Addr) net.IP {
	switch v := a.(type) {
	case *net.IPNet:
		return v.IP
	case *net.IPAddr:
		return v.IP
	}
	return nil
}
