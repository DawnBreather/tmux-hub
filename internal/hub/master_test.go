package hub

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/DawnBreather/tmux-hub/internal/tmux"
)

// fakeExec returns an Exec that calls f for every command.
func fakeExec(f func(argv []string) (string, int)) interface {
	RunRaw(context.Context, string, ...string) (tmux.Result, error)
} {
	return &fakeRunner{f: f}
}

type fakeRunner struct {
	f func(argv []string) (string, int)
}

func (r *fakeRunner) RunRaw(ctx context.Context, name string, args ...string) (tmux.Result, error) {
	argv := append([]string{name}, args...)
	out, rc := r.f(argv)
	return tmux.Result{Stdout: out, RC: rc}, nil
}

func contains(argv []string, s string) bool {
	for _, a := range argv {
		if a == s {
			return true
		}
	}
	return false
}

// The hazard this whole type exists for, and it is measured: pointed at a control
// path that is NOT a live master, `ssh -S <path> host 'tmux -V'` returns rc=0 with
// the right answer, because ssh silently opens its own connection. So a dead master
// never announces itself — it costs latency instead, 323 ms becoming 7.0 s against
// this fleet's slowest host. Presence is therefore ASSERTED, never inferred from a
// failure, because there is no failure to infer it from.
func TestEnsureAssertsThePresenceOfTheMaster(t *testing.T) {
	var calls []string
	var spawned bool
	run := fakeExec(func(argv []string) (string, int) {
		calls = append(calls, strings.Join(argv, " "))
		if contains(argv, "-O") {
			if spawned {
				return "Master running", 0
			}
			return "", 1 // no master yet
		}
		spawned = true
		return "", 0
	})
	m := &Master{Alias: "nuc", ControlPath: "/run/cm-nuc"}
	if err := m.Ensure(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	if len(calls) < 2 || !strings.Contains(calls[0], "-O check") {
		t.Fatalf("Ensure must CHECK before it spawns; calls = %q", calls)
	}
	// Find the spawn call (the one with -M and -N).
	var spawnIdx int
	for i, call := range calls {
		if strings.Contains(call, "-M") && strings.Contains(call, "-N") {
			spawnIdx = i
			break
		}
	}
	if spawnIdx == 0 {
		t.Fatal("no spawn call found")
	}
	if !strings.Contains(calls[spawnIdx], "-N") || !strings.Contains(calls[spawnIdx], "-M") {
		t.Errorf("the spawn is not a master: %q", calls[spawnIdx])
	}
	if !strings.Contains(calls[spawnIdx], "-f") {
		t.Errorf("spawn missing -f: without it the call never returns, because a master does not exit: %q", calls[spawnIdx])
	}
	if strings.Contains(calls[spawnIdx], "-L") {
		t.Errorf("there is no port forward in this design (§5): %q", calls[spawnIdx])
	}
}

// The master spawn must not carry -o ProxyCommand=false, because the master
// legitimately uses the ssh config's ProxyJump (this machine has `Host *.internal
// ProxyJump nuc`). That flag on the spawn breaks every proxied host — silently,
// since such a host would simply never come up. Task 2 put the flag on the
// per-command argv, where it turns a dead master from a silent fallback into
// rc=255; the spawn argv lives here and must never gain it.
func TestTheSpawnNeverCarriesProxyCommandFalse(t *testing.T) {
	var spawnArgv []string
	run := fakeExec(func(argv []string) (string, int) {
		if contains(argv, "-O") {
			return "", 1 // no master yet
		}
		if contains(argv, "-M") && contains(argv, "-N") {
			spawnArgv = argv
			return "", 0
		}
		return "", 0
	})
	m := &Master{Alias: "prod", ControlPath: "/run/cm-prod"}
	m.Ensure(context.Background(), run)

	if len(spawnArgv) == 0 {
		t.Fatal("no spawn call captured")
	}
	// Assert the spawn has the required flags.
	if !contains(spawnArgv, "-N") || !contains(spawnArgv, "-M") {
		t.Errorf("spawn is missing -N or -M: %v", spawnArgv)
	}
	if !contains(spawnArgv, "-f") {
		t.Errorf("spawn missing -f: without it the call never returns: %v", spawnArgv)
	}
	if !contains(spawnArgv, "-S") {
		t.Errorf("spawn is missing -S: %v", spawnArgv)
	}
	if !contains(spawnArgv, "prod") {
		t.Errorf("spawn is missing alias: %v", spawnArgv)
	}
	// Assert ProxyCommand is NOT present.
	for _, arg := range spawnArgv {
		if strings.Contains(arg, "ProxyCommand") {
			t.Errorf("spawn must not carry ProxyCommand (breaks ProxyJump): %v", spawnArgv)
		}
	}
}

// Without -f, the spawn call blocks until the master exits (which never happens)
// and Ensure times out. This test uses a blocking fake to verify Ensure completes:
// a fake that returns immediately cannot distinguish -N -M from -f -N -M by
// behaviour, which is why five green tests coexisted with a spawn that cannot work.
func TestEnsureCompletesEvenWhenSpawnWouldBlock(t *testing.T) {
	var calls []string
	var spawned bool
	run := fakeExec(func(argv []string) (string, int) {
		calls = append(calls, strings.Join(argv, " "))
		if contains(argv, "-O") {
			if spawned {
				return "Master running", 0
			}
			return "", 1 // no master yet
		}
		// Verify the spawn has -f before returning. Without -f, a real ssh would
		// block forever, and this test would timeout.
		if contains(argv, "-M") && contains(argv, "-N") {
			if !contains(argv, "-f") {
				t.Errorf("spawn missing -f: would block forever")
			}
			spawned = true
			return "", 0
		}
		return "", 0
	})
	m := &Master{Alias: "nuc", ControlPath: "/run/cm-nuc"}

	// Use a short timeout to verify Ensure completes quickly rather than blocking.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := m.Ensure(ctx, run); err != nil {
		t.Fatalf("Ensure failed: %v", err)
	}

	// Verify the spawn was issued.
	var sawSpawn bool
	for _, call := range calls {
		if strings.Contains(call, "-M") && strings.Contains(call, "-N") {
			sawSpawn = true
			break
		}
	}
	if !sawSpawn {
		t.Error("no spawn call was issued")
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

// Stop is idempotent: a second call on the same master does not error.
func TestStopIsIdempotent(t *testing.T) {
	var calls []string
	run := fakeExec(func(argv []string) (string, int) {
		calls = append(calls, strings.Join(argv, " "))
		return "", 0
	})
	m := &Master{Alias: "nuc", ControlPath: "/run/cm-nuc"}
	if err := m.Stop(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	// Two calls now, and the ORDER is the property: `-O check` decides whether there is
	// anything to stop, because `-O exit`'s rc=255 cannot tell "nothing is listening"
	// from "ssh could not run" (measured — both give the identical message). The fake
	// answers rc=0 to the check, which stands for a live master.
	if len(calls) != 2 {
		t.Fatalf("Stop must ask, then act; calls = %q", calls)
	}
	if !strings.Contains(calls[0], "-O check") || !strings.Contains(calls[1], "-O exit") {
		t.Errorf("Stop must check before it exits; calls = %q", calls)
	}
}

// An orphaned master (one whose alias is not in the configured set) is stopped.
func TestReconcileStopsOrphanedMasters(t *testing.T) {
	dir := t.TempDir()
	// Create a socket file matching the control path pattern.
	orphanPath := filepath.Join(dir, "cm-deadbeef-old_host")
	sock := createSocketKeptOpen(t, orphanPath)
	defer sock.Close()

	var calls []string
	run := fakeExec(func(argv []string) (string, int) {
		calls = append(calls, strings.Join(argv, " "))
		return "", 0
	})

	// Reconcile with a configured alias that is NOT the orphan.
	if _, err := ReconcileMasters(context.Background(), run, dir, []string{"prod"}); err != nil {
		t.Fatal(err)
	}

	if len(calls) != 2 {
		t.Fatalf("expected a check and then an exit, got %d: %q", len(calls), calls)
	}
	if !strings.Contains(calls[1], "-O exit") || !strings.Contains(calls[1], orphanPath) {
		t.Errorf("the stop did not target the orphan: %q", calls)
	}
}

// A configured alias's master is not stopped during reconciliation.
func TestReconcilePreservesConfiguredMasters(t *testing.T) {
	dir := t.TempDir()
	// Create a socket for a configured alias.
	alias := "prod"
	configuredPath := ControlPathFor(dir, alias)
	sock := createSocketKeptOpen(t, configuredPath)
	defer sock.Close()

	var calls []string
	run := fakeExec(func(argv []string) (string, int) {
		calls = append(calls, strings.Join(argv, " "))
		return "", 0
	})

	if _, err := ReconcileMasters(context.Background(), run, dir, []string{alias}); err != nil {
		t.Fatal(err)
	}

	if len(calls) != 0 {
		t.Errorf("configured master was stopped: %q", calls)
	}
}

// An empty runtime directory is a no-op and does not error.
func TestReconcileOnEmptyDirectoryIsANoOp(t *testing.T) {
	dir := t.TempDir()
	var calls []string
	run := fakeExec(func(argv []string) (string, int) {
		calls = append(calls, strings.Join(argv, " "))
		return "", 0
	})

	if _, err := ReconcileMasters(context.Background(), run, dir, []string{"prod"}); err != nil {
		t.Fatal(err)
	}

	if len(calls) != 0 {
		t.Errorf("empty directory triggered calls: %q", calls)
	}
}

// A stop failure does not abort the sweep: other orphans are still stopped.
func TestReconcileContinuesAfterStopFailure(t *testing.T) {
	dir := t.TempDir()
	orphan1 := filepath.Join(dir, "cm-aaaaaaaa-host1")
	orphan2 := filepath.Join(dir, "cm-bbbbbbbb-host2")
	sock1 := createSocketKeptOpen(t, orphan1)
	defer sock1.Close()
	sock2 := createSocketKeptOpen(t, orphan2)
	defer sock2.Close()

	var calls []string
	run := fakeExec(func(argv []string) (string, int) {
		line := strings.Join(argv, " ")
		calls = append(calls, line)
		// A LIVE master that refuses: the check says it is running (rc=0) and the exit
		// fails. That pair is what a failure is now — an `-O exit` failing on its own
		// means nothing was listening, which is the intent already satisfied rather than
		// a fault, so this test would otherwise be asserting the old conflation.
		if strings.Contains(line, orphan1) && strings.Contains(line, "-O exit") {
			return "", 1
		}
		return "", 0
	})

	// Pass a configured alias different from the orphans.
	_, err := ReconcileMasters(context.Background(), run, dir, []string{"prod"})
	if err == nil {
		t.Fatal("a live master that refused to stop must be an error")
	}
	if !strings.Contains(err.Error(), "1 of 2") {
		t.Errorf("the error must say how many of how many: %v", err)
	}
	// And it must carry the path, because the count alone gives the operator nothing to
	// act on — the whole reason this sweep reports at all.
	if !strings.Contains(err.Error(), orphan1) {
		t.Errorf("the error does not name the master that refused: %v", err)
	}

	// Two victims, two verbs each: the sweep did not abort at the first refusal.
	if len(calls) != 4 {
		t.Errorf("sweep aborted after first failure; calls = %q", calls)
	}
}

// Only files matching the control path pattern are considered. Regular files,
// symlinks, and misnamed sockets are ignored.
func TestReconcileIgnoresNonMatchingFiles(t *testing.T) {
	dir := t.TempDir()
	// Create files that should be ignored.
	_ = os.WriteFile(filepath.Join(dir, "cm-notahex-host"), []byte("data"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "cm-short-x"), []byte("data"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "other-file"), []byte("data"), 0o644)
	_ = os.Symlink("/nonexistent", filepath.Join(dir, "cm-deadbeef-symlink"))

	var calls []string
	run := fakeExec(func(argv []string) (string, int) {
		calls = append(calls, strings.Join(argv, " "))
		return "", 0
	})

	if _, err := ReconcileMasters(context.Background(), run, dir, []string{"prod"}); err != nil {
		t.Fatal(err)
	}

	if len(calls) != 0 {
		t.Errorf("non-matching files triggered stops: %q", calls)
	}
}

// ReconcileMasters refuses an empty alias list, because "I have no configured
// hosts" and "stop everything" are different intents.
func TestReconcileRefusesEmptyAliasList(t *testing.T) {
	dir := t.TempDir()
	orphanPath := filepath.Join(dir, "cm-deadbeef-old_host")
	sock := createSocketKeptOpen(t, orphanPath)
	defer sock.Close()

	var calls []string
	run := fakeExec(func(argv []string) (string, int) {
		calls = append(calls, strings.Join(argv, " "))
		return "", 0
	})

	_, err := ReconcileMasters(context.Background(), run, dir, nil)
	if err == nil {
		t.Fatal("expected error for empty alias list")
	}
	if !strings.Contains(err.Error(), "StopAllMasters") {
		t.Errorf("error does not name the remedy: %v", err)
	}

	// The orphan must not have been stopped.
	if len(calls) != 0 {
		t.Errorf("empty list triggered stops: %q", calls)
	}
}

// StopAllMasters stops everything matching the pattern, including a configured
// host's master.
func TestStopAllMastersStopsEverything(t *testing.T) {
	dir := t.TempDir()
	// Create two masters: one that would be configured, one that wouldn't.
	alias := "prod"
	configuredPath := ControlPathFor(dir, alias)
	sock1 := createSocketKeptOpen(t, configuredPath)
	defer sock1.Close()

	orphanPath := filepath.Join(dir, "cm-deadbeef-old_host")
	sock2 := createSocketKeptOpen(t, orphanPath)
	defer sock2.Close()

	var calls []string
	run := fakeExec(func(argv []string) (string, int) {
		calls = append(calls, strings.Join(argv, " "))
		return "", 0
	})

	if _, err := StopAllMasters(context.Background(), run, dir); err != nil {
		t.Fatal(err)
	}

	// Both masters must have been stopped, each asked before it was told.
	if len(calls) != 4 {
		t.Fatalf("expected a check and an exit for each of two masters, got %d: %q",
			len(calls), calls)
	}
	// Verify both paths were targeted.
	allCalls := strings.Join(calls, " ")
	if !strings.Contains(allCalls, configuredPath) {
		t.Errorf("configured master was not stopped: %q", calls)
	}
	if !strings.Contains(allCalls, orphanPath) {
		t.Errorf("orphan was not stopped: %q", calls)
	}
}

// isValidControlPathName decides what gets killed by ReconcileMasters and
// StopAllMasters. Calibrate it the way the repo calibrates its scanners: one
// case each for valid and every rejection reason.
func TestIsValidControlPathName(t *testing.T) {
	cases := []struct {
		name  string
		valid bool
	}{
		// Valid: cm-<8hex>-<sanitized>
		{"cm-deadbeef-host", true},
		{"cm-12345678-prod", true},
		{"cm-abcdef01-my_server", true},

		// Invalid: no cm- prefix
		{"deadbeef-host", false},
		{"other-file", false},

		// Invalid: wrong number of parts (fewer than 3)
		{"cm-deadbeef", false},
		{"cm-", false},

		// Invalid: prefix wrong length
		{"cm-dead-host", false},       // 4 chars, need 8
		{"cm-deadbeef00-host", false}, // 10 chars, need 8
		{"cm-short-x", false},         // 5 chars, need 8

		// Invalid: non-hex in prefix
		{"cm-notahex!-host", false},
		{"cm-DEADBEEF-host", false}, // uppercase not allowed
		{"cm-deadbeeg-host", false}, // 'g' is not hex
	}

	for _, tc := range cases {
		got := isValidControlPathName(tc.name)
		if got != tc.valid {
			t.Errorf("isValidControlPathName(%q) = %v, want %v", tc.name, got, tc.valid)
		}
	}
}

// createSocketKeptOpen creates a unix socket at the given path and returns the
// listener. The caller must Close() the listener when done. The socket file
// persists while the listener is open.
func createSocketKeptOpen(t *testing.T, path string) net.Listener {
	t.Helper()
	// Remove any existing file first.
	os.Remove(path)

	l, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("failed to create socket %s: %v", path, err)
	}

	// Verify the socket file exists.
	info, err := os.Lstat(path)
	if err != nil {
		l.Close()
		t.Fatalf("socket file not created: %v", err)
	}
	if info.Mode()&os.ModeSocket == 0 {
		l.Close()
		t.Fatalf("file is not a socket: %v", info.Mode())
	}

	return l
}
