package bitmap

import (
	"crypto"
	"io"
	"testing"

	fixtures "github.com/go-git/go-git-fixtures/v6"
	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/format/idxfile"
	"github.com/go-git/go-git/v6/plumbing/format/revfile"
	"github.com/go-git/go-git/v6/plumbing/hash"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDecode(t *testing.T) {
	t.Parallel()

	q := fixtures.ByTag("bitmap").ByURL("https://github.com/go-git/go-git.git").One()
	f, err := q.Bitmap()

	//f, err := os.Open(sampleBitmap)
	require.NoError(t, err)
	defer f.Close()

	h := hash.New(crypto.SHA1)
	d := NewDecoder(f, h)

	var idx Index
	err = d.Decode(&idx)
	require.NoError(t, err)

	assert.Equal(t, uint16(1), idx.Version)
	assert.Equal(t, uint16(OptFullDAG|OptHashCache), idx.Flags)

	// Pack checksum should match the filename.
	assert.Equal(t, q.PackfileHash, idx.PackChecksum.String())

	// 140 bitmap entries.
	assert.Len(t, idx.Entries, 140)

	// Type bitmaps should be present.
	assert.NotNil(t, idx.Commits)
	assert.NotNil(t, idx.Trees)
	assert.NotNil(t, idx.Blobs)
	assert.NotNil(t, idx.Tags)

	// Type bitmap bit counts (from EWAH headers).
	assert.Equal(t, uint32(6731), idx.Commits.BitCount())
	assert.Equal(t, uint32(25072), idx.Trees.BitCount())
	assert.Equal(t, uint32(25061), idx.Blobs.BitCount())
	assert.Equal(t, uint32(0), idx.Tags.BitCount())

	// Name-hash cache should have one entry per object in the pack.
	assert.Len(t, idx.NameHashCache, 25072)
}

func TestDecodeEntries(t *testing.T) {
	t.Parallel()

	q := fixtures.ByTag("bitmap").ByURL("https://github.com/go-git/go-git.git").One()
	f, err := q.Bitmap()
	require.NoError(t, err)
	defer f.Close()

	h := hash.New(crypto.SHA1)
	d := NewDecoder(f, h)

	var idx Index
	err = d.Decode(&idx)
	require.NoError(t, err)

	// First entry.
	e := idx.Entries[0]
	assert.Equal(t, uint32(2393), e.ObjectPosition)
	assert.Equal(t, uint8(0), e.XOROffset)
	assert.Equal(t, uint8(0), e.Flags)
	assert.NotNil(t, e.Bitmap)
	assert.Equal(t, uint32(25088), e.Bitmap.BitCount())

	// Second entry has XOR offset 1.
	e = idx.Entries[1]
	assert.Equal(t, uint32(12044), e.ObjectPosition)
	assert.Equal(t, uint8(1), e.XOROffset)
}

func BenchmarkDecode(b *testing.B) {
	q := fixtures.ByTag("bitmap").ByURL("https://github.com/go-git/go-git.git").One()

	for b.Loop() {
		f, err := q.Bitmap()
		require.NoError(b, err)

		h := hash.New(crypto.SHA1)
		d := NewDecoder(f, h)

		var idx Index
		err = d.Decode(&idx)
		require.NoError(b, err)

		f.Close()
	}
}

func BenchmarkDecodeEWAH(b *testing.B) {
	q := fixtures.ByTag("bitmap").ByURL("https://github.com/go-git/go-git.git").One()

	f, err := q.Bitmap()
	require.NoError(b, err)
	defer f.Close()

	h := hash.New(crypto.SHA1)
	var idx Index
	require.NoError(b, NewDecoder(f, h).Decode(&idx))
	require.GreaterOrEqual(b, len(idx.Entries), 100)

	b.ResetTimer()
	for b.Loop() {
		for _, data := range idx.Entries {
			_, err := DecodeEWAH(data.Bitmap)
			if err != nil {
				b.Fatal(err)
			}
		}
	}
}

func loadSearcherFixture(t *testing.T) (*Index, *Searcher, []plumbing.Hash) {
	t.Helper()
	q := fixtures.ByTag("bitmap").ByURL("https://github.com/go-git/go-git.git").One()

	bf, err := q.Bitmap()
	require.NoError(t, err)
	defer bf.Close()

	h := hash.New(crypto.SHA1)
	var idx Index
	require.NoError(t, NewDecoder(bf, h).Decode(&idx))

	idxf, err := q.Idx()
	require.NoError(t, err)
	defer idxf.Close()

	packIdx := idxfile.NewMemoryIndex(crypto.SHA1.Size())
	require.NoError(t, idxfile.NewDecoder(idxf, hash.New(crypto.SHA1)).Decode(packIdx))

	revf, err := q.Rev()
	require.NoError(t, err)
	defer revf.Close()

	count, err := packIdx.Count()
	require.NoError(t, err)

	ch := make(chan uint32, count)
	go func() {
		require.NoError(t, revfile.Decode(revf, count, packIdx.PackfileChecksum, ch))
	}()

	packOrder := make([]uint32, 0, count)
	for pos := range ch {
		packOrder = append(packOrder, pos)
	}

	s, err := NewSearcher(&idx, packIdx, packOrder)
	require.NoError(t, err)

	// Build idx position → hash list for test lookups.
	iter, err := packIdx.Entries()
	require.NoError(t, err)
	defer iter.Close()

	var idxHashes []plumbing.Hash
	for {
		e, err := iter.Next()
		if err == io.EOF {
			break
		}
		require.NoError(t, err)
		idxHashes = append(idxHashes, e.Hash)
	}

	return &idx, s, idxHashes
}

func TestSearcherReachable(t *testing.T) {
	t.Parallel()

	bitmapIdx, s, idxHashes := loadSearcherFixture(t)

	commitHash := idxHashes[bitmapIdx.Entries[0].ObjectPosition]

	reachable, err := s.Reachable(commitHash)
	require.NoError(t, err)
	assert.Greater(t, len(reachable), 10)
}

func TestSearcherXORResolution(t *testing.T) {
	t.Parallel()

	bitmapIdx, s, idxHashes := loadSearcherFixture(t)

	// The second entry has XOROffset=1 — verify XOR resolution works.
	require.Equal(t, uint8(1), bitmapIdx.Entries[1].XOROffset)

	commitHash := idxHashes[bitmapIdx.Entries[1].ObjectPosition]

	reachable, err := s.Reachable(commitHash)
	require.NoError(t, err)
	assert.Greater(t, len(reachable), 10)
}

func TestSearcherNotFound(t *testing.T) {
	t.Parallel()

	_, s, _ := loadSearcherFixture(t)

	_, err := s.Reachable(plumbing.NewHash("0000000000000000000000000000000000000000"))
	assert.ErrorIs(t, err, plumbing.ErrObjectNotFound)
}

func TestDecodeInvalidSignature(t *testing.T) {
	t.Parallel()

	q := fixtures.ByTag("bitmap").ByURL("https://github.com/go-git/go-git.git").One()
	f, err := q.Idx()
	require.NoError(t, err)
	defer f.Close()

	h := hash.New(crypto.SHA1)
	d := NewDecoder(f, h)

	var idx Index
	err = d.Decode(&idx)
	assert.ErrorIs(t, err, ErrInvalidSignature)
}
