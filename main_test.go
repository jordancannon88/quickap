package main

import (
	"os"
	"path/filepath"
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

	groups, unreadable := findDuplicates(entries)
	if unreadable != 0 {
		t.Fatalf("unreadable = %d, want 0", unreadable)
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
