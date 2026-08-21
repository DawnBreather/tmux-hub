# Hosts and Transport Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** The hub finds its own hosts in `~/.ssh/config`, asks which ones to keep, and reaches every kept host over one ssh master — so `--host label=/path/to/socket` stops being how you use the tool.

**Architecture:** One long-lived `ssh -N -M -S <ctl>` per enabled host and no port forward. Every tmux command for a remote host is that host's validated tmux argv, shell-quoted, handed to `ssh -S <ctl> <host>`. Host candidates come from `~/.ssh/config` (plus the system config's `Include`), membership from a positive probe keyed on stdout, and the decision persists in a generated `~/.config/tmux-hub/hosts.toml` that a picker screen writes.

**Tech Stack:** Go 1.x, bubbletea, OpenSSH control masters, tmux 3.2a–3.7b.

**Spec:** `docs/design.md` §5 (transport, rewritten 2026-08-13), §9 (hosts and configuration), §16 (the timing commitments this must not break). It replaces `docs/plans/2026-08-12-transport-supervision.md`, which was written for the forwarded-socket design and never executed — see "What this plan drops" below.

## Global Constraints

Every value here is measured, most of them on this machine against its real fleet on 2026-08-13. Do not re-derive them and do not soften them.

- **No port forward, ever.** One ssh master per host, used for polling and for attach. The reason is the deleted failure class, NOT speed: measured with the product's own batch code against a real host, a poll cycle is **1337 ms over the master against 1432 ms through a forward** — four invocations either way, so 7%. The factor of two an earlier draft claimed came from a cycle assembled by hand into one ssh command line, which the product does not do.
- **A four-invocation cycle can become one, and that is a later task, not this one.** ssh's payload is an arbitrary shell command line, so `FetchSnapshot`'s labels, zones and full captures could travel together — worth ~1.3 s → ~0.33 s per remote tick. A forwarded socket could never do it. Do not attempt it inside this plan; the batch code's framing is what makes the captures parseable and changing both at once would make a failure unattributable.
- **Validate the tmux arguments, THEN wrap them.** `Validate` enforces the `-t` shape over argv (§7) and an ssh payload is one opaque string, so wrapping first blinds the guard that makes the write path safe.
- **The remote path has TWO shells** — tmux's `$SHELL -c` here and the login shell there — and a `$N` session id is expanded by either given the chance. One `shellQuote` per element, at both levels. This project has paid for it twice.
- **Membership keys on stdout matching `^tmux \S+`, never on the exit code.** Measured on five hosts at once (`studio-ws`, `web-ws`, `crater-ws`, `st-ws`, `qa-ws`): `ssh host 'tmux -V; id -u'` returns **rc=0 with no tmux installed**, because the shell's status belongs to the last command. An rc-keyed probe admits ten hosts where five are usable.
- **A timeout is a third outcome, not an exclusion, and it is 40% of this fleet.** Measured, three probes each: `eu` at 5.4/9.1/15.7/18.4 s and `web-app` at 4.4/7.4/19.6 s, every one answering `tmux 3.2a`, against `web-db` at 3.0/2.9/2.9 and `nuc` at 2.4/2.0/2.0. Two of five usable hosts swing ~4× and straddle any timeout; both were read as "the host is gone" on first encounter, by two different readers. **The picker therefore re-probes on demand (one key) and shows when it last asked** — otherwise correcting a coin flip means quitting the hub, and the file the user edits is a snapshot of a moment nobody recorded. A host swinging 3.4× straddles any fixed timeout, so membership keyed on "answered within N" makes it a coin flip that then gets written into `hosts.toml` as a decision. `Result` therefore carries `TimedOut` distinctly from `Usable`, the picker offers a tick box for a timed-out host, and its reason names the remedy: slow rather than absent, enable it anyway or raise the timeout. A host the user enabled is never un-enabled by a later timeout — that is a `connecting` row with a reason, not a membership question re-asked behind their back.
- **Probing never gates the UI.** 20 hosts probed concurrently took **7.01 s**, all of it the slowest host (`eu`, 7.0 s). §16 promises a usable dashboard before any network work.
- **The probe's ssh runner MUST pass `-o BatchMode=yes -o ConnectTimeout=6`, and they are not hygiene.** `ProbeAll` is concurrent, so the wall time is the slowest probe — and measured on the same host, `ssh metrics-engine 'tmux -V; id -u'` takes **133.3 s** bare against **6.1 s** with those two flags, because without `ConnectTimeout` a host that does not answer TCP holds until the system default and without `BatchMode` ssh can sit on a password prompt forever. A first run of 7 s and one of 134 s are the same code with two flags between them; Task 4's first real-fleet measurement came in at 134.45 s for exactly this reason, one host setting the whole wall. Whoever writes the production runner in Task 8 inherits this requirement, and `Runner`'s doc comment must carry it.
- **Dropping wildcards is not enough.** The system config's `Include` pulls in systemd's `20-systemd-ssh-proxy.conf`, whose `Host` patterns include `.host` and `machine/.host` — **no wildcard characters in either**. Unroutable names go too: anything containing `/`, `%`, or equal to `.host`.
- **`ssh -O check` says the master lives, not that tmux answers.** It is not health.
- **Spawning a master costs 1.55 s, so it can never be on the first-paint path.** Measured over three trials: 1530, 1551 and 1606 ms from `ssh -N -M` to the first `ssh -O check` that answers, about 28 failed checks at 50 ms apart. §16 promises a usable dashboard in under 50 ms, so Task 8's "concurrent and off the critical path" is an obligation, not a nicety — on the path it would miss the commitment by thirty times.
- **A master OUTLIVES the hub, reparented to pid 1.** Measured. That is the leaked-process class this design deleted for forwards, reappearing somewhere else — and here it is turned into an asset rather than fought: an existing master is adopted for **3.6 ms** where spawning one costs 1550, so the hub leaves masters running on exit deliberately and adopts them next time. What that requires is an explicit way out (see Task 6), because a leak nobody can clean up is still a leak even when it is fast.
- **A missing master is invisible.** Measured: pointed at a control path that is not a live master, `ssh -S <path> host 'tmux -V'` returns **rc=0** with the right answer — ssh silently opens its own connection. A dead master costs latency (323 ms → 7.0 s on the slowest host), not an error, so presence is asserted rather than inferred from failure.
- **`XDG_RUNTIME_DIR` is asserted at startup**, absolute and a real directory we own. Unset, `filepath.Join("", "tmux-hub")` is relative and the hub puts control sockets wherever it started.
- **Production UI strings are English.**
- **Every test must fail against the unmodified product**; show red, then green.
- Run tests with `rtk proxy go test …`, and quote any `-run` pattern — an unquoted `|` segfaults the rtk proxy and drops a core file.
- Gates: `gofmt -l .` silent; `go vet ./...`, `-tags mockup ./internal/ui/`, `-tags e2e ./internal/e2e/` clean; `go test -count=1 -race ./...` reporting 14 `ok`; `go test -count=1 -tags e2e ./internal/e2e/` ok. Verify the COMMIT via `git archive HEAD` in a clean directory, never the working tree.
- Commit messages: lowercase conventional prefix, no AI co-authorship trailers of any kind.
- An agent that needs its own branch uses `git worktree add`, never `git checkout -b` in the shared checkout — measured cost of getting this wrong: a controller's HEAD moved onto a subagent's branch, an untracked report destroyed by a branch switch, and a subagent diagnosing another's uncommitted file as a defect.

## What this plan drops, and why that is the point

`docs/plans/2026-08-12-transport-supervision.md` had nine tasks. Six of them existed to keep a forwarded socket honest, and the forward is gone, so they go with it. Each was a measured hazard:

| dropped | what it was for |
|---|---|
| the dial classifier (`net.Dial` + EOF-vs-held) | learning whether a forward was carrying anything |
| `SO_PEERCRED` identity | learning *which* process was behind a socket path |
| probe-then-act reconciliation, adopt/repair/reap | not leaking one ssh per restart when unlinking a live socket |
| `ExitOnForwardFailure` handling | a silently forwardless master reading as an empty host |
| the `start-server` squatter defence | a local tmux answering rc=0 on the forward path as itself, keying the capability gate to the *local* version |
| remote socket path derivation | needing the remote path before any server existed |

None of those failure modes has a place to happen now: there is no local socket file to squat, unlink, stat, or mistake for a server. What replaces all six is one assertion — the master is present — and one rule — validate before wrapping.

## File structure

| file | responsibility |
|---|---|
| `internal/tmux/run.go` (modify) | `Target` gains a remote dimension; `execRunner.Run` validates then wraps. The one place that knows a command can go over ssh. |
| `internal/tmux/quote.go` (create) | `ShellQuote` / `ShellJoin`, moved out of `internal/ui` so both the seam and the attach path share one rule. |
| `internal/hostset/sshconfig.go` (create) | `~/.ssh/config` + system `Include` → candidates. |
| `internal/hostset/probe.go` (create) | the positive probe, concurrent, stdout-keyed, with a remedy per exclusion. |
| `internal/hostset/hosts.go` (create) | `hosts.toml` load/save. Generated, hand-editable. |
| `internal/hub/master.go` (create) | the ssh master's life: spawn, assert, re-spawn, stop. |
| `internal/ui/picker.go` (create) | the picker screen. |
| `cmd/tmux-hub/main.go` (modify) | `hosts.toml` becomes the source of hosts; `--host` stays as the escape hatch. |
| `internal/e2e/transport_test.go` (create) | one real host, end to end. |

---

### Task 1: One quoting rule, in one place

`shellJoin` and its escaping body live in `internal/ui/possession.go`. The seam is about to need the same rule for a different reason, and two copies of a quoting rule is how one of them ends up subtly different.

**Files:**
- Create: `internal/tmux/quote.go`
- Test: `internal/tmux/quote_test.go`
- Modify: `internal/ui/possession.go` (delegate, do not re-implement)

**Interfaces:**
- Produces: `func ShellQuote(s string) string`, `func ShellJoin(args []string) string`

- [ ] **Step 1: Write the failing test**

```go
package tmux

import (
	"os/exec"
	"strings"
	"testing"
)

// The property, asserted through a REAL shell rather than by comparing to a
// hand-written expectation: whatever a shell does to the joined string, it must
// split back into the argv element for element. `printf '[%s] '` reuses its format
// once per argument, so the output names the argv the shell actually built.
func TestShellJoinSurvivesARealShell(t *testing.T) {
	for _, argv := range [][]string{
		{"tmux", "attach", "-t", "$0"},
		{"tmux", "attach", "-t", "$10"},
		{"tmux", "send-keys", "-t", "%3", "-l", "it's a $HOME; rm -rf *"},
		{"ssh", "-S", "/home/w/.ssh/cm-%h-%p-%r", "-t", "nuc", "tmux", "attach", "-t", "$3"},
		{"tmux", "display", "-p", "-F", "#{pane_id}|#{window_activity}"},
		{"a b", "", "~", "&", ";", "back\\slash", "new\nline", "$(id)", "`id`"},
	} {
		out, err := exec.Command("sh", "-c", `printf '[%s] ' `+ShellJoin(argv)).CombinedOutput()
		if err != nil {
			t.Fatalf("sh: %v (%s)", err, out)
		}
		var want strings.Builder
		for _, a := range argv {
			want.WriteString("[" + a + "] ")
		}
		if string(out) != want.String() {
			t.Errorf("argv %q\n  shell built %q\n  want        %q", argv, out, want.String())
		}
	}
}

// Two levels, which is the remote path: tmux hands one argument to `$SHELL -c`
// here, and ssh hands its command to a login shell there. A rule that survives one
// pass and not two is the defect this project shipped twice.
func TestShellJoinSurvivesTwoShells(t *testing.T) {
	argv := []string{"tmux", "attach", "-t", "$0"}
	inner := ShellJoin(argv)
	outer := ShellJoin([]string{"sh", "-c", `printf '[%s] ' ` + inner})
	out, err := exec.Command("sh", "-c", outer).CombinedOutput()
	if err != nil {
		t.Fatalf("sh: %v (%s)", err, out)
	}
	if got, want := string(out), "[tmux] [attach] [-t] [$0] "; got != want {
		t.Errorf("through two shells got %q, want %q", got, want)
	}
}
```

- [ ] **Step 2: Run it and watch it fail**

Run: `rtk proxy go test ./internal/tmux/ -run 'TestShellJoinSurvives'`
Expected: build failure — `undefined: ShellJoin`.

- [ ] **Step 3: Implement**

Create `internal/tmux/quote.go`:

```go
package tmux

import "strings"

// ShellQuote makes one argument survive a shell verbatim.
//
// Single quotes because they suspend every expansion a shell would otherwise do —
// `$`, whitespace, `*`, `~`, `;`, `&`, backticks. An embedded single quote is the
// only character they cannot carry, so it is closed, escaped and reopened.
//
// It lives in this package rather than in the caller because two consumers need the
// SAME rule for different reasons: the seam wraps a validated tmux argv into an ssh
// command line (§5), and the attach path quotes a session id so the REMOTE login
// shell does not expand it (§20). Both were shipped wrong once, in opposite halves,
// and two copies of a quoting rule is how one of them drifts.
func ShellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// ShellJoin turns an argv into one string a shell re-splits into exactly that argv.
func ShellJoin(args []string) string {
	parts := make([]string, len(args))
	for i, a := range args {
		parts[i] = ShellQuote(a)
	}
	return strings.Join(parts, " ")
}
```

- [ ] **Step 4: Make `internal/ui` delegate**

Replace `internal/ui/possession.go`'s `shellJoin` body with `return tmux.ShellJoin(args)` and keep its doc comment where it explains the WINDOW path's use of it. Do not delete the comment: it records the measurement that `-t $10` became a bare `0`.

- [ ] **Step 5: Run both packages**

Run: `rtk proxy go test ./internal/tmux/ ./internal/ui/`
Expected: PASS. `TestNoShellCanAlterTheWindowPathPayload` in `internal/ui` must still pass unchanged — it is the existing guard on the same rule.

- [ ] **Step 6: Commit**

```bash
git add internal/tmux/quote.go internal/tmux/quote_test.go internal/ui/possession.go
git commit -m "refactor(tmux): one shell-quoting rule, for the two shells that need it"
```

---

### Task 2: The seam can send a command over ssh

**Files:**
- Modify: `internal/tmux/run.go`
- Test: `internal/tmux/run_test.go`

**Interfaces:**
- Consumes: `ShellJoin` (Task 1), `Validate`, `Target`.
- Produces: `Target` gains `SSHDest string` and `ControlPath string`; `Target.Remote() bool`; `func (t Target) argv(args []string) []string` — the process argv for a validated tmux argv.

- [ ] **Step 1: Write the failing test**

```go
// The safety property, and the reason the order is fixed: Validate enforces the -t
// shape over argv, and an ssh payload is ONE opaque string. Wrapping first would
// hide `send-keys -t @4` inside a payload the shape check cannot read, which is the
// write-into-the-wrong-pane this seam exists to prevent.
func TestARemoteTargetValidatesTheTmuxArgvBeforeWrapping(t *testing.T) {
	rt := Target{Label: "nuc", SSHDest: "nuc", ControlPath: "/run/cm-nuc"}
	if err := Validate([]string{"send-keys", "-t", "@4", "-l", "x"}); err == nil {
		t.Fatal("the tmux argv must still be refused")
	}
	// And the wrapper must not be reachable with an argv Validate refuses.
	if _, err := NewExec(time.Second).(*execRunner).build(rt, []string{"send-keys", "-t", "@4", "-l", "x"}); err == nil {
		t.Error("build accepted an argv whose -t is a window id")
	}
}

// The shape of what actually runs. The tmux argv becomes ONE argument to ssh, so a
// whole poll cycle is one round trip — measured 327 ms against 698 ms for the same
// cycle as two invocations through a forwarded socket.
func TestARemoteTargetBuildsOneSSHInvocation(t *testing.T) {
	rt := Target{Label: "nuc", SSHDest: "nuc", ControlPath: "/run/cm-nuc"}
	got, err := NewExec(time.Second).(*execRunner).build(rt, []string{"list-panes", "-a", "-F", "#{pane_id}"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"ssh", "-o", "BatchMode=yes", "-S", "/run/cm-nuc", "nuc",
		`'tmux' 'list-panes' '-a' '#{pane_id}'`}
	// The payload is asserted by SHAPE below rather than by this literal, because the
	// -F argument's position matters and a literal here would be a second copy of the
	// format string.
	_ = want
	if len(got) != 7 || got[0] != "ssh" || got[5] != "nuc" {
		t.Fatalf("argv = %q", got)
	}
	if !strings.HasPrefix(got[6], "'tmux'") {
		t.Errorf("the payload must be a quoted tmux command line, got %q", got[6])
	}
	if !strings.Contains(got[6], `'#{pane_id}'`) {
		t.Errorf("the format must survive quoting intact, got %q", got[6])
	}
}

// A local target is unchanged, and that is load-bearing: exec runs it with no shell
// at all, so quoting it would make tmux look for a session literally named `'$0'`.
func TestALocalTargetIsStillBareTmux(t *testing.T) {
	lt := Target{Label: "local", Socket: "/tmp/tmux-1000/default"}
	got, err := NewExec(time.Second).(*execRunner).build(lt, []string{"list-panes", "-a"})
	if err != nil {
		t.Fatal(err)
	}
	assertArgv(t, got, "tmux", "-S", "/tmp/tmux-1000/default", "list-panes", "-a")
}

// A remote target with no control path is a hub defect, not a runtime condition:
// the supervisor spawns the master before any host is polled.
func TestARemoteTargetWithoutAControlPathIsRefused(t *testing.T) {
	rt := Target{Label: "nuc", SSHDest: "nuc"}
	if _, err := NewExec(time.Second).(*execRunner).build(rt, []string{"list-panes", "-a"}); err == nil {
		t.Error("a remote target with no control path must be refused, not run bare")
	}
}
```

- [ ] **Step 2: Run and watch it fail**

Run: `rtk proxy go test ./internal/tmux/ -run 'TestARemoteTarget|TestALocalTargetIsStillBareTmux'`
Expected: build failure — `Target` has no `SSHDest`, `execRunner` has no `build`.

- [ ] **Step 3: Implement**

In `internal/tmux/run.go`, extend `Target`:

```go
type Target struct {
	Label  string
	Socket string

	// SSHDest and ControlPath make this host remote: every command becomes one
	// `ssh -S <ControlPath> <SSHDest> '<the tmux command line>'`.
	//
	// There is no forwarded socket, and §5 has the measurements: a poll cycle is
	// 327 ms as one ssh invocation against 698 ms as two through a forward, and a
	// forward cannot carry an attach at all. What that removes is a class rather
	// than a cost — with no local socket file there is nothing to squat, unlink,
	// stat, or mistake for a server.
	SSHDest     string
	ControlPath string
}

func (t Target) Remote() bool { return t.SSHDest != "" }
```

Add `build`, and route `Run`/`RunInput` through it:

```go
// build turns a tmux argv into the process argv, validating BEFORE wrapping.
//
// The order is the safety property. Validate reads the -t shape out of argv (§7);
// once an argv is inside an ssh payload it is one opaque string and the shape check
// cannot see it. So a remote `send-keys -t @4` is refused here exactly as a local
// one is, and the wrapping happens to an argv that has already passed.
func (r *execRunner) build(t Target, args []string) ([]string, error) {
	if err := Validate(args); err != nil {
		return nil, err
	}
	if !t.Remote() {
		if t.Socket == "" {
			return nil, ErrNoSocket
		}
		return append([]string{"tmux", "-S", t.Socket}, args...), nil
	}
	if t.ControlPath == "" {
		return nil, fmt.Errorf("%w: host %q is remote and has no control path, so there is "+
			"no master to send through", ErrNoSocket, t.Label)
	}
	// The whole tmux command line is ONE argument, so ssh hands it to the far
	// shell as a unit and the cycle costs one round trip. Quoted per element,
	// because that far shell would otherwise expand `$N` and glob `#{...}`.
	payload := ShellJoin(append([]string{"tmux"}, args...))
	return []string{"ssh", "-o", "BatchMode=yes", "-S", t.ControlPath, t.SSHDest, payload}, nil
}
```

`Run` then becomes: `argv, err := r.build(t, args)`; on error return it; otherwise exec `argv[0]` with `argv[1:]`. The existing RC/stderr handling is unchanged.

- [ ] **Step 4: Run the whole seam suite**

Run: `rtk proxy go test ./internal/tmux/ -v 2>&1 | tail -20`
Expected: PASS, including `TestRunRefusesEmptySocket`, `TestRunValidatesBeforeExecuting`, the adversarial sweep, and the forbidden-format scan.

- [ ] **Step 5: Mutation-calibrate the order**

Move the `Validate` call in `build` to after the wrapping (validate the ssh argv instead of the tmux argv) and run `rtk proxy go test ./internal/tmux/ -run TestARemoteTargetValidatesTheTmuxArgvBeforeWrapping`. Expected: FAIL. Restore, and confirm `git diff --stat internal/tmux/run.go` shows only the intended change.

- [ ] **Step 6: Commit**

```bash
git add internal/tmux/run.go internal/tmux/run_test.go
git commit -m "feat(tmux): a target can be remote, and the argv is validated before it is wrapped"
```

---

### Task 3: Candidates out of `~/.ssh/config`

**Files:**
- Create: `internal/hostset/sshconfig.go`
- Test: `internal/hostset/sshconfig_test.go`

**Interfaces:**
- Produces:
  - `type Candidate struct { Alias string; Skip string }` — `Skip` is empty for a real candidate and otherwise says why it was dropped.
  - `func ParseSSHConfig(userPath, systemPath string) []Candidate`

- [ ] **Step 1: Write the failing test**

The fixture is this machine's real shape, including the systemd drop-in that only appears through the system config's `Include`.

```go
package hostset

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseSSHConfigDropsWhatIsNotAMachine(t *testing.T) {
	dir := t.TempDir()
	user := filepath.Join(dir, "config")
	os.WriteFile(user, []byte(`
Host nuc
    HostName nuc-sandbox-a.DawnBreather.net
Host github.com dev.github.com orbits.github.com
    User git
Host web-app web-db
    User deploy
Host *.internal
    ProxyJump nuc
`), 0o600)

	system := filepath.Join(dir, "ssh_config")
	os.WriteFile(system, []byte("Include "+dir+"/conf.d/*.conf\n"), 0o644)
	os.MkdirAll(filepath.Join(dir, "conf.d"), 0o755)
	// systemd's drop-in, verbatim in shape. `.host` and `machine/.host` carry NO
	// wildcard character, so a filter that only drops `*` and `?` admits them as
	// ordinary machines — measured on this machine, where they are what
	// /etc/ssh/ssh_config.d/20-systemd-ssh-proxy.conf contributes.
	os.WriteFile(filepath.Join(dir, "conf.d", "20-systemd-ssh-proxy.conf"), []byte(`
Host .host machine/.host unix/* vsock/* machine/* vsock-mux/*
    ProxyCommand /usr/lib/systemd/systemd-ssh-proxy %h %p
`), 0o644)

	got := ParseSSHConfig(user, system)
	kept := map[string]bool{}
	skipped := map[string]string{}
	for _, c := range got {
		if c.Skip == "" {
			kept[c.Alias] = true
		} else {
			skipped[c.Alias] = c.Skip
		}
	}

	for _, want := range []string{"nuc", "github.com", "dev.github.com", "orbits.github.com", "web-app", "web-db"} {
		if !kept[want] {
			t.Errorf("%s should be a candidate (the PROBE decides membership, not the name)", want)
		}
	}
	for _, gone := range []string{"*.internal", ".host", "machine/.host", "unix/*", "vsock/*", "machine/*", "vsock-mux/*"} {
		if kept[gone] {
			t.Errorf("%s must not be a candidate", gone)
		}
		if skipped[gone] == "" {
			t.Errorf("%s was dropped with no reason recorded", gone)
		}
	}
}

// Multi-name Host lines expand, which is how five of this machine's twenty
// candidates exist at all.
func TestParseSSHConfigExpandsMultiNameHostLines(t *testing.T) {
	dir := t.TempDir()
	user := filepath.Join(dir, "config")
	os.WriteFile(user, []byte("Host a b c\n    User x\n"), 0o600)
	got := ParseSSHConfig(user, filepath.Join(dir, "absent"))
	if len(got) != 3 {
		t.Fatalf("got %d candidates, want 3: %+v", len(got), got)
	}
}

// A missing system config is the common case and must not be an error.
func TestParseSSHConfigToleratesAMissingFile(t *testing.T) {
	if got := ParseSSHConfig(filepath.Join(t.TempDir(), "nope"), filepath.Join(t.TempDir(), "nope")); len(got) != 0 {
		t.Fatalf("got %+v, want none", got)
	}
}
```

- [ ] **Step 2: Run and watch it fail**

Run: `rtk proxy go test ./internal/hostset/ -run TestParseSSHConfig`
Expected: the package does not exist.

- [ ] **Step 3: Implement**

Create `internal/hostset/sshconfig.go`:

```go
// Package hostset turns ~/.ssh/config into a set of hosts the hub polls.
//
// Two rules carry the whole design (docs/design.md §9). Candidacy is syntactic and
// generous: anything that could be a machine gets to try. MEMBERSHIP is a positive
// probe — `github.com` eliminates itself by answering `Invalid command: tmux -V`,
// and a name blacklist would need maintaining forever while a probe does not.
package hostset

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
)

// Candidate is a name from an ssh config. Skip is empty for something worth
// probing, and otherwise says why it is not.
type Candidate struct {
	Alias string
	Skip  string
}

// ParseSSHConfig reads the user's config and the system's, following the system
// config's Include — which is where systemd's ssh-proxy drop-in lives, and it
// contributes patterns that must be dropped without looking like patterns.
func ParseSSHConfig(userPath, systemPath string) []Candidate {
	var out []Candidate
	seen := map[string]bool{}
	for _, path := range append([]string{userPath}, includesOf(systemPath)...) {
		for _, name := range hostNames(path) {
			if seen[name] {
				continue
			}
			seen[name] = true
			out = append(out, Candidate{Alias: name, Skip: skipReason(name)})
		}
	}
	return out
}

// skipReason drops what cannot be a host the hub reaches by name.
//
// Wildcards are the obvious half. The other half is measured: systemd's drop-in
// declares `.host` and `machine/.host`, which contain no wildcard character at all,
// so a wildcard-only filter offers them to the probe as ordinary machines.
func skipReason(name string) string {
	switch {
	case strings.ContainsAny(name, "*?!"):
		return "a pattern, not a host"
	case strings.ContainsAny(name, "/%"):
		return "a systemd ssh-proxy pattern, not a host reachable by name"
	case name == ".host" || strings.HasPrefix(name, "."):
		return "systemd's local-machine alias, not a remote host"
	}
	return ""
}

func hostNames(path string) []string {
	f, err := os.Open(path)
	if err != nil {
		return nil // a missing config is the common case, not an error
	}
	defer f.Close()
	var out []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, rest, ok := strings.Cut(line, " ")
		if !ok || !strings.EqualFold(key, "host") {
			continue
		}
		out = append(out, strings.Fields(rest)...)
	}
	return out
}

// includesOf expands the system config's Include globs. It does not recurse: one
// level is what OpenSSH ships and what this machine has.
func includesOf(systemPath string) []string {
	f, err := os.Open(systemPath)
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		key, rest, ok := strings.Cut(line, " ")
		if !ok || !strings.EqualFold(key, "include") {
			continue
		}
		matches, _ := filepath.Glob(strings.TrimSpace(rest))
		out = append(out, matches...)
	}
	return out
}
```

- [ ] **Step 4: Run**

Run: `rtk proxy go test ./internal/hostset/ -v`
Expected: PASS.

- [ ] **Step 5: Check it against the real files, and print what it finds**

Add a test guarded by an env var to `sshconfig_test.go` — env-guarded rather than a
throwaway file, so the check survives in the repo without running in a gate:

```go
// Not a gate: a look at this machine's real files, for when the fixture and reality
// disagree. Measured when this was written: 20 candidates, 0 dropped from the user
// config (it has no wildcard Host lines) and 10 dropped from the systemd drop-in.
func TestParseSSHConfigAgainstTheRealFiles(t *testing.T) {
	if os.Getenv("HOSTSET_REAL") == "" {
		t.Skip("HOSTSET_REAL unset")
	}
	home, _ := os.UserHomeDir()
	got := ParseSSHConfig(filepath.Join(home, ".ssh", "config"), "/etc/ssh/ssh_config")
	for _, c := range got {
		t.Logf("%-24s %s", c.Alias, c.Skip)
	}
}
```

Run: `HOSTSET_REAL=1 rtk proxy go test ./internal/hostset/ -run TestParseSSHConfigAgainstTheRealFiles -v`
Expected: 20 candidates listed, the systemd patterns each with a reason.

- [ ] **Step 6: Commit**

```bash
git add internal/hostset/sshconfig.go internal/hostset/sshconfig_test.go
git commit -m "feat(hostset): candidates from ssh config, including what systemd's include adds"
```

---

### Task 4: The probe decides membership, on stdout

**Files:**
- Create: `internal/hostset/probe.go`
- Test: `internal/hostset/probe_test.go`

**Interfaces:**
- Consumes: `Candidate`.
- Produces:
  - `type Result struct { Alias, Version, Reason string; Usable bool; Took time.Duration }`
  - `func Probe(ctx context.Context, alias string, timeout time.Duration, ssh Runner) Result`
  - `func ProbeAll(ctx context.Context, cands []Candidate, timeout time.Duration, ssh Runner) []Result`
  - `type Runner func(ctx context.Context, alias string, args ...string) (stdout, stderr string, rc int)` — injected so the tests need no network.

- [ ] **Step 1: Write the failing test**

```go
// The measured defect this rule exists for, with the exact five outcomes seen on
// this machine's fleet. The middle case is the one that matters: rc=0 and no tmux.
func TestProbeKeysOnStdoutNotOnTheExitCode(t *testing.T) {
	for _, tc := range []struct {
		name           string
		stdout, stderr string
		rc             int
		usable         bool
		reasonHas      string
	}{
		{"a real host", "tmux 3.2a\n1000\n", "", 0, true, ""},
		// Measured on studio-ws, web-ws, crater-ws, st-ws and qa-ws: the shell's
		// status belongs to `id -u`, so tmux's own 127 is swallowed and rc is 0.
		{"rc=0 with no tmux", "1000\n", "zsh:1: command not found: tmux\n", 0, false, "no tmux"},
		{"a git remote", "", "Invalid command: tmux -V\n", 1, false, "not a shell host"},
		{"dns", "", "ssh: Could not resolve hostname sandbox-a.ops.eu\n", 255, false, "does not resolve"},
		{"unreachable", "", "ssh: connect to host 20.127.207.74 port 22: Connection timed out\n", 255, false, "cannot be reached"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := Probe(context.Background(), "h", time.Second,
				func(context.Context, string, ...string) (string, string, int) {
					return tc.stdout, tc.stderr, tc.rc
				})
			if r.Usable != tc.usable {
				t.Fatalf("Usable = %v, want %v (reason %q)", r.Usable, tc.usable, r.Reason)
			}
			if tc.usable && r.Version != "3.2a" {
				t.Errorf("Version = %q, want 3.2a", r.Version)
			}
			if !tc.usable && !strings.Contains(r.Reason, tc.reasonHas) {
				t.Errorf("Reason = %q, want it to contain %q", r.Reason, tc.reasonHas)
			}
		})
	}
}

// Every exclusion carries its remedy, because a status with no remedy is a bug
// report addressed to the wrong person (§16).
func TestEveryExclusionReasonNamesWhatToDo(t *testing.T) {
	for _, tc := range []struct{ stdout, stderr string; rc int }{
		{"1000\n", "command not found: tmux\n", 0},
		{"", "Invalid command: tmux -V\n", 1},
		{"", "ssh: Could not resolve hostname x\n", 255},
	} {
		r := Probe(context.Background(), "h", time.Second,
			func(context.Context, string, ...string) (string, string, int) {
				return tc.stdout, tc.stderr, tc.rc
			})
		if !strings.ContainsAny(r.Reason, "—-") {
			t.Errorf("reason %q states a breakage without a remedy", r.Reason)
		}
	}
}

// Concurrency is the whole reason a first run is affordable: 20 hosts took 7.01 s
// wall, all of it the slowest one. A serial loop would have been a minute.
func TestProbeAllRunsConcurrently(t *testing.T) {
	cands := make([]Candidate, 12)
	for i := range cands {
		cands[i] = Candidate{Alias: fmt.Sprintf("h%d", i)}
	}
	start := time.Now()
	got := ProbeAll(context.Background(), cands, 2*time.Second,
		func(context.Context, string, ...string) (string, string, int) {
			time.Sleep(300 * time.Millisecond)
			return "tmux 3.4\n", "", 0
		})
	if len(got) != 12 {
		t.Fatalf("got %d results, want 12", len(got))
	}
	if el := time.Since(start); el > 1500*time.Millisecond {
		t.Errorf("12 probes of 300ms took %s — they are not running concurrently", el)
	}
}

// A candidate the parser already rejected is never probed: it costs a connection
// attempt to learn what the name already said.
func TestProbeAllSkipsWhatTheParserRejected(t *testing.T) {
	calls := 0
	ProbeAll(context.Background(),
		[]Candidate{{Alias: "real"}, {Alias: "unix/*", Skip: "a pattern, not a host"}},
		time.Second,
		func(context.Context, string, ...string) (string, string, int) {
			calls++
			return "tmux 3.4\n", "", 0
		})
	if calls != 1 {
		t.Errorf("probed %d times, want 1 — a skipped candidate must not be dialled", calls)
	}
}
```

- [ ] **Step 2: Run and watch it fail**

Run: `rtk proxy go test ./internal/hostset/ -run 'Probe'`
Expected: build failure — `undefined: Probe`.

- [ ] **Step 3: Implement**

Create `internal/hostset/probe.go`. The classifier keys on stdout first and only consults stderr to choose which remedy to name:

```go
var tmuxVersion = regexp.MustCompile(`^tmux (\S+)`)

// Probe asks one host whether it can host the hub, and answers with a remedy when
// it cannot.
//
// Membership keys on STDOUT. Measured on five of this machine's hosts at once,
// `ssh host 'tmux -V; id -u'` returns rc=0 with no tmux installed, because a
// shell's status belongs to its last command and `tmux -V`'s own 127 is swallowed.
// An rc-keyed probe admits ten hosts where five are usable, and the other five fail
// mysteriously later, which is the worst direction for this to be wrong in.
func Probe(ctx context.Context, alias string, timeout time.Duration, ssh Runner) Result {
	start := time.Now()
	out, errOut, rc := ssh(ctx, alias, "tmux -V; id -u")
	r := Result{Alias: alias, Took: time.Since(start)}
	if m := tmuxVersion.FindStringSubmatch(strings.TrimSpace(out)); m != nil {
		r.Usable, r.Version = true, m[1]
		return r
	}
	r.Reason = reasonFor(errOut, rc)
	return r
}

// reasonFor names the remedy, not the breakage. Every string here was observed —
// the table in §9 carries the timings that came with them.
func reasonFor(stderr string, rc int) string {
	s := strings.ToLower(stderr)
	switch {
	case strings.Contains(s, "command not found") || strings.Contains(s, "not found: tmux"):
		return "no tmux — install it there, or leave this host off"
	case strings.Contains(s, "invalid command") || strings.Contains(s, "remote:"):
		return "not a shell host — this is a git remote, so leave it off"
	case strings.Contains(s, "could not resolve hostname"):
		return "DNS does not resolve — a stale ssh config entry? fix or remove it"
	case strings.Contains(s, "connection timed out") || strings.Contains(s, "banner exchange"):
		return "cannot be reached — powered off, or behind a VPN that is not up"
	case strings.Contains(s, "permission denied"):
		return "ssh refused the key — add one, or leave this host off"
	case rc == 255:
		return "ssh failed — " + firstLine(stderr)
	}
	return "no tmux version on stdout — " + firstLine(stderr)
}
```

`ProbeAll` runs one goroutine per candidate whose `Skip` is empty, collecting into a slice in input order, and gives each probe its own `context.WithTimeout`.

- [ ] **Step 4: Run**

Run: `rtk proxy go test ./internal/hostset/ -v 2>&1 | tail -12`
Expected: PASS.

- [ ] **Step 5: Measure it against the real fleet**

Add an env-guarded test that probes the real candidates through a real `ssh` runner and logs the table plus the wall time. Run it:

`HOSTSET_REAL=1 rtk proxy go test ./internal/hostset/ -run TestProbeAllAgainstTheRealFleet -v`

Expected, from the measurement this plan was written against: about **7 s** wall for ~20 candidates, **5 usable** (`nuc`, `web-app`, `web-db`, `eu` on 3.2a and `side-desk` on 3.4), and five hosts excluded with `no tmux` despite rc=0. If the count differs, the fleet changed — record the new numbers in the report rather than adjusting the test.

- [ ] **Step 6: Commit**

```bash
git add internal/hostset/probe.go internal/hostset/probe_test.go
git commit -m "feat(hostset): a positive probe keyed on stdout, with a remedy per exclusion"
```

---

### Task 5: `hosts.toml`, generated and hand-editable

**Files:**
- Create: `internal/hostset/hosts.go`
- Test: `internal/hostset/hosts_test.go`

**Interfaces:**
- Produces:
  - `type Entry struct { Alias string; Enabled bool; Tags []string; TmuxArgs []string }`
  - `func DefaultPath() string` — `~/.config/tmux-hub/hosts.toml`
  - `func LoadHosts(path string) ([]Entry, error)`
  - `func SaveHosts(path string, es []Entry) error`

- [ ] **Step 1: Write the failing test**

```go
// Round trip first, because the file is GENERATED by the picker and hand-edited by
// the user, and a generated file that cannot read its own output is a file that
// drifts from the thing that writes it.
func TestHostsRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hosts.toml")
	want := []Entry{
		{Alias: "nuc", Enabled: true, Tags: []string{"work", "eu"}},
		{Alias: "web-app", Enabled: true, Tags: []string{"prod"}, TmuxArgs: []string{"-L", "deploy"}},
		{Alias: "eu", Enabled: false},
	}
	if err := SaveHosts(path, want); err != nil {
		t.Fatal(err)
	}
	got, err := LoadHosts(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("round trip changed the set:\n got %+v\nwant %+v", got, want)
	}
}

// An absent file is "nothing decided yet", which is what makes the picker open
// itself on a first run — not an error the user has to clear first.
func TestLoadHostsTreatsAnAbsentFileAsNoDecisions(t *testing.T) {
	got, err := LoadHosts(filepath.Join(t.TempDir(), "nope.toml"))
	if err != nil {
		t.Fatalf("an absent file must not be an error: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("got %+v, want none", got)
	}
}

// Malformed content is a warning, not a wipe. The same rule the hidden set follows
// (§18): a file the user edited badly must not silently discard their decisions.
func TestLoadHostsRefusesToGuessAtBrokenContent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hosts.toml")
	os.WriteFile(path, []byte("this is not toml at all ]]]"), 0o600)
	if _, err := LoadHosts(path); err == nil {
		t.Error("broken content must be reported, so the picker can say so instead of " +
			"writing a fresh file over it")
	}
}

// The written file is meant to be read by a person, so the shape is asserted.
func TestSavedFileIsReadable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hosts.toml")
	SaveHosts(path, []Entry{{Alias: "nuc", Enabled: true, Tags: []string{"work"}}})
	b, _ := os.ReadFile(path)
	for _, want := range []string{"[[host]]", `alias = "nuc"`, "enabled = true", `tags = ["work"]`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("the file does not contain %q:\n%s", want, b)
		}
	}
}
```

- [ ] **Step 2: Run and watch it fail**, then implement with the stdlib plus a minimal TOML writer and reader for this one shape — the file has one table array and four scalar kinds, so a dependency is not warranted. Write with `os.WriteFile` to a temp name plus `os.Rename`, so a crash mid-write cannot truncate the user's decisions.

- [ ] **Step 3: Run, then commit**

```bash
git add internal/hostset/hosts.go internal/hostset/hosts_test.go
git commit -m "feat(hostset): hosts.toml, written by the picker and safe to hand-edit"
```

---

### Task 6: The master's life

**Files:**
- Create: `internal/hub/master.go`
- Test: `internal/hub/master_test.go`

**Interfaces:**
- Produces:
  - `type Master struct { Alias, ControlPath string }`
  - `func RuntimeDir() (string, error)` — asserts `XDG_RUNTIME_DIR`
  - `func ControlPathFor(dir, alias string) string` — sanitised alias + `sha256(alias)[:8]`
  - `func (m *Master) Ensure(ctx context.Context, run Exec) error` — assert, spawn if absent
  - `func (m *Master) Stop(ctx context.Context, run Exec) error`

- [ ] **Step 1: Write the failing test**

```go
// The hazard this whole type exists for, and it is measured: pointed at a control
// path that is NOT a live master, `ssh -S <path> host 'tmux -V'` returns rc=0 with
// the right answer, because ssh silently opens its own connection. So a dead master
// never announces itself — it costs latency instead, 323 ms becoming 7.0 s against
// this fleet's slowest host. Presence is therefore ASSERTED, never inferred from a
// failure, because there is no failure to infer it from.
func TestEnsureAssertsThePresenceOfTheMaster(t *testing.T) {
	var calls []string
	run := fakeExec(func(argv []string) (string, int) {
		calls = append(calls, strings.Join(argv, " "))
		if contains(argv, "-O") {
			return "", 1 // no master yet
		}
		return "", 0
	})
	m := &Master{Alias: "nuc", ControlPath: "/run/cm-nuc"}
	if err := m.Ensure(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 2 || !strings.Contains(calls[0], "-O check") {
		t.Fatalf("Ensure must CHECK before it spawns; calls = %q", calls)
	}
	if !strings.Contains(calls[1], "-N") || !strings.Contains(calls[1], "-M") {
		t.Errorf("the spawn is not a master: %q", calls[1])
	}
	if strings.Contains(calls[1], "-L") {
		t.Errorf("there is no port forward in this design (§5): %q", calls[1])
	}
}

// A live master is not re-spawned: a second `ssh -M` on the same control path is
// how you get two masters and a confusing one to kill.
func TestEnsureIsANoOpWhenTheMasterIsAlreadyRunning(t *testing.T) {
	var spawns int
	run := fakeExec(func(argv []string) (string, int) {
		if contains(argv, "-O") {
			return "Master running (pid=123)", 0
		}
		spawns++
		return "", 0
	})
	m := &Master{Alias: "nuc", ControlPath: "/run/cm-nuc"}
	m.Ensure(context.Background(), run)
	if spawns != 0 {
		t.Errorf("spawned %d masters over a live one", spawns)
	}
}

// The control path cannot be broken by an alias, and two aliases cannot collide.
func TestControlPathIsSafeForAnyAlias(t *testing.T) {
	dir := "/run/user/1000/tmux-hub"
	for _, alias := range []string{"nuc", "a/b", "..", "with space", strings.Repeat("x", 200)} {
		p := ControlPathFor(dir, alias)
		if filepath.Dir(p) != dir {
			t.Errorf("alias %q escaped the runtime dir: %s", alias, p)
		}
		if len(filepath.Base(p)) > 100 {
			t.Errorf("alias %q produced a name too long for a unix socket path: %s", alias, p)
		}
	}
	if ControlPathFor(dir, "a/b") == ControlPathFor(dir, "a-b") {
		t.Error("two different aliases collided on one control path")
	}
}

// XDG_RUNTIME_DIR unset makes filepath.Join("", …) RELATIVE, so the hub would put
// control sockets wherever it started and work unpredictably until the user ran it
// from somewhere else (§5).
func TestRuntimeDirRefusesARelativePath(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", "")
	if _, err := RuntimeDir(); err == nil {
		t.Error("an unset XDG_RUNTIME_DIR must be refused with a remedy, not joined")
	}
}
```

- [ ] **Step 2: Run, watch it fail, implement.** `Ensure` runs `ssh -O check -S <ctl> <alias>`; on rc≠0 it spawns `ssh -N -M -S <ctl> -o BatchMode=yes -o ServerAliveInterval=15 <alias>` and then waits on the FACT — polling `-O check` every 50 ms — rather than sleeping.

  **The deadline is 10 s and the poll is 50 ms, both from measurement:** a spawn became checkable at 1530, 1551 and 1606 ms over three trials, with about 28 failed checks before the first success. A deadline near the measurement would flake on a slower link; 10 s is six times the observed worst and still bounded.

  **An existing master is adopted, not replaced.** `-O check` on a live one answers in 3.6 ms against 1550 to spawn, so a master left by a previous run makes the next start effectively free. This is why the hub does not stop its masters on exit — measured, a master outlives its parent and is reparented to pid 1, and that is useful rather than a bug here.

- [ ] **Step 3: The way out of the adoption, because a leak nobody can clean is still a leak**

  Deliberately leaving processes running requires a way to end them, or the hub accumulates one ssh per host the user ever enabled. Three exits, and each is one call to `Stop` (`ssh -O exit -S <ctl> <alias>`):

  - disabling a host in the picker stops its master immediately — the user said no, so nothing of theirs should keep running;
  - `tmux-hub --stop-masters` stops every master under `$RT` and exits, for "get off my machine";
  - a master whose control path is under `$RT` but whose alias is in no `hosts.toml` entry is stopped at startup, which is the only reconciliation this design needs — there is no socket to unlink, no pid to persist, and `-O check`/`-O exit` address a master by the path the hub chose.

  Test each: a fake exec asserting `-O exit` is issued exactly once per stopped alias, and that a still-enabled host's master is left alone.

- [ ] **Step 4: Run, then commit**

```bash
git add internal/hub/master.go internal/hub/master_test.go
git commit -m "feat(hub): the ssh master's life, asserted rather than assumed"
```

---

### Task 7: The picker

**Files:**
- Create: `internal/ui/picker.go`
- Test: `internal/ui/picker_test.go`
- Modify: `internal/ui/model.go`

**Interfaces:**
- Consumes: `hostset.Result`, `hostset.Entry`.
- Produces:
  - `type PickerRow struct { Alias, Reason, Version string; Enabled, Usable bool }`
  - `func RenderPicker(rows []PickerRow, width, height, cursor int) []string`
  - keys: `p` opens, `space` toggles, `enter` saves, `esc` cancels.

**The approved layout.** This is a TARGET frame, seeded from the real launch-form
overlay in `docs/ui-mockup.html` (rule 1 of `docs/mockup-authoring.md`) and emitted
by a script that pads to its real widths (rule 2), with this machine's real fleet in
it. Build to this, and when it ships regenerate the real frame and diff the two with
`prototypes/framediff.py`:

```
tmux-hub  6 sessions
LOCAL API                    ┌─ local api %0 ───────────────────────────────────────────────────────┐
> ⚑ needs  %0   claude       │  Do you want to proceed?                                             │
NUC DEPLOY                   │  ❯ 1. Yes                                                            │
  ✱ quiet  %4   claude       │    2. No                                                             │
LOCAL OPS                    └──────────────────────────────────────────────────────────────────────┘
  ▸ idle   %3   tail         
LOCAL API                    
  · works  %1   claude       
  · works  %2   make         
NUC DEPLOY                   
local up · nuc up
────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────
Hosts — 20 candidates in ~/.ssh/config, 5 answer with tmux

  [x] nuc           tmux 3.2a
  [x] web-app       tmux 3.2a
› [ ] eu            tmux 3.2a
  [x] side-desk     tmux 3.4
      github.com    not a shell host — this is a git remote, so leave it off
      studio-ws     no tmux — install it there, or leave this host off
  ↓ 14 more · j/k to move

space: keep this host · enter: save and connect · esc: cancel · r: probe again
```

Producing it found what prose had missed: at 120×24 the overlay leaves **six rows for
hosts** and this machine offers **twenty candidates**, so the picker SCROLLS. That is
the same defect §16 records for the inbox — thirty panes on a 24-row terminal moving a
cursor nobody could see — and it is why the frame carries a `↓ 14 more` line.

Each line of the frame is an assertion:

| the frame shows | wrong unless |
|---|---|
| `[x]` / `[ ]` on usable hosts only | an unusable host offers no box to tick |
| `tmux 3.2a` beside a usable host | the probe's version reaches the screen |
| the reason beside an unusable one | every exclusion carries its remedy (§16) |
| `↓ 14 more · j/k to move` | the list scrolls and says so when it does |
| the dashboard still above the rule | the picker is an overlay, not a takeover — the same shape as the launch form |
| `space: keep this host · enter: save and connect · esc: cancel · r: probe again` | the keys are on screen, in English. `r` was added to this frame AFTER it was approved, when two of five hosts turned out to swing fourfold in latency — and the implementation then matched the frame on 23 of 24 lines with that one as the only difference, which is the diff doing its job |

- [ ] **Step 1: Write the failing test**

```go
// Every excluded host shows its reason ON SCREEN, because the picker is where a
// person decides, and "nuc is missing" without "DNS does not resolve" sends them
// to read logs the hub already read.
func TestThePickerShowsEveryReasonOnTheScreenViewReturns(t *testing.T) {
	rows := []PickerRow{
		{Alias: "nuc", Version: "3.2a", Usable: true, Enabled: true},
		{Alias: "side-desk", Version: "3.4", Usable: true},
		{Alias: "github.com", Reason: "not a shell host — this is a git remote, so leave it off"},
		{Alias: "studio-ws", Reason: "no tmux — install it there, or leave this host off"},
	}
	m := base(t, 120, 24)
	m.mode = modePicker
	m.picker = rows
	view := m.View()
	for _, want := range []string{"nuc", "3.2a", "github.com", "git remote", "studio-ws", "no tmux"} {
		if !strings.Contains(view, want) {
			t.Errorf("View() does not show %q:\n%s", want, view)
		}
	}
}

// A usable host that is off must be visibly off, and an unusable one must not look
// like a choice the user failed to make.
func TestThePickerDistinguishesOffFromUnusable(t *testing.T) {
	m := base(t, 120, 24)
	m.mode = modePicker
	m.picker = []PickerRow{
		{Alias: "eu", Version: "3.2a", Usable: true, Enabled: false},
		{Alias: "github.com", Reason: "not a shell host — this is a git remote, so leave it off"},
	}
	view := m.View()
	eu := lineContaining(t, view, "eu")
	gh := lineContaining(t, view, "github.com")
	if !strings.Contains(eu, "[ ]") {
		t.Errorf("a usable host that is off must show an empty box: %q", eu)
	}
	if strings.Contains(gh, "[ ]") || strings.Contains(gh, "[x]") {
		t.Errorf("an unusable host must not offer a box to tick: %q", gh)
	}
}

// Twenty candidates and six rows: the list scrolls, and it says so. Without this the
// cursor walks off the screen exactly as it did in the inbox before §16's fix.
func TestThePickerScrollsAndSaysHowMuchIsLeft(t *testing.T) {
	rows := make([]PickerRow, 20)
	for i := range rows {
		rows[i] = PickerRow{Alias: fmt.Sprintf("host%02d", i), Version: "3.4", Usable: true}
	}
	m := base(t, 120, 24)
	m.mode = modePicker
	m.picker = rows
	m.pickerCursor = 17
	view := m.View()
	if !strings.Contains(view, "host17") {
		t.Errorf("the cursor's row is off screen — the list does not scroll:\n%s", view)
	}
	if !strings.Contains(view, "more") {
		t.Errorf("the list is truncated without saying so:\n%s", view)
	}
}

// space toggles only what can be enabled.
func TestSpaceCannotEnableAnUnusableHost(t *testing.T) {
	m := base(t, 120, 24)
	m.mode = modePicker
	m.picker = []PickerRow{{Alias: "github.com", Reason: "not a shell host — a git remote"}}
	m.pickerCursor = 0
	after, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{' '}})
	if after.(model).picker[0].Enabled {
		t.Error("space enabled a host the probe said cannot work")
	}
}
```

- [ ] **Step 2: Run, watch it fail, implement.** `RenderPicker` draws one row per candidate: a tick box only when `Usable`, the version when known, the reason otherwise. `p` opens it from the dashboard; it opens itself when `hosts.toml` has decided nothing yet (§9).

  > **Amended during implementation, and this line is the corrected one.** It used to name a
  > SECOND trigger as well: a probe discovering that an enabled host no longer answers. That
  > trigger was cut, because deciding it at startup needs the probe §16 keeps off the
  > first-paint path, and a picker opened over a host that is merely slow would open on every
  > start — two of five usable hosts swing fourfold. What replaces it is §10's rule, that such
  > a host is a `connecting` row carrying its reason. §9 is the authority and already reads
  > this way; the sentence is corrected here rather than left standing, because a stale plan
  > step contradicting the shipped spec is what Task 10's Step 7 grep exists to catch. The
  > cut wording is deliberately not quoted: that grep matches a substring, so restating the
  > old text here would make the check fail on its own correction.

- [ ] **Step 3: Run the whole `internal/ui` suite, then commit**

```bash
git add internal/ui/picker.go internal/ui/picker_test.go internal/ui/model.go
git commit -m "feat(ui): the picker, where a host's exclusion carries its remedy"
```

---

### Task 8: `hosts.toml` becomes how you run the tool

**Files:**
- Modify: `cmd/tmux-hub/main.go`
- Modify: `internal/hub/poll.go` (a host carries its control path)
- Test: `cmd/tmux-hub/main_test.go`, `cmd/tmux-hub/wiring_test.go`

- [ ] **Step 1: Write the failing test**

```go
// The point of the whole plan: hosts come from the file, and --host is the escape
// hatch rather than the interface.
func TestHostsComeFromTheFileWhenNoFlagIsGiven(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hosts.toml")
	hostset.SaveHosts(path, []hostset.Entry{
		{Alias: "nuc", Enabled: true},
		{Alias: "eu", Enabled: false},
	})
	got, err := hostsFrom(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Label != "nuc" {
		t.Fatalf("got %+v, want only the enabled host", got)
	}
	if got[0].SSHDest != "nuc" || got[0].ControlPath == "" {
		t.Errorf("an enabled host must carry what the transport needs: %+v", got[0])
	}
	if got[0].Socket != "" {
		t.Error("a remote host has no socket in this design — that was the forward (§5)")
	}
}

// --host still works, and still wins, because it is what an unusual setup uses.
func TestAnExplicitHostFlagIsAddedToTheFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hosts.toml")
	hostset.SaveHosts(path, []hostset.Entry{{Alias: "nuc", Enabled: true}})
	got, err := hostsFrom(path, []hub.Host{{Label: "odd", Socket: "/tmp/odd.sock"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %+v, want the file's host and the flag's", got)
	}
}
```

- [ ] **Step 2: Run, watch it fail, implement.** `hostsFrom(path, extra)` loads the file, keeps `Enabled` entries, gives each a `ControlPath` from `hub.ControlPathFor`, and appends the `--host` entries.

  **`Master.Ensure` runs after `tea.NewProgram` has painted, one goroutine per host, and its result arrives as a message.** Measured, a spawn is 1550 ms and §16's commitment is 50 ms, so calling it before the program starts would miss by thirty times — with five enabled hosts the user would watch a blank terminal for a second and a half. A host whose master is not up yet renders `connecting`, which §9 already specifies and the new screens in `docs/ui-mockup.html` already show.

- [ ] **Step 3: Assert the ordering, do not trust it**

  A test that spawning is off the path, written so it fails if someone moves the call: give `main`'s startup a `Master.Ensure` that blocks for 500 ms and assert the model is built and `Init` returns before it completes. The shape this repo already uses for exactly this is `TestAStalledHostDoesNotStopIdentificationEverywhere` in `internal/ui/wiring_test.go` — a blocking walker plus a release channel. Copy that shape rather than inventing one.

- [ ] **Step 4: Run every gate, then commit**

```bash
git add cmd/tmux-hub internal/hub/poll.go
git commit -m "feat(cmd): hosts come from hosts.toml, and --host becomes the escape hatch"
```

---

### Task 9: One real host, end to end

**Files:**
- Create: `internal/e2e/transport_test.go` (build tag `e2e`)

- [ ] **Step 1: Read the existing harness** — `sed -n '1,60p' internal/e2e/lifecycle_test.go` — and reuse its private-socket setup and `t.Cleanup`. Do not write a second harness.

- [ ] **Step 2: Write the test**

It needs a real host, so it skips without one: `HUB_E2E_HOST` names an ssh destination the runner can reach. Guarded that way rather than hardcoded, because a suite that fails on someone else's machine is a suite people delete.

```go
//go:build e2e

// The claim: a remote host is polled over one master, with no forward anywhere, and
// the argv the seam built is what ran. Everything below the master is already
// covered by unit tests; what only a real host can show is that the two shells
// between here and tmux leave the command intact.
func TestE2EARemoteHostIsPolledOverOneMaster(t *testing.T) {
	host := os.Getenv("HUB_E2E_HOST")
	if host == "" {
		t.Skip("HUB_E2E_HOST unset — needs an ssh destination with tmux")
	}
	dir := t.TempDir()
	ctl := filepath.Join(dir, "cm")
	m := &hub.Master{Alias: host, ControlPath: ctl}
	if err := m.Ensure(context.Background(), tmux.NewExec(20*time.Second)); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	t.Cleanup(func() { m.Stop(context.Background(), tmux.NewExec(10*time.Second)) })

	// A session on the far side, on a PRIVATE socket, so nothing of the user's is
	// touched and the socket override is exercised at the same time.
	tgt := tmux.Target{Label: host, SSHDest: host, ControlPath: ctl}
	r := tmux.NewExec(20 * time.Second)
	// ... new-session on `-L hube2e`, then FetchDeltas, then assert the pane is
	// there with a non-empty start command and an epoch, then kill-server.
}
```

The assertions that matter, each naming what its absence would mean:
- the delta parses and the pane is present — the format survived two shells
- `Epoch` is non-empty — `#{pid}:#{start_time}` came back, so selections can be invalidated
- `StartCommand` is non-empty — the corroborator half of the hide key reaches the registry over ssh too
- a `send-keys` with a window target is refused *before* anything is sent — the seam still guards the write path when the command is destined for an ssh payload

- [ ] **Step 3: Run it against a real host**

`HUB_E2E_HOST=nuc rtk proxy go test -count=1 -tags e2e ./internal/e2e/ -run TestE2EARemoteHostIsPolledOverOneMaster -v`

- [ ] **Step 4: Vet the tagged package and commit**

```bash
rtk proxy go vet -tags e2e ./internal/e2e/
git add internal/e2e/transport_test.go
git commit -m "test(e2e): a remote host polled over one master, no forward anywhere"
```

---

### Task 10: Close the branch

- [ ] **Step 1: Every gate**

```bash
gofmt -l .
rtk proxy go vet ./...
rtk proxy go vet -tags mockup ./internal/ui/
rtk proxy go vet -tags e2e ./internal/e2e/
rtk proxy go test -count=1 -race ./... 2>&1 | grep -cE '^ok'
rtk proxy go test -count=1 -tags e2e ./internal/e2e/ 2>&1 | tail -3
```
Expected: gofmt silent, three vets clean, the `ok` count equal to the package count (15 with `internal/hostset` added — count, do not scan for FAIL, because a package that never ran prints nothing).

- [ ] **Step 2: Verify the COMMIT, not the tree**

```bash
rm -rf /tmp/v && mkdir -p /tmp/v && git archive HEAD | tar -x -C /tmp/v
cd /tmp/v && gofmt -l . && rtk proxy go vet ./... && rtk proxy go test -count=1 ./... 2>&1 | grep -cE '^ok'
```

- [ ] **Step 3: The under-construction exemption must be empty**

```bash
grep -A4 'var underConstruction' cmd/tmux-hub/wiring_test.go
```
Expected: an empty map. `internal/hostset` is created by Task 3 and wired by Task 8, and for the
tasks in between the wiring floor cannot tell "not yet consumed" from the defect it exists for — a
package shipped built, tested and wired to nothing. The exemption names the task that removes it,
and the floor already fails if an exempt package turns out to BE linked, so an entry cannot outlive
its reason quietly. Task 8 deletes it; this step is what notices if Task 8 forgot.

- [ ] **Step 4: The wiring floor**

```bash
grep -rl 'hostset\.\|Master{\|ParseSSHConfig\|ProbeAll' --include='*.go' . | grep -v _test | wc -l
```
Expected: at least 3, and `cmd/tmux-hub/main.go` among them. A package that exists, is covered, and is constructed by nobody is this repo's signature failure.

- [ ] **Step 5: Try it as a person would**

With no `hosts.toml` present, run `tmux-hub` and confirm the picker opens itself, lists this machine's real candidates with reasons, and that ticking one and pressing enter produces a `hosts.toml` and a polled host. Then run it again and confirm it starts straight into the dashboard. Record the wall time to first paint — §16's commitment is under 50 ms with no host probing on the critical path.

- [ ] **Step 6: Regenerate the documents**

```bash
prototypes/possession-captures.sh /tmp/flows
HUB_FLOW_CAPTURES=/tmp/flows rtk proxy go test -tags mockup -run 'TestGenerateFlows|TestGenerateMockup' ./internal/ui/
```
The picker is a new screen and belongs in `docs/ui-mockup.html`: add a scenario for it with the real states — nothing decided yet, a mixed fleet, every host excluded.

- [ ] **Step 7: Commit**

```bash
git add -A
git commit -m "chore: close the hosts-and-transport branch — gates, wiring floor, screens"
```

---

## Self-Review

**1. Spec coverage.** §5's new transport → Tasks 1, 2, 6. §9's candidates → Task 3; its positive probe and remedies → Task 4; its `hosts.toml` → Task 5; its picker → Task 7. §16's "probing never gates the UI" → Task 8's concurrent `Ensure` off the critical path, checked by hand in Task 10 Step 4. §5's validate-then-wrap → Task 2, with a mutation to calibrate it.

**Gaps I am leaving deliberately**, each because it is a separate decision rather than a missing step:
- **Backoff and re-promotion.** A host whose master dies repeatedly should not be retried every tick. Task 6's `Ensure` is idempotent and cheap, so the naive behaviour is "check every tick", which is one `ssh -O check` per host per tick. That is affordable now and will need a backoff later; it is not in this plan.
- **`tags` are stored and unused.** Task 5 persists them because the picker writes them and §9 names them, and the per-project grouping they exist for is its own sub-project.
- **The local host still uses a socket.** That is correct — there is no ssh for it — and it is why `Target` keeps both shapes.

**2. Placeholder scan.** Task 5 Step 2 and Tasks 7–9 give the shape and the assertions rather than the whole implementation, and that is deliberate for the three of them that must fit code they cannot see from here (`internal/ui`'s mode plumbing, `main`'s startup order, `internal/e2e`'s harness). Every test literal is written out. One stray heredoc fragment in Task 3 Step 5 was found by this review and removed rather than annotated: a plan that ships a known defect with a note beside it is a plan that gets read past the note.

**3. Type consistency.** `Candidate{Alias, Skip}` from Task 3 is consumed by Task 4's `ProbeAll` and by Task 7's rows. `Result{Alias, Version, Reason, Usable, Took}` from Task 4 feeds `PickerRow` in Task 7. `Entry{Alias, Enabled, Tags, TmuxArgs}` from Task 5 is what Task 8's `hostsFrom` reads and Task 7's `enter` writes. `Target.SSHDest`/`ControlPath` from Task 2 are what Task 8 fills from `hub.ControlPathFor`. `Master{Alias, ControlPath}` from Task 6 is used in Tasks 8 and 9.

**4. What this plan has not measured.** The 7.01 s first-run figure is 20 candidates against this fleet on this network; a fleet behind a slower link will differ, and the number that matters is that it is off the critical path rather than its value. Backoff behaviour is unmeasured because it is unimplemented. And `internal/hostset`'s TOML writer is asserted by round trip only — if a user's hand-edited file uses a TOML shape the minimal reader does not accept, the failure is Task 5's `TestLoadHostsRefusesToGuessAtBrokenContent` path, which reports rather than wipes.
