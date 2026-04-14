package bitmap

import (
	"crypto"
	"encoding/hex"
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

type ordinal struct {
	*idxfile.MemoryIndex
	rev          []uint32 // packPos → idxPos
	idxToPackPos []uint32 // idxPos → packPos (inverse of rev)
	hashSize     int
}

func newOrdinal(index *idxfile.MemoryIndex, rev []uint32, hashSize int) *ordinal {
	idxToPackPos := make([]uint32, len(rev))
	for packPos, idxPos := range rev {
		idxToPackPos[idxPos] = uint32(packPos)
	}
	return &ordinal{MemoryIndex: index, rev: rev, idxToPackPos: idxToPackPos, hashSize: hashSize}
}

func (o *ordinal) FindPackRank(h plumbing.Hash) (uint32, bool) {
	// Use MemoryIndex to find the idx-sorted position, then map to pack rank.
	// findHashIndex is unexported, so use the Fanout table directly.
	bucket := int(h.Bytes()[0])
	k := o.FanoutMapping[bucket]
	if k < 0 {
		return 0, false
	}

	var base uint32
	if bucket > 0 {
		base = o.Fanout[bucket-1]
	}
	count := o.Fanout[bucket] - base

	// Binary search within the bucket's Names slice.
	lo, hi := uint32(0), count
	for lo < hi {
		mid := (lo + hi) / 2
		start := int(mid) * o.hashSize
		cmp := h.Compare(o.Names[k][start : start+o.hashSize])
		if cmp == 0 {
			idxPos := base + mid
			return o.idxToPackPos[idxPos], true
		} else if cmp > 0 {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	return 0, false
}

func (o *ordinal) HashAtIdxRank(idxPos uint32) (plumbing.Hash, bool) {
	// Binary search the 256-entry Fanout table to find the bucket
	// containing idxPos. Fanout[b] = cumulative count of objects
	// with first hash byte <= b.
	lo, hi := 0, 256
	for lo < hi {
		mid := (lo + hi) / 2
		if o.Fanout[mid] <= idxPos {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	if lo >= 256 {
		return plumbing.ZeroHash, false
	}

	k := o.FanoutMapping[lo]
	if k < 0 {
		return plumbing.ZeroHash, false
	}

	var base uint32
	if lo > 0 {
		base = o.Fanout[lo-1]
	}
	localPos := int(idxPos - base)

	start := localPos * o.hashSize
	end := start + o.hashSize
	if end > len(o.Names[k]) {
		return plumbing.ZeroHash, false
	}
	h, _ := plumbing.FromBytes(o.Names[k][start:end])
	return h, true
}

func (o *ordinal) HashAtPackRank(packPos uint32) (plumbing.Hash, bool) {
	if int(packPos) >= len(o.rev) {
		return plumbing.ZeroHash, false
	}
	return o.HashAtIdxRank(o.rev[packPos])
}

var _ idxfile.OrdinalIndex = (*ordinal)(nil)

func openFixture(t testing.TB) (*Index, idxfile.OrdinalIndex) {
	t.Helper()
	return openFixtureByURL(t, "https://github.com/go-git/go-git.git", crypto.SHA1)
}

func getOrdinalIndexFromIdxFile(rIdx io.ReadCloser, rRev io.ReadCloser, h crypto.Hash) idxfile.OrdinalIndex {
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

	return newOrdinal(idx, got, h.Size())
}

func openFixtureByURL(t testing.TB, url string, h crypto.Hash) (*Index, idxfile.OrdinalIndex) {
	t.Helper()
	q := fixtures.ByTag("bitmap").ByURL(url).One()

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

	q := fixtures.ByTag("bitmap").ByURL("https://github.com/go-git/go-git.git").One()

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
	assert.Equal(t, uint32(140), idx.EntryCount())

	assert.Equal(t, uint32(6731), idx.Commits().BitCount())
	assert.Equal(t, uint32(25072), idx.Trees().BitCount())
	assert.Equal(t, uint32(25061), idx.Blobs().BitCount())
	assert.Equal(t, uint32(0), idx.Tags().BitCount())

	cache := idx.NameHashCache()
	assert.Equal(t, 25072*4, len(cache))
}

func TestOpenEntries(t *testing.T) {
	t.Parallel()

	idx, _ := openFixture(t)

	e := idx.Entry(0)
	assert.Equal(t, uint32(2393), e.ObjectPosition)
	assert.Equal(t, uint8(0), e.XOROffset)
	assert.Equal(t, uint8(0), e.Flags)
	assert.NotNil(t, e.Bitmap)
	assert.Equal(t, uint32(25088), e.Bitmap.BitCount())

	e = idx.Entry(1)
	assert.Equal(t, uint32(12044), e.ObjectPosition)
	assert.Equal(t, uint8(1), e.XOROffset)
}

func TestOpenInvalidSignature(t *testing.T) {
	t.Parallel()

	q := fixtures.ByTag("bitmap").ByURL("https://github.com/go-git/go-git.git").One()
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

	q := fixtures.ByTag("bitmap").ByURL("https://gitlab.com/pjbgf/sha256.git").One()

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

	idx, _ := openFixtureByURL(t, "https://gitlab.com/pjbgf/sha256.git", crypto.SHA256)

	e := idx.Entry(0)
	assert.Greater(t, e.Bitmap.BitCount(), uint32(0))
	assert.NotNil(t, e.Bitmap)
}

func TestSearcherReachableSHA256(t *testing.T) {
	t.Parallel()

	idx, _ := openFixtureByURL(t, "https://gitlab.com/pjbgf/sha256.git", crypto.SHA256)
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
	q := fixtures.ByTag("bitmap").ByURL("https://github.com/go-git/go-git.git").One()

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
