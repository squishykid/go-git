package bitmap

import (
	"fmt"
	"io"

	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/format/idxfile"
)

// Searcher provides object reachability lookups using a bitmap index
// combined with a pack index and reverse index.
type Searcher struct {
	idx      BitmapIndex
	hashSize int

	// packOrderHashes maps pack-offset position to object hash.
	packOrderHashes []plumbing.Hash
	// hashToIdxPos maps object hash to its pack index position.
	hashToIdxPos map[plumbing.Hash]uint32

	// entryIndex maps ObjectPosition (idx position) to the entry
	// ordinal in the bitmap file.
	entryIndex map[uint32]int
	// cache holds decompressed and XOR-resolved bitmaps, keyed by
	// entry ordinal. Populated lazily on first access.
	cache []Bitmap
}

// NewSearcher builds a Searcher by combining a decoded bitmap BitmapIndex with
// the corresponding pack BitmapIndex and reverse index.
//
// packOrder maps pack-offset position to pack index position, as
// decoded from the reverse index (.rev) file. It must have one entry
// per object in the pack.
func NewSearcher(bitmapIdx BitmapIndex, hashSize int, packIdx idxfile.Index, packOrder []uint32) (*Searcher, error) {
	count, err := packIdx.Count()
	if err != nil {
		return nil, fmt.Errorf("reading pack index count: %w", err)
	}

	idxHashes := make([]plumbing.Hash, 0, count)
	hashToIdxPos := make(map[plumbing.Hash]uint32, count)

	iter, err := packIdx.Entries()
	if err != nil {
		return nil, fmt.Errorf("reading pack index entries: %w", err)
	}
	defer iter.Close()

	for pos := uint32(0); ; pos++ {
		entry, err := iter.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("reading pack index entry %d: %w", pos, err)
		}
		idxHashes = append(idxHashes, entry.Hash)
		hashToIdxPos[entry.Hash] = pos
	}

	packOrderHashes := make([]plumbing.Hash, len(packOrder))
	for packPos, idxPos := range packOrder {
		packOrderHashes[packPos] = idxHashes[idxPos]
	}

	entryCount := int(bitmapIdx.EntryCount())
	entryIndex := make(map[uint32]int, entryCount)
	off := bitmapIdx.entriesOffset(hashSize)
	for i := range entryCount {
		e := parseEntry(bitmapIdx[off:])
		entryIndex[e.ObjectPosition] = i
		off += entrySize(bitmapIdx[off:])
	}

	return &Searcher{
		idx:             bitmapIdx,
		hashSize:        hashSize,
		packOrderHashes: packOrderHashes,
		hashToIdxPos:    hashToIdxPos,
		entryIndex:      entryIndex,
		cache:           make([]Bitmap, entryCount),
	}, nil
}

// Reachable returns the hashes of all objects reachable from the given
// commit. Returns plumbing.ErrObjectNotFound if the commit has no
// bitmap entry.
func (s *Searcher) Reachable(commit plumbing.Hash) ([]plumbing.Hash, error) {
	idxPos, ok := s.hashToIdxPos[commit]
	if !ok {
		return nil, plumbing.ErrObjectNotFound
	}

	ei, ok := s.entryIndex[idxPos]
	if !ok {
		return nil, plumbing.ErrObjectNotFound
	}

	bm, err := s.resolve(ei)
	if err != nil {
		return nil, err
	}

	var result []plumbing.Hash
	for i := uint32(0); i < uint32(len(s.packOrderHashes)); i++ {
		if bm.Get(i) {
			result = append(result, s.packOrderHashes[i])
		}
	}
	return result, nil
}

// resolve returns the decompressed, XOR-resolved bitmap for the entry
// at the given ordinal, populating the cache on first access.
func (s *Searcher) resolve(ordinal int) (Bitmap, error) {
	if bm := s.cache[ordinal]; bm != nil {
		return bm, nil
	}

	e := s.idx.Entry(s.hashSize, ordinal)
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
