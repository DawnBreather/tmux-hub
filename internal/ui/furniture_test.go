package ui

import (
	"strings"
	"testing"

	"github.com/DawnBreather/tmux-hub/internal/project"
	"github.com/DawnBreather/tmux-hub/internal/registry"
	"github.com/DawnBreather/tmux-hub/internal/state"
)

// attachStart is #{pane_start_command} for a window `a` opened, COPIED BYTE FOR BYTE from the
// operator's own server rather than written from imagination: tmux word-quotes this value, so the
// words `tmux` and `attach` are never adjacent in it and a hand-typed fixture would not have that
// shape — the same trap that once made a grep over this field return zero on a fleet with eight
// such panes. Read out of `list-panes -F '#{pane_start_command}'` and escaped for Go.
const attachStart = "\"'sh' '-c' ''\\\\''ssh'\\\\'' '\\\\''-S'\\\\'' '\\\\''/run/user/1000/tmux-hub/cm-f6032288-nuc'\\\\'' '\\\\''-t'\\\\'' '\\\\''nuc'\\\\'' '\\\\''tmux'\\\\'' '\\\\''attach'\\\\'' '\\\\''-t'\\\\'' '\\\\'''\\\\''\\\\'\\\\'''\\\\''$0'\\\\''\\\\'\\\\'''\\\\'''\\\\''; s=$?; [ \\\"\\$s\\\" -eq 0 ] || { printf '\\\\''\\\\n[tmux-hub] the attach exited %s \u2014 press enter to close this window\\\\n'\\\\'' \\\"\\$s\\\"; read _; }'\""

// hubFurnitureFleet is the operator's own screen, reduced to the five rows they could not place:
// two hub panes in two numbered sessions, three attach windows the hub opened, and one real row on
// the far host that the attaches are looking AT.
func hubFurnitureFleet() []registry.Pane {
	far := agentPane("nuc", "tmux-hub-demo", "logs", "%1", 1, state.Quiet, "the session an attach shows")
	hubA := toolPane("local", "0", "tmux-hub", "%0", "tmux-hub", 0, state.Works, "the hub")
	// The attach: a pane in the hub's own session, in a window the hub NAMED after the row it
	// shows. Its command is the `sh` that wraps the ssh, which is why the row said nothing.
	attachA := toolPane("local", "0", "nuc/tmux-hub-demo", "%24", "sh", 1, state.Works, "ssh")
	attachA.StartCommand = attachStart
	hubB := toolPane("local", "15", "tmux-hub", "%21", "tmux-hub", 0, state.Works, "a second hub")
	attachB := toolPane("local", "15", "nuc/tmux-hub-demo", "%22", "sh", 1, state.Works, "ssh")
	attachB.StartCommand = attachStart
	return []registry.Pane{far, hubA, attachA, hubB, attachB}
}

func TestOwnFurnitureIsTheHubAndTheWindowsItOpened(t *testing.T) {
	fleet := hubFurnitureFleet()
	own := ownFurniture(fleet, project.Aliases{}, "tmux-hub")
	if len(own) != 4 {
		var got []string
		for _, p := range fleet {
			if own[MarkKey(p)] {
				got = append(got, p.PaneID)
			}
		}
		t.Fatalf("expected the two hub panes and the two attach windows, got %d: %v", len(own), got)
	}
	for _, p := range fleet {
		want := p.Command == "tmux-hub" || p.Window == "nuc/tmux-hub-demo" && p.Host == "local"
		if own[MarkKey(p)] != want {
			t.Errorf("%s (%s/%s window %q command %q): dropped=%v, want %v",
				p.PaneID, p.Host, p.Session, p.Window, p.Command, own[MarkKey(p)], want)
		}
	}
	// The row the attaches are looking at STAYS. It is the only one of the five that the operator
	// can do anything with, and hiding it would turn a tidying fix into a lost session.
	if own[MarkKey(fleet[0])] {
		t.Error("the far row the attach windows show was dropped, which is the session itself")
	}
}

// The two clauses of the attach rule, each shown to be load-bearing by removing what it tests.
// Without the session clause an operator's own `nuc/api` window disappears; without the name clause
// every window in the hub's session disappears, including work they put there themselves.
func TestOwnFurnitureKeepsRowsThatOnlyHalfMatch(t *testing.T) {
	far := agentPane("nuc", "tmux-hub-demo", "logs", "%1", 1, state.Quiet, "far")
	hub := toolPane("local", "0", "tmux-hub", "%0", "tmux-hub", 0, state.Works, "the hub")
	// Same window NAME as an attach, but not in a session the hub lives in: the operator named
	// this window themselves.
	elsewhere := agentPane("local", "myproject", "nuc/tmux-hub-demo", "%30", 3, state.Quiet, "mine")
	// In the hub's session, but a window the hub would never have named: `C-b c` in the hub's
	// session is a normal thing to do.
	beside := agentPane("local", "0", "notes", "%31", 2, state.Quiet, "mine too")

	own := ownFurniture([]registry.Pane{far, hub, elsewhere, beside}, project.Aliases{}, "tmux-hub")
	if own[MarkKey(elsewhere)] {
		t.Error("a window the OPERATOR named like an attach was dropped, outside any hub session")
	}
	if own[MarkKey(beside)] {
		t.Error("a window the operator opened BESIDE the hub was dropped")
	}
	if !own[MarkKey(hub)] {
		t.Error("the hub pane itself was kept")
	}
}

// No name to compare against means no row is dropped. The failure direction matters: a row the
// operator cannot act on is noise, a row that vanishes is a session they think is gone.
func TestOwnFurnitureDropsNothingWithoutAName(t *testing.T) {
	if own := ownFurniture(hubFurnitureFleet(), project.Aliases{}, ""); len(own) != 0 {
		t.Errorf("dropped %d rows with no self name to compare against", len(own))
	}
}

// The screen, which is the claim: the five rows are gone AND the header says why. A count that
// silently disagrees with `tmux ls` would trade one confusion for another.
func TestTheHeaderSaysHowManyOfItsOwnWindowsAreNotListed(t *testing.T) {
	fleet := hubFurnitureFleet()
	m := base(t, 120, 24, fleet...)
	m.self = "tmux-hub"
	m.setFleet(fleet)

	if len(m.panes) != 1 {
		t.Fatalf("the fleet holds %d rows, want 1 (the far session)", len(m.panes))
	}
	screen := m.View()
	head := strings.SplitN(screen, "\n", 2)[0]
	if !strings.Contains(head, "1 session") {
		t.Errorf("the header does not count the one real session: %q", head)
	}
	if !strings.Contains(head, "4 windows of its own not listed") {
		t.Errorf("the header does not explain its count: %q", head)
	}
	// And none of the four is on the screen under any name.
	for _, gone := range []string{"%0", "%24", "%21", "%22"} {
		if strings.Contains(screen, gone) {
			t.Errorf("the hub's own pane %s is still a row:\n%s", gone, screen)
		}
	}
}

// A window that only LOOKS like an attach keeps its row.
//
// This is the finding an adversarial review earned, and it is a row the operator would have lost in
// silence: `nuc/api` is a real pane on nuc, so the hub would name an attach window for it `nuc/api`,
// and an operator who has their own window of that name inside the session the hub runs in matched
// both of the first two clauses. The pane's own start command is what settles it — a Claude session
// the operator opened was never started by `ssh … tmux attach`.
func TestOwnFurnitureKeepsAWindowThatOnlyLooksLikeAnAttach(t *testing.T) {
	far := agentPane("nuc", "api", "logs", "%1", 1, state.Quiet, "the row whose name the hub would use")
	hub := toolPane("local", "0", "tmux-hub", "%0", "tmux-hub", 0, state.Works, "the hub")
	// Same name, same session as the hub, but the operator started it themselves.
	mine := agentPane("local", "0", "nuc/api", "%9", 9, state.Needs, "my own claude")
	mine.StartCommand = `"claude"`
	// And the real thing beside it, so the case cannot pass by the rule having stopped working.
	real := toolPane("local", "0", "nuc/api", "%8", "sh", 8, state.Works, "ssh")
	real.StartCommand = attachStart

	own := ownFurniture([]registry.Pane{far, hub, mine, real}, project.Aliases{}, "tmux-hub")
	if own[MarkKey(mine)] {
		t.Error("a window the OPERATOR started, named like an attach and sitting beside the hub, " +
			"was dropped from the fleet — the name proves only that the hub COULD have chosen it")
	}
	if !own[MarkKey(real)] {
		t.Error("the real attach window was kept, so this case proves nothing about the clause")
	}
	if !own[MarkKey(hub)] {
		t.Error("the hub pane itself was kept")
	}
	if own[MarkKey(far)] {
		t.Error("the far row the attach shows was dropped")
	}
}

// looksLikeAnAttach reads the field as tmux WRITES it, and the order matters.
func TestLooksLikeAnAttachReadsTmuxOwnQuoting(t *testing.T) {
	for _, c := range []struct {
		name  string
		start string
		want  bool
	}{
		{"the real thing, word-quoted by tmux", attachStart, true},
		{"nothing recorded", "", false},
		{"a plain claude pane", `"claude"`, false},
		{"the door's payload, which is not a tmux attach", `"claude" "attach" "30f3382b"`, false},
		// A path can contain the word and must not qualify on its own.
		{"a path that says attach", `"vim" "/home/dev/notes/attach.md"`, false},
		{"attach before tmux, which is not the shape", `"attach" "then" "tmux"`, false},
		{"unquoted, two argv words", `tmux attach -t $0`, true},
	} {
		if got := looksLikeAnAttach(c.start); got != c.want {
			t.Errorf("%s: looksLikeAnAttach(%q) = %v, want %v", c.name, c.start, got, c.want)
		}
	}
}
