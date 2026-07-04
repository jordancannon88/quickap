// quickap indexes all image and document files under the current directory
// and prints a per-category summary: total count, count and size per
// extension, total size, and duplicate statistics based on file content
// (SHA-256).
package main

import (
	"crypto/sha256"
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

const version = "1.0.0"

type category struct {
	cmd      string // subcommand name, e.g. "docs"
	key      string // report/dir name, e.g. "documents"
	label    string // section heading, e.g. "Documents"
	singular string // used in labels, e.g. "document"
	exts     map[string]bool
}

var categories = []category{
	{
		cmd: "images", key: "images", label: "Images", singular: "image",
		exts: map[string]bool{
			".jpg": true, ".jpeg": true, ".png": true, ".gif": true,
			".webp": true, ".bmp": true, ".svg": true, ".tiff": true,
			".tif": true, ".heic": true, ".heif": true, ".avif": true,
			".ico": true,
		},
	},
	{
		cmd: "docs", key: "documents", label: "Documents", singular: "document",
		exts: map[string]bool{
			".pdf": true, ".doc": true, ".docx": true, ".xls": true,
			".xlsx": true, ".ppt": true, ".pptx": true, ".odt": true,
			".ods": true, ".odp": true, ".txt": true, ".md": true,
			".rtf": true, ".csv": true, ".epub": true,
		},
	},
	{
		cmd: "music", key: "music", label: "Music", singular: "music",
		exts: map[string]bool{
			".mp3": true, ".flac": true, ".wav": true, ".aac": true,
			".ogg": true, ".m4a": true, ".wma": true, ".opus": true,
			".aiff": true, ".aif": true, ".mid": true, ".midi": true,
		},
	},
	{
		cmd: "videos", key: "videos", label: "Videos", singular: "video",
		exts: map[string]bool{
			".mp4": true, ".mkv": true, ".avi": true, ".mov": true,
			".wmv": true, ".flv": true, ".webm": true, ".m4v": true,
			".mpg": true, ".mpeg": true, ".3gp": true, ".ogv": true,
		},
	},
	{
		cmd: "archives", key: "archives", label: "Archives", singular: "archive",
		exts: map[string]bool{
			".zip": true, ".7z": true, ".7zip": true, ".rar": true,
			".tar": true, ".gz": true, ".bz2": true, ".xz": true,
			".zst": true, ".tgz": true, ".tbz": true, ".iso": true,
		},
	},
	{
		cmd: "apps", key: "apps", label: "Applications", singular: "application",
		exts: map[string]bool{
			".exe": true, ".msi": true, ".dmg": true, ".pkg": true,
			".deb": true, ".rpm": true, ".appimage": true, ".apk": true,
		},
	},
}

// categoryBySub resolves a subcommand name (or alias) to its category.
func categoryBySub(sub string) *category {
	aliases := map[string]string{
		"documents": "docs", "video": "videos", "archive": "archives",
		"app": "apps", "applications": "apps",
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
	path string
	ext  string
	size int64
	hash string // set only for files that share a size with another file
	dup  bool
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

func bold(s string) string    { return style("1", s) }
func dim(s string) string     { return style("2", s) }
func cyan(s string) string    { return style("36", s) }
func green(s string) string   { return style("32", s) }
func magenta(s string) string { return style("35", s) }
func yellow(s string) string  { return style("33", s) }
func red(s string) string     { return style("31", s) }

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
		if cat == nil {
			fmt.Fprintf(os.Stderr, "quickap: unknown command %q (expected %s, \"help\", or \"version\")\nrun \"quickap help\" for usage\n", sub, commandList())
			os.Exit(1)
		}
		sub = cat.cmd
		active = []category{*cat}
	}

	fs := flag.NewFlagSet("quickap "+sub, flag.ExitOnError)
	listDups := fs.Bool("list", false, "list duplicate groups with file paths")
	hidden := fs.Bool("hidden", false, "include hidden directories in the scan")
	showVersion := fs.Bool("version", false, "print version and exit")
	var ignores multiFlag
	fs.Var(&ignores, "ignore", "skip `DIR` while scanning; repeat or comma-separate for multiple")
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
	if *deleteDups && *moveDir != "" {
		fmt.Fprintln(os.Stderr, "quickap: -delete and -move are mutually exclusive")
		os.Exit(1)
	}

	root, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(os.Stderr, "quickap:", err)
		os.Exit(1)
	}

	start := time.Now()
	byCategory, skipped := scan(root, active, *hidden, ignores)

	var results []catResult
	for _, cat := range active {
		entries := byCategory[cat.key]
		groups, unreadable := findDuplicates(entries)
		skipped += unreadable

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
	elapsed := time.Since(start)

	fmt.Println()
	fmt.Printf("  %s %s\n", bold(magenta("◆ quickap")), dim("· file index"))
	fmt.Printf("  %s\n", dim(root))
	if len(results) == 1 {
		renderSection(results[0].cat, results[0].stats, results[0].t)
	} else {
		renderOverview(results)
	}
	renderFooter(skipped, elapsed)

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
		ext := strings.ToLower(filepath.Ext(d.Name()))
		for _, cat := range active {
			if !cat.exts[ext] {
				continue
			}
			info, err := d.Info()
			if err != nil {
				skipped++
				return nil
			}
			byCategory[cat.key] = append(byCategory[cat.key], &fileEntry{path: path, ext: ext, size: info.Size()})
			return nil
		}
		return nil
	})
	return byCategory, skipped
}

// findDuplicates hashes files that share a byte size, flags every file whose
// content was already seen at a lexically earlier path, and returns the
// identical-content groups (each sorted by path; the first file is treated as
// the original). Files that cannot be read count as unique.
func findDuplicates(entries []*fileEntry) ([][]*fileEntry, int) {
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
	if len(candidates) == 0 {
		return nil, 0
	}

	var unreadable int
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, runtime.NumCPU())
	for _, e := range candidates {
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
	return groups, unreadable
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
	return string(h.Sum(nil)), nil
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
	fmt.Printf("  %s\n", bold("Moving duplicate "+cat.singular+" groups"))
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
		groupDir := filepath.Join(catDir, fmt.Sprintf("group-%03d", i+1))
		if err := os.MkdirAll(groupDir, 0o755); err != nil {
			return err
		}
		// Collision keys are lowercased so names differing only by case get
		// suffixed too — on case-insensitive filesystems (macOS APFS) they
		// would otherwise silently overwrite each other.
		taken := map[string]bool{}
		for _, e := range group {
			name := filepath.Base(e.path)
			for n := 2; taken[strings.ToLower(name)]; n++ {
				ext := filepath.Ext(name)
				name = strings.TrimSuffix(filepath.Base(e.path), ext) + fmt.Sprintf("-%d", n) + ext
			}
			taken[strings.ToLower(name)] = true
			dst := filepath.Join(groupDir, name)
			if err := moveFile(e.path, dst); err != nil {
				fmt.Printf("    %s %s %s\n", red("!"), relPath(root, e.path), dim(err.Error()))
				continue
			}
			if e.dup {
				movedDup++
			} else {
				movedOrig++
			}
			movedSize += e.size
			fmt.Printf("    %s %s %s %s\n", green("→"), relPath(root, e.path), dim("⇒"), relPath(root, dst))
		}
	}
	fmt.Printf("\n  %s %s\n\n",
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
	fmt.Printf("  %s\n", bold("Deleting duplicate "+cat.key))
	if len(groups) == 0 {
		fmt.Printf("  %s\n\n", dim("nothing to delete — no duplicate "+cat.key+" found"))
		return
	}
	var deleted int
	var freed int64
	for _, group := range groups {
		fmt.Printf("    %s %s %s\n", green("✓"), relPath(root, group[0].path), dim("(kept)"))
		for _, e := range group[1:] {
			if err := os.Remove(e.path); err != nil {
				fmt.Printf("    %s %s %s\n", red("!"), relPath(root, e.path), dim(err.Error()))
				continue
			}
			deleted++
			freed += e.size
			fmt.Printf("    %s %s\n", red("✗"), relPath(root, e.path))
		}
	}
	fmt.Printf("\n  %s\n\n", green(fmt.Sprintf("deleted %d duplicate %s, freed %s", deleted, cat.key, humanSize(freed))))
}

// renderOverview prints the compact all-categories table used by the bare
// command: one row per category plus a total.
func renderOverview(results []catResult) {
	fmt.Println()
	row := func(label, files, size, uniq, dup, reclaim string) {
		fmt.Printf("  %s%s%s%s%s%s\n",
			padRight(label, 14), pad(files, 6), pad(size, 11), pad(uniq, 7), pad(dup, 6), pad(reclaim, 14))
	}
	row(dim("category"), dim("files"), dim("size"), dim("uniq"), dim("dup"), dim("reclaimable"))
	var g totals
	for _, r := range results {
		g.count += r.t.count
		g.size += r.t.size
		g.uniq += r.t.uniq
		g.dup += r.t.dup
		g.dupSize += r.t.dupSize
		if r.t.count == 0 {
			row(dim(r.cat.label), dim("0"), dim("–"), dim("–"), dim("–"), dim("–"))
			continue
		}
		dupStr, reclaim := dim("–"), dim("–")
		if r.t.dup > 0 {
			dupStr = yellow(fmt.Sprintf("%d", r.t.dup))
			reclaim = yellow(humanSize(r.t.dupSize))
		}
		row(r.cat.label,
			cyan(fmt.Sprintf("%d", r.t.count)), humanSize(r.t.size),
			green(fmt.Sprintf("%d", r.t.uniq)), dupStr, reclaim)
	}
	fmt.Printf("  %s\n", dim(strings.Repeat("─", 58)))
	dupStr, reclaim := dim("–"), dim("–")
	if g.dup > 0 {
		dupStr = bold(yellow(fmt.Sprintf("%d", g.dup)))
		reclaim = bold(yellow(humanSize(g.dupSize)))
	}
	row(bold("Total"),
		bold(cyan(fmt.Sprintf("%d", g.count))), bold(humanSize(g.size)),
		bold(green(fmt.Sprintf("%d", g.uniq))), dupStr, reclaim)
	fmt.Println()
	fmt.Printf("  %s\n", dim("run \"quickap <category>\" for per-extension detail — e.g. quickap images"))
}

// commandList returns the quoted category subcommand names for errors.
func commandList() string {
	names := make([]string, len(categories))
	for i, cat := range categories {
		names[i] = fmt.Sprintf("%q", cat.cmd)
	}
	return strings.Join(names, ", ")
}

// renderSection prints one category's summary box and extension table.
func renderSection(cat category, stats map[string]*extStat, t totals) {
	fmt.Printf("\n  %s\n\n", bold(cyan(cat.label)))

	if t.count == 0 {
		fmt.Printf("  %s\n", dim("no "+cat.singular+" files found"))
		return
	}

	const inner = 44
	boxRow := func(label, count, size string) string {
		lbl := label + strings.Repeat(" ", 18-utf8.RuneCountInString(label))
		return "  │  " + dim(lbl) + pad(count, 8) + pad(size, 14) + "  │"
	}
	dupCount := fmt.Sprintf("%d", t.dup)
	dupSize := humanSize(t.dupSize)
	if t.dup > 0 {
		dupCount = bold(yellow(dupCount))
		dupSize = yellow(dupSize)
	} else {
		dupCount = bold(green(dupCount))
		dupSize = green(dupSize)
	}
	line := strings.Repeat("─", inner)
	fmt.Println("  ┌" + line + "┐")
	fmt.Println(boxRow("All "+cat.key, bold(cyan(fmt.Sprintf("%d", t.count))), cyan(humanSize(t.size))))
	fmt.Println(boxRow("Unique "+cat.key, bold(green(fmt.Sprintf("%d", t.uniq))), green(humanSize(t.uniqSize))))
	fmt.Println(boxRow("Duplicates", dupCount, dupSize))
	fmt.Println("  └" + line + "┘")
	if t.dup > 0 {
		fmt.Printf("  %s\n", dim(fmt.Sprintf("%s reclaimable by removing duplicates", humanSize(t.dupSize))))
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

	fmt.Printf("  %s\n", bold("By extension"))
	fmt.Printf("  %-7s %s %s %s  %-24s %s\n",
		"", pad(dim("all"), 5), pad(dim("uniq"), 6), pad(dim("dup"), 5), "", pad(dim("size"), 9))
	maxCount := sorted[0].count
	const barWidth = 24
	for _, s := range sorted {
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
			dim(strings.Repeat("·", barWidth-barLen))
		dupCell := dim("–")
		if s.dup > 0 {
			dupCell = yellow(fmt.Sprintf("%d", s.dup))
		}
		fmt.Printf("  %-7s %5d %6d %s  %s %9s\n",
			s.ext, s.count, s.uniq, pad(dupCell, 5), bar, humanSize(s.size))
	}
	if t.dup > 0 {
		fmt.Printf("  %s %s%s %s%s\n", dim("bar:"), cyan("█"), dim(" unique "), yellow("█"), dim(" duplicate"))
	}
}

func renderFooter(skipped int, elapsed time.Duration) {
	unit := time.Millisecond
	if elapsed < time.Millisecond {
		unit = time.Microsecond
	}
	note := fmt.Sprintf("scanned in %s", elapsed.Round(unit))
	if skipped > 0 {
		note += fmt.Sprintf(" · %d entries unreadable", skipped)
	}
	fmt.Printf("\n  %s\n\n", dim(note))
}

// renderGroups prints each duplicate group with its file paths.
func renderGroups(root string, cat category, groups [][]*fileEntry) {
	fmt.Printf("  %s\n", bold(fmt.Sprintf("Duplicate %s groups (%d)", cat.singular, len(groups))))
	if len(groups) == 0 {
		fmt.Printf("  %s\n\n", dim("no duplicate "+cat.key+" found"))
		return
	}
	for i, group := range groups {
		fmt.Printf("\n  %s %s\n", yellow(fmt.Sprintf("● group %d", i+1)),
			dim(fmt.Sprintf("· %d files · %s each", len(group), humanSize(group[0].size))))
		for j, e := range group {
			marker, note := yellow("✗"), ""
			if j == 0 {
				marker, note = green("✓"), dim("  (original)")
			}
			fmt.Printf("    %s %s%s\n", marker, relPath(root, e.path), note)
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
	h(bold("USAGE"))
	h("  quickap [command] [flags]")
	fmt.Println()
	h("  Indexes image, document, music, video, archive, and application")
	h("  files under the current directory (recursive). The bare command")
	h("  prints a compact overview of every category; a category command")
	h("  prints detailed per-extension stats and enables -move/-delete.")
	fmt.Println()
	h(bold("COMMANDS"))
	h("  " + cyan("(none)") + "       index all categories, compact overview")
	h("  " + cyan("images") + "       index images only")
	h("  " + cyan("docs") + "         index documents only (alias: documents)")
	h("  " + cyan("music") + "        index music only")
	h("  " + cyan("videos") + "       index videos only (alias: video)")
	h("  " + cyan("archives") + "     index archives only (alias: archive)")
	h("  " + cyan("apps") + "         index applications only (aliases: app, applications)")
	h("  " + cyan("help [cmd]") + "   show this help, or help for one command")
	h("  " + cyan("version") + "      print version")
	fmt.Println()
	for _, name := range []string{"", "images", "docs", "music", "videos", "archives", "apps"} {
		printCommandBlock(cmdHelps[name])
		fmt.Println()
	}
	h(bold("NOTES"))
	h("  · duplicates are byte-identical files (SHA-256), regardless of name")
	h("    or extension; the lexically first path counts as the original")
	for _, cat := range categories {
		h(fmt.Sprintf("  · %-11s %s", cat.key+":", strings.Join(sortedExts(cat), " ")))
	}
	h("  · -move and -delete act on one category at a time, so they require")
	h("    a category command: quickap images -delete, quickap docs -move DIR")
	h("  · hidden directories (.git, ...) are skipped unless -hidden is set")
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
