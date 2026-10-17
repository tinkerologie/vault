// Package bufpool provides fixed-size byte buffer pools for data-plane hot
// paths, so a copy loop run once per file does not allocate a fresh buffer
// each time (issue #500).
//
// Pools hand out *[]byte rather than []byte so Put does not allocate.
// Returned buffers are cleared before reuse, so file content or plaintext is
// never retained between borrowers.
package bufpool

import "sync"

// Pool is a sync.Pool of buffers that all have the same length.
type Pool struct {
	size int
	pool sync.Pool
}

// New returns a pool of size-byte buffers.
func New(size int) *Pool {
	p := &Pool{size: size}
	p.pool.New = func() any {
		b := make([]byte, size)
		return &b
	}
	return p
}

// Copy32K holds 32 KiB buffers, the io.Copy default, for stream copy loops.
var Copy32K = New(32 << 10)

// Size reports the length of the buffers this pool hands out.
func (p *Pool) Size() int { return p.size }

// Get returns a buffer of exactly Size bytes. Its contents are zero.
func (p *Pool) Get() *[]byte {
	b := p.pool.Get().(*[]byte)
	*b = (*b)[:p.size]
	return b
}

// Put clears b and returns it to the pool. Buffers of a different capacity,
// and nil, are dropped so the pool never hands out a short buffer.
func (p *Pool) Put(b *[]byte) {
	if b == nil || cap(*b) != p.size {
		return
	}
	*b = (*b)[:p.size]
	clear(*b)
	p.pool.Put(b)
}
