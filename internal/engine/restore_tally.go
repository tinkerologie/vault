package engine

import (
	"context"
	"sync/atomic"
)

// RestoreTally counts the regular files and bytes a restore actually wrote
// to its target. The runner attaches one per item with WithRestoreTally so a
// selective restore of one small file reports that file, not the whole
// item's recorded size (#455). Engines that never consult it are unaffected.
type RestoreTally struct {
	files atomic.Int64
	bytes atomic.Int64
}

// Add records one restored file of n bytes. Safe on a nil receiver.
func (t *RestoreTally) Add(n int64) {
	if t == nil {
		return
	}
	t.files.Add(1)
	t.bytes.Add(n)
}

// Files returns the number of files recorded.
func (t *RestoreTally) Files() int64 {
	if t == nil {
		return 0
	}
	return t.files.Load()
}

// Bytes returns the number of bytes recorded.
func (t *RestoreTally) Bytes() int64 {
	if t == nil {
		return 0
	}
	return t.bytes.Load()
}

type restoreTallyKey struct{}

// WithRestoreTally attaches t to ctx.
func WithRestoreTally(ctx context.Context, t *RestoreTally) context.Context {
	return context.WithValue(ctx, restoreTallyKey{}, t)
}

// RestoreTallyFrom returns the tally on ctx, or nil.
func RestoreTallyFrom(ctx context.Context) *RestoreTally {
	t, _ := ctx.Value(restoreTallyKey{}).(*RestoreTally)
	return t
}
