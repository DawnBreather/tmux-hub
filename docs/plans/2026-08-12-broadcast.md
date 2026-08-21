# tmux-hub Broadcast Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Type once, land in the agents you selected — and know, per target, whether the text arrived.

**Architecture:** One write path. Text goes in through `load-buffer -` on stdin so no payload ever touches argv, and out through a single `if -F`-guarded `paste-buffer` that checks a per-pane token in the same invocation as the send. A separate `internal/proc` walk identifies which pane actually runs an agent, and that identification — not `pane_active`, not `pane_current_command` — is what a token is stamped from. Delivery is a three-valued fact witnessed by a second read, never by the confirmation.

**Tech Stack:** Go 1.26.1, stdlib only.

**Design source:** `docs/design.md` §7 (broadcast), §11 (killed classes), §3's write-path table. Read §3 first. **Nine of its write-path claims are corrections to earlier drafts of §7**, three of them added while this plan was being written, and every one of them is a case where a confirmation said success and nothing had arrived.

## Global Constraints

- Go **1.26.1**; module `github.com/DawnBreather/tmux-hub`; stdlib only.
- Every tmux invocation goes through `tmux.Runner` with an explicit socket.
- **No payload text in argv, ever.** `load-buffer -` from stdin is the only way text enters tmux. `send-keys -l` truncates a trailing `;` at rc=0, `send-keys -H` takes one byte per *argument* and delivers nothing at rc=0 for a single hex string, and `set-buffer` inherits argv quoting. All three are measured and all three are forbidden.
- **`%NN` is legal only as the value of a `-t` flag.** Enforced in the seam (Task 1). A bare `%2` anywhere else is either the strftime bug or an unguarded target.
- **Both the `if` and every sub-command inside it carry their own `-t`.** Measured separately: without the sub-command's, a crossed pair delivered to the unstamped pane and confirmed `OK` for it; without the `if`'s, the guard read the option from the server's *current* pane and pasted into an unstamped one, printing `OK %1`. Neither implies the other.
- **A confirmation is not a delivery.** `display -p` fires whenever the pane resolves and the guard passed. Every send resolves to `delivered`, `sent-unwitnessed` or `refused` — never a boolean.
- **The witness is a second read.** `#{window_activity}` cannot advance inside the invocation that writes: measured three times, identical before and after. A same-invocation witness reports every delivery as unwitnessed.
- **Enter is always a separate act.** Never in the payload, never in the same invocation.
- All remote state is namespaced per hub instance: option `@hub_<instance>`, buffers `tmux-hub-<instance>-<seq>`. A laptop and a desktop pointed at one host is normal.
- Tests never touch the developer's own server: every tmux test starts its own on a socket under `t.TempDir()`, and asserts by **capturing the target pane**, not by reading the confirmation.
- `gofmt -l .` empty, `go test -race ./...` green before every commit.

---

### Task 1: The seam learns stdin, and `-t` becomes checked

Everything else is unreachable until this lands: `load-buffer -` needs stdin, which `Runner` has no way to supply, and the guarded send is a single argument containing `-t %12`, which the current `Validate` **rejects** — it allows `%` only in an argument that is exactly a pane id.

**Files:**
- Modify: `internal/tmux/run.go`
- Modify: `internal/tmux/run_test.go`

**Interfaces:**
- Produces:
  - `type InputRunner interface { RunInput(ctx, t Target, stdin []byte, args ...string) (Result, error) }` — `*execRunner` satisfies it
  - `Validate` gains the positional `%NN` rule and the `-t` value rule
  - `var ErrBadTarget error`

- [ ] **Step 1: Write the failing tests**

Append to `internal/tmux/run_test.go`:

```go
// The guarded send is ONE argument that contains `-t %12`. The original rule —
// "% is legal only in an argument that is exactly a pane id" — rejects it, so the
// entire write path could not go through the seam.
func TestValidateAcceptsAPaneIDInsideASubCommand(t *testing.T) {
	sub := "paste-buffer -d -p -r -b tmux-hub-ab12-1 -t %12 ; " +
		"display -p -t %12 'OK #{pane_id} #{@hub_ab12}'"
	if err := Validate([]string{"if", "-F", "-t", "%12", "#{==:#{@hub_ab12},u}", sub}); err != nil {
		t.Fatalf("the guarded send must be expressible: %v", err)
	}
}

// ...and the strftime hole must stay closed. `display -p 'OK %2'` returns an empty
// string at rc=0, so a %NN that is NOT a -t value is exactly the measured bug.
func TestValidateStillRefusesAPercentInATemplate(t *testing.T) {
	for _, args := range [][]string{
		{"display", "-p", "-t", "%2", "OK %2"},
		{"display", "-p", "CONFIRM-%2"},
		{"display", "-p", "-t", "%2", "activity %Y"},
		{"if", "-F", "-t", "%1", "cond", "display -p -t %1 'OK %1'"},
	} {
		if err := Validate(args); !errors.Is(err, ErrPercentInArg) {
			t.Errorf("Validate(%q) = %v, want ErrPercentInArg", args, err)
		}
	}
}

// An empty -t fails OPEN: measured, a send-keys whose -t value is the empty
// string returns rc=0 and
// delivers to the server's current pane — the pane the user last touched. A stale
// %999 fails closed, which is safe. So only the open direction needs refusing, and
// it can only ever be a hub defect.
func TestValidateRefusesATargetThatIsNotAPaneID(t *testing.T) {
	for _, args := range [][]string{
		{"send-keys", "-t", "", "-l", "X"},
		{"paste-buffer", "-t", "mysession", "-b", "b"},
		{"display", "-p", "-t", "%", "x"},
		{"if", "-F", "-t", "%1", "cond", "paste-buffer -t  -b b"},
	} {
		if err := Validate(args); !errors.Is(err, ErrBadTarget) {
			t.Errorf("Validate(%q) = %v, want ErrBadTarget", args, err)
		}
	}
}

// A -t that names a real pane is fine at both levels.
func TestValidateAcceptsWellFormedTargets(t *testing.T) {
	for _, args := range [][]string{
		{"capture-pane", "-p", "-t", "%0"},
		{"display", "-p", "-t", "%137", "#{pane_id} #{window_activity}"},
		{"list-panes", "-a", "-F", "#{pane_id}|#{session_id}"},
	} {
		if err := Validate(args); err != nil {
			t.Errorf("Validate(%q) = %v, want nil", args, err)
		}
	}
}

// The payload never touches argv, so the seam must carry it on stdin.
func TestRunInputFeedsStdin(t *testing.T) {
	tgt := testServer(t) // the helper this package already has
	r := NewExec(10 * time.Second).(*execRunner)

	payload := []byte("first line;\nsecond line with % and ; inside\n")
	if _, err := r.RunInput(context.Background(), tgt, payload,
		"load-buffer", "-b", "probe", "-"); err != nil {
		t.Fatalf("RunInput: %v", err)
	}
	res, err := r.Run(context.Background(), tgt, "show-buffer", "-b", "probe")
	if err != nil {
		t.Fatalf("show-buffer: %v", err)
	}
	if res.Stdout != string(payload) {
		t.Errorf("buffer round-trip changed the text:\n got %q\nwant %q", res.Stdout, payload)
	}
}

// The payload is not argv, so it is not validated — a prompt containing a literal
// % or a trailing ; is ordinary text and must survive untouched. Only the ARGS are
// checked.
func TestRunInputValidatesArgsButNotThePayload(t *testing.T) {
	tgt := testServer(t) // the helper this package already has
	r := NewExec(10 * time.Second).(*execRunner)

	if _, err := r.RunInput(context.Background(), tgt, []byte("100% done; really"),
		"load-buffer", "-b", "pct", "-"); err != nil {
		t.Fatalf("a payload with %% and ; must be accepted: %v", err)
	}
	if _, err := r.RunInput(context.Background(), tgt, []byte("x"),
		"load-buffer", "-b", "OK %2", "-"); !errors.Is(err, ErrPercentInArg) {
		t.Errorf("args must still be validated, got %v", err)
	}
}

func TestRunInputRefusesAnEmptySocket(t *testing.T) {
	r := NewExec(time.Second).(*execRunner)
	if _, err := r.RunInput(context.Background(), Target{Label: "x"}, []byte("y"),
		"load-buffer", "-"); !errors.Is(err, ErrNoSocket) {
		t.Fatalf("got %v, want ErrNoSocket", err)
	}
}
```

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./internal/tmux/ -run 'Validate|RunInput' -v`
Expected: FAIL — `undefined: ErrBadTarget`, `undefined: RunInput`, and
`TestValidateAcceptsAPaneIDInsideASubCommand` failing with `ErrPercentInArg`.

That last failure is the point of the task: it is the current rule refusing the
only shape the write path can take.

- [ ] **Step 3: Rewrite Validate around token position**

In `internal/tmux/run.go`, replace `Validate` and add the error:

```go
// ErrBadTarget is a -t whose value is not a pane id. It is always a hub defect,
// which is why it is refused rather than handled.
var ErrBadTarget = errors.New("tmux: -t value is not a pane id")

// Validate applies the invariants to a full argument list, and to the inside of
// any sub-command string an argument carries.
//
// The rule for % is POSITIONAL, and the position has to be read at TWO levels
// because that is how tmux commands are shaped:
//
//   - `%NN` is legal only as the value of a -t flag. That is the one place tmux
//     means a pane id.
//   - A % anywhere else is refused, because display -p runs its argument through
//     strftime: `display -p 'OK %2'` returns an EMPTY string at rc=0, so identity
//     must be emitted as #{pane_id} instead.
//
// For an ordinary argument the -t is the PREVIOUS argv element, so the check needs
// argv context; inside a sub-command string (`paste-buffer … -t %12 ; display …`)
// the -t and its value are adjacent tokens of one argument. A scan that sees only
// one argument at a time cannot express the first case and rejects every command
// that targets a pane, and a scan over the joined string cannot tell a missing
// value from a collapsed one.
func Validate(args []string) error {
	for i, a := range args {
		for _, f := range forbiddenVars {
			if strings.Contains(a, f) {
				return fmt.Errorf("%w: %s", ErrForbiddenFormat, f)
			}
		}
		prev := ""
		if i > 0 {
			prev = args[i-1]
		}
		if err := validateArg(a, prev); err != nil {
			return err
		}
	}
	return nil
}

func unquote(s string) string { return strings.Trim(s, `'"`) }

// validateArg checks one argv element. prev is the element before it.
func validateArg(a, prev string) error {
	toks := strings.Fields(a)

	if len(toks) <= 1 {
		bare := unquote(a)
		if unquote(prev) == "-t" {
			// An empty or non-pane -t fails OPEN: measured, a send-keys whose -t
			// value is the empty string returns rc=0 and delivers to the server's
			// current pane — the one the user last touched. A stale %999 fails
			// closed and is safe to pass on.
			if !paneID.MatchString(bare) {
				return fmt.Errorf("%w: %q", ErrBadTarget, bare)
			}
			return nil
		}
		if strings.Contains(bare, "%") {
			return fmt.Errorf("%w: %q", ErrPercentInArg, a)
		}
		return nil
	}

	// A multi-token argument is a sub-command chain, where -t and its value are
	// adjacent tokens.
	for i, tok := range toks {
		bare := unquote(tok)
		afterT := i > 0 && unquote(toks[i-1]) == "-t"
		if afterT && !paneID.MatchString(bare) {
			return fmt.Errorf("%w: %q", ErrBadTarget, bare)
		}
		if strings.Contains(bare, "%") && !afterT {
			return fmt.Errorf("%w: %q", ErrPercentInArg, tok)
		}
	}
	if unquote(toks[len(toks)-1]) == "-t" {
		return fmt.Errorf("%w: -t with no value", ErrBadTarget)
	}
	return nil
}
```

Note that the rule is read at **two levels**, and both are needed. For an ordinary
argument the `-t` is the *previous argv element*; inside a sub-command string the
`-t` and its value are adjacent tokens of one argument. A scan that sees one
argument at a time cannot express the first case — measured, it rejected every
command that targets a pane, `capture-pane -p -t %0` included — and a scan over the
arguments joined into one string cannot tell a missing `-t` value from a collapsed
empty one.

- [ ] **Step 4: Add the stdin-capable runner**

Append to `internal/tmux/run.go`:

```go
// InputRunner is the seam for commands whose payload is stdin. It exists because
// the payload must NEVER be an argument: send-keys -l truncates text ending in
// `;` at rc=0, send-keys -H takes one byte per argument and delivers nothing for a
// single hex string at rc=0, and set-buffer inherits the same argv quoting. Text
// on stdin has no quoting layer at all, so the whole class is gone.
type InputRunner interface {
	RunInput(ctx context.Context, t Target, stdin []byte, args ...string) (Result, error)
}

func (r *execRunner) RunInput(ctx context.Context, t Target, stdin []byte, args ...string) (Result, error) {
	if t.Socket == "" {
		return Result{}, ErrNoSocket
	}
	// The ARGS are validated; the payload deliberately is not. A prompt containing
	// a literal % or a trailing ; is ordinary text, and it is only dangerous when
	// it travels as argv — which is exactly what this method exists to avoid.
	if err := Validate(args); err != nil {
		return Result{}, err
	}
	full := append([]string{"-S", t.Socket}, args...)

	ctx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "tmux", full...)
	cmd.Stdin = bytes.NewReader(stdin)
	var out, errb strings.Builder
	cmd.Stdout = &out
	cmd.Stderr = &errb

	err := cmd.Run()
	res := Result{Stdout: out.String(), Stderr: strings.TrimSpace(errb.String())}
	if ctx.Err() != nil {
		return res, fmt.Errorf("tmux: deadline exceeded after %s", r.timeout)
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		res.RC = ee.ExitCode()
		return res, nil
	}
	return res, err
}
```

Add `"bytes"` to the imports.

- [ ] **Step 5: Run the whole tmux suite**

Run: `go test ./internal/tmux/ -v`
Expected: PASS, including every pre-existing test. If an existing test now fails
with `ErrBadTarget`, it was passing a target the seam should always have refused —
fix the test's target, not the rule.

- [ ] **Step 6: Commit**

```bash
gofmt -l . && go test -race ./... && git add internal/tmux/ && git commit -m "feat(tmux): the seam learns stdin, and -t becomes checked

Two changes the write path cannot exist without.

The % rule becomes POSITIONAL: %NN is legal only as the value of a -t flag. The
old per-argument rule refused the guarded send — one argument containing
'-t %12' — which is the only shape the write path can take, while still
admitting a bare %2 used as a display template, which is the strftime bug it was
written to prevent. Position expresses both correctly.

A -t whose value is not a pane id is now refused, because it fails OPEN:
send-keys -t '' -l X returns rc=0 and delivers to the server's current pane, the
one the user last touched. A stale %999 fails closed and needs no rule.

RunInput carries the payload on stdin. Text as argv is three measured silent
failures — send-keys -l truncating a trailing semicolon, send-keys -H taking one
byte per argument and delivering nothing for a single hex string, set-buffer
inheriting both — and stdin has no quoting layer at all. The args are validated;
the payload is not, because a prompt containing % or ; is ordinary text and is
only dangerous when it travels as argv."
```

---

### Task 2: Instance identity, and names that cannot collide

**Files:**
- Create: `internal/broadcast/names.go`
- Test: `internal/broadcast/names_test.go`

**Interfaces:**
- Produces:
  - `type Instance string`
  - `func NewInstance() Instance`
  - `func (i Instance) Option() string` → `@hub_<instance>`
  - `func (i Instance) BufferPrefix() string` → `tmux-hub-<instance>-`
  - `func (i Instance) Buffer(seq uint64) string`
  - `const BufferGlob = "tmux-hub-*"`
  - `func NewToken() string`

- [ ] **Step 1: Write the failing test**

Create `internal/broadcast/names_test.go`:

```go
package broadcast

import (
	"regexp"
	"strings"
	"testing"
)

// A laptop and a desktop pointed at one host is a normal setup, and flock is
// per-machine, so nothing but the name keeps two hubs' remote state apart.
func TestInstancesDiffer(t *testing.T) {
	a, b := NewInstance(), NewInstance()
	if a == b {
		t.Fatal("two instances got the same id")
	}
	if a == "" {
		t.Fatal("empty instance id")
	}
}

// The instance id ends up inside a tmux option NAME and a buffer NAME, so it must
// contain nothing that either syntax treats specially.
func TestInstanceIDIsNameSafe(t *testing.T) {
	safe := regexp.MustCompile(`^[a-z0-9]+$`)
	for i := 0; i < 32; i++ {
		id := string(NewInstance())
		if !safe.MatchString(id) {
			t.Fatalf("instance id %q is not name-safe", id)
		}
		if len(id) < 6 || len(id) > 16 {
			t.Errorf("instance id %q has an awkward length %d", id, len(id))
		}
	}
}

func TestOptionAndBufferNames(t *testing.T) {
	i := Instance("ab12cd")
	if got := i.Option(); got != "@hub_ab12cd" {
		t.Errorf("Option() = %q", got)
	}
	if got := i.Buffer(7); got != "tmux-hub-ab12cd-7" {
		t.Errorf("Buffer(7) = %q", got)
	}
	if !strings.HasPrefix(i.Buffer(7), i.BufferPrefix()) {
		t.Error("Buffer must sit under BufferPrefix, or the sweep misses it")
	}
	// The sweep at connect and shutdown must match another instance's leftovers
	// too — a hub that crashed is exactly the case worth cleaning.
	if !strings.HasPrefix(i.BufferPrefix(), strings.TrimSuffix(BufferGlob, "*")) {
		t.Errorf("BufferGlob %q does not cover %q", BufferGlob, i.BufferPrefix())
	}
}

// A token proves identity only if a wrong pane cannot produce one, so it must be
// unguessable and never repeat.
func TestTokensAreUniqueAndOpaque(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 256; i++ {
		tok := NewToken()
		if seen[tok] {
			t.Fatalf("token repeated: %q", tok)
		}
		if len(tok) < 24 {
			t.Fatalf("token %q is too short to be unguessable", tok)
		}
		if strings.ContainsAny(tok, " ;'\"%$") {
			t.Fatalf("token %q contains something a tmux format would treat specially", tok)
		}
		seen[tok] = true
	}
}
```

- [ ] **Step 2: Run it**

Run: `go test ./internal/broadcast/ -v`
Expected: FAIL — the package does not exist.

- [ ] **Step 3: Write the implementation**

Create `internal/broadcast/names.go`:

```go
// Package broadcast is tmux-hub's write path: it puts text into panes the user
// selected, and reports per target whether it arrived.
//
// Everything here is built around one measured asymmetry: a tmux command that
// fails to deliver still succeeds. `send-keys -H <hex>` delivered nothing at rc=0
// with an empty stderr; a batch that aborts leaves the payload as the user's most
// recent paste buffer; an empty `-t` delivers to whichever pane they last touched;
// and a `display -p` confirmation fires whether or not any bytes arrived. So no
// step here treats a zero exit code as evidence.
package broadcast

import (
	"crypto/rand"
	"encoding/hex"
	"strconv"
)

// Instance names one running hub. It is in every name the hub writes on a remote
// server, because two hubs pointed at one host is a normal setup — a laptop and a
// desktop — and the flock that makes one hub authoritative is per-machine.
type Instance string

// NewInstance returns a fresh id. Lowercase hex only: it becomes part of a tmux
// option name and a buffer name, and neither syntax should ever have to quote it.
func NewInstance() Instance {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		// A hub without randomness cannot namespace its own state, and sharing a
		// name with another instance is the failure this type exists to prevent.
		panic("broadcast: no randomness for an instance id: " + err.Error())
	}
	return Instance(hex.EncodeToString(b[:]))
}

// Option is the per-pane user option holding the identity token.
func (i Instance) Option() string { return "@hub_" + string(i) }

// BufferPrefix is what this instance's buffers start with.
func (i Instance) BufferPrefix() string { return "tmux-hub-" + string(i) + "-" }

// Buffer names the buffer for one send. The sequence makes a concurrent second
// send impossible to confuse with the first.
func (i Instance) Buffer(seq uint64) string {
	return i.BufferPrefix() + strconv.FormatUint(seq, 10)
}

// BufferGlob matches EVERY instance's buffers, not just ours. The connect and
// shutdown sweeps use it deliberately: a hub that crashed mid-send left its
// payload as the most recent buffer on that server, and refusing to clean up
// after a previous run leaves the user's `prefix ]` pasting someone's prompt.
const BufferGlob = "tmux-hub-*"

// NewToken returns a per-pane identity token. It has to be unguessable rather
// than merely unique: the confirmation echoes the token back, and that is what
// makes a reply from the wrong pane impossible to mistake for the right one.
func NewToken() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("broadcast: no randomness for a token: " + err.Error())
	}
	return hex.EncodeToString(b[:])
}
```

- [ ] **Step 4: Run the tests**

Run: `go test ./internal/broadcast/ -v`
Expected: PASS all four.

- [ ] **Step 5: Commit**

```bash
gofmt -l . && go test -race ./... && git add internal/broadcast/ && git commit -m "feat(broadcast): instance identity, and names that cannot collide

Two hubs pointed at one host is a normal setup — a laptop and a desktop — and
the flock that makes one hub authoritative is per-machine, so the name is the
only thing keeping their remote state apart.

The sweep glob deliberately matches every instance's buffers rather than only
ours: a hub that crashed mid-send left its payload as the most recent buffer on
that server, so refusing to clean up after a previous run leaves the user's
prefix-] pasting someone else's prompt."
```

---

### Task 3: Which pane actually runs an agent

`pane_active` is measured to be actively dangerous: in a session where the user had
split off a shell, the active pane *was* the shell, and a broadcast produced
`bash: please: command not found` from a prompt beginning "please refactor…". A
prompt beginning `rm …` would not have been funny.

`pane_current_command` is not the answer either, though **not** for the reason §7
originally gave. That reason — both panes report `bash` — is measured false: an agent
pane reports `claude` in both launch shapes. It is still not the key because it names
the **foreground** process, so it becomes `bash`/`git`/`npm` the moment the agent
shells out for a tool, and because it cannot separate an interactive agent from
`claude bg-pty-host`.

So the pane is identified **positively**, by looking for the claude process **at or
under** `#{pane_pid}`. "At" is not a detail: when the pane's own command is claude
(`tmux new-window claude`), `pane_pid` IS the agent, and a walk over descendants
alone reported *not identified* for a live agent pane.

**Files:**
- Create: `internal/proc/proc.go`
- Create: `internal/proc/walk_linux.go`
- Test: `internal/proc/proc_test.go`

**Interfaces:**
- Produces:
  - `type Proc struct { PID, PPID int; Argv []string; Comm string }`
  - `func Snapshot() ([]Proc, error)`
  - `func Descendants(all []Proc, root int) []Proc`
  - `func IdentifyAgent(all []Proc, panePID int) (agentPID int, ok bool)`
  - `func RemoteWalkScript(panePIDs []int) string`
  - `func ParseRemoteWalk(stdout string) map[int]int`

- [ ] **Step 1: Write the failing test**

Create `internal/proc/proc_test.go`:

```go
package proc

import (
	"os"
	"testing"
)

// The fixture is the real shape of this machine's process table, including the
// three traps that make the obvious implementation wrong.
func fixture() []Proc {
	return []Proc{
		{PID: 100, PPID: 1, Comm: "zsh", Argv: []string{"-zsh"}},
		// A real interactive agent, as measured: comm is "claude", argv[0] is the
		// bare word, and it sits under the pane's shell.
		{PID: 263249, PPID: 100, Comm: "claude",
			Argv: []string{"claude", "--dangerously-skip-permissions"}},
		// Trap 1: Node overwrites comm with a THREAD name. Measured values on this
		// machine include "MainThread", "node-MainThread" and "2.1.226" — so comm
		// cannot be the key, and "2.1.226" would not match any name you would guess.
		{PID: 263613, PPID: 263249, Comm: "MainThread",
			Argv: []string{"node", "/home/dev/.claude/plugins/x/server.js"}},
		{PID: 263615, PPID: 263249, Comm: "2.1.226",
			Argv: []string{"node", "/home/dev/.claude/plugins/y/server.js"}},

		// Trap 2: these ARE claude processes and are NOT an interactive agent.
		// Measured: `claude bg-pty-host --bg-pty-host /tmp/cc-daemon-1000` and
		// `claude bg-spare`. A pane holding one of these must not be stamped.
		{PID: 200, PPID: 1, Comm: "zsh", Argv: []string{"-zsh"}},
		{PID: 467408, PPID: 200, Comm: "2.1.226",
			Argv: []string{"claude", "bg-pty-host", "--bg-pty-host", "/tmp/cc-daemon-1000"}},

		// A plain shell pane: the case that must never be identified as an agent,
		// because it is the one the measured accident happened in.
		{PID: 300, PPID: 1, Comm: "zsh", Argv: []string{"-zsh"}},
		{PID: 301, PPID: 300, Comm: "vim", Argv: []string{"vim", "notes.md"}},
	}
}

func TestIdentifiesARealAgent(t *testing.T) {
	got, ok := IdentifyAgent(fixture(), 100)
	if !ok {
		t.Fatal("a pane whose shell has a claude child was not identified")
	}
	if got != 263249 {
		t.Errorf("agent pid = %d, want the claude process 263249", got)
	}
}

// Trap 1 as an assertion: keying on comm would match "MainThread" and "2.1.226"
// and miss nothing useful, so argv[0]'s basename is the key.
func TestDoesNotKeyOnComm(t *testing.T) {
	all := fixture()
	for i := range all {
		if all[i].PID == 263249 {
			all[i].Comm = "node-MainThread" // what Node would leave there
		}
	}
	if _, ok := IdentifyAgent(all, 100); !ok {
		t.Error("identification broke when comm was rewritten — it must use argv[0]")
	}
}

// Trap 2: a background daemon is a claude process and is not an agent to send to.
func TestRefusesTheBackgroundDaemonRoles(t *testing.T) {
	if pid, ok := IdentifyAgent(fixture(), 200); ok {
		t.Errorf("a bg-pty-host pane was identified as an agent (pid %d)", pid)
	}
}

func TestRefusesAPlainShellPane(t *testing.T) {
	if pid, ok := IdentifyAgent(fixture(), 300); ok {
		t.Errorf("a shell running vim was identified as an agent (pid %d)", pid)
	}
	if pid, ok := IdentifyAgent(fixture(), 999999); ok {
		t.Errorf("an unknown pane pid was identified (pid %d)", pid)
	}
}

// The walk must reach a grandchild, because a pane's shell may run claude through
// a wrapper.
func TestDescendantsReachesGrandchildren(t *testing.T) {
	all := append(fixture(), Proc{PID: 400, PPID: 1, Comm: "zsh", Argv: []string{"-zsh"}},
		Proc{PID: 401, PPID: 400, Comm: "sh", Argv: []string{"sh", "-c", "exec claude"}},
		Proc{PID: 402, PPID: 401, Comm: "claude", Argv: []string{"claude"}})
	if pid, ok := IdentifyAgent(all, 400); !ok || pid != 402 {
		t.Errorf("IdentifyAgent through a wrapper = (%d, %v), want (402, true)", pid, ok)
	}
	d := Descendants(all, 400)
	if len(d) != 2 {
		t.Errorf("Descendants(400) returned %d, want 2", len(d))
	}
}

// A cycle in the parent links must not hang the walk. It should not happen, and a
// hub that freezes because it did would be indistinguishable from a hung tunnel.
func TestDescendantsTerminatesOnACycle(t *testing.T) {
	all := []Proc{{PID: 1, PPID: 2}, {PID: 2, PPID: 1}}
	done := make(chan int, 1)
	go func() { done <- len(Descendants(all, 1)) }()
	select {
	case <-done:
	case <-timeAfter():
		t.Fatal("Descendants did not terminate on a cyclic parent chain")
	}
}

// Against the real /proc. It cannot assert a specific agent, so it asserts the
// invariants any snapshot must satisfy — and that our own pid is found, which is
// the one thing guaranteed true.
func TestSnapshotFindsThisProcess(t *testing.T) {
	all, err := Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if len(all) < 5 {
		t.Fatalf("Snapshot returned %d processes", len(all))
	}
	me := os.Getpid()
	for _, p := range all {
		if p.PID == me {
			if len(p.Argv) == 0 {
				t.Error("our own process has no argv")
			}
			return
		}
	}
	t.Errorf("Snapshot did not include our own pid %d", me)
}

// The pane's command may BE claude — `tmux new-window claude` — in which case
// pane_pid is the claude process itself and there is nothing beneath it to find.
// Measured against a live session: a descendants-only walk reported "not
// identified" for a pane plainly running an agent, which is the whole write path
// silently refusing to work for a completely normal way of starting one.
func TestIdentifiesAgentWhenThePaneCommandIsClaudeItself(t *testing.T) {
	all := []Proc{
		{PID: 715435, PPID: 1, Comm: "claude", Argv: []string{"claude"}},
		{PID: 716117, PPID: 715435, Comm: "MainThread", Argv: []string{"node", "mcp.js"}},
	}
	got, ok := IdentifyAgent(all, 715435)
	if !ok {
		t.Fatal("a pane whose own command is claude was not identified")
	}
	if got != 715435 {
		t.Errorf("agent pid = %d, want the pane pid itself", got)
	}
}

// Widening the walk to include the root must not widen what is ACCEPTED: a pane
// whose own command is a daemon role, or something else entirely, still answers no.
func TestTheRootIsHeldToTheSameRules(t *testing.T) {
	daemon := []Proc{{PID: 500, PPID: 1, Comm: "2.1.226",
		Argv: []string{"claude", "bg-pty-host", "--bg-pty-host", "/tmp/cc-daemon-1000"}}}
	if pid, ok := IdentifyAgent(daemon, 500); ok {
		t.Errorf("a pane running bg-pty-host was identified (pid %d)", pid)
	}
	shell := []Proc{{PID: 600, PPID: 1, Comm: "zsh", Argv: []string{"-zsh"}}}
	if pid, ok := IdentifyAgent(shell, 600); ok {
		t.Errorf("a plain shell pane was identified (pid %d)", pid)
	}
}

// The remote form must be one command for ALL selected panes, not one per pane:
// a per-pane ssh would put a round trip per target into every tick.
func TestRemoteWalkIsOneCommandAndRoundTrips(t *testing.T) {
	script := RemoteWalkScript([]int{100, 200, 300})
	for _, want := range []string{"100", "200", "300"} {
		if !contains(script, want) {
			t.Errorf("the script does not mention pane pid %s", want)
		}
	}
	got := ParseRemoteWalk("100 263249\n200 0\n300 0\n")
	if got[100] != 263249 || got[200] != 0 || got[300] != 0 {
		t.Errorf("ParseRemoteWalk = %v", got)
	}
	if len(ParseRemoteWalk("bash: python3: command not found\n")) != 0 {
		t.Error("garbage output must parse to nothing, not to a false identification")
	}
}
```

Add these two helpers at the bottom of `internal/proc/proc_test.go`, and import
`"strings"` and `"time"`:

```go
func timeAfter() <-chan struct{} {
	ch := make(chan struct{})
	go func() { time.Sleep(2 * time.Second); close(ch) }()
	return ch
}

func contains(hay, needle string) bool { return strings.Contains(hay, needle) }
```

- [ ] **Step 2: Run it**

Run: `go test ./internal/proc/ -v`
Expected: FAIL — the package does not exist.

- [ ] **Step 3: Write the identification**

Create `internal/proc/proc.go`:

```go
// Package proc answers one question: does this pane run an agent?
//
// `pane_active` is measured actively wrong — in a session where the user had split
// off a shell, the active pane WAS the shell, and a broadcast produced
// `bash: please: command not found` from a prompt beginning "please refactor…".
//
// `pane_current_command` is not the key either. An earlier draft said it reports
// `bash` for both the agent pane and the user's shell; measured, it reports `claude`
// in both launch shapes. It is still unusable because it names the FOREGROUND
// process — becoming `bash`/`git`/`npm` whenever the agent shells out for a tool —
// and because an interactive agent and `claude bg-pty-host` are both `claude`.
//
// So identification is positive and structural: look for the agent AT or under
// #{pane_pid}. "At" matters — when the pane's command is claude, pane_pid is the
// agent itself.
package proc

import (
	"path/filepath"
	"strconv"
	"strings"
)

// Proc is one process, reduced to what identification needs.
type Proc struct {
	PID  int
	PPID int
	Argv []string
	Comm string
}

// agentName is the program that makes a pane an agent pane.
const agentName = "claude"

// daemonRoles are argv[1] values that mark a claude process which is NOT an
// interactive agent. Measured on this machine: `claude bg-pty-host --bg-pty-host
// /tmp/cc-daemon-1000` and `claude bg-spare`. Stamping a pane that holds one of
// these would let a prompt be sent to a process that cannot read it.
var daemonRoles = map[string]bool{
	"bg-pty-host":   true,
	"bg-spare":      true,
	"--bg-pty-host": true,
	"--bg-spare":    true,
	"mcp":           true,
}

// isAgent reports whether one process is an interactive agent.
//
// It keys on argv[0]'s BASENAME and never on Comm. Comm is measured unreliable
// here: Node overwrites it with a thread name, so the same population contains
// `MainThread`, `node-MainThread` and — least guessable of all — `2.1.226`, the
// version string. Keying on comm would both miss real agents and match arbitrary
// helpers.
func isAgent(p Proc) bool {
	if len(p.Argv) == 0 {
		return false
	}
	if filepath.Base(p.Argv[0]) != agentName {
		return false
	}
	if len(p.Argv) > 1 && daemonRoles[p.Argv[1]] {
		return false
	}
	return true
}

// Descendants returns every process under root, root itself excluded.
//
// The visited set is not defensive clutter: a cyclic parent chain would spin
// forever, and a hub frozen in a process walk looks exactly like a hung tunnel,
// which is the most expensive symptom to diagnose.
func Descendants(all []Proc, root int) []Proc {
	children := make(map[int][]Proc, len(all))
	for _, p := range all {
		children[p.PPID] = append(children[p.PPID], p)
	}
	var out []Proc
	visited := map[int]bool{root: true}
	queue := []int{root}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for _, c := range children[cur] {
			if visited[c.PID] {
				continue
			}
			visited[c.PID] = true
			out = append(out, c)
			queue = append(queue, c.PID)
		}
	}
	return out
}

// IdentifyAgent returns the agent process at or under a pane, if any.
//
// "At" is load-bearing and was measured the hard way: there are TWO launch shapes,
// and a walk over descendants alone is blind to one of them.
//
//   - the pane's command IS claude (`tmux new-window claude`), so `pane_pid` is the
//     claude process itself. Against a live session, a descendants-only walk
//     reported "not identified" for a pane plainly running an agent.
//   - the pane runs a shell and claude is a child of it, possibly through a
//     wrapper.
//
// The root is therefore tested first, and a plain `sleep` is still refused by both
// forms — the fix widens what is found without widening what is accepted.
//
// The answer must be recomputed rather than cached: in the shell shape `pane_pid`
// is the shell and does not change as the agent comes and goes, so an unchanged
// `pane_pid` is not evidence the agent is still there.
func IdentifyAgent(all []Proc, panePID int) (int, bool) {
	for _, p := range all {
		if p.PID == panePID && isAgent(p) {
			return p.PID, true
		}
	}
	for _, p := range Descendants(all, panePID) {
		if isAgent(p) {
			return p.PID, true
		}
	}
	return 0, false
}

// RemoteWalkScript builds ONE command that answers for every selected pane at
// once. One ssh per pane would add a round trip per target to every tick; this
// adds one, and only while something is selected.
//
// It prints `<panePID> <agentPID|0>` per line, and it is deliberately written to
// print nothing rather than something wrong when the far side lacks python3 —
// ParseRemoteWalk then identifies no agent, and an unidentified target is one the
// confirmation step will ask about.
func RemoteWalkScript(panePIDs []int) string {
	ids := make([]string, 0, len(panePIDs))
	for _, p := range panePIDs {
		ids = append(ids, strconv.Itoa(p))
	}
	return `python3 -c '
import os,glob,sys
ps={}
for d in glob.glob("/proc/[0-9]*"):
    try:
        pid=int(os.path.basename(d))
        st=open(d+"/stat").read()
        ppid=int(st.rsplit(")",1)[1].split()[1])
        argv=[c for c in open(d+"/cmdline","rb").read().split(b"\x00") if c]
    except Exception: continue
    ps[pid]=(ppid,[a.decode("utf-8","replace") for a in argv])
kids={}
for pid,(ppid,_) in ps.items(): kids.setdefault(ppid,[]).append(pid)
ROLES={"bg-pty-host","bg-spare","--bg-pty-host","--bg-spare","mcp"}
def agent(root):
    # the root FIRST: when the pane command is claude, pane_pid is claude itself,
    # and a descendants-only walk reports nothing for a live agent pane
    ra=ps.get(root,(0,[]))[1]
    if ra and os.path.basename(ra[0])=="claude" and not (len(ra)>1 and ra[1] in ROLES):
        return root
    seen={root}; q=[root]
    while q:
        cur=q.pop(0)
        for k in kids.get(cur,[]):
            if k in seen: continue
            seen.add(k); q.append(k)
            argv=ps[k][1]
            if argv and os.path.basename(argv[0])=="claude" and not (len(argv)>1 and argv[1] in ROLES):
                return k
    return 0
for a in sys.argv[1:]:
    print(a, agent(int(a)))
' ` + strings.Join(ids, " ")
}

// ParseRemoteWalk reads the script's output. Anything unparseable yields no
// entry, which reads downstream as "not identified" — the safe direction, since an
// unidentified target triggers confirmation rather than a silent send.
func ParseRemoteWalk(stdout string) map[int]int {
	out := map[int]int{}
	for _, line := range strings.Split(stdout, "\n") {
		f := strings.Fields(line)
		if len(f) != 2 {
			continue
		}
		pane, err1 := strconv.Atoi(f[0])
		agent, err2 := strconv.Atoi(f[1])
		if err1 != nil || err2 != nil {
			continue
		}
		out[pane] = agent
	}
	return out
}
```

- [ ] **Step 4: Write the local snapshot**

Create `internal/proc/walk_linux.go`:

```go
package proc

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Snapshot reads /proc once. One pass for the whole table beats per-pane lookups:
// the tree has to be built anyway, and a partial read would make identification
// depend on scheduling.
//
// Unreadable entries are skipped rather than reported. A process that exits during
// the walk is the normal case, and a foreign process whose environ we cannot read
// is irrelevant here — identification uses argv, which /proc exposes for every
// process. (CLAUDECODE=1 is NOT usable as the key: measured, it is absent from
// claude's own environ and present only on the children it spawns, and environ was
// readable for just 71 of 145 candidates.)
func Snapshot() ([]Proc, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, err
	}
	out := make([]Proc, 0, len(entries))
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue // not a process directory
		}
		p, ok := readProc(pid)
		if !ok {
			continue
		}
		out = append(out, p)
	}
	return out, nil
}

func readProc(pid int) (Proc, bool) {
	dir := filepath.Join("/proc", strconv.Itoa(pid))

	raw, err := os.ReadFile(filepath.Join(dir, "stat"))
	if err != nil {
		return Proc{}, false
	}
	// The comm field is parenthesised and may itself contain spaces and
	// parentheses, so the fields after it are found from the LAST ')'.
	i := strings.LastIndexByte(string(raw), ')')
	if i < 0 || i+2 >= len(raw) {
		return Proc{}, false
	}
	rest := strings.Fields(string(raw[i+2:]))
	if len(rest) < 2 {
		return Proc{}, false
	}
	ppid, err := strconv.Atoi(rest[1])
	if err != nil {
		return Proc{}, false
	}
	comm := ""
	if j := strings.IndexByte(string(raw), '('); j >= 0 && j < i {
		comm = string(raw[j+1 : i])
	}

	cmdline, err := os.ReadFile(filepath.Join(dir, "cmdline"))
	if err != nil {
		return Proc{}, false
	}
	var argv []string
	for _, part := range strings.Split(string(cmdline), "\x00") {
		if part != "" {
			argv = append(argv, part)
		}
	}
	return Proc{PID: pid, PPID: ppid, Argv: argv, Comm: comm}, true
}
```

- [ ] **Step 5: Run the tests**

Run: `go test ./internal/proc/ -v`
Expected: PASS all eight.

- [ ] **Step 6: Calibrate against a real agent**

Run:

```bash
go test ./internal/proc/ -run Snapshot -v
go run ./internal/proc/testdata/identify 2>/dev/null || \
  python3 -c "print('skip: write a two-line main if you want a live check')"
```

Then confirm by hand that `IdentifyAgent` answers `true` for a pane you know runs
Claude Code and `false` for a plain shell pane. This is the one part of the write
path whose input is another program's process tree, so a live check is worth the
minute.

- [ ] **Step 7: Commit**

```bash
gofmt -l . && go test -race ./... && git add internal/proc/ && git commit -m "feat(proc): identify an agent pane positively, by its process tree

Nothing tmux reports can answer this. pane_current_command is 'bash' for BOTH
the agent pane and the user's shell, because Claude Code runs under a shell; and
pane_active is measured actively wrong — in a session where the user had split
off a shell, the active pane WAS the shell, and a broadcast produced
'bash: please: command not found' from a prompt beginning 'please refactor'.

Three traps, all measured on this machine's real process table:

- comm cannot be the key. Node overwrites it with a thread name, so the same
  population holds MainThread, node-MainThread and — least guessable — 2.1.226,
  the version string. argv[0]'s basename is the key instead.
- 'claude bg-pty-host' and 'claude bg-spare' are claude processes that are not
  interactive agents. Stamping one would let a prompt be sent to a process that
  cannot read it.
- CLAUDECODE=1 is NOT usable as the key: it is absent from claude's own environ
  and present only on the children it spawns, and environ was readable for just
  71 of 145 candidates.

The remote form is one command for all selected panes, because one ssh per pane
would add a round trip per target to every tick."
```

---

### Task 3b: Join a pane to the Claude session running in it

This is where the join belongs — not before the broadcast plan, as an earlier
recommendation had it, but here, because it reuses Task 3's process walk and cannot
exist without it.

It pays for three separate things:

- **duplicate rows.** A Claude session that has a tmux pane is currently TWO rows —
  one `KindPane` from the poll, one `KindAgent` from `claude agents --json` — with
  nothing joining them.
- **`quiet` versus `idle`** (§14). The screen cannot distinguish a finished agent
  from a working one, and neither can the transcript; `claude agents --json` reports
  `done` against `working` as a fact. A joined pane can take its state from the fact
  instead of from pixels.
- **the confirmation rule.** "Identified as an agent" becomes "identified as *this*
  session", which is what makes an exited-and-restarted agent detectable.

**Files:**
- Create: `internal/proc/session.go`
- Test: `internal/proc/session_test.go`
- Modify: `internal/registry/registry.go` (join agent rows onto pane rows)

**Interfaces:**
- Consumes: `Descendants` from Task 3.
- Produces: `func SessionID(all []Proc, agentPID int) (string, bool)`

- [ ] **Step 1: Write the failing test**

Create `internal/proc/session_test.go`:

```go
package proc

import (
	"os"
	"os/exec"
	"testing"
)

// The value must be validated, not trusted: a wrong join lets a send target a pane
// on the strength of another session's state, which is worse than no join at all.
func TestSessionIDRejectsAMalformedValue(t *testing.T) {
	for _, v := range []string{"", "not-a-uuid", "85b8055c", "85b8055c-c34a-4c60-91e5"} {
		t.Setenv(sessionEnv, v)
		if id, ok := SessionID(nil, os.Getpid()); ok {
			t.Errorf("%q was accepted as a session id (got %q)", v, id)
		}
	}
}

func TestSessionIDAcceptsAWellFormedValue(t *testing.T) {
	const want = "85b8055c-c34a-4c60-91e5-b0048842cc66"
	t.Setenv(sessionEnv, want)
	got, ok := SessionID(nil, os.Getpid())
	if !ok {
		t.Fatal("a well-formed session id was not found")
	}
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// The measured shape: the variable is on a CHILD, not on the agent itself. This
// drives a real child so the /proc read is exercised rather than mocked.
func TestSessionIDFindsItOnAChild(t *testing.T) {
	const want = "5095a613-0000-4000-8000-000000000000"
	cmd := exec.Command("sleep", "30")
	cmd.Env = append(os.Environ(), sessionEnv+"="+want)
	if err := cmd.Start(); err != nil {
		t.Skipf("cannot start a child: %v", err)
	}
	defer func() { _ = cmd.Process.Kill(); _, _ = cmd.Process.Wait() }()

	// The parent must NOT carry it, so a hit can only come from the child.
	t.Setenv(sessionEnv, "")
	all := []Proc{
		{PID: os.Getpid(), PPID: 1, Argv: []string{"parent"}},
		{PID: cmd.Process.Pid, PPID: os.Getpid(), Argv: []string{"sleep"}},
	}
	got, ok := SessionID(all, os.Getpid())
	if !ok {
		t.Fatal("the child's session id was not found")
	}
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// An unreadable environ is routine on a shared machine and must not be an error.
func TestSessionIDOnAnUnreachablePID(t *testing.T) {
	if id, ok := SessionID(nil, 999999999); ok {
		t.Errorf("a nonexistent pid yielded %q", id)
	}
}
```

- [ ] **Step 2: Run it**

Run: `go test ./internal/proc/ -run SessionID -v`
Expected: FAIL — `undefined: SessionID`.

- [ ] **Step 3: Write the implementation**

Create `internal/proc/session.go`:

```go
package proc

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// sessionEnv is the variable that carries a Claude Code session's identity.
const sessionEnv = "CLAUDE_CODE_SESSION_ID"

var uuidRe = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// SessionID finds the Claude session a pane's agent belongs to, which is what
// joins a pane row to a `claude agents --json` row.
//
// It reads the environ of the agent's CHILDREN, and that is measured rather than
// stylistic: across 47 live interactive agents on this machine,
// CLAUDE_CODE_SESSION_ID was absent from every sampled agent's own environ and
// present on a child of each. (The 5 that do carry it are themselves children of
// another agent and inherited it.) The obvious reverse route is worse still —
// TMUX_PANE was readable on only 8 of the 47, so a pane cannot be found from a
// session and the join has to run pane → process → child → session.
//
// Only the identified agent's own subtree is read, so the cost is a handful of
// small files per selected pane rather than a sweep of /proc.
func SessionID(all []Proc, agentPID int) (string, bool) {
	if id, ok := readSessionEnv(agentPID); ok {
		return id, true
	}
	for _, c := range Descendants(all, agentPID) {
		if id, ok := readSessionEnv(c.PID); ok {
			return id, true
		}
	}
	return "", false
}

// readSessionEnv returns the session id from one process's environ. An unreadable
// environ is not an error: most processes on a shared machine belong to somebody
// else, and 47 agents here yielded a readable child every time.
func readSessionEnv(pid int) (string, bool) {
	b, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "environ"))
	if err != nil {
		return "", false
	}
	for _, kv := range strings.Split(string(b), "\x00") {
		name, val, ok := strings.Cut(kv, "=")
		if !ok || name != sessionEnv {
			continue
		}
		// Validated as a uuid rather than taken on trust: an empty or malformed
		// value would join a pane to the WRONG session, and a wrong join is worse
		// than none — it would let a send proceed on another session's state.
		if uuidRe.MatchString(val) {
			return val, true
		}
		return "", false
	}
	return "", false
}
```

- [ ] **Step 4: Join the rows in the registry**

`Registry.UpdateAgents` currently creates a row keyed `agent:<shortid>` for every
session. Give it the pane rows' session ids first, so a session that HAS a pane
updates that pane instead of creating a second row:

```go
// UpdateAgents folds Claude's own account of its sessions into the registry.
//
// A session that is running in a pane the hub can see updates THAT row rather than
// adding one: before this, such a session appeared twice — once as the pane the
// poll found and once as the agent the CLI reported — and the two rows disagreed
// about its state, because one came from pixels and the other from a fact.
//
// The fact wins for those rows. §14 explains why: the screen cannot separate a
// finished agent from a working one (Claude renders its input box at all times), so
// `quiet` from the classifier is a guess where `done` from the CLI is not.
func (r *Registry) UpdateAgents(host string, ss []agents.Session, now time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()

	bySession := make(map[string]*Pane, len(r.panes))
	for _, p := range r.panes {
		if p.Kind == KindPane && p.ClaudeSession != "" {
			bySession[p.ClaudeSession] = p
		}
	}
	for _, s := range ss {
		if p, ok := bySession[s.SessionID]; ok {
			p.AgentState = state.FromWord(s.Attention())
			p.AgentName = s.Name
			p.AgentSeenAt = now
			continue // joined: no second row
		}
		// … the existing agent:<id> row, unchanged, for a session with no pane
	}
}
```

`Pane` gains three fields — `ClaudeSession string` (filled by the process walk),
`AgentState state.State` and `AgentSeenAt time.Time` — and `Pane.State()` prefers
`AgentState` when it is fresh, falling back to the pixel classification when it is
not. Freshness matters because `claude agents --json` was measured **30 minutes
stale** (§17): a fact that old is worse than a live guess.

- [ ] **Step 5: Test the join**

```go
// A session with a pane must produce ONE row, not two, and the row must carry the
// FACT rather than the pixel guess.
func TestAgentWithAPaneDoesNotDuplicateTheRow(t *testing.T) {
	r := New()
	now := time.Now()
	// A pane the poll found, already joined to its Claude session.
	r.Update("local", []tmux.Delta{{PaneID: "%3", SessionID: "$0", PaneHeight: 24}},
		map[string]tmux.Labels{"%3": {Session: "work"}}, nil, nil, now, time.Second)
	r.SetClaudeSession("local", "%3", "1ff133f7-c34a-4c60-91e5-b0048842cc66")

	r.UpdateAgents("local", []agents.Session{{
		SessionID: "1ff133f7-c34a-4c60-91e5-b0048842cc66",
		ID:        "1ff133f7", Name: "goldens", Kind: "interactive", State: "blocked",
	}}, now)

	panes := r.Panes()
	if len(panes) != 1 {
		t.Fatalf("got %d rows, want 1 — the session and its pane were not joined: %+v",
			len(panes), panes)
	}
	if panes[0].PaneID != "%3" {
		t.Errorf("the surviving row is %q, want the pane", panes[0].PaneID)
	}
	if panes[0].State() != state.Needs {
		t.Errorf("state = %v, want needs — the CLI's fact must win over the pixels",
			panes[0].State())
	}
}

// A session with NO pane still gets its own row: most of them have none, which is
// the whole reason the producer exists.
func TestAgentWithoutAPaneStillGetsARow(t *testing.T) {
	r := New()
	r.UpdateAgents("local", []agents.Session{{
		SessionID: "4ca5ffa9-e6ed-45f2-aa6c-3dd4a76946d8",
		ID:        "4ca5ffa9", Name: "erp", Kind: "background", State: "working",
	}}, time.Now())
	panes := r.Panes()
	if len(panes) != 1 || panes[0].Kind != KindAgent {
		t.Fatalf("got %+v, want one agent row", panes)
	}
}

// A stale fact must not beat a live guess: the CLI was measured 30 minutes behind.
func TestAStaleAgentStateYieldsToThePixels(t *testing.T) {
	r := New()
	old := time.Now().Add(-30 * time.Minute)
	r.Update("local", []tmux.Delta{{PaneID: "%3", SessionID: "$0", PaneHeight: 24}},
		map[string]tmux.Labels{"%3": {Session: "work"}}, nil, nil, time.Now(), time.Second)
	r.SetClaudeSession("local", "%3", "1ff133f7-c34a-4c60-91e5-b0048842cc66")
	r.UpdateAgents("local", []agents.Session{{
		SessionID: "1ff133f7-c34a-4c60-91e5-b0048842cc66", State: "done",
	}}, old)

	if got := r.Panes()[0].State(); got == state.Idle {
		t.Error("a 30-minute-old `done` overrode the live classification")
	}
}
```

- [ ] **Step 6: Run and commit**

Run: `go test -race ./internal/proc/ ./internal/registry/ -v`

```bash
gofmt -l . && go test -race ./... && git add internal/proc/ internal/registry/ && git commit -m "feat: join a pane to the Claude session running in it

A session with a pane was TWO rows — one from the poll, one from
\`claude agents --json\` — and they disagreed about its state, because one came
from pixels and the other from a fact. They are one row now, and the fact wins
while it is fresh.

The join runs pane -> process -> child -> session, and the direction is measured:
CLAUDE_CODE_SESSION_ID was absent from every sampled agent's own environ and
present on a child of each across 47 live agents, while TMUX_PANE was readable on
only 8 of them — so a session cannot be found from a pane the other way round.

Freshness is checked because the CLI was measured 30 minutes stale: a fact that
old is worse than a live guess, and section 14's quiet-versus-idle question is
only answered by a fact that is actually current."
```

---

### Task 4: The token, stamped from the identification

**Files:**
- Create: `internal/broadcast/stamp.go`
- Test: `internal/broadcast/stamp_test.go`

**Interfaces:**
- Consumes: `Instance` (Task 2), `tmux.Runner`.
- Produces:
  - `type Stamper struct { … }`
  - `func NewStamper(r tmux.Runner, i Instance) *Stamper`
  - `func (s *Stamper) Stamp(ctx, t tmux.Target, paneID string) (token string, err error)`
  - `func (s *Stamper) Unstamp(ctx, t tmux.Target, paneID string) error`
  - `func (s *Stamper) Token(host, paneID string) (string, bool)`

- [ ] **Step 1: Write the failing test**

Create `internal/broadcast/stamp_test.go`:

```go
package broadcast

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/DawnBreather/tmux-hub/internal/tmux"
)

func liveServer(t *testing.T, panes int) (tmux.Target, []string) {
	t.Helper()
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	sock := filepath.Join(t.TempDir(), "tmux.sock")
	must := func(args ...string) {
		t.Helper()
		full := append([]string{"-S", sock, "-f", "/dev/null"}, args...)
		if out, err := exec.Command("tmux", full...).CombinedOutput(); err != nil {
			t.Fatalf("tmux %v: %v: %s", args, err, out)
		}
	}
	// `cat` panes: input is echoed verbatim and nothing is ever executed, so a
	// test can assert on what ARRIVED rather than on what tmux said.
	must("new-session", "-d", "-s", "w", "-x", "80", "-y", "24", "cat")
	for i := 1; i < panes; i++ {
		must("split-window", "-t", "w", "-d", "cat")
	}
	t.Cleanup(func() { _ = exec.Command("tmux", "-S", sock, "kill-server").Run() })

	out, err := exec.Command("tmux", "-S", sock, "list-panes", "-a", "-F", "#{pane_id}").Output()
	if err != nil {
		t.Fatalf("list-panes: %v", err)
	}
	var ids []string
	for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if l != "" {
			ids = append(ids, l)
		}
	}
	if len(ids) != panes {
		t.Fatalf("got %d panes, want %d", len(ids), panes)
	}
	return tmux.Target{Label: "test", Socket: sock}, ids
}

func TestStampIsPaneScopedAndReadableBack(t *testing.T) {
	tgt, ids := liveServer(t, 2)
	r := tmux.NewExec(10 * time.Second)
	s := NewStamper(r, Instance("t1"))

	tok, err := s.Stamp(context.Background(), tgt, ids[0])
	if err != nil {
		t.Fatalf("Stamp: %v", err)
	}
	if tok == "" {
		t.Fatal("Stamp returned an empty token")
	}

	res, err := r.Run(context.Background(), tgt, "list-panes", "-a",
		"-F", "#{pane_id} [#{@hub_t1}]")
	if err != nil {
		t.Fatalf("list-panes: %v", err)
	}
	if !strings.Contains(res.Stdout, ids[0]+" ["+tok+"]") {
		t.Errorf("the stamped pane does not carry the token:\n%s", res.Stdout)
	}
	// The measured fail-open: `set -t <pane>` with no -p lands at SESSION scope,
	// and an unstamped pane then resolves the value and passes the guard.
	if !strings.Contains(res.Stdout, ids[1]+" []") {
		t.Errorf("the token leaked to another pane — -p is missing:\n%s", res.Stdout)
	}
}

// The option must be invisible outside the pane, or it is a global mutation the
// user could trip over.
func TestStampIsInvisibleGlobally(t *testing.T) {
	tgt, ids := liveServer(t, 1)
	r := tmux.NewExec(10 * time.Second)
	s := NewStamper(r, Instance("t2"))
	if _, err := s.Stamp(context.Background(), tgt, ids[0]); err != nil {
		t.Fatalf("Stamp: %v", err)
	}
	res, _ := r.Run(context.Background(), tgt, "show", "-gv", "@hub_t2")
	if res.RC == 0 && strings.TrimSpace(res.Stdout) != "" {
		t.Errorf("the option is visible server-wide: %q", res.Stdout)
	}
}

// Re-stamping must produce a NEW token. A pane-bound token proves pane identity,
// not process identity: respawn-pane keeps the id, the pane_pid and the token
// while replacing the process.
func TestReStampRotatesTheToken(t *testing.T) {
	tgt, ids := liveServer(t, 1)
	s := NewStamper(tmux.NewExec(10*time.Second), Instance("t3"))
	a, err := s.Stamp(context.Background(), tgt, ids[0])
	if err != nil {
		t.Fatalf("Stamp: %v", err)
	}
	b, err := s.Stamp(context.Background(), tgt, ids[0])
	if err != nil {
		t.Fatalf("re-Stamp: %v", err)
	}
	if a == b {
		t.Error("re-stamping reused the token, so a stale selection would still pass")
	}
	if got, ok := s.Token("test", ids[0]); !ok || got != b {
		t.Errorf("Token() = (%q, %v), want the newest token", got, ok)
	}
}

func TestUnstampRemovesTheOptionAndTheMemory(t *testing.T) {
	tgt, ids := liveServer(t, 1)
	r := tmux.NewExec(10 * time.Second)
	s := NewStamper(r, Instance("t4"))
	if _, err := s.Stamp(context.Background(), tgt, ids[0]); err != nil {
		t.Fatalf("Stamp: %v", err)
	}
	if err := s.Unstamp(context.Background(), tgt, ids[0]); err != nil {
		t.Fatalf("Unstamp: %v", err)
	}
	res, _ := r.Run(context.Background(), tgt, "list-panes", "-a",
		"-F", "#{pane_id} [#{@hub_t4}]")
	if !strings.Contains(res.Stdout, ids[0]+" []") {
		t.Errorf("the option survived Unstamp:\n%s", res.Stdout)
	}
	if _, ok := s.Token("test", ids[0]); ok {
		t.Error("the hub still remembers a token it has unstamped")
	}
}

// Unstamping a pane that is already gone is routine, not exceptional: it is what
// happens every time an agent exits.
func TestUnstampAVanishedPaneIsNotAnError(t *testing.T) {
	tgt, _ := liveServer(t, 1)
	s := NewStamper(tmux.NewExec(10*time.Second), Instance("t5"))
	if err := s.Unstamp(context.Background(), tgt, "%999"); err != nil {
		t.Errorf("Unstamp of a vanished pane = %v, want nil", err)
	}
}
```

- [ ] **Step 2: Run it**

Run: `go test ./internal/broadcast/ -run Stamp -v`
Expected: FAIL — `undefined: NewStamper`.

- [ ] **Step 3: Write the implementation**

Create `internal/broadcast/stamp.go`:

```go
package broadcast

import (
	"context"
	"fmt"
	"sync"

	"github.com/DawnBreather/tmux-hub/internal/tmux"
)

// Stamper owns the per-pane identity tokens.
//
// The token exists because existence is not identity: #{pane_id} restarts at %0
// when a server restarts, so a stale %0 EXISTS and points at a different session.
// Over ssh that is invisible — the tunnel and the master survive a remote tmux
// restart and the poll returns a full pane list — so a pre-flight that only checks
// existence delivers the prompt to the wrong pane.
type Stamper struct {
	run  tmux.Runner
	inst Instance

	mu     sync.Mutex
	tokens map[string]string // "host\x00%NN" -> token
}

func NewStamper(r tmux.Runner, i Instance) *Stamper {
	return &Stamper{run: r, inst: i, tokens: map[string]string{}}
}

func key(host, paneID string) string { return host + "\x00" + paneID }

// Stamp writes a fresh token onto one pane and remembers it.
//
// `-p` is not optional. Measured: `set -t <pane>` with no -p lands at SESSION
// scope, and a pane that was never stamped then resolves the value and passes the
// guard — one missing character is a session-wide fail-open. (`-g` would be
// server-wide, which is worse.)
//
// A fresh token every time is equally load-bearing: a pane-bound token proves
// PANE identity, not PROCESS identity. `respawn-pane` keeps the id, the token and
// even `pane_pid` while replacing the process, and the commoner case needs no tmux
// command at all, because `pane_pid` is the shell and does not change as the agent
// comes and goes. Rotating on every re-stamp is what makes the guard mean
// "identified as an agent no more than one tick ago".
func (s *Stamper) Stamp(ctx context.Context, t tmux.Target, paneID string) (string, error) {
	tok := NewToken()
	res, err := s.run.Run(ctx, t, "set", "-p", "-t", paneID, s.inst.Option(), tok)
	if err != nil {
		return "", err
	}
	if res.RC != 0 {
		return "", fmt.Errorf("broadcast: cannot stamp %s on %s: %s",
			paneID, t.Label, res.Stderr)
	}
	s.mu.Lock()
	s.tokens[key(t.Label, paneID)] = tok
	s.mu.Unlock()
	return tok, nil
}

// Unstamp removes the option and forgets the token, so the guard refuses by
// construction.
//
// A non-zero rc is deliberately not an error: the pane being gone is the
// commonest reason to unstamp — the agent exited — and treating the routine case
// as a failure would put an error on screen every time someone finished a task.
func (s *Stamper) Unstamp(ctx context.Context, t tmux.Target, paneID string) error {
	s.mu.Lock()
	delete(s.tokens, key(t.Label, paneID))
	s.mu.Unlock()

	_, err := s.run.Run(ctx, t, "set", "-pu", "-t", paneID, s.inst.Option())
	return err
}

// Token returns what the hub believes is stamped on a pane. The guard is built
// from this value, so a pane the hub has no token for cannot be sent to at all.
func (s *Stamper) Token(host, paneID string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tok, ok := s.tokens[key(host, paneID)]
	return tok, ok
}
```

- [ ] **Step 4: Run the tests**

Run: `go test ./internal/broadcast/ -v`
Expected: PASS all nine.

- [ ] **Step 5: Commit**

```bash
gofmt -l . && go test -race ./... && git add internal/broadcast/stamp.go internal/broadcast/stamp_test.go && git commit -m "feat(broadcast): the per-pane identity token

Existence is not identity. #{pane_id} restarts at %0 when a server restarts, so
a stale %0 exists and points at a different session — and over ssh that is
invisible, because the tunnel and master survive a remote tmux restart and the
poll returns a full pane list. A pre-flight that checks existence therefore
delivers the prompt to the wrong pane.

Two details are one character each and both are fail-opens. -p is mandatory:
set -t <pane> without it lands at SESSION scope, and an unstamped pane then
resolves the value and passes the guard. And the token rotates on every
re-stamp, because a pane-bound token proves pane identity and not process
identity — respawn-pane keeps the id, the token and pane_pid while replacing the
process, and the commoner case needs no tmux command at all since pane_pid is
the shell and never changes as the agent comes and goes.

Unstamping a vanished pane is not an error. It is what happens every time an
agent exits, and putting an error on screen for it would train the user to
ignore errors."
```

---

### Task 5: The guarded send, and three outcomes

The core. One invocation stamps nothing, checks the token, pastes, and reports —
and then a **second** read decides whether anything arrived.

**Files:**
- Create: `internal/broadcast/send.go`
- Test: `internal/broadcast/send_test.go`

**Interfaces:**
- Consumes: `Instance`, `Stamper`, `tmux.InputRunner`.
- Produces:
  - `type Outcome string` with `Delivered`, `Unwitnessed`, `Refused`
  - `type Target struct { Host string; Tmux tmux.Target; PaneID string }`
  - `type Result struct { Target Target; Outcome Outcome; Reason string; ActivityBefore int64 }`
  - `func (s *Sender) Send(ctx, tg Target, text string) (Result, error)`
  - `func (s *Sender) Witness(ctx, r Result, text string) Result`
  - `func (s *Sender) Submit(ctx, tg Target) error`
  - `func (s *Sender) Interrupt(ctx, tg Target, key string) error`

- [ ] **Step 1: Write the failing test**

Create `internal/broadcast/send_test.go`:

```go
package broadcast

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/DawnBreather/tmux-hub/internal/tmux"
)

func sender(t *testing.T, inst Instance) (*Sender, *Stamper, tmux.Target, []string) {
	t.Helper()
	tgt, ids := liveServer(t, 2)
	r := tmux.NewExec(10 * time.Second)
	st := NewStamper(r, inst)
	return NewSender(r.(tmux.InputRunner), st, inst), st, tgt, ids
}

func captured(t *testing.T, tgt tmux.Target, paneID string) string {
	t.Helper()
	out, err := exec.Command("tmux", "-S", tgt.Socket, "capture-pane", "-p", "-t", paneID).Output()
	if err != nil {
		t.Fatalf("capture-pane: %v", err)
	}
	return string(out)
}

// The assertion is on the PANE, not on the confirmation. Measured with the old
// -H primitive, the hub printed `OK %0` having delivered nothing — so a test that
// reads the confirmation would have passed on a completely broken write path.
func TestSendDeliversAndIsWitnessed(t *testing.T) {
	s, st, tgt, ids := sender(t, Instance("s1"))
	if _, err := st.Stamp(context.Background(), tgt, ids[0]); err != nil {
		t.Fatalf("Stamp: %v", err)
	}
	time.Sleep(1100 * time.Millisecond) // let window_activity settle in the past

	const text = "refactor the auth module; run the tests"
	res, err := s.Send(context.Background(), Target{Host: "test", Tmux: tgt, PaneID: ids[0]}, text)
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if res.Outcome == Refused {
		t.Fatalf("a stamped pane was refused: %s", res.Reason)
	}

	res = s.Witness(context.Background(), res, text)
	if res.Outcome != Delivered {
		t.Errorf("Outcome = %s (%s), want delivered", res.Outcome, res.Reason)
	}
	if got := captured(t, tgt, ids[0]); !strings.Contains(got, "refactor the auth module") {
		t.Errorf("the text did not arrive in the pane:\n%s", got)
	}
	// Nothing may execute: Enter is a separate act.
	if strings.Contains(captured(t, tgt, ids[0]), "command not found") {
		t.Error("the payload executed")
	}
}

// The witness must be a SECOND read. Measured three times on 3.7b: activity reads
// identical before and after the paste inside one invocation, because it tracks
// the pane's OUTPUT and the process cannot have answered while the batch that
// wrote to it is still running. So Send alone must never claim delivery.
func TestSendAloneNeverClaimsDelivery(t *testing.T) {
	s, st, tgt, ids := sender(t, Instance("s2"))
	if _, err := st.Stamp(context.Background(), tgt, ids[0]); err != nil {
		t.Fatalf("Stamp: %v", err)
	}
	res, err := s.Send(context.Background(), Target{Host: "test", Tmux: tgt, PaneID: ids[0]}, "x")
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if res.Outcome == Delivered {
		t.Error("Send claimed delivery without a second read — the witness cannot work there")
	}
	if res.ActivityBefore == 0 {
		t.Error("Send did not record activity_before, so Witness has nothing to compare")
	}
}

// An unstamped pane must be refused AND must receive nothing. Both halves matter:
// measured, a guard without its own -t passed while pasting into the unstamped
// pane, and printed OK for it.
func TestUnstampedPaneIsRefusedAndReceivesNothing(t *testing.T) {
	s, st, tgt, ids := sender(t, Instance("s3"))
	if _, err := st.Stamp(context.Background(), tgt, ids[0]); err != nil {
		t.Fatalf("Stamp: %v", err)
	}
	res, err := s.Send(context.Background(),
		Target{Host: "test", Tmux: tgt, PaneID: ids[1]}, "MUST-NOT-ARRIVE")
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if res.Outcome != Refused {
		t.Errorf("Outcome = %s, want refused", res.Outcome)
	}
	time.Sleep(300 * time.Millisecond)
	for _, id := range ids {
		if got := captured(t, tgt, id); strings.Contains(got, "MUST-NOT-ARRIVE") {
			t.Errorf("the payload reached %s despite the refusal:\n%s", id, got)
		}
	}
}

// A pane the hub holds no token for cannot be sent to at all — the guard would
// otherwise be built from an empty expected value, which is the fail-open §7 names.
func TestSendRefusesAPaneWithNoToken(t *testing.T) {
	s, _, tgt, ids := sender(t, Instance("s4"))
	res, err := s.Send(context.Background(), Target{Host: "test", Tmux: tgt, PaneID: ids[0]}, "x")
	if err == nil && res.Outcome != Refused {
		t.Errorf("Outcome = %s, want refused for an unknown token", res.Outcome)
	}
}

// After the agent goes, the guard must refuse by construction.
func TestUnstampMakesTheNextSendRefuse(t *testing.T) {
	s, st, tgt, ids := sender(t, Instance("s5"))
	if _, err := st.Stamp(context.Background(), tgt, ids[0]); err != nil {
		t.Fatalf("Stamp: %v", err)
	}
	if err := st.Unstamp(context.Background(), tgt, ids[0]); err != nil {
		t.Fatalf("Unstamp: %v", err)
	}
	res, _ := s.Send(context.Background(), Target{Host: "test", Tmux: tgt, PaneID: ids[0]}, "y")
	if res.Outcome != Refused {
		t.Errorf("Outcome = %s, want refused after unstamp", res.Outcome)
	}
}

// Multi-line is the case send-keys got wrong: with bracketed paste on, an embedded
// newline submits the first paragraph. Both flags on paste-buffer prevent it.
func TestMultiLineArrivesWholeAndUnexecuted(t *testing.T) {
	s, st, tgt, ids := sender(t, Instance("s6"))
	if _, err := st.Stamp(context.Background(), tgt, ids[0]); err != nil {
		t.Fatalf("Stamp: %v", err)
	}
	text := "line one of the prompt\nline two of the prompt\nline three;"
	res, err := s.Send(context.Background(), Target{Host: "test", Tmux: tgt, PaneID: ids[0]}, text)
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if res.Outcome == Refused {
		t.Fatalf("refused: %s", res.Reason)
	}
	time.Sleep(400 * time.Millisecond)
	got := captured(t, tgt, ids[0])
	for _, want := range []string{"line one of the prompt", "line two of the prompt", "line three;"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q from the pane:\n%s", want, got)
		}
	}
}

// A trailing semicolon is what send-keys -l silently ate. It must survive.
func TestTrailingSemicolonSurvives(t *testing.T) {
	s, st, tgt, ids := sender(t, Instance("s7"))
	if _, err := st.Stamp(context.Background(), tgt, ids[0]); err != nil {
		t.Fatalf("Stamp: %v", err)
	}
	if _, err := s.Send(context.Background(),
		Target{Host: "test", Tmux: tgt, PaneID: ids[0]}, "done;"); err != nil {
		t.Fatalf("Send: %v", err)
	}
	time.Sleep(400 * time.Millisecond)
	if got := captured(t, tgt, ids[0]); !strings.Contains(got, "done;") {
		t.Errorf("the trailing semicolon was eaten:\n%s", got)
	}
}

// The buffer must not survive the send, in EITHER direction. A batch that aborts
// left `tmux-hub-2: 42 bytes: "secret prompt…"` as the most recent buffer, ahead
// of the user's own, so their next `prefix ]` pastes the hub's prompt.
func TestNoBufferSurvivesASendOrARefusal(t *testing.T) {
	s, st, tgt, ids := sender(t, Instance("s8"))
	if _, err := st.Stamp(context.Background(), tgt, ids[0]); err != nil {
		t.Fatalf("Stamp: %v", err)
	}
	if _, err := s.Send(context.Background(),
		Target{Host: "test", Tmux: tgt, PaneID: ids[0]}, "delivered payload"); err != nil {
		t.Fatalf("Send: %v", err)
	}
	// The refusal path is the one that skipped cleanup.
	if _, err := s.Send(context.Background(),
		Target{Host: "test", Tmux: tgt, PaneID: "%999"}, "secret prompt"); err != nil {
		t.Logf("Send to a missing pane returned %v (expected)", err)
	}
	out, _ := exec.Command("tmux", "-S", tgt.Socket, "list-buffers",
		"-F", "#{buffer_name}").Output()
	if strings.Contains(string(out), "tmux-hub-") {
		t.Errorf("a hub buffer survived:\n%s", out)
	}
	if strings.Contains(string(out), "secret") {
		t.Errorf("the payload is still a buffer:\n%s", out)
	}
}

// Submit is a separate invocation ~50ms later, never part of the payload.
func TestSubmitIsSeparate(t *testing.T) {
	s, st, tgt, ids := sender(t, Instance("s9"))
	if _, err := st.Stamp(context.Background(), tgt, ids[0]); err != nil {
		t.Fatalf("Stamp: %v", err)
	}
	tg := Target{Host: "test", Tmux: tgt, PaneID: ids[0]}
	if _, err := s.Send(context.Background(), tg, "echo hi"); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if err := s.Submit(context.Background(), tg); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	time.Sleep(400 * time.Millisecond)
	if got := captured(t, tgt, ids[0]); !strings.Contains(got, "echo hi") {
		t.Errorf("text missing after submit:\n%s", got)
	}
}
```

- [ ] **Step 2: Run it**

Run: `go test ./internal/broadcast/ -run Send -v`
Expected: FAIL — `undefined: NewSender`.

- [ ] **Step 3: Write the implementation**

Create `internal/broadcast/send.go`:

```go
package broadcast

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/DawnBreather/tmux-hub/internal/tmux"
)

// Outcome is what happened to one target. It is three-valued on purpose: a
// confirmation fires whenever the pane resolves and the guard passed, so a boolean
// would report success for a send that delivered nothing — measured, the hub
// printed `OK %0` having delivered nothing at all, and the operator then waits on
// an agent that never got the prompt.
type Outcome string

const (
	Delivered   Outcome = "delivered"
	Unwitnessed Outcome = "sent-unwitnessed"
	Refused     Outcome = "refused"
)

// Target is one pane to write to.
type Target struct {
	Host   string
	Tmux   tmux.Target
	PaneID string
}

// Result is the per-target record. ActivityBefore is carried so the witness can
// be a later read, which is the only place it can work.
type Result struct {
	Target         Target
	Outcome        Outcome
	Reason         string
	ActivityBefore int64
	Token          string
}

// WitnessDelay is how long to wait before the second read. 150 ms is enough for the
// observable that actually decides: measured, the pane's screen shows the text at
// +250 ms in 6 of 6 rapid sends. It is NOT enough to cross a one-second boundary,
// which is why activity is the secondary observable rather than the primary — see
// Witness. The next ordinary tick is the backstop for either.
const WitnessDelay = 150 * time.Millisecond

// Sender is the write path.
type Sender struct {
	run  tmux.InputRunner
	st   *Stamper
	inst Instance
	seq  atomic.Uint64
}

func NewSender(r tmux.InputRunner, st *Stamper, i Instance) *Sender {
	return &Sender{run: r, st: st, inst: i}
}

// Send puts text into one pane, guarded by that pane's token inside the same
// invocation. It never returns Delivered: only Witness can, because the witness
// cannot be read here (see WitnessDelay).
func (s *Sender) Send(ctx context.Context, tg Target, text string) (Result, error) {
	res := Result{Target: tg, Outcome: Refused}

	tok, ok := s.st.Token(tg.Host, tg.PaneID)
	if !ok || tok == "" {
		// Building a guard from an empty expected value is the fail-open this
		// refuses: every unstamped pane would satisfy it.
		res.Reason = "the hub holds no identity token for this pane"
		return res, nil
	}
	res.Token = tok

	buf := s.inst.Buffer(s.seq.Add(1))

	// The payload travels on stdin, so no quoting layer touches it. `load-buffer`
	// runs AFTER the token check has been decided and BEFORE the paste, and the
	// deferred delete is its own invocation so it cannot be skipped by a batch
	// that aborts.
	if r, err := s.run.RunInput(ctx, tg.Tmux, []byte(text),
		"load-buffer", "-b", buf, "-"); err != nil {
		return res, err
	} else if r.RC != 0 {
		res.Reason = "load-buffer: " + r.Stderr
		return res, nil
	}
	defer func() {
		// Its own invocation, regardless of what happened: a batch that aborts
		// skips its tail, which is how a payload became the user's most recent
		// paste buffer.
		_, _ = s.run.RunInput(context.Background(), tg.Tmux, nil, "delete-buffer", "-b", buf)
	}()

	// Both the `if` and every sub-command carry their own -t. Measured separately:
	// without the sub-command's, a crossed pair delivered to the unstamped pane and
	// confirmed OK for it; without the `if`'s, the guard read the option from the
	// server's CURRENT pane and pasted into an unstamped one, printing OK %1.
	//
	// No literal % appears in any template: display -p runs through strftime, so
	// identity is emitted as #{pane_id} and the token as #{@hub_<instance>}.
	opt := s.inst.Option()
	then := strings.Join([]string{
		"display -p -t " + tg.PaneID + " 'BEFORE #{pane_id} #{window_activity}'",
		"paste-buffer -d -p -r -b " + buf + " -t " + tg.PaneID,
		"display -p -t " + tg.PaneID + " 'SENT #{pane_id} #{" + opt + "}'",
	}, " ; ")
	els := "display -p -t " + tg.PaneID + " 'REFUSED #{pane_id}'"

	out, err := s.run.RunInput(ctx, tg.Tmux, nil,
		"if", "-F", "-t", tg.PaneID,
		"#{==:#{"+opt+"},"+tok+"}", then, els)
	if err != nil {
		return res, err
	}

	before, sentID, sentTok, refused := parseGuardOutput(out.Stdout)
	switch {
	case refused:
		res.Reason = "the guard refused: the pane is no longer the one that was selected"
		return res, nil
	case sentID == "":
		res.Reason = "no confirmation came back: " + firstLine(out.Stderr)
		return res, nil
	case sentID != tg.PaneID:
		// Corroboration, not the proof. Measured: removing this check leaves the
		// whole suite green, because the token check below catches everything it
		// would. It stays because it costs nothing and it NAMES the pane in the
		// failure message.
		res.Reason = fmt.Sprintf("confirmation named %s, not %s", sentID, tg.PaneID)
		return res, nil
	case sentTok != tok:
		// THIS is the proof of identity: a wrong pane cannot produce a matching
		// random token. Measured — removing it turns the suite red, where removing
		// the id check above does not.
		res.Reason = "confirmation carried a different token"
		return res, nil
	}

	res.Outcome = Unwitnessed
	res.ActivityBefore = before
	res.Reason = "awaiting the witness"
	return res, nil
}

// Witness decides whether anything actually arrived, from two observables read in
// ONE invocation:
//
//   - the pane's tail now contains a prefix of what was sent;
//   - window_activity advanced past what the guard invocation recorded.
//
// The SCREEN is checked first, and the order is measured rather than aesthetic. On
// six back-to-back sends against tmux 3.2a the screen confirmed **6 of 6** while
// activity confirmed **2 of 6**: window_activity has one-second resolution, and a
// broadcast writes to several panes inside one second, so the send and the pane's
// previous output land in the same tick of that clock. Activity is therefore the
// SECONDARY observable — it earns its place only for a pane that redraws without
// showing the text, such as a password prompt.
//
// A pane that satisfies neither is Unwitnessed, which is a real answer and not a
// failure: a prompt that echoes nothing looks exactly like this.
func (s *Sender) Witness(ctx context.Context, r Result, text string) Result {
	if r.Outcome == Refused {
		return r
	}
	select {
	case <-ctx.Done():
		return r
	case <-time.After(WitnessDelay):
	}

	// One invocation for both. Measured on 3.2a, the ACT line comes first 6 times
	// out of 6, so the split is reliable — but it is keyed on the marker rather
	// than on the position, because an ordering that happens to hold is not one to
	// depend on.
	res, err := s.run.RunInput(ctx, r.Target.Tmux, nil,
		"display", "-p", "-t", r.Target.PaneID, "ACT #{window_activity}",
		";", "capture-pane", "-p", "-t", r.Target.PaneID)
	if err != nil || res.RC != 0 {
		r.Outcome = Unwitnessed
		r.Reason = "the witness read did not come back: " + firstLine(res.Stderr)
		return r
	}
	act, screen := splitWitness(res.Stdout)

	if echoedPrefix(screen, text) {
		r.Outcome, r.Reason = Delivered, "the text is on the pane"
		return r
	}
	if act > r.ActivityBefore {
		r.Outcome, r.Reason = Delivered, "activity advanced"
		return r
	}
	r.Outcome = Unwitnessed
	r.Reason = "the pane does not show the text and produced no new output"
	return r
}

// splitWitness separates the activity line from the captured screen. It finds the
// marker rather than trusting the first line, so a pane whose own content begins
// with something ACT-shaped cannot shift the parse.
func splitWitness(out string) (int64, string) {
	lines := strings.Split(out, "\n")
	for i, l := range lines {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(l), "ACT "); ok {
			act, _ := strconv.ParseInt(strings.TrimSpace(rest), 10, 64)
			return act, strings.Join(append(lines[:i:i], lines[i+1:]...), "\n")
		}
	}
	return 0, out
}

// echoedPrefix looks for the first meaningful run of the sent text. A whole-text
// match would fail on any pane that wraps or reformats — Claude Code renders a
// prompt inside its own input box — so the check is a prefix, and short enough to
// survive wrapping at any width the tile supports.
func echoedPrefix(screen, text string) bool {
	line := strings.TrimSpace(strings.SplitN(text, "\n", 2)[0])
	if len(line) > 24 {
		line = line[:24]
	}
	if len(line) < 4 {
		return false // too short to be evidence of anything
	}
	return strings.Contains(screen, line)
}

// parseGuardOutput reads the confirmation lines. It asserts nothing here; the
// caller compares the echoed id and token against what it intended.
func parseGuardOutput(stdout string) (before int64, paneID, token string, refused bool) {
	for _, l := range strings.Split(stdout, "\n") {
		f := strings.Fields(strings.TrimSpace(l))
		switch {
		case len(f) >= 3 && f[0] == "BEFORE":
			before, _ = strconv.ParseInt(f[2], 10, 64)
		case len(f) >= 3 && f[0] == "SENT":
			paneID, token = f[1], f[2]
		case len(f) >= 1 && f[0] == "REFUSED":
			refused = true
		}
	}
	return before, paneID, token, refused
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// Submit presses Enter, always as its own invocation and never as part of the
// payload. A newline inside the text is what made send-keys execute paragraph one.
func (s *Sender) Submit(ctx context.Context, tg Target) error {
	tok, ok := s.st.Token(tg.Host, tg.PaneID)
	if !ok {
		return fmt.Errorf("broadcast: refusing to submit to an unidentified pane %s", tg.PaneID)
	}
	// Guarded exactly like the send: between the paste and the Enter the pane could
	// have been replaced, and an Enter into the wrong pane executes whatever is
	// sitting at that prompt.
	_, err := s.run.RunInput(ctx, tg.Tmux, nil,
		"if", "-F", "-t", tg.PaneID, "#{==:#{"+s.inst.Option()+"},"+tok+"}",
		"send-keys -t "+tg.PaneID+" Enter", "")
	return err
}

// Interrupt sends a control key. It is a separate hotkey rather than text because
// C-c and Escape are not expressible as a payload, and it is guarded for the same
// reason Submit is.
func (s *Sender) Interrupt(ctx context.Context, tg Target, keyName string) error {
	switch keyName {
	case "C-c", "Escape":
	default:
		return fmt.Errorf("broadcast: %q is not an interrupt key", keyName)
	}
	tok, ok := s.st.Token(tg.Host, tg.PaneID)
	if !ok {
		return fmt.Errorf("broadcast: refusing to interrupt an unidentified pane %s", tg.PaneID)
	}
	_, err := s.run.RunInput(ctx, tg.Tmux, nil,
		"if", "-F", "-t", tg.PaneID, "#{==:#{"+s.inst.Option()+"},"+tok+"}",
		"send-keys -t "+tg.PaneID+" "+keyName, "")
	return err
}
```

- [ ] **Step 4: Run the tests**

Run: `go test ./internal/broadcast/ -v`
Expected: PASS. Every delivery assertion reads the **pane**, so a green run means
text arrived, not that tmux said `OK`.

- [ ] **Step 5: Add the three tests that the calibration proved were missing**

Written after running the sweep below, because all three guarantees they cover were
**green under the mutation that removes them**. Each failure had a different cause
and all three are worth knowing:

- The `if`'s own `-t` was masked by an earlier layer: `Send` refuses outright when
  the hub holds no token, so an unstamped pane never reaches the guard at all.
- `paste-buffer -d` was masked by the deferred `delete-buffer`: either alone removes
  the buffer, so with both in place neither is tested.
- `-p` and `-r` have no observable consequence against a `cat` pane, which
  interprets neither bracketed paste nor CR.

Append to `internal/broadcast/send_test.go`:

```go
// The `if` needs its own -t, and proving it requires DISABLING the earlier layer
// that masks it: Send refuses outright when the hub holds no token, so a plain
// unstamped pane never reaches the guard at all. Measured — the mutation that
// drops the if's -t left TestUnstampedPaneIsRefusedAndReceivesNothing green.
//
// The case that does reach the guard is a token the hub remembers and the SERVER
// no longer agrees with, which is the real-world one: respawn-pane, an out-of-band
// unstamp, or a server restart recycling the id. With the if targeted, the guard
// reads the target's own option and refuses. Without it, the guard reads the
// server's CURRENT pane — stamped, and a different pane — passes, and the payload
// lands somewhere nobody selected.
func TestGuardReadsTheTargetsOwnOptionNotTheCurrentPanes(t *testing.T) {
	s, st, tgt, ids := sender(t, Instance("sg"))
	ctx := context.Background()

	// The target is stamped, so the hub holds a token for it and the early
	// "no token" refusal — which masked this guarantee entirely — cannot fire.
	tok, err := st.Stamp(ctx, tgt, ids[1])
	if err != nil {
		t.Fatalf("Stamp %s: %v", ids[1], err)
	}

	// The active pane is the stamped one whose option the untargeted guard would
	// read. Assert it rather than assume it, or the test silently stops
	// discriminating the day tmux picks another pane.
	act, err := exec.Command("tmux", "-S", tgt.Socket, "display", "-p", "#{pane_id}").Output()
	if err != nil {
		t.Fatalf("display: %v", err)
	}
	if strings.TrimSpace(string(act)) != ids[0] {
		t.Skipf("the active pane is %q, not %s — this test needs the decoy to be active",
			strings.TrimSpace(string(act)), ids[0])
	}

	// The decoy carries EXACTLY the token we are about to send with, and the target
	// no longer does. That is what makes the two implementations differ: an
	// untargeted guard reads the decoy, matches, and passes. Giving the decoy any
	// other value would make the guard fail for the wrong reason and the test would
	// pass whether or not the -t is there — measured, that is what the first
	// version of this test did.
	set := func(pane, val string) {
		t.Helper()
		if out, err := exec.Command("tmux", "-S", tgt.Socket, "set", "-p", "-t", pane,
			"@hub_sg", val).CombinedOutput(); err != nil {
			t.Fatalf("out-of-band set on %s: %v: %s", pane, err, out)
		}
	}
	set(ids[0], tok)                     // the decoy matches
	set(ids[1], "the-pane-was-replaced") // the target does not

	res, err := s.Send(ctx, Target{Host: "test", Tmux: tgt, PaneID: ids[1]}, "MUST-NOT-ARRIVE")
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if res.Outcome != Refused {
		t.Errorf("Outcome = %s (%s), want refused", res.Outcome, res.Reason)
	}
	time.Sleep(400 * time.Millisecond)
	for _, id := range ids {
		if got := captured(t, tgt, id); strings.Contains(got, "MUST-NOT-ARRIVE") {
			t.Errorf("the payload reached %s: the guard read the wrong pane's option\n%s", id, got)
		}
	}
}

// dropDeleteRunner forwards everything except a standalone delete-buffer, so a
// test can tell `paste-buffer -d` apart from the deferred cleanup. Both exist on
// purpose — the defer cannot run in a hub that was killed, and -d cannot run if
// the batch never reaches the paste — but with both in place either one alone
// makes the buffer vanish, so neither is tested unless the other is removed.
type dropDeleteRunner struct{ inner tmux.InputRunner }

func (d dropDeleteRunner) RunInput(ctx context.Context, t tmux.Target, stdin []byte, args ...string) (tmux.Result, error) {
	if len(args) > 0 && args[0] == "delete-buffer" {
		return tmux.Result{}, nil
	}
	return d.inner.RunInput(ctx, t, stdin, args...)
}

func (d dropDeleteRunner) Run(ctx context.Context, t tmux.Target, args ...string) (tmux.Result, error) {
	return d.inner.RunInput(ctx, t, nil, args...)
}

func TestPasteDeletesTheBufferWithoutTheDeferredCleanup(t *testing.T) {
	tgt, ids := liveServer(t, 1)
	real := tmux.NewExec(10 * time.Second)
	dropped := dropDeleteRunner{inner: real.(tmux.InputRunner)}

	st := NewStamper(dropped, Instance("pd"))
	s := NewSender(dropped, st, Instance("pd"))
	if _, err := st.Stamp(context.Background(), tgt, ids[0]); err != nil {
		t.Fatalf("Stamp: %v", err)
	}
	if _, err := s.Send(context.Background(),
		Target{Host: "test", Tmux: tgt, PaneID: ids[0]}, "atomic cleanup payload"); err != nil {
		t.Fatalf("Send: %v", err)
	}

	out, _ := exec.Command("tmux", "-S", tgt.Socket, "list-buffers",
		"-F", "#{buffer_name}").Output()
	if strings.Contains(string(out), "tmux-hub-") {
		t.Errorf("with the deferred delete removed, -d did not clean up:\n%s", out)
	}
}

// The CONSEQUENCE of -p and -r cannot be reproduced against the `cat` pane these
// tests use: cat interprets neither bracketed paste nor CR, so removing both flags
// changes nothing observable, and the multi-line arrival test stayed green under
// that mutation. The consequence is measured in docs/design.md section 3 against a
// real readline prompt — without -p the first paragraph EXECUTES — and what a unit
// test can honestly assert is that the flags are still there.
func TestPasteCarriesBothBracketedPasteFlags(t *testing.T) {
	tgt, ids := liveServer(t, 1)
	rec := &recordingRunner{inner: tmux.NewExec(10 * time.Second).(tmux.InputRunner)}
	st := NewStamper(rec, Instance("bf"))
	s := NewSender(rec, st, Instance("bf"))
	if _, err := st.Stamp(context.Background(), tgt, ids[0]); err != nil {
		t.Fatalf("Stamp: %v", err)
	}
	if _, err := s.Send(context.Background(),
		Target{Host: "test", Tmux: tgt, PaneID: ids[0]}, "a\nb"); err != nil {
		t.Fatalf("Send: %v", err)
	}
	// Isolate the paste-buffer SEGMENT. The chain also contains `display -p`, so
	// searching the whole string for " -p " finds another command's flag and the
	// check passes with -p removed — measured, that is what the first version did.
	var paste string
	for _, c := range rec.calls {
		for _, a := range c {
			for _, seg := range strings.Split(a, ";") {
				seg = strings.TrimSpace(seg)
				if strings.HasPrefix(seg, "paste-buffer") {
					paste = seg
				}
			}
		}
	}
	if paste == "" {
		t.Fatal("no paste-buffer was issued at all")
	}
	for _, flag := range []string{"-d", "-p", "-r"} {
		if !strings.Contains(paste+" ", flag+" ") {
			t.Errorf("paste-buffer lost %s: %q", flag, paste)
		}
	}
}

type recordingRunner struct {
	inner tmux.InputRunner
	calls [][]string
}

func (r *recordingRunner) RunInput(ctx context.Context, t tmux.Target, stdin []byte, args ...string) (tmux.Result, error) {
	r.calls = append(r.calls, args)
	return r.inner.RunInput(ctx, t, stdin, args...)
}

func (r *recordingRunner) Run(ctx context.Context, t tmux.Target, args ...string) (tmux.Result, error) {
	r.calls = append(r.calls, args)
	return r.inner.RunInput(ctx, t, nil, args...)
}

// Two sends in quick succession must BOTH be witnessed. This is the case the
// activity observable cannot answer: window_activity has one-second resolution, so
// measured against tmux 3.2a six back-to-back sends advanced it only 2 times in 6
// while the text arrived 6 times in 6. A broadcast writes to several panes inside
// one second, which makes that the common case rather than a corner.
func TestRapidSuccessiveSendsAreBothWitnessed(t *testing.T) {
	s, st, tgt, ids := sender(t, Instance("rs"))
	ctx := context.Background()
	if _, err := st.Stamp(ctx, tgt, ids[0]); err != nil {
		t.Fatalf("Stamp: %v", err)
	}
	tg := Target{Host: "test", Tmux: tgt, PaneID: ids[0]}

	for i, text := range []string{"first rapid payload", "second rapid payload"} {
		res, err := s.Send(ctx, tg, text)
		if err != nil {
			t.Fatalf("Send %d: %v", i, err)
		}
		res = s.Witness(ctx, res, text)
		if res.Outcome != Delivered {
			t.Errorf("send %d: Outcome = %s (%s), want delivered — the text is on the pane",
				i, res.Outcome, res.Reason)
		}
	}
}

// A pane that echoes nothing is Unwitnessed rather than Delivered or Refused. That
// is the third outcome doing its job: `cat -v` with the terminal's echo off is the
// closest stand-in for a password prompt.
func TestANonEchoingPaneIsUnwitnessedNotDelivered(t *testing.T) {
	tgt, _ := liveServer(t, 1)
	r := tmux.NewExec(10 * time.Second)
	// A pane that consumes input and prints nothing.
	if out, err := exec.Command("tmux", "-S", tgt.Socket, "new-window", "-d", "-P",
		"-F", "#{pane_id}", "sh -c 'stty -echo; head -c 200 >/dev/null'").Output(); err == nil {
		quiet := strings.TrimSpace(string(out))
		st := NewStamper(r, Instance("ne"))
		s := NewSender(r.(tmux.InputRunner), st, Instance("ne"))
		if _, err := st.Stamp(context.Background(), tgt, quiet); err != nil {
			t.Fatalf("Stamp: %v", err)
		}
		time.Sleep(1200 * time.Millisecond)
		res, err := s.Send(context.Background(),
			Target{Host: "test", Tmux: tgt, PaneID: quiet}, "silent payload text")
		if err != nil {
			t.Fatalf("Send: %v", err)
		}
		res = s.Witness(context.Background(), res, "silent payload text")
		if res.Outcome == Refused {
			t.Errorf("a stamped pane was refused: %s", res.Reason)
		}
		if res.Outcome == Delivered && !strings.Contains(res.Reason, "activity") {
			t.Errorf("a non-echoing pane reported %s via %q — the screen cannot show it",
				res.Outcome, res.Reason)
		}
	} else {
		t.Skipf("could not make a non-echoing pane: %v", err)
	}
}

// splitWitness is tested directly because the screen observable masks it: if the
// marker parse breaks, activity reads 0 and the screen check still answers, so
// nothing goes red — measured. A pure test is the only place this can fail.
func TestSplitWitnessFindsTheMarkerAnywhere(t *testing.T) {
	act, screen := splitWitness("ACT 1786490349\nfirst line\nsecond line\n")
	if act != 1786490349 {
		t.Errorf("act = %d", act)
	}
	if strings.Contains(screen, "ACT") {
		t.Errorf("the marker leaked into the screen: %q", screen)
	}
	if !strings.Contains(screen, "first line") || !strings.Contains(screen, "second line") {
		t.Errorf("screen lost content: %q", screen)
	}

	// Keyed on the marker, not the position: a pane whose own first line looks
	// ACT-shaped must not shift the parse.
	act2, screen2 := splitWitness("ACTUALLY not a marker\nACT 42\ntail\n")
	if act2 != 42 {
		t.Errorf("act = %d, want 42 — the marker must be found by prefix, not by line 0", act2)
	}
	if !strings.Contains(screen2, "ACTUALLY not a marker") {
		t.Errorf("the pane's own line was eaten: %q", screen2)
	}

	// No marker at all: activity unknown, screen intact — so the screen observable
	// still works and the activity one simply abstains.
	act3, screen3 := splitWitness("just a screen\nno marker here\n")
	if act3 != 0 {
		t.Errorf("act = %d, want 0 when there is no marker", act3)
	}
	if !strings.Contains(screen3, "just a screen") {
		t.Errorf("screen = %q", screen3)
	}
}
```

- [ ] **Step 6: Calibrate on the negative pole**

The tests must fail when the guarantees are removed. Run each of these, confirm
red, then revert:

Twenty mutations, each of which must turn exactly one test red. **This was run, and
it is what produced Step 5** — three of the twenty were green the first time.

| mutation | test that must fail |
|---|---|
| the `if` loses its own `-t` | `TestGuardReadsTheTargetsOwnOptionNotTheCurrentPanes` |
| `Stamp` loses `-p` | `TestStampIsPaneScopedAndReadableBack` |
| `paste-buffer` loses `-d` | `TestPasteDeletesTheBufferWithoutTheDeferredCleanup` |
| `paste-buffer` loses `-p` or `-r` | `TestPasteCarriesBothBracketedPasteFlags` |
| `Send` returns `Delivered` itself | `TestSendAloneNeverClaimsDelivery` |
| the echoed token goes unchecked | `TestSendDeliversAndIsWitnessed` |
| `Validate` admits a non-pane `-t` | `TestValidateRefusesATargetThatIsNotAPaneID` |
| `Validate` admits `%` in a template | `TestValidateStillRefusesAPercentInATemplate` |
| the payload travels as argv (`set-buffer`) | `TestTrailingSemicolonSurvives` |
| identification keys on `comm` | `TestDoesNotKeyOnComm` |
| the bg daemon roles are admitted | `TestRefusesTheBackgroundDaemonRoles` |
| `Sweep` matches only our own prefix | `TestSweepRemovesEveryHubBufferAndNothingElse` |
| confirmation reverts to a target count | `TestEachClauseTriggersAlone` |
| the exited-agent clause is folded in | `TestTheExitedAgentCaseIsNotJustUnidentified` |
| history stores no outcome word | `TestOutcomeIsStoredAsAWord` |
| rotation keeps the oldest half | `TestRotationKeepsTheNewestAndDropsTheOldest` |
| the screen observable is removed from `Witness` | `TestRapidSuccessiveSendsAreBothWitnessed` |
| the activity observable is removed from `Witness` | `TestANonEchoingPaneIsUnwitnessedNotDelivered` |
| the `ACT` marker parse breaks | `TestSplitWitnessFindsTheMarkerAnywhere` |
| the walk skips the root process | `TestIdentifiesAgentWhenThePaneCommandIsClaudeItself` |
| `Composer.Backspace` goes byte-wise | `TestComposerBackspaceIsRuneWise` |
| `Selection` drops the host from its key | `TestSelectionHoldsPanesNotGroups` |

Drive it from a script that restores each file in a `finally:`, never by hand: a
120-second tool timeout in the middle of a hand-run sweep leaves a mutation in the
tree, and the next full run then fails for a reason nobody connects to the sweep.

**One redundancy that no test covers, recorded rather than papered over — and now
confirmed by running it.** Removing the echoed *pane id* check leaves every test
green, while removing the TOKEN check turns the suite red; the token check is
strictly stronger — a wrong pane cannot produce a matching random token, so it
catches everything the id check would. The id check stays because it costs nothing
and makes the failure message name the pane, but it is corroboration and not a
guarantee, and pretending otherwise would be the kind of comment that stops the
next reader checking.

- [ ] **Step 7: Commit**

```bash
gofmt -l . && go test -race ./... && git add internal/broadcast/send.go internal/broadcast/send_test.go && git commit -m "feat(broadcast): the guarded send, and three outcomes

The send checks the pane's token in the same invocation that writes, so there is
no window between deciding and delivering. Both the if and every sub-command
carry their own -t, which are two separate measured fail-opens: without the
sub-command's, a crossed pair delivered to the unstamped pane and confirmed OK
for it; without the if's, the guard read the option from the server's CURRENT
pane and pasted into an unstamped one, printing OK %1.

Send never returns delivered. The witness cannot be read in the invocation that
writes — window_activity tracks the pane's OUTPUT, and the process cannot have
answered while the batch that wrote to it is still running, measured identical
three times — so Send records activity_before and Witness reads again at 150ms.
Either observable suffices: activity advanced, or the text is on the pane. The
second covers window_activity's one-second resolution; a pane that legitimately
echoes nothing is what sent-unwitnessed is for.

Every test asserts on the PANE rather than on the confirmation, because the
confirmation is exactly what was measured lying: the hub printed OK %0 having
delivered nothing, and a test reading it would pass on a broken write path."
```

---

### Task 6: Buffer hygiene, swept at both ends

**Files:**
- Create: `internal/broadcast/sweep.go`
- Test: `internal/broadcast/sweep_test.go`

**Interfaces:**
- Produces:
  - `func Sweep(ctx, r tmux.Runner, t tmux.Target) (removed []string, err error)`

- [ ] **Step 1: Write the failing test**

Create `internal/broadcast/sweep_test.go`:

```go
package broadcast

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/DawnBreather/tmux-hub/internal/tmux"
)

// The sweep must remove ANOTHER instance's leftovers, because the case worth
// cleaning is a hub that crashed mid-send — and it must leave the user's own
// buffers strictly alone.
func TestSweepRemovesEveryHubBufferAndNothingElse(t *testing.T) {
	tgt, _ := liveServer(t, 1)
	r := tmux.NewExec(10 * time.Second)

	load := func(name, body string) {
		t.Helper()
		cmd := exec.Command("tmux", "-S", tgt.Socket, "load-buffer", "-b", name, "-")
		cmd.Stdin = strings.NewReader(body)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("load-buffer %s: %v: %s", name, err, out)
		}
	}
	load("tmux-hub-dead1-7", "a crashed hub's secret prompt")
	load("tmux-hub-ab12cd-1", "our own leftover")
	load("mine", "the user's own clipboard")

	removed, err := Sweep(context.Background(), r, tgt)
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if len(removed) != 2 {
		t.Errorf("Sweep removed %v, want both hub buffers", removed)
	}

	out, _ := exec.Command("tmux", "-S", tgt.Socket, "list-buffers",
		"-F", "#{buffer_name}").Output()
	if strings.Contains(string(out), "tmux-hub-") {
		t.Errorf("a hub buffer survived the sweep:\n%s", out)
	}
	if !strings.Contains(string(out), "mine") {
		t.Errorf("the sweep took the user's own buffer:\n%s", out)
	}
}

// A server with no buffers at all answers rc=1, which is not a failure.
func TestSweepOnAnEmptyServer(t *testing.T) {
	tgt, _ := liveServer(t, 1)
	removed, err := Sweep(context.Background(), tmux.NewExec(10*time.Second), tgt)
	if err != nil {
		t.Fatalf("Sweep on an empty server = %v, want nil", err)
	}
	if len(removed) != 0 {
		t.Errorf("Sweep removed %v from an empty server", removed)
	}
}
```

- [ ] **Step 2: Run it**

Run: `go test ./internal/broadcast/ -run Sweep -v`
Expected: FAIL — `undefined: Sweep`.

- [ ] **Step 3: Write the implementation**

Create `internal/broadcast/sweep.go`:

```go
package broadcast

import (
	"context"
	"strings"

	"github.com/DawnBreather/tmux-hub/internal/tmux"
)

// Sweep removes every hub paste buffer from a server, whichever instance left it.
//
// It runs at connect and at shutdown, and it is not tidiness. A batch that aborts
// skips its own cleanup — measured, one missing pane left
// `tmux-hub-2: 42 bytes: "secret prompt…"` as the MOST RECENT buffer, ahead of the
// user's own, so their next `prefix ]` in any session on that host pastes the hub's
// prompt. A vanished pane is an expected condition, so the leak is routine rather
// than exotic, and a crashed hub cannot clean up after itself at all.
//
// It matches every instance's prefix rather than only ours for exactly that
// reason, and it touches nothing else: a buffer the user named is theirs.
func Sweep(ctx context.Context, r tmux.Runner, t tmux.Target) ([]string, error) {
	res, err := r.Run(ctx, t, "list-buffers", "-F", "#{buffer_name}")
	if err != nil {
		return nil, err
	}
	// A server with no buffers answers rc=1. That is an empty list, not a failure.
	if res.RC != 0 {
		return nil, nil
	}

	prefix := strings.TrimSuffix(BufferGlob, "*")
	var removed []string
	for _, name := range strings.Split(res.Stdout, "\n") {
		name = strings.TrimSpace(name)
		if name == "" || !strings.HasPrefix(name, prefix) {
			continue
		}
		if _, derr := r.Run(ctx, t, "delete-buffer", "-b", name); derr != nil {
			return removed, derr
		}
		removed = append(removed, name)
	}
	return removed, nil
}
```

- [ ] **Step 4: Run the tests, then commit**

Run: `go test ./internal/broadcast/ -v`
Expected: PASS.

```bash
gofmt -l . && go test -race ./... && git add internal/broadcast/sweep.go internal/broadcast/sweep_test.go && git commit -m "feat(broadcast): sweep hub paste buffers at connect and shutdown

Not tidiness. A batch that aborts skips its own cleanup, and one missing pane
left 'tmux-hub-2: 42 bytes: \"secret prompt…\"' as the MOST RECENT buffer on that
server, ahead of the user's own — so their next prefix-] in any session pastes
the hub's prompt. A vanished pane is an expected condition, so that leak is
routine rather than exotic, and a hub that crashed cannot clean up after itself
at all.

The sweep therefore matches every instance's prefix and not only ours, and
touches nothing else: a buffer the user named is theirs. A server with no
buffers answers rc=1, which is an empty list rather than a failure."
```

---

### Task 7: Selection, and the input box

**Files:**
- Create: `internal/ui/selection.go`
- Create: `internal/ui/compose.go`
- Test: `internal/ui/selection_test.go`
- Modify: `internal/ui/model.go`, `internal/ui/render.go`

**Interfaces:**
- Produces:
  - `type Selection struct { … }` with `Toggle`, `Clear`, `Members`, `Has`, `Len`
  - `type SelectionKey struct { Host, PaneID string }`
  - `type Composer struct { … }` with `Insert`, `Backspace`, `Newline`, `Text`, `Clear`
  - keys: `space` toggles, `A` selects all visible, `C` clears, `i` composes, `Esc` leaves compose

- [ ] **Step 1: Write the failing test**

Create `internal/ui/selection_test.go`:

```go
package ui

import (
	"strings"
	"testing"
)

// Tags SELECT; a tag is never itself a target. So the selection is always a set
// of concrete panes, and expanding a tag adds its members rather than storing the
// tag — otherwise a pane that joined the tag after the fact becomes a target
// nobody chose.
func TestSelectionHoldsPanesNotGroups(t *testing.T) {
	var s Selection
	s.Toggle(SelectionKey{"local", "%0"})
	s.Toggle(SelectionKey{"nuc", "%3"})
	if s.Len() != 2 {
		t.Fatalf("Len = %d, want 2", s.Len())
	}
	if !s.Has(SelectionKey{"nuc", "%3"}) {
		t.Error("Has lost a member")
	}
	// A pane id is only unique within a server, so the host must be part of the key.
	s.Toggle(SelectionKey{"nuc", "%0"})
	if !s.Has(SelectionKey{"local", "%0"}) || !s.Has(SelectionKey{"nuc", "%0"}) {
		t.Error("two hosts' %0 collided — the key must include the host")
	}
}

func TestToggleIsIdempotentPairwise(t *testing.T) {
	var s Selection
	k := SelectionKey{"local", "%1"}
	s.Toggle(k)
	s.Toggle(k)
	if s.Has(k) || s.Len() != 0 {
		t.Errorf("Toggle twice left %d members", s.Len())
	}
}

func TestClearEmptiesEverything(t *testing.T) {
	var s Selection
	s.Toggle(SelectionKey{"a", "%0"})
	s.Toggle(SelectionKey{"b", "%1"})
	s.Clear()
	if s.Len() != 0 {
		t.Errorf("Clear left %d members", s.Len())
	}
}

// Members must be in a stable order, or the confirmation dialog reshuffles under
// the user between one look and the next.
func TestMembersAreOrdered(t *testing.T) {
	var s Selection
	for _, k := range []SelectionKey{{"nuc", "%9"}, {"local", "%2"}, {"nuc", "%1"}} {
		s.Toggle(k)
	}
	first := s.Members()
	for i := 0; i < 8; i++ {
		got := s.Members()
		for j := range got {
			if got[j] != first[j] {
				t.Fatalf("Members reordered between calls: %v vs %v", first, got)
			}
		}
	}
}

// A pane that vanished must leave the selection, or it stays a target forever and
// every send has to ask about it.
func TestPruneDropsVanishedPanes(t *testing.T) {
	var s Selection
	s.Toggle(SelectionKey{"local", "%0"})
	s.Toggle(SelectionKey{"local", "%1"})
	s.Prune(func(k SelectionKey) bool { return k.PaneID == "%0" })
	if s.Len() != 1 || !s.Has(SelectionKey{"local", "%0"}) {
		t.Errorf("Prune kept %v", s.Members())
	}
}

// The composer must keep a newline as text. This is where the measured accident
// lives: a newline that reaches tmux as a keypress submits the first paragraph.
func TestComposerKeepsNewlinesAsText(t *testing.T) {
	var c Composer
	c.Insert("line one")
	c.Newline()
	c.Insert("line two")
	if got := c.Text(); got != "line one\nline two" {
		t.Errorf("Text() = %q", got)
	}
	if strings.Contains(c.Text(), "\r") {
		t.Error("a CR got in; -r on paste-buffer exists to stop exactly that")
	}
}

func TestComposerBackspaceAndClear(t *testing.T) {
	var c Composer
	c.Insert("abc")
	c.Backspace()
	if c.Text() != "ab" {
		t.Errorf("Text() = %q", c.Text())
	}
	c.Backspace()
	c.Backspace()
	c.Backspace() // one too many
	if c.Text() != "" {
		t.Errorf("Text() = %q, want empty", c.Text())
	}
	c.Insert("x")
	c.Clear()
	if c.Text() != "" || !c.Empty() {
		t.Error("Clear left something behind")
	}
}

// Backspace must not split a multi-byte character, which would send invalid UTF-8
// into someone's prompt.
func TestComposerBackspaceIsRuneWise(t *testing.T) {
	var c Composer
	c.Insert("привет")
	c.Backspace()
	if c.Text() != "приве" {
		t.Errorf("Text() = %q, want %q", c.Text(), "приве")
	}
}
```

- [ ] **Step 2: Run it**

Run: `go test ./internal/ui/ -run 'Selection|Toggle|Clear|Members|Prune|Composer' -v`
Expected: FAIL — `undefined: Selection`.

- [ ] **Step 3: Write the selection**

Create `internal/ui/selection.go`:

```go
package ui

import "sort"

// SelectionKey names one pane. The host is part of the key because a pane id is
// unique only within a server: two hosts both have a %0, and merging them would
// send to whichever the map happened to hold.
type SelectionKey struct {
	Host   string
	PaneID string
}

// Selection is the set of panes the user has explicitly chosen.
//
// It holds PANES and never groups. Tags select; a tag is never itself a target —
// expanding a tag adds its members at the moment of selection, so a pane that
// joins the tag afterwards does not silently become a target nobody chose.
type Selection struct {
	members map[SelectionKey]bool
	order   []SelectionKey // insertion order, so the confirmation list is stable
}

func (s *Selection) Toggle(k SelectionKey) {
	if s.members == nil {
		s.members = map[SelectionKey]bool{}
	}
	if s.members[k] {
		delete(s.members, k)
		for i, o := range s.order {
			if o == k {
				s.order = append(s.order[:i], s.order[i+1:]...)
				break
			}
		}
		return
	}
	s.members[k] = true
	s.order = append(s.order, k)
}

func (s *Selection) Has(k SelectionKey) bool { return s.members[k] }
func (s *Selection) Len() int                { return len(s.members) }

func (s *Selection) Clear() {
	s.members = nil
	s.order = nil
}

// Members returns the selection in a stable order. The confirmation dialog lists
// them, and a list that reshuffles between one look and the next is a list nobody
// reads.
func (s *Selection) Members() []SelectionKey {
	out := make([]SelectionKey, 0, len(s.members))
	for _, k := range s.order {
		if s.members[k] {
			out = append(out, k)
		}
	}
	// Insertion order is the primary sort; anything the order slice lost is
	// appended deterministically rather than dropped.
	if len(out) < len(s.members) {
		var extra []SelectionKey
		seen := map[SelectionKey]bool{}
		for _, k := range out {
			seen[k] = true
		}
		for k := range s.members {
			if !seen[k] {
				extra = append(extra, k)
			}
		}
		sort.Slice(extra, func(i, j int) bool {
			if extra[i].Host != extra[j].Host {
				return extra[i].Host < extra[j].Host
			}
			return extra[i].PaneID < extra[j].PaneID
		})
		out = append(out, extra...)
	}
	return out
}

// Prune drops every member the predicate rejects. A pane that vanished must leave
// the selection: otherwise it stays a target forever and every send has to ask
// about it, which trains the user to confirm without reading.
func (s *Selection) Prune(alive func(SelectionKey) bool) {
	for k := range s.members {
		if !alive(k) {
			delete(s.members, k)
		}
	}
	kept := s.order[:0]
	for _, k := range s.order {
		if s.members[k] {
			kept = append(kept, k)
		}
	}
	s.order = kept
}
```

- [ ] **Step 4: Write the composer**

Create `internal/ui/compose.go`:

```go
package ui

import "strings"

// Composer is the input box. It holds text and nothing else — no keys, no
// submission.
//
// That separation is the point. A newline here is a CHARACTER in a payload that
// will travel on stdin; a newline that reaches tmux as a keypress submits the
// first paragraph of the prompt, which is the measured accident that made
// `send-keys -l` unusable for multi-line text.
type Composer struct {
	runes []rune
}

func (c *Composer) Insert(s string) { c.runes = append(c.runes, []rune(s)...) }

// Newline appends a literal LF. Never a CR: `paste-buffer -r` exists to stop LF
// becoming CR on the way out, and putting one in here would defeat it.
func (c *Composer) Newline() { c.runes = append(c.runes, '\n') }

// Backspace removes one RUNE, not one byte. A byte-wise delete would split a
// multi-byte character and send invalid UTF-8 into someone's prompt.
func (c *Composer) Backspace() {
	if len(c.runes) > 0 {
		c.runes = c.runes[:len(c.runes)-1]
	}
}

func (c *Composer) Clear()       { c.runes = nil }
func (c *Composer) Text() string { return string(c.runes) }
func (c *Composer) Empty() bool  { return len(strings.TrimSpace(string(c.runes))) == 0 }
```

- [ ] **Step 5: Wire the keys into the model**

**First, resolve a duplicate concept.** `model` already has
`marked map[string]bool` keyed by `MarkKey(pane)`, toggled by `space`, and passed
to `Render` — that *is* the selection, and it already decides which panes get a
full-screen capture. Adding `Selection` beside it would give the hub two answers to
"what is selected". So `Selection` **replaces** `marked`, and `MarkKey` stays as the
render-side key.

Change the struct and the key handler in `internal/ui/model.go`:

```go
// mode is what the keyboard means right now. Compose is a separate mode rather
// than a focused widget because in compose EVERY rune is text — including `q`,
// which in browse mode quits. A dashboard that quits when someone types "quit the
// server" into a prompt is worse than one with modes.
type uiMode int

const (
	modeBrowse uiMode = iota
	modeCompose
	modeConfirm
)

type model struct {
	poller *hub.Poller
	reg    *registry.Registry

	hosts  []hub.Host
	panes  []registry.Pane
	cursor int
	sel    Selection // replaces `marked`: one answer to "what is selected"

	mode     uiMode
	composer Composer
	pending  []broadcast.Reason // why the confirmation is up, empty when it is not

	sender  *broadcast.Sender
	stamper *broadcast.Stamper
	hist    *history.Log

	width, height int
	log           *hub.StateLog
	note          string
	ctx           context.Context
}

// selKey turns a registry pane into a selection key. Two hosts both have a %0, so
// the host is part of it.
func selKey(p registry.Pane) SelectionKey {
	return SelectionKey{Host: p.Host, PaneID: p.PaneID}
}

// markedSet adapts the selection to what Render already expects, so the renderer
// does not have to change.
func (m model) markedSet() map[string]bool {
	out := make(map[string]bool, m.sel.Len())
	for _, p := range m.panes {
		if m.sel.Has(selKey(p)) {
			out[MarkKey(p)] = true
		}
	}
	return out
}
```

Then the key handler. Replace the whole `case tea.KeyMsg:` block's inner switch
with one that dispatches on mode first:

```go
	case tea.KeyMsg:
		switch m.mode {
		case modeCompose:
			return m.composeKey(msg)
		case modeConfirm:
			return m.confirmKey(msg)
		}
		switch msg.String() {
		case "q", "ctrl+c":
			return m, tea.Quit
		case "j", "down":
			m.note = ""
			if m.cursor < len(m.panes)-1 {
				m.cursor++
			}
		case "k", "up":
			m.note = ""
			if m.cursor > 0 {
				m.cursor--
			}
		case "a":
			return m.attach()
		case " ":
			if m.cursor < len(m.panes) {
				m.sel.Toggle(selKey(m.panes[m.cursor]))
			}
		case "A":
			// VISIBLE panes only. Selecting something off-screen would break §7's
			// rule that a target is always a tile the user can see, and the whole
			// point of the rule is that nobody sends into a pane they are not
			// looking at.
			for _, p := range VisiblePanes(m.panes, m.width, m.height, m.cursor) {
				if !m.sel.Has(selKey(p)) {
					m.sel.Toggle(selKey(p))
				}
			}
		case "C":
			m.sel.Clear()
			m.note = "selection cleared"
		case "i":
			if m.sel.Len() == 0 {
				m.note = "select a pane with space first — a prompt needs a target"
				return m, nil
			}
			m.mode, m.note = modeCompose, ""
		case "!":
			// Interrupt is guarded by the same confirmation rule as a send: C-c into
			// the wrong pane kills whatever is actually running there.
			m.pending = broadcast.Needed(m.targetStates())
			if len(m.pending) == 0 {
				return m, m.interrupt("C-c")
			}
			m.mode = modeConfirm
		case "h":
			return m.openHistory()
		}
	}
```

And the two mode handlers, plus the send:

```go
// composeKey turns keystrokes into text. Enter LEAVES compose and runs the
// confirmation rule; it never sends directly, because §7 requires a fresh check of
// every target at the moment of sending rather than at the moment of selecting.
func (m model) composeKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		// The text is KEPT. Losing a half-written prompt to a stray Esc is the kind
		// of thing that makes people stop using a tool.
		m.mode, m.note = modeBrowse, "draft kept — press i to go back to it"
		return m, nil
	case "enter":
		if m.composer.Empty() {
			m.note = "nothing to send"
			return m, nil
		}
		m.mode = modeBrowse
		m.pending = broadcast.Needed(m.targetStates())
		if len(m.pending) == 0 {
			return m, m.send(m.composer.Text())
		}
		m.mode = modeConfirm
		return m, nil
	case "alt+enter", "ctrl+j":
		m.composer.Newline()
		return m, nil
	case "backspace":
		m.composer.Backspace()
		return m, nil
	}
	// Every other key is text, which is why compose is a mode.
	if msg.Type == tea.KeyRunes {
		m.composer.Insert(string(msg.Runes))
	} else if msg.String() == " " {
		m.composer.Insert(" ")
	}
	return m, nil
}

// confirmKey requires a SECOND enter. The reasons are on screen; anything other
// than enter cancels, because the safe default for "I do not understand this
// dialog" is to do nothing.
func (m model) confirmKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if msg.String() != "enter" {
		m.mode, m.pending = modeBrowse, nil
		m.note = "cancelled"
		return m, nil
	}
	m.mode, m.pending = modeBrowse, nil
	if m.composer.Empty() {
		return m, m.interrupt("C-c")
	}
	return m, m.send(m.composer.Text())
}

// send writes to every selected pane, then witnesses each one and records the
// outcome. It runs as a tea.Cmd so the UI never blocks on a remote round trip.
func (m model) send(text string) tea.Cmd {
	targets := m.targets()
	hist, sender := m.hist, m.sender
	ctx := m.ctx
	return func() tea.Msg {
		results := make([]broadcast.Result, 0, len(targets))
		for _, tg := range targets {
			res, err := sender.Send(ctx, tg, text)
			if err != nil {
				res.Outcome, res.Reason = broadcast.Refused, err.Error()
			} else {
				res = sender.Witness(ctx, res, text)
			}
			results = append(results, res)
			if hist != nil {
				_ = hist.Append(history.Entry{
					At: time.Now(), Host: tg.Host, PaneID: tg.PaneID,
					Text: text, Outcome: string(res.Outcome), Reason: res.Reason,
					Token: res.Token,
				})
			}
		}
		return sentMsg{results: results}
	}
}

func (m model) interrupt(key string) tea.Cmd {
	targets := m.targets()
	sender, ctx := m.sender, m.ctx
	return func() tea.Msg {
		var results []broadcast.Result
		for _, tg := range targets {
			r := broadcast.Result{Target: tg, Outcome: broadcast.Delivered, Reason: key}
			if err := sender.Interrupt(ctx, tg, key); err != nil {
				r.Outcome, r.Reason = broadcast.Refused, err.Error()
			}
			results = append(results, r)
		}
		return sentMsg{results: results}
	}
}

// sentMsg carries the per-target outcomes back to the UI.
type sentMsg struct{ results []broadcast.Result }
```

Handle it in `Update`, and clear the draft only on a witnessed delivery — a refused
send must not silently eat the text the user typed:

```go
	case sentMsg:
		var delivered, unwitnessed, refused int
		for _, r := range msg.results {
			switch r.Outcome {
			case broadcast.Delivered:
				delivered++
			case broadcast.Unwitnessed:
				unwitnessed++
			default:
				refused++
			}
		}
		m.note = summarise(msg.results)
		if refused == 0 && unwitnessed == 0 {
			// Only a clean run clears the draft. Otherwise the text stays, because
			// retyping a prompt the tool failed to deliver is the tool's fault.
			m.composer.Clear()
		}
		return m, m.poll()
```

Finally, prune every tick so a vanished pane stops being a target — in the
`tickMsg` case, right after `m.panes = msg.panes`:

```go
		alive := make(map[SelectionKey]bool, len(m.panes))
		for _, p := range m.panes {
			alive[selKey(p)] = true
		}
		m.sel.Prune(func(k SelectionKey) bool { return alive[k] })
```

and change `View` to `Render(m.panes, m.hosts, m.width, m.height, m.cursor,
m.markedSet(), m.note)` plus the compose and confirm overlays.

- [ ] **Step 6: Write the tests for the mode machine**

The parts worth testing here are pure, and one of them is a real hazard:

```go
// In compose mode every rune is TEXT. `q` quits in browse mode, and a dashboard
// that exits when someone types "quit the stuck server" into a prompt has lost the
// user's work for a keystroke they did not mean as a command.
func TestQDoesNotQuitInComposeMode(t *testing.T) {
	m := model{mode: modeCompose}
	got, cmd := m.composeKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")})
	if cmd != nil {
		t.Error("compose mode issued a command for an ordinary rune")
	}
	if text := got.(model).composer.Text(); text != "q" {
		t.Errorf("composer holds %q, want the rune as text", text)
	}
}

// Esc keeps the draft. Losing a half-written prompt is what makes people stop
// trusting an input box.
func TestEscapeKeepsTheDraft(t *testing.T) {
	m := model{mode: modeCompose}
	m.composer.Insert("half a prompt")
	got, _ := m.composeKey(tea.KeyMsg{Type: tea.KeyEsc})
	after := got.(model)
	if after.mode != modeBrowse {
		t.Error("esc did not leave compose")
	}
	if after.composer.Text() != "half a prompt" {
		t.Errorf("the draft was lost: %q", after.composer.Text())
	}
}

// Anything but enter cancels a confirmation: the safe answer to a dialog nobody
// understands is to do nothing.
func TestOnlyEnterConfirms(t *testing.T) {
	for _, key := range []tea.KeyMsg{
		{Type: tea.KeyRunes, Runes: []rune("y")},
		{Type: tea.KeyEsc},
		{Type: tea.KeyRunes, Runes: []rune(" ")},
	} {
		m := model{mode: modeConfirm, pending: []broadcast.Reason{broadcast.ReasonMultiple}}
		got, cmd := m.confirmKey(key)
		if cmd != nil {
			t.Errorf("%v sent something", key)
		}
		if got.(model).mode != modeBrowse {
			t.Errorf("%v did not dismiss the dialog", key)
		}
	}
}

// A refused send must not eat the text.
func TestARefusedSendKeepsTheDraft(t *testing.T) {
	m := model{mode: modeBrowse}
	m.composer.Insert("please retry this")
	got, _ := m.Update(sentMsg{results: []broadcast.Result{
		{Outcome: broadcast.Refused, Reason: "the pane is gone"},
	}})
	if text := got.(model).composer.Text(); text != "please retry this" {
		t.Errorf("the draft was cleared after a refusal: %q", text)
	}
}
```

Run: `go test ./internal/ui/ -v`
Expected: PASS. `VisiblePanes` and `summarise` are small helpers to add beside
`renderTiles`; `VisiblePanes` returns exactly the panes `renderTiles` drew, so
`A` can never select something off-screen.

- [ ] **Step 7: Run and commit**

Run: `go test -race ./internal/ui/ -v && go build ./...`

```bash
gofmt -l . && go test -race ./... && git add internal/ui/ && git commit -m "feat(ui): selection, the input box, and a mode machine

The selection holds PANES and never groups. Tags select; a tag is never itself a
target, so expanding one adds its members at the moment of selection and a pane
that joins the tag afterwards does not silently become a target nobody chose.
The host is part of the key because a pane id is unique only within a server —
two hosts both have a %0.

The composer holds text and nothing else. A newline here is a character in a
payload that travels on stdin; a newline that reaches tmux as a keypress submits
the first paragraph, which is the measured accident that made send-keys -l
unusable for multi-line prompts. Backspace is rune-wise so it cannot split a
multi-byte character and send invalid UTF-8 into someone's prompt.

A vanished pane is pruned every tick: left in, it stays a target forever and
every send has to ask about it, which trains the user to confirm without
reading."
```

---

### Task 8: When to ask, computed rather than guessed

**Files:**
- Create: `internal/broadcast/confirm.go`
- Test: `internal/broadcast/confirm_test.go`

**Interfaces:**
- Produces:
  - `type TargetState struct { … }`
  - `type Reason string` and `func Needed(ts []TargetState) []Reason`

- [ ] **Step 1: Write the failing test**

Create `internal/broadcast/confirm_test.go`:

```go
package broadcast

import "testing"

func fresh() TargetState {
	return TargetState{
		PaneID: "%1", Host: "local",
		IdentifiedNow: true, IdentifiedAtSelection: true,
		SessionAtSelection: "$0", SessionNow: "$0",
		WindowAtSelection: "@0", WindowNow: "@0",
		EpochAtSelection: "1:99", EpochNow: "1:99",
		LastOutcome: Delivered, FromHistory: false, Bracketed: true,
	}
}

// "> 1 target" turned out to be neither necessary nor sufficient: every dangerous
// SINGLE-target send is one where something changed since selection, and the
// count rule fires on the safe common case of two freshly identified agents.
func TestOneFreshTargetSendsWithoutAsking(t *testing.T) {
	if got := Needed([]TargetState{fresh()}); len(got) != 0 {
		t.Errorf("Needed = %v, want nothing for a fresh single target", got)
	}
}

func TestTwoTargetsAlwaysAsk(t *testing.T) {
	got := Needed([]TargetState{fresh(), fresh()})
	if !hasReason(got, ReasonMultiple) {
		t.Errorf("Needed = %v, want ReasonMultiple", got)
	}
}

// Each clause on its own must trigger, or a disjunction with a broken arm looks
// exactly like a working one.
func TestEachClauseTriggersAlone(t *testing.T) {
	cases := []struct {
		name string
		want Reason
		mut  func(*TargetState)
	}{
		{"never identified", ReasonUnidentified, func(s *TargetState) {
			// BOTH false. With IdentifiedAtSelection left true this is the
			// exited-agent case instead, which is reported separately — so the
			// clause under test would never be reached and the case would pass
			// against a broken implementation.
			s.IdentifiedAtSelection, s.IdentifiedNow = false, false
		}},
		{"agent exited", ReasonAgentGone, func(s *TargetState) {
			s.IdentifiedAtSelection, s.IdentifiedNow = true, false
		}},
		{"moved session", ReasonMoved, func(s *TargetState) { s.SessionNow = "$7" }},
		{"moved window", ReasonMoved, func(s *TargetState) { s.WindowNow = "@7" }},
		{"server restarted", ReasonEpochChanged, func(s *TargetState) { s.EpochNow = "2:100" }},
		{"last send unwitnessed", ReasonLastUnwitnessed, func(s *TargetState) {
			s.LastOutcome = Unwitnessed
		}},
		{"from history", ReasonFromHistory, func(s *TargetState) { s.FromHistory = true }},
		{"no bracketed paste", ReasonNoBracketedPaste, func(s *TargetState) { s.Bracketed = false }},
	}
	for _, c := range cases {
		s := fresh()
		c.mut(&s)
		got := Needed([]TargetState{s})
		if !hasReason(got, c.want) {
			t.Errorf("%s: Needed = %v, want %v", c.name, got, c.want)
		}
	}
}

// The exited-agent case is the one a count rule misses entirely, and it is the
// one where the pane is now a SHELL — the measured 'bash: please: command not
// found' accident.
func TestTheExitedAgentCaseIsNotJustUnidentified(t *testing.T) {
	s := fresh()
	s.IdentifiedAtSelection, s.IdentifiedNow = true, false
	got := Needed([]TargetState{s})
	if !hasReason(got, ReasonAgentGone) {
		t.Fatalf("Needed = %v, want ReasonAgentGone specifically", got)
	}
}

// Reasons must be human sentences: a dialog that says "reason 3" is a dialog
// people dismiss.
func TestReasonsReadAsSentences(t *testing.T) {
	for _, r := range []Reason{ReasonMultiple, ReasonUnidentified, ReasonAgentGone,
		ReasonMoved, ReasonEpochChanged, ReasonLastUnwitnessed, ReasonFromHistory,
		ReasonNoBracketedPaste} {
		if len(r) < 12 {
			t.Errorf("reason %q is too terse to be read", r)
		}
	}
}

func hasReason(got []Reason, want Reason) bool {
	for _, r := range got {
		if r == want {
			return true
		}
	}
	return false
}
```

- [ ] **Step 2: Run it**

Run: `go test ./internal/broadcast/ -run 'Confirm|Needed|Clause|Fresh|Exited|Reasons' -v`
Expected: FAIL — `undefined: TargetState`.

- [ ] **Step 3: Write the implementation**

Create `internal/broadcast/confirm.go`:

```go
package broadcast

// TargetState is everything the confirmation rule looks at. Every field comes
// from the two poll snapshots the registry already holds, so asking the question
// costs no extra tmux read.
type TargetState struct {
	Host   string
	PaneID string

	IdentifiedNow         bool
	IdentifiedAtSelection bool

	SessionAtSelection string
	SessionNow         string
	WindowAtSelection  string
	WindowNow          string

	EpochAtSelection string
	EpochNow         string

	LastOutcome Outcome
	FromHistory bool

	// Bracketed is #{bracket_paste_flag} for the target, straight off the delta.
	// Measured: `less` reports 0 and turned a pasted three-line prompt into
	// KEYSTROKES, opening its help screen — a payload containing `q` would have
	// quit it and `!cmd` would have run a shell command. bash, vim and the python
	// REPL report 1 and took the same payload as inert text.
	Bracketed bool
}

// Reason is why the hub is asking, in words the user can act on.
type Reason string

const (
	ReasonMultiple        Reason = "more than one target is selected"
	ReasonUnidentified    Reason = "this pane cannot be identified as an agent"
	ReasonAgentGone       Reason = "the agent that was here has exited — this pane is now a shell"
	ReasonMoved           Reason = "this pane changed session or window since you selected it"
	ReasonEpochChanged    Reason = "the tmux server restarted, so pane ids may name different panes"
	ReasonLastUnwitnessed Reason = "the previous send to this pane was never witnessed arriving"
	ReasonFromHistory     Reason = "this came from the history view rather than the input box"
	ReasonNoBracketedPaste Reason = "this pane does not accept pasted text — it will read the prompt as keypresses"
)

// Needed returns the reasons to confirm, empty when the send may go straight out.
//
// It is a disjunction rather than a target count, because "> 1 target" is neither
// necessary nor sufficient: every dangerous SINGLE-target send is one where
// something changed since selection, and the count rule fires on the safe common
// case of two freshly identified agents. A fresh single target sends immediately,
// so the common case does not pay for the rare one — and "fresh" is now checked
// rather than assumed.
//
// Not disableable. The one clause a user would want to switch off is the one that
// catches the exited-agent case, where the pane is now a shell and the prompt
// becomes a command line.
func Needed(ts []TargetState) []Reason {
	var out []Reason
	seen := map[Reason]bool{}
	add := func(r Reason) {
		if !seen[r] {
			seen[r] = true
			out = append(out, r)
		}
	}

	if len(ts) > 1 {
		add(ReasonMultiple)
	}
	for _, s := range ts {
		switch {
		case s.IdentifiedAtSelection && !s.IdentifiedNow:
			// Reported specifically rather than as "unidentified": this is the
			// case where a prompt lands at a shell prompt, and the user needs to
			// know it USED to be an agent.
			add(ReasonAgentGone)
		case !s.IdentifiedNow:
			add(ReasonUnidentified)
		}
		if s.SessionNow != s.SessionAtSelection || s.WindowNow != s.WindowAtSelection {
			add(ReasonMoved)
		}
		if s.EpochNow != s.EpochAtSelection {
			add(ReasonEpochChanged)
		}
		if s.LastOutcome == Unwitnessed || s.LastOutcome == Refused {
			add(ReasonLastUnwitnessed)
		}
		if s.FromHistory {
			add(ReasonFromHistory)
		}
		// A 0 is a reason to ask, not a refusal: `cat` also reports 0 and merely
		// echoes, so refusing outright would block a legitimate target. A 1 is not a
		// guarantee either — vim in a modal swap-file dialog consumed the paste — so
		// this clause narrows the risk rather than removing it.
		if !s.Bracketed {
			add(ReasonNoBracketedPaste)
		}
	}
	return out
}
```

- [ ] **Step 4: Run and commit**

Run: `go test ./internal/broadcast/ -v`

```bash
gofmt -l . && go test -race ./... && git add internal/broadcast/confirm.go internal/broadcast/confirm_test.go && git commit -m "feat(broadcast): confirmation triggers on state change, not on target count

'> 1 target' is neither necessary nor sufficient. Every dangerous SINGLE-target
send is one where something changed since selection, and the count rule fires on
the safe common case of two freshly identified agents — so it asks when it
should not and stays quiet when it should ask.

The rule is a disjunction over state the registry already holds, so it costs no
extra tmux read, and a fresh single target still sends immediately: the common
case does not pay for the rare one, and 'fresh' is now checked rather than
assumed.

The exited-agent clause is reported separately from 'unidentified' because the
user needs to know the pane USED to be an agent — that is the case where a
prompt lands at a shell prompt and becomes a command line, which is exactly how
'bash: please: command not found' happened."
```

---

### Task 9: History that can be read and re-sent

**Files:**
- Create: `internal/history/history.go`
- Test: `internal/history/history_test.go`
- Modify: `internal/ui/model.go` (a history view on `h`)

**Interfaces:**
- Produces:
  - `type Entry struct { … }`
  - `type Log struct { … }`, `func Open(path string, maxBytes int64) (*Log, error)`
  - `func (l *Log) Append(e Entry) error`, `func (l *Log) Recent(n int) ([]Entry, error)`, `func (l *Log) Close() error`
  - `func DefaultPath() string`

- [ ] **Step 1: Write the failing test**

Create `internal/history/history_test.go`:

```go
package history

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func entry(text string, outcome string) Entry {
	return Entry{
		At: time.Unix(1786487832, 0).UTC(), Host: "nuc", PaneID: "%3",
		SessionName: "work", WindowName: "agent", Text: text,
		Outcome: outcome, Token: "abc123",
	}
}

func TestAppendAndReadBack(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.jsonl")
	l, err := Open(path, 1<<20)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer l.Close()

	for i, txt := range []string{"first prompt", "second prompt", "third prompt"} {
		e := entry(txt, "delivered")
		if i == 1 {
			e.Outcome = "sent-unwitnessed"
		}
		if err := l.Append(e); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}
	got, err := l.Recent(10)
	if err != nil {
		t.Fatalf("Recent: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("Recent returned %d entries", len(got))
	}
	// Newest first: a history view is read from the top.
	if got[0].Text != "third prompt" {
		t.Errorf("Recent[0] = %q, want the newest", got[0].Text)
	}
	if got[1].Outcome != "sent-unwitnessed" {
		t.Errorf("the outcome word was not preserved: %q", got[1].Outcome)
	}
}

// The outcome is one of three words, never a boolean. A log that says `ok: true`
// cannot distinguish a delivery from a confirmation that fired over nothing.
func TestOutcomeIsStoredAsAWord(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.jsonl")
	l, _ := Open(path, 1<<20)
	defer l.Close()
	for _, w := range []string{"delivered", "sent-unwitnessed", "refused"} {
		if err := l.Append(entry("x", w)); err != nil {
			t.Fatalf("Append(%s): %v", w, err)
		}
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range []string{"delivered", "sent-unwitnessed", "refused"} {
		if !strings.Contains(string(raw), w) {
			t.Errorf("%q is not in the file", w)
		}
	}
	if strings.Contains(string(raw), `"ok":`) {
		t.Error("a boolean crept in")
	}
}

// A multi-line prompt must survive as one entry — JSONL means the newline is
// escaped, and a reader that split on raw newlines would tear the entry apart.
func TestMultiLineTextStaysOneEntry(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.jsonl")
	l, _ := Open(path, 1<<20)
	defer l.Close()
	text := "line one\nline two\nline three"
	if err := l.Append(entry(text, "delivered")); err != nil {
		t.Fatalf("Append: %v", err)
	}
	got, err := l.Recent(5)
	if err != nil {
		t.Fatalf("Recent: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("a multi-line prompt became %d entries", len(got))
	}
	if got[0].Text != text {
		t.Errorf("Text = %q, want %q", got[0].Text, text)
	}
}

// Rotation must bound the file and keep the NEWEST entries, which are the ones a
// re-send uses. Asserting only that the last-written entry is still there does not
// test that: by the end of a long loop the file is small again, so the final append
// triggers no rotation at all and the assertion holds whichever half rotation keeps
// — measured, the mutation that keeps the OLDEST half left that version green.
//
// So the assertion is on the SPAN: the newest entry present, the oldest gone.
func TestRotationKeepsTheNewestAndDropsTheOldest(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.jsonl")
	l, err := Open(path, 4096) // small on purpose, so rotation fires repeatedly
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer l.Close()

	const n = 400
	for i := 0; i < n; i++ {
		e := entry(fmt.Sprintf("entry-%04d %s", i, strings.Repeat("x", 48)), "delivered")
		if err := l.Append(e); err != nil {
			t.Fatalf("Append %d: %v", i, err)
		}
	}

	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Size() > 4*4096 {
		t.Errorf("the file grew to %d bytes despite a 4096 limit", fi.Size())
	}

	got, err := l.Recent(10000)
	if err != nil {
		t.Fatalf("Recent: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("rotation emptied the log")
	}
	if len(got) >= n {
		t.Errorf("rotation never fired: %d entries survived out of %d", len(got), n)
	}
	var texts []string
	for _, e := range got {
		texts = append(texts, e.Text)
	}
	joined := strings.Join(texts, "\n")
	if !strings.Contains(joined, fmt.Sprintf("entry-%04d", n-1)) {
		t.Errorf("rotation lost the NEWEST entry; newest kept is %q", texts[0])
	}
	if strings.Contains(joined, "entry-0000") {
		t.Error("rotation kept the OLDEST entry, so it is discarding the wrong half")
	}
}

// A corrupt line — a hub killed mid-write — must not make the whole log
// unreadable, or one bad shutdown costs the user their history.
func TestACorruptLineIsSkipped(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.jsonl")
	l, _ := Open(path, 1<<20)
	if err := l.Append(entry("good one", "delivered")); err != nil {
		t.Fatal(err)
	}
	l.Close()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString("{\"At\":\"broken\n")
	f.Close()

	l2, err := Open(path, 1<<20)
	if err != nil {
		t.Fatalf("Open on a log with a torn line: %v", err)
	}
	defer l2.Close()
	got, err := l2.Recent(10)
	if err != nil {
		t.Fatalf("Recent: %v", err)
	}
	if len(got) != 1 || got[0].Text != "good one" {
		t.Errorf("got %+v, want the one intact entry", got)
	}
}

func TestRecentOnAMissingFile(t *testing.T) {
	l, err := Open(filepath.Join(t.TempDir(), "sub", "history.jsonl"), 1<<20)
	if err != nil {
		t.Fatalf("Open must create its directory: %v", err)
	}
	defer l.Close()
	got, err := l.Recent(5)
	if err != nil {
		t.Fatalf("Recent: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %+v from a fresh log", got)
	}
}
```

- [ ] **Step 2: Run it**

Run: `go test ./internal/history/ -v`
Expected: FAIL — the package does not exist.

- [ ] **Step 3: Write the implementation**

Create `internal/history/history.go`:

```go
// Package history records what the hub sent, and lets it be read and re-sent.
//
// A write-only log is not a feature: the reason to keep this is that after
// broadcasting to six agents the operator needs to know which ones got it, and
// then to send the same thing again to the ones that did not.
package history

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Entry is one send to one target.
//
// Outcome is the WORD — delivered, sent-unwitnessed, refused — and never a
// boolean, because a confirmation fires whether or not any bytes arrived, so
// `ok: true` cannot distinguish a delivery from a confirmation over nothing.
type Entry struct {
	At          time.Time `json:"at"`
	Host        string    `json:"host"`
	PaneID      string    `json:"pane_id"`
	SessionName string    `json:"session_name"`
	WindowName  string    `json:"window_name"`
	Text        string    `json:"text"`
	Outcome     string    `json:"outcome"`
	Reason      string    `json:"reason,omitempty"`
	Token       string    `json:"token,omitempty"`
}

// Log is an append-only JSONL file with size-based rotation.
type Log struct {
	mu       sync.Mutex
	path     string
	maxBytes int64
	f        *os.File
}

// Open creates the log and its directory. A missing file is an empty history, not
// an error.
func Open(path string, maxBytes int64) (*Log, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	return &Log{path: path, maxBytes: maxBytes, f: f}, nil
}

// Append writes one entry. JSON encoding is what keeps a multi-line prompt on one
// line: the newline is escaped, so a reader splitting on newlines cannot tear the
// entry in half.
func (l *Log) Append(e Entry) error {
	b, err := json.Marshal(e)
	if err != nil {
		return err
	}
	l.mu.Lock()
	defer l.mu.Unlock()

	if _, err := l.f.Write(append(b, '\n')); err != nil {
		return err
	}
	return l.rotateLocked()
}

// rotateLocked keeps the newest half when the file outgrows its limit. Truncating
// from the front rather than deleting the file is what preserves the entries a
// re-send would use.
func (l *Log) rotateLocked() error {
	fi, err := l.f.Stat()
	if err != nil || fi.Size() <= l.maxBytes {
		return err
	}
	raw, err := os.ReadFile(l.path)
	if err != nil {
		return err
	}
	lines := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	keep := lines[len(lines)/2:]

	tmp := l.path + ".rotating"
	if err := os.WriteFile(tmp, []byte(strings.Join(keep, "\n")+"\n"), 0o600); err != nil {
		return err
	}
	if err := l.f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, l.path); err != nil {
		return err
	}
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	l.f = f
	return nil
}

// Recent returns up to n entries, newest first.
//
// A line that will not parse is SKIPPED rather than fatal: a hub killed mid-write
// leaves a torn last line, and one bad shutdown must not cost the user their
// history.
func (l *Log) Recent(n int) ([]Entry, error) {
	f, err := os.Open(l.path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var all []Entry
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		var e Entry
		if json.Unmarshal(sc.Bytes(), &e) != nil {
			continue
		}
		all = append(all, e)
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}

	out := make([]Entry, 0, n)
	for i := len(all) - 1; i >= 0 && len(out) < n; i-- {
		out = append(out, all[i])
	}
	return out, nil
}

func (l *Log) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f == nil {
		return nil
	}
	err := l.f.Close()
	l.f = nil
	return err
}

// DefaultPath is $XDG_STATE_HOME/tmux-hub/history.jsonl, falling back to
// ~/.local/state — state rather than cache, because a re-send needs it.
func DefaultPath() string {
	base := os.Getenv("XDG_STATE_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "history.jsonl"
		}
		base = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(base, "tmux-hub", "history.jsonl")
}
```

- [ ] **Step 4: Wire the history view**

Add to `internal/ui/model.go` — the `h` key from Task 7's handler lands here:

```go
// historyMsg carries entries read off disk, so the read never blocks the UI.
type historyMsg struct {
	entries []history.Entry
	err     error
}

func (m model) openHistory() (tea.Model, tea.Cmd) {
	if m.hist == nil {
		m.note = "no history log — start the hub without --no-history"
		return m, nil
	}
	l := m.hist
	return m, func() tea.Msg {
		es, err := l.Recent(200)
		return historyMsg{entries: es, err: err}
	}
}
```

and in `Update`:

```go
	case historyMsg:
		if msg.err != nil {
			m.note = "cannot read history: " + msg.err.Error()
			return m, nil
		}
		if len(msg.entries) == 0 {
			m.note = "history is empty"
			return m, nil
		}
		m.mode, m.history, m.histCursor = modeHistory, msg.entries, 0
		return m, nil
```

with `modeHistory` added to the `uiMode` constants and these fields on `model`:

```go
	history    []history.Entry // loaded on demand, newest first
	histCursor int
```

The history-mode keys. `r` re-sends to the **current** selection and never to the
entry's own recorded targets:

```go
func (m model) historyKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "q", "h":
		m.mode, m.history = modeBrowse, nil
		return m, nil
	case "j", "down":
		if m.histCursor < len(m.history)-1 {
			m.histCursor++
		}
		return m, nil
	case "k", "up":
		if m.histCursor > 0 {
			m.histCursor--
		}
		return m, nil
	case "r":
		if m.histCursor >= len(m.history) {
			return m, nil
		}
		if m.sel.Len() == 0 {
			m.note = "select the panes to re-send to first"
			return m, nil
		}
		// The text is reloaded into the composer rather than sent, and the send
		// goes to the CURRENT selection. Reusing the entry's own recorded targets
		// would write into panes the user is no longer looking at — an hour-old
		// %3 on a host that has since restarted its server is a different pane.
		m.composer.Clear()
		m.composer.Insert(m.history[m.histCursor].Text)
		m.mode = modeBrowse
		m.fromHistory = true // Task 8's rule then always asks
		m.pending = broadcast.Needed(m.targetStates())
		if len(m.pending) == 0 {
			// Cannot happen while fromHistory is set, and asserted rather than
			// assumed: a re-send that skipped the dialog would be the one case
			// where the user did not type the text they are about to send.
			m.note = "internal: a history re-send must always confirm"
			return m, nil
		}
		m.mode = modeConfirm
		return m, nil
	}
	return m, nil
}
```

`m.fromHistory` is a `bool` on `model`, read by `targetStates()` into every
`TargetState.FromHistory` and cleared in the `sentMsg` case. And the render:

```go
// RenderHistory lists sends newest-first with the outcome WORD, because that is
// the column a person scans after broadcasting to six agents: which ones got it.
func RenderHistory(es []history.Entry, width, height, cursor int) []string {
	out := make([]string, 0, height)
	for i, e := range es {
		if len(out) >= height {
			break
		}
		point := " "
		if i == cursor {
			point = ">"
		}
		glyph := "?"
		switch e.Outcome {
		case "delivered":
			glyph = "✓"
		case "sent-unwitnessed":
			glyph = "~"
		case "refused":
			glyph = "✗"
		}
		out = append(out, lines.Truncate(fmt.Sprintf("%s%s %s %-10s %-6s %s",
			point, glyph, e.At.Format("15:04:05"), e.Host, e.PaneID,
			strings.ReplaceAll(firstLineOf(e.Text), "\n", " ")), width))
	}
	for len(out) < height {
		out = append(out, "")
	}
	return out
}

func firstLineOf(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i] + " …"
	}
	return s
}
```

- [ ] **Step 5: Test the re-send rule**

```go
// A history re-send must ALWAYS confirm, because the one thing that separates it
// from an ordinary send is that the user did not just type the text.
func TestHistoryResendAlwaysConfirms(t *testing.T) {
	m := model{
		mode:    modeHistory,
		history: []history.Entry{{Text: "an old prompt", Outcome: "delivered"}},
		panes:   []registry.Pane{{Host: "local", PaneID: "%1"}},
	}
	m.sel.Toggle(SelectionKey{Host: "local", PaneID: "%1"})

	got, _ := m.historyKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("r")})
	after := got.(model)
	if after.mode != modeConfirm {
		t.Fatalf("mode = %v, want modeConfirm — a re-send must ask", after.mode)
	}
	if after.composer.Text() != "an old prompt" {
		t.Errorf("the text was not loaded: %q", after.composer.Text())
	}
	if !after.fromHistory {
		t.Error("fromHistory was not set, so the confirmation rule cannot see it")
	}
}

// With nothing selected, a re-send does nothing and says why. The entry's own
// recorded targets are deliberately not used.
func TestHistoryResendNeedsACurrentSelection(t *testing.T) {
	m := model{mode: modeHistory,
		history: []history.Entry{{Text: "x", Host: "nuc", PaneID: "%9"}}}
	got, cmd := m.historyKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("r")})
	if cmd != nil {
		t.Error("a re-send with no selection sent something")
	}
	if after := got.(model); after.note == "" {
		t.Error("it failed silently instead of saying what to do")
	}
}

func TestRenderHistoryShowsTheOutcomeWord(t *testing.T) {
	es := []history.Entry{
		{At: time.Unix(1786487832, 0), Host: "nuc", PaneID: "%3",
			Text: "first\nsecond", Outcome: "delivered"},
		{At: time.Unix(1786487833, 0), Host: "eu", PaneID: "%1",
			Text: "other", Outcome: "refused"},
	}
	out := RenderHistory(es, 80, 6, 0)
	if !strings.Contains(out[0], "✓") || !strings.Contains(out[1], "✗") {
		t.Errorf("outcomes are not distinguishable:\n%s", strings.Join(out, "\n"))
	}
	if strings.Contains(out[0], "\n") {
		t.Error("a multi-line prompt broke the row")
	}
	for _, l := range out {
		if lineWidth(l) > 80 {
			t.Errorf("row exceeds the width: %q", l)
		}
	}
}
```

Run: `go test ./internal/ui/ -v`
Expected: PASS.

- [ ] **Step 6: Run and commit**

Run: `go test -race ./... && go build ./...`

```bash
gofmt -l . && go test -race ./... && git add internal/history/ internal/ui/ && git commit -m "feat(history): a log that can be read and re-sent

A write-only log is not a feature. The reason to keep this is that after
broadcasting to six agents the operator needs to know which got it and then send
the same text again to the ones that did not.

The outcome is stored as the WORD, never a boolean: a confirmation fires whether
or not any bytes arrived, so 'ok: true' cannot distinguish a delivery from a
confirmation over nothing. JSON keeps a multi-line prompt on one line, so a
reader splitting on newlines cannot tear an entry in half, and a line that will
not parse is skipped rather than fatal — a hub killed mid-write leaves a torn
last line, and one bad shutdown must not cost the user their history.

Re-send goes to the CURRENT selection and sets FromHistory, so the confirmation
rule always asks. Silently reusing the old targets would send to panes the user
is no longer looking at."
```

---

### Task 10: wire the write path into the running program

**This task was missing, and its absence was the branch's central defect.** The plan
built nine tasks of machinery and never said to connect it. Every brief was met
exactly, nine task reviews passed, and the shipped binary constructed
`&broadcast.Sender{}`, `&broadcast.Stamper{}` and `&history.Log{}` as ZERO VALUES:
no pane was ever stamped, `internal/proc` had no importer outside its own tests,
`Sweep` never ran, history had no path, and the first Enter on the confirm dialog
panicked on a nil mutex. The whole-branch review found it; nothing before it could,
because each review saw one task's diff against one task's brief and this seam
belonged to no brief.

It is written down here rather than quietly fixed because the lesson is about plans,
not about this code: **a plan of components needs a task that joins them, and it needs
a test that fails when they are not joined.** The second half is what makes it
structural — see the wiring floor below.

**Files:**
- Modify: `cmd/tmux-hub/main.go`, `internal/ui/model.go`
- Create: `internal/broadcast/identify.go` (the per-tick identify-and-stamp step)
- Create: `internal/proc` walker per transport (local and over ssh)
- Test: `internal/ui/wiring_test.go`

**What it must do:**

- construct the instance, stamper, sender and history log with their real
  constructors, in `ui.Run` and `main.go` where the flags belong;
- run `broadcast.Sweep` at connect and at shutdown, so the measured paste-buffer leak
  cannot outlive a crash;
- identify agent panes from the poll tick — the local walk and the remote one — and
  stamp exactly those, issuing `set -pu` the moment the walk stops finding an agent;
- fill every field of `broadcast.TargetState` from the registry. Before this, three of
  eleven were populated, so four of `Needed`'s seven reasons could not fire in
  production and the whole point of the confirmation rule had no input. It failed
  safe — it always asked — which is exactly what hid it.

**The wiring floor, which is the part worth keeping:**

```go
// TestTheWritePathHasProductionCallSites fails when a package or constructor this
// program depends on has no importer outside its own tests.
//
// It exists because the branch shipped once with none of them wired: every unit test
// passed, every task review passed, and the binary could not send. No assertion in the
// suite could see it, because every test builds the thing it tests. This one asks
// whether anything BUILDS it.
func TestTheWritePathHasProductionCallSites(t *testing.T) {
	// … greps the non-test tree for NewSender, NewStamper, NewInstance, Sweep(,
	// history.Open and an import of internal/proc, failing on any with zero hits.
}
```

A floor like this is cheap and it catches a class no amount of unit testing can: code
that is correct, tested, and unreachable.

## Self-Review

**Spec coverage for §7:**

| requirement | task |
|---|---|
| the unit is a pane, never a session | 7 (`SelectionKey`) |
| a session with a pane is one row, and takes the CLI's fact | 3b |
| agent pane identified positively by process tree, never `pane_active` | 3 |
| `pane_current_command` is `bash` for both panes, so it is not used | 3 |
| per-pane token, `-p` mandatory, rotated every re-stamp | 4 |
| `set -pu` the moment the walk stops finding an agent | 4 + 7 (tick) |
| guard inside the same invocation as the send | 5 |
| the `if` and every sub-command carry their own `-t` | 1 (enforced) + 5 (built) |
| seam refuses a `-t` that is not a pane id | 1 |
| one text primitive: `load-buffer -` + `paste-buffer -d -p -r` | 1 (stdin) + 5 |
| `send-keys -H` and `-l` deleted from the payload path | 1 (forbidden by rule) + 5 |
| Enter a separate act ~50 ms later | 5 (`Submit`) |
| interrupt / cancel as their own hotkeys | 5 (`Interrupt`) + 7 (key) |
| `paste-buffer -d`, deferred `delete-buffer`, sweep at both ends | 5 + 6 |
| three outcomes, witnessed by a **second** read | 5 |
| confirmation echoes id **and** token, asserted against intent | 5 |
| confirmation triggers on state change, not target count | 8 |
| a target that would read the prompt as keypresses is confirmed | 8 |
| self-exclusion from every target set | already in `hub` (`excludeSelf`) |
| `history.jsonl` with a reader, re-send and rotation | 9 |
| remote state namespaced per instance | 2 |

**Task 10 was added during execution**, after the whole-branch review found the write
path had no production caller. See its section above; the plan originally ended at
Task 9 and that omission is the single most instructive defect this plan produced.

**Deliberately not here:** §5 transport supervision (its own plan, now second),
§8's resize warning, §16's `--watch`. The write path works against the existing
`--host`, which is what makes this plan first.

**Type consistency:** `Instance` (2) is used in 4, 5, 6. `Stamper` (4) is used in 5.
`Outcome` (5) is used in 8 and, as a string, in 9. `tmux.InputRunner` (1) is used in
5. `SelectionKey` (7) keys on `Host` + `PaneID`, matching `broadcast.Target`'s
`Host` + `PaneID`. `history.Entry.Outcome` is a `string` rather than
`broadcast.Outcome` so `history` does not import `broadcast` — the log is a record,
not a participant.

**Three facts measured while writing this plan, which changed it:**

1. `if -F` **without its own `-t`** reads the pane option from the server's *current*
   pane. With `%0` stamped and `%1` not, the guard passed and the payload was pasted
   into `%1`, which printed `OK %1`. §7 had `-t` on the `if` in its example but gave
   the reason only for sub-commands; this is a second fail-open.
2. `#{window_activity}` **cannot** advance inside the invocation that writes —
   measured identical before and after, three times — so §7's same-invocation witness
   would have reported every delivery as unwitnessed. A separate read at +50 ms
   already shows it moved. `#{history_size}` stayed `0` across a delivery plainly on
   screen, so it is no substitute.
3. `comm` is unusable for identification (Node overwrites it: `MainThread`,
   `node-MainThread`, `2.1.226`), `claude bg-pty-host` / `bg-spare` are claude
   processes that are not agents, and `CLAUDECODE=1` is absent from claude's own
   environ and present only on its children — readable for 71 of 145 candidates.

All three are now rows in `docs/design.md` §3 and corrections in §7.

**Six defects this plan had before its code was extracted and run:**

1. Task 1's tests called `liveTarget`, which lives in `internal/hub`. This package's
   helper is `testServer`, so Task 1 did not compile.
2. The first `Validate` applied the trailing-`-t` rule **per argv element**, and a
   single element that *is* `-t` therefore tripped it — every command targeting a
   pane was rejected, including the seven pre-existing tests in that package.
3. The second `Validate` scanned each argument in isolation, so a bare `%0` whose
   `-t` was the *previous argv element* had no context and was refused. The rule has
   to be read at two levels, which is what the shipped version does.
4. Task 8's "unidentified" case set only `IdentifiedNow = false`, which lands in the
   exited-agent branch — the clause it named was unreachable, so the case would have
   passed against a broken implementation.
5. Task 9's rotation test asserted only that the last-written entry survived. By the
   end of the loop the file is small again, so the final append triggers no rotation
   and the assertion holds whichever half rotation keeps — it stayed green under the
   mutation that discards the newest.
6. Task 3's helper block had no `Create` line of its own and was easy to lose, taking
   `timeAfter` and `contains` with it.

**And three guarantees had no test at all, which the mutation sweep found and Step 5
of Task 5 now covers.** Each was masked differently — by an earlier layer that
refuses first, by a redundant sibling that cleans up anyway, and by a test fixture
(`cat`) in which the guarantee has no observable consequence. Two of the three
*replacement* tests were themselves green on the first attempt: the decoy pane held a
token that did not match either, and the flag check found `-p` in the chain's
`display -p`. Both are fixed and both mutations are now caught.

**A third verification round, against a live Claude Code session** (throwaway, private
socket, cleaned up) found the worst defect of all and confirmed the rest:

- **`IdentifyAgent` did not identify a real agent pane.** There are two launch shapes,
  and the plan handled one. When the pane's own command is claude — `tmux new-window
  claude` — `pane_pid` IS the claude process, and a walk over *descendants* finds
  nothing: measured "not identified" against a live session. Fixed by testing the root
  first, calibrated by removing the fix (the new test goes red), and checked against a
  plain `sleep` so the widening did not widen what is accepted.
- **§7's justification for the process walk was false.** `pane_current_command` reports
  `claude`, not `bash`, in *both* shapes — so that field would separate an agent pane
  from an idle shell. The walk is still right, for three different reasons now recorded
  in §3: the field names the foreground process and becomes `bash`/`git` when the agent
  shells out; it cannot separate an interactive agent from `claude bg-pty-host`; and one
  tree rule covers both shapes and a wrapper.
- **`respawn-pane -k` does not keep `pane_pid`** (`702400` → `702406`), only the id and
  the option. The conclusion — a pane-bound token proves pane identity, not process
  identity — is unchanged, but it rests on the option surviving, not the pid.
- **The whole write path works against the real target.** `bracket_paste_flag`=1; a
  three-line payload ending in `;` landed whole in the input box, nothing submitted, no
  buffers left. Both witnesses fired: activity `1786489714` → `1786489766` on the later
  read, and `echoedPrefix` found every fragment. The same-invocation reads inside the
  batch both said `1786489714`, confirming against a real agent what `cat` had already
  shown.
- **Server epoch and remote prerequisites hold.** A restart changes
  `#{pid}:#{start_time}` and hands out `%0` again, so pane-id recycling is real.
  `nuc` and `eu` both have python3 for the walk script; `eu` has no `claude`, which the
  agents producer already tolerates.

**Verification performed:** every Go block extracted into a copy of the repository,
`gofmt`- and `vet`-clean, green under `go test -race ./...` — **172 tests across 11
packages** — and 23 of 25 injected mutations turn a test red. The final round was a
clean-room re-extraction from the committed document, which caught one last thing
worth knowing: **gofmt rewrites doc comments**, so `''` inside one becomes a
typographic quote and the plan's own `gofmt -l .` gate would have failed on its own
code. The comment now says "the empty string" in prose instead. The two that do not are
the pane-id echo (redundant behind the token check, recorded above) and nothing else.

**Known rough edges:** `Witness` spends one extra read per target on the 150 ms path,
which the next tick would have answered for free — accepted because a person who
just pressed Enter should not wait a full poll interval to learn whether it landed.
`echoedPrefix` uses the first 24 characters, which a pane that reflows aggressively
could still break; the activity check covers that case, and the pair failing together
is what `sent-unwitnessed` means. And `RemoteWalkScript` needs `python3` on the far
side — its absence yields no identification, which routes every target through
confirmation rather than through a silent send.
