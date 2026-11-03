package handlers

import (
	"os"
	"testing"

	"github.com/tinkerologie/vault/internal/replication"
)

// Tests here use httptest peers, which listen on loopback.
func TestMain(m *testing.M) {
	_ = os.Setenv(replication.AllowLoopbackEnv, "1")
	os.Exit(m.Run())
}
