package bitmap

import (
	"crypto"
	"encoding/hex"
	"io"
	"testing"

	fixtures "github.com/go-git/go-git-fixtures/v6"
	"github.com/go-git/go-git/v6/plumbing/format/idxfile"
	"github.com/go-git/go-git/v6/plumbing/format/revfile"
	"github.com/go-git/go-git/v6/plumbing/hash"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func openFixture(t testing.TB) (*Index, revfile.RevIndex) {
	t.Helper()
	return openFixtureFromQuery(t, fixtures.ByTag("bitmap-xor").One(), crypto.SHA1)
}

func getOrdinalIndexFromIdxFile(rIdx io.ReadCloser, rRev io.ReadCloser, h crypto.Hash) revfile.RevIndex {
	defer rIdx.Close()
	defer rRev.Close()

	idx := idxfile.NewMemoryIndex(h.Size())
	if err := idxfile.NewDecoder(rIdx, hash.New(h)).Decode(idx); err != nil {
		panic(err)
	}

	count, err := idx.Count()
	if err != nil {
		panic(err)
	}

	idxPos := make(chan uint32)
	got := []uint32{}
	errCh := make(chan error, 1)
	go func() {
		errCh <- revfile.Decode(rRev, count, idx.PackfileChecksum, idxPos)
	}()

	for pos := range idxPos {
		got = append(got, pos)
	}

	if err := <-errCh; err != nil {
		panic(err)
	}

	return revfile.NewMemoryRevIndex(idx, got, h.Size())
}

func openFixtureFromQuery(t testing.TB, q *fixtures.Fixture, h crypto.Hash) (*Index, revfile.RevIndex) {
	t.Helper()

	f, err := q.Bitmap()
	require.NoError(t, err)
	defer f.Close()

	data, err := io.ReadAll(f)
	require.NoError(t, err)

	idx, err := Open(data, hash.New(h))
	require.NoError(t, err)

	idxF, err := q.Idx()
	require.NoError(t, err)
	defer idxF.Close()

	revF, err := q.Rev()
	require.NoError(t, err)
	defer revF.Close()

	return idx, getOrdinalIndexFromIdxFile(idxF, revF, h)
}

func TestOpen(t *testing.T) {
	t.Parallel()

	q := fixtures.ByTag("bitmap-xor").One()

	f, err := q.Bitmap()
	require.NoError(t, err)
	defer f.Close()

	data, err := io.ReadAll(f)
	require.NoError(t, err)

	idx, err := Open(data, hash.New(crypto.SHA1))
	require.NoError(t, err)

	assert.Equal(t, uint16(1), idx.Version())
	assert.Equal(t, uint16(OptFullDAG|OptHashCache), idx.Flags())
	assert.Equal(t, q.PackfileHash, hex.EncodeToString(idx.PackChecksum()))
	assert.Equal(t, uint32(107), idx.EntryCount())

	assert.Equal(t, uint32(643), idx.Commits().BitCount())
	assert.Equal(t, uint32(2133), idx.Trees().BitCount())
	assert.Equal(t, uint32(2122), idx.Blobs().BitCount())
	assert.Equal(t, uint32(0), idx.Tags().BitCount())

	cache := idx.NameHashCache()
	assert.Equal(t, 2133*4, len(cache))
}

func TestOpenEntries(t *testing.T) {
	t.Parallel()

	idx, _ := openFixture(t)

	e := idx.Entry(0)
	assert.Equal(t, uint32(1949), e.ObjectPosition)
	assert.Equal(t, uint8(0), e.XOROffset)
	assert.Equal(t, uint8(0), e.Flags)
	assert.NotNil(t, e.Bitmap)
	assert.Equal(t, uint32(2176), e.Bitmap.BitCount())

	e = idx.Entry(1)
	assert.Equal(t, uint32(1755), e.ObjectPosition)
	assert.Equal(t, uint8(1), e.XOROffset)
}

func TestOpenInvalidSignature(t *testing.T) {
	t.Parallel()

	q := fixtures.ByTag("bitmap-xor").One()
	f, err := q.Idx()
	require.NoError(t, err)
	defer f.Close()

	data, err := io.ReadAll(f)
	require.NoError(t, err)

	_, err = Open(data, hash.New(crypto.SHA1))
	assert.ErrorIs(t, err, ErrInvalidSignature)
}

func TestOpenSHA256(t *testing.T) {
	t.Parallel()

	q := fixtures.ByTag("bitmap").ByObjectFormat("sha256").One()

	f, err := q.Bitmap()
	require.NoError(t, err)
	defer f.Close()

	data, err := io.ReadAll(f)
	require.NoError(t, err)

	idx, err := Open(data, hash.New(crypto.SHA256))
	require.NoError(t, err)

	assert.Equal(t, uint16(1), idx.Version())
	assert.Equal(t, uint16(OptFullDAG|OptHashCache), idx.Flags())
	assert.Equal(t, q.PackfileHash, hex.EncodeToString(idx.PackChecksum()))
	assert.Greater(t, idx.EntryCount(), uint32(0))
}

func TestOpenEntriesSHA256(t *testing.T) {
	t.Parallel()

	idx, _ := openFixtureFromQuery(t, fixtures.ByTag("bitmap").ByObjectFormat("sha256").One(), crypto.SHA256)

	e := idx.Entry(0)
	assert.Greater(t, e.Bitmap.BitCount(), uint32(0))
	assert.NotNil(t, e.Bitmap)
}

func TestSearcherReachableSHA256(t *testing.T) {
	t.Parallel()

	idx, _ := openFixtureFromQuery(t, fixtures.ByTag("bitmap").ByObjectFormat("sha256").One(), crypto.SHA256)
	s := NewSearcher(idx)

	e := idx.Entry(0)
	bm, err := s.Reachable(e.ObjectPosition)
	require.NoError(t, err)

	setBits := 0
	it := bm.SetBits()
	for _, ok := it.Next(); ok; _, ok = it.Next() {
		setBits++
	}
	assert.Greater(t, setBits, 1)
}

func BenchmarkOpen(b *testing.B) {
	q := fixtures.ByTag("bitmap-xor").One()

	f, err := q.Bitmap()
	require.NoError(b, err)
	defer f.Close()

	data, err := io.ReadAll(f)
	require.NoError(b, err)

	for b.Loop() {
		_, err := Open(data, hash.New(crypto.SHA1))
		if err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkDecodeEWAH(b *testing.B) {
	idx, _ := openFixture(b)
	require.GreaterOrEqual(b, int(idx.EntryCount()), 100)

	entries := make([]EWAH, idx.EntryCount())
	for i := range entries {
		entries[i] = idx.Entry(i).Bitmap
	}

	b.ResetTimer()
	for b.Loop() {
		for _, data := range entries {
			_, err := DecodeEWAH(data)
			if err != nil {
				b.Fatal(err)
			}
		}
	}
}
