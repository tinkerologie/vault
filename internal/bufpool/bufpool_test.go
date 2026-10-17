package bufpool

import "testing"

func TestGetReturnsFullLengthBuffer(t *testing.T) {
	p := New(1024)
	if p.Size() != 1024 {
		t.Fatalf("Size() = %d, want 1024", p.Size())
	}
	b := p.Get()
	if len(*b) != 1024 || cap(*b) != 1024 {
		t.Fatalf("len/cap = %d/%d, want 1024/1024", len(*b), cap(*b))
	}
}

func TestPutClearsContent(t *testing.T) {
	p := New(64)
	b := p.Get()
	for i := range *b {
		(*b)[i] = 0xff
	}
	*b = (*b)[:10] // a borrower that resliced must still get a full buffer back
	p.Put(b)
	if len(*b) != 64 {
		t.Fatalf("len after Put = %d, want 64", len(*b))
	}
	for i, v := range *b {
		if v != 0 {
			t.Fatalf("byte %d = %#x after Put, want 0", i, v)
		}
	}
	got := p.Get()
	if len(*got) != 64 {
		t.Fatalf("len after reuse = %d, want 64", len(*got))
	}
	for i, v := range *got {
		if v != 0 {
			t.Fatalf("reused byte %d = %#x, want 0", i, v)
		}
	}
}

func TestPutUsedClearsOnlyThePrefix(t *testing.T) {
	p := New(64)
	b := p.Get()
	for i := range 16 {
		(*b)[i] = 0xff
	}
	p.PutUsed(b, 16)
	for i, v := range *b {
		if v != 0 {
			t.Fatalf("byte %d = %#x after PutUsed, want 0", i, v)
		}
	}
	// Out-of-range counts are clamped rather than panicking.
	b = p.Get()
	(*b)[63] = 0xff
	p.PutUsed(b, 1000)
	if (*b)[63] != 0 {
		t.Fatal("PutUsed(1000) did not clear the whole buffer")
	}
	p.PutUsed(p.Get(), -5)
	p.PutUsed(nil, 1)
}

func TestPutDropsWrongSizeAndNil(t *testing.T) {
	p := New(64)
	p.Put(nil)
	wrong := make([]byte, 32, 32)
	wrong[0] = 0xff
	p.Put(&wrong)
	if wrong[0] != 0xff {
		t.Fatal("Put modified a buffer it should have dropped")
	}
	for range 8 {
		if b := p.Get(); cap(*b) != 64 {
			t.Fatalf("Get returned cap %d, want 64", cap(*b))
		}
	}
}

func TestCopy32K(t *testing.T) {
	b := Copy32K.Get()
	defer Copy32K.Put(b)
	if len(*b) != 32<<10 {
		t.Fatalf("Copy32K buffer len = %d, want %d", len(*b), 32<<10)
	}
}

func BenchmarkGetPut(b *testing.B) {
	p := New(32 << 10)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		buf := p.Get()
		p.Put(buf)
	}
}
