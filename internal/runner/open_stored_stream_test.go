package runner

import (
	"bytes"
	"compress/gzip"
	"io"
	"strings"
	"testing"

	"github.com/ruaan-deysel/vault/internal/crypto"
)

// TestOpenStoredStream checks each storage wrapping is undone and that an
// encrypted object without the right passphrase is an error, never
// ciphertext passed through.
func TestOpenStoredStream(t *testing.T) {
	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	_, _ = zw.Write([]byte("hello"))
	_ = zw.Close()

	enc, err := crypto.EncryptReader("pw", bytes.NewReader(gz.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	encrypted, _ := io.ReadAll(enc)
	_ = enc.Close()

	read := func(data []byte, name, pass string) (string, string, error) {
		r, closeFn, local, err := OpenStoredStream(bytes.NewReader(data), name, pass)
		if err != nil {
			return "", "", err
		}
		defer closeFn() //nolint:errcheck
		b, err := io.ReadAll(r)
		return string(b), local, err
	}

	if got, local, err := read([]byte("plain"), "volumes.json", ""); err != nil || got != "plain" || local != "volumes.json" {
		t.Errorf("plain = %q %q %v", got, local, err)
	}
	if got, local, err := read(gz.Bytes(), "vdisk.img.gz", ""); err != nil || got != "hello" || local != "vdisk.img" {
		t.Errorf("gzip = %q %q %v", got, local, err)
	}
	if got, local, err := read(encrypted, "vdisk.img.gz.age", "pw"); err != nil || got != "hello" || local != "vdisk.img" {
		t.Errorf("age+gzip = %q %q %v", got, local, err)
	}
	if _, _, err := read(encrypted, "vdisk.img.gz.age", ""); err == nil || !strings.Contains(err.Error(), "passphrase is required") {
		t.Errorf("missing passphrase: %v", err)
	}
	if _, _, err := read(encrypted, "vdisk.img.gz.age", "wrong"); err == nil {
		t.Error("wrong passphrase was accepted")
	}
}
