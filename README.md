# quickap

[![CI](https://github.com/jordancannon88/quickap/actions/workflows/ci.yml/badge.svg)](https://github.com/jordancannon88/quickap/actions/workflows/ci.yml)

> [!NOTE]
> AI (Claude Code) was used to help write the code in this project.

A fast, zero-dependency CLI that indexes images, documents, music, videos,
archives, and applications under the current directory and reports totals,
per-extension stats, and duplicates — with a clean, colorful terminal UI.

The bare command gives a compact overview of every category:

<img src="assets/screenshot.svg" alt="quickap overview output: a table of Images, Documents, Music, Videos, Archives, and Applications with file counts, sizes, unique/duplicate counts, and reclaimable space" width="680">

A category command gives the detailed view — summary box, reclaimable space,
and a per-extension breakdown with two-tone bars (cyan unique, yellow
duplicate):

```
  ◆ quickap · file index
  /home/jordan/stuff

  Videos

  ┌────────────────────────────────────────────┐
  │  All videos               3        1.8 MB  │
  │  Unique videos            2        1.0 MB  │
  │  Duplicates               1      781.2 KB  │
  └────────────────────────────────────────────┘
  781.2 KB reclaimable by removing duplicates

  By extension
            all   uniq   dup                                size
  .mkv        1      1     –  ████████████████████████  781.2 KB
  .mp4        1      0     1  ████████████████████████  781.2 KB
  .webm       1      1     –  ████████████████████████  293.0 KB
  bar: █ unique  █ duplicate

  scanned in 2ms
```

## Install

### Download a release

Prebuilt binaries for every tagged version are on the
[releases page](https://github.com/jordancannon88/quickap/releases),
built automatically by the release workflow:

| File                          | Platform                        |
| ----------------------------- | --------------------------------|
| `quickap-linux-amd64`         | Linux, x86-64                   |
| `quickap-linux-arm64`         | Linux, ARM64                    |
| `quickap-darwin-arm64`        | macOS, Apple Silicon (M-series) |
| `quickap-darwin-amd64`        | macOS, Intel                    |
| `quickap-windows-amd64.exe`   | Windows, x86-64                 |

Each release also includes a `checksums.txt` with the SHA-256 of every
binary, signed keylessly with [cosign](https://docs.sigstore.dev/)
(`checksums.txt.sig` + `checksums.txt.pem`) by the release workflow's
GitHub OIDC identity.

```sh
# example: Linux x86-64
curl -sLo quickap https://github.com/jordancannon88/quickap/releases/latest/download/quickap-linux-amd64
chmod +x quickap
mv quickap ~/.local/bin/   # or anywhere on your PATH
```

To verify a download:

```sh
curl -sLO https://github.com/jordancannon88/quickap/releases/latest/download/checksums.txt
sha256sum -c checksums.txt --ignore-missing   # macOS: shasum -a 256 -c
```

To additionally verify the checksums file is authentic (requires
[cosign](https://docs.sigstore.dev/cosign/system_config/installation/)):

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

This proves the checksums file was produced by this repository's release
workflow, not by someone who merely obtained upload access.

macOS note: binaries downloaded via browser get quarantined by Gatekeeper —
clear it with `xattr -d com.apple.quarantine ./quickap`. The binaries are
unsigned; build locally if you prefer.

### Build locally

Requires Go 1.26+. No external dependencies, no cgo.

```sh
git clone https://github.com/jordancannon88/quickap.git
cd quickap
go test ./...              # run the tests
go build -o quickap .      # build for this machine
cp quickap ~/.local/bin/   # or anywhere on your PATH
```

Cross-compile by setting the target platform, e.g.:

```sh
GOOS=darwin GOARCH=arm64 go build -o quickap-darwin-arm64 .
GOOS=windows GOARCH=amd64 go build -o quickap-windows-amd64.exe .
```

### CI & releases

GitHub Actions runs vet, tests, and a build on every push and pull request
(`.github/workflows/ci.yml`). Pushing a tag matching `v*` (e.g. `v1.0.0`)
runs the release workflow (`.github/workflows/release.yml`), which builds
the five platform binaries above and publishes them as a GitHub release
with a per-commit changelog and a cosign-signed `checksums.txt` of
SHA-256 sums.

## Usage

```sh
quickap [command] [flags]

quickap                   # overview of all categories
quickap images            # detailed image report
quickap docs -list        # document report + duplicate groups
quickap music -move DIR   # move music duplicate groups into DIR for sorting
quickap videos -delete    # delete video duplicates, keeping originals
quickap -ignore dist      # skip every dir named "dist" (repeatable, or a,b,c)
quickap -hidden           # include hidden directories in the scan
quickap help              # full help, including per-command flags
quickap help docs         # help for one command (also: quickap docs -help)
quickap version           # print version (also: -version)
```

### Commands

| Command      | Description                                          |
| ------------ | -----------------------------------------------------|
| *(none)*     | Index all categories, compact overview.              |
| `images`     | Index images only.                                   |
| `docs`       | Index documents only (alias: `documents`).           |
| `music`      | Index music only.                                    |
| `videos`     | Index videos only (alias: `video`).                  |
| `archives`   | Index archives only (alias: `archive`).              |
| `apps`       | Index applications only (aliases: `app`, `applications`). |
| `help [cmd]` | Show help, or help for one command.                  |
| `version`    | Print the version.                                   |

### Flags

Flags operate on the current command's category. The modifying flags `-move`
and `-delete` act on one category at a time, so they **require a category
command**; the bare `quickap` command indexes and reports only.
`quickap help <cmd>` shows the exact per-command flag descriptions.

| Flag        | Commands       | Description                                                                                                                 |
| ----------- | -------------- | --------------------------------------------------------------------------------------------------------------------------|
| `-list`     | all            | List each duplicate group with file paths. The kept original is marked `✓`, duplicates `✗`.                                 |
| `-move DIR` | category cmds  | Move each duplicate group — **original and copies** — into `DIR/<category>/group-NNN/` for manual side-by-side sorting. `DIR` is created if needed and resolved relative to the current directory. |
| `-delete`   | category cmds  | **Permanently delete** duplicate files, keeping each group's original. No undo. Cannot be combined with `-move`.            |
| `-ignore DIR` | all          | Skip a directory while scanning. A bare name (`node_modules`) skips every directory with that name; a path (`files/cache`) skips that path relative to the current directory. Repeat the flag or comma-separate for multiple: `-ignore tunes,media -ignore dist`. |
| `-hidden`   | all            | Include hidden directories (`.foo/`) in the scan. Skipped by default.                                                       |
| `-version`  | all            | Print the version and exit.                                                                                                 |
| `-help`     | all            | Show help for the current command.                                                                                          |

Commands and flags compose: `quickap docs -hidden -list -move ../dupes`.
`-move` keeps categories separate — e.g. `quickap images -move ../dupes`
writes to `../dupes/images/group-001/`.

## How duplicate detection works

- Files are grouped by byte size first; only same-size candidates are hashed
  (SHA-256, in parallel across CPU cores), so unique-sized files are never
  read and scans stay fast on large trees.
- **Duplicates are byte-identical files**, regardless of filename or
  extension — the same bytes saved as `movie.mp4` and `movie-copy.mkv` are
  caught. Similar-looking but re-encoded/resized files are *not* flagged.
- Duplicates are detected within each category independently.
- Within a group, the lexically first path counts as the original; the rest
  are duplicates. The "Duplicates" count is the number of redundant copies,
  so a group of 3 identical files counts as 1 original + 2 duplicates.
  `-list` shows exactly which file each group keeps — review it before
  running `-delete`.

## Categories

| Category     | Extensions                                                              |
| ------------ | ------------------------------------------------------------------------|
| images       | `.avif .bmp .gif .heic .heif .ico .jpeg .jpg .png .svg .tif .tiff .webp` |
| documents    | `.csv .doc .docx .epub .md .odp .ods .odt .pdf .ppt .pptx .rtf .txt .xls .xlsx` |
| music        | `.aac .aif .aiff .flac .m4a .mid .midi .mp3 .ogg .opus .wav .wma`       |
| videos       | `.3gp .avi .flv .m4v .mkv .mov .mp4 .mpeg .mpg .ogv .webm .wmv`         |
| archives     | `.7z .7zip .bz2 .gz .iso .rar .tar .tbz .tgz .xz .zip .zst`             |
| apps         | `.apk .appimage .deb .dmg .exe .msi .pkg .rpm`                          |

Extensions are matched case-insensitively.

## Notes

- The scan is recursive from the current working directory.
- The summary report always reflects the state **before** `-move`/`-delete`.
- `-move` keeps original filenames, suffixing collisions within a group
  (`a.jpg`, `a-2.jpg`), and falls back to copy+delete across filesystems.
  Collisions are detected case-insensitively (`Photo.JPG` vs `photo.jpg`)
  so nothing is overwritten on case-insensitive filesystems (macOS APFS,
  Windows).
- A `-move` target inside the current directory will be re-indexed on the
  next run; use a target outside it (e.g. `../dupes`) to avoid that.
- Unreadable files or directories are skipped and counted in the footer,
  never fatal.
- Colors turn off automatically when output is piped, or set `NO_COLOR=1`.

## License

[AGPL-3.0](LICENSE) — GNU Affero General Public License v3.0.
