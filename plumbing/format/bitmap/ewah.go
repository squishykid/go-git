package bitmap

import (
	"encoding/binary"
	"fmt"
)

// EWAH holds the raw on-disk EWAH-compressed bitmap data:
//
//	4-byte bit count (big-endian)
//	4-byte compressed word count (big-endian)
//	N × 8-byte compressed words (big-endian)
//	4-byte RLW position (big-endian)
type EWAH []byte

// BitCount returns the number of uncompressed bits described by the
// compressed bitmap.
func (b EWAH) BitCount() uint32 {
	if len(b) < 4 {
		return 0
	}
	return binary.BigEndian.Uint32(b[0:4])
}

// Size returns the total on-disk byte size of this EWAH entry
// (header + compressed words + trailing RLW position).
func (b EWAH) Size() int {
	if len(b) < 8 {
		return 0
	}
	wordCount := binary.BigEndian.Uint32(b[4:8])
	return 4 + 4 + int(wordCount)*8 + 4
}

// Bitmap is a decompressed bitmap stored as a byte slice. Bit i is stored
// at byte i/8, bit position 7-(i%8) (big-endian / MSB-first within each
// byte), matching the git pack-bitmap convention where the most
// significant bit of each word is bit 0.
type Bitmap []byte

// Get returns the value of the bit at position pos.
// Bit numbering follows the git pack-bitmap convention: within each
// 64-bit word (stored big-endian) bit 0 is the least-significant bit.
func (b Bitmap) Get(pos uint32) bool {
	wordIdx := pos / 64
	bitInWord := pos % 64
	byteIdx := wordIdx*8 + 7 - bitInWord/8
	if byteIdx >= uint32(len(b)) {
		return false
	}
	return b[byteIdx]&(1<<(bitInWord%8)) != 0
}

// Set sets the bit at position pos.
func (b Bitmap) Set(pos uint32) {
	wordIdx := pos / 64
	bitInWord := pos % 64
	byteIdx := wordIdx*8 + 7 - bitInWord/8
	if byteIdx >= uint32(len(b)) {
		return
	}
	b[byteIdx] |= 1 << (bitInWord % 8)
}

// Bits returns the number of bits in the bitmap (always a multiple of 8).
func (b Bitmap) Bits() uint32 {
	return uint32(len(b)) * 8
}

// Extend grows b with zero bytes so that it is at least as long as
// other, returning the (possibly reallocated) bitmap.
func Extend(b, other Bitmap) Bitmap {
	if len(b) >= len(other) {
		return b
	}
	grown := make(Bitmap, len(other))
	copy(grown, b)
	return grown
}

// And applies bitwise AND with other, modifying b in place.
// Only the first min(len(b), len(other)) bytes are ANDed; any
// trailing bytes in b beyond len(other) are zeroed.
func (b Bitmap) And(other Bitmap) {
	n := min(len(b), len(other))
	for i := range n {
		b[i] &= other[i]
	}
	for i := n; i < len(b); i++ {
		b[i] = 0
	}
}

// Or applies bitwise OR with other, modifying b in place.
// Panics if other is longer than b.
func (b Bitmap) Or(other Bitmap) {
	if len(other) > len(b) {
		panic("bitmap: Or: other bitmap is longer than receiver")
	}
	for i := range len(other) {
		b[i] |= other[i]
	}
}

// Xor applies bitwise XOR with other, modifying b in place.
// Panics if other is longer than b.
func (b Bitmap) Xor(other Bitmap) {
	if len(other) > len(b) {
		panic("bitmap: Xor: other bitmap is longer than receiver")
	}
	for i := range len(other) {
		b[i] ^= other[i]
	}
}

// SetBitsIterator iterates over the indices of set bits in a Bitmap
// in ascending bit-position order (git's LSB-first word convention).
type SetBitsIterator struct {
	b    Bitmap
	word uint32 // current word index
	rem  uint64 // remaining bits in current word
}

// SetBits returns an iterator over the indices of all set bits.
func (b Bitmap) SetBits() *SetBitsIterator {
	it := &SetBitsIterator{b: b}
	it.advance()
	return it
}

// Next returns the index of the next set bit and true, or (0, false)
// when there are no more set bits.
func (it *SetBitsIterator) Next() (uint32, bool) {
	for {
		if it.rem != 0 {
			bit := trailingZeros64(it.rem)
			it.rem &= it.rem - 1 // clear lowest set bit
			pos := it.word*64 + uint32(bit)
			if it.rem == 0 {
				it.word++
				it.advance()
			}
			return pos, true
		}
		return 0, false
	}
}

// advance loads the next non-zero word.
func (it *SetBitsIterator) advance() {
	nWords := uint32(len(it.b)) / 8
	for it.word < nWords {
		w := binary.BigEndian.Uint64(it.b[it.word*8:])
		if w != 0 {
			it.rem = w
			return
		}
		it.word++
	}
	it.rem = 0
}

// trailingZeros64 returns the number of trailing zero bits in x.
func trailingZeros64(x uint64) uint32 {
	if x == 0 {
		return 64
	}
	n := uint32(0)
	for x&1 == 0 {
		n++
		x >>= 1
	}
	return n
}

// DecodeEWAH decompresses an EWAH-encoded bitmap into a flat Bitmap.
func DecodeEWAH(data EWAH) (Bitmap, error) {
	if len(data) < 12 {
		return nil, fmt.Errorf("ewah: data too short (%d bytes, need at least 12)", len(data))
	}

	bits := binary.BigEndian.Uint32(data[0:4])
	wordCount := binary.BigEndian.Uint32(data[4:8])

	expected := 4 + 4 + int(wordCount)*8 + 4
	if len(data) < expected {
		return nil, fmt.Errorf("ewah: data truncated (have %d bytes, need %d)", len(data), expected)
	}

	if bits == 0 {
		return Bitmap{}, nil
	}

	nBytes := (bits + 7) / 8
	out := make(Bitmap, nBytes)

	outWord := uint32(0)
	nWords := (bits + 63) / 64
	compIdx := uint32(0)
	compBase := 8

	for compIdx < wordCount {
		rlw := binary.BigEndian.Uint64(data[compBase+int(compIdx)*8:])
		compIdx++

		// RLW layout (JGit convention):
		//   bit 0:     fill bit (running bit)
		//   bits 1-32: k (running length, 32 bits)
		//   bits 33-63: l (literal count, 31 bits)
		fillBit := rlw & 1
		k := uint32((rlw >> 1) & 0xFFFFFFFF)
		l := uint32(rlw >> 33)

		if fillBit != 0 {
			for j := uint32(0); j < k && outWord < nWords; j++ {
				writeWord(out, outWord, ^uint64(0))
				outWord++
			}
		} else {
			outWord += min(k, nWords-outWord)
		}

		for j := uint32(0); j < l && outWord < nWords && compIdx < wordCount; j++ {
			w := binary.BigEndian.Uint64(data[compBase+int(compIdx)*8:])
			compIdx++
			writeWord(out, outWord, w)
			outWord++
		}
	}

	return out, nil
}

// writeWord writes a 64-bit word into the byte-slice bitmap at the given
// word index, using big-endian byte order.
func writeWord(b Bitmap, wordIdx uint32, w uint64) {
	start := wordIdx * 8
	end := start + 8
	if end > uint32(len(b)) {
		end = uint32(len(b))
	}
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], w)
	copy(b[start:end], buf[:end-start])
}
