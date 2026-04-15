# Commented-out bitmap-reuse optimization

## Summary

Two functions have commented-out "reuse an old bitmap" blocks and a
benchmark for the feature is entirely commented out:

- `Encoder.commitReachability` (encoder.go ~L195): `//if old != nil { ... }`
  with `TODO: calculate bitmap transform from old bitmap to new bitmap`.
- `Encoder.reachability` (encoder.go ~L500): same TODO, same scaffolding.
- `encoder_test.go` ~L293: `// TODO re-enable when we have reuse
  implemented again` above a fully commented-out `BenchmarkEncodeReuse`.

This looks like an abandoned optimization path. The public `Encode`
signature doesn't accept an `old *Searcher` parameter, so the code
isn't reachable anyway — it's just visual clutter.

## Where

- `plumbing/format/bitmap/encoder.go` ~L195-L207, ~L493-L496.
- `plumbing/format/bitmap/encoder_test.go` ~L293-L309.

## Fix sketch

Pick one:

1. **Delete the commented blocks and the commented benchmark.** The
   intent is recorded in git history; the comments clutter the current
   file. If reuse becomes a real feature, implement it fresh with
   current-day context.

2. **File an issue** capturing the design idea (incremental bitmap
   encoding reusing the previous pack's bitmaps when most commits are
   unchanged) and then delete the comments. This keeps the intent
   tracked without leaving dead code.

Option 2 is preferred if the maintainer still wants to pursue the
optimization.

## Scope

Trivial. Text deletion.

## Priority

**Low.** Pure hygiene. Good task for a "janitor" pass.
