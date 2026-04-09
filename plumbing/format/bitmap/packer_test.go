package bitmap

import (
	"bytes"
	"crypto"
	"encoding/binary"
	"io"
	"testing"

	"github.com/go-git/go-billy/v6/osfs"
	fixtures "github.com/go-git/go-git-fixtures/v6"
	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/format/idxfile"
	"github.com/go-git/go-git/v6/plumbing/format/packfile"
	"github.com/go-git/go-git/v6/plumbing/hash"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testPackSource adapts a packfile.Packfile + reverse index to the
// PackSource interface used by Packer. Positions are in pack-offset
// order (the order objects appear in the .pack file), matching the
// bitmap format.
type testPackSource struct {
	// hashToOffset maps object hash → pack-offset position.
	hashToOffset map[plumbing.Hash]uint32
	// offsetToIdx maps pack-offset position → idx (hash-sorted) position.
	offsetToIdx []uint32
	// idxToHash maps idx position → hash.
	idxToHash []plumbing.Hash
	pf        *packfile.Packfile
}

func (s *testPackSource) ObjectCount() int {
	return len(s.offsetToIdx)
}

func (s *testPackSource) FindPosition(h plumbing.Hash) (uint32, bool) {
	pos, ok := s.hashToOffset[h]
	return pos, ok
}

func (s *testPackSource) Object(offsetPos uint32) (plumbing.EncodedObject, error) {
	if int(offsetPos) >= len(s.offsetToIdx) {
		return nil, plumbing.ErrObjectNotFound
	}
	idxPos := s.offsetToIdx[offsetPos]
	return s.pf.Get(s.idxToHash[idxPos])
}

const revFileHeader = 12 // 4 sig + 4 version + 4 hash version

func openPackSource(t testing.TB) *testPackSource {
	t.Helper()
	q := fixtures.ByTag("bitmap").ByURL("https://github.com/go-git/go-git.git").One()

	// Decode pack index.
	idxFile, err := q.Idx()
	require.NoError(t, err)
	defer idxFile.Close()

	idx := idxfile.NewMemoryIndex(crypto.SHA1.Size())
	require.NoError(t, idxfile.NewDecoder(idxFile, hash.New(crypto.SHA1)).Decode(idx))

	count, err := idx.Count()
	require.NoError(t, err)
	n := int(count)

	// Build idx-position → hash table. Normalise via FromBytes so the
	// ObjectID trailing bytes are clean (the idx iterator may leave
	// garbage beyond the hash length).
	idxToHash := make([]plumbing.Hash, n)
	entries, err := idx.Entries()
	require.NoError(t, err)
	for i := 0; i < n; i++ {
		entry, err := entries.Next()
		require.NoError(t, err)
		idxToHash[i], _ = plumbing.FromBytes(entry.Hash.Bytes())
	}
	entries.Close()

	// Read reverse index: pack-offset-position → idx-position.
	revFile, err := q.Rev()
	require.NoError(t, err)
	defer revFile.Close()
	revData, err := io.ReadAll(revFile)
	require.NoError(t, err)

	offsetToIdx := make([]uint32, n)
	idxToOffset := make([]uint32, n)
	for i := 0; i < n; i++ {
		idxPos := binary.BigEndian.Uint32(revData[revFileHeader+i*4:])
		offsetToIdx[i] = idxPos
		idxToOffset[idxPos] = uint32(i)
	}

	// Build hash → pack-offset-position table.
	hashToOffset := make(map[plumbing.Hash]uint32, n)
	for idxPos, h := range idxToHash {
		hashToOffset[h] = idxToOffset[idxPos]
	}

	// Open pack file.
	packFile, err := q.Packfile()
	require.NoError(t, err)

	pf := packfile.NewPackfile(packFile,
		packfile.WithIdx(idx),
		packfile.WithFs(osfs.New(t.TempDir())),
	)
	t.Cleanup(func() { pf.Close() })

	return &testPackSource{
		hashToOffset: hashToOffset,
		offsetToIdx:  offsetToIdx,
		idxToHash:    idxToHash,
		pf:           pf,
	}
}

// hashAtOffset returns the object hash at the given pack-offset position.
func (s *testPackSource) hashAtOffset(offsetPos uint32) plumbing.Hash {
	return s.idxToHash[s.offsetToIdx[offsetPos]]
}

func TestPackerNegotiateWalk(t *testing.T) {
	t.Parallel()

	bitmapIdx := openFixture(t)
	src := openPackSource(t)
	s := NewSearcher(bitmapIdx)

	// Build set of positions that have precomputed bitmap entries.
	hasEntry := make(map[uint32]bool, bitmapIdx.EntryCount())
	for i := range int(bitmapIdx.EntryCount()) {
		hasEntry[bitmapIdx.Entry(i).ObjectPosition] = true
	}

	// Find a commit that does NOT have a bitmap entry.
	commitsBm, err := DecodeEWAH(bitmapIdx.Commits())
	require.NoError(t, err)

	var commitPos uint32
	found := false
	it := commitsBm.SetBits()
	for pos, ok := it.Next(); ok; pos, ok = it.Next() {
		if !hasEntry[pos] {
			commitPos = pos
			found = true
			break
		}
	}
	require.True(t, found, "need a commit without a bitmap entry")

	commitHash := src.hashAtOffset(commitPos)
	p := NewPacker(s, src, hash.New(crypto.SHA1))

	bm, err := p.Negotiate([]plumbing.Hash{commitHash}, nil)
	require.NoError(t, err)

	// The commit itself must be in the result.
	assert.True(t, bm.Get(commitPos), "walked commit should be in result")

	// The result should produce a valid packfile with many objects.
	var buf bytes.Buffer
	checksum, err := p.WritePack(&buf, bm)
	require.NoError(t, err)

	data := buf.Bytes()
	assert.Equal(t, []byte("PACK"), data[0:4])
	assert.Greater(t, binary.BigEndian.Uint32(data[8:12]), uint32(1))
	assert.False(t, checksum.IsZero())
}

func TestPackerNegotiate(t *testing.T) {
	t.Parallel()

	bitmapIdx := openFixture(t)
	src := openPackSource(t)
	s := NewSearcher(bitmapIdx)
	p := NewPacker(s, src, hash.New(crypto.SHA1))

	e0 := bitmapIdx.Entry(0)
	h0 := src.hashAtOffset(e0.ObjectPosition)

	bm, err := p.Negotiate([]plumbing.Hash{h0}, nil)
	require.NoError(t, err)
	require.NotNil(t, bm)

	// Without haves the result should match the raw reachability bitmap.
	expected, err := s.Reachable(e0.ObjectPosition)
	require.NoError(t, err)
	assert.Equal(t, expected, bm)
}

func TestPackerNegotiateWithHaves(t *testing.T) {
	t.Parallel()

	bitmapIdx := openFixture(t)
	src := openPackSource(t)
	s := NewSearcher(bitmapIdx)
	p := NewPacker(s, src, hash.New(crypto.SHA1))

	e0 := bitmapIdx.Entry(0)
	h0 := src.hashAtOffset(e0.ObjectPosition)

	// When want == have, the result bitmap should have no set bits.
	bm, err := p.Negotiate([]plumbing.Hash{h0}, []plumbing.Hash{h0})
	require.NoError(t, err)

	setBits := 0
	it := bm.SetBits()
	for _, ok := it.Next(); ok; _, ok = it.Next() {
		setBits++
	}
	assert.Equal(t, 0, setBits)
}

func TestPackerPack(t *testing.T) {
	t.Parallel()

	bitmapIdx := openFixture(t)
	src := openPackSource(t)
	s := NewSearcher(bitmapIdx)
	p := NewPacker(s, src, hash.New(crypto.SHA1))

	e0 := bitmapIdx.Entry(0)
	h0 := src.hashAtOffset(e0.ObjectPosition)

	var buf bytes.Buffer
	checksum, err := p.Pack(&buf, []plumbing.Hash{h0}, nil)
	require.NoError(t, err)

	data := buf.Bytes()
	require.GreaterOrEqual(t, len(data), 12)

	// Valid pack header.
	assert.Equal(t, []byte("PACK"), data[0:4])
	assert.Equal(t, uint32(2), binary.BigEndian.Uint32(data[4:8]))

	objectCount := binary.BigEndian.Uint32(data[8:12])
	assert.Greater(t, objectCount, uint32(10))

	// Trailing checksum matches the returned hash.
	assert.False(t, checksum.IsZero())
	assert.Equal(t, checksum.Bytes(), data[len(data)-checksum.Size():])
}

func TestPackerPackWithHaves(t *testing.T) {
	t.Parallel()

	bitmapIdx := openFixture(t)
	src := openPackSource(t)
	s := NewSearcher(bitmapIdx)
	p := NewPacker(s, src, hash.New(crypto.SHA1))

	e0 := bitmapIdx.Entry(0)
	h0 := src.hashAtOffset(e0.ObjectPosition)
	e1 := bitmapIdx.Entry(1)
	h1 := src.hashAtOffset(e1.ObjectPosition)

	var buf1 bytes.Buffer
	_, err := p.Pack(&buf1, []plumbing.Hash{h0}, nil)
	require.NoError(t, err)
	countWithout := binary.BigEndian.Uint32(buf1.Bytes()[8:12])

	var buf2 bytes.Buffer
	_, err = p.Pack(&buf2, []plumbing.Hash{h0}, []plumbing.Hash{h1})
	require.NoError(t, err)
	countWith := binary.BigEndian.Uint32(buf2.Bytes()[8:12])

	// Providing haves should reduce the object count.
	assert.Less(t, countWith, countWithout)
}
