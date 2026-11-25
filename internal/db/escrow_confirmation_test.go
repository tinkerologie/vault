package db

import (
	"path/filepath"
	"testing"
)

func TestEscrowConfirmationIsNotTheHash(t *testing.T) {
	hash := "$2a$10$abcdefghijklmnopqrstuuABCDEFGHIJKLMNOPQRSTUVWXYZ0123"
	got := EscrowConfirmation(hash)
	if len(got) != 16 || got == hash || EscrowConfirmation(hash+"x") == got {
		t.Fatalf("EscrowConfirmation(%q) = %q", hash, got)
	}
	if EscrowConfirmation("") != "" {
		t.Fatal("an empty hash has no confirmation")
	}
}

// Records written before confirmations were hashed held the raw bcrypt hash;
// opening the database drops them, keeping the hashed markers (#714).
func TestOpenDropsRawEscrowRecords(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vault.db")
	d, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.SetSetting(DedupEscrowConfirmedPrefix+"1", "$2a$10$abcdefghijklmnopqrstuuABCDEFGHIJKLMNOPQRSTUVWXYZ0123"); err != nil {
		t.Fatal(err)
	}
	marker := EscrowConfirmation("current")
	if err := d.SetSetting(DedupEscrowConfirmedPrefix+"2", marker); err != nil {
		t.Fatal(err)
	}
	d.Close()

	d, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if got, _ := d.GetSetting(DedupEscrowConfirmedPrefix+"1", ""); got != "" {
		t.Fatalf("raw hash record kept: %q", got)
	}
	if got, _ := d.GetSetting(DedupEscrowConfirmedPrefix+"2", ""); got != marker {
		t.Fatalf("marker record changed: %q", got)
	}
}
