package bounded

import (
	"errors"
	"strings"
	"testing"
)

func TestReadAll(t *testing.T) {
	t.Parallel()
	if got, err := ReadAll(strings.NewReader("12345"), 5); err != nil || string(got) != "12345" {
		t.Fatalf("at the limit: %q, %v", got, err)
	}
	if _, err := ReadAll(strings.NewReader("123456"), 5); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("over the limit: %v", err)
	}
	if _, err := ReadAll(failingReader{}, 5); !errors.Is(err, errRead) {
		t.Fatalf("read error: %v", err)
	}
}

func TestDecodeJSON(t *testing.T) {
	t.Parallel()
	var v []int
	if err := DecodeJSON(strings.NewReader(`[1,2,3]`), 7, &v); err != nil || len(v) != 3 {
		t.Fatalf("at the limit: %v, %v", v, err)
	}
	if err := DecodeJSON(strings.NewReader(`[1,2,3,4,5,6,7,8,9]`), 7, &v); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("over the limit: %v", err)
	}
	// A complete value one byte past the limit is still too large.
	if err := DecodeJSON(strings.NewReader(`[1]`), 2, &v); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("limit+1: %v", err)
	}
	// Bytes after the value do not count against it.
	if err := DecodeJSON(strings.NewReader(`[1]        `), 3, &v); err != nil {
		t.Fatalf("trailing bytes: %v", err)
	}
	if err := DecodeJSON(strings.NewReader(`{bad`), 100, &v); err == nil || errors.Is(err, ErrTooLarge) {
		t.Fatalf("bad JSON: %v", err)
	}
}

var errRead = errors.New("read failed")

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errRead }
