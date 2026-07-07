package engine

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/moby/moby/api/types/container"
)

func TestBackupableMount(t *testing.T) {
	t.Parallel()
	tests := []struct {
		typ  string
		want bool
	}{
		{"bind", true},
		{"volume", true},
		{"tmpfs", false},
		{"npipe", false},
		{"cluster", false},
		{"", false},
	}
	for _, tc := range tests {
		t.Run(tc.typ, func(t *testing.T) {
			if got := backupableMount(tc.typ); got != tc.want {
				t.Errorf("backupableMount(%q) = %v, want %v", tc.typ, got, tc.want)
			}
		})
	}
}

func TestRestorableVolume(t *testing.T) {
	t.Parallel()
	anon := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	tests := []struct {
		name   string
		typ    string
		volume string
		want   bool
	}{
		{"bind is always restorable", "bind", "", true},
		{"named volume", "volume", "postgres-data", true},
		{"anonymous 64-hex volume", "volume", anon, false},
		{"volume with no name", "volume", "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := restorableVolume(tc.typ, tc.volume); got != tc.want {
				t.Errorf("restorableVolume(%q,%q) = %v, want %v", tc.typ, tc.volume, got, tc.want)
			}
		})
	}
}

func TestSparseInfo(t *testing.T) {
	t.Parallel()

	// A small regular file is never flagged.
	small := filepath.Join(t.TempDir(), "small")
	if err := os.WriteFile(small, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(small); err != nil {
		t.Fatal(err)
	} else if sparse, _ := sparseInfo(fi); sparse {
		t.Error("small file flagged as sparse")
	}

	// A large file with almost no physical blocks (created via truncate) is a
	// sparse file and must be flagged. Skipped on filesystems that don't report
	// a hole (some CI tmpfs), which would make the test meaningless.
	sparsePath := filepath.Join(t.TempDir(), "sparse")
	f, err := os.Create(sparsePath)
	if err != nil {
		t.Fatal(err)
	}
	const logical = int64(4) << 30 // 4 GiB logical, ~0 physical
	if err := f.Truncate(logical); err != nil {
		_ = f.Close()
		t.Skipf("truncate unsupported: %v", err)
	}
	_ = f.Close()
	fi, err := os.Stat(sparsePath)
	if err != nil {
		t.Fatal(err)
	}
	sparse, physical := sparseInfo(fi)
	if physical >= logical/sparseWarnRatio {
		t.Skipf("filesystem materialised the hole (physical=%d) — sparse test not meaningful here", physical)
	}
	if !sparse {
		t.Errorf("sparseInfo() did not flag a %d-byte file with %d physical bytes", logical, physical)
	}
}

func TestNetworkDependency(t *testing.T) {
	t.Parallel()
	mk := func(mode string) container.InspectResponse {
		return container.InspectResponse{
			HostConfig: &container.HostConfig{NetworkMode: container.NetworkMode(mode)},
		}
	}
	if got := networkDependency(mk("container:gluetun")); got != "gluetun" {
		t.Errorf("networkDependency(container:gluetun) = %q, want gluetun", got)
	}
	if got := networkDependency(mk("bridge")); got != "" {
		t.Errorf("networkDependency(bridge) = %q, want empty", got)
	}
	if got := networkDependency(container.InspectResponse{}); got != "" {
		t.Errorf("networkDependency(nil hostconfig) = %q, want empty", got)
	}
}

func TestCompressionLevelSpec(t *testing.T) {
	t.Parallel()

	if got := JoinCompression("zstd", CompressionLevelBest); got != "zstd:best" {
		t.Errorf("JoinCompression zstd best = %q, want zstd:best", got)
	}
	// Default level and empty inputs collapse to the bare algo (byte-compatible
	// with existing stored values).
	for _, tc := range []struct{ algo, level, want string }{
		{"zstd", "", "zstd"},
		{"zstd", CompressionLevelDefault, "zstd"},
		{"none", "best", "none"},
		{"", "best", ""},
	} {
		t.Run(tc.algo+"/"+tc.level, func(t *testing.T) {
			if got := JoinCompression(tc.algo, tc.level); got != tc.want {
				t.Errorf("JoinCompression(%q,%q) = %q, want %q", tc.algo, tc.level, got, tc.want)
			}
		})
	}

	algo, level := splitCompression("gzip:fastest")
	if algo != "gzip" || level != "fastest" {
		t.Errorf("splitCompression(gzip:fastest) = %q,%q", algo, level)
	}
	if algo, level := splitCompression("zstd"); algo != "zstd" || level != "" {
		t.Errorf("splitCompression(zstd) = %q,%q", algo, level)
	}

	// archiveExt ignores the level suffix.
	if ext := archiveExt("zstd:best"); ext != ".zst" {
		t.Errorf("archiveExt(zstd:best) = %q, want .zst", ext)
	}

	// A compound spec still produces a valid, decodable archive.
	var buf bytes.Buffer
	cw, closer, err := compressedWriter(&buf, "zstd:best")
	if err != nil {
		t.Fatalf("compressedWriter(zstd:best) error = %v", err)
	}
	if _, err := cw.Write([]byte("payload")); err != nil {
		t.Fatal(err)
	}
	if err := closer(); err != nil {
		t.Fatal(err)
	}
	r, rcloser, err := detectingReader(&buf)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rcloser() }()
	out := make([]byte, 7)
	if _, err := r.Read(out); err != nil {
		t.Fatal(err)
	}
	if string(out) != "payload" {
		t.Errorf("round-trip = %q, want payload", out)
	}
}
