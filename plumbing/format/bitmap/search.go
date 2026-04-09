package bitmap

import (
	"fmt"

	"github.com/go-git/go-git/v6/plumbing"
)

// PackIndex provides the pack metadata needed by the Searcher to map
// bitmap bit positions to object hashes. Implementations must support
// lookup by both idx position (sorted hash order) and pack-offset
// position (reverse index order).
type PackIndex interface {
	// ObjectCount returns the total number of objects in the pack.
	ObjectCount() int
	// ObjectID returns the object hash at the given idx position
	// (sorted hash order, 0-indexed).
	ObjectID(idxPos int) plumbing.Hash
	// IdxPositionAtOffset returns the idx position of the object at
	// the given pack-offset position (reverse index order, 0-indexed).
	IdxPositionAtOffset(packPos int) int
}

// Searcher provides object reachability lookups using a bitmap index
// combined with a pack index.
type Searcher struct {
	idx  Index
	pack PackIndex

	// entryIndex maps ObjectPosition (idx position) to the entry
	// ordinal in the bitmap file.
	entryIndex map[uint32]int
	// cache holds decompressed and XOR-resolved bitmaps, keyed by
	// entry ordinal. Populated lazily on first access.
	cache []Bitmap
}

// NewSearcher builds a Searcher from a bitmap Index and a PackIndex.
func NewSearcher(bitmapIdx Index, pack PackIndex) *Searcher {
	entryCount := int(bitmapIdx.EntryCount())
	entryIndex := make(map[uint32]int, entryCount)
	for i := range entryCount {
		entryIndex[bitmapIdx.Entry(i).ObjectPosition] = i
	}

	return &Searcher{
		idx:        bitmapIdx,
		pack:       pack,
		entryIndex: entryIndex,
		cache:      make([]Bitmap, entryCount),
	}
}

// Reachable returns the hashes of all objects reachable from the given
// commit. Returns plumbing.ErrObjectNotFound if the commit has no
// bitmap entry.
func (s *Searcher) Reachable(commit plumbing.Hash) ([]plumbing.Hash, error) {
	// Find the commit's idx position by scanning the entry index.
	idxPos, ok := s.findIdxPos(commit)
	if !ok {
		return nil, plumbing.ErrObjectNotFound
	}

	ei, ok := s.entryIndex[uint32(idxPos)]
	if !ok {
		return nil, plumbing.ErrObjectNotFound
	}

	bm, err := s.resolve(ei)
	if err != nil {
		return nil, err
	}

	count := s.pack.ObjectCount()
	var result []plumbing.Hash
	for packPos := 0; packPos < count; packPos++ {
		if bm.Get(uint32(packPos)) {
			idxP := s.pack.IdxPositionAtOffset(packPos)
			result = append(result, s.pack.ObjectID(idxP))
		}
	}
	return result, nil
}

// findIdxPos finds the idx position for the given hash by checking
// which entry has a matching ObjectPosition.
func (s *Searcher) findIdxPos(h plumbing.Hash) (int, bool) {
	count := s.pack.ObjectCount()
	for i := range count {
		if s.pack.ObjectID(i).Equal(h) {
			return i, true
		}
	}
	return 0, false
}

// resolve returns the decompressed, XOR-resolved bitmap for the entry
// at the given ordinal, populating the cache on first access.
func (s *Searcher) resolve(ordinal int) (Bitmap, error) {
	if bm := s.cache[ordinal]; bm != nil {
		return bm, nil
	}

	e := s.idx.Entry(ordinal)
	bm, err := DecodeEWAH(e.Bitmap)
	if err != nil {
		return nil, fmt.Errorf("decompressing entry %d: %w", ordinal, err)
	}

	if e.XOROffset > 0 {
		base := ordinal - int(e.XOROffset)
		if base < 0 {
			return nil, fmt.Errorf("%w: entry %d references offset %d",
				ErrInvalidXOROffset, ordinal, e.XOROffset)
		}
		baseBm, err := s.resolve(base)
		if err != nil {
			return nil, err
		}
		bm = xorBitmaps(bm, baseBm)
	}

	s.cache[ordinal] = bm
	return bm, nil
}

// xorBitmaps returns a new Bitmap where each byte is a XOR b.
func xorBitmaps(a, b Bitmap) Bitmap {
	n := max(len(a), len(b))
	out := make(Bitmap, n)
	copy(out, a)
	for i := range min(len(out), len(b)) {
		out[i] ^= b[i]
	}
	if len(b) > len(a) {
		copy(out[len(a):], b[len(a):])
	}
	return out
}
