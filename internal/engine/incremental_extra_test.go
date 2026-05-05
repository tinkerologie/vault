package engine

import (
	"path/filepath"
	"testing"
	"time"
)

func TestParseChangedSince(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   map[string]any
		want bool
	}{
		{name: "nil settings", in: nil, want: false},
		{name: "missing key", in: map[string]any{}, want: false},
		{name: "empty string", in: map[string]any{"changed_since": ""}, want: false},
		{name: "non-string", in: map[string]any{"changed_since": 1234}, want: false},
		{name: "bad format", in: map[string]any{"changed_since": "not-a-date"}, want: false},
		{name: "valid RFC3339", in: map[string]any{"changed_since": "2024-01-01T00:00:00Z"}, want: true},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			got, ok := parseChangedSince(c.in)
			if ok != c.want {
				t.Errorf("got ok=%v, want %v (got=%v)", ok, c.want, got)
			}
		})
	}
}

func TestPathChangedSinceMissing(t *testing.T) {
	t.Parallel()
	_, err := pathChangedSince(filepath.Join(t.TempDir(), "missing"), time.Now())
	if err == nil {
		t.Error("expected error for missing path")
	}
}

func TestPathChangedSinceZeroTime(t *testing.T) {
	t.Parallel()
	// Zero time → always changed (shortcut return)
	got, err := pathChangedSince("/tmp", time.Time{})
	if err != nil {
		t.Errorf("zero time err: %v", err)
	}
	if !got {
		t.Error("zero time should report changed")
	}
}

func TestFilterChangedDomainDisksZeroTime(t *testing.T) {
	t.Parallel()
	disks := []domainDisk{{Path: "/x"}, {Path: "/y"}}
	got, err := filterChangedDomainDisks(disks, time.Time{})
	if err != nil {
		t.Errorf("err: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("zero time should pass through, got %d", len(got))
	}
}

func TestFilterChangedDomainDisksError(t *testing.T) {
	t.Parallel()
	_, err := filterChangedDomainDisks(
		[]domainDisk{{Path: "/nonexistent-disk-xyz"}},
		time.Now(),
	)
	if err == nil {
		t.Error("expected error for missing disk path")
	}
}
