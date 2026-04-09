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

func openFixture(t testing.TB) Index {
	t.Helper()
	q := fixtures.ByTag("bitmap").ByURL("https://github.com/go-git/go-git.git").One()

	f, err := q.Bitmap()
	require.NoError(t, err)
	defer f.Close()

	data, err := io.ReadAll(f)
	require.NoError(t, err)

	idx, err := Open(data, hash.New(crypto.SHA1))
	require.NoError(t, err)
	return idx
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

	idx := openFixture(t)

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
	idx := openFixture(b)
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

// testPackIndex implements PackIndex for tests using idxfile + revfile data.
type testPackIndex struct {
	hashes    []plumbing.Hash
	packOrder []uint32
}

func (p *testPackIndex) ObjectCount() int                  { return len(p.hashes) }
func (p *testPackIndex) ObjectID(idxPos int) plumbing.Hash { return p.hashes[idxPos] }
func (p *testPackIndex) IdxPositionAtOffset(packPos int) int {
	return int(p.packOrder[packPos])
}

func loadTestPackIndex(t *testing.T) *testPackIndex {
	t.Helper()
	q := fixtures.ByTag("bitmap").ByURL("https://github.com/go-git/go-git.git").One()

	idxf, err := q.Idx()
	require.NoError(t, err)
	defer idxf.Close()

	packIdx := idxfile.NewMemoryIndex(crypto.SHA1.Size())
	require.NoError(t, idxfile.NewDecoder(idxf, hash.New(crypto.SHA1)).Decode(packIdx))

	iter, err := packIdx.Entries()
	require.NoError(t, err)
	defer iter.Close()

	var hashes []plumbing.Hash
	for {
		e, err := iter.Next()
		if err == io.EOF {
			break
		}
		require.NoError(t, err)
		hashes = append(hashes, e.Hash)
	}

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

	return &testPackIndex{hashes: hashes, packOrder: packOrder}
}

func TestSearcherReachable(t *testing.T) {
	t.Parallel()

	idx := openFixture(t)
	pack := loadTestPackIndex(t)
	s := NewSearcher(idx, pack)

	commitHash := pack.ObjectID(int(idx.Entry(0).ObjectPosition))
	reachable, err := s.Reachable(commitHash)
	require.NoError(t, err)
	assert.Greater(t, len(reachable), 10)
}

func TestSearcherXORResolution(t *testing.T) {
	t.Parallel()

	idx := openFixture(t)
	pack := loadTestPackIndex(t)
	s := NewSearcher(idx, pack)

	e := idx.Entry(1)
	require.Equal(t, uint8(1), e.XOROffset)

	commitHash := pack.ObjectID(int(e.ObjectPosition))
	reachable, err := s.Reachable(commitHash)
	require.NoError(t, err)
	assert.Greater(t, len(reachable), 10)
}

func TestSearcherNotFound(t *testing.T) {
	t.Parallel()

	idx := openFixture(t)
	pack := loadTestPackIndex(t)
	s := NewSearcher(idx, pack)

	_, err := s.Reachable(plumbing.NewHash("0000000000000000000000000000000000000000"))
	assert.ErrorIs(t, err, plumbing.ErrObjectNotFound)
}
