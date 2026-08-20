package proc

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
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

	// Wait for the EXEC, not just the fork. Between the two, /proc/<pid>/environ
	// still shows the PARENT's environment, so reading straight after Start() found
	// no session id — measured flaky, 1 run in 3. The wait is on a different
	// observable than the one under test (the child's argv, not its environ), so it
	// cannot turn into "poll until the assertion passes".
	waitForExec(t, cmd.Process.Pid, "sleep")

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

// waitForExec blocks until a freshly started child has replaced its image, which
// is when its own environment becomes visible in /proc.
func waitForExec(t *testing.T, pid int, want string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		b, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "cmdline"))
		if err == nil && strings.Contains(string(b), want) {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("pid %d never exec'd %q", pid, want)
}

// An unreadable environ is routine on a shared machine and must not be an error.
func TestSessionIDOnAnUnreachablePID(t *testing.T) {
	if id, ok := SessionID(nil, 999999999); ok {
		t.Errorf("a nonexistent pid yielded %q", id)
	}
}
