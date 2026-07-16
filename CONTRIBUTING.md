# Contributing to quickap

Thanks for your interest in improving quickap! This guide covers
everything you need to go from idea to merged pull request.

## Open an issue first

**Before opening a PR, please open an
[issue](https://github.com/jordancannon88/quickap/issues) first to
discuss your idea and get feedback from maintainers.** This applies to
features and non-trivial changes alike — it saves you from building
something that can't be merged, and it gives the change a place to be
designed before code exists. Small fixes (typos, broken links, obvious
one-line bugs) can skip straight to a PR.

When reporting a bug, include:

- The output of `quickap version`
- Your OS and platform (e.g. Linux x86-64, macOS Apple Silicon)
- What you ran, what you expected, and what happened instead —
  `NO_COLOR=1` output pastes cleanly

## Development setup

quickap is a single-package Go program with **zero dependencies** — no
`go.sum`, no cgo, nothing to vendor. All you need is Go 1.26+:

```sh
git clone https://github.com/jordancannon88/quickap.git
cd quickap
go test ./...              # run the tests
go vet ./...               # what CI runs
go build -o quickap .      # build for this machine
```

## Project principles

These are the constraints every change must respect:

- **Zero dependencies.** Standard library only. A PR that adds a module
  dependency will not be merged — that's the project's core identity.
- **No cgo, single static binary.** Everything must cross-compile with
  plain `GOOS`/`GOARCH` for Linux, the BSDs, macOS, and Windows.
- **Polished, readable output.** Full-word labels (no cryptic
  abbreviations), colors that disable automatically when piped or under
  `NO_COLOR`, aligned tables that survive odd inputs (long extensions,
  ANSI-width math).
- **Safe by default.** Reports never modify files; destructive actions
  (`-delete`) are explicit, scoped to a category command, and warned
  about. Unreadable files are counted, never fatal.

## Making changes

- **Branch from `dev`.** Active development happens on `dev`; `main`
  tracks releases (`dev` → `main` → tag).
- **Add or update tests** in `main_test.go` for behavior changes.
- **Keep the docs in sync.** A user-facing change usually touches four
  places: the code, the built-in help (`printHelp` /
  `printCommandBlock` in `main.go`), the man page (`quickap.1`), and
  `README.md`. PRs that change behavior without updating all of them
  will be asked to.
- **Screenshots are generated, not drawn.** The README's SVG captures
  come from real program output rendered by `assets/ansi2svg.py`. If
  your change alters the output, regenerate them:
  `script -qec "quickap" /dev/null | python3 assets/ansi2svg.py /dev/stdin`.

## Pull requests

1. Make sure an issue exists and the approach has been discussed
   (see above).
2. Fork, branch from `dev`, and keep the PR to one logical change.
3. Run `go vet ./...` and `go test ./...` locally. CI additionally runs
   the tests with `-race` and a build on every push and PR — it must
   pass.
4. Use imperative commit subjects ("Add -list-large flag", not "Added"
   or "Adds").
5. Open the PR against `dev` and link the issue it resolves.

## License

quickap is licensed under [AGPL-3.0](LICENSE). By contributing, you
agree that your contributions are licensed under the same terms.
