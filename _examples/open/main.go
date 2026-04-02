package main

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/go-git/go-git/v6"
	. "github.com/go-git/go-git/v6/_examples"
	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/revlist"
)

func main() {
	CheckArgs("<path> <ref1> <ref2>")
	path := os.Args[1]
	ref1 := os.Args[2]
	ref2 := os.Args[3]

	r, err := git.PlainOpen(path)
	CheckIfError(err)

	local, err := r.Reference(plumbing.ReferenceName(ref1), true)
	CheckIfError(err)

	remote, err := r.Reference(plumbing.ReferenceName(ref2), true)
	CheckIfError(err)

	Info("local:  %s (%s)", local.Name(), local.Hash())
	Info("remote: %s (%s)", remote.Name(), remote.Hash())

	localHashes := []plumbing.Hash{local.Hash()}
	remoteHashes := []plumbing.Hash{remote.Hash()}

	// All objects reachable from ref1.
	Info("git rev-list %s --objects --count: %s", ref1, gitRevListCount(path, ref1))

	bench("revlist.Objects ref1", func() ([]plumbing.Hash, error) {
		return revlist.Objects(r.Storer, localHashes, nil)
	})

	bench("revlist.ObjectsDiff ref1", func() ([]plumbing.Hash, error) {
		return revlist.ObjectsDiff(r.Storer, localHashes, nil)
	})

	// Objects reachable from ref1 but not ref2.
	Info("\ngit rev-list %s..%s --objects --count: %s", ref1, ref2, gitRevListCount(path, ref1, "^"+ref2))

	bench("revlist.Objects ref1..ref2", func() ([]plumbing.Hash, error) {
		return revlist.Objects(r.Storer, localHashes, remoteHashes)
	})

	bench("revlist.ObjectsDiff ref1..ref2", func() ([]plumbing.Hash, error) {
		return revlist.ObjectsDiff(r.Storer, localHashes, remoteHashes)
	})
}

func bench(label string, fn func() ([]plumbing.Hash, error)) {
	runtime.GC()
	var before runtime.MemStats
	runtime.ReadMemStats(&before)

	t0 := time.Now()
	hashes, err := fn()
	elapsed := time.Since(t0)
	CheckIfError(err)

	runtime.GC()
	var after runtime.MemStats
	runtime.ReadMemStats(&after)

	Info("%s", label)
	fmt.Printf("  objects: %d  time: %v  alloc: %d MB  total-alloc: %d MB  gc-cycles: %d\n",
		len(hashes),
		elapsed,
		after.Alloc/1024/1024,
		(after.TotalAlloc-before.TotalAlloc)/1024/1024,
		after.NumGC-before.NumGC,
	)
}

func gitRevListCount(path string, refs ...string) string {
	args := append([]string{"-C", path, "rev-list", "--objects", "--count"}, refs...)
	out, err := exec.Command("git", args...).Output()
	if err != nil {
		return fmt.Sprintf("error: %v", err)
	}
	return strings.TrimSpace(string(out))
}
