package cli

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/tinkerologie/vault/internal/db"
)

func TestDurableHostKeyPinner(t *testing.T) {
	d, err := db.Open(filepath.Join(t.TempDir(), "v.db"))
	if err != nil {
		t.Fatal(err)
	}
	saves := 0
	var saveErr error
	pin := durableHostKeyPinner(func() *db.DB { return d }, func() error { saves++; return saveErr })

	// Only a new pin is saved.
	for range 2 {
		if got, err := pin("nas:22", "SHA256:a"); err != nil || got != "SHA256:a" {
			t.Fatalf("%q, %v", got, err)
		}
	}
	if saves != 1 {
		t.Fatalf("saves = %d, want 1", saves)
	}

	// A pin that cannot be saved refuses the connection and is retried.
	saveErr = errors.New("flash is read-only")
	if _, err := pin("other:22", "SHA256:b"); !errors.Is(err, saveErr) {
		t.Fatalf("err = %v", err)
	}
	if v, _ := d.GetSetting(db.SFTPHostKeyPinPrefix+"other:22", ""); v != "" {
		t.Fatalf("unsaved pin kept: %q", v)
	}
	saveErr = nil
	saves = 0
	if got, err := pin("other:22", "SHA256:b"); err != nil || got != "SHA256:b" || saves != 1 {
		t.Fatalf("retry: %q, %v, saves=%d", got, err, saves)
	}

	_ = d.Close()
	if _, err := pin("third:22", "SHA256:c"); err == nil {
		t.Fatal("closed database: want error")
	}
}

// A stalled save holds up its own address only.
func TestDurableHostKeyPinner_StalledSaveBlocksOnlyItsAddress(t *testing.T) {
	d, err := db.Open(filepath.Join(t.TempDir(), "v.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if _, err := d.PinSFTPHostKey("known:22", "SHA256:k"); err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	pin := durableHostKeyPinner(func() *db.DB { return d }, func() error {
		close(entered)
		<-release
		return nil
	})
	first := make(chan error, 1)
	go func() {
		_, err := pin("new:22", "SHA256:n")
		first <- err
	}()
	<-entered

	other := make(chan error, 1)
	go func() {
		_, err := pin("known:22", "SHA256:k")
		other <- err
	}()
	select {
	case err := <-other:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("another address waited for the save")
	}

	same := make(chan error, 1)
	go func() {
		_, err := pin("new:22", "SHA256:n")
		same <- err
	}()
	select {
	case <-same:
		t.Fatal("same address accepted a pin still being saved")
	case <-time.After(200 * time.Millisecond):
	}
	close(release)
	if err := <-first; err != nil {
		t.Fatal(err)
	}
	if err := <-same; err != nil {
		t.Fatal(err)
	}
}
