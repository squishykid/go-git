package bitmap

import (
	"encoding/binary"
	"fmt"
	mathbits "math/bits"
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

	// Allocate word-aligned so Get/Set (which address within 8-byte
	// words) can reach every bit, including LSBs of the last word.
	nBytes := ((bits + 63) / 64) * 8
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

// EncodeEWAH compresses a flat Bitmap into the on-disk EWAH format.
// The returned slice contains the full EWAH entry (header + words + trailer).
func EncodeEWAH(bm Bitmap) EWAH {
	nWords := (uint32(len(bm)) + 7) / 8

	// Read bitmap as big-endian 64-bit words.
	words := make([]uint64, nWords)
	for i := range words {
		start := uint32(i) * 8
		end := start + 8
		if end > uint32(len(bm)) {
			// Partial last word — read available bytes.
			var buf [8]byte
			copy(buf[:], bm[start:])
			words[i] = binary.BigEndian.Uint64(buf[:])
		} else {
			words[i] = binary.BigEndian.Uint64(bm[start:end])
		}
	}

	// Trim trailing zero words and compute the logical bit count
	// as the position of the highest set bit + 1.
	for nWords > 0 && words[nWords-1] == 0 {
		nWords--
	}
	words = words[:nWords]
	var bits uint32
	if nWords > 0 {
		lastWord := words[nWords-1]
		bits = (nWords-1)*64 + uint32(64-mathbits.LeadingZeros64(lastWord))
	}

	// Compress into RLWs + literal words.
	var compressed []uint64
	lastRLW := uint32(0)
	i := uint32(0)

	for i < nWords {
		rlwIdx := uint32(len(compressed))
		lastRLW = rlwIdx
		compressed = append(compressed, 0) // placeholder RLW

		// Count fill words (all-zeros or all-ones).
		var fillBit uint64
		var k uint32
		if i < nWords {
			if words[i] == 0 {
				fillBit = 0
				for i+k < nWords && words[i+k] == 0 {
					k++
				}
			} else if words[i] == ^uint64(0) {
				fillBit = 1
				for i+k < nWords && words[i+k] == ^uint64(0) {
					k++
				}
			}
		}
		i += k

		// Count following literal words (neither all-zeros nor all-ones).
		lStart := i
		for i < nWords && words[i] != 0 && words[i] != ^uint64(0) {
			i++
		}
		l := i - lStart

		compressed[rlwIdx] = fillBit | uint64(k)<<1 | uint64(l)<<33
		compressed = append(compressed, words[lStart:lStart+l]...)
	}

	// Build the EWAH byte slice.
	wordCount := uint32(len(compressed))
	out := make(EWAH, 4+4+wordCount*8+4)
	binary.BigEndian.PutUint32(out[0:4], bits)
	binary.BigEndian.PutUint32(out[4:8], wordCount)
	for j, w := range compressed {
		binary.BigEndian.PutUint64(out[8+j*8:], w)
	}
	binary.BigEndian.PutUint32(out[8+wordCount*8:], lastRLW)
	return out
}

// readWord reads a 64-bit word from the byte-slice bitmap at the given
// word index, using big-endian byte order.
func readWord(b Bitmap, wordIdx uint32) uint64 {
	start := wordIdx * 8
	end := start + 8
	if end > uint32(len(b)) {
		var buf [8]byte
		copy(buf[:], b[start:])
		return binary.BigEndian.Uint64(buf[:])
	}
	return binary.BigEndian.Uint64(b[start:end])
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
