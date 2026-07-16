// quickap indexes application, archive, document, image, video, and
// music files under a directory (the running user's home directory by
// default) — plus an "other" category for every file those don't claim
// — and prints a per-category summary: total count, count and size per
// extension, total size, and duplicate statistics based on file
// content (SHA-256).
package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// version is stamped at build time by the release workflow
// (-ldflags "-X main.version=..."); local builds report "dev".
var version = "dev"

type category struct {
	cmd      string // subcommand name, e.g. "docs"
	key      string // report/dir name, e.g. "documents"
	label    string // section heading, e.g. "Documents"
	singular string // used in labels, e.g. "document"
	hue      int    // xterm-256 accent color
	exts     map[string]bool
}

var categories = []category{
	{
		cmd: "apps", key: "apps", label: "Applications", singular: "application", hue: 114,
		exts: map[string]bool{
			".exe": true, ".msi": true, ".dmg": true, ".pkg": true,
			".deb": true, ".rpm": true, ".appimage": true, ".apk": true,
		},
	},
	{
		cmd: "archives", key: "archives", label: "Archives", singular: "archive", hue: 179,
		exts: map[string]bool{
			".zip": true, ".7z": true, ".7zip": true, ".rar": true,
			".tar": true, ".gz": true, ".bz2": true, ".xz": true,
			".zst": true, ".tgz": true, ".tbz": true, ".iso": true,
		},
	},
	{
		cmd: "docs", key: "documents", label: "Documents", singular: "document", hue: 75,
		exts: map[string]bool{
			".pdf": true, ".doc": true, ".docx": true, ".xls": true,
			".xlsx": true, ".ppt": true, ".pptx": true, ".odt": true,
			".ods": true, ".odp": true, ".txt": true, ".md": true,
			".rtf": true, ".csv": true, ".epub": true,
		},
	},
	{
		cmd: "images", key: "images", label: "Images", singular: "image", hue: 80,
		exts: map[string]bool{
			".jpg": true, ".jpeg": true, ".png": true, ".gif": true,
			".webp": true, ".bmp": true, ".svg": true, ".tiff": true,
			".tif": true, ".heic": true, ".heif": true, ".avif": true,
			".ico": true,
		},
	},
	{
		cmd: "videos", key: "videos", label: "Videos", singular: "video", hue: 215,
		exts: map[string]bool{
			".mp4": true, ".mkv": true, ".avi": true, ".mov": true,
			".wmv": true, ".flv": true, ".webm": true, ".m4v": true,
			".mpg": true, ".mpeg": true, ".3gp": true, ".ogv": true,
		},
	},
	{
		cmd: "music", key: "music", label: "Music", singular: "music", hue: 176,
		exts: map[string]bool{
			".mp3": true, ".flac": true, ".wav": true, ".aac": true,
			".ogg": true, ".m4a": true, ".wma": true, ".opus": true,
			".aiff": true, ".aif": true, ".mid": true, ".midi": true,
		},
	},
	{
		// exts nil: matches every extension no category above claims.
		cmd: "other", key: "other files", label: "Other", singular: "other", hue: 250,
	},
}

// claimedExts is the union of every concrete category's extension set;
// the "other" category matches whatever is missing from it.
var claimedExts = func() map[string]bool {
	u := map[string]bool{}
	for _, cat := range categories {
		for e := range cat.exts {
			u[e] = true
		}
	}
	return u
}()

// matches reports whether a file extension belongs to the category. A
// category without its own extension set (other) claims every extension
// no concrete category lists — including files with no extension at all.
func (c category) matches(ext string) bool {
	if c.exts != nil {
		return c.exts[ext]
	}
	return !claimedExts[ext]
}

// categoryBySub resolves a subcommand name (or alias) to its category.
func categoryBySub(sub string) *category {
	aliases := map[string]string{
		"documents": "docs", "video": "videos", "archive": "archives",
		"app": "apps", "applications": "apps", "others": "other",
	}
	if a, ok := aliases[sub]; ok {
		sub = a
	}
	for i := range categories {
		if categories[i].cmd == sub {
			return &categories[i]
		}
	}
	return nil
}

type fileEntry struct {
	path  string
	ext   string
	size  int64
	mtime int64  // UnixNano, for hash-cache validity
	hash  string // set only for files that share a size with another file
	dup   bool
}

type extStat struct {
	ext      string
	count    int
	size     int64
	uniq     int
	uniqSize int64
	dup      int
	dupSize  int64
}

type totals struct {
	count    int
	size     int64
	uniq     int
	uniqSize int64
	dup      int
	dupSize  int64
}

type catResult struct {
	cat     category
	entries []*fileEntry
	groups  [][]*fileEntry
	stats   map[string]*extStat
	t       totals
}

// ANSI styling, disabled when stdout is not a terminal or NO_COLOR is set.
var colorOn = func() bool {
	if os.Getenv("NO_COLOR") != "" {
		return false
	}
	fi, err := os.Stdout.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}()

func style(code, s string) string {
	if !colorOn {
		return s
	}
	return "\x1b[" + code + "m" + s + "\x1b[0m"
}

// c256 colors s with an xterm-256 foreground color.
func c256(n int, s string) string {
	if !colorOn {
		return s
	}
	return fmt.Sprintf("\x1b[38;5;%dm%s\x1b[0m", n, s)
}

func bold(s string) string    { return style("1", s) }
func dim(s string) string     { return style("2", s) }
func ital(s string) string    { return style("3", s) }
func cyan(s string) string    { return c256(80, s) }
func green(s string) string   { return c256(114, s) }
func magenta(s string) string { return c256(176, s) }
func yellow(s string) string  { return c256(179, s) }
func red(s string) string     { return c256(203, s) }

// Bordered-table helpers. Borders are dim so data stays in the foreground.

// tLine draws a horizontal border, e.g. tLine("╭","┬","╮", w) or
// tLine("├","┼","┤", w).
func tLine(l, m, r string, widths []int) string {
	parts := make([]string, len(widths))
	for i, w := range widths {
		parts[i] = strings.Repeat("─", w)
	}
	return "  " + dim(l+strings.Join(parts, m)+r)
}

// spacious switches tables from compact rows (default) to airy ones with
// a blank row between line items; set by the -spacious flag.
var spacious bool

// tSpace draws an empty row — vertical breathing room between line items.
func tSpace(widths []int) string {
	cells := make([]string, len(widths))
	return tRow(widths, strings.Repeat("l", len(widths)), cells...)
}

// tRow draws one table row; aligns holds 'l' or 'r' per column. Cells may
// contain ANSI styling — widths are measured without escape codes.
func tRow(widths []int, aligns string, cells ...string) string {
	var b strings.Builder
	b.WriteString("  " + dim("│"))
	for i, cell := range cells {
		w := widths[i] - 2
		if aligns[i] == 'l' {
			cell = padRight(cell, w)
		} else {
			cell = pad(cell, w)
		}
		b.WriteString(" " + cell + " " + dim("│"))
	}
	return b.String()
}

func humanSize(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(1024), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(b)/float64(div), "KMGTPE"[exp])
}

func main() {
	args := os.Args[1:]
	sub := ""
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		sub, args = args[0], args[1:]
	}

	var active []category
	var rootArg string
	switch sub {
	case "":
		active = categories
	case "help":
		if len(args) > 0 {
			printCommandHelp(args[0])
		} else {
			printHelp()
		}
		return
	case "version":
		fmt.Println("quickap " + version)
		return
	default:
		cat := categoryBySub(sub)
		if cat != nil {
			sub = cat.cmd
			active = []category{*cat}
			break
		}
		// Not a command — a directory to scan? (quickap ~/Pictures)
		if info, err := os.Stat(sub); err == nil && info.IsDir() {
			rootArg = sub
			sub = ""
			active = categories
			break
		}
		fmt.Fprintf(os.Stderr, "quickap: unknown command %q (expected %s, \"help\", \"version\", or a directory)\nrun \"quickap help\" for usage\n", sub, commandList())
		os.Exit(1)
	}

	fs := flag.NewFlagSet("quickap "+sub, flag.ExitOnError)
	var listDups bool
	fs.BoolVar(&listDups, "list-duplicates", false, "list duplicate groups with file paths")
	fs.BoolVar(&listDups, "ld", false, "shorthand for -list-duplicates")
	var listUnique bool
	fs.BoolVar(&listUnique, "list-unique", false, "list unique files (every file that is not a duplicate copy) with paths")
	fs.BoolVar(&listUnique, "lu", false, "shorthand for -list-unique")
	var listLarge bool
	fs.BoolVar(&listLarge, "list-large", false, "list the 50 largest files, largest first")
	fs.BoolVar(&listLarge, "ll", false, "shorthand for -list-large")
	hidden := fs.Bool("hidden", false, "include hidden directories in the scan")
	showVersion := fs.Bool("version", false, "print version and exit")
	var ignores multiFlag
	fs.Var(&ignores, "ignore", "skip `DIR` while scanning; repeat or comma-separate for multiple")
	noCache := fs.Bool("no-cache", false, "disable the hash cache for this run")
	verify := fs.Bool("verify", false, "re-hash all duplicate candidates, ignoring cached hashes")
	clearCache := fs.Bool("clear-cache", false, "delete the hash cache and exit")
	var verbose bool
	fs.BoolVar(&verbose, "verbose", false, "show scan details (timing, hash-cache stats and location, hints)")
	fs.BoolVar(&verbose, "vv", false, "shorthand for -verbose")
	fs.BoolVar(&spacious, "spacious", false, "add vertical space between table rows (default: compact)")
	organizeDir := fs.String("organize", "", "copy every indexed file that is not a duplicate copy into `DIR`, one subdirectory per category, verifying each copy by hash")
	organizeMoveDir := fs.String("organize-move", "", "like -organize, but move: each file is hash-verified at the destination before its source is deleted")
	organizeLog := fs.String("organize-log", "", "append a log of every -organize/-organize-move action to `FILE`")
	// -move and -delete act on one category, so they require a category
	// command; the bare command indexes and reports only.
	moveDir, deleteDups := new(string), new(bool)
	if sub != "" {
		moveDir = fs.String("move", "", "move each duplicate group (original + copies) into `DIR`")
		deleteDups = fs.Bool("delete", false, "delete duplicate files, keeping each group's original")
	}
	if sub == "" {
		fs.Usage = printHelp
	} else {
		fs.Usage = func() { printCommandHelp(sub) }
	}
	fs.Parse(args)

	if *showVersion {
		fmt.Println("quickap " + version)
		return
	}
	if *clearCache {
		wipeCache()
		return
	}
	if *deleteDups && *moveDir != "" {
		fmt.Fprintln(os.Stderr, "quickap: -delete and -move are mutually exclusive")
		os.Exit(1)
	}
	if *organizeDir != "" && *organizeMoveDir != "" {
		fmt.Fprintln(os.Stderr, "quickap: -organize and -organize-move are mutually exclusive")
		os.Exit(1)
	}
	orgDir, orgMove := *organizeDir, false
	if *organizeMoveDir != "" {
		orgDir, orgMove = *organizeMoveDir, true
	}
	if orgDir != "" && (*deleteDups || *moveDir != "") {
		fmt.Fprintln(os.Stderr, "quickap: -organize and -organize-move cannot be combined with -move or -delete")
		os.Exit(1)
	}
	if *organizeLog != "" && orgDir == "" {
		fmt.Fprintln(os.Stderr, "quickap: -organize-log requires -organize or -organize-move")
		os.Exit(1)
	}

	// A trailing positional argument is the directory to scan.
	switch rest := fs.Args(); {
	case len(rest) > 1, len(rest) == 1 && rootArg != "":
		fmt.Fprintln(os.Stderr, "quickap: expected at most one directory (note: flags must come before the directory)")
		os.Exit(1)
	case len(rest) == 1:
		rootArg = rest[0]
	}

	var root string
	var err error
	if rootArg == "" {
		// No directory given: scan the running user's home directory
		// (/home/... on Linux, /Users/... on macOS, C:\Users\... on
		// Windows).
		root, err = os.UserHomeDir()
	} else {
		root, err = filepath.Abs(rootArg)
		if err == nil {
			info, statErr := os.Stat(root)
			if statErr != nil {
				err = statErr
			} else if !info.IsDir() {
				err = fmt.Errorf("%s is not a directory", rootArg)
			} else {
				// WalkDir does not follow symlinks, so a symlinked
				// root would scan nothing; walk its target instead.
				root, err = filepath.EvalSymlinks(root)
			}
		}
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "quickap:", err)
		os.Exit(1)
	}

	start := time.Now()
	byCategory, skipped := scan(root, active, *hidden, ignores)

	var cache *hashCache
	if !*noCache {
		cache = loadCache(root)
	}
	var hs hashStats
	var results []catResult
	for _, cat := range active {
		entries := byCategory[cat.key]
		groups, unreadable, s := findDuplicates(entries, cache, *verify)
		skipped += unreadable
		hs.hashed += s.hashed
		hs.cached += s.cached

		stats := map[string]*extStat{}
		var t totals
		for _, e := range entries {
			s, ok := stats[e.ext]
			if !ok {
				s = &extStat{ext: e.ext}
				stats[e.ext] = s
			}
			s.count++
			s.size += e.size
			t.count++
			t.size += e.size
			if e.dup {
				s.dup++
				s.dupSize += e.size
				t.dup++
				t.dupSize += e.size
			} else {
				s.uniq++
				s.uniqSize += e.size
				t.uniq++
				t.uniqSize += e.size
			}
		}
		results = append(results, catResult{cat, entries, groups, stats, t})
	}
	cache.save()
	elapsed := time.Since(start)

	fmt.Println()
	fmt.Printf("  %s %s\n", bold(magenta("◆ quickap")), dim("· file index"))
	if home, homeErr := os.UserHomeDir(); homeErr == nil && home != "" {
		fmt.Printf("  %s\n", dim("home: "+home))
	}
	fmt.Printf("  %s\n", dim("scan: "+root))
	if len(results) == 1 {
		renderSection(results[0].cat, results[0].stats, results[0].t)
	} else {
		renderOverview(results, verbose)
	}
	renderFooter(skipped, elapsed, hs, cache, *noCache, verbose)

	if listUnique {
		for _, r := range results {
			renderUnique(root, r.cat, r.entries)
		}
	}
	if listLarge {
		for _, r := range results {
			renderLarge(root, r.cat, r.entries)
		}
	}
	if listDups {
		for _, r := range results {
			renderGroups(root, r.cat, r.groups)
		}
	}
	if orgDir != "" {
		if err := organizeFiles(root, orgDir, results, orgMove, *organizeLog); err != nil {
			fmt.Fprintln(os.Stderr, "quickap:", err)
			os.Exit(1)
		}
	}
	if *moveDir != "" {
		for _, r := range results {
			if err := moveGroups(root, *moveDir, r.cat, r.groups); err != nil {
				fmt.Fprintln(os.Stderr, "quickap:", err)
				os.Exit(1)
			}
		}
	}
	if *deleteDups {
		for _, r := range results {
			deleteGroups(root, r.cat, r.groups)
		}
	}
}

// multiFlag collects repeated (or comma-separated) flag values.
type multiFlag []string

func (m *multiFlag) String() string { return strings.Join(*m, ",") }

func (m *multiFlag) Set(v string) error {
	for _, p := range strings.Split(v, ",") {
		if p = strings.TrimSpace(p); p != "" {
			*m = append(*m, p)
		}
	}
	return nil
}

// ignoredDir reports whether path matches any ignore pattern: a pattern with
// a slash matches the directory's path relative to root, a bare name matches
// any directory with that name.
func ignoredDir(root, path string, patterns []string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		rel = path
	}
	for _, p := range patterns {
		p = filepath.Clean(strings.TrimSuffix(p, "/"))
		if strings.Contains(p, string(filepath.Separator)) {
			if rel == p {
				return true
			}
		} else if filepath.Base(path) == p {
			return true
		}
	}
	return false
}

// scan walks root once and collects every file matching an active category.
// Hidden directories are skipped unless includeHidden is set, ignored
// directories always; unreadable entries are counted, not fatal.
// Non-regular files (symlinks, sockets, FIFOs, devices) are ignored:
// never followed, indexed, or hashed.
//
// Directories are walked in parallel — the walk is syscall-bound
// (one ReadDir per directory plus one lstat per matched file), so a
// bounded pool of goroutines keeps the kernel busy where a serial walk
// would wait on one call at a time. Each directory batches its results
// and takes the shared lock once.
func scan(root string, active []category, includeHidden bool, ignores []string) (map[string][]*fileEntry, int) {
	type hit struct {
		key string
		e   *fileEntry
	}
	byCategory := map[string][]*fileEntry{}
	var skipped int
	var mu sync.Mutex
	// The pool is sized for outstanding filesystem requests, not CPU
	// work, so it gets a floor: even a single-core machine profits from
	// overlapping disk waits on a cold directory cache.
	workers := 4 * runtime.NumCPU()
	if workers < 16 {
		workers = 16
	}
	sem := make(chan struct{}, workers)
	var wg sync.WaitGroup
	var walk func(dir string)
	walk = func(dir string) {
		defer wg.Done()
		// Subdirectory goroutines spawned below block on sem until this
		// directory releases its slot; a parent never waits on its
		// children, so holding the slot while spawning cannot deadlock.
		sem <- struct{}{}
		defer func() { <-sem }()
		dirents, err := os.ReadDir(dir)
		if err != nil {
			mu.Lock()
			skipped++
			mu.Unlock()
			return
		}
		var hits []hit
		unreadable := 0
		for _, d := range dirents {
			name := d.Name()
			if d.IsDir() {
				path := filepath.Join(dir, name)
				if !includeHidden && strings.HasPrefix(name, ".") {
					continue
				}
				if ignoredDir(root, path, ignores) {
					continue
				}
				wg.Add(1)
				go walk(path)
				continue
			}
			if d.Type()&fs.ModeType != 0 {
				// Symlinks would otherwise be indexed with the link's own
				// lstat size while hashing follows to the target; skip all
				// non-regular files instead.
				continue
			}
			ext := strings.ToLower(filepath.Ext(name))
			for _, cat := range active {
				if !cat.matches(ext) {
					continue
				}
				info, err := d.Info()
				if err != nil {
					unreadable++
					break
				}
				hits = append(hits, hit{cat.key, &fileEntry{
					path: filepath.Join(dir, name), ext: ext, size: info.Size(), mtime: info.ModTime().UnixNano(),
				}})
				break
			}
		}
		if len(hits) == 0 && unreadable == 0 {
			return
		}
		mu.Lock()
		defer mu.Unlock()
		for _, h := range hits {
			byCategory[h.key] = append(byCategory[h.key], h.e)
		}
		skipped += unreadable
	}
	wg.Add(1)
	walk(root)
	wg.Wait()
	// The parallel walk collects entries in nondeterministic order; sort
	// by path so every downstream consumer sees a stable view.
	for _, entries := range byCategory {
		sort.Slice(entries, func(i, j int) bool { return entries[i].path < entries[j].path })
	}
	return byCategory, skipped
}

// findDuplicates hashes files that share a byte size, flags every file whose
// content was already seen at a lexically earlier path, and returns the
// identical-content groups (each sorted by path; the first file is treated as
// the original). Files that cannot be read count as unique. Hashes for
// unchanged files are reused from cache unless verify is set.
func findDuplicates(entries []*fileEntry, cache *hashCache, verify bool) ([][]*fileEntry, int, hashStats) {
	bySize := map[int64][]*fileEntry{}
	for _, e := range entries {
		bySize[e.size] = append(bySize[e.size], e)
	}

	var candidates []*fileEntry
	for _, group := range bySize {
		if len(group) > 1 {
			candidates = append(candidates, group...)
		}
	}
	var stats hashStats
	if len(candidates) == 0 {
		return nil, 0, stats
	}

	var unreadable int
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, runtime.NumCPU())
	for _, e := range candidates {
		if !verify {
			if h, ok := cache.get(e.path, e.size, e.mtime); ok {
				e.hash = h
				stats.cached++
				continue
			}
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(e *fileEntry) {
			defer wg.Done()
			defer func() { <-sem }()
			h, err := hashFile(e.path)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				unreadable++
				return
			}
			e.hash = h
			cache.put(e.path, e.size, e.mtime, h)
			stats.hashed++
		}(e)
	}
	wg.Wait()

	byHash := map[string][]*fileEntry{}
	for _, e := range candidates {
		if e.hash != "" {
			byHash[e.hash] = append(byHash[e.hash], e)
		}
	}
	var groups [][]*fileEntry
	for _, group := range byHash {
		if len(group) < 2 {
			continue
		}
		sort.Slice(group, func(i, j int) bool { return group[i].path < group[j].path })
		for _, e := range group[1:] {
			e.dup = true
		}
		groups = append(groups, group)
	}
	sort.Slice(groups, func(i, j int) bool { return groups[i][0].path < groups[j][0].path })
	return groups, unreadable, stats
}

func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// hashCache persists computed SHA-256 hashes between runs so unchanged
// files (same size and mtime) are never re-read. One cache file shared by
// all scans, keyed by absolute file path — so hashing a parent directory
// also warms the cache for later runs against its subdirectories. The
// file uses a compact length-prefixed binary format (hashes.bin): at
// hundreds of thousands of entries, parsing the older JSON layout
// dominated warm-cache runs. A nil *hashCache is a valid no-op cache.
// get/put are safe for concurrent use (lookups on the scan goroutine
// race with puts from hash workers).
type hashCache struct {
	mu      sync.Mutex
	file    string
	root    string   // scan root; pruning is scoped to entries under it
	legacy  []string // older JSON cache files, merged then removed on save
	dirty   bool
	touched map[string]bool
	Entries map[string]cacheEntry
}

type cacheEntry struct {
	Size  int64  `json:"size"`
	Mtime int64  `json:"mtime_ns"`
	Hash  string `json:"sha256"`
}

// cacheMagic starts every binary cache file: format name plus a version
// byte. A file without it (or from a different version) is ignored.
var cacheMagic = []byte("QAPC\x01")

// encodeCache serializes entries into the binary cache format: the magic,
// an entry count, then per entry the path, size, mtime, and hash, with
// varint lengths and values.
func encodeCache(entries map[string]cacheEntry) []byte {
	var b bytes.Buffer
	b.Write(cacheMagic)
	var tmp [binary.MaxVarintLen64]byte
	putUvarint := func(v uint64) { b.Write(tmp[:binary.PutUvarint(tmp[:], v)]) }
	putVarint := func(v int64) { b.Write(tmp[:binary.PutVarint(tmp[:], v)]) }
	putString := func(s string) { putUvarint(uint64(len(s))); b.WriteString(s) }
	putUvarint(uint64(len(entries)))
	for path, e := range entries {
		putString(path)
		putVarint(e.Size)
		putVarint(e.Mtime)
		putString(e.Hash)
	}
	return b.Bytes()
}

// decodeCache parses data produced by encodeCache. ok is false for any
// malformed or foreign input — the caller treats that as an empty cache.
func decodeCache(data []byte) (entries map[string]cacheEntry, ok bool) {
	if !bytes.HasPrefix(data, cacheMagic) {
		return nil, false
	}
	data = data[len(cacheMagic):]
	uvarint := func() (uint64, bool) {
		v, n := binary.Uvarint(data)
		if n <= 0 {
			return 0, false
		}
		data = data[n:]
		return v, true
	}
	varint := func() (int64, bool) {
		v, n := binary.Varint(data)
		if n <= 0 {
			return 0, false
		}
		data = data[n:]
		return v, true
	}
	str := func() (string, bool) {
		l, ok := uvarint()
		if !ok || l > uint64(len(data)) {
			return "", false
		}
		s := string(data[:l])
		data = data[l:]
		return s, true
	}
	count, ok := uvarint()
	if !ok {
		return nil, false
	}
	// The count is a size hint from untrusted bytes; cap the initial
	// allocation so a corrupt header can't balloon memory.
	hint := count
	if hint > 1<<20 {
		hint = 1 << 20
	}
	entries = make(map[string]cacheEntry, hint)
	for i := uint64(0); i < count; i++ {
		path, ok1 := str()
		size, ok2 := varint()
		mtime, ok3 := varint()
		hash, ok4 := str()
		if !ok1 || !ok2 || !ok3 || !ok4 {
			return nil, false
		}
		entries[path] = cacheEntry{Size: size, Mtime: mtime, Hash: hash}
	}
	return entries, true
}

// loadCache reads the shared cache, returning an empty cache on any error
// (missing file, corrupt data) — the cache is an optimization only.
// Legacy JSON caches from older versions (the shared hashes.json, and the
// per-root layout before that) are merged in and cleaned up on save.
func loadCache(root string) *hashCache {
	dir, err := os.UserCacheDir()
	if err != nil {
		return nil
	}
	dir = filepath.Join(dir, "quickap")
	sum := sha256.Sum256([]byte(root))
	return loadCacheFiles(root, filepath.Join(dir, "hashes.bin"), []string{
		filepath.Join(dir, "hashes.json"),
		filepath.Join(dir, hex.EncodeToString(sum[:8])+".json"),
	})
}

// loadCacheFiles reads the binary cache at file, then merges every legacy
// JSON cache that still exists. Reading a legacy file marks the cache
// dirty so the next save persists the merge and removes the old file.
func loadCacheFiles(root, file string, legacy []string) *hashCache {
	c := &hashCache{
		file:    file,
		root:    root,
		legacy:  legacy,
		touched: map[string]bool{},
		Entries: map[string]cacheEntry{},
	}
	if data, err := os.ReadFile(c.file); err == nil {
		if entries, ok := decodeCache(data); ok {
			c.Entries = entries
		}
	}
	for _, lf := range legacy {
		data, err := os.ReadFile(lf)
		if err != nil {
			continue
		}
		c.dirty = true // rewrite in the new format and remove the old file
		var old struct {
			Entries map[string]cacheEntry `json:"entries"`
		}
		if json.Unmarshal(data, &old) == nil {
			for path, e := range old.Entries {
				if _, ok := c.Entries[path]; !ok {
					c.Entries[path] = e
				}
			}
		}
	}
	return c
}

// get returns the cached hash for path if size and mtime still match.
func (c *hashCache) get(path string, size, mtime int64) (string, bool) {
	if c == nil {
		return "", false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.Entries[path]
	if !ok || e.Size != size || e.Mtime != mtime {
		return "", false
	}
	c.touched[path] = true
	return e.Hash, true
}

func (c *hashCache) put(path string, size, mtime int64, hash string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.Entries[path] = cacheEntry{Size: size, Mtime: mtime, Hash: hash}
	c.touched[path] = true
	c.dirty = true
}

// save prunes entries under the scan root whose files no longer exist
// (entries from other trees are left alone) and writes the cache back if
// anything changed. Errors are ignored — worst case the next run
// re-hashes.
func (c *hashCache) save() {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	prefix := c.root + string(filepath.Separator)
	for path := range c.Entries {
		if c.touched[path] || !strings.HasPrefix(path, prefix) {
			continue
		}
		if _, err := os.Lstat(path); os.IsNotExist(err) {
			delete(c.Entries, path)
			c.dirty = true
		}
	}
	if !c.dirty {
		return
	}
	if os.MkdirAll(filepath.Dir(c.file), 0o755) != nil {
		return
	}
	if os.WriteFile(c.file, encodeCache(c.Entries), 0o644) == nil {
		for _, lf := range c.legacy {
			os.Remove(lf)
		}
	}
}

// hashStats counts how duplicate-candidate hashes were obtained.
type hashStats struct {
	hashed int // files read and hashed this run
	cached int // hashes reused from the cache
}

// wipeCache deletes the entire hash cache directory and reports what was
// removed. The next scan simply re-hashes from scratch.
func wipeCache() {
	base, err := os.UserCacheDir()
	if err != nil {
		fmt.Fprintln(os.Stderr, "quickap:", err)
		os.Exit(1)
	}
	dir := filepath.Join(base, "quickap")
	var files int
	var size int64
	filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if info, err := d.Info(); err == nil {
			files++
			size += info.Size()
		}
		return nil
	})
	fmt.Println()
	if files == 0 {
		fmt.Printf("  %s\n\n", dim("hash cache is already empty ("+dir+")"))
		return
	}
	if err := os.RemoveAll(dir); err != nil {
		fmt.Fprintln(os.Stderr, "quickap:", err)
		os.Exit(1)
	}
	fmt.Printf("  %s %s %s\n\n", green("✔"),
		green(fmt.Sprintf("cleared hash cache (%d files, %s)", files, humanSize(size))),
		dim(dir))
}

func relPath(root, path string) string {
	if r, err := filepath.Rel(root, path); err == nil {
		return r
	}
	return path
}

// moveGroups moves every file of every duplicate group into
// dir/<category>/group-NNN/, keeping original filenames (suffixed on
// collision within a group).
func moveGroups(root, dir string, cat category, groups [][]*fileEntry) error {
	fmt.Printf("  %s %s\n", c256(cat.hue, "●"), bold("Moving duplicate "+cat.singular+" groups"))
	if len(groups) == 0 {
		fmt.Printf("  %s\n\n", dim("nothing to move — no duplicate "+cat.key+" found"))
		return nil
	}
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(root, dir)
	}
	catDir := filepath.Join(dir, cat.key)
	var movedOrig, movedDup int
	var movedSize int64
	for i, group := range groups {
		groupName := fmt.Sprintf("group-%03d", i+1)
		groupDir := filepath.Join(catDir, groupName)
		if err := os.MkdirAll(groupDir, 0o755); err != nil {
			return err
		}
		fmt.Printf("\n  %s %s\n", bold(groupName),
			dim(fmt.Sprintf("· %d files → %s", len(group), relPath(root, groupDir))))
		// Collision keys are lowercased so names differing only by case get
		// suffixed too — on case-insensitive filesystems (macOS APFS) they
		// would otherwise silently overwrite each other.
		taken := map[string]bool{}
		for j, e := range group {
			conn := dim("├")
			if j == len(group)-1 {
				conn = dim("╰")
			}
			name := filepath.Base(e.path)
			for n := 2; taken[strings.ToLower(name)]; n++ {
				ext := filepath.Ext(name)
				name = strings.TrimSuffix(filepath.Base(e.path), ext) + fmt.Sprintf("-%d", n) + ext
			}
			taken[strings.ToLower(name)] = true
			dst := filepath.Join(groupDir, name)
			if err := moveFile(e.path, dst); err != nil {
				fmt.Printf("  %s %s %s %s\n", conn, red("!"), relPath(root, e.path), dim(err.Error()))
				continue
			}
			if e.dup {
				movedDup++
			} else {
				movedOrig++
			}
			movedSize += e.size
			fmt.Printf("  %s %s %s %s\n", conn, green("→"), relPath(root, e.path), dim("as "+name))
		}
	}
	fmt.Printf("\n  %s %s %s\n\n", green("✔"),
		green(fmt.Sprintf("moved %d files (%s) into %s", movedOrig+movedDup, humanSize(movedSize), relPath(root, catDir))),
		dim(fmt.Sprintf("· %d originals + %d duplicates", movedOrig, movedDup)))
	return nil
}

// moveFile renames src to dst, falling back to copy+delete across devices.
func moveFile(src, dst string) error {
	if err := os.Rename(src, dst); err == nil {
		return nil
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(dst)
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Remove(src)
}

// organizeFiles copies (or, with move set, moves) every indexed file that
// is not a duplicate copy into dir/<Category>/ (Applications, Documents,
// Music, ...), creating the tree as needed. Every file is verified before
// it counts: the destination is re-read from disk and its SHA-256 must
// match the source's — and in move mode the source is deleted only after
// that verification succeeds, so no content is ever lost to a bad copy.
// A destination file that already holds a source's exact content is reused
// rather than copied again (in move mode the redundant source is deleted),
// so re-running into the same directory converges instead of piling up
// suffixed copies. Per-file failures are logged and skipped; only a failure
// to create a category directory is fatal. A non-empty logPath appends
// every action taken this run to that file.
func organizeFiles(root, dir string, results []catResult, move bool, logPath string) error {
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(root, dir)
	}
	if logPath != "" && !filepath.IsAbs(logPath) {
		logPath = filepath.Join(root, logPath)
	}
	var actions []string
	logAct := func(action, src, detail string) {
		if logPath != "" {
			actions = append(actions, action+"\t"+src+"\t"+detail)
		}
	}
	mode := "copying — sources are left in place"
	if move {
		mode = "moving — sources are deleted after verification"
	}
	fmt.Printf("  %s %s %s\n", green("●"), bold("Organizing files into "+tildePath(dir)), dim("· "+mode))
	var copied, present, skippedDups, failed, undeleted int
	var copiedSize int64
	var failures []organizeFailure
	for _, r := range results {
		// originalOf lets the action log say which kept original a skipped
		// duplicate copy matches.
		originalOf := map[string]string{}
		if logPath != "" {
			for _, g := range r.groups {
				for _, d := range g[1:] {
					originalOf[d.path] = g[0].path
				}
			}
		}
		var files []*fileEntry
		for _, e := range r.entries {
			if e.dup {
				skippedDups++
				logAct("skipped-duplicate", e.path, "duplicate of "+originalOf[e.path])
				continue
			}
			files = append(files, e)
		}
		if len(files) == 0 {
			continue
		}
		catDir := filepath.Join(dir, r.cat.label)
		if err := os.MkdirAll(catDir, 0o755); err != nil {
			return err
		}
		sort.Slice(files, func(i, j int) bool { return files[i].path < files[j].path })
		fmt.Printf("\n  %s %s %s\n", c256(r.cat.hue, "●"), bold(r.cat.label),
			dim(fmt.Sprintf("· %d files → %s", len(files), relPath(root, catDir))))
		hue := func(s string) string { return c256(r.cat.hue, s) }
		taken := map[string]bool{}
		for i, e := range files {
			conn := dim("├")
			switch {
			case len(files) == 1:
				conn = dim("─")
			case i == 0:
				conn = dim("┌")
			case i == len(files)-1:
				conn = dim("╰")
			}
			name, already, err := destName(catDir, e, taken)
			if err == nil && !already {
				err = copyFileVerified(e.path, filepath.Join(catDir, name))
			}
			if err != nil {
				failed++
				failures = append(failures, organizeFailure{path: e.path, reason: err.Error()})
				logAct("failed", e.path, err.Error())
				fmt.Printf("  %s %s %s %s\n", conn, red("!"), stylePath(relPath(root, e.path), red), dim(err.Error()))
				continue
			}
			dst := filepath.Join(catDir, name)
			if already {
				present++
			} else {
				copied++
				copiedSize += e.size
			}
			// The destination is verified at this point; in move mode
			// deleting the source is now safe.
			if move {
				if rmErr := os.Remove(e.path); rmErr != nil {
					undeleted++
					what := "copied and verified"
					if already {
						what = "already present"
					}
					reason := what + ", but the source could not be deleted: " + rmErr.Error()
					failures = append(failures, organizeFailure{path: e.path, reason: reason})
					logAct("source-not-deleted", e.path, reason)
					fmt.Printf("  %s %s %s %s\n", conn, yellow("!"), stylePath(relPath(root, e.path), yellow), dim(reason))
					continue
				}
			}
			if already {
				note, action := "already present", "already-present"
				if move {
					note, action = "already present · source deleted", "already-present-source-deleted"
				}
				logAct(action, e.path, dst)
				fmt.Printf("  %s %s %s %s\n", conn, dim("≡"), dim(relPath(root, e.path)), dim(ital(note)))
				continue
			}
			action := "copied"
			if move {
				action = "moved"
			}
			logAct(action, e.path, dst)
			note := ""
			if name != filepath.Base(e.path) {
				note = " " + dim("as "+name)
			}
			fmt.Printf("  %s %s %s%s\n", conn, green("→"), stylePath(relPath(root, e.path), hue), note)
		}
	}
	verb, verbPast := "copy", "copied"
	if move {
		verb, verbPast = "move", "moved"
	}
	logNote := func() {
		if logPath == "" {
			return
		}
		err := appendOrganizeLog(logPath, move, root, dir, actions,
			fmt.Sprintf("%d %s, %d already present, %d duplicate copies skipped, %d failed, %d sources not deleted",
				copied, verbPast, present, skippedDups, failed, undeleted))
		if err != nil {
			fmt.Fprintln(os.Stderr, "quickap: could not write the action log:", err)
			return
		}
		fmt.Printf("  %s\n", dim("action log: "+tildePath(logPath)))
	}
	if copied+present+failed == 0 {
		fmt.Printf("  %s\n", dim("nothing to organize — no files found"))
		logNote()
		fmt.Println()
		return nil
	}
	var notes []string
	if copied > 0 {
		if move {
			notes = append(notes, "every file verified by SHA-256 before its source was deleted")
		} else {
			notes = append(notes, "every copy verified by SHA-256")
		}
	}
	if skippedDups > 0 {
		notes = append(notes, fmt.Sprintf("%d duplicate copies skipped", skippedDups))
	}
	if present > 0 {
		notes = append(notes, fmt.Sprintf("%d already present", present))
	}
	var note string
	if len(notes) > 0 {
		note = "· " + strings.Join(notes, " · ")
	}
	summary := fmt.Sprintf("organized %d files (%s) into %s", copied, humanSize(copiedSize), tildePath(dir))
	if copied == 0 && failed == 0 && undeleted == 0 {
		summary = "nothing new to " + verb + " — " + tildePath(dir) + " is already up to date"
	}
	fmt.Printf("\n  %s %s %s\n", green("✔"), green(summary), dim(note))
	if failed > 0 {
		fmt.Printf("  %s\n", red(fmt.Sprintf("! %d files could not be %s", failed, verbPast)))
	}
	if undeleted > 0 {
		fmt.Printf("  %s\n", yellow(fmt.Sprintf("! %d sources could not be deleted — their verified copies are in %s", undeleted, tildePath(dir))))
	}
	listPath := filepath.Join(dir, organizeFailuresName)
	if len(failures) > 0 {
		if err := writeOrganizeFailures(listPath, verbPast, failures); err != nil {
			fmt.Fprintln(os.Stderr, "quickap: could not write the failure list:", err)
		} else {
			fmt.Printf("  %s\n", dim("failure list: "+tildePath(listPath)))
		}
	} else {
		// A clean run makes a failure list from a previous run stale and
		// misleading; remove it.
		os.Remove(listPath)
	}
	logNote()
	fmt.Println()
	return nil
}

// appendOrganizeLog appends one organize run to the action log at path: a
// commented header, one tab-separated "action, source path, destination or
// detail" line per action, and a commented summary. Appending (rather than
// overwriting) keeps the history of earlier runs.
func appendOrganizeLog(path string, move bool, root, dir string, actions []string, summary string) error {
	mode := "copy"
	if move {
		mode = "move"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# quickap organize log · %s · mode: %s · %s → %s\n",
		time.Now().Format("2006-01-02 15:04:05"), mode, root, dir)
	b.WriteString("# format: <action> <TAB> <source path> <TAB> <destination or detail>\n")
	for _, a := range actions {
		b.WriteString(a + "\n")
	}
	b.WriteString("# summary: " + summary + "\n")
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.WriteString(b.String()); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// organizeFailuresName is the file organizeFiles writes into the
// destination directory when files could not be copied or moved. A run
// without failures removes it again.
const organizeFailuresName = "quickap-organize-failures.txt"

// organizeFailure records one file organize could not handle and why.
type organizeFailure struct {
	path   string // absolute source path
	reason string
}

// writeOrganizeFailures writes the failure list: a commented header
// followed by one tab-separated "source path, reason" line per file.
func writeOrganizeFailures(path, verbPast string, failures []organizeFailure) error {
	var b strings.Builder
	fmt.Fprintf(&b, "# quickap organize failures · %s\n", time.Now().Format("2006-01-02 15:04:05"))
	fmt.Fprintf(&b, "# %d files could not be %s · format: <source path> <TAB> <reason>\n", len(failures), verbPast)
	for _, f := range failures {
		b.WriteString(f.path + "\t" + f.reason + "\n")
	}
	return os.WriteFile(path, []byte(b.String()), 0o644)
}

// destName picks the destination filename for e inside catDir: the original
// base name, or a numeric-suffixed variant (a.jpg, a-2.jpg) when that name
// is claimed by a file organized earlier this run (tracked in taken, keyed
// case-insensitively so nothing is overwritten on case-insensitive
// filesystems) or by a different file already on disk. A file already on
// disk with e's exact content short-circuits with already=true: the copy
// would be redundant.
func destName(catDir string, e *fileEntry, taken map[string]bool) (name string, already bool, err error) {
	base := filepath.Base(e.path)
	srcHash := e.hash // set during duplicate detection; empty for most files
	name = base
	for n := 2; ; n++ {
		if !taken[strings.ToLower(name)] {
			dst := filepath.Join(catDir, name)
			if _, statErr := os.Lstat(dst); os.IsNotExist(statErr) {
				taken[strings.ToLower(name)] = true
				return name, false, nil
			}
			if srcHash == "" {
				if srcHash, err = hashFile(e.path); err != nil {
					return "", false, err
				}
			}
			if h, hashErr := hashFile(dst); hashErr == nil && h == srcHash {
				taken[strings.ToLower(name)] = true
				return name, true, nil
			}
		}
		ext := filepath.Ext(base)
		name = strings.TrimSuffix(base, ext) + fmt.Sprintf("-%d", n) + ext
	}
}

// copyFileVerified copies src to dst and verifies the result: the
// destination is re-read from disk and its SHA-256 must match the hash of
// the bytes that were read from src. On any error or mismatch the partial
// destination is removed.
func copyFileVerified(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	h := sha256.New()
	if _, err := io.Copy(out, io.TeeReader(in, h)); err != nil {
		out.Close()
		os.Remove(dst)
		return err
	}
	if err := out.Close(); err != nil {
		os.Remove(dst)
		return err
	}
	dstHash, err := hashFile(dst)
	if err != nil {
		os.Remove(dst)
		return err
	}
	if dstHash != hex.EncodeToString(h.Sum(nil)) {
		os.Remove(dst)
		return fmt.Errorf("copy verification failed: %s does not match its source", filepath.Base(dst))
	}
	return nil
}

// deleteGroups removes every duplicate file, keeping each group's original
// (the lexically first path). Failures are logged and skipped.
func deleteGroups(root string, cat category, groups [][]*fileEntry) {
	fmt.Printf("  %s %s\n", c256(cat.hue, "●"), bold("Deleting duplicate "+cat.key))
	if len(groups) == 0 {
		fmt.Printf("  %s\n\n", dim("nothing to delete — no duplicate "+cat.key+" found"))
		return
	}
	var deleted int
	var freed int64
	for _, group := range groups {
		fmt.Println()
		fmt.Printf("  %s %s %s\n", dim("┌"), green("✓ "+relPath(root, group[0].path)), dim(ital("kept")))
		for j, e := range group[1:] {
			conn := dim("├")
			if j == len(group)-2 {
				conn = dim("╰")
			}
			if err := os.Remove(e.path); err != nil {
				fmt.Printf("  %s %s %s\n", conn, red("! "+relPath(root, e.path)), dim(err.Error()))
				continue
			}
			deleted++
			freed += e.size
			fmt.Printf("  %s %s %s\n", conn, red("✗ "+relPath(root, e.path)), dim(ital("deleted")))
		}
	}
	fmt.Printf("\n  %s %s\n\n", green("✔"),
		green(fmt.Sprintf("deleted %d duplicate %s, freed %s", deleted, cat.key, humanSize(freed))))
}

// renderOverview prints the compact all-categories table used by the bare
// command: one row per category plus a total.
func renderOverview(results []catResult, verbose bool) {
	fmt.Println()
	widths := []int{18, 9, 13, 10, 13, 15}
	const aligns = "lrrrrr"
	fmt.Println(tLine("╭", "┬", "╮", widths))
	fmt.Println(tRow(widths, aligns,
		dim("category"), dim("files"), dim("size"), dim("unique"), dim("duplicates"), dim("reclaimable")))
	fmt.Println(tLine("├", "┼", "┤", widths))
	var g totals
	for i, r := range results {
		if spacious && i > 0 {
			fmt.Println(tSpace(widths))
		}
		g.count += r.t.count
		g.size += r.t.size
		g.uniq += r.t.uniq
		g.dup += r.t.dup
		g.dupSize += r.t.dupSize
		if r.t.count == 0 {
			fmt.Println(tRow(widths, aligns,
				dim("○ "+r.cat.label), dim("0"), dim("–"), dim("–"), dim("–"), dim("–")))
			continue
		}
		dupStr, reclaim := dim("–"), dim("–")
		if r.t.dup > 0 {
			dupStr = yellow(fmt.Sprintf("%d", r.t.dup))
			reclaim = yellow(humanSize(r.t.dupSize))
		}
		fmt.Println(tRow(widths, aligns,
			c256(r.cat.hue, "●")+" "+r.cat.label,
			cyan(fmt.Sprintf("%d", r.t.count)), humanSize(r.t.size),
			green(fmt.Sprintf("%d", r.t.uniq)), dupStr, reclaim))
	}
	fmt.Println(tLine("├", "┼", "┤", widths))
	dupStr, reclaim := dim("–"), dim("–")
	if g.dup > 0 {
		dupStr = bold(yellow(fmt.Sprintf("%d", g.dup)))
		reclaim = bold(yellow(humanSize(g.dupSize)))
	}
	fmt.Println(tRow(widths, aligns,
		bold("Total"),
		bold(cyan(fmt.Sprintf("%d", g.count))), bold(humanSize(g.size)),
		bold(green(fmt.Sprintf("%d", g.uniq))), dupStr, reclaim))
	fmt.Println(tLine("╰", "┴", "╯", widths))
	if g.dup > 0 {
		fmt.Println()
		fmt.Println("  " + meter(g.dupSize, g.size))
	}
	if verbose {
		fmt.Println()
		fmt.Printf("  %s\n", dim(ital("run \"quickap <category>\" for per-extension detail — e.g. quickap images")))
	}
}

// meter renders the reclaimable-space gauge: a 20-cell bar of the
// duplicate share of total bytes, with the amounts spelled out.
func meter(dupSize, total int64) string {
	const cells = 24
	filled := 0
	if total > 0 {
		filled = int(float64(dupSize)/float64(total)*cells + 0.5)
	}
	if filled < 1 && dupSize > 0 {
		filled = 1
	}
	if filled > cells {
		filled = cells
	}
	pct := 0.0
	if total > 0 {
		pct = float64(dupSize) / float64(total) * 100
	}
	return dim("reclaimable ") +
		yellow(strings.Repeat("█", filled)) + dim(strings.Repeat("░", cells-filled)) +
		" " + yellow(humanSize(dupSize)) + dim(fmt.Sprintf(" · %.0f%% of %s", pct, humanSize(total)))
}

// commandList returns the quoted category subcommand names for errors.
func commandList() string {
	names := make([]string, len(categories))
	for i, cat := range categories {
		names[i] = fmt.Sprintf("%q", cat.cmd)
	}
	return strings.Join(names, ", ")
}

// renderSection prints one category's summary table, reclaimable meter,
// and per-extension breakdown.
func renderSection(cat category, stats map[string]*extStat, t totals) {
	fmt.Printf("\n  %s %s\n\n", c256(cat.hue, "●"), bold(cat.label))

	if t.count == 0 {
		fmt.Printf("  %s\n", dim("no "+cat.singular+" files found"))
		return
	}

	sw := []int{20, 10, 14}
	dupCount := fmt.Sprintf("%d", t.dup)
	dupSize := humanSize(t.dupSize)
	if t.dup > 0 {
		dupCount = bold(yellow(dupCount))
		dupSize = yellow(dupSize)
	} else {
		dupCount = bold(green(dupCount))
		dupSize = green(dupSize)
	}
	fmt.Println(tLine("╭", "┬", "╮", sw))
	fmt.Println(tRow(sw, "lrr", dim("all "+cat.key),
		bold(cyan(fmt.Sprintf("%d", t.count))), cyan(humanSize(t.size))))
	fmt.Println(tRow(sw, "lrr", dim("unique"),
		bold(green(fmt.Sprintf("%d", t.uniq))), green(humanSize(t.uniqSize))))
	fmt.Println(tRow(sw, "lrr", dim("duplicates"), dupCount, dupSize))
	fmt.Println(tLine("╰", "┴", "╯", sw))
	if t.dup > 0 {
		fmt.Println("  " + meter(t.dupSize, t.size))
	}
	fmt.Println()

	sorted := make([]*extStat, 0, len(stats))
	for _, s := range stats {
		sorted = append(sorted, s)
	}
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].count != sorted[j].count {
			return sorted[i].count > sorted[j].count
		}
		return sorted[i].ext < sorted[j].ext
	})

	ew := []int{12, 7, 9, 12, 29, 12}
	const eAligns = "lrrrlr"
	fmt.Println(tLine("╭", "┬", "╮", ew))
	fmt.Println(tRow(ew, eAligns,
		dim("extension"), dim("all"), dim("unique"), dim("duplicates"), "", dim("size")))
	fmt.Println(tLine("├", "┼", "┤", ew))
	maxCount := sorted[0].count
	const barWidth = 27
	for i, s := range sorted {
		if spacious && i > 0 {
			fmt.Println(tSpace(ew))
		}
		barLen := s.count * barWidth / maxCount
		if barLen < 1 {
			barLen = 1
		}
		uniqLen := 0
		if s.count > 0 {
			uniqLen = s.uniq * barLen / s.count
		}
		if uniqLen < 1 && s.uniq > 0 {
			uniqLen = 1
		}
		bar := cyan(strings.Repeat("█", uniqLen)) +
			yellow(strings.Repeat("█", barLen-uniqLen)) +
			dim(strings.Repeat("░", barWidth-barLen))
		dupCell := dim("–")
		if s.dup > 0 {
			dupCell = yellow(fmt.Sprintf("%d", s.dup))
		}
		ext := s.ext
		if ext == "" {
			ext = "(none)"
		}
		// The other category admits arbitrary extensions; keep the
		// fixed-width table intact for unusually long ones.
		if max := ew[0] - 2; utf8.RuneCountInString(ext) > max {
			ext = string([]rune(ext)[:max-1]) + "…"
		}
		fmt.Println(tRow(ew, eAligns,
			c256(cat.hue, ext),
			fmt.Sprintf("%d", s.count), green(fmt.Sprintf("%d", s.uniq)), dupCell,
			bar, humanSize(s.size)))
	}
	fmt.Println(tLine("╰", "┴", "╯", ew))
	if t.dup > 0 {
		fmt.Printf("  %s%s %s%s\n", cyan("█"), dim(" unique "), yellow("█"), dim(" duplicate"))
	}
}

func renderFooter(skipped int, elapsed time.Duration, hs hashStats, cache *hashCache, noCache bool, verbose bool) {
	// Unreadable entries are surfaced even without -verbose — that's a
	// warning, not detail.
	if !verbose && skipped > 0 {
		fmt.Printf("\n  %s\n\n", dim(fmt.Sprintf("%d entries unreadable", skipped)))
		return
	}
	if !verbose {
		fmt.Println()
		return
	}
	unit := time.Millisecond
	if elapsed < time.Millisecond {
		unit = time.Microsecond
	}
	note := fmt.Sprintf("scanned in %s", elapsed.Round(unit))
	if hs.hashed > 0 || hs.cached > 0 {
		note += fmt.Sprintf(" · %d hashed", hs.hashed)
		if hs.cached > 0 {
			note += fmt.Sprintf(", %d from cache", hs.cached)
		}
	}
	if skipped > 0 {
		note += fmt.Sprintf(" · %d entries unreadable", skipped)
	}
	var cacheNote string
	switch {
	case noCache:
		cacheNote = "hash cache disabled (-no-cache)"
	case cache == nil:
		cacheNote = "hash cache unavailable (no user cache directory)"
	default:
		cacheNote = "hash cache: " + tildePath(cache.file)
	}
	fmt.Printf("\n  %s\n  %s\n\n", dim(note), dim(cacheNote))
}

// tildePath abbreviates a path under the user's home directory to ~/...
// for display.
func tildePath(p string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return p
	}
	if p == home {
		return "~"
	}
	if strings.HasPrefix(p, home+string(filepath.Separator)) {
		return "~" + p[len(home):]
	}
	return p
}

// renderGroups prints each duplicate group with its file paths.
func renderGroups(root string, cat category, groups [][]*fileEntry) {
	fmt.Printf("  %s %s\n", c256(cat.hue, "●"),
		bold(fmt.Sprintf("Duplicate %s groups (%d)", cat.singular, len(groups))))
	if len(groups) == 0 {
		fmt.Printf("  %s\n\n", dim("no duplicate "+cat.key+" found"))
		return
	}
	for i, group := range groups {
		fmt.Printf("\n  %s %s\n", bold(fmt.Sprintf("group %d", i+1)),
			dim(fmt.Sprintf("· %d files · %s each", len(group), humanSize(group[0].size))))
		for j, e := range group {
			conn := dim("├")
			if j == len(group)-1 {
				conn = dim("╰")
			}
			if j == 0 {
				conn = dim("┌")
			}
			marker, note := yellow("✗ ")+stylePath(relPath(root, e.path), yellow), ""
			if j == 0 {
				marker, note = green("✓ ")+stylePath(relPath(root, e.path), green), " "+dim(ital("original"))
			}
			fmt.Printf("  %s %s%s\n", conn, marker, note)
		}
	}
	fmt.Println()
}

// stylePath renders a relative path with its directory part dimmed and
// the file name run through color, so names pop in long listings.
func stylePath(rel string, color func(string) string) string {
	dir, name := filepath.Split(rel)
	if dir == "" {
		return color(name)
	}
	return dim(dir) + color(name)
}

// renderUnique prints every unique file with its path and size. Unique
// matches the tables' unique column: every file that is not a duplicate
// copy, so a duplicate group's kept original counts as unique.
func renderUnique(root string, cat category, entries []*fileEntry) {
	var uniq []*fileEntry
	for _, e := range entries {
		if !e.dup {
			uniq = append(uniq, e)
		}
	}
	sort.Slice(uniq, func(i, j int) bool { return uniq[i].path < uniq[j].path })
	fmt.Printf("  %s %s\n", c256(cat.hue, "●"),
		bold(fmt.Sprintf("Unique %s files (%d)", cat.singular, len(uniq))))
	if len(uniq) == 0 {
		fmt.Printf("  %s\n\n", dim("no unique "+cat.key+" found"))
		return
	}
	hue := func(s string) string { return c256(cat.hue, s) }
	for i, e := range uniq {
		conn := dim("├")
		switch {
		case len(uniq) == 1:
			conn = dim("─")
		case i == 0:
			conn = dim("┌")
		case i == len(uniq)-1:
			conn = dim("╰")
		}
		fmt.Printf("  %s %s %s\n", conn, stylePath(relPath(root, e.path), hue), dim("· "+humanSize(e.size)))
	}
	fmt.Println()
}

// renderLarge prints the largest files, largest first, capped at 50.
func renderLarge(root string, cat category, entries []*fileEntry) {
	const limit = 50
	sorted := make([]*fileEntry, len(entries))
	copy(sorted, entries)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].size != sorted[j].size {
			return sorted[i].size > sorted[j].size
		}
		return sorted[i].path < sorted[j].path
	})
	shown := sorted
	if len(shown) > limit {
		shown = shown[:limit]
	}
	head := fmt.Sprintf("Largest %s files (%d)", cat.singular, len(shown))
	if len(sorted) > limit {
		head = fmt.Sprintf("Largest %s files (top %d of %d)", cat.singular, limit, len(sorted))
	}
	fmt.Printf("  %s %s\n", c256(cat.hue, "●"), bold(head))
	if len(shown) == 0 {
		fmt.Printf("  %s\n\n", dim("no "+cat.key+" found"))
		return
	}
	hue := func(s string) string { return c256(cat.hue, s) }
	for i, e := range shown {
		conn := dim("├")
		switch {
		case len(shown) == 1:
			conn = dim("─")
		case i == 0:
			conn = dim("┌")
		case i == len(shown)-1:
			conn = dim("╰")
		}
		// Size leads the color here — it is what -list-large is about.
		fmt.Printf("  %s %s %s %s\n", conn, stylePath(relPath(root, e.path), hue), dim("·"), yellow(humanSize(e.size)))
	}
	fmt.Println()
}

// cmdHelp holds the help copy for one subcommand.
type cmdHelp struct {
	invoke   string // e.g. "quickap images"
	summary  string
	scope    string // e.g. "image", used in flag descriptions
	moveDest string // where -move puts groups
	actions  bool   // whether -move/-delete are available
}

var cmdHelps = map[string]cmdHelp{
	"": {
		invoke:  "quickap",
		summary: "index all categories, compact overview report",
		scope:   "",
	},
	"images": {
		invoke:   "quickap images",
		summary:  "index image files only",
		scope:    "image",
		moveDest: "DIR/images/",
		actions:  true,
	},
	"docs": {
		invoke:   "quickap docs",
		summary:  "index document files only (alias: documents)",
		scope:    "document",
		moveDest: "DIR/documents/",
		actions:  true,
	},
	"music": {
		invoke:   "quickap music",
		summary:  "index music files only",
		scope:    "music",
		moveDest: "DIR/music/",
		actions:  true,
	},
	"videos": {
		invoke:   "quickap videos",
		summary:  "index video files only (alias: video)",
		scope:    "video",
		moveDest: "DIR/videos/",
		actions:  true,
	},
	"archives": {
		invoke:   "quickap archives",
		summary:  "index archive files only (alias: archive)",
		scope:    "archive",
		moveDest: "DIR/archives/",
		actions:  true,
	},
	"apps": {
		invoke:   "quickap apps",
		summary:  "index application files only (aliases: app, applications)",
		scope:    "application",
		moveDest: "DIR/apps/",
		actions:  true,
	},
	"other": {
		invoke:   "quickap other",
		summary:  "index files no other category claims (alias: others)",
		scope:    "other",
		moveDest: "DIR/other files/",
		actions:  true,
	},
}

// printCommandBlock prints one subcommand's summary and flags.
func printCommandBlock(c cmdHelp) {
	h := func(s string) { fmt.Println("  " + s) }
	scope := c.scope + " "
	if c.scope == "" {
		scope = ""
	}
	h(bold(c.invoke) + dim(" — "+c.summary))
	h("  " + cyan("-list-duplicates") + " list duplicate " + scope + "groups with file paths")
	h("                   (shorthand: " + cyan("-ld") + ")")
	h("  " + cyan("-list-unique") + "     list unique " + scope + "files — every file that is not a")
	h("                   duplicate copy, as counted by the unique column")
	h("                   (shorthand: " + cyan("-lu") + ")")
	h("  " + cyan("-list-large") + "      list the 50 largest " + scope + "files, largest first")
	h("                   (shorthand: " + cyan("-ll") + ")")
	h("  " + cyan("-organize DIR") + "    copy every indexed " + scope + "file that is not a duplicate")
	h("                   copy into DIR/<Category>/, verifying each copy against")
	h("                   its source by SHA-256; sources are left in place, and")
	h("                   files already present in DIR with identical content")
	h("                   are not copied again; excludes -move and -delete")
	h("  " + cyan("-organize-move DIR"))
	h("                   like -organize, but move the files: each one is")
	h("                   hash-verified at the destination before its source is")
	h("                   deleted; skipped duplicate copies are left in place")
	h("                   (both modes list any files that could not be handled")
	h("                   in DIR/" + organizeFailuresName + ")")
	h("  " + cyan("-organize-log FILE"))
	h("                   append every action -organize/-organize-move takes")
	h("                   (copied, moved, already present, skipped duplicates,")
	h("                   failures) to FILE, one tab-separated line per action")
	if c.actions {
		h("  " + cyan("-move DIR") + "        move each duplicate " + c.scope + " group (original +")
		h("                   copies) into " + c.moveDest + "group-NNN/ for manual")
		h("                   sorting; DIR is created if needed, resolved relative")
		h("                   to the scanned directory")
		h("  " + cyan("-delete") + "          " + red("permanently delete") + " duplicate " + c.scope + " files,")
		h("                   keeping each group's original; excludes -move")
	}
	h("  " + cyan("-ignore DIR") + "      skip a directory while scanning; a bare name")
	h("                   (node_modules) skips every dir with that name, a path")
	h("                   (files/cache) skips that path relative to the scanned")
	h("                   directory; repeat or comma-separate for multiple")
	h("  " + cyan("-hidden") + "          include hidden directories (.foo/) in the scan")
	h("  " + cyan("-no-cache") + "        disable the hash cache for this run")
	h("  " + cyan("-verify") + "          re-hash all duplicate candidates, ignoring cached")
	h("                   hashes (the cache is still updated)")
	h("  " + cyan("-clear-cache") + "     delete the hash cache entirely and exit; the next")
	h("                   scan re-hashes from scratch")
	h("  " + cyan("-spacious") + "        add vertical space between table rows for easier")
	h("                   reading (default is compact)")
	h("  " + cyan("-verbose") + "         show scan details: timing, hash-cache stats and")
	h("                   location, and hints (shorthand: " + cyan("-vv") + ")")
	h("  " + cyan("-version") + "         print version and exit")
	h("  " + cyan("-help") + "            show help for this command")
}

// printCommandHelp prints help for a single subcommand.
func printCommandHelp(name string) {
	if cat := categoryBySub(name); cat != nil {
		name = cat.cmd
	}
	c, ok := cmdHelps[name]
	if !ok {
		fmt.Fprintf(os.Stderr, "quickap: unknown command %q (expected %s)\nrun \"quickap help\" for usage\n", name, commandList())
		os.Exit(1)
	}
	fmt.Println()
	printCommandBlock(c)
	fmt.Println()
	fmt.Println("  " + dim("run \"quickap help\" for all commands and notes"))
	fmt.Println()
}

func printHelp() {
	h := func(s string) { fmt.Println("  " + s) }
	fmt.Println()
	h(bold(magenta("◆ quickap "+version)) + dim(" · file indexer with duplicate detection"))
	fmt.Println()
	h(bold(magenta("USAGE")))
	h("  quickap [command] [flags] [directory]")
	fmt.Println()
	h("  Indexes application, archive, document, image, video, and music")
	h("  files under a directory (recursive), plus an \"other\" category")
	h("  for every file outside those — your home directory by default,")
	h("  or the one given as the last argument. The report header always")
	h("  shows the running user's home directory. The bare command prints")
	h("  a compact overview of every category; a category command prints")
	h("  detailed per-extension stats and enables -move/-delete.")
	fmt.Println()
	h(bold(magenta("COMMANDS")))
	h("  " + cyan("(none)") + "       index all categories, compact overview")
	h("  " + cyan("apps") + "         index applications only (aliases: app, applications)")
	h("  " + cyan("archives") + "     index archives only (alias: archive)")
	h("  " + cyan("docs") + "         index documents only (alias: documents)")
	h("  " + cyan("images") + "       index images only")
	h("  " + cyan("videos") + "       index videos only (alias: video)")
	h("  " + cyan("music") + "        index music only")
	h("  " + cyan("other") + "        index files no other category claims (alias: others)")
	h("  " + cyan("help [cmd]") + "   show this help, or help for one command")
	h("  " + cyan("version") + "      print version")
	fmt.Println()
	h(bold(magenta("FLAGS")))
	h("  Every command accepts these:")
	fmt.Println()
	h("  " + cyan("-list-duplicates") + " list duplicate groups with file paths")
	h("                   (shorthand: " + cyan("-ld") + ")")
	h("  " + cyan("-list-unique") + "     list unique files — every file that is not a duplicate")
	h("                   copy, as counted by the unique column (shorthand: " + cyan("-lu") + ")")
	h("  " + cyan("-list-large") + "      list the 50 largest files, largest first")
	h("                   (shorthand: " + cyan("-ll") + ")")
	h("  " + cyan("-organize DIR") + "    copy every indexed file that is not a duplicate copy")
	h("                   into DIR/<Category>/ (Applications, Documents, Music,")
	h("                   ...), verifying each copy against its source by")
	h("                   SHA-256; sources are left in place, and files already")
	h("                   present in DIR with identical content are not copied")
	h("                   again; excludes -move and -delete")
	h("  " + cyan("-organize-move DIR"))
	h("                   like -organize, but move the files: each one is")
	h("                   hash-verified at the destination before its source is")
	h("                   deleted; skipped duplicate copies are left in place")
	h("                   (both modes list any files that could not be handled")
	h("                   in DIR/" + organizeFailuresName + ")")
	h("  " + cyan("-organize-log FILE"))
	h("                   append every action -organize/-organize-move takes")
	h("                   (copied, moved, already present, skipped duplicates,")
	h("                   failures) to FILE, one tab-separated line per action")
	h("  " + cyan("-ignore DIR") + "      skip a directory while scanning; a bare name")
	h("                   (node_modules) skips every dir with that name, a path")
	h("                   (files/cache) skips that path relative to the scanned")
	h("                   directory; repeat or comma-separate for multiple")
	h("  " + cyan("-hidden") + "          include hidden directories (.foo/) in the scan")
	h("  " + cyan("-no-cache") + "        disable the hash cache for this run")
	h("  " + cyan("-verify") + "          re-hash all duplicate candidates, ignoring cached")
	h("                   hashes (the cache is still updated)")
	h("  " + cyan("-clear-cache") + "     delete the hash cache entirely and exit; the next")
	h("                   scan re-hashes from scratch")
	h("  " + cyan("-spacious") + "        add vertical space between table rows for easier")
	h("                   reading (default is compact)")
	h("  " + cyan("-verbose") + "         show scan details: timing, hash-cache stats and")
	h("                   location, and hints (shorthand: " + cyan("-vv") + ")")
	h("  " + cyan("-version") + "         print version and exit")
	h("  " + cyan("-help") + "            show help for the current command")
	fmt.Println()
	h("  The cleanup flags work on one category at a time, so they need a")
	h("  category command — e.g. " + cyan("quickap images -delete") + ":")
	fmt.Println()
	h("  " + cyan("-move DIR") + "        move each duplicate group (original + copies) into")
	h("                   DIR/<category>/group-NNN/ for manual sorting; DIR is")
	h("                   created if needed, resolved relative to the scanned")
	h("                   directory")
	h("  " + cyan("-delete") + "          " + red("permanently delete") + " duplicate files, keeping each")
	h("                   group's original; excludes -move")
	fmt.Println()
	h(bold(magenta("NOTES")))
	h("  · duplicates are byte-identical files (SHA-256), regardless of name")
	h("    or extension; the lexically first path counts as the original")
	for _, cat := range categories {
		if cat.exts == nil {
			h(fmt.Sprintf("  · %-13s every file the categories above don't claim,", cat.key+":"))
			h("                    including files with no extension")
			continue
		}
		h(fmt.Sprintf("  · %-13s %s", cat.key+":", strings.Join(sortedExts(cat), " ")))
	}
	h("  · -move and -delete act on one category at a time, so they require")
	h("    a category command: quickap images -delete, quickap docs -move DIR")
	h("  · hidden directories (.git, ...) are skipped unless -hidden is set")
	h("  · symbolic links are ignored (never followed, indexed, or hashed);")
	h("    a symlinked scan directory is resolved to its target first")
	h("  · computed hashes are cached (per scan directory, keyed by file size")
	h("    and mtime) so unchanged files are never re-read; repeat runs only")
	h("    hash new or modified files — use -verify to force a full re-hash")
	h("  · " + yellow("-delete is permanent") + " — there is no undo; run -list-duplicates")
	h("    first to review, or use -move to sort manually instead")
	h("  · -move modifies your files — the summary report always shows the")
	h("    state before moving; colliding filenames within a group get a")
	h("    numeric suffix (a.jpg, a-2.jpg)")
	h("  · -organize copies — sources are untouched; every copy is verified by")
	h("    re-reading the destination and comparing SHA-256 hashes, duplicate")
	h("    copies are skipped so each unique content lands exactly once, and")
	h("    colliding filenames get a numeric suffix (a.jpg, a-2.jpg)")
	h("  · -organize-move gives the same result but deletes each source once")
	h("    its file is hash-verified at the destination — a file is never")
	h("    deleted before its content is confirmed safe; duplicate copies are")
	h("    skipped and left in place (use -delete on a category to remove them)")
	h("  · files -organize/-organize-move could not copy or move are written,")
	h("    with reasons, to " + organizeFailuresName + " in the target")
	h("    directory; a later run without failures removes that list again")
	h("  · -organize-log FILE appends a full audit trail of an organize run:")
	h("    a header (time, mode, directories), one tab-separated line per")
	h("    action with source and destination or reason, and a summary; runs")
	h("    accumulate in the file, and a relative FILE resolves against the")
	h("    scanned directory")
	h("  · a -move, -organize, or -organize-move target inside the current")
	h("    directory will be re-indexed on the next run; use a target outside")
	h("    it (e.g. ../sorted) to avoid that")
	h("  · colors turn off automatically when piped, or set NO_COLOR=1")
	fmt.Println()
}

func sortedExts(cat category) []string {
	exts := make([]string, 0, len(cat.exts))
	for e := range cat.exts {
		exts = append(exts, e)
	}
	sort.Strings(exts)
	return exts
}

// pad right-aligns s to width, measuring width without ANSI codes.
func pad(s string, width int) string {
	n := width - utf8.RuneCountInString(stripANSI(s))
	if n < 0 {
		n = 0
	}
	return strings.Repeat(" ", n) + s
}

// padRight left-aligns s to width, measuring width without ANSI codes.
func padRight(s string, width int) string {
	n := width - utf8.RuneCountInString(stripANSI(s))
	if n < 0 {
		n = 0
	}
	return s + strings.Repeat(" ", n)
}

// stripANSI returns s without ANSI escape sequences, for width math.
func stripANSI(s string) string {
	var b strings.Builder
	inEsc := false
	for _, r := range s {
		switch {
		case inEsc:
			if r == 'm' {
				inEsc = false
			}
		case r == '\x1b':
			inEsc = true
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}
