package storage

import "sync"

// sftpConn is the subset of a pooled SFTP connection the pool manages. Real
// connections (ssh+sftp clients) satisfy it; tests provide a fake.
type sftpConn interface {
	Close() error
}

// sftpPool is a bounded pool of reusable SFTP connections. A connection
// returned with a non-nil error is closed and discarded rather than reused.
type sftpPool struct {
	dial   func() (sftpConn, error)
	sem    chan struct{}
	mu     sync.Mutex
	idle   []sftpConn
	closed bool
}

func newSFTPPool(size int, dial func() (sftpConn, error)) *sftpPool {
	if size < 1 {
		size = 1
	}
	return &sftpPool{dial: dial, sem: make(chan struct{}, size)}
}

func (p *sftpPool) get() (sftpConn, error) {
	p.sem <- struct{}{}
	p.mu.Lock()
	if n := len(p.idle); n > 0 {
		c := p.idle[n-1]
		p.idle = p.idle[:n-1]
		p.mu.Unlock()
		return c, nil
	}
	p.mu.Unlock()
	c, err := p.dial()
	if err != nil {
		<-p.sem
		return nil, err
	}
	return c, nil
}

func (p *sftpPool) put(c sftpConn, opErr error) {
	defer func() { <-p.sem }()
	if c == nil {
		return
	}
	if opErr != nil {
		_ = c.Close()
		return
	}
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		_ = c.Close()
		return
	}
	p.idle = append(p.idle, c)
	p.mu.Unlock()
}

func (p *sftpPool) closeAll() {
	p.mu.Lock()
	p.closed = true
	idle := p.idle
	p.idle = nil
	p.mu.Unlock()
	for _, c := range idle {
		_ = c.Close()
	}
}
