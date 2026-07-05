package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestHumanSize(t *testing.T) {
	cases := []struct {
		in   int64
		want string
	}{
		{0, "0 B"},
		{512, "512 B"},
		{1024, "1.0 KB"},
		{144000, "140.6 KB"},
		{1<<20 + 512*1024, "1.5 MB"},
		{1 << 30, "1.0 GB"},
	}
	for _, c := range cases {
		if got := humanSize(c.in); got != c.want {
			t.Errorf("humanSize(%d) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestStripANSI(t *testing.T) {
	in := "\x1b[1m\x1b[36mhello\x1b[0m world"
	if got := stripANSI(in); got != "hello world" {
		t.Errorf("stripANSI = %q, want %q", got, "hello world")
	}
}

func TestPad(t *testing.T) {
	if got := pad("\x1b[33m42\x1b[0m", 5); stripANSI(got) != "   42" {
		t.Errorf("pad = %q, want width 5", stripANSI(got))
	}
	if got := padRight("ab", 4); got != "ab  " {
		t.Errorf("padRight = %q, want %q", got, "ab  ")
	}
}

func TestIgnoredDir(t *testing.T) {
	root := "/scan"
	cases := []struct {
		path     string
		patterns []string
		want     bool
	}{
		{"/scan/node_modules", []string{"node_modules"}, true},
		{"/scan/a/b/node_modules", []string{"node_modules"}, true},
		{"/scan/files/cache", []string{"files/cache"}, true},
		{"/scan/other/files/cache", []string{"files/cache"}, false},
		{"/scan/files", []string{"files/cache"}, false},
		{"/scan/dist", []string{"dist/"}, true},
		{"/scan/keep", []string{"skip"}, false},
	}
	for _, c := range cases {
		if got := ignoredDir(root, c.path, c.patterns); got != c.want {
			t.Errorf("ignoredDir(%q, %v) = %v, want %v", c.path, c.patterns, got, c.want)
		}
	}
}

func TestCategoryBySub(t *testing.T) {
	cases := map[string]string{
		"images": "images", "docs": "documents", "documents": "documents",
		"video": "videos", "archive": "archives", "app": "apps",
		"applications": "apps",
	}
	for sub, key := range cases {
		cat := categoryBySub(sub)
		if cat == nil || cat.key != key {
			t.Errorf("categoryBySub(%q) = %v, want key %q", sub, cat, key)
		}
	}
	if categoryBySub("bogus") != nil {
		t.Error("categoryBySub(bogus) should be nil")
	}
}

func TestMultiFlag(t *testing.T) {
	var m multiFlag
	m.Set("a,b")
	m.Set(" c ")
	if len(m) != 3 || m[0] != "a" || m[1] != "b" || m[2] != "c" {
		t.Errorf("multiFlag = %v, want [a b c]", m)
	}
}

func TestFindDuplicates(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) *fileEntry {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		return &fileEntry{path: path, ext: filepath.Ext(name), size: int64(len(content))}
	}
	entries := []*fileEntry{
		write("a.jpg", "same-bytes"),
		write("b.jpg", "same-bytes"),
		write("c.png", "same-bytes"),  // duplicate across extensions
		write("d.jpg", "same-size!!"), // same size, different content
		write("e.jpg", "unique"),
	}

	groups, unreadable, stats := findDuplicates(entries, nil, false)
	if unreadable != 0 {
		t.Fatalf("unreadable = %d, want 0", unreadable)
	}
	if stats.hashed != 3 || stats.cached != 0 {
		t.Errorf("stats = %+v, want 3 hashed (the size-collision candidates), 0 cached", stats)
	}
	if len(groups) != 1 || len(groups[0]) != 3 {
		t.Fatalf("groups = %d with %d files, want 1 group of 3", len(groups), len(groups[0]))
	}
	// Lexically first path is the original; the rest are duplicates.
	if groups[0][0].path != entries[0].path || groups[0][0].dup {
		t.Errorf("original should be a.jpg and not flagged dup")
	}
	var dups int
	for _, e := range entries {
		if e.dup {
			dups++
		}
	}
	if dups != 2 {
		t.Errorf("dup count = %d, want 2", dups)
	}
}

func TestHashCache(t *testing.T) {
	c := &hashCache{
		file:    filepath.Join(t.TempDir(), "cache.json"),
		touched: map[string]bool{},
		Entries: map[string]cacheEntry{},
	}

	c.put("/a.jpg", 100, 42, "deadbeef")
	if h, ok := c.get("/a.jpg", 100, 42); !ok || h != "deadbeef" {
		t.Errorf("get after put = %q, %v; want deadbeef, true", h, ok)
	}
	if _, ok := c.get("/a.jpg", 100, 43); ok {
		t.Error("get with changed mtime should miss")
	}
	if _, ok := c.get("/a.jpg", 101, 42); ok {
		t.Error("get with changed size should miss")
	}
	if _, ok := c.get("/other.jpg", 100, 42); ok {
		t.Error("get of unknown path should miss")
	}

	// nil cache is a valid no-op
	var nc *hashCache
	nc.put("/x", 1, 1, "h")
	if _, ok := nc.get("/x", 1, 1); ok {
		t.Error("nil cache should always miss")
	}
	nc.save() // must not panic
}

// Regression: cache lookups on the scan goroutine race with puts from
// hash workers; hashCache must be internally synchronized. Run with -race.
func TestHashCacheConcurrent(t *testing.T) {
	c := &hashCache{
		file:    filepath.Join(t.TempDir(), "cache.json"),
		touched: map[string]bool{},
		Entries: map[string]cacheEntry{},
	}
	var wg sync.WaitGroup
	for w := 0; w < 8; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < 500; i++ {
				path := fmt.Sprintf("/f%d-%d.jpg", w, i)
				c.put(path, int64(i), int64(i), "h")
				c.get(path, int64(i), int64(i))
				c.get("/f0-0.jpg", 0, 0)
			}
		}(w)
	}
	wg.Wait()
}

func TestCacheSavePrunesDeleted(t *testing.T) {
	dir := t.TempDir()
	kept := filepath.Join(dir, "kept.jpg")
	if err := os.WriteFile(kept, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	c := &hashCache{
		file:    filepath.Join(dir, "cache.json"),
		root:    dir,
		touched: map[string]bool{},
		Entries: map[string]cacheEntry{
			kept:                           {Size: 1, Mtime: 1, Hash: "aa"},
			filepath.Join(dir, "gone.jpg"): {Size: 2, Mtime: 2, Hash: "bb"},
		},
	}
	c.save()

	loaded := &hashCache{Entries: map[string]cacheEntry{}}
	data, err := os.ReadFile(c.file)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, loaded); err != nil {
		t.Fatal(err)
	}
	if _, ok := loaded.Entries[kept]; !ok {
		t.Error("existing file's entry should survive save")
	}
	if len(loaded.Entries) != 1 {
		t.Errorf("entries after prune = %d, want 1", len(loaded.Entries))
	}
}

// Pruning is scoped to the scan root: a missing file outside the root
// keeps its entry (it belongs to a different scanned tree), so scanning a
// subdirectory can't evict hashes computed by a parent-directory scan.
func TestCacheSavePruneScopedToRoot(t *testing.T) {
	dir := t.TempDir()
	inRootGone := filepath.Join(dir, "sub", "gone.jpg")
	outside := filepath.Join(t.TempDir(), "elsewhere.jpg") // missing, different tree
	c := &hashCache{
		file:    filepath.Join(dir, "cache.json"),
		root:    dir,
		touched: map[string]bool{},
		Entries: map[string]cacheEntry{
			inRootGone: {Size: 1, Mtime: 1, Hash: "aa"},
			outside:    {Size: 2, Mtime: 2, Hash: "bb"},
		},
	}
	c.save()

	loaded := &hashCache{Entries: map[string]cacheEntry{}}
	data, err := os.ReadFile(c.file)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, loaded); err != nil {
		t.Fatal(err)
	}
	if _, ok := loaded.Entries[inRootGone]; ok {
		t.Error("missing file under root should be pruned")
	}
	if _, ok := loaded.Entries[outside]; !ok {
		t.Error("missing file outside root must be kept — it belongs to another tree")
	}
}

func TestFindDuplicatesUsesCache(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) *fileEntry {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		return &fileEntry{path: path, ext: filepath.Ext(name), size: info.Size(), mtime: info.ModTime().UnixNano()}
	}
	a := write("a.jpg", "same-bytes")
	b := write("b.jpg", "same-bytes")

	c := &hashCache{
		file:    filepath.Join(dir, "cache.json"),
		touched: map[string]bool{},
		Entries: map[string]cacheEntry{},
	}

	// First run: both hashed, cache populated.
	_, _, s1 := findDuplicates([]*fileEntry{a, b}, c, false)
	if s1.hashed != 2 || s1.cached != 0 {
		t.Fatalf("first run stats = %+v, want 2 hashed", s1)
	}

	// Second run: both served from cache, no reads.
	a2 := &fileEntry{path: a.path, ext: a.ext, size: a.size, mtime: a.mtime}
	b2 := &fileEntry{path: b.path, ext: b.ext, size: b.size, mtime: b.mtime}
	groups, _, s2 := findDuplicates([]*fileEntry{a2, b2}, c, false)
	if s2.hashed != 0 || s2.cached != 2 {
		t.Errorf("second run stats = %+v, want 2 cached", s2)
	}
	if len(groups) != 1 || !b2.dup {
		t.Error("cached hashes should still produce the duplicate group")
	}

	// -verify ignores the cache and re-hashes.
	a3 := &fileEntry{path: a.path, ext: a.ext, size: a.size, mtime: a.mtime}
	b3 := &fileEntry{path: b.path, ext: b.ext, size: b.size, mtime: b.mtime}
	_, _, s3 := findDuplicates([]*fileEntry{a3, b3}, c, true)
	if s3.hashed != 2 || s3.cached != 0 {
		t.Errorf("verify run stats = %+v, want 2 hashed", s3)
	}
}
