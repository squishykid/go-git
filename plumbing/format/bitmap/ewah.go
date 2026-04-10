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
