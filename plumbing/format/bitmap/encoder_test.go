package bitmap

import (
	"bytes"
	"crypto"
	"encoding/hex"
	"io"
	"testing"

	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/format/revfile"
	"github.com/go-git/go-git/v6/plumbing/hash"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fixtureCommits returns the SelectedCommit list matching the fixture's
// bitmap entries, suitable for passing to Encoder.Encode.
func fixtureCommits(idx *Index, ordIdx revfile.RevIndex) []SelectedCommit {
	commits := make([]SelectedCommit, idx.EntryCount())
	for i := range commits {
		pos := idx.entries.commitPosition(i)
		h, _ := ordIdx.HashAtIdxRank(pos)
		commits[i] = SelectedCommit{
			Hash:   h,
			IdxPos: pos,
		}
	}
	return commits
}

func TestEncodeRoundTrip(t *testing.T) {
	t.Parallel()

	bitmapIdx, ordIdx := openFixture(t)
	src := openPackSource(t)

	commits := fixtureCommits(bitmapIdx, ordIdx)

	packChecksum, _ := plumbing.FromBytes(bitmapIdx.PackChecksum())

	enc := NewEncoder(src, hash.New(crypto.SHA1), ordIdx)
	var buf bytes.Buffer
	err := enc.Encode(&buf, packChecksum, commits)
	require.NoError(t, err)

	// Parse the output back.
	result, err := Open(buf.Bytes(), hash.New(crypto.SHA1))
	require.NoError(t, err)

	// Header checks.
	assert.Equal(t, uint16(1), result.Version())
	assert.Equal(t, uint16(OptFullDAG), result.Flags())
	assert.Equal(t, hex.EncodeToString(bitmapIdx.PackChecksum()), hex.EncodeToString(result.PackChecksum()))
	assert.Equal(t, uint32(len(commits)), result.EntryCount())

	// Type bitmaps should have the same bit counts as the fixture.
	assert.Equal(t, bitmapIdx.Commits().BitCount(), result.Commits().BitCount())
	assert.Equal(t, bitmapIdx.Trees().BitCount(), result.Trees().BitCount())
	assert.Equal(t, bitmapIdx.Blobs().BitCount(), result.Blobs().BitCount())
	assert.Equal(t, bitmapIdx.Tags().BitCount(), result.Tags().BitCount())
}

func TestEncodeMatchesFixture(t *testing.T) {
	t.Parallel()

	bitmapIdx, ordIdx := openFixture(t)
	src := openPackSource(t)

	commits := fixtureCommits(bitmapIdx, ordIdx)
	packChecksum, _ := plumbing.FromBytes(bitmapIdx.PackChecksum())

	enc := NewEncoder(src, hash.New(crypto.SHA1), ordIdx)
	var buf bytes.Buffer
	err := enc.Encode(&buf, packChecksum, commits)
	require.NoError(t, err)

	result, err := Open(buf.Bytes(), hash.New(crypto.SHA1))
	require.NoError(t, err)

	origSearcher := NewSearcher(bitmapIdx)
	newSearcher := NewSearcher(result)

	// Every original entry's reachability must match in the new bitmap.
	// Entry ordering may differ (topological sort), so look up by
	// commit position rather than comparing entry-by-entry.
	for i := range int(bitmapIdx.EntryCount()) {
		pos := bitmapIdx.entries.commitPosition(i)
		origBm, err := origSearcher.Reachable(pos)
		require.NoError(t, err, "orig entry %d", i)

		newBm, err := newSearcher.Reachable(pos)
		require.NoError(t, err, "new entry for pos %d", pos)

		assertBitmapsEqual(t, origBm, newBm)
	}
}

func TestEncodeSHA256(t *testing.T) {
	t.Parallel()

	bitmapIdx, ordIdx := openFixtureByURL(t, "https://gitlab.com/pjbgf/sha256.git", crypto.SHA256)
	src := openPackSourceByURL(t, "https://gitlab.com/pjbgf/sha256.git", crypto.SHA256)

	commits := fixtureCommits(bitmapIdx, ordIdx)
	packChecksum, _ := plumbing.FromBytes(bitmapIdx.PackChecksum())

	enc := NewEncoder(src, hash.New(crypto.SHA256), ordIdx)
	var buf bytes.Buffer
	err := enc.Encode(&buf, packChecksum, commits)
	require.NoError(t, err)

	result, err := Open(buf.Bytes(), hash.New(crypto.SHA256))
	require.NoError(t, err)

	assert.Equal(t, bitmapIdx.EntryCount(), result.EntryCount())

	origSearcher := NewSearcher(bitmapIdx)
	newSearcher := NewSearcher(result)

	for i := range int(bitmapIdx.EntryCount()) {
		pos := bitmapIdx.entries.commitPosition(i)
		origBm, err := origSearcher.Reachable(pos)
		require.NoError(t, err)
		newBm, err := newSearcher.Reachable(pos)
		require.NoError(t, err)
		assertBitmapsEqual(t, origBm, newBm)
	}
}

func TestSelectCommits(t *testing.T) {
	t.Parallel()

	bitmapIdx, ordIdx := openFixture(t)
	src := openPackSource(t)

	// Use the fixture's HEAD as the tip.
	head, _ := ordIdx.HashAtIdxRank(bitmapIdx.entries.commitPosition(0))

	enc := NewEncoder(src, hash.New(crypto.SHA1), ordIdx)
	commits, err := enc.SelectCommits([]plumbing.Hash{head}, 0)
	require.NoError(t, err)

	// With maxDistance=0, every reachable commit should be selected.
	assert.Greater(t, len(commits), 100)

	// The tip must be the first entry.
	assert.Equal(t, head, commits[0].Hash)

	// Every selected entry must be a commit with a valid idx position.
	for _, sc := range commits[:10] {
		_, packPos, ok := src.FindPosition(sc.Hash)
		require.True(t, ok)
		assert.Equal(t, plumbing.CommitObject, src.ObjectType(packPos))
	}
}

func TestSelectCommitsDistance(t *testing.T) {
	t.Parallel()

	bitmapIdx, ordIdx := openFixture(t)
	src := openPackSource(t)

	head, _ := ordIdx.HashAtIdxRank(bitmapIdx.entries.commitPosition(0))
	enc := NewEncoder(src, hash.New(crypto.SHA1), ordIdx)

	all, err := enc.SelectCommits([]plumbing.Hash{head}, 0)
	require.NoError(t, err)

	sparse, err := enc.SelectCommits([]plumbing.Hash{head}, 100)
	require.NoError(t, err)

	// Sparse selection should have fewer commits.
	assert.Less(t, len(sparse), len(all))
	// But should still include the tip.
	assert.Equal(t, head, sparse[0].Hash)

	t.Logf("all=%d sparse=%d", len(all), len(sparse))
}

func TestTopoSort(t *testing.T) {
	t.Parallel()

	bitmapIdx, ordIdx := openFixture(t)
	src := openPackSource(t)

	head, _ := ordIdx.HashAtIdxRank(bitmapIdx.entries.commitPosition(0))
	enc := NewEncoder(src, hash.New(crypto.SHA1), ordIdx)

	commits, err := enc.SelectCommits([]plumbing.Hash{head}, 0)
	require.NoError(t, err)

	sorted, err := TopoSort(src, commits)
	require.NoError(t, err)
	assert.Equal(t, len(commits), len(sorted))

	// Build position map: for each commit, record its index in the
	// sorted output.
	pos := make(map[plumbing.Hash]int, len(sorted))
	for i, sc := range sorted {
		pos[sc.Hash] = i
	}

	// Verify: every commit must appear before its parents in the
	// sorted output (lower index = earlier in slice = child).
	for _, sc := range sorted {
		_, packPos, ok := src.FindPosition(sc.Hash)
		require.True(t, ok)
		obj, err := src.Object(packPos)
		require.NoError(t, err)
		_, parents, err := parseCommitObj(obj)
		require.NoError(t, err)

		for _, p := range parents {
			if pi, ok := pos[p]; ok {
				assert.Greater(t, pi, pos[sc.Hash],
					"commit %s (pos %d) should appear before parent %s (pos %d)",
					sc.Hash, pos[sc.Hash], p, pi)
			}
		}
	}
}

func TestSelectCommitsMissingObject(t *testing.T) {
	t.Parallel()

	_, ordIdx := openFixture(t)
	src := openPackSource(t)
	enc := NewEncoder(src, hash.New(crypto.SHA1), ordIdx)

	// Use a hash that's not in the pack.
	bogus, _ := plumbing.FromHex("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	_, err := enc.SelectCommits([]plumbing.Hash{bogus}, 0)
	assert.ErrorIs(t, err, ErrMissingObject)
}

// findTips returns all commits in the pack that are not referenced as
// a parent by any other commit (i.e. branch/tag tips).
func findTips(t testing.TB, src *testPackSource) []plumbing.Hash {
	t.Helper()
	n := src.Count()
	hasParent := make(map[plumbing.Hash]struct{})

	// First pass: collect all parent references.
	for pos := uint32(0); pos < uint32(n); pos++ {
		if src.ObjectType(pos) != plumbing.CommitObject {
			continue
		}
		obj, err := src.Object(pos)
		if err != nil {
			continue
		}
		_, parents, err := parseCommitObj(obj)
		if err != nil {
			continue
		}
		for _, p := range parents {
			hasParent[p] = struct{}{}
		}
	}

	// Second pass: commits not in hasParent are tips.
	var tips []plumbing.Hash
	for pos := uint32(0); pos < uint32(n); pos++ {
		if src.ObjectType(pos) != plumbing.CommitObject {
			continue
		}
		h := src.hashAtOffset(pos)
		if _, ok := hasParent[h]; !ok {
			tips = append(tips, h)
		}
	}
	return tips
}

func BenchmarkSelectCommits(b *testing.B) {
	_, ordIdx := openFixture(b)
	src := openPackSource(b)
	enc := NewEncoder(src, hash.New(crypto.SHA1), ordIdx)
	tips := findTips(b, src)
	b.Logf("tips=%d objects=%d", len(tips), src.Count())

	b.ResetTimer()
	for b.Loop() {
		_, err := enc.SelectCommits(tips, 100)
		if err != nil {
			b.Fatal(err)
		}
	}
}

// TODO re-enable when we have reuse implemented again
//func BenchmarkEncodeReuse(b *testing.B) {
//	bitmapIdx := openFixture(b)
//	src := openPackSource(b)
//	old := NewSearcher(bitmapIdx)
//
//	commits := fixtureCommits(bitmapIdx, src)
//	packChecksum := bitmapIdx.PackChecksum()
//
//	b.ResetTimer()
//	for b.Loop() {
//		enc := NewEncoder(src, hash.New(crypto.SHA1))
//		err := enc.Encode(io.Discard, packChecksum, commits, old)
//		if err != nil {
//			b.Fatal(err)
//		}
//	}
//}

func BenchmarkEncode(b *testing.B) {
	bitmapIdx, ordIdx := openFixture(b)
	src := openPackSource(b)

	commits := fixtureCommits(bitmapIdx, ordIdx)
	packChecksum, _ := plumbing.FromBytes(bitmapIdx.PackChecksum())

	b.ResetTimer()
	for b.Loop() {
		enc := NewEncoder(src, hash.New(crypto.SHA1), ordIdx)
		err := enc.Encode(io.Discard, packChecksum, commits)
		if err != nil {
			b.Fatal(err)
		}
	}
}
