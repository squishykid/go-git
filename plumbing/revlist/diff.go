package revlist

import (
	"fmt"
	"sort"

	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/filemode"
	"github.com/go-git/go-git/v6/plumbing/object"
	"github.com/go-git/go-git/v6/plumbing/storer"
)

// ObjectsDiff computes the set of object hashes reachable from localCommits but not
// reachable from remoteCommits. It walks local and remote commits simultaneously
// in committer-time order, only advancing the remote side far enough to
// determine which local commits are new. Tree objects are then collected in
// topological order (parents before children) so that the seen-set is fully
// populated before any diff walk relies on it.
func ObjectsDiff(
	s storer.EncodedObjectStorer,
	localCommits []plumbing.Hash,
	remoteCommits []plumbing.Hash,
) ([]plumbing.Hash, error) {
	var localQueue []*object.Commit
	var remoteQueue []*object.Commit
	localSeen := make(map[plumbing.Hash]bool)
	remoteSeen := make(map[plumbing.Hash]bool)

	// insertSorted inserts a commit into a slice sorted by committer time
	// descending (newest first). For the small sizes involved in a typical
	// push this is faster than maintaining a heap.
	insertSorted := func(q *[]*object.Commit, c *object.Commit) {
		i := sort.Search(len(*q), func(i int) bool {
			return (*q)[i].Committer.When.Before(c.Committer.When)
		})
		*q = append(*q, nil)
		copy((*q)[i+1:], (*q)[i:])
		(*q)[i] = c
	}

	// Seed queues with tip commits.
	for _, h := range localCommits {
		c, err := object.GetCommit(s, h)
		if err != nil {
			return nil, fmt.Errorf("getting local commit %s: %w", h, err)
		}
		localSeen[h] = true
		insertSorted(&localQueue, c)
	}
	for _, h := range remoteCommits {
		c, err := object.GetCommit(s, h)
		if err != nil {
			continue
		}
		remoteSeen[h] = true
		insertSorted(&remoteQueue, c)
	}

	// Phase 1: Walk commits newest-first to determine which are new (local
	// but not remote-reachable). Collect them without processing trees yet.
	var newCommits []*object.Commit

	for len(localQueue) > 0 {
		// Pop whichever side has the newer commit.
		if len(remoteQueue) > 0 && !remoteQueue[0].Committer.When.Before(localQueue[0].Committer.When) {
			// Remote commit is newer or equal — pop it and mark as known.
			rc := remoteQueue[0]
			remoteQueue = remoteQueue[1:]
			for _, ph := range rc.ParentHashes {
				if remoteSeen[ph] {
					continue
				}
				remoteSeen[ph] = true
				if pc, err := object.GetCommit(s, ph); err == nil {
					insertSorted(&remoteQueue, pc)
				}
			}
			continue
		}

		// Local commit is newer — pop and check.
		lc := localQueue[0]
		localQueue = localQueue[1:]

		if remoteSeen[lc.Hash] {
			// Boundary — remote already has this commit.
			continue
		}

		newCommits = append(newCommits, lc)

		// Insert parents into local queue.
		for _, ph := range lc.ParentHashes {
			if localSeen[ph] {
				continue
			}
			localSeen[ph] = true
			if pc, err := object.GetCommit(s, ph); err == nil {
				insertSorted(&localQueue, pc)
			}
		}
	}

	// Phase 2: Sort new commits in topological order (parents before
	// children) so that a parent's full tree walk populates the seen-set
	// before any child's diff walk relies on it for skipping.
	topoOrder := topoSortCommits(newCommits)

	seen := make(map[plumbing.Hash]bool)
	// complete tracks trees whose entire contents (recursively) are in
	// seen, allowing safe skipping on re-encounter regardless of oldTree.
	complete := make(map[plumbing.Hash]bool)
	var result []plumbing.Hash

	// treeCache maps commit hash → its root tree, avoiding redundant
	// tree fetches when a commit is referenced as another's parent.
	treeCache := make(map[plumbing.Hash]*object.Tree, len(topoOrder))

	for _, lc := range topoOrder {
		if !seen[lc.Hash] {
			seen[lc.Hash] = true
			result = append(result, lc.Hash)
		}

		newTree, err := lc.Tree()
		if err != nil {
			return nil, fmt.Errorf("getting tree for %s: %w", lc.Hash, err)
		}
		treeCache[lc.Hash] = newTree

		var oldTree *object.Tree
		if lc.NumParents() > 0 {
			ph := lc.ParentHashes[0]
			if cached, ok := treeCache[ph]; ok {
				oldTree = cached
			} else if parent, err := lc.Parent(0); err == nil {
				oldTree, _ = parent.Tree()
			}
		}

		if err := collectChangedTreeObjects(s, newTree, oldTree, seen, complete, &result); err != nil {
			return nil, fmt.Errorf("diffing trees for %s: %w", lc.Hash, err)
		}
	}

	return result, nil
}

// topoSortCommits returns commits in topological order (parents before
// children) using Kahn's algorithm. Commits whose parents are outside the
// input set (boundary or root commits) are placed first.
func topoSortCommits(commits []*object.Commit) []*object.Commit {
	if len(commits) <= 1 {
		return commits
	}

	commitMap := make(map[plumbing.Hash]*object.Commit, len(commits))
	inDegree := make(map[plumbing.Hash]int, len(commits))
	for _, c := range commits {
		commitMap[c.Hash] = c
		inDegree[c.Hash] = 0
	}

	// In-degree = number of local children referencing this commit as parent.
	for _, c := range commits {
		for _, ph := range c.ParentHashes {
			if _, ok := commitMap[ph]; ok {
				inDegree[ph]++
			}
		}
	}

	// Seed with leaf commits (no local children).
	var queue []*object.Commit
	for _, c := range commits {
		if inDegree[c.Hash] == 0 {
			queue = append(queue, c)
		}
	}

	// Kahn's: peel leaves, collecting in reverse topological order.
	ordered := make([]*object.Commit, 0, len(commits))
	for len(queue) > 0 {
		c := queue[0]
		queue = queue[1:]
		ordered = append(ordered, c)
		for _, ph := range c.ParentHashes {
			if _, ok := commitMap[ph]; ok {
				inDegree[ph]--
				if inDegree[ph] == 0 {
					queue = append(queue, commitMap[ph])
				}
			}
		}
	}

	// Reverse: leaves-first → roots-first (parents before children).
	for i, j := 0, len(ordered)-1; i < j; i, j = i+1, j-1 {
		ordered[i], ordered[j] = ordered[j], ordered[i]
	}
	return ordered
}

// collectChangedTreeObjects walks newTree, comparing entry hashes against
// oldTree. Subtrees with matching hashes are skipped entirely. Only new or
// modified tree and blob hashes are added to result. Trees whose entire
// contents are confirmed collected are marked in complete for fast skipping.
func collectChangedTreeObjects(
	s storer.EncodedObjectStorer,
	newTree, oldTree *object.Tree,
	seen map[plumbing.Hash]bool,
	complete map[plumbing.Hash]bool,
	result *[]plumbing.Hash,
) error {
	if complete[newTree.Hash] {
		return nil
	}
	if seen[newTree.Hash] {
		return nil
	}
	if oldTree != nil && newTree.Hash == oldTree.Hash {
		return nil
	}
	seen[newTree.Hash] = true
	*result = append(*result, newTree.Hash)

	isComplete := true

	// Build old-entry index. For small trees (≤8 entries), use linear
	// scan instead of allocating a map.
	var oldEntries map[string]plumbing.Hash
	if oldTree != nil && len(oldTree.Entries) > 8 {
		oldEntries = make(map[string]plumbing.Hash, len(oldTree.Entries))
		for _, e := range oldTree.Entries {
			oldEntries[e.Name] = e.Hash
		}
	}

	for _, e := range newTree.Entries {
		if e.Mode == filemode.Submodule {
			continue
		}

		// Check if entry is unchanged from old tree.
		if oldTree != nil {
			if unchanged, oldHash := entryUnchanged(e.Name, e.Hash, oldEntries, oldTree); unchanged {
				// Entry unchanged — skip but check completeness.
				if e.Mode == filemode.Dir {
					if !complete[e.Hash] {
						isComplete = false
					}
				} else if !seen[e.Hash] {
					isComplete = false
				}
				continue
			} else if e.Mode == filemode.Dir && oldHash != plumbing.ZeroHash {
				// Both sides are present with different hashes.
				// Compare subtree hashes before fetching objects.
				if seen[e.Hash] {
					continue
				}
				newSub, err := object.GetTree(s, e.Hash)
				if err != nil {
					return fmt.Errorf("getting subtree %s: %w", e.Hash, err)
				}
				oldSub, err := object.GetTree(s, oldHash)
				if err != nil {
					oldSub = nil
				}
				if err := collectChangedTreeObjects(s, newSub, oldSub, seen, complete, result); err != nil {
					return err
				}
				if !complete[e.Hash] {
					isComplete = false
				}
				continue
			}
		}

		if seen[e.Hash] {
			continue
		}

		if e.Mode == filemode.Dir {
			newSub, err := object.GetTree(s, e.Hash)
			if err != nil {
				return fmt.Errorf("getting subtree %s: %w", e.Hash, err)
			}
			if err := collectChangedTreeObjects(s, newSub, nil, seen, complete, result); err != nil {
				return err
			}
			if !complete[e.Hash] {
				isComplete = false
			}
		} else {
			seen[e.Hash] = true
			*result = append(*result, e.Hash)
		}
	}

	if isComplete {
		complete[newTree.Hash] = true
	}

	return nil
}

// entryUnchanged checks whether name exists in the old tree with the given hash.
// Returns (unchanged bool, oldHash). oldHash is non-zero when the name exists
// in the old tree (even if the hash differs).
func entryUnchanged(name string, hash plumbing.Hash, oldEntries map[string]plumbing.Hash, oldTree *object.Tree) (bool, plumbing.Hash) {
	if oldEntries != nil {
		if oldHash, ok := oldEntries[name]; ok {
			return oldHash == hash, oldHash
		}
		return false, plumbing.ZeroHash
	}
	// Linear scan for small trees.
	for i := range oldTree.Entries {
		if oldTree.Entries[i].Name == name {
			oh := oldTree.Entries[i].Hash
			return oh == hash, oh
		}
	}
	return false, plumbing.ZeroHash
}
