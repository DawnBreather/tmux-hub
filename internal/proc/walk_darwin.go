package proc

import (
	"context"
	"fmt"
	"os/exec"
)

// Local walks this machine's own processes. One pass for the whole batch, as on Linux.
func Local() Walker { return localWalker{} }

type localWalker struct{}

func (localWalker) Walk(_ context.Context, panePIDs []int) (map[int]int, error) {
	all, err := Snapshot()
	if err != nil {
		return nil, err
	}
	out := make(map[int]int, len(panePIDs))
	for _, pid := range panePIDs {
		agent, ok := IdentifyAgent(all, pid)
		if !ok {
			// Recorded as 0 rather than omitted, so a caller can tell "asked and
			// not found" from "never asked".
			out[pid] = 0
			continue
		}
		out[pid] = agent
	}
	return out, nil
}

// Snapshot reads the process table once, through `ps`, because darwin has no `/proc`.
//
// `-ww` is not optional and is the trap in this command: without it `ps` truncates each line to the
// terminal width, and the field that would be cut is `args` — so `claude` at the head of a long
// command line survives while a daemon role at `Argv[1]` can be lost, which turns a process the
// hub must REFUSE into one it accepts. `-A` is every process, `-o …=` suppresses the header.
//
// The remote walk is unaffected: `RemoteWalkScript` runs on the far side and that side is Linux.
func Snapshot() ([]Proc, error) {
	out, err := exec.Command("ps", "-A", "-ww", "-o", "pid=,ppid=,args=").Output()
	if err != nil {
		return nil, fmt.Errorf("proc: reading the process table with ps: %w", err)
	}
	return parsePSTable(string(out)), nil
}
