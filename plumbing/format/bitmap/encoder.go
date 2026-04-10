package bitmap

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"slices"

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
// commits lists the commits to create bitmap entries for, in
// topological order (children before parents). [SelectCommits]
// produces this ordering.
//
// If old is non-nil, its precomputed reachability bitmaps are reused
// for commits that exist in both the old and new packs.
func (e *Encoder) Encode(w io.Writer, packChecksum []byte, commits []SelectedCommit, old *Searcher) error {
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

	// --- Topological sort ---
	//
	// Ensure children come before parents so that when computing a
	// commit's reachability bitmap we can OR in already-computed
	// parent bitmaps instead of re-walking the full history.
	sorted, err := TopoSort(e.source, commits)
	if err != nil {
		return fmt.Errorf("topological sort: %w", err)
	}

	// --- Compute reachability bitmaps ---
	type entry struct {
		idxPos    uint32
		bitmap    Bitmap
		xorOffset uint8
		encoded   EWAH
	}

	computed := make(map[plumbing.Hash]Bitmap, len(sorted))
	entries := make([]entry, 0, len(sorted))
	slices.Reverse(sorted)

	for _, sc := range sorted {
		bm, err := e.commitReachability(sc.Hash, bmLen, computed, old)
		if err != nil {
			return fmt.Errorf("computing reachability for %s: %w", sc.Hash, err)
		}
		computed[sc.Hash] = bm

		entries = append(entries, entry{
			idxPos: sc.IdxPos,
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
	_, err = w.Write(checksum)
	return err
}

// commitReachability computes the reachability bitmap for a single
// commit. It walks only the commit's tree, then ORs in the bitmaps
// of parent commits from the computed map. If old is non-nil, it is
// checked first.
func (e *Encoder) commitReachability(
	commit plumbing.Hash,
	bmLen int,
	computed map[plumbing.Hash]Bitmap,
	old *Searcher,
) (Bitmap, error) {
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

	_, packPos, ok := e.source.FindPosition(commit)
	if !ok {
		return nil, fmt.Errorf("commit %s not found in pack", commit)
	}

	bm := make(Bitmap, bmLen)
	bm.Set(packPos)

	// Walk the commit's tree.
	obj, err := e.source.Object(packPos)
	if err != nil {
		return nil, err
	}
	tree, parents, err := parseCommitObj(obj)
	if err != nil {
		return nil, err
	}
	if err := e.walkTree(tree, bm); err != nil {
		return nil, err
	}

	// OR in parent bitmaps that were already computed.
	for _, p := range parents {
		if pbm, ok := computed[p]; ok {
			bm.Or(pbm)
		} else {
			// Parent not in the selected set — walk its full graph.
			pbm, err := e.reachability(p, bmLen, old)
			if err != nil {
				return nil, err
			}
			bm.Or(pbm)
		}
	}

	return bm, nil
}

// walkTree sets bits for the tree at h and all its descendants.
func (e *Encoder) walkTree(h plumbing.Hash, bm Bitmap) error {
	stack := []plumbing.Hash{h}
	for len(stack) > 0 {
		cur := stack[len(stack)-1]
		stack = stack[:len(stack)-1]

		_, packPos, ok := e.source.FindPosition(cur)
		if !ok {
			continue
		}
		if bm.Get(packPos) {
			continue
		}
		bm.Set(packPos)

		obj, err := e.source.Object(packPos)
		if err != nil {
			return err
		}
		if obj.Type() == plumbing.TreeObject {
			entries, err := parseTreeObj(obj, e.hasher.Size())
			if err != nil {
				return err
			}
			stack = append(stack, entries...)
		}
	}
	return nil
}

// ErrMissingObject is returned by [SelectCommits] when a reachable
// object is not present in the pack, violating the full-DAG closure
// requirement.
var ErrMissingObject = errors.New("pack missing reachable object")

// TopoSort reorders commits so that children appear before their
// parents (topological order). This is the ordering expected by
// [Encoder.Encode] so that parent bitmaps are available when
// computing a child's reachability.
//
// Only parent relationships between commits in the input set are
// considered. Parents outside the set are ignored.
func TopoSort(source PackSource, commits []SelectedCommit) ([]SelectedCommit, error) {
	// Build the set of selected commits and an adjacency list of
	// parent edges within the set.
	idx := make(map[plumbing.Hash]int, len(commits))
	for i, sc := range commits {
		idx[sc.Hash] = i
	}

	// children[i] = indices of commits whose parent is commits[i].
	children := make([][]int, len(commits))
	// inDegree[i] = number of parents of commits[i] that are in the set.
	inDegree := make([]int, len(commits))

	for i, sc := range commits {
		_, packPos, ok := source.FindPosition(sc.Hash)
		if !ok {
			continue
		}
		obj, err := source.Object(packPos)
		if err != nil {
			return nil, fmt.Errorf("reading commit %s: %w", sc.Hash, err)
		}
		_, parents, err := parseCommitObj(obj)
		if err != nil {
			return nil, fmt.Errorf("parsing commit %s: %w", sc.Hash, err)
		}
		for _, p := range parents {
			if pi, ok := idx[p]; ok {
				children[pi] = append(children[pi], i)
				inDegree[i]++
			}
		}
	}

	// Kahn's algorithm: start from commits with no in-set parents
	// (the roots / oldest commits), emit in reverse so children
	// come first.
	queue := make([]int, 0, len(commits))
	for i, d := range inDegree {
		if d == 0 {
			queue = append(queue, i)
		}
	}

	order := make([]SelectedCommit, 0, len(commits))
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		order = append(order, commits[cur])
		for _, child := range children[cur] {
			inDegree[child]--
			if inDegree[child] == 0 {
				queue = append(queue, child)
			}
		}
	}

	// Reverse: Kahn's produces parents-first; we want children-first.
	for i, j := 0, len(order)-1; i < j; i, j = i+1, j-1 {
		order[i], order[j] = order[j], order[i]
	}

	return order, nil
}

// SelectedCommit pairs a commit hash with its position in the pack index
// (hash-sorted order), as needed by [Encoder.Encode].
type SelectedCommit struct {
	Hash   plumbing.Hash
	IdxPos uint32
}

// SelectCommits walks the commit graph backward from the given tips
// and returns a list of commits suitable for bitmap entries. It also
// verifies that the pack has full closure: every object reachable
// from the selected commits must be in the pack.
//
// The selection uses a distance-based heuristic: a commit is selected
// when it is at least maxDistance commits from the previous selection
// along its first-parent chain, or when it is the tip itself.
// A maxDistance of 0 selects every commit.
func SelectCommits(source PackSource, tips []plumbing.Hash, maxDistance int) ([]SelectedCommit, error) {
	type commitInfo struct {
		hash   plumbing.Hash
		idxPos uint32
	}

	// BFS backward from tips, collecting commits in walk order.
	var commits []commitInfo
	visited := make(map[plumbing.Hash]struct{})

	queue := make([]plumbing.Hash, 0, len(tips))
	for _, h := range tips {
		if _, seen := visited[h]; !seen {
			visited[h] = struct{}{}
			queue = append(queue, h)
		}
	}

	hashSize := 20 // default SHA-1
	if source.ObjectCount() > 0 {
		// Detect hash size from the first object.
		obj, err := source.Object(0)
		if err == nil {
			hashSize = obj.Hash().Size()
		}
	}

	for len(queue) > 0 {
		h := queue[0]
		queue = queue[1:]

		idxPos, packPos, ok := source.FindPosition(h)
		if !ok {
			return nil, fmt.Errorf("%w: commit %s", ErrMissingObject, h)
		}

		obj, err := source.Object(packPos)
		if err != nil {
			return nil, fmt.Errorf("reading commit %s: %w", h, err)
		}
		if obj.Type() != plumbing.CommitObject {
			continue
		}

		commits = append(commits, commitInfo{h, idxPos})

		tree, parents, err := parseCommitObj(obj)
		if err != nil {
			return nil, fmt.Errorf("parsing commit %s: %w", h, err)
		}

		// Verify the tree (and its children) are in the pack.
		if err := verifyReachable(source, tree, visited, hashSize); err != nil {
			return nil, err
		}

		for _, p := range parents {
			if _, seen := visited[p]; !seen {
				visited[p] = struct{}{}
				queue = append(queue, p)
			}
		}
	}

	// Select commits using distance heuristic.
	// Walk in reverse (oldest first) so selections propagate forward.
	selected := make(map[plumbing.Hash]struct{})
	distSinceSelected := maxDistance // force-select the first (oldest) commit

	for i := len(commits) - 1; i >= 0; i-- {
		c := commits[i]
		isTip := false
		for _, t := range tips {
			if c.hash == t {
				isTip = true
				break
			}
		}

		distSinceSelected++
		if isTip || maxDistance == 0 || distSinceSelected >= maxDistance {
			selected[c.hash] = struct{}{}
			distSinceSelected = 0
		}
	}

	// Return selected commits in walk order (newest first).
	result := make([]SelectedCommit, 0, len(selected))
	for _, c := range commits {
		if _, ok := selected[c.hash]; ok {
			result = append(result, SelectedCommit{Hash: c.hash, IdxPos: c.idxPos})
		}
	}
	return result, nil
}

// verifyReachable checks that the tree at h and all its descendants
// are in the pack. It adds verified hashes to visited.
func verifyReachable(source PackSource, h plumbing.Hash, visited map[plumbing.Hash]struct{}, hashSize int) error {
	stack := []plumbing.Hash{h}
	for len(stack) > 0 {
		cur := stack[len(stack)-1]
		stack = stack[:len(stack)-1]

		if _, seen := visited[cur]; seen {
			continue
		}
		visited[cur] = struct{}{}

		_, packPos, ok := source.FindPosition(cur)
		if !ok {
			return fmt.Errorf("%w: %s", ErrMissingObject, cur)
		}

		obj, err := source.Object(packPos)
		if err != nil {
			return fmt.Errorf("reading object %s: %w", cur, err)
		}

		if obj.Type() == plumbing.TreeObject {
			entries, err := parseTreeObj(obj, hashSize)
			if err != nil {
				return fmt.Errorf("parsing tree %s: %w", cur, err)
			}
			stack = append(stack, entries...)
		}
	}
	return nil
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
