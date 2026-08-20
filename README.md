# tmux-hub

[![CI](https://github.com/DawnBreather/tmux-hub/actions/workflows/ci.yml/badge.svg)](https://github.com/DawnBreather/tmux-hub/actions/workflows/ci.yml) [![Go Reference](https://pkg.go.dev/badge/github.com/DawnBreather/tmux-hub.svg)](https://pkg.go.dev/github.com/DawnBreather/tmux-hub) [![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)

A control panel over your tmux sessions, built for orchestrating many Claude Code
sessions at once.

It shows two things in one list, sorted by which one needs you most:

- every pane on a tmux server, with its state read off the screen and a tile of
  what it actually said;
- every Claude Code session `claude agents --json` reports, whose state comes
  from Claude itself as a fact. Most of those have **no tmux pane at all**, so
  nothing else shows them.

Then it writes back: select panes, type a prompt, and it is pasted into each one
and submitted — guarded per target by that pane's own identity token, so a pane
that stopped being the agent you selected is refused rather than written to
(`docs/design.md` §7).

## Install

macOS, with Homebrew:

    brew install DawnBreather/tap/tmux-hub

A release binary. Every release ships `linux_amd64`, `linux_arm64`, `macos_arm64` and
`macos_amd64`, with a `checksums.txt` beside them — pick yours from the
[latest release](https://github.com/DawnBreather/tmux-hub/releases/latest), or:

    VER=0.1.1   # a released version; the tag is v$VER
    curl -sSfL "https://github.com/DawnBreather/tmux-hub/releases/download/v$VER/tmux-hub_${VER}_linux_amd64.tar.gz" \
      | tar -xz tmux-hub && ./tmux-hub --version

With Go:

    go install github.com/DawnBreather/tmux-hub/cmd/tmux-hub@latest

Or from a clone:

    go build -o tmux-hub ./cmd/tmux-hub

`tmux` is the only requirement, on any host you point the hub at. Versions 3.2a and 3.7b are both
supported and are the two every tmux-facing claim in `docs/design.md` was measured against.

### Platforms

Linux and macOS, on x86-64 and arm64. Two things differ by platform, both in one place each:

- **Reading the process table.** Identifying which pane runs an agent means walking processes.
  On Linux that is one pass over `/proc`; macOS has no `/proc`, so it is one `ps -A -ww` and the
  same identification on the result (`internal/proc`).
- **Asking who is on the other end of a socket.** Linux answers with `SO_PEERCRED`; macOS splits
  the same answer into `LOCAL_PEERPID` and `LOCAL_PEERCRED` (`internal/hub/peer_*.go`). On a
  platform with neither, the hub loses that corroboration and nothing else.

A **remote** host is expected to be Linux: the one-shot walk the hub runs over ssh reads `/proc`
on the far side, so a remote macOS host polls and displays but identifies no agents — and an
unidentified target is one every send refuses.

## Use

    ./tmux-hub                 # the fleet as a filesystem
    ./tmux-hub --view=flat     # the flat dashboard instead
    ./tmux-hub status          # one poll cycle as JSON, for scripts and monitors

Each row in that JSON carries the claim its state is quoting — `agent_word` is the
listing's own word for the session and `agent_pid` the pid the reporting host gave
for it. A pid means that host can see the worker, so a state beside `agent_word`
with no `agent_pid` came from a machine that shares `~/.claude` and nothing else.

The hub opens on the FILESYSTEM view: hosts are volumes, directories are
directories, sessions are files, and what you pinned is a `FAVOURITES` band at the
top. The map is open and the folders are shut — every volume and every directory
is on the first screen, and a closed directory still says how much of it wants you
(`▸ st-edgebox/  ⚑ 2  of 8`), so thirteen waiting sessions across fifty-four are
one key away rather than a screenful of scrolling. `enter` opens and closes, `a`
goes straight to whatever has waited longest inside — opening every directory on
the way — `n` creates a session in the directory the cursor is on with the path
already filled in, and `t` toggles to the flat dashboard and back. The band at the
bottom describes whatever the cursor is on: a session's pane, or a directory's
address, counts and contents.

On the first run there is no `hosts.toml`, so the picker opens on it: every `Host`
in your ssh config, probed with `tmux -V`, each excluded one saying why and what
would fix it. Tick the ones you want and press enter: that writes the fleet to
`~/.config/tmux-hub/hosts.toml`, generated and hand-editable. Every run after that
starts straight into the fleet, and `p` reopens the picker.

A second, optional file groups your sessions by what you are working on.
`P` lists every project and how much of each one wants you; `enter` narrows the
dashboard to one. With no file at all the grouping is the last segment of each
pane's working directory, which is usually right — write
`~/.config/tmux-hub/projects.toml` when it is not:

    [[project]]
    name = "streams"
    prefix = "/home/dev/lab/streams"

    [[project]]
    name = "just this host"
    prefix = "/w"
    host = "nuc"

The longest matching prefix wins, a prefix matches on a path boundary (so
`/home/dev/lab/st` does not swallow `/home/dev/lab/streams`), and `host` is
optional — without it the rule applies everywhere. Unlike `hosts.toml`, a
`projects.toml` that cannot be parsed does **not** stop the hub: it loses the names,
keeps the fleet, and says what was wrong.

Enabling a host is all there is to it: the hub spawns an `ssh -N -M` control master
per host and polls tmux over it, with no port forward anywhere. A master
deliberately outlives the hub — that is what makes the second run of the day free
(3.6 ms to adopt one against 1.55 s to spawn) — so there is a way out:

    ./tmux-hub --stop-masters   # stop every master this hub may have left running

Unticking a host stops its master too, and a master whose host is no longer enabled
is stopped at the next start.

`--host` is the escape hatch for a setup the file cannot describe, and it is **added
to** the file's hosts rather than replacing them. A second local server:

    ./tmux-hub --host sandbox=/tmp/hub-sandbox.sock,local

`local` marks the socket as a server on *this* machine, which is what turns the
identity walk on for it and therefore what makes it writable. Or an operator's own
forward, if you have one:

    ssh -N -M -S /run/user/$UID/nuc.ctl \
        -L /run/user/$UID/nuc.sock:/tmp/tmux-1000/default nuc &
    ./tmux-hub --host nuc=/run/user/$UID/nuc.sock,ssh=nuc,ctl=/run/user/$UID/nuc.ctl

A host given a socket is polled over that socket. `ssh=` and `ctl=` are then what
`a` needs: a forwarded socket can carry the polling but not an attach, so attaching
goes through the ssh master. The same master is what identifies a remote pane as an
agent — that walk needs a shell on the far side — so **without `ctl=` a remote host
is read-only**: it polls and shows its panes, but none of them can be identified, so
none is ever stamped and every send to it is refused for want of an identity token.

`--hosts <path>` moves the host list. `--probe-timeout` is how long one host gets to
answer the picker's probe; a host that runs out of time keeps its tick box and is
shown as slow rather than absent, because two of five real hosts here swing fourfold
between probes and no timeout is right for all of them.

To help settle the timing thresholds, log every state change:

    ./tmux-hub --log-states ~/.local/state/tmux-hub/states.jsonl

Every send is appended to `$XDG_STATE_HOME/tmux-hub/history.jsonl`, which the
history view reads. `--history <path>` moves it; `--no-history` turns both off.

Favourites are persisted to `$XDG_STATE_HOME/tmux-hub/favourites.json`; `--favourites` moves the
file and `--no-favourites` ignores it. A pin follows the SESSION, including across the moment it
gains a pane — press `a` on a pinned pane-less row and it stays pinned and stays at the top, because
the key is the Claude session's own id rather than the name, which the door changes. Pinning does not reorder the fleet, it SPLITS it: every
pinned row above every other one, with the attention order untouched inside each half — so a pinned
session that is waiting for you still leads the pinned band, and an unpinned one that is waiting
still leads the rest. Pinning a project pins every session in it, including ones that appear later.

Hidden panes are persisted to `$XDG_STATE_HOME/tmux-hub/hidden.json`. `--hidden <path>`
moves it; `--no-hide` shows every pane, ignoring the hidden set.

## Keys

| | |
|---|---|
| `j` / `k` | move the cursor |
| `space` | select the pane under the cursor |
| `A` | select every pane **on screen** — never one below the fold |
| `C` | clear the selection |
| `i` | open the input box; `alt+enter` for a newline, `enter` to send, `esc` keeps the draft |
| `!` | interrupt the selected panes with `C-c` |
| `h` | the history of what was sent, `r` re-sends an entry to the CURRENT selection |
| `n` | open the launch form to create a new Claude Code session |
| `R` | restart the selected pane, resuming its Claude session with `--resume` |
| `K` | kill the selected pane (always confirms, naming what is running) |
| `p` | the host picker: tick what to keep, `r` probes again, `enter` saves |
| `P` | the project list: what state each thing you are working on is in, `enter` narrows the dashboard to one, `a` narrows AND puts the cursor on the row that wants you, `esc` widens again |
| `tab` | the next project, without going back to the list; it cycles, and with no project selected it opens the first |
| `N` | name the session under the cursor, so it reads by your name everywhere. `ctrl+u` then `enter` removes the name; `esc` cancels. Names live in `projects.toml` beside the project rules |
| | on the project list, `h`, `p` and `n` work as they do on the dashboard — their subject is the fleet. A key that acts on a session (`i`, `!`, `R`, `K`, `x`, `A`, `C`) says so instead of doing nothing: open a project first |
| | `N` on the project list names the PROJECT, writing a `[[project]]` rule for you. It refuses if the rule would also capture rows outside that project, and says which ones |
| `x` | hide the pane under the cursor, or every selected pane if there is a selection. On a row that is WAITING it says when the mark takes effect, because such a row stays on screen until it stops asking |
| `X` | show the hidden panes too — each marked `[x]`, and the footer says the screen is unfiltered |
| `f` | pin the session under the cursor, or the PROJECT under the cursor on the project list. Pinned rows carry `★` and sit above everything else |
| `v` | switch the inbox between **per-host** and **per-project** grouping; the header names the view while it is not the default |
| `a` | go to it — a jump on this server, a window for another (§20). On a session that has NO pane, it MAKES one: a tmux session running `claude attach <id>`, named after the row (§22) |
| `t` | switch between the filesystem view and the flat dashboard. `--view=flat` picks which one you start on |
| | on the filesystem view every key above that acts on a SESSION acts on the session under the cursor there, and every key that cannot act says what to press instead. `enter` and `l` open a directory, `h` closes it or goes out to its parent, and `n` on a directory opens the launch form with THAT directory already in it. `h` therefore does not open the history here — that stays a dashboard key, one `esc` away |
| `esc` | leave the screen you are on and go back to the dashboard, deciding nothing — and on the dashboard, clear a project filter |
| `q` | quit — on the filesystem view and on the dashboard. Inside a text box it types a `q`, so leave with `esc` first |
| `ctrl+c` | quit, from **every** screen, including the picker and the text boxes |

A send asks for confirmation whenever anything about a target changed since you
selected it — it stopped being an agent, it moved session or window, its server
restarted, its last send was not witnessed, the text came from the history view,
the pane would read a paste as keypresses, or there is more than one target. The
dialog names the act, the payload and the targets; `enter` goes ahead and any
other key cancels.

**A session with no pane still has a door.** Most background Claude sessions are not
running in any tmux pane — they are jobs the daemon holds — so the dashboard lists
them with no pane to attach to. `a` on one of those makes the pane it is missing: the
hub creates a detached tmux session on that row's host named `<name>-<short id>`,
running `claude attach <short id>` inside a shell that outlives it, and then goes
there. Press `a` on it again and you land in the same session rather than a second
one. A session of any other kind has no background id for the verb to take, and `a`
says so and names the fix (`/background`).

Waking a row that is **finished or reaped** asks first, on a screen that lists what it
costs: the turn a reaped session was running was abandoned and a killed turn can still
report success, a process its last tool call started may still be alive, and — measured
— most background sessions are set to reply when they resume, so a wake can pick the
work up again and spend tokens. `enter` wakes it, any other key leaves it alone. A row
that is merely blocked or working is woken immediately: waking a blocked session is how
you answer it.

`a` dispatches over four paths, chosen from what the row and the hub's own
situation are. On the hub's own tmux server it jumps directly (`switch-client` +
`select-window`), leaving the hub running; `C-b L` returns. On another server —
another machine, or a second socket on this one — it opens a new window holding
today's attach command; `C-b` there is consumed by the hub's own tmux, so leaving
that inner tmux takes `C-b C-b d` and a plain `C-b d` would detach you from the
hub instead. In that window the attach runs inside a shell that stays in the
foreground after it on failure, so an attach that fails — a dead ssh master is
the usual reason — leaves its own error on screen, adds the exit status, and waits
for you to press enter instead of closing the window and leaving you with a cheerful
"back from …". On success the window closes silently. When the hub
is not inside tmux, `a` takes the terminal — there is no client to switch and no
session to hold a window.

Both hints assume the default `C-b`. The hub never changes anyone's prefix: a
distinct one would only work for a session the hub itself created, and it normally
runs inside yours.
The hub leaves its own pane out of the list, so it does not render itself.

## What the states mean

| | |
|---|---|
| ⚑ needs | waiting on you |
| ✱ quiet | silent for over 180s |
| ▸ idle | finished, prompt empty |
| · works | producing output |
| ✗ error | failed, or the pane is dead |
| ? unknown | a source reported the session but not its state |
| ✓ done | the job ended — nothing to type into, its output is there to read |
| ✝ gone | the pane vanished; its last screen is kept |

Rows are sorted by how much the pane wants you, in that order. A pane whose host
stops answering stays on the list marked `stale`, keeping its last screen.

## Design

`docs/design.md` is the spec, and §3 is worth reading before changing anything:
it records measurements against live tmux 3.7b and 3.2a where the obvious
implementation is wrong.

The UI review scenes in `docs/ui-mockup.html` and `docs/ui-flows-possession.html` are generated
from the renderer itself and are byte-reproducible, so they are also the regression instrument for
any change to a frame. Their narration is in Russian; the program's own strings are English, and a
test enforces that.

## Contributing

`CONTRIBUTING.md` lists the gates and how to run the interface tests. Security reports go through
`SECURITY.md`.

## License

MIT — see `LICENSE`.
