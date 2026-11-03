package replication

import (
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
)

func TestCheckPeerAddress(t *testing.T) {
	t.Setenv(AllowLoopbackEnv, "")
	allowed := []string{"10.0.0.5", "172.16.1.1", "192.168.1.20", "100.64.0.7", "fd12::1", "203.0.113.9", "2001:db8::1", "::ffff:192.168.1.20"}
	for _, a := range allowed {
		if err := checkPeerAddress(netip.MustParseAddr(a)); err != nil {
			t.Errorf("%s: %v", a, err)
		}
	}
	blocked := []string{
		"127.0.0.1", "127.8.8.8", "::1", "::ffff:127.0.0.1", "0.0.0.0", "0.1.2.3", "::",
		"169.254.169.254", "::ffff:169.254.169.254", "64:ff9b::a9fe:a9fe", "169.254.1.1", "fe80::1",
		"224.0.0.1", "ff02::1", "ff01::1", "255.255.255.255",
		"100.100.100.200", "168.63.129.16", "192.0.0.192", "fd00:ec2::254", "fd20:ce::254", "fd00:ec2::254%eth0", "fd20:ce::254%eth0",
	}
	for _, a := range blocked {
		if err := checkPeerAddress(netip.MustParseAddr(a)); err == nil {
			t.Errorf("%s: allowed, want blocked", a)
		}
	}
}

func TestCheckPeerAddress_LoopbackOptIn(t *testing.T) {
	t.Setenv(AllowLoopbackEnv, "1")
	if err := checkPeerAddress(netip.MustParseAddr("127.0.0.1")); err != nil {
		t.Errorf("opt-in ignored: %v", err)
	}
	if err := checkPeerAddress(netip.MustParseAddr("169.254.169.254")); err == nil {
		t.Error("opt-in must not unblock metadata")
	}
}

func TestCheckPeerDial_BadAddress(t *testing.T) {
	if err := checkPeerDial("tcp", "not an address", nil); err == nil {
		t.Fatal("want parse error")
	}
}

// The check runs at connect time, so a hostname cannot get past it either.
func TestClientRefusesLoopbackAtDial(t *testing.T) {
	var hit bool
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hit = true }))
	defer srv.Close()
	t.Setenv(AllowLoopbackEnv, "")
	port := srv.URL[strings.LastIndex(srv.URL, ":"):]
	for _, base := range []string{srv.URL, "http://localhost" + port} {
		c, err := NewClient(base)
		if err != nil {
			t.Fatal(err)
		}
		_, err = c.TestConnection()
		if err == nil || !strings.Contains(err.Error(), "not allowed") {
			t.Errorf("%s: err = %v", base, err)
		}
	}
	if hit {
		t.Error("the loopback service was reached")
	}
}

func TestClientDoesNotReflectResponseBodies(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte("internal-service-secret"))
	}))
	defer srv.Close()
	c, err := NewClient(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.TestConnection()
	if err == nil || strings.Contains(err.Error(), "internal-service-secret") || !strings.Contains(err.Error(), "403") {
		t.Errorf("TestConnection err = %v", err)
	}
	_, err = c.DownloadFile(1, "x")
	if err == nil || strings.Contains(err.Error(), "internal-service-secret") || !strings.Contains(err.Error(), "403") {
		t.Errorf("DownloadFile err = %v", err)
	}
}
