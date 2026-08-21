package registry

import (
	"testing"
	"time"

	"github.com/DawnBreather/tmux-hub/internal/agents"
	"github.com/DawnBreather/tmux-hub/internal/tmux"
)

// A pane created by the launch or the door runs its program under a wrapper shell, and the row must
// name the PROGRAM — while a shell the operator started themselves must still read as a shell.
//
// The two are one test because they are one rule, and the rule is only right if both poles hold: the
// discriminator is the start command, so a fix that just says "a shell is never the answer" would
// rename every ordinary shell on the fleet.
//
// EVERY FIXTURE HERE IS VERBATIM tmux OUTPUT, measured on 3.7b in two invocations against a private
// socket, one `new-session -d -s <name> <ONE argv word>` per cell — because tmux quotes this field
// itself and a value quoted by hand is a value that agrees with nothing. The launch cell's payload
// is `ui.LoginPayload(["claude", "--session-id", <uuid>])` character for character (that package
// cannot be imported here: it imports this one).
//
// It runs through Update, which is the only producer of a pane row's Command, because a test that
// builds a Pane by hand cannot see a producer that forgets a field — three recorded instances in
// this repo.
func TestAWrappedPaneNamesTheProgramItRunsAndAShellStaysAShell(t *testing.T) {
	cases := []struct {
		pane    string
		why     string
		current string // #{pane_current_command}
		start   string // #{pane_start_command}, as tmux hands it back
		want    string
	}{{
		pane:    "%0",
		why:     "the launch: a `||` arm cannot be exec'd, so the wrapper shell stays in the foreground",
		current: "sh",
		start:   `"sh -lc \"claude --session-id 1cca566d-0000-4000-8000-000000000000 || { echo; echo tmux-hub: the command above failed — press enter to close this pane; read x; }\""`,
		want:    "claude",
	}, {
		pane:    "%1",
		why:     "the door, whose panes have carried an `sh` wrapper since §20",
		current: "sh",
		start:   `"sh -lc \"claude attach 30f3382b || { echo; echo failed; read x; }\""`,
		want:    "claude",
	}, {
		pane:    "%2",
		why:     "a pane created with NO command: the shell a person gets by opening a window, and it IS a shell",
		current: "zsh",
		start:   "",
		want:    "zsh",
	}, {
		pane:    "%3",
		why:     "a shell somebody asked for BY NAME: nothing follows the shell word, so there is nothing better to say",
		current: "sh",
		start:   "sh",
		want:    "sh",
	}, {
		pane:    "%4",
		why:     "a lone command IS exec'd, so tmux already reports the program and nothing is substituted",
		current: "sleep",
		start:   "sleep 300",
		want:    "sleep",
	}, {
		pane:    "%5",
		why:     "the attach window `a` opens: the foreground is not a shell, so the start command is not consulted",
		current: "ssh",
		start:   `"ssh -S /tmp/nope -t nuc tmux attach -t \"$0\""`,
		want:    "ssh",
	}, {
		pane:    "%6",
		why:     "a payload naming an absolute path: the field's contract is a process NAME",
		current: "sh",
		start:   `"sh -lc \"/usr/bin/sleep 300 || { echo; echo failed; read x; }\""`,
		want:    "sleep",
	}, {
		pane: "%7",
		// This is the documented limit rather than an aspiration: the first non-flag word of the
		// payload is what the row names. Asserted so that widening the parse is a deliberate change
		// with a red test in front of it, not a silent one.
		why:     "a wrapper whose payload opens with another wrapper: the payload's first word is named",
		current: "bash",
		start:   `"bash -c \"/usr/bin/env sleep 300 || { echo; echo failed; read x; }\""`,
		want:    "env",
	}, {
		pane:    "%8",
		why:     "a payload that DID exec: tmux already names the program, and the wrapper in the start command must not be re-read",
		current: "sleep",
		start:   `"sh -lc \"exec sleep 300\""`,
		want:    "sleep",
	}, {
		pane:    "%9",
		why:     "a start command that opens with something that is NOT a shell: its second word is not a program",
		current: "sh",
		start:   `"env FOO=1 sh"`,
		want:    "sh",
	}, {
		pane:    "%10",
		why:     "a payload whose own program is a shell: that shell is what the pane runs, and tmux says so too",
		current: "bash",
		start:   `"sh -lc \"bash -i || { echo; echo failed; read x; }\""`,
		want:    "bash",
	}}

	now := time.Unix(1786450000, 0)
	ds := make([]tmux.Delta, 0, len(cases))
	ls := make(map[string]tmux.Labels, len(cases))
	for i, c := range cases {
		ds = append(ds, tmux.Delta{
			PaneID: c.pane, Activity: now.Unix(),
			PaneHeight: 24, WindowWidth: 80, PanePID: 100 + i,
		})
		ls[c.pane] = tmux.Labels{
			Session: "s" + c.pane, Window: "w", Command: c.current, StartCommand: c.start,
		}
	}

	r := New()
	r.Update("local", ds, ls, nil, nil, now, time.Second)

	rows := map[string]Pane{}
	for _, p := range r.Panes() {
		rows[p.PaneID] = p
	}
	if len(rows) != len(cases) {
		t.Fatalf("Update produced %d rows for %d panes: %v", len(rows), len(cases), rows)
	}
	for _, c := range cases {
		p, ok := rows[c.pane]
		if !ok {
			t.Fatalf("%s (%s): no row at all", c.pane, c.why)
		}
		if p.Command != c.want {
			t.Errorf("%s (%s): the row says %q, want %q\n  current command %q\n  start command  %s",
				c.pane, c.why, p.Command, c.want, c.current, c.start)
		}
		// The PREMISE stays on the row, untouched and still quoted the way tmux quoted it. Anything
		// that answers "why does this row say that" reads this field, and the hide key corroborates
		// a mark with it (internal/hide), so a substitution that also rewrote it would be a second
		// defect wearing this one's clothes.
		if p.StartCommand != c.start {
			t.Errorf("%s: StartCommand was rewritten to %s, want %s", c.pane, p.StartCommand, c.start)
		}
	}
}

// The substitution belongs to PANE rows. An agent row's Command is the listing's own kind, which
// `internal/ui` keys on by string (`p.Command == "background"` gates the wake dialog and the
// lifecycle verbs), so a rule that reached those rows would break a screen rather than fix one.
//
// It cannot reach them today because it lives in Update's pane branch, and this asserts that
// structurally rather than trusting the branch: a pane-less row has no start command to consult.
func TestTheWrapperRuleDoesNotReachAPaneLessRow(t *testing.T) {
	now := time.Unix(1786450000, 0)
	r := New()
	r.UpdateAgents("local", []agents.Session{
		sess("30f3382b", "20260820--wrapped", "background", "/home/dev/lab", "working", now),
	}, now)

	got := r.Panes()
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1: %+v", len(got), got)
	}
	if got[0].Kind != KindAgent {
		t.Fatalf("kind = %q, want %q", got[0].Kind, KindAgent)
	}
	// `background` and not `claude`: the word the listing used, which is what the screens read.
	if got[0].Command != "background" {
		t.Fatalf("a pane-less row says %q, want %q", got[0].Command, "background")
	}
}
