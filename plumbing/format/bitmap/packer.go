package bitmap

import (
	"bytes"
	"errors"
	"fmt"
	"io"

	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/hash"
)

// PackSource provides access to objects in the source packfile.
//
// Bitmap entries reference two position spaces:
//   - idx position: hash-sorted order (Entry.ObjectPosition, used by Searcher)
//   - pack-offset position: order objects appear in the .pack file
//     (used by type and reachability bitmaps)
type PackSource interface {
	// ObjectCount returns the total number of objects in the pack.
	ObjectCount() int
	// FindPosition returns both the idx position and pack-offset position
	// for the given hash, or false if the hash is not in the pack.
	FindPosition(h plumbing.Hash) (idxPos, packPos uint32, ok bool)
	// Object returns the encoded object at the given pack-offset position.
	// The returned object must have resolved (non-delta) content.
	Object(packPos uint32) (plumbing.EncodedObject, error)
	// ObjectType returns the object type at the given pack-offset position.
	ObjectType(packPos uint32) plumbing.ObjectType
}

// Packer uses bitmap reachability data to efficiently build a packfile
// containing only the objects needed to bring a client from "haves"
// to "wants".
type Packer struct {
	searcher *Searcher
	source   PackSource
	hasher   hash.Hash
}

// NewPacker creates a Packer from a Searcher, a PackSource for reading
// objects, and a hash function for computing the packfile checksum.
func NewPacker(s *Searcher, source PackSource, h hash.Hash) *Packer {
	return &Packer{
		searcher: s,
		source:   source,
		hasher:   h,
	}
}

// Negotiate computes the bitmap of objects reachable from wants but
// not reachable from haves. Wants that are not in the pack index
// return an error. Unknown haves are silently ignored.
func (p *Packer) Negotiate(wants, haves []plumbing.Hash) (Bitmap, error) {
	wantBm, _, err := p.Reachability(wants)
	if err != nil {
		return nil, err
	}
	if len(wantBm) == 0 {
		return nil, nil
	}

	haveBm, _, _ := p.Reachability(haves)

	// AND NOT: clear bits for objects the client already has.
	wantBm.AndNot(haveBm)

	return wantBm, nil
}

// Reachability builds a bitmap with a set bit for every object
// reachable from the given hashes. The hashes are used as a BFS
// queue: when an object has a precomputed bitmap entry its bitmap
// is ORed in directly; otherwise the object is marked and its
// children are appended to the queue so they may still hit the
// fast path.
//
// The returned missing slice contains hashes that were encountered
// during the walk but are not present in the pack index.
func (p *Packer) Reachability(hashes []plumbing.Hash) (bm Bitmap, missing []plumbing.Hash, err error) {
	// Round up to 64-bit word boundary so the bitmap is at least as
	// large as any EWAH-decompressed bitmap from the same pack.
	n := p.source.ObjectCount()
	bmLen := ((n + 63) / 64) * 8
	bm = make(Bitmap, bmLen)

	var rq resolveQueue
	rq.init(p.source)

	// Resolve input hashes to positions up front so the main loop
	// only works with integer positions — no hash lookups.
	for _, h := range hashes {
		rq.add(h)
	}

	for len(rq.queue) > 0 {
		cur := rq.queue[0]
		rq.queue = rq.queue[1:]

		// Use the result bitmap itself as the visited set: if a
		// bit is already set (from a previous walk step or an ORed
		// precomputed bitmap) the object can be skipped.
		if bm.Get(cur.packPos) {
			continue
		}

		// Fast path: use precomputed bitmap when available.
		// Searcher is keyed by idx position (Entry.ObjectPosition).
		reachBm, err := p.searcher.Reachable(cur.idxPos)
		if err == nil {
			bm.Or(reachBm)
			continue
		}
		if !errors.Is(err, ErrNoEntry) {
			return nil, nil, fmt.Errorf("position %d: %w", cur.packPos, err)
		}

		// No precomputed bitmap — mark this object and enqueue children.
		bm.Set(cur.packPos)

		obj, err := p.source.Object(cur.packPos)
		if err != nil {
			return nil, nil, err
		}

		switch obj.Type() {
		case plumbing.CommitObject:
			tree, parents, err := parseCommitObj(obj)
			if err != nil {
				return nil, nil, fmt.Errorf("parsing commit at %d: %w", cur.packPos, err)
			}
			rq.add(tree)
			rq.addAll(parents)

		case plumbing.TreeObject:
			entries, err := parseTreeObj(obj, p.hasher.Size())
			if err != nil {
				return nil, nil, fmt.Errorf("parsing tree at %d: %w", cur.packPos, err)
			}
			rq.addAll(entries)

		case plumbing.TagObject:
			target, err := parseTagObj(obj)
			if err != nil {
				return nil, nil, fmt.Errorf("parsing tag at %d: %w", cur.packPos, err)
			}
			rq.add(target)
		}
	}

	return bm, rq.missing, nil
}

// objPos holds the two position spaces for an object in the pack.
type objPos struct {
	idxPos  uint32 // hash-sorted order (for Searcher)
	packPos uint32 // pack-offset order (for bitmaps)
}

// resolveQueue maps hashes to pack positions as they are enqueued,
// collecting any hashes not found in the pack.
type resolveQueue struct {
	source  PackSource
	queue   []objPos
	missing []plumbing.Hash
}

func (q *resolveQueue) init(src PackSource) {
	q.source = src
}

func (q *resolveQueue) add(h plumbing.Hash) {
	idxPos, packPos, ok := q.source.FindPosition(h)
	if ok {
		q.queue = append(q.queue, objPos{idxPos, packPos})
	} else {
		q.missing = append(q.missing, h)
	}
}

func (q *resolveQueue) addAll(hashes []plumbing.Hash) {
	for _, h := range hashes {
		q.add(h)
	}
}

// parseCommitObj extracts the tree hash and parent hashes from a
// commit object's content.
func parseCommitObj(obj plumbing.EncodedObject) (tree plumbing.Hash, parents []plumbing.Hash, err error) {
	r, err := obj.Reader()
	if err != nil {
		return plumbing.ZeroHash, nil, err
	}
	defer r.Close()

	data, err := io.ReadAll(r)
	if err != nil {
		return plumbing.ZeroHash, nil, err
	}

	for len(data) > 0 {
		nl := bytes.IndexByte(data, '\n')
		if nl < 0 {
			break
		}
		line := string(data[:nl])
		data = data[nl+1:]

		if len(line) == 0 {
			break // end of headers
		}

		switch {
		case len(line) > 5 && line[:5] == "tree ":
			tree, _ = plumbing.FromHex(line[5:])
		case len(line) > 7 && line[:7] == "parent ":
			h, _ := plumbing.FromHex(line[7:])
			parents = append(parents, h)
		}
	}
	return tree, parents, nil
}

// parseTagObj extracts the target hash from a tag object's content.
func parseTagObj(obj plumbing.EncodedObject) (plumbing.Hash, error) {
	r, err := obj.Reader()
	if err != nil {
		return plumbing.ZeroHash, err
	}
	defer r.Close()

	data, err := io.ReadAll(r)
	if err != nil {
		return plumbing.ZeroHash, err
	}

	for len(data) > 0 {
		nl := bytes.IndexByte(data, '\n')
		if nl < 0 {
			break
		}
		line := string(data[:nl])
		data = data[nl+1:]

		if len(line) == 0 {
			break
		}
		if len(line) > 7 && line[:7] == "object " {
			h, _ := plumbing.FromHex(line[7:])
			return h, nil
		}
	}
	return plumbing.ZeroHash, fmt.Errorf("no object header in tag")
}

// parseTreeObj extracts the child object hashes from a tree object.
// hashSize is the raw hash length in bytes (e.g. 20 for SHA-1).
func parseTreeObj(obj plumbing.EncodedObject, hashSize int) ([]plumbing.Hash, error) {
	r, err := obj.Reader()
	if err != nil {
		return nil, err
	}
	defer r.Close()

	data, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}

	var hashes []plumbing.Hash
	for len(data) > 0 {
		// Format: "<mode> <name>\0<raw-hash>"
		nul := bytes.IndexByte(data, 0)
		if nul < 0 || nul+hashSize >= len(data) {
			break
		}
		raw := data[nul+1 : nul+1+hashSize]
		h, _ := plumbing.FromBytes(raw)
		hashes = append(hashes, h)
		data = data[nul+1+hashSize:]
	}
	return hashes, nil
}
