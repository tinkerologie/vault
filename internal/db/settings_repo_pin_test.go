package db

import (
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestPinSFTPHostKey(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "v.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	var wg sync.WaitGroup
	got := make([]string, 8)
	for i := range got {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var e error
			got[i], e = d.PinSFTPHostKey("nas:22", "SHA256:"+string(rune('a'+i)))
			if e != nil {
				t.Error(e)
			}
		}()
	}
	wg.Wait()
	for _, g := range got {
		if g != got[0] || g == "" {
			t.Fatalf("racing pins disagree: %v", got)
		}
	}
	if p, err := d.PinSFTPHostKey("nas:22", "SHA256:new"); err != nil || p != got[0] {
		t.Fatalf("a later key replaced the pin: %q, %v", p, err)
	}
	if p, err := d.PinSFTPHostKey("nas:2222", "SHA256:other"); err != nil || p != "SHA256:other" {
		t.Fatalf("another port shares the pin: %q, %v", p, err)
	}
	// The pin is visible in the UI's activity log, once per address, with the key.
	logs, err := d.ListActivityLogs(50, "storage", 0)
	if err != nil {
		t.Fatal(err)
	}
	var forNAS, forOther int
	for _, e := range logs {
		if strings.Contains(e.Message, got[0]) && strings.Contains(e.Message, "nas:22") {
			forNAS++
		}
		if strings.Contains(e.Message, "SHA256:other") && strings.Contains(e.Message, "nas:2222") {
			forOther++
		}
	}
	if forNAS != 1 || forOther != 1 {
		t.Fatalf("activity entries = %d and %d, want one each: %+v", forNAS, forOther, logs)
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := d.PinSFTPHostKey("x:22", "SHA256:y"); err == nil {
		t.Fatal("closed database: want error")
	}
}
