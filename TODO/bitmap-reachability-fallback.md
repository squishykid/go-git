# `commitReachability` falls off a cliff when parent isn't selected

## Summary

In `Encoder.Encode`, commits are processed in topological order so each
commit's parent bitmaps are ready to `Or` into its own. `commitReachability`
handles a missing parent this way:

```go
for _, p := range parents {
    if pbm, ok := computed[p]; ok {
        bm.Or(pbm)                      // O(bitmap size)
    } else {
        pbm, err := e.reachability(p)   // full graph walk from p
        ...
        computed[p] = pbm
        bm.Or(pbm)
    }
}
```

When `SelectCommits` uses `maxDistance > 0` (the normal case — git
defaults to 100), only ~1% of commits are selected. That means **most
parents of selected commits are not themselves selected**, so for every
selected commit we hit the `reachability(p)` branch for most of its
parents.

`reachability` does a full BFS from `p`, walking commits → trees →
blobs and calling `pf.Get` for every object. With N selected commits
and M objects per graph, cost is roughly O(N × M). On a medium pack
(~150 MB, ~10k commits, ~500k objects) this hangs the encoder for
minutes even though the intended big-O for encoding is close to O(M).

## Where

- `plumbing/format/bitmap/encoder.go` — `commitReachability` (~L190),
  `reachability` (~L497).

## Why this slipped through

Test fixtures use `maxDistance = 0`, which selects every commit and
guarantees every parent is in `computed`. The slow path is never
exercised in CI.

## Fix sketch

Either path works; pick based on memory budget:

1. **Dense internal computation, sparse output.** Build reachability
   bitmaps for every commit reachable from the tips (not just selected
   ones) in one topologically-sorted pass. Only emit entries for
   `selected`. Memory: one bitmap per commit transiently, but bitmaps
   can be dropped as soon as all children have consumed them — so peak
   memory ≈ width of the DAG × bitmap size.

2. **Cache `reachability` results globally** keyed by hash. First call
   for a commit walks; subsequent calls return the cached bitmap. This
   is a one-line change but memory is proportional to the number of
   non-selected commits ever fed through `reachability`, which can be
   most of the graph.

Option 1 is closer to what C git's `pack-bitmap-write.c` does
(`fill_bitmap_commit`): it keeps a single `reused` bitmap as it walks
the sorted commit list and only snapshots it at selected commits. That
gives O(commits + objects) total and constant auxiliary memory.

## Scope

Medium. Restructures the core of `Encode`. Guard with a benchmark on a
moderately-sized pack with `maxDistance=100` so regressions show up.

## Priority

**High.** This is the reason bitmap encoding is unusably slow on real
repositories in practice, and hides behind the tests because fixtures
use dense selection.
