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
		literal := uint64(0xFF00000000000000) // first 8 bits set
		data := encodeEWAH(192, []uint64{rlw(false, 2, 1), literal}, 0)
		bm, err := DecodeEWAH(data)
		require.NoError(t, err)
		assert.Equal(t, 24, len(bm))

		// First 128 bits: zero fill
		for i := uint32(0); i < 128; i++ {
			assert.False(t, bm.Get(i), "bit %d", i)
		}
		// Bits 128-135: set (from 0xFF byte)
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
		literal := uint64(0x0000000000000001) // bit 63 set
		data := encodeEWAH(128, []uint64{
			rlw(false, 1, 0),            // 64 zero bits
			rlw(false, 0, 1), literal, // 64 bits with bit 63 set
		}, 1)
		bm, err := DecodeEWAH(data)
		require.NoError(t, err)
		assert.Equal(t, 16, len(bm))

		for i := uint32(0); i < 63; i++ {
			assert.False(t, bm.Get(i), "bit %d", i)
		}
		assert.False(t, bm.Get(126))
		assert.True(t, bm.Get(127))
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

func TestBitmapGet(t *testing.T) {
	t.Parallel()

	bm := Bitmap([]byte{0xA5}) // 1010 0101
	assert.True(t, bm.Get(0))  // MSB
	assert.False(t, bm.Get(1))
	assert.True(t, bm.Get(2))
	assert.False(t, bm.Get(3))
	assert.False(t, bm.Get(4))
	assert.True(t, bm.Get(5))
	assert.False(t, bm.Get(6))
	assert.True(t, bm.Get(7)) // LSB
	assert.False(t, bm.Get(8)) // out of range
}

func TestSetBitsIterator(t *testing.T) {
	t.Parallel()

	t.Run("empty", func(t *testing.T) {
		t.Parallel()
		bm := Bitmap([]byte{0x00, 0x00})
		it := bm.SetBits()
		_, ok := it.Next()
		assert.False(t, ok)
	})

	t.Run("single byte", func(t *testing.T) {
		t.Parallel()
		bm := Bitmap([]byte{0xA5}) // 1010 0101 → bits 0,2,5,7
		it := bm.SetBits()

		var got []uint32
		for pos, ok := it.Next(); ok; pos, ok = it.Next() {
			got = append(got, pos)
		}
		assert.Equal(t, []uint32{0, 2, 5, 7}, got)
	})

	t.Run("multi byte with gaps", func(t *testing.T) {
		t.Parallel()
		bm := Bitmap([]byte{0x80, 0x00, 0x01}) // bit 0, then zeros, then bit 23
		it := bm.SetBits()

		var got []uint32
		for pos, ok := it.Next(); ok; pos, ok = it.Next() {
			got = append(got, pos)
		}
		assert.Equal(t, []uint32{0, 23}, got)
	})

	t.Run("all ones", func(t *testing.T) {
		t.Parallel()
		bm := Bitmap([]byte{0xFF})
		it := bm.SetBits()

		var got []uint32
		for pos, ok := it.Next(); ok; pos, ok = it.Next() {
			got = append(got, pos)
		}
		assert.Equal(t, []uint32{0, 1, 2, 3, 4, 5, 6, 7}, got)
	})
}

// rlw builds a Running Length Word.
func rlw(fill bool, k uint32, l uint32) uint64 {
	var b uint64
	if fill {
		b = 1
	}
	return b<<63 | uint64(k)<<31 | uint64(l)
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
