# Contributing

## Two rules that are not style preferences

**1. Every tmux invocation carries an explicit socket.**

A tmux command with no `-S`/`-L` talks to *your own live tmux server*. During this
project's design phase a probe run against a live server destroyed two
long-running sessions. `tmux.Runner` refuses an empty socket, and tests use
`t.TempDir()` sockets that they kill in `t.Cleanup`.

Never target the session `live1` or the default socket from a test or a script.

**2. Never name `#{client_activity}` or `#{client_created}`.**

On tmux 3.2a, querying either with no client attached **segfaults the entire
server**, taking every session with it. No guard helps: `#{q:...}`, `#{?...}`,
`x#{...}y` and `#{t:...}` all crash. `TestNoSourceFileNamesAForbiddenFormat`
enforces this at the repo level.

## A third, subtler one

**No literal `%` in a tmux argument unless the whole argument is a pane id.**

`display -p` runs its argument through `strftime`, so `display -p 'OK-%2'` returns
an **empty string with rc=0**. Emit identity through the format layer:
`display -p -t %2 'OK #{pane_id}'`. `Validate` enforces this.

## Tests

    go test ./...
    gofmt -l .        # must be empty

Tests that need a real tmux skip cleanly when it is absent. `classify()` fixtures
require **both poles**: a waiting pane must read `needs` and a working pane must
not.

## Priorities, as of 2026-08-11

The concern right now is **experience** — UX, DX, DevOpsX, TerminalX, SysAdminX
(`docs/design.md` §16). Security hardening is explicitly *not* a goal at this
stage. The two rules above are kept anyway because they are correctness rules
that happen to look like safety ones: `#{client_activity}` crashes a real
server, and a bare `tmux` command edits the wrong machine's state.
