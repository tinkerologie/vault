package storage

import (
	"errors"
	"sync"
)

// errSFTPPoolClosed is returned by get when the pool has been closed.
var errSFTPPoolClosed = errors.New("sftp: connection pool closed")

type sftpConn interface {
	Close() error
}

type sftpPool struct {
	dial   func() (sftpConn, error)
	sem    chan struct{}
	mu     sync.Mutex
	idle   []sftpConn
	closed bool
	done   chan struct{} // closed by closeAll to unblock waiters in get
}

func newSFTPPool(size int, dial func() (sftpConn, error)) *sftpPool {
	if size < 1 {
		size = 1
	}
	return &sftpPool{dial: dial, sem: make(chan struct{}, size), done: make(chan struct{})}
}

// get borrows a connection, blocking until a slot frees. It returns
// errSFTPPoolClosed if the pool is closed while waiting (or already closed),
// so a backup whose adapter is torn down on cancellation does not block forever.
func (p *sftpPool) get() (sftpConn, error) {
	// Fast-path: if the pool is already closed, don't even try to acquire the
	// semaphore. This makes get() deterministically return an error when called
	// after closeAll(), regardless of whether a semaphore slot is free.
	select {
	case <-p.done:
		return nil, errSFTPPoolClosed
	default:
	}
	select {
	case p.sem <- struct{}{}:
	case <-p.done:
		return nil, errSFTPPoolClosed
	}
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

// closeAll closes the pool: it signals waiters via done (once), then closes all
// idle connections. Idempotent.
func (p *sftpPool) closeAll() {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return
	}
	p.closed = true
	close(p.done)
	idle := p.idle
	p.idle = nil
	p.mu.Unlock()
	for _, c := range idle {
		_ = c.Close()
	}
}
