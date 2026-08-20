package ui

import (
	"os"
	"strings"
	"testing"

	"github.com/DawnBreather/tmux-hub/internal/lines"
	"github.com/DawnBreather/tmux-hub/internal/project"
	"github.com/DawnBreather/tmux-hub/internal/registry"
	"github.com/DawnBreather/tmux-hub/internal/state"
)

// pinnedFleet is the operator's own pinned band, names and paths measured from their fleet: the four
// favourites whose rows run 73 to 100 columns whole and 65 to 92 with HOME folded.
func pinnedFleet(t *testing.T) ([]registry.Pane, map[string]bool) {
	t.Helper()
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Skip("no home directory, so the `~` fold cannot be exercised")
	}
	mk := func(name, dir string, st state.State) registry.Pane {
		return registry.Pane{
			Kind: registry.KindPane, Host: "local", Session: name, Window: "w",
			PaneID: "%" + name[:1], SessionID: "$1", Command: "claude",
			ClaudeSession: name + "-uuid", AgentName: name,
			Path: home + dir, ClassifiedState: st, Content: []string{"  ❯ "},
		}
	}
	rows := []registry.Pane{
		mk("xmap-universal-reader", "/lab/streams/experiments/xmap-reverse-engineering", state.Needs),
		mk("billing-cicd", "/lab/streams/orbits/billing-iac", state.Needs),
		mk("seedtool-development", "/lab/streams/st/st-edgebox", state.Works),
	}
	fav := map[string]bool{}
	for _, p := range rows {
		fav[MarkKey(p)] = true
	}
	return rows, fav
}

// A PINNED ROW LEADS WITH ITS NAME AND CARRIES ITS ADDRESS.
//
// An unpinned row leads with where it lives, which is right for a fleet the operator is scanning. A
// pinned row is one they chose, so its question is "which one" — the name first, then `@host:path`.
// The path is what tells two favourites of one name in two checkouts apart, which is the case a
// person cannot resolve from the name.
func TestAPinnedRowLeadsWithItsNameAndCarriesItsPath(t *testing.T) {
	rows, fav := pinnedFleet(t)
	home, _ := os.UserHomeDir()

	screen := Render(Frame{Panes: rows, Hosts: hosts2(), Width: 120, Height: 24,
		Aliases: project.Aliases{}, Favourites: fav, Home: home})

	for _, p := range rows {
		row := inboxRow(t, screen, p.AgentName)
		// The name comes BEFORE the address, which is the whole shape.
		iName := strings.Index(row, p.AgentName)
		iAt := strings.Index(row, "@local:")
		if iName < 0 || iAt < 0 || iName > iAt {
			t.Errorf("%s: the row does not read `name @host:path`: %q", p.AgentName, row)
		}
		// HOME is folded, because those eight columns decide whether the path fits at 80.
		if strings.Contains(row, home) {
			t.Errorf("%s: the row spells HOME out instead of `~`: %q", p.AgentName, row)
		}
		tail := p.Path[len(home):]
		if !strings.Contains(row, "~"+tail) {
			t.Errorf("%s: the row does not carry its path as `~%s`: %q", p.AgentName, tail, row)
		}
	}
}

// The PATH yields and the NAME does not, at every width the project speaks to.
//
// Measured on the real favourites: 65 to 92 columns with HOME folded, so one of them cannot fit at
// the 80 §16 commits to and the row has to choose. It chooses the name — this repo has paid three
// times for a layout that kept a label and dropped the thing the operator acts on — and it cuts the
// path from the LEFT, because the last segments are what identify a checkout.
func TestOnANarrowRowThePathYieldsAndTheNameSurvives(t *testing.T) {
	rows, fav := pinnedFleet(t)
	home, _ := os.UserHomeDir()
	long := rows[0] // xmap-universal-reader, the widest of the four
	for _, w := range []int{60, 80, 100, 120, 200} {
		screen := Render(Frame{Panes: rows, Hosts: hosts2(), Width: w, Height: 24,
			Aliases: project.Aliases{}, Favourites: fav, Home: home})
		row := inboxRow(t, screen, long.AgentName)
		if !strings.Contains(row, long.AgentName) {
			t.Errorf("width %d: the NAME was cut: %q", w, row)
		}
		if got := lines.Width(strings.TrimRight(row, " ")); got > w {
			t.Errorf("width %d: the row is %d columns wide: %q", w, got, row)
		}
		// The path is cut from the LEFT and never from the right, at every width: a row that ends in
		// `…` has lost the segment that names the checkout, which is the only part of a path a person
		// reads. Measured before this was fixed: at 60 and 80 the row read `…reverse-engineeri…`,
		// because a hand-counted chrome width made the row one column too wide and the OUTER truncate
		// then took the tail as well.
		if strings.HasSuffix(row, "…") {
			t.Errorf("width %d: the path was cut from the RIGHT, losing the segment that names the "+
				"checkout: %q", w, row)
		}
		// And where there is room for the last segment whole, it is there. At 60 the head alone is
		// 28 columns and the segment 24, so the project makes no promise below 80.
		if w >= 80 && !strings.Contains(row, "xmap-reverse-engineering") {
			t.Errorf("width %d: the last path segment does not fit where it should: %q", w, row)
		}
	}
}

// A pinned row takes NO HEADER, and its unpinned sibling still gets one.
//
// The pinned band is contiguous at the top (favouritesFirst), and these rows carry their own address,
// so a header over them would repeat what the row says — the same argument that took the header off a
// group of one. The half that needs a test is the sibling: a session with one pinned pane and one
// unpinned must not lose the unpinned one's header because the pinned one was counted into its group.
func TestAPinnedRowTakesNoHeaderAndItsSiblingKeepsOne(t *testing.T) {
	home, _ := os.UserHomeDir()
	pin := agentPane("local", "shared", "one", "%1", 1, state.Needs, "pinned")
	pin.Path, pin.AgentName = home+"/lab/streams/st", "the-pinned-one"
	sib := agentPane("local", "shared", "two", "%2", 2, state.Quiet, "sibling")
	sib.Path = home + "/lab/streams/st"
	fav := map[string]bool{MarkKey(pin): true}

	screen := Render(Frame{Panes: []registry.Pane{pin, sib}, Hosts: hosts2(), Width: 120, Height: 24,
		Aliases: project.Aliases{}, Favourites: fav, Home: home})
	pinRow := inboxRow(t, screen, "the-pinned-one")
	if !strings.Contains(pinRow, "@local:") {
		t.Errorf("the pinned row does not carry its address: %q", pinRow)
	}
	// The sibling is now alone in its group, so it says where it lives itself.
	// By `local/shared`, not by the pane id: excluding the pinned row from its group's count leaves
	// the sibling alone in that group, so it is headerless and says its own host — and its label
	// collides with nothing, so rowIdentity keeps the id off it.
	sibRow := inboxRow(t, screen, "local/shared")
	if strings.Contains(sibRow, "@local:") {
		t.Errorf("an UNPINNED row is wearing the pinned shape, so the shape says nothing about "+
			"being pinned: %q", sibRow)
	}
}

// A pinned row with NO path says where it lives rather than inventing an address.
func TestAPinnedRowWithNoPathFallsBackToItsHost(t *testing.T) {
	p := registry.Pane{
		Kind: registry.KindAgent, Host: "nuc", Session: "no-cwd", AgentID: "aaaa1111",
		PaneID: "agent:aaaa1111@bbbb", ClassifiedState: state.Idle, Content: []string{"  (no pane)"},
	}
	fav := map[string]bool{MarkKey(p): true}
	screen := Render(Frame{Panes: []registry.Pane{p}, Hosts: hosts2(), Width: 120, Height: 24,
		Aliases: project.Aliases{}, Favourites: fav})
	row := inboxRow(t, screen, "no-cwd")
	if strings.Contains(row, "@nuc:") {
		t.Errorf("a row with no cwd was given an address anyway: %q", row)
	}
	if !strings.Contains(row, "nuc/no-cwd") {
		t.Errorf("a pinned row with no path does not say where it lives: %q", row)
	}
}
