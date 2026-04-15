# Two minor consistency issues

Grouped because both are small; neither is a correctness bug on its
own but both surprise callers.

## 1. `Bitmap.Or` panics on length mismatch

```go
func (b Bitmap) Or(other Bitmap) {
    if len(other) > len(b) {
        panic("bitmap: Or: other bitmap is longer than receiver")
    }
    for i := range len(other) {
        b[i] |= other[i]
    }
}
```

Callers combining bitmaps of different widths (e.g., searcher bitmaps
sized for the bitmap pack vs. an accumulator sized explicitly by the
caller) have to remember to call `Extend` first. Forgetting causes a
panic at runtime.

### Fix

Either:

- **Auto-extend inside `Or`.** Change the signature to
  `func (b Bitmap) Or(other Bitmap) Bitmap` and return the
  possibly-grown slice, matching `Extend`'s style. Removes the
  panic path.

- **Document the panic** more loudly in the doc comment and leave the
  behavior. Callers who want auto-extend can call `Extend` explicitly.

Auto-extend is slightly nicer ergonomically but changes the mutating
in-place contract. Prefer documentation unless multiple internal
callers already need the extension.

## 2. `walkTree` sets the bit before reading the object

```go
for len(stack) > 0 {
    cur := stack[len(stack)-1]
    stack = stack[:len(stack)-1]

    packPos, ok := e.index.FindHashRank(cur)
    if !ok {
        continue
    }
    if bm.Get(packPos) {
        continue
    }
    bm.Set(packPos)                  // <-- set first

    obj, err := e.pf.Get(cur)        // <-- then read
    if err != nil {
        return err                   // bit is set but we bail
    }
    ...
}
```

If `pf.Get` fails for a hash that `FindHashRank` successfully
resolved, the bitmap has a bit set for a hash we couldn't actually
read. Since we return the error immediately, the caller will discard
the bitmap, so this is cosmetic. But if someone later changes the
error handling to "log and continue" it would silently produce a
bitmap that claims coverage of an unreadable object.

### Fix

Swap the order: `pf.Get` first, set the bit only after success. Zero
runtime cost; future-proofs against error-handling changes.

## Where

- `plumbing/format/bitmap/bitmap.go` — `Bitmap.Or` (~L76).
- `plumbing/format/bitmap/encoder.go` — `Encoder.walkTree` (~L253).

## Scope

Trivial.

## Priority

**Low.** Hygiene. Pick up alongside any adjacent work in these files.
