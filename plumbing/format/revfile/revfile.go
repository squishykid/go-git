package revfile

import (
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
}

type MemoryRevIndex struct {
	*idxfile.MemoryIndex
	rev      []uint32 // packPos → idxPos
	hashSize int
}

var _ RevIndex = (*MemoryRevIndex)(nil)

func NewMemoryRevIndex(index *idxfile.MemoryIndex, rev []uint32, hashSize int) *MemoryRevIndex {
	return &MemoryRevIndex{MemoryIndex: index, rev: rev, hashSize: hashSize}
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
