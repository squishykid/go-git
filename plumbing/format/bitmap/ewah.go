package bitmap

import (
	"encoding/binary"
	"fmt"
)

// BitmapEWAH holds the raw on-disk EWAH-compressed bitmap data:
//
//	4-byte bit count (big-endian)
//	4-byte compressed word count (big-endian)
//	N × 8-byte compressed words (big-endian)
//	4-byte RLW position (big-endian)
type BitmapEWAH []byte

// BitCount returns the number of uncompressed bits described by the
// compressed bitmap.
func (b BitmapEWAH) BitCount() uint32 {
	if len(b) < 4 {
		return 0
	}
	return binary.BigEndian.Uint32(b[0:4])
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

// DecodeEWAH decompresses an EWAH-encoded bitmap into a flat Bitmap.
func DecodeEWAH(data BitmapEWAH) (Bitmap, error) {
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

		for j := uint32(0); j < l && outWord < nWords; j++ {
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
