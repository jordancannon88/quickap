<div align="center">

<br>

<img src="assets/logo.svg" alt="quickap logo: a miniature terminal window showing ◆ quickap with a block cursor, above a row of seven colored dots, one for each file category" width="380">

<br>

**A fast CLI with zero dependencies that shows you what's in a
directory, and what's in it twice.**

*⚡ **quick**ap = **quick cap**ture. Say it "quick cap": one quick
capture of everything in a directory.*

<sub>applications · archives · documents · images · videos · music ·
everything else</sub>

[![CI](https://github.com/jordancannon88/quickap/actions/workflows/ci.yml/badge.svg)](https://github.com/jordancannon88/quickap/actions/workflows/ci.yml)
[![License: AGPL-3.0](https://img.shields.io/badge/license-AGPL--3.0-blue.svg)](LICENSE)
[![Release](https://img.shields.io/github/v/release/jordancannon88/quickap)](https://github.com/jordancannon88/quickap/releases/latest)

**[Install](#install)** · **[Usage](#usage)** ·
**[Examples](#examples)** ·
**[How it works](#how-duplicate-detection-works)** ·
**[Wiki](https://github.com/jordancannon88/quickap/wiki)**

</div>

<table>
  <tr>
    <td>🔍 <b>Index</b></td>
    <td>Seven file categories in one recursive scan, with totals and stats for every extension in a clean, colorful terminal UI.</td>
  </tr>
  <tr>
    <td>👯 <b>Find duplicates</b></td>
    <td>By content (SHA&#8209;256). A renamed copy, or one saved under a different extension, is still a copy.</td>
  </tr>
  <tr>
    <td>🧹 <b>Clean up</b></td>
    <td>List duplicates, move them out for manual sorting, or delete them. Each group's original is always kept.</td>
  </tr>
  <tr>
    <td>🗂️ <b>Organize</b></td>
    <td>Copy — or move — everything minus the duplicates into a clean tree with one folder per category, every file verified by hash.</td>
  </tr>
  <tr>
    <td>📦 <b>Single binary</b></td>
    <td>No runtime, no config, no dependencies. Linux, BSD, macOS, Windows.</td>
  </tr>
</table>

> [!NOTE]
> AI was used to help write the code in this project.

## At a glance

`quickap` with no arguments gives a compact overview of every category:

<img src="assets/screenshot.svg" alt="quickap overview output: a table of Applications, Archives, Documents, Images, Videos, Music, and Other with file counts, sizes, unique/duplicate counts, and reclaimable space, plus a reclaimable-space meter" width="760">

A category command — here `quickap videos` — gives the detailed view: a
summary, a reclaimable-space meter, and a per-extension breakdown with
two-tone bars (cyan unique, yellow duplicate):

<img src="assets/screenshot-detail.svg" alt="quickap videos output: a summary table showing 3 videos, 2 unique, 1 duplicate, a reclaimable-space meter at 42%, and a per-extension table for .mkv, .mp4, and .webm with two-tone bar charts" width="780">

*Both images are real program output, rendered to SVG by
[`assets/ansi2svg.py`](assets/ansi2svg.py) — what you see is exactly
what the tool prints.*

## Install

### Download a release

Grab the binary for your platform from the
[latest release](https://github.com/jordancannon88/quickap/releases/latest)
and put it on your `PATH`:

```sh
# example: Linux x86-64
curl -sLo quickap https://github.com/jordancannon88/quickap/releases/latest/download/quickap-linux-amd64
chmod +x quickap
mv quickap ~/.local/bin/
```

| Platform | Assets |
| -------- | ------ |
| Linux    | `quickap-linux-amd64` · `quickap-linux-arm64` |
| macOS    | `quickap-darwin-arm64` (Apple Silicon) · `quickap-darwin-amd64` (Intel) |
| BSD      | `quickap-{freebsd,openbsd,netbsd,dragonfly}-amd64` · `quickap-freebsd-arm64` |
| Windows  | `quickap-windows-amd64.exe` |
| Any      | `quickap.1` (manual page) |

macOS note: browser downloads get quarantined by Gatekeeper — clear it
with `xattr -d com.apple.quarantine ./quickap`.

<details>
<summary><strong>Verify your download</strong> (SHA-256 + cosign)</summary>

Every release includes a `checksums.txt` with the SHA-256 of every
asset:

```sh
curl -sLO https://github.com/jordancannon88/quickap/releases/latest/download/checksums.txt
sha256sum -c checksums.txt --ignore-missing   # macOS: shasum -a 256 -c
```

The checksums file itself is signed keylessly with
[cosign](https://docs.sigstore.dev/) by the release workflow's GitHub
OIDC identity — verifying the signature proves it was produced by this
repository's release workflow:

```sh
base=https://github.com/jordancannon88/quickap/releases/latest/download
curl -sLO $base/checksums.txt.sig
curl -sLO $base/checksums.txt.pem
cosign verify-blob \
  --certificate checksums.txt.pem \
  --signature checksums.txt.sig \
  --certificate-identity-regexp 'https://github\.com/jordancannon88/quickap/\.github/workflows/release\.yml@refs/tags/v.*' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  checksums.txt
```

</details>

### Build locally

Requires Go 1.26+. No external dependencies, no cgo — cross-compile
with plain `GOOS`/`GOARCH`.

```sh
git clone https://github.com/jordancannon88/quickap.git
cd quickap
go test ./...
go build -o quickap .
cp quickap ~/.local/bin/
```

### Man page

The manual page [`quickap.1`](quickap.1) documents every command, flag,
and behavior in full — it's at the repository root and attached to each
release. Install it so `man quickap` works anywhere:

```sh
install -Dm644 quickap.1 ~/.local/share/man/man1/quickap.1        # per-user
sudo install -Dm644 quickap.1 /usr/local/share/man/man1/quickap.1 # system-wide
```

Or preview it without installing: `man ./quickap.1`.

## Usage

```sh
quickap [command] [flags] [directory]
```

The directory to scan defaults to your home directory (`/home/you` on
Linux, `/Users/you` on macOS, `C:\Users\you` on Windows); pass a
different one as the **last** argument, after any flags. The report
header always shows the running user's home directory alongside the
scanned directory. Relative `-move`, `-organize`, `-organize-move`,
`-organize-log`, and `-ignore` paths resolve against the scanned
directory.

### Commands

| Command      | Description |
| ------------ | ----------- |
| *(none)*     | Index all categories, compact overview. |
| `apps`       | Index applications only (aliases: `app`, `applications`). |
| `archives`   | Index archives only (alias: `archive`). |
| `docs`       | Index documents only (alias: `documents`). |
| `images`     | Index images only. |
| `videos`     | Index videos only (alias: `video`). |
| `music`      | Index music only. |
| `other`      | Index files no other category claims (alias: `others`). |
| `help [cmd]` | Show help, or help for one command. |
| `version`    | Print the version. |

### Flags

Every command accepts these:

| Flag | Description |
| ---- | ----------- |
| `-list-duplicates`, `-ld` | List each duplicate group; the kept original is marked `✓`, copies `✗`. |
| `-list-unique`, `-lu`     | List every file that is not a duplicate copy, with sizes. |
| `-list-large`, `-ll`      | List the 50 largest files, largest first. |
| `-organize DIR`           | Copy every indexed file that is not a duplicate copy into `DIR/<Category>/` (`Applications`, `Documents`, `Music`, …), verifying each copy against its source by SHA-256. Sources are left in place; files already present in `DIR` with identical content are not copied again. Cannot be combined with `-move` or `-delete`. |
| `-organize-move DIR`      | Like `-organize`, but **move** the files: each one is hash-verified at the destination before its source is deleted, so no content is ever lost to a bad copy. Skipped duplicate copies are left in place. |
| `-organize-log FILE`      | Append a full audit trail of the organize run to `FILE`: every copied/moved file with its destination, every skipped duplicate with the original it matches, every "already present" hit, and every failure with its reason. Requires `-organize` or `-organize-move`. |

In both organize modes, any files that could not be copied or moved are
listed — with the reason per file — in `DIR/quickap-organize-failures.txt`.
The list is only created when something failed, and a later run without
failures removes it again.
| `-ignore DIR`             | Skip a directory: a bare name (`node_modules`) matches anywhere, a path (`files/cache`) only that location. Repeat or comma-separate for multiple. |
| `-hidden`                 | Include hidden directories (`.foo/`); skipped by default. |
| `-no-cache`               | Disable the hash cache for this run. |
| `-verify`                 | Re-hash every duplicate candidate, ignoring cached hashes. |
| `-clear-cache`            | Delete the hash cache and exit. |
| `-spacious`               | Add vertical space between table rows; default is compact. |
| `-verbose`, `-vv`         | Show scan timing, hash-cache stats and location, and hints. |
| `-version`                | Print the version and exit. |
| `-help`                   | Show help for the current command. |

The cleanup flags act on one category at a time, so they **require a
category command** (e.g. `quickap images -delete`):

| Flag | Description |
| ---- | ----------- |
| `-move DIR` | Move each duplicate group — **original and copies** — into `DIR/<category>/group-NNN/` for manual side-by-side sorting. `DIR` is created if needed. |
| `-delete`   | **Permanently delete** duplicate files, keeping each group's original. No undo. Cannot be combined with `-move`. |

Full flag semantics, file locations, and exit statuses are in
`man quickap` and `quickap help <cmd>`.

## Examples

**Get the lay of the land.** Overview first, then drill into the
category that looks bloated:

```sh
quickap ~/Pictures                    # all categories, compact table
quickap images ~/Pictures             # per-extension detail for images
quickap images -verbose ~/Pictures    # ... plus scan timing and cache stats
```

**Clean up carefully.** Review what would be touched, then move groups
out for side-by-side sorting:

```sh
quickap images -list-duplicates        # see every duplicate group; ✓ = kept
quickap images -move ../photo-dupes    # writes to ../photo-dupes/images/group-001/ etc.
```

**Clean up decisively.** When you trust the byte-identical guarantee
and just want the space back:

```sh
quickap videos -ld         # one last look (-ld = -list-duplicates)
quickap videos -delete     # remove duplicates, keep each group's original
```

**Consolidate a messy tree.** Copy everything — minus the duplicates —
into a clean structure with one folder per category, every file
verified by hash. Re-running skips whatever is already there. Use
`-organize` to copy (originals stay put) or `-organize-move` to move
(each source is deleted only after its copy is verified):

```sh
quickap -organize ~/sorted ~/Downloads        # copy: ~/sorted/Documents/, ~/sorted/Images/, ...
quickap -organize-move ~/sorted ~/Downloads   # move: same layout, sources removed once verified
quickap images -organize ~/sorted             # just the images from your home dir
quickap -organize ~/sorted -organize-log ~/sorted.log   # ... plus a full audit trail
```

**Scope the scan.** Skip directories by name or path, or opt into
hidden ones:

```sh
quickap -ignore node_modules,dist      # skip every dir with those names
quickap docs -ignore files/archive     # skip only that path
quickap images -hidden -ignore .git    # scan hidden dirs, but not .git
```

**Force a fresh look.** If you suspect files changed without their
size/mtime changing, or want to time a cold scan:

```sh
quickap -verify        # re-hash everything, refresh the cache
quickap -no-cache -vv  # ignore the cache entirely, show timing
```

Flags stack freely on any command:
`quickap docs -hidden -ld -move ../dupes -verbose`.

## How duplicate detection works

- **Duplicates are byte-identical files** (SHA-256), regardless of
  filename or extension — `movie.mp4` and `movie-copy.mkv` with the same
  bytes are caught; re-encoded or resized lookalikes are *not* flagged.
- Files are grouped by byte size first; only same-size candidates are
  hashed (in parallel across CPU cores), so unique-sized files are never
  read and scans stay fast on large trees.
- Duplicates are detected within each category independently. Within a
  group, the lexically first path counts as the original; a group of 3
  identical files is 1 original + 2 duplicates. `-list-duplicates` shows
  exactly which file each group keeps — review it before `-delete`.
- **Hashes are cached between runs** and reused while a file's size and
  mtime are unchanged, so repeat scans only read new or modified files.
  Run with `-vv` to see the split (`12 hashed, 240 from cache`) and
  where the cache file lives.

### The hash cache

| Platform | Location |
| -------- | -------- |
| Linux    | `~/.cache/quickap/` (or `$XDG_CACHE_HOME/quickap/`) |
| macOS    | `~/Library/Caches/quickap/` |
| Windows  | `%LocalAppData%\quickap\` |

All scans share a single `hashes.bin` (a compact binary format — fast
to load even at hundreds of thousands of entries) keyed by absolute
file path, so hashing a parent directory also warms the cache for its
subdirectories. A `hashes.json` from an older version is migrated
automatically on the next run. Nothing is ever written into the
scanned directories. Housekeeping is automatic — stale entries are
pruned, and a missing or corrupt cache just means the next run
re-hashes. Deleting the cache directory is always safe; use `-verify`
to force a full re-hash.

## Categories

| Category    | Extensions |
| ----------- | ---------- |
| apps        | `.apk .appimage .deb .dmg .exe .msi .pkg .rpm` |
| archives    | `.7z .7zip .bz2 .gz .iso .rar .tar .tbz .tgz .xz .zip .zst` |
| documents   | `.csv .doc .docx .epub .md .odp .ods .odt .pdf .ppt .pptx .rtf .txt .xls .xlsx` |
| images      | `.avif .bmp .gif .heic .heif .ico .jpeg .jpg .png .svg .tif .tiff .webp` |
| videos      | `.3gp .avi .flv .m4v .mkv .mov .mp4 .mpeg .mpg .ogv .webm .wmv` |
| music       | `.aac .aif .aiff .flac .m4a .mid .midi .mp3 .ogg .opus .wav .wma` |
| other files | *everything the categories above don't claim, including files with no extension* |

Extensions match case-insensitively. **Other files** is the exact
complement of the rest — `.json`, `.log`, `.go`, or no extension at all
land there — so the seven categories together always account for every
file in the scan (hidden and `-ignore`d directories aside).

## Notes

- The summary report always reflects the state **before**
  `-move`/`-delete` ran.
- `-move`, `-organize`, and `-organize-move` keep original filenames,
  suffixing collisions (`a.jpg`, `a-2.jpg`) — detected
  case-insensitively so nothing is overwritten on macOS/Windows
  filesystems. A target inside the scanned directory will be re-indexed
  on the next run; use one outside it (e.g. `../dupes`, `../sorted`).
- `-organize` copies — it never touches the sources. Every copy is
  verified by re-reading the destination and comparing SHA-256 hashes;
  a failed or mismatched copy is removed and reported. Duplicate copies
  are skipped, so each unique content lands in the new tree exactly
  once.
- `-organize-move` deletes each source only **after** its file is
  hash-verified at the destination — a failed or mismatched copy leaves
  the source untouched. A source whose exact content is already in the
  destination is deleted without copying again. The skipped duplicate
  copies stay in the source tree; remove them with `-delete` on a
  category if desired.
- Files the organize modes could not handle (unreadable sources, failed
  verification, a source that could not be deleted after its verified
  copy) are recorded in `quickap-organize-failures.txt` inside the
  target directory, one tab-separated `path` + `reason` line per file —
  handy for retrying or auditing. A run without failures deletes the
  stale list.
- `-organize-log` **appends** each run to the log file — a commented
  header (time, mode, scanned directory, target), one tab-separated
  `action` + `source` + `destination or detail` line per action, and a
  commented summary — so the file accumulates the full history of your
  organize runs.
- Symbolic links and other non-regular files are ignored — never
  followed, indexed, or hashed. A symlinked scan directory is resolved
  to its target first.
- Unreadable files or directories are skipped and counted, never fatal.
- Colors turn off automatically when piped, or set `NO_COLOR=1`.

## Contributing

Contributions are welcome! **Before opening a PR, please open an
[issue](https://github.com/jordancannon88/quickap/issues) first** to
discuss your idea and get feedback from maintainers.

1. Open an issue describing the bug or the feature you'd like to build.
2. Fork, branch from `dev`, make the change — standard library only,
   no new dependencies.
3. Run `go vet ./...` and `go test ./...`; keep the docs (README,
   `quickap.1`, built-in help) in sync with user-facing changes.
4. Open a PR against `dev`, linking the issue.

The full guide lives in [CONTRIBUTING.md](CONTRIBUTING.md) and on the
[Contributing wiki page](https://github.com/jordancannon88/quickap/wiki/Contributing);
the [wiki](https://github.com/jordancannon88/quickap/wiki) also covers
CI, branch protection, and the release process.

## License

[AGPL-3.0](LICENSE) — GNU Affero General Public License v3.0.
