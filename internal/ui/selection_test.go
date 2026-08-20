package ui

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/DawnBreather/tmux-hub/internal/registry"
	"github.com/DawnBreather/tmux-hub/internal/state"
)

// Tags SELECT; a tag is never itself a target. So the selection is always a set
// of concrete panes, and expanding a tag adds its members rather than storing the
// tag — otherwise a pane that joined the tag after the fact becomes a target
// nobody chose.
func TestSelectionHoldsPanesNotGroups(t *testing.T) {
	var s Selection
	s.Toggle(SelectionKey{"local", "%0"})
	s.Toggle(SelectionKey{"nuc", "%3"})
	if s.Len() != 2 {
		t.Fatalf("Len = %d, want 2", s.Len())
	}
	if !s.Has(SelectionKey{"nuc", "%3"}) {
		t.Error("Has lost a member")
	}
	// A pane id is only unique within a server, so the host must be part of the key.
	s.Toggle(SelectionKey{"nuc", "%0"})
	if !s.Has(SelectionKey{"local", "%0"}) || !s.Has(SelectionKey{"nuc", "%0"}) {
		t.Error("two hosts' %0 collided — the key must include the host")
	}
}

func TestToggleIsIdempotentPairwise(t *testing.T) {
	var s Selection
	k := SelectionKey{"local", "%1"}
	s.Toggle(k)
	s.Toggle(k)
	if s.Has(k) || s.Len() != 0 {
		t.Errorf("Toggle twice left %d members", s.Len())
	}
}

func TestClearEmptiesEverything(t *testing.T) {
	var s Selection
	s.Toggle(SelectionKey{"a", "%0"})
	s.Toggle(SelectionKey{"b", "%1"})
	s.Clear()
	if s.Len() != 0 {
		t.Errorf("Clear left %d members", s.Len())
	}
}

// Members must be in a stable order, or the confirmation dialog reshuffles under
// the user between one look and the next.
func TestMembersAreOrdered(t *testing.T) {
	var s Selection
	for _, k := range []SelectionKey{{"nuc", "%9"}, {"local", "%2"}, {"nuc", "%1"}} {
		s.Toggle(k)
	}
	first := s.Members()
	for i := 0; i < 8; i++ {
		got := s.Members()
		for j := range got {
			if got[j] != first[j] {
				t.Fatalf("Members reordered between calls: %v vs %v", first, got)
			}
		}
	}
}

// A pane that vanished must leave the selection, or it stays a target forever and
// every send has to ask about it.
func TestPruneDropsVanishedPanes(t *testing.T) {
	var s Selection
	s.Toggle(SelectionKey{"local", "%0"})
	s.Toggle(SelectionKey{"local", "%1"})
	s.Prune(func(k SelectionKey) bool { return k.PaneID == "%0" })
	if s.Len() != 1 || !s.Has(SelectionKey{"local", "%0"}) {
		t.Errorf("Prune kept %v", s.Members())
	}
}

// The composer must keep a newline as text. This is where the measured accident
// lives: a newline that reaches tmux as a keypress submits the first paragraph.
func TestComposerKeepsNewlinesAsText(t *testing.T) {
	var c Composer
	c.Insert("line one")
	c.Newline()
	c.Insert("line two")
	if got := c.Text(); got != "line one\nline two" {
		t.Errorf("Text() = %q", got)
	}
	if strings.Contains(c.Text(), "\r") {
		t.Error("a CR got in; -r on paste-buffer exists to stop exactly that")
	}
}

func TestComposerBackspaceAndClear(t *testing.T) {
	var c Composer
	c.Insert("abc")
	c.Backspace()
	if c.Text() != "ab" {
		t.Errorf("Text() = %q", c.Text())
	}
	c.Backspace()
	c.Backspace()
	c.Backspace() // one too many
	if c.Text() != "" {
		t.Errorf("Text() = %q, want empty", c.Text())
	}
	c.Insert("x")
	c.Clear()
	if c.Text() != "" || !c.Empty() {
		t.Error("Clear left something behind")
	}
}

// Backspace must not split a multi-byte character, which would send invalid UTF-8
// into someone's prompt.
func TestComposerBackspaceIsRuneWise(t *testing.T) {
	var c Composer
	c.Insert("привет")
	c.Backspace()
	if c.Text() != "приве" {
		t.Errorf("Text() = %q, want %q", c.Text(), "приве")
	}
}

// `A` marks every row ON THE SCREEN and no other — asserted against the screen the same model
// draws, not against the arithmetic that decides it.
//
// This is the property in the operator's terms, and it is the one that broke: the viewport used to
// ESTIMATE the rows a grouped list spends on session headers at one per two panes, deliberately
// conservative on the argument that showing one row fewer is invisible. Then §16's group-of-one
// rule made a header the exception — measured on a fleet of 45 single-pane sessions, the renderer
// drew 25 rows and `A` marked 12 — so the operator selected half of what they could see and sent
// to it. Going through Update and View means a Frame field the arithmetic reads and the key's own
// frame forgets to set cannot hide here.
func TestSelectAllMarksEveryRowOnTheScreenAndNoOther(t *testing.T) {
	// One pane per session, which is what the door produces: no row takes a header.
	var rows []registry.Pane
	for i := 0; i < 45; i++ {
		rows = append(rows, registry.Pane{Kind: registry.KindPane, Host: "local",
			PaneID: fmt.Sprintf("%%%03d", i), SessionID: fmt.Sprintf("$%d", i),
			Session: fmt.Sprintf("s%03d", i), Command: "sh", Path: "/w/x",
			ClassifiedState: state.Idle})
	}
	for _, cols := range []int{80, 100, 160, 200} {
		t.Run(fmt.Sprintf("%d columns", cols), func(t *testing.T) {
			m := base(t, cols, 40, rows...)
			screen := m.View()
			drawn := map[string]bool{}
			for _, p := range rows {
				// By the needle the row DRAWS, not by the pane id: rowPaneID keeps the id off a
				// row whose session puts one up, and this counter then reported one row drawn
				// out of a fixture of many, failing its own floor.
				if drawsRow(screen, rowNeedle(p, rows)) {
					drawn[p.PaneID] = true
				}
			}
			if len(drawn) < 2 {
				t.Fatalf("the fixture draws %d rows, so nothing is being tested", len(drawn))
			}

			out, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("A")})
			after := out.(model)
			marked := map[string]bool{}
			for _, p := range rows {
				if after.sel.Has(selKey(p)) {
					marked[p.PaneID] = true
				}
			}
			if len(marked) != len(drawn) {
				t.Errorf("the screen drew %d rows and `A` marked %d — the operator marked what "+
					"they could not see, or missed what they could", len(drawn), len(marked))
			}
			for id := range marked {
				if !drawn[id] {
					t.Errorf("`A` marked %s, which is not on the screen", id)
				}
			}
			for id := range drawn {
				if !marked[id] {
					t.Errorf("%s is on the screen and `A` did not mark it", id)
				}
			}
		})
	}
}
