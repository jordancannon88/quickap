// quickap indexes image, document, music, video, archive, and
// application files under the current directory — plus an "other"
// category for every file those don't claim — and prints a per-category
// summary: total count, count and size per extension, total size, and
// duplicate statistics based on file content (SHA-256).
package main

import (
	"crypto/sha256"
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
		cmd: "images", key: "images", label: "Images", singular: "image", hue: 80,
		exts: map[string]bool{
			".jpg": true, ".jpeg": true, ".png": true, ".gif": true,
			".webp": true, ".bmp": true, ".svg": true, ".tiff": true,
			".tif": true, ".heic": true, ".heif": true, ".avif": true,
			".ico": true,
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
		cmd: "music", key: "music", label: "Music", singular: "music", hue: 176,
		exts: map[string]bool{
			".mp3": true, ".flac": true, ".wav": true, ".aac": true,
			".ogg": true, ".m4a": true, ".wma": true, ".opus": true,
			".aiff": true, ".aif": true, ".mid": true, ".midi": true,
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
		cmd: "archives", key: "archives", label: "Archives", singular: "archive", hue: 179,
		exts: map[string]bool{
			".zip": true, ".7z": true, ".7zip": true, ".rar": true,
			".tar": true, ".gz": true, ".bz2": true, ".xz": true,
			".zst": true, ".tgz": true, ".tbz": true, ".iso": true,
		},
	},
	{
		cmd: "apps", key: "apps", label: "Applications", singular: "application", hue: 114,
		exts: map[string]bool{
			".exe": true, ".msi": true, ".dmg": true, ".pkg": true,
			".deb": true, ".rpm": true, ".appimage": true, ".apk": true,
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
	cat    category
	groups [][]*fileEntry
	stats  map[string]*extStat
	t      totals
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
	listDups := fs.Bool("list", false, "list duplicate groups with file paths")
	hidden := fs.Bool("hidden", false, "include hidden directories in the scan")
	showVersion := fs.Bool("version", false, "print version and exit")
	var ignores multiFlag
	fs.Var(&ignores, "ignore", "skip `DIR` while scanning; repeat or comma-separate for multiple")
	noCache := fs.Bool("no-cache", false, "disable the hash cache for this run")
	verify := fs.Bool("verify", false, "re-hash all duplicate candidates, ignoring cached hashes")
	clearCache := fs.Bool("clear-cache", false, "delete the hash cache and exit")
	var verbose bool
	fs.BoolVar(&verbose, "verbose", false, "show scan details (timing, hash-cache stats, hints)")
	fs.BoolVar(&verbose, "vv", false, "shorthand for -verbose")
	fs.BoolVar(&spacious, "spacious", false, "add vertical space between table rows (default: compact)")
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
		root, err = os.Getwd()
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
		results = append(results, catResult{cat, groups, stats, t})
	}
	cache.save()
	elapsed := time.Since(start)

	fmt.Println()
	fmt.Printf("  %s %s\n", bold(magenta("◆ quickap")), dim("· file index"))
	fmt.Printf("  %s\n", dim(root))
	if len(results) == 1 {
		renderSection(results[0].cat, results[0].stats, results[0].t)
	} else {
		renderOverview(results, verbose)
	}
	renderFooter(skipped, elapsed, hs, verbose)

	if *listDups {
		for _, r := range results {
			renderGroups(root, r.cat, r.groups)
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
func scan(root string, active []category, includeHidden bool, ignores []string) (map[string][]*fileEntry, int) {
	byCategory := map[string][]*fileEntry{}
	var skipped int
	filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			skipped++
			return nil
		}
		if d.IsDir() {
			if path == root {
				return nil
			}
			if !includeHidden && strings.HasPrefix(d.Name(), ".") {
				return filepath.SkipDir
			}
			if ignoredDir(root, path, ignores) {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Type()&fs.ModeType != 0 {
			// Symlinks would otherwise be indexed with the link's own
			// lstat size while hashing follows to the target; skip all
			// non-regular files instead.
			return nil
		}
		ext := strings.ToLower(filepath.Ext(d.Name()))
		for _, cat := range active {
			if !cat.matches(ext) {
				continue
			}
			info, err := d.Info()
			if err != nil {
				skipped++
				return nil
			}
			byCategory[cat.key] = append(byCategory[cat.key], &fileEntry{
				path: path, ext: ext, size: info.Size(), mtime: info.ModTime().UnixNano(),
			})
			return nil
		}
		return nil
	})
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
// also warms the cache for later runs against its subdirectories. A nil
// *hashCache is a valid no-op cache. get/put are safe for concurrent use
// (lookups on the scan goroutine race with puts from hash workers).
type hashCache struct {
	mu      sync.Mutex
	file    string
	root    string // scan root; pruning is scoped to entries under it
	legacy  string // pre-shared-layout per-root cache, merged then removed
	dirty   bool
	touched map[string]bool
	Entries map[string]cacheEntry `json:"entries"`
}

type cacheEntry struct {
	Size  int64  `json:"size"`
	Mtime int64  `json:"mtime_ns"`
	Hash  string `json:"sha256"`
}

// loadCache reads the shared cache, returning an empty cache on any error
// (missing file, corrupt JSON) — the cache is an optimization only. A
// legacy per-root cache file (older layout) is merged in and cleaned up
// on save.
func loadCache(root string) *hashCache {
	dir, err := os.UserCacheDir()
	if err != nil {
		return nil
	}
	c := &hashCache{
		file:    filepath.Join(dir, "quickap", "hashes.json"),
		root:    root,
		touched: map[string]bool{},
		Entries: map[string]cacheEntry{},
	}
	if data, err := os.ReadFile(c.file); err == nil {
		if json.Unmarshal(data, c) != nil || c.Entries == nil {
			c.Entries = map[string]cacheEntry{}
		}
	}
	sum := sha256.Sum256([]byte(root))
	c.legacy = filepath.Join(dir, "quickap", hex.EncodeToString(sum[:8])+".json")
	if data, err := os.ReadFile(c.legacy); err == nil {
		var old hashCache
		if json.Unmarshal(data, &old) == nil {
			for path, e := range old.Entries {
				if _, ok := c.Entries[path]; !ok {
					c.Entries[path] = e
					c.dirty = true
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
	data, err := json.Marshal(c)
	if err != nil {
		return
	}
	if os.MkdirAll(filepath.Dir(c.file), 0o755) != nil {
		return
	}
	if os.WriteFile(c.file, data, 0o644) == nil && c.legacy != "" {
		os.Remove(c.legacy)
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

func renderFooter(skipped int, elapsed time.Duration, hs hashStats, verbose bool) {
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
	fmt.Printf("\n  %s\n\n", dim(note))
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
			marker, note := yellow("✗ "+relPath(root, e.path)), ""
			if j == 0 {
				marker, note = green("✓ "+relPath(root, e.path)), " "+dim(ital("original"))
			}
			fmt.Printf("  %s %s%s\n", conn, marker, note)
		}
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
	h("  " + cyan("-list") + "        list duplicate " + scope + "groups with file paths")
	if c.actions {
		h("  " + cyan("-move DIR") + "    move each duplicate " + c.scope + " group (original +")
		h("               copies) into " + c.moveDest + "group-NNN/ for manual")
		h("               sorting; DIR is created if needed, resolved relative")
		h("               to the current directory")
		h("  " + cyan("-delete") + "      " + red("permanently delete") + " duplicate " + c.scope + " files,")
		h("               keeping each group's original; excludes -move")
	}
	h("  " + cyan("-ignore DIR") + "  skip a directory while scanning; a bare name")
	h("               (node_modules) skips every dir with that name, a path")
	h("               (files/cache) skips that path relative to the current")
	h("               directory; repeat or comma-separate for multiple")
	h("  " + cyan("-hidden") + "      include hidden directories (.foo/) in the scan")
	h("  " + cyan("-no-cache") + "    disable the hash cache for this run")
	h("  " + cyan("-verify") + "      re-hash all duplicate candidates, ignoring cached")
	h("               hashes (the cache is still updated)")
	h("  " + cyan("-clear-cache") + " delete the hash cache entirely and exit; the next")
	h("               scan re-hashes from scratch")
	h("  " + cyan("-spacious") + "    add vertical space between table rows for easier")
	h("               reading (default is compact)")
	h("  " + cyan("-verbose") + "     show scan details: timing, hash-cache stats, and")
	h("               hints (shorthand: " + cyan("-vv") + ")")
	h("  " + cyan("-version") + "     print version and exit")
	h("  " + cyan("-help") + "        show help for this command")
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
	h("  Indexes image, document, music, video, archive, and application")
	h("  files under a directory (recursive), plus an \"other\" category")
	h("  for every file outside those — the current directory by default,")
	h("  or the one given as the last argument. The bare command prints a")
	h("  compact overview of every category; a category command prints")
	h("  detailed per-extension stats and enables -move/-delete.")
	fmt.Println()
	h(bold(magenta("COMMANDS")))
	h("  " + cyan("(none)") + "       index all categories, compact overview")
	h("  " + cyan("images") + "       index images only")
	h("  " + cyan("docs") + "         index documents only (alias: documents)")
	h("  " + cyan("music") + "        index music only")
	h("  " + cyan("videos") + "       index videos only (alias: video)")
	h("  " + cyan("archives") + "     index archives only (alias: archive)")
	h("  " + cyan("apps") + "         index applications only (aliases: app, applications)")
	h("  " + cyan("other") + "        index files no other category claims (alias: others)")
	h("  " + cyan("help [cmd]") + "   show this help, or help for one command")
	h("  " + cyan("version") + "      print version")
	fmt.Println()
	h(bold(magenta("FLAGS")))
	h("  Every command accepts these:")
	fmt.Println()
	h("  " + cyan("-list") + "        list duplicate groups with file paths")
	h("  " + cyan("-ignore DIR") + "  skip a directory while scanning; a bare name")
	h("               (node_modules) skips every dir with that name, a path")
	h("               (files/cache) skips that path relative to the current")
	h("               directory; repeat or comma-separate for multiple")
	h("  " + cyan("-hidden") + "      include hidden directories (.foo/) in the scan")
	h("  " + cyan("-no-cache") + "    disable the hash cache for this run")
	h("  " + cyan("-verify") + "      re-hash all duplicate candidates, ignoring cached")
	h("               hashes (the cache is still updated)")
	h("  " + cyan("-clear-cache") + " delete the hash cache entirely and exit; the next")
	h("               scan re-hashes from scratch")
	h("  " + cyan("-spacious") + "    add vertical space between table rows for easier")
	h("               reading (default is compact)")
	h("  " + cyan("-verbose") + "     show scan details: timing, hash-cache stats, and")
	h("               hints (shorthand: " + cyan("-vv") + ")")
	h("  " + cyan("-version") + "     print version and exit")
	h("  " + cyan("-help") + "        show help for the current command")
	fmt.Println()
	h("  The cleanup flags work on one category at a time, so they need a")
	h("  category command — e.g. " + cyan("quickap images -delete") + ":")
	fmt.Println()
	h("  " + cyan("-move DIR") + "    move each duplicate group (original + copies) into")
	h("               DIR/<category>/group-NNN/ for manual sorting; DIR is")
	h("               created if needed, resolved relative to the scanned")
	h("               directory")
	h("  " + cyan("-delete") + "      " + red("permanently delete") + " duplicate files, keeping each")
	h("               group's original; excludes -move")
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
	h("  · " + yellow("-delete is permanent") + " — there is no undo; run -list first to")
	h("    review, or use -move to sort manually instead")
	h("  · -move modifies your files — the summary report always shows the")
	h("    state before moving; colliding filenames within a group get a")
	h("    numeric suffix (a.jpg, a-2.jpg)")
	h("  · a -move target inside the current directory will be re-indexed on")
	h("    the next run; use a target outside it (e.g. ../dupes) to avoid that")
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
