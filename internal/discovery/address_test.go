package discovery

import (
	"context"
	"errors"
	"net"
	"slices"
	"strings"
	"testing"
)

func TestShouldAdvertise(t *testing.T) {
	cases := []struct {
		name string
		addr string
		want bool
	}{
		{"all interfaces empty host", ":24085", true},
		{"unspecified ipv4", "0.0.0.0:24085", true},
		{"unspecified ipv6", "[::]:24085", true},
		{"specific lan ip", "192.168.20.21:24085", true},
		{"loopback ipv4", "127.0.0.1:24085", false},
		{"loopback ipv4 range", "127.0.0.5:24085", false},
		{"loopback ipv6", "[::1]:24085", false},
		{"localhost name", "localhost:24085", false},
		{"hostname", "myserver:24085", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := shouldAdvertise(tc.addr); got != tc.want {
				t.Errorf("shouldAdvertise(%q) = %v, want %v", tc.addr, got, tc.want)
			}
		})
	}
}

func TestPortFromAddr(t *testing.T) {
	cases := []struct {
		addr   string
		want   int
		wantOK bool
	}{
		{":24085", 24085, true},
		{"0.0.0.0:24085", 24085, true},
		{"[::1]:8080", 8080, true},
		{"192.168.1.1:80", 80, true},
		{"noport", 0, false},
		{":0", 0, false},
		{":99999", 0, false},
		{":abc", 0, false},
	}
	for _, tc := range cases {
		got, ok := portFromAddr(tc.addr)
		if got != tc.want || ok != tc.wantOK {
			t.Errorf("portFromAddr(%q) = (%d, %v), want (%d, %v)", tc.addr, got, ok, tc.want, tc.wantOK)
		}
	}
}

func TestNewDisablesOnLoopback(t *testing.T) {
	cases := []struct {
		name          string
		bindAddr      string
		wantAdvertise bool
	}{
		{"loopback bind", "127.0.0.1:24085", false},
		{"all-interfaces bind", ":24085", true},
		{"missing port", "noport", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc := New(tc.bindAddr, "1.0.0", false)
			if svc.advertise != tc.wantAdvertise {
				t.Errorf("New(%q).advertise = %v, want %v", tc.bindAddr, svc.advertise, tc.wantAdvertise)
			}
		})
	}
}

func TestVaultHostLabel(t *testing.T) {
	long := strings.Repeat("a", 80)
	cases := []struct {
		name   string
		osHost string
		want   string
	}{
		{"unraid mixed case", "TheVault", "vault-thevault"},
		{"fqdn keeps first label", "nas.example.lan", "vault-nas"},
		{"underscores and spaces become hyphens", "my_nas box", "vault-my-nas-box"},
		{"strips invalid runes", "nås!#", "vault-ns"},
		{"trims edge hyphens", "-tower-", "vault-tower"},
		{"empty", "", "vault"},
		{"sanitises to nothing", "!!!", "vault"},
		{"long name truncated to one label", long, "vault-" + long[:maxDNSLabel-len("vault-")]},
		{"truncation never reproduces os label", strings.Repeat("vault-", 10) + "vau", "vault"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := vaultHostLabel(tc.osHost)
			if got != tc.want {
				t.Errorf("vaultHostLabel(%q) = %q, want %q", tc.osHost, got, tc.want)
			}
			if len(got) > maxDNSLabel {
				t.Errorf("vaultHostLabel(%q) length %d exceeds %d", tc.osHost, len(got), maxDNSLabel)
			}
			if osLabel, _, _ := strings.Cut(tc.osHost, "."); strings.EqualFold(got, osLabel) {
				t.Errorf("vaultHostLabel(%q) = %q collides with the OS host label", tc.osHost, got)
			}
		})
	}
}

func ipNet(s string) *net.IPNet {
	ip, n, err := net.ParseCIDR(s)
	if err != nil {
		panic(err)
	}
	n.IP = ip
	return n
}

// fakeInterfaces returns a deterministic interface set covering loopback,
// LAN, VPN, a down interface, and a non-multicast interface.
func fakeInterfaces() ([]interfaceAddrs, error) {
	up := net.FlagUp | net.FlagMulticast
	return []interfaceAddrs{
		{iface: net.Interface{Index: 1, Name: "lo", Flags: net.FlagUp | net.FlagLoopback}, addrs: []net.Addr{ipNet("127.0.0.1/8"), ipNet("::1/128")}},
		{iface: net.Interface{Index: 2, Name: "br0", Flags: up}, addrs: []net.Addr{ipNet("192.0.2.10/24"), ipNet("fe80::1/64"), ipNet("2001:db8::10/64")}},
		{iface: net.Interface{Index: 3, Name: "tailscale1", Flags: up}, addrs: []net.Addr{&net.IPAddr{IP: net.ParseIP("100.64.0.5")}}},
		{iface: net.Interface{Index: 4, Name: "eth9", Flags: net.FlagMulticast}, addrs: []net.Addr{ipNet("198.51.100.9/24")}},
		{iface: net.Interface{Index: 5, Name: "tun0", Flags: net.FlagUp}, addrs: []net.Addr{ipNet("203.0.113.7/24")}},
		{iface: net.Interface{Index: 6, Name: "odd", Flags: up}, addrs: []net.Addr{&net.UnixAddr{Name: "sock"}}},
	}, nil
}

func TestAdvertisedAddrs(t *testing.T) {
	wildcard := []string{"192.0.2.10", "2001:db8::10", "100.64.0.5"}
	cases := []struct {
		name       string
		bindHost   string
		wantIPs    []string
		wantIfaces []string
	}{
		{"specific lan ip", "192.0.2.10", []string{"192.0.2.10"}, []string{"br0"}},
		{"specific ipv6", "2001:db8::10", []string{"2001:db8::10"}, []string{"br0"}},
		{"specific ip on vpn", "100.64.0.5", []string{"100.64.0.5"}, []string{"tailscale1"}},
		{"ip no interface owns", "192.0.2.99", []string{"192.0.2.99"}, nil},
		{"wildcard empty", "", wildcard, nil},
		{"wildcard ipv4", "0.0.0.0", wildcard, nil},
		{"wildcard ipv6", "::", wildcard, nil},
		{"hostname resolves to lan ip", "tower", []string{"192.0.2.10"}, []string{"br0"}},
		{"hostname prefers ipv4 like the listener", "dual", []string{"192.0.2.10"}, []string{"br0"}},
		{"hostname with only ipv6", "v6only", []string{"2001:db8::10"}, []string{"br0"}},
		{"hostname resolves to loopback", "loopy", nil, nil},
		{"hostname resolves to nothing", "empty", nil, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ips, ifaces, err := advertisedAddrs(t.Context(), tc.bindHost, fakeInterfaces, fakeResolver)
			if err != nil {
				t.Fatalf("advertisedAddrs: %v", err)
			}
			if !slices.Equal(ips, tc.wantIPs) {
				t.Errorf("ips = %v, want %v", ips, tc.wantIPs)
			}
			var names []string
			for _, i := range ifaces {
				names = append(names, i.Name)
			}
			if !slices.Equal(names, tc.wantIfaces) {
				t.Errorf("ifaces = %v, want %v", names, tc.wantIfaces)
			}
		})
	}
}

func TestAdvertisedAddrsListerError(t *testing.T) {
	boom := errors.New("boom")
	_, _, err := advertisedAddrs(t.Context(), "", func() ([]interfaceAddrs, error) { return nil, boom }, fakeResolver)
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want %v", err, boom)
	}
}

func TestAdvertisedAddrsResolverError(t *testing.T) {
	_, _, err := advertisedAddrs(t.Context(), "nxdomain", fakeInterfaces, fakeResolver)
	if !errors.Is(err, errNoSuchHost) {
		t.Fatalf("err = %v, want %v", err, errNoSuchHost)
	}
}

var errNoSuchHost = errors.New("no such host")

// fakeResolver answers host-name lookups without DNS.
func fakeResolver(_ context.Context, host string) ([]net.IP, error) {
	switch host {
	case "tower":
		return []net.IP{net.ParseIP("192.0.2.10")}, nil
	case "dual":
		return []net.IP{net.ParseIP("2001:db8::10"), net.ParseIP("192.0.2.10")}, nil
	case "v6only":
		return []net.IP{net.ParseIP("2001:db8::10")}, nil
	case "loopy":
		return []net.IP{net.ParseIP("127.0.1.1")}, nil
	case "empty":
		return nil, nil
	}
	return nil, errNoSuchHost
}

func TestSystemResolver(t *testing.T) {
	ips, err := systemResolver(t.Context(), "localhost")
	if err != nil {
		t.Skipf("resolver unavailable: %v", err)
	}
	if len(ips) == 0 {
		t.Fatal("localhost resolved to no addresses")
	}
}

func TestAdvertisedAddrsNoneEligible(t *testing.T) {
	onlyLoopback := func() ([]interfaceAddrs, error) {
		return []interfaceAddrs{{iface: net.Interface{Name: "lo", Flags: net.FlagUp | net.FlagLoopback | net.FlagMulticast}, addrs: []net.Addr{ipNet("127.0.0.1/8")}}}, nil
	}
	ips, ifaces, err := advertisedAddrs(t.Context(), "", onlyLoopback, fakeResolver)
	if err != nil || len(ips) != 0 || ifaces != nil {
		t.Fatalf("advertisedAddrs = (%v, %v, %v), want empty", ips, ifaces, err)
	}
}

func TestSystemInterfaces(t *testing.T) {
	entries, err := systemInterfaces()
	if err != nil {
		t.Skipf("interfaces unavailable: %v", err)
	}
	for _, e := range entries {
		if e.iface.Name == "" {
			t.Errorf("interface with empty name: %+v", e.iface)
		}
	}
}
