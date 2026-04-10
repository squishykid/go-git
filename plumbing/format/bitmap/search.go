package bitmap

import (
	"errors"
	"fmt"
)

// ErrNoEntry is returned when the given index position has no bitmap entry.
var ErrNoEntry = errors.New("no bitmap entry for index position")

// Searcher provides object reachability lookups using a bitmap index.
type Searcher struct {
	idx *Index

	// entryIndex maps ObjectPosition (idx position) to the entry
	// ordinal in the bitmap file.
	// TODO: optimise with scannedOffsets
	entryIndex map[uint32]int
	// cache holds decompressed and XOR-resolved bitmaps, keyed by
	// entry ordinal. Populated lazily on first access.
	cache []Bitmap
}

// NewSearcher builds a Searcher from a bitmap Index.
func NewSearcher(bitmapIdx *Index) *Searcher {
	entryCount := int(bitmapIdx.EntryCount())
	entryIndex := make(map[uint32]int, entryCount)
	for i := range entryCount {
		entryIndex[bitmapIdx.entries.commitPosition(i)] = i
	}

	return &Searcher{
		idx:        bitmapIdx,
		entryIndex: entryIndex,
		cache:      make([]Bitmap, entryCount),
	}
}

// Reachable returns the decompressed reachability bitmap for the commit
// at the given pack index position. The bitmap has one bit per object
// in pack-offset order. Returns [ErrNoEntry] if the position has no
// bitmap entry.
func (s *Searcher) Reachable(idxPos uint32) (Bitmap, error) {
	ei, ok := s.entryIndex[idxPos]
	if !ok {
		return nil, ErrNoEntry
	}
	return s.resolve(ei)
}

// resolve returns the decompressed, XOR-resolved bitmap for the entry
// at the given ordinal, populating the cache on first access.
func (s *Searcher) resolve(ordinal int) (Bitmap, error) {
	if bm := s.cache[ordinal]; bm != nil {
		return bm, nil
	}

	// Walk backwards through the XOR chain to find the first entry
	// that is either already cached or has no XOR dependency.
	var chain []int
	for cur := ordinal; ; {
		if s.cache[cur] != nil {
			break
		}
		chain = append(chain, cur)

		base := s.idx.entries.xorBase(cur)
		if base < 0 {
			break
		}
		cur = base
	}

	// Walk the chain forwards, decompressing and XOR-resolving each
	// entry against the previous one.
	for i := len(chain) - 1; i >= 0; i-- {
		cur := chain[i]
		e := s.idx.Entry(cur)

		bm, err := DecodeEWAH(e.Bitmap)
		if err != nil {
			return nil, fmt.Errorf("decompressing entry %d: %w", cur, err)
		}

		if base := s.idx.entries.xorBase(cur); base >= 0 {
			bm = Extend(bm, s.cache[base])
			bm.Xor(s.cache[base])
		}

		s.cache[cur] = bm
	}

	return s.cache[ordinal], nil
}

// ReachableCommits returns an iterator over the pack index positions of
// all commit objects that are set in bm. It intersects bm with the
// commits type bitmap from the index.
func (s *Searcher) ReachableCommits(bm Bitmap) (*SetBitsIterator, error) {
	commits, err := DecodeEWAH(s.idx.Commits())
	if err != nil {
		return nil, fmt.Errorf("decompressing commits bitmap: %w", err)
	}
	result := make(Bitmap, len(bm))
	copy(result, bm)
	result.And(commits)
	return result.SetBits(), nil
}
