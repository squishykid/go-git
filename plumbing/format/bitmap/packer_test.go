package bitmap

import (
	"bytes"
	"crypto"
	"encoding/binary"
	"fmt"
	"io"
	"testing"

	"github.com/go-git/go-billy/v6/osfs"
	fixtures "github.com/go-git/go-git-fixtures/v6"
	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/format/idxfile"
	"github.com/go-git/go-git/v6/plumbing/format/packfile"
	"github.com/go-git/go-git/v6/plumbing/hash"
	"github.com/go-git/go-git/v6/plumbing/revlist"
	"github.com/go-git/go-git/v6/plumbing/storer"
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

func (s *testPackSource) FindPosition(h plumbing.Hash) (idxPos, packPos uint32, ok bool) {
	packPos, ok = s.hashToOffset[h]
	if !ok {
		return 0, 0, false
	}
	idxPos = s.offsetToIdx[packPos]
	return idxPos, packPos, true
}

func (s *testPackSource) Object(packPos uint32) (plumbing.EncodedObject, error) {
	if int(packPos) >= len(s.offsetToIdx) {
		return nil, plumbing.ErrObjectNotFound
	}
	idxPos := s.offsetToIdx[packPos]
	return s.pf.Get(s.idxToHash[idxPos])
}

func (s *testPackSource) ObjectType(packPos uint32) plumbing.ObjectType {
	obj, err := s.Object(packPos)
	if err != nil {
		return plumbing.InvalidObject
	}
	return obj.Type()
}

const revFileHeader = 12 // 4 sig + 4 version + 4 hash version

func openPackSource(t testing.TB) *testPackSource {
	t.Helper()
	return openPackSourceByURL(t, "https://github.com/go-git/go-git.git", crypto.SHA1)
}

func openPackSourceByURL(t testing.TB, url string, h crypto.Hash) *testPackSource {
	t.Helper()
	q := fixtures.ByTag("bitmap").ByURL(url).One()

	// Decode pack index.
	idxFile, err := q.Idx()
	require.NoError(t, err)
	defer idxFile.Close()

	idx := idxfile.NewMemoryIndex(h.Size())
	require.NoError(t, idxfile.NewDecoder(idxFile, hash.New(h)).Decode(idx))

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
		ip := binary.BigEndian.Uint32(revData[revFileHeader+i*4:])
		offsetToIdx[i] = ip
		idxToOffset[ip] = uint32(i)
	}

	// Build hash → pack-offset-position table.
	hashToOffset := make(map[plumbing.Hash]uint32, n)
	for ip, h := range idxToHash {
		hashToOffset[h] = idxToOffset[ip]
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
func (s *testPackSource) hashAtOffset(packPos uint32) plumbing.Hash {
	return s.idxToHash[s.offsetToIdx[packPos]]
}

// hashAtIdx returns the object hash at the given idx (hash-sorted) position.
func (s *testPackSource) hashAtIdx(idxPos uint32) plumbing.Hash {
	return s.idxToHash[idxPos]
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
	h0 := src.hashAtIdx(e0.ObjectPosition)

	bm, err := p.Negotiate([]plumbing.Hash{h0}, nil)
	require.NoError(t, err)
	require.NotNil(t, bm)

	// Without haves the result should match the raw reachability bitmap.
	expected, err := s.Reachable(e0.ObjectPosition)
	require.NoError(t, err)
	assert.Equal(t, expected, bm)
}

func TestReachabilityMissing(t *testing.T) {
	t.Parallel()

	bitmapIdx := openFixture(t)
	src := openPackSource(t)
	s := NewSearcher(bitmapIdx)
	p := NewPacker(s, src, hash.New(crypto.SHA1))

	e0 := bitmapIdx.Entry(0)
	valid := src.hashAtIdx(e0.ObjectPosition)
	bogus1, _ := plumbing.FromHex("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	bogus2, _ := plumbing.FromHex("bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")

	// Reachability should succeed and report the bogus hashes as missing.
	bm, missing, err := p.Reachability([]plumbing.Hash{valid, bogus1, bogus2})
	require.NoError(t, err)

	// The valid commit's reachability bitmap should be populated.
	setBits := 0
	it := bm.SetBits()
	for _, ok := it.Next(); ok; _, ok = it.Next() {
		setBits++
	}
	assert.Greater(t, setBits, 10)

	// Both bogus hashes should be in the missing slice.
	missingSet := make(map[string]struct{}, len(missing))
	for _, h := range missing {
		missingSet[h.String()] = struct{}{}
	}
	assert.Contains(t, missingSet, bogus1.String())
	assert.Contains(t, missingSet, bogus2.String())

	// The valid hash should NOT be in missing.
	assert.NotContains(t, missingSet, valid.String())
}

func TestPackerNegotiateWithHaves(t *testing.T) {
	t.Parallel()

	bitmapIdx := openFixture(t)
	src := openPackSource(t)
	s := NewSearcher(bitmapIdx)
	p := NewPacker(s, src, hash.New(crypto.SHA1))

	e0 := bitmapIdx.Entry(0)
	h0 := src.hashAtIdx(e0.ObjectPosition)

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
	h0 := src.hashAtIdx(e0.ObjectPosition)

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
	h0 := src.hashAtIdx(e0.ObjectPosition)
	e1 := bitmapIdx.Entry(1)
	h1 := src.hashAtIdx(e1.ObjectPosition)

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

func openReadOnlyStorer(t testing.TB) *readOnlyStorer {
	t.Helper()
	q := fixtures.ByTag("bitmap").ByURL("https://github.com/go-git/go-git.git").One()
	idxFile, err := q.Idx()
	require.NoError(t, err)
	defer idxFile.Close()
	idx := idxfile.NewMemoryIndex(crypto.SHA1.Size())
	require.NoError(t, idxfile.NewDecoder(idxFile, hash.New(crypto.SHA1)).Decode(idx))

	packFile, err := q.Packfile()
	require.NoError(t, err)
	pf := packfile.NewPackfile(packFile,
		packfile.WithIdx(idx),
		packfile.WithFs(osfs.New(t.TempDir())),
	)
	t.Cleanup(func() { pf.Close() })
	return &readOnlyStorer{pf: pf}
}

func TestNegotiateMatchesRevlistObjects(t *testing.T) {
	t.Parallel()

	bitmapIdx := openFixture(t)
	src := openPackSource(t)
	sto := openReadOnlyStorer(t)

	s := NewSearcher(bitmapIdx)
	p := NewPacker(s, src, hash.New(crypto.SHA1))

	e0 := bitmapIdx.Entry(0)
	want := src.hashAtIdx(e0.ObjectPosition)

	// Bitmap path.
	bm, err := p.Negotiate([]plumbing.Hash{want}, nil)
	require.NoError(t, err)

	maxPos := uint32(src.ObjectCount())
	bitmapSet := make(map[string]struct{})
	it := bm.SetBits()
	for pos, ok := it.Next(); ok; pos, ok = it.Next() {
		if pos < maxPos {
			bitmapSet[src.hashAtOffset(pos).String()] = struct{}{}
		}
	}

	// Graph-walk path.
	revlistHashes, err := revlist.Objects(sto, []plumbing.Hash{want}, nil)
	require.NoError(t, err)

	revlistSet := make(map[string]struct{}, len(revlistHashes))
	for _, h := range revlistHashes {
		revlistSet[h.String()] = struct{}{}
	}

	t.Logf("both produced %d objects", len(bitmapSet))
	assert.Equal(t, len(revlistSet), len(bitmapSet))

	for h := range revlistSet {
		assert.Contains(t, bitmapSet, h, "revlist object missing from bitmap result")
	}
}

func TestNegotiateWalkMatchesRevlistObjects(t *testing.T) {
	t.Parallel()

	bitmapIdx := openFixture(t)
	src := openPackSource(t)
	sto := openReadOnlyStorer(t)

	s := NewSearcher(bitmapIdx)
	p := NewPacker(s, src, hash.New(crypto.SHA1))
	wants, haves := benchBitmapMiss()

	// Bitmap path (walks graph until hitting a bitmap entry).
	bm, err := p.Negotiate(wants, haves)
	require.NoError(t, err)

	maxPos := uint32(src.ObjectCount())
	bitmapSet := make(map[string]struct{})
	it := bm.SetBits()
	for pos, ok := it.Next(); ok; pos, ok = it.Next() {
		if pos < maxPos {
			bitmapSet[src.hashAtOffset(pos).String()] = struct{}{}
		}
	}

	// Graph-walk path.
	revlistHashes, err := revlist.Objects(sto, wants, haves)
	require.NoError(t, err)

	revlistSet := make(map[string]struct{}, len(revlistHashes))
	for _, h := range revlistHashes {
		revlistSet[h.String()] = struct{}{}
	}

	t.Logf("bitmap: %d objects, revlist: %d objects", len(bitmapSet), len(revlistSet))

	// The bitmap result must contain every object revlist found.
	for h := range revlistSet {
		assert.Contains(t, bitmapSet, h, "revlist object missing from bitmap result")
	}
}

// readOnlyStorer wraps a packfile.Packfile to satisfy
// storer.EncodedObjectStorer for read-only benchmarking.
type readOnlyStorer struct{ pf *packfile.Packfile }

func (s *readOnlyStorer) EncodedObject(t plumbing.ObjectType, h plumbing.Hash) (plumbing.EncodedObject, error) {
	obj, err := s.pf.Get(h)
	if err != nil {
		return nil, err
	}
	if t != plumbing.AnyObject && obj.Type() != t {
		return nil, plumbing.ErrObjectNotFound
	}
	return obj, nil
}

func (s *readOnlyStorer) IterEncodedObjects(t plumbing.ObjectType) (storer.EncodedObjectIter, error) {
	return s.pf.GetByType(t)
}

func (s *readOnlyStorer) HasEncodedObject(h plumbing.Hash) error {
	_, err := s.pf.Get(h)
	return err
}

func (s *readOnlyStorer) EncodedObjectSize(h plumbing.Hash) (int64, error) {
	obj, err := s.pf.Get(h)
	if err != nil {
		return 0, err
	}
	return obj.Size(), nil
}

func (s *readOnlyStorer) NewEncodedObject() plumbing.EncodedObject {
	return &plumbing.MemoryObject{}
}

func (s *readOnlyStorer) SetEncodedObject(plumbing.EncodedObject) (plumbing.Hash, error) {
	return plumbing.ZeroHash, fmt.Errorf("read-only")
}

func (s *readOnlyStorer) RawObjectWriter(plumbing.ObjectType, int64) (io.WriteCloser, error) {
	return nil, fmt.Errorf("read-only")
}

func (s *readOnlyStorer) AddAlternate(string) error { return nil }

// benchWantHave returns want/have hashes for a client ~1 week behind.
// Entry 0 is the newest commit (2026-04-08), entry 77 is ~7 days
// earlier (2026-04-01).
func benchWantHave(b *testing.B, idx *Index, src *testPackSource) (want, have plumbing.Hash) {
	b.Helper()
	want = src.hashAtIdx(idx.Entry(0).ObjectPosition)
	have = src.hashAtIdx(idx.Entry(77).ObjectPosition)
	return want, have
}

// benchBitmapMiss returns want/have hashes for commits that do NOT
// have precomputed bitmap entries. Simulates a client ~1 week behind
// tracking several branches.
//
//	wants: 5 commits from 2026-03-28 to 2026-03-30
//	haves: 5 commits from 2026-03-24
func benchBitmapMiss() (wants, haves []plumbing.Hash) {
	hex := func(s string) plumbing.Hash { h, _ := plumbing.FromHex(s); return h }
	wants = []plumbing.Hash{
		hex("949b9bb475494424d72adf28a4ab312703611b7d"),
		hex("a7d9bf9aa32136dd22aba3c6ec218c6c7b27f475"),
		hex("616469c3006bae526e37a9c349ee1ebe8c708b88"),
		hex("91495350c82f8d3a5633354604e5f8e12be99a7f"),
		hex("a93bccd59f82c947ede2c9d0e0062bc04e96c998"),
	}
	haves = []plumbing.Hash{
		hex("cd85c8c75d344dfcc571c7e8897106a5e8622a58"),
		hex("7002a0e5456172fa4d81a39996501146942e1ed9"),
		hex("b1b844577fd751ad66e272426fc961494523f756"),
		hex("a629a31674dd7da7bbfb2a18ab416ca6fec6c486"),
		hex("772c1ee4f817aafd64544a9ac1180b2fe88ed29e"),
	}
	return wants, haves
}

func BenchmarkNegotiate(b *testing.B) {
	bitmapIdx := openFixture(b)
	src := openPackSource(b)
	s := NewSearcher(bitmapIdx)
	want, have := benchWantHave(b, bitmapIdx, src)

	b.ResetTimer()
	for b.Loop() {
		p := NewPacker(s, src, hash.New(crypto.SHA1))
		_, err := p.Negotiate([]plumbing.Hash{want}, []plumbing.Hash{have})
		if err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkNegotiateWalk(b *testing.B) {
	bitmapIdx := openFixture(b)
	src := openPackSource(b)
	s := NewSearcher(bitmapIdx)

	wants, haves := benchBitmapMiss()

	b.ResetTimer()
	for b.Loop() {
		p := NewPacker(s, src, hash.New(crypto.SHA1))
		_, err := p.Negotiate(wants, haves)
		if err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkRevlistObjectsWalk(b *testing.B) {
	sto := openReadOnlyStorer(b)
	wants, haves := benchBitmapMiss()

	b.ResetTimer()
	for b.Loop() {
		_, err := revlist.Objects(sto, wants, haves)
		if err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkRevlistObjects(b *testing.B) {
	bitmapIdx := openFixture(b)
	src := openPackSource(b)
	sto := openReadOnlyStorer(b)
	want, have := benchWantHave(b, bitmapIdx, src)

	b.ResetTimer()
	for b.Loop() {
		_, err := revlist.Objects(sto, []plumbing.Hash{want}, []plumbing.Hash{have})
		if err != nil {
			b.Fatal(err)
		}
	}
}
