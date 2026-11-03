package engine

import (
	"slices"
	"testing"

	"github.com/tinkerologie/vault/internal/dedup"
)

// A __vol__ chunk that cannot be read may be a sub-manifest, so the walk fails
// instead of counting it as data and hiding what it references from GC.
func TestWalkManifestClosureFailsOnUnreadableNestedManifest(t *testing.T) {
	t.Parallel()
	repo := newChunkTestRepo(t)
	missing := dedup.ID{0xAA}
	top, err := repo.PutManifest("test", dedup.Manifest{Files: map[string]dedup.ManifestEntry{
		"__vol__/config": {IsDir: true, Chunks: []dedup.ID{missing}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.Flush(); err != nil {
		t.Fatal(err)
	}
	if m, d, err := WalkManifestClosure(repo, []dedup.ID{top}); err == nil {
		t.Fatalf("closure = %x, %x, want an error", m, d)
	}
}

// A file that happens to be named like a volume key decodes as nothing and is
// still ordinary data, and a real sub-manifest is still followed.
func TestWalkManifestClosureClassifiesVolumeEntries(t *testing.T) {
	t.Parallel()
	repo := newChunkTestRepo(t)
	plain, err := repo.Put([]byte("not json, just a file"))
	if err != nil {
		t.Fatal(err)
	}
	segEmpty, err := repo.Put([]byte(`{"type":"segmented"}`))
	if err != nil {
		t.Fatal(err)
	}
	segBad, err := repo.Put([]byte(`{"type":"segmented","segments":"nope"}`))
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := repo.Put([]byte("volume data"))
	if err != nil {
		t.Fatal(err)
	}
	sub, err := repo.PutManifest("test", dedup.Manifest{Files: map[string]dedup.ManifestEntry{"a.bin": {Chunks: []dedup.ID{leaf}}}})
	if err != nil {
		t.Fatal(err)
	}
	top, err := repo.PutManifest("test", dedup.Manifest{Files: map[string]dedup.ManifestEntry{
		"__vol__x":       {Chunks: []dedup.ID{plain}},
		"__vol__seg":     {Chunks: []dedup.ID{segEmpty}},
		"__vol__segbad":  {Chunks: []dedup.ID{segBad}},
		"__vol__/config": {IsDir: true, Chunks: []dedup.ID{sub}},
		"__vol__/empty":  {Size: -1},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.Flush(); err != nil {
		t.Fatal(err)
	}
	manifests, data, err := WalkManifestClosure(repo, []dedup.ID{top})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(manifests, sub) || !slices.Contains(data, leaf) || !slices.Contains(data, plain) ||
		!slices.Contains(data, segEmpty) || !slices.Contains(data, segBad) {
		t.Fatalf("manifests=%x data=%x", manifests, data)
	}
}
