package proc

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Local walks this machine's own processes. It reads /proc once for the whole
// batch, so the cost is one pass however many panes are asked about.
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

// Snapshot reads /proc once. One pass for the whole table beats per-pane lookups:
// the tree has to be built anyway, and a partial read would make identification
// depend on scheduling.
//
// Unreadable entries are skipped rather than reported. A process that exits during
// the walk is the normal case, and a foreign process whose environ we cannot read
// is irrelevant here — identification uses argv, which /proc exposes for every
// process. (CLAUDECODE=1 is NOT usable as the key: measured, it is absent from
// claude's own environ and present only on the children it spawns, and environ was
// readable for just 71 of 145 candidates.)
func Snapshot() ([]Proc, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, err
	}
	out := make([]Proc, 0, len(entries))
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue // not a process directory
		}
		p, ok := readProc(pid)
		if !ok {
			continue
		}
		out = append(out, p)
	}
	return out, nil
}

func readProc(pid int) (Proc, bool) {
	dir := filepath.Join("/proc", strconv.Itoa(pid))

	raw, err := os.ReadFile(filepath.Join(dir, "stat"))
	if err != nil {
		return Proc{}, false
	}
	// The comm field is parenthesised and may itself contain spaces and
	// parentheses, so the fields after it are found from the LAST ')'.
	i := strings.LastIndexByte(string(raw), ')')
	if i < 0 || i+2 >= len(raw) {
		return Proc{}, false
	}
	rest := strings.Fields(string(raw[i+2:]))
	if len(rest) < 2 {
		return Proc{}, false
	}
	ppid, err := strconv.Atoi(rest[1])
	if err != nil {
		return Proc{}, false
	}
	comm := ""
	if j := strings.IndexByte(string(raw), '('); j >= 0 && j < i {
		comm = string(raw[j+1 : i])
	}

	cmdline, err := os.ReadFile(filepath.Join(dir, "cmdline"))
	if err != nil {
		return Proc{}, false
	}
	var argv []string
	for _, part := range strings.Split(string(cmdline), "\x00") {
		if part != "" {
			argv = append(argv, part)
		}
	}
	return Proc{PID: pid, PPID: ppid, Argv: argv, Comm: comm}, true
}
