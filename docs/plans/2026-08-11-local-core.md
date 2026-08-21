# tmux-hub Local Core Implementation Plan

> **STATUS: COMPLETE.** Every task here is implemented and merged, and the work went
> considerably past it — the agents producer (§17), inbox scrolling, the tile grid, remote attach,
> concurrent host polling, the dial-based transport classifier and the state-transition log all
> arrived after this plan was written. See `docs/plans/2026-08-12-transport-supervision.md` for what
> is next, and `docs/design.md` §15 for the overall sequence. Kept for the record: its task
> boundaries and its tests are what the code still looks like.

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A read-only `tmux-hub` that shows every pane on the **local** tmux server as a pane-level inbox sorted by attention state, with content-line tiles, plus `status --json`.

**Architecture:** One `Runner` seam is the only code path that executes tmux; it enforces the two measured invariants (no `client_*` format variables, no literal `%` outside a pane id) so no caller can violate them. Above it: a free-text-free delta format, length-framed range captures, a pure line-kind classifier, a pure state classifier, a registry that merges and sorts, and a bubbletea UI. Nothing writes to a pane in this plan.

**Tech Stack:** Go 1.26.1 (via mise), stdlib only for everything below the UI; `bubbletea v1.3.10` + `lipgloss v1.1.0` for the UI; `go test` for tests.

**Design source:** `docs/design.md`. Section references below (§3, §5, §6…) point at it. Read §3 before starting — it records the measurements that make several obvious implementations wrong.

**This plan's code has been compiled and run.** Every Go block was extracted into a scratch
module, built, vetted and tested against a real tmux 3.7b on private sockets: **47 tests pass, 0
skips, `gofmt` clean**, and `tmux-hub status` returns the live local server. Three defects were
found that way and are already fixed here — the guard test could never pass (it scanned the file
that defines the forbidden list), `Update` derived tile content from the classification zone which
is chrome by construction so the tile came back empty, and the JSON report carried raw ANSI. If a
step fails for you, suspect your transcription before the plan.

## Global Constraints

- Go version: **1.26.1**. `go.mod` declares `go 1.26.1`.
- Module path: `github.com/DawnBreather/tmux-hub`.
- Dependencies: **only** `github.com/charmbracelet/bubbletea v1.3.10` and `github.com/charmbracelet/lipgloss v1.1.0`. Everything else is stdlib. No test framework beyond `testing`.
- **Every** tmux invocation carries an explicit `-S <socket>`. A tmux command with no socket flag talks to the developer's own live server; `Runner` refuses to build one.
- **Never** emit `#{client_activity}` or `#{client_created}` — they segfault a whole tmux 3.2a server when no client is attached, and no guard idiom helps (§3).
- **Never** put a literal `%` in a tmux argument unless the whole argument is a pane id matching `^%[0-9]+$` — `display -p` runs its argument through `strftime` and a `%` token makes it return an empty string with rc=0 (§3).
- Tests that touch a real tmux use socket `-S $(t.TempDir())/tmux.sock` and kill their server in `t.Cleanup`. No test may target the session `live1` or the default socket.
- `gofmt -l .` must be empty before every commit.
- Nothing in this plan sends input to a pane, resizes a pane, sets a pane or window option, or starts a tmux server on a socket it did not create.

---

### Task 1: The Runner seam and its invariants

**Files:**
- Create: `go.mod`
- Create: `internal/tmux/run.go`
- Test: `internal/tmux/run_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces:
  - `type Target struct { Label, Socket string }`
  - `type Result struct { Stdout, Stderr string; RC int }`
  - `type Runner interface { Run(ctx context.Context, t Target, args ...string) (Result, error) }`
  - `func NewExec(timeout time.Duration) Runner`
  - `func Validate(args []string) error`
  - `var ErrForbiddenFormat, ErrPercentInArg, ErrNoSocket error`
  - `func LocalSocket() string`

- [ ] **Step 1: Initialise the module**

```bash
cd /home/dev/lab/streams/experiments/tmux-hub
go mod init github.com/DawnBreather/tmux-hub
```

- [ ] **Step 2: Write the failing test**

Create `internal/tmux/run_test.go`:

```go
package tmux

import (
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestValidateRejectsForbiddenFormats(t *testing.T) {
	cases := []string{
		"#{client_activity}",
		"#{q:client_activity}",
		"#{?client_activity,y,n}",
		"prefix#{client_created}suffix",
	}
	for _, c := range cases {
		if err := Validate([]string{"display", "-p", c}); !errors.Is(err, ErrForbiddenFormat) {
			t.Errorf("Validate(%q) = %v, want ErrForbiddenFormat", c, err)
		}
	}
}

func TestValidateRejectsLiteralPercent(t *testing.T) {
	bad := []string{"CONFIRM-%2", "OK %s", "100%", "%"}
	for _, c := range bad {
		if err := Validate([]string{"display", "-p", c}); !errors.Is(err, ErrPercentInArg) {
			t.Errorf("Validate(%q) = %v, want ErrPercentInArg", c, err)
		}
	}
}

func TestValidateAllowsPaneIDs(t *testing.T) {
	ok := [][]string{
		{"capture-pane", "-p", "-e", "-t", "%0", "-S", "18", "-E", "23"},
		{"display", "-p", "-t", "%12", "#{pane_id} #{pane_height}"},
		{"list-panes", "-a", "-F", "#{pane_id}|#{window_activity}"},
	}
	for _, args := range ok {
		if err := Validate(args); err != nil {
			t.Errorf("Validate(%v) = %v, want nil", args, err)
		}
	}
}

func TestRunRefusesEmptySocket(t *testing.T) {
	r := NewExec(2 * time.Second)
	_, err := r.Run(context.Background(), Target{Label: "local"}, "list-panes")
	if !errors.Is(err, ErrNoSocket) {
		t.Fatalf("Run with empty socket = %v, want ErrNoSocket", err)
	}
}

// A real server on a private socket. Never the default socket, never live1.
func testServer(t *testing.T) Target {
	t.Helper()
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	sock := filepath.Join(t.TempDir(), "tmux.sock")
	cmd := exec.Command("tmux", "-S", sock, "-f", "/dev/null",
		"new-session", "-d", "-s", "one", "-x", "80", "-y", "24")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("new-session: %v: %s", err, out)
	}
	t.Cleanup(func() {
		_ = exec.Command("tmux", "-S", sock, "kill-server").Run()
	})
	return Target{Label: "test", Socket: sock}
}

func TestRunAgainstRealServer(t *testing.T) {
	tgt := testServer(t)
	r := NewExec(5 * time.Second)
	res, err := r.Run(context.Background(), tgt, "list-panes", "-a", "-F", "#{pane_id}")
	if err != nil {
		t.Fatalf("Run: %v (stderr=%q)", err, res.Stderr)
	}
	if res.RC != 0 {
		t.Fatalf("RC = %d, stderr = %q", res.RC, res.Stderr)
	}
	if !strings.HasPrefix(strings.TrimSpace(res.Stdout), "%") {
		t.Fatalf("Stdout = %q, want a pane id", res.Stdout)
	}
}

func TestRunReportsNonZeroWithoutError(t *testing.T) {
	tgt := testServer(t)
	r := NewExec(5 * time.Second)
	res, err := r.Run(context.Background(), tgt, "capture-pane", "-p", "-t", "%999")
	if err != nil {
		t.Fatalf("a tmux failure must come back as RC, not err: %v", err)
	}
	if res.RC == 0 {
		t.Fatal("RC = 0, want non-zero for a missing pane")
	}
	if !strings.Contains(res.Stderr, "find pane") {
		t.Fatalf("Stderr = %q, want it to mention the missing pane", res.Stderr)
	}
}

func TestRunEnforcesDeadline(t *testing.T) {
	r := NewExec(300 * time.Millisecond)
	start := time.Now()
	// A tmux command against a socket with no listener fails fast, so the deadline
	// is exercised through the same code path with a command that blocks. RunRaw is
	// reached by assertion rather than being part of the Runner interface, so that
	// callers cannot bypass Validate by using it.
	rr, ok := r.(interface {
		RunRaw(context.Context, string, ...string) (Result, error)
	})
	if !ok {
		t.Fatal("the exec runner must expose RunRaw so the deadline is testable")
	}
	res, err := rr.RunRaw(context.Background(), "sleep", "10")
	elapsed := time.Since(start)
	if err == nil {
		t.Fatalf("want a deadline error, got RC=%d", res.RC)
	}
	if elapsed > 3*time.Second {
		t.Fatalf("deadline not enforced: took %v", elapsed)
	}
}
```

- [ ] **Step 3: Run the tests to verify they fail**

Run: `go test ./internal/tmux/ -run 'TestValidate|TestRun' -v`
Expected: FAIL — the package does not compile, `undefined: Validate`.

- [ ] **Step 4: Write the implementation**

Create `internal/tmux/run.go`:

```go
// Package tmux is the only place in tmux-hub that executes the tmux binary.
//
// Two invariants live here rather than at the call sites, because a caller then
// cannot violate them. Both come from defects measured against live servers and
// recorded in docs/design.md §3:
//
//   - #{client_activity} and #{client_created} segfault an entire tmux 3.2a
//     server when no client is attached. No guard idiom helps: #{q:...},
//     #{?...,y,n}, x#{...}y and #{t:...} all crash it.
//   - display -p runs its argument through strftime, so a literal % makes the
//     whole message come back empty with rc=0. Identity must be emitted through
//     the format layer (#{pane_id}) instead. The only argument that may contain
//     a % is a bare pane id such as %12.
package tmux

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Target names one tmux server.
type Target struct {
	Label  string // "local", or an ssh host label
	Socket string // always set; passed as tmux -S
}

// Result is the raw outcome of one invocation. A tmux-level failure is an RC,
// not an error: host-state classification is a pure function of Result, which
// is what makes the failure taxonomy testable without a live remote.
type Result struct {
	Stdout string
	Stderr string
	RC     int
}

// Runner is the single seam through which hub code reaches tmux.
type Runner interface {
	Run(ctx context.Context, t Target, args ...string) (Result, error)
}

var (
	ErrForbiddenFormat = errors.New("tmux: forbidden format variable")
	ErrPercentInArg    = errors.New("tmux: literal % outside a pane id")
	ErrNoSocket        = errors.New("tmux: refusing to run without an explicit socket")
)

var forbiddenVars = []string{"client_activity", "client_created"}

var paneID = regexp.MustCompile(`^%[0-9]+$`)

// Validate applies the two invariants to a full argument list.
func Validate(args []string) error {
	for _, a := range args {
		for _, f := range forbiddenVars {
			if strings.Contains(a, f) {
				return fmt.Errorf("%w: %s", ErrForbiddenFormat, f)
			}
		}
		if strings.Contains(a, "%") && !paneID.MatchString(a) {
			return fmt.Errorf("%w: %q", ErrPercentInArg, a)
		}
	}
	return nil
}

type execRunner struct {
	timeout time.Duration
}

// NewExec returns a Runner that shells out to tmux with a per-call deadline.
// The deadline is mandatory: a stalled ssh forward makes tmux hang forever
// (design.md §3), and a hung invocation would freeze the UI.
func NewExec(timeout time.Duration) Runner {
	return &execRunner{timeout: timeout}
}

func (r *execRunner) Run(ctx context.Context, t Target, args ...string) (Result, error) {
	if t.Socket == "" {
		return Result{}, ErrNoSocket
	}
	if err := Validate(args); err != nil {
		return Result{}, err
	}
	full := append([]string{"-S", t.Socket}, args...)
	return r.RunRaw(ctx, "tmux", full...)
}

// RunRaw executes a command under the runner's deadline. It exists so the
// deadline itself is testable without a tmux server.
func (r *execRunner) RunRaw(ctx context.Context, name string, args ...string) (Result, error) {
	ctx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, name, args...)
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
	if err != nil {
		return res, err
	}
	return res, nil
}

// LocalSocket derives the local server's socket path without running tmux, so
// that even the first call carries an explicit -S. When the hub runs inside
// tmux, $TMUX names the socket directly.
func LocalSocket() string {
	if v := os.Getenv("TMUX"); v != "" {
		if i := strings.IndexByte(v, ','); i > 0 {
			return v[:i]
		}
	}
	dir := os.Getenv("TMUX_TMPDIR")
	if dir == "" {
		dir = "/tmp"
	}
	uid := "0"
	if u, err := user.Current(); err == nil {
		uid = u.Uid
	}
	return filepath.Join(dir, "tmux-"+uid, "default")
}
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test ./internal/tmux/ -v`
Expected: PASS for all seven tests. `TestRunAgainstRealServer` and friends skip only if tmux is absent.

- [ ] **Step 6: Add the guard test that no format string can reach tmux unchecked**

Append to `internal/tmux/run_test.go`:

```go
func TestRunValidatesBeforeExecuting(t *testing.T) {
	tgt := testServer(t)
	r := NewExec(5 * time.Second)
	_, err := r.Run(context.Background(), tgt, "display", "-p", "#{client_activity}")
	if !errors.Is(err, ErrForbiddenFormat) {
		t.Fatalf("Run must reject before executing, got %v", err)
	}
}
```

- [ ] **Step 7: Run it**

Run: `go test ./internal/tmux/ -run TestRunValidatesBeforeExecuting -v`
Expected: PASS.

- [ ] **Step 8: Commit**

```bash
gofmt -l . && go test ./... && git add go.mod internal/tmux/ && git commit -m "feat(tmux): the Runner seam, with its invariants enforced inside it

Two measured defects become impossible rather than avoidable: a
client_activity query that segfaults a tmux 3.2a server, and a literal %
in a display template that makes tmux return an empty string with rc=0.
Validate() runs before exec, so no call site can bypass either.

Run also refuses an empty socket, so a bare tmux command that would talk
to the developer's own live server cannot be constructed."
```

---

### Task 2: The delta format — no free text, bounded parse

**Files:**
- Create: `internal/tmux/delta.go`
- Test: `internal/tmux/delta_test.go`

**Interfaces:**
- Consumes: `Runner`, `Target`, `Result` from Task 1.
- Produces:
  - `type Delta struct { PaneID string; Activity int64; HistorySize int; Dead bool; Alt bool; WindowWidth, PaneHeight int; PanePID int }`
  - `const DeltaFormat string`
  - `func ParseDelta(stdout string) ([]Delta, error)`
  - `func FetchDeltas(ctx context.Context, r Runner, t Target) ([]Delta, error)`

- [ ] **Step 1: Write the failing test**

Create `internal/tmux/delta_test.go`:

```go
package tmux

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestParseDelta(t *testing.T) {
	in := "%0|1786450154|153|0|NORM|80|24|12345\n%7|1786450160|0|1|ALT|200|50|999\n"
	got, err := ParseDelta(in)
	if err != nil {
		t.Fatalf("ParseDelta: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
	if got[0].PaneID != "%0" || got[0].Activity != 1786450154 || got[0].HistorySize != 153 {
		t.Errorf("got[0] = %+v", got[0])
	}
	if got[0].Dead || got[0].Alt || got[0].PaneHeight != 24 || got[0].PanePID != 12345 {
		t.Errorf("got[0] = %+v", got[0])
	}
	if !got[1].Dead || !got[1].Alt || got[1].WindowWidth != 200 {
		t.Errorf("got[1] = %+v", got[1])
	}
}

// The delta format must carry no free text, so no value can contain the
// delimiter. A field count that is not exactly 8 is a bug in the hub, not
// data to be salvaged (design.md §6).
func TestParseDeltaRejectsWrongFieldCount(t *testing.T) {
	for _, in := range []string{
		"%0|1|2|0|NORM|80|24\n",
		"%0|1|2|0|NORM|80|24|1|extra\n",
	} {
		if _, err := ParseDelta(in); err == nil {
			t.Errorf("ParseDelta(%q) = nil error, want a failure", in)
		}
	}
}

func TestParseDeltaSkipsBlankLines(t *testing.T) {
	got, err := ParseDelta("\n%0|1|2|0|NORM|80|24|1\n\n")
	if err != nil {
		t.Fatalf("ParseDelta: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1", len(got))
	}
}

// A session or window name containing the delimiter must not be able to shift
// fields, because the delta format does not select any name at all. This test
// creates the hostile name and proves the parse is unaffected.
func TestDeltaFormatIsImmuneToNamesWithDelimiter(t *testing.T) {
	tgt := testServer(t)
	if out, err := exec.Command("tmux", "-S", tgt.Socket,
		"rename-session", "-t", "one", "a|b").CombinedOutput(); err != nil {
		t.Fatalf("rename-session: %v: %s", err, out)
	}
	if out, err := exec.Command("tmux", "-S", tgt.Socket,
		"rename-window", "-t", "a|b", "w|x").CombinedOutput(); err != nil {
		t.Fatalf("rename-window: %v: %s", err, out)
	}
	r := NewExec(5 * time.Second)
	got, err := FetchDeltas(context.Background(), r, tgt)
	if err != nil {
		t.Fatalf("FetchDeltas: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1", len(got))
	}
	if !strings.HasPrefix(got[0].PaneID, "%") || got[0].PaneHeight == 0 {
		t.Fatalf("parsed badly: %+v", got[0])
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/tmux/ -run Delta -v`
Expected: FAIL — `undefined: ParseDelta`.

- [ ] **Step 3: Write the implementation**

Create `internal/tmux/delta.go`:

```go
package tmux

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

// DeltaFormat selects only character-restricted values: a pane id, integers and
// a fixed token. Injection through a session, window or command name is not
// defended against here — it is impossible, because no such name is selected.
// Labels come from LabelFormats (see labels.go), each with exactly one trailing
// free-text field.
const DeltaFormat = "#{pane_id}|#{window_activity}|#{history_size}|" +
	"#{pane_dead}|#{?alternate_on,ALT,NORM}|#{window_width}|#{pane_height}|#{pane_pid}"

const deltaFields = 8

// Delta is one pane's cheap per-tick state.
type Delta struct {
	PaneID      string
	Activity    int64 // #{window_activity}: unix seconds of last output in this WINDOW
	HistorySize int
	Dead        bool
	Alt         bool
	WindowWidth int
	PaneHeight  int
	PanePID     int
}

// ParseDelta parses DeltaFormat output. A wrong field count is a hub bug and is
// reported, never salvaged.
func ParseDelta(stdout string) ([]Delta, error) {
	var out []Delta
	for i, line := range strings.Split(stdout, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		f := strings.Split(line, "|")
		if len(f) != deltaFields {
			return nil, fmt.Errorf("delta line %d: got %d fields, want %d: %q",
				i+1, len(f), deltaFields, line)
		}
		d := Delta{PaneID: f[0], Alt: f[4] == "ALT", Dead: f[3] == "1"}
		var err error
		if d.Activity, err = strconv.ParseInt(f[1], 10, 64); err != nil {
			return nil, fmt.Errorf("delta line %d: window_activity: %w", i+1, err)
		}
		for _, p := range []struct {
			dst *int
			src string
			nm  string
		}{
			{&d.HistorySize, f[2], "history_size"},
			{&d.WindowWidth, f[5], "window_width"},
			{&d.PaneHeight, f[6], "pane_height"},
			{&d.PanePID, f[7], "pane_pid"},
		} {
			v, err := strconv.Atoi(p.src)
			if err != nil {
				return nil, fmt.Errorf("delta line %d: %s: %w", i+1, p.nm, err)
			}
			*p.dst = v
		}
		out = append(out, d)
	}
	return out, nil
}

// FetchDeltas runs one list-panes for every pane on the server.
func FetchDeltas(ctx context.Context, r Runner, t Target) ([]Delta, error) {
	res, err := r.Run(ctx, t, "list-panes", "-a", "-F", DeltaFormat)
	if err != nil {
		return nil, err
	}
	if res.RC != 0 {
		return nil, fmt.Errorf("list-panes rc=%d: %s", res.RC, res.Stderr)
	}
	return ParseDelta(res.Stdout)
}
```

- [ ] **Step 4: Run to verify it passes**

Run: `go test ./internal/tmux/ -run Delta -v`
Expected: PASS, including `TestDeltaFormatIsImmuneToNamesWithDelimiter`.

- [ ] **Step 5: Commit**

```bash
gofmt -l . && go test ./... && git add internal/tmux/delta.go internal/tmux/delta_test.go && git commit -m "feat(tmux): free-text-free delta format

pane_current_command is free text and sat before the name fields in an
earlier draft, so a process named 'weird|name' shifted session_name and
window_name with rc=0 and the correct field count. The delta format now
selects only a pane id, integers and a fixed token, so delimiter injection
is impossible rather than guarded. A test renames a session to 'a|b' and a
window to 'w|x' and proves the parse is unaffected."
```

---

### Task 3: Labels — one trailing free-text field each

**Files:**
- Create: `internal/tmux/labels.go`
- Test: `internal/tmux/labels_test.go`

**Interfaces:**
- Consumes: `Runner`, `Target`, `Delta` from Tasks 1–2.
- Produces:
  - `type Labels struct { Session, Window, Command string }`
  - `func FetchLabels(ctx context.Context, r Runner, t Target) (map[string]Labels, error)`

- [ ] **Step 1: Write the failing test**

Create `internal/tmux/labels_test.go`:

```go
package tmux

import (
	"context"
	"os/exec"
	"testing"
	"time"
)

func TestParseLabelPairs(t *testing.T) {
	got, err := parseLabelPairs("%0|my|session\n%3|plain\n")
	if err != nil {
		t.Fatalf("parseLabelPairs: %v", err)
	}
	// SplitN(2) keeps everything after the first delimiter, so a value
	// containing the delimiter survives intact.
	if got["%0"] != "my|session" {
		t.Errorf("got[%%0] = %q, want %q", got["%0"], "my|session")
	}
	if got["%3"] != "plain" {
		t.Errorf("got[%%3] = %q", got["%3"])
	}
}

func TestFetchLabelsWithHostileNames(t *testing.T) {
	tgt := testServer(t)
	for _, args := range [][]string{
		{"rename-session", "-t", "one", "a|b"},
		{"rename-window", "-t", "a|b", "w|x"},
	} {
		full := append([]string{"-S", tgt.Socket}, args...)
		if out, err := exec.Command("tmux", full...).CombinedOutput(); err != nil {
			t.Fatalf("%v: %v: %s", args, err, out)
		}
	}
	r := NewExec(5 * time.Second)
	got, err := FetchLabels(context.Background(), r, tgt)
	if err != nil {
		t.Fatalf("FetchLabels: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1", len(got))
	}
	for id, l := range got {
		if l.Session != "a|b" {
			t.Errorf("%s Session = %q, want %q", id, l.Session, "a|b")
		}
		if l.Window != "w|x" {
			t.Errorf("%s Window = %q, want %q", id, l.Window, "w|x")
		}
		if l.Command == "" {
			t.Errorf("%s Command is empty", id)
		}
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/tmux/ -run Label -v`
Expected: FAIL — `undefined: parseLabelPairs`.

- [ ] **Step 3: Write the implementation**

Create `internal/tmux/labels.go`:

```go
package tmux

import (
	"context"
	"fmt"
	"strings"
)

// Labels are a pane's free-text descriptors. They are fetched in separate
// sub-commands, each selecting exactly ONE free-text field placed last, so a
// bounded split is provably sufficient. Three extra sub-commands cost about
// 700us locally and nothing over ssh, where one round trip dominates.
type Labels struct {
	Session string
	Window  string
	Command string
}

var labelFormats = []struct {
	format string
	assign func(*Labels, string)
}{
	{"#{pane_id}|#{session_name}", func(l *Labels, v string) { l.Session = v }},
	{"#{pane_id}|#{window_name}", func(l *Labels, v string) { l.Window = v }},
	{"#{pane_id}|#{pane_current_command}", func(l *Labels, v string) { l.Command = v }},
}

func parseLabelPairs(stdout string) (map[string]string, error) {
	out := map[string]string{}
	for i, line := range strings.Split(stdout, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		f := strings.SplitN(line, "|", 2)
		if len(f) != 2 {
			return nil, fmt.Errorf("label line %d: no delimiter: %q", i+1, line)
		}
		out[f[0]] = f[1]
	}
	return out, nil
}

// FetchLabels batches the label queries into one invocation. tmux does not frame
// sub-command output, so each query is run separately here rather than relying on
// a delimiter: the label path is cheap and clarity beats one saved round trip on
// the local socket. Remote batching is added with the host agent.
func FetchLabels(ctx context.Context, r Runner, t Target) (map[string]Labels, error) {
	out := map[string]Labels{}
	for _, lf := range labelFormats {
		res, err := r.Run(ctx, t, "list-panes", "-a", "-F", lf.format)
		if err != nil {
			return nil, err
		}
		if res.RC != 0 {
			return nil, fmt.Errorf("list-panes rc=%d: %s", res.RC, res.Stderr)
		}
		pairs, err := parseLabelPairs(res.Stdout)
		if err != nil {
			return nil, err
		}
		for id, v := range pairs {
			l := out[id]
			lf.assign(&l, v)
			out[id] = l
		}
	}
	return out, nil
}
```

- [ ] **Step 4: Run to verify it passes**

Run: `go test ./internal/tmux/ -run Label -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
gofmt -l . && go test ./... && git add internal/tmux/labels.go internal/tmux/labels_test.go && git commit -m "feat(tmux): labels, one trailing free-text field per query

Only the last field of a format string is protected by a bounded split, so
each label gets its own query with the free-text value last. A test renames
a session to 'a|b' and a window to 'w|x' and requires both to survive."
```

---

### Task 4: Length-framed range capture

**Files:**
- Create: `internal/tmux/capture.go`
- Test: `internal/tmux/capture_test.go`

**Interfaces:**
- Consumes: `Runner`, `Target`, `Delta`.
- Produces:
  - `type Capture struct { PaneID string; Lines []string; Height int; Stale bool }`
  - `const ZoneLines = 6`
  - `func ZoneRange(paneHeight, zone int) (start, end int)`
  - `func FetchZones(ctx context.Context, r Runner, t Target, ds []Delta) ([]Capture, error)`
  - `func FetchFull(ctx context.Context, r Runner, t Target, d Delta) (Capture, error)`

- [ ] **Step 1: Write the failing test**

Create `internal/tmux/capture_test.go`:

```go
package tmux

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestZoneRange(t *testing.T) {
	cases := []struct{ h, zone, wantS, wantE int }{
		{24, 6, 18, 23},
		{50, 6, 44, 49},
		{4, 6, 0, 3}, // a pane shorter than the zone clamps to the whole pane
		{1, 6, 0, 0},
	}
	for _, c := range cases {
		s, e := ZoneRange(c.h, c.zone)
		if s != c.wantS || e != c.wantE {
			t.Errorf("ZoneRange(%d,%d) = (%d,%d), want (%d,%d)", c.h, c.zone, s, e, c.wantS, c.wantE)
		}
	}
}

func fillPane(t *testing.T, tgt Target, target string, cmd string) {
	t.Helper()
	full := append([]string{"-S", tgt.Socket}, "send-keys", "-t", target, "-l", cmd)
	if out, err := exec.Command("tmux", full...).CombinedOutput(); err != nil {
		t.Fatalf("send-keys: %v: %s", err, out)
	}
	full = append([]string{"-S", tgt.Socket}, "send-keys", "-t", target, "Enter")
	if out, err := exec.Command("tmux", full...).CombinedOutput(); err != nil {
		t.Fatalf("send-keys Enter: %v: %s", err, out)
	}
	time.Sleep(1500 * time.Millisecond)
}

// The zone must be exactly the tail of the visible screen. `-S -N` is NOT the
// last N lines: it returns N lines of scrollback PLUS the whole screen
// (design.md §3), which is why the range is computed from pane_height.
func TestFetchZonesIsExactlyTheTail(t *testing.T) {
	tgt := testServer(t)
	fillPane(t, tgt, "one", "for i in $(seq 1 80); do echo LINE-$i; done")

	r := NewExec(10 * time.Second)
	ds, err := FetchDeltas(context.Background(), r, tgt)
	if err != nil {
		t.Fatalf("FetchDeltas: %v", err)
	}
	caps, err := FetchZones(context.Background(), r, tgt, ds)
	if err != nil {
		t.Fatalf("FetchZones: %v", err)
	}
	if len(caps) != len(ds) {
		t.Fatalf("got %d captures for %d panes", len(caps), len(ds))
	}
	if n := len(caps[0].Lines); n != ZoneLines {
		t.Fatalf("zone has %d lines, want %d", n, ZoneLines)
	}
	full, err := FetchFull(context.Background(), r, tgt, ds[0])
	if err != nil {
		t.Fatalf("FetchFull: %v", err)
	}
	want := full.Lines[len(full.Lines)-ZoneLines:]
	for i := range want {
		if strings.TrimRight(want[i], " ") != strings.TrimRight(caps[0].Lines[i], " ") {
			t.Fatalf("zone line %d = %q, want %q", i, caps[0].Lines[i], want[i])
		}
	}
}

// A pane whose own text reproduces the framing marker must not be able to
// confuse the demux. Framing is by declared length, so content cannot forge it.
func TestFetchZonesSurvivesForgedMarker(t *testing.T) {
	tgt := testServer(t)
	fillPane(t, tgt, "one", `printf '%s\n' "--TMUXHUB-0001--" "--TMUXHUB-0002--" "real line"`)

	r := NewExec(10 * time.Second)
	ds, err := FetchDeltas(context.Background(), r, tgt)
	if err != nil {
		t.Fatalf("FetchDeltas: %v", err)
	}
	caps, err := FetchZones(context.Background(), r, tgt, ds)
	if err != nil {
		t.Fatalf("FetchZones: %v", err)
	}
	if len(caps) != 1 || caps[0].PaneID != ds[0].PaneID {
		t.Fatalf("demux went wrong: %+v", caps)
	}
	if len(caps[0].Lines) != ZoneLines {
		t.Fatalf("zone has %d lines, want %d", len(caps[0].Lines), ZoneLines)
	}
}

// FetchFull is used for on-screen tiles. capture-pane with no -S emits exactly
// pane_height lines (design.md §3), which is the property the demux relies on.
func TestFetchFullEmitsExactlyPaneHeight(t *testing.T) {
	tgt := testServer(t)
	r := NewExec(10 * time.Second)
	ds, err := FetchDeltas(context.Background(), r, tgt)
	if err != nil {
		t.Fatalf("FetchDeltas: %v", err)
	}
	c, err := FetchFull(context.Background(), r, tgt, ds[0])
	if err != nil {
		t.Fatalf("FetchFull: %v", err)
	}
	if len(c.Lines) != ds[0].PaneHeight {
		t.Fatalf("full capture has %d lines, want pane_height %d", len(c.Lines), ds[0].PaneHeight)
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/tmux/ -run 'Zone|Fetch' -v`
Expected: FAIL — `undefined: ZoneRange`.

- [ ] **Step 3: Write the implementation**

Create `internal/tmux/capture.go`:

```go
package tmux

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

// ZoneLines is how many lines of a pane's tail carry its live state markers:
// the prompt box, and 'esc to interrupt' in the footer region. Measured on a
// real Claude Code pane (design.md §6).
const ZoneLines = 6

// Capture is one pane's screen text.
type Capture struct {
	PaneID string
	Lines  []string
	Height int  // pane_height as reported at capture time
	Stale  bool // the pane was resized between the delta and the capture
}

// ZoneRange converts a pane height into absolute capture bounds. `-S -N` cannot
// be used: it returns N lines of scrollback PLUS the entire visible screen.
func ZoneRange(paneHeight, zone int) (start, end int) {
	if paneHeight < 1 {
		paneHeight = 1
	}
	if zone > paneHeight {
		zone = paneHeight
	}
	return paneHeight - zone, paneHeight - 1
}

// FetchZones captures every pane's classification zone in one invocation.
// Each capture is preceded by a frame line declaring the pane id and the pane's
// current height, so a block can be attributed without a text marker (which a
// pane's own content can forge) and a mid-batch resize is detected.
func FetchZones(ctx context.Context, r Runner, t Target, ds []Delta) ([]Capture, error) {
	if len(ds) == 0 {
		return nil, nil
	}
	var args []string
	for _, d := range ds {
		if len(args) > 0 {
			args = append(args, ";")
		}
		s, e := ZoneRange(d.PaneHeight, ZoneLines)
		args = append(args,
			"display", "-p", "-t", d.PaneID, "#{pane_id} #{pane_height}", ";",
			"capture-pane", "-p", "-e", "-t", d.PaneID,
			"-S", strconv.Itoa(s), "-E", strconv.Itoa(e))
	}
	res, err := r.Run(ctx, t, args...)
	if err != nil {
		return nil, err
	}
	// A batch aborts at its first failing sub-command, so a non-zero RC means
	// the tail of the batch never ran. Parse what arrived and report the rest.
	caps, perr := demux(res.Stdout, ds)
	if perr != nil {
		return caps, fmt.Errorf("demux (rc=%d, stderr=%q): %w", res.RC, res.Stderr, perr)
	}
	return caps, nil
}

func demux(stdout string, ds []Delta) ([]Capture, error) {
	lines := strings.Split(stdout, "\n")
	var out []Capture
	pos := 0
	for _, d := range ds {
		if pos >= len(lines) {
			break // the batch aborted before reaching this pane
		}
		frame := strings.Fields(lines[pos])
		pos++
		if len(frame) != 2 {
			return out, fmt.Errorf("pane %s: bad frame line %q", d.PaneID, lines[pos-1])
		}
		if frame[0] != d.PaneID {
			return out, fmt.Errorf("frame out of order: got %s, want %s", frame[0], d.PaneID)
		}
		h, err := strconv.Atoi(frame[1])
		if err != nil {
			return out, fmt.Errorf("pane %s: bad height %q", d.PaneID, frame[1])
		}
		s, e := ZoneRange(d.PaneHeight, ZoneLines)
		want := e - s + 1
		if pos+want > len(lines) {
			return out, fmt.Errorf("pane %s: declared %d lines, only %d remain",
				d.PaneID, want, len(lines)-pos)
		}
		c := Capture{
			PaneID: d.PaneID,
			Lines:  lines[pos : pos+want],
			Height: h,
			Stale:  h != d.PaneHeight,
		}
		pos += want
		out = append(out, c)
	}
	return out, nil
}

// FetchFull captures a pane's whole visible screen, for an on-screen tile.
// capture-pane with no -S emits exactly pane_height lines.
func FetchFull(ctx context.Context, r Runner, t Target, d Delta) (Capture, error) {
	res, err := r.Run(ctx, t, "capture-pane", "-p", "-e", "-t", d.PaneID)
	if err != nil {
		return Capture{}, err
	}
	if res.RC != 0 {
		return Capture{}, fmt.Errorf("capture-pane rc=%d: %s", res.RC, res.Stderr)
	}
	lines := strings.Split(res.Stdout, "\n")
	// capture-pane's output ends with a newline, which Split turns into a
	// trailing empty element.
	if n := len(lines); n > 0 && lines[n-1] == "" {
		lines = lines[:n-1]
	}
	return Capture{PaneID: d.PaneID, Lines: lines, Height: d.PaneHeight}, nil
}
```

- [ ] **Step 4: Run to verify it passes**

Run: `go test ./internal/tmux/ -v`
Expected: PASS. If `TestFetchZonesIsExactlyTheTail` fails on line counts, print the raw batch output and check whether `capture-pane` emitted a trailing newline per block; adjust `demux`'s `want` accounting only after seeing the real bytes.

- [ ] **Step 5: Commit**

```bash
gofmt -l . && go test ./... && git add internal/tmux/capture.go internal/tmux/capture_test.go && git commit -m "feat(tmux): length-framed range capture

Captures are framed by a declared pane id and height rather than a text
marker, because a pane's own content can forge a marker — and the hub's own
use case puts its marker on screen. A test prints '--TMUXHUB-0001--' into a
pane and requires the demux to be unaffected.

The zone is computed from pane_height because '-S -6' is not the last six
lines: it returns six lines of scrollback plus the entire visible screen. A
test asserts the zone equals the tail of a full capture."
```

---

### Task 5: Connect-time required-field assertion

**Files:**
- Create: `internal/tmux/assert.go`
- Test: `internal/tmux/assert_test.go`

**Interfaces:**
- Consumes: `Runner`, `Target`, `DeltaFormat`.
- Produces:
  - `type FieldReport struct { Version string; Missing []string }`
  - `func AssertFields(ctx context.Context, r Runner, t Target) (FieldReport, error)`

- [ ] **Step 1: Write the failing test**

Create `internal/tmux/assert_test.go`:

```go
package tmux

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestAssertFieldsPassesOnRealServer(t *testing.T) {
	tgt := testServer(t)
	r := NewExec(10 * time.Second)
	rep, err := AssertFields(context.Background(), r, tgt)
	if err != nil {
		t.Fatalf("AssertFields: %v", err)
	}
	if len(rep.Missing) != 0 {
		t.Fatalf("Missing = %v, want none", rep.Missing)
	}
	if !strings.HasPrefix(rep.Version, "3.") && !strings.HasPrefix(rep.Version, "next-") {
		t.Fatalf("Version = %q, want something tmux-shaped", rep.Version)
	}
}

// tmux never errors on an unknown format variable: it returns an empty value
// with the field count intact (design.md §3). So a wrong field name can only be
// caught by asserting the value is non-empty. This test injects a bogus name and
// requires it to be reported by name.
func TestAssertFieldsNamesAMissingField(t *testing.T) {
	tgt := testServer(t)
	r := NewExec(10 * time.Second)
	rep, err := assertFieldsWith(context.Background(), r, tgt,
		[]string{"pane_id", "no_such_variable", "pane_height"})
	if err != nil {
		t.Fatalf("assertFieldsWith: %v", err)
	}
	if len(rep.Missing) != 1 || rep.Missing[0] != "no_such_variable" {
		t.Fatalf("Missing = %v, want [no_such_variable]", rep.Missing)
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/tmux/ -run AssertFields -v`
Expected: FAIL — `undefined: AssertFields`.

- [ ] **Step 3: Write the implementation**

Create `internal/tmux/assert.go`:

```go
package tmux

import (
	"context"
	"fmt"
	"strings"
)

// requiredFields are every structural field the hub depends on. They are checked
// by VALUE at connect, not declared in a per-version allowlist: tmux never errors
// on an unknown format variable, so a wrong or unsupported name silently yields
// an empty field with the count intact — and an empty window_activity parses as
// 0, i.e. every pane on that host reads as last active in 1970.
var requiredFields = []string{
	"pane_id",
	"window_activity",
	"history_size",
	"pane_dead",
	"window_width",
	"pane_height",
	"pane_pid",
	"session_name",
	"window_name",
	"pane_current_command",
	"version",
}

// FieldReport is the outcome of the connect-time assertion.
type FieldReport struct {
	Version string
	Missing []string // named, so the UI can say which field is absent
}

// AssertFields runs the hub's own fields against a real pane and requires each
// to come back non-empty.
func AssertFields(ctx context.Context, r Runner, t Target) (FieldReport, error) {
	return assertFieldsWith(ctx, r, t, requiredFields)
}

func assertFieldsWith(ctx context.Context, r Runner, t Target, fields []string) (FieldReport, error) {
	ds, err := FetchDeltas(ctx, r, t)
	if err != nil {
		return FieldReport{}, err
	}
	if len(ds) == 0 {
		return FieldReport{}, fmt.Errorf("no panes on %s: cannot verify fields", t.Label)
	}
	parts := make([]string, len(fields))
	for i, f := range fields {
		parts[i] = "#{" + f + "}"
	}
	res, err := r.Run(ctx, t, "display", "-p", "-t", ds[0].PaneID, strings.Join(parts, "|"))
	if err != nil {
		return FieldReport{}, err
	}
	if res.RC != 0 {
		return FieldReport{}, fmt.Errorf("display rc=%d: %s", res.RC, res.Stderr)
	}
	line := strings.TrimRight(res.Stdout, "\n")
	vals := strings.Split(line, "|")
	if len(vals) != len(fields) {
		return FieldReport{}, fmt.Errorf("assertion returned %d values for %d fields: %q",
			len(vals), len(fields), line)
	}
	rep := FieldReport{}
	for i, f := range fields {
		if strings.TrimSpace(vals[i]) == "" {
			rep.Missing = append(rep.Missing, f)
			continue
		}
		if f == "version" {
			rep.Version = vals[i]
		}
	}
	return rep, nil
}
```

- [ ] **Step 4: Run to verify it passes**

Run: `go test ./internal/tmux/ -run AssertFields -v`
Expected: PASS both tests.

- [ ] **Step 5: Commit**

```bash
gofmt -l . && go test ./... && git add internal/tmux/assert.go internal/tmux/assert_test.go && git commit -m "feat(tmux): verify format fields instead of declaring them

A per-version allowlist is unfalsifiable: tmux returns an empty value for an
unknown format variable with rc=0 and the field count intact, so a wrong entry
is indistinguishable from an empty one — and an empty window_activity parses as
0, making every pane read as last active in 1970. The assertion runs the hub's
own fields against a real pane and names any that come back empty."
```

---

### Task 6: Line kinds and display-width truncation

**Files:**
- Create: `internal/lines/lines.go`
- Test: `internal/lines/lines_test.go`

**Interfaces:**
- Consumes: nothing (pure).
- Produces:
  - `type Kind int` with `Blank, Rule, Prompt, Footer, Spinner, Content`
  - `func Classify(line string) Kind`
  - `func StripANSI(s string) string`
  - `func Width(s string) int`
  - `func Truncate(s string, cols int) string`
  - `func ContentTail(lines []string, n int) []string`

- [ ] **Step 1: Write the failing test**

Create `internal/lines/lines_test.go`:

```go
package lines

import (
	"strings"
	"testing"
)

func TestClassify(t *testing.T) {
	cases := []struct {
		in   string
		want Kind
	}{
		{"", Blank},
		{"   ", Blank},
		{strings.Repeat("─", 40), Rule},
		{"❯ ", Prompt},
		{"❯", Prompt},
		{"  ⏵⏵ bypass permissions on (shift+tab to cycle) · ← for agents", Footer},
		{"      new task? /clear to save 142k tokens", Footer},
		{"✻ Churned for 6s", Spinner},
		{"● Hi! What you need?", Content},
		{"⎿  ITER2-LOCAL-FIXED", Content},
		{"❯ echo hello", Content}, // a prompt WITH text typed is content
	}
	for _, c := range cases {
		if got := Classify(c.in); got != c.want {
			t.Errorf("Classify(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestClassifyIgnoresANSI(t *testing.T) {
	in := "\x1b[38;5;231m●\x1b[39m \x1b[1mBash\x1b[0m(echo hi)"
	if got := Classify(in); got != Content {
		t.Errorf("Classify(ansi content) = %v, want Content", got)
	}
}

func TestWidthCountsDisplayCells(t *testing.T) {
	if got := Width("abc"); got != 3 {
		t.Errorf("Width(abc) = %d, want 3", got)
	}
	// A CJK glyph occupies two cells.
	if got := Width("日本"); got != 4 {
		t.Errorf("Width(日本) = %d, want 4", got)
	}
	// ANSI escapes occupy none.
	if got := Width("\x1b[31mab\x1b[0m"); got != 2 {
		t.Errorf("Width(ansi ab) = %d, want 2", got)
	}
}

func TestTruncateNeverSplitsAnEscape(t *testing.T) {
	in := "\x1b[31mHELLO\x1b[0m world"
	got := Truncate(in, 4)
	if Width(got) > 4 {
		t.Fatalf("Truncate width = %d, want <= 4: %q", Width(got), got)
	}
	// The reset must be re-emitted so colour cannot bleed past the tile.
	if !strings.HasSuffix(got, "\x1b[0m") {
		t.Fatalf("Truncate(%q) = %q, want a trailing SGR reset", in, got)
	}
}

func TestContentTailDropsChrome(t *testing.T) {
	in := []string{
		"● first answer",
		"",
		"✻ Churned for 6s",
		strings.Repeat("─", 30),
		"❯",
		strings.Repeat("─", 30),
		"  ⏵⏵ bypass permissions on (shift+tab to cycle)",
		"      new task? /clear to save 1k tokens",
		"",
	}
	got := ContentTail(in, 3)
	want := []string{"● first answer", "✻ Churned for 6s"}
	if len(got) != len(want) {
		t.Fatalf("ContentTail = %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("ContentTail[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/lines/ -v`
Expected: FAIL — package does not exist.

- [ ] **Step 3: Write the implementation**

Create `internal/lines/lines.go`:

```go
// Package lines classifies a captured pane line as chrome or content, and
// measures and truncates by display width.
//
// A tile that renders "the last N lines" of a pane shows nothing useful: on a
// real 80x24 Claude Code pane only 6 of 25 lines are content, and the bottom 10
// are rule lines, an empty prompt box, a constant footer and blanks. Measured
// against four other pane kinds, the raw tile failed on every non-alt-screen
// pane for the same reason — once a command returns, the bottom of a shell pane
// is a prompt and blank space. See docs/design.md §6.
package lines

import (
	"regexp"
	"strings"
	"unicode"
)

type Kind int

const (
	Blank Kind = iota
	Rule
	Prompt
	Footer
	Spinner
	Content
)

func (k Kind) String() string {
	switch k {
	case Blank:
		return "blank"
	case Rule:
		return "rule"
	case Prompt:
		return "prompt"
	case Footer:
		return "footer"
	case Spinner:
		return "spinner"
	default:
		return "content"
	}
}

var (
	ansiRe = regexp.MustCompile(`\x1b\[[0-9;?]*[a-zA-Z]`)
	// A rule is a run of box-drawing or dash characters and nothing else.
	ruleRe = regexp.MustCompile(`^[\p{Pd}\x{2500}-\x{257F}_=]{8,}$`)
	// An empty prompt: a marker, optionally one cursor-ish glyph, nothing more.
	promptRe = regexp.MustCompile(`^[❯>\$#][\s\x{00a0}]*$`)
	// Footer text is Claude Code's own status furniture. Patterns live here for
	// now; they move to a config file when the classifier is calibrated on a
	// wider sample (docs/design.md §12 records that this stays a heuristic).
	footerRe = regexp.MustCompile(`(bypass permissions|shift\+tab|for agents|new task\?|/clear to save|esc to interrupt|\d+k tokens)`)
	spinRe   = regexp.MustCompile(`^[✻✽✢·*]\s`)
)

// StripANSI removes CSI escape sequences.
func StripANSI(s string) string { return ansiRe.ReplaceAllString(s, "") }

// Classify labels one captured line.
func Classify(line string) Kind {
	bare := StripANSI(line)
	trimmed := strings.TrimRight(bare, " \t")
	compact := strings.TrimSpace(strings.Map(func(r rune) rune {
		if r == ' ' {
			return ' '
		}
		return r
	}, trimmed))

	if compact == "" {
		return Blank
	}
	if ruleRe.MatchString(compact) {
		return Rule
	}
	if promptRe.MatchString(compact) {
		return Prompt
	}
	if footerRe.MatchString(compact) {
		return Footer
	}
	if spinRe.MatchString(compact) {
		return Spinner
	}
	return Content
}

// Width is the number of terminal cells a string occupies.
func Width(s string) int {
	n := 0
	for _, r := range StripANSI(s) {
		n += runeWidth(r)
	}
	return n
}

func runeWidth(r rune) int {
	switch {
	case r == ' ':
		return 1
	case unicode.Is(unicode.Mn, r), unicode.Is(unicode.Me, r), unicode.Is(unicode.Cf, r):
		return 0
	case wide(r):
		return 2
	default:
		return 1
	}
}

// wide covers the East Asian Wide and Fullwidth ranges the hub can encounter.
func wide(r rune) bool {
	switch {
	case r >= 0x1100 && r <= 0x115F, // Hangul Jamo
		r >= 0x2E80 && r <= 0xA4CF, // CJK radicals through Yi
		r >= 0xAC00 && r <= 0xD7A3, // Hangul syllables
		r >= 0xF900 && r <= 0xFAFF, // CJK compatibility
		r >= 0xFE30 && r <= 0xFE6F, // CJK forms
		r >= 0xFF00 && r <= 0xFF60, // fullwidth forms
		r >= 0xFFE0 && r <= 0xFFE6,
		r >= 0x1F300 && r <= 0x1F64F, // emoji blocks in common use
		r >= 0x20000 && r <= 0x3FFFD:
		return true
	}
	return false
}

const sgrReset = "\x1b[0m"

// Truncate cuts a line to cols display cells without splitting an escape
// sequence, and re-emits an SGR reset so colour cannot bleed past a tile edge.
func Truncate(s string, cols int) string {
	if cols <= 0 {
		return ""
	}
	var b strings.Builder
	sawEscape := false
	used := 0
	rs := []rune(s)
	for i := 0; i < len(rs); {
		if rs[i] == 0x1b {
			// Copy the whole escape sequence; it costs no cells.
			j := i + 1
			for j < len(rs) && !unicode.IsLetter(rs[j]) {
				j++
			}
			if j < len(rs) {
				j++
			}
			b.WriteString(string(rs[i:j]))
			sawEscape = true
			i = j
			continue
		}
		w := runeWidth(rs[i])
		if used+w > cols {
			break
		}
		b.WriteRune(rs[i])
		used += w
		i++
	}
	out := b.String()
	if sawEscape && !strings.HasSuffix(out, sgrReset) {
		out += sgrReset
	}
	return out
}

// ContentTail returns the last n content-bearing lines, chrome removed. Spinner
// lines are kept: "Churned for 6s" is information even though it is furniture.
func ContentTail(all []string, n int) []string {
	var keep []string
	for _, l := range all {
		switch Classify(l) {
		case Content, Spinner:
			keep = append(keep, strings.TrimRight(l, " \t"))
		}
	}
	if len(keep) > n {
		keep = keep[len(keep)-n:]
	}
	return keep
}
```

- [ ] **Step 4: Run to verify it passes**

Run: `go test ./internal/lines/ -v`
Expected: PASS. `TestClassify` may fail on `{"❯ echo hello", Content}` if `promptRe` is too greedy — the regex requires the line to be *only* the marker, so a marker plus text falls through to `Content`. Confirm that is what happens rather than adjusting the expectation.

- [ ] **Step 5: Commit**

```bash
gofmt -l . && go test ./... && git add internal/lines/ && git commit -m "feat(lines): chrome/content classifier and display-width truncation

A tile rendering the last N lines of a pane shows a box and a footer: only
6 of 25 lines of a real Claude pane are content and the bottom 10 are pure
chrome. Truncation is display-width aware and re-emits an SGR reset, because
a byte-wise cut lands inside an escape sequence and bleeds colour across the
rest of the screen."
```

---

### Task 7: The state classifier, on fixtures with both poles

**Files:**
- Create: `internal/state/state.go`
- Create: `internal/state/testdata/claude_idle.txt`
- Create: `internal/state/testdata/claude_needs.txt`
- Create: `internal/state/testdata/claude_works.txt`
- Create: `internal/state/testdata/shell_idle.txt`
- Test: `internal/state/state_test.go`

**Interfaces:**
- Consumes: `internal/lines`.
- Produces:
  - `type State int` with `Works, Needs, Idle, Quiet, Error, Gone`
  - `func (s State) Glyph() string`
  - `func (s State) Rank() int`
  - `type Input struct { Zone []string; ActivityAge time.Duration; Dead, Alt bool; Command string }`
  - `func Classify(in Input) State`
  - `const QuietAfter = 90 * time.Second`

- [ ] **Step 1: Create the fixtures**

`internal/state/testdata/claude_idle.txt` — an idle Claude pane's zone, six lines. Note the trailing marker line is the constant footer, and the spinner text present in scrollback must NOT make this read as working:

```
✻ Churned for 6s

────────────────────────────────────────
❯
────────────────────────────────────────
  ⏵⏵ bypass permissions on (shift+tab to cycle) · ← for agents
```

`internal/state/testdata/claude_needs.txt`:

```
● Ran 42 tests, 3 failed
Do you want to proceed?
  1. Yes
  2. No, tell Claude what to do differently
────────────────────────────────────────
❯
```

`internal/state/testdata/claude_works.txt` — the live marker is `esc to interrupt`:

```
● Bash(go build ./...)

────────────────────────────────────────
❯
────────────────────────────────────────
  ✻ Brewed for 46s · esc to interrupt
```

`internal/state/testdata/shell_idle.txt`:

```
ok   pkg/foo 0.4s
FAIL pkg/bar 1.2s  assertion failed


tmux-hub  main
❯
```

- [ ] **Step 2: Write the failing test**

Create `internal/state/state_test.go`:

```go
package state

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func zone(t *testing.T, name string) []string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	return strings.Split(strings.TrimRight(string(b), "\n"), "\n")
}

func TestClassifyBothPoles(t *testing.T) {
	cases := []struct {
		fixture string
		age     time.Duration
		want    State
	}{
		// A waiting pane MUST read Needs.
		{"claude_needs.txt", 5 * time.Second, Needs},
		// A working pane MUST NOT read Needs or Idle.
		{"claude_works.txt", 1 * time.Second, Works},
		// An idle pane MUST NOT read Works, even though its scrollback still
		// contains a spinner line.
		{"claude_idle.txt", 5 * time.Second, Idle},
		// The same idle pane, silent long enough, is Quiet.
		{"claude_idle.txt", 10 * time.Minute, Quiet},
		// A shell that printed a failure and returned to its prompt.
		{"shell_idle.txt", 5 * time.Second, Error},
	}
	for _, c := range cases {
		got := Classify(Input{Zone: zone(t, c.fixture), ActivityAge: c.age})
		if got != c.want {
			t.Errorf("Classify(%s, age=%v) = %v, want %v", c.fixture, c.age, got, c.want)
		}
	}
}

func TestSpinnerTextAloneIsNotWorking(t *testing.T) {
	// The decisive negative pole: 'Churned for 6s' and the glyph persist in an
	// idle pane's scrollback, so keying Works on them pins every finished
	// session as working and defeats the whole inbox.
	in := Input{Zone: []string{"✻ Churned for 6s", "", "❯"}, ActivityAge: time.Second}
	if got := Classify(in); got == Works {
		t.Fatal("spinner text alone must not read as Works")
	}
}

func TestDeadPaneIsError(t *testing.T) {
	in := Input{Zone: []string{"anything"}, Dead: true}
	if got := Classify(in); got != Error {
		t.Fatalf("dead pane = %v, want Error", got)
	}
}

func TestAltPaneIsNotClassifiedFromContent(t *testing.T) {
	// An alt-screen pane's capture is a full-screen app's rendering, which the
	// chrome/content classifier has no purchase on. It reports Works while its
	// activity is fresh, Quiet when it is not — never Needs or Error.
	in := Input{Zone: []string{"Do you want to proceed?"}, Alt: true, ActivityAge: time.Second}
	if got := Classify(in); got != Works {
		t.Fatalf("fresh alt pane = %v, want Works", got)
	}
	in.ActivityAge = 10 * time.Minute
	if got := Classify(in); got != Quiet {
		t.Fatalf("stale alt pane = %v, want Quiet", got)
	}
}

func TestRankOrdersTheInbox(t *testing.T) {
	order := []State{Needs, Quiet, Idle, Works, Error, Gone}
	for i := 1; i < len(order); i++ {
		if order[i-1].Rank() >= order[i].Rank() {
			t.Fatalf("%v must rank before %v", order[i-1], order[i])
		}
	}
}
```

- [ ] **Step 3: Run to verify it fails**

Run: `go test ./internal/state/ -v`
Expected: FAIL — package does not exist.

- [ ] **Step 4: Write the implementation**

Create `internal/state/state.go`:

```go
// Package state turns a pane's captured tail into one attention state.
//
// Classify is pure, and it is the only heuristic part of tmux-hub: Claude
// Code's screen is not an API. A wrong classification mis-sorts the inbox and
// can never cause a send, because sends require an explicit selection.
package state

import (
	"regexp"
	"strings"
	"time"

	"github.com/DawnBreather/tmux-hub/internal/lines"
)

type State int

const (
	Needs State = iota // waiting on the user
	Quiet              // silent for longer than QuietAfter
	Idle               // prompt present, nothing running
	Works              // actively producing output
	Error              // failed, or the pane is dead
	Gone               // the pane vanished between ticks
)

// QuietAfter is how long a pane must be silent before it is suspicious.
const QuietAfter = 90 * time.Second

func (s State) String() string {
	switch s {
	case Needs:
		return "needs"
	case Quiet:
		return "quiet"
	case Idle:
		return "idle"
	case Works:
		return "works"
	case Error:
		return "error"
	default:
		return "gone"
	}
}

// Glyph is the inbox marker.
func (s State) Glyph() string {
	switch s {
	case Needs:
		return "⚑"
	case Quiet:
		return "✱"
	case Idle:
		return "▸"
	case Works:
		return "·"
	case Error:
		return "✗"
	default:
		return "✝"
	}
}

// Rank is the inbox sort order: needs first, gone last.
func (s State) Rank() int { return int(s) }

// Input is everything Classify may look at.
type Input struct {
	Zone        []string // the bottom ZoneLines of the pane
	ActivityAge time.Duration
	Dead        bool
	Alt         bool
	Command     string
}

var (
	// The only reliable "working" marker: it is rendered live and disappears
	// when the work stops. The spinner glyph and "Churned for Ns" persist in
	// scrollback and must never be used for this.
	workingRe = regexp.MustCompile(`esc to interrupt`)
	// A question or a numbered choice awaiting an answer.
	needsRe = regexp.MustCompile(`(?i)(do you want to proceed|\bwould you like\b|^\s*1\.\s|\[y/n\]|\(y/n\)|press enter to)`)
	errorRe = regexp.MustCompile(`(?i)(^|\s)(FAIL|FAILED|ERROR|Traceback|panic:|fatal:)(\s|:|$)`)
)

// Classify maps one sample to a state. It works from a single sample so that a
// cold start has a defined answer: the markers come from the capture, and
// ActivityAge is an absolute age rather than a diff against a previous tick.
func Classify(in Input) State {
	if in.Dead {
		return Error
	}
	if in.Alt {
		// A full-screen app's rendering carries no chrome to strip, so only the
		// timestamp is meaningful.
		if in.ActivityAge > QuietAfter {
			return Quiet
		}
		return Works
	}

	bare := make([]string, 0, len(in.Zone))
	for _, l := range in.Zone {
		bare = append(bare, lines.StripANSI(l))
	}
	joined := strings.Join(bare, "\n")

	if workingRe.MatchString(joined) {
		return Works
	}
	if needsRe.MatchString(joined) {
		return Needs
	}
	if errorRe.MatchString(joined) {
		return Error
	}
	if in.ActivityAge > QuietAfter {
		return Quiet
	}
	return Idle
}
```

- [ ] **Step 5: Run to verify it passes**

Run: `go test ./internal/state/ -v`
Expected: PASS all six tests.

- [ ] **Step 6: Commit**

```bash
gofmt -l . && go test ./... && git add internal/state/ && git commit -m "feat(state): attention classifier with both poles asserted

Works keys on 'esc to interrupt', never on the spinner glyph or 'Churned
for Ns' — both persist in an idle pane's scrollback, so keying on them would
pin every finished session as working and defeat the inbox. A test asserts
exactly that negative pole.

Classify works from a single sample so a cold start has a defined answer:
markers come from the capture and the activity age is absolute, not a diff
against a tick the hub may not have."
```

---

### Task 8: The registry — merge, sort, and remember what vanished

**Files:**
- Create: `internal/registry/registry.go`
- Test: `internal/registry/registry_test.go`

**Interfaces:**
- Consumes: `internal/tmux` (`Delta`, `Labels`, `Capture`), `internal/state`.
- Produces:
  - `type Pane struct { Host, PaneID, Session, Window, Command string; State state.State; Zone, Content []string; Activity time.Time; Height, Width int; Alt bool; SeenAt time.Time }`
  - `type Registry struct { … }`
  - `func New() *Registry`
  - `func (r *Registry) Update(host string, ds []tmux.Delta, ls map[string]tmux.Labels, zones []tmux.Capture, fulls map[string]tmux.Capture, now time.Time)`
  - `func (r *Registry) Panes() []Pane`

**Why two capture sets.** The classification zone is the *tail* of a pane, and on an idle Claude
pane the tail is chrome by construction — measured on the live pane, the bottom six lines are
`rule / ❯ / rule / footer / footer / blank`, so stripping chrome from the zone yields **nothing**.
Content lives above the chrome. So `zones` (every pane, cheap) drives classification and `fulls`
(on-screen tiles only, ~3.6 KB each) drives `Content`. When no full capture is supplied for a
pane, its previous `Content` is retained — which is also what lets a `Gone` pane carry a last
screen.

- [ ] **Step 1: Write the failing test**

Create `internal/registry/registry_test.go`:

```go
package registry

import (
	"testing"
	"time"

	"github.com/DawnBreather/tmux-hub/internal/state"
	"github.com/DawnBreather/tmux-hub/internal/tmux"
)

func sample(now time.Time) ([]tmux.Delta, map[string]tmux.Labels, []tmux.Capture, map[string]tmux.Capture) {
	ds := []tmux.Delta{
		{PaneID: "%0", Activity: now.Unix(), PaneHeight: 24, WindowWidth: 80, PanePID: 1},
		{PaneID: "%1", Activity: now.Add(-10 * time.Minute).Unix(), PaneHeight: 24, WindowWidth: 80, PanePID: 2},
	}
	ls := map[string]tmux.Labels{
		"%0": {Session: "live1", Window: "w0", Command: "claude"},
		"%1": {Session: "work", Window: "w1", Command: "bash"},
	}
	zones := []tmux.Capture{
		{PaneID: "%0", Lines: []string{"● Ran tests", "Do you want to proceed?", "❯"}, Height: 24},
		{PaneID: "%1", Lines: []string{"● done", "❯"}, Height: 24},
	}
	fulls := map[string]tmux.Capture{
		"%0": {PaneID: "%0", Height: 24, Lines: []string{"● Ran tests", "Do you want to proceed?", "❯"}},
		"%1": {PaneID: "%1", Height: 24, Lines: []string{"● done", "❯"}},
	}
	return ds, ls, zones, fulls
}

func TestUpdateSortsByAttention(t *testing.T) {
	now := time.Unix(1786450000, 0)
	r := New()
	ds, ls, zones, fulls := sample(now)
	r.Update("local", ds, ls, zones, fulls, now)

	got := r.Panes()
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
	if got[0].State != state.Needs {
		t.Fatalf("first pane state = %v, want Needs", got[0].State)
	}
	if got[1].State != state.Quiet {
		t.Fatalf("second pane state = %v, want Quiet", got[1].State)
	}
	if got[0].Session != "live1" || got[0].Command != "claude" {
		t.Fatalf("labels not merged: %+v", got[0])
	}
}

// A pane that disappears must not vanish from the list: its last screen is the
// only remaining evidence of why it died, because tmux destroys a pane and its
// scrollback together.
func TestVanishedPaneBecomesGoneAndKeepsItsLastScreen(t *testing.T) {
	now := time.Unix(1786450000, 0)
	r := New()
	ds, ls, zones, fulls := sample(now)
	r.Update("local", ds, ls, zones, fulls, now)

	// %1 disappears on the next tick.
	r.Update("local", ds[:1], map[string]tmux.Labels{"%0": ls["%0"]},
		zones[:1], map[string]tmux.Capture{"%0": fulls["%0"]}, now.Add(time.Second))

	var gone Pane
	var found bool
	for _, p := range r.Panes() {
		if p.PaneID == "%1" {
			gone, found = p, true
		}
	}
	if !found {
		t.Fatal("%1 dropped out of the registry entirely")
	}
	if gone.State != state.Gone {
		t.Fatalf("%%1 state = %v, want Gone", gone.State)
	}
	if len(gone.Content) == 0 {
		t.Fatal("%1 lost its last screen, which is the only evidence it left")
	}
}

func TestContentIsChromeStripped(t *testing.T) {
	now := time.Unix(1786450000, 0)
	r := New()
	ds, ls, zones, _ := sample(now)
	fulls := map[string]tmux.Capture{
		"%0": {PaneID: "%0", Height: 24, Lines: []string{
			"● the answer",
			"────────────────────────────────",
			"❯",
			"  ⏵⏵ bypass permissions on (shift+tab to cycle)",
		}},
		"%1": {PaneID: "%1", Height: 24, Lines: []string{"● other"}},
	}
	r.Update("local", ds, ls, zones, fulls, now)
	for _, p := range r.Panes() {
		for _, l := range p.Content {
			if l == "❯" {
				t.Fatalf("chrome leaked into Content: %q", p.Content)
			}
		}
	}
}

// The defect this signature exists to prevent: the classification zone of an
// idle Claude pane is entirely chrome, so deriving Content from it leaves the
// tile empty for exactly the panes that matter. Measured on the live pane.
func TestZoneAloneYieldsNoContentButAFullCaptureDoes(t *testing.T) {
	now := time.Unix(1786450000, 0)
	idleZone := []tmux.Capture{{PaneID: "%0", Height: 24, Lines: []string{
		"────────────────────────────────",
		"❯",
		"────────────────────────────────",
		"  ⏵⏵ bypass permissions on (shift+tab to cycle)",
		"      new task? /clear to save 142k tokens",
		"",
	}}}
	ds := []tmux.Delta{{PaneID: "%0", Activity: now.Unix(), PaneHeight: 24, WindowWidth: 80}}
	ls := map[string]tmux.Labels{"%0": {Session: "live1", Command: "claude"}}

	r := New()
	r.Update("local", ds, ls, idleZone, nil, now)
	if got := r.Panes()[0].Content; len(got) != 0 {
		t.Fatalf("zone-only Content = %q, want empty — the zone is chrome", got)
	}

	full := map[string]tmux.Capture{"%0": {PaneID: "%0", Height: 24, Lines: []string{
		"● Hi! What you need?",
		"",
		"────────────────────────────────",
		"❯",
	}}}
	r.Update("local", ds, ls, idleZone, full, now.Add(time.Second))
	if got := r.Panes()[0].Content; len(got) != 1 || got[0] != "● Hi! What you need?" {
		t.Fatalf("full-capture Content = %q, want the answer line", got)
	}

	// Content must survive a tick with no full capture, so a tile that scrolls
	// off screen does not go blank and a Gone pane keeps its last screen.
	r.Update("local", ds, ls, idleZone, nil, now.Add(2*time.Second))
	if got := r.Panes()[0].Content; len(got) != 1 {
		t.Fatalf("Content = %q after a tick with no full capture, want it retained", got)
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/registry/ -v`
Expected: FAIL — package does not exist.

- [ ] **Step 3: Write the implementation**

Create `internal/registry/registry.go`:

```go
// Package registry merges every host's panes into one ordered view.
//
// It performs no I/O: Update takes what the tmux package fetched and returns a
// sorted snapshot, so the whole attention model is table-testable.
package registry

import (
	"sort"
	"time"

	"github.com/DawnBreather/tmux-hub/internal/lines"
	"github.com/DawnBreather/tmux-hub/internal/state"
	"github.com/DawnBreather/tmux-hub/internal/tmux"
)

// ContentLines is how many content-bearing lines a tile keeps.
const ContentLines = 10

// Pane is one pane as the UI sees it.
type Pane struct {
	Host    string
	PaneID  string
	Session string
	Window  string
	Command string

	State   state.State
	Zone    []string // the captured tail, as captured
	Content []string // chrome stripped, newest last

	Activity time.Time
	Height   int
	Width    int
	Alt      bool
	SeenAt   time.Time // the last tick this pane was present
}

// Key identifies a pane across ticks. A pane id is unique only within one
// server's lifetime, so the host is part of the key.
type Key struct {
	Host   string
	PaneID string
}

type Registry struct {
	panes  map[Key]*Pane
	sorted []Pane
}

func New() *Registry {
	return &Registry{panes: map[Key]*Pane{}}
}

// Update replaces one host's panes.
//
// zones are the cheap classification captures, taken for every pane. fulls are
// whole-screen captures, taken only for panes whose tile is on screen — they are
// the ONLY source of Content, because the zone is a pane's tail and on an idle
// Claude pane the tail is chrome by construction (rule, empty prompt, rule,
// footer, footer, blank), so stripping chrome from it yields nothing. A pane with
// no full capture this tick keeps the Content it had.
//
// Panes that were present before and are absent now become Gone and keep their
// last screen: tmux destroys a pane and its scrollback together, so the hub's own
// cache is the only remaining evidence of why it died.
func (r *Registry) Update(host string, ds []tmux.Delta, ls map[string]tmux.Labels, zones []tmux.Capture, fulls map[string]tmux.Capture, now time.Time) {
	byID := make(map[string]tmux.Capture, len(zones))
	for _, c := range zones {
		byID[c.PaneID] = c
	}

	present := make(map[Key]bool, len(ds))
	for _, d := range ds {
		k := Key{Host: host, PaneID: d.PaneID}
		present[k] = true

		p := r.panes[k]
		if p == nil {
			p = &Pane{Host: host, PaneID: d.PaneID}
			r.panes[k] = p
		}
		l := ls[d.PaneID]
		p.Session, p.Window, p.Command = l.Session, l.Window, l.Command
		p.Activity = time.Unix(d.Activity, 0)
		p.Height, p.Width, p.Alt = d.PaneHeight, d.WindowWidth, d.Alt
		p.SeenAt = now

		if c, ok := byID[d.PaneID]; ok {
			p.Zone = c.Lines
		}
		if c, ok := fulls[d.PaneID]; ok {
			p.Content = lines.ContentTail(c.Lines, ContentLines)
		}
		p.State = state.Classify(state.Input{
			Zone:        p.Zone,
			ActivityAge: now.Sub(p.Activity),
			Dead:        d.Dead,
			Alt:         d.Alt,
			Command:     p.Command,
		})
	}

	for k, p := range r.panes {
		if k.Host == host && !present[k] {
			p.State = state.Gone
		}
	}
	r.resort()
}

func (r *Registry) resort() {
	out := make([]Pane, 0, len(r.panes))
	for _, p := range r.panes {
		out = append(out, *p)
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.State.Rank() != b.State.Rank() {
			return a.State.Rank() < b.State.Rank()
		}
		if a.Host != b.Host {
			return a.Host < b.Host
		}
		if a.Session != b.Session {
			return a.Session < b.Session
		}
		return a.PaneID < b.PaneID
	})
	r.sorted = out
}

// Panes returns the current snapshot, sorted by attention then host, session,
// pane id.
func (r *Registry) Panes() []Pane { return r.sorted }
```

- [ ] **Step 4: Run to verify it passes**

Run: `go test ./internal/registry/ -v`
Expected: PASS all four tests, including `TestZoneAloneYieldsNoContentButAFullCaptureDoes`, which is the one that pins down why `Update` takes two capture sets.

Note for a reader who wonders why the `Gone` test copies the value instead of taking a pointer: `r.Panes()` builds a fresh slice on every call, so `&r.Panes()[i]` would address a temporary.

- [ ] **Step 5: Commit**

```bash
gofmt -l . && go test ./... && git add internal/registry/ && git commit -m "feat(registry): merge panes, sort by attention, remember the vanished

A pane whose process exits is destroyed with its scrollback before the next
poll, so the registry caches every pane's last screen and turns an absent
pane into Gone rather than dropping it. That replaces remain-on-exit, which
is a window option (so it cannot cover a session) and leaves zombie windows
in sessions the hub merely observes."
```

---

### Task 9: The poller — one tick, local host only

**Files:**
- Create: `internal/hub/poll.go`
- Test: `internal/hub/poll_test.go`

**Interfaces:**
- Consumes: `internal/tmux`, `internal/registry`.
- Produces:
  - `type Host struct { Label, Socket string; Status Status; Reason string; Version string }`
  - `type Status int` with `Connecting, Up, UpEmpty, DegradedFormat, Down`
  - `type Poller struct { … }`
  - `func NewPoller(r tmux.Runner, reg *registry.Registry) *Poller`
  - `func (p *Poller) Add(h Host)`
  - `func (p *Poller) AddLocal() Host`
  - `func (p *Poller) Tick(ctx context.Context, now time.Time, wantFull map[string]bool) []Host`

`wantFull` is the set of `registry.MarkKey`-style `host\x00paneID` identities whose tile is on
screen. Only those get a full-screen capture; everything else gets the cheap zone. Pass `nil` for
a zone-only tick.

- [ ] **Step 1: Write the failing test**

Create `internal/hub/poll_test.go`:

```go
package hub

import (
	"context"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/DawnBreather/tmux-hub/internal/registry"
	"github.com/DawnBreather/tmux-hub/internal/tmux"
)

func liveTarget(t *testing.T) tmux.Target {
	t.Helper()
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	sock := filepath.Join(t.TempDir(), "tmux.sock")
	cmd := exec.Command("tmux", "-S", sock, "-f", "/dev/null",
		"new-session", "-d", "-s", "one", "-x", "80", "-y", "24")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("new-session: %v: %s", err, out)
	}
	t.Cleanup(func() { _ = exec.Command("tmux", "-S", sock, "kill-server").Run() })
	return tmux.Target{Label: "test", Socket: sock}
}

func TestTickPopulatesTheRegistry(t *testing.T) {
	tgt := liveTarget(t)
	reg := registry.New()
	p := NewPoller(tmux.NewExec(10*time.Second), reg)
	p.Add(Host{Label: tgt.Label, Socket: tgt.Socket})

	hosts := p.Tick(context.Background(), time.Now(), nil)
	if len(hosts) != 1 {
		t.Fatalf("len(hosts) = %d, want 1", len(hosts))
	}
	if hosts[0].Status != Up {
		t.Fatalf("status = %v, reason = %q, want Up", hosts[0].Status, hosts[0].Reason)
	}
	if hosts[0].Version == "" {
		t.Fatal("Version is empty; the field assertion did not run")
	}
	if len(reg.Panes()) != 1 {
		t.Fatalf("registry has %d panes, want 1", len(reg.Panes()))
	}
}

// A socket with no server must report Down with a reason a person can act on,
// never an empty status and never a crash.
func TestTickOnAbsentServerIsDownWithAReason(t *testing.T) {
	reg := registry.New()
	p := NewPoller(tmux.NewExec(5*time.Second), reg)
	p.Add(Host{Label: "ghost", Socket: filepath.Join(t.TempDir(), "nope.sock")})

	hosts := p.Tick(context.Background(), time.Now(), nil)
	if hosts[0].Status != Down {
		t.Fatalf("status = %v, want Down", hosts[0].Status)
	}
	if hosts[0].Reason == "" {
		t.Fatal("Down with no reason: a status without a remedy is a bug report to the wrong person")
	}
}

// The poll path must never mutate. This asserts it at the argument level: no
// command the poller issues may be one of tmux's mutating verbs.
func TestPollPathIsPure(t *testing.T) {
	tgt := liveTarget(t)
	rec := &recordingRunner{inner: tmux.NewExec(10 * time.Second)}
	reg := registry.New()
	p := NewPoller(rec, reg)
	p.Add(Host{Label: tgt.Label, Socket: tgt.Socket})
	p.Tick(context.Background(), time.Now(), nil)

	mutating := map[string]bool{
		"send-keys": true, "set": true, "set-option": true, "resize-window": true,
		"resize-pane": true, "new-session": true, "start-server": true,
		"kill-session": true, "kill-pane": true, "kill-server": true,
		"load-buffer": true, "paste-buffer": true, "set-buffer": true,
		"rename-session": true, "rename-window": true, "set-hook": true,
	}
	if len(rec.calls) == 0 {
		t.Fatal("no calls recorded")
	}
	for _, call := range rec.calls {
		for _, a := range call {
			if mutating[a] {
				t.Errorf("poll path issued a mutating command %q in %v", a, call)
			}
		}
	}
}

type recordingRunner struct {
	inner tmux.Runner
	calls [][]string
}

func (r *recordingRunner) Run(ctx context.Context, t tmux.Target, args ...string) (tmux.Result, error) {
	r.calls = append(r.calls, append([]string(nil), args...))
	return r.inner.Run(ctx, t, args...)
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/hub/ -v`
Expected: FAIL — package does not exist.

- [ ] **Step 3: Write the implementation**

Create `internal/hub/poll.go`:

```go
// Package hub drives the poll loop.
//
// Everything here is read-only. A pane is never written to, no option is set,
// and no server is started — including as a side effect of probing, which in an
// earlier design started a LOCAL tmux server on the forward socket and answered
// as if it were the remote (docs/design.md §5).
package hub

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/DawnBreather/tmux-hub/internal/registry"
	"github.com/DawnBreather/tmux-hub/internal/tmux"
)

type Status int

const (
	Connecting Status = iota
	Up
	UpEmpty        // reachable, no tmux server
	DegradedFormat // a required format field came back empty
	Down
)

func (s Status) String() string {
	switch s {
	case Connecting:
		return "connecting"
	case Up:
		return "up"
	case UpEmpty:
		return "up-empty"
	case DegradedFormat:
		return "degraded:format"
	default:
		return "down"
	}
}

// Host is one tmux server the hub watches, plus its status. Status is always a
// positive assertion with a reason; absence of an error is never a status.
type Host struct {
	Label   string
	Socket  string
	Status  Status
	Reason  string
	Version string
}

type Poller struct {
	run    tmux.Runner
	reg    *registry.Registry
	hosts  []Host
	probed map[string]bool // label -> the field assertion has passed
}

func NewPoller(r tmux.Runner, reg *registry.Registry) *Poller {
	return &Poller{run: r, reg: reg, probed: map[string]bool{}}
}

// Add registers a host to poll.
func (p *Poller) Add(h Host) {
	h.Status = Connecting
	p.hosts = append(p.hosts, h)
}

// AddLocal registers the local server, whose socket is derived rather than
// discovered so that even the first call carries an explicit -S.
func (p *Poller) AddLocal() Host {
	h := Host{Label: "local", Socket: tmux.LocalSocket(), Status: Connecting}
	p.hosts = append(p.hosts, h)
	return h
}

// Tick polls every host once and returns their statuses. wantFull holds
// "host\x00paneID" identities whose tile is on screen; only those get a
// full-screen capture, because that is the only source of tile content and it
// costs about 3.6 KB per pane against the zone's 650 B.
func (p *Poller) Tick(ctx context.Context, now time.Time, wantFull map[string]bool) []Host {
	for i := range p.hosts {
		p.tickHost(ctx, &p.hosts[i], now, wantFull)
	}
	out := make([]Host, len(p.hosts))
	copy(out, p.hosts)
	return out
}

func (p *Poller) tickHost(ctx context.Context, h *Host, now time.Time, wantFull map[string]bool) {
	tgt := tmux.Target{Label: h.Label, Socket: h.Socket}

	ds, err := tmux.FetchDeltas(ctx, p.run, tgt)
	if err != nil {
		h.Status, h.Reason = Down, reasonFor(err)
		return
	}
	if len(ds) == 0 {
		h.Status = UpEmpty
		h.Reason = "no tmux server here — start one, or leave this host off"
		return
	}

	if !p.probed[h.Label] {
		rep, err := tmux.AssertFields(ctx, p.run, tgt)
		if err != nil {
			h.Status, h.Reason = Down, reasonFor(err)
			return
		}
		if len(rep.Missing) > 0 {
			h.Status = DegradedFormat
			h.Reason = fmt.Sprintf("tmux %s returned nothing for: %s",
				rep.Version, strings.Join(rep.Missing, ", "))
			return
		}
		h.Version = rep.Version
		p.probed[h.Label] = true
	}

	ls, err := tmux.FetchLabels(ctx, p.run, tgt)
	if err != nil {
		h.Status, h.Reason = Down, reasonFor(err)
		return
	}
	zones, err := tmux.FetchZones(ctx, p.run, tgt, ds)
	if err != nil {
		h.Status, h.Reason = Down, reasonFor(err)
		return
	}

	// Full captures only for on-screen tiles. The zone is a pane's tail and on an
	// idle Claude pane the tail is chrome, so Content can only come from here.
	fulls := map[string]tmux.Capture{}
	for _, d := range ds {
		if !wantFull[h.Label+"\x00"+d.PaneID] {
			continue
		}
		c, err := tmux.FetchFull(ctx, p.run, tgt, d)
		if err != nil {
			// One unreadable pane must not fail the whole host: the pane may have
			// died between the delta and the capture, which the registry will see
			// as Gone on the next tick.
			continue
		}
		fulls[d.PaneID] = c
	}

	p.reg.Update(h.Label, ds, ls, zones, fulls, now)
	h.Status, h.Reason = Up, ""
}

// reasonFor turns a failure into a sentence naming the remedy. The strings tmux
// and the OS produce are matched loosely on purpose: the classification must not
// depend on an exact message, and an unrecognised failure falls through with its
// own text rather than being flattened.
func reasonFor(err error) string {
	s := err.Error()
	switch {
	case strings.Contains(s, "no server running"):
		return "no tmux server on that socket — start one, or leave this host off"
	case strings.Contains(s, "error connecting"), strings.Contains(s, "No such file"):
		return "socket is not there — the tunnel is down or was never built"
	case strings.Contains(s, "deadline exceeded"):
		return "tmux did not answer in time — the link is stalled, retrying"
	case strings.Contains(s, "protocol version mismatch"):
		return "tmux version mismatch — this host needs the per-call fallback"
	default:
		return s
	}
}
```

- [ ] **Step 4: Run to verify it passes**

Run: `go test ./internal/hub/ -v`
Expected: PASS all three tests, including `TestPollPathIsPure`.

- [ ] **Step 5: Commit**

```bash
gofmt -l . && go test ./... && git add internal/hub/ && git commit -m "feat(hub): the poll loop, with purity asserted

Host status is always a positive assertion carrying a remedy, never the
absence of an error, and TestPollPathIsPure walks every argument the poller
issued and fails if any is a mutating tmux verb — so 'observing cannot change
a host' is a test rather than a claim."
```

---

### Task 10: `status --json`

**Files:**
- Create: `internal/hub/report.go`
- Create: `cmd/tmux-hub/main.go`
- Test: `internal/hub/report_test.go`

**Interfaces:**
- Consumes: `Poller`, `Host`, `registry.Registry`.
- Produces:
  - `type Report struct { Hosts []HostReport; Panes []PaneReport }`
  - `func BuildReport(hosts []Host, panes []registry.Pane) Report`
  - a binary that prints the report as JSON and exits.

- [ ] **Step 1: Write the failing test**

Create `internal/hub/report_test.go`:

```go
package hub

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/DawnBreather/tmux-hub/internal/registry"
	"github.com/DawnBreather/tmux-hub/internal/state"
)

func TestBuildReportIsMachineShaped(t *testing.T) {
	hosts := []Host{{Label: "local", Status: Up, Version: "3.7b"}}
	panes := []registry.Pane{{
		Host: "local", PaneID: "%0", Session: "live1", Window: "w0", Command: "claude",
		State: state.Needs, Activity: time.Unix(1786450000, 0),
		Content: []string{"● Ran tests", "Do you want to proceed?"},
	}}
	rep := BuildReport(hosts, panes)
	b, err := json.Marshal(rep)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	s := string(b)
	for _, want := range []string{`"label":"local"`, `"status":"up"`, `"state":"needs"`, `"pane_id":"%0"`} {
		if !strings.Contains(s, want) {
			t.Errorf("report JSON missing %s: %s", want, s)
		}
	}
}

func TestBuildReportStripsANSI(t *testing.T) {
	panes := []registry.Pane{{
		Host: "local", PaneID: "%0", State: state.Idle,
		Content: []string{"\x1b[38;5;231m●\x1b[39m plain answer"},
	}}
	rep := BuildReport(nil, panes)
	got := rep.Panes[0].Content[0]
	if strings.Contains(got, "\x1b") {
		t.Fatalf("report content = %q, want no escape sequences", got)
	}
	if got != "● plain answer" {
		t.Fatalf("report content = %q, want the text intact", got)
	}
}

func TestBuildReportCarriesTheHostReason(t *testing.T) {
	hosts := []Host{{Label: "ghost", Status: Down, Reason: "socket is not there"}}
	rep := BuildReport(hosts, nil)
	if rep.Hosts[0].Reason == "" {
		t.Fatal("a Down host must carry its reason into the report")
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/hub/ -run BuildReport -v`
Expected: FAIL — `undefined: BuildReport`.

- [ ] **Step 3: Write the implementation**

Create `internal/hub/report.go`:

```go
package hub

import (
	"github.com/DawnBreather/tmux-hub/internal/lines"
	"github.com/DawnBreather/tmux-hub/internal/registry"
)

// HostReport and PaneReport are the machine-readable shape of one poll cycle.
// This is the read path with a different renderer, so it inherits the poll
// path's purity for free. Broadcast is deliberately absent: reading is safe to
// automate, writing into a live agent is not (docs/design.md §16).
type HostReport struct {
	Label   string `json:"label"`
	Status  string `json:"status"`
	Reason  string `json:"reason,omitempty"`
	Version string `json:"version,omitempty"`
}

type PaneReport struct {
	Host     string   `json:"host"`
	PaneID   string   `json:"pane_id"`
	Session  string   `json:"session"`
	Window   string   `json:"window"`
	Command  string   `json:"command"`
	State    string   `json:"state"`
	Activity int64    `json:"activity_unix"`
	Content  []string `json:"content,omitempty"`
}

type Report struct {
	Hosts []HostReport `json:"hosts"`
	Panes []PaneReport `json:"panes"`
}

func BuildReport(hosts []Host, panes []registry.Pane) Report {
	r := Report{}
	for _, h := range hosts {
		r.Hosts = append(r.Hosts, HostReport{
			Label: h.Label, Status: h.Status.String(), Reason: h.Reason, Version: h.Version,
		})
	}
	for _, p := range panes {
		// The JSON renderer strips ANSI: a monitor or a shell prompt consuming
		// this wants text, and escape sequences would be noise it has to undo.
		// The TUI keeps the escapes, because it is rendering to a terminal.
		content := make([]string, 0, len(p.Content))
		for _, l := range p.Content {
			content = append(content, lines.StripANSI(l))
		}
		r.Panes = append(r.Panes, PaneReport{
			Host: p.Host, PaneID: p.PaneID, Session: p.Session, Window: p.Window,
			Command: p.Command, State: p.State.String(),
			Activity: p.Activity.Unix(), Content: content,
		})
	}
	return r
}
```

Create `cmd/tmux-hub/main.go`:

```go
// Command tmux-hub is a read-only control panel over local tmux sessions.
//
// This build has no broadcast, no attach and no remote hosts: read-only value
// ships first (docs/design.md §15).
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/DawnBreather/tmux-hub/internal/hub"
	"github.com/DawnBreather/tmux-hub/internal/registry"
	"github.com/DawnBreather/tmux-hub/internal/tmux"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "status" {
		if err := runStatus(); err != nil {
			fmt.Fprintln(os.Stderr, "tmux-hub:", err)
			os.Exit(1)
		}
		return
	}
	fmt.Fprintln(os.Stderr, "usage: tmux-hub status [--json]")
	os.Exit(2)
}

func runStatus() error {
	reg := registry.New()
	p := hub.NewPoller(tmux.NewExec(5*time.Second), reg)
	p.AddLocal()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	// First tick discovers the panes; the second asks for a full capture of each
	// so the report carries content. Two round trips is free for a one-shot
	// command, and a monitor wants the content.
	p.Tick(ctx, time.Now(), nil)
	want := map[string]bool{}
	for _, pn := range reg.Panes() {
		want[pn.Host+"\x00"+pn.PaneID] = true
	}
	hosts := p.Tick(ctx, time.Now(), want)
	rep := hub.BuildReport(hosts, reg.Panes())

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(rep)
}
```

- [ ] **Step 4: Run to verify it passes**

Run: `go test ./internal/hub/ -v && go build ./... && go run ./cmd/tmux-hub status`
Expected: tests PASS; the binary prints JSON describing the local server. If the local server has no sessions the host reads `up-empty` with its reason, which is correct behaviour, not a failure.

- [ ] **Step 5: Verify it against the real local server**

Run: `go run ./cmd/tmux-hub status | head -40`
Expected: `"label":"local"`, `"status":"up"`, and one `panes` entry per pane on your own tmux server, each with a `state`. Sanity-check one state against what that pane is actually doing.

- [ ] **Step 6: Commit**

```bash
gofmt -l . && go test ./... && git add internal/hub/report.go cmd/ internal/hub/report_test.go && git commit -m "feat(cmd): status --json, the read path with a different renderer

Scriptable for monitoring without opening the write path. Broadcast stays
interactive-only and the asymmetry is deliberate: reading is safe to
automate, writing into a live agent is exactly what must keep a human and an
on-screen target in the loop."
```

---

### Task 11: The TUI — pane inbox, content tiles, width thresholds

**Files:**
- Create: `internal/ui/render.go`
- Create: `internal/ui/model.go`
- Modify: `cmd/tmux-hub/main.go`
- Test: `internal/ui/render_test.go`

**Interfaces:**
- Consumes: `registry.Pane`, `hub.Host`, `internal/lines`.
- Produces:
  - `type Layout int` with `InboxOnly, InboxOneTile, InboxGrid`
  - `func LayoutFor(width int) Layout`
  - `const MaxTileWidth = 72`
  - `func RenderInbox(panes []registry.Pane, width, height int, cursor int, marked map[string]bool, inlineHostSession bool) []string`
  - `func RenderTile(p registry.Pane, width, height int) []string`
  - `func Render(panes []registry.Pane, hosts []hub.Host, width, height, cursor int, marked map[string]bool) string`
  - `func Run(ctx context.Context) error` — the bubbletea program

- [ ] **Step 1: Write the failing test**

Create `internal/ui/render_test.go`:

```go
package ui

import (
	"strings"
	"testing"
	"time"

	"github.com/DawnBreather/tmux-hub/internal/lines"
	"github.com/DawnBreather/tmux-hub/internal/registry"
	"github.com/DawnBreather/tmux-hub/internal/state"
)

func samplePanes() []registry.Pane {
	return []registry.Pane{
		{Host: "local", PaneID: "%0", Session: "live1", Command: "claude", State: state.Needs,
			Content:  []string{"● Ran 42 tests, 3 failed", "Do you want to proceed?"},
			Activity: time.Unix(1786450000, 0)},
		{Host: "nuc", PaneID: "%7", Session: "work", Command: "claude", State: state.Works,
			Content:  []string{"● Bash(go build ./...)", "✻ Brewed for 46s"},
			Activity: time.Unix(1786450010, 0)},
	}
}

func TestLayoutForWidth(t *testing.T) {
	cases := []struct {
		w    int
		want Layout
	}{{80, InboxOnly}, {99, InboxOnly}, {100, InboxOneTile}, {159, InboxOneTile}, {160, InboxGrid}}
	for _, c := range cases {
		if got := LayoutFor(c.w); got != c.want {
			t.Errorf("LayoutFor(%d) = %v, want %v", c.w, got, c.want)
		}
	}
}

// At 80 columns, per-session header rows cost more than they give: six panes
// across five sessions spent 5 of 11 body rows on headers. So the host and
// session go inline on the pane row (docs/design.md §16).
func TestNarrowInboxPutsHostSessionInline(t *testing.T) {
	rows := RenderInbox(samplePanes(), 80, 10, 0, nil, true)
	joined := strings.Join(rows, "\n")
	if strings.Contains(joined, "LOCAL LIVE1") {
		t.Fatal("narrow layout must not spend a row on a session header")
	}
	if !strings.Contains(rows[0], "live1") || !strings.Contains(rows[0], "%0") {
		t.Fatalf("row 0 = %q, want host/session and pane id inline", rows[0])
	}
}

func TestEveryRenderedLineFitsTheWidth(t *testing.T) {
	for _, w := range []int{60, 80, 120, 200} {
		out := Render(samplePanes(), nil, w, 20, 0, nil)
		for i, l := range strings.Split(out, "\n") {
			if got := lines.Width(l); got > w {
				t.Errorf("width %d: line %d is %d cells: %q", w, i, got, l)
			}
		}
	}
}

// Surplus width must become tile columns, not a wider tile: at 160 columns a
// naive split produced a 130-column tile holding 30-column content.
func TestTileWidthIsBounded(t *testing.T) {
	rows := RenderTile(samplePanes()[0], 200, 6)
	for _, l := range rows {
		if got := lines.Width(l); got > MaxTileWidth {
			t.Fatalf("tile line is %d cells, want <= %d: %q", got, MaxTileWidth, l)
		}
	}
}

func TestTileRendersContentNotChrome(t *testing.T) {
	p := samplePanes()[0]
	rows := RenderTile(p, 60, 6)
	joined := strings.Join(rows, "\n")
	if !strings.Contains(joined, "Ran 42 tests") {
		t.Fatalf("tile lost its content: %q", joined)
	}
}

func TestMarkedPaneIsVisiblyMarked(t *testing.T) {
	marked := map[string]bool{"local\x00%0": true}
	rows := RenderInbox(samplePanes(), 80, 10, 0, marked, true)
	if !strings.Contains(rows[0], "◆") {
		t.Fatalf("row 0 = %q, want a mark glyph", rows[0])
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/ui/ -v`
Expected: FAIL — package does not exist.

- [ ] **Step 3: Write the renderer**

Create `internal/ui/render.go`:

```go
// Package ui renders the dashboard. Layout thresholds and the tile width bound
// come from a prototype rendered against real pane content (docs/design.md §16
// and prototypes/layout.py), which corrected two assertions made without it.
package ui

import (
	"fmt"
	"strings"

	"github.com/DawnBreather/tmux-hub/internal/hub"
	"github.com/DawnBreather/tmux-hub/internal/lines"
	"github.com/DawnBreather/tmux-hub/internal/registry"
)

type Layout int

const (
	InboxOnly Layout = iota
	InboxOneTile
	InboxGrid
)

func (l Layout) String() string {
	switch l {
	case InboxOnly:
		return "inbox-only"
	case InboxOneTile:
		return "inbox+tile"
	default:
		return "inbox+grid"
	}
}

const (
	// InboxWidth is the inbox column when tiles share the screen.
	InboxWidth = 28
	// MaxTileWidth bounds a tile so surplus width becomes more columns rather
	// than a 130-column tile holding 30-column content.
	MaxTileWidth = 72
)

// LayoutFor picks a layout for a terminal width. 80x24 is the size to hold, not
// a degraded case.
func LayoutFor(width int) Layout {
	switch {
	case width < 100:
		return InboxOnly
	case width < 160:
		return InboxOneTile
	default:
		return InboxGrid
	}
}

// MarkKey is the identity used for selection: a pane id is unique only within
// one server, so the host is part of it.
func MarkKey(p registry.Pane) string { return p.Host + "\x00" + p.PaneID }

// RenderInbox renders the pane list. When inlineHostSession is set the host and
// session share the pane's row instead of taking a header row of their own.
func RenderInbox(panes []registry.Pane, width, height, cursor int, marked map[string]bool, inlineHostSession bool) []string {
	var rows []string
	lastGroup := ""
	for i, p := range panes {
		if len(rows) >= height {
			break
		}
		mark := " "
		if marked[MarkKey(p)] {
			mark = "◆"
		}
		point := " "
		if i == cursor {
			point = ">"
		}
		if inlineHostSession {
			row := fmt.Sprintf("%s%s%s %s %s/%s %s",
				point, mark, p.State.Glyph(), p.PaneID, p.Host, p.Session, p.State)
			rows = append(rows, lines.Truncate(row, width))
			continue
		}
		group := p.Host + " " + p.Session
		if group != lastGroup {
			rows = append(rows, lines.Truncate(strings.ToUpper(group), width))
			lastGroup = group
			if len(rows) >= height {
				break
			}
		}
		row := fmt.Sprintf("%s%s%s %-4s %-8s %s",
			point, mark, p.State.Glyph(), p.PaneID, p.Command, p.State)
		rows = append(rows, lines.Truncate(row, width))
	}
	for len(rows) < height {
		rows = append(rows, "")
	}
	return rows
}

// RenderTile renders one pane's content lines inside a box, bounded by
// MaxTileWidth.
func RenderTile(p registry.Pane, width, height int) []string {
	w := width
	if w > MaxTileWidth {
		w = MaxTileWidth
	}
	if w < 8 || height < 3 {
		return nil
	}
	inner := w - 2
	head := fmt.Sprintf("─ %s %s %s ", p.Host, p.Session, p.PaneID)
	if lines.Width(head) > inner {
		head = lines.Truncate(head, inner)
	}
	top := "┌" + head + strings.Repeat("─", inner-lines.Width(head)) + "┐"

	body := p.Content
	if n := height - 2; len(body) > n {
		body = body[len(body)-n:]
	}
	rows := []string{top}
	for _, l := range body {
		t := lines.Truncate(l, inner)
		rows = append(rows, "│"+t+strings.Repeat(" ", inner-lines.Width(t))+"│")
	}
	for len(rows) < height-1 {
		rows = append(rows, "│"+strings.Repeat(" ", inner)+"│")
	}
	rows = append(rows, "└"+strings.Repeat("─", inner)+"┘")
	return rows
}

// Render composes the whole screen.
func Render(panes []registry.Pane, hosts []hub.Host, width, height, cursor int, marked map[string]bool) string {
	if width < 20 || height < 4 {
		return "terminal too small"
	}
	layout := LayoutFor(width)
	bodyH := height - 2

	var out []string
	out = append(out, lines.Truncate(fmt.Sprintf("tmux-hub  %d panes", len(panes)), width))

	switch layout {
	case InboxOnly:
		out = append(out, RenderInbox(panes, width, bodyH, cursor, marked, true)...)
	default:
		inbox := RenderInbox(panes, InboxWidth, bodyH, cursor, marked, false)
		tiles := renderTileColumn(panes, marked, cursor, width-InboxWidth-1, bodyH)
		for i := 0; i < bodyH; i++ {
			left := ""
			if i < len(inbox) {
				left = inbox[i]
			}
			right := ""
			if i < len(tiles) {
				right = tiles[i]
			}
			row := left + strings.Repeat(" ", max(0, InboxWidth-lines.Width(left))) + " " + right
			out = append(out, lines.Truncate(row, width))
		}
	}

	out = append(out, lines.Truncate(hostLine(hosts, marked), width))
	return strings.Join(out, "\n")
}

func renderTileColumn(panes []registry.Pane, marked map[string]bool, cursor, width, height int) []string {
	sel := selected(panes, marked, cursor)
	var rows []string
	for _, p := range sel {
		if len(rows) >= height {
			break
		}
		h := min(8, height-len(rows))
		rows = append(rows, RenderTile(p, width, h)...)
	}
	return rows
}

func selected(panes []registry.Pane, marked map[string]bool, cursor int) []registry.Pane {
	var sel []registry.Pane
	for _, p := range panes {
		if marked[MarkKey(p)] {
			sel = append(sel, p)
		}
	}
	if len(sel) == 0 && cursor < len(panes) {
		sel = append(sel, panes[cursor])
	}
	return sel
}

func hostLine(hosts []hub.Host, marked map[string]bool) string {
	var parts []string
	for _, h := range hosts {
		s := h.Label + " " + h.Status.String()
		if h.Reason != "" {
			s += " (" + h.Reason + ")"
		}
		parts = append(parts, s)
	}
	line := strings.Join(parts, " · ")
	if n := len(marked); n > 0 {
		line += fmt.Sprintf("   → %d marked", n)
	}
	return line
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
```

- [ ] **Step 4: Run the renderer tests**

Run: `go test ./internal/ui/ -v`
Expected: PASS. `TestEveryRenderedLineFitsTheWidth` is the one most likely to fail; if it does, the padding arithmetic in `Render` is over-counting — fix the padding, not the assertion.

- [ ] **Step 5: Add the bubbletea model**

Create `internal/ui/model.go`:

```go
package ui

import (
	"context"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/DawnBreather/tmux-hub/internal/hub"
	"github.com/DawnBreather/tmux-hub/internal/registry"
	"github.com/DawnBreather/tmux-hub/internal/tmux"
)

// PollInterval is the tick period. The floor exists because a tick costs one
// round trip per host; on the local socket a full poll is a couple of
// milliseconds.
const PollInterval = 1200 * time.Millisecond

type tickMsg struct {
	hosts []hub.Host
	panes []registry.Pane
}

type model struct {
	poller *hub.Poller
	reg    *registry.Registry

	hosts  []hub.Host
	panes  []registry.Pane
	cursor int
	marked map[string]bool

	width, height int
	ctx           context.Context
}

// Run starts the dashboard. The first paint happens before any poll completes,
// so a usable screen is on display immediately (docs/design.md §16).
func Run(ctx context.Context) error {
	reg := registry.New()
	p := hub.NewPoller(tmux.NewExec(5*time.Second), reg)
	p.AddLocal()

	m := model{poller: p, reg: reg, marked: map[string]bool{}, ctx: ctx, width: 80, height: 24}
	_, err := tea.NewProgram(m, tea.WithAltScreen()).Run()
	return err
}

func (m model) Init() tea.Cmd { return m.poll() }

func (m model) poll() tea.Cmd {
	// Only panes whose tile is on screen get a full-screen capture; everything
	// else gets the cheap classification zone.
	want := map[string]bool{}
	for k := range m.marked {
		want[k] = true
	}
	if len(want) == 0 && m.cursor < len(m.panes) {
		want[MarkKey(m.panes[m.cursor])] = true
	}
	return func() tea.Msg {
		hosts := m.poller.Tick(m.ctx, time.Now(), want)
		return tickMsg{hosts: hosts, panes: m.reg.Panes()}
	}
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil

	case tickMsg:
		m.hosts, m.panes = msg.hosts, msg.panes
		if m.cursor >= len(m.panes) {
			m.cursor = max(0, len(m.panes)-1)
		}
		return m, tea.Tick(PollInterval, func(time.Time) tea.Msg { return pollNow{} })

	case pollNow:
		return m, m.poll()

	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c":
			return m, tea.Quit
		case "j", "down":
			if m.cursor < len(m.panes)-1 {
				m.cursor++
			}
		case "k", "up":
			if m.cursor > 0 {
				m.cursor--
			}
		case " ":
			if m.cursor < len(m.panes) {
				k := MarkKey(m.panes[m.cursor])
				if m.marked[k] {
					delete(m.marked, k)
				} else {
					m.marked[k] = true
				}
			}
		}
	}
	return m, nil
}

type pollNow struct{}

func (m model) View() string {
	return Render(m.panes, m.hosts, m.width, m.height, m.cursor, m.marked)
}
```

- [ ] **Step 6: Wire the binary**

Replace `cmd/tmux-hub/main.go` **entirely** with this — the whole file, so nothing has to be
reconciled against the Task 10 version:

```go
// Command tmux-hub is a read-only control panel over local tmux sessions.
//
// This build has no broadcast, no attach and no remote hosts: read-only value
// ships first (docs/design.md §15).
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/DawnBreather/tmux-hub/internal/hub"
	"github.com/DawnBreather/tmux-hub/internal/registry"
	"github.com/DawnBreather/tmux-hub/internal/tmux"
	"github.com/DawnBreather/tmux-hub/internal/ui"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "status" {
		if err := runStatus(); err != nil {
			fmt.Fprintln(os.Stderr, "tmux-hub:", err)
			os.Exit(1)
		}
		return
	}
	if err := ui.Run(context.Background()); err != nil {
		fmt.Fprintln(os.Stderr, "tmux-hub:", err)
		os.Exit(1)
	}
}

func runStatus() error {
	reg := registry.New()
	p := hub.NewPoller(tmux.NewExec(5*time.Second), reg)
	p.AddLocal()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	// First tick discovers the panes; the second asks for a full capture of each
	// so the report carries content. Two round trips is free for a one-shot
	// command, and a monitor wants the content.
	p.Tick(ctx, time.Now(), nil)
	want := map[string]bool{}
	for _, pn := range reg.Panes() {
		want[pn.Host+"\x00"+pn.PaneID] = true
	}
	hosts := p.Tick(ctx, time.Now(), want)
	rep := hub.BuildReport(hosts, reg.Panes())

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(rep)
}
```

- [ ] **Step 7: Add the dependencies and build**

```bash
go get github.com/charmbracelet/bubbletea@v1.3.10
go get github.com/charmbracelet/lipgloss@v1.1.0
go mod tidy
go build ./...
```

- [ ] **Step 8: Run the tests and the binary**

Run: `go test ./... && go run ./cmd/tmux-hub`
Expected: tests PASS; the dashboard appears, `j`/`k` move, `space` marks, `q` quits. Verify against your own tmux server that a pane you know is idle reads `idle`.

- [ ] **Step 9: Commit**

```bash
gofmt -l . && go test ./... && git add internal/ui/ cmd/ go.mod go.sum && git commit -m "feat(ui): pane inbox, content tiles, measured width thresholds

At 80 columns the host and session go inline on the pane row: a prototype
showed per-session header rows spending 5 of 11 body rows on headers with 12
rows left empty. Tile width is bounded at 72 columns so surplus width becomes
more tiles rather than a 130-column tile holding 30-column content.

A test walks every rendered line at four widths and fails if any exceeds the
terminal, since a byte-wise cut in ANSI content bleeds colour across the
screen."
```

---

### Task 12: README and the developer safety rule

**Files:**
- Create: `README.md`
- Create: `CONTRIBUTING.md`
- Test: `internal/tmux/guard_test.go`

**Interfaces:**
- Consumes: everything above.
- Produces: a repo-level guard test that fails if any source file builds a tmux command without a socket or names a forbidden format variable.

- [ ] **Step 1: Write the failing guard test**

Create `internal/tmux/guard_test.go`:

```go
package tmux

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// No source file may name a format variable that can crash a server, and no
// file may construct a tmux command without an explicit socket. This is the
// repo-level form of the two invariants Validate enforces at runtime.
func TestNoSourceFileNamesAForbiddenFormat(t *testing.T) {
	root := filepath.Join("..", "..")
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		switch filepath.Base(path) {
		case "guard_test.go", "run_test.go", "run.go":
			// run.go DEFINES the forbidden list and documents why; the two test
			// files exercise it. Every other file is scanned.
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, f := range forbiddenVars {
			// The violation is a FORMAT reference, not a mention: the name
			// inside #{...} is what would reach tmux.
			if strings.Contains(string(b), "#{"+f) {
				t.Errorf("%s references #{%s}, which segfaults a tmux 3.2a server", path, f)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
}
```

- [ ] **Step 2: Run it, then calibrate it on both poles**

Run: `go test ./internal/tmux/ -run TestNoSourceFileNames -v`
Expected: PASS.

A guard that has never gone red is not a guard, so plant a violation and require it to fail:

```bash
cat > internal/lines/zz_probe.go <<'EOF'
package lines

// deliberate violation for calibration
const probe = "#{client_activity}"
EOF
go test ./internal/tmux/ -run TestNoSourceFileNames    # must FAIL, naming zz_probe.go
rm internal/lines/zz_probe.go
go test ./internal/tmux/ -run TestNoSourceFileNames    # must PASS again
```

Expected: FAIL with `internal/lines/zz_probe.go references #{client_activity}`, then PASS after removal. Both poles verified in the plan's own sandbox build.

- [ ] **Step 3: Write the README**

Create `README.md`:

```markdown
# tmux-hub

A control panel over your tmux sessions, built for orchestrating many Claude Code
sessions at once.

This build is **read-only and local**: it shows every pane on your local tmux
server as a list sorted by which one needs you, with tiles showing what each pane
actually said. It does not send input, attach, or reach remote hosts yet — see
`docs/design.md` §15 for the sequence.

## Install

    go build -o tmux-hub ./cmd/tmux-hub

## Use

    ./tmux-hub                 # the dashboard
    ./tmux-hub status          # one poll cycle as JSON, for scripts and monitors

Keys: `j`/`k` move, `space` marks, `q` quits.

## What the states mean

| | |
|---|---|
| ⚑ needs | waiting on you |
| ✱ quiet | silent for over 90s |
| ▸ idle | finished, prompt empty |
| · works | producing output |
| ✗ error | failed, or the pane is dead |
| ✝ gone | the pane vanished; its last screen is kept |

## Design

`docs/design.md` is the spec, and §3 is worth reading before changing anything:
it records measurements against live tmux 3.7b and 3.2a where the obvious
implementation is wrong.
```

- [ ] **Step 4: Write CONTRIBUTING**

Create `CONTRIBUTING.md`:

```markdown
# Contributing

## Two rules that are not style preferences

**1. Every tmux invocation carries an explicit socket.**

A tmux command with no `-S`/`-L` talks to *your own live tmux server*. During this
project's design phase a probe run against a live server destroyed two
long-running sessions. `tmux.Runner` refuses an empty socket, and tests use
`t.TempDir()` sockets that they kill in `t.Cleanup`.

Never target the session `live1` or the default socket from a test or a script.

**2. Never name `#{client_activity}` or `#{client_created}`.**

On tmux 3.2a, querying either with no client attached **segfaults the entire
server**, taking every session with it. No guard helps: `#{q:...}`, `#{?...}`,
`x#{...}y` and `#{t:...}` all crash. `TestNoSourceFileNamesAForbiddenFormat`
enforces this at the repo level.

## A third, subtler one

**No literal `%` in a tmux argument unless the whole argument is a pane id.**

`display -p` runs its argument through `strftime`, so `display -p 'OK-%2'` returns
an **empty string with rc=0**. Emit identity through the format layer:
`display -p -t %2 'OK #{pane_id}'`. `Validate` enforces this.

## Tests

    go test ./...
    gofmt -l .        # must be empty

Tests that need a real tmux skip cleanly when it is absent. `classify()` fixtures
require **both poles**: a waiting pane must read `needs` and a working pane must
not.
```

- [ ] **Step 5: Commit**

```bash
gofmt -l . && go test ./... && git add README.md CONTRIBUTING.md internal/tmux/guard_test.go && git commit -m "docs: README, contributing rules, and a repo-level guard

The two rules that are not style preferences get a test rather than a
paragraph: no source file may name a format variable that segfaults a tmux
3.2a server, and Runner refuses a command without an explicit socket."
```

---

## Self-Review

**Spec coverage for this plan's scope (§15 steps 1–2 plus `status --json`):**

| design requirement | task |
|---|---|
| one `Run()` seam, host state a pure function of the result | 1 |
| no `client_*` emitted, ever | 1, 12 |
| no literal `%` in a display template | 1, 12 |
| every invocation carries an explicit socket | 1, 12 |
| per-call deadline so a stall cannot freeze the UI | 1 |
| free-text-free delta format, bounded parse | 2 |
| labels with one trailing free-text field each | 3 |
| zone computed from `pane_height`, not `-S -N` | 4 |
| captures framed by declared length, not a marker | 4 |
| mid-batch resize detected (`Stale`) | 4 |
| required-field assertion naming the missing field | 5 |
| chrome/content classifier; display-width truncation with SGR reset | 6 |
| `classify()` pure, both poles, `esc to interrupt` not the spinner | 7 |
| cold start defined from a single sample | 7 |
| vanished pane → `gone` with its last screen (no `remain-on-exit`) | 8 |
| inbox is panes, sorted by attention | 8, 11 |
| host status a positive assertion with a remedy | 9 |
| poll path pure — asserted, not claimed | 9 |
| `status --json`, read path with another renderer | 10 |
| 80×24 layout with inline host/session; bounded tile width | 11 |
| first paint before any poll completes | 11 |

**Deferred to later plans, deliberately:** the ssh host agent and its identity assertion, socket reconciliation, `flock`, attach and the resize warning, broadcast in every form (token stamp, guarded send, hex/paste primitives, per-target confirmation, history), the host picker and `hosts.toml`, `--watch`, out-of-band `needs` notification, and the delta gate for large fleets. None is reachable from this plan's code, because nothing here can write to a pane.

**Type consistency:** `tmux.Target/Result/Runner/Delta/Labels/Capture` are defined in Tasks 1–4 and used unchanged in 5, 9. `state.State/Input/Classify` from Task 7 is used in 8. `registry.Pane` from Task 8 is used in 10 and 11. `hub.Host/Status` from Task 9 is used in 10 and 11. `lines.Truncate/Width/ContentTail/StripANSI` from Task 6 is used in 8 and 11.

**Known rough edges the implementer will hit, called out so they are not mistaken for bugs:** `RunRaw` is reachable only through a type assertion (Task 1 Step 4 note); `demux`'s line accounting may need one adjustment against real bytes (Task 4 Step 4 note); the `TestVanishedPane` loop must copy rather than address (Task 8 Step 4 note); `TestClassify`'s prompt-with-text case depends on `promptRe` anchoring (Task 6 Step 4 note).
