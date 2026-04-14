package revfile

import (
	encbin "encoding/binary"
	"io"

	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/format/idxfile"
)

// RevIndex extends [Index] with O(log n) bidirectional lookup
// between an object's hash and its two MemoryRevIndex positions in the pack:
//
//   - idxPos: the 0-based rank in hash-sorted (.idx) order. This is
//     what pack bitmap entry headers reference.
//   - packPos: the 0-based rank in pack-offset order (objects sorted by
//     their byte offset in the .pack file). This is the position space
//     used by pack bitmap bits. Physical order in the packfile.
//
// Both positions are derivable from [Index] alone (idxPos via
// [Index.Entries], packPos via [Index.EntriesByOffset]) but the
// iterator-based approach is O(n). PositionedIndex implementations
// expose these lookups directly so callers building bitmap indices, or
// otherwise needing random access, can avoid the upfront walk.
type RevIndex interface {
	idxfile.Index
	// HashAtIdxRank returns the hash of the object at the given
	// hash-sorted position.
	HashAtIdxRank(idxPos uint32) (plumbing.Hash, bool)
	// HashAtPackRank returns the hash of the object at the given
	// pack-offset position.
	HashAtPackRank(packPos uint32) (plumbing.Hash, bool)
	// IdxPosAtPackRank returns the index position for the object
	// at the given pack rank.
	IdxPosAtPackRank(packRank uint32) (uint32, bool)
	// FindHashRank returns the rank position of the object
	// in the packfile, or [false].
	FindHashRank(h plumbing.Hash) (uint32, bool)
}

type MemoryRevIndex struct {
	*idxfile.MemoryIndex
	rev         []uint32 // packPos → idxPos
	packOffsets []int64  // packPos → byte offset in packfile
	hashSize    int
}

var _ RevIndex = (*MemoryRevIndex)(nil)

func NewMemoryRevIndex(index *idxfile.MemoryIndex, rev []uint32, hashSize int) *MemoryRevIndex {
	// Build a flat idxPos → offset table by walking the fanout.
	idxToOffset := make([]int64, len(rev))
	i := uint32(0)
	for firstLevel, fanoutValue := range index.Fanout {
		mappedFirstLevel := index.FanoutMapping[firstLevel]
		for secondLevel := uint32(0); i < fanoutValue; i++ {
			idxToOffset[i] = idxOffset(index, mappedFirstLevel, int(secondLevel))
			secondLevel++
		}
	}

	// Remap to pack-offset order: packOffsets[packPos] = offset.
	packOffsets := make([]int64, len(rev))
	for p, idxPos := range rev {
		packOffsets[p] = idxToOffset[idxPos]
	}

	return &MemoryRevIndex{MemoryIndex: index, rev: rev, packOffsets: packOffsets, hashSize: hashSize}
}

func Decode2(r io.Reader, count int64, packChecksum plumbing.ObjectID) ([]uint32, error) {
	idxPos := make(chan uint32)
	var got []uint32
	errCh := make(chan error, 1)
	go func() {
		errCh <- Decode(r, count, packChecksum, idxPos)
	}()

	for pos := range idxPos {
		got = append(got, pos)
	}

	err := <-errCh
	return got, err
}

func (o *MemoryRevIndex) FindHashRank(h plumbing.Hash) (uint32, bool) {
	offset, err := o.MemoryIndex.FindOffset(h)
	if err != nil {
		return 0, false
	}

	n := uint32(len(o.rev))
	lo, hi := uint32(0), n
	for lo < hi {
		mid := (lo + hi) / 2
		if o.packOffsets[mid] < offset {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	return lo, lo < n
}

func (o *MemoryRevIndex) HashAtIdxRank(idxPos uint32) (plumbing.Hash, bool) {
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

func (o *MemoryRevIndex) HashAtPackRank(packRank uint32) (plumbing.Hash, bool) {
	if int(packRank) >= len(o.rev) {
		return plumbing.ZeroHash, false
	}
	return o.HashAtIdxRank(o.rev[packRank])
}

func (o *MemoryRevIndex) IdxPosAtPackRank(packRank uint32) (uint32, bool) {
	if int(packRank) >= len(o.rev) {
		return 0, false
	}
	return o.rev[packRank], true
}

const isO64Mask = uint64(1) << 31

// idxOffset reads the pack byte-offset for the entry at
// (firstLevel, secondLevel) in the idx file's offset tables.
func idxOffset(idx *idxfile.MemoryIndex, firstLevel, secondLevel int) int64 {
	off := secondLevel << 2
	ofs := encbin.BigEndian.Uint32(idx.Offset32[firstLevel][off : off+4])
	if (uint64(ofs) & isO64Mask) != 0 {
		off64 := 8 * (uint64(ofs) & ^isO64Mask)
		return int64(encbin.BigEndian.Uint64(idx.Offset64[off64 : off64+8]))
	}
	return int64(ofs)
}
