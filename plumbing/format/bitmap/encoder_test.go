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
