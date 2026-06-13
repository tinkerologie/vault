// Package discovery provides mDNS/zeroconf service announcement so that
// integrations such as the Home Assistant Vault integration can auto-discover
// a running Vault daemon on the local network. Discovery is automatically
// disabled when the daemon is bound to a loopback address (the local-only
// default), since a service reachable only on 127.0.0.1 cannot be reached by
// other hosts and must not be advertised.
package discovery

import (
	"net"
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
