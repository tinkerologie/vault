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

var errSomeOpFailure = errTest("op failed")

type errTest string

func (e errTest) Error() string { return string(e) }

type fakeSFTPConn struct{ closed bool }

func (f *fakeSFTPConn) Close() error { f.closed = true; return nil }
