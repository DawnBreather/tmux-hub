# Prototypes

Throwaway renderers used to settle design questions in `../docs/design.md` by
building them rather than reasoning about them. Not part of the eventual Go
implementation — kept because each one produced a measurement the spec cites.

| file | question it settled | finding |
|---|---|---|
| `tile.py` | should a tile show the last N lines, or derived content lines? | the raw tile fails on **every** non-ALT pane: after a command returns, the bottom of a shell pane is a prompt and blank space. On an ALT pane the two renderings are identical |
| `layout.py` | do the width thresholds in §16 hold? | no: at 80x24 per-session header rows ate 5 of 11 body rows, and at 160 cols a full-width tile wasted 100 columns. Both corrected in §16 |
| `views.py` | is the inbox the right primary screen, or is a "next" view better? | keep the inbox: it plus the focused pane's tile already IS the next view with context, and the alternative adds a waiting count while losing the overview (§14) |
| `possession-captures.sh` | does tmux really answer "where am I" loudly enough that §20 needs no UI? | yes, and the captures are in `../docs/ui-flows-possession.html`: the left status segment goes `[hub]` → `[work]` on a same-server jump, and a jump to another server stacks two status lines (`[ag] … "cachyos"` над `[hub] 0:zsh- 1:other*`), so "another session" and "another machine" are distinct without the hub drawing anything. Re-measured the asymmetry `link-window` is banned for: closing the hub's own window left the other server's `pane_pid` unchanged with 0 clients |
| `framediff.py` | can a screen we *draw* for an unbuilt feature be trusted as a review target? | only if it is seeded from a real frame: grounded on four real screens an author hit 24/24 lines on one frame and 20/24 on the other, while the same author working from the spec alone scored 0.22–0.45 similarity — below a control that pasted an unrelated real frame verbatim. Full method and numbers in `../docs/mockup-authoring.md` |

Run:

    python3 tile.py live1                    # renders a live pane both ways, side by side
    python3 layout.py                        # the full layout at 80x24 and 160x32
    ../tmux-hub status | python3 views.py    # inbox vs "next", from live data
    python3 framediff.py truth.txt target.txt [more.txt ...]   # score drawn frames
    ./possession-captures.sh /tmp/flows                        # §20 status lines, private sockets

`tile.py` targets the DEFAULT tmux socket and is read-only by construction
(`capture-pane -p` only). Anything that mutates belongs on a private socket
(`tmux -L <name>`) — see the dev rule in §13 of the design.
