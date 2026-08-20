# Contributing

Bug reports and patches are welcome. The bar here is unusual in one way worth knowing up front:
**this project treats a measurement as the argument.** Where a change rests on how tmux, ssh or
Claude Code behaves, say what you ran and what it answered — `docs/design.md` is full of that
shape and is the authority for how the program is meant to work.

## Build and run

    go build -o tmux-hub ./cmd/tmux-hub
    ./tmux-hub --help

You need `tmux` on any host you point it at. `tmux 3.2a` and `3.7b` are both supported and are the
two versions every tmux-facing claim in `docs/design.md` was measured on.

## The gates

CI runs all of these on every push. Run them before you open a pull request:

    gofmt -l .                              # must print nothing
    go build ./...
    go vet ./...
    go vet -tags e2e ./internal/e2e/        # behind a build tag, so the default vet cannot see it
    go vet -tags mockup ./internal/ui/      # likewise
    go test ./...                           # 19 packages
    go test -race ./...

    # The two HTML documents in docs/ are generated from the renderer and byte-reproducible.
    go test -tags mockup -run TestGenerate ./internal/ui/
    git diff --exit-code -- docs/

**Count the `ok` lines against the package count (19).** A package that failed to build is not
reported as a failure — it is not reported at all.

Changing production copy in `internal/ui` means regenerating the documents, because they are the
renderer's own bytes. That regeneration is also a refactor check: generate from a `git archive HEAD`
tree and from your tree, then `diff` — identical output proves you moved no frame.

## The interface tests

`internal/e2e` starts the real binary in a tmux pane on a private socket, drives it with
`send-keys` and reads it with `capture-pane`. It is the only suite that runs the program rather
than its model, and it is not part of CI because it needs a real `tmux` and, for some cases, a
`claude` on `PATH`:

    go test -tags e2e -timeout 30m ./internal/e2e/

A remote leg is skipped unless you name a host you can reach over ssh:

    HUB_E2E_HOST=your-host go test -tags e2e -timeout 30m ./internal/e2e/

Two rules that suite has paid for: **never run `tmux` without an explicit `-L` or `-S`** (a bare
`tmux` command reaches your own server), and **wait for the signal your assertion is about** — the
header paints before any poll, the tmux fleet arrives on the first tick, and agent rows arrive
0.5–2.8 s later from a different producer.

## Tests

- A frame test asserts on the string `View()` returns, and must fail against a stub. A screen that
  is defined, tested and never called is this repo's signature defect, and `t.Skip` reports PASS.
- Calibrate both ways: green on correct code, and red when you put the defect back. A guard whose
  red half you never saw is a guard you are guessing about.
- Assert a floor, never an exact count, for anything a scan produces — and print how many items
  were checked, because a checker that matched nothing looks exactly like a clean run.

## Releasing (maintainer)

Tag and push; `.github/workflows/release.yml` builds `linux/{amd64,arm64}` and
`darwin/{amd64,arm64}` with goreleaser and publishes the archives and checksums.

    git tag -a v0.1.0 -m "v0.1.0" && git push origin v0.1.0

The Homebrew **cask** is pushed to `DawnBreather/homebrew-tap` in the same run, which needs a
repository secret `HOMEBREW_TAP_TOKEN` — a fine-grained PAT with **Contents: read and write** on
that tap repository only. Without the secret the release still publishes its binaries and skips
the cask, so a missing optional token cannot fail a release.
