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

// entryLookup maps an entry ordinal to the byte offset within the
// bitmap file where that entry begins.
type entryLookup interface {
	// entryOffset returns the byte offset of the i-th entry.
	entryOffset(i int) uint32
	// count returns the number of entries.
	count() int
}

// Index is a read-only view over the raw bytes of a pack bitmap index
// file. All field access is on-demand with no upfront decoding, making
// it suitable for use with memory-mapped files.
//
// Use [Open] to validate and create an Index from raw bytes.
type Index struct {
	b        []byte
	hashSize int
	entries  entryLookup
}

// Version returns the bitmap index version.
func (idx *Index) Version() uint16 {
	return binary.BigEndian.Uint16(idx.b[4:6])
}

// Flags returns the option flags.
func (idx *Index) Flags() uint16 {
	return binary.BigEndian.Uint16(idx.b[6:8])
}

// EntryCount returns the number of per-commit bitmap entries.
func (idx *Index) EntryCount() uint32 {
	return binary.BigEndian.Uint32(idx.b[8:12])
}

// PackChecksum returns the raw pack checksum bytes from the header.
func (idx *Index) PackChecksum() []byte {
	return idx.b[headerFixedSize : headerFixedSize+idx.hashSize]
}

// Commits returns the EWAH-compressed type bitmap for commits.
func (idx *Index) Commits() EWAH {
	return idx.typeBitmap(0)
}

// Trees returns the EWAH-compressed type bitmap for trees.
func (idx *Index) Trees() EWAH {
	return idx.typeBitmap(1)
}

// Blobs returns the EWAH-compressed type bitmap for blobs.
func (idx *Index) Blobs() EWAH {
	return idx.typeBitmap(2)
}

// Tags returns the EWAH-compressed type bitmap for tags.
func (idx *Index) Tags() EWAH {
	return idx.typeBitmap(3)
}

// typeBitmap returns the i-th type bitmap (0=commits, 1=trees, 2=blobs, 3=tags).
func (idx *Index) typeBitmap(i int) EWAH {
	off := headerFixedSize + idx.hashSize
	for j := 0; j < i; j++ {
		off += EWAH(idx.b[off:]).Size()
	}
	return EWAH(idx.b[off:])
}

// Entry returns the i-th per-commit bitmap entry. The offset is
// looked up from the entry table built during [Open], so this is O(1).
func (idx *Index) Entry(i int) Entry {
	return parseEntry(idx.b[idx.entries.entryOffset(i):])
}

// scannedOffsets is the default entryLookup built by scanning through
// the variable-length entries once during [Open].
type scannedOffsets []uint32

func (s scannedOffsets) entryOffset(i int) uint32 { return s[i] }
func (s scannedOffsets) count() int               { return len(s) }

// buildEntryOffsets scans the variable-length entries once and records
// the byte offset of each entry. Called during [Open].
func (idx *Index) buildEntryOffsets() {
	n := int(idx.EntryCount())
	offsets := make(scannedOffsets, n)

	off := headerFixedSize + idx.hashSize
	for range 4 {
		off += EWAH(idx.b[off:]).Size()
	}

	for i := range n {
		offsets[i] = uint32(off)
		off += entrySize(idx.b[off:])
	}

	idx.entries = offsets
}

// entryHeaderSize is the fixed portion of each entry before the EWAH bitmap.
const entryHeaderSize = 6 // 4 (position) + 1 (xor) + 1 (flags)

// entrySize returns the total byte size of the entry at data[0:].
func entrySize(data []byte) int {
	return entryHeaderSize + EWAH(data[entryHeaderSize:]).Size()
}

func parseEntry(data []byte) Entry {
	return Entry{
		ObjectPosition: binary.BigEndian.Uint32(data[0:4]),
		XOROffset:      data[4],
		Flags:          data[5],
		Bitmap:         EWAH(data[entryHeaderSize:]),
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
	// It is a sub-slice of the Index data.
	Bitmap EWAH
}

// NameHashCache returns the name-hash cache values. Returns nil if the
// OptHashCache flag is not set. Each value is a 4-byte big-endian uint32.
func (idx *Index) NameHashCache() []byte {
	if idx.Flags()&OptHashCache == 0 {
		return nil
	}
	n := idx.entries.count()
	if n == 0 {
		return nil
	}
	// The hash cache starts right after the last entry.
	last := idx.entries.entryOffset(n - 1)
	off := int(last) + entrySize(idx.b[last:])
	end := len(idx.b) - idx.hashSize
	if off >= end {
		return nil
	}
	return idx.b[off:end]
}
