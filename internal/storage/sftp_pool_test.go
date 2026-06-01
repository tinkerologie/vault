package storage

import (
	"testing"
	"time"
)

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
	// After close, get() returns an error so put() must not be called.
	if _, err := p.get(); err == nil {
		t.Error("get after closeAll must return an error")
	}
}

func TestSFTPPoolGetAfterCloseErrors(t *testing.T) {
	p := newSFTPPool(1, func() (sftpConn, error) { return &fakeSFTPConn{}, nil })
	p.closeAll()
	if _, err := p.get(); err == nil {
		t.Error("get after closeAll must return an error, not block or succeed")
	}
}

func TestSFTPPoolGetUnblocksOnClose(t *testing.T) {
	p := newSFTPPool(1, func() (sftpConn, error) { return &fakeSFTPConn{}, nil })
	if _, err := p.get(); err != nil { // hold the only slot, never return it
		t.Fatal(err)
	}
	errCh := make(chan error, 1)
	go func() { _, err := p.get(); errCh <- err }() // sem is full, so this waits
	// No sleep needed: closeAll unblocks get() whether it is already parked on
	// the semaphore (done fires) or reaches the select after close (done is
	// already closed). Either way the get must return promptly with an error.
	p.closeAll()
	select {
	case err := <-errCh:
		if err == nil {
			t.Error("blocked get must return an error after closeAll")
		}
	case <-time.After(2 * time.Second):
		t.Error("blocked get did not unblock after closeAll")
	}
}

var errSomeOpFailure = errTest("op failed")

type errTest string

func (e errTest) Error() string { return string(e) }

type fakeSFTPConn struct{ closed bool }

func (f *fakeSFTPConn) Close() error { f.closed = true; return nil }
