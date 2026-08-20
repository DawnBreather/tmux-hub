package proc

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// The local walker answers for every pid it was asked about, including the ones
// with no agent. "Asked and not found" has to be distinguishable from "never
// asked": the caller turns a 0 into "unidentified", and a MISSING key into the
// same thing, so a walker that silently dropped pids would look identical to one
// that found nothing — and both must be distinguishable from a walk that failed.
func TestLocalWalkerAnswersForEveryPID(t *testing.T) {
	got, err := Local().Walk(context.Background(), []int{os.Getpid(), 999999999})
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("Walk answered for %d of 2 pids: %v", len(got), got)
	}
	// The test binary is not `claude`, so neither pid may be identified. A walker
	// that identified this process would identify anything.
	if got[os.Getpid()] != 0 {
		t.Errorf("the test binary was identified as an agent (pid %d)", got[os.Getpid()])
	}
	if got[999999999] != 0 {
		t.Errorf("a pid that does not exist was identified as an agent")
	}
}

func TestLocalWalkerOnAnEmptyList(t *testing.T) {
	got, err := Local().Walk(context.Background(), nil)
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("Walk(nil) = %v, want empty", got)
	}
}

// An ssh walk that cannot reach its host must return an ERROR, never an empty map
// that reads as "walked and found no agents". The caller unstamps on both, but
// only the error can tell the user why every target now needs confirming.
func TestSSHWalkerReportsAnUnreachableHost(t *testing.T) {
	w := OverSSH("/nonexistent/control/path", "no-such-host.invalid", time.Second)
	got, err := w.Walk(context.Background(), []int{1234})
	if err == nil {
		t.Fatalf("Walk on an unreachable host = %v, want an error", got)
	}
	if len(got) != 0 {
		t.Errorf("a failed walk still returned identifications: %v", got)
	}
}

// The walk must not outlive its own timeout, and the thing that makes it outlive it
// is a GRANDCHILD holding the pipe — which is the normal shape here, since ssh forks.
//
// Stdout and Stderr are strings.Builders, so os/exec makes pipes plus copier
// goroutines and Run does not return while anything still holds the write end, even
// after the deadline killed ssh itself. Only cmd.WaitDelay bounds that. The stand-in
// for ssh is a script that exits at once and leaves a background process holding its
// stdout: measured on this machine, Walk returned in 1.0 s with WaitDelay set and
// 20 s — the grandchild's whole lifetime — with it removed, while the walker's own
// 2-second timeout claimed otherwise in both cases.
func TestSSHWalkDoesNotOutliveItsTimeout(t *testing.T) {
	dir := t.TempDir()
	fake := filepath.Join(dir, "ssh")
	// The grandchild inherits the pipe and outlives its parent by far longer than
	// the walker's timeout, so a Walk that waits for it is unmistakable.
	script := "#!/bin/sh\nsleep 20 &\nexit 0\n"
	if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
		t.Fatalf("writing the ssh stand-in: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	w := OverSSH("/nonexistent/control/path", "somewhere", 2*time.Second)
	done := make(chan time.Duration, 1)
	go func() {
		start := time.Now()
		w.Walk(context.Background(), []int{1234})
		done <- time.Since(start)
	}()
	select {
	case took := <-done:
		t.Logf("Walk returned in %s", took)
		// Generous, because the point is 1 s against 20 s and not the exact figure.
		if took > 8*time.Second {
			t.Errorf("Walk took %s, longer than its own 2s timeout allows", took)
		}
	case <-time.After(12 * time.Second):
		t.Fatal("Walk never returned: a grandchild still holds the pipe, so cmd.Run " +
			"is parked reading it and the identification round can never come back")
	}
}

// An empty pane list costs no round trip at all: the tick asks on every poll, and a
// host with nothing selected must not pay for an ssh invocation.
func TestSSHWalkerSkipsAnEmptyList(t *testing.T) {
	w := OverSSH("/nonexistent/control/path", "no-such-host.invalid", time.Second)
	got, err := w.Walk(context.Background(), nil)
	if err != nil {
		t.Fatalf("Walk(nil) = %v, want no error and no invocation", err)
	}
	if len(got) != 0 {
		t.Errorf("Walk(nil) = %v, want empty", got)
	}
}
