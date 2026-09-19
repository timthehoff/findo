package smbclient

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeFileInfo implements os.FileInfo for tests, without needing a real SMB
// session or filesystem.
type fakeFileInfo struct {
	name  string
	isDir bool
}

func (fi fakeFileInfo) Name() string { return fi.name }
func (fi fakeFileInfo) Size() int64  { return 42 }
func (fi fakeFileInfo) Mode() os.FileMode {
	if fi.isDir {
		return os.ModeDir
	}
	return 0
}
func (fi fakeFileInfo) ModTime() time.Time { return time.Unix(1700000000, 0) }
func (fi fakeFileInfo) IsDir() bool        { return fi.isDir }
func (fi fakeFileInfo) Sys() interface{}   { return nil }

func dir(name string) fakeFileInfo  { return fakeFileInfo{name: name, isDir: true} }
func file(name string) fakeFileInfo { return fakeFileInfo{name: name, isDir: false} }

// fakeTree maps SMB-style ("."-rooted, backslash-joined) directory paths to
// their contents, standing in for fs.ReadDir.
type fakeTree map[string][]os.FileInfo

func (t fakeTree) readDir(dir string) ([]os.FileInfo, error) {
	infos, ok := t[dir]
	if !ok {
		return nil, fmt.Errorf("no such directory: %q", dir)
	}
	return infos, nil
}

// collector gathers fn callbacks from concurrent walk workers.
type collector struct {
	mu      sync.Mutex
	entries []Entry
}

func (c *collector) add(e Entry) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries = append(c.entries, e)
}

func (c *collector) paths() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	paths := make([]string, len(c.entries))
	for i, e := range c.entries {
		paths[i] = e.Path
	}
	sort.Strings(paths)
	return paths
}

func TestWalkDiscoversWholeTree(t *testing.T) {
	tree := fakeTree{
		".":          {file("a.txt"), dir("sub")},
		"sub":        {file("b.txt"), dir("nested")},
		`sub\nested`: {file("c.txt")},
	}

	var c collector
	errs := walk(".", tree.readDir, c.add)
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}

	want := []string{"a.txt", "sub", "sub/b.txt", "sub/nested", "sub/nested/c.txt"}
	got := c.paths()
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("got paths %v, want %v", got, want)
	}
}

func TestWalkIsBestEffortOnListingErrors(t *testing.T) {
	tree := fakeTree{
		".":  {dir("ok"), dir("broken"), file("root.txt")},
		"ok": {file("fine.txt")},
		// "broken" deliberately has no entry in tree, so readDir errors on it.
	}

	var c collector
	errs := walk(".", tree.readDir, c.add)

	if len(errs) != 1 {
		t.Fatalf("expected exactly one listing error, got %d: %v", len(errs), errs)
	}
	if !strings.Contains(errs[0].Error(), "broken") {
		t.Fatalf("error should mention the failed directory, got: %v", errs[0])
	}

	// The rest of the tree should still have been walked despite the one
	// failure — best effort, not fail-fast.
	got := c.paths()
	want := []string{"broken", "ok", "ok/fine.txt", "root.txt"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("got paths %v, want %v", got, want)
	}
}

func TestWalkWideTreeConcurrently(t *testing.T) {
	// A directory with many subdirectories, each with a file, exercises the
	// worker pool's fan-out/fan-in without relying on a specific
	// concurrency level — run with -race to catch any data races in
	// dirQueue or the shared error/collector state.
	const n = 500
	tree := fakeTree{".": nil}
	var root []os.FileInfo
	for i := 0; i < n; i++ {
		name := fmt.Sprintf("d%d", i)
		root = append(root, dir(name))
		tree[name] = []os.FileInfo{file("leaf.txt")}
	}
	tree["."] = root

	var c collector
	errs := walk(".", tree.readDir, c.add)
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if len(c.paths()) != n*2 {
		t.Fatalf("expected %d entries, got %d", n*2, len(c.paths()))
	}
}
