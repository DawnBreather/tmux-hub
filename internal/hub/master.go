package hub

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/DawnBreather/tmux-hub/internal/tmux"
)

// Master is an ssh control master for one host.
type Master struct {
	Alias       string
	ControlPath string
}

// RuntimeDir returns the hub's own runtime directory — `$XDG_RUNTIME_DIR/tmux-hub`,
// which is what docs/design.md §5 calls `$RT` — creating it at 0700 if it is not there.
//
// The subdirectory is BLAST RADIUS, not tidiness and not a security control.
// ReconcileMasters and StopAllMasters ENUMERATE this directory and `ssh -O exit`
// everything in it whose name matches `cm-<8hex>-*`, whoever created it; with the
// sockets in `$XDG_RUNTIME_DIR` itself, a routine startup swept the directory every
// other application on the machine keeps its runtime files in. A destructive sweep
// belongs in a directory the hub owns.
//
// The 0700 is a property of the directory we CREATE, not a check on one we find. This
// project does not implement ownership, symlink or group-accessibility refusals —
// security is out of scope at this stage and §5 no longer promises them, because an
// unimplemented promise in a doc is worse than an honest absence. Nothing is exposed
// today either way: on systemd `/run/user/$UID` is itself 0700, so what the
// subdirectory changes is what a SWEEP can reach, not who can read a socket.
func RuntimeDir() (string, error) {
	base := os.Getenv("XDG_RUNTIME_DIR")
	if base == "" {
		return "", fmt.Errorf("XDG_RUNTIME_DIR is unset; set it to /run/user/$UID or equivalent")
	}
	// The two checks come BEFORE the join, and that order is the point: with the
	// variable unset, `filepath.Join("", "tmux-hub")` is the RELATIVE path `tmux-hub`,
	// so joining first would turn a missing environment into a directory created in
	// whatever the working directory happens to be (§5's own table names this case).
	if !filepath.IsAbs(base) {
		return "", fmt.Errorf("XDG_RUNTIME_DIR is relative (%q); it must be an absolute path", base)
	}
	dir := filepath.Join(base, "tmux-hub")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("cannot create the hub's runtime directory %s: %w", dir, err)
	}
	return dir, nil
}

// ControlPathFor returns a safe control socket path for an alias. The name is
// sanitised (slashes and dots removed) plus a sha256 prefix to ensure two
// different aliases never collide.
func ControlPathFor(dir, alias string) string {
	h := sha256.Sum256([]byte(alias))
	prefix := fmt.Sprintf("%x", h[:4])
	safe := strings.NewReplacer("/", "_", ".", "_", " ", "_").Replace(alias)
	if len(safe) > 60 {
		safe = safe[:60]
	}
	return filepath.Join(dir, fmt.Sprintf("cm-%s-%s", prefix, safe))
}

// isValidControlPathName returns true if name matches the control path pattern:
// cm-<8hex>-<sanitized>, where the hex prefix is exactly 8 lowercase hex chars.
// This predicate decides what gets killed by ReconcileMasters and StopAllMasters,
// so it is extracted once rather than duplicated — two copies of a destructive
// test is a different risk from two copies of a formatter.
func isValidControlPathName(name string) bool {
	if !strings.HasPrefix(name, "cm-") {
		return false
	}
	parts := strings.SplitN(name, "-", 3)
	if len(parts) < 3 {
		return false
	}
	if len(parts[1]) != 8 {
		return false
	}
	// Verify it's a hex prefix.
	for _, c := range parts[1] {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}

// Ensure asserts that the master is running. If it is not, it spawns one and
// waits for it to become reachable.
//
// The run parameter is tmux.RawRunner, not the usual tmux.Runner, because a
// master spawn (`ssh -N -M`) carries no tmux command at all and tmux.Validate
// would refuse it. An Exec holder can assert its way to RunRaw: the type keeps
// the door out of reach and guard_test.go's source ban catches the reach.
func (m *Master) Ensure(ctx context.Context, run tmux.RawRunner) error {
	// Check if the master is already running.
	res, err := run.RunRaw(ctx, "ssh", "-O", "check", "-S", m.ControlPath, m.Alias)
	if err != nil {
		return fmt.Errorf("ssh -O check: %w", err)
	}
	if res.RC == 0 {
		// Master is already running.
		return nil
	}

	// Spawn the master. The -f flag is load-bearing: without it this call never
	// returns, because a master does not exit. ssh -f backgrounds itself after
	// authentication, which is also why a master outlives the hub and gets
	// reparented to pid 1 (the adoption behaviour §5 relies on).
	_, err = run.RunRaw(ctx, "ssh", "-f", "-N", "-M", "-S", m.ControlPath,
		"-o", "BatchMode=yes", "-o", "ServerAliveInterval=15", m.Alias)
	if err != nil {
		return fmt.Errorf("ssh master spawn: %w", err)
	}

	// Wait for the master to become checkable. Measured: 1530-1606 ms, about 28
	// failed checks at 50 ms apart. The deadline is 10 s, six times the observed
	// worst.
	deadline := time.Now().Add(10 * time.Second)
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if time.Now().After(deadline) {
				return fmt.Errorf("master did not become checkable within 10s")
			}
			res, err := run.RunRaw(ctx, "ssh", "-O", "check", "-S", m.ControlPath, m.Alias)
			if err != nil {
				continue
			}
			if res.RC == 0 {
				return nil
			}
		}
	}
}

// Stop stops the master. It is idempotent, and that now includes the case a
// leftover socket used to turn into a failure: see stopMasterAt.
func (m *Master) Stop(ctx context.Context, run tmux.RawRunner) error {
	_, err := stopMasterAt(ctx, run, m.ControlPath, m.Alias)
	return err
}

// SweepOutcome is what became of one control path. It is a closed enumeration rather
// than a bool because "there was nothing to stop" is a THIRD answer, and collapsing it
// into either of the others is the defect this type exists to prevent.
type SweepOutcome int

const (
	// MasterStopped: a live master was asked to exit and did. ssh removes its own
	// socket on a clean exit (measured — the directory is empty afterwards).
	MasterStopped SweepOutcome = iota
	// MasterStale: nothing was listening, so the caller's intent was already true.
	// The leftover socket is removed, because a state that reports itself on every
	// future run and never clears is worse than either other outcome.
	MasterStale
	// MasterRefused: a live master would not stop. The ONLY outcome with anything for
	// the operator to do, and therefore the only one that makes a sweep exit non-zero.
	MasterRefused
)

func (o SweepOutcome) String() string {
	switch o {
	case MasterStopped:
		return "stopped"
	case MasterStale:
		return "stale"
	default:
		return "refused"
	}
}

// SweepEvent is one control path a sweep dealt with. The hub reports the FACT and the
// caller owns the wording: `--stop-masters` prints these, the startup reconcile drops
// them, and neither can disagree with the other about what happened.
type SweepEvent struct {
	Path    string
	Outcome SweepOutcome
}

// StopReport is what a sweep did. A sweep that returned only an error could not tell
// the operator it had removed a leftover socket, nor which one — and reported failure
// for an intent that was already satisfied.
type StopReport struct {
	Events []SweepEvent
}

// Count returns how many paths ended in the given outcome.
func (r StopReport) Count(o SweepOutcome) int {
	n := 0
	for _, e := range r.Events {
		if e.Outcome == o {
			n++
		}
	}
	return n
}

// Paths returns the paths that ended in the given outcome, in the order they were swept.
func (r StopReport) Paths(o SweepOutcome) []string {
	var out []string
	for _, e := range r.Events {
		if e.Outcome == o {
			out = append(out, e.Path)
		}
	}
	return out
}

// stopMasterAt stops whatever is at path, and it is the ONE place the three doors —
// Stop, ReconcileMasters and StopAllMasters — decide what happened. Three copies of
// this judgement would be three chances for `--stop-masters` and the startup sweep to
// disagree about the same socket.
//
// It asks ssh's OWN predicate first, the same one Ensure uses, instead of reading
// `-O exit`'s exit code. Measured against a real master on a private path:
//
//   - live master, `-O check`  → rc=0, `Master running (pid=2216420)` — and with a
//     PLACEHOLDER host argument too, because `-S` names the socket and the host is
//     ignored, exactly as for `-O exit`;
//   - after `kill -9`, the socket file SURVIVES, and `-O check` → rc=255
//     `Control socket connect(…): Connection refused`;
//   - the same stale socket, `-O exit` → rc=255 with the IDENTICAL message.
//
// That last pair is why the exit code cannot carry this decision: it conflates "nothing
// is listening" with "ssh could not run at all". The two are told apart here by where
// the failure comes from — a non-zero RC is ssh answering, while an error from RunRaw is
// ssh not running (a missing binary, a deadline), which is a fault and stays one.
func stopMasterAt(ctx context.Context, run tmux.RawRunner, path, host string) (SweepOutcome, error) {
	res, err := run.RunRaw(ctx, "ssh", "-O", "check", "-S", path, host)
	if err != nil {
		return MasterRefused, fmt.Errorf("ssh -O check %s: %w", path, err)
	}
	if res.RC != 0 {
		// Nothing is listening, so the caller's intent is already true. Remove the
		// leftover socket so this does not repeat itself forever; a removal that fails
		// is worth saying, because then it WILL repeat.
		if rmErr := os.Remove(path); rmErr != nil && !os.IsNotExist(rmErr) {
			return MasterStale, fmt.Errorf("no master was listening at %s and its leftover "+
				"socket could not be removed: %w", path, rmErr)
		}
		return MasterStale, nil
	}
	res, err = run.RunRaw(ctx, "ssh", "-O", "exit", "-S", path, host)
	if err != nil {
		return MasterRefused, fmt.Errorf("ssh -O exit %s: %w", path, err)
	}
	if res.RC != 0 {
		return MasterRefused, fmt.Errorf("the master at %s is running and refused to stop "+
			"(ssh said: %s)", path, strings.TrimSpace(res.Stderr))
	}
	return MasterStopped, nil
}

// ReconcileMasters stops orphaned masters at startup. An orphaned master is one
// whose control path exists under runtimeDir but whose alias is not present in
// the configured set.
//
// An empty configuredAliases list is refused with an error, because "I have no
// configured hosts" and "stop everything" are different intents. Use StopAllMasters
// to explicitly stop every master regardless of configuration.
//
// Measured: `ssh -O exit -S <path> placeholder` returns rc=0 even when the host
// argument is nonsense — the host is syntactically required but semantically
// irrelevant for `-O exit` against an existing control socket. So orphan cleanup
// needs no reverse map from path to alias: enumerate the configured aliases
// forward, build the set of their control paths, and stop everything else that
// matches the control path pattern.
//
// One unreachable orphan does not abort the sweep: every path is attempted, and the
// error afterwards names how many LIVE masters refused. A leftover socket is not a
// failure — see stopMasterAt.
func ReconcileMasters(ctx context.Context, run tmux.RawRunner, runtimeDir string, configuredAliases []string) (StopReport, error) {
	if len(configuredAliases) == 0 {
		return StopReport{}, fmt.Errorf("ReconcileMasters called with no configured aliases; use StopAllMasters to stop everything explicitly")
	}

	// Build the set of control paths for configured aliases.
	safe := make(map[string]bool)
	for _, alias := range configuredAliases {
		safe[ControlPathFor(runtimeDir, alias)] = true
	}

	return sweep(ctx, run, runtimeDir, "orphan-cleanup", func(path string) bool {
		return !safe[path]
	})
}

// StopAllMasters stops every master under runtimeDir that matches the control
// path pattern, regardless of whether it is configured. This is the explicit
// intent for `--stop-masters`: stop everything.
//
// One unreachable master does not abort the sweep: every path is attempted, and the
// error afterwards names how many LIVE masters refused. A leftover socket is not a
// failure — the intent of this command is that no master is running, and a socket with
// nothing behind it already satisfies that. See stopMasterAt.
func StopAllMasters(ctx context.Context, run tmux.RawRunner, runtimeDir string) (StopReport, error) {
	return sweep(ctx, run, runtimeDir, "stop-all", func(string) bool { return true })
}

// sweep is the enumeration both sweeps share: which files are candidates, and what
// happened to each. Only the CHOICE of victim differs between them, which is the `want`
// predicate — everything else, including the judgement in stopMasterAt, is one copy.
//
// The candidate rules are unchanged and each is load-bearing: the name must match
// `cm-<8hex>-*` (isValidControlPathName, calibrated separately), and the entry must be a
// SOCKET by lstat — not a symlink to one, because following a symlink would let a path
// outside the hub's directory be swept through a name inside it.
func sweep(ctx context.Context, run tmux.RawRunner, runtimeDir, host string,
	want func(path string) bool) (StopReport, error) {

	entries, err := os.ReadDir(runtimeDir)
	if err != nil {
		return StopReport{}, fmt.Errorf("list runtime dir: %w", err)
	}

	var rep StopReport
	var refused []error
	for _, e := range entries {
		name := e.Name()
		if !isValidControlPathName(name) {
			continue
		}
		path := filepath.Join(runtimeDir, name)
		info, err := os.Lstat(path)
		if err != nil {
			continue
		}
		if info.Mode()&os.ModeSocket == 0 {
			continue
		}
		if !want(path) {
			continue
		}

		// The host argument is syntactically required and semantically ignored for both
		// `-O check` and `-O exit` against an explicit `-S` (measured, both verbs).
		outcome, err := stopMasterAt(ctx, run, path, host)
		rep.Events = append(rep.Events, SweepEvent{Path: path, Outcome: outcome})
		if err != nil {
			refused = append(refused, err)
		}
	}

	// Non-zero for a LIVE master that refused, and for nothing else. The count and the
	// first reason both travel, because "failed to stop 2 master(s)" tells the operator
	// how big the problem is and nothing about what to do.
	if len(refused) > 0 {
		return rep, fmt.Errorf("%d of %d master(s) could not be stopped; first: %w",
			len(refused), len(rep.Events), refused[0])
	}
	return rep, nil
}
