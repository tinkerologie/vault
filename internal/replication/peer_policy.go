package replication

import (
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"os"
	"syscall"
	"time"
)

// AllowLoopbackEnv, set to 1, lets the replication client reach loopback
// addresses, for a peer behind a local tunnel such as ssh -L.
const AllowLoopbackEnv = "VAULT_REPLICATION_ALLOW_LOOPBACK"

// Addresses that serve instance credentials but sit outside the link-local
// ranges. 100.100.100.200 is inside CGNAT space, which stays allowed for
// overlay networks such as Tailscale.
var metadataAddrs = map[netip.Addr]bool{
	netip.MustParseAddr("100.100.100.200"): true, // Alibaba Cloud
	netip.MustParseAddr("168.63.129.16"):   true, // Azure
	netip.MustParseAddr("192.0.0.192"):     true, // Oracle Cloud
	netip.MustParseAddr("fd00:ec2::254"):   true, // AWS IPv6
	netip.MustParseAddr("fd20:ce::254"):    true, // Google Cloud IPv6
}

var (
	thisNetwork = netip.MustParsePrefix("0.0.0.0/8")
	nat64Prefix = netip.MustParsePrefix("64:ff9b::/96")
)

// checkPeerAddress is the outbound policy for replication peers. Peers normally
// sit on private LANs, overlays or the internet, so RFC 1918, ULA, CGNAT and
// public addresses pass. Addresses that reach this host or cloud metadata do
// not.
func checkPeerAddress(addr netip.Addr) error {
	addr = addr.WithZone("").Unmap()
	if nat64Prefix.Contains(addr) {
		b := addr.As16()
		addr = netip.AddrFrom4([4]byte(b[12:]))
	}
	switch {
	case addr.IsLoopback():
		if os.Getenv(AllowLoopbackEnv) == "1" {
			return nil
		}
		return fmt.Errorf("loopback address %s is not allowed as a replication peer", addr)
	case addr.IsUnspecified(), thisNetwork.Contains(addr):
		return fmt.Errorf("address %s is not allowed as a replication peer", addr)
	case addr.IsLinkLocalUnicast(), addr.IsLinkLocalMulticast(), addr.IsInterfaceLocalMulticast():
		return fmt.Errorf("link-local address %s is not allowed as a replication peer", addr)
	case addr.IsMulticast(), addr == netip.AddrFrom4([4]byte{255, 255, 255, 255}):
		return fmt.Errorf("multicast or broadcast address %s is not allowed as a replication peer", addr)
	case metadataAddrs[addr]:
		return fmt.Errorf("cloud metadata address %s is not allowed as a replication peer", addr)
	}
	return nil
}

// checkPeerDial applies the policy to the address about to be connected, after
// DNS resolution, so a name that rebinds between lookups cannot bypass it.
func checkPeerDial(_, address string, _ syscall.RawConn) error {
	ap, err := netip.ParseAddrPort(address)
	if err != nil {
		return fmt.Errorf("parse peer address %q: %w", address, err)
	}
	return checkPeerAddress(ap.Addr())
}

// peerTransport is shared by every client so connections are reused. It ignores
// proxy settings because a proxy would make the dial check see only the proxy.
var peerTransport = func() *http.Transport {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.Proxy = nil
	t.DialContext = (&net.Dialer{
		Timeout:   30 * time.Second,
		KeepAlive: 30 * time.Second,
		Control:   checkPeerDial,
	}).DialContext
	return t
}()
