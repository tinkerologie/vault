package storage

import "testing"

func TestSFTPPoolReusesConnections(t *testing.T) {
	dials := 0
	p := newSFTPPool(2, func() (sftpConn, error) {
		dials++
		return &fakeSFTPConn{}, nil
	})
	c1, err := p.get()
	if err != nil {
		t.Fatal(err)
	}
	p.put(c1, nil) // healthy return
	c2, err := p.get()
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
	c, _ := p.get()
	p.put(c, errSomeOpFailure) // returned with error => discarded
	c2, _ := p.get()
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
	c, _ := p.get()
	p.put(c, nil)
	p.closeAll()
	if !c.(*fakeSFTPConn).closed {
		t.Error("closeAll should close idle connections")
	}
	// After close, putting a connection back closes it instead of pooling.
	// Acquire a semaphore slot via get() so put() can release it properly.
	_, _ = p.get() // consumes a slot; we intentionally discard this conn
	c2 := &fakeSFTPConn{}
	p.put(c2, nil) // releases the slot; closes c2 because pool is closed
	if !c2.closed {
		t.Error("put after closeAll should close the connection")
	}
}

var errSomeOpFailure = errTest("op failed")

type errTest string

func (e errTest) Error() string { return string(e) }

type fakeSFTPConn struct{ closed bool }

func (f *fakeSFTPConn) Close() error { f.closed = true; return nil }
