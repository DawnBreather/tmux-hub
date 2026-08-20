package proc

import "testing"

// A real `ps -A -ww -o pid=,ppid=,args=` frame, in the shape darwin prints: the two numeric
// columns are right-aligned with leading spaces, and `args` is the command line already joined.
//
// It carries every case identification has to get right, so the assertions below are about
// `IdentifyAgent` reached THROUGH the parser rather than about field splitting — a parser whose
// fields are correct and whose answer is wrong is the failure worth catching.
const psFrame = `    1     0 /sbin/launchd
  412     1 /usr/local/bin/tmux -L work new-session -d
  418   412 -zsh
  * malformed line with no numbers at all
  501   418 /opt/homebrew/bin/claude
  502   501 /opt/homebrew/bin/node --experimental-vm-modules /opt/homebrew/lib/claude/cli.js
  610     1 /opt/homebrew/bin/claude bg-pty-host --bg-pty-host /tmp/cc-daemon-501
  700   412 /opt/homebrew/bin/claude
  810     1 /Applications/Some App/Contents/MacOS/Some App --flag
  900   412 sleep 300
`

func TestPSTableParsesTheColumnsDarwinPrints(t *testing.T) {
	all := parsePSTable(psFrame)
	if len(all) != 9 {
		t.Fatalf("parsed %d processes, want 9 (the malformed line is skipped, nothing else is)",
			len(all))
	}

	byPID := map[int]Proc{}
	for _, p := range all {
		byPID[p.PID] = p
	}
	// Right-aligned columns must not leave the pid as a string with spaces in it.
	if got := byPID[501]; got.PPID != 418 || len(got.Argv) != 1 ||
		got.Argv[0] != "/opt/homebrew/bin/claude" {
		t.Errorf("pid 501 parsed as %+v, want ppid 418 and argv [/opt/homebrew/bin/claude]", got)
	}
	// Comm is deliberately empty; `isAgent` documents it as unreliable and never reads it.
	if byPID[501].Comm != "" {
		t.Errorf("pid 501 carries Comm %q — this parser must not invent one", byPID[501].Comm)
	}
	// The documented approximation, asserted rather than left implicit: a path containing a space
	// arrives split, and this is the case that says so.
	// `/Applications/Some App/Contents/MacOS/Some App --flag` → 4 fields, not 3: the two spaces
	// inside the path each start a new one.
	if got := byPID[810].Argv; len(got) != 4 {
		t.Errorf("pid 810 argv is %q (%d elements); the space-joined rendering splits, and the "+
			"test exists to record that it does", got, len(got))
	}
}

func TestIdentifyAgentThroughThePSTable(t *testing.T) {
	all := parsePSTable(psFrame)

	for _, c := range []struct {
		name    string
		panePID int
		want    int
		ok      bool
	}{
		// The shell shape: the pane runs zsh and claude is its child.
		{"a shell pane with an agent under it", 418, 501, true},
		// The other launch shape: the pane's command IS claude, so pane_pid is the agent.
		{"a pane that is the agent itself", 700, 700, true},
		// A daemon role must be REFUSED. Accepting it would let a prompt be written to a process
		// that cannot read one.
		{"a claude daemon is not an agent", 610, 0, false},
		// A plain command stays refused, which is what says the walk did not widen what it accepts.
		{"a sleep is not an agent", 900, 0, false},
		{"an unknown pid finds nothing", 4242, 0, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, ok := IdentifyAgent(all, c.panePID)
			if got != c.want || ok != c.ok {
				t.Errorf("IdentifyAgent(pane %d) = %d, %v; want %d, %v",
					c.panePID, got, ok, c.want, c.ok)
			}
		})
	}
}

// Truncation is the reason `-ww` is in the command, and this is what it would cost: with the daemon
// role cut off the line still begins with `claude`, so the process would be ACCEPTED as an agent.
func TestATruncatedArgsColumnWouldAcceptADaemon(t *testing.T) {
	full := parsePSTable("  610     1 /opt/homebrew/bin/claude bg-pty-host --bg-pty-host /tmp/x\n")
	if _, ok := IdentifyAgent(full, 610); ok {
		t.Fatal("the untruncated line identified a daemon as an agent")
	}
	cut := parsePSTable("  610     1 /opt/homebrew/bin/claude\n")
	if _, ok := IdentifyAgent(cut, 610); !ok {
		t.Fatal("the truncated line was refused for some other reason, so this test no longer " +
			"demonstrates what -ww buys")
	}
}
