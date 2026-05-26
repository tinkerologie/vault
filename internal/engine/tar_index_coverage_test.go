package engine

import (
	"bytes"
	"testing"
)

// TestReadTarIndex_InvalidJSON drives the JSON decode error branch.
func TestReadTarIndex_InvalidJSON(t *testing.T) {
	t.Parallel()
	_, err := ReadTarIndex(bytes.NewReader([]byte("not valid json")))
	if err == nil {
		t.Fatal("expected JSON decode error")
	}
}

// (TestReadTarIndex_VersionMismatch lives in tar_index_test.go.)

// TestReadTarIndex_HappyPath drives the success branch.
func TestReadTarIndex_HappyPath(t *testing.T) {
	t.Parallel()
	body := []byte(`{"version": 1, "archive": "x.tar", "files": [{"path":"a"}]}`)
	idx, err := ReadTarIndex(bytes.NewReader(body))
	if err != nil {
		t.Fatalf("ReadTarIndex: %v", err)
	}
	if idx.Archive != "x.tar" || len(idx.Files) != 1 {
		t.Errorf("unexpected idx: %+v", idx)
	}
}

// TestBaseName_AllVariants drives every branch of baseName.
func TestBaseName_AllVariants(t *testing.T) {
	t.Parallel()
	tests := []struct {
		in   string
		want string
	}{
		{in: "data.tar", want: "data.tar"},
		{in: "/abs/path/data.tar", want: "data.tar"},
		{in: "rel/path/data.tar", want: "data.tar"},
		{in: `C:\Users\admin\data.tar`, want: "data.tar"},
		{in: "/trailing/", want: ""},
		{in: "no-slash", want: "no-slash"},
		{in: "", want: ""},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.in, func(t *testing.T) {
			t.Parallel()
			got := baseName(tt.in)
			if got != tt.want {
				t.Errorf("baseName(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// TestWriteTarIndex_MissingArchive drives the os.Open error branch.
func TestWriteTarIndex_MissingArchive(t *testing.T) {
	t.Parallel()
	if err := WriteTarIndex("/no/such/path/archive.tar"); err == nil {
		t.Fatal("expected open error for missing archive")
	}
}
