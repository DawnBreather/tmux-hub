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
	"bytes"
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
	Socket string // the LOCAL server's socket; passed as tmux -S

	// SSHDest and ControlPath make this host remote: every command becomes one
	// `ssh -S <ControlPath> <SSHDest> '<the tmux command line>'`.
	//
	// There is no forwarded socket, and docs/design.md §5 has the measurements: a
	// poll cycle is 327 ms as one ssh invocation against 698 ms as two through a
	// forward, and a forward cannot carry an attach at all. What that removes is a
	// class rather than a cost — with no local socket file there is nothing to
	// squat, unlink, stat, or mistake for a server.
	SSHDest     string
	ControlPath string

	// TmuxArgs are extra GLOBAL tmux options for this target, inserted after the
	// socket/ssh decision and before the verb: `tmux -S <socket> <TmuxArgs…> <verb…>`
	// and `ssh … 'tmux <TmuxArgs…> <verb…>'`. This is docs/design.md §9's socket
	// override — a host whose server lives on `-L <label>` rather than the default
	// socket — and it is a list rather than a socket string because the position is
	// the only thing the seam needs to own; which option the user reaches for is
	// theirs.
	//
	// Position is load-bearing in both directions, measured on tmux 3.7b:
	//
	//   - the LAST -S wins, so `-S /a -S /b` uses /b. Appending here is therefore
	//     what makes an -S override actually override.
	//   - -S beats -L whatever the order: `-L lbl -S /a` and `-S /a -L lbl` both use
	//     /a, because tmux only resolves a label when no path was given. So an -L
	//     override cannot take effect on a target that already has a socket. Today no
	//     production path can build one — a host out of hosts.toml is remote and has
	//     no socket (Target()) — but the ordering is not a preference, it is tmux's.
	TmuxArgs []string
}

// Remote reports whether this target is reached over ssh rather than over a
// local socket. It keys on SSHDest alone, so a half-filled target is remote
// with no control path — refused by build rather than run bare.
func (t Target) Remote() bool { return t.SSHDest != "" }

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

// Exec is both seams at once, which is what a caller that reads AND writes needs.
// It exists so no production call site has to assert one interface out of the
// other: the write path takes an InputRunner and the poll path a Runner, and both
// are the same binary under the same deadline.
type Exec interface {
	Runner
	InputRunner
}

var (
	ErrForbiddenFormat = errors.New("tmux: forbidden format variable")
	ErrPercentInArg    = errors.New("tmux: literal % outside a pane id")
	ErrNoSocket        = errors.New("tmux: refusing to run without an explicit socket")
	// ErrBadTarget is a -t whose value is not an id of the shape its verb requires
	// (see shapeFor). It is always a hub defect, which is why it is refused rather
	// than handled.
	ErrBadTarget = errors.New("tmux: -t value has the wrong shape")
)

var forbiddenVars = []string{"client_activity", "client_created"}

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
// fails, while a kill-window whose -t value is the empty string would destroy
// whatever window the user is looking at. Said in prose deliberately: gofmt
// rewrites two single quotes inside a DOC comment into a typographic quote,
// which is how this sentence previously came to read `-t ”`.
// The registry already carries SessionID (`$N`) and WindowID (`@N`)
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
	case "switch-client":
		// $TMUX's third field is a BARE number, and a session target needs the $
		// sigil. Without this shape `switch-client -t 0` would target a session
		// NAMED 0; with it, forgetting the sigil is a refusal at the seam.
		return sessionID, "session id"
	case "select-window":
		return windowID, "window id"
	case "rename-window":
		// The hub renames a window it MADE, to keep the operator's session tree reading the same
		// names as the dashboard (docs/design.md §21.18). A window target, for the reason
		// select-window needs one: `-t 3` without the sigil addresses a window NAMED 3.
		return windowID, "window id"
	case "set", "set-option":
		// `set` is the one verb used at THREE scopes: the stamper writes `set -p`
		// on a pane, lifecycle writes `set -w` on a window, and the status line
		// (docs/design.md §12) writes a SESSION option with no scope flag at all.
		// Keying on the verb alone would let `set -p -t @4` through, and a
		// pane-scoped option written against a window target is a stamp landing
		// somewhere nobody checked.
		//
		// The scope letter is read out of a CLUSTER rather than compared against a
		// whole token, because tmux takes clustered flags and a real caller uses
		// one: `broadcast/stamp.go` unstamps with `set -pu`, which a scan for the
		// literal `-p` does not see. That went unnoticed while the DEFAULT was a
		// pane id — the wrong-scope write it was meant to refuse happened to have
		// the right shape — so moving the default without reading clusters would
		// have refused a live write path.
		//
		// Precedence: window before pane if a cluster somehow carries both (no real
		// caller does). Every dangerous mismatch is refused by the downstream shape
		// check, so the ambiguity is safe.
		//
		// The scan STOPS at the option name, because tmux's flags precede it and
		// everything after it is the option and its VALUE — and a value may look
		// exactly like a cluster. Measured on 3.7b: `set -t p @hub_alias -wip` is
		// rc=0 and the option reads back as `-wip`, so tmux takes it as the value,
		// while a scan of the whole argv read the `w` and demanded a window target —
		// refusing an alias the server would have accepted. (`--` is not the escape
		// here either: `set -t p @hub_alias -- -wip` is rc=1, `too many arguments`.)
		// `-t` is the only flag `set` has that takes a value, so skipping its
		// argument is the whole of the parse.
		for i := 1; i < len(toks); i++ {
			f := unquote(toks[i])
			if f == "-t" {
				i++ // its value is a target, never a flag cluster
				continue
			}
			if !strings.HasPrefix(f, "-") {
				break // the option name: nothing after it is a flag
			}
			if strings.ContainsRune(f, 'w') {
				return windowID, "window id"
			}
			if strings.ContainsRune(f, 'p') {
				return paneID, "pane id"
			}
		}
		// No scope flag: tmux's own default for `set` is the session, and a session
		// target needs the `$` sigil for the reason switch-client does — without it
		// `-t 3` addresses a session NAMED 3.
		return sessionID, "session id"
	}
	return paneID, "pane id"
}

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

func unquote(s string) string { return strings.Trim(s, `'"`) }

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

// validateArg checks one argv element. prev is the element before it.
// shape and want describe what kind of id the command's -t value must name.
func validateArg(a, prev string, shape *regexp.Regexp, want string) error {
	toks := strings.Fields(a)

	if len(toks) <= 1 {
		bare := unquote(a)
		if unquote(prev) == "-t" {
			// An empty or non-pane -t fails OPEN: measured, a send-keys whose -t
			// value is the empty string returns rc=0 and delivers to the server's
			// current pane — the one the user last touched. A stale %999 fails
			// closed and is safe to pass on.
			if !shape.MatchString(bare) {
				return fmt.Errorf("%w: %q is not a %s", ErrBadTarget, bare, want)
			}
			return nil
		}
		if strings.Contains(bare, "%") {
			return fmt.Errorf("%w: %q", ErrPercentInArg, a)
		}
		return nil
	}

	// A multi-token argument is a sub-command chain, where -t and its value are
	// adjacent tokens. A sub-command chain carries its OWN verb, so the shape must
	// be computed from toks rather than inherited from the outer argv.
	subShape, subWant := shapeFor(toks)
	for i, tok := range toks {
		bare := unquote(tok)
		afterT := i > 0 && unquote(toks[i-1]) == "-t"
		if afterT && !subShape.MatchString(bare) {
			return fmt.Errorf("%w: %q is not a %s", ErrBadTarget, bare, subWant)
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

type execRunner struct {
	timeout time.Duration
}

// RawRunner is the unvalidated door, and it is a SEPARATE interface from Exec on
// purpose: nothing that holds an Exec can reach RunRaw by accident, so the ban in
// guard_test.go has a type-level counterpart. Its one legitimate caller is the ssh
// master's own lifecycle (internal/hub/master.go) — `ssh -O check` and `ssh -N -M …`
// carry no tmux argv at all, so Validate would refuse them.
//
// "By accident" is the exact claim. A caller holding an Exec can still assert its way
// in, which the deadline test does deliberately — but that assertion has to be written,
// and writing it puts `RunRaw(` in a production file, which the source ban then names.
// The type keeps it out of reach; the ban catches the reach.
type RawRunner interface {
	RunRaw(ctx context.Context, name string, args ...string) (Result, error)
}

// NewRawRunner returns the raw door under the same per-call deadline as NewExec.
//
// Two constructors over ONE implementation. `main` asks for an Exec for tmux work and
// a RawRunner for master work, and the ASKING is the point: reaching the unvalidated
// door becomes a decision at the call site instead of something an Exec hands you.
// Nothing here is a second process configuration — both are the same *execRunner.
func NewRawRunner(timeout time.Duration) RawRunner {
	return &execRunner{timeout: timeout}
}

// NewExec returns an Exec that shells out to tmux with a per-call deadline.
//
// The deadline is mandatory: a stalled ssh CONNECTION makes the call hang forever
// (design.md §3), and a hung invocation would freeze the UI. Said as "forward"
// until §5 deleted the forward — the rule outlived its mechanism, and there is
// still a stalled connection to bound.
func NewExec(timeout time.Duration) Exec {
	return &execRunner{timeout: timeout}
}

// build turns a tmux argv into the process argv, validating BEFORE wrapping.
//
// The order is the safety property. Validate reads the -t shape out of argv
// (docs/design.md §7); once an argv is inside an ssh payload it is one opaque
// string and the shape check cannot see it. So a remote `send-keys -t @4` is
// refused here exactly as a local one is, and the wrapping happens to an argv
// that has already passed.
func (r *execRunner) build(t Target, args []string) ([]string, error) {
	if err := Validate(args); err != nil {
		return nil, err
	}
	// The target's own args are validated too, and that is a deliberate yes: they come
	// out of a user's hosts.toml, so they are exactly as untrusted as any other string
	// that reaches tmux. `tmux_args = ["-L", "#{client_activity}"]` would segfault the
	// whole server (§3) — the forbidden-format scan is the guard that exists for it,
	// and a config file is a stranger path into this seam than the hub's own call sites.
	//
	// SEPARATELY, in its own call, because Validate reads the -t shape out of args[0]:
	// folding these in front would make shapeFor see `-L` where the verb belongs, so
	// every verb would collapse to the pane-only default and `new-window -t $0` would
	// start being refused. Two calls keep the shape check AND cover the injected args.
	if err := Validate(t.TmuxArgs); err != nil {
		return nil, fmt.Errorf("host %q: its tmux_args %q cannot be sent to tmux: %w — correct "+
			"or delete the tmux_args line for that host in the host list (hosts.toml)",
			t.Label, t.TmuxArgs, err)
	}
	if !t.Remote() {
		if t.Socket == "" {
			return nil, ErrNoSocket
		}
		// After -S, never before: the last -S wins, which is what lets an override
		// override (see Target.TmuxArgs).
		argv := append([]string{"tmux", "-S", t.Socket}, t.TmuxArgs...)
		return append(argv, args...), nil
	}
	if t.ControlPath == "" {
		return nil, fmt.Errorf("%w: host %q is remote and has no control path, so there is "+
			"no master to send through", ErrNoSocket, t.Label)
	}
	// The whole tmux command line is ONE argument, so ssh hands it to the far
	// shell as a unit and the cycle costs one round trip. Quoted per element,
	// because that far shell would otherwise expand `$N` and glob `#{...}`.
	//
	// ProxyCommand=false is what makes a missing master VISIBLE, and it is measured
	// in both directions (§5): through a live master it is a no-op — rc=0, `tmux
	// 3.2a`, the multiplexed path untouched — while with the master absent it gives
	// rc=255 where without it ssh silently opens its own connection and returns rc=0
	// with the right answer. A dead master would then cost latency instead of an
	// error, which is the state hardest to debug. It belongs on the per-command
	// invocation ONLY, never on the master spawn: the master legitimately uses the
	// ssh config's ProxyJump (this machine has `Host *.internal ProxyJump nuc`).
	//
	// There is deliberately NO `-n`. One argv serves both doors, and Run is safe
	// only because exec leaves cmd.Stdin nil, i.e. /dev/null. Adding `-n` "for
	// hygiene" would make the far `load-buffer -` read EOF, so a send would land
	// EMPTY at rc=0 — the exact silent truncation InputRunner exists to close.
	cmdline := append([]string{"tmux"}, t.TmuxArgs...)
	cmdline = append(cmdline, args...)
	payload := ShellJoin(cmdline)
	return []string{"ssh", "-o", "BatchMode=yes", "-o", "ProxyCommand=false",
		"-S", t.ControlPath, t.SSHDest, payload}, nil
}

// sshFailedRC is ssh's own exit code for its own failures: it reserves 255, and
// the far tmux exits 0 or 1. So the code alone identifies a transport failure,
// with no need to match ssh's wording.
const sshFailedRC = 255

// explainTransport rewrites ssh's verdict into one the operator can act on.
//
// What ProxyCommand=false actually fails with is `Connection closed by UNKNOWN
// port 65535` at rc=255 — true, and addressed to nobody. §16's rule is that a
// status with no remedy is a bug report sent to the wrong person, so the message
// names the host, the control path, and the command that fixes it.
//
// It stays an RC and never becomes an error: host-state classification is a pure
// function of Result (§3), and a transport failure is exactly the state that
// classification exists to name. ssh's own text is kept AFTER the remedy rather
// than instead of it, because a reader debugging a ProxyJump needs it.
func explainTransport(t Target, res Result) Result {
	if !t.Remote() || res.RC != sshFailedRC {
		return res
	}
	msg := fmt.Sprintf("host %q was not reached and nothing was sent: no live ssh master at %s "+
		"— respawn it with `ssh -N -M -S %s %s`, then retry",
		t.Label, t.ControlPath, t.ControlPath, t.SSHDest)
	if res.Stderr != "" {
		msg += " [ssh said: " + res.Stderr + "]"
	}
	res.Stderr = msg
	return res
}

func (r *execRunner) Run(ctx context.Context, t Target, args ...string) (Result, error) {
	argv, err := r.build(t, args)
	if err != nil {
		return Result{}, err
	}
	res, err := r.RunRaw(ctx, argv[0], argv[1:]...)
	return explainTransport(t, res), err
}

// RunRaw executes a command under the runner's deadline. It exists so the
// deadline itself is testable without a tmux server.
//
// It is deliberately NOT routed through build: it is the raw-process door, and
// the ssh master the supervisor spawns (`ssh -N -M`) is not a tmux command at
// all, so Validate would refuse it. Nothing that carries a tmux argv may enter
// here — Run and RunInput are the doors that validate.
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

// InputRunner is the seam for commands whose payload is stdin. It exists because
// the payload must NEVER be an argument: send-keys -l truncates text ending in
// `;` at rc=0, send-keys -H takes one byte per argument and delivers nothing for a
// single hex string at rc=0, and set-buffer inherits the same argv quoting. Text
// on stdin has no quoting layer at all, so the whole class is gone.
type InputRunner interface {
	RunInput(ctx context.Context, t Target, stdin []byte, args ...string) (Result, error)
}

func (r *execRunner) RunInput(ctx context.Context, t Target, stdin []byte, args ...string) (Result, error) {
	// The ARGS are validated; the payload deliberately is not. A prompt containing
	// a literal % or a trailing ; is ordinary text, and it is only dangerous when
	// it travels as argv — which is exactly what this method exists to avoid.
	//
	// It goes through the same build as Run, so a remote `load-buffer -` is one ssh
	// invocation whose stdin ssh forwards to the far tmux. Without that the write
	// path would keep its guard and lose its payload transport.
	argv, err := r.build(t, args)
	if err != nil {
		return Result{}, err
	}

	ctx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Stdin = bytes.NewReader(stdin)
	var out, errb strings.Builder
	cmd.Stdout = &out
	cmd.Stderr = &errb

	err = cmd.Run()
	res := Result{Stdout: out.String(), Stderr: strings.TrimSpace(errb.String())}
	if ctx.Err() != nil {
		return res, fmt.Errorf("tmux: deadline exceeded after %s", r.timeout)
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		res.RC = ee.ExitCode()
		return explainTransport(t, res), nil
	}
	return res, err
}
