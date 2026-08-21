# Hiding and Lifecycle Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let the user remove noise from the dashboard permanently, and own the full lifecycle of tmux windows and Claude Code sessions on every host the hub watches — create, restart, resume, kill — without ever leaving the hub.

**Architecture:** Three pillars over the existing seam. (1) `internal/hide` holds a persisted set of panes the user does not want to see, keyed on a human path plus one immutable corroborator, with one rule — a pane waiting on the user is shown anyway — that makes a wrong key match structurally unable to lose work. (2) `internal/tmux/lifecycle.go` adds the creation and destruction verbs to the same validated `Runner` the read path uses, so every new command inherits the existing guards. (3) `internal/launch` dictates a Claude session id at birth (`claude --session-id <uuid>`) and stamps the pane in the same breath, which means a hub-created agent needs **no** process-tree inference at all — the identification walk in `internal/proc`, where this project's one Critical defect lived, becomes a fallback for foreign panes only.

**Tech Stack:** Go 1.22+, bubbletea/lipgloss (already vendored in `internal/ui`), tmux 3.2a+ (measured on 3.7b), Claude Code CLI 2.1.228+.

## Global Constraints

- **tmux floor is 3.2a.** Every format and flag below is verified on 3.7b; anything that needs a newer tmux must degrade with a message, never fail silently.
- **Every tmux invocation goes through `tmux.Runner`** and therefore through `tmux.Validate`. No `exec.Command("tmux", …)` anywhere outside `internal/tmux`.
- **Never a tmux command without an explicit `-L`/`-S`.** The hub only ever addresses a socket it was told about.
- **Never emit `#{client_activity}` or `#{client_created}`** — they segfault tmux 3.2a with no client attached. `tmux.Validate` already refuses them; do not add exceptions.
- **`display -p` output is empty at rc=0 for a pane that does not exist** (measured: `display -p -t %999 '#{pane_id}'` → rc=0, empty). Emptiness is therefore the liveness signal, and no code may read an empty `display` result as "the option is unset".
- **`display -p` runs its argument through `strftime`**, so a literal `%` empties the message at rc=0. Identity is emitted as `#{pane_id}`, never as a literal `%N`.
- **Fail toward VISIBLE.** Any ambiguity in the hidden set resolves to showing the pane. Hiding a live agent loses work; showing a noisy pane only annoys.
- **A destructive verb always confirms**, and the confirmation names what is running in the pane it is about to destroy.
- **`--resume` is NOT exclusive, so the hub must make it so.** Measured: a second `claude --resume <uuid>` succeeds at rc=0 while an interactive claude still holds that session — no lock, no warning, two processes appending to one transcript. `--session-id` refuses a duplicate; `--resume` does not. Every restart therefore kills first and confirms the pane is gone with `PaneAlive` before resuming, which makes `PaneAlive` load-bearing rather than a convenience.
- **`--session-id` takes a FRESH uuid, always.** Reusing one exits 1 with `Error: Session ID <uuid> is already in use.` (measured). Continuity is `--resume <uuid>`.
- **Docs are part of every task.** A task that changes behaviour updates `docs/design.md` in the same commit.
- No AI co-authorship marks in commits, code or docs.

---

## Measured foundations this plan rests on

All measured this session, tmux 3.7b, private socket, Claude Code 2.1.228. These rows belong in `docs/design.md` §3 (Task 1).

| Question | Answer |
|---|---|
| `new-window -t <sess> -c <dir> -P -F '#{pane_id}' '<cmd>'` | returns the new pane's id on stdout, e.g. `%2` |
| `new-session -d -s <name> -c <dir> -P -F '#{pane_id} #{session_name}'` | returns `%3 my proj` — works with a space in the session name |
| `-c` with a space in the path | works; it is its own argv element, no quoting needed |
| a command with quoted arguments, as one string | works: `'sh -c "echo hello world; sleep 302"'` → screen shows `hello world` |
| is a pane id reused after the pane dies? | **no** — killed `%3`, next pane was `%4`. Monotonic within one server's life |
| `respawn-pane -k -t %N '<newcmd>'` | keeps `pane_id`, keeps `@hub_*` options, keeps cwd; `pane_pid` changes (222976 → 222983) |
| `remain-on-exit on`, command exits 7 | pane **stays**: `#{pane_dead}=1`, `#{pane_dead_status}=7`, listed by `list-panes -a` |
| `remain-on-exit off` (default), command exits | pane is destroyed; `display -p -t <gone>` → **rc=0, empty** |
| `display -p -t %999 '#{pane_id}'` (never existed) | **rc=0, empty** — tmux does not error with no client attached |
| `#{pane_start_command}` | quoted in output (`"sleep 301"`); immutable for the pane's life, changes only on respawn |
| `#{pane_current_command}` as a persisted key | unusable — walks `zsh` → `claude` → `zsh` |
| `claude --session-id <fresh-uuid> -p …` | rc=0; transcript lands at `~/.claude/projects/<slug>/<uuid>.jsonl` |
| `claude --session-id <existing-uuid>` | **rc=1**, `Error: Session ID <uuid> is already in use.` |
| `claude --resume <uuid> -p …` | rc=0, continues that conversation (transcript grew to 32 lines) |
| `claude --permission-mode` choices | `acceptEdits`, `auto`, `bypassPermissions`, `manual`, `dontAsk`, `plan` |
| `claude --model` values | aliases `fable`, `opus`, `sonnet`, or a full model name |
| `claude --resume <uuid>` after `respawn-pane -k` SIGKILLed the interactive claude | **rc=0, and it recalled the token stored before the kill** (`MARKER42`); transcript grew 20 → 49 lines. Restart-with-continuity survives a hard kill |
| a SECOND `claude --resume <uuid>` while an interactive one holds that session | **rc=0 — no lock, no error.** Unlike `--session-id`, `--resume` does not refuse concurrent use |
| interactive `claude --resume <uuid>` in a tmux pane | `pane_current_command` = `claude`; `pane_start_command` = `"claude --resume <uuid>"` |
| `pane_start_command` for a pane created with NO command | **empty**; `pane_current_command` = `zsh` |
| `claude --resume <nonexistent>` in a pane | does **not** exit: `pane_dead=0`, no status — it draws an interactive session picker and waits |

Two facts about the code as it stands, both verified by reading it:

- **`state.Error` from a dead pane is currently unreachable.** `#{pane_dead}` is already in `DeltaFormat` (`internal/tmux/delta.go:16`), flows to `registry.Pane.Dead` (`registry.go:250`) and is consumed by `state.Classify` (`state.go:213`) — but nothing sets `remain-on-exit`, so the pane is destroyed before any tick can see it. The branch only fires for a user whose own tmux config happens to enable it.
- **`DeltaFormat` is parsed with `strings.Split(line, "|")`** (`delta.go:87`), so no delta field may contain `|`. Labels are parsed with `strings.SplitN(line, "|", 2)` (`labels.go:34`), so a label value may contain anything. `#{pane_start_command}` therefore belongs in labels, never in the delta.

---

## File Structure

**New:**
- `internal/statedir/statedir.go` — one owner for the XDG state path. `history.DefaultPath` currently inlines this logic; the hidden set would be a second copy.
- `internal/hide/hide.go` — `Key`, `Set`, load/save, the resurface rule.
- `internal/hide/hide_test.go`
- `internal/tmux/lifecycle.go` — `NewSession`, `NewWindow`, `KillWindow`, `KillSession`, `RespawnPane`, `PaneAlive`, `SetWindowOption`.
- `internal/tmux/lifecycle_test.go`
- `internal/launch/launch.go` — `Spec`, `Validate`, `Argv`, `NewSessionID`.
- `internal/launch/launch_test.go`
- `internal/ui/launchform.go` — the launch form's model and rendering.
- `internal/ui/launchform_test.go`
- `internal/e2e/lifecycle_test.go`, `internal/e2e/hide_test.go` — build-tagged `e2e`.

**Modified:**
- `internal/tmux/labels.go` — one new label: `#{pane_start_command}`.
- `internal/tmux/delta.go` — two new delta fields: `#{pane_index}`, `#{pane_dead_status}`.
- `internal/registry/registry.go` — carry `Index`, `StartCommand`, `DeadStatus` onto `Pane`.
- `internal/history/history.go` — `DefaultPath` delegates to `statedir`.
- `internal/ui/model.go` — hidden set, new keys (`x`, `X`, `n`, `R`, `K`), new `launch` mode.
- `internal/ui/render.go` — filter hidden, footer counter, dead-pane row, launch form.
- `internal/ui/selection.go` — hidden panes are not selectable while hidden.
- `internal/hub/poll.go` — nothing about hiding (hidden panes are still polled, by decision), but `Host` gains nothing; only the new label/delta fields flow through.
- `cmd/tmux-hub/main.go` — open the hidden set, pass it to `ui.Run`; `--no-hide` to disable.
- `cmd/tmux-hub/wiring_test.go` — extend the wiring floor to the new constructors.
- `docs/design.md` — new §18 (Hiding), §19 (Lifecycle), new §3 rows, §11 gains one killed class, §12 gains one honest limitation.
- `README.md` — the new keys.

---

### Task 1: Spec first — §18 Hiding, §19 Lifecycle, and the measured rows

The spec is the contract the other twelve tasks implement. It goes first so that a reviewer of any later task has something to check against.

**Files:**
- Modify: `docs/design.md`

**Interfaces:**
- Consumes: nothing.
- Produces: §18 and §19, cited by every later task.

- [ ] **Step 1: Add the measured rows to §3**

Append every row from the "Measured foundations" table above to `docs/design.md` §3, in the existing table's format. Keep the exact values — `%4` after killing `%3`, `222976 → 222983`, `rc=1 Error: Session ID … is already in use.`, `#{pane_dead_status}=7`. A row without its measured value is a claim, not a foundation.

- [ ] **Step 2: Write §18 Hiding**

It must state, in prose:

1. **What hiding is for:** a host accumulates panes that are not agents and never will be — a log tail, a `htop`, a build watcher. They are permanent noise. Hiding is the user saying "never show me this again", not "not right now".
2. **Why the persisted key is not a pane id:** `%12` is monotonic and never reused *within one server's life*, which makes it an exact key while the hub runs and the wrong key on disk — a restarted server numbers from `%0` again, so a persisted `%3` names a different pane in the next generation.
3. **What the persisted key is:** `{host, session_name, window_name, pane_index, pane_start_command}`. The start command is the corroborator because it is immutable for the pane's life; `pane_current_command` is not (it walks `zsh` → `claude` → `zsh`).
4. **The one rule that makes a wrong match safe:** a marked pane whose state is `Needs` is shown anyway. This is load-bearing, not a courtesy. A key can only mis-match a pane sharing a window, an index and a start command with the pane the user hid — and even then the mis-matched pane cannot be hidden while it is *blocked*, which is the only state where hiding loses work. Say this explicitly, because a later reader will otherwise read the resurface as cosmetic and remove it.
5. **Hidden panes are still polled.** The cost is accepted deliberately: a pane that is not polled cannot resurface, and a blocked agent that cannot resurface is exactly the loss the rule above prevents.
6. **Fail toward visible:** a malformed or unreadable hidden-set file is a warning and an empty set, never a fatal error and never a guess.
7. **Where the corroborator is empty, say so.** Measured: `#{pane_start_command}` is the **empty string** for a pane created with no command — the plain shell, which is the commonest thing a user hides. For those keys the corroborator carries no information and the match rests on position alone, so a `pane_index` shift can move a mark to a sibling shell. That is acceptable and it is why the resurface rule, not the corroborator, is the actual safety net. Do not write §18 as though every key had a corroborator; a wrong comment on this stops the next reader checking.
8. **A hub-created agent's mark cannot persist, by construction.** Measured: an interactive agent's start command is `"claude --resume <uuid>"` — it contains the session's uuid, so its key is unique to that launch and can never match a future one. Hiding an agent is therefore effective for the life of that pane and no longer. State it; it is the right behaviour (a stale mark on an agent is the dangerous kind) but a user will otherwise report it as a bug.

- [ ] **Step 3: Write §19 Lifecycle**

It must state:

1. **The verbs:** create a window, create a session, restart a pane in place, resume a dead session, kill a window, kill a session. Each on any host the hub watches, local or over a forwarded socket.
2. **Identity at birth, and what it kills.** The hub generates the uuid and passes `claude --session-id <uuid>`, and `new-window -P -F '#{pane_id}'` hands back the pane id in the same call. So for a hub-created agent the pane↔session binding is *known*, and the process-tree walk in `internal/proc` — the code where this project's one Critical defect lived (a forwarded socket walked against the local process table; 97 of 3117 local pids answer "agent here") — is never consulted. Add this to §11 as a killed class, scoped honestly: it is killed **for hub-created panes only**; foreign panes still need the walk.
3. **Why `--session-id` is never reused:** rc=1 with `Error: Session ID <uuid> is already in use.` A hub that reuses an id fails loudly instead of silently forking a conversation. Restart-with-continuity is `--resume <uuid>`.
4. **Restart in place keeps more than it looks.** `respawn-pane -k` keeps `pane_id`, `@hub_*` options and cwd, and changes `pane_pid`. The surviving stamp is a hazard as well as a convenience: the token still matches, so the guarded write path still trusts the pane, while the *agent* behind it is a different process. Identity must therefore be explicitly invalidated on respawn — a task below does this, and the spec says why.
5. **Dead panes need `remain-on-exit`.** Without it a pane whose command exits is destroyed, so "claude exited with code 7" is not a state the hub can ever observe — it is a row that vanishes. The hub sets `remain-on-exit on` **per window, on windows it creates**, never globally: a global set would change the user's own windows' behaviour. Consequence, and it goes in §12 as an honest limitation: for a foreign pane the hub cannot distinguish "the agent exited" from "the pane was closed", and will not pretend to.
6. **Liveness is emptiness.** `display -p -t <gone> '#{pane_id}'` returns rc=0 and an empty string, so a lifecycle verb checks a pane exists by asking for its own id and treating empty as gone.

- [ ] **Step 4: Add the §12 limitation and the §11 killed class**

§12 gains: foreign dead panes are invisible (above). §11 gains: identity inference is structurally unnecessary for hub-created panes.

- [ ] **Step 5: Commit**

```bash
git add docs/design.md
git commit -m "docs: spec hiding (§18) and lifecycle (§19) with the measured rows"
```

---

### Task 2: The fields hiding and lifecycle need

**Files:**
- Modify: `internal/tmux/labels.go`, `internal/tmux/delta.go`, `internal/registry/registry.go`
- Test: `internal/tmux/labels_test.go`, `internal/tmux/delta_test.go`, `internal/registry/registry_test.go`

**Interfaces:**
- Consumes: `tmux.Labels`, `tmux.Delta`, `registry.Pane` as they are.
- Produces: `Labels.StartCommand string`; `Delta.Index int`, `Delta.DeadStatus int`; `registry.Pane.Index int`, `Pane.StartCommand string`, `Pane.DeadStatus int`.

- [ ] **Step 1: Write the failing label test**

`#{pane_start_command}` can contain `|`. Labels are parsed with `SplitN(line, "|", 2)`, so this must survive — assert it, because a future refactor to `Split` would break it silently.

```go
func TestLabelsCarryStartCommandContainingAPipe(t *testing.T) {
	// pane_start_command is arbitrary shell text. Labels are parsed with
	// SplitN(line, "|", 2) precisely so a value may contain the delimiter;
	// this test is what makes that a guarantee instead of an accident.
	out := "%3|sh -c \"tail -f log | grep boom\"\n"
	var l Labels
	if err := applyLabel(&l, startCommandLabel, out); err != nil {
		t.Fatalf("applyLabel: %v", err)
	}
	if want := `sh -c "tail -f log | grep boom"`; l.StartCommand != want {
		t.Fatalf("StartCommand = %q, want %q", l.StartCommand, want)
	}
}
```

Adapt the helper names to whatever `labels.go` already exposes for a single-format apply; if it exposes only the whole-batch parse, drive the test through that instead and keep the assertion identical.

- [ ] **Step 2: Run it and watch it fail**

Run: `go test ./internal/tmux/ -run StartCommand -v`
Expected: FAIL — `Labels` has no field `StartCommand`.

- [ ] **Step 3: Add the label**

In `internal/tmux/labels.go`, beside the existing two entries:

```go
{"#{pane_id}|#{pane_start_command}", func(l *Labels, v string) { l.StartCommand = v }},
```

and the field on `Labels`:

```go
// StartCommand is #{pane_start_command}: the command the pane was created with,
// quoted by tmux (`"sleep 301"`). It is immutable for the life of the pane and
// changes only on respawn-pane, which is what makes it the corroborator for a
// persisted hide key (docs/design.md §18). pane_current_command is not usable
// for that: it walks zsh → claude → zsh.
StartCommand string
```

- [ ] **Step 4: Write the failing delta test**

```go
func TestDeltaCarriesIndexAndDeadStatus(t *testing.T) {
	// A dead pane's exit code is the difference between "the agent exited" and
	// "the agent exited BADLY", and the row has to say which.
	line := deltaLine(t, map[string]string{"pane_index": "2", "pane_dead": "1", "pane_dead_status": "7"})
	d := parseDeltaLine(t, line)
	if d.Index != 2 {
		t.Errorf("Index = %d, want 2", d.Index)
	}
	if !d.Dead {
		t.Error("Dead = false, want true")
	}
	if d.DeadStatus != 7 {
		t.Errorf("DeadStatus = %d, want 7", d.DeadStatus)
	}
}
```

Write `deltaLine` as a local helper that builds a `|`-joined line with every field of `DeltaFormat` in order, defaulting each to a benign value and overriding from the map. Building the line from the *format* rather than from a hand-written literal is what makes the test survive the next field addition.

- [ ] **Step 5: Run it and watch it fail**

Run: `go test ./internal/tmux/ -run DeltaCarries -v`
Expected: FAIL — no `Index`/`DeadStatus` fields.

- [ ] **Step 6: Extend `DeltaFormat`**

Append the two fields — both are integers, so neither can contain the `|` that `strings.Split` would trip on. Put them at the end and bump the field count constant:

```go
const DeltaFormat = "#{pane_id}|#{window_activity}|#{history_size}|" +
	"#{pane_dead}|#{?alternate_on,ALT,NORM}|#{window_width}|#{pane_height}|#{pane_pid}|" +
	"#{cursor_y}|#{session_id}|#{bracket_paste_flag}|#{pid}|#{start_time}|#{window_id}|" +
	"#{pane_index}|#{pane_dead_status}"
```

Bump `deltaFields` to match the new count, and parse the two values with `strconv.Atoi`, tolerating an empty string as 0 — `#{pane_dead_status}` is empty for a live pane (measured).

- [ ] **Step 7: Carry them onto `registry.Pane`**

Add `Index int`, `StartCommand string`, `DeadStatus int` and populate them where `Dead` is already populated (`registry.go:250` and the label apply). Write one registry test that goes through the real apply path and asserts all three arrive — a test that hand-builds the struct proves only that assignment works.

- [ ] **Step 8: Run the suite**

Run: `go test -race ./... -count=1`
Expected: PASS, all packages.

- [ ] **Step 9: Commit**

```bash
git add internal/tmux/labels.go internal/tmux/delta.go internal/registry/registry.go internal/tmux/labels_test.go internal/tmux/delta_test.go internal/registry/registry_test.go
git commit -m "tmux: carry pane_index, pane_dead_status and pane_start_command"
```

---

### Task 3: `internal/statedir` — one owner for the XDG path

**Files:**
- Create: `internal/statedir/statedir.go`, `internal/statedir/statedir_test.go`
- Modify: `internal/history/history.go`

**Interfaces:**
- Produces: `statedir.Path(name string) string`.

- [ ] **Step 1: Write the test**

```go
func TestPathHonoursXDGStateHome(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", "/xdg")
	if got, want := Path("hidden.json"), "/xdg/tmux-hub/hidden.json"; got != want {
		t.Fatalf("Path = %q, want %q", got, want)
	}
}

func TestPathFallsBackToHome(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", "")
	t.Setenv("HOME", "/home/u")
	if got, want := Path("history.jsonl"), "/home/u/.local/state/tmux-hub/history.jsonl"; got != want {
		t.Fatalf("Path = %q, want %q", got, want)
	}
}
```

- [ ] **Step 2: Run it and watch it fail**

Run: `go test ./internal/statedir/ -v`
Expected: FAIL — package does not exist.

- [ ] **Step 3: Implement**

```go
// Package statedir owns where the hub keeps state that must survive a restart.
//
// One owner, because there are now two such files (the send log and the hidden
// set) and a second copy of the XDG rules is a second place to get them wrong.
package statedir

import (
	"os"
	"path/filepath"
)

// Path returns the absolute path for a named state file.
//
// The last-resort return is the bare name, i.e. the current directory: a hub
// that cannot find a home directory still runs, it just keeps state where it
// was started. That matches what history.DefaultPath did before this package.
func Path(name string) string {
	base := os.Getenv("XDG_STATE_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return name
		}
		base = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(base, "tmux-hub", name)
}
```

- [ ] **Step 4: Delegate from history**

```go
func DefaultPath() string { return statedir.Path("history.jsonl") }
```

- [ ] **Step 5: Run the tests**

Run: `go test ./internal/statedir/ ./internal/history/ -v -count=1`
Expected: PASS — including the existing `history` tests, unchanged. If a history test asserted the literal old path, it must still pass; if it does not, the refactor changed behaviour and is wrong.

- [ ] **Step 6: Commit**

```bash
git add internal/statedir internal/history/history.go
git commit -m "statedir: one owner for the XDG state path"
```

---

### Task 4: `internal/hide` — the hidden set

**Files:**
- Create: `internal/hide/hide.go`, `internal/hide/hide_test.go`

**Interfaces:**
- Consumes: `registry.Pane` (for `KeyOf`), `state.State`, `statedir.Path`.
- Produces:
  - `type Key struct { Host, Session, Window string; Index int; Start string }`
  - `func KeyOf(p registry.Pane) Key`
  - `func Open(path string) (*Set, error)`
  - `func (s *Set) Marked(k Key) bool`
  - `func (s *Set) Hidden(p registry.Pane) bool`
  - `func (s *Set) Toggle(p registry.Pane) error`
  - `func (s *Set) Count() int`
  - `func DefaultPath() string`

- [ ] **Step 1: Write the failing tests**

```go
func TestMarkedSurvivesReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hidden.json")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	p := pane("local", "logs", "tail", 0, `"tail -f app.log"`, state.Works)
	if err := s.Toggle(p); err != nil {
		t.Fatal(err)
	}

	// Reopened from disk, with no help from the first Set.
	s2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if !s2.Marked(KeyOf(p)) {
		t.Fatal("the mark did not survive a reopen")
	}
}

func TestABlockedPaneIsShownEvenWhenMarked(t *testing.T) {
	// The resurface rule, and it is load-bearing: it is what makes a wrong key
	// match unable to lose work (docs/design.md §18). Removing it must go red.
	s := must(Open(filepath.Join(t.TempDir(), "h.json")))
	p := pane("local", "logs", "tail", 0, `"tail -f app.log"`, state.Works)
	if err := s.Toggle(p); err != nil {
		t.Fatal(err)
	}
	if !s.Hidden(p) {
		t.Fatal("a marked working pane should be hidden")
	}

	blocked := p
	blocked.ClassifiedState = state.Needs
	if s.Hidden(blocked) {
		t.Fatal("a marked pane WAITING ON THE USER must be shown")
	}
	if !s.Marked(KeyOf(blocked)) {
		t.Fatal("resurfacing must not clear the mark — it is temporary, not an unhide")
	}
}

func TestAMarkDoesNotMatchADifferentStartCommand(t *testing.T) {
	// The corroborator earning its place: same host, session, window and index,
	// different start command, therefore a different pane.
	s := must(Open(filepath.Join(t.TempDir(), "h.json")))
	noise := pane("local", "logs", "tail", 0, `"tail -f app.log"`, state.Works)
	if err := s.Toggle(noise); err != nil {
		t.Fatal(err)
	}

	agent := noise
	agent.StartCommand = `"claude --session-id 7007b23f"`
	if s.Hidden(agent) {
		t.Fatal("a pane that only shares the PATH must not inherit the mark")
	}
}

func TestAMalformedFileYieldsAnEmptySetNotAnError(t *testing.T) {
	// Fail toward visible: an unreadable set shows everything, which is
	// annoying. Refusing to start, or guessing, is worse.
	path := filepath.Join(t.TempDir(), "hidden.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open on a malformed file must not fail: %v", err)
	}
	if s.Count() != 0 {
		t.Fatalf("Count = %d, want 0", s.Count())
	}
	if s.Warning() == "" {
		t.Fatal("a dropped set must say so — a silent empty set is indistinguishable from an empty one")
	}
}

func TestToggleTwiceUnhides(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hidden.json")
	s := must(Open(path))
	p := pane("local", "logs", "tail", 0, `"tail -f app.log"`, state.Works)
	if err := s.Toggle(p); err != nil {
		t.Fatal(err)
	}
	if err := s.Toggle(p); err != nil {
		t.Fatal(err)
	}
	if s.Marked(KeyOf(p)) {
		t.Fatal("the second toggle must unhide")
	}
	s2 := must(Open(path))
	if s2.Marked(KeyOf(p)) {
		t.Fatal("the unhide must persist too")
	}
}
```

Write `pane(host, session, window string, index int, start string, st state.State) registry.Pane` and `must[T]` as local helpers.

- [ ] **Step 2: Run and watch them fail**

Run: `go test ./internal/hide/ -v`
Expected: FAIL — package does not exist.

- [ ] **Step 3: Implement**

```go
// Package hide keeps the set of panes the user never wants to see.
//
// A host accumulates panes that are not agents and never will be: a log tail, a
// htop, a build watcher. They are permanent noise, so hiding is persistent —
// "never show me this again", not "not right now".
package hide

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/DawnBreather/tmux-hub/internal/registry"
	"github.com/DawnBreather/tmux-hub/internal/state"
	"github.com/DawnBreather/tmux-hub/internal/statedir"
)

// Key identifies a hidden pane across hub restarts.
//
// NOT the pane id. `%12` is monotonic and never reused within one tmux server's
// life (measured: kill %3, the next pane is %4), which makes it an exact key
// while the hub runs and the WRONG key on disk — a restarted server numbers from
// %0 again, so a persisted %3 names a different pane in the next generation.
//
// Start is #{pane_start_command}, the corroborator, because it is immutable for
// the life of the pane. #{pane_current_command} is not usable: it walks
// zsh → claude → zsh, so a mark taken while it read `zsh` would stop matching
// the moment the agent started.
//
// TWO honest limits, both measured. Start is the EMPTY string for a pane created
// with no command — the plain shell, which is the commonest thing anyone hides —
// so those keys carry no corroboration and rest on position alone. And an
// interactive agent's start command is `"claude --resume <uuid>"`, which is
// unique to one launch, so a mark on an agent pane cannot match a future one.
// Neither is a defect to fix here: the resurface rule in Hidden is the safety
// net, and it does not depend on the corroborator at all.
type Key struct {
	Host    string `json:"host"`
	Session string `json:"session"`
	Window  string `json:"window"`
	Index   int    `json:"index"`
	Start   string `json:"start"`
}

// KeyOf is the only place a pane becomes a Key, so the fields that make up the
// key cannot drift between the writer and the reader.
func KeyOf(p registry.Pane) Key {
	return Key{Host: p.Host, Session: p.Session, Window: p.Window, Index: p.Index, Start: p.StartCommand}
}

// Set is the persisted hidden set.
type Set struct {
	mu      sync.Mutex
	path    string
	marked  map[Key]bool
	warning string
}

// DefaultPath is where the set lives, beside the send log.
func DefaultPath() string { return statedir.Path("hidden.json") }

// Open reads the set, and never fails because of its contents.
//
// A missing file is an empty set. A malformed or unreadable one is an empty set
// plus a Warning the dashboard shows: fail toward VISIBLE, because showing a
// noisy pane annoys and hiding a live agent loses work.
func Open(path string) (*Set, error) {
	s := &Set{path: path, marked: map[Key]bool{}}
	raw, err := os.ReadFile(path)
	switch {
	case os.IsNotExist(err):
		return s, nil
	case err != nil:
		s.warning = fmt.Sprintf("hidden set unreadable (%v) — showing everything", err)
		return s, nil
	}
	var keys []Key
	if err := json.Unmarshal(raw, &keys); err != nil {
		s.warning = fmt.Sprintf("hidden set malformed (%v) — showing everything", err)
		return s, nil
	}
	for _, k := range keys {
		s.marked[k] = true
	}
	return s, nil
}

// Marked is the raw answer: did the user hide this pane?
func (s *Set) Marked(k Key) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.marked[k]
}

// Hidden is the EFFECTIVE answer, and the one the dashboard uses.
//
// A marked pane that is waiting on the user is shown anyway. This is not a
// courtesy — it is what makes a wrong key match safe. A key can only mis-match a
// pane that shares a host, session, window, index AND start command with the one
// the user hid; even then the mis-matched pane cannot be hidden while it is
// BLOCKED, which is the only state where hiding loses work. Do not remove this
// without reading docs/design.md §18.
func (s *Set) Hidden(p registry.Pane) bool {
	if p.ClassifiedState == state.Needs {
		return false
	}
	return s.Marked(KeyOf(p))
}

// Toggle flips a pane's mark and writes the set to disk immediately, so a hub
// killed without a clean exit does not lose the user's last decision.
func (s *Set) Toggle(p registry.Pane) error {
	k := KeyOf(p)
	s.mu.Lock()
	if s.marked[k] {
		delete(s.marked, k)
	} else {
		s.marked[k] = true
	}
	keys := make([]Key, 0, len(s.marked))
	for k := range s.marked {
		keys = append(keys, k)
	}
	path := s.path
	s.mu.Unlock()

	sortKeys(keys) // stable file: a diff of hidden.json should show only real changes
	return writeAtomic(path, keys)
}

// Count is how many marks exist, for the footer.
func (s *Set) Count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.marked)
}

// Warning is non-empty when the set on disk could not be used.
func (s *Set) Warning() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.warning
}

// writeAtomic writes through a temp file in the same directory, so a crash
// mid-write leaves the previous set rather than a truncated one.
func writeAtomic(path string, keys []Key) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(keys, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".hidden-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
```

Write `sortKeys` with `sort.Slice` over the five fields in declaration order.

- [ ] **Step 4: Run the tests**

Run: `go test -race ./internal/hide/ -v -count=1`
Expected: PASS, all five.

- [ ] **Step 5: Calibrate the resurface rule**

Delete the `if p.ClassifiedState == state.Needs { return false }` lines from `Hidden`, run `go test ./internal/hide/`, and confirm `TestABlockedPaneIsShownEvenWhenMarked` goes RED. Restore it. A guarantee whose removal leaves the suite green is not guarded.

- [ ] **Step 6: Wire it into main in the same commit — the suite is RED until you do**

Not optional and not deferrable to Task 7. `cmd/tmux-hub`'s `TestEveryPackageIsReachableFromMain` fails the moment a package exists without a production caller; verified while writing this plan:

```
wiring_test.go:96: github.com/DawnBreather/tmux-hub/internal/hide is not reachable
                  from cmd/tmux-hub — it is built, tested and not wired in
```

That floor exists because this project once shipped a fully-built, fully-reviewed, entirely unwired write path. Leaving it red across three tasks would train the next implementer to ignore it, so the minimum wiring lands here:

```go
// main.go — the flags and the warning are Task 7's; this is only enough that the
// package has a production caller and the floor stays green.
hidden, err := hide.Open(hide.DefaultPath())
if err != nil {
	fmt.Fprintln(os.Stderr, "tmux-hub: cannot open the hidden set:", err)
	os.Exit(1)
}
```

and `ui.Run` takes it as a parameter it does not yet read. An unused parameter is legal Go and honest: the seam exists before the behaviour, which is the order the floor is asking for.

- [ ] **Step 7: Run the full suite, including the floor**

Run: `go test -race ./... -count=1`
Expected: PASS, every package — `cmd/tmux-hub` included. If the floor is still red, Step 6 is incomplete.

- [ ] **Step 8: Commit**

```bash
git add internal/hide cmd/tmux-hub/main.go internal/ui/model.go
git commit -m "hide: persisted hidden set, keyed on path plus start command"
```

---

### Task 5: Wire hiding into the dashboard

**Files:**
- Modify: `internal/ui/model.go`, `internal/ui/render.go`, `internal/ui/selection.go`
- Test: `internal/ui/render_test.go`, `internal/ui/model_test.go`

**Interfaces:**
- Consumes: `hide.Set` from Task 4.
- Produces: `Model.hidden *hide.Set`, `Model.showHidden bool`; `visibleRows(m Model) []registry.Pane` as the ONE place the filter is applied.

- [ ] **Step 1: Write the failing render tests**

```go
func TestAHiddenPaneIsNotInTheView(t *testing.T) {
	m := modelWith(t, pane("local", "logs", "tail", 0, `"tail -f app.log"`, state.Works),
		pane("local", "api", "claude", 0, `"claude"`, state.Works))
	hidePane(t, &m, 0)

	out := m.View()
	if strings.Contains(out, "logs") {
		t.Fatalf("the hidden pane is still drawn:\n%s", out)
	}
	if !strings.Contains(out, "api") {
		t.Fatalf("the visible pane vanished:\n%s", out)
	}
}

func TestTheFooterCountsHiddenPanesAndTheBlockedOnesAmongThem() {
	// A count with no breakdown reads as "nothing to see". The breakdown is
	// what tells the user a hidden pane is waiting for them.
	...
	if !strings.Contains(out, "скрыто 2") { ... }
}

func TestAResurfacedPaneIsDrawnAndMarkedAsResurfaced(t *testing.T) {
	m := modelWith(t, pane("local", "logs", "tail", 0, `"tail -f app.log"`, state.Works))
	hidePane(t, &m, 0)
	m = withState(m, 0, state.Needs)

	out := m.View()
	if !strings.Contains(out, "logs") {
		t.Fatalf("a blocked hidden pane must be drawn:\n%s", out)
	}
	if !strings.Contains(out, resurfacedMark) {
		t.Fatalf("a resurfaced row must say why it is back:\n%s", out)
	}
}

func TestShowHiddenRevealsThemWithoutClearingTheMarks(t *testing.T) { ... }
```

Every assertion is on the string `m.View()` returns. This project has already shipped a whole invisible UI mode because its tests asserted on a render helper nobody called — assert on `View()` or the test proves nothing.

- [ ] **Step 2: Run and watch them fail**

Run: `go test ./internal/ui/ -run Hidden -v`
Expected: FAIL.

- [ ] **Step 3: Implement the single filter point**

Add to `Model`: `hidden *hide.Set`, `showHidden bool`. Then exactly one function decides visibility, and every consumer — the render loop, the cursor, `VisiblePanes` for `A`, the inbox — goes through it:

```go
// visibleRows is the ONE place the hidden set is applied.
//
// One owner because the alternative has already bitten this project: `A` was a
// logical no-op for a whole review cycle because two functions disagreed about
// what "visible" meant. If you need a filtered list, call this.
func (m Model) visibleRows() []registry.Pane {
	if m.hidden == nil || m.showHidden {
		return m.rows
	}
	out := make([]registry.Pane, 0, len(m.rows))
	for _, p := range m.rows {
		if !m.hidden.Hidden(p) {
			out = append(out, p)
		}
	}
	return out
}
```

Adapt `m.rows` to whatever the model actually calls its pane slice. Then replace every other place that iterates panes for display or selection with a call to `visibleRows`, and `grep` the package for the old field name to prove none is left.

- [ ] **Step 4: The footer**

Render `скрыто N` when `N > 0`, and `скрыто N · из них ждут ввода: B` when `B > 0`. `B` counts marked panes whose state is `Needs` — which are, by the resurface rule, currently drawn, so the footer and the list agree rather than contradicting.

- [ ] **Step 5: Run the tests, then the suite**

Run: `go test -race ./internal/ui/ -count=1` then `go test -race ./... -count=1`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/ui
git commit -m "ui: filter hidden panes through one owner, count them in the footer"
```

---

### Task 6: The hide keys

**Files:**
- Modify: `internal/ui/model.go`, `README.md`
- Test: `internal/ui/model_test.go`

**Interfaces:**
- Consumes: `Model.hidden`, `Model.showHidden`, `visibleRows`.
- Produces: `x` hides the pane under the cursor (or every selected pane), `X` toggles showing hidden panes.

`x` and `X` are free: browse mode currently binds `q`, `ctrl+c`, `j`/`down`, `k`/`up`, `a`, `space`, `A`, `C`, `i`, `!`, `h`.

- [ ] **Step 1: Write the failing tests**

```go
func TestXHidesThePaneUnderTheCursor(t *testing.T) { ... }

func TestXHidesEverySelectedPaneWhenThereIsASelection(t *testing.T) {
	// The selection is the user's stated subject. Hiding one row while three are
	// selected would be the same "which did I mean" ambiguity the send path
	// already resolves this way.
	...
}

func TestHidingMovesTheCursorToAStillVisibleRow(t *testing.T) {
	// Hiding the last row must not leave the cursor past the end — the crash
	// this project already fixed once in the viewport.
	...
}

func TestXOnAnEmptyDashboardIsANoOp(t *testing.T) { ... }
```

- [ ] **Step 2: Run and watch them fail**

Run: `go test ./internal/ui/ -run 'XHides|HidingMoves' -v`
Expected: FAIL.

- [ ] **Step 3: Implement**

In the browse-mode key switch:

```go
case "x":
    m = m.hideSubject()
case "X":
    m.showHidden = !m.showHidden
    m = m.clampCursor()
```

`hideSubject` toggles the marks for the selection if there is one, else for the row under the cursor, then clamps the cursor into `visibleRows`. A `Toggle` error becomes the status line's message — a hidden set that cannot be written is worth saying out loud, and it carries the fix (the path).

- [ ] **Step 4: Run the tests**

Run: `go test -race ./internal/ui/ -count=1`
Expected: PASS.

- [ ] **Step 5: Document the keys**

Add `x` and `X` to the key table in `README.md` and in §10 of `docs/design.md`.

- [ ] **Step 6: Commit**

```bash
git add internal/ui README.md docs/design.md
git commit -m "ui: x hides, X reveals"
```

---

### Task 7: The hidden-set flags and its warning

Task 4 Step 6 already gave `hide.Open` a production call site, because the wiring floor refuses to be red across three tasks. This task adds what a user actually touches: the flags, and the warning surfacing when the set on disk could not be read.

**Files:**
- Modify: `cmd/tmux-hub/main.go`, `internal/ui/model.go` (the `Run` signature)
- Test: `cmd/tmux-hub/wiring_test.go`

**Interfaces:**
- Consumes: `hide.Open`, `hide.DefaultPath`.
- Produces: `--hidden <path>`, `--no-hide`; `ui.Run` takes `*hide.Set`.

- [ ] **Step 1: Extend the wiring floor first**

`cmd/tmux-hub/wiring_test.go` already asserts the write path has production call sites, because this project shipped a fully-built, fully-tested and entirely unwired write path once. Add `hide.Open` to the constructors it demands a non-test caller for:

```go
func TestHidingHasProductionCallSites(t *testing.T) {
	// Not a formality: the broadcast write path was built, reviewed nine times
	// and never wired, because every ordinary test CONSTRUCTS the thing it
	// tests. This one asks whether anything constructs it.
	for _, ctor := range []string{"hide.Open", "hide.DefaultPath"} {
		if n := nonTestCallers(t, ctor); n == 0 {
			t.Errorf("%s has no production call site", ctor)
		}
	}
}
```

Reuse the existing helper in that file rather than writing a second one.

- [ ] **Step 2: Run it and watch it fail**

Run: `go test ./cmd/tmux-hub/ -run HidingHasProduction -v`
Expected: FAIL — `hide.Open has no production call site`.

- [ ] **Step 3: Wire it**

```go
hiddenPath := flag.String("hidden", hide.DefaultPath(),
	"the persisted set of panes to keep out of the dashboard")
noHide := flag.Bool("no-hide", false, "show every pane, ignoring the hidden set")
```

```go
// Opened HERE, before the TUI starts, for the same reason the send log is: a
// path that cannot be read is a startup message rather than a surprise later.
// Open never fails on CONTENT — a malformed set is an empty set plus a warning
// (docs/design.md §18) — so an error here is a real filesystem problem.
var hidden *hide.Set
if !*noHide {
	h, err := hide.Open(*hiddenPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "tmux-hub: cannot open the hidden set:", err)
		os.Exit(1)
	}
	hidden = h
}
```

Pass it to `ui.Run`. Surface `hidden.Warning()` in the status line on the first frame if it is non-empty.

- [ ] **Step 4: Run the tests and build**

Run: `go build ./... && go test -race ./... -count=1`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add cmd/tmux-hub internal/ui
git commit -m "hub: open the hidden set at startup, --no-hide to ignore it"
```

---

### Task 8: The seam learns the lifecycle targets

**This task exists because the plan was verified by compiling it.** Extracting Task 9's verbs and running `tmux.Validate` over the argv each one builds refused four of the seven:

```
set -w        builds argv the seam REFUSES: tmux: -t value is not a pane id: "@4"
new-window    builds argv the seam REFUSES: tmux: -t value is not a pane id: "api"
kill-window   builds argv the seam REFUSES: tmux: -t value is not a pane id: "@4"
kill-session  builds argv the seam REFUSES: tmux: -t value is not a pane id: "$1"
```

`validateArg` (`internal/tmux/run.go:130`) requires every `-t` value to match `^%[0-9]+$`, and its comment says exactly why, with the measurement: *"An empty or non-pane `-t` fails OPEN: measured, a send-keys whose -t value is the empty string returns rc=0 and delivers to the server's current pane — the one the user last touched."* So the rule cannot simply be loosened: `kill-window -t ''` would destroy whatever window the user is looking at, which is a worse fail-open than the one the rule was written for.

**Files:**
- Modify: `internal/tmux/run.go`
- Test: `internal/tmux/run_test.go`

**Interfaces:**
- Consumes: `Validate`, `validateArg`, `paneID` as they are.
- Produces: `windowID`, `sessionID` regexps and `shapeFor(toks []string) *regexp.Regexp`. `ErrBadTarget` is unchanged, and its message gains the shape that was expected.

- [ ] **Step 1: Write the failing tests — the accepting half**

```go
func TestValidateAcceptsTheLIFECYCLETargets(t *testing.T) {
	// Verified against real tmux 3.7b: every one of these forms works.
	//   new-window -t $0            → created %1 in that session
	//   set -w -t @1 remain-on-exit → rc=0, value reads back `on`
	//   kill-window -t @1           → rc=0
	//   kill-session -t $1          → rc=0
	for _, argv := range [][]string{
		{"new-window", "-t", "$0", "-c", "/srv/api", "-P", "-F", "#{pane_id}", "claude --session-id abc"},
		{"kill-window", "-t", "@4"},
		{"kill-session", "-t", "$1"},
		{"set", "-w", "-t", "@4", "remain-on-exit", "on"},
		{"new-session", "-d", "-s", "proj", "-c", "/srv", "-P", "-F", "#{pane_id}", "claude"},
		{"respawn-pane", "-k", "-t", "%3", "claude --resume abc"},
	} {
		if err := Validate(argv); err != nil {
			t.Errorf("Validate(%q) = %v, want nil", argv, err)
		}
	}
}
```

- [ ] **Step 2: Write the failing tests — the half that proves nothing leaked**

This is the more important half. The loosening must not reach a single verb that writes into a pane.

```go
func TestTheWRITEVerbsStillDemandAPaneID(t *testing.T) {
	// The guard the loosening must not touch. A send addressed to a WINDOW
	// lands in that window's active pane — not the pane the user verified —
	// which is precisely the write-into-the-wrong-agent this branch exists to
	// make impossible.
	for _, argv := range [][]string{
		{"send-keys", "-t", "@4", "-l", "hello"},
		{"send-keys", "-t", "$1", "-l", "hello"},
		{"paste-buffer", "-d", "-p", "-r", "-b", "buf", "-t", "@4"},
		{"capture-pane", "-p", "-t", "@4"},
		{"display", "-p", "-t", "$1", "#{pane_id}"},
		{"respawn-pane", "-k", "-t", "@4"},
	} {
		if err := Validate(argv); !errors.Is(err, ErrBadTarget) {
			t.Errorf("Validate(%q) = %v, want ErrBadTarget", argv, err)
		}
	}
}

func TestSetPStillDemandAPaneIDWhileSetWDemandsAWindowID(t *testing.T) {
	// `set` is the one verb used at two scopes: the stamper writes `set -p` on a
	// PANE (broadcast/stamp.go), lifecycle writes `set -w` on a WINDOW. Keying
	// the shape on the verb alone would let `set -p -t @4` through, and a
	// pane-scoped option written against a window target is the stamp landing
	// somewhere nobody checked.
	if err := Validate([]string{"set", "-p", "-t", "@4", "@hub_x", "1"}); !errors.Is(err, ErrBadTarget) {
		t.Errorf("set -p with a window target must be refused, got %v", err)
	}
	if err := Validate([]string{"set", "-p", "-t", "%3", "@hub_x", "1"}); err != nil {
		t.Errorf("set -p with a pane id must still pass: %v", err)
	}
	if err := Validate([]string{"set", "-w", "-t", "%3", "remain-on-exit", "on"}); !errors.Is(err, ErrBadTarget) {
		t.Errorf("set -w with a pane target must be refused, got %v", err)
	}
}

func TestAnEmptyTargetIsRefusedForEVERYVerb(t *testing.T) {
	// The original fail-open, and it must stay closed at every shape: an empty
	// -t means "whatever is current", so `kill-window -t ''` destroys the
	// window the user is looking at.
	for _, argv := range [][]string{
		{"new-window", "-t", ""},
		{"kill-window", "-t", ""},
		{"kill-session", "-t", ""},
		{"set", "-w", "-t", "", "remain-on-exit", "on"},
		{"send-keys", "-t", "", "-l", "x"},
	} {
		if err := Validate(argv); !errors.Is(err, ErrBadTarget) {
			t.Errorf("Validate(%q) = %v, want ErrBadTarget", argv, err)
		}
	}
}

func TestASessionNAMEIsRefusedWhereAnIDIsRequired(t *testing.T) {
	// Names are refused everywhere, deliberately: an id cannot resolve to "the
	// current one", and the registry already carries SessionID ($N) and
	// WindowID (@N) for every pane, so the hub never needs to pass a name.
	if err := Validate([]string{"new-window", "-t", "api"}); !errors.Is(err, ErrBadTarget) {
		t.Errorf("a session NAME must be refused, got %v", err)
	}
}

func TestTheErrorSaysWHICHSHAPEWasExpected(t *testing.T) {
	// An error that carries its fix. `-t value is not a pane id: "@4"` is
	// actively misleading for kill-window, where a pane id is the wrong answer.
	err := Validate([]string{"kill-window", "-t", "%3"})
	if err == nil {
		t.Fatal("want an error")
	}
	if !strings.Contains(err.Error(), "window id") {
		t.Fatalf("the message must name the expected shape, got %q", err)
	}
}
```

- [ ] **Step 3: Run them and watch them fail**

Run: `go test ./internal/tmux/ -run 'LIFECYCLETargets|WRITEVerbs|SetP|EmptyTarget|SessionNAME|WHICHSHAPE' -v`
Expected: FAIL — `TestValidateAcceptsTheLIFECYCLETargets` on all four lifecycle forms, `TestTheErrorSaysWHICHSHAPE` on the message. The others should already pass, which is the point: they are the regression net for the change, not new behaviour.

- [ ] **Step 4: Implement the per-verb shape**

```go
var (
	paneID    = regexp.MustCompile(`^%[0-9]+$`)
	windowID  = regexp.MustCompile(`^@[0-9]+$`)
	sessionID = regexp.MustCompile(`^\$[0-9]+$`)
)

// shapeFor says what kind of tmux id a command's -t value must name.
//
// The constraint belongs to the VERB, not to the flag. The rule it refines
// exists for a measured reason — a send-keys whose -t value is the empty string
// returns rc=0 and delivers to the server's CURRENT pane, the one the user last
// touched — so a non-pane -t fails OPEN and Validate demanded `%N` everywhere.
// Lifecycle verbs legitimately address a session or a window, and refusing them
// is what a compile of this plan's own verbs discovered.
//
// Nothing here gets weaker. Every shape is an ID, never a name, because an id
// cannot resolve to "the current one": `kill-window -t @4` either finds @4 or
// fails, while `kill-window -t ''` would destroy whatever window the user is
// looking at. The registry already carries SessionID (`$N`) and WindowID (`@N`)
// for every pane, so the hub never needs to pass a name.
//
// And the DEFAULT is the old rule verbatim, so no existing call site can change
// behaviour by omission — a verb absent from this switch is still pane-only.
func shapeFor(toks []string) (*regexp.Regexp, string) {
	if len(toks) == 0 {
		return paneID, "pane id"
	}
	switch toks[0] {
	case "new-window":
		return sessionID, "session id"
	case "kill-session":
		return sessionID, "session id"
	case "kill-window":
		return windowID, "window id"
	case "set", "set-option":
		// `set` is the one verb used at two scopes: the stamper writes `set -p`
		// on a pane, lifecycle writes `set -w` on a window. Keying on the verb
		// alone would let `set -p -t @4` through, and a pane-scoped option
		// written against a window target is a stamp landing somewhere nobody
		// checked.
		for _, tk := range toks[1:] {
			switch unquote(tk) {
			case "-w":
				return windowID, "window id"
			case "-p":
				return paneID, "pane id"
			}
		}
		return paneID, "pane id"
	}
	return paneID, "pane id"
}
```

Then `validateArg` takes the shape for the command it is part of. Both call sites in `validateArg` — the single-token branch, whose command is `args[0]`, and the sub-command branch, whose command is `toks[0]` — must use the same function; that is the two-level reading the existing comment already describes, and getting the level wrong has already broken this validator twice. `Validate` therefore passes `args` down so the single-token branch knows its verb:

```go
func Validate(args []string) error {
	shape, want := shapeFor(args)
	for i, a := range args {
		// … forbiddenVars loop unchanged …
		prev := ""
		if i > 0 {
			prev = args[i-1]
		}
		if err := validateArg(a, prev, shape, want); err != nil {
			return err
		}
	}
	// … the trailing -t check, unchanged …
}
```

In the sub-command branch of `validateArg`, call `shapeFor(toks)` locally rather than using the outer shape — a sub-command chain carries its own verb, so `'paste-buffer … -t %12 ; display …'` must be judged as `paste-buffer`, not as whatever the outer argv began with.

Widen the `ErrBadTarget` message to name the shape: `fmt.Errorf("%w: %q is not a %s", ErrBadTarget, bare, want)`.

- [ ] **Step 5: Reword the sentinel, because its own text hardcodes the old shape**

Running the above produced `tmux: -t value is not a pane id: "api" is not a session id` — the sentinel says "pane id" and the wrapper says "session id", in one message, contradicting each other. `ErrBadTarget` must become shape-neutral:

```go
// ErrBadTarget is a -t whose value is not an id of the shape its verb requires
// (see shapeFor). It is always a hub defect, …
ErrBadTarget = errors.New("tmux: -t value has the wrong shape")
```

Verified before changing it: nothing in the tree asserts on the old string (`grep -rn 'is not a pane id' --include='*.go' .` returns only the declaration and its comment), so this is safe. The message then reads `tmux: -t value has the wrong shape: "api" is not a session id`.

- [ ] **Step 6: Run the whole tmux package**

Run: `go test -race ./internal/tmux/ -count=1`
Expected: PASS — **47 tests**, including every pre-existing one (`TestValidateRefusesATargetThatIsNotAPaneID`, `TestValidateAllowsPaneIDs`, `TestValidateAcceptsWellFormedTargets`, `TestValidateStillRefusesAPercentInATemplate`, `TestRunValidatesBeforeExecuting`). This exact count was reached while verifying this plan. If any pre-existing test goes red, the default branch is not the old rule and the change is wrong.

- [ ] **Step 7: Calibrate — and note that `send-keys` is not in the switch**

There is no `send-keys` case to mutate: every write and read verb reaches the shape through the DEFAULT branch, which is the point of the design. So the mutation that tests the guard is on the default itself. All three below were run while verifying this plan; each is caught by the test written for it, and each COMPILES — a mutation that only breaks the build is caught by the compiler, not the test, and proves nothing about coverage.

| Mutation | Must go red |
|---|---|
| default branch returns `windowID, "window id"` | `TestTheWRITEVerbsStillDemandAPaneID` — plus 16 read-path tests, which is worth seeing: the whole package notices when the default rule moves |
| `case "set", "set-option":` never matches | `TestSetPStillDemandAPaneIDWhileSetWDemandsAWindowID`, `TestValidateAcceptsTheLIFECYCLETargets` |
| `sessionID` becomes `^.+$` | `TestASessionNAMEIsRefusedWhereAnIDIsRequired` |

Run each, confirm the named test goes red, restore, and confirm the package is green again before moving on.

- [ ] **Step 8: Run the full suite and the e2e suite**

Run: `go test -race ./... -count=1 && go test -tags e2e ./internal/e2e/ -count=1`
Expected: PASS. The broadcast write path is the code most exposed to a mistake here, and its e2e cases send into real panes. Verified while writing this plan: with the change applied, 10 of 12 packages were `ok` and the only two failures were the wiring floor (the new packages were deliberately unwired in the scratch tree) and a scratch test still passing a session NAME — no pre-existing test moved.

- [ ] **Step 9: Commit**

```bash
git add internal/tmux/run.go internal/tmux/run_test.go
git commit -m "tmux: -t shape is per-verb, ids only, write verbs unchanged"
```

---

### Task 9: `internal/tmux/lifecycle.go` — the verbs

**Files:**
- Create: `internal/tmux/lifecycle.go`, `internal/tmux/lifecycle_test.go`

**Interfaces:**
- Consumes: `tmux.Runner`, `tmux.Target`, `tmux.Validate`.
- Produces:
  - `func NewWindow(ctx context.Context, r Runner, t Target, sessionID, cwd, cmd string) (paneID string, err error)` — sessionID is `$N`, never a name (Task 8)
  - `func NewSession(ctx context.Context, r Runner, t Target, name, cwd, cmd string) (paneID string, err error)`
  - `func RespawnPane(ctx context.Context, r Runner, t Target, paneID, cmd string) error`
  - `func KillWindow(ctx context.Context, r Runner, t Target, windowID string) error`
  - `func KillSession(ctx context.Context, r Runner, t Target, sessionID string) error`
  - `func SetWindowOption(ctx context.Context, r Runner, t Target, windowID, name, value string) error`
  - `func PaneAlive(ctx context.Context, r Runner, t Target, paneID string) (bool, error)`

- [ ] **Step 1: Write the failing tests**

```go
func TestNewWindowReturnsTheNewPaneID(t *testing.T) {
	// -P -F is what makes a created pane knowable without a search. The whole
	// identity-at-birth argument (docs/design.md §19) rests on this one string.
	r := &fakeRunner{stdout: "%7\n"}
	id, err := NewWindow(context.Background(), r, target(), "$0", "/srv/api", "claude --session-id abc")
	if err != nil {
		t.Fatal(err)
	}
	if id != "%7" {
		t.Fatalf("paneID = %q, want %q", id, "%7")
	}
	assertArgv(t, r.last, "new-window", "-t", "$0", "-c", "/srv/api", "-P", "-F", "#{pane_id}", "claude --session-id abc")
}

func TestNewWindowKeepsCWDAsItsOwnArgvElement(t *testing.T) {
	// Measured: `-c "/a/dir with space"` works because it is one argv element.
	// Joining it into the command string would need quoting and would be a
	// class of bug rather than an instance.
	r := &fakeRunner{stdout: "%1\n"}
	if _, err := NewWindow(context.Background(), r, target(), "$0", "/a/dir with space", "claude"); err != nil {
		t.Fatal(err)
	}
	for i, a := range r.last {
		if a == "-c" {
			if got := r.last[i+1]; got != "/a/dir with space" {
				t.Fatalf("cwd argv element = %q", got)
			}
			return
		}
	}
	t.Fatal("no -c in argv")
}

func TestNewWindowOmitsCWDWhenEmpty(t *testing.T) {
	// `-c ""` is not "no directory" to tmux. Omit the flag instead.
	...
}

func TestPaneAliveReadsEMPTINESSNotAnError(t *testing.T) {
	// Measured: display -p -t %999 '#{pane_id}' returns rc=0 and an EMPTY
	// string — tmux does not error with no client attached. So a check that
	// waits for a non-zero exit code concludes every pane is alive, forever.
	dead := &fakeRunner{stdout: "\n"}
	alive, err := PaneAlive(context.Background(), dead, target(), "%999")
	if err != nil {
		t.Fatal(err)
	}
	if alive {
		t.Fatal("an empty display result means the pane is GONE")
	}

	live := &fakeRunner{stdout: "%3\n"}
	alive, err = PaneAlive(context.Background(), live, target(), "%3")
	if err != nil {
		t.Fatal(err)
	}
	if !alive {
		t.Fatal("a pane that echoes its own id is alive")
	}
}

func TestPaneAliveRejectsAnEchoOfADifferentPane(t *testing.T) {
	// If tmux ever falls back to another pane, "non-empty" is not enough.
	r := &fakeRunner{stdout: "%4\n"}
	alive, err := PaneAlive(context.Background(), r, target(), "%3")
	if err != nil {
		t.Fatal(err)
	}
	if alive {
		t.Fatal("%4 is not evidence about %3")
	}
}

func TestANONZEROTmuxExitIsAFailureCarryingTMUXOWNMESSAGE(t *testing.T) {
	// THE contract of this package, and it is a trap: execRunner.Run returns a
	// NIL error when tmux exits non-zero (run.go:201 — `res.RC = ee.ExitCode();
	// return res, nil`). Every existing caller checks res.RC by hand
	// (assert.go:54, labels.go:54, delta.go:132, capture.go:129,
	// stamp.go:51). A verb that checks only `err` therefore treats a REFUSED
	// new-window as a success and then fails on the empty pane id, hiding the
	// one message the user needs — `can't find directory /nope`.
	r := &fakeRunner{rc: 1, stderr: "can't find directory /nope"}
	_, err := NewWindow(context.Background(), r, target(), "$0", "/nope", "claude")
	if err == nil {
		t.Fatal("a non-zero tmux exit must be an error")
	}
	if !strings.Contains(err.Error(), "can't find directory /nope") {
		t.Fatalf("the error must carry tmux's own message, got %q", err)
	}
}

func TestEveryVerbBuildsArgvTheSEAMACCEPTS(t *testing.T) {
	// NOT "the verbs call Validate" — they cannot be tested for that here,
	// because Validate lives INSIDE execRunner.Run (run.go:174) and a fake
	// runner bypasses it by construction. The real hazard is the opposite
	// direction, and this project has already been bitten by it: a tightened
	// Validate once rejected every command that targeted a pane, including
	// seven working call sites. So assert that each verb's argv is ACCEPTED.
	cases := map[string][]string{
		"new-window":    argvOf(t, func(r Runner) { NewWindow(context.Background(), r, target(), "$0", "/srv/api", "claude --session-id abc") }),
		"new-session":   argvOf(t, func(r Runner) { NewSession(context.Background(), r, target(), "proj", "/srv", "claude") }),
		"respawn-pane":  argvOf(t, func(r Runner) { RespawnPane(context.Background(), r, target(), "%3", "claude --resume abc") }),
		"kill-window":   argvOf(t, func(r Runner) { KillWindow(context.Background(), r, target(), "@4") }),
		"kill-session":  argvOf(t, func(r Runner) { KillSession(context.Background(), r, target(), "$1") }),
		"set -w":        argvOf(t, func(r Runner) { SetWindowOption(context.Background(), r, target(), "@4", "remain-on-exit", "on") }),
		"display alive": argvOf(t, func(r Runner) { PaneAlive(context.Background(), r, target(), "%3") }),
	}
	for name, argv := range cases {
		if err := Validate(argv); err != nil {
			t.Errorf("%s builds argv the seam refuses: %v\nargv: %q", name, err, argv)
		}
	}
}
```

Reuse the package's existing fake runner and `assertArgv` helpers if they exist; otherwise write them in this file. `fakeRunner` needs `rc` and `stderr` fields so the RC test above can drive a refusal. `argvOf(t, fn)` runs `fn` against a recording runner and returns the argv it captured.

- [ ] **Step 2: Run and watch them fail**

Run: `go test ./internal/tmux/ -run 'NewWindow|PaneAlive|EveryVerb' -v`
Expected: FAIL — undefined.

- [ ] **Step 3: Implement**

```go
// NewWindow creates a window in session and returns the new pane's id.
//
// `-P -F '#{pane_id}'` is the point of the whole call: tmux prints the created
// pane's id, so the caller never has to search for what it just made. That is
// what lets a hub-created agent be stamped and bound to a session id at birth
// (docs/design.md §19) instead of being recognised later by walking pids.
//
// cwd is its own argv element deliberately — measured, `-c "/a/dir with space"`
// works, while folding the path into the command string would need quoting.
func NewWindow(ctx context.Context, r Runner, t Target, sessionID, cwd, cmd string) (string, error) {
	args := []string{"new-window", "-t", sessionID}
	if cwd != "" {
		args = append(args, "-c", cwd)
	}
	args = append(args, "-P", "-F", "#{pane_id}")
	if cmd != "" {
		args = append(args, cmd)
	}
	res, err := r.Run(ctx, t, args...)
	if err != nil {
		return "", err
	}
	// RC is checked SEPARATELY from err, because execRunner.Run returns a nil
	// error for a non-zero tmux exit (run.go:201). Without this line a refused
	// new-window — a directory that does not exist is the common case — looks
	// like a success whose pane id could not be read, and tmux's own message
	// (`can't find directory /nope`), which is the only useful thing here, is
	// dropped on the floor. Every other reader in this package does the same:
	// assert.go:54, labels.go:54, delta.go:132, capture.go:129, stamp.go:51.
	if res.RC != 0 {
		return "", fmt.Errorf("new-window: %s", firstNonEmpty(res.Stderr, fmt.Sprintf("tmux exited %d", res.RC)))
	}
	return firstPaneID(res.Stdout)
}

// PaneAlive answers whether a pane still exists.
//
// The instrument is emptiness, not an error: measured, `display -p -t %999
// '#{pane_id}'` returns rc=0 and an EMPTY string, because tmux does not fail on
// an unknown target with no client attached. A liveness check that waits for a
// non-zero exit therefore reports every pane alive forever. Asking for the
// pane's OWN id also means a non-empty answer about some other pane is refused.
func PaneAlive(ctx context.Context, r Runner, t Target, paneID string) (bool, error) {
	res, err := r.Run(ctx, t, "display", "-p", "-t", paneID, "#{pane_id}")
	if err != nil {
		return false, err
	}
	// A non-zero RC here is NOT "the pane is gone" — measured, tmux returns
	// rc=0 with an empty string for an unknown target when no client is
	// attached. So RC != 0 means something else went wrong and must not be
	// reported as a confident "dead".
	if res.RC != 0 {
		return false, fmt.Errorf("display -t %s: %s", paneID, firstNonEmpty(res.Stderr, fmt.Sprintf("tmux exited %d", res.RC)))
	}
	return strings.TrimSpace(res.Stdout) == paneID, nil
}
```

`NewSession` mirrors `NewWindow` with `new-session -d -s <name>`. `RespawnPane` is `respawn-pane -k -t <paneID> [cmd]`. `KillWindow`/`KillSession` are `kill-window -t`/`kill-session -t`. `SetWindowOption` is `set -w -t <windowID> <name> <value>`. Every one of them checks `res.RC` the same way and wraps `res.Stderr`, because a lifecycle verb that fails silently is worse than one that fails: the hub would report a window it never created. Write `firstNonEmpty(a, b string) string` once in this file. `firstPaneID` trims, takes the first whitespace-separated token, and returns an error if it does not match `^%[0-9]+$` — a created pane whose id the hub cannot read is a failure, not a pane to guess about.

- [ ] **Step 4: Run the tests**

Run: `go test -race ./internal/tmux/ -count=1`
Expected: PASS.

- [ ] **Step 5: Prove the verbs against real tmux**

Add to `internal/e2e/lifecycle_test.go` (tag `e2e`) one case that creates a window on a private socket, asserts the returned id appears in `list-panes -a`, respawns it and asserts `pane_id` and a `@hub_*` option both survived while `pane_pid` changed. A fake runner proves argv; only real tmux proves the flags mean what the table says.

Run: `go test -tags e2e ./internal/e2e/ -run Lifecycle -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/tmux/lifecycle.go internal/tmux/lifecycle_test.go internal/e2e/lifecycle_test.go
git commit -m "tmux: creation, destruction and liveness verbs on the validated seam"
```

---

### Task 10: Dead panes become visible

**Files:**
- Modify: `internal/state/state.go` (comment only), `internal/ui/render.go`
- Test: `internal/ui/render_test.go`, `internal/e2e/lifecycle_test.go`

**Interfaces:**
- Consumes: `registry.Pane.Dead`, `Pane.DeadStatus` (Task 2), `tmux.SetWindowOption` (Task 9).
- Produces: a dead pane renders as `exited 7`; hub-created windows carry `remain-on-exit on`.

- [ ] **Step 1: Write the failing render test**

```go
func TestADeadPaneSaysItsExitCode(t *testing.T) {
	// state.Error already covers "failed, or the pane is dead" and #{pane_dead}
	// already reaches it — but the row said nothing about WHY, and an exit code
	// is the difference between a finished job and a crash.
	p := pane("local", "api", "claude", 0, `"claude"`, state.Error)
	p.Dead, p.DeadStatus = true, 7
	m := modelWith(t, p)

	out := m.View()
	if !strings.Contains(out, "exited 7") {
		t.Fatalf("a dead pane must carry its exit code:\n%s", out)
	}
}

func TestADeadPaneThatExitedCleanlySaysSo(t *testing.T) {
	// Exit 0 is "the agent finished", not "the agent failed". Same row, and it
	// must not read as an error.
	...
	if !strings.Contains(out, "exited 0") { ... }
}
```

- [ ] **Step 2: Run and watch it fail**

Run: `go test ./internal/ui/ -run DeadPane -v`
Expected: FAIL.

- [ ] **Step 3: Render it**

In the row renderer, when `p.Dead`, replace the age/state cell with `exited <status>`.

- [ ] **Step 4: Correct the spec comment on `state.Error`**

`internal/state/state.go`'s comment says "failed, or the pane is dead". Add two sentences. First: that half of it is reachable only where `remain-on-exit` is on, which the hub sets on its own windows and never globally. Second, from the spike: an agent that fails to start does **not** necessarily die — `claude --resume <nonexistent>` draws a session picker and waits, `pane_dead=0` — so the dead-pane path covers a narrower set of failures than its name suggests, and a failed launch is more often a live pane in an unexpected mode. A comment that promises coverage the code cannot deliver is worse than none, because it stops the next reader checking.

- [ ] **Step 5: Prove it end to end**

Add to `internal/e2e/lifecycle_test.go`: create a window through `tmux.NewWindow` with `remain-on-exit on` set on it, run a command that exits 7, poll once, and assert the snapshot carries `Dead == true` and `DeadStatus == 7`. This is the case that proves the previously-unreachable branch is now reachable.

Run: `go test -tags e2e ./internal/e2e/ -run Dead -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/ui internal/state/state.go internal/e2e/lifecycle_test.go
git commit -m "ui: a dead pane carries its exit code"
```

---

### Task 11: `internal/launch` — the launch spec

**Files:**
- Create: `internal/launch/launch.go`, `internal/launch/launch_test.go`

**Interfaces:**
- Produces:
  - `type Spec struct { Host, CWD, Model, PermissionMode string; NewSession bool; SessionName string }`
  - `type Plan struct { SessionID string; Command string }`
  - `func (s Spec) Validate() error`
  - `func (s Spec) Build(id string) (Plan, error)`
  - `func NewSessionID() (string, error)`
  - `var Models = []string{"opus", "sonnet", "fable"}`
  - `var PermissionModes = []string{"default", "plan", "acceptEdits", "auto", "manual", "dontAsk", "bypassPermissions"}`

- [ ] **Step 1: Write the failing tests**

```go
func TestBuildDictatesTheSessionID(t *testing.T) {
	// The whole point: the hub chooses the id, so pane↔session is KNOWN at
	// birth and no process-tree walk is needed (docs/design.md §19).
	p, err := Spec{CWD: "/srv/api", Model: "opus"}.Build("7007b23f-1599-4efa-81c5-4195621cc273")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(p.Command, "--session-id 7007b23f-1599-4efa-81c5-4195621cc273") {
		t.Fatalf("command must dictate the session id: %q", p.Command)
	}
	if !strings.Contains(p.Command, "--model opus") {
		t.Fatalf("command must carry the model: %q", p.Command)
	}
}

func TestBuildOmitsWhatTheUserDidNotChoose(t *testing.T) {
	// An empty model must not become `--model ""`, which claude rejects.
	p, err := Spec{CWD: "/srv/api"}.Build("abc")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(p.Command, "--model") || strings.Contains(p.Command, "--permission-mode") {
		t.Fatalf("unset fields must be absent, not empty: %q", p.Command)
	}
}

func TestDefaultPermissionModeIsNotPassedAtAll(t *testing.T) {
	// "default" is the hub's word for "do not pass the flag". claude's own
	// choices are acceptEdits|auto|bypassPermissions|manual|dontAsk|plan —
	// `--permission-mode default` is not one of them and exits non-zero.
	p, _ := Spec{CWD: "/x", PermissionMode: "default"}.Build("abc")
	if strings.Contains(p.Command, "--permission-mode") {
		t.Fatalf("`default` must mean the flag is omitted: %q", p.Command)
	}
}

func TestValidateRefusesARelativeCWD(t *testing.T) {
	// tmux -c resolves relative to the SERVER's cwd, which on a remote host is
	// not anywhere the user is thinking about. Refuse it with the fix in the
	// message.
	err := Spec{CWD: "api"}.Validate()
	if err == nil {
		t.Fatal("a relative cwd must be refused")
	}
	if !strings.Contains(err.Error(), "absolute") {
		t.Fatalf("the error must carry its fix, got %q", err)
	}
}

func TestValidateRefusesAnEmptyCWD(t *testing.T) { ... }

func TestValidateRefusesAModelClaudeDoesNotKnowUnlessItLooksLikeAFullName(t *testing.T) {
	// Aliases are a closed set (opus|sonnet|fable); a full model name is not,
	// so anything containing a '-' is passed through and claude judges it.
	if err := (Spec{CWD: "/x", Model: "opu"}).Validate(); err == nil {
		t.Fatal("a typo'd alias must be refused before a pane is created")
	}
	if err := (Spec{CWD: "/x", Model: "claude-opus-5"}).Validate(); err != nil {
		t.Fatalf("a full model name must pass through: %v", err)
	}
}

func TestNewSessionIDIsAFreshUUIDEveryTime(t *testing.T) {
	// Reuse exits 1 with `Session ID <uuid> is already in use.` (measured), so
	// a duplicate here would surface as a pane that dies immediately.
	a, err := NewSessionID()
	if err != nil {
		t.Fatal(err)
	}
	b, err := NewSessionID()
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Fatal("two launches must not share a session id")
	}
	if !regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`).MatchString(a) {
		t.Fatalf("not a v4 uuid: %q", a)
	}
}
```

- [ ] **Step 2: Run and watch them fail**

Run: `go test ./internal/launch/ -v`
Expected: FAIL — package does not exist.

- [ ] **Step 3: Implement**

`NewSessionID` uses `crypto/rand` and formats a v4 uuid; do not shell out to `uuidgen`, which would not exist on every host. `Build` assembles the command as a single string for tmux's `shell-command` argument — measured, a command with quoted arguments works as one string — and quotes only what needs it (the cwd never appears in the command, only in `-c`).

- [ ] **Step 4: Run the tests**

Run: `go test -race ./internal/launch/ -count=1`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/launch
git commit -m "launch: the launch spec, with the session id dictated at birth"
```

---

### Task 12: The launch form

**Files:**
- Create: `internal/ui/launchform.go`, `internal/ui/launchform_test.go`
- Modify: `internal/ui/model.go`, `internal/ui/render.go`

**Interfaces:**
- Consumes: `launch.Spec`, `launch.Models`, `launch.PermissionModes`, the hosts the model knows.
- Produces: `uiMode` gains `modeLaunch`; `n` opens the form; the form emits a `launch.Spec`.

Five fields, in this order: host (defaults to the host under the cursor), directory (free text), model (cycled), permission mode (cycled), destination (new window in the cursor's session / new session). `tab` and `shift+tab` move, `left`/`right` cycle a choice field, `enter` submits, `esc` cancels.

- [ ] **Step 1: Write the failing tests**

```go
func TestNOpensTheLaunchFormAndItIsVISIBLE(t *testing.T) {
	// This project shipped four invisible UI modes because View() was one line
	// and no test read its output. Assert on the string.
	m := modelWith(t, pane("local", "api", "claude", 0, `"claude"`, state.Works))
	m = press(m, "n")

	out := m.View()
	for _, want := range []string{"каталог", "модель", "режим", "куда"} {
		if !strings.Contains(out, want) {
			t.Fatalf("the launch form is not drawn — missing %q:\n%s", want, out)
		}
	}
}

func TestTheFormDefaultsToTheHostUnderTheCursor(t *testing.T) { ... }

func TestTabMovesBetweenFieldsAndTypingLandsInTheFocusedOne(t *testing.T) { ... }

func TestSubmittingAnInvalidSpecShowsTheFIXAndKeepsTheForm(t *testing.T) {
	// A relative path is the commonest mistake and the message must say what to
	// do, not that something is wrong.
	m := modelWith(t, ...)
	m = press(m, "n")
	m = typeInto(m, "api")
	m = press(m, "enter")

	out := m.View()
	if !strings.Contains(out, "absolute") {
		t.Fatalf("the error must carry its fix:\n%s", out)
	}
	if !strings.Contains(out, "каталог") {
		t.Fatalf("the form must stay open so the user can correct it:\n%s", out)
	}
}

func TestEscapeClosesTheFormWithoutCreatingAnything(t *testing.T) { ... }
```

- [ ] **Step 2: Run and watch them fail**

Run: `go test ./internal/ui/ -run Launch -v`
Expected: FAIL.

- [ ] **Step 3: Implement the form**

Model the text field on the existing `internal/ui/compose.go`, which already handles rune-wise backspace correctly — reuse it rather than writing a second text input.

- [ ] **Step 4: Dispatch the mode in `View()`**

Add `modeLaunch` to the `View()` switch. Then delete the case and re-run: the tests from Step 1 must go RED. A mode that renders only because `View()` happens to fall through is the defect this project already shipped.

- [ ] **Step 5: Run the tests**

Run: `go test -race ./internal/ui/ -count=1`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/ui
git commit -m "ui: the launch form, one field per decision"
```

---

### Task 13: Launch execution — identity at birth

**Files:**
- Modify: `internal/ui/model.go`, `internal/broadcast/identify.go`
- Test: `internal/ui/model_test.go`, `internal/broadcast/identify_test.go`, `internal/e2e/lifecycle_test.go`

**Interfaces:**
- Consumes: `launch.Spec.Build`, `tmux.NewWindow`/`NewSession`, `tmux.SetWindowOption`, `broadcast.Stamper.Stamp`, `history.Log`.
- Produces: `func (k *Keeper) Adopt(host, paneID, claudeSession string)` — a pane the hub created is identified without a walk.

- [ ] **Step 1: Write the failing Keeper test**

```go
func TestAdoptIdentifiesWithoutAWalk(t *testing.T) {
	// The payoff of dictating the session id: for a pane the hub created, the
	// pane↔session binding is KNOWN. The process-tree walk — where this
	// project's one Critical defect lived (a forwarded socket walked against
	// the LOCAL process table, 97 of 3117 local pids answering "agent here") —
	// is never consulted for it.
	k := NewKeeper(NewStamper(&fakeRunner{}, Instance("t")))
	k.Adopt("nuc", "%7", "7007b23f-1599-4efa-81c5-4195621cc273")

	if !k.Identified("nuc", "%7") {
		t.Fatal("an adopted pane must be identified")
	}
}

func TestAdoptIsForgottenWithItsHost(t *testing.T) {
	// Otherwise a host that goes away leaves an identification behind, and the
	// write path would trust a pane on a server the hub can no longer see.
	...
}
```

- [ ] **Step 2: Run and watch them fail**

Run: `go test ./internal/broadcast/ -run Adopt -v`
Expected: FAIL — undefined.

- [ ] **Step 3: Implement `Adopt`**

Set the identified flag for the pane and record the claude session id, in the same map `Refresh` writes, so `Forget(host)` already covers it.

- [ ] **Step 4: Write the failing execution test**

```go
func TestLaunchCreatesStampsAndAdoptsInThatOrder(t *testing.T) {
	// Order matters: the pane must be stamped and adopted BEFORE the user can
	// select it, or the first send falls back to the inference path for a pane
	// whose identity was never in doubt.
	...
	assertOrder(t, r.calls, "new-window", "set -p", "set -w")
}

func TestLaunchSetsRemainOnExitOnITSOWNWindowOnly(t *testing.T) {
	// A global set would change the behaviour of the user's own windows. The
	// hub's footprint stays inside what it created (docs/design.md §19).
	...
	for _, c := range r.calls {
		if strings.HasPrefix(c, "set -g") && strings.Contains(c, "remain-on-exit") {
			t.Fatalf("remain-on-exit must never be set globally: %q", c)
		}
	}
}

func TestAFailedLaunchSaysWhatToDoAndCreatesNothingHalfway(t *testing.T) { ... }

func TestLaunchIsRecordedInTheHistoryLog(t *testing.T) {
	// A created session is an action with a consequence, exactly like a send.
	...
}
```

- [ ] **Step 5: Run and watch them fail**

Run: `go test ./internal/ui/ -run Launch -v`
Expected: FAIL.

- [ ] **Step 6: Implement the launch command**

On submit: `launch.NewSessionID()`, `spec.Build(id)`, then `tmux.NewWindow` or `tmux.NewSession`, then `SetWindowOption(windowID, "remain-on-exit", "on")` on the created window, then `Stamper.Stamp`, then `Keeper.Adopt`, then a history record. Any error leaves a status-line message carrying the fix and does not partially adopt.

- [ ] **Step 7: Prove it against real tmux and a real claude**

In `internal/e2e/lifecycle_test.go`, launch a real `claude --session-id <uuid> -p 'reply with exactly: OK'` into a created window and assert: the pane id came back, the `@hub_*` option is on that pane, and `~/.claude/projects/*/<uuid>.jsonl` exists. Skip the case with a clear reason if `claude` is not on `PATH` — and make the skip say which binary was missing, because this suite has already shipped two skips that claimed a thing was unverifiable when it was not.

Run: `go test -tags e2e ./internal/e2e/ -run LaunchReal -v`
Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add internal/ui internal/broadcast internal/e2e
git commit -m "launch: create, stamp and adopt in one path — no inference for our own panes"
```

---

### Task 14: Restart, resume, and kill

**Files:**
- Modify: `internal/ui/model.go`, `internal/ui/render.go`, `internal/broadcast/confirm.go`
- Test: `internal/ui/model_test.go`, `internal/broadcast/confirm_test.go`, `internal/e2e/lifecycle_test.go`

**Interfaces:**
- Consumes: `tmux.RespawnPane`, `tmux.KillWindow`, `tmux.KillSession`, `broadcast.Needed`, `Keeper`.
- Produces: `R` restarts the pane under the cursor; `K` kills it; both confirm first.

- [ ] **Step 1: Write the failing tests**

```go
func TestRestartResumesTheSameConversation(t *testing.T) {
	// Measured: --session-id on an existing id exits 1 with
	// `Session ID <uuid> is already in use.` So continuity is --resume, and a
	// restart that reached for --session-id again would kill the pane it just
	// made.
	...
	if !strings.Contains(r.last, "--resume 7007b23f-1599-4efa-81c5-4195621cc273") {
		t.Fatalf("restart must resume, got %q", r.last)
	}
	if strings.Contains(r.last, "--session-id") {
		t.Fatalf("restart must NOT dictate an id again: %q", r.last)
	}
}

func TestRestartInvalidatesIdentityBecauseTheSTAMPSURVIVES(t *testing.T) {
	// Measured: respawn-pane -k keeps pane_id AND the @hub_* option, and
	// changes pane_pid. So the guarded write path still trusts the pane while
	// the process behind it is a different one. Identity must be dropped
	// explicitly; nothing about the pane will tell us.
	...
	if k.Identified("local", "%3") {
		t.Fatal("a respawned pane must be re-identified, not inherited")
	}
}

func TestResumingASessionAnotherLIVEPaneHoldsIsREFUSED(t *testing.T) {
	// Measured, and the reason this test exists: a second `claude --resume <uuid>`
	// returns rc=0 while an interactive claude still holds that session — no
	// lock, no warning, two processes appending to one transcript. `--session-id`
	// refuses a duplicate; `--resume` does not, so the hub has to.
	//
	// respawn-pane -k is safe by construction (it kills, then spawns, in one
	// call). The exposed path is resuming a session into a NEW window while the
	// pane that owns it is still alive — and PaneAlive is the instrument.
	...
	if err == nil {
		t.Fatal("resuming a session a live pane holds must be refused")
	}
	if !strings.Contains(err.Error(), "%3") {
		t.Fatalf("the refusal must name the pane that already holds it, got %q", err)
	}
}

func TestAFailedResumeIsALIVEPaneShowingAPickerNotADeadOne(t *testing.T) {
	// Measured: `claude --resume <nonexistent>` does NOT exit. pane_dead=0, no
	// status, and the pane draws an interactive session picker and waits. So the
	// hub must not report a failed restart by looking for an exit code — there
	// isn't one. The honest report is that the pane is alive and the hub no
	// longer knows what it is, i.e. identity was dropped and not re-established.
	...
}

func TestKillAlwaysConfirmsAndTheDialogNAMESWhatIsRunning(t *testing.T) {
	// "Kill this?" with no subject is how the wrong window dies.
	...
	if !strings.Contains(out, "api") || !strings.Contains(out, "claude") {
		t.Fatalf("the confirmation must name its subject:\n%s", out)
	}
}

func TestKillingADeadPaneStillConfirmsButSaysNothingIsRunning(t *testing.T) {
	// A dead pane is the common case for cleanup. The dialog stays — one habit,
	// not two — but it must not imply work is at risk.
	...
}

func TestEscapeDuringAKillConfirmationKillsNothing(t *testing.T) { ... }
```

- [ ] **Step 2: Run and watch them fail**

Run: `go test ./internal/ui/ -run 'Restart|Kill' -v`
Expected: FAIL.

- [ ] **Step 3: Implement**

`R` builds `claude --resume <session-id>` from the pane's known `ClaudeSession` (empty → the status line says the hub does not know this pane's session and offers a plain restart), calls `RespawnPane`, then `Keeper.Forget`-equivalent for that one pane, then re-stamps.

Two things this path must get right, both from the spike:

- **Restart in place is safe; resuming elsewhere is not.** `respawn-pane -k` kills the old process and spawns the new one in one call, so no window exists where two claudes share the transcript. Resuming a session into a *different* window does open that window, and `--resume` will not stop it — so that path checks `PaneAlive` on the pane the hub believes owns the session and refuses while it answers true, naming the pane.
- **A failed resume leaves a LIVE pane.** `claude --resume <nonexistent>` draws a session picker and waits; `pane_dead` stays 0. So the failure report cannot be "look for an exit code". After a respawn the hub has dropped identity, and if re-identification does not succeed the honest status is "this pane is no longer a known agent" — which is also what the next tick would conclude on its own. `K` routes through the existing confirmation machinery in `internal/broadcast/confirm.go`, adding a reason for "this pane is running an identified agent" and one for "nothing is running".

- [ ] **Step 4: Calibrate both guarantees**

Remove the identity invalidation from the restart path, run `go test ./internal/ui/`, and confirm `TestRestartInvalidatesIdentity…` goes RED. Then remove the confirmation gate from `K` and confirm the kill tests go RED. Restore both. This project has shipped three guarantees whose removal left the suite green.

- [ ] **Step 5: Prove restart against real tmux**

In `internal/e2e/lifecycle_test.go`: create a window, stamp it, respawn it, and assert `pane_id` and the `@hub_*` option are unchanged while `pane_pid` differs — the measured behaviour that makes Step 3's invalidation necessary. Then assert the hub's own state no longer reports the pane identified.

Run: `go test -tags e2e ./internal/e2e/ -run Restart -v`
Expected: PASS.

- [ ] **Step 6: Document the keys**

`R` and `K` into `README.md` and §10.

- [ ] **Step 7: Commit**

```bash
git add internal/ui internal/broadcast internal/e2e README.md docs/design.md
git commit -m "lifecycle: R restarts with continuity, K kills with a named confirmation"
```

---

### Task 15: Remote hosts, and the honest edge

**Files:**
- Modify: `internal/ui/launchform.go`, `docs/design.md`
- Test: `internal/ui/launchform_test.go`, `internal/e2e/remote_test.go`

**Interfaces:**
- Consumes: `hub.Host.SSHDest`, `Host.ControlPath` (already present, used by attach).
- Produces: directory completion on a remote host through the existing ssh master, and a clear message when there is none.

- [ ] **Step 1: Write the failing tests**

```go
func TestTheFormDoesNotCOMPLETEALocalPathForARemoteHost(t *testing.T) {
	// The trap: the form runs on this machine, and `/srv/api` existing HERE
	// says nothing about the host the pane will be created on. Completing from
	// the local filesystem would be confidently wrong.
	...
}

func TestARemoteHostWithoutSSHSaysWhyCompletionIsUnavailableAndStillACCEPTSATYPEDPATH(t *testing.T) {
	// A forwarded socket carries tmux, not a shell. Without ssh= the hub cannot
	// list remote directories — but the user knows the path, so refusing the
	// launch would be the ceremony this project's principles reject.
	...
	if !strings.Contains(out, "ssh=") {
		t.Fatalf("the message must name the missing config: %s", out)
	}
}

func TestABadRemotePathFailsWithTMUXOWNMESSAGE(t *testing.T) {
	// tmux refuses new-window -c on a directory that does not exist. Surface
	// that verbatim rather than pre-guessing.
	...
}
```

- [ ] **Step 2: Run and watch them fail**

Run: `go test ./internal/ui/ -run Remote -v`
Expected: FAIL.

- [ ] **Step 3: Implement**

Completion runs `ssh -S <ControlPath> <SSHDest> -- ls -1dF <prefix>*` only when both are set; otherwise the field accepts a typed path and the hint line says completion needs `ssh=`. Local hosts complete from the local filesystem.

- [ ] **Step 4: Add the §12 limitation**

`docs/design.md` §12: without `ssh=`, a remote launch cannot verify or complete its directory before creating the pane; tmux's own error is what the user sees.

- [ ] **Step 5: Extend the e2e remote case**

`internal/e2e/remote_test.go` already builds a forwarded-socket host. Add: create a window through the forwarded socket and assert the pane appears in a poll of that host and is NOT walked against the local process table — the exact Critical this branch fixed. Reuse `Host.LocalProc` rather than re-deriving locality.

Run: `go test -tags e2e ./internal/e2e/ -run Remote -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/ui internal/e2e docs/design.md
git commit -m "launch: remote directories through the ssh master, honest without one"
```

---

### Task 16: Close it out — wiring floor, e2e sweep, docs

**Files:**
- Modify: `cmd/tmux-hub/wiring_test.go`, `docs/design.md`, `docs/known-issues.md`, `README.md`
- Test: everything

- [ ] **Step 1: Extend the wiring floor to every new constructor**

Add `launch.NewSessionID`, `tmux.NewWindow`, `tmux.NewSession`, `tmux.RespawnPane`, `tmux.KillWindow`, `tmux.PaneAlive`, `hide.Open` and `Keeper.Adopt` to the list the floor demands a non-test caller for. Then confirm the floor works by deleting one production call site, running it, and watching it go RED.

- [ ] **Step 2: Run every gate, fresh**

```bash
gofmt -l .
go vet ./... && go vet -tags e2e ./internal/e2e/
go test -race ./... -count=1
go test -tags e2e ./internal/e2e/ -count=1
```

Expected: `gofmt` silent; vet clean; every package `ok`; the e2e suite green. Count the `ok`/`FAIL` lines against the package count — a package that never ran is silent, and silence looks like patience.

- [ ] **Step 3: Verify the tree is clean before quoting any number**

```bash
git status --porcelain --untracked-files=no
```

Expected: empty. A full-suite number from a dirty tree is not a number.

- [ ] **Step 4: Refresh the docs against the code**

Re-read §18, §19, §10, §11, §12 and the README key table against what was actually built. Correct anything the implementation changed. Delete any claim the code no longer supports.

- [ ] **Step 5: Park what was not done**

Anything deferred goes into `docs/known-issues.md` with its ruling, in the format the existing entries use.

- [ ] **Step 6: Commit**

```bash
git add -- cmd/tmux-hub/wiring_test.go docs README.md
git commit -m "docs: refresh hiding and lifecycle against the implementation"
```

Stage explicit paths, never `git add -A`: another session sharing this worktree has twice swept in-progress files into an unrelated commit.

---

## Self-Review

**1. Spec coverage.** The user asked for two things and both are covered end to end: noise removal is Tasks 3–7 (set, filter, keys, wiring), lifecycle is Tasks 8–15 (verbs, dead panes, spec, form, execution, restart/kill, remote). Task 1 writes the contract first; Task 16 verifies and refreshes the docs. Two decisions the user made are load-bearing and each has a task that would fail without them: resurface-on-`Needs` (Task 4 Step 5 calibrates it), persist by path (Task 4's reopen test), full form every time (Task 12's five fields).

**2. Placeholder scan.** Six test bodies are elided with `...` in Tasks 5, 12, 14 and 15 where the assertion is named in the test's own name and comment and the setup is identical to the test above it. Every test whose *mechanism* is novel is written out. The `...` bodies are the ones a reader can complete from the neighbour; if an implementer cannot, that is a question for the controller, not a guess.

**3. Type consistency.** `hide.Key` has the same five fields everywhere it appears (Tasks 1, 4). `registry.Pane` gains exactly `Index`, `StartCommand`, `DeadStatus` in Task 2 and every later task reads only those. `launch.Spec` is defined once in Task 11 and consumed unchanged in Tasks 12, 13, 15. `tmux.NewWindow`'s signature in Task 9 matches its call in Task 13. `Keeper.Adopt` is defined in Task 13 and used in Task 14.

**4. Known conflicts with the existing code.** Two, both deliberate and both stated where they land:

- `state.Error`'s comment currently overpromises (it claims dead-pane coverage that `remain-on-exit` gates). Task 10 Step 4 corrects the comment rather than the state, because the ordering of the state enum is the inbox order and changing it is a behaviour change nobody asked for.
- `internal/hide` importing `registry` puts a UI-shaped concern in a package the poller also uses. The alternative — passing five primitives from every caller — moves the resurface rule out of the one place that cannot forget it. The import is the cheaper mistake, and `registry` does not import `hide`, so there is no cycle.

**5. This plan was verified by compiling it, not by reading it.** `internal/statedir`, `internal/hide` and `internal/tmux/lifecycle.go` were extracted into a scratch checkout (`git archive HEAD`, so the working tree was never touched), built, and run against the plan's own tests. Seven defects surfaced that reading had not:

| # | Defect | How it would have shown up |
|---|---|---|
| 1 | The code called a package-level `run(ctx, r, t, …)` helper that does not exist — the seam is `Runner.Run(ctx, t, …) (Result, error)` | compile error in Task 9, first step |
| 2 | **`RC != 0` returns a nil error** (`run.go:201`), and all five existing readers check `res.RC` by hand. Checking only `err` would treat a refused `new-window` as success and drop tmux's own `can't find directory /nope` | a lifecycle verb reporting a window it never created |
| 3 | `TestEveryVerbGoesThroughValidate` was structurally impossible — `Validate` lives inside `execRunner.Run`, so a fake runner bypasses it. Replaced with the test for the real hazard: does the seam ACCEPT each verb's argv | a test that can never pass, blocking Task 9 |
| 4 | **`Validate` refuses four of the seven verbs** — it demands `%N` for every `-t`, and `new-window`/`kill-window`/`kill-session`/`set -w` target sessions and windows. This became Task 8, which did not exist in the first draft | discovered at implementation, after the verbs were written |
| 5 | `NewWindow` took a session NAME; Task 8's rule requires an ID, so all four call sites and the signature changed to `$N` | argv refused at runtime |
| 6 | `ErrBadTarget`'s own text hardcodes "pane id", producing the self-contradicting `-t value is not a pane id: "api" is not a session id` | a message that misleads the next reader |
| 7 | **Ordering:** the wiring floor goes red the moment `internal/hide` exists, so Tasks 4–6 would each have committed on a red suite. The minimal wiring moved into Task 4 | three commits training the next implementer to ignore the floor |

What the run also confirmed positively: all five `hide` tests pass and each of three compiling mutations is caught by the test written for it (resurface→`Gone`, corroborator dropped, `Toggle` not writing); `internal/tmux` is 47/47 green with the shape change, every pre-existing Validate test included; and the three shape mutations each redden their own test. The scratch checkout was deleted afterwards.

**6. A spike measured the Claude side, and one result would have broken a task.** Five questions the plan assumed and could not answer from `-p` mode alone were run against a real interactive agent in a real pane on a private socket:

- **`--resume` after a hard kill works.** `respawn-pane -k` SIGKILLs the interactive claude, and `--resume` then recalls a token stored before the kill (transcript 20 → 49 lines). Had this failed, Task 14's entire restart-with-continuity path would have been wrong, and no amount of reading would have said so.
- **`--resume` is not exclusive** — a second resume of a live session returns rc=0 with no lock and no warning, two processes on one transcript. Now a Global Constraint, with `PaneAlive` promoted from convenience to guard.
- **A failed resume is a LIVE pane showing a picker**, not a dead one — so Tasks 10 and 14 both stopped looking for an exit code that does not exist.
- **`pane_start_command` is empty for a plain shell**, so the hide key's corroborator carries no information for the commonest thing anyone hides. §18 now says so instead of implying every key is corroborated.
- **An agent's start command contains its session uuid**, so a mark on an agent pane cannot survive a relaunch. Correct behaviour, now documented rather than left to be reported as a bug.

**7. What this plan does not do.** It does not add named launch profiles (the user chose the full form over a config), does not touch the transport plan's ssh supervision (`docs/plans/2026-08-12-transport-supervision.md` still owns that, and Task 15 uses only the ssh master that already exists for attach), and does not change `QuietAfter` or the inbox order.
