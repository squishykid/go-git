# `verifyReachable` fails on submodule gitlinks

## Summary

`bitmap.Encoder.SelectCommits` invokes `verifyReachable` on each
commit's tree. That function recursively walks the tree and calls
`pf.Get(hash)` on every entry, including gitlink entries whose hash
points to a submodule commit not stored in this pack. `pf.Get` errors
for missing hashes and the whole `SelectCommits` call aborts.

Consequence: bitmap encoding fails on any repository that contains a
submodule, which is the common case for any non-trivial codebase.

## Where

- `plumbing/format/bitmap/encoder.go` — `verifyReachable` (~L470).
- `plumbing/format/bitmap/packer.go` — `parseTreeObj` (~L272) returns
  raw hashes with no mode info.

## Why this slipped through

`walkTree` (encoder.go:253, used by `commitReachability`) silently
`continue`s when `FindHashRank` misses a hash — so the main encoding
path handles submodules correctly. Only the pre-verification path is
broken, and the fixture test packs have no submodules.

## Reproducer

Any repo with a `.gitmodules` / submodule entry and a tree that
references it. `Encoder.SelectCommits(tips, 0)` returns an error that
wraps `pf.Get` for the submodule's commit hash.

## Fix sketch

Two options, not mutually exclusive:

1. **Extend `parseTreeObj` to return (hash, mode) pairs.** Callers
   (`verifyReachable`, `walkTree`) can then skip `filemode.Submodule`
   entries. Small behavior change but matches what `object.Tree.Decode`
   already does, so there's no new interpretation of the tree format.

2. **Make `verifyReachable` tolerant of missing objects**, matching
   `walkTree`'s behavior: on `pf.Get` error, `continue` instead of
   returning. Simpler, but weakens the "verify" guarantee — the function
   no longer distinguishes "submodule" from "broken pack".

Option 1 is preferred because it preserves the defensive
"pack-has-closure" check for non-submodule entries.

## Scope

Small. Touches three functions. Worth a regression test using a
fixture pack that contains a submodule entry (point entiredb's
`makeTreeWithSubmoduleEntry` helper — or build one inline from raw
bytes, mode `160000`).

## Priority

**High.** This is the main reason go-git's bitmap encoder is
effectively unusable on realistic repos today.
