# Replace design-question TODO in `search.go` with spec rationale

## Summary

`Searcher` keys its entry lookup table by idx rank:

```go
// TODO: optimise with scannedOffsets
// TODO: why is this based on the index rank? why not packfile rank?
entryIndex map[uint32]int
```

The second TODO invites a future maintainer to "fix" the indexing by
switching to pack rank, which would silently break interop with git.

The answer is: git's on-disk bitmap format (documented in
`Documentation/technical/bitmap-format.txt` in the git source) specifies
that a bitmap entry's `ObjectPosition` field is the position of the
commit object **in the pack index** — i.e., the hash-sorted idx rank.
Matching that is a correctness requirement for reading and writing
files interoperable with C git.

(This same TODO misdirected the `SelectCommits` implementation once
already; see the fix that produced this TODO file for the full story.)

## Where

- `plumbing/format/bitmap/search.go:17-18`.

## Fix sketch

Replace the question with a reference:

```go
// entryIndex maps an entry's ObjectPosition (an idx rank, per the
// bitmap file format — see Documentation/technical/bitmap-format.txt
// in the git source) to the entry ordinal in the bitmap file.
// NOTE: do not re-key this on pack rank; doing so would break
// interop with C git. Callers that have a pack rank must convert
// via RevIndex.IdxPosAtPackRank before calling Reachable.
entryIndex map[uint32]int
```

The `TODO: optimise with scannedOffsets` can stay if it's still
relevant — that's a separate improvement.

## Scope

Trivial. Comment change only.

## Priority

**Low.** Pure documentation. Worth doing alongside any of the other
bitmap fixes so the next maintainer doesn't re-introduce the old bug.
