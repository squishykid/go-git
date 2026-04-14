package bitmap

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func BenchmarkReachable(b *testing.B) {
	idx, _ := openFixture(b)
	s := NewSearcher(idx)

	lastEntry := idx.Entry(int(idx.EntryCount() - 1))

	b.ResetTimer()
	for b.Loop() {
		_, err := s.Reachable(lastEntry.ObjectPosition)
		if err != nil {
			b.Fatal(err)
		}

	}
}

func TestSearcherReachable(t *testing.T) {
	t.Parallel()

	idx, _ := openFixture(t)
	s := NewSearcher(idx)

	e := idx.Entry(0)
	bm, err := s.Reachable(e.ObjectPosition)
	require.NoError(t, err)

	// The bitmap should have a non-trivial number of bits set.
	setBits := 0
	for i := uint32(0); i < bm.Bits(); i++ {
		if bm.Get(i) {
			setBits++
		}
	}
	assert.Greater(t, setBits, 10)
}

func TestSearcherXORResolution(t *testing.T) {
	t.Parallel()

	idx, _ := openFixture(t)
	s := NewSearcher(idx)

	e := idx.Entry(1)
	require.Equal(t, uint8(1), e.XOROffset)

	bm, err := s.Reachable(e.ObjectPosition)
	require.NoError(t, err)

	setBits := 0
	for i := uint32(0); i < bm.Bits(); i++ {
		if bm.Get(i) {
			setBits++
		}
	}
	assert.Greater(t, setBits, 10)
}

func TestSearcherReachableCommits(t *testing.T) {
	t.Parallel()

	idx, _ := openFixture(t)
	s := NewSearcher(idx)

	commitsBm, err := DecodeEWAH(idx.Commits())
	require.NoError(t, err)

	// Search entries for one with reachable commits (earlier entries
	// may only reach trees/blobs in the bitmap's position range).
	var positions []uint32
	for ei := range int(idx.EntryCount()) {
		bm, err := s.Reachable(idx.Entry(ei).ObjectPosition)
		require.NoError(t, err)

		it, err := s.ReachableCommits(bm)
		require.NoError(t, err)

		positions = positions[:0]
		for pos, ok := it.Next(); ok; pos, ok = it.Next() {
			positions = append(positions, pos)
		}
		if len(positions) > 0 {
			break
		}
	}

	require.NotEmpty(t, positions, "expected at least one entry with reachable commits")

	// Every returned position must be a commit per the type bitmap.
	for _, pos := range positions {
		assert.True(t, commitsBm.Get(pos), "position %d should be a commit", pos)
	}
}

func TestSearcherNotFound(t *testing.T) {
	t.Parallel()

	idx, _ := openFixture(t)
	s := NewSearcher(idx)

	_, err := s.Reachable(999999)
	assert.ErrorIs(t, err, ErrNoEntry)
}
