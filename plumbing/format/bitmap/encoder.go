package bitmap

import (
	"encoding/binary"
	"fmt"
	"io"

	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/hash"
)

// Encoder writes pack bitmap index files.
type Encoder struct {
	source PackSource
	hasher hash.Hash
}

// NewEncoder creates an Encoder that reads objects from source and uses
// h for the file checksum.
func NewEncoder(source PackSource, h hash.Hash) *Encoder {
	return &Encoder{source: source, hasher: h}
}

// Encode writes a complete bitmap index to w.
//
// packChecksum is the trailing checksum of the associated packfile.
// commits lists the commit hashes to create bitmap entries for, in
// the order they should appear in the file.
//
// If old is non-nil, its precomputed reachability bitmaps are reused
// for commits that exist in both the old and new packs.
func (e *Encoder) Encode(w io.Writer, packChecksum []byte, commits []plumbing.Hash, old *Searcher) error {
	e.hasher.Reset()
	hw := io.MultiWriter(w, e.hasher)

	n := e.source.ObjectCount()
	bmLen := ((n + 63) / 64) * 8

	// --- Build type bitmaps ---
	typeBitmaps := [4]Bitmap{
		make(Bitmap, bmLen),
		make(Bitmap, bmLen),
		make(Bitmap, bmLen),
		make(Bitmap, bmLen),
	}
	for pos := uint32(0); pos < uint32(n); pos++ {
		switch e.source.ObjectType(pos) {
		case plumbing.CommitObject:
			typeBitmaps[0].Set(pos)
		case plumbing.TreeObject:
			typeBitmaps[1].Set(pos)
		case plumbing.BlobObject:
			typeBitmaps[2].Set(pos)
		case plumbing.TagObject:
			typeBitmaps[3].Set(pos)
		}
	}

	// --- Compute reachability bitmaps ---
	type entry struct {
		idxPos    uint32
		bitmap    Bitmap
		xorOffset uint8
		encoded   EWAH
	}

	entries := make([]entry, 0, len(commits))
	for _, h := range commits {
		idxPos, _, ok := e.source.FindPosition(h)
		if !ok {
			return fmt.Errorf("commit %s not found in pack", h)
		}

		bm, err := e.reachability(h, bmLen, old)
		if err != nil {
			return fmt.Errorf("computing reachability for %s: %w", h, err)
		}

		entries = append(entries, entry{
			idxPos: idxPos,
			bitmap: bm,
		})
	}

	// --- XOR compress ---
	for i := range entries {
		plain := EncodeEWAH(entries[i].bitmap)
		bestSize := plain.Size()
		entries[i].encoded = plain
		entries[i].xorOffset = 0

		// Try XOR against previous entries within a window.
		window := min(i, 10)
		for d := 1; d <= window && d <= 255; d++ {
			base := entries[i-d].bitmap
			xored := make(Bitmap, max(len(entries[i].bitmap), len(base)))
			copy(xored, entries[i].bitmap)
			xored.Xor(base)

			candidate := EncodeEWAH(xored)
			if candidate.Size() < bestSize {
				bestSize = candidate.Size()
				entries[i].encoded = candidate
				entries[i].xorOffset = uint8(d)
			}
		}
	}

	// --- Write header ---
	var hdr [headerFixedSize]byte
	copy(hdr[0:4], bitmapHeader)
	binary.BigEndian.PutUint16(hdr[4:6], VersionSupported)
	binary.BigEndian.PutUint16(hdr[6:8], OptFullDAG)
	binary.BigEndian.PutUint32(hdr[8:12], uint32(len(entries)))
	if _, err := hw.Write(hdr[:]); err != nil {
		return err
	}
	if _, err := hw.Write(packChecksum); err != nil {
		return err
	}

	// --- Write type bitmaps ---
	for _, tb := range typeBitmaps {
		ewah := EncodeEWAH(tb)
		if _, err := hw.Write(ewah); err != nil {
			return err
		}
	}

	// --- Write entries ---
	for _, ent := range entries {
		var buf [entryHeaderSize]byte
		binary.BigEndian.PutUint32(buf[0:4], ent.idxPos)
		buf[4] = ent.xorOffset
		buf[5] = 0 // flags
		if _, err := hw.Write(buf[:]); err != nil {
			return err
		}
		if _, err := hw.Write(ent.encoded); err != nil {
			return err
		}
	}

	// --- Write file checksum ---
	checksum := e.hasher.Sum(nil)
	_, err := w.Write(checksum)
	return err
}

// reachability computes the full reachability bitmap for a commit.
// If old is provided, it tries to reuse the precomputed bitmap.
func (e *Encoder) reachability(commit plumbing.Hash, bmLen int, old *Searcher) (Bitmap, error) {
	// Try reusing from old bitmap.
	if old != nil {
		idxPos, _, ok := e.source.FindPosition(commit)
		if ok {
			oldBm, err := old.Reachable(idxPos)
			if err == nil {
				bm := make(Bitmap, bmLen)
				copy(bm, oldBm)
				return bm, nil
			}
		}
	}

	// Walk the graph from scratch.
	bm := make(Bitmap, bmLen)
	queue := make([]objPos, 0, 64)

	idxPos, packPos, ok := e.source.FindPosition(commit)
	if !ok {
		return nil, fmt.Errorf("commit not found in pack")
	}
	queue = append(queue, objPos{idxPos, packPos})

	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]

		if bm.Get(cur.packPos) {
			continue
		}
		bm.Set(cur.packPos)

		obj, err := e.source.Object(cur.packPos)
		if err != nil {
			return nil, err
		}

		switch obj.Type() {
		case plumbing.CommitObject:
			tree, parents, err := parseCommitObj(obj)
			if err != nil {
				return nil, err
			}
			queue = resolveHashes(e.source, queue, tree)
			queue = resolveHashes(e.source, queue, parents...)

		case plumbing.TreeObject:
			entries, err := parseTreeObj(obj, e.hasher.Size())
			if err != nil {
				return nil, err
			}
			queue = resolveHashes(e.source, queue, entries...)

		case plumbing.TagObject:
			target, err := parseTagObj(obj)
			if err != nil {
				return nil, err
			}
			queue = resolveHashes(e.source, queue, target)
		}
	}

	return bm, nil
}

// resolveHashes maps hashes to positions and appends them to the queue.
func resolveHashes(src PackSource, queue []objPos, hashes ...plumbing.Hash) []objPos {
	for _, h := range hashes {
		idxPos, packPos, ok := src.FindPosition(h)
		if ok {
			queue = append(queue, objPos{idxPos, packPos})
		}
	}
	return queue
}
