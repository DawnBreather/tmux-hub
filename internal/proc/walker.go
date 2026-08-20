package proc

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// Walker answers, for one host, which of these panes has an agent at or under it.
// Local and remote are the same question asked through a different transport,
// which is the shape §5 uses for tmux and §17 for the agent listing.
type Walker interface {
	// Walk maps each pane pid to the agent pid found at or under it, or to 0.
	// A pid missing from the result is NOT identified — that is the direction every
	// failure here takes, because an unidentified target is one the confirmation
	// step asks about while a wrongly identified one is written to silently.
	Walk(ctx context.Context, panePIDs []int) (map[int]int, error)
}

// ErrNoTransport is a host the hub cannot walk at all: a remote host reached only
// by a forwarded socket has no shell to run the walk in, which is why the remote
// half of §7 needs the same ssh master §8 requires for attach.
var ErrNoTransport = errors.New("proc: no way to run a process walk on this host")

type sshWalker struct {
	controlPath string
	dest        string
	timeout     time.Duration
}

// OverSSH walks a remote host's processes through an existing ControlMaster.
//
// One invocation answers for every pane, which is the whole reason
// RemoteWalkScript takes a list: one ssh per pane would add a round trip per
// target to every tick.
func OverSSH(controlPath, dest string, timeout time.Duration) Walker {
	return &sshWalker{controlPath: controlPath, dest: dest, timeout: timeout}
}

func (w *sshWalker) Walk(ctx context.Context, panePIDs []int) (map[int]int, error) {
	if len(panePIDs) == 0 {
		return map[int]int{}, nil
	}
	ctx, cancel := context.WithTimeout(ctx, w.timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "ssh", "-S", w.controlPath, w.dest,
		RemoteWalkScript(panePIDs))
	var out, errb strings.Builder
	cmd.Stdout = &out
	cmd.Stderr = &errb
	// WaitDelay is what makes the timeout above real. Because Stdout/Stderr are not
	// *os.File, os/exec creates PIPES plus copier goroutines, and Run does not return
	// while ANYTHING still holds the write end — including a process ssh forked, such
	// as a master's listener. So killing ssh on the deadline is not enough: Wait
	// stays parked reading a pipe a grandchild holds open, and the round never comes
	// back. With WaitDelay set, Wait closes the pipes and returns ErrWaitDelay a
	// second after the process is gone. Measured: a child that leaves a background
	// process holding the pipe took 20 s to return without it and 1.0 s with it.
	cmd.WaitDelay = time.Second
	err := cmd.Run()

	if ctx.Err() != nil {
		return nil, fmt.Errorf("proc: walk on %s exceeded %s", w.dest, w.timeout)
	}
	if err != nil {
		return nil, fmt.Errorf("proc: walk on %s: %w: %s", w.dest, err,
			firstLine(errb.String()))
	}
	// A far side without python3 prints nothing at rc=0 by design, so an empty map
	// is a real answer here: nothing identified, every target confirmed.
	return ParseRemoteWalk(out.String()), nil
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
