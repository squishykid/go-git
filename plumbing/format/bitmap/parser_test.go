package bitmap

import (
	"crypto"
	"testing"

	fixtures "github.com/go-git/go-git-fixtures/v6"
	"github.com/go-git/go-git/v6/plumbing/hash"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDecode(t *testing.T) {
	t.Parallel()

	q := fixtures.ByTag("bitmap")[0]
	f, err := q.Bitmap()

	//f, err := os.Open(sampleBitmap)
	require.NoError(t, err)
	defer f.Close()

	h := hash.New(crypto.SHA1)
	d := NewDecoder(f, h)

	var idx Index
	err = d.Decode(&idx)
	require.NoError(t, err)

	assert.Equal(t, uint16(1), idx.Version)
	assert.Equal(t, uint16(OptFullDAG|OptHashCache), idx.Flags)

	// Pack checksum should match the filename.
	assert.Equal(t, q.PackfileHash, idx.PackChecksum.String())

	// 140 bitmap entries.
	assert.Len(t, idx.Entries, 140)

	// Type bitmaps should be present.
	assert.NotNil(t, idx.Commits)
	assert.NotNil(t, idx.Trees)
	assert.NotNil(t, idx.Blobs)
	assert.NotNil(t, idx.Tags)

	// Type bitmap bit counts.
	assert.Equal(t, uint32(6731), idx.Commits.Bits())
	assert.Equal(t, uint32(25072), idx.Trees.Bits())
	assert.Equal(t, uint32(25061), idx.Blobs.Bits())
	assert.Equal(t, uint32(0), idx.Tags.Bits())

	// Name-hash cache should have one entry per object in the pack.
	assert.Len(t, idx.NameHashCache, 25072)
}

func TestDecodeEntries(t *testing.T) {
	t.Parallel()

	q := fixtures.ByTag("bitmap").ByURL("https://github.com/go-git/go-git.git").One()
	f, err := q.Bitmap()
	require.NoError(t, err)
	defer f.Close()

	h := hash.New(crypto.SHA1)
	d := NewDecoder(f, h)

	var idx Index
	err = d.Decode(&idx)
	require.NoError(t, err)

	// First entry.
	e := idx.Entries[0]
	assert.Equal(t, uint32(2393), e.ObjectPosition)
	assert.Equal(t, uint8(0), e.XOROffset)
	assert.Equal(t, uint8(0), e.Flags)
	assert.NotNil(t, e.Bitmap)
	assert.Equal(t, uint32(25088), e.Bitmap.Bits())

	// Second entry has XOR offset 1.
	e = idx.Entries[1]
	assert.Equal(t, uint32(12044), e.ObjectPosition)
	assert.Equal(t, uint8(1), e.XOROffset)
}

func BenchmarkDecode(b *testing.B) {
	q := fixtures.ByTag("bitmap").ByURL("https://github.com/go-git/go-git.git").One()

	for b.Loop() {
		f, err := q.Bitmap()
		require.NoError(b, err)

		h := hash.New(crypto.SHA1)
		d := NewDecoder(f, h)

		var idx Index
		err = d.Decode(&idx)
		require.NoError(b, err)

		f.Close()
	}
}

func TestDecodeInvalidSignature(t *testing.T) {
	t.Parallel()

	q := fixtures.ByTag("bitmap").ByURL("https://github.com/go-git/go-git.git").One()
	f, err := q.Idx()
	require.NoError(t, err)
	defer f.Close()

	h := hash.New(crypto.SHA1)
	d := NewDecoder(f, h)

	var idx Index
	err = d.Decode(&idx)
	assert.ErrorIs(t, err, ErrInvalidSignature)
}
