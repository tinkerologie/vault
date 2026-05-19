package dedup

import (
	"bytes"
	"crypto/rand"
	"io"
	"testing"
)

func TestChunkerDeterminism(t *testing.T) {
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		t.Fatal(err)
	}
	data := make([]byte, 4<<20)
	if _, err := rand.Read(data); err != nil {
		t.Fatal(err)
	}

	b1 := collectBoundaries(t, secret, data)
	b2 := collectBoundaries(t, secret, data)
	if len(b1) != len(b2) {
		t.Fatalf("len mismatch: %d vs %d", len(b1), len(b2))
	}
	for i := range b1 {
		if b1[i] != b2[i] {
			t.Fatalf("boundary %d differs: %d vs %d", i, b1[i], b2[i])
		}
	}
}

func TestChunkerSecretAffectsBoundaries(t *testing.T) {
	s1 := bytes.Repeat([]byte{0x01}, 32)
	s2 := bytes.Repeat([]byte{0x02}, 32)
	data := make([]byte, 4<<20)
	if _, err := rand.Read(data); err != nil {
		t.Fatal(err)
	}
	b1 := collectBoundaries(t, s1, data)
	b2 := collectBoundaries(t, s2, data)
	if len(b1) == 0 || len(b2) == 0 {
		t.Fatal("no chunks produced")
	}
	overlap := 0
	set := make(map[int]struct{}, len(b1))
	for _, v := range b1 {
		set[v] = struct{}{}
	}
	for _, v := range b2 {
		if _, ok := set[v]; ok {
			overlap++
		}
	}
	if overlap*2 >= len(b1) {
		t.Fatalf("too much boundary overlap (%d/%d); secret should change boundaries", overlap, len(b1))
	}
}

func TestChunkerEdgeSizes(t *testing.T) {
	secret := bytes.Repeat([]byte{0xab}, 32)
	cases := []struct {
		name string
		size int
	}{
		{"empty", 0},
		{"one_byte", 1},
		{"below_min", 100 * 1024},
		{"at_min", 256 * 1024},
		{"at_avg", 1024 * 1024},
		{"at_max", 4 * 1024 * 1024},
		{"above_max", 4*1024*1024 + 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			data := make([]byte, tc.size)
			chunker, err := NewChunker(secret)
			if err != nil {
				t.Fatal(err)
			}
			n := 0
			err = chunker.Split(bytes.NewReader(data), func(chunk []byte) error {
				n++
				return nil
			})
			if err != nil && err != io.EOF {
				t.Fatal(err)
			}
			if tc.size == 0 && n != 0 {
				t.Fatalf("empty input produced %d chunks", n)
			}
			if tc.size > 0 && n == 0 {
				t.Fatal("non-empty input produced 0 chunks")
			}
		})
	}
}

func collectBoundaries(t *testing.T, secret, data []byte) []int {
	t.Helper()
	chunker, err := NewChunker(secret)
	if err != nil {
		t.Fatal(err)
	}
	boundaries := []int{}
	cum := 0
	err = chunker.Split(bytes.NewReader(data), func(chunk []byte) error {
		cum += len(chunk)
		boundaries = append(boundaries, cum)
		return nil
	})
	if err != nil && err != io.EOF {
		t.Fatal(err)
	}
	return boundaries
}
