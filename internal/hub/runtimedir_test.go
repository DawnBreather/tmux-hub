package hub

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"

	"github.com/DawnBreather/tmux-hub/internal/tmux"
)

// exitSpy records what the sweeps asked ssh to kill. Nothing else about them is
// observable — a stop is a message to a process that then exits — so a test that did
// not record the argv could only assert that sweeping does not crash.
type exitSpy struct {
	mu    sync.Mutex
	paths []string
}

// RunRaw answers rc=0 to everything, which stands for a LIVE master: `-O check` says it
// is running and `-O exit` says it stopped. Only the exit is recorded, because a sweep
// now asks `-O check` first and counting both verbs would report every victim twice.
func (s *exitSpy) RunRaw(_ context.Context, name string, args ...string) (tmux.Result, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	exit := len(args) > 1 && args[0] == "-O" && args[1] == "exit"
	for i, a := range args {
		if a == "-S" && i+1 < len(args) && exit {
			// `ssh -O exit -S <path> <host>`: the path identifies the victim, and the
			// host argument is required by ssh and ignored by it for both verbs.
			s.paths = append(s.paths, args[i+1])
		}
	}
	if name != "ssh" {
		s.paths = append(s.paths, "unexpected command: "+name)
	}
	return tmux.Result{}, nil
}

func (s *exitSpy) stopped() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, len(s.paths))
	copy(out, s.paths)
	return out
}

// socketAt makes a real unix socket, because both sweeps lstat every candidate and skip
// anything that is not one.
func socketAt(t *testing.T, path string) string {
	t.Helper()
	l, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("listen %s: %v", path, err)
	}
	t.Cleanup(func() { _ = l.Close() })
	return path
}

// $RT is `$XDG_RUNTIME_DIR/tmux-hub` at 0700, which is what §5 promises and what the
// code did not build — every control socket landed in the shared runtime directory.
//
// The path is asserted against a LITERAL join rather than against RuntimeDir's own
// answer: a test that took the base from the function under test would pass just as
// happily if the subdirectory were dropped again.
func TestTheRuntimeDirIsTheHubsOwnAndIsCreatedAt0700(t *testing.T) {
	base := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", base)

	got, err := RuntimeDir()
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(base, "tmux-hub"); got != want {
		t.Fatalf("RuntimeDir() = %q, want %q", got, want)
	}
	info, err := os.Stat(got)
	if err != nil {
		t.Fatalf("RuntimeDir() did not create it: %v", err)
	}
	if !info.IsDir() {
		t.Fatalf("%s is not a directory", got)
	}
	if perm := info.Mode().Perm(); perm != 0o700 {
		t.Errorf("mode = %04o, want 0700", perm)
	}
	// Called on every startup, so an existing directory is the normal case.
	if again, err := RuntimeDir(); err != nil || again != got {
		t.Errorf("second call = %q, %v; want the same path and no error", again, err)
	}
}

// The environment is refused BEFORE the join, and that order is the whole reason the
// check exists: `filepath.Join("", "tmux-hub")` is the relative path `tmux-hub`, so
// joining first would silently create a directory under the working directory and put
// every control socket there.
func TestAnUnusableRuntimeEnvironmentIsRefusedRatherThanJoined(t *testing.T) {
	for _, c := range []struct{ name, value string }{
		{"unset", ""},
		{"relative", "run/user/1000"},
	} {
		t.Setenv("XDG_RUNTIME_DIR", c.value)
		dir, err := RuntimeDir()
		if err == nil {
			t.Errorf("%s: RuntimeDir() = %q, want an error", c.name, dir)
		}
		if _, serr := os.Stat("tmux-hub"); serr == nil {
			t.Fatalf("%s: a `tmux-hub` directory was created relative to the working "+
				"directory — this is the failure the absolute check exists to prevent", c.name)
		}
	}
}

// The reason for the subdirectory, asserted as the CONSEQUENCE rather than as a string:
// both sweeps kill by enumerating a directory, so what matters is which directory they
// can reach. A socket named exactly like one of ours, sitting in `$XDG_RUNTIME_DIR`
// beside every other application's runtime files, must survive — and before the join it
// did not, because that directory WAS $RT.
func TestASweepCannotReachOutsideTheHubsOwnDirectory(t *testing.T) {
	base := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", base)
	rt, err := RuntimeDir()
	if err != nil {
		t.Fatal(err)
	}

	// Said before the sockets exist, because otherwise the two paths are one and the
	// second Listen fails with `bind: address already in use` — a true error that
	// explains nothing. The premise of the whole test is that there IS an outside.
	inside, outside := ControlPathFor(rt, "nuc"), ControlPathFor(base, "nuc")
	if inside == outside {
		t.Fatalf("$RT is $XDG_RUNTIME_DIR itself (%s), so there is no outside for the sweep "+
			"to be bounded by — every application's runtime socket is a candidate", rt)
	}
	ours := socketAt(t, inside)
	// Same naming scheme, one directory up: another application's socket, or an older
	// hub's, or a name that merely collides. Either way it is not in our directory.
	theirs := socketAt(t, outside)

	spy := &exitSpy{}
	if _, err := StopAllMasters(context.Background(), spy, rt); err != nil {
		t.Fatalf("StopAllMasters: %v", err)
	}
	got := spy.stopped()
	if len(got) != 1 || got[0] != ours {
		t.Fatalf("stopped %v, want exactly [%s]", got, ours)
	}
	for _, p := range got {
		if p == theirs {
			t.Errorf("the sweep reached %s, which is outside the hub's directory", theirs)
		}
	}

	// The same boundary for the startup reconcile, whose victim set is everything the
	// configured aliases do not account for.
	spy2 := &exitSpy{}
	if _, err := ReconcileMasters(context.Background(), spy2, rt, []string{"eu"}); err != nil {
		t.Fatalf("ReconcileMasters: %v", err)
	}
	got2 := spy2.stopped()
	if len(got2) != 1 || got2[0] != ours {
		t.Fatalf("reconcile stopped %v, want exactly [%s] — `nuc` is not in the configured "+
			"set, so its master is the orphan, and nothing outside $RT is a candidate at all", got2, ours)
	}
}

// A LEFTOVER socket is a SUCCESS, and this asserts the three consequences the ruling
// names rather than the wording of any of them.
//
// Measured before it did: `--stop-masters` with one stale socket present printed
// `failed to stop 1 master(s)` and exited 1 — reporting failure precisely when its own
// intent was already satisfied, and naming neither the path nor anything to do. The
// residue is ordinary: ssh removes its control socket when asked to exit, so a socket
// with nothing behind it is what a master that was KILLED leaves.
func TestALeftoverSocketIsSuccessAndIsRemoved(t *testing.T) {
	base := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", base)
	rt, err := RuntimeDir()
	if err != nil {
		t.Fatal(err)
	}
	stale := staleSocketAt(t, ControlPathFor(rt, "ghost"))

	// rc=1 on `-O check` is ssh saying nothing is listening — the real message is
	// `Control socket connect(…): Connection refused`, and `-O exit` gives the IDENTICAL
	// one, which is why the check is what decides.
	run := fakeExec(func(argv []string) (string, int) { return "", 1 })

	rep, err := StopAllMasters(context.Background(), run, rt)
	if err != nil {
		t.Fatalf("a leftover socket must not be a failure: %v", err)
	}
	if got := rep.Count(MasterStale); got != 1 {
		t.Fatalf("report has %d stale of %d events, want 1 — the caller cannot say what "+
			"happened if the sweep does not tell it", got, len(rep.Events))
	}
	if paths := rep.Paths(MasterStale); len(paths) != 1 || paths[0] != stale {
		t.Errorf("stale paths = %v, want [%s]: a line that does not name the socket sends "+
			"the operator looking for it", paths, stale)
	}
	// Removed, because a state that reports itself on every future run and never clears
	// is worse than either other outcome.
	if _, serr := os.Lstat(stale); !os.IsNotExist(serr) {
		t.Errorf("the leftover socket is still at %s (%v), so the next sweep finds it again",
			stale, serr)
	}
}

// A LIVE master that refuses is the only outcome with anything for the operator to do,
// and therefore the only one that fails. Without this arm, "always succeed" would pass
// the test above.
func TestOnlyALiveMasterThatRefusesIsAFailure(t *testing.T) {
	base := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", base)
	rt, err := RuntimeDir()
	if err != nil {
		t.Fatal(err)
	}
	live := socketAt(t, ControlPathFor(rt, "stubborn"))

	// The check says it is running; the exit fails. That pair is a refusal.
	run := fakeExec(func(argv []string) (string, int) {
		if contains(argv, "exit") {
			return "", 255
		}
		return "Master running (pid=4242)", 0
	})

	rep, err := StopAllMasters(context.Background(), run, rt)
	if err == nil {
		t.Fatal("a live master that would not stop must be a failure — it is the only case " +
			"where the operator has something to do")
	}
	if !strings.Contains(err.Error(), live) {
		t.Errorf("the error does not name the master: %v", err)
	}
	if got := rep.Count(MasterRefused); got != 1 {
		t.Errorf("report has %d refused, want 1", got)
	}
	// And its socket is NOT removed: something is listening on it, and unlinking a live
	// master's socket does not stop the master — it only makes the path undialable while
	// the process runs on (§5's own measurement of the deleted forward design).
	if _, serr := os.Lstat(live); serr != nil {
		t.Errorf("a live master's socket was removed: %v", serr)
	}
}

// staleSocketAt leaves the residue a killed master leaves: a socket file with nothing
// listening. Binding and then closing the listener would unlink it, so the file is
// re-created after the close — the same inode state ssh leaves behind, which is what
// both sweeps have to deal with.
func staleSocketAt(t *testing.T, path string) string {
	t.Helper()
	l, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("listen %s: %v", path, err)
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	// Close unlinks it, so put a socket file back with no listener behind it.
	if err := recreateSocketFile(path); err != nil {
		t.Fatalf("recreate %s: %v", path, err)
	}
	return path
}

// recreateSocketFile makes a socket-mode file at path with nothing listening: bind, then
// drop the listener's fd without letting Go's Close unlink it. syscall.Bind creates the
// inode; closing the fd leaves it on disk.
func recreateSocketFile(path string) error {
	fd, err := syscall.Socket(syscall.AF_UNIX, syscall.SOCK_STREAM, 0)
	if err != nil {
		return err
	}
	defer syscall.Close(fd)
	return syscall.Bind(fd, &syscall.SockaddrUnix{Name: path})
}
