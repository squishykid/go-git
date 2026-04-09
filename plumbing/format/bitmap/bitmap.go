package bitmap

import (
	"encoding/binary"
	"errors"
)

const (
	// VersionSupported is the only bitmap index version supported.
	VersionSupported = 1
)

// Option flags for the bitmap index.
const (
	// OptFullDAG indicates the bitmap index was generated for a packfile
	// with full closure.
	OptFullDAG = 0x1
	// OptHashCache indicates the end of the bitmap file contains
	// name-hash values, one per object in the pack.
	OptHashCache = 0x4
)

// ErrInvalidXOROffset is returned when a bitmap entry references
// an XOR offset that is out of range.
var ErrInvalidXOROffset = errors.New("bitmap entry has invalid XOR offset")

var bitmapHeader = []byte{'B', 'I', 'T', 'M'}

// headerFixedSize is the fixed portion of the header before the pack
// checksum: 4 (sig) + 2 (version) + 2 (flags) + 4 (entry count).
const headerFixedSize = 12

// BitmapIndex is a read-only view over the raw bytes of a pack bitmap index
// file. All field access is on-demand with no upfront decoding, making
// it suitable for use with memory-mapped files.
//
// Use [Open] to validate and create an BitmapIndex from raw bytes.
type BitmapIndex []byte

// Version returns the bitmap index version.
func (idx BitmapIndex) Version() uint16 {
	return binary.BigEndian.Uint16(idx[4:6])
}

// Flags returns the option flags.
func (idx BitmapIndex) Flags() uint16 {
	return binary.BigEndian.Uint16(idx[6:8])
}

// EntryCount returns the number of per-commit bitmap entries.
func (idx BitmapIndex) EntryCount() uint32 {
	return binary.BigEndian.Uint32(idx[8:12])
}

// PackChecksum returns the raw pack checksum bytes from the header.
func (idx BitmapIndex) PackChecksum(hashSize int) []byte {
	return idx[headerFixedSize : headerFixedSize+hashSize]
}

// Commits returns the EWAH-compressed type bitmap for commits.
func (idx BitmapIndex) Commits(hashSize int) BitmapEWAH {
	return idx.typeBitmap(hashSize, 0)
}

// Trees returns the EWAH-compressed type bitmap for trees.
func (idx BitmapIndex) Trees(hashSize int) BitmapEWAH {
	return idx.typeBitmap(hashSize, 1)
}

// Blobs returns the EWAH-compressed type bitmap for blobs.
func (idx BitmapIndex) Blobs(hashSize int) BitmapEWAH {
	return idx.typeBitmap(hashSize, 2)
}

// Tags returns the EWAH-compressed type bitmap for tags.
func (idx BitmapIndex) Tags(hashSize int) BitmapEWAH {
	return idx.typeBitmap(hashSize, 3)
}

// typeBitmap returns the i-th type bitmap (0=commits, 1=trees, 2=blobs, 3=tags).
func (idx BitmapIndex) typeBitmap(hashSize int, i int) BitmapEWAH {
	off := headerFixedSize + hashSize
	for j := 0; j < i; j++ {
		off += BitmapEWAH(idx[off:]).Size()
	}
	return BitmapEWAH(idx[off:])
}

// entriesOffset returns the byte offset where the per-commit entries begin.
func (idx BitmapIndex) entriesOffset(hashSize int) int {
	off := headerFixedSize + hashSize
	for range 4 {
		off += BitmapEWAH(idx[off:]).Size()
	}
	return off
}

// Entry returns the i-th per-commit bitmap entry.
func (idx BitmapIndex) Entry(hashSize int, i int) Entry {
	off := idx.entriesOffset(hashSize)
	for j := 0; j < i; j++ {
		off += entrySize(idx[off:])
	}
	return parseEntry(idx[off:])
}

// entryHeaderSize is the fixed portion of each entry before the EWAH bitmap.
const entryHeaderSize = 6 // 4 (position) + 1 (xor) + 1 (flags)

// entrySize returns the total byte size of the entry at data[0:].
func entrySize(data []byte) int {
	return entryHeaderSize + BitmapEWAH(data[entryHeaderSize:]).Size()
}

func parseEntry(data []byte) Entry {
	return Entry{
		ObjectPosition: binary.BigEndian.Uint32(data[0:4]),
		XOROffset:      data[4],
		Flags:          data[5],
		Bitmap:         BitmapEWAH(data[entryHeaderSize:]),
	}
}

// Entry is a single bitmap entry for a commit in the pack.
type Entry struct {
	// ObjectPosition is the position of the object in the pack index.
	ObjectPosition uint32
	// XOROffset indicates which previous entry's bitmap to XOR with.
	// 0 means no XOR.
	XOROffset uint8
	// Flags holds per-entry flags.
	Flags uint8
	// Bitmap is the EWAH-compressed reachability bitmap for this commit.
	// It is a sub-slice of the BitmapIndex data.
	Bitmap BitmapEWAH
}

// NameHashCache returns the name-hash cache values. Returns nil if the
// OptHashCache flag is not set. Each value is a 4-byte big-endian uint32.
func (idx BitmapIndex) NameHashCache(hashSize int) []byte {
	if idx.Flags()&OptHashCache == 0 {
		return nil
	}
	off := idx.entriesOffset(hashSize)
	n := int(idx.EntryCount())
	for i := 0; i < n; i++ {
		off += entrySize(idx[off:])
	}
	// The hash cache runs from off to len(idx) - hashSize (trailing checksum).
	end := len(idx) - hashSize
	if off >= end {
		return nil
	}
	return idx[off:end]
}
