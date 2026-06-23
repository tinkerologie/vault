package db

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestIsUniqueViolation(t *testing.T) {
	t.Parallel()

	if IsUniqueViolation(nil) {
		t.Fatal("nil error must not be reported as a unique violation")
	}
	if IsUniqueViolation(errors.New("some unrelated failure")) {
		t.Fatal("unrelated error must not be reported as a unique violation")
	}

	d, err := Open(filepath.Join(t.TempDir(), "vault.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })

	if _, err := d.CreateJob(Job{Name: "dup-name"}); err != nil {
		t.Fatalf("first CreateJob: %v", err)
	}
	_, err = d.CreateJob(Job{Name: "dup-name"})
	if err == nil {
		t.Fatal("expected a duplicate-name error on the second insert")
	}
	if !IsUniqueViolation(err) {
		t.Fatalf("expected IsUniqueViolation==true for %v", err)
	}
}
