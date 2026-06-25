package db

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestIsUniqueViolation(t *testing.T) {
	t.Parallel()

	// Produce a genuine SQLite UNIQUE violation to classify alongside the
	// nil/unrelated cases.
	d, err := Open(filepath.Join(t.TempDir(), "vault.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	if _, err := d.CreateJob(Job{Name: "dup-name"}); err != nil {
		t.Fatalf("first CreateJob: %v", err)
	}
	_, uniqueErr := d.CreateJob(Job{Name: "dup-name"})
	if uniqueErr == nil {
		t.Fatal("expected a duplicate-name error on the second insert")
	}

	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil error", nil, false},
		{"unrelated error", errors.New("some unrelated failure"), false},
		{"unique violation", uniqueErr, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsUniqueViolation(tt.err); got != tt.want {
				t.Errorf("IsUniqueViolation(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}
