# How to author a target frame

`docs/ui-mockup.html` holds only bytes the renderer printed, and that rule exists because a mockup
that invents its screens lets us approve a layout the program does not produce. But a *new* feature
has no bytes yet: to review §20's flows before building them, some screens have to be drawn.

This note settles how, by measurement rather than preference. It also names what the measurement
found in the product — those live in `docs/known-issues.md` as M1–M3.

## The benchmark

Two model states nobody had looked at were rendered for real and held out. Three authors then tried
to compose the same two screens blind, each in two modes, and every attempt was diffed against the
real bytes by `prototypes/framediff.py` — no judgement anywhere, because one of the authors was the
person reading the scores.

- **Frame A** — 80×24, the narrow layout: 7 panes over 3 hosts, one host `degraded:format`, two
  panes selected on different hosts, the cursor on a third, a note showing.
- **Frame B** — 190×24, the wide layout: 5 panes over 2 hosts, one dead with exit status 1, two
  stale, no selection, no note.

Authors: **claude** (this session), **codex** (codex-cli 0.147.0, dispatched with no repository
access), **nano-banana** (Gemini image generation, so PNG rather than text).

Modes: **scratch** = the spec's prose and sketch only. **grounded** = the same, plus four real
frames of *other* states copied out of `ui-mockup.html`.

Two controls, both essential:

- **nearest** — paste an unrelated real frame verbatim and change nothing. This is the floor any
  authoring must clear to have added value.
- **blank** — 24 empty lines. This is what catches a metric that rewards absence.

The frames themselves were throwaway and are not in the repository: the ground truth came from a
temporary `TestBenchFrames` beside `internal/ui/mockup_test.go` (same `mockup` tag, reusing
`base`/`agentPane`), which wrote the two `View()` strings to files and printed only their digest so
the answer never reached the author. Reproducing the table means writing that generator again;
`framediff.py` and the numbers below are what was kept.

All text authors were also told each frame's line count and widest line (24 lines; 72 and 101
columns). That disclosure was material — it reveals that frame B has one tile column rather than a
grid — so it was given to everyone.

## Results

Rank by `whole` (similarity of the entire screen). `exact` is lines identical at the same index.

**Frame A** — truth 24 lines, widest 72 columns:

| candidate | lines | wide | exact | exact~ | line~ | whole | tok | inv |
|---|---|---|---|---|---|---|---|---|
| claude · grounded | 24 | 72 | **20** | 20 | 0.925 | **0.961** | 1.000 | 4 |
| banana · generate ¹ | 17 | 60 | 11 | 11 | 0.716 | 0.793 | 1.000 | 4 |
| banana · edit a real screenshot ¹ | 22 | 59 | 5 | 5 | 0.397 | 0.760 | 1.000 | 4 |
| codex · grounded | 24 | 72 | 12 | 12 | 0.763 | 0.665 | 1.000 | 4 |
| claude · scratch | 23 | 70 | 0 | 0 | 0.138 | 0.447 | 0.656 | 11 |
| *control: nearest real frame* | 24 | 72 | 11 | 11 | 0.722 | *0.314* | 0.281 | 23 |
| codex · scratch | 24 | 72 | 3 | 3 | 0.251 | 0.075 | 0.562 | 20 |
| *control: blank* | 1 | 0 | *11* | *11* | 0.458 | *0.000* | 0.000 | 0 |

**Frame B** — truth 24 lines, widest 101 columns:

| candidate | lines | wide | exact | exact~ | line~ | whole | tok | inv |
|---|---|---|---|---|---|---|---|---|
| claude · grounded | 24 | 101 | **24** | 24 | 1.000 | **1.000** | 1.000 | 0 |
| *control: nearest real frame* | 24 | 101 | 14 | 14 | 0.869 | *0.716* | 0.516 | 18 |
| codex · grounded | 23 | 101 | 5 | **21** | 0.329 | 0.713 | 0.968 | 1 |
| claude · scratch | 24 | 100 | 0 | 0 | 0.472 | 0.221 | 0.677 | 41 |
| codex · scratch | 24 | 101 | 0 | 9 | 0.272 | 0.173 | 0.581 | 13 |
| *control: blank* | 1 | 0 | 0 | 15 | 0.000 | 0.000 | 0.000 | 0 |

¹ The image arms are **not comparable** to the text arms. A PNG cannot be diffed, so both were
transcribed back to text by hand — and the transcriber knew the real format, which flatters them.
Box widths are not recoverable from pixels at all. Their numbers are printed to show the shape of
the failure, not to rank them.

## What it decided

**Grounding beats authorship, by a lot.** Same author, same state, only the reference frames added:
0.447 → 0.961 on A, 0.221 → 1.000 on B. The gap between the two *models* in the same mode
(0.961 vs 0.665) is smaller than the gap between the two *modes* for either model.

**The nearest-frame control is the argument.** On frame B, pasting a real frame of a different state
and changing nothing scores 0.716 — higher than every from-spec attempt by a factor of 3.2, and
level with a grounded author. Layout skeleton is most of a screen; content is the small part. On the
simpler frame A the control is beaten by one of the two scratch arms (0.447) and beats the other
(0.075), so the claim is bounded: **on the complex layout, a real frame with the wrong content is a
better starting point than a drawn frame with the right content.**

**A drawn target invents exactly where no reference covers it.** The grounded frame A missed 4 lines
of 24, and all four are places the four reference frames were silent: the two rows carrying a
selection marker (real is `◆` in column 1; ` ◆⚑ claude` — the guess was `* ⚑ claude`), and the
footer, because none of the references had a note showing. Nothing else diverged. So reference
coverage, not authoring effort, is what bounds fidelity — choose references that contain every
element the new screen needs.

**Generate the frame, do not type it.** Codex's grounded frame B had the content essentially right
(`tok` 0.968, 21 of 24 lines correct ignoring trailing space) and scored 5 of 24 on exact match,
because it did not pad the inbox column to its real width on every row. The winning frame was
emitted by a ten-line script that ljust-ed to 29 and 72. Typing 24 monospace lines by hand does not
converge; padding in code does.

**PNG is the wrong representation, and not because it looks wrong.** Both image arms were legible
and convincing. They were also wrong in the one way that matters: the generate arm drifted a column
(`st/worker  %13` gained a space the others do not have), and the edit arm — handed a real
screenshot and told to change only the text — **invented an eighth pane row**, duplicating `%11`
with a different state, and duplicated a content line inside the tile. A reviewer would have
approved a screen the program cannot produce. Both also invented colour, which the real renderer
emits none of without a TTY.

## The rule

1. A target frame is **seeded from the nearest real frame**, never composed from the spec.
2. It is **emitted by a script** that pads to the real column widths (inbox 28 narrow / 29 wide,
   tile bounded at `MaxTileWidth` 72), not typed.
3. Choose reference frames that **cover every element** the new screen contains. Anything with no
   reference is the part that will be wrong.
4. Each target frame carries the **assertion** it promises — "this frame is wrong unless `View()`
   contains X" — so the implementation plan takes its tests from the picture instead of inventing
   them beside it.
5. Target frames live in a **separate, labelled file**. They never enter `ui-mockup.html`, whose
   whole value is that nothing in it was drawn.
6. Once the feature ships, regenerate the real frames and run
   `python3 prototypes/framediff.py truth.txt target.txt`. A divergence is a defect in the code or
   in the approved target; either way it is not a matter of opinion.
   **But read the diff by classifying every changed line, never by asking whether it is empty** —
   neither generated document is reproducible today, and a regeneration with no product change
   moves 22 lines of `ui-flows-possession.html` (the git branch, which the captured shell prompt
   renders, and the tmux status clock) plus the mockup's history timestamps, which are relative to
   `time.Now()`. A frame must be a function of the product alone; until it is, an always-dirty diff
   is the condition under which a real divergence gets waved through. Carried as known-issues C2,
   with the fix.
7. Never PNG.
8. **The assertion has to be checked somewhere it can FAIL.** A generator that both builds a frame
   and verifies it, behind a build tag *and* an env var, verifies nothing: `t.Skip` reports **PASS**,
   so `ui-flows-possession.html`'s fourteen assertions ran in no gate for the document's whole life
   while its banner said every frame was checked. Split by what a frame NEEDS — a frame built from
   `View()` needs only the product, so it is checked in the **default** suite (`go test ./...`,
   `TestFlowFramesAssertWhatTheyShow`); only `capture-pane` frames and the file write stay behind
   the tag. Both callers must build the frames from **one** function, or the published document
   drifts from the checked one. And the checker must assert a **floor** on how many frames it
   checked: a count that starts at zero satisfies any assertion about frames, including one that
   stopped looking.
   `ui-mockup.html` follows the same split as of the picker's five frames: `shot`, `scene` and the
   builder live in `internal/ui/mockup_frames_test.go` with no build tag, each frame carries the
   substrings it promises, and `TestMockupFramesAssertWhatTheyShow` checks them with a floor of 34.
   The types had to leave the tagged generator for the builder to be reachable at all — a `scene`
   declared under `//go:build mockup` puts every frame that returns one out of the default suite's
   reach, which is the same move `fixtures_test.go` already records.
   **And "can it fail?" has to be answered against the PRODUCT, which took two gates rather than
   one.** Calibrating the promise check found the hole: editing a count line and a remedy straight
   into `docs/ui-mockup.html` left it green, because it builds frames from `View()` and never reads
   the file — so the *document* being wrong, which is the failure that matters for a file served at
   a public URL, had nothing to answer to. `TestThePublishedMockupHoldsTheFramesTheProductPrints`
   is the second gate: every line of every picker frame must appear in the published bytes. Both
   directions are now measured — mutate the product (`box()` giving an unusable host a tick,
   `pickerCount` dropping its timeout tally) and the promise check reddens; mutate the document and
   the drift check reddens. A frame test that can only go green is this repo's signature defect.
9. **A frame's state has to be one production can reach.** `ui-flows-possession.html` carried a
   frame captioned "the hub is not inside tmux" that was built with `$TMUX` set and
   `model.selfSession` empty — and with `$TMUX` set, `hub.SelfSessionID()` answers `$0` and never
   `""`. Its assertion passed *because of* the contradiction: outside tmux there is no hint at all,
   since `hintFor` returns `""` when not nested. So state the frame's preconditions the way the
   product produces them, and when what a frame shows is an **absence**, pair the negative with a
   positive discriminator — an empty screen satisfies every absence.

## Two traps in the method itself

**Rank by `whole`, never by `exact`.** A blank 24-line file scored **11 of 24** `exact` on frame A:
short candidates are padded to the truth's length and collect a free match on every blank row the
truth has. This is the same shape as a check that passes because it looked at nothing — which is
why the blank control is not decoration.

**A file existing is not a file finished.** The first scoring pass was taken as soon as the codex
candidates appeared on disk, and it reported one of them as 192 columns wide when the finished file
is 101 — the agent was still writing. Poll for a **stable digest**, not for existence, before
quoting any number derived from another process's output.
