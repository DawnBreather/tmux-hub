# tmux-hub Transport Supervision Implementation Plan

> **SUPERSEDED, 2026-08-13, and not by a change of mind — by a measurement.**
> This plan forwards the remote tmux socket. Measured against a real host, a poll
> cycle costs **698 ms** that way against **327 ms** as one ssh invocation over the
> master, and the master is required for `attach` regardless. Six of this plan's
> nine tasks exist only to keep a forwarded socket honest — the dial classifier,
> `SO_PEERCRED` identity, adopt/repair/reap reconciliation, `ExitOnForwardFailure`
> handling, the `start-server` squatter defence and remote socket path derivation —
> and none of those failure modes has anywhere to happen once there is no local
> socket file. See `docs/plans/2026-08-13-hosts-and-transport.md` and §5.
>
> Kept, not deleted: its Tasks 2 (one hub at a time), 5 (where hosts come from) and
> 7 (the picker) carry test fixtures and reasoning the new plan builds on, and a
> design that erases the record of what it rejected invites the rejection back.

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** The hub owns its own ssh. Remote hosts come from `hosts.toml`, their tunnels are spawned, adopted, repaired and reaped by the hub, and `--host` stops being necessary.

**Architecture:** One `tunnel` package owns an ssh master's whole life — spawn with the measured flags, wait on the fact, classify a failure with `transport.Dial`, back off, and reap by a persisted pid. One `hostset` package turns `~/.ssh/config` plus `hosts.toml` into the host list the poller already consumes. Reconciliation at startup is the same code as a normal connect, and a `flock` makes one hub the only writer of that state.

**Tech Stack:** Go 1.26.1, stdlib only. No new dependencies — TOML is read with a ~60-line parser for the three key shapes this file uses, because pulling a dependency for `enabled = true` is not worth the supply chain.

**Design source:** `docs/design.md` §5 (transport), §9 (hosts and config), §12 (the standing capability), §16 (value before infrastructure). Read §3's Transport table first: **six of its claims were wrong when written and were corrected by measurement**, and three of the corrections are load-bearing here.

## Global Constraints

- Go version **1.26.1**; module `github.com/DawnBreather/tmux-hub`; stdlib only.
- Every tmux invocation goes through `tmux.Runner` and carries an explicit socket. Nothing in this plan runs a bare `tmux`.
- **`ExitOnForwardFailure=yes` is mandatory on every spawn.** Measured: without it ssh stays alive with a dead forward, and `tmux -S <that path> ls` then answers `no server running` — the same words as a reachable host whose tmux is not started. A silently forwardless master is the worst state available.
- **Never unlink a socket before probing it.** Measured: unlinking a *live* forwarded socket orphans its ssh — the process survives, the path becomes undialable, and a new tunnel binds it while the old one runs forever. Unconditional unlinking leaks one ssh per restart.
- **A spawn that succeeds proves nothing.** Measured: `ssh -N -M -L … github.com` authenticated and sat there past 40 s with a tunnel to nowhere. Health is only ever `transport.Dial` plus a tmux answer.
- Host membership keys on **stdout matching `^tmux \S+`**, never on an exit code. Measured: `studio-ws` returns rc=0 with no tmux installed.
- `$XDG_RUNTIME_DIR` must be asserted set and absolute. Measured: unset yields the **relative** path `tmux-hub`, so the hub would create sockets in whatever directory it started in and appear to work until run from elsewhere.
- Tests never touch the developer's own tmux server or ssh hosts: sockets under `t.TempDir()`, and a **fake `ssh` binary on `PATH`** for lifecycle tests.
- `gofmt -l .` empty and `go test -race ./...` clean before every commit.
- **Never call `poller.Add` from a goroutine.** `Poller.hosts` is unguarded and
  `Tick` hands out `&p.hosts[i]`, so an `Add` from a background goroutine both races
  that read and can reallocate the slice under a running tick. Everything in Task 6
  therefore *returns* hosts and lets the model register them on its own thread.

---

### Task 0: The dial classifier becomes a leaf package

A prerequisite refactor with no new behaviour, and it exists because the obvious
arrangement does not compile. `tunnel` needs the dial classifier to know when a
forward is usable, and `hub` needs `tunnel` to supervise one — so
`tunnel → hub → tunnel` is an import cycle. Injecting a readiness predicate into
`tunnel` would break the cycle and would also move the measured rule ("accepted
**and held**") out of the package whose tests depend on it, leaving those tests
proving something weaker. Moving the classifier to a leaf both packages import
keeps one implementation and one rule.

**Files:**
- Create: `internal/transport/transport.go` (moved from `internal/hub/dial.go`)
- Create: `internal/transport/transport_test.go` (moved from `internal/hub/dial_test.go`)
- Delete: `internal/hub/dial.go`, `internal/hub/dial_test.go`
- Modify: `internal/hub/poll.go:79` (the `Peer` field's type), `:333`, `:338`, `:341`

**Interfaces:**
- Produces:
  - `type Kind int` with `Absent`, `Empty`, `Live`
  - `type Peer struct { … }`
  - `func Dial(path string, wait time.Duration) (Kind, Peer, error)`

- [ ] **Step 1: Move the files, renaming only the exported names**

```bash
mkdir -p internal/transport
git mv internal/hub/dial.go internal/transport/transport.go
git mv internal/hub/dial_test.go internal/transport/transport_test.go
```

Then in both files change `package hub` to `package transport`, and rename:
`Transport` → `Kind`, `TransportAbsent` → `Absent`, `TransportEmpty` → `Empty`,
`TransportLive` → `Live`, `PeerInfo` → `Peer`. `Dial` keeps its name — it does not
stutter as `transport.Dial`.

Keep every comment verbatim. They record what each classification was measured to
mean, and that is the whole value of the file.

- [ ] **Step 2: Carry the test helper over**

`dial_test.go`'s most valuable test — the one that dials a REAL tmux server —
calls `liveTarget`, which is defined in `poll_test.go` and stays behind in `hub`.
The move therefore does not compile until the helper comes too. Append this to
`internal/transport/transport_test.go` and change that test's two lines to use it
(`sock := liveTMUXSocket(t)`, `Dial(sock, …)`):

```go
// liveTMUXSocket starts a private tmux server and returns its socket path. It is
// a copy of the hub test's helper trimmed to what this package needs: hub's
// version returns a tmux.Target, and transport only ever wants a path. The
// duplication is deliberate — sharing it would mean a testing-only package
// imported by two others to save twelve lines.
func liveTMUXSocket(t *testing.T) string {
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
	return sock
}
```

- [ ] **Step 3: Update the four call sites in `poll.go`**

`Host.Peer` becomes `transport.Peer`; `Dial(h.Socket, …)` becomes
`transport.Dial(h.Socket, …)`; `tr == TransportAbsent` becomes `tr == transport.Absent`;
`tr == TransportEmpty` becomes `tr == transport.Empty`. Add the import.

- [ ] **Step 4: Verify it is a pure move**

Run: `go build ./... && go test -race ./...`
Expected: PASS, with the same test count as before. `TestHappyPathDoesNotPayForADial`
in `poll_test.go` still passes without modification — it asserts timing, not names.

If any test needed its *assertions* changed rather than its identifiers, the move
was not pure and something was rewritten by accident. Revert and redo it.

- [ ] **Step 5: Commit**

```bash
gofmt -l . && go test -race ./... && git add -A && git commit -m "refactor: the dial classifier becomes a leaf package

No behaviour change. The supervisor needs tunnel and tunnel needs the dial
classifier, so leaving it in hub makes tunnel -> hub -> tunnel a cycle.

The alternative — injecting a readiness predicate into tunnel — would break the
cycle and also move the measured rule (a dial accepted AND held, never the
socket file appearing) out of the package whose tests rest on it, leaving those
tests proving something weaker. One implementation in a leaf both import keeps
the rule where it is checked."
```

---

### Task 1: The runtime directory, asserted rather than assumed

**Files:**
- Create: `internal/tunnel/paths.go`
- Test: `internal/tunnel/paths_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces:
  - `func RuntimeDir() (string, error)`
  - `func SocketLabel(alias string) string`
  - `func Paths(rt, alias string) (sock, ctl, pid string)`
  - `var ErrNoRuntimeDir, ErrUnsafeRuntimeDir error`

- [ ] **Step 1: Write the failing test**

Create `internal/tunnel/paths_test.go`:

```go
package tunnel

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Measured: with XDG_RUNTIME_DIR unset, filepath.Join("", "tmux-hub") is the
// RELATIVE path "tmux-hub", so the hub would put sockets in whatever directory
// it started in and work until run from somewhere else.
func TestRuntimeDirRefusesAnUnsetVariable(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", "")
	_, err := RuntimeDir()
	if !errors.Is(err, ErrNoRuntimeDir) {
		t.Fatalf("RuntimeDir with an unset variable = %v, want ErrNoRuntimeDir", err)
	}
	if err != nil && !strings.Contains(err.Error(), "--runtime-dir") {
		t.Errorf("the error must name the remedy, got %q", err)
	}
}

func TestRuntimeDirRefusesARelativePath(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", "relative/path")
	if _, err := RuntimeDir(); !errors.Is(err, ErrNoRuntimeDir) {
		t.Fatalf("a relative XDG_RUNTIME_DIR = %v, want ErrNoRuntimeDir", err)
	}
}

// mkdir -p -m 0700 does NOT change an existing directory's mode and follows a
// symlink, so the mode is asserted rather than set.
func TestRuntimeDirRefusesASymlink(t *testing.T) {
	base := t.TempDir()
	real := filepath.Join(base, "real")
	if err := os.Mkdir(real, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("cannot symlink here: %v", err)
	}
	t.Setenv("XDG_RUNTIME_DIR", link)
	if _, err := RuntimeDir(); !errors.Is(err, ErrUnsafeRuntimeDir) {
		t.Fatalf("a symlinked runtime dir = %v, want ErrUnsafeRuntimeDir", err)
	}
}

func TestRuntimeDirRefusesAGroupReadableDir(t *testing.T) {
	base := t.TempDir()
	if err := os.Chmod(base, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_RUNTIME_DIR", base)
	if _, err := RuntimeDir(); !errors.Is(err, ErrUnsafeRuntimeDir) {
		t.Fatalf("a 0755 runtime dir = %v, want ErrUnsafeRuntimeDir", err)
	}
}

func TestRuntimeDirAcceptsAndCreatesItsSubdir(t *testing.T) {
	base := t.TempDir()
	if err := os.Chmod(base, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_RUNTIME_DIR", base)
	got, err := RuntimeDir()
	if err != nil {
		t.Fatalf("RuntimeDir: %v", err)
	}
	if got != filepath.Join(base, "tmux-hub") {
		t.Fatalf("RuntimeDir = %q", got)
	}
	fi, err := os.Lstat(got)
	if err != nil {
		t.Fatalf("the directory was not created: %v", err)
	}
	if fi.Mode().Perm()&0o077 != 0 {
		t.Errorf("mode = %o, want no group or other bits", fi.Mode().Perm())
	}
}

// An alias may legally contain a slash (`machine/.host` is a real entry in the
// system ssh config), which would break a path built from it raw. The hash keeps
// two aliases from colliding after sanitising.
func TestSocketLabelIsPathSafeAndCollisionFree(t *testing.T) {
	a := SocketLabel("machine/.host")
	b := SocketLabel("machine-.host")
	for _, l := range []string{a, b} {
		if strings.ContainsAny(l, "/\\ ") {
			t.Errorf("label %q is not path-safe", l)
		}
	}
	if a == b {
		t.Errorf("two different aliases produced the same label %q", a)
	}
	if SocketLabel("nuc") != SocketLabel("nuc") {
		t.Error("labels must be stable across calls")
	}
	if !strings.HasPrefix(SocketLabel("nuc"), "nuc-") {
		t.Errorf("a readable prefix should survive: %q", SocketLabel("nuc"))
	}
}

func TestPathsAreDistinctAndUnderTheRuntimeDir(t *testing.T) {
	sock, ctl, pid := Paths("/run/user/1000/tmux-hub", "nuc")
	seen := map[string]bool{}
	for _, p := range []string{sock, ctl, pid} {
		if !strings.HasPrefix(p, "/run/user/1000/tmux-hub/") {
			t.Errorf("%q is not under the runtime dir", p)
		}
		if seen[p] {
			t.Errorf("duplicate path %q", p)
		}
		seen[p] = true
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./internal/tunnel/ -v`
Expected: FAIL — the package does not exist.

- [ ] **Step 3: Write the implementation**

Create `internal/tunnel/paths.go`:

```go
// Package tunnel owns the life of one ssh master per remote host.
package tunnel

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var (
	ErrNoRuntimeDir     = errors.New("tunnel: no usable XDG_RUNTIME_DIR")
	ErrUnsafeRuntimeDir = errors.New("tunnel: XDG_RUNTIME_DIR is not safe to use")
)

// RuntimeDir returns $XDG_RUNTIME_DIR/tmux-hub, asserting what it needs rather
// than assuming it.
//
// Unset is refused because `filepath.Join("", "tmux-hub")` is the RELATIVE path
// `tmux-hub` — measured — so the hub would create sockets in whatever directory
// it happened to start in, work perfectly, and then report every host down when
// run from somewhere else. That is a baffling symptom for a silent cause.
//
// The mode and symlink checks are assertions and not fixes because
// `mkdir -p -m 0700` does not change an existing directory's mode and follows a
// symlink — also measured.
func RuntimeDir() (string, error) {
	base := os.Getenv("XDG_RUNTIME_DIR")
	if base == "" || !filepath.IsAbs(base) {
		return "", fmt.Errorf("%w: it is %s, so there is nowhere predictable to put "+
			"per-host sockets — set it, or pass --runtime-dir",
			ErrNoRuntimeDir, describeEmpty(base))
	}
	fi, err := os.Lstat(base)
	if err != nil {
		return "", fmt.Errorf("%w: cannot stat %s: %v", ErrNoRuntimeDir, base, err)
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("%w: %s is a symlink, so what it points at could change "+
			"under us", ErrUnsafeRuntimeDir, base)
	}
	if !fi.IsDir() {
		return "", fmt.Errorf("%w: %s is not a directory", ErrUnsafeRuntimeDir, base)
	}
	if fi.Mode().Perm()&0o077 != 0 {
		return "", fmt.Errorf("%w: %s is mode %o — a forwarded tmux socket there would be "+
			"reachable by others", ErrUnsafeRuntimeDir, base, fi.Mode().Perm())
	}

	dir := filepath.Join(base, "tmux-hub")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("%w: %v", ErrUnsafeRuntimeDir, err)
	}
	// MkdirAll leaves an existing directory's mode alone, so set it explicitly.
	if err := os.Chmod(dir, 0o700); err != nil {
		return "", fmt.Errorf("%w: %v", ErrUnsafeRuntimeDir, err)
	}
	return dir, nil
}

func describeEmpty(v string) string {
	if v == "" {
		return "unset"
	}
	return "relative (" + v + ")"
}

var unsafeInLabel = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// SocketLabel turns a host alias into a path-safe filename stem. The hash is not
// decoration: an alias may legally contain a slash — `machine/.host` is a real
// entry in the system ssh config — so sanitising alone could map two aliases onto
// one path.
func SocketLabel(alias string) string {
	clean := strings.Trim(unsafeInLabel.ReplaceAllString(alias, "-"), "-")
	if clean == "" {
		clean = "host"
	}
	sum := sha256.Sum256([]byte(alias))
	return clean + "-" + hex.EncodeToString(sum[:])[:8]
}

// Paths returns the socket, control and pid paths for a host.
func Paths(rt, alias string) (sock, ctl, pid string) {
	l := SocketLabel(alias)
	return filepath.Join(rt, l+".sock"),
		filepath.Join(rt, l+".ctl"),
		filepath.Join(rt, l+".pid")
}
```

- [ ] **Step 4: Run the tests**

Run: `go test ./internal/tunnel/ -v`
Expected: PASS all seven.

- [ ] **Step 5: Commit**

```bash
gofmt -l . && go test ./... && git add internal/tunnel/ && git commit -m "feat(tunnel): assert the runtime directory instead of assuming it

An unset XDG_RUNTIME_DIR yields the RELATIVE path 'tmux-hub', so the hub would
create sockets wherever it started, work, and then report every host down when
run from elsewhere. The mode and symlink checks are assertions rather than
fixes because mkdir -p -m 0700 neither changes an existing directory's mode nor
refuses a symlink.

The socket label carries a hash because an alias may legally contain a slash —
machine/.host is a real entry in the system ssh config — so sanitising alone
could map two aliases onto one path."
```

---

### Task 2: One hub at a time

**Files:**
- Create: `internal/tunnel/lock.go`
- Test: `internal/tunnel/lock_test.go`

**Interfaces:**
- Consumes: `RuntimeDir` from Task 1.
- Produces:
  - `type Lock struct{ … }`
  - `func Acquire(rt string) (*Lock, error)`
  - `func (l *Lock) Release() error`
  - `var ErrHeld error`

- [ ] **Step 1: Write the failing test**

Create `internal/tunnel/lock_test.go`:

```go
package tunnel

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestLockIsExclusiveWithinAProcess(t *testing.T) {
	rt := t.TempDir()
	l1, err := Acquire(rt)
	if err != nil {
		t.Fatalf("first Acquire: %v", err)
	}
	defer l1.Release()

	_, err = Acquire(rt)
	if !errors.Is(err, ErrHeld) {
		t.Fatalf("second Acquire = %v, want ErrHeld", err)
	}
	if err != nil && !strings.Contains(err.Error(), strconv.Itoa(os.Getpid())) {
		t.Errorf("the error must name the holder's pid, got %q", err)
	}
}

// flock is per open file description, so the real question is across PROCESSES.
// Measured with two processes: the second is refused with EAGAIN and gets it once
// the first closes.
func TestLockIsExclusiveAcrossProcesses(t *testing.T) {
	rt := t.TempDir()
	l, err := Acquire(rt)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}

	helper := func() string {
		cmd := exec.Command(os.Args[0], "-test.run=TestLockHelperProcess")
		cmd.Env = append(os.Environ(), "TUNNEL_LOCK_HELPER="+rt)
		out, _ := cmd.CombinedOutput()
		return string(out)
	}
	if got := helper(); !strings.Contains(got, "REFUSED") {
		t.Fatalf("a second process got the lock while we hold it: %q", got)
	}
	if err := l.Release(); err != nil {
		t.Fatalf("Release: %v", err)
	}
	if got := helper(); !strings.Contains(got, "ACQUIRED") {
		t.Fatalf("a second process could not take the released lock: %q", got)
	}
}

// TestLockHelperProcess is not a test; it is the child of the test above.
func TestLockHelperProcess(t *testing.T) {
	rt := os.Getenv("TUNNEL_LOCK_HELPER")
	if rt == "" {
		t.Skip("not the helper")
	}
	// fmt, not t.Log: the parent reads this child's combined output, and t.Log is
	// swallowed unless the child runs with -test.v. Without that the child printed
	// only "PASS", the parent found no marker, and the test failed while claiming a
	// second process had taken a held lock — a false alarm in the worst direction.
	l, err := Acquire(rt)
	if err != nil {
		fmt.Println("REFUSED")
		return
	}
	fmt.Println("ACQUIRED")
	_ = l.Release()
}

func TestLockFileLivesInTheRuntimeDir(t *testing.T) {
	rt := t.TempDir()
	l, err := Acquire(rt)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	defer l.Release()
	if _, err := os.Stat(filepath.Join(rt, "hub.lock")); err != nil {
		t.Fatalf("no lock file: %v", err)
	}
}
```

- [ ] **Step 2: Run it**

Run: `go test ./internal/tunnel/ -run Lock -v`
Expected: FAIL — `undefined: Acquire`.

- [ ] **Step 3: Write the implementation**

Create `internal/tunnel/lock.go`:

```go
package tunnel

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// ErrHeld means another hub owns this runtime directory.
var ErrHeld = errors.New("tunnel: another tmux-hub holds the lock")

// Lock is the right to own the tunnels in one runtime directory.
//
// It matters because the state a hub supervises is shared: two hubs on one
// machine would each spawn a master for the same host, and the second's spawn
// fails (`Address already in use`) while its reconciliation is tempted to unlink
// the first's live socket — which orphans that ssh. One writer removes the whole
// question.
//
// It is deliberately per-MACHINE and does not pretend otherwise: two hubs on two
// machines pointed at one host still share that host's paste buffers and pane
// options, which is why §11 namespaces remote state per instance instead.
type Lock struct {
	f    *os.File
	path string
}

// Acquire takes an exclusive, non-blocking flock. The pid inside the file is for
// the error message: it tells the user who to look for rather than only that
// they lost.
func Acquire(rt string) (*Lock, error) {
	path := filepath.Join(rt, "hub.lock")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		holder := readPID(path)
		f.Close()
		if holder != "" {
			return nil, fmt.Errorf("%w (pid %s) — stop it, or point this one at a "+
				"different --runtime-dir", ErrHeld, holder)
		}
		return nil, fmt.Errorf("%w — stop it, or use a different --runtime-dir", ErrHeld)
	}
	if err := f.Truncate(0); err == nil {
		_, _ = f.WriteAt([]byte(strconv.Itoa(os.Getpid())+"\n"), 0)
	}
	return &Lock{f: f, path: path}, nil
}

func readPID(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// Release drops the lock. The file is left behind on purpose: its mtime and pid
// are useful to a person diagnosing a refusal, and an empty file is harmless.
func (l *Lock) Release() error {
	if l == nil || l.f == nil {
		return nil
	}
	err := syscall.Flock(int(l.f.Fd()), syscall.LOCK_UN)
	if cerr := l.f.Close(); err == nil {
		err = cerr
	}
	l.f = nil
	return err
}
```

- [ ] **Step 4: Run the tests**

Run: `go test ./internal/tunnel/ -run Lock -v`
Expected: PASS. `TestLockHelperProcess` shows as a passing test in its own right — it skips when it is not the helper.

- [ ] **Step 5: Commit**

```bash
gofmt -l . && go test ./... && git add internal/tunnel/lock.go internal/tunnel/lock_test.go && git commit -m "feat(tunnel): one hub owns one runtime directory

Two hubs on one machine would each spawn a master for the same host: the
second's spawn fails 'Address already in use', and its reconciliation is
tempted to unlink the first's live socket — which orphans that ssh rather than
replacing it. One writer removes the question.

The lock is per-machine and the comment says so: two hubs on two machines
pointed at one host still share that host's paste buffers and pane options,
which is what per-instance namespacing is for. Tested across processes, not
just within one, because flock is per open file description."
```

---

### Task 3: The ssh master's life

**Files:**
- Create: `internal/tunnel/tunnel.go`
- Test: `internal/tunnel/tunnel_test.go`
- Test: `internal/tunnel/testdata/fake-ssh` (a shell script)

**Interfaces:**
- Consumes: `Paths` from Task 1, `transport.Dial` from Task 0 for classification.
- Produces:
  - `type Spec struct { Alias, SSHDest, RemoteSocket string }`
  - `type Tunnel struct { … }`
  - `func Open(ctx context.Context, rt string, s Spec, wait time.Duration) (*Tunnel, error)`
  - `func (t *Tunnel) SocketPath() string`, `ControlPath() string`, `PID() int`
  - `func (t *Tunnel) Close() error`
  - `var ErrForwardRefused, ErrNotReady error`

- [ ] **Step 1: Write the fake ssh**

Create `internal/tunnel/testdata/fake-ssh` and `chmod +x` it. It has to be a real
program because the point is to test the *lifecycle* without a network:

```sh
#!/bin/sh
# A stand-in for ssh, so tunnel lifecycle is testable with no network and no
# remote host. Behaviour is chosen by FAKE_SSH_MODE:
#
#   ok       bind the forward socket with a listener that holds connections open,
#            the way a live tmux server does, and sleep until killed
#   refuse   print what ssh prints when the local bind fails, exit 255
#   slow     sleep before binding, so "wait on the fact" can be exercised
#   hang     never bind, never exit
#
# The socket path is parsed out of the -L argument exactly as ssh would use it.
sock=""
prev=""
for a in "$@"; do
  case "$prev" in
    -L) sock="${a%%:*}" ;;
  esac
  prev="$a"
done

case "${FAKE_SSH_MODE:-ok}" in
  refuse)
    echo "unix_listener: cannot bind to path $sock: Address already in use" >&2
    echo "Could not request local forwarding." >&2
    exit 255
    ;;
  hang)
    exec sleep 3600
    ;;
  slow)
    # Reproduce the measured shape exactly: the socket PATH appears BEFORE the
    # forward is usable. A readiness check that keys on the file's existence
    # passes here immediately and is wrong, so this mode is what makes
    # TestOpenDoesNotReturnOnTheFileAlone discriminating rather than decorative.
    : > "$sock"
    sleep "${FAKE_SSH_DELAY:-2}"
    rm -f "$sock"
    ;;
esac

# `ok` and `slow` both end up here: hold a listener on the socket so a dial is
# accepted AND held, which is what transport.Dial classifies as live.
python3 - "$sock" <<'PY' &
import socket, sys, os, time
p = sys.argv[1]
try: os.unlink(p)
except FileNotFoundError: pass
s = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
s.bind(p); s.listen(8)
held = []
while True:
    c, _ = s.accept()
    held.append(c)          # hold it, like a tmux server waiting to be spoken to
PY
listener=$!
trap 'kill $listener 2>/dev/null; rm -f "$sock"; exit 0' TERM INT
wait $listener
```

- [ ] **Step 2: Write the failing test**

Create `internal/tunnel/tunnel_test.go`:

```go
package tunnel

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func withFakeSSH(t *testing.T, mode string) {
	t.Helper()
	abs, err := filepath.Abs("testdata")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(abs, "fake-ssh")); err != nil {
		t.Skipf("fake-ssh missing: %v", err)
	}
	// A directory holding only a program named `ssh`, put first on PATH.
	bin := t.TempDir()
	if err := os.Symlink(filepath.Join(abs, "fake-ssh"), filepath.Join(bin, "ssh")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("FAKE_SSH_MODE", mode)
}

func spec() Spec {
	return Spec{Alias: "nuc", SSHDest: "nuc", RemoteSocket: "/tmp/tmux-1000/default"}
}

func TestOpenWaitsForTheSocketToBeUsable(t *testing.T) {
	withFakeSSH(t, "ok")
	rt := t.TempDir()
	tun, err := Open(context.Background(), rt, spec(), 10*time.Second)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer tun.Close()

	if _, err := os.Stat(tun.SocketPath()); err != nil {
		t.Fatalf("socket not there: %v", err)
	}
	if tun.PID() == 0 {
		t.Error("no pid recorded — reconciliation needs it to reap an orphan")
	}
	// The pid file is what a LATER hub reads, so it must be on disk, not just in
	// memory.
	b, err := os.ReadFile(filepath.Join(rt, SocketLabel("nuc")+".pid"))
	if err != nil {
		t.Fatalf("no pid file: %v", err)
	}
	if strings.TrimSpace(string(b)) == "" {
		t.Error("pid file is empty")
	}
}

// A socket file appearing is NOT readiness: measured against a real host, the
// file exists before the forward is usable.
func TestOpenDoesNotReturnOnTheFileAlone(t *testing.T) {
	withFakeSSH(t, "slow")
	t.Setenv("FAKE_SSH_DELAY", "2")
	rt := t.TempDir()
	start := time.Now()
	tun, err := Open(context.Background(), rt, spec(), 15*time.Second)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer tun.Close()
	if d := time.Since(start); d < 1500*time.Millisecond {
		t.Fatalf("Open returned in %v, before the forward could exist", d)
	}
}

// ExitOnForwardFailure turns a stale path into a fast, loud failure. Without it
// ssh stays alive with a dead forward and tmux then says 'no server running',
// which reads as a benign empty host.
func TestOpenReportsARefusedForward(t *testing.T) {
	withFakeSSH(t, "refuse")
	rt := t.TempDir()
	_, err := Open(context.Background(), rt, spec(), 10*time.Second)
	if !errors.Is(err, ErrForwardRefused) {
		t.Fatalf("Open = %v, want ErrForwardRefused", err)
	}
	if !strings.Contains(err.Error(), "already in use") {
		t.Errorf("the error should carry what ssh said, got %q", err)
	}
}

func TestOpenGivesUpOnAHangingSSH(t *testing.T) {
	withFakeSSH(t, "hang")
	rt := t.TempDir()
	start := time.Now()
	_, err := Open(context.Background(), rt, spec(), 1500*time.Millisecond)
	if !errors.Is(err, ErrNotReady) {
		t.Fatalf("Open = %v, want ErrNotReady", err)
	}
	if d := time.Since(start); d > 6*time.Second {
		t.Fatalf("Open waited %v past its own budget", d)
	}
}

// Close must reap the child and leave nothing dialable behind, or the next
// Open's spawn fails on a path this one still owns.
func TestCloseReapsAndCleansUp(t *testing.T) {
	withFakeSSH(t, "ok")
	rt := t.TempDir()
	tun, err := Open(context.Background(), rt, spec(), 10*time.Second)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	sock, pidPath := tun.SocketPath(), filepath.Join(rt, SocketLabel("nuc")+".pid")
	if err := tun.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := os.Stat(sock); !os.IsNotExist(err) {
		t.Errorf("socket survived Close: %v", err)
	}
	if _, err := os.Stat(pidPath); !os.IsNotExist(err) {
		t.Errorf("pid file survived Close: %v", err)
	}
}

// The flags are not stylistic. ExitOnForwardFailure is what makes a broken
// forward loud, and the keepalive is what bounds a stall.
func TestSpawnArgsCarryTheMeasuredFlags(t *testing.T) {
	args := spawnArgs("/rt/nuc.sock", "/rt/nuc.ctl", spec())
	joined := strings.Join(args, " ")
	for _, want := range []string{
		"-N", "-M", "-S /rt/nuc.ctl",
		"-L /rt/nuc.sock:/tmp/tmux-1000/default",
		"-o BatchMode=yes",
		"-o ExitOnForwardFailure=yes",
		"-o ServerAliveInterval=15",
		"nuc",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("spawn args lack %q\ngot: %s", want, joined)
		}
	}
}
```

- [ ] **Step 3: Run it**

Run: `go test ./internal/tunnel/ -run 'Open|Close|Spawn' -v`
Expected: FAIL — `undefined: Open`.

- [ ] **Step 4: Write the implementation**

Create `internal/tunnel/tunnel.go`:

```go
package tunnel

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/DawnBreather/tmux-hub/internal/transport"
)

var (
	// ErrForwardRefused: ssh could not bind the local socket. With
	// ExitOnForwardFailure=yes this is fast and loud; without it ssh would stay
	// alive with a dead forward and tmux would then answer `no server running`,
	// which reads as a reachable host with no tmux.
	ErrForwardRefused = errors.New("tunnel: ssh refused to set up the forward")
	// ErrNotReady: the socket never became usable inside the budget.
	ErrNotReady = errors.New("tunnel: the forward did not become usable")
)

// Spec is what the hub knows about a host before it connects.
type Spec struct {
	Alias        string // the ssh_config Host name, and the label's source
	SSHDest      string // what to pass to ssh; usually the alias
	RemoteSocket string // the tmux socket path on the far side
}

// Tunnel is one live ssh master plus the paths it owns.
type Tunnel struct {
	spec Spec
	sock string
	ctl  string
	pidf string
	cmd  *exec.Cmd

	// done is closed once the child has been reaped. One goroutine owns
	// cmd.Wait() and nothing else may call it, so this channel is how both the
	// readiness loop and kill learn that the master is gone. Closing it also
	// publishes cmd.Stderr safely: Wait returns only after the stderr copier has
	// finished, so a read after <-done is ordered behind every write to it.
	done chan struct{}
}

func (t *Tunnel) SocketPath() string  { return t.sock }
func (t *Tunnel) ControlPath() string { return t.ctl }
func (t *Tunnel) PID() int {
	if t.cmd == nil || t.cmd.Process == nil {
		return 0
	}
	return t.cmd.Process.Pid
}

// spawnArgs builds the invocation. Every flag here was chosen by measurement and
// the reasons live in docs/design.md §3:
//
//   - -N -M -S: one process is both the ControlMaster (attach needs it, because a
//     forwarded socket cannot carry an attach at all) and the forward (polling
//     needs it).
//   - ExitOnForwardFailure=yes: makes a stale path a fast rc=255 instead of a
//     silently forwardless master.
//   - ServerAliveInterval=15: bounds a stall. Verified at 5s, where ssh exited
//     19.5s into a real stall, so 15s means roughly 45-60s.
//   - BatchMode=yes: never prompt. A hub cannot answer a passphrase prompt.
func spawnArgs(sock, ctl string, s Spec) []string {
	return []string{
		"-N", "-M", "-S", ctl,
		"-L", sock + ":" + s.RemoteSocket,
		"-o", "BatchMode=yes",
		"-o", "ExitOnForwardFailure=yes",
		"-o", "ServerAliveInterval=15",
		s.SSHDest,
	}
}

// Open spawns a master and waits until the forward is USABLE, which is not the
// same as the socket file existing — measured against a real host, the file
// appears first. The wait is on the fact: a dial that is accepted and held.
//
// It does NOT unlink the path first. Unlinking a live forwarded socket orphans
// its ssh: the process survives, the path becomes undialable, and a new tunnel
// binds it while the old one runs forever. Adoption is the caller's job
// (Reconcile), and by the time Open runs the path is known dead.
func Open(ctx context.Context, rt string, s Spec, wait time.Duration) (*Tunnel, error) {
	sock, ctl, pidf := Paths(rt, s.Alias)
	t := &Tunnel{spec: s, sock: sock, ctl: ctl, pidf: pidf}

	t.cmd = exec.Command("ssh", spawnArgs(sock, ctl, s)...)

	// ssh's stderr goes to a FILE, and that is load-bearing rather than
	// stylistic. Any non-*os.File writer makes exec create a pipe plus a copier
	// goroutine, and cmd.Wait() then blocks until every process holding the write
	// end has exited — INCLUDING processes ssh itself forked. Measured against the
	// fake master: the master exited, Wait sat in awaitGoroutines forever because a
	// grandchild still held the pipe, so the done channel never closed and Close
	// hung for the full test timeout. A file is handed to the child as a plain fd,
	// so Wait returns when the process does.
	errf, err := os.CreateTemp("", "tmux-hub-ssh-*.err")
	if err != nil {
		return nil, err
	}
	defer func() { _ = errf.Close(); _ = os.Remove(errf.Name()) }()
	t.cmd.Stderr = errf

	if err := t.cmd.Start(); err != nil {
		return nil, err
	}
	// One goroutine owns Wait. Polling the pid instead does not work: a child that
	// has exited but not been reaped is a zombie, and signalling a zombie
	// SUCCEEDS, so a signal-based liveness check reports a dead master as alive.
	t.done = make(chan struct{})
	go func() {
		_ = t.cmd.Wait()
		close(t.done)
	}()

	refused := func() error {
		// Safe to read: Wait has returned, so the child has flushed and exited.
		// The deferred remove runs after the return value is built, not before.
		b, _ := os.ReadFile(errf.Name())
		return fmt.Errorf("%w: %s", ErrForwardRefused, firstLine(string(b)))
	}

	deadline := time.Now().Add(wait)
	for {
		// A dead child is the fastest and clearest failure available, and with
		// ExitOnForwardFailure it is what a stale socket path produces.
		select {
		case <-t.done:
			return nil, refused()
		default:
		}
		if tr, _, err := transport.Dial(sock, 200*time.Millisecond); err == nil && tr == transport.Live {
			break
		}
		if time.Now().After(deadline) {
			_ = t.kill()
			return nil, fmt.Errorf("%w after %s", ErrNotReady, wait)
		}
		select {
		case <-ctx.Done():
			_ = t.kill()
			return nil, ctx.Err()
		case <-t.done:
			return nil, refused()
		case <-time.After(100 * time.Millisecond):
		}
	}

	// The pid goes on disk because a LATER hub needs it: that is how an orphaned
	// master is reaped instead of leaked.
	_ = os.WriteFile(pidf, []byte(strconv.Itoa(t.PID())+"\n"), 0o600)
	return t, nil
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// Close reaps the master and removes what it owned. Leaving the socket behind
// would make the next Open fail on a path nothing is listening to.
func (t *Tunnel) Close() error {
	if t == nil {
		return nil
	}
	err := t.kill()
	_ = os.Remove(t.sock)
	_ = os.Remove(t.pidf)
	return err
}

func (t *Tunnel) kill() error {
	if t == nil || t.cmd == nil || t.cmd.Process == nil || t.done == nil {
		return nil
	}
	// -O exit is the polite form and also tears down the control socket. It gets
	// its own short deadline, because the case this is called in is precisely a
	// master that may not be answering — against a hung ssh a bare Run() blocks
	// forever, and that turned the "gives up on a hanging ssh" path into a test
	// that never returns.
	pctx, pcancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer pcancel()
	_ = exec.CommandContext(pctx, "ssh", "-S", t.ctl, "-O", "exit", t.spec.SSHDest).Run()

	// SIGTERM before SIGKILL: it lets ssh remove its own control socket, and on a
	// master with multiplexed children it lets those exit too. SIGKILL is only the
	// fallback for a master that ignores it.
	select {
	case <-t.done:
	default:
		_ = t.cmd.Process.Signal(syscall.SIGTERM)
		select {
		case <-t.done:
		case <-time.After(2 * time.Second):
			_ = t.cmd.Process.Kill()
		}
	}
	<-t.done // the waiter owns Wait; calling it here would be the second call
	_ = os.Remove(t.ctl)
	// The child's exit status is deliberately not returned. We killed it, so
	// `signal: killed` is the expected outcome and reporting it as an error would
	// make every clean shutdown look like a failure.
	return nil
}
```

- [ ] **Step 5: Run the tests**

Run: `go test ./internal/tunnel/ -v`
Expected: PASS. If `TestOpenDoesNotReturnOnTheFileAlone` is flaky, raise `FAKE_SSH_DELAY` rather than lowering the assertion — the point is that readiness is not the file.

- [ ] **Step 6: Commit**

```bash
gofmt -l . && go test -race ./... && git add internal/tunnel/ && git commit -m "feat(tunnel): the ssh master's life, tested without a network

Open waits on the FACT — a dial accepted and held — because a socket file
appearing is not readiness: measured against a real host, the file exists
before the forward is usable. It never unlinks the path first, since unlinking
a live forwarded socket orphans its ssh rather than replacing it, and it writes
the child's pid to disk because that is how a later hub reaps an orphan instead
of leaking one.

The lifecycle is tested against a fake ssh on PATH: bind-and-hold, refuse,
slow, hang. No network, no remote host, and the four failure paths are
ordinary test cases rather than things to hope about."
```

---

### Task 4: Reconcile at startup — adopt, repair, reap

**Files:**
- Create: `internal/tunnel/reconcile.go`
- Test: `internal/tunnel/reconcile_test.go`

**Interfaces:**
- Consumes: `Paths`, `Open`, `transport.Dial`.
- Produces:
    - `type Found struct { Alias, Socket string; PID int; Kind transport.Kind }`
  - `func Survey(rt string, aliases []string) ([]Found, error)`
  - `func Reap(rt string, f Found) error`

- [ ] **Step 1: Write the failing test**

Create `internal/tunnel/reconcile_test.go`:

```go
package tunnel

import (
	"net"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/DawnBreather/tmux-hub/internal/transport"
)

// A live socket from a previous run is ADOPTABLE: measured, a second process can
// dial it, while a second `ssh -L` on the same path gets rc=255. So survey must
// distinguish live from dead rather than clearing the ground.
func TestSurveyClassifiesWhatItFinds(t *testing.T) {
	rt := t.TempDir()

	// live: something listening and holding
	liveSock, _, livePID := Paths(rt, "live-host")
	l, err := net.Listen("unix", liveSock)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	go func() {
		var held []net.Conn
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			held = append(held, c)
		}
	}()
	if err := os.WriteFile(livePID, []byte("4242\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	// dead: a socket file with nothing behind it
	deadSock, _, _ := Paths(rt, "dead-host")
	if err := os.WriteFile(deadSock, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := Survey(rt, []string{"live-host", "dead-host", "never-connected"})
	if err != nil {
		t.Fatalf("Survey: %v", err)
	}
	by := map[string]Found{}
	for _, f := range got {
		by[f.Alias] = f
	}
	if f := by["live-host"]; f.Kind != transport.Live {
		t.Errorf("live host classified %v", f.Kind)
	} else if f.PID != 4242 {
		t.Errorf("pid = %d, want the one on disk", f.PID)
	}
	if f := by["dead-host"]; f.Kind == transport.Live {
		t.Errorf("a plain file classified as live")
	}
	if _, ok := by["never-connected"]; ok {
		t.Error("a host with no socket should not appear at all")
	}
}

// Reaping is what keeps an orphan from surviving forever. It must remove the
// socket AND the pid file, and it must not blow up on a pid that is already gone.
func TestReapRemovesThePathsAndToleratesADeadPID(t *testing.T) {
	rt := t.TempDir()
	sock, _, pidf := Paths(rt, "gone")
	if err := os.WriteFile(sock, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pidf, []byte("999999999\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	f := Found{Alias: "gone", Socket: sock, PID: 999999999}
	if err := Reap(rt, f); err != nil {
		t.Fatalf("Reap: %v", err)
	}
	for _, p := range []string{sock, pidf} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("%s survived Reap", filepath.Base(p))
		}
	}
}

// A survey entry with no pid file is still reapable — an older hub may have
// crashed before writing one.
func TestReapWithoutAPIDFile(t *testing.T) {
	rt := t.TempDir()
	sock, _, _ := Paths(rt, "nopid")
	if err := os.WriteFile(sock, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Reap(rt, Found{Alias: "nopid", Socket: sock}); err != nil {
		t.Fatalf("Reap: %v", err)
	}
	if _, err := os.Stat(sock); !os.IsNotExist(err) {
		t.Error("socket survived")
	}
}

// The control path is surveyed separately from the socket. Measured on a real
// host: `ssh -S <ctl> -O check` names whoever OWNS that path, which need not be the
// process this hub spawned — with two masters on one ctl path it named the second
// while the recorded pid was the first, so killing the recorded pid left a master
// running. A ctl path with no file behind it must therefore read as unowned, and
// the check must be bounded in time because a hung master simply does not answer.
func TestSurveyReportsAnUnownedControlPath(t *testing.T) {
	rt := t.TempDir()
	sock, _, _ := Paths(rt, "noctl")
	if err := os.WriteFile(sock, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := Survey(rt, []string{"noctl"})
	if err != nil {
		t.Fatalf("Survey: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d entries", len(got))
	}
	if got[0].CtlOwn {
		t.Error("a control path that does not exist was reported as owned")
	}
}

// A ctl path that exists but answers nothing is also unowned, and the check must
// not hang deciding that.
func TestSurveyDoesNotHangOnADeadControlPath(t *testing.T) {
	rt := t.TempDir()
	sock, ctl, _ := Paths(rt, "deadctl")
	for _, f := range []string{sock, ctl} {
		if err := os.WriteFile(f, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	start := time.Now()
	got, err := Survey(rt, []string{"deadctl"})
	if err != nil {
		t.Fatalf("Survey: %v", err)
	}
	if d := time.Since(start); d > 10*time.Second {
		t.Fatalf("Survey took %v on one dead control path", d)
	}
	if len(got) == 1 && got[0].CtlOwn {
		t.Error("a control path nothing answers on was reported as owned")
	}
}

func TestSurveyIgnoresUnrelatedFiles(t *testing.T) {
	rt := t.TempDir()
	if err := os.WriteFile(filepath.Join(rt, "hub.lock"), []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := Survey(rt, []string{"nuc"})
	if err != nil {
		t.Fatalf("Survey: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("Survey found %+v, want nothing", got)
	}
}
```

- [ ] **Step 2: Run it**

Run: `go test ./internal/tunnel/ -run 'Survey|Reap' -v`
Expected: FAIL — `undefined: Survey`.

- [ ] **Step 3: Write the implementation**

Create `internal/tunnel/reconcile.go`:

```go
package tunnel

import (
	"context"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/DawnBreather/tmux-hub/internal/transport"
)

// Found is one socket left over from a previous run, classified.
type Found struct {
	Alias  string
	Socket string
	PID    int  // 0 when no pid file was written
	CtlOwn bool // a live master answers on the control path
	Kind   transport.Kind
}

// Survey looks at what a previous hub left behind, WITHOUT changing anything.
//
// The order matters and it is the correction to an earlier draft of §5, which
// said to unlink before spawning: unlinking a live forwarded socket does not stop
// its ssh — the process survives, the path becomes undialable, and a new tunnel
// binds it while the old one runs forever. So the rule is probe, then act: adopt
// what answers, reap only what does not.
func Survey(rt string, aliases []string) ([]Found, error) {
	var out []Found
	for _, a := range aliases {
		sock, ctlPath, pidf := Paths(rt, a)
		alias := a
		if _, err := os.Lstat(sock); err != nil {
			continue // nothing left behind for this host
		}
		f := Found{Alias: a, Socket: sock}
		if b, err := os.ReadFile(pidf); err == nil {
			f.PID, _ = strconv.Atoi(strings.TrimSpace(string(b)))
		}
		tr, _, err := transport.Dial(sock, 300*time.Millisecond)
		if err == nil {
			f.Kind = tr
		}
		// The CONTROL path is surveyed too, and it is a separate question from the
		// socket. Measured on a real host: `ssh -S <ctl> -O check` reports the pid
		// that OWNS the control socket, which need not be the process this hub
		// spawned — with two masters sharing one ctl path, -O check named the second
		// while the recorded pid was the first, and a SIGTERM to the recorded pid
		// reaped that one and left the other running. So a live master on the ctl
		// path is a thing Open must not walk into, and Reap must not assume its
		// recorded pid is the only claimant.
		f.CtlOwn = ctlHasLiveMaster(ctlPath, alias)
		out = append(out, f)
	}
	return out, nil
}

// ctlHasLiveMaster asks whether anything still owns a control path. It is bounded
// in time because the answer for a hung master is "no answer", and an unbounded
// check there would hang reconciliation at startup — the one place a hang is
// indistinguishable from a slow network.
func ctlHasLiveMaster(ctlPath, dest string) bool {
	if _, err := os.Lstat(ctlPath); err != nil {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "ssh", "-S", ctlPath, "-O", "check", dest).CombinedOutput()
	return err == nil && strings.Contains(string(out), "Master running")
}

// Reap removes a dead tunnel's leftovers and kills its master if it is still
// around. Killing by the recorded pid is the whole reason Open writes one: a
// master whose socket is gone is unreachable but not dead, and nothing else can
// find it.
func Reap(rt string, f Found) error {
	if f.PID > 0 {
		// Signal 0 first: a pid may have been reused, and killing a stranger is
		// worse than leaving an orphan.
		if err := syscall.Kill(f.PID, 0); err == nil {
			_ = syscall.Kill(f.PID, syscall.SIGTERM)
		}
	}
	_, ctlPath, pidf := Paths(rt, f.Alias)
	// The control path goes too, but only after the polite exit has had its chance:
	// removing it first would leave a master alive with no way to ask it to leave.
	if f.CtlOwn {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = exec.CommandContext(ctx, "ssh", "-S", ctlPath, "-O", "exit", f.Alias).Run()
	}
	_ = os.Remove(f.Socket)
	_ = os.Remove(ctlPath)
	_ = os.Remove(pidf)
	return nil
}
```

- [ ] **Step 4: Run the tests**

Run: `go test ./internal/tunnel/ -v`
Expected: PASS all of them.

- [ ] **Step 5: Commit**

```bash
gofmt -l . && go test -race ./... && git add internal/tunnel/reconcile.go internal/tunnel/reconcile_test.go && git commit -m "feat(tunnel): probe, then act — never unlink first

An earlier draft of section 5 said to unlink the socket unconditionally before
spawning. Measured, that orphans one ssh per restart: unlinking a live
forwarded socket leaves the process running and the path undialable, and the
next tunnel binds it happily while the old master survives forever.

Survey therefore classifies without changing anything, and Reap kills by the
pid Open recorded — which is the entire reason it records one, since a master
whose socket is gone is unreachable but not dead and nothing else can find it.
Signal 0 before SIGTERM, because a pid may have been reused and killing a
stranger is worse than leaving an orphan."
```

---

### Task 5: Where hosts come from

**Files:**
- Create: `internal/hostset/sshconfig.go`
- Create: `internal/hostset/hosts.go`
- Test: `internal/hostset/sshconfig_test.go`
- Test: `internal/hostset/hosts_test.go`

**Interfaces:**
- Produces:
  - `func ParseSSHConfig(r io.Reader) []Candidate`
  - `type Candidate struct { Alias string; Wildcard, Unroutable bool }`
  - `type Entry struct { Alias string; Enabled bool; Tags []string; Socket string }`
  - `func LoadHosts(path string) ([]Entry, error)`
  - `func SaveHosts(path string, es []Entry) error`
  - `func Probe(ctx context.Context, alias string, timeout time.Duration) (version, tmpdir string, err error)`

- [ ] **Step 1: Write the failing tests**

Create `internal/hostset/sshconfig_test.go`. The fixture is this machine's real
shape, including the systemd drop-in patterns that only appear through the *system*
config's `Include`:

```go
package hostset

import (
	"strings"
	"testing"
)

const realShape = `
Host orbits.github.com
  HostName github.com
Host metrics-engine
  HostName reports.example
Host studio-ws web-ws crater-ws st-ws side-desk qa-ws
  User won
Host cortex-web
  ProxyCommand docker exec -i tailscale-cortex nc %h %p
Host .host machine/.host
  ProxyCommand /usr/lib/systemd/systemd-ssh-proxy unix/run/ssh-unix-local/socket %p
Host unix/* unix%* vsock/* machine/*
  ProxyCommand /usr/lib/systemd/systemd-ssh-proxy %h %p
Host *
  ServerAliveInterval 30
`

// One Host line can carry SEVERAL names — measured, 15 lines expand to 20 names
// on this machine — and a parser that misses that silently loses five hosts.
func TestParseExpandsMultiNameHostLines(t *testing.T) {
	got := ParseSSHConfig(strings.NewReader(realShape))
	var names []string
	for _, c := range got {
		names = append(names, c.Alias)
	}
	for _, want := range []string{"studio-ws", "web-ws", "crater-ws", "st-ws", "side-desk", "qa-ws"} {
		if !contains(names, want) {
			t.Errorf("lost %q from a multi-name Host line", want)
		}
	}
}

// Wildcards cannot be connected to, and the unroutable systemd patterns are not
// hosts at all. They are FLAGGED rather than dropped so a picker can say why.
func TestParseFlagsWildcardsAndUnroutableNames(t *testing.T) {
	got := ParseSSHConfig(strings.NewReader(realShape))
	by := map[string]Candidate{}
	for _, c := range got {
		by[c.Alias] = c
	}
	for _, a := range []string{"*", "unix/*", "unix%*", "vsock/*", "machine/*"} {
		if c, ok := by[a]; !ok {
			t.Errorf("%q was dropped instead of flagged", a)
		} else if !c.Wildcard {
			t.Errorf("%q not flagged as a wildcard", a)
		}
	}
	if c, ok := by[".host"]; !ok || !c.Unroutable {
		t.Errorf(".host should be flagged unroutable, got %+v", c)
	}
	if c, ok := by["machine/.host"]; !ok || !(c.Unroutable || c.Wildcard) {
		t.Errorf("machine/.host should be flagged, got %+v", c)
	}
	if c := by["nuc"]; c.Wildcard || c.Unroutable {
		t.Errorf("a normal alias was flagged: %+v", c)
	}
}

func TestParseIgnoresCommentsAndIndentation(t *testing.T) {
	got := ParseSSHConfig(strings.NewReader("# Host commented\n   Host   indented   \n"))
	if len(got) != 1 || got[0].Alias != "indented" {
		t.Fatalf("got %+v", got)
	}
}

func contains(hay []string, needle string) bool {
	for _, h := range hay {
		if h == needle {
			return true
		}
	}
	return false
}
```

Create `internal/hostset/hosts_test.go`:

```go
package hostset

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestHostsRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hosts.toml")
	in := []Entry{
		{Alias: "nuc", Enabled: true, Tags: []string{"prod", "eu"}},
		{Alias: "side-desk", Enabled: true},
		{Alias: "studio-ws", Enabled: false},
		{Alias: "odd name", Enabled: true, Socket: "/tmp/tmux-1000/other"},
	}
	if err := SaveHosts(path, in); err != nil {
		t.Fatalf("SaveHosts: %v", err)
	}
	out, err := LoadHosts(path)
	if err != nil {
		t.Fatalf("LoadHosts: %v", err)
	}
	if len(out) != len(in) {
		t.Fatalf("got %d entries, want %d", len(out), len(in))
	}
	by := map[string]Entry{}
	for _, e := range out {
		by[e.Alias] = e
	}
	if e := by["nuc"]; !e.Enabled || len(e.Tags) != 2 || e.Tags[0] != "prod" {
		t.Errorf("nuc = %+v", e)
	}
	if by["studio-ws"].Enabled {
		t.Error("studio-ws should have stayed disabled")
	}
	if by["odd name"].Socket != "/tmp/tmux-1000/other" {
		t.Errorf("the socket override was lost: %+v", by["odd name"])
	}
}

// The file is GENERATED by the picker, so it must be readable back by the tool
// that wrote it — and a hand-edited file with the usual sloppiness must survive.
func TestLoadHostsToleratesHandEditing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hosts.toml")
	if err := os.WriteFile(path, []byte(`
# a comment

[[host]]
alias = "nuc"
enabled = true
tags = [ "prod" , "eu" ]

[[host]]
  alias="side-desk"
  enabled   =   false
`), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := LoadHosts(path)
	if err != nil {
		t.Fatalf("LoadHosts: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d entries: %+v", len(got), got)
	}
	if !got[0].Enabled || got[0].Tags[1] != "eu" {
		t.Errorf("first = %+v", got[0])
	}
	if got[1].Enabled {
		t.Errorf("second should be disabled: %+v", got[1])
	}
}

func TestLoadHostsOnAMissingFileIsNotAnError(t *testing.T) {
	got, err := LoadHosts(filepath.Join(t.TempDir(), "absent.toml"))
	if err != nil {
		t.Fatalf("a missing hosts file must be an empty list, not an error: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("got %+v", got)
	}
}

// Membership keys on STDOUT, never on the exit code: measured, studio-ws returns
// rc=0 from the shell while having no tmux at all, so an rc-keyed probe admits a
// host that will fail mysteriously later.
func TestProbeKeysOnOutput(t *testing.T) {
	ok, tmp, err := parseProbe("tmux 3.2a\nTMPDIR=/tmp\n")
	if err != nil {
		t.Fatalf("parseProbe: %v", err)
	}
	if ok != "3.2a" || tmp != "/tmp" {
		t.Errorf("got version %q tmpdir %q", ok, tmp)
	}
	if _, _, err := parseProbe("zsh:1: command not found: tmux\n"); err == nil {
		t.Error("a host with no tmux must be rejected even though the shell exits 0")
	}
	if _, _, err := parseProbe(""); err == nil {
		t.Error("empty output must be rejected")
	}
}

func TestProbeHonoursItsDeadline(t *testing.T) {
	start := time.Now()
	_, _, err := Probe(context.Background(), "definitely-not-a-host-xyz", 2*time.Second)
	if err == nil {
		t.Fatal("want an error for a nonexistent host")
	}
	if d := time.Since(start); d > 20*time.Second {
		t.Fatalf("probe took %v", d)
	}
	if !strings.Contains(err.Error(), "definitely-not-a-host-xyz") {
		t.Errorf("the error should name the host, got %q", err)
	}
}
```

- [ ] **Step 2: Run them**

Run: `go test ./internal/hostset/ -v`
Expected: FAIL — the package does not exist.

- [ ] **Step 3: Write the ssh_config parser**

Create `internal/hostset/sshconfig.go`:

```go
// Package hostset decides which hosts exist and which the user wants.
package hostset

import (
	"bufio"
	"io"
	"strings"
)

// Candidate is one alias from an ssh config, with the reasons it might not be
// usable. They are flags rather than exclusions so a picker can explain itself:
// "not a host" is more useful to a person than a shorter list.
type Candidate struct {
	Alias      string
	Wildcard   bool // contains a glob; cannot be connected to
	Unroutable bool // a systemd-style pseudo-host such as .host
}

// ParseSSHConfig reads Host lines. It deliberately does NOT follow Include:
// measured on this machine, the user's own config has no Include and no wildcard
// patterns at all — those live in a systemd drop-in reached through the SYSTEM
// config's include chain, so following it only adds names the probe would reject
// anyway.
func ParseSSHConfig(r io.Reader) []Candidate {
	var out []Candidate
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 || !strings.EqualFold(fields[0], "host") {
			continue
		}
		// One Host line may name SEVERAL hosts — measured, 15 lines expand to 20
		// names here, so treating the rest of the line as one name loses five.
		for _, name := range fields[1:] {
			out = append(out, Candidate{
				Alias:      name,
				Wildcard:   strings.ContainsAny(name, "*?!"),
				Unroutable: strings.HasPrefix(name, ".") || strings.Contains(name, "/"),
			})
		}
	}
	return out
}

// Usable reports whether a candidate is worth probing.
func (c Candidate) Usable() bool { return !c.Wildcard && !c.Unroutable }
```

- [ ] **Step 4: Write the hosts file and the probe**

Create `internal/hostset/hosts.go`:

```go
package hostset

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

// Entry is one line of the user's choice. The file is GENERATED by the picker and
// hand-editable, which is why it is read leniently and written plainly.
type Entry struct {
	Alias   string
	Enabled bool
	Tags    []string
	Socket  string // optional override for `tmux -L other` servers
}

// LoadHosts reads hosts.toml. A missing file is an empty list rather than an
// error: zero configuration is a working configuration (§16), so the hub must
// start without one.
//
// This is a deliberate ~60-line reader for the three shapes this file uses rather
// than a TOML dependency: `alias`, `enabled`, `tags`, `socket` inside `[[host]]`.
// A dependency for `enabled = true` is not worth the supply chain.
func LoadHosts(path string) ([]Entry, error) {
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var out []Entry
	var cur *Entry
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if line == "[[host]]" {
			out = append(out, Entry{})
			cur = &out[len(out)-1]
			continue
		}
		if cur == nil {
			continue
		}
		key, val, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		val = strings.TrimSpace(val)
		switch key {
		case "alias":
			cur.Alias = unquote(val)
		case "socket":
			cur.Socket = unquote(val)
		case "enabled":
			cur.Enabled = val == "true"
		case "tags":
			cur.Tags = parseList(val)
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	// An entry with no alias cannot be acted on, so it is dropped rather than
	// carried as a nameless host.
	kept := out[:0]
	for _, e := range out {
		if e.Alias != "" {
			kept = append(kept, e)
		}
	}
	return kept, nil
}

func unquote(v string) string {
	v = strings.TrimSpace(v)
	if len(v) >= 2 && (v[0] == '"' || v[0] == '\'') && v[len(v)-1] == v[0] {
		return v[1 : len(v)-1]
	}
	return v
}

func parseList(v string) []string {
	v = strings.TrimSpace(v)
	v = strings.TrimPrefix(v, "[")
	v = strings.TrimSuffix(v, "]")
	var out []string
	for _, part := range strings.Split(v, ",") {
		if s := unquote(strings.TrimSpace(part)); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// SaveHosts writes the file the picker generates. It is generated rather than
// documented so it cannot drift from what LoadHosts understands.
func SaveHosts(path string, es []Entry) error {
	var b strings.Builder
	b.WriteString("# Generated by `tmux-hub` — hand-editable.\n")
	b.WriteString("# Reachability comes from ~/.ssh/config; this file only records\n")
	b.WriteString("# which hosts you want watched, and how you group them.\n")
	for _, e := range es {
		b.WriteString("\n[[host]]\n")
		fmt.Fprintf(&b, "alias = %q\n", e.Alias)
		fmt.Fprintf(&b, "enabled = %t\n", e.Enabled)
		if len(e.Tags) > 0 {
			quoted := make([]string, 0, len(e.Tags))
			for _, t := range e.Tags {
				quoted = append(quoted, fmt.Sprintf("%q", t))
			}
			fmt.Fprintf(&b, "tags = [%s]\n", strings.Join(quoted, ", "))
		}
		if e.Socket != "" {
			fmt.Fprintf(&b, "socket = %q\n", e.Socket)
		}
	}
	if err := os.MkdirAll(dirOf(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(b.String()), 0o600)
}

func dirOf(p string) string {
	if i := strings.LastIndexByte(p, '/'); i > 0 {
		return p[:i]
	}
	return "."
}

var versionLine = regexp.MustCompile(`^tmux (\S+)`)

// Probe decides membership. It keys on STDOUT, never on the exit code: measured,
// a host with no tmux answers rc=0 from the shell while printing
// `command not found`, so an rc-keyed probe admits a host that fails
// mysteriously later.
func Probe(ctx context.Context, alias string, timeout time.Duration) (version, tmpdir string, err error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "ssh",
		"-o", "BatchMode=yes",
		"-o", "ConnectTimeout=5",
		alias,
		`tmux -V; echo "TMPDIR=${TMUX_TMPDIR:-/tmp}"`)
	var out, errb strings.Builder
	cmd.Stdout = &out
	cmd.Stderr = &errb
	_ = cmd.Run() // the exit code is deliberately ignored; see parseProbe

	version, tmpdir, perr := parseProbe(out.String())
	if perr != nil {
		detail := strings.TrimSpace(errb.String())
		if detail == "" {
			detail = perr.Error()
		}
		return "", "", fmt.Errorf("%s: %s", alias, firstLine(detail))
	}
	return version, tmpdir, nil
}

func parseProbe(stdout string) (version, tmpdir string, err error) {
	for _, l := range strings.Split(stdout, "\n") {
		l = strings.TrimSpace(l)
		if m := versionLine.FindStringSubmatch(l); m != nil {
			version = m[1]
		}
		if v, ok := strings.CutPrefix(l, "TMPDIR="); ok {
			tmpdir = v
		}
	}
	if version == "" {
		return "", "", fmt.Errorf("no tmux version in the reply")
	}
	if tmpdir == "" {
		tmpdir = "/tmp"
	}
	return version, tmpdir, nil
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
```

- [ ] **Step 5: Run the tests**

Run: `go test ./internal/hostset/ -v`
Expected: PASS. `TestProbeHonoursItsDeadline` needs no network — a nonexistent host fails at DNS.

- [ ] **Step 6: Commit**

```bash
gofmt -l . && go test -race ./... && git add internal/hostset/ && git commit -m "feat(hostset): where hosts come from, and who is admitted

One Host line can name several hosts — measured, 15 lines expand to 20 names
here — so treating the rest of the line as one name silently loses five.
Wildcards and the systemd pseudo-hosts are FLAGGED rather than dropped, so a
picker can say 'not a host' instead of just showing a shorter list. Include is
deliberately not followed: the user's own config has none, and the patterns
worth filtering live in a systemd drop-in reached through the SYSTEM config.

Membership keys on stdout matching '^tmux <version>', never on an exit code:
studio-ws answers rc=0 with no tmux installed, so an rc-keyed probe admits a
host that fails mysteriously later.

hosts.toml is read by a small deliberate reader rather than a TOML dependency —
a dependency for 'enabled = true' is not worth the supply chain — and a missing
file is an empty list, because zero configuration is a working configuration."
```

---

### Task 6: The supervisor — connect, back off, re-promote

**Files:**
- Create: `internal/hub/supervisor.go`
- Test: `internal/hub/supervisor_test.go`
- Modify: `internal/hub/poll.go` (host states gain a re-entry rule)

**Interfaces:**
- Consumes: `tunnel`, `hostset`, `Poller`.
- Produces:
  - `type Supervisor struct { … }`
  - `func NewSupervisor(rt string, p *Poller) *Supervisor`
  - `func (s *Supervisor) Want(e hostset.Entry, dest, remoteSocket string)`
  - `func (s *Supervisor) Reconcile(ctx context.Context) []Host`
  - `func (s *Supervisor) Ensure(ctx context.Context, now time.Time) []Host`
  - `func (s *Supervisor) Failures() map[string]string`
  - `func (s *Supervisor) Close() error`
  - `func Backoff(attempt int) time.Duration`

Both `Reconcile` and `Ensure` **return** hosts rather than registering them,
because `Poller.Add` is not safe to call from a goroutine — see Global Constraints.

- [ ] **Step 1: Write the failing test**

Create `internal/hub/supervisor_test.go`:

```go
package hub

import (
	"testing"
	"time"
)

// Tunnel loss is the NORMAL outcome of a network blip — the keepalive is designed
// to drop it — so reconnect is routine and the backoff must not punish a host for
// a hiccup, nor hammer one that is genuinely gone.
func TestBackoffGrowsAndIsCapped(t *testing.T) {
	first := Backoff(0)
	if first > 2*time.Second {
		t.Errorf("the first retry should be prompt, got %v", first)
	}
	prev := first
	for i := 1; i < 12; i++ {
		got := Backoff(i)
		if got < prev {
			t.Fatalf("Backoff(%d) = %v, shorter than Backoff(%d) = %v", i, got, i-1, prev)
		}
		prev = got
	}
	if prev > 2*time.Minute {
		t.Errorf("the cap is too high at %v: a host that comes back should be picked up promptly", prev)
	}
	if Backoff(100) != Backoff(12) {
		t.Error("beyond the cap the delay should stop growing")
	}
}

// Every non-up state needs a way OUT, or a host that recovers stays broken on
// screen forever.
func TestEveryFailedStateIsRetried(t *testing.T) {
	for _, st := range []Status{Down, UpEmpty, DegradedFormat} {
		if !retryable(st) {
			t.Errorf("%v has no way back to up", st)
		}
	}
	if retryable(Up) {
		t.Error("an up host should not be on the retry path")
	}
}
```

- [ ] **Step 2: Run it**

Run: `go test ./internal/hub/ -run 'Backoff|Retried' -v`
Expected: FAIL — `undefined: Backoff`.

- [ ] **Step 3: Write the implementation**

Create `internal/hub/supervisor.go`:

```go
package hub

import (
	"context"
	"math"
	"sync"
	"time"

	"github.com/DawnBreather/tmux-hub/internal/hostset"
	"github.com/DawnBreather/tmux-hub/internal/transport"
	"github.com/DawnBreather/tmux-hub/internal/tunnel"
)

// Backoff is the delay before retrying a host. It starts prompt because tunnel
// loss is the NORMAL outcome of a blip — the keepalive is designed to drop a
// stalled tunnel after ~45 s — and it caps low because a host that comes back
// should appear soon rather than after an exponential punishment.
func Backoff(attempt int) time.Duration {
	const base = time.Second
	const ceiling = 60 * time.Second
	if attempt < 0 {
		attempt = 0
	}
	if attempt > 12 {
		attempt = 12
	}
	d := time.Duration(float64(base) * math.Pow(2, float64(attempt)))
	if d > ceiling {
		return ceiling
	}
	return d
}

// retryable says whether a state has a way back to up. Every non-up state must,
// or a host that recovers stays broken on screen forever.
func retryable(s Status) bool {
	switch s {
	case Down, UpEmpty, DegradedFormat, Connecting:
		return true
	default:
		return false
	}
}

type want struct {
	entry        hostset.Entry
	dest         string
	remoteSocket string

	tun      *tunnel.Tunnel
	adopted  bool // a previous run's master, alive and reused; not ours to kill
	attempt  int
	nextTry  time.Time
	lastFail string
}

// Supervisor owns the tunnels. It is the piece that makes `--host` unnecessary.
type Supervisor struct {
	rt     string
	poller *Poller

	mu    sync.Mutex
	hosts map[string]*want
}

func NewSupervisor(rt string, p *Poller) *Supervisor {
	return &Supervisor{rt: rt, poller: p, hosts: map[string]*want{}}
}

// Want registers a host the user has enabled.
func (s *Supervisor) Want(e hostset.Entry, dest, remoteSocket string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.hosts[e.Alias] = &want{entry: e, dest: dest, remoteSocket: remoteSocket}
}

// Reconcile adopts or reaps what a previous run left behind, and RETURNS the
// hosts it adopted for the caller to register. This is the NORMAL startup path,
// not an error path: a hub that crashed leaves live tunnels, and adopting them is
// both faster than rebuilding and the only way to avoid orphaning their ssh
// processes.
//
// An adopted host deliberately leaves `want.tun` nil, because this hub does not
// own that ssh and must not kill it on quit — someone else's master outliving us
// is correct, and the next run's Survey will adopt it again.
func (s *Supervisor) Reconcile(ctx context.Context) []Host {
	s.mu.Lock()
	aliases := make([]string, 0, len(s.hosts))
	for a := range s.hosts {
		aliases = append(aliases, a)
	}
	s.mu.Unlock()

	found, _ := tunnel.Survey(s.rt, aliases)
	var adopted []Host
	for _, f := range found {
		if f.Kind != transport.Live {
			_ = tunnel.Reap(s.rt, f)
			continue
		}
		sock, ctl, _ := tunnel.Paths(s.rt, f.Alias)
		s.mu.Lock()
		w := s.hosts[f.Alias]
		if w != nil {
			w.adopted = true // so Ensure does not also spawn one
		}
		s.mu.Unlock()
		if w != nil {
			adopted = append(adopted, Host{Label: f.Alias, Socket: sock,
				SSHDest: w.dest, ControlPath: ctl})
		}
	}
	return adopted
}

// Ensure opens a tunnel for every wanted host that has none, respecting each
// host's backoff, and returns the hosts that came up.
//
// It returns them rather than calling poller.Add itself: Poller.hosts is
// unguarded and Tick hands out &p.hosts[i], so an Add from one of these
// goroutines both races that read and can reallocate the slice underneath a
// running tick. The caller registers them from the single thread that owns the
// model.
func (s *Supervisor) Ensure(ctx context.Context, now time.Time) []Host {
	s.mu.Lock()
	pending := make([]*want, 0, len(s.hosts))
	for _, w := range s.hosts {
		if w.tun == nil && !w.adopted && !now.Before(w.nextTry) {
			pending = append(pending, w)
		}
	}
	s.mu.Unlock()

	var (
		wg    sync.WaitGroup
		mu    sync.Mutex
		fresh []Host
	)
	for _, w := range pending {
		wg.Add(1)
		go func(w *want) {
			defer wg.Done()
			tun, err := tunnel.Open(ctx, s.rt, tunnel.Spec{
				Alias:        w.entry.Alias,
				SSHDest:      w.dest,
				RemoteSocket: w.remoteSocket,
			}, 20*time.Second)

			s.mu.Lock()
			if err != nil {
				w.attempt++
				w.nextTry = now.Add(Backoff(w.attempt))
				w.lastFail = err.Error()
				s.mu.Unlock()
				return
			}
			w.tun, w.attempt, w.lastFail = tun, 0, ""
			h := Host{Label: w.entry.Alias, Socket: tun.SocketPath(),
				SSHDest: w.dest, ControlPath: tun.ControlPath()}
			s.mu.Unlock()

			mu.Lock()
			fresh = append(fresh, h)
			mu.Unlock()
		}(w)
	}
	wg.Wait()
	return fresh
}

// Failures is what a host row shows while it is down. It carries the LAST
// failure's own words rather than a generic "down", because the three causes look
// identical on screen and want different actions: a refused forward is a stale
// path, a refused auth is an agent problem, and a timeout is the network.
func (s *Supervisor) Failures() map[string]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]string, len(s.hosts))
	for a, w := range s.hosts {
		if w.lastFail != "" {
			out[a] = w.lastFail
		}
	}
	return out
}

// Close reaps every tunnel this hub owns. Without it a quit leaks one ssh per
// host, and the next run's Survey has to clean up after it.
func (s *Supervisor) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, w := range s.hosts {
		if w.tun != nil {
			_ = w.tun.Close()
			w.tun = nil
		}
	}
	return nil
}
```

- [ ] **Step 4: Run the tests**

Run: `go test ./internal/hub/ -v`
Expected: PASS, including the existing hub tests.

- [ ] **Step 5: Commit**

```bash
gofmt -l . && go test -race ./... && git add internal/hub/supervisor.go internal/hub/supervisor_test.go && git commit -m "feat(hub): the supervisor, so --host stops being necessary

Reconcile is the NORMAL startup path rather than an error path: a hub that
crashed leaves live tunnels behind, and adopting them is both faster than
rebuilding and the only way to avoid orphaning their ssh processes — measured,
unlinking a live forwarded socket leaves the master running forever.

Backoff starts prompt and caps at a minute because tunnel loss is the normal
outcome of a blip: the keepalive is DESIGNED to drop a stalled tunnel after
about 45 seconds, so a reconnect is routine and an exponential punishment would
keep a recovered host off screen. Every non-up state is on the retry path,
because a state with no way out leaves a recovered host broken forever."
```

---

### Task 7: The picker, and hosts without a flag

**Files:**
- Create: `internal/ui/picker.go`
- Test: `internal/ui/picker_test.go`
- Modify: `cmd/tmux-hub/main.go`
- Modify: `internal/ui/model.go`

**Interfaces:**
- Consumes: `hostset`, `Supervisor`.
- Produces:
  - `type PickerRow struct { Alias, Reason string; Enabled, Usable bool; Version string }`
  - `func RenderPicker(rows []PickerRow, width, height, cursor int) []string`
  - `p` opens it, `space` toggles, `enter` saves and reconnects.

- [ ] **Step 1: Write the failing test**

Create `internal/ui/picker_test.go`:

```go
package ui

import (
	"strings"
	"testing"
)

// Ten hosts probed concurrently took 7.65s, all of it the two slowest, so the
// picker shows rows as they resolve and never blocks on the set.
func TestPickerShowsUnresolvedRows(t *testing.T) {
	rows := []PickerRow{
		{Alias: "nuc", Usable: true, Enabled: true, Version: "3.2a"},
		{Alias: "eu", Usable: true},
		{Alias: "studio-ws", Usable: true, Reason: "no tmux — install it, or leave this host off"},
	}
	out := strings.Join(RenderPicker(rows, 80, 10, 0), "\n")
	if !strings.Contains(out, "3.2a") {
		t.Error("a resolved host should show its version")
	}
	if !strings.Contains(out, "no tmux") {
		t.Error("an excluded host should show why")
	}
	if !strings.Contains(out, "eu") {
		t.Error("an unresolved host should still be listed")
	}
}

// A wildcard or pseudo-host is listed with its reason rather than hidden, so the
// user is not left wondering where an alias went.
func TestPickerExplainsUnusableCandidates(t *testing.T) {
	rows := []PickerRow{{Alias: "unix/*", Reason: "not a host — a wildcard pattern"}}
	out := strings.Join(RenderPicker(rows, 80, 6, 0), "\n")
	if !strings.Contains(out, "not a host") {
		t.Errorf("want the reason on screen:\n%s", out)
	}
	if strings.Contains(out, "[x]") {
		t.Error("an unusable candidate must not look selectable")
	}
}

func TestPickerMarksTheEnabledOnes(t *testing.T) {
	rows := []PickerRow{
		{Alias: "nuc", Usable: true, Enabled: true},
		{Alias: "eu", Usable: true, Enabled: false},
	}
	out := RenderPicker(rows, 80, 6, 0)
	if !strings.Contains(out[0], "[x]") {
		t.Errorf("enabled row = %q", out[0])
	}
	if strings.Contains(out[1], "[x]") {
		t.Errorf("disabled row = %q", out[1])
	}
}

func TestPickerFitsItsWidth(t *testing.T) {
	rows := []PickerRow{{Alias: strings.Repeat("long-", 30), Usable: true,
		Reason: strings.Repeat("because ", 20)}}
	for _, w := range []int{40, 80, 120} {
		for _, l := range RenderPicker(rows, w, 6, 0) {
			if got := lineWidth(l); got > w {
				t.Errorf("width %d: a row is %d cells", w, got)
			}
		}
	}
}
```

The width test needs one helper, and without it the package does not compile.
Append to `internal/ui/picker_test.go`:

```go
func lineWidth(s string) int { return widthOf(s) }
```

`widthOf` is the thin wrapper over `lines.Width` defined in `picker.go` below, so the
test does not import a second package for one call.

- [ ] **Step 2: Run it**

Run: `go test ./internal/ui/ -run Picker -v`
Expected: FAIL — `undefined: PickerRow`.

- [ ] **Step 3: Write the picker**

Create `internal/ui/picker.go`:

```go
package ui

import (
	"fmt"

	"github.com/DawnBreather/tmux-hub/internal/lines"
)

// PickerRow is one candidate host as the picker shows it.
type PickerRow struct {
	Alias   string
	Enabled bool
	Usable  bool   // false for a wildcard or pseudo-host
	Version string // filled once the probe answers
	Reason  string // why it is excluded, or why the probe failed
}

func widthOf(s string) int { return lines.Width(s) }

// RenderPicker draws the host list. Rows appear before their probes finish,
// because ten hosts probed concurrently took 7.65 s — all of it the two slowest —
// and a picker that waits for the set is a picker nobody sees for eight seconds.
//
// An unusable candidate is LISTED with its reason rather than hidden: a user who
// cannot find an alias they know is in their ssh config has been left guessing.
func RenderPicker(rows []PickerRow, width, height, cursor int) []string {
	out := make([]string, 0, height)
	for i, r := range rows {
		if len(out) >= height {
			break
		}
		point := " "
		if i == cursor {
			point = ">"
		}
		box := "[ ]"
		switch {
		case !r.Usable:
			box = "   "
		case r.Enabled:
			box = "[x]"
		}
		right := r.Version
		if right == "" {
			right = r.Reason
		}
		if right == "" {
			right = "probing…"
		}
		out = append(out, lines.Truncate(
			fmt.Sprintf("%s%s %-24s %s", point, box, r.Alias, right), width))
	}
	for len(out) < height {
		out = append(out, "")
	}
	return out
}
```

- [ ] **Step 4: Wire it into the binary**

`--host` is removed rather than deprecated. It existed only because the hub could
not spawn ssh; leaving two ways to name a host means two paths to keep correct, and
the one that stays is the one a picker can write.

Replace the flag block and `main` in `cmd/tmux-hub/main.go`. The whole `hostFlags`
type goes with it:

```go
func main() {
	runtimeDir := flag.String("runtime-dir", "",
		"where per-host sockets live (default $XDG_RUNTIME_DIR/tmux-hub)")
	hostsFile := flag.String("hosts", defaultHostsPath(),
		"which hosts to watch; written by the picker, hand-editable")
	noLocal := flag.Bool("no-local", false, "do not watch the local tmux server")
	logStates := flag.String("log-states", "",
		"append every state transition to this JSONL file, to ground the timing thresholds")
	status := flag.Bool("status", false, "print one poll cycle as JSON and exit")
	flag.Parse()
	if flag.Arg(0) == "status" {
		*status = true
	}

	rt := *runtimeDir
	if rt == "" {
		var err error
		// Resolved here rather than lazily, so a broken environment is one clear
		// message at startup instead of every host reporting itself down.
		if rt, err = tunnel.RuntimeDir(); err != nil {
			fmt.Fprintln(os.Stderr, "tmux-hub:", err)
			os.Exit(1)
		}
	}

	// One hub owns one runtime directory. Two would each spawn a master for the
	// same host, and the loser's reconciliation is tempted to unlink the winner's
	// live socket — which orphans that ssh rather than replacing it.
	lock, err := tunnel.Acquire(rt)
	if err != nil {
		fmt.Fprintln(os.Stderr, "tmux-hub:", err)
		os.Exit(1)
	}
	defer lock.Release()

	entries, err := hostset.LoadHosts(*hostsFile)
	if err != nil {
		fmt.Fprintln(os.Stderr, "tmux-hub: reading", *hostsFile+":", err)
		os.Exit(1)
	}

	if *status {
		if err := runStatus(rt, entries, !*noLocal); err != nil {
			fmt.Fprintln(os.Stderr, "tmux-hub:", err)
			os.Exit(1)
		}
		return
	}

	var log *hub.StateLog
	if *logStates != "" {
		l, err := hub.OpenStateLog(*logStates)
		if err != nil {
			fmt.Fprintln(os.Stderr, "tmux-hub:", err)
			os.Exit(1)
		}
		log = l
		defer log.Close()
	}

	if err := ui.Run(context.Background(), ui.Config{
		RuntimeDir: rt,
		HostsFile:  *hostsFile,
		Entries:    entries,
		Local:      !*noLocal,
		StateLog:   log,
	}); err != nil {
		fmt.Fprintln(os.Stderr, "tmux-hub:", err)
		os.Exit(1)
	}
}

// defaultHostsPath is $XDG_CONFIG_HOME/tmux-hub/hosts.toml. A missing file is an
// empty host list rather than an error, so the hub starts with zero configuration
// and the picker is how you get one.
func defaultHostsPath() string {
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "hosts.toml"
		}
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "tmux-hub", "hosts.toml")
}
```

`runStatus` grows the same two arguments and reconciles before it polls, so a
one-shot report sees adopted tunnels rather than reporting every remote host down:

```go
func runStatus(rt string, entries []hostset.Entry, local bool) error {
	reg := registry.New()
	p := hub.NewPoller(tmux.NewExec(5*time.Second), reg)
	if local {
		p.AddLocal()
	}
	sup := hub.NewSupervisor(rt, p)
	for _, e := range entries {
		if e.Enabled {
			sup.Want(e, e.Alias, remoteSocketFor(e))
		}
	}
	defer sup.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	for _, h := range sup.Reconcile(ctx) {
		p.Add(h)
	}
	for _, h := range sup.Ensure(ctx, time.Now()) {
		p.Add(h)
	}
	// … the existing two-tick body, unchanged from here
	p.Tick(ctx, time.Now(), nil)
	want := map[string]bool{}
	for _, pn := range reg.Panes() {
		want[pn.Host+"\x00"+pn.PaneID] = true
	}
	hosts := p.Tick(ctx, time.Now(), want)
	p.TickAgents(ctx, time.Now())

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(hub.BuildReport(hosts, reg.Panes()))
}

// remoteSocketFor is the tmux socket path on the far side. The probe reports the
// host's own TMUX_TMPDIR, and an explicit `socket =` in hosts.toml wins — that is
// how a `tmux -L other` server is reached.
func remoteSocketFor(e hostset.Entry) string {
	if e.Socket != "" {
		return e.Socket
	}
	return "/tmp/tmux-1000/default"
}
```

- [ ] **Step 5: Wire the picker into the model**

`ui.Run` takes a `Config` now, and the model owns the supervisor:

```go
// Config is what the binary hands the UI. It is a struct rather than five
// positional arguments because the next addition would make the call site
// unreadable.
type Config struct {
	RuntimeDir string
	HostsFile  string
	Entries    []hostset.Entry
	Local      bool
	StateLog   *hub.StateLog
}

func Run(ctx context.Context, cfg Config) error {
	reg := registry.New()
	p := hub.NewPoller(tmux.NewExec(5*time.Second), reg)
	if cfg.Local {
		p.AddLocal()
	}
	sup := hub.NewSupervisor(cfg.RuntimeDir, p)
	for _, e := range cfg.Entries {
		if e.Enabled {
			sup.Want(e, e.Alias, remoteSocketFor(e))
		}
	}
	defer sup.Close()

	m := model{poller: p, reg: reg, sup: sup, cfg: cfg,
		marked: map[string]bool{}, ctx: ctx, width: 80, height: 24,
		log: cfg.StateLog}
	_, err := tea.NewProgram(m, tea.WithAltScreen()).Run()
	return err
}
```

The supervisor runs on its own slower cadence, and — this is the constraint from
the Global Constraints section — every host it produces is registered **here**, on
the model's own goroutine, never inside the supervisor:

```go
// SupervisorInterval is how often tunnels are reconsidered. It is far slower than
// the poll because opening a tunnel is seconds, not milliseconds, and a host on
// backoff has nothing to do until its timer comes round.
const SupervisorInterval = 3 * time.Second

type superviseMsg struct{ fresh []hub.Host }
type superviseNow struct{}

func (m model) supervise() tea.Cmd {
	sup, ctx := m.sup, m.ctx
	return func() tea.Msg { return superviseMsg{fresh: sup.Ensure(ctx, time.Now())} }
}

// reconcile adopts what a previous run left behind. It runs ONCE, at startup, and
// it is the normal path rather than an error path: a hub that crashed leaves live
// tunnels, and adopting them is both faster and the only way not to orphan their
// ssh processes.
func (m model) reconcile() tea.Cmd {
	sup, ctx := m.sup, m.ctx
	return func() tea.Msg { return superviseMsg{fresh: sup.Reconcile(ctx)} }
}

func (m model) Init() tea.Cmd {
	return tea.Batch(m.reconcile(), m.poll(), m.pollAgents())
}
```

and in `Update`:

```go
	case superviseMsg:
		// Registered on THIS goroutine. Poller.hosts is unguarded and Tick hands
		// out &p.hosts[i], so an Add from the supervisor's goroutine would both
		// race that read and be able to reallocate the slice under a running tick.
		for _, h := range msg.fresh {
			m.poller.Add(h)
		}
		if n := len(msg.fresh); n > 0 {
			m.note = fmt.Sprintf("%d host(s) connected", n)
		}
		return m, tea.Tick(SupervisorInterval, func(time.Time) tea.Msg {
			return superviseNow{}
		})

	case superviseNow:
		return m, m.supervise()
```

The picker keys. `p` opens it, and the rows are probed concurrently so the screen
is useful before the slow hosts answer:

```go
type pickerMsg struct {
	rows []PickerRow
	err  error
}
type probedMsg struct {
	alias, version, reason string
}

// openPicker lists every candidate from ~/.ssh/config immediately, with the
// enabled ones already ticked, and probes them in the background. Ten hosts probed
// concurrently took 7.65 s — all of it the two slowest — so a picker that waits
// for the set is one nobody sees for eight seconds.
func (m model) openPicker() (tea.Model, tea.Cmd) {
	enabled := map[string]bool{}
	for _, e := range m.cfg.Entries {
		enabled[e.Alias] = e.Enabled
	}
	f, err := os.Open(sshConfigPath())
	if err != nil {
		m.note = "cannot read ~/.ssh/config: " + err.Error()
		return m, nil
	}
	defer f.Close()

	var rows []PickerRow
	for _, c := range hostset.ParseSSHConfig(f) {
		r := PickerRow{Alias: c.Alias, Usable: c.Usable(), Enabled: enabled[c.Alias]}
		switch {
		case c.Wildcard:
			r.Reason = "not a host — a wildcard pattern"
		case c.Unroutable:
			r.Reason = "not a host — a systemd pseudo-host"
		}
		rows = append(rows, r)
	}
	m.mode, m.picker, m.pickCursor = modePicker, rows, 0

	var cmds []tea.Cmd
	for _, r := range rows {
		if !r.Usable {
			continue
		}
		alias := r.Alias
		cmds = append(cmds, func() tea.Msg {
			v, _, err := hostset.Probe(m.ctx, alias, 15*time.Second)
			if err != nil {
				return probedMsg{alias: alias, reason: err.Error()}
			}
			return probedMsg{alias: alias, version: "tmux " + v}
		})
	}
	return m, tea.Batch(cmds...)
}

func (m model) pickerKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "q":
		m.mode, m.picker = modeBrowse, nil
		return m, nil
	case "j", "down":
		if m.pickCursor < len(m.picker)-1 {
			m.pickCursor++
		}
	case "k", "up":
		if m.pickCursor > 0 {
			m.pickCursor--
		}
	case " ":
		if m.pickCursor < len(m.picker) && m.picker[m.pickCursor].Usable {
			m.picker[m.pickCursor].Enabled = !m.picker[m.pickCursor].Enabled
		}
	case "enter":
		// Saved BEFORE connecting: if the connect half fails, the choice survives
		// and a restart retries it. The reverse order loses the user's selection to
		// a network blip.
		var entries []hostset.Entry
		for _, r := range m.picker {
			if r.Usable {
				entries = append(entries, hostset.Entry{Alias: r.Alias, Enabled: r.Enabled})
			}
		}
		if err := hostset.SaveHosts(m.cfg.HostsFile, entries); err != nil {
			m.note = "cannot save " + m.cfg.HostsFile + ": " + err.Error()
			return m, nil
		}
		m.cfg.Entries = entries
		for _, e := range entries {
			if e.Enabled {
				m.sup.Want(e, e.Alias, remoteSocketFor(e))
			}
		}
		m.mode, m.picker = modeBrowse, nil
		m.note = "saved — connecting"
		return m, m.supervise()
	}
	return m, nil
}

func sshConfigPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ".ssh/config"
	}
	return filepath.Join(home, ".ssh", "config")
}
```

and the probe results land per-row, so a slow host does not hold up the rest:

```go
	case probedMsg:
		for i := range m.picker {
			if m.picker[i].Alias != msg.alias {
				continue
			}
			m.picker[i].Version, m.picker[i].Reason = msg.version, msg.reason
			// A host that answers without a tmux version is not admitted: measured,
			// a host with no tmux exits 0 from the shell, so only the version line
			// is evidence.
			if msg.version == "" {
				m.picker[i].Usable = false
				m.picker[i].Enabled = false
			}
		}
		return m, nil
```

Add `modePicker` to the mode constants, `picker []PickerRow` and `pickCursor int`
to `model`, `p` to the browse-mode key switch calling `openPicker`, and a `View`
branch rendering `RenderPicker(m.picker, m.width, m.height-2, m.pickCursor)`.

- [ ] **Step 6: Test the picker's decisions**

```go
// A probe that answers without a tmux version must UNSELECT the host. Measured, a
// host with no tmux exits 0 from the shell, so admitting it on a successful
// connection means it fails mysteriously later.
func TestAProbeWithoutAVersionDisablesTheRow(t *testing.T) {
	m := model{mode: modePicker, picker: []PickerRow{
		{Alias: "studio-ws", Usable: true, Enabled: true},
	}}
	got, _ := m.Update(probedMsg{alias: "studio-ws", reason: "no tmux in the reply"})
	row := got.(model).picker[0]
	if row.Enabled || row.Usable {
		t.Errorf("row = %+v, want it unselected and unusable", row)
	}
	if row.Reason == "" {
		t.Error("the row does not say why")
	}
}

// Space must not tick an unusable row — a wildcard is not something to connect to.
func TestSpaceCannotSelectAnUnusableRow(t *testing.T) {
	m := model{mode: modePicker, picker: []PickerRow{{Alias: "unix/*", Usable: false}}}
	got, _ := m.pickerKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(" ")})
	if got.(model).picker[0].Enabled {
		t.Error("a wildcard row was selected")
	}
}

// Enter saves before it connects, so a failed connect cannot lose the choice.
func TestEnterSavesBeforeConnecting(t *testing.T) {
	dir := t.TempDir()
	m := model{mode: modePicker,
		cfg:    Config{HostsFile: filepath.Join(dir, "hosts.toml"), RuntimeDir: dir},
		sup:    hub.NewSupervisor(dir, hub.NewPoller(tmux.NewExec(time.Second), registry.New())),
		picker: []PickerRow{{Alias: "nuc", Usable: true, Enabled: true}},
	}
	got, _ := m.pickerKey(tea.KeyMsg{Type: tea.KeyEnter})
	entries, err := hostset.LoadHosts(filepath.Join(dir, "hosts.toml"))
	if err != nil {
		t.Fatalf("LoadHosts: %v", err)
	}
	if len(entries) != 1 || !entries[0].Enabled || entries[0].Alias != "nuc" {
		t.Errorf("saved %+v", entries)
	}
	if got.(model).mode != modeBrowse {
		t.Error("enter did not close the picker")
	}
}
```

Run: `go test ./internal/ui/ -v`
Expected: PASS.

- [ ] **Step 7: Run everything**

Run: `go test -race ./... && go build ./... && ./tmux-hub`
Expected: tests PASS; with no `hosts.toml` the hub shows local sessions and `p`
offers the ssh-config candidates; enabling one connects it without a flag.

- [ ] **Step 8: Commit**

```bash
gofmt -l . && go test -race ./... && git add internal/ui/ cmd/ && git commit -m "feat(ui): the host picker, and --host is gone

Rows appear before their probes finish: ten hosts probed concurrently took
7.65s, all of it the two slowest, so a picker that waits for the set is one
nobody sees for eight seconds. An unusable candidate is listed with its reason
rather than hidden, because a user who cannot find an alias they know is in
their ssh config has been left guessing.

--host is removed rather than deprecated. It existed only because the hub could
not spawn ssh; leaving two ways to name a host would mean two paths to keep
correct."
```

---

### Task 8: End to end against a real host

**Files:**
- Create: `internal/hub/e2e_test.go` (build-tagged)

- [ ] **Step 1: Write the test**

Create `internal/hub/e2e_test.go`:

```go
//go:build e2e

package hub

// Run with: go test -tags e2e ./internal/hub/ -run E2E -v -e2e-host=nuc
//
// Everything else in this suite runs against a fake ssh and a private tmux socket.
// This one needs a real host, so it is tagged out of the default run — but it is the
// only test that can catch what the fake cannot: an ssh_config with a ProxyCommand,
// a remote tmux of a different version (measured: 3.2a there against 3.7b here), and
// the real cost of a round trip.

import (
	"context"
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/DawnBreather/tmux-hub/internal/hostset"
	"github.com/DawnBreather/tmux-hub/internal/registry"
	"github.com/DawnBreather/tmux-hub/internal/tmux"
	"github.com/DawnBreather/tmux-hub/internal/tunnel"
)

var e2eHost = flag.String("e2e-host", "", "ssh destination to run the live test against")

func TestE2ERealHostTunnelLifecycle(t *testing.T) {
	if *e2eHost == "" {
		t.Skip("no -e2e-host given")
	}
	// The runtime dir is a temp dir, not the user's: this test spawns and reaps ssh
	// masters, and doing that in the real directory would fight a running hub for
	// the same socket paths.
	rt := t.TempDir()
	if err := os.Chmod(rt, 0o700); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	// The probe decides membership, and it keys on stdout: a host with no tmux
	// answers rc=0 from the shell, so a version is the only evidence.
	version, tmpdir, err := hostset.Probe(ctx, *e2eHost, 20*time.Second)
	if err != nil {
		t.Skipf("%s is not usable: %v", *e2eHost, err)
	}
	t.Logf("remote tmux %s, TMUX_TMPDIR=%s", version, tmpdir)

	uid, err := exec.Command("ssh", "-o", "BatchMode=yes", *e2eHost, "id -u").Output()
	if err != nil {
		t.Fatalf("cannot read the remote uid: %v", err)
	}
	remoteSocket := filepath.Join(tmpdir, "tmux-"+strings.TrimSpace(string(uid)), "default")

	spec := tunnel.Spec{Alias: *e2eHost, SSHDest: *e2eHost, RemoteSocket: remoteSocket}
	tun, err := tunnel.Open(ctx, rt, spec, 30*time.Second)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	pid := tun.PID()
	sock, ctl := tun.SocketPath(), tun.ControlPath()

	// A tunnel that opened is not a tunnel that WORKS: the assertion is that tmux
	// answers over it, which is the only thing the dashboard depends on.
	reg := registry.New()
	p := NewPoller(tmux.NewExec(10*time.Second), reg)
	p.Add(Host{Label: *e2eHost, Socket: sock, SSHDest: *e2eHost, ControlPath: ctl})

	var up Host
	deadline := time.Now().Add(45 * time.Second)
	for time.Now().Before(deadline) {
		hosts := p.Tick(ctx, time.Now(), nil)
		if len(hosts) != 1 {
			t.Fatalf("Tick returned %d hosts", len(hosts))
		}
		up = hosts[0]
		// UpEmpty is a legitimate answer — the host may simply have no tmux server
		// running — so both count as reachable and Down does not.
		if up.Status == Up || up.Status == UpEmpty {
			break
		}
		time.Sleep(time.Second)
	}
	if up.Status != Up && up.Status != UpEmpty {
		t.Fatalf("host never became reachable: status=%v reason=%q", up.Status, up.Reason)
	}
	if up.Version == "" {
		t.Errorf("reachable but no version recorded — the field assertion did not run")
	}
	// The version must be the REMOTE tmux, not ours. A squatter on the forward path
	// answers as itself, and that is the failure this catches.
	if up.Version != version {
		t.Errorf("poller saw tmux %q, the probe saw %q — the socket may not reach the far end",
			up.Version, version)
	}
	t.Logf("host up: status=%v version=%s panes=%d", up.Status, up.Version, len(reg.Panes()))

	// Teardown must leave nothing behind: a surviving socket makes the next Open
	// fail on a path nothing listens to, and a surviving master is unreachable but
	// not dead.
	if err := tun.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	for _, path := range []string{sock, filepath.Join(rt, tunnel.SocketLabel(*e2eHost)+".pid")} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("%s survived Close (err=%v)", filepath.Base(path), err)
		}
	}
	if pid > 0 {
		if out, err := exec.Command("ps", "-o", "pid=", "-p", strconv.Itoa(pid)).Output(); err == nil &&
			strings.TrimSpace(string(out)) != "" {
			t.Errorf("the ssh master (pid %d) is still running", pid)
		}
	}
}

// Reconcile must ADOPT a live tunnel rather than replace it. Measured: unlinking a
// live forwarded socket leaves its ssh running forever with an undialable path, so
// this is the case that decides whether a restart leaks one process per host.
func TestE2EReconcileAdoptsALiveTunnel(t *testing.T) {
	if *e2eHost == "" {
		t.Skip("no -e2e-host given")
	}
	rt := t.TempDir()
	if err := os.Chmod(rt, 0o700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	_, tmpdir, err := hostset.Probe(ctx, *e2eHost, 20*time.Second)
	if err != nil {
		t.Skipf("%s is not usable: %v", *e2eHost, err)
	}
	uid, err := exec.Command("ssh", "-o", "BatchMode=yes", *e2eHost, "id -u").Output()
	if err != nil {
		t.Fatalf("uid: %v", err)
	}
	remote := filepath.Join(tmpdir, "tmux-"+strings.TrimSpace(string(uid)), "default")

	// Stand in for a previous run of the hub.
	first, err := tunnel.Open(ctx, rt, tunnel.Spec{
		Alias: *e2eHost, SSHDest: *e2eHost, RemoteSocket: remote}, 30*time.Second)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	firstPID := first.PID()
	t.Cleanup(func() { _ = first.Close() })

	// A fresh supervisor, as a restarted hub would build one.
	reg := registry.New()
	p := NewPoller(tmux.NewExec(10*time.Second), reg)
	sup := NewSupervisor(rt, p)
	sup.Want(hostset.Entry{Alias: *e2eHost, Enabled: true}, *e2eHost, remote)

	adopted := sup.Reconcile(ctx)
	if len(adopted) != 1 {
		t.Fatalf("Reconcile adopted %d hosts, want 1 — a live tunnel was not recognised", len(adopted))
	}
	if adopted[0].Socket != first.SocketPath() {
		t.Errorf("adopted %q, want the existing %q", adopted[0].Socket, first.SocketPath())
	}
	// Ensure must NOT spawn a second master for an adopted host.
	if fresh := sup.Ensure(ctx, time.Now()); len(fresh) != 0 {
		t.Errorf("Ensure opened %d more tunnels for an already-adopted host", len(fresh))
	}
	if out, err := exec.Command("ps", "-o", "pid=", "-p", strconv.Itoa(firstPID)).Output(); err != nil ||
		strings.TrimSpace(string(out)) == "" {
		t.Errorf("the adopted master (pid %d) is gone — adoption killed what it adopted", firstPID)
	}
	// And this hub must not reap someone else's master on shutdown.
	if err := sup.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
	if out, err := exec.Command("ps", "-o", "pid=", "-p", strconv.Itoa(firstPID)).Output(); err != nil ||
		strings.TrimSpace(string(out)) == "" {
		t.Errorf("Close reaped the adopted master (pid %d), which this hub does not own", firstPID)
	}
}
```

- [ ] **Step 2: Run it against a real host**

Run: `go test -tags e2e ./internal/hub/ -run E2E -v -e2e-host=nuc`
Expected: PASS both cases. Then check by hand that nothing was left:
`ls $(go env GOTMPDIR 2>/dev/null || echo /tmp)/Test*` is empty of sockets, and
`ps -o pid=,cmd= -u "$(id -u)" | grep '[s]sh -N -M'` lists only masters you started
yourself.

Note the flag is `-e2e-host`, not `-host`: `go test` would otherwise collide with
nothing, but a name that reads like the removed `--host` flag invites confusion in
exactly the plan that removes it.

- [ ] **Step 3: Commit**

```bash
gofmt -l . && go test -race ./... && git add internal/hub/e2e_test.go && git commit -m "test(hub): one end-to-end against a real host, tagged out of the default run

The fake ssh covers the lifecycle; it cannot cover an ssh_config with a
ProxyCommand, a remote tmux of a different version, or the real cost of a round
trip. This does, and it asserts the teardown leaves no socket, no pid file and
no surviving ssh."
```

---

## Self-Review

**Spec coverage for §5 and §9:**

| requirement | task |
|---|---|
| `$XDG_RUNTIME_DIR` asserted set, absolute, not a symlink, 0700 | 1 |
| socket label sanitised + hashed so an alias with `/` cannot break the path | 1 |
| one hub per runtime directory (`flock`, holder named) | 2 |
| spawn flags: `-N -M -S`, `-L`, `BatchMode`, `ExitOnForwardFailure`, `ServerAliveInterval` | 3 |
| readiness is a dial accepted **and held**, never the socket file | 3 |
| the child's pid on disk, so a later hub can reap an orphan | 3 |
| probe-then-act: adopt a live tunnel, reap only a dead one | 4 |
| kill by pid with signal 0 first | 4 |
| survey the **control** path too, and refuse a live master on it | 4 |
| ssh_config: multi-name lines, wildcards and pseudo-hosts flagged | 5 |
| membership keyed on stdout, not rc | 5 |
| `hosts.toml` generated, hand-editable, missing = empty | 5 |
| backoff, and every non-up state retried | 6 |
| shutdown reaps every tunnel | 6 |
| picker reachable, rows before probes, reasons shown | 7 |
| `--host` removed | 7 |
| one real-host test, tagged out | 8 |

**Deliberately not here:** §7 (the whole write path — its own plan), §8's resize
warning, §16's `--watch`, and the agent-pane identification by process tree. None
is reachable from this code.

**Type consistency:** `tunnel.Spec/Tunnel/Found` from Tasks 1–4 are used unchanged
in 6. `hostset.Entry/Candidate` from Task 5 is used in 6 and 7. `hub.Host` and
`hub.Host` already exists and is not changed; `hub.Dial` becomes
`transport.Dial` in Task 0 and every later reference uses that name.
`Supervisor` from Task 6 is used in 7.

**Seven defects this plan had before its code was extracted and run, recorded
because each is the kind that survives reading:**

1. `tunnel → hub → tunnel` was an import cycle. Task 0 exists only because of it.
2. Liveness was a signal-0 probe of the child. Signalling a **zombie succeeds**, so a
   dead master would have read as alive — and the `os.Signal(nil)` form it was written
   in returns an error unconditionally, so it would have read as dead *always* and
   `Open` would have failed on every host. One goroutine owning `cmd.Wait()` and a
   `done` channel is both correct and simpler.
3. The polite `ssh -O exit` ran without a deadline, so tearing down a *hung* master —
   the one case it is called for — blocked forever, and `TestOpenGivesUpOnAHangingSSH`
   would never have returned.
4. `Ensure` called `poller.Add` from its goroutines. `Poller.hosts` is unguarded and
   `Tick` takes `&p.hosts[i]`, so that both races the read and can reallocate the slice
   under a running tick. Both `Ensure` and `Reconcile` now return hosts.
5. The moved `dial_test.go` calls `liveTarget`, which lives in `poll_test.go` and stays
   behind. Task 0 did not compile until the helper came too — and the test it breaks is
   the only one that dials a real tmux server.
6. **`cmd.Stderr` as a `strings.Builder` made `cmd.Wait()` block forever.** A non-file
   writer makes exec create a pipe plus a copier goroutine, and `Wait` does not return
   while *any* process holds the write end — including one the child forked. Observed
   as a 40-second test timeout with `Open` having already succeeded, which reads as a
   hung `Close` rather than as a stderr decision. Stderr now goes to a temp file.
7. The lock's helper process announced itself with `t.Log`, which `go test` swallows
   without `-test.v`. The parent read only `PASS`, found no marker, and failed claiming
   a second process had taken a held lock — a false alarm in the worst direction for a
   mutual-exclusion test. It prints with `fmt` now.

**The four load-bearing tests were then calibrated on the negative pole**, by injecting
the defect each is meant to catch and confirming it goes red:

| injected defect | test that caught it |
|---|---|
| readiness keys on the socket file existing | `TestOpenDoesNotReturnOnTheFileAlone` |
| `Survey` unlinks before probing | `TestSurveyClassifiesWhatItFinds` |
| `Probe` keys on the exit code | `TestProbeKeysOnOutput` |
| a `Host` line's remainder taken as one name | `TestParseExpandsMultiNameHostLines` |

All four failed on the defect and passed on its removal, so none of them is decorative.
The `slow` mode of `fake-ssh` exists for the first row: it touches the socket path and
*then* sleeps before binding, reproducing the measured shape where the file appears
before the forward works. Without that, a file-based readiness check would pass the test.

**Verification performed:** every Go block in this plan was extracted into a copy of the
repository with Task 0 applied, and the result is `gofmt`-clean, `go vet`-clean, and
green under `go test -race ./...` — 144 tests across 11 packages, no leaked ssh
processes and no leaked stderr temp files.

**Known rough edges, called out so they are not mistaken for bugs:** the TOML
reader handles the three shapes this file uses and would need work for arrays of
tables nested deeper; `Reconcile` marks an adopted host `want.adopted` so `Ensure`
does not also spawn one, and deliberately leaves `want.tun` nil so `Close` does not
kill an ssh this hub does not own; and `firstLine` is defined in both `tunnel` and
`hostset`, which is duplication small enough that a shared package for it would
cost more than it saves.
