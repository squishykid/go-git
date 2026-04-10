package bitmap

import (
	"bytes"
	"crypto"
	"encoding/hex"
	"io"
	"testing"

	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/hash"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var _ = plumbing.ZeroHash // keep import

func TestEncodeRoundTrip(t *testing.T) {
	t.Parallel()

	bitmapIdx := openFixture(t)
	src := openPackSource(t)

	// Collect the commit hashes from the existing fixture's entries.
	commits := make([]plumbing.Hash, bitmapIdx.EntryCount())
	for i := range commits {
		commits[i] = src.hashAtIdx(bitmapIdx.entries.commitPosition(i))
	}

	packChecksum := bitmapIdx.PackChecksum()

	enc := NewEncoder(src, hash.New(crypto.SHA1))
	var buf bytes.Buffer
	err := enc.Encode(&buf, packChecksum, commits, nil)
	require.NoError(t, err)

	// Parse the output back.
	result, err := Open(buf.Bytes(), hash.New(crypto.SHA1))
	require.NoError(t, err)

	// Header checks.
	assert.Equal(t, uint16(1), result.Version())
	assert.Equal(t, uint16(OptFullDAG), result.Flags())
	assert.Equal(t, hex.EncodeToString(packChecksum), hex.EncodeToString(result.PackChecksum()))
	assert.Equal(t, uint32(len(commits)), result.EntryCount())

	// Type bitmaps should have the same bit counts as the fixture.
	assert.Equal(t, bitmapIdx.Commits().BitCount(), result.Commits().BitCount())
	assert.Equal(t, bitmapIdx.Trees().BitCount(), result.Trees().BitCount())
	assert.Equal(t, bitmapIdx.Blobs().BitCount(), result.Blobs().BitCount())
	assert.Equal(t, bitmapIdx.Tags().BitCount(), result.Tags().BitCount())
}

func TestEncodeMatchesFixture(t *testing.T) {
	t.Parallel()

	bitmapIdx := openFixture(t)
	src := openPackSource(t)

	commits := make([]plumbing.Hash, bitmapIdx.EntryCount())
	for i := range commits {
		commits[i] = src.hashAtIdx(bitmapIdx.entries.commitPosition(i))
	}

	enc := NewEncoder(src, hash.New(crypto.SHA1))
	var buf bytes.Buffer
	err := enc.Encode(&buf, bitmapIdx.PackChecksum(), commits, nil)
	require.NoError(t, err)

	result, err := Open(buf.Bytes(), hash.New(crypto.SHA1))
	require.NoError(t, err)

	origSearcher := NewSearcher(bitmapIdx)
	newSearcher := NewSearcher(result)

	// Every entry's resolved reachability must match.
	for i := range int(bitmapIdx.EntryCount()) {
		origPos := bitmapIdx.entries.commitPosition(i)
		origBm, err := origSearcher.Reachable(origPos)
		require.NoError(t, err, "entry %d", i)

		newPos := result.entries.commitPosition(i)
		newBm, err := newSearcher.Reachable(newPos)
		require.NoError(t, err, "entry %d", i)

		assert.Equal(t, origPos, newPos, "entry %d commit position", i)
		assertBitmapsEqual(t, origBm, newBm)
	}
}

func TestEncodeSHA256(t *testing.T) {
	t.Parallel()

	bitmapIdx := openFixtureByURL(t, "https://gitlab.com/pjbgf/sha256.git", crypto.SHA256)
	src := openPackSourceByURL(t, "https://gitlab.com/pjbgf/sha256.git", crypto.SHA256)

	commits := make([]plumbing.Hash, bitmapIdx.EntryCount())
	for i := range commits {
		commits[i] = src.hashAtIdx(bitmapIdx.entries.commitPosition(i))
	}

	enc := NewEncoder(src, hash.New(crypto.SHA256))
	var buf bytes.Buffer
	err := enc.Encode(&buf, bitmapIdx.PackChecksum(), commits, nil)
	require.NoError(t, err)

	result, err := Open(buf.Bytes(), hash.New(crypto.SHA256))
	require.NoError(t, err)

	assert.Equal(t, bitmapIdx.EntryCount(), result.EntryCount())

	origSearcher := NewSearcher(bitmapIdx)
	newSearcher := NewSearcher(result)

	for i := range int(bitmapIdx.EntryCount()) {
		origBm, err := origSearcher.Reachable(bitmapIdx.entries.commitPosition(i))
		require.NoError(t, err)
		newBm, err := newSearcher.Reachable(result.entries.commitPosition(i))
		require.NoError(t, err)
		assertBitmapsEqual(t, origBm, newBm)
	}
}

func TestSelectCommits(t *testing.T) {
	t.Parallel()

	bitmapIdx := openFixture(t)
	src := openPackSource(t)

	// Use the fixture's HEAD as the tip.
	head := src.hashAtIdx(bitmapIdx.entries.commitPosition(0))

	commits, err := SelectCommits(src, []plumbing.Hash{head}, 0)
	require.NoError(t, err)

	// With maxDistance=0, every reachable commit should be selected.
	assert.Greater(t, len(commits), 100)

	// The tip must be the first entry.
	assert.Equal(t, head, commits[0])

	// Every selected hash must be a commit in the pack.
	for _, h := range commits[:10] {
		_, packPos, ok := src.FindPosition(h)
		require.True(t, ok)
		assert.Equal(t, plumbing.CommitObject, src.ObjectType(packPos))
	}
}

func TestSelectCommitsDistance(t *testing.T) {
	t.Parallel()

	bitmapIdx := openFixture(t)
	src := openPackSource(t)

	head := src.hashAtIdx(bitmapIdx.entries.commitPosition(0))

	all, err := SelectCommits(src, []plumbing.Hash{head}, 0)
	require.NoError(t, err)

	sparse, err := SelectCommits(src, []plumbing.Hash{head}, 100)
	require.NoError(t, err)

	// Sparse selection should have fewer commits.
	assert.Less(t, len(sparse), len(all))
	// But should still include the tip.
	assert.Equal(t, head, sparse[0])

	t.Logf("all=%d sparse=%d", len(all), len(sparse))
}

func TestSelectCommitsMissingObject(t *testing.T) {
	t.Parallel()

	src := openPackSource(t)

	// Use a hash that's not in the pack.
	bogus, _ := plumbing.FromHex("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	_, err := SelectCommits(src, []plumbing.Hash{bogus}, 0)
	assert.ErrorIs(t, err, ErrMissingObject)
}

// findTips returns all commits in the pack that are not referenced as
// a parent by any other commit (i.e. branch/tag tips).
func findTips(t testing.TB, src *testPackSource) []plumbing.Hash {
	t.Helper()
	n := src.ObjectCount()
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
	src := openPackSource(b)
	tips := findTips(b, src)
	b.Logf("tips=%d objects=%d", len(tips), src.ObjectCount())

	b.ResetTimer()
	for b.Loop() {
		_, err := SelectCommits(src, tips, 100)
		if err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkEncodeReuse(b *testing.B) {
	bitmapIdx := openFixture(b)
	src := openPackSource(b)
	old := NewSearcher(bitmapIdx)

	commits := make([]plumbing.Hash, bitmapIdx.EntryCount())
	for i := range commits {
		commits[i] = src.hashAtIdx(bitmapIdx.entries.commitPosition(i))
	}
	packChecksum := bitmapIdx.PackChecksum()

	b.ResetTimer()
	for b.Loop() {
		enc := NewEncoder(src, hash.New(crypto.SHA1))
		err := enc.Encode(io.Discard, packChecksum, commits, old)
		if err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkEncode(b *testing.B) {
	bitmapIdx := openFixture(b)
	src := openPackSource(b)

	commits := make([]plumbing.Hash, bitmapIdx.EntryCount())
	for i := range commits {
		commits[i] = src.hashAtIdx(bitmapIdx.entries.commitPosition(i))
	}
	packChecksum := bitmapIdx.PackChecksum()

	b.ResetTimer()
	for b.Loop() {
		enc := NewEncoder(src, hash.New(crypto.SHA1))
		err := enc.Encode(io.Discard, packChecksum, commits, nil)
		if err != nil {
			b.Fatal(err)
		}
	}
}
