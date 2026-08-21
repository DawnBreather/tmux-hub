# §20 Possession Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make `a` take the operator into a pane without taking the terminal away from the hub — a `switch-client` jump on the hub's own tmux server, and the existing remote attach moved into a window of the hub's own session.

**Architecture:** The hub renders nothing new. tmux is already the terminal emulator, so possession is two tmux commands for a local target and one `new-window` wrapping today's unchanged ssh attach for a remote one. `a` becomes a dispatcher over five paths chosen from what the row and its host *report* — never from a string the operator typed. Nothing is stored: the only state is which row the operator was sent to, so the hub can say where they came back from.

**Tech Stack:** Go 1.x, bubbletea, tmux 3.4/3.7b, the existing `internal/tmux` seam.

**Spec:** `docs/design.md` §20 (with §3 "Possession", §7 targets-by-id, §8 attach, §16 keys). The visual target is `docs/ui-flows-possession.html` — 14 frames, each carrying the assertion it promises; the two drawn frames are the only design decision left in it, and `docs/mockup-authoring.md` says why the rest are real.

## Global Constraints

- **Never construct a tmux command without an explicit socket.** Everything goes through `tmux.Runner` with a `tmux.Target`; `Run` refuses an empty socket (`ErrNoSocket`).
- **Every `-t` value is an ID, never a name** — `%N` pane, `@N` window, `$N` session. A name does not survive a rename (measured: `has-session -t <old>` fails rc=1 immediately after one).
- **RC is checked separately from `err`.** `execRunner.Run` returns a nil error for a non-zero tmux exit, so every verb must test `res.RC != 0` and surface `res.Stderr`.
- **`#{client_activity}` and `#{client_created}` appear nowhere.** They segfault a tmux 3.2a server with no client attached; `TestNoSourceFileNamesAForbiddenFormat` enforces it repo-wide.
- **`link-window` must never appear in the production tree.** Measured: `kill-window` on a linked window destroys it in every session and kills the pane's process, and a linked window is indistinguishable from an owned one (`window_flags` is `*`, and the default `window-status-format` does not include `window_linked`). Task 2 makes this a source-level test.
- **Production UI strings are English.** The launch form's Russian strings are a parked issue (`docs/known-issues.md`); do not add more.
- **Assert on the string `View()` returns**, never on a render helper. A helper can be fully covered and called by nobody — that has happened in this repo (§13, and `RenderHistory` specifically).
- **Every test must fail against the unmodified product.** Before committing a test, break the line it guards, watch it go red, restore. A refactor keeps tests green by definition, so re-run this check after moving code.
- **Possession requires the hub to be inside tmux.** When `$TMUX` is empty there is no client to switch and no session to put a window in, so both new paths are impossible and `a` must fall back to today's full-screen `AttachCmd`. §20's table has four rows and does not name this case; Task 7 makes it the fifth path, and it is the reason the change cannot regress a hub started from a plain terminal.

## Measured facts this plan depends on

Re-measured on tmux 3.7b, private sockets, on 2026-08-12. Do not re-derive; do not assume beyond them.

| fact | value |
|---|---|
| `switch-client -t '$N'` then `select-window -t '@N'`, run from inside a pane of the same server, **no `-c`** | rc=0 both, the attached client moves; status-left goes `[hub]` → `[work]` |
| `display -p -F '#{pid}:#{start_time}'` with **no client attached** | rc=0, prints e.g. `1525344:1786587652` |
| a `-t` whose value is empty | tmux says `command switch-client: -t expects an argument`, rc=1 — which is why `Validate` refuses a dangling `-t` rather than passing it on |
| closing the hub's own window that held a remote attach | the other server's `pane_pid` is **unchanged**, its session is present, `list-clients` drops to 0 |
| closing a `link-window`ed copy | the window vanishes from every session and the agent's pid is **gone** |
| the hub while its pane is not displayed | keeps polling — a window created during the absence was already listed on return — and the TUI redraws with no artefact |

## File structure

| file | responsibility |
|---|---|
| `internal/tmux/run.go` (modify) | `opaqueArg`, `commandVerbs`, two `shapeFor` cases. The seam is the only place that decides what a `-t` may be. |
| `internal/tmux/possession.go` (create) | `SwitchClient`, `SelectWindow`, `AttachWindow`, `ServerEpoch` — the four verbs §20 needs, each with its measured fact in the doc comment. |
| `internal/tmux/guard_test.go` (modify) | the source scan, refactored so it can be calibrated against a planted file, plus `link-window`. |
| `internal/hub/poll.go` (modify) | `SelfSessionID` beside the existing `SelfPaneID`/`SelfSocket`. |
| `internal/ui/possession.go` (create) | the dispatcher's decision function and the four commands it can return. Kept out of `model.go`, which is already 1506 lines. |
| `internal/ui/model.go` (modify) | the `a` case delegates; `m.sentTo` remembers where the operator went. |
| `internal/ui/render.go` (modify) | the header hint becomes path-specific. |
| `internal/e2e/possession_test.go` (create) | the nested harness: a real hub binary, a real jump, a real return. |

---

### Task 1: The seam admits the possession container

`Validate` refuses §20's remote command twice over — first on `ssh`'s own `-t` (which means "give me a tty", not a tmux target), then on the remote tmux's `-t $3`. Both live inside one multi-token argument, which `validateArg` reads as a tmux sub-command chain. It also refuses any ssh `ControlPath` containing `%` (`~/.ssh/cm-%h-%p-%r` is the common spelling), via `ErrPercentInArg`.

The fix keys on the **outer verb**, and keying on the payload instead is wrong in both directions — also measured. A real chain often begins with a flag continuing the outer command (`paste-buffer -b b '-t @4 ; display …'`), so "the payload starts with a tmux verb" would skip real chains; and it would stop checking `display -p 'OK %s'`, whose `%` is the strftime hole §3 records.

**Files:**
- Modify: `internal/tmux/run.go` (add after `unquote`, wire into `Validate`, extend `shapeFor`)
- Test: `internal/tmux/run_test.go`

**Interfaces:**
- Consumes: `unquote(string) string`, `Validate([]string) error`, `shapeFor([]string) (*regexp.Regexp, string)`, `sessionID`/`windowID`/`paneID` regexps — all already in `run.go`.
- Produces: `opaqueArg(args []string) int` — the index of the trailing argument tmux hands to a shell, or `-1`. `Validate` skips the target and `%` rules for that index only.

- [ ] **Step 1: Write the failing tests**

Add to `internal/tmux/run_test.go`:

```go
// The container §20 puts the remote attach in. Validate refused it twice over
// before opaqueArg: on ssh's own -t (a tty request, not a tmux target) and on the
// remote tmux's -t $3. A ControlPath spelled ~/.ssh/cm-%h-%p-%r was refused a
// third time, as a literal % outside a pane id.
func TestValidateAcceptsThePossessionContainer(t *testing.T) {
	for _, argv := range [][]string{
		{"new-window", "-t", "$0", "-n", "nuc", "ssh -S /home/dev/.ssh/cm-nuc -t nuc tmux attach -t $3"},
		{"new-window", "-t", "$0", "-n", "nuc", "ssh -S /home/dev/.ssh/cm-%h-%p-%r -t nuc tmux attach -t $3"},
	} {
		if err := Validate(argv); err != nil {
			t.Errorf("Validate(%q) = %v, want nil", argv, err)
		}
	}
}

// The exemption is for the SHELL payload only. A forbidden format hidden inside
// it must still be refused, because Validate scans every argument for those
// before anything else — that is the invariant which may never be scoped.
func TestTheOpaqueArgumentIsStillScannedForForbiddenFormats(t *testing.T) {
	argv := []string{"new-window", "-t", "$0", "-n", "x", "sh -c 'tmux display -p \"#{client_activity}\"'"}
	if err := Validate(argv); !errors.Is(err, ErrForbiddenFormat) {
		t.Errorf("Validate(%q) = %v, want ErrForbiddenFormat", argv, err)
	}
}

// The guard that makes the exemption safe. `respawn-pane -k -t @4` has a command
// verb as its outer verb AND -t as its second-to-last argument, so it has no
// shell payload at all: the last argument IS the target value, and exempting it
// would open the write path to a window target. Keyed on args[len-2] being a
// flag, it stays refused.
func TestACommandVerbWithNoPayloadIsNotExempted(t *testing.T) {
	for _, argv := range [][]string{
		{"respawn-pane", "-k", "-t", "@4"},
		{"new-window", "-t", "@4"},
		{"new-session", "-d", "-s", "proj", "-t", "$1"},
	} {
		if err := Validate(argv); !errors.Is(err, ErrBadTarget) {
			t.Errorf("Validate(%q) = %v, want ErrBadTarget", argv, err)
		}
	}
}

// switch-client addresses a SESSION and select-window a WINDOW. Both are new
// verbs in the seam; without a shape they inherited the pane-only default and
// were refused. `-t 0` is the trap $TMUX sets: its third field is a bare number,
// and a session target needs the $ sigil, so 0 would name a session CALLED 0.
func TestValidateAcceptsThePossessionTargets(t *testing.T) {
	for _, argv := range [][]string{
		{"switch-client", "-t", "$0"},
		{"select-window", "-t", "@12"},
	} {
		if err := Validate(argv); err != nil {
			t.Errorf("Validate(%q) = %v, want nil", argv, err)
		}
	}
	for _, argv := range [][]string{
		{"switch-client", "-t", "0"},
		{"switch-client", "-t", "hub"},
		{"switch-client", "-t", "@4"},
		{"select-window", "-t", "api"},
		{"select-window", "-t", "$0"},
		{"select-window", "-t", ""},
	} {
		if err := Validate(argv); !errors.Is(err, ErrBadTarget) {
			t.Errorf("Validate(%q) = %v, want ErrBadTarget", argv, err)
		}
	}
}
```

- [ ] **Step 2: Run them and watch all four fail**

Run: `rtk proxy go test ./internal/tmux/ -run 'TestValidateAcceptsThePossessionContainer|TestTheOpaqueArgumentIsStillScannedForForbiddenFormats|TestACommandVerbWithNoPayloadIsNotExempted|TestValidateAcceptsThePossessionTargets' -v`

Expected: `TestValidateAcceptsThePossessionContainer` fails with `ErrBadTarget` (`"nuc" is not a pane id`); `TestValidateAcceptsThePossessionTargets` fails on the two accepting cases with `ErrBadTarget`. The other two pass already — they are regression guards, and that is why they are written now rather than after.

- [ ] **Step 3: Add `opaqueArg` and the two shapes**

In `internal/tmux/run.go`, after `func unquote`:

```go
// commandVerbs are the verbs whose trailing argument tmux hands to a SHELL
// rather than parsing as tmux syntax.
var commandVerbs = map[string]bool{
	"new-window": true, "new-session": true, "respawn-pane": true,
	"respawn-window": true, "run-shell": true, "split-window": true,
}

// opaqueArg returns the index of the argument tmux will hand to a shell, or -1.
//
// That argument is not tmux syntax, so the target and % rules do not apply to it:
// §20's remote container is `new-window -t $0 -n nuc 'ssh -S … -t nuc tmux attach
// -t $3'`, whose payload contains ssh's own -t (a tty request) and the remote
// tmux's -t. The forbidden-FORMAT scan still applies, because Validate runs it
// over every argument before this.
//
// It keys on the OUTER verb. Keying on the payload is wrong in both directions,
// and both were measured: a real sub-command chain often begins with a flag
// continuing the outer command (`paste-buffer -b b '-t @4 ; display …'`), and
// `display -p 'OK %s'` must keep being checked because its % is the strftime hole
// that returns an empty string at rc=0.
//
// The args[len-2] test is what keeps the write path closed. `respawn-pane -k -t
// @4` has a command verb and no payload — its last argument IS the target value —
// so exempting it would admit a window target to a pane-only verb, which is the
// write-into-the-wrong-agent this seam exists to prevent.
func opaqueArg(args []string) int {
	if len(args) < 2 || !commandVerbs[unquote(args[0])] {
		return -1
	}
	if strings.HasPrefix(unquote(args[len(args)-2]), "-") {
		return -1
	}
	return len(args) - 1
}
```

In `shapeFor`, add two cases to the switch (beside `new-window`):

```go
	case "switch-client":
		// $TMUX's third field is a BARE number, and a session target needs the $
		// sigil. Without this shape `switch-client -t 0` would target a session
		// NAMED 0; with it, forgetting the sigil is a refusal at the seam.
		return sessionID, "session id"
	case "select-window":
		return windowID, "window id"
```

In `Validate`, skip the exempt index:

```go
func Validate(args []string) error {
	shape, want := shapeFor(args)
	opaque := opaqueArg(args)
	for i, a := range args {
		for _, f := range forbiddenVars {
			if strings.Contains(a, f) {
				return fmt.Errorf("%w: %s", ErrForbiddenFormat, f)
			}
		}
		// The forbidden-format scan above is deliberately NOT scoped: a format
		// hidden in a shell payload still reaches tmux.
		if i == opaque {
			continue
		}
		prev := ""
		if i > 0 {
			prev = args[i-1]
		}
		if err := validateArg(a, prev, shape, want); err != nil {
			return err
		}
	}
	if len(args) > 0 && unquote(args[len(args)-1]) == "-t" {
		return fmt.Errorf("%w: -t with no value", ErrBadTarget)
	}
	return nil
}
```

- [ ] **Step 4: Run the whole seam suite**

Run: `rtk proxy go test ./internal/tmux/ -v 2>&1 | tail -30`
Expected: PASS, including every existing test. `TestValidateStillRefusesAPercentInATemplate`, `TestValidateRejectsLiteralPercent`, `TestTheWRITEVerbsStillDemandAPaneID` and `TestValidateAcceptsTheLIFECYCLETargets` are the four that a payload-keyed fix breaks; if any of them is red, the discriminator was written on the payload instead of the outer verb.

- [ ] **Step 5: Mutation-calibrate the new guard**

Delete the `if strings.HasPrefix(...)` early return from `opaqueArg`, run `rtk proxy go test ./internal/tmux/ -run TestACommandVerbWithNoPayloadIsNotExempted`. Expected: FAIL. Restore it and confirm `git diff --stat internal/tmux/run.go` shows only the intended change.

- [ ] **Step 6: Commit**

```bash
git add internal/tmux/run.go internal/tmux/run_test.go
git commit -m "feat(tmux): the seam admits a shell payload, keyed on the outer verb"
```

---

### Task 2: `link-window` cannot enter the tree

The obvious tmux-native mechanism for possession is `link-window` — borrow the target's window into the hub's session — and measured, it is a loaded gun: `kill-window` on the borrowed copy destroys the window in every session and kills the pane's process, while the copy is indistinguishable from an owned window. The operator's own `C-b &` would silently kill a live Claude session behind a confirmation whose wording is identical to closing their own window.

The existing `TestNoSourceFileNamesAForbiddenFormat` is the pattern, but it cannot be calibrated: it walks the real repo, so there is no way to see it go red without planting a violation in the tree. This task refactors the scan to take a root, which makes both bans testable against a planted file.

**Files:**
- Modify: `internal/tmux/guard_test.go`

**Interfaces:**
- Produces: `scanTree(root string) []string` — one line per violation, `"<path>: <what>"`. Test-only; it lives in `guard_test.go`.

- [ ] **Step 1: Write the failing test**

Replace the body of `internal/tmux/guard_test.go` with:

```go
package tmux

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// exemptFiles are the files that legitimately NAME what the rest of the tree may
// not use: run.go defines the forbidden list and documents why, and the two test
// files exercise it.
var exemptFiles = map[string]bool{
	"guard_test.go": true, "run_test.go": true, "run.go": true,
}

// scanTree reports every banned construct under root, one line per violation.
//
// It takes a root rather than walking the repo directly so it can be calibrated:
// a scan that only ever runs against a clean tree is indistinguishable from a
// scan that looks at nothing, and this repo has shipped that shape before.
func scanTree(root string) []string {
	var out []string
	_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(path, ".go") {
			return err
		}
		if exemptFiles[filepath.Base(path)] {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		s := string(b)
		for _, f := range forbiddenVars {
			// The violation is a FORMAT reference, not a mention: the name inside
			// #{...} is what would reach tmux.
			if strings.Contains(s, "#{"+f) {
				out = append(out, path+": #{"+f+"} segfaults a tmux 3.2a server")
			}
		}
		// The QUOTED verb, which is what it looks like when it can reach argv.
		// Scanning for the bare word fails on the tree as it stands: measured,
		// internal/ui/flows_test.go carries "link-window" twice in the prose that
		// explains why it is banned, and the doc comment on AttachWindow (Task 3)
		// carries it a third time. A ban whose first act is to fail on the
		// documentation of itself gets deleted, not obeyed.
		if strings.Contains(s, `"link-window"`) {
			out = append(out, path+": link-window in argv — kill-window on a linked "+
				"window kills the pane's process in every session (docs/design.md §20)")
		}
		return nil
	})
	return out
}

func TestTheTreeNamesNoBannedConstruct(t *testing.T) {
	if v := scanTree(filepath.Join("..", "..")); len(v) != 0 {
		t.Errorf("banned constructs found:\n%s", strings.Join(v, "\n"))
	}
}

// The calibration the old test could not have: the scanner must go red on a
// planted violation, and it must not fire on a mention that cannot become argv.
func TestTheScannerCatchesAPlantedViolation(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("linker.go", "package x\nfunc f() { run(\"link-window\", \"-s\", \"@1\") }\n")
	write("crasher.go", "package x\nconst F = \"#{client_activity}\"\n")
	// The innocent file NAMES the ban in prose, unquoted — the shape the real tree
	// already has. A scan on the bare word fires here, which is the calibration
	// that matters; a file saying only "we never link windows" would not test it.
	write("innocent.go", "package x\n// a link-window'ed copy kills the agent; we never do it\n")

	v := scanTree(dir)
	if len(v) != 2 {
		t.Fatalf("scanTree found %d violations, want 2:\n%s", len(v), strings.Join(v, "\n"))
	}
	joined := strings.Join(v, "\n")
	for _, want := range []string{"linker.go", "crasher.go"} {
		if !strings.Contains(joined, want) {
			t.Errorf("scanTree missed %s:\n%s", want, joined)
		}
	}
	if strings.Contains(joined, "innocent.go") {
		t.Errorf("scanTree fired on prose that cannot become argv:\n%s", joined)
	}
}
```

- [ ] **Step 2: Run it**

Run: `rtk proxy go test ./internal/tmux/ -run 'TestTheTreeNamesNoBannedConstruct|TestTheScannerCatchesAPlantedViolation' -v`
Expected: both PASS. `TestTheScannerCatchesAPlantedViolation` is the calibration — if it fails, the scanner is broken, not the tree.

- [ ] **Step 3: Prove the tree scan can go red**

```bash
printf 'package tmux\n\nvar bad = []string{"link-window", "-s", "@1"}\n' > internal/tmux/planted_test_only.go
rtk proxy go test ./internal/tmux/ -run TestTheTreeNamesNoBannedConstruct
rm internal/tmux/planted_test_only.go
rtk proxy go test ./internal/tmux/ -run TestTheTreeNamesNoBannedConstruct
```
Expected: FAIL naming `planted_test_only.go`, then PASS. Confirm with `git status --porcelain` that the planted file is gone.

- [ ] **Step 4: Commit**

```bash
git add internal/tmux/guard_test.go
git commit -m "test(tmux): ban link-window in the tree, and calibrate the scanner that bans it"
```

---

### Task 3: The four possession verbs

**Files:**
- Create: `internal/tmux/possession.go`
- Test: `internal/tmux/possession_test.go`

**Interfaces:**
- Consumes: `Runner`, `Target`, `Result`, `firstNonEmpty(a, b string) string` (in `lifecycle.go`).
- Produces:
  - `SwitchClient(ctx context.Context, r Runner, t Target, sessionID string) error`
  - `SelectWindow(ctx context.Context, r Runner, t Target, windowID string) error`
  - `AttachWindow(ctx context.Context, r Runner, t Target, sessionID, name, cmd string) error`
  - `ServerEpoch(ctx context.Context, r Runner, t Target) (string, error)`

- [ ] **Step 1: Write the failing tests**

Create `internal/tmux/possession_test.go`:

```go
package tmux

import (
	"context"
	"strings"
	"testing"
)

// The pair §20 specifies, verbatim and by id. Measured on tmux 3.7b from inside a
// pane of the same server with NO -c: rc=0 both, and the attached client moves
// (status-left [hub] -> [work]).
func TestSwitchClientAndSelectWindowSendExactlyTheSpecifiedArgv(t *testing.T) {
	r := &fakeRunner{}
	if err := SwitchClient(context.Background(), r, target(), "$1"); err != nil {
		t.Fatal(err)
	}
	assertArgv(t, r.last, "switch-client", "-t", "$1")

	r2 := &fakeRunner{}
	if err := SelectWindow(context.Background(), r2, target(), "@4"); err != nil {
		t.Fatal(err)
	}
	assertArgv(t, r2.last, "select-window", "-t", "@4")
}

// RC is checked separately from err because execRunner returns a nil error for a
// non-zero tmux exit. Without this, a refused switch (the session died between the
// poll and the keypress) reads as a successful jump and the operator is told they
// were moved somewhere they are not.
func TestPossessionVerbsSurfaceANonZeroRC(t *testing.T) {
	r := &fakeRunner{rc: 1, stderr: "can't find session $9"}
	err := SwitchClient(context.Background(), r, target(), "$9")
	if err == nil || !strings.Contains(err.Error(), "can't find session $9") {
		t.Fatalf("err = %v, want tmux's own message", err)
	}
	r2 := &fakeRunner{rc: 1, stderr: "can't find window @9"}
	if err := SelectWindow(context.Background(), r2, target(), "@9"); err == nil {
		t.Fatal("SelectWindow swallowed rc=1")
	}
	r3 := &fakeRunner{rc: 1, stderr: "no space for new window"}
	if err := AttachWindow(context.Background(), r3, target(), "$0", "nuc", "ssh x"); err == nil {
		t.Fatal("AttachWindow swallowed rc=1")
	}
}

// The remote container: -n so the window list reads as the host rather than as
// `ssh`, and the command as ONE argv element because that is what tmux hands to a
// shell. No -P -F: nothing is stamped here, so there is no pane id to read back.
func TestAttachWindowNamesTheWindowAndPassesTheCommandWhole(t *testing.T) {
	r := &fakeRunner{}
	cmd := "ssh -S /home/dev/.ssh/cm-nuc -t nuc tmux attach -t $3"
	if err := AttachWindow(context.Background(), r, target(), "$0", "nuc", cmd); err != nil {
		t.Fatal(err)
	}
	assertArgv(t, r.last, "new-window", "-t", "$0", "-n", "nuc", cmd)
	if err := Validate(r.last); err != nil {
		t.Fatalf("the argv AttachWindow builds must survive the seam: %v", err)
	}
}

// Measured: `display -p -F '#{pid}:#{start_time}'` answers rc=0 with NO client
// attached, which is what makes it usable for the locality check — the hub is not
// an attached client of the servers it polls.
func TestServerEpochReadsThePidAndStartTime(t *testing.T) {
	r := &fakeRunner{stdout: "1525344:1786587652\n"}
	got, err := ServerEpoch(context.Background(), r, target())
	if err != nil {
		t.Fatal(err)
	}
	if got != "1525344:1786587652" {
		t.Fatalf("epoch = %q, want %q", got, "1525344:1786587652")
	}
	assertArgv(t, r.last, "display", "-p", "-F", "#{pid}:#{start_time}")
}

// An empty answer at rc=0 is the shape that fails OPEN: a locality check that
// compares "" to "" would call every server the same one, and the hub would run
// switch-client against a remote pane id.
func TestServerEpochRefusesAnEmptyAnswer(t *testing.T) {
	r := &fakeRunner{stdout: "\n"}
	if _, err := ServerEpoch(context.Background(), r, target()); err == nil {
		t.Fatal("an empty epoch must be an error, not an empty string")
	}
}
```

- [ ] **Step 2: Run and watch them fail**

Run: `rtk proxy go test ./internal/tmux/ -run 'Possession|SwitchClient|AttachWindow|ServerEpoch' -v`
Expected: build failure — `undefined: SwitchClient`, `SelectWindow`, `AttachWindow`, `ServerEpoch`.

- [ ] **Step 3: Write the verbs**

Create `internal/tmux/possession.go`:

```go
package tmux

import (
	"context"
	"fmt"
	"strings"
)

// SwitchClient moves the attached client to a session on THIS server.
//
// Measured on tmux 3.7b, run from inside a pane of the same server with no -c:
// rc=0, and the client moves — status-left goes [hub] to [work]. The client is
// resolved from the invoking pane's own $TMUX, so the hub does not have to name
// one; naming one would also be wrong, because a session can have several.
//
// By session ID, never by name: a name does not survive a rename (§7). The seam
// enforces the shape, which matters here more than anywhere else — $TMUX's third
// field is a BARE number, so a forgotten $ sigil would target a session named 0.
func SwitchClient(ctx context.Context, r Runner, t Target, sessionID string) error {
	res, err := r.Run(ctx, t, "switch-client", "-t", sessionID)
	if err != nil {
		return err
	}
	if res.RC != 0 {
		return fmt.Errorf("switch-client: %s",
			firstNonEmpty(res.Stderr, fmt.Sprintf("tmux exited %d", res.RC)))
	}
	return nil
}

// SelectWindow makes windowID current in its session, by window ID.
func SelectWindow(ctx context.Context, r Runner, t Target, windowID string) error {
	res, err := r.Run(ctx, t, "select-window", "-t", windowID)
	if err != nil {
		return err
	}
	if res.RC != 0 {
		return fmt.Errorf("select-window: %s",
			firstNonEmpty(res.Stderr, fmt.Sprintf("tmux exited %d", res.RC)))
	}
	return nil
}

// AttachWindow opens a window in sessionID running cmd — the container §20 puts
// the existing remote attach in, so the attach stops taking the hub's terminal.
//
// It is a sibling of NewWindow rather than a parameter on it, deliberately.
// NewWindow's contract is "create a pane and tell me its id" (that is what makes
// identity-at-birth possible, §19) and it takes a cwd; this one needs a window
// NAME, has no cwd, and has no id to read back because nothing is stamped in it —
// the window is a viewport the operator closes. Adding -n to NewWindow would
// change a signature used at nine call sites for a parameter one caller needs.
//
// Measured: closing this window leaves the other server's pane_pid unchanged and
// its session present, with the client count dropping to zero. Closing a
// link-window'ed copy kills the agent instead — the asymmetry the whole design
// rests on, which is why the hub only ever creates windows it owns.
func AttachWindow(ctx context.Context, r Runner, t Target, sessionID, name, cmd string) error {
	res, err := r.Run(ctx, t, "new-window", "-t", sessionID, "-n", name, cmd)
	if err != nil {
		return err
	}
	if res.RC != 0 {
		return fmt.Errorf("new-window: %s",
			firstNonEmpty(res.Stderr, fmt.Sprintf("tmux exited %d", res.RC)))
	}
	return nil
}

// ServerEpoch is what a server says about itself: `#{pid}:#{start_time}`, the
// same string the registry already carries on every pane (delta.go). Comparing it
// is how the hub decides "same server" — an equality of what each end REPORTS,
// never of the paths the operator typed, because a symlink to a socket reaches
// the same server while comparing unequal (measured).
//
// Measured: this answers rc=0 with NO client attached, which the hub needs — it
// is not an attached client of the servers it polls.
func ServerEpoch(ctx context.Context, r Runner, t Target) (string, error) {
	res, err := r.Run(ctx, t, "display", "-p", "-F", "#{pid}:#{start_time}")
	if err != nil {
		return "", err
	}
	if res.RC != 0 {
		return "", fmt.Errorf("display: %s",
			firstNonEmpty(res.Stderr, fmt.Sprintf("tmux exited %d", res.RC)))
	}
	// An empty answer at rc=0 fails OPEN: two empty epochs compare equal, and the
	// hub would then run switch-client against a pane on somebody else's server.
	epoch := strings.TrimSpace(res.Stdout)
	if epoch == "" {
		return "", fmt.Errorf("display: empty #{pid}:#{start_time} — cannot identify this server")
	}
	return epoch, nil
}
```

- [ ] **Step 4: Run the tests**

Run: `rtk proxy go test ./internal/tmux/ -v 2>&1 | tail -20`
Expected: PASS, all of them.

- [ ] **Step 5: Commit**

```bash
git add internal/tmux/possession.go internal/tmux/possession_test.go
git commit -m "feat(tmux): switch-client, select-window, attach-window and the server epoch"
```

---

### Task 4: The hub knows its own session

`$TMUX` is `socket,server_pid,session_id`. `SelfSocket` already reads the first field. The third is a **bare number** while every tmux session target needs the `$` sigil, so `0` must become `$0`; forgetting it would target a session *named* `0`, which Task 1's shape now refuses at the seam.

**Files:**
- Modify: `internal/hub/poll.go` (add after `SelfSocket`, around line 157)
- Test: `internal/hub/poll_test.go`

**Interfaces:**
- Produces: `hub.SelfSessionID() string` — the hub's own session as `$N`, or `""` when the hub is not inside tmux.

- [ ] **Step 1: Write the failing test**

Add to `internal/hub/poll_test.go`:

```go
// $TMUX is socket,server_pid,session_id, and the third field is a BARE number
// while every session target needs the $ sigil. The bare form would name a
// session CALLED 0, which is a different session that may well exist.
func TestSelfSessionIDAddsTheSigil(t *testing.T) {
	for _, tc := range []struct{ env, want string }{
		{"/tmp/tmux-1000/default,4242,0", "$0"},
		{"/tmp/tmux-1000/default,4242,17", "$17"},
		{"", ""},
		{"/tmp/tmux-1000/default", ""},          // no session field at all
		{"/tmp/tmux-1000/default,4242", ""},     // still no session field
		{"/tmp/tmux-1000/default,4242,", ""},    // present but empty
		{"/tmp/tmux-1000/default,4242,abc", ""}, // not a number: refuse rather than guess
	} {
		t.Setenv("TMUX", tc.env)
		if got := SelfSessionID(); got != tc.want {
			t.Errorf("TMUX=%q: SelfSessionID() = %q, want %q", tc.env, got, tc.want)
		}
	}
}
```

- [ ] **Step 2: Run it**

Run: `rtk proxy go test ./internal/hub/ -run TestSelfSessionIDAddsTheSigil -v`
Expected: build failure — `undefined: SelfSessionID`.

- [ ] **Step 3: Implement**

In `internal/hub/poll.go`, after `SelfSocket`:

```go
// SelfSessionID is the session the hub's own pane belongs to, as a tmux session
// target (`$N`), or "" when the hub is not inside tmux.
//
// $TMUX is `socket,server_pid,session_id` and the third field is a BARE number,
// while every session target needs the $ sigil — so `0` must become `$0` or the
// command would address a session NAMED 0. Task 1's shape for switch-client
// refuses the bare form at the seam, but the conversion belongs here, where the
// field is read.
//
// A malformed or non-numeric third field returns "" rather than a guess: an empty
// answer disables possession and falls back to today's full-screen attach, which
// is the safe direction.
func SelfSessionID() string {
	v := os.Getenv("TMUX")
	if v == "" {
		return ""
	}
	parts := strings.Split(v, ",")
	if len(parts) < 3 || parts[2] == "" {
		return ""
	}
	for _, c := range parts[2] {
		if c < '0' || c > '9' {
			return ""
		}
	}
	return "$" + parts[2]
}
```

- [ ] **Step 4: Run**

Run: `rtk proxy go test ./internal/hub/ -run TestSelfSessionIDAddsTheSigil -v`
Expected: PASS.

- [ ] **Step 5: Mutation-calibrate**

Change `return "$" + parts[2]` to `return parts[2]`, run the test. Expected: FAIL on the first two cases. Restore.

- [ ] **Step 6: Commit**

```bash
git add internal/hub/poll.go internal/hub/poll_test.go
git commit -m "feat(hub): read the hub's own session id out of \$TMUX, with the sigil"
```

---

### Task 5: The locality decision

"Same server" is an equality of what each server **reports**, never of the strings the operator typed: measured, a symlink to a socket reaches the same server while comparing unequal, and tmux canonicalises for us — reached through a symlink, a server reports the same `#{socket_path}` and `#{pid}`. The registry already carries `Epoch` (`#{pid}:#{start_time}`) on every pane, so one read of the hub's own server closes it, and there is one notion of server identity rather than two.

**Files:**
- Create: `internal/ui/possession.go`
- Test: `internal/ui/possession_test.go`

**Interfaces:**
- Consumes: `registry.Pane` (fields `Host`, `PaneID`, `SessionID`, `WindowID`, `Session`, `Window`, `Kind`, `Epoch`), `hub.Host` (`Label`, `Socket`, `SSHDest`, `ControlPath`, `Remote()`), `hub.SelfSessionID()`, `AttachCmd`, `hostFor`.
- Produces:
  - `type possessionPath int` with `pathJump`, `pathWindow`, `pathFullScreen`, `pathRefuse`
  - `func decidePossession(p registry.Pane, h hub.Host, selfSession, selfEpoch string) (possessionPath, string)` — the path and, for `pathRefuse`, the note text.

- [ ] **Step 1: Write the failing test**

Create `internal/ui/possession_test.go`:

```go
package ui

import (
	"strings"
	"testing"

	"github.com/DawnBreather/tmux-hub/internal/hub"
	"github.com/DawnBreather/tmux-hub/internal/registry"
)

func localHost() hub.Host {
	return hub.Host{Label: "local", Socket: "/tmp/tmux-1000/default", LocalProc: true}
}

func remoteHost() hub.Host {
	return hub.Host{Label: "nuc", Socket: "/run/user/1000/nuc.sock",
		SSHDest: "nuc", ControlPath: "/home/dev/.ssh/cm-nuc"}
}

func pane(host, epoch string) registry.Pane {
	return registry.Pane{Kind: registry.KindPane, Host: host, Session: "api", Window: "review",
		PaneID: "%0", SessionID: "$1", WindowID: "@4", Epoch: epoch}
}

// The hub's own server: a jump, which is the case §20 exists for.
func TestSameEpochIsAJump(t *testing.T) {
	got, note := decidePossession(pane("local", "999:111"), localHost(), "$0", "999:111")
	if got != pathJump {
		t.Fatalf("path = %v, want pathJump (note %q)", got, note)
	}
}

// Another server: a window holding the existing attach, never a jump. A
// switch-client against a pane id from a different server would either fail or,
// worse, find an unrelated session with that id.
func TestADifferentEpochIsAWindow(t *testing.T) {
	got, _ := decidePossession(pane("nuc", "222:333"), remoteHost(), "$0", "999:111")
	if got != pathWindow {
		t.Fatalf("path = %v, want pathWindow", got)
	}
}

// The fifth path §20's table does not name. Outside tmux there is no client to
// switch and no session to hold a window, so both new paths are impossible and
// the honest answer is today's behaviour rather than a refusal.
func TestOutsideTmuxEveryPathFallsBackToTheFullScreenAttach(t *testing.T) {
	for _, tc := range []struct {
		name string
		p    registry.Pane
		h    hub.Host
	}{
		{"local target", pane("local", "999:111"), localHost()},
		{"remote target", pane("nuc", "222:333"), remoteHost()},
	} {
		got, note := decidePossession(tc.p, tc.h, "", "999:111")
		if got != pathFullScreen {
			t.Errorf("%s: path = %v, want pathFullScreen (note %q)", tc.name, got, note)
		}
	}
}

// An unknown own-server identity must not read as "same server". Two empty
// epochs compare equal, which would send switch-client at a remote pane.
func TestAnEmptySelfEpochIsNeverASameServerMatch(t *testing.T) {
	got, _ := decidePossession(pane("local", ""), localHost(), "$0", "")
	if got == pathJump {
		t.Fatal("an unknown epoch matched itself and produced a jump")
	}
}

// The two refusals already exist and already carry their fix. §20 does not
// reword them; it routes to them.
func TestTheRefusalsKeepNamingTheMissingThing(t *testing.T) {
	noCtl := remoteHost()
	noCtl.ControlPath = ""
	got, note := decidePossession(pane("nuc", "222:333"), noCtl, "$0", "999:111")
	if got != pathRefuse {
		t.Fatalf("path = %v, want pathRefuse", got)
	}
	if !strings.Contains(note, "has no ssh control path") {
		t.Errorf("note = %q, want it to name the missing field", note)
	}

	agent := registry.Pane{Kind: registry.KindAgent, Host: "local", Session: "api"}
	got2, note2 := decidePossession(agent, localHost(), "$0", "999:111")
	if got2 != pathRefuse {
		t.Fatalf("path = %v, want pathRefuse", got2)
	}
	if !strings.Contains(note2, "nothing to attach to until it runs in one") {
		t.Errorf("note = %q, want the agent-row explanation", note2)
	}
}
```

- [ ] **Step 2: Run it**

Run: `rtk proxy go test ./internal/ui/ -run 'Possession|Epoch|Refusals|Jump|Window' -v`
Expected: build failure — `undefined: decidePossession`, `pathJump`, …

- [ ] **Step 3: Implement**

Create `internal/ui/possession.go`:

```go
package ui

import (
	"github.com/DawnBreather/tmux-hub/internal/hub"
	"github.comth/DawnBreather/tmux-hub/internal/registry"
)

// possessionPath is what `a` will do for the row under the cursor.
type possessionPath int

const (
	// pathJump is the target on the hub's own tmux server: switch-client plus
	// select-window, the hub keeps running, the way back is C-b L.
	pathJump possessionPath = iota
	// pathWindow is the target on another server: today's ssh attach, unchanged,
	// in a new window of the hub's own session.
	pathWindow
	// pathFullScreen is today's behaviour, and it is what happens when the hub is
	// not inside tmux — there is no client to switch and no session to hold a
	// window, so possession is impossible and taking the terminal is honest.
	pathFullScreen
	// pathRefuse is one of the two existing refusals, each of which already names
	// the thing that is missing.
	pathRefuse
)

// decidePossession chooses the path for one row, and returns the note to show
// when the answer is a refusal.
//
// Nothing here is inferred from a string the operator typed. Locality is an
// equality of what each server REPORTS about itself — the pane's Epoch, which is
// `#{pid}:#{start_time}` from the same server, against the hub's own server's —
// because a symlinked socket path reaches the same server while comparing
// unequal (measured). This is the rule Host.LocalProc already establishes: the
// hub never guesses which machine or server something is on.
func decidePossession(p registry.Pane, h hub.Host, selfSession, selfEpoch string) (possessionPath, string) {
	// The refusals come first: they are about the ROW, so they hold whatever the
	// hub's own situation is.
	if _, err := AttachCmd(p, h); err != nil {
		return pathRefuse, "cannot attach: " + err.Error()
	}
	if selfSession == "" {
		return pathFullScreen, ""
	}
	// An unknown epoch must never match itself: two empty strings compare equal,
	// and a jump aimed at another server's session id is the one outcome that
	// puts the operator somewhere nobody checked.
	if selfEpoch != "" && p.Epoch == selfEpoch {
		return pathJump, ""
	}
	if h.Remote() {
		return pathWindow, ""
	}
	// A local server that is not the hub's own — a second socket on this machine.
	// switch-client cannot cross servers, so it takes the window path too, and
	// AttachCmd has already confirmed the socket is known.
	return pathWindow, ""
}
```

**Note for the implementer:** the import line above contains a deliberate typo (`github.com th/…`) so that a copy-paste without reading fails to compile. Write `github.com/DawnBreather/tmux-hub/internal/registry`.

- [ ] **Step 4: Run**

Run: `rtk proxy go test ./internal/ui/ -run 'Possession|Epoch|Refusals|Jump|Window' -v`
Expected: PASS.

- [ ] **Step 5: Mutation-calibrate the empty-epoch guard**

Change `if selfEpoch != "" && p.Epoch == selfEpoch` to `if p.Epoch == selfEpoch`, run `rtk proxy go test ./internal/ui/ -run TestAnEmptySelfEpochIsNeverASameServerMatch`. Expected: FAIL. Restore.

- [ ] **Step 6: Commit**

```bash
git add internal/ui/possession.go internal/ui/possession_test.go
git commit -m "feat(ui): decide the possession path from what each server reports"
```

---

### Task 6: `a` dispatches, and the hub remembers where it sent the operator

**Files:**
- Modify: `internal/ui/model.go:782-799` (the `case "a":` block), and the `model` struct
- Modify: `internal/ui/possession.go` (the commands)
- Test: `internal/ui/possession_test.go`

**Interfaces:**
- Consumes: `decidePossession` (Task 5), `tmux.SwitchClient`/`SelectWindow`/`AttachWindow`/`ServerEpoch` (Task 3), `hub.SelfSessionID` (Task 4), `m.run`, `m.ctx`, `socketFor(m.hosts, label)`, `tea.ExecProcess`, `attachedMsg`.
- Produces: `m.possess(p registry.Pane, h hub.Host) (tea.Model, tea.Cmd)`; `possessedMsg{from string; err error}`; `model.sentTo string`; `model.selfEpoch string`.

- [ ] **Step 1: Write the failing test**

Add to `internal/ui/possession_test.go`:

```go
// The jump must send the two commands §20 specifies, by ID, in that order.
// Order is not cosmetic: select-window addresses a window in the session the
// client is now displaying.
func TestTheJumpSendsSwitchClientThenSelectWindow(t *testing.T) {
	rec := &recordingRunner{}
	m := newTestModel(t, rec)
	m.selfEpoch = "999:111"
	p := pane("local", "999:111")
	_, cmd := m.possess(p, localHost())
	if cmd == nil {
		t.Fatal("possess returned no command for a same-server target")
	}
	msg := cmd()
	if got, ok := msg.(possessedMsg); !ok || got.err != nil {
		t.Fatalf("msg = %#v, want a clean possessedMsg", msg)
	}
	if len(rec.calls) != 2 {
		t.Fatalf("sent %d commands, want 2: %q", len(rec.calls), rec.calls)
	}
	assertCall(t, rec.calls[0], "switch-client", "-t", "$1")
	assertCall(t, rec.calls[1], "select-window", "-t", "@4")
}

// The remote path reuses AttachCmd's argv UNCHANGED and only changes its
// container. If this test has to be edited to accommodate a reworded ssh
// command, the change went further than §20 allows.
func TestTheWindowPathWrapsTheExistingAttachUnchanged(t *testing.T) {
	rec := &recordingRunner{}
	m := newTestModel(t, rec)
	m.selfEpoch = "999:111"
	h := remoteHost()
	p := pane("nuc", "222:333")

	want, err := AttachCmd(p, h)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(want.Args, " ")

	_, cmd := m.possess(p, h)
	if cmd == nil {
		t.Fatal("possess returned no command for a remote target")
	}
	cmd()
	if len(rec.calls) != 1 {
		t.Fatalf("sent %d commands, want 1: %q", len(rec.calls), rec.calls)
	}
	assertCall(t, rec.calls[0], "new-window", "-t", "$0", "-n", "nuc", joined)
}

// The hub's only state: where it sent the operator, so it can say so on return.
func TestReturningNamesWhereTheOperatorWas(t *testing.T) {
	m := newTestModel(t, &recordingRunner{})
	m.selfEpoch = "999:111"
	m.possess(pane("local", "999:111"), localHost())
	m2, _ := m.Update(possessedMsg{from: "api:review"})
	view := m2.(model).View()
	if !strings.Contains(view, "back from api:review") {
		t.Fatalf("View() does not say where the operator came back from:\n%s", view)
	}
}
```

Add the recording runner and helpers to the same file:

```go
// recordingRunner records every argv the model sends, in order.
type recordingRunner struct{ calls [][]string }

func (r *recordingRunner) Run(ctx context.Context, t tmux.Target, args ...string) (tmux.Result, error) {
	r.calls = append(r.calls, args)
	return tmux.Result{Stdout: "999:111\n"}, nil
}

func assertCall(t *testing.T, got []string, want ...string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("argv = %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("argv[%d] = %q, want %q (full %q)", i, got[i], want[i], got)
		}
	}
}

// newTestModel is a model with the two hosts the possession tests use and a
// runner that records. It sets $TMUX so the hub counts as being inside tmux.
func newTestModel(t *testing.T, r tmux.Runner) model {
	t.Helper()
	t.Setenv("TMUX", "/tmp/tmux-1000/default,4242,0")
	s, err := hide.Open(filepath.Join(t.TempDir(), "hidden.json"))
	if err != nil {
		t.Fatal(err)
	}
	return model{
		panes:       []registry.Pane{pane("local", "999:111"), pane("nuc", "222:333")},
		hosts:       []hub.Host{localHost(), remoteHost()},
		hidden:      s,
		width:       120,
		height:      24,
		run:         r,
		ctx:         context.Background(),
		selfSession: hub.SelfSessionID(),
		atSelection: map[SelectionKey]paneSnapshot{},
		lastOutcome: map[SelectionKey]broadcast.Outcome{},
	}
}
```

- [ ] **Step 2: Run and watch it fail**

Run: `rtk proxy go test ./internal/ui/ -run 'TestTheJumpSends|TestTheWindowPath|TestReturningNames' -v`
Expected: build failure — `undefined: m.possess`, `possessedMsg`, `model.selfEpoch`, `model.selfSession`.

- [ ] **Step 3: Add the fields**

In the `model` struct in `internal/ui/model.go`, beside the other identity fields:

```go
	// selfSession and selfEpoch are the hub's own coordinates: which session its
	// pane belongs to ($N, or "" outside tmux) and what its own tmux server says
	// about itself (#{pid}:#{start_time}). Both are read once at startup — the
	// hub's own pane cannot move between servers, and if the server restarts the
	// hub's pane is gone with it.
	selfSession string
	selfEpoch   string

	// sentTo is the one thing possession stores: the session:window the operator
	// was last sent to, so the hub can say where they came back from. There is
	// nothing else to clean up — no borrowed window, no saved geometry, no unlink.
	sentTo string
```

- [ ] **Step 4: Write the dispatcher**

Add to `internal/ui/possession.go`:

```go
type possessedMsg struct {
	from string
	err  error
}

// possess is what `a` does. The hub renders nothing: for a target on its own
// server it moves the client, and for one on another server it puts today's
// unchanged ssh attach in a window of its own session.
func (m model) possess(p registry.Pane, h hub.Host) (tea.Model, tea.Cmd) {
	path, note := decidePossession(p, h, m.selfSession, m.selfEpoch)
	switch path {
	case pathRefuse:
		m.note = note
		return m, nil

	case pathFullScreen:
		// Not inside tmux: today's behaviour, which takes the terminal. Possession
		// needs a client to switch and a session to hold a window, and there is
		// neither.
		c, err := AttachCmd(p, h)
		if err != nil {
			m.note = "cannot attach: " + err.Error()
			return m, nil
		}
		m.note = ""
		return m, tea.ExecProcess(c, func(err error) tea.Msg { return attachedMsg{err} })

	case pathJump:
		where := p.Session + ":" + p.Window
		m.note, m.sentTo = "", where
		r, ctx := m.run, m.ctx
		tgt := tmux.Target{Label: h.Label, Socket: h.Socket}
		sess, win := p.SessionID, p.WindowID
		return m, func() tea.Msg {
			// Order matters: select-window addresses a window in the session the
			// client is displaying, so the switch has to land first.
			if err := tmux.SwitchClient(ctx, r, tgt, sess); err != nil {
				return possessedMsg{err: err}
			}
			if err := tmux.SelectWindow(ctx, r, tgt, win); err != nil {
				return possessedMsg{err: err}
			}
			return possessedMsg{from: where}
		}

	default: // pathWindow
		c, err := AttachCmd(p, h)
		if err != nil {
			m.note = "cannot attach: " + err.Error()
			return m, nil
		}
		// The attach argv is reused WHOLE. tmux hands a trailing argument to a
		// shell, and Task 1's opaqueArg is what lets it through the seam with
		// ssh's own -t and the remote tmux's -t inside it.
		payload := strings.Join(c.Args, " ")
		where := p.Session + ":" + p.Window
		m.note, m.sentTo = "", where
		r, ctx := m.run, m.ctx
		self := tmux.Target{Label: hub.LocalLabel, Socket: hub.SelfSocket()}
		sess, name := m.selfSession, h.Label
		return m, func() tea.Msg {
			if err := tmux.AttachWindow(ctx, r, self, sess, name, payload); err != nil {
				return possessedMsg{err: err}
			}
			return possessedMsg{from: where}
		}
	}
}
```

- [ ] **Step 5: Wire the key and the message**

Replace `internal/ui/model.go:782-799` (the whole `case "a":` block) with:

```go
		case "a":
			visible := m.visibleRows()
			if m.cursor >= len(visible) {
				return m, nil
			}
			p := visible[m.cursor]
			h, ok := hostFor(m.hosts, p.Host)
			if !ok {
				m.note = "cannot attach: host " + p.Host + " is not in this hub's list"
				return m, nil
			}
			return m.possess(p, h)
```

And add a case beside `attachedMsg` in `Update`:

```go
	case possessedMsg:
		if msg.err != nil {
			m.note = "cannot go there: " + msg.err.Error()
			return m, nil
		}
		if msg.from != "" {
			m.note = "back from " + msg.from
		}
		return m, nil
```

- [ ] **Step 6: Read the hub's own epoch at startup**

In `Run` (the constructor in `internal/ui/model.go`), beside where the model is built:

```go
	// The hub's own server identity, read once. A failure is not fatal: an unknown
	// epoch disables the jump path and every target takes the window path, which
	// works everywhere and is exactly the behaviour before §20.
	selfSession := hub.SelfSessionID()
	selfEpoch := ""
	if s := hub.SelfSocket(); s != "" {
		if e, err := tmux.ServerEpoch(ctx, run, tmux.Target{Label: hub.LocalLabel, Socket: s}); err == nil {
			selfEpoch = e
		}
	}
```

and pass both into the model literal.

- [ ] **Step 7: Run the suite**

Run: `rtk proxy go test -race ./internal/ui/ 2>&1 | tail -20`
Expected: PASS.

- [ ] **Step 8: Mutation-calibrate the order**

Swap the `SwitchClient`/`SelectWindow` calls in `pathJump`, run `rtk proxy go test ./internal/ui/ -run TestTheJumpSendsSwitchClientThenSelectWindow`. Expected: FAIL. Restore.

- [ ] **Step 9: Commit**

```bash
git add internal/ui/possession.go internal/ui/possession_test.go internal/ui/model.go
git commit -m "feat(ui): a dispatches over five paths and the hub stops taking its own terminal"
```

---

### Task 7: The header hint becomes path-specific

The hint §8 added — `nested: leave an attached session with C-b C-b d` — stays true for a remote jump's inner tmux and is **misleading** for a same-server jump, where the way back is `C-b L` and nothing is detached. The approved target frames are in `docs/ui-flows-possession.html`, section 4.

**Files:**
- Modify: `internal/ui/render.go:214-223`
- Modify: every `Render` call site (`internal/ui/model.go`, `internal/ui/render.go:548`)
- Test: `internal/ui/render_test.go`

**Interfaces:**
- Consumes: `Nested() bool`, `LayoutFor`, `lines.Truncate`.
- Produces: `Render` takes one more parameter, `hint string`, immediately after `note`. `hintFor(path possessionPath) string` in `internal/ui/possession.go` returns the text.

- [ ] **Step 1: Write the failing test**

Add to `internal/ui/render_test.go`:

```go
// The hint has to say the way back for the path `a` will actually take. One
// phrase for all paths was true for exactly one of them, and it named a detach
// that a jump does not perform.
func TestTheHintNamesTheWayBackForThisRow(t *testing.T) {
	t.Setenv("TMUX", "/tmp/tmux-1000/default,4242,0")
	for _, tc := range []struct {
		path possessionPath
		want string
		deny string
	}{
		{pathJump, "C-b L", "C-b C-b d"},
		{pathWindow, "C-b C-b d", ""},
	} {
		got := hintFor(tc.path)
		if !strings.Contains(got, tc.want) {
			t.Errorf("hintFor(%v) = %q, want it to name %q", tc.path, got, tc.want)
		}
		if tc.deny != "" && strings.Contains(got, tc.deny) {
			t.Errorf("hintFor(%v) = %q, must not name %q — this path detaches nothing",
				tc.path, got, tc.deny)
		}
	}
	// Outside tmux there is nothing nested to warn about.
	t.Setenv("TMUX", "")
	if got := hintFor(pathJump); got != "" {
		t.Errorf("hintFor outside tmux = %q, want empty", got)
	}
}

// And it has to reach the screen. A hint that is computed and not rendered is the
// defect this repo has shipped before: four interface modes were fully covered
// and never drawn.
func TestTheHintIsOnTheScreenViewReturns(t *testing.T) {
	t.Setenv("TMUX", "/tmp/tmux-1000/default,4242,0")
	m := newTestModel(t, &recordingRunner{})
	m.selfEpoch = "999:111"
	m.cursor = 0 // the local pane: a jump
	if v := m.View(); !strings.Contains(v, "C-b L") {
		t.Fatalf("View() lacks the jump hint:\n%s", v)
	}
	m.cursor = 1 // the remote pane: a window
	if v := m.View(); !strings.Contains(v, "C-b C-b d") {
		t.Fatalf("View() lacks the window hint:\n%s", v)
	}
}
```

- [ ] **Step 2: Run it**

Run: `rtk proxy go test ./internal/ui/ -run 'TestTheHint' -v`
Expected: build failure — `undefined: hintFor`.

- [ ] **Step 3: Implement `hintFor`**

Add to `internal/ui/possession.go`:

```go
// hintFor is the header's one-line reminder of how to come back, for the path `a`
// would take on the row under the cursor.
//
// It is empty outside tmux: the warning exists because both servers share the
// default prefix, and there is no outer session to be thrown out of.
func hintFor(path possessionPath) string {
	if !Nested() {
		return ""
	}
	switch path {
	case pathJump:
		return "a → jump into the pane, C-b L comes back"
	case pathWindow:
		return "a → a window with the attach, C-b C-b d leaves the inner tmux"
	default:
		// pathFullScreen takes the terminal, which is what the original phrasing
		// described, and pathRefuse never leaves the hub.
		return "nested: leave an attached session with C-b C-b d"
	}
}
```

- [ ] **Step 4: Thread it through `Render`**

In `internal/ui/render.go`, change the signature and the header block:

```go
func Render(panes []registry.Pane, hosts []hub.Host, width, height, cursor int, marked map[string]bool, note, hint string, hiddenCount, blockedCount int, resurfaced map[string]bool) string {
```

```go
	head := "tmux-hub  " + plural(len(panes), "session", "sessions")
	if hint != "" {
		head += "   · " + hint
	}
	out = append(out, lines.Truncate(head, width))
```

Delete the `if Nested() { … }` block: the decision now lives in `hintFor`, which the caller applies to the row under the cursor. Update `RenderCompose` at `render.go:548` and every `Render` call in `model.go` to pass the extra argument — in `View()` that is `hintFor(m.pathForCursor())`, where:

```go
// pathForCursor is the path `a` would take right now, which is what the header
// hint describes. A row that is gone (an empty list, a cursor past the end)
// yields pathRefuse, whose hint is the ambient nested warning.
func (m model) pathForCursor() possessionPath {
	visible := m.visibleRows()
	if m.cursor >= len(visible) {
		return pathRefuse
	}
	p := visible[m.cursor]
	h, ok := hostFor(m.hosts, p.Host)
	if !ok {
		return pathRefuse
	}
	path, _ := decidePossession(p, h, m.selfSession, m.selfEpoch)
	return path
}
```

- [ ] **Step 5: Run everything that renders**

Run: `rtk proxy go test -race ./internal/ui/ 2>&1 | tail -25`
Expected: PASS. Any test asserting the old phrase on a same-server row must be updated to the new one — and check each such edit is a change of expectation, not a test being loosened.

- [ ] **Step 6: Regenerate both documents and diff the target**

```bash
prototypes/possession-captures.sh /tmp/flows
HUB_FLOW_CAPTURES=/tmp/flows rtk proxy go test -tags mockup -run 'TestGenerateFlows|TestGenerateMockup' ./internal/ui/
```
Then, for each of the two drawn frames in section 4 of `docs/ui-flows-possession.html`, extract the real frame and the approved target and compare:

```bash
python3 prototypes/framediff.py /tmp/flows/real-hint-jump.txt /tmp/flows/target-hint-jump.txt
```
Expected: the hint line matches the approved target. A divergence is a defect in the code or in the target; decide which, and say so in the commit.

- [ ] **Step 7: Commit**

```bash
git add internal/ui/render.go internal/ui/render_test.go internal/ui/possession.go internal/ui/model.go docs/ui-flows-possession.html docs/ui-mockup.html
git commit -m "feat(ui): the header hint names the way back for the path a would take"
```

---

### Task 8: The e2e case — a real jump against a real server

The unit tests prove the argv. Only a real tmux proves the client moves, and this is the one path whose failure mode is invisible to a fake: a `switch-client` that returns rc=0 without moving anything.

**Files:**
- Create: `internal/e2e/possession_test.go` (build tag `e2e`)

**Interfaces:**
- Consumes: whatever socket/session helpers `internal/e2e` already provides — read `internal/e2e/lifecycle_test.go` first and reuse its harness rather than writing a second one.

- [ ] **Step 1: Read the existing harness**

Run: `sed -n '1,60p' internal/e2e/lifecycle_test.go` and reuse its private-socket setup, its `t.Cleanup` kill-server, and its runner construction. Do not create a new pattern.

- [ ] **Step 2: Write the test**

```go
//go:build e2e

package e2e

import (
	"context"
	"strings"
	"testing"

	"github.com/DawnBreather/tmux-hub/internal/tmux"
)

// A jump is the one path whose failure is invisible to a fake runner: switch-client
// can return rc=0 and move nothing. So this asserts the CONSEQUENCE — which session
// the client is displaying — rather than the call.
//
// It needs a real attached client, which is why the harness runs an outer tmux on a
// second private socket and attaches from inside it.
func TestAJumpMovesTheAttachedClient(t *testing.T) {
	ctx := context.Background()
	inner := newServer(t)  // reuse the helper from lifecycle_test.go
	outer := newServer(t)
	r := tmux.NewExec(5 * time.Second)

	hubSession := mustNewSession(t, inner, "hub")
	workSession := mustNewSession(t, inner, "work")
	workWindow := mustDisplay(t, inner, "work", "#{window_id}")

	attachFromOuter(t, outer, inner, "hub")   // leaves a client on session hub
	waitForClient(t, inner, "hub")

	if err := tmux.SwitchClient(ctx, r, inner.target(), workSession); err != nil {
		t.Fatal(err)
	}
	if err := tmux.SelectWindow(ctx, r, inner.target(), workWindow); err != nil {
		t.Fatal(err)
	}

	got := mustDisplay(t, inner, "", "#{client_session}")
	if got != "work" {
		t.Fatalf("client is displaying %q, want %q — switch-client returned rc=0 and moved nothing",
			got, "work")
	}
	_ = hubSession

	// And the negative half: the outer server must be untouched, because a jump is
	// not allowed to reach across servers.
	if s := mustDisplay(t, outer, "", "#{session_name}"); !strings.Contains(s, "view") {
		t.Fatalf("the outer server moved: %q", s)
	}
}
```

**Note:** the helper names above (`newServer`, `mustNewSession`, `mustDisplay`, `attachFromOuter`, `waitForClient`) are what this task must either find in `internal/e2e` or add there. If they do not exist under those names, use the existing equivalents — do not add a second harness.

- [ ] **Step 3: Run it**

Run: `rtk proxy go test -tags e2e -run TestAJumpMovesTheAttachedClient ./internal/e2e/ -v`
Expected: PASS.

- [ ] **Step 4: Vet the tagged package**

Run: `rtk proxy go vet -tags e2e ./internal/e2e/`
Expected: clean. This package is invisible to `go vet ./...` — measured, a 36-case suite stayed broken across four tasks because of exactly that.

- [ ] **Step 5: Commit**

```bash
git add internal/e2e/possession_test.go
git commit -m "test(e2e): a jump moves the attached client, asserted on the consequence"
```

---

### Task 9: The docs stop describing the old `a`

**Files:**
- Modify: `docs/design.md` §16 (the key table), §8 (the attach section's parked row), §20 (the fifth path)
- Modify: `README.md` (the key list, if it names `a`)
- Modify: `docs/known-issues.md` (close anything this branch fixed)

- [ ] **Step 1: Find every claim about `a`**

Run: `grep -rn 'attach (full-screen)\|C-b C-b d\|full-screen' docs/ README.md`

- [ ] **Step 2: Rewrite §16's row**

The row must read `a  go to it — a jump on this server, a window for another (§20)`. Check the surrounding rows are still true: the same edit that makes one sentence true usually makes its neighbour false, because both described the old state.

- [ ] **Step 3: Add the fifth path to §20's table**

```markdown
| the hub is not inside tmux (`$TMUX` empty) | today's full-screen attach — there is no client to switch and no session to hold a window |
```

and one sentence saying why that row exists: possession is a property of running inside tmux, and a hub started from a plain terminal must not lose `a`.

- [ ] **Step 4: Update §8's parked row**

§8's attach table carries *"`$TMUX` set, later — optionally `new-window` in the hub session instead"*. Mark it done and point at §20, so a reader does not implement it twice.

- [ ] **Step 5: Verify no stale claim survives**

Run the grep from Step 1 again and read every hit.

- [ ] **Step 6: Commit**

```bash
git add docs/ README.md
git commit -m "docs: a is a jump now, and §20 names the case where it cannot be"
```

---

### Task 10: Close the branch

- [ ] **Step 1: Full gates**

```bash
gofmt -l .
rtk proxy go vet ./...
rtk proxy go vet -tags mockup ./internal/ui/
rtk proxy go vet -tags e2e ./internal/e2e/
rtk proxy go test -race ./... 2>&1 | grep -cE '^ok'
rtk proxy go test -tags e2e ./internal/e2e/ 2>&1 | tail -5
```
Expected: gofmt silent, vet clean on all three tag sets, the `ok` count equal to the package count (14 at the time of writing — a package that never ran prints nothing at all, so count rather than watch for FAIL).

- [ ] **Step 2: Verify the COMMIT, not the tree**

```bash
rm -rf /tmp/verify && mkdir -p /tmp/verify
git archive HEAD | tar -x -C /tmp/verify
cd /tmp/verify && gofmt -l . && rtk proxy go vet ./... && rtk proxy go test ./... 2>&1 | grep -cE '^ok'
```
Expected: identical results. `go test` compiles the working tree, so a green suite in a dirty tree says nothing about the commit — measured, a task once shipped a commit that only built because four test files were uncommitted.

- [ ] **Step 3: Wiring floor**

Run: `grep -rl 'possess\|SwitchClient\|AttachWindow\|ServerEpoch' --include='*.go' . | grep -v _test | wc -l`
Expected: at least 3 (`internal/tmux/possession.go`, `internal/ui/possession.go`, `internal/ui/model.go`). A verb that exists, is covered, and is called by nobody is the failure mode this repo has shipped: ten tasks once built a complete write path that `ui.Run` never constructed.

- [ ] **Step 4: Regenerate the review surface and compare against the approved target**

```bash
prototypes/possession-captures.sh /tmp/flows
HUB_FLOW_CAPTURES=/tmp/flows rtk proxy go test -tags mockup -run 'TestGenerateFlows|TestGenerateMockup' ./internal/ui/
```
Then re-read `docs/ui-flows-possession.html`: every frame that was `нарисовано` should now be regenerable as real, and the two hint frames should be replaced by `View()` output. Convert them and note in the commit which assertions survived unchanged.

- [ ] **Step 5: Commit**

```bash
git add -A
git commit -m "chore: close the possession branch — gates, wiring floor, real frames"
```

---

## Self-Review

**1. Spec coverage.** §20's sections map to tasks as follows. *The key is `a`, which becomes a dispatcher* → Task 6 (and the fifth path, which the spec lacks). *§16's key table changes* → Task 9. *The hub does not render the pane* → nothing to build; the absence is the design, and Task 3's verbs are the whole mechanism. *`link-window` is forbidden, and a test enforces it* → Task 2. *Same server: `switch-client`* → Tasks 3, 5, 6, 8. *Other server: the existing attach, in a new local window* → Tasks 3, 6. *The seam needs a third change* → Task 1. *What the operator sees* → Task 7 plus the captures already in `docs/ui-flows-possession.html`; "no marker column" and "nothing added to the dashboard" are satisfied by building nothing, and the flows document is the evidence. *The hub keeps no state, and keeps polling* → Task 6's `sentTo`, and the e2e case in Task 8.

**Gap found and filled:** §20's table has four rows and none covers `$TMUX` being empty, where both new paths are impossible. Task 5 makes it `pathFullScreen` with a test, and Task 9 adds the row to the spec. This is the plan's one departure from the spec, and it is additive.

**2. Placeholder scan.** No TBDs. Task 8 names helper functions it may not find under those names and says explicitly to reuse `internal/e2e`'s existing harness rather than inventing one — that is a real instruction, not a placeholder, but it is the one task whose code is not fully determined here, because it depends on a file the plan should not paste in full. Task 7 Step 6 references `/tmp/flows/real-hint-jump.txt` and `target-hint-jump.txt`, which Task 7 itself must produce by extracting the two frames; that extraction is one `python3` line and the plan says which frames.

**3. Type consistency.** `possessionPath` and its four constants are defined in Task 5 and used in Tasks 6 and 7. `decidePossession(p, h, selfSession, selfEpoch)` has the same four parameters everywhere. `possessedMsg{from, err}` is defined in Task 6 and consumed in Task 6's `Update` case and Task 6's test. `tmux.ServerEpoch` returns `(string, error)` in Task 3 and is used that way in Task 6 Step 6. `Render`'s new `hint` parameter is inserted after `note` in Task 7 and every call site is named there. `hub.SelfSessionID()` returns `$N` in Task 4 and is stored in `model.selfSession` in Task 6.

**One deliberate trap:** Task 5's code block contains a broken import path (`github.com th/…`) with a note underneath. A subagent that pastes without reading gets a compile error immediately rather than a plausible-looking file.

## Verified by compiling, not by reading

Tasks 1, 3, 4 and 5 were written into a clean `git archive HEAD` checkout and run before this
plan was committed, because a plan verified by reading is a plan whose defects arrive as
review rounds. `docs/plans/2026-08-12-lifecycle-and-hiding.md` found seven that way, one of
them Critical.

**What passed:** `internal/tmux` and `internal/ui` green with the new code and the new tests,
including every existing test. The four that a payload-keyed `opaqueArg` breaks —
`TestValidateStillRefusesAPercentInATemplate`, `TestValidateRejectsLiteralPercent`,
`TestTheWRITEVerbsStillDemandAPaneID`, `TestValidateAcceptsTheLIFECYCLETargets` — all stayed
green, which is the evidence that the outer-verb discriminator is the right one.

**One defect found, and it would have failed Task 2 on the very first run.** The `link-window`
ban as first written scanned for the bare word, and the tree **already contains it**:
`internal/ui/flows_test.go` names it twice in the prose explaining why it is forbidden, and
Task 3's own doc comment on `AttachWindow` names it again. So the guard's first act would have
been to fail on the documentation of the thing it guards — the shape that gets a guard deleted
rather than obeyed. Fixed to scan the quoted verb (`"link-window"`), and re-calibrated on both
poles: clean against the real tree, and red against a planted `run("link-window", …)` while
staying silent on a file whose comment says `link-window'ed`. That third case is the one that
makes the discriminator worth having, so it is now what the planted-violation test asserts.

**Not compiled:** Tasks 6, 7, 8. Task 6 rewrites a live `case "a":` and adds struct fields,
Task 7 changes `Render`'s signature at every call site, and Task 8 depends on
`internal/e2e`'s existing harness — all three are edits to files the plan cannot paste whole,
so their compile happens during execution. Their risk is concentrated in one place named
above: `Render` gains a parameter, and a missed call site is a build failure rather than a
silent wrong answer.
