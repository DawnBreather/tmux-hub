package ui

import (
	"strings"
	"testing"

	"github.com/DawnBreather/tmux-hub/internal/project"
	"github.com/DawnBreather/tmux-hub/internal/registry"
	"github.com/DawnBreather/tmux-hub/internal/state"
)

// A CONVERSATION renders the same whether or not it has a pane, because it is the same thing to
// the operator either way.
//
// Reported from real use, about the pinned band: "они не выглядят переименованными, также они
// выглядят как отдельные проекты". Measured on the operator's own fleet at 150×45 — the four
// favourites drew as four HEADERS with a nameless row under each:
//
//	LOCAL » seedtool-development
//	> ★⚑ needs  %3   sh
//
// The name was on the screen and not on the ROW, and every pinned session had a header of its
// own, which is what "separate projects" looks like. The cause is that the test for "this row's
// name IS the row" was the row's KIND, and the door changes the kind: it makes a tmux session
// called `<name>-<short id>` and the join folds the pane-less row into that pane, so a
// conversation that gains a pane becomes a pane row and inherits the pane row's shape — a
// session header plus `paneID command`. That shape is right for a shell and wrong for a
// conversation, whose name is the only thing that identifies it to a person (§17).
//
// The header itself is not the defect: the comment that skips it for pane-less rows already
// says giving each session a header of its own "made the list half headers". The defect is that
// the skip was keyed on the kind rather than on whether the row is a conversation.
func TestAConversationRendersTheSameWhetherOrNotItHasAPane(t *testing.T) {
	const uuid = "77ef6f5e-2c1e-4a4e-9c4b-1a2b3c4d5e6f"
	const cwd = "/home/dev/lab/streams/st/st-edgebox"
	const claudeName = "20260803--testing-seedtool"

	// The two shapes of ONE conversation, before and after the door.
	before := registry.Pane{Kind: registry.KindAgent, Host: "local", PaneID: "agent:77ef6f5e",
		SessionID: uuid, Session: claudeName, Path: cwd, AgentID: "77ef6f5e",
		ClassifiedState: state.Needs}
	after := registry.Pane{Kind: registry.KindPane, Host: "local", PaneID: "%3", SessionID: "$1",
		Session: claudeName + "-77ef6f5e", Command: "sh", ClaudeSession: uuid,
		AgentName: claudeName, Path: cwd, ClassifiedState: state.Needs}

	var al project.Aliases
	al.Set(project.AliasKeyOf(before), "seedtool-development")

	for _, cols := range []int{80, 100, 150, 200} {
		for _, c := range []struct {
			what string
			row  registry.Pane
		}{{"before the door", before}, {"after the door", after}} {
			out := Render(Frame{Panes: []registry.Pane{c.row}, Hosts: hosts2(),
				Width: cols, Height: 24, Aliases: al})

			// The NAME is on the row the operator's cursor is on — the line carrying the state
			// word — and not merely somewhere on the screen. `Contains` cannot say this: the
			// tile below prints the name too, which is how the defect passed every naming test.
			var stateLine string
			for _, l := range strings.Split(out, "\n") {
				if strings.Contains(l, "needs") && strings.Contains(l, ">") {
					stateLine = l
					break
				}
			}
			if stateLine == "" {
				t.Fatalf("%d cols / %s: no cursor row on screen:\n%s", cols, c.what, out)
			}
			if !strings.Contains(stateLine, "seedtool-development") {
				t.Errorf("%d cols / %s: the row carries no name — %q", cols, c.what,
					strings.TrimRight(stateLine, " "))
			}
			// And it has NO header of its own. A fleet of one conversation draws no host
			// header at all; four of them drew four, which is the "separate projects" report.
			for _, l := range strings.Split(out, "\n") {
				if strings.HasPrefix(l, "LOCAL") {
					t.Errorf("%d cols / %s: a conversation got a header of its own: %q",
						cols, c.what, strings.TrimRight(l, " "))
				}
			}
		}
	}
}

// Dropping a header must LOSE NOTHING, which is the whole argument for dropping it. A lone pane
// that is not a conversation keeps every field the header and the grouped row shape carried
// between them: where it lives — HOST and session, since the header said both — and the command.
//
// The PANE ID is not one of them any more, and that is a decision rather than a loss: a session
// putting ONE row on the screen has nothing for its id to distinguish, and the operator could not
// read the column ("я не понимаю %1, %5, %3"). Measured on their fleet, 49 of 60 rows carried an
// id that distinguished nothing. The sibling test below is the other pole: where the id DOES tell
// two rows apart, it is drawn.
func TestALonePaneKeepsEverythingTheHeaderCarried(t *testing.T) {
	shell := registry.Pane{Kind: registry.KindPane, Host: "local", PaneID: "%0", SessionID: "$1",
		Session: "scratch", Command: "bash", Path: "/w/x", ClassifiedState: state.Quiet}

	out := Render(Frame{Panes: []registry.Pane{shell}, Hosts: hosts2(), Width: 150, Height: 24})
	var row string
	for _, l := range strings.Split(out, "\n") {
		if strings.Contains(l, "quiet") && strings.Contains(l, ">") {
			row = l
		}
	}
	for _, want := range []string{"local/scratch", "bash"} {
		if !strings.Contains(row, want) {
			t.Errorf("a lone pane's row lost %q, which its header used to carry: %q",
				want, strings.TrimRight(row, " "))
		}
	}
	if strings.Contains(row, "%0") {
		t.Errorf("a lone pane's row carries a pane id that distinguishes nothing: %q",
			strings.TrimRight(row, " "))
	}
}

// The CONTROL for the rule, and it is what keeps it from being "never draw headers": a session with
// SIBLINGS keeps its header, and its rows keep the compact shape. That is the case a header is for
// — one name over several rows — and it is the case the count distinguishes.
func TestASessionWithSiblingsKeepsItsHeader(t *testing.T) {
	one := registry.Pane{Kind: registry.KindPane, Host: "local", PaneID: "%0", SessionID: "$1",
		Session: "scratch", Command: "bash", Path: "/w/x", ClassifiedState: state.Quiet}
	two := one
	two.PaneID, two.Command, two.ClassifiedState = "%1", "vim", state.Idle

	out := Render(Frame{Panes: []registry.Pane{one, two}, Hosts: hosts2(), Width: 150, Height: 24})
	if !strings.Contains(out, "LOCAL SCRATCH") {
		t.Errorf("a session with two panes lost its header:\n%s", out)
	}
	// And exactly ONE header for the two rows, which is what a header buys.
	if n := strings.Count(out, "LOCAL SCRATCH"); n != 1 {
		t.Errorf("%d headers for one session of two panes", n)
	}
	// THE OTHER POLE of the pane-id rule: these two rows share a session, a host and a header, so
	// the id is the only thing that tells them apart and it must be drawn. Without this cell the
	// rule "show it only when it disambiguates" is indistinguishable from "never show it".
	for _, want := range []string{"%0", "%1"} {
		if !strings.Contains(out, want) {
			t.Errorf("two panes of ONE session and %s is not on the screen — nothing tells the "+
				"two rows apart:\n%s", want, out)
		}
	}
}

// A conversation shows its PANE ID exactly when the id says something: with a SIBLING in the same
// session it is the only thing telling the two rows apart, and alone it is a column the operator
// cannot read. Both poles in one test, on one fleet, so neither can be satisfied by construction.
func TestAConversationShowsItsPaneIDOnlyWhenItTellsRowsApart(t *testing.T) {
	row := registry.Pane{Kind: registry.KindPane, Host: "local", PaneID: "%3", SessionID: "$1",
		Session: "n-77ef6f5e", Command: "sh", ClaudeSession: "77ef6f5e-2c1e", AgentName: "n",
		Path: "/w/x", ClassifiedState: state.Needs}
	lineWith := func(out, word string) string {
		var line string
		for _, l := range strings.Split(out, "\n") {
			if strings.Contains(l, word) && strings.Contains(l, ">") {
				line = l
			}
		}
		return strings.TrimRight(line, " ")
	}

	alone := Render(Frame{Panes: []registry.Pane{row}, Hosts: hosts2(), Width: 150, Height: 24})
	if got := lineWith(alone, "needs"); strings.Contains(got, "%3") {
		t.Errorf("a conversation alone in its session carries an id that distinguishes nothing: %q", got)
	}

	sibling := row
	sibling.PaneID, sibling.ClaudeSession, sibling.ClassifiedState = "%4", "77ef6f5e-9999", state.Quiet
	shared := Render(Frame{Panes: []registry.Pane{row, sibling}, Hosts: hosts2(), Width: 150, Height: 24})
	for _, want := range []string{"%3", "%4"} {
		if !strings.Contains(shared, want) {
			t.Errorf("two conversations in ONE session and %s is missing, so the rows are "+
				"indistinguishable:\n%s", want, shared)
		}
	}
}

// In the PROJECT view the header is the project and gathers many rows, so it stays — but the row
// must still say which conversation it is, or the same defect appears one view along.
func TestAConversationCarriesItsNameInTheProjectViewToo(t *testing.T) {
	const uuid = "30f3382b-1111-2222-3333-444455556666"
	row := registry.Pane{Kind: registry.KindPane, Host: "local", PaneID: "%6", SessionID: "$2",
		Session: "20260817-cicd-30f3382b", Command: "sh", ClaudeSession: uuid,
		AgentName: "20260817-cicd", Path: "/w/billing-iac", ClassifiedState: state.Needs}
	var al project.Aliases
	al.Set(project.AliasKeyOf(row), "billing-cicd")

	out := Render(Frame{Panes: []registry.Pane{row}, Hosts: hosts2(), Width: 150, Height: 24,
		Aliases: al, GroupBy: ByProject,
		Groups: map[string]string{MarkKey(row): "billing-iac"}})
	if !strings.Contains(out, "BILLING-IAC") {
		t.Errorf("the project view lost its project header:\n%s", out)
	}
	var line string
	for _, l := range strings.Split(out, "\n") {
		if strings.Contains(l, "needs") && strings.Contains(l, ">") {
			line = l
		}
	}
	if !strings.Contains(line, "billing-cicd") {
		t.Errorf("the project view's row carries no name: %q", strings.TrimRight(line, " "))
	}
}
