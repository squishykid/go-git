package bitmap

import (
	"encoding/binary"
	"math/bits"
)

// TODO move this to 'bitmapfile' package

// Bitmap is a decompressed bitmap stored as a byte slice. Bits are
// numbered using the git pack-bitmap convention: within each 64-bit
// word (stored big-endian) bit 0 is the least-significant bit.
type Bitmap []byte

// NewBitmap allocates a zero-initialised Bitmap wide enough to hold
// objectCount bits. The underlying slice is rounded up to a 64-bit
// word boundary so Get/Set can address every bit without special
// handling of a partial final word.
func NewBitmap(objectCount int64) Bitmap {
	return make(Bitmap, ((objectCount+63)/64)*8)
}

// Get returns the value of the bit at position pos.
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

// AndNot clears every bit in b that is set in other (b &^= other).
// Only the first min(len(b), len(other)) bytes are affected.
func (b Bitmap) AndNot(other Bitmap) {
	n := min(len(b), len(other))
	for i := range n {
		b[i] &^= other[i]
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
			bit := bits.TrailingZeros64(it.rem)
			it.rem &= it.rem - 1            // clear lowest set bit
			pos := it.word*64 + uint32(bit) //nolint:gosec
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
