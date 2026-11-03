package api

import (
	"context"
	"strings"
	"testing"
)

func TestStartRefusesHalfConfiguredTLS(t *testing.T) {
	d := testDB(t)
	srv := NewServer(d, ServerConfig{Addr: "127.0.0.1:0", TLSCert: "/tmp/only-a-cert"})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := srv.StartWithContext(ctx); err == nil || !strings.Contains(err.Error(), "--tls-key") {
		t.Fatalf("err = %v", err)
	}
}

func TestIsLoopbackAddr(t *testing.T) {
	t.Parallel()
	for addr, want := range map[string]bool{
		"127.0.0.1:24085": true, "[::1]:24085": true, "localhost:24085": true,
		":24085": false, "0.0.0.0:24085": false, "192.0.2.5:24085": false, "garbage": false,
	} {
		if got := isLoopbackAddr(addr); got != want {
			t.Errorf("isLoopbackAddr(%q) = %v", addr, got)
		}
	}
}
