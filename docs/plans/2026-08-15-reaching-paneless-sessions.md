# Reaching a Session That Has No Pane — Implementation Plan

> **SUPERSEDED, 2026-08-16, by `docs/plans/2026-08-16-paneless-producer.md`.** This was a hand-written
> draft, written before the spec's scope check ran. That check found §22 to be 814 lines against §21's 433
> and §20's 397 — about twice the largest piece this project has taken in one approval — so it split, and
> the successor plan covers the half that rests on nothing open: the producer, the row key, `K`'s refusal
> and the ordering. Two things the successor does differently, both because verifying it found them:
> this draft's wake confirmation is **dropped** (there is nothing to confirm until `a` acts on a pane-less
> row, and `internal/ui/attach.go:36` still refuses every agent row), and its `K` task was written against
> the row under the cursor when `confirmKill` acts on the SELECTION. Kept for the reasoning in its Global
> Constraints, which the successor copies forward.

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `a` reaches every session the hub lists, not only the ones that happen to own a tmux pane — and the sessions it cannot reach say why in a sentence the operator can act on, because the reason is a property of the world rather than a gap in the hub.

**Architecture:** The other program already owns the hard part. `claude attach <id>` revives a background session whose worker is dead, so the hub does not build possession or respawn for pane-less rows — it creates a pane on the row's own host, runs that verb inside it, and hands over the terminal exactly as §20 already does for a pane row. Two producers feed one registry (§17); this plan fixes what the producer was hiding, gives the row's tile a sentence out of the session's own state file, and turns `a` into one path with two doors and one honest refusal.

**Tech Stack:** Go 1.x, bubbletea, OpenSSH control masters, tmux 3.2a–3.7b, `claude` 2.1.224–2.1.233.

**Spec:** `docs/design.md` §22, added in the same series as this plan. It extends §17 (the second producer, whose operational rules were written from one machine and are corrected here), reuses §20 (possession — `a` is already a dispatcher), and must not break §16 (the timing commitments).

## Global Constraints

Every value here was measured on **2026-08-15** against the real fleet: this machine running `claude` **2.1.233** and `nuc` running **2.1.224** with tmux **3.2a** — a nine-release spread, which is the point. Do not re-derive them and do not soften them. Where a value is a snapshot of a live fleet it says so, because the fleet drifts between probes (the interactive row count moved 6 → 5 inside one session).

- **Measure every `claude` fact on 2.1.224, the oldest version in the fleet, and take the contrast in the same invocation.** A version that does not know a flag is not distinguishable from one that silently ignores it unless the run also shows the flag changing the answer. Applied here: `agents --json --all` is accepted on 2.1.224 (rc=0, empty stderr) **and** moves that host's count from 11 to 14, so it is supported rather than tolerated. This project has already shipped one format change measured on one version only.
- **`claude attach <id>`, `claude logs <id>` and `claude stop <id>` exist on both versions with identical usage lines.** The door is portable across the fleet's actual spread; nothing here needs a capability gate.
- **`kind=background` ⇔ the listing carries `id`.** Measured 32/32 background rows carry it (25 here, 7 on `nuc`) and 0/6 interactive rows do. `claude attach` takes an id, so an interactive session has no argument to pass and therefore no door — that is a MECHANISM, not a policy, and Task 5 pins it with a test rather than a comment.
- **The listing is authoritative for state; `state.json` is authoritative for the SENTENCE.** They agree on 24 of 25 rows, and the single disagreement is not staleness: job `77ef6f5e` had `state.json` 0.3 minutes old saying `blocked` while the listing said `working`, with `inFlight.tasks=5`. The two fields mean different things — the work is blocked on something external while the daemon is computing. Read state from the listing. Read `detail` from the file.
- **The tile's sentence is `detail`, never `needs`.** Census over 32 state files: `detail` non-empty 31/32, `needs` non-empty **6/32**. A tile keyed on `needs` is blank in 26 of 32 cases.
- **`tempo` adds nothing.** Over 25 rows it is a deterministic projection of the listing state: `done`→`idle`, `failed`→`idle`, `blocked`→`blocked`, `working`→`active`. Do not introduce a fourth vocabulary.
- **`blocked` in the listing is NOT evidence that a question is waiting.** `block.questions` is present on 1 of the 6 rows the listing calls `blocked`. A row's tile may not claim someone is being asked something unless `block` is there.
- **Liveness comes from roster membership and from nothing else.** `pid` is **absent** from all 25 state files, and the listing's own `pid` is non-null on only 3 of 25 background rows — including none of the 7 rows the listing calls `working` or `blocked`. A row exists because the listing names it.
- **Always `agents --json --all`.** Bare `--json` hides every terminal row: snapshot 13 rows against 31, and the 17 it omits are `done` 13 and `failed` 4. The actionable half is `failed`, and it is invisible on the main screen today.
- **Nothing is filtered: `--all` is taken WHOLE.** The operator's first ruling dropped `done` and `completed` to control noise and was replaced the same day by a better instrument for the same problem — order by last activity, newest first, so old rows sink on their own. Filtering removes an instance; ordering removes the class. A settled session therefore stays reachable by pressing `a`, which the filter had taken away.
- **`agents.Attention()` does not separate a finished row from a live one, and nothing may assume it does.** It folds `done`, `completed` **and** `idle` into `idle` (`internal/agents/agents.go:56-67`), and `idle` is 2.1.224's word for a LIVE interactive session — measured on 2.1.233 too, so this is not a version hedge. With `done` now shown, the consequence lands in the RANK rather than in a filter: `state.FromWord(s.Attention())` (`internal/registry/registry.go:278`) gives a finished background row and a live interactive session the same rank, and only the recency tiebreak separates them.
- **`failed` is shown and never dismissible.** All 4 rows carrying `reapedMidWorkAt` report `failed` in both the listing and the file, 4/4, so this word is how a reaped-mid-work row surfaces at all — §22.5's rule that a reaped departure is announced rather than inferred depends on it. The operator ruled there is no dismissal, so such a row stays until its session is stopped or woken: 4 rows of 31 on the measured fleet.
- **`--all` makes a stated residue live, so the registry key gains `Kind` in the same commit.** Mirrored over the real listing: bare → 13 rows / 13 distinct `agentRowID`, 0 collisions; `--all` → 31 rows / **30** distinct keys, one row lost silently; `--all` minus `done` → 17 / 17, 0. The colliding pair is a `background`/`done` row and an `interactive` continuation of the same conversation in the same `cwd`, and `Kind` distinguishes them. Filtering `done` removes this instance and not the class: a `failed` row — which this plan deliberately admits — can have the same twin.
- **A bad id refuses identically everywhere: rc=1 and `No job matching '<id>'. Run 'claude agents' to list running sessions.`** Measured for all three verbs on both versions. Do not match on the older wording `job not found — it may have already exited`; it no longer exists. Better: the hub knows the row's name and the operator is already looking at `claude agents`' output, so the hub's own refusal must name the row rather than forward this sentence.
- **`agents` REJECTS `--debug-file`; `logs` ACCEPTS it.** Measured 6/6 runs: `claude agents --json --all --debug-file <path>` answers rc=1 in ~165 ms with `error: unknown option '--debug-file'` and writes nothing, on both versions. `claude logs <id> --debug-file <path>` writes the file even for a bogus id (620 B here, 278 B on `nuc`). So the listing must be judged by exit code alone, and `logs` is the one reading that can be instrumented — the opposite of what the design draft carried.
- **Bare `claude agents --json --all` costs 222 ms locally**, so the separate 20 s timer §17 gave it stays. `--all` does not change the order of magnitude.
- **Never hand `claude` an argument list built by interpolating one string.** A bare unrecognised argument is a PROMPT, not an error: `claude "agents --json --all"` starts a real session, burns tokens, and only stops on a timeout. Go's `exec.Command` passes argv element by element and is safe; any shell helper in a test or probe must too, and zsh does **not** word-split an unquoted `$var` the way bash does.
- **`#{client_activity}` and `#{client_created}` are forbidden in every format string** — both segfault tmux 3.2a, which is what `nuc` runs. `internal/tmux`'s `Validate` is the guard.
- **Never run `tmux` without an explicit `-L` or `-S`.** Tests and probes use a private socket and kill only what they created. The remote e2e case already does this correctly: `-L hube2e` on the TARGET, no `kill-server`, and the private server dies with its own `sleep 300`.
- **Validate the tmux arguments, THEN wrap them for ssh**, and shell-quote per element at BOTH levels — the remote path has two shells. §20's window path and §22's attach payload compose through the same rule.
- **Production UI strings are English**, and `internal/ui/english_test.go` parses every production file to enforce it.
- **Every test must fail against the unmodified product**; show red, then green. A test whose fixture hand-builds the struct cannot see a producer that forgets a field — this repository has paid for that three times, so a field with two producers gets a test per producer that goes THROUGH the producer.
- Run tests with `rtk proxy go test …`, and quote any `-run` pattern — an unquoted `|` segfaults the rtk proxy and drops a core file.
- **Gates, and the SKIP column is not optional.** `gofmt -l .` silent; `go vet ./...`, `go vet -tags mockup ./internal/ui/`, `go vet -tags e2e ./internal/e2e/` all clean; `go test -count=1 -race ./...` reporting **18** `ok` with 0 `FAIL` and 0 `no test files`; `go test -count=1 -tags e2e ./internal/e2e/` ok. Verify the COMMIT via `git archive HEAD | tar -x -C <scratch>`, never the working tree.
- **The e2e gate must run with `HUB_E2E_HOST=nuc`.** Without it the only remote case skips — baseline on `a4a08e2` is `rc=0 PASS=53 FAIL=0 SKIP=1`, and that one skip is the whole remote path. §22 is a remote feature, so a gate that does not set the variable has not tested it. With it: rc=0, the case passes in 3.94 s, and nothing is left on the far host.
- **Any change to production copy needs `go test -tags mockup -run TestGenerateMockup ./internal/ui/`** to republish `docs/ui-mockup.html`, which is byte-reproducible and therefore also the refactor instrument. Diff the generator first: its scene titles are Russian prose that a blanket replace will eat.
- **`docs/` is served publicly**, so a frame or a document may not name a private host. `nuc` is the one sanctioned exception; do not add another.
- Commit with the pathspec on the COMMIT (`git commit -m … -- path/one.go`), `git add` any NEW file first and name it in the pathspec, and read `git show --stat` afterwards against what you meant to send.
- Commit messages: lowercase conventional prefix, **no AI co-authorship trailers of any kind**.
- One owner per contested file, declared in each task's `**Files:**` block and checked across every pair of tasks that could run at the same time.

## What this plan drops, and why that is the point

The hub does not learn to drive a pane-less session. It learns to open the door the other program already has.

| dropped | what it was for | why it goes |
|---|---|---|
| possession for background rows | typing into a session with no pane | `claude attach` revives a dead worker; a hand-built equivalent would be worse and would have to be maintained against a moving CLI |
| respawn / continuity for background rows | restarting a session the hub can see | same door, same verb |
| `claude logs` as a pre-wake explanation | telling the operator what a dead row was doing before they wake it | the row's own `state.json` carries `detail` for 31 of 32 rows without a second process; `logs` stays available for a live row |
| gating `a` on `reapedMidWorkAt` | refusing to wake a row that was killed mid-turn | 4/4 such rows report `failed`, which is already on screen; the operator decides |
| matching on `job not found — …` | detecting a vanished job from the verb's text | the wording changed between versions; rc=1 plus the hub's own roster is the durable signal |
| `--debug-file` on every `claude` exec | instrumenting a silent failure | `agents` rejects the flag outright, so this was never possible for the reading that matters |
| treating the absence of a row as completion | inferring "done" from a listing that no longer names it | true only with `--all`; without it a reaped row leaves for the wrong reason |

## File structure

| file | responsibility |
|---|---|
| `internal/agents/agents.go` (modify) | both fetchers carry `--all`; one named predicate drops terminal rows by the literal word |
| `internal/agents/jobstate.go` (create) | read one session's `state.json` → the sentence and the block, local and over ssh, with an absent file a normal answer rather than an error |
| `internal/registry/registry.go` (modify) | `Kind` joins the agent row key; the row carries the sentence |
| `internal/ui/attach.go` (modify) | `a` becomes one path: a pane row attaches, a background row gets a pane on its own host running `claude attach <id>`, an interactive row is refused by mechanism |
| `internal/ui/model.go` (modify) | the confirmation, which names every cost and is not asked when nothing is spent |
| `internal/ui/render.go` (modify) | the tile's sentence for a pane-less row |
| `internal/e2e/paneless_test.go` (create) | the remote leg, against a real host, gated on `HUB_E2E_HOST` |
| `docs/design.md` (modify) | §22 becomes a section, and the 77 forward references stop pointing at nothing |
| `docs/known-issues.md` (modify) | close what §22 closes; leave N3 open with its ruling |
| `docs/plans/README.md` (modify) | the index gains this plan and stops calling a merged plan "next" |

---

### Task 1: The listing stops hiding more than half of itself

The producer calls bare `claude agents --json`, which omits every terminal row — a snapshot 13 against 31 — so a background job that FAILED is invisible on the main screen. The fix is one argument, on both fetchers, and **no predicate**: the operator's ordering ruling replaced the filter, so `--all` is taken whole.

That makes this task smaller than it was drafted and Task 2 larger: with nothing filtered the key collision is live, so the two land in one commit.

**Files:**
- Modify: `internal/agents/agents.go`
- Test: `internal/agents/agents_test.go`

**Interfaces:**
- Changes: `Local` and `OverSSH` argv both carry `--all`. No filtering function is added — an earlier draft of this plan specified `Unfinished(ss []Session)`, and it is withdrawn rather than deleted so a reader meeting the ordering rule can tell that hiding was considered and why it lost.

- [ ] **Step 1: Write the failing tests**

```go
// The flag is on BOTH fetchers, and the remote one is the half a local-only test cannot see.
// `--all` is accepted on 2.1.224 as well as 2.1.233, so this needs no version gate; and it is
// not cosmetic — bare `--json` hid 17 of 31 rows on the measured fleet, every one of them
// terminal, four of them `failed`.
func TestBothFetchersAskForEverySession(t *testing.T) {
	local, ok := Local(time.Second).(*cmdFetcher)
	if !ok {
		t.Fatal("Local is no longer a *cmdFetcher; this test must be rewritten, not deleted")
	}
	if !slices.Contains(local.args, "--all") {
		t.Errorf("Local omits --all: %q — bare --json hides every terminal row", local.args)
	}
	remote, ok := OverSSH("/tmp/ctl", "host", time.Second).(*cmdFetcher)
	if !ok {
		t.Fatal("OverSSH is no longer a *cmdFetcher; this test must be rewritten, not deleted")
	}
	payload := strings.Join(remote.args, " ")
	if !strings.Contains(payload, "--all") {
		t.Errorf("OverSSH omits --all: %q", payload)
	}
}

// A CENSUS rather than a list of branches, because the vocabulary is a version pair and every
// word must survive now that nothing is filtered. The row that matters most is `failed`: it is
// the only word a reaped-mid-work session shows (4/4 measured), so losing it loses the case
// §22.5 exists to announce. Built as a JSON literal, the shape `agents_test.go` already uses —
// a fixture that hand-builds []Session never goes through the parser.
func TestEveryReportedWordSurvivesTheProducer(t *testing.T) {
	words := []string{"done", "completed", "failed", "blocked", "working", "busy", "idle", ""}
	var b strings.Builder
	b.WriteString("[")
	for i, w := range words {
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, `{"sessionId":"aaaa%04d-bbbb-cccc-dddd-eeeeffff0000","kind":"background","state":%q}`, i, w)
	}
	b.WriteString("]")

	got, err := Parse([]byte(b.String()))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(got) != len(words) {
		t.Fatalf("%d rows in, %d out — the producer drops a word", len(words), len(got))
	}
	seen := map[string]bool{}
	for _, s := range got {
		seen[s.State] = true
	}
	for _, w := range words {
		if !seen[w] {
			t.Errorf("state %q did not survive Parse", w)
		}
	}
}
```

- [ ] **Step 2: Implement**

`--all` on both fetchers, and nothing else. `Parse` keeps reporting the listing's own population truthfully, which is what makes "a session leaving the listing is done with" true — and with `--all` that sentence is finally sound, because bare `--json` let a row leave for the wrong reason.

- [ ] **Step 3: Confirm no caller filters**

`internal/hub/poll.go:393` hands `ss` straight to `UpdateAgents` and must keep doing so. Grep the tree for any population filter over `Attention()` and record the grep in the commit: the trap is real (`done`, `completed` and `idle` share one output) even though this task no longer walks into it.

---

### Task 2: The registry key admits Kind, so one conversation cannot eat the other

**Files:**
- Modify: `internal/registry/registry.go`
- Test: `internal/registry/registry_test.go`

**Interfaces:**
- Changes: `agentRowID` includes `Kind`

`registry.go`'s own comment states the residue: "two sessions sharing an id AND a cwd would still merge. That does not occur on this fleet." With `--all` it does — 31 rows collapse to 30 keys and one row disappears with no error anywhere. The pair is a `background` row and an `interactive` continuation of the same conversation in the same directory, and `Kind` separates them for free.

- [ ] **Step 1: Write the failing test**

```go
// count(DISTINCT key), never count(*): a collision keeps the total right and makes one row
// unreachable, which is exactly how this went unnoticed. And the assertion is on the VALUES
// both rows carry, not on the absence of a panic — a copy-on-write "fix" that still discards
// one row would pass a silence check while keeping the symptom.
func TestABackgroundRowAndItsInteractiveContinuationAreTwoRows(t *testing.T) {
	r := New()
	const cwd = "/w/project-hub"
	r.UpdateAgents("local", []agents.Session{
		{SessionID: "3ec21f39-ad9f-4083-91e8-d257cbb22b30", ID: "3ec21f39",
			Kind: "background", Name: "bg half", CWD: cwd, State: "failed"},
		// No id, so Parse back-fills it from SessionID[:8] — the same string. Same cwd. Only
		// Kind differs, and this pair was on the real fleet.
		{SessionID: "3ec21f39-ad9f-4083-91e8-d257cbb22b30", ID: "3ec21f39",
			Kind: "interactive", Name: "interactive half", CWD: cwd, State: "idle"},
	}, time.Now())

	// The agent row's identity lands in Key.PaneID (registry.go:260 —
	// `Key{Host: host, PaneID: agentRowID(s)}`), so PaneID is the key to count.
	rows := r.Panes()
	keys := map[string]bool{}
	byName := map[string]*Pane{}
	for i := range rows {
		keys[rows[i].PaneID] = true
		byName[rows[i].Session] = &rows[i]
	}
	if len(keys) != 2 {
		t.Fatalf("%d distinct keys for two sessions, want 2 — one row is unreachable", len(keys))
	}
	for _, want := range []string{"bg half", "interactive half"} {
		p, ok := byName[want]
		if !ok {
			t.Fatalf("row %q was merged away; rows: %+v", want, rows)
		}
		if p.Path != cwd {
			t.Errorf("row %q lost its path: %q", want, p.Path)
		}
	}
}
```

- [ ] **Step 2: Implement, and say why it is Kind rather than a suffix**

`Kind` joins the hashed part of `agentRowID`. Unique by construction, not repaired on collision: a detect-and-suffix scheme would make a row's identity depend on the order the listing arrived in, and that identity is what a hidden-set entry and an alias are keyed on.

- [ ] **Step 3: Correct the comment that was measured false**

The residue paragraph must state what is now true — the pair exists, `Kind` closes it, and the remaining residue (two sessions sharing id, cwd AND kind) has not been observed. A comment that says "does not occur on this fleet" is a claim with a date, so it gets one.

---

### Task 3: `K` stops pretending a pane-less row is a pane

**Operator ruling (§22.9 decision 3): the hub does not end Claude sessions.** So this task is a refusal, not a capability — and the refusal is the whole feature, because today the failure is silent in the worst way.

**Files:**
- Modify: `internal/ui/lifecycle.go`
- Test: `internal/ui/lifecycle_test.go`

Measured: `internal/ui/lifecycle.go` contains **zero** occurrences of `KindAgent`, so `confirmKill` offers a pane-less row as a target like any other and `killSelected` (`:150`) issues `kill-pane -t agent:<shortid>@<hash>` (`internal/tmux/lifecycle.go:97`). tmux parses that as session `agent`, window `<shortid>@<hash>`; the failure lands in `killMsg.failed` with no reason shown. The operator sees a kill that did nothing and is told nothing.

- [ ] **Step 1: Write the failing test**

The guard goes **before** the confirmation, so the dialog never offers a target the hub will refuse. Assert both halves: that `confirmKill` on an agent row does not enter the confirm mode, and that the note names the command. A test that only checks the mode passes against a version that confirms and then fails silently.

```go
func TestKOnAPaneLessRowRefusesAndNamesTheCommand(t *testing.T) {
	m := base(t, 100, 24)
	m.panes = []registry.Pane{{Kind: registry.KindAgent, Host: "nuc",
		Session: "deploy-audit", SessionID: "ea11c9d3", ClassifiedState: state.Error}}
	got, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'K'}})
	after := got.(model)
	if after.mode == modeConfirm {
		t.Error("K offered a pane-less row for killing; the guard must precede the dialog")
	}
	for _, want := range []string{"no pane to kill", "claude stop", "ea11c9d3", "nuc"} {
		if !strings.Contains(after.note, want) {
			t.Errorf("the refusal does not name %q: %q", want, after.note)
		}
	}
}
```

- [ ] **Step 2: Implement the refusal, with the sentence**

`<name> has no pane to kill, and the hub does not stop Claude sessions — run "claude stop <id>" on <host>; its conversation is kept, and "a" reopens it.`

Every clause is load-bearing: the operator learns why, what to run, where to run it, that nothing is lost, and how to come back. Measured: `claude stop <id>` exists on both 2.1.233 and 2.1.224 with identical usage, so declining it is a policy and the sentence must not imply a missing capability.

- [ ] **Step 3: Calibrate**

Delete the guard and require the test to fail, printing the tmux target the old path built — that string is the evidence the defect was real.

---

### Task 4: the wake confirmation, and it asks only where something is at stake

**Operator ruling (§22.9 decisions 1 and 2): confirm only when the listing's word is `failed`. The host cost is a LINE in that dialog, never a reason to open one.**

That answer leaves a hole the question had already measured, and closing it is part of this task rather than a later thought: an `up-empty` host has no panes, so every Claude row on it is pane-less, and a healthy row there goes straight through with no dialog — so the tmux server the keypress starts on someone else's machine would be announced nowhere. **The note names it after the fact**, which costs no keypress: `made <name> on <host> — <host> had no tmux server, so this started one`. §22.9 marks that clause as the author's rather than the operator's, so it can be overruled in one line.

**Files:**
- Modify: `internal/ui/model.go`, `internal/ui/render.go`
- Test: `internal/ui/confirm_test.go`

Rationale, in the operator's own words: confirming every press trains you to click through the one dialog that matters. Waking a live or settled row is one call; waking a `failed` one abandons the turn it was on.

- [ ] **Step 1: Write the failing tests**

Three cases, and the third is the one a careless implementation gets wrong: a healthy row on a host with no tmux server must go straight through, because the host cost does not gate.

```go
func TestWakeConfirmsOnlyForFailed(t *testing.T) {
	for _, c := range []struct {
		word    string
		hostUp  bool
		confirm bool
	}{
		{"failed", true, true},
		{"blocked", true, false},
		{"working", true, false},
		{"failed", false, true},
		// The host cost is a LINE, not a trigger: an up-empty host does not open a dialog.
		{"blocked", false, false},
	} {
		// … assert mode == modeConfirm iff c.confirm
	}
}
```

- [ ] **Step 2: The dialog names every cost it imposes, and no cost it does not**

Sized from the room LEFT rather than by subtracting from the height: `RenderConfirm` already truncates below ~15 rows while `enter` still sends (known-issues N6), and three more lines move that into ordinary terminals. Count each cost line at 80×24 rather than asking whether the screen contains a phrase — a `Contains` over a whole screen cannot tell a dialog line from a note.

---

### Task 5: a `failed` row is not dismissible, and that is written down

**Operator ruling (§22.9 decision 4): no dismissal.** Nothing is built. The task exists so the next reader does not build it by accident, and so the cost is recorded rather than discovered: under `--all` a `failed` row never leaves the listing, so it stays until the session is stopped or woken. Measured, 4 of 31 rows locally.

**Files:**
- Modify: `docs/known-issues.md` (record it, with the measurement and the ruling)

---

### Task 6: recency orders what attention does not, coarsely

**Operator ruling (§22.9 decision 5): order by last activity, newest first — and therefore `done` rows come back into the list and nothing is filtered.** This is the task that makes Task 1's simplification legitimate.

**Files:**
- Modify: `internal/registry/registry.go` (`SortByAttention` only)
- Test: `internal/registry/sort_test.go`

Three rules, and the second and third are what stop this inverting the tool:

1. **Attention rank stays the primary key.** Recency as the primary key would sink a session that has waited three hours below one that printed a log line, which is the opposite of §1.
2. **Inside the waiting block, the longest wait still comes first** (§21.11.1). That rule points the other way from recency on purpose: it is the order the question "who needs me" is asked in.
3. **Recency replaces the alphabetical `host`/`session`/`paneID` tiebreak for every OTHER rank**, and it is **bucketed to the minute**.

The bucketing is not tidiness. `markActivity` moves on nearly every tick for a working pane — three signals, `HistorySize`, `CursorY` and the classification zone — and `m.cursor` is an INDEX into the on-screen list (`internal/ui/model.go:130`), whose own comment records that a changing list makes that index name a different pane from the one the operator is looking at. A per-second order would move the row under their hand between looking and pressing.

- [ ] **Step 1: Write the failing tests**

Two properties, and the second is the one this task exists to protect:

```go
// Attention still wins. A row that has waited hours must outrank a chatty one.
func TestRecencyNeverOutranksAttention(t *testing.T) { /* needs above works, regardless of activity */ }

// STABILITY: within one minute-bucket the order must not move, however much activity is
// reported. Feed the same set twice with activity advanced by seconds and require an
// identical sequence — the cursor is an index, so a reorder is a mis-aimed keypress.
func TestOrderIsStableWithinAMinute(t *testing.T) { /* … */ }
```

- [ ] **Step 2: Implement, and say what the bucket is for in the comparator's own comment**

The existing comparator already documents why the tiebreak is alphabetical ("what makes the list stable between ticks"). That reason does not disappear — it is being *paid for differently* — so the comment must say so, or the next reader will restore the alphabetical order to fix a jitter that the bucket already fixes.

- [ ] **Step 3: Settle the rank collision this exposes, in §12 and not here**

With `done` shown, `state.FromWord(s.Attention())` (`internal/registry/registry.go:278`) gives a finished background row and a live interactive session the same rank, because `Attention()` folds `done`, `completed` and `idle` together (`internal/agents/agents.go:56-67`). So a live session quiet for an hour sorts below a job that finished a minute ago. Either `done` earns a rank below `idle` or that ordering is accepted deliberately — §12 owns the rank table, so the change belongs there, with its own test.

---

## The structural fix this plan does NOT make, and the reason it is named anyway

`m.cursor` is a position, not an identity. Every reordering and every filter in this product is therefore one comment away from aiming a keypress at the wrong row, and the comment at `internal/ui/model.go:311` exists because that already happened once. Task 6 works around it with a coarse bucket; keying the cursor on the ROW would remove the class. That is a bounded change to one field and its nine readers, it is not §22's, and doing it first would make any future reordering free.

## The scope check says this is two plans, and the seam is here

§22 is **814 lines**. §21 is 433 and §20 is 397, and each of those shipped as one plan. So this section is
about twice the largest piece of work this project has taken in a single plan, which is the question the
spec self-review is supposed to ask and the answer is that it decomposes — along the line this document
already broke at:

| | Tasks 1–6, above | Tasks 7 onwards, below |
|---|---|---|
| what it is | what the hub ALREADY has, made correct | the DOOR itself |
| the work | `--all` on both fetchers, `Kind` in the row key, `K`'s missing guard, the wake confirmation, the no-dismissal ruling recorded, recency in the comparator | `new-session -d` + `claude attach <id>` on the row's own host, the `state.json` reader and the tile's sentence, the interactive refusal, the remote e2e case |
| new subsystems | none — every task edits existing code | a reader for another program's files, and a new dispatch routing |
| depends on an OPEN unknown | **no.** Every value they rest on is measured, on the oldest version in the fleet | **yes, three of the eleven left in §22.10** — most sharply that 22 of 25 ids the hub can put under the cursor are absent from the roster, so "an id the roster dropped" is the majority case rather than an edge |

**So Tasks 1–6 can be approved and started on their own, and nothing about them gets better by waiting.**
They close a live blind spot — a background job that FAILED is invisible on the main screen today — and a
silent row-loss the code's own comment said could not happen. Tasks 7 onwards want three more probes first,
and each is named with the probe that settles it.

Whether that becomes a second plan document or stays the tail of this one is a bookkeeping choice. What the
scope check refuses is treating the two halves as one approval.

## Tasks 7 onwards

The `state.json` reader and the tile's sentence (§22.5), `a` becoming one path with the create on the row's own host (§22.3), the interactive refusal gated on `Kind` rather than on a back-filled id (§22.8), and the remote e2e case. Every operator decision they depend on is now answered, so what remains is ordinary work — written when Tasks 1-6 have landed, so their file ownership can be stated against a tree that exists rather than against a predicted one.
