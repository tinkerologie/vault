//go:build linux

package engine

import (
	"bytes"
	"testing"
)

func TestZFSCountingStreamsReportBytes(t *testing.T) {
	rec := &recorder{}
	ctx := rec.ctx(t.Context(), "tank/data")
	// A full send reports against its estimated stream size.
	cw := &countingWriter{ctx: ctx, w: &bytes.Buffer{}, total: 100, detailTotal: 100, name: "tank/data"}
	if _, err := cw.Write(make([]byte, 40)); err != nil {
		t.Fatal(err)
	}
	if d, _ := rec.last(PhaseProcessing); d.BytesDone != 40 || d.BytesTotal != 100 {
		t.Fatalf("full send report = %+v, want 40/100", d)
	}
	// An incremental send has no real total: dataset usage is not its size.
	inc := &countingWriter{ctx: ctx, w: &bytes.Buffer{}, total: 100, name: "tank/data"}
	if _, err := inc.Write(make([]byte, 10)); err != nil {
		t.Fatal(err)
	}
	if d, _ := rec.last(PhaseProcessing); d.BytesDone != 10 || d.BytesTotal != 0 {
		t.Fatalf("incremental send report = %+v, want 10 bytes and no total", d)
	}
	cr := &countingReader{ctx: ctx, r: bytes.NewReader(make([]byte, 30)), total: 30, name: "tank/data"}
	buf := make([]byte, 64)
	if _, err := cr.Read(buf); err != nil {
		t.Fatal(err)
	}
	if d, _ := rec.last(PhaseProcessing); d.BytesDone != 30 || d.BytesTotal != 30 {
		t.Fatalf("recv report = %+v", d)
	}
	// Without a context the streams still work.
	if _, err := (&countingWriter{w: &bytes.Buffer{}}).Write([]byte("x")); err != nil {
		t.Fatal(err)
	}
}
