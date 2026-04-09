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

func loadTestPackIndex(t testing.TB) *testPackIndex {
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

func BenchmarkReachable(b *testing.B) {
	idx := openFixture(b)
	pack := loadTestPackIndex(b)
	s := NewSearcher(idx, pack)

	commitHash := pack.ObjectID(int(idx.Entry(int(idx.EntryCount() - 1)).ObjectPosition))

	b.ResetTimer()
	for b.Loop() {
		_, err := s.Reachable(commitHash)
		if err != nil {
			b.Fatal(err)
		}
	}
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
