# The Pane-less Producer Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** The dashboard stops hiding more than half of the sessions it is supposed to be an inbox for, stops silently losing one of them to a key collision, stops pretending a pane-less row is a pane when `K` is pressed, and orders what attention does not.

**Architecture:** Four changes to code that already exists, no new subsystem and no new door. The agents producer takes `--all` and filters nothing; the registry row key gains `Kind`, without which `--all` loses a row in silence; `K` grows the `Kind` guard it never had; and `SortByAttention` gains a coarse recency tiebreak so the rows `--all` admits sink as they age instead of being hidden.

**Tech Stack:** Go 1.x, bubbletea, `claude` 2.1.224–2.1.233, tmux 3.2a–3.7b.

**Spec:** `docs/design.md` §22 — specifically §22.6 (the producer's operational rules), §22.11 (the registry key), §22.9 decisions 3 and 4 (`K`'s refusal, and no dismissal for a `failed` row), and §22.6's ordering ruling. Read §22.10 too: it lists what is still unmeasured, and none of it gates this plan.

**Supersedes:** `docs/plans/2026-08-15-reaching-paneless-sessions.md`, a hand-written draft covering the same ground plus the door. That draft numbered the wake confirmation as its Task 4; **this plan drops it**, because there is nothing to confirm until `a` can act on a pane-less row and `internal/ui/attach.go:36` still refuses every agent row. The confirmation belongs to the door plan, and saying so is the honest half of the scope check that split them.

## Global Constraints

Copied verbatim from the spec and from `CLAUDE.md`. Every task's requirements implicitly include this section.

- **Never run `tmux` without an explicit `-L` or `-S`.** A bare `tmux` command reaches the operator's own server.
- **Never emit `#{client_activity}` or `#{client_created}`** in any format string — both segfault tmux 3.2a, which half this fleet runs. `internal/tmux`'s `Validate` is the guard.
- **Production UI strings are English.** `internal/ui/english_test.go` parses every non-test file and fails on a non-Latin string literal.
- **`docs/design.md` and `docs/known-issues.md` are guarded by `internal/ui/docs_test.go`** — seven mechanical checks. A doc edit that breaks a table, repeats a heading, leaves a directive, or reintroduces a refuted figure fails the suite.
- **Verify the COMMIT, never the working tree:** `git archive HEAD | tar -x -C <scratch>` then run the gates there.
- **Gates:** `gofmt -l .` silent; `go vet ./...`, `go vet -tags e2e ./internal/e2e/`, `go vet -tags mockup ./internal/ui/` all rc=0; `go test -count=1 -race ./...` reporting **18** `ok` with 0 `FAIL` and 0 `no test files`; `go test -count=1 -tags e2e ./internal/e2e/` ok. **Count SKIP beside PASS** — and run the e2e suite with `HUB_E2E_HOST=nuc`, because without it the only remote case skips.
- **Run tests with `rtk proxy go test …`** and quote any `-run` pattern; an unquoted `|` segfaults the rtk proxy.
- **Never build an argv by interpolating one string — zsh does not word-split `$var`.** Write arguments literally or use an array. This has bitten three times, once making a `claude` probe start a real session because its single argument was read as a prompt.
- **`git commit` commits the shared INDEX**, so put the pathspec on the COMMIT (`git commit -m … -- path/one.go`), `git add` any NEW file first and name it in the pathspec too, and read `git show --stat` afterwards against what you meant to send.
- **Commit messages:** lowercase conventional prefix, **no AI co-authorship trailers of any kind**.
- **Every test must fail against the unmodified product.** Show red, then green. A fixture that hand-builds a struct never exercises the producer that fills it — this repository has paid for that three times.

**Measured values this plan rests on.** All taken 2026-08-15/16 against the real fleet: local `claude` 2.1.233, `nuc` 2.1.224, and §17's third machine 2.1.226. Do not re-derive them and do not soften them.

- Bare `claude agents --json` hides every terminal row. One snapshot across all three claude-bearing hosts: **65 rows under `--all`, 57 of them `background`** — 87%. Per host, bare against `--all`: local 13→31 (measured 2026-08-15), `nuc` 11→14, third machine **4→25**, so the default call hides 84% of that host.
- The rows bare omits are terminal: locally 13 `done` and 4 `failed`. **`failed` is the actionable half and it is invisible on the main screen today.**
- **All 4 rows carrying `reapedMidWorkAt` report `failed`** in both `state.json` and the listing, 4/4. So `failed` is how a reaped-mid-work session surfaces at all.
- `agents.Attention()` folds `done`, `completed` **and** `idle` onto one output (`internal/agents/agents.go:56-67`), and `idle` is a LIVE interactive session's word on 2.1.233 as well as 2.1.224. **Nothing may derive a population or a rank from `Attention()` and assume it separates finished from live.**
- Mirroring `registry.agentRowID` over the real listing: bare → 13 rows / 13 distinct keys / 0 collisions; **`--all` → 31 rows / 30 distinct keys / 1 row lost silently**; `--all` minus `done` → 17 / 17 / 0. The colliding pair is a `background` row and an `interactive` continuation of the same conversation in the same `cwd`, differing only in `Kind`.
- `internal/ui/lifecycle.go` contains **zero** occurrences of `KindAgent`, and `killSelected` issues `kill-pane -t agent:<shortid>@<hash>` (`internal/tmux/lifecycle.go:97`), which tmux parses as session `agent` — counted into `killMsg.failed` with no reason shown.
- `claude stop <id>` exists on all three versions with byte-identical usage, so declining to offer it is a policy and the refusal must not imply a missing capability.
- `markActivity` writes `Pane.Activity` on three per-pane signals — `HistorySize`, `CursorY`, and the classification zone (`internal/registry/registry.go:358-367`) — so it moves on nearly every tick for a working pane.
- `m.cursor` is an **index** into the on-screen list (`internal/ui/model.go:130`), and its own comment records that a changing list makes that index name a different pane from the one the operator is looking at.
- **`--all` is FREE.** Three trials on each of the three hosts, timed on the host itself: local bare 194–204 ms against `--all` 189–207; `nuc` 365–378 against 371–392; the third machine 186–197 against 182–196, where `--all` was sometimes the faster of the two. So §17's separate 20 s timer stands and §16's commitments are untouched. (These are the CLI's own cost on that machine; §17's 0.5–2.8 s per-host figure includes the ssh leg.)
- **Changing `agentRowID` orphans nothing, and the reason is measured rather than assumed.** `hide.Key` is `{Host, Session, WindowIndex, PaneIndex, Start}` (`internal/hide/hide.go:53-59`) — it does not carry the row id at all. `SelectionKey` is `{Host, PaneID}` but lives in memory and is pruned every tick. The broadcast history cannot contain an agent row, because `i` on one is deferred. The only artefact carrying the id is the opt-in `--log-states` diagnostic (`internal/hub/statelog.go:75,87`), which nothing in the product reads. **No migration step is needed**; an operator grepping that log would see the id change mid-file, which is the annoying direction and not the dangerous one.

---

### Task 1: The producer stops hiding more than half of itself

`internal/agents`' two fetchers both call bare `claude agents --json`. That omits every terminal row, so a background job that FAILED does not appear on the main screen at all. The fix is one argument per fetcher and **no filter**: §22.6's ordering ruling replaced the filter an earlier draft specified, so `--all` is taken whole.

**Files:**
- Modify: `internal/agents/agents.go:137-151` (`Local` and `OverSSH`)
- Test: `internal/agents/agents_test.go`
- Modify: `docs/known-issues.md` (record that a `failed` row is now permanent and gets no dismissal — §22.9 decision 4)

**Interfaces:**
- Consumes: nothing from earlier tasks.
- Produces: no new symbol. `Local(timeout time.Duration) Fetcher` and `OverSSH(controlPath, dest string, timeout time.Duration) Fetcher` keep their signatures; only the argv behind them changes. Task 2 depends on this task landing first, because `--all` is what makes the collision live.

- [ ] **Step 1: Write the failing test for both fetchers**

Append to `internal/agents/agents_test.go`. Both fetchers, because the remote one is the half a local-only test cannot see, and a fix that names one site leaves the ssh fetcher on the narrow call.

```go
// `--all` on BOTH fetchers. Measured: bare `--json` hid 17 of 31 rows locally and 21 of 25 on
// the fleet's third host — every one of them terminal, and the `failed` ones are the half an
// operator can act on. `--all` is accepted on 2.1.224, the oldest version in the fleet, and it
// changes that host's answer in the same run (11 → 14), so it is supported rather than tolerated.
func TestBothFetchersAskForEverySession(t *testing.T) {
	local, ok := Local(time.Second).(*cmdFetcher)
	if !ok {
		t.Fatal("Local no longer returns a *cmdFetcher; rewrite this test, do not delete it")
	}
	if !slices.Contains(local.args, "--all") {
		t.Errorf("Local omits --all: %q — bare --json hides every terminal row", local.args)
	}

	remote, ok := OverSSH("/tmp/ctl", "host", time.Second).(*cmdFetcher)
	if !ok {
		t.Fatal("OverSSH no longer returns a *cmdFetcher; rewrite this test, do not delete it")
	}
	payload := strings.Join(remote.args, " ")
	if !strings.Contains(payload, "--all") {
		t.Errorf("OverSSH omits --all: %q", payload)
	}
}
```

- [ ] **Step 2: Run it and watch it fail**

```bash
rtk proxy go test -run 'TestBothFetchersAskForEverySession' ./internal/agents/
```

Expected: FAIL, twice — `Local omits --all: ["agents" "--json"]` and `OverSSH omits --all: …`.

- [ ] **Step 3: Add the argument to both fetchers**

In `internal/agents/agents.go`, `Local`:

```go
// Local lists sessions on this machine.
//
// `--all` is not optional: bare `--json` omits every terminal row, so a background job that
// FAILED is invisible. Measured on one snapshot across three hosts — 65 rows against a bare
// call's 28 — and accepted on 2.1.224, the oldest version in the fleet (docs/design.md §22.6).
func Local(timeout time.Duration) Fetcher {
	return &cmdFetcher{name: "claude", args: []string{"agents", "--json", "--all"}, timeout: timeout}
}
```

And in `OverSSH`, inside the shell string:

```go
		"command -v claude >/dev/null 2>&1 && claude agents --json --all || echo '[]'",
```

- [ ] **Step 4: Run it and watch it pass**

```bash
rtk proxy go test -run 'TestBothFetchersAskForEverySession' ./internal/agents/
```

Expected: PASS.

- [ ] **Step 5: Write the census test that proves nothing is filtered**

Also in `internal/agents/agents_test.go`. Built from a JSON literal, which is the shape this file already uses — a fixture that hand-builds `[]Session` never goes through the parser, and that is how this repository has hidden a forgotten field three times.

```go
// A CENSUS rather than a list of branches. Every word the fleet reports must survive the
// producer now that nothing is filtered, and the one that matters most is `failed`: all 4 rows
// carrying `reapedMidWorkAt` report it, in both the listing and the file, so losing that word
// loses the reaped-mid-work case §22.5 exists to announce.
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

- [ ] **Step 6: Run the census test**

```bash
rtk proxy go test -run 'TestEveryReportedWordSurvivesTheProducer' ./internal/agents/
```

Expected: PASS immediately — `Parse` already filters nothing, and this test is the guard that keeps it that way when someone later reaches for a filter.

- [ ] **Step 7: Prove the mutant dies**

Temporarily add a filter to `Parse`, just before `out = append(out, s)`:

```go
		if s.Attention() == "idle" {
			continue
		}
```

```bash
rtk proxy go test -run 'TestEveryReportedWordSurvivesTheProducer' ./internal/agents/
```

Expected: FAIL, naming `done`, `completed` **and** `idle` — which is the whole point: that mutant looks like "drop finished work" and also drops a live interactive session. **Remove the three lines before continuing.**

- [ ] **Step 8: Record the consequence in `docs/known-issues.md`**

`--all` admits `failed` rows and they never leave the listing, so add a row to the table. Keep the pipe count matching the header — `internal/ui/docs_test.go` fails the suite otherwise.

```markdown
| N7 | **A `failed` background row is permanent and has no dismissal.** Under the `--all` call §22.6 mandates, a row the daemon has finished with never leaves the listing: measured 2026-08-15, 4 of 31 local rows are `failed`, at rank `Error`, second only to `Needs`, in an inbox §16 sizes at 24 lines. | **By design, §22.9 decision 4.** `x` is taken and §22.5 refuses hiding a pane-less row, so a dismissal would need a new key and a new persisted set. What makes it bearable is the ordering rule instead: a `failed` row from two hours ago sinks below one from two minutes ago (§22.6), so nothing is hidden and nothing stays at the top forever. Reopen this if the ordering turns out not to be enough. |
```

- [ ] **Step 9: Run the gates**

```bash
gofmt -l .
rtk proxy go test ./...
rtk proxy go vet ./...
```

Expected: `gofmt` silent, 18 `ok`, 0 `FAIL`, vet rc=0.

- [ ] **Step 10: Commit**

```bash
git add -- internal/agents/agents_test.go
git commit -m "fix(agents): the listing was hiding more than half of itself

Both fetchers called bare \`claude agents --json\`, which omits every terminal
row — so a background job that FAILED was invisible on the main screen. One
snapshot across all three claude-bearing hosts: 65 rows under \`--all\`, and the
default call returns 28. On the third host bare returns 4 against 25, hiding 84%.

Nothing is filtered. An earlier draft dropped \`done\` to control noise and was
replaced by the ordering rule, so \`--all\` is taken whole and a settled session
stays reachable.

The census test is the guard that keeps it that way: it feeds every word the
fleet reports and requires each to survive Parse. Calibrated by adding the
obvious filter — \`Attention() == \"idle\"\` — which drops \`done\`, \`completed\` AND
a live interactive session, because that helper folds all three onto one output." -- internal/agents/agents.go internal/agents/agents_test.go docs/known-issues.md
git show --stat
```

---

### Task 2: The row key admits Kind, so one conversation cannot eat the other

`registry.go`'s own comment states the residue: "two sessions sharing an id AND a cwd would still merge. That does not occur on this fleet." Under Task 1's `--all` it does — 31 rows collapse to 30 keys and one of the operator's rows disappears with no error anywhere. The pair is a `background` row and an `interactive` continuation of the same conversation in the same directory, and `Kind` separates them for free.

**Files:**
- Modify: `internal/registry/registry.go:196-199` (`agentRowID`) and its residue comment at `:251-258`
- Test: `internal/registry/agentkey_test.go`
- Modify: `docs/known-issues.md` (N4's residue paragraph)

**Interfaces:**
- Consumes: Task 1's `--all`, which is what makes the collision reachable.
- Produces: `agentRowID(s agents.Session) string` keeps its signature and changes its VALUE. Nothing outside the registry may depend on the string's shape: an agent row's `PaneID` is identity only, and the renderer draws `p.Session` for these rows.

- [ ] **Step 1: Write the failing test**

Append to `internal/registry/agentkey_test.go`. In-package, so `Pane` and `New()` are unqualified.

```go
// The pair is real: on the measured fleet one `background` row and one `interactive`
// continuation of the same conversation shared a cwd and, after Parse back-fills the short id
// from SessionID[:8], a short id — differing only in Kind. Under `--all` they collapsed to one
// map entry and one of the operator's rows was gone with no error anywhere.
//
// count(DISTINCT key), never count(*): a collision keeps the total right and makes one row
// unreachable, which is exactly how this went unnoticed. And the assertion is on the VALUES both
// rows carry, because a change that keys them apart while writing the same record into both
// passes a count check and is the one outcome worse than the bug — it removes the evidence and
// keeps the symptom.
func TestABackgroundRowAndItsInteractiveContinuationAreTwoRows(t *testing.T) {
	const cwd = "/w/project-hub"
	const uuid = "3ec21f39-ad9f-4083-91e8-d257cbb22b30"

	r := New()
	r.UpdateAgents("local", []agents.Session{
		{SessionID: uuid, ID: "3ec21f39", Kind: "background",
			Name: "bg half", CWD: cwd, State: "failed"},
		{SessionID: uuid, ID: "3ec21f39", Kind: "interactive",
			Name: "interactive half", CWD: cwd, State: "idle"},
	}, time.Now())

	// The agent row's identity lands in Key.PaneID (`Key{Host: host, PaneID: agentRowID(s)}`),
	// and registry.Pane has no ID field, so PaneID is the key to count.
	rows := r.Panes()
	keys := map[string]bool{}
	byName := map[string]Pane{}
	for _, p := range rows {
		keys[p.PaneID] = true
		byName[p.Session] = p
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
		if p.SessionID != uuid {
			t.Errorf("row %q lost its session id: %q", want, p.SessionID)
		}
	}
}
```

- [ ] **Step 2: Run it and watch it fail**

```bash
rtk proxy go test -run 'TestABackgroundRowAndItsInteractiveContinuationAreTwoRows' ./internal/registry/
```

Expected: FAIL with `1 distinct keys for two sessions, want 2 — one row is unreachable`.

- [ ] **Step 3: Put Kind in the key**

Replace `agentRowID` in `internal/registry/registry.go`:

```go
// agentRowID is the row identity for a Claude session with no pane.
//
// Unique by construction rather than repaired on collision: a detect-and-suffix scheme would make
// a row's identity depend on the ORDER the listing arrived in, and the key is not private to the
// registry — a hide mark, an alias, the state log and the selection are all keyed on it, so every
// one of those artefacts would move under the operator for reasons nobody can see.
//
// `Kind` is in the hash because `--all` makes a stated residue live: measured 2026-08-15, 31 rows
// collapsed to 30 keys and one row was lost in silence, the pair being a `background` row and an
// `interactive` continuation of the same conversation in the same cwd (docs/design.md §22.11).
// Kind is fixed for the life of a session, so it cannot move the key between polls.
func agentRowID(s agents.Session) string {
	sum := sha256.Sum256([]byte(s.Kind + "\x00" + s.CWD))
	return "agent:" + s.ID + "@" + hex.EncodeToString(sum[:4])
}
```

The `\x00` separator matters: without it a `Kind` ending in the first characters of a `CWD` could produce the same input for two different pairs.

- [ ] **Step 4: Run it and watch it pass**

```bash
rtk proxy go test -run 'TestABackgroundRowAndItsInteractiveContinuationAreTwoRows' ./internal/registry/
```

Expected: PASS.

- [ ] **Step 5: Run the whole registry package**

```bash
rtk proxy go test ./internal/registry/
```

Expected: PASS. If a test asserted the literal key shape it fails here — fix it to look rows up by Claude's session id, which is what the listing is about, rather than by the key string.

- [ ] **Step 6: Correct the comment that was measured false**

The residue paragraph above `agentRowID`'s call site (`internal/registry/registry.go`, the `// Unique by construction …` block) asserts the collision does not occur on this fleet. Replace that sentence:

```go
		// Residue worth stating: two sessions sharing an id, a cwd AND a kind would still
		// merge. That has not been observed. The weaker residue — id and cwd alone — DID
		// occur under `--all` on 2026-08-15, 31 rows to 30 keys, which is why Kind is in
		// the hash (docs/design.md §22.11).
```

- [ ] **Step 7: Correct N4's residue in `docs/known-issues.md`**

Its residue sentence says the id+cwd collision "does not occur on this fleet". Under `--all` it does. Replace that clause with this, keeping the row's pipe count — `internal/ui/docs_test.go` fails the suite on a malformed table row:

```
**Residue, and it is no longer hypothetical:** two sessions sharing an id AND a cwd still merge, and under the `--all` call docs/design.md §22.6 mandates it **DOES** occur on this fleet — measured 2026-08-15, 31 rows collapse to **30** distinct keys and one of the operator's rows is lost in silence, the pair being a `background` row and an `interactive` continuation of the same conversation in the same cwd, differing only in `Kind`. Fixed by putting `Kind` in `agentRowID` (§22.11); the name cannot help, since two rows here share a name and a cwd while differing in id.
```

- [ ] **Step 8: Run the gates**

```bash
gofmt -l .
rtk proxy go test ./...
rtk proxy go test -race ./internal/registry/
```

Expected: `gofmt` silent, 18 `ok`, 0 `FAIL`, race rc=0.

- [ ] **Step 9: Commit**

```bash
git commit -m "fix(registry): --all made a stated residue live, so Kind joins the row key

The comment above agentRowID's call site said two sessions sharing an id AND a
cwd would still merge, and that it does not occur on this fleet. Under the
\`--all\` call the producer now makes, it does: 31 rows collapse to 30 distinct
keys and one of the operator's rows disappears with no error anywhere. The pair
is a background row and an interactive continuation of the same conversation in
the same directory, differing only in Kind.

Kind goes into the hash rather than a detect-and-suffix scheme, because a scheme
that repairs on collision makes a row's identity depend on the order the listing
arrived in — and the key is not private to the registry: a hide mark, an alias,
the state log and the selection are all keyed on it.

The test counts DISTINCT keys, never the total, because a collision keeps the
total right and leaves one row unreachable. It asserts the values on both rows
too: a change that keys them apart while writing the same record into both would
pass a count check and remove the evidence while keeping the symptom." -- internal/registry/registry.go internal/registry/agentkey_test.go docs/known-issues.md
git show --stat
```

---

### Task 3: `K` stops pretending a pane-less row is a pane

Measured: `internal/ui/lifecycle.go` contains **zero** occurrences of `KindAgent`, so `confirmKill` accepts a pane-less row like any other and `killSelected` issues `kill-pane -t agent:<shortid>@<hash>` (`internal/tmux/lifecycle.go:97`), which tmux parses as session `agent`, window `<shortid>@<hash>`. The failure lands in `killMsg.failed` with no reason shown: the operator sees a kill that did nothing and is told nothing.

§22.9 decision 3 rules that the hub does not end Claude sessions, so this task is a refusal rather than a capability — and the refusal is the whole feature.

**Read this before writing the test.** `confirmKill` acts on the SELECTION, not on the row under the cursor: it returns early when `m.sel.Len() == 0`, then iterates `m.sel.Members()` and matches each `SelectionKey{Host, PaneID}` against `m.panes` (`internal/ui/lifecycle.go:106-140`). A test that presses `K` without selecting anything gets `select a pane with space first` and would pass for the wrong reason. So the test selects the row, and the guard walks the selection.

**The rule, and it is a decision:** if ANY selected row is pane-less, the whole action refuses and names them. Killing the pane rows while silently skipping the agent rows would be a partial action the operator did not ask for, and this repository refuses silent partial success everywhere else.

**Files:**
- Modify: `internal/ui/lifecycle.go:106-140` (`confirmKill`)
- Test: `internal/ui/lifecycle_kind_test.go` (create)

**Interfaces:**
- Consumes: `registry.KindAgent` (`internal/registry/registry.go:34-35`); `Pane.Command`, which carries Claude's own kind for an agent row (`registry.go:270`); `Selection.Members() []SelectionKey` and `Selection.Toggle(SelectionKey)` (`internal/ui/selection.go:23,52`); `selKey(p registry.Pane) SelectionKey` = `{Host: p.Host, PaneID: p.PaneID}` (`internal/ui/model.go:1183-1185`); the model's `note` field (`model.go:240`).
- Produces: no new exported symbol. `confirmKill()` keeps its `(tea.Model, tea.Cmd)` signature and gains one early return after the empty-selection check.

- [ ] **Step 1: Write the failing tests**

Create `internal/ui/lifecycle_kind_test.go`.

```go
package ui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/DawnBreather/tmux-hub/internal/registry"
	"github.com/DawnBreather/tmux-hub/internal/state"
)

// `K` on a pane-less row issues `kill-pane -t agent:<shortid>@<hash>` today, which tmux reads as
// session `agent`, and the failure is counted with no reason shown. §22.9 decision 3 rules that
// the hub does not end Claude sessions, so the row is refused with the command the operator runs
// instead — and the guard goes BEFORE the dialog, so it never offers a target the hub will refuse.
//
// The row must be SELECTED: confirmKill acts on m.sel, not on the cursor, and without a selection
// it returns "select a pane with space first" — which would make this test pass for the wrong
// reason.
func TestKRefusesWhenTheSelectionHoldsABackgroundRow(t *testing.T) {
	row := registry.Pane{
		Kind: registry.KindAgent, Command: "background", Host: "nuc",
		Session: "deploy-audit", SessionID: "ea11c9d3",
		PaneID: "agent:ea11c9d3@1a2b3c4d", ClassifiedState: state.Error,
	}
	m := base(t, 100, 24, row)
	m.sel.Toggle(selKey(row))

	got, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("K")})
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

// The other half of the population needs a different sentence: an interactive session carries no
// background id — measured 0 of 13 rows across two hosts — so there is nothing to put in a
// `claude stop` command, and offering one would be an instruction the operator cannot follow.
func TestKRefusesAnInteractiveRowWithoutNamingAnId(t *testing.T) {
	row := registry.Pane{
		Kind: registry.KindAgent, Command: "interactive", Host: "local",
		Session: "scratch", PaneID: "agent:9f9f9f9f@5e6f7a8b",
		ClassifiedState: state.Idle,
	}
	m := base(t, 100, 24, row)
	m.sel.Toggle(selKey(row))

	got, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("K")})
	after := got.(model)

	if after.mode == modeConfirm {
		t.Error("K offered an interactive row for killing")
	}
	if !strings.Contains(after.note, "no pane to kill") {
		t.Errorf("the refusal does not say what is missing: %q", after.note)
	}
	if strings.Contains(after.note, "claude stop") {
		t.Errorf("the refusal offers `claude stop` for a row with no id: %q", after.note)
	}
}

// A MIXED selection refuses whole rather than killing the pane rows and skipping the rest, which
// would be a partial action nobody asked for.
func TestKRefusesAMixedSelectionWhole(t *testing.T) {
	agent := registry.Pane{
		Kind: registry.KindAgent, Command: "background", Host: "nuc",
		Session: "deploy-audit", SessionID: "ea11c9d3",
		PaneID: "agent:ea11c9d3@1a2b3c4d", ClassifiedState: state.Error,
	}
	pane := registry.Pane{
		Kind: registry.KindPane, Host: "local", Session: "api", Window: "fix",
		PaneID: "%0", Command: "claude", ClassifiedState: state.Needs,
	}
	m := base(t, 100, 24, pane, agent)
	m.sel.Toggle(selKey(pane))
	m.sel.Toggle(selKey(agent))

	got, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("K")})
	after := got.(model)

	if after.mode == modeConfirm {
		t.Error("a mixed selection opened the dialog; it must refuse whole")
	}
	if !strings.Contains(after.note, "deploy-audit") {
		t.Errorf("the refusal does not name the row that cannot be killed: %q", after.note)
	}
}
```

- [ ] **Step 2: Run them and watch them fail**

```bash
rtk proxy go test -run 'TestKRefusesWhenTheSelectionHoldsABackgroundRow|TestKRefusesAnInteractiveRowWithoutNamingAnId|TestKRefusesAMixedSelectionWhole' ./internal/ui/
```

Note the quotes: an unquoted `|` segfaults the rtk proxy.

Expected: all three FAIL with `K offered … for killing`, because `confirmKill` has no `Kind` guard at any layer.

- [ ] **Step 3: Add the guard, after the empty-selection check and before the reasons are built**

In `internal/ui/lifecycle.go`, immediately after the `if m.sel.Len() == 0 { … }` block in `confirmKill`:

```go
	// A pane-less row has no pane to kill, and §22.9 decision 3 rules that the hub does not end
	// Claude sessions. The guard is here rather than in killSelected so the dialog never offers a
	// target the hub will refuse: before this, `K` confirmed and then issued
	// `kill-pane -t agent:<shortid>@<hash>`, which tmux reads as session `agent`, and the failure
	// was counted into killMsg.failed with no reason shown.
	//
	// A MIXED selection refuses whole. Killing the pane rows and skipping the rest would be a
	// partial action the operator did not ask for.
	//
	// Two sentences, because one is wrong for half the population: an interactive session carries
	// no background id (measured 0 of 13 rows across two hosts), so there is nothing to put in a
	// `claude stop` command.
	for _, k := range m.sel.Members() {
		for _, p := range m.panes {
			if p.Host != k.Host || p.PaneID != k.PaneID || p.Kind != registry.KindAgent {
				continue
			}
			if p.Command == "background" {
				m.note = fmt.Sprintf("%s on %s has no pane to kill, and the hub does not stop "+
					"Claude sessions — run \"claude stop %s\" there; its conversation is kept, "+
					"and a reopens it.", p.Session, p.Host, p.SessionID)
			} else {
				m.note = fmt.Sprintf("%s on %s has no pane to kill, and the hub has no id to "+
					"stop it with — end it in the terminal that holds it.", p.Session, p.Host)
			}
			return m, nil
		}
	}
```

**`registry` is already imported by this file; `fmt` is NOT.** Add it first, or the paste does not compile — `internal/ui/lifecycle.go`'s import block is `tea`, `broadcast`, `hub`, `registry`, `tmux` and nothing else:

```go
import (
	"fmt"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/DawnBreather/tmux-hub/internal/broadcast"
	"github.com/DawnBreather/tmux-hub/internal/hub"
	"github.com/DawnBreather/tmux-hub/internal/registry"
	"github.com/DawnBreather/tmux-hub/internal/tmux"
)
```

- [ ] **Step 4: Run them and watch them pass**

```bash
rtk proxy go test -run 'TestKRefusesWhenTheSelectionHoldsABackgroundRow|TestKRefusesAnInteractiveRowWithoutNamingAnId|TestKRefusesAMixedSelectionWhole' ./internal/ui/
```

Expected: PASS, all three.

- [ ] **Step 5: Prove the mutant dies**

Delete the whole `for _, k := range m.sel.Members()` guard, run the three tests, and require all three to fail. Restore it. A guard that passes with and without itself is not a guard.

- [ ] **Step 6: Run the gates**

```bash
gofmt -l .
rtk proxy go test ./...
rtk proxy go vet ./...
rtk proxy go vet -tags mockup ./internal/ui/
```

Expected: `gofmt` silent, 18 `ok`, 0 `FAIL`, both vets rc=0. `english_test.go` runs here — the two sentences above are English and each carries the fix.

- [ ] **Step 7: Commit**

```bash
git add -- internal/ui/lifecycle_kind_test.go
git commit -m "fix(ui): K refuses a pane-less row instead of killing a pane that is not there

internal/ui/lifecycle.go contained zero occurrences of KindAgent, so confirmKill
accepted a pane-less row like any other and killSelected issued
\`kill-pane -t agent:<shortid>@<hash>\` — which tmux parses as session \`agent\`,
with the failure counted into killMsg.failed and no reason shown. The operator saw
a kill that did nothing and was told nothing.

The guard walks the SELECTION, because that is what confirmKill acts on rather
than the row under the cursor, and it sits before the reasons are built so the
dialog never offers a target the hub will refuse. A mixed selection refuses whole:
killing the pane rows and skipping the rest would be a partial action nobody asked
for.

Two sentences, because one is wrong for half the population: an interactive
session carries no background id — measured 0 of 13 rows across two hosts — so
there is nothing to put in a \`claude stop\` command. \`claude stop\` exists on all
three versions in the fleet, so declining to offer it is a policy and the sentence
must not imply a missing capability (§22.9 decision 3)." -- internal/ui/lifecycle.go internal/ui/lifecycle_kind_test.go
git show --stat
```

---

### Task 4: Recency orders what attention does not, coarsely

Task 1 admits every terminal row, and §22.6's ruling is that ordering replaces the filter an earlier draft specified: order by last activity, newest first, so old rows sink on their own. Three rules, and the second and third are what stop this inverting the tool.

**Files:**
- Modify: `internal/registry/registry.go:458-489` (`SortByAttention`)
- Test: `internal/registry/sort_recency_test.go` (create)

**Interfaces:**
- Consumes: `Pane.Activity` (`registry.go:59`), written by `markActivity` on three per-pane signals; `Pane.StateSince` (`:118`); `state.Needs.Rank()`.
- Produces: `SortByAttention(out []Pane)` keeps its signature. Its ORDER changes for every rank except `Needs`.

- [ ] **Step 1: Write the failing tests**

Create `internal/registry/sort_recency_test.go`.

```go
package registry

import (
	"testing"
	"time"

	"github.com/DawnBreather/tmux-hub/internal/state"
)

// Attention stays the primary key. Recency first would sink a session that has waited three
// hours below one that printed a log line, which is the opposite of §1.
func TestRecencyNeverOutranksAttention(t *testing.T) {
	now := time.Now()
	rows := []Pane{
		{Host: "h", Session: "chatty", PaneID: "%1",
			ClassifiedState: state.Works, Activity: now},
		{Host: "h", Session: "waiting", PaneID: "%2",
			ClassifiedState: state.Needs, Activity: now.Add(-3 * time.Hour)},
	}
	SortByAttention(rows)
	if rows[0].Session != "waiting" {
		t.Errorf("order is %q then %q; the row that needs the operator must come first",
			rows[0].Session, rows[1].Session)
	}
}

// Within one rank, newer activity comes first — which is what lets a `failed` row from two hours
// ago sit below one from two minutes ago instead of being hidden.
//
// THE NAMES FIGHT THE OLD TIEBREAK ON PURPOSE. The tiebreak being replaced is alphabetical by
// host, then session, then pane id, so a fixture named "old" and "fresh" passes against the
// UNMODIFIED product — "fresh" sorts first by accident and the test never sees recency at all.
// Here the older row sorts FIRST alphabetically, so only the recency rule can reorder them.
func TestWithinARankTheNewestComesFirst(t *testing.T) {
	now := time.Now()
	rows := []Pane{
		{Host: "h", Session: "aaa-two-hours-old", PaneID: "%1",
			ClassifiedState: state.Error, Activity: now.Add(-2 * time.Hour)},
		{Host: "h", Session: "zzz-two-minutes-old", PaneID: "%2",
			ClassifiedState: state.Error, Activity: now.Add(-2 * time.Minute)},
	}
	SortByAttention(rows)
	if rows[0].Session != "zzz-two-minutes-old" {
		t.Errorf("order is %q then %q; the newest row in a rank comes first",
			rows[0].Session, rows[1].Session)
	}
}

// STABILITY, and this is the test the task exists to satisfy. `markActivity` moves on nearly
// every tick for a working pane, and m.cursor is an INDEX into the on-screen list — its own
// comment records that a changing list makes that index name a different pane from the one the
// operator is looking at. So the order must not move for activity inside one minute.
func TestOrderIsStableWithinOneMinute(t *testing.T) {
	base := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
	// The names are in the OPPOSITE order to the activity times, so a per-second recency rule
	// would visibly reorder them and the alphabetical tiebreak cannot mask the movement.
	build := func(offset time.Duration) []Pane {
		return []Pane{
			{Host: "h", Session: "aaa", PaneID: "%1",
				ClassifiedState: state.Works, Activity: base.Add(offset)},
			{Host: "h", Session: "mmm", PaneID: "%2",
				ClassifiedState: state.Works, Activity: base.Add(20 * time.Second)},
			{Host: "h", Session: "zzz", PaneID: "%3",
				ClassifiedState: state.Works, Activity: base.Add(50 * time.Second)},
		}
	}
	first := build(0)
	SortByAttention(first)
	want := []string{first[0].Session, first[1].Session, first[2].Session}

	// Advance alpha's activity by seconds, inside the same minute bucket.
	second := build(40 * time.Second)
	SortByAttention(second)
	for i, w := range want {
		if second[i].Session != w {
			t.Fatalf("the order moved inside one minute: %q then %q then %q, was %v",
				second[0].Session, second[1].Session, second[2].Session, want)
		}
	}
}

// A zero Activity is UNKNOWN, not the beginning of time — the same rule the Needs block already
// applies to StateSince. A row the registry has never stamped must not outrank a real one.
func TestAnUnstampedRowDoesNotOutrankAStampedOne(t *testing.T) {
	// Again the names fight the alphabetical tiebreak: the UNSTAMPED row sorts first by name, so
	// only the zero-Activity rule can move the stamped one above it.
	rows := []Pane{
		{Host: "h", Session: "aaa-unstamped", PaneID: "%1", ClassifiedState: state.Works},
		{Host: "h", Session: "zzz-stamped", PaneID: "%2",
			ClassifiedState: state.Works, Activity: time.Now().Add(-time.Hour)},
	}
	SortByAttention(rows)
	if rows[0].Session != "zzz-stamped" {
		t.Errorf("order is %q then %q; a known activity time outranks an unknown one",
			rows[0].Session, rows[1].Session)
	}
}
```

- [ ] **Step 2: Run them and watch the right ones fail**

```bash
rtk proxy go test -run 'TestRecencyNeverOutranksAttention|TestWithinARankTheNewestComesFirst|TestOrderIsStableWithinOneMinute|TestAnUnstampedRowDoesNotOutrankAStampedOne' ./internal/registry/
```

Expected, and check each one — this is where a fixture that cannot fail hides:

- `TestRecencyNeverOutranksAttention` **PASSES already.** Attention is the existing primary key; the test exists to keep it that way.
- `TestWithinARankTheNewestComesFirst` **FAILS** with `order is "aaa-two-hours-old" then "zzz-two-minutes-old"`. It fails only because the fixture's names run against the alphabetical tiebreak; named "old" and "fresh" it would pass against the unmodified product and test nothing.
- `TestAnUnstampedRowDoesNotOutrankAStampedOne` **FAILS** with `order is "aaa-unstamped" then "zzz-stamped"`, for the same reason.
- `TestOrderIsStableWithinOneMinute` **PASSES already**, trivially: the alphabetical order does not move. Its teeth come from Step 5, which switches the bucket to a second and requires it to fail.

- [ ] **Step 3: Add the bucketed recency tiebreak**

In `SortByAttention`, between the `Needs` block and the `a.Host != b.Host` tiebreak:

```go
		// Recency for every rank except Needs, which keeps longest-wait-first above. Bucketed to
		// the MINUTE, and the bucket is not tidiness: markActivity moves on nearly every tick for
		// a working pane (HistorySize, CursorY, and the classification zone), while m.cursor is an
		// INDEX into the on-screen list — so a per-second order would move the row under the
		// operator's hand between looking and pressing. The alphabetical tiebreak below still
		// exists and still does the job it always did; it is now the tiebreak WITHIN a bucket,
		// which is why the list is still stable between ticks (docs/design.md §22.6).
		//
		// A zero Activity is UNKNOWN, not the beginning of time, so a stamped row outranks an
		// unstamped one — the same rule the Needs block applies to StateSince.
		az, bz := a.Activity.IsZero(), b.Activity.IsZero()
		switch {
		case az != bz:
			return bz // a known activity time outranks an unknown one
		case !az && !bz:
			am, bm := a.Activity.Truncate(time.Minute), b.Activity.Truncate(time.Minute)
			if !am.Equal(bm) {
				return am.After(bm) // newest first
			}
		}
```

`time` is already imported by this file.

- [ ] **Step 4: Run them and watch them pass**

```bash
rtk proxy go test -run 'TestRecencyNeverOutranksAttention|TestWithinARankTheNewestComesFirst|TestOrderIsStableWithinOneMinute|TestAnUnstampedRowDoesNotOutrankAStampedOne' ./internal/registry/
```

Expected: all four PASS.

- [ ] **Step 5: Prove the stability test can fail**

Change `Truncate(time.Minute)` to `Truncate(time.Second)` and run `TestOrderIsStableWithinOneMinute`. Expected: FAIL, `the order moved inside one minute`. Restore `time.Minute`. Without this step the bucket is a comment rather than a guarantee.

- [ ] **Step 6: Give the ordering a SCREEN, because otherwise it has none**

Measured before this plan was written, by applying all four tasks to a copy of the tree: the full suite stays green, no frame test moves, and `docs/ui-mockup.html` regenerates **byte-identical**. That is not evidence the ordering works — it is evidence the frames cannot see it. Only **4 of 83** `registry.Pane` literals in `internal/ui`'s tests set `Activity`, and the mockup generator sets it **zero** times, so the zero-Activity rule sends every fixture row to the alphabetical tiebreak and the order never changes.

Shipping there would leave this repository's signature defect: a behaviour that is defined, unit-tested, and never seen on a screen. So add one frame assertion.

```go
// The ordering on a real screen, at the width §16 commits to. Two rows in ONE rank, so only
// recency can separate them, and the names run against the alphabetical tiebreak — without that
// the assertion passes on the old comparator and tests nothing.
//
// This is the only screen coverage the ordering has: 4 of 83 Pane literals in this package set
// Activity, and the mockup generator sets none, which is why the mockup regenerates identical.
func TestTheScreenShowsTheNewestRowFirstWithinARank(t *testing.T) {
	now := time.Now()
	older := registry.Pane{
		Kind: registry.KindAgent, Command: "background", Host: "local",
		Session: "aaa-two-hours-old", PaneID: "agent:11111111@aaaaaaaa",
		ClassifiedState: state.Error, Activity: now.Add(-2 * time.Hour),
	}
	newer := registry.Pane{
		Kind: registry.KindAgent, Command: "background", Host: "local",
		Session: "zzz-two-minutes-old", PaneID: "agent:22222222@bbbbbbbb",
		ClassifiedState: state.Error, Activity: now.Add(-2 * time.Minute),
	}
	// ONE slice, sorted in place, and the SAME slice handed to Render. Sorting a fresh literal
	// and rendering another one throws the sort away and the test then fails in both poles.
	rows := []registry.Pane{older, newer}
	registry.SortByAttention(rows) // the product's own comparator, not a hand-ordered slice

	out := Render(Frame{Panes: rows, Width: 80, Height: 24})
	iNew := strings.Index(out, "zzz-two-minutes-old")
	iOld := strings.Index(out, "aaa-two-hours-old")
	if iNew < 0 || iOld < 0 {
		t.Fatalf("both rows must be on the screen: newest at %d, oldest at %d\n%s", iNew, iOld, out)
	}
	if iNew > iOld {
		t.Errorf("the older row is drawn first; recency must put the newest above\n%s", out)
	}
}
```

Put it in `internal/ui/render_order_test.go`. Note that `Render` takes the panes in the order given, so the test sorts them through `registry.SortByAttention` first — the same call the model makes — rather than trusting a hand-ordered slice.

```bash
rtk proxy go test -run 'TestTheScreenShowsTheNewestRowFirstWithinARank' ./internal/ui/
```

Expected: FAIL against the unmodified comparator (the alphabetical tiebreak puts `aaa-` first), PASS with Task 4's change in place.

- [ ] **Step 7: Run the whole suite, then regenerate the mockup**

```bash
rtk proxy go test ./...
rtk proxy go test -race ./...
rtk proxy go test -tags mockup -run TestGenerateMockup ./internal/ui/
git diff --stat -- docs/ui-mockup.html
```

Expected: 18 `ok`, 0 `FAIL`, race rc=0, and **no diff in the mockup** — measured. If a diff appears, a fixture somewhere does set `Activity` and the frame genuinely moved; read it before committing rather than regenerating past it.

- [ ] **Step 8: Commit**

```bash
# BOTH new files, or the frame test never reaches the commit: a pathspec cannot name a file
# git has never seen, and this repository verifies the COMMIT rather than the working tree.
git add -- internal/registry/sort_recency_test.go internal/ui/render_order_test.go
git commit -m "feat(registry): recency orders what attention does not, bucketed to the minute

§22.6's ruling: ordering replaces the filter an earlier draft specified, so the
rows --all admits sink as they age instead of being hidden. Three rules, and the
last two are what stop this inverting the tool.

Attention stays the primary key — recency first would sink a session that has
waited three hours below one that printed a log line. Longest-wait-first stays
inside the Needs block, which points the other way from recency on purpose: it is
the order the question \"who needs me\" is asked in.

Recency replaces the alphabetical tiebreak for every other rank, bucketed to the
MINUTE. The bucket is not tidiness: markActivity moves on nearly every tick for a
working pane, and m.cursor is an INDEX into the on-screen list, so a per-second
order would move the row under the operator's hand between looking and pressing.
Calibrated by switching the bucket to a second, which makes the stability test
fail.

A zero Activity is UNKNOWN rather than the beginning of time, the same rule the
Needs block already applies to StateSince." -- internal/registry/registry.go internal/registry/sort_recency_test.go internal/ui/render_order_test.go docs/ui-mockup.html
git show --stat
```

---

## Final verification, on the COMMIT

`go test` compiles the working tree, so uncommitted files can carry a green suite. Verify the commit.

- [ ] **Step 1: Extract and gate**

```bash
S=$(mktemp -d)
git archive HEAD | tar -x -C "$S"
cd "$S"
gofmt -l .
rtk proxy go test ./...
rtk proxy go vet ./...
rtk proxy go vet -tags e2e ./internal/e2e/
rtk proxy go vet -tags mockup ./internal/ui/
rtk proxy go test -race ./...
HUB_E2E_HOST=nuc rtk proxy go test -tags e2e -count=1 -v ./internal/e2e/
```

Expected: `gofmt` silent; 18 `ok` with 0 `FAIL` and 0 `no test files`; three vets rc=0; race rc=0 with 0 `DATA RACE`; e2e rc=0 with **54 PASS, 0 FAIL, 0 SKIP**. Count the SKIP column: without `HUB_E2E_HOST` the only remote case skips and the remote leg is untested.

- [ ] **Step 2: Look at the fleet**

Run the hub against the real fleet and confirm the change is the one intended: `failed` rows are visible, no row is missing, and the list does not jitter between ticks.

- [ ] **Step 3: Update the plan index**

Add this plan to `docs/plans/README.md` with its status, and mark `2026-08-15-reaching-paneless-sessions.md` superseded by it with one sentence of reason.

## What this plan does NOT do, and where it went

The door. `a` on a background row still refuses, because `internal/ui/attach.go:36` refuses every agent row and this plan does not touch it. Four things wait for the door plan, and each is named in `docs/design.md` §22 with the measurement behind it:

| deferred | why it cannot land here |
|---|---|
| `a` creates a pane on the row's own host and runs `claude attach <id>` (§22.3) | the door itself; three of §22.10's eleven open items bear on it, most sharply that **22 of 25** ids the hub can put under the cursor are on disk and absent from the roster |
| the wake confirmation, `failed`-only, with the host cost as a line (§22.9 decisions 1 and 2) | there is nothing to confirm until `a` acts on a pane-less row |
| the tile's sentence from `state.json` (§22.5) | a reader for another program's files, and its own task |
| the interactive refusal for `a`, gated on `Kind` (§22.8) | same door; note that Task 3 already establishes the `Kind`-not-id gate for `K`, so the door inherits the pattern rather than inventing it |
| a rank of its own for `done`, below `idle` (§22.6) | §22.6 names this consequence and gives it to §12, which owns the rank table: `Attention()` folds `done`, `completed` and `idle` onto one output, so with `done` now shown a FINISHED background row and a LIVE interactive session share a rank and only Task 4's recency tiebreak separates them. An executor of Task 4 will see it; it is known, it is not this plan's, and §12 is where the change and its test belong |
