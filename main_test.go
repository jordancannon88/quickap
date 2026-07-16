package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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
		"applications": "apps", "other": "other files", "others": "other files",
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

func TestCategoryMatches(t *testing.T) {
	images := categoryBySub("images")
	other := categoryBySub("other")
	if !images.matches(".jpg") || images.matches(".xyz") || images.matches("") {
		t.Error("images should match .jpg only")
	}
	// other matches exactly the complement of every claimed extension.
	for _, claimed := range []string{".jpg", ".pdf", ".mp3", ".mkv", ".zip", ".exe"} {
		if other.matches(claimed) {
			t.Errorf("other should not match %s — a category claims it", claimed)
		}
	}
	for _, unclaimed := range []string{".xyz", ".json", ".go", ""} {
		if !other.matches(unclaimed) {
			t.Errorf("other should match unclaimed extension %q", unclaimed)
		}
	}
}

func TestScanRoutesToOther(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"photo.jpg", "song.mp3", "data.json", "README", "notes.XYZ"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(name), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	byCategory, skipped := scan(dir, categories, false, nil)
	if skipped != 0 {
		t.Fatalf("skipped = %d, want 0", skipped)
	}
	if n := len(byCategory["images"]); n != 1 {
		t.Errorf("images = %d files, want 1", n)
	}
	if n := len(byCategory["music"]); n != 1 {
		t.Errorf("music = %d files, want 1", n)
	}
	got := map[string]bool{}
	for _, e := range byCategory["other files"] {
		got[filepath.Base(e.path)] = true
	}
	if len(got) != 3 || !got["data.json"] || !got["README"] || !got["notes.XYZ"] {
		t.Errorf("other files = %v, want data.json, README, notes.XYZ", got)
	}
}

func TestScanIgnoresSymlinks(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "real.jpg"), []byte("jpegdata"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sub", "notes.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("real.jpg", filepath.Join(dir, "link.jpg")); err != nil {
		t.Skipf("cannot create symlinks here: %v", err)
	}
	if err := os.Symlink("sub", filepath.Join(dir, "dirlink")); err != nil {
		t.Fatal(err)
	}

	byCategory, skipped := scan(dir, categories, false, nil)
	if skipped != 0 {
		t.Errorf("skipped = %d, want 0 (symlinks should be ignored, not counted)", skipped)
	}
	var total int
	for key, entries := range byCategory {
		total += len(entries)
		for _, e := range entries {
			switch filepath.Base(e.path) {
			case "link.jpg", "dirlink":
				t.Errorf("%s: symlink %s was indexed", key, e.path)
			}
		}
	}
	// Exactly the two regular files: real.jpg and sub/notes.txt.
	if total != 2 {
		t.Errorf("indexed %d files, want 2", total)
	}
	if n := len(byCategory["images"]); n != 1 {
		t.Errorf("images = %d files, want 1", n)
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

func TestCopyFileVerified(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.txt")
	if err := os.WriteFile(src, []byte("hello world"), 0o644); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(dir, "dst.txt")
	if err := copyFileVerified(src, dst); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "hello world" {
		t.Errorf("dst content = %q, want %q", data, "hello world")
	}

	// A missing source fails and must not leave a destination behind.
	bad := filepath.Join(dir, "copy-of-missing.txt")
	if err := copyFileVerified(filepath.Join(dir, "missing.txt"), bad); err == nil {
		t.Error("copying a missing source should fail")
	}
	if _, err := os.Lstat(bad); !os.IsNotExist(err) {
		t.Error("failed copy left a destination file behind")
	}
}

// organizeResults scans root and runs duplicate detection, mirroring how
// main assembles the catResults that organizeFiles consumes.
func organizeResults(t *testing.T, root string) []catResult {
	t.Helper()
	byCategory, skipped := scan(root, categories, false, nil)
	if skipped != 0 {
		t.Fatalf("skipped = %d, want 0", skipped)
	}
	var results []catResult
	for _, cat := range categories {
		entries := byCategory[cat.key]
		groups, unreadable, _ := findDuplicates(entries, nil, false)
		if unreadable != 0 {
			t.Fatalf("unreadable = %d, want 0", unreadable)
		}
		results = append(results, catResult{cat: cat, entries: entries, groups: groups})
	}
	return results
}

// treeFiles returns every regular file under dir, as slash-separated paths
// relative to dir, mapped to its content.
func treeFiles(t *testing.T, dir string) map[string]string {
	t.Helper()
	got := map[string]string{}
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		got[filepath.ToSlash(rel)] = string(data)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestOrganizeFiles(t *testing.T) {
	root := t.TempDir()
	write := func(name, content string) {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("a-photo.jpg", "jpeg-bytes")
	write("z-copy.jpg", "jpeg-bytes") // duplicate of a-photo.jpg — must not be copied
	write("song.mp3", "mp3-bytes")
	write("notes.txt", "first notes")
	write("sub/notes.txt", "different notes") // same name, different content — needs a suffix
	write("data.json", "{}")                  // lands in Other

	dest := t.TempDir()
	if err := organizeFiles(root, dest, organizeResults(t, root), false, ""); err != nil {
		t.Fatal(err)
	}

	want := map[string]string{
		"Images/a-photo.jpg":    "jpeg-bytes",
		"Music/song.mp3":        "mp3-bytes",
		"Documents/notes.txt":   "first notes",
		"Documents/notes-2.txt": "different notes",
		"Other/data.json":       "{}",
	}
	if got := treeFiles(t, dest); !mapsEqual(got, want) {
		t.Errorf("organized tree = %v, want %v", got, want)
	}

	// Sources must be untouched — organize copies, never moves.
	if _, err := os.Stat(filepath.Join(root, "a-photo.jpg")); err != nil {
		t.Error("source file should still exist after -organize")
	}

	// Re-running against the same destination must copy nothing twice:
	// every file is recognized as already present, no -2/-3 suffixes pile up.
	if err := organizeFiles(root, dest, organizeResults(t, root), false, ""); err != nil {
		t.Fatal(err)
	}
	if got := treeFiles(t, dest); !mapsEqual(got, want) {
		t.Errorf("tree after re-run = %v, want unchanged %v", got, want)
	}
}

func TestOrganizeFilesMove(t *testing.T) {
	root := t.TempDir()
	write := func(name, content string) {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("a-photo.jpg", "jpeg-bytes")
	write("z-copy.jpg", "jpeg-bytes") // duplicate — skipped, must stay in place
	write("song.mp3", "mp3-bytes")
	write("notes.txt", "first notes")
	write("sub/notes.txt", "different notes")

	dest := t.TempDir()
	if err := organizeFiles(root, dest, organizeResults(t, root), true, ""); err != nil {
		t.Fatal(err)
	}

	want := map[string]string{
		"Images/a-photo.jpg":    "jpeg-bytes",
		"Music/song.mp3":        "mp3-bytes",
		"Documents/notes.txt":   "first notes",
		"Documents/notes-2.txt": "different notes",
	}
	if got := treeFiles(t, dest); !mapsEqual(got, want) {
		t.Errorf("organized tree = %v, want %v", got, want)
	}

	// Moved sources are gone; the skipped duplicate copy is left in place.
	if got := treeFiles(t, root); !mapsEqual(got, map[string]string{"z-copy.jpg": "jpeg-bytes"}) {
		t.Errorf("source tree after move = %v, want only the skipped duplicate z-copy.jpg", got)
	}
}

func TestOrganizeFailureList(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"ok.txt", "gone.txt"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(name), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	results := organizeResults(t, root)
	// Deleting a scanned file before organizing forces a copy failure
	// regardless of privileges or filesystem.
	gonePath := filepath.Join(root, "gone.txt")
	if err := os.Remove(gonePath); err != nil {
		t.Fatal(err)
	}

	dest := t.TempDir()
	if err := organizeFiles(root, dest, results, false, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dest, "Documents", "ok.txt")); err != nil {
		t.Error("the readable file should still be organized:", err)
	}
	listPath := filepath.Join(dest, organizeFailuresName)
	data, err := os.ReadFile(listPath)
	if err != nil {
		t.Fatal("failure list should exist after a failed copy:", err)
	}
	list := string(data)
	if !strings.Contains(list, gonePath+"\t") {
		t.Errorf("failure list should name %s with a tab-separated reason; got:\n%s", gonePath, list)
	}
	if strings.Contains(list, filepath.Join(root, "ok.txt")) {
		t.Errorf("failure list should not mention files that were organized; got:\n%s", list)
	}

	// A clean re-run (the broken file no longer exists to scan) must
	// remove the now-stale failure list.
	if err := organizeFiles(root, dest, organizeResults(t, root), false, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(listPath); !os.IsNotExist(err) {
		t.Error("stale failure list should be removed by a run without failures")
	}
}

func TestOrganizeActionLog(t *testing.T) {
	root := t.TempDir()
	write := func(name, content string) {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("a.jpg", "jpeg-bytes")
	write("b.jpg", "jpeg-bytes") // duplicate of a.jpg
	write("notes.txt", "notes")

	dest := t.TempDir()
	logPath := filepath.Join(t.TempDir(), "actions.log")
	if err := organizeFiles(root, dest, organizeResults(t, root), false, logPath); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal("action log should exist:", err)
	}
	log := string(data)
	for _, line := range []string{
		"copied\t" + filepath.Join(root, "a.jpg") + "\t" + filepath.Join(dest, "Images", "a.jpg"),
		"copied\t" + filepath.Join(root, "notes.txt") + "\t" + filepath.Join(dest, "Documents", "notes.txt"),
		"skipped-duplicate\t" + filepath.Join(root, "b.jpg") + "\tduplicate of " + filepath.Join(root, "a.jpg"),
		"# summary: 2 copied, 0 already present, 1 duplicate copies skipped, 0 failed, 0 sources not deleted",
	} {
		if !strings.Contains(log, line+"\n") {
			t.Errorf("action log missing line %q; got:\n%s", line, log)
		}
	}

	// A second run appends — the log keeps both runs' history, and the
	// re-run records its files as already present.
	if err := organizeFiles(root, dest, organizeResults(t, root), false, logPath); err != nil {
		t.Fatal(err)
	}
	data, err = os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	log = string(data)
	if got := strings.Count(log, "# quickap organize log"); got != 2 {
		t.Errorf("log has %d run headers, want 2 (append, not overwrite)", got)
	}
	if !strings.Contains(log, "already-present\t"+filepath.Join(root, "a.jpg")+"\t") {
		t.Errorf("re-run should log already-present actions; got:\n%s", log)
	}
}

// In move mode, a source whose exact content already sits in the
// destination is deleted without another copy being made — the move's
// outcome is already on disk, verified by hash.
func TestOrganizeMoveAlreadyPresent(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "notes.txt"), []byte("same content"), 0o644); err != nil {
		t.Fatal(err)
	}
	dest := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dest, "Documents"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dest, "Documents", "notes.txt"), []byte("same content"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := organizeFiles(root, dest, organizeResults(t, root), true, ""); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"Documents/notes.txt": "same content"}
	if got := treeFiles(t, dest); !mapsEqual(got, want) {
		t.Errorf("destination = %v, want unchanged %v (no notes-2.txt)", got, want)
	}
	if _, err := os.Lstat(filepath.Join(root, "notes.txt")); !os.IsNotExist(err) {
		t.Error("source should be deleted once its content is verified in the destination")
	}
}

func mapsEqual(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

func TestDestName(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "on-disk.txt"), []byte("disk content"), 0o644); err != nil {
		t.Fatal(err)
	}
	src := t.TempDir()
	write := func(name, content string) *fileEntry {
		path := filepath.Join(src, name)
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		return &fileEntry{path: path, ext: filepath.Ext(name)}
	}

	taken := map[string]bool{}
	if name, already, err := destName(dir, write("fresh.txt", "x"), taken); err != nil || already || name != "fresh.txt" {
		t.Errorf("fresh name = %q, %v, %v; want fresh.txt, false, nil", name, already, err)
	}
	// Same lowercased name this run — suffixed even though disk is free
	// (case-insensitive filesystems would otherwise overwrite).
	if name, already, err := destName(dir, write("FRESH.txt", "y"), taken); err != nil || already || name != "FRESH-2.txt" {
		t.Errorf("case-colliding name = %q, %v, %v; want FRESH-2.txt, false, nil", name, already, err)
	}
	// Identical content already on disk — skip, keep the existing name.
	if name, already, err := destName(dir, write("on-disk.txt", "disk content"), taken); err != nil || !already || name != "on-disk.txt" {
		t.Errorf("identical on disk = %q, %v, %v; want on-disk.txt, true, nil", name, already, err)
	}
	// Different content behind the on-disk name — suffixed.
	if name, already, err := destName(dir, write("on-disk.txt", "other content"), taken); err != nil || already || name != "on-disk-2.txt" {
		t.Errorf("differing on disk = %q, %v, %v; want on-disk-2.txt, false, nil", name, already, err)
	}
}

func TestTildePath(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory:", err)
	}
	cases := []struct{ in, want string }{
		{filepath.Join(home, ".cache", "quickap", "hashes.json"), "~/.cache/quickap/hashes.json"},
		{home, "~"},
		{home + "stuff/x", home + "stuff/x"}, // sibling dir sharing the prefix, not under home
		{"/etc/hosts", "/etc/hosts"},
	}
	for _, c := range cases {
		if got := tildePath(c.in); got != c.want {
			t.Errorf("tildePath(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
