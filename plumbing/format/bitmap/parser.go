package bitmap

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"

	"github.com/erizocosmico/go-ewah"
	"github.com/go-git/go-git/v6/plumbing/hash"
	utilbin "github.com/go-git/go-git/v6/utils/binary"
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

// Decoder reads and decodes bitmap index files from an input stream.
type Decoder struct {
	io.Reader
	h hash.Hash
}

// NewDecoder returns a new bitmap index decoder that reads from r.
func NewDecoder(r io.Reader, h hash.Hash) *Decoder {
	tr := io.TeeReader(r, h)
	return &Decoder{Reader: tr, h: h}
}

// Decode reads the bitmap index from the stream and populates idx.
func (d *Decoder) Decode(idx *Index) error {
	d.h.Reset()

	if err := d.decodeHeader(idx); err != nil {
		return err
	}

	if err := d.decodeTypeBitmaps(idx); err != nil {
		return err
	}

	if err := d.decodeEntries(idx); err != nil {
		return err
	}

	if idx.Flags&OptHashCache != 0 {
		if err := d.decodeNameHashCache(idx); err != nil {
			return err
		}
	}

	computed := d.h.Sum(nil)

	idx.Checksum.ResetBySize(d.h.Size())
	if _, err := idx.Checksum.ReadFrom(d); err != nil {
		return fmt.Errorf("reading bitmap checksum: %w", err)
	}

	if idx.Checksum.Compare(computed) != 0 {
		return fmt.Errorf("%w: got %s, want %s",
			ErrInvalidChecksum, idx.Checksum.String(), fmt.Sprintf("%x", computed))
	}

	return nil
}

func (d *Decoder) decodeHeader(idx *Index) error {
	sig := make([]byte, 4)
	if _, err := io.ReadFull(d, sig); err != nil {
		return fmt.Errorf("reading bitmap signature: %w", err)
	}
	if !bytes.Equal(sig, bitmapHeader) {
		return ErrInvalidSignature
	}

	var version, flags uint16
	var entryCount uint32
	if err := utilbin.Read(d, &version, &flags, &entryCount); err != nil {
		return fmt.Errorf("reading bitmap header: %w", err)
	}

	if version != VersionSupported {
		return fmt.Errorf("%w: %d", ErrUnsupportedVersion, version)
	}
	if flags&OptFullDAG == 0 {
		return ErrMissingFullDAG
	}

	idx.Version = version
	idx.Flags = flags

	idx.PackChecksum.ResetBySize(d.h.Size())
	if _, err := idx.PackChecksum.ReadFrom(d); err != nil {
		return fmt.Errorf("reading pack checksum: %w", err)
	}

	idx.Entries = make([]Entry, entryCount)
	return nil
}

func (d *Decoder) decodeTypeBitmaps(idx *Index) error {
	var err error
	idx.Commits, err = ewah.FromReader(d, binary.BigEndian)
	if err != nil {
		return fmt.Errorf("reading commits bitmap: %w", err)
	}
	idx.Trees, err = ewah.FromReader(d, binary.BigEndian)
	if err != nil {
		return fmt.Errorf("reading trees bitmap: %w", err)
	}
	idx.Blobs, err = ewah.FromReader(d, binary.BigEndian)
	if err != nil {
		return fmt.Errorf("reading blobs bitmap: %w", err)
	}
	idx.Tags, err = ewah.FromReader(d, binary.BigEndian)
	if err != nil {
		return fmt.Errorf("reading tags bitmap: %w", err)
	}
	return nil
}

func (d *Decoder) decodeEntries(idx *Index) error {
	for i := range idx.Entries {
		var pos uint32
		var xor, flags uint8
		if err := utilbin.Read(d, &pos, &xor, &flags); err != nil {
			return fmt.Errorf("reading entry %d header: %w", i, err)
		}

		bm, err := ewah.FromReader(d, binary.BigEndian)
		if err != nil {
			return fmt.Errorf("reading entry %d bitmap: %w", i, err)
		}

		idx.Entries[i] = Entry{
			ObjectPosition: pos,
			XOROffset:      xor,
			Flags:          flags,
			Bitmap:         bm,
		}
	}
	return nil
}

func (d *Decoder) decodeNameHashCache(idx *Index) error {
	// The hash cache contains one uint32 per object in the pack.
	// We determine the count from the commits type bitmap which
	// tracks all pack index positions.
	//
	// The number of objects equals the highest bit count among
	// the type bitmaps, since each type bitmap covers the full
	// pack index range.
	n := maxBits(idx.Commits, idx.Trees, idx.Blobs, idx.Tags)
	if n == 0 {
		return nil
	}

	idx.NameHashCache = make([]uint32, n)
	for i := range idx.NameHashCache {
		v, err := utilbin.ReadUint32(d)
		if err != nil {
			return fmt.Errorf("reading name-hash %d: %w", i, err)
		}
		idx.NameHashCache[i] = v
	}
	return nil
}

func maxBits(bitmaps ...*ewah.Bitmap) uint32 {
	var max uint32
	for _, b := range bitmaps {
		if b != nil && b.Bits() > max {
			max = b.Bits()
		}
	}
	return max
}