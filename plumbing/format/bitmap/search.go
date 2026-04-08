package bitmap

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"

	"github.com/erizocosmico/go-ewah"
	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/format/idxfile"
)

// bitset is a flat uncompressed bitmap stored as a slice of uint64 words.
// Bit i is stored in words[i/64] at position (i%64), using LSB-first
// ordering to match git's bitmap convention.
type bitset struct {
	bits  uint32
	words []uint64
}

// get returns the value of bit at position pos.
func (b *bitset) get(pos uint32) bool {
	if pos >= b.bits {
		return false
	}
	w := pos / 64
	bit := pos % 64
	return b.words[w]&(1<<bit) != 0
}

// xor modifies b in place: b = b XOR other.
func (b *bitset) xor(other *bitset) {
	for i := range min(len(b.words), len(other.words)) {
		b.words[i] ^= other.words[i]
	}
	if len(other.words) > len(b.words) {
		b.words = append(b.words, other.words[len(b.words):]...)
	}
	b.bits = max(b.bits, other.bits)
}

// Searcher provides object reachability lookups using a bitmap index
// combined with a pack index and reverse index.
type Searcher struct {
	// packOrderHashes maps pack-offset position to object hash.
	// Bitmap bits are indexed by pack-offset position.
	packOrderHashes []plumbing.Hash
	// entryBitmaps maps entry ObjectPosition (idx position) to the
	// resolved reachability bitset.
	entryBitmaps map[uint32]*bitset
	// hashToIdxPos maps object hash to its pack index position.
	hashToIdxPos map[plumbing.Hash]uint32
}

// NewSearcher builds a Searcher by combining a decoded bitmap Index with
// the corresponding pack Index and reverse index.
//
// packOrder maps pack-offset position to pack index position, as
// decoded from the reverse index (.rev) file. It must have one entry
// per object in the pack.
func NewSearcher(bitmapIdx *Index, packIdx idxfile.Index, packOrder []uint32) (*Searcher, error) {
	count, err := packIdx.Count()
	if err != nil {
		return nil, fmt.Errorf("reading pack index count: %w", err)
	}

	// Build idx position → hash mapping.
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

	// Build pack-offset-order hash list using the reverse index.
	packOrderHashes := make([]plumbing.Hash, len(packOrder))
	for packPos, idxPos := range packOrder {
		packOrderHashes[packPos] = idxHashes[idxPos]
	}

	bitmaps, err := resolveEntries(bitmapIdx.Entries)
	if err != nil {
		return nil, err
	}

	return &Searcher{
		packOrderHashes: packOrderHashes,
		entryBitmaps:    bitmaps,
		hashToIdxPos:    hashToIdxPos,
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

	bs, ok := s.entryBitmaps[idxPos]
	if !ok {
		return nil, plumbing.ErrObjectNotFound
	}

	var result []plumbing.Hash
	for i := uint32(0); i < uint32(len(s.packOrderHashes)); i++ {
		if bs.get(i) {
			result = append(result, s.packOrderHashes[i])
		}
	}
	return result, nil
}

// resolveEntries resolves XOR compression across all entries and returns
// a map from ObjectPosition (idx position) to the fully resolved bitset.
func resolveEntries(entries []Entry) (map[uint32]*bitset, error) {
	resolved := make([]*bitset, len(entries))
	out := make(map[uint32]*bitset, len(entries))

	for i, e := range entries {
		bs, err := decompress(e.Bitmap)
		if err != nil {
			return nil, fmt.Errorf("decompressing entry %d: %w", i, err)
		}
		if e.XOROffset > 0 {
			base := i - int(e.XOROffset)
			if base < 0 || base >= len(resolved) || resolved[base] == nil {
				return nil, fmt.Errorf("%w: entry %d references offset %d",
					ErrInvalidXOROffset, i, e.XOROffset)
			}
			bs.xor(resolved[base])
		}
		resolved[i] = bs
		out[e.ObjectPosition] = bs
	}
	return out, nil
}

// decompress converts an EWAH compressed bitmap into a flat bitset by
// walking the serialized RLW/literal structure.
func decompress(bm *ewah.Bitmap) (*bitset, error) {
	var buf bytes.Buffer
	if _, err := bm.Write(&buf, binary.BigEndian); err != nil {
		return nil, err
	}
	data := buf.Bytes()
	if len(data) < 12 {
		return nil, fmt.Errorf("ewah data too short: %d bytes", len(data))
	}

	bits := binary.BigEndian.Uint32(data[0:4])
	wordCount := binary.BigEndian.Uint32(data[4:8])

	nWords := (bits + 63) / 64
	words := make([]uint64, nWords)

	pos := uint32(0) // position in uncompressed output (in words)
	i := uint32(0)   // position in compressed word array

	for i < wordCount {
		if 8+int(i)*8+8 > len(data) {
			break
		}
		rlw := binary.BigEndian.Uint64(data[8+i*8 : 8+i*8+8])
		i++

		fillBit := rlw >> 63
		k := uint32((rlw >> 31) & 0xFFFFFFFF)
		l := uint32(rlw & 0x7FFFFFFF)

		var fillWord uint64
		if fillBit != 0 {
			fillWord = ^uint64(0)
		}
		for j := uint32(0); j < k && pos < nWords; j++ {
			words[pos] = fillWord
			pos++
		}

		for j := uint32(0); j < l && pos < nWords; j++ {
			if 8+int(i)*8+8 > len(data) {
				break
			}
			words[pos] = binary.BigEndian.Uint64(data[8+i*8 : 8+i*8+8])
			pos++
			i++
		}
	}

	return &bitset{bits: bits, words: words}, nil
}