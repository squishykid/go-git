package bitmap

import (
	"bytes"
	"errors"
	"fmt"

	"github.com/go-git/go-git/v6/plumbing/hash"
)

var (
	// ErrInvalidSignature is returned when the bitmap file does not
	// start with the expected "BITM" magic bytes.
	ErrInvalidSignature = errors.New("invalid bitmap signature")
	// ErrUnsupportedVersion is returned when the bitmap version is
	// not supported.
	ErrUnsupportedVersion = errors.New("unsupported bitmap version")
	// ErrMissingFullDAG is returned when the required OptFullDAG flag
	// is not set.
	ErrMissingFullDAG = errors.New("bitmap missing required FULL_DAG flag")
	// ErrInvalidChecksum is returned when the file checksum does not
	// match the computed checksum.
	ErrInvalidChecksum = errors.New("bitmap checksum mismatch")
)

// Open validates the raw bytes of a bitmap index file and returns an
// Index for on-demand field access. h is used for checksum verification.
func Open(data []byte, h hash.Hash) (Index, error) {
	hashSize := h.Size()
	minSize := headerFixedSize + hashSize + hashSize // header + at least trailing checksum
	if len(data) < minSize {
		return nil, fmt.Errorf("bitmap file too short (%d bytes)", len(data))
	}

	if !bytes.Equal(data[:4], bitmapHeader) {
		return nil, ErrInvalidSignature
	}

	idx := Index(data)

	if idx.Version() != VersionSupported {
		return nil, fmt.Errorf("%w: %d", ErrUnsupportedVersion, idx.Version())
	}
	if idx.Flags()&OptFullDAG == 0 {
		return nil, ErrMissingFullDAG
	}

	// Verify file checksum: hash everything except the trailing checksum.
	h.Reset()
	h.Write(data[:len(data)-hashSize])
	computed := h.Sum(nil)
	trailing := data[len(data)-hashSize:]
	if !bytes.Equal(computed, trailing) {
		return nil, fmt.Errorf("%w: got %x, want %x", ErrInvalidChecksum, trailing, computed)
	}

	return idx, nil
}
