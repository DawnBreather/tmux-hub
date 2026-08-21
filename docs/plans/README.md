# Plans

One plan per subsystem, each producing working software on its own. The order below
is the user's, and it differs from `docs/design.md` §15: broadcast comes before
transport supervision, because the write path is what makes the tool a conductor and
it works against the existing `--host`.

| plan | covers | status |
|---|---|---|
| `2026-08-11-local-core.md` | §§4–6, §10, §16 — the read-only local dashboard, `status --json` | **complete**, and the work went well past it |
| *(unplanned, done)* | §17's second producer — `claude agents --json` as a source of fact | **complete** |
| `2026-08-12-broadcast.md` | §7 — the write path: stdin payloads, the guarded send, three outcomes, history | **complete**, merged |
| `2026-08-12-lifecycle-and-hiding.md` | §§18–19 — hiding noise, launching, restarting, killing | **complete**, merged |
| `2026-08-12-possession.md` | §20 — `a` becomes a dispatcher; the hub stops taking its own terminal | **complete**, merged, plus two review waves |
| `2026-08-12-transport-supervision.md` | §5, §9 with a forwarded socket | **superseded** by the plan below — a measurement, not a change of mind; see its banner |
| `2026-08-13-hosts-and-transport.md` | §5, §9 — one ssh master per host, no forward; `hosts.toml` and the picker replace `--host` | **complete**, merged |
| *(unplanned, done)* | §21 — projects, aliases, the naming overlay, `projects.toml` | **complete**, 52 commits; the design was folded into §21 rather than written as a plan |
| `2026-08-15-reaching-paneless-sessions.md` | §22 — the first, hand-written draft | **superseded** by the plan below; see its banner for the two defects verifying it found |
| `2026-08-16-paneless-producer.md` | §22's cleared half — `--all` on both fetchers, `Kind` in the row key, `K`'s missing guard, recency in the comparator | **next**; four tasks, verified on both poles: 7 of its 10 tests fail against the unmodified product and all 10 pass with the implementation applied |
| `2026-08-15-design-corrections.md` | the figures §22's re-measurement refuted elsewhere in the document — 66 rows over three documents and four Go files | **complete**, 2026-08-16; applied by quoted text rather than by line number, and the completion check found three rows a head-only comparison had reported as done |
| `polish.md` *(not yet written)* | §8's resize warning, §16's `--watch` | small, after the above |

The transport and broadcast plans were verified the same way, and it is the method
every plan here inherits: every Go block extracted into a copy
of the repository, compiled, run, and then **mutated** — each guarantee removed in
turn, with the sweep required to turn a test red. That found 7 defects in the
transport plan and 6 in the broadcast plan, plus three broadcast guarantees that had
no working test at all. The details are in each plan's Self-Review.

## Why the transport plan was rewritten

The first one forwarded the remote tmux socket so that everything above the socket
path could be the same code as for the local server. The master is required for
`attach` regardless, since a forward cannot carry one at all, so the forward was a
second transport carrying only the polling — and measured with the product's own
batch code against a real host, it carries it 7% slower: **1337 ms** per cycle over
the master against **1432 ms**, four invocations either way.

The latency is not why it was rewritten. Six of the old plan's nine tasks existed
only to keep a forwarded socket honest, and with no local socket file there is
nothing to squat, unlink, stat, or mistake for a server. The `start-server` squatter
that answered rc=0 as the LOCAL tmux — and would have keyed the capability gate to
the wrong version, which was the original incident's precondition — has nowhere to
happen now.

## Why broadcast was first

It is the feature that makes the tool a conductor rather than a viewer, and it does
not depend on the transport work: targets are on-screen panes, which the existing
`--host` already reaches. The argument for delaying it was that writing into a live
agent is where a silent failure costs real work — and that turned out to be the
argument for planning it *carefully*, not for planning it later. Three measured facts
from `docs/design.md` §3 say why the care is needed:

- `send-keys -H` delivered **nothing** at rc=0, so the obvious text primitive never
  worked;
- nothing in the design observed that text had **arrived** — the confirmation fired
  whenever the pane merely resolved, and §7's same-invocation witness turned out to
  be impossible;
- the token guarding a send proved *pane* identity, not *process* identity, so an
  exited agent left a stamped pane that was now a shell.

Each was found only by running the thing. All three are now closed by construction in
the plan rather than left to care at the call site.
