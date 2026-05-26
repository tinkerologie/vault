package storage

import (
	"testing"
	"time"
)

func TestCapacityIsZero(t *testing.T) {
	t.Parallel()
	if !(Capacity{}).IsZero() {
		t.Error("zero-value Capacity should report IsZero")
	}
	if (Capacity{TotalBytes: 1}).IsZero() {
		t.Error("non-zero Total should not report IsZero")
	}
	if (Capacity{ProbedAt: time.Now()}).IsZero() {
		t.Error("ProbedAt alone should not report IsZero")
	}
}
