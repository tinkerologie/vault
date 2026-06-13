package discovery

import "testing"

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
