package storage

import (
	"context"
	"testing"
)

func TestSFTPPoolReusesConnections(t *testing.T) {
	dials := 0
	p := newSFTPPool(2, func() (sftpConn, error) {
		dials++
		return &fakeSFTPConn{}, nil
	})
	c1, err := p.get(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	p.put(c1, nil) // healthy return
	c2, err := p.get(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	p.put(c2, nil)
	if dials != 1 {
		t.Errorf("dials = %d, want 1 (connection reused)", dials)
	}
}

func TestSFTPPoolDiscardsOnError(t *testing.T) {
	dials := 0
	p := newSFTPPool(2, func() (sftpConn, error) {
		dials++
		return &fakeSFTPConn{}, nil
	})
	c, _ := p.get(context.Background())
	p.put(c, errSomeOpFailure) // returned with error => discarded
	c2, _ := p.get(context.Background())
	p.put(c2, nil)
	if dials != 2 {
		t.Errorf("dials = %d, want 2 (broken conn discarded, new one dialed)", dials)
	}
	if !c.(*fakeSFTPConn).closed {
		t.Error("broken connection should have been Closed")
	}
}

func TestSFTPPoolCloseAllClosesIdle(t *testing.T) {
	p := newSFTPPool(2, func() (sftpConn, error) { return &fakeSFTPConn{}, nil })
	c, _ := p.get(context.Background())
	p.put(c, nil)
	p.closeAll()
	if !c.(*fakeSFTPConn).closed {
		t.Error("closeAll should close idle connections")
	}
	// After close, returning a connection closes it instead of pooling it.
	// get() pairs with put() so the semaphore slot is balanced and the dialed
	// connection isn't leaked.
	c2, _ := p.get(context.Background())
	p.put(c2, nil) // pool is closed → put closes c2 instead of pooling
	if !c2.(*fakeSFTPConn).closed {
		t.Error("put after closeAll should close the connection")
	}
}

func TestSFTPPoolGetCancelled(t *testing.T) {
	p := newSFTPPool(1, func() (sftpConn, error) { return &fakeSFTPConn{}, nil })
	if _, err := p.get(context.Background()); err != nil { // take the only slot
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := p.get(ctx); err == nil {
		t.Error("get with cancelled ctx and no free slot must return an error")
	}
}

var errSomeOpFailure = errTest("op failed")

type errTest string

func (e errTest) Error() string { return string(e) }

type fakeSFTPConn struct{ closed bool }

func (f *fakeSFTPConn) Close() error { f.closed = true; return nil }
