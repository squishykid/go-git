# `verifyReachable` runs once per selected commit

## Summary

`Encoder.SelectCommits` calls `verifyReachable(e.pf, tree, visited,
hashSize)` for every commit it enqueues. `verifyReachable` DFS-walks
the commit's tree, calling `pf.Get` (zlib decompress) on each newly
seen hash.

The shared `visited` map deduplicates across commits, so for a repo
where file paths are stable, the second commit's walk mostly hits
already-visited nodes. Still, every new blob in a commit's tree costs
a `pf.Get`. On a repo with thousands of commits and tens of thousands
of files, this materializes as hundreds of thousands of zlib
decompresses during `SelectCommits`.

In entiredb benchmarking, this plus the `reachability` fallback
(separate TODO) made `SelectCommits` take >10 minutes on a 150 MB
pack that the bitmap-less walker processed in ~60 ms.

## Where

- `plumbing/format/bitmap/encoder.go` — `SelectCommits` (~L372),
  `verifyReachable` (~L470).

## Why it's there

The function's name says what it does: it verifies that the pack
contains the full closure the bitmap will eventually reference. That's
a meaningful invariant — bitmap entries are useless if their
reachability isn't actually in the pack.

But: the encoder will also walk those same objects in
`commitReachability`/`walkTree` to build the bitmap, which ALREADY
panics or errors on missing objects. So the verify pass is redundant
with the encode pass.

## Fix sketch

1. **Remove `verifyReachable` entirely.** Let `commitReachability`
   surface missing-object errors when they happen. Simplest and
   fastest.

2. **If the verify is kept for a reason** (e.g., fail early rather than
   halfway through encoding), promote `visited` to a type-tagged map so
   it can also feed the bitmap-build phase — don't re-walk in
   `walkTree` what `verifyReachable` already traversed.

Option 1 is preferred unless someone can articulate why we want a
separate verification pass.

## Scope

Small. Delete `verifyReachable` and its call site. Keep the tree walk
in `commitReachability`/`walkTree` as the authoritative pass. Add a
test that encoding a pack with a missing blob fails with a clear error
message.

## Priority

**Medium.** Removing it gives a large perf win, but the big-O problem
(see `bitmap-reachability-fallback.md`) dominates and needs fixing
first.
