package bitmap

import (
	"github.com/erizocosmico/go-ewah"
	"github.com/go-git/go-git/v6/plumbing"
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

var bitmapHeader = []byte{'B', 'I', 'T', 'M'}

// Index is the in-memory representation of a pack bitmap index file.
type Index struct {
	// Version is the version of the bitmap index.
	Version uint16
	// Flags holds the option flags.
	Flags uint16
	// PackChecksum is the checksum of the corresponding packfile.
	PackChecksum plumbing.Hash
	// Checksum is the checksum of the bitmap index file itself.
	Checksum plumbing.Hash

	// Commits is an EWAH bitmap indicating which pack index positions
	// correspond to commit objects.
	Commits *ewah.Bitmap
	// Trees is an EWAH bitmap indicating which pack index positions
	// correspond to tree objects.
	Trees *ewah.Bitmap
	// Blobs is an EWAH bitmap indicating which pack index positions
	// correspond to blob objects.
	Blobs *ewah.Bitmap
	// Tags is an EWAH bitmap indicating which pack index positions
	// correspond to tag objects.
	Tags *ewah.Bitmap

	// Entries holds the per-commit reachability bitmaps.
	Entries []Entry

	// NameHashCache holds optional name-hash values, one per object
	// in the pack. Present only when OptHashCache is set.
	NameHashCache []uint32
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
	// Bitmap is the EWAH compressed reachability bitmap for this commit.
	Bitmap *ewah.Bitmap
}