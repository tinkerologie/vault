package replication

import (
	"os"
	"testing"
)

// Tests here use httptest peers, which listen on loopback.
func TestMain(m *testing.M) {
	_ = os.Setenv(AllowLoopbackEnv, "1")
	os.Exit(m.Run())
}
