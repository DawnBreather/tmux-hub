package ui

import (
	"fmt"
	"testing"

	"github.com/DawnBreather/tmux-hub/internal/project"
	"github.com/DawnBreather/tmux-hub/internal/registry"
	"github.com/DawnBreather/tmux-hub/internal/state"
)

// THE FILESYSTEM VIEW'S OWN COST, so the next efficiency proposal can be argued with a number.
//
// This repo has a standing figure for the flat dashboard — a whole `View()` at 60 µs on a 45-row fleet,
// against a paint interval of 250 ms — and it has been used twice to decline a micro-optimisation. That
// number is now true of ONE screen. Measured here on a fleet shaped like the operator's (54 sessions,
// three volumes, six levels, twenty directories, names as long as the prompts that started them):
//
//	treeLines        137 µs before memoising rollUp, 103 µs after
//	RenderTreeScreen  82 µs
//
// So the tree's frame is about 180 µs — three times the flat screen's, and still 0.07% of the interval it
// is painted in. Anything cheaper than this is not worth arguing about; anything that makes `treeLines`
// grow with the fleet faster than linearly is.
//
// realShapedFleet is the operator's own fleet in shape: 54 sessions, six levels deep, ~20 directories
// across three volumes, names as long as a prompt.
func realShapedFleet() []registry.Pane {
	dirs := []string{
		"/home/dev/lab/streams/st/st-edgebox", "/home/dev/lab/streams/st/frontend",
		"/home/dev/lab/streams/st/tundra-security-server", "/home/dev/lab/streams/st/lex",
		"/home/dev/lab/streams/experiments/xmap-reverse-engineering",
		"/home/dev/lab/streams/experiments/project-hub", "/home/dev/lab/streams/experiments/smtp-server",
		"/home/dev/lab/streams/experiments/tmux-hub", "/home/dev/lab/streams/orbits/billing-iac",
		"/home/dev/lab/streams/qa/ansible", "/home/dev/lab/streams/qa/sample-author/ansible-20260818",
		"/home/dev/lab/crater/erp", "/opt/vendor/thing", "",
	}
	hosts := []string{"local", "nuc", "side-desk"}
	states := []state.State{state.Needs, state.Works, state.Done, state.Idle, state.Error}
	var ps []registry.Pane
	for i := 0; i < 54; i++ {
		id := fmt.Sprintf("%08d", i+1)
		ps = append(ps, registry.Pane{
			Kind: registry.KindAgent, Command: "background", Host: hosts[i%len(hosts)],
			Session: fmt.Sprintf("20260803--a-session-named-after-the-prompt-that-started-it-%d", i),
			AgentID: id, SessionID: id + "-a", PaneID: "agent:" + id + "@b",
			ClassifiedState: states[i%len(states)], Path: dirs[i%len(dirs)],
			Content: []string{"  (no pane)"},
		})
	}
	return ps
}

// TreeLines is the dominant term of a tree frame, and the one to watch: it walks the fleet, builds a
// trie, collapses it, rolls the attention up and flattens the result.
func BenchmarkTreeLines(b *testing.B) {
	fleet := realShapedFleet()
	for i := 0; i < b.N; i++ {
		treeLines(fleet, project.Aliases{}, nil, nil, "/home/dev", "local")
	}
}

// TreeFrame is the paint over lines already built, which is what View does with the cached `Frame.Tree`.
func BenchmarkTreeFrame(b *testing.B) {
	fleet := realShapedFleet()
	tls := treeLines(fleet, project.Aliases{}, nil, nil, "/home/dev", "local")
	f := Frame{Panes: fleet, Hosts: hosts2(), Width: 120, Height: 40, Home: "/home/dev",
		Screen: modeTree, Tree: tls, TreeCursor: 0, Aliases: project.Aliases{}}
	for i := 0; i < b.N; i++ {
		RenderTreeScreen(f)
	}
}
