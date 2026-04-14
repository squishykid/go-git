package bitmap

import (
	"encoding/binary"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDecodeEWAH(t *testing.T) {
	t.Parallel()

	t.Run("empty bitmap", func(t *testing.T) {
		t.Parallel()
		// bits=0, words=1, one RLW (b=0, k=0, l=0), rlwpos=0
		data := encodeEWAH(0, []uint64{rlw(false, 0, 0)}, 0)
		bm, err := DecodeEWAH(data)
		require.NoError(t, err)
		assert.Len(t, bm, 0)
	})

	t.Run("single literal word", func(t *testing.T) {
		t.Parallel()
		// 64 bits, 2 words: RLW(k=0, l=1) + one literal
		// Literal 0x8000000000000001 → bits 0 and 63 set.
		literal := uint64(0x8000000000000001)
		data := encodeEWAH(64, []uint64{rlw(false, 0, 1), literal}, 0)
		bm, err := DecodeEWAH(data)
		require.NoError(t, err)
		assert.Equal(t, 8, len(bm)) // 64 bits = 8 bytes

		assert.True(t, bm.Get(0))
		assert.False(t, bm.Get(1))
		assert.False(t, bm.Get(62))
		assert.True(t, bm.Get(63))
	})

	t.Run("zero fill", func(t *testing.T) {
		t.Parallel()
		// 128 bits: RLW(k=2, l=0) → two 64-bit words of zeros
		data := encodeEWAH(128, []uint64{rlw(false, 2, 0)}, 0)
		bm, err := DecodeEWAH(data)
		require.NoError(t, err)
		assert.Equal(t, 16, len(bm))

		for i := uint32(0); i < 128; i++ {
			assert.False(t, bm.Get(i), "bit %d", i)
		}
	})

	t.Run("one fill", func(t *testing.T) {
		t.Parallel()
		// 64 bits: RLW(b=1, k=1, l=0) → one word of all-ones
		data := encodeEWAH(64, []uint64{rlw(true, 1, 0)}, 0)
		bm, err := DecodeEWAH(data)
		require.NoError(t, err)
		assert.Equal(t, 8, len(bm))

		for i := uint32(0); i < 64; i++ {
			assert.True(t, bm.Get(i), "bit %d", i)
		}
	})

	t.Run("fill then literal", func(t *testing.T) {
		t.Parallel()
		// 192 bits: RLW(k=2, l=1) → 128 zero bits + one literal word
		literal := uint64(0x00000000000000FF) // bits 0-7 set (LSB-first)
		data := encodeEWAH(192, []uint64{rlw(false, 2, 1), literal}, 0)
		bm, err := DecodeEWAH(data)
		require.NoError(t, err)
		assert.Equal(t, 24, len(bm))

		// First 128 bits: zero fill
		for i := uint32(0); i < 128; i++ {
			assert.False(t, bm.Get(i), "bit %d", i)
		}
		// Bits 128-135: set (from literal bits 0-7)
		for i := uint32(128); i < 136; i++ {
			assert.True(t, bm.Get(i), "bit %d", i)
		}
		// Bits 136-191: clear
		for i := uint32(136); i < 192; i++ {
			assert.False(t, bm.Get(i), "bit %d", i)
		}
	})

	t.Run("multiple RLWs", func(t *testing.T) {
		t.Parallel()
		// Two RLWs: first fills 64 zeros, second has 1 literal.
		literal := uint64(0x0000000000000001) // bit 0 set (LSB-first)
		data := encodeEWAH(128, []uint64{
			rlw(false, 1, 0),            // 64 zero bits
			rlw(false, 0, 1), literal, // 64 bits with bit 0 set
		}, 1)
		bm, err := DecodeEWAH(data)
		require.NoError(t, err)
		assert.Equal(t, 16, len(bm))

		for i := uint32(1); i < 64; i++ {
			assert.False(t, bm.Get(i), "bit %d", i)
		}
		assert.True(t, bm.Get(64))
		assert.False(t, bm.Get(65))
	})

	t.Run("trailing data ignored", func(t *testing.T) {
		t.Parallel()
		// Two EWAH bitmaps concatenated. DecodeEWAH should only
		// read the first, ignoring the second.
		first := encodeEWAH(64, []uint64{rlw(true, 1, 0)}, 0)  // all ones
		second := encodeEWAH(64, []uint64{rlw(false, 1, 0)}, 0) // all zeros

		combined := append(first, second...)
		bm, err := DecodeEWAH(combined)
		require.NoError(t, err)
		assert.Equal(t, 8, len(bm))

		for i := uint32(0); i < 64; i++ {
			assert.True(t, bm.Get(i), "bit %d", i)
		}

		// Decoding from an offset into the same slice yields the second bitmap.
		bm2, err := DecodeEWAH(combined[len(first):])
		require.NoError(t, err)
		assert.Equal(t, 8, len(bm2))

		for i := uint32(0); i < 64; i++ {
			assert.False(t, bm2.Get(i), "bit %d", i)
		}
	})

	t.Run("too short", func(t *testing.T) {
		t.Parallel()
		_, err := DecodeEWAH([]byte{0, 0, 0})
		assert.Error(t, err)
	})

	t.Run("truncated words", func(t *testing.T) {
		t.Parallel()
		// Claim 1 word but don't provide it.
		data := encodeEWAH(64, nil, 0)
		// Manually set word count to 1 without providing the word.
		binary.BigEndian.PutUint32(data[4:8], 1)
		_, err := DecodeEWAH(data)
		assert.Error(t, err)
	})
}

func TestEncodeEWAH(t *testing.T) {
	t.Parallel()

	t.Run("empty", func(t *testing.T) {
		t.Parallel()
		bm := Bitmap{}
		encoded := EncodeEWAH(bm)
		assert.Equal(t, uint32(0), encoded.BitCount())

		decoded, err := DecodeEWAH(encoded)
		require.NoError(t, err)
		assert.Equal(t, Bitmap{}, decoded)
	})

	t.Run("all zeros", func(t *testing.T) {
		t.Parallel()
		bm := make(Bitmap, 16) // 128 bits, all zero
		encoded := EncodeEWAH(bm)
		assert.Equal(t, uint32(0), encoded.BitCount())

		decoded, err := DecodeEWAH(encoded)
		require.NoError(t, err)
		assert.Equal(t, Bitmap{}, decoded)
	})

	t.Run("all ones", func(t *testing.T) {
		t.Parallel()
		bm := make(Bitmap, 8) // 64 bits
		for i := range bm {
			bm[i] = 0xFF
		}
		encoded := EncodeEWAH(bm)

		decoded, err := DecodeEWAH(encoded)
		require.NoError(t, err)
		assert.Equal(t, bm, decoded)
	})

	t.Run("sparse", func(t *testing.T) {
		t.Parallel()
		bm := make(Bitmap, 64) // 512 bits
		bm.Set(0)
		bm.Set(100)
		bm.Set(500)
		encoded := EncodeEWAH(bm)

		decoded, err := DecodeEWAH(encoded)
		require.NoError(t, err)
		assert.True(t, decoded.Get(0))
		assert.True(t, decoded.Get(100))
		assert.True(t, decoded.Get(500))
		assert.False(t, decoded.Get(1))
		assert.False(t, decoded.Get(101))
	})

	t.Run("mixed fills and literals", func(t *testing.T) {
		t.Parallel()
		// 256 bits: 64 ones + 64 zeros + 64 literal + 64 ones
		bm := make(Bitmap, 32)
		// Word 0: all ones
		for i := 0; i < 8; i++ {
			bm[i] = 0xFF
		}
		// Word 1: all zeros (already zero)
		// Word 2: literal pattern
		bm.Set(128)
		bm.Set(191)
		// Word 3: all ones
		for i := 24; i < 32; i++ {
			bm[i] = 0xFF
		}

		encoded := EncodeEWAH(bm)

		decoded, err := DecodeEWAH(encoded)
		require.NoError(t, err)
		assert.Equal(t, bm, decoded)
	})

	t.Run("fixture round-trip", func(t *testing.T) {
		t.Parallel()
		// Decompress a real bitmap entry, re-encode, decode again.
		idx, _ := openFixture(t)
		e := idx.Entry(0)

		original, err := DecodeEWAH(e.Bitmap)
		require.NoError(t, err)

		reencoded := EncodeEWAH(original)
		decoded, err := DecodeEWAH(reencoded)
		require.NoError(t, err)

		// The decoded bitmap may be shorter (trailing zeros trimmed)
		// but all set bit positions must match.
		assertBitmapsEqual(t, original, decoded)
	})
}

func TestBitmapGet(t *testing.T) {
	t.Parallel()

	// Full 8-byte word. Bit positions use git's LSB-first convention:
	// bit 0 = LSB of byte[7], bit 63 = MSB of byte[0].
	bm := Bitmap([]byte{0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0xA5})
	// byte[7] = 0xA5 = 1010_0101 → bits 0-7 of word
	assert.True(t, bm.Get(0))  // bit 0 = byte[7] & 0x01 = 1
	assert.False(t, bm.Get(1)) // bit 1 = byte[7] & 0x02 = 0
	assert.True(t, bm.Get(2))  // bit 2 = byte[7] & 0x04 = 1
	assert.False(t, bm.Get(3))
	assert.False(t, bm.Get(4))
	assert.True(t, bm.Get(5))  // bit 5 = byte[7] & 0x20 = 1
	assert.False(t, bm.Get(6))
	assert.True(t, bm.Get(7))  // bit 7 = byte[7] & 0x80 = 1
	assert.False(t, bm.Get(8)) // bit 8 = byte[6] & 0x01 = 0
	assert.False(t, bm.Get(64)) // out of range
}

func TestSetBitsIterator(t *testing.T) {
	t.Parallel()

	t.Run("empty", func(t *testing.T) {
		t.Parallel()
		bm := Bitmap(make([]byte, 8))
		it := bm.SetBits()
		_, ok := it.Next()
		assert.False(t, ok)
	})

	t.Run("single word", func(t *testing.T) {
		t.Parallel()
		// byte[7]=0xA5 → bits 0,2,5,7 set (LSB-first within word)
		bm := Bitmap([]byte{0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0xA5})
		it := bm.SetBits()

		var got []uint32
		for pos, ok := it.Next(); ok; pos, ok = it.Next() {
			got = append(got, pos)
		}
		assert.Equal(t, []uint32{0, 2, 5, 7}, got)
	})

	t.Run("two words with gap", func(t *testing.T) {
		t.Parallel()
		// Word 0: bit 0 set (byte[7] = 0x01)
		// Word 1: bit 0 set (byte[15] = 0x01) → position 64
		bm := Bitmap(make([]byte, 16))
		bm[7] = 0x01
		bm[15] = 0x01

		it := bm.SetBits()
		var got []uint32
		for pos, ok := it.Next(); ok; pos, ok = it.Next() {
			got = append(got, pos)
		}
		assert.Equal(t, []uint32{0, 64}, got)
	})

	t.Run("all ones", func(t *testing.T) {
		t.Parallel()
		bm := Bitmap([]byte{0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF})
		it := bm.SetBits()

		var got []uint32
		for pos, ok := it.Next(); ok; pos, ok = it.Next() {
			got = append(got, pos)
		}
		assert.Len(t, got, 64)
		assert.Equal(t, uint32(0), got[0])
		assert.Equal(t, uint32(63), got[63])
	})
}

// assertBitmapsEqual checks that two bitmaps have the same set bits,
// ignoring any trailing zero bytes that may differ in length.
func assertBitmapsEqual(t *testing.T, a, b Bitmap) {
	t.Helper()
	n := max(a.Bits(), b.Bits())
	for i := uint32(0); i < n; i++ {
		if a.Get(i) != b.Get(i) {
			t.Errorf("bit %d: a=%v b=%v", i, a.Get(i), b.Get(i))
			return
		}
	}
}

// rlw builds a Running Length Word.
func rlw(fill bool, k uint32, l uint32) uint64 {
	var b uint64
	if fill {
		b = 1
	}
	// JGit layout: bit 0 = fill, bits 1-32 = k, bits 33-63 = l
	return b | uint64(k)<<1 | uint64(l)<<33
}

// encodeEWAH builds the on-disk EWAH representation.
func encodeEWAH(bits uint32, words []uint64, rlwPos uint32) []byte {
	buf := make([]byte, 4+4+len(words)*8+4)
	binary.BigEndian.PutUint32(buf[0:4], bits)
	binary.BigEndian.PutUint32(buf[4:8], uint32(len(words)))
	for i, w := range words {
		binary.BigEndian.PutUint64(buf[8+i*8:8+i*8+8], w)
	}
	binary.BigEndian.PutUint32(buf[8+len(words)*8:], rlwPos)
	return buf
}
