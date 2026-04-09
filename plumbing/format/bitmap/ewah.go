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
func (b Bitmap) Get(pos uint32) bool {
	byteIdx := pos / 8
	if byteIdx >= uint32(len(b)) {
		return false
	}
	bitIdx := 7 - (pos % 8)
	return b[byteIdx]&(1<<bitIdx) != 0
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

// SetBitsIterator iterates over the indices of set bits in a Bitmap.
type SetBitsIterator struct {
	b   Bitmap
	pos uint32 // current byte index
	bit uint8  // next bit to check within current byte (7 = MSB, 0 = LSB)
	rem byte   // remaining bits in current byte (masked copy)
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
	for it.rem != 0 {
		bit := it.bit
		mask := byte(1 << bit)
		if it.rem&mask != 0 {
			it.rem &^= mask
			pos := it.pos*8 + uint32(7-bit)
			if it.rem == 0 {
				it.pos++
				it.advance()
			}
			return pos, true
		}
		if bit == 0 {
			it.pos++
			it.advance()
		} else {
			it.bit--
		}
	}
	return 0, false
}

// advance skips zero bytes to find the next byte with set bits.
func (it *SetBitsIterator) advance() {
	for it.pos < uint32(len(it.b)) {
		if it.b[it.pos] != 0 {
			it.rem = it.b[it.pos]
			it.bit = 7
			return
		}
		it.pos++
	}
	it.rem = 0
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

		fillBit := rlw >> 63
		k := uint32((rlw >> 31) & 0xFFFFFFFF)
		l := uint32(rlw & 0x7FFFFFFF)

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
