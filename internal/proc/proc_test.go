package proc

import (
	"os"
	"strings"
	"testing"
	"time"
)

// The fixture is the real shape of this machine's process table, including the
// three traps that make the obvious implementation wrong.
func fixture() []Proc {
	return []Proc{
		{PID: 100, PPID: 1, Comm: "zsh", Argv: []string{"-zsh"}},
		// A real interactive agent, as measured: comm is "claude", argv[0] is the
		// bare word, and it sits under the pane's shell.
		{PID: 263249, PPID: 100, Comm: "claude",
			Argv: []string{"claude", "--dangerously-skip-permissions"}},
		// Trap 1: Node overwrites comm with a THREAD name. Measured values on this
		// machine include "MainThread", "node-MainThread" and "2.1.226" — so comm
		// cannot be the key, and "2.1.226" would not match any name you would guess.
		{PID: 263613, PPID: 263249, Comm: "MainThread",
			Argv: []string{"node", "/home/dev/.claude/plugins/x/server.js"}},
		{PID: 263615, PPID: 263249, Comm: "2.1.226",
			Argv: []string{"node", "/home/dev/.claude/plugins/y/server.js"}},

		// Trap 2: these ARE claude processes and are NOT an interactive agent.
		// Measured: `claude bg-pty-host --bg-pty-host /tmp/cc-daemon-1000` and
		// `claude bg-spare`. A pane holding one of these must not be stamped.
		{PID: 200, PPID: 1, Comm: "zsh", Argv: []string{"-zsh"}},
		{PID: 467408, PPID: 200, Comm: "2.1.226",
			Argv: []string{"claude", "bg-pty-host", "--bg-pty-host", "/tmp/cc-daemon-1000"}},

		// A plain shell pane: the case that must never be identified as an agent,
		// because it is the one the measured accident happened in.
		{PID: 300, PPID: 1, Comm: "zsh", Argv: []string{"-zsh"}},
		{PID: 301, PPID: 300, Comm: "vim", Argv: []string{"vim", "notes.md"}},
	}
}

func TestIdentifiesARealAgent(t *testing.T) {
	got, ok := IdentifyAgent(fixture(), 100)
	if !ok {
		t.Fatal("a pane whose shell has a claude child was not identified")
	}
	if got != 263249 {
		t.Errorf("agent pid = %d, want the claude process 263249", got)
	}
}

// Trap 1 as an assertion: keying on comm would match "MainThread" and "2.1.226"
// and miss nothing useful, so argv[0]'s basename is the key.
func TestDoesNotKeyOnComm(t *testing.T) {
	all := fixture()
	for i := range all {
		if all[i].PID == 263249 {
			all[i].Comm = "node-MainThread" // what Node would leave there
		}
	}
	if _, ok := IdentifyAgent(all, 100); !ok {
		t.Error("identification broke when comm was rewritten — it must use argv[0]")
	}
}

// Trap 2: a background daemon is a claude process and is not an agent to send to.
func TestRefusesTheBackgroundDaemonRoles(t *testing.T) {
	if pid, ok := IdentifyAgent(fixture(), 200); ok {
		t.Errorf("a bg-pty-host pane was identified as an agent (pid %d)", pid)
	}
}

func TestRefusesAPlainShellPane(t *testing.T) {
	if pid, ok := IdentifyAgent(fixture(), 300); ok {
		t.Errorf("a shell running vim was identified as an agent (pid %d)", pid)
	}
	if pid, ok := IdentifyAgent(fixture(), 999999); ok {
		t.Errorf("an unknown pane pid was identified (pid %d)", pid)
	}
}

// The walk must reach a grandchild, because a pane's shell may run claude through
// a wrapper.
func TestDescendantsReachesGrandchildren(t *testing.T) {
	all := append(fixture(), Proc{PID: 400, PPID: 1, Comm: "zsh", Argv: []string{"-zsh"}},
		Proc{PID: 401, PPID: 400, Comm: "sh", Argv: []string{"sh", "-c", "exec claude"}},
		Proc{PID: 402, PPID: 401, Comm: "claude", Argv: []string{"claude"}})
	if pid, ok := IdentifyAgent(all, 400); !ok || pid != 402 {
		t.Errorf("IdentifyAgent through a wrapper = (%d, %v), want (402, true)", pid, ok)
	}
	d := Descendants(all, 400)
	if len(d) != 2 {
		t.Errorf("Descendants(400) returned %d, want 2", len(d))
	}
}

// A cycle in the parent links must not hang the walk. It should not happen, and a
// hub that freezes because it did would be indistinguishable from a hung tunnel.
func TestDescendantsTerminatesOnACycle(t *testing.T) {
	all := []Proc{{PID: 1, PPID: 2}, {PID: 2, PPID: 1}}
	done := make(chan int, 1)
	go func() { done <- len(Descendants(all, 1)) }()
	select {
	case <-done:
	case <-timeAfter():
		t.Fatal("Descendants did not terminate on a cyclic parent chain")
	}
}

// Against the real /proc. It cannot assert a specific agent, so it asserts the
// invariants any snapshot must satisfy — and that our own pid is found, which is
// the one thing guaranteed true.
func TestSnapshotFindsThisProcess(t *testing.T) {
	all, err := Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if len(all) < 5 {
		t.Fatalf("Snapshot returned %d processes", len(all))
	}
	me := os.Getpid()
	for _, p := range all {
		if p.PID == me {
			if len(p.Argv) == 0 {
				t.Error("our own process has no argv")
			}
			return
		}
	}
	t.Errorf("Snapshot did not include our own pid %d", me)
}

// The pane's command may BE claude — `tmux new-window claude` — in which case
// pane_pid is the claude process itself and there is nothing beneath it to find.
// Measured against a live session: a descendants-only walk reported "not
// identified" for a pane plainly running an agent, which is the whole write path
// silently refusing to work for a completely normal way of starting one.
func TestIdentifiesAgentWhenThePaneCommandIsClaudeItself(t *testing.T) {
	all := []Proc{
		{PID: 715435, PPID: 1, Comm: "claude", Argv: []string{"claude"}},
		{PID: 716117, PPID: 715435, Comm: "MainThread", Argv: []string{"node", "mcp.js"}},
	}
	got, ok := IdentifyAgent(all, 715435)
	if !ok {
		t.Fatal("a pane whose own command is claude was not identified")
	}
	if got != 715435 {
		t.Errorf("agent pid = %d, want the pane pid itself", got)
	}
}

// Widening the walk to include the root must not widen what is ACCEPTED: a pane
// whose own command is a daemon role, or something else entirely, still answers no.
func TestTheRootIsHeldToTheSameRules(t *testing.T) {
	daemon := []Proc{{PID: 500, PPID: 1, Comm: "2.1.226",
		Argv: []string{"claude", "bg-pty-host", "--bg-pty-host", "/tmp/cc-daemon-1000"}}}
	if pid, ok := IdentifyAgent(daemon, 500); ok {
		t.Errorf("a pane running bg-pty-host was identified (pid %d)", pid)
	}
	shell := []Proc{{PID: 600, PPID: 1, Comm: "zsh", Argv: []string{"-zsh"}}}
	if pid, ok := IdentifyAgent(shell, 600); ok {
		t.Errorf("a plain shell pane was identified (pid %d)", pid)
	}
}

// The remote form must be one command for ALL selected panes, not one per pane:
// a per-pane ssh would put a round trip per target into every tick.
func TestRemoteWalkIsOneCommandAndRoundTrips(t *testing.T) {
	script := RemoteWalkScript([]int{100, 200, 300})
	for _, want := range []string{"100", "200", "300"} {
		if !contains(script, want) {
			t.Errorf("the script does not mention pane pid %s", want)
		}
	}
	got := ParseRemoteWalk("100 263249\n200 0\n300 0\n")
	if got[100] != 263249 || got[200] != 0 || got[300] != 0 {
		t.Errorf("ParseRemoteWalk = %v", got)
	}
	if len(ParseRemoteWalk("bash: python3: command not found\n")) != 0 {
		t.Error("garbage output must parse to nothing, not to a false identification")
	}
}

func timeAfter() <-chan struct{} {
	ch := make(chan struct{})
	go func() { time.Sleep(2 * time.Second); close(ch) }()
	return ch
}

func contains(hay, needle string) bool { return strings.Contains(hay, needle) }
