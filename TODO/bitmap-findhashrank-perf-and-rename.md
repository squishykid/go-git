# `FindHashRank` is ambiguously named and O(log n)

## Summary

Two problems, both small, both worth fixing together.

### 1. Name doesn't say which rank it returns

`RevIndex.FindHashRank(h) (uint32, bool)` returns a **pack rank**
(position in pack-offset order), but nothing in the name says so.
`RevIndex` exposes two rank spaces — idxRank and packRank — and calls
like `HashAtIdxRank`/`HashAtPackRank`/`IdxPosAtPackRank` are
unambiguous. `FindHashRank` isn't.

This ambiguity produced a real bug in `bitmap.Encoder.SelectCommits`,
which stored the result as `SelectedCommit.IdxPos`. The bug was
silent — entries were written with the wrong field — and only surfaced
when a non-trivial caller did cross-lookups via `Searcher.Reachable`.

### 2. Implementation is O(log n)

```go
func (o *MemoryRevIndex) FindHashRank(h plumbing.Hash) (uint32, bool) {
    offset, err := o.MemoryIndex.FindOffset(h)
    ...
    // binary search o.packOffsets for offset
}
```

The index is immutable after load. An `offset → packRank` map built
once at construction would make the lookup O(1). `FindHashRank` sits
on the encoder's hot path (called per commit, per tree entry, per
parent).

## Where

- `plumbing/format/revfile/revfile.go` — interface `RevIndex` (~L36),
  `MemoryRevIndex.FindHashRank` (~L87).
- All call sites in `plumbing/format/bitmap/encoder.go` and
  `plumbing/format/bitmap/packer.go`.

## Fix sketch

1. Rename `FindHashRank` → `FindPackRank`. Update the interface
   definition, the `MemoryRevIndex` implementation, and all call sites.
   This is a breaking API change but the method is new enough that few
   external callers exist.

2. In `MemoryRevIndex`, build a `map[int64]uint32` or
   `map[plumbing.Hash]uint32` (the latter avoids the extra offset
   lookup in the hot path) at `NewMemoryRevIndex` time. Replace the
   binary search with a map lookup.

(1) and (2) can be separate commits.

## Scope

Small-medium. The rename is mechanical. The perf change is localized
to `MemoryRevIndex`. Any external implementation of `RevIndex` (e.g.,
entiredb's `mmapIndex`) will need the same treatment.

## Priority

**Medium.** The rename is a bug-prevention measure. The perf is a
moderate win only relevant when `Encode` is running (which itself has
bigger bottlenecks — see `bitmap-reachability-fallback.md`).
