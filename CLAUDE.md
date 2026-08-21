# tmux-hub

A TUI over local and remote tmux servers. `docs/design.md` is the spec and the authority;
`docs/known-issues.md` holds what is deliberately open, with the ruling for each.

## Hard rules, each paid for once

- **Never run `tmux` without an explicit `-L` or `-S`.** A bare `tmux` command reaches the
  operator's own server. Tests and probes use a private socket and kill only what they created.
- **Never emit `#{client_activity}` or `#{client_created}`** in any format string. Both segfault
  tmux 3.2a, which is what half this fleet runs. `internal/tmux`'s `Validate` is the guard; do not
  route around it with a raw `exec.Command`.
- **Production UI strings are English**, and `internal/ui/english_test.go` is the guard: it parses
  every non-test file in that package and fails on a non-Latin string LITERAL. Comments and test
  fixtures are deliberately out of scope — a hostile Cyrillic directory name is DATA two tests are
  built from.
- **`docs/design.md` and `docs/known-issues.md` are GUARDED, and the guard is `internal/ui/docs_test.go`.**
  Seven mechanical checks, each earned by damage found by hand: no unapplied edit directive or TODO left in
  prose, no heading repeated in one document, every table well formed (no paragraph spliced between rows, and
  every row's cell count matching its header — one row had lost its closing pipe), every code span closed
  (parity outside fenced blocks; a span WRAPPED across a line is legal CommonMark and this document does it
  42 times, so a per-line rule would accuse the document of its own convention), every `§N` and `§N.M`
  resolving to a heading or to an item of that section's numbered list, every `file:line` citation pointing
  inside its file, and no refuted figure surviving outside §22 which quotes them as withdrawn. Calibrated
  both ways: green on the real documents, and all eight injected defects caught by the intended check. When
  a figure is re-measured, add the old string to that last check rather than trusting a grep.
- **A PERSISTED MARK IS KEYED ON THE CLAUDE UUID, and `registry.Pane.Claude` is the one place that says
  which field carries it.** Two surfaces have paid for this — `internal/fav` (a pin) and
  `internal/project` (an alias) — because the product renames the thing under the operator: the door
  makes a tmux session called `<name>-<short id>` and the join folds the pane-less row into that pane
  (agent → pane), while the shared-store dedup moves a row's Host. Any key made of `(kind, host, name)`
  comes off at both. The field differs BY KIND — an agent row's `SessionID` is the uuid, a pane row's is
  tmux's `$3` and its uuid is `ClaudeSession` — so a third surface must call `Claude()` rather than
  reach for a field. Where there is no uuid, `(kind, host, name)` is correct and the same name on two
  hosts is two sessions; the two rules answer different questions.
- **Verify the COMMIT, never the working tree.** `git archive HEAD | tar -x -C <scratch>` then run
  the gates there. `go test` compiles the tree, so uncommitted files can carry a green suite.
- **Count `ok` lines against the package count** (22 today — `internal/project` and `internal/conf` were added when the projects screen landed and when the
  config dialect was extracted so `projects.toml` could not grow a second parser; `internal/configdir`
  when `hosts.toml` moved out of the state directory; `internal/fav` when favourites landed, kept
  apart from `hide` because the two fail in OPPOSITE directions — an unreadable hidden set must leave
  everything visible, an unreadable favourites set must leave the ordinary order; `internal/fleet`, `internal/fleetcache` and
  `harness/gen` when the fleet became a graph — `internal/fleetcache` is the one a stale figure here hid,
  which is why the next line says to take the number from `go list`). A package that failed to build is not
  reported as a failure — it is not reported at all. **Take the number from `go list ./... | wc -l`
  rather than from this line** — it counts what the DEFAULT gate can see, so `internal/e2e` is absent
  from it (build tag), and a stale figure here reads as a package that failed to build.
- **`go vet -tags e2e ./internal/e2e/` AND `go vet -tags mockup ./internal/ui/` beside `go vet ./...`.**
  Both are behind build tags, so the default gates cannot see them and a signature change stays broken
  silently. The mockup tag also GENERATES `docs/ui-mockup.html`, so any change to production copy needs
  `go test -tags mockup -run TestGenerateMockup ./internal/ui/` to republish it — and a diff of the
  generator itself first, because its scene titles are Russian prose that a blanket replace will eat.
- **The mockup is BYTE-REPRODUCIBLE, so regenerating it is a refactor check.** Every scene is stamped
  at one fixed instant (`mockupNow`); with `time.Now()` each run differed in six timestamp lines and
  the check could not tell "nothing changed" from "something did". To prove a refactor moved no frame:
  generate from a `git archive HEAD` tree with only the generator copied over, generate from the
  working tree, and `diff` the two documents. That is how the `Frame` refactor was verified — and
  count the `ok` lines, because a broken build prints "identical" for the wrong reason.
- **The INTERFACE has its own tests now, and they are the only ones that run the binary.**
  `internal/e2e/tui_test.go` starts the real hub in a pane on a private tmux socket, drives it with
  `send-keys` and reads it with `capture-pane`. Everything else in `internal/ui` drives `m.Update()`
  and `View()` in process, which covers the model and the renderer and cannot see the bubbletea
  runtime, the terminal size, a real keystroke or the quit path — the first run of the tmux harness
  found a shipped defect for exactly that reason. When adding to it: **wait for the signal your
  assertion is about.** The header paints before any poll (§16 promises that), the tmux fleet arrives
  on the first tick, and agent rows arrive 0.5–2.8 s later from a different producer; waiting for the
  wrong one turns a working product into a failure or a `t.Skip`. Give every skip a poll loop, and
  read the WHOLE capture — a form can paint below the fold.
- **A frame test asserts on the string `View()` returns**, and must fail against a stub — a screen
  that is defined, tested and never called is this repo's signature defect. `t.Skip` reports PASS.
- **Calling a mutating method on a shared structure from bubbletea's Update goroutine needs a lock,
  not care.** `tea.Cmd` bodies run concurrently with `Update`, so anything reachable from both is
  shared. This shipped once: `Poller.Add` appended to `p.hosts` unguarded while a tick held
  `&p.hosts[i]`, and `growslice` then reallocated so the tick's status writes landed in the abandoned
  array and were silently discarded — hosts snapping back to `connecting`. `go test -race ./...` was
  16/16 green throughout, because no test called `Add` against a live `Tick`. **A `-race` gate proves
  only what the tests actually run concurrently**, so a new cross-goroutine caller needs its own
  concurrent test in the same commit — and that test must **assert the value, not the silence**:
  hold the reader open, mutate, release, then require the reader's write to have SURVIVED. A test
  that only asks whether `-race` stayed quiet passes against a copy-on-write fix that still discards
  the write, which is the one outcome worse than the bug, because it removes the evidence and keeps
  the symptom. The shape that works here: no goroutine ever holds a pointer into shared state —
  snapshot under the lock, work on a private copy, merge back **by key and by field**, never by index
  (the set can change mid-read, and an index write-back lands one host's result on another).

## Tooling in this environment

- `rtk` (0.42.0) rewrites bare local commands, including `go test` output. Use `rtk proxy go test …`
  whenever a script parses the output, and judge by the exit code. **Quote `-run` patterns** — an
  unquoted `|` segfaults rtk and drops a core file.
- **Never build a command's arguments by interpolating one string: zsh does NOT word-split `$var`.**
  `for t in "-tags e2e ./internal/e2e/"; do go vet $t; done` passes that whole string as argv[1] and
  `go vet` answers rc=2, which reads as a broken gate. Write the arguments literally, or use an array
  (`t=(-tags e2e ./internal/e2e/); go vet $t[@]`). This bit three times in one session — twice on gate
  loops, once on a `claude` probe where the single argument was taken as a PROMPT and started a real
  session. The tell for the last one is rc=124 and a large debug artefact where a fast rc=1 was expected.
- Several agents may share this checkout, and **`git commit` commits the shared INDEX, so
  `git add <paths>` protects nothing.** Put the pathspec on the COMMIT:
  `git commit -m "msg" -- path/one.go path/two.go`. Measured in a throwaway repo: that form takes
  only those paths and leaves a neighbour's staged entry in the index, where a bare `git commit`
  after the same explicit `add` takes everything staged. Two consequences worth knowing:
  - With a pathspec, commit takes the **worktree** content of those paths, not the staged content
    (measured: index `v3`, worktree `v4`, commit held `v4`). So `git write-tree` on the shared index
    is not what you are about to commit. Verify a prospective tree through a PRIVATE index —
    `GIT_INDEX_FILE=$(mktemp) git read-tree HEAD`, then `git add -- <paths>`, then `git write-tree` —
    built from the same paths you pass to `git commit --`, or the check compares two different trees.
  - **A NEW file still needs `git add` first** — measured: `git commit -m … -- newfile.go` dies with
    `pathspec 'newfile.go' did not match any file(s) known to git`, because a pathspec can only name
    paths git already knows. So the window reopens, one file wide, for the instant between the `add`
    and the commit. Put them back to back, and name that file in the pathspec too.
  - **Read `git show --stat` after every commit** and compare it against what you meant to send. This
    failure is silent in both directions — the commit succeeds and the suite stays green — so a stat
    naming a file you never opened is the only evidence there is. It has happened once on this repo:
    a UI fix commit carries an unrelated new package.
- Every commit here carries the same `user.name`, so `%an` and `git blame` cannot tell two agents
  apart. Ownership is answerable only from a commit's CONTENTS. Do not infer it from a subject line.
- A plan that runs tasks concurrently must give each contested file one owner, in the task's
  `**Files:**` block, checked across every pair of tasks that overlap in time.

@.lessons.md
