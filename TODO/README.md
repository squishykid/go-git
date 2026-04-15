# TODO

A parking lot for concrete issues found in go-git that are worth fixing
but not blocking current work. Each file is a single, self-contained
task — enough context that someone (or a future agent) can pick one up,
understand the problem, and produce a fix without re-doing the
investigation.

Entries here describe **what** is wrong and **why it matters**, and
sketch a fix. They are not design documents; they are small actionable
items.

## Current entries

- `bitmap-submodule-verify.md` — `verifyReachable` fails on submodule gitlinks.
- `bitmap-reachability-fallback.md` — sparse `SelectCommits` triggers O(N×M) full-graph walks.
- `bitmap-verify-per-commit.md` — `verifyReachable` called once per selected commit.
- `bitmap-searcher-index-rank-rationale.md` — replace design-question TODO with spec rationale.
- `bitmap-findhashrank-perf-and-rename.md` — `FindHashRank` is O(log n) and ambiguously named.
- `bitmap-stale-reuse-code.md` — commented-out bitmap-reuse optimization path.
- `bitmap-minor-hygiene.md` — `Bitmap.Or` length check, `walkTree` bit-then-get order.

## How to add an entry

- One issue per file.
- Name: `<area>-<slug>.md`.
- Cover: summary, where (file:line), reproducer or pointer to a test,
  sketch of a fix, risk / scope.
- Keep it short. If it's growing past a page, it's probably a design
  doc, not a TODO.