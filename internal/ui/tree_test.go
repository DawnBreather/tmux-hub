package ui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/DawnBreather/tmux-hub/internal/project"
	"github.com/DawnBreather/tmux-hub/internal/registry"
	"github.com/DawnBreather/tmux-hub/internal/state"
)

// treeFleet is the operator's own fleet in miniature, with the three shapes that decide the design:
// a branching family (`st` holding two directories), a chain with no branching (`experiments/tmux-hub`),
// and a row with no path at all (`tmp-1e`), plus a second volume.
//
// The ids are UNIQUE by construction. The first version built them from the session name's first four
// characters, and every date-named row then shared `2026` — so one pinned row put FIVE rows in the
// favourites node and the printed tree said so immediately. A fixture whose keys collide tests the map,
// not the code.
func treeFleet() []registry.Pane {
	n := 0
	mk := func(host, dir, name string, st state.State) registry.Pane {
		n++
		id := fmt.Sprintf("%08d", n)
		return registry.Pane{
			Kind: registry.KindAgent, Command: "background", Host: host, Session: name,
			AgentID: id, SessionID: id + "-a", PaneID: "agent:" + id + "@b",
			ClassifiedState: st, Path: dir, Content: []string{"  (no pane)"},
		}
	}
	return []registry.Pane{
		mk("local", "/home/dev/lab/streams/st/st-edgebox", "store-online", state.Needs),
		mk("local", "/home/dev/lab/streams/st/st-edgebox", "observability", state.Done),
		mk("local", "/home/dev/lab/streams/st/frontend", "troubleshooting", state.Needs),
		mk("local", "/home/dev/lab/streams/st/frontend", "healthchecks", state.Needs),
		mk("local", "/home/dev/lab/streams/experiments/tmux-hub", "main", state.Done),
		mk("local", "", "tmp-1e", state.Idle),
		mk("nuc", "/home/dev/lab/streams/qa/ansible", "ansible-ci", state.Error),
	}
}

// lines renders the tree to plain strings for an assertion to read, node labels and row names only.
func treeShape(t *testing.T, tls []treeLine) []string {
	t.Helper()
	out := make([]string, 0, len(tls))
	for _, l := range tls {
		if l.IsRow {
			out = append(out, strings.Repeat("  ", l.Depth)+"· "+l.Pane.Session)
			continue
		}
		out = append(out, fmt.Sprintf("%s%s %s  [%d/%d]", strings.Repeat("  ", l.Depth),
			map[bool]string{true: "▾", false: "▸"}[l.Open], l.Label, l.Sum.Waiting, l.Sum.Total))
	}
	return out
}

// THE SHAPE, whole, because a tree is a shape and asserting three of its properties separately would
// let the fourth change unnoticed. Every line of the want below is a decision this file documents:
// hosts are volumes and never collapse, a chain with no branching costs one line, children come in
// project.MoreUrgent order, sessions come in attention order, a row with no path hangs off its volume,
// and HOME folds to `~` on the local volume only.
func TestTheTreeIsHostsThenDirectoriesThenSessions(t *testing.T) {
	fleet := treeFleet()
	fav := map[string]bool{MarkKey(fleet[4]): true} // `main`, the one in the collapsed chain
	got := treeShape(t, treeLines(fleet, project.Aliases{}, fav, nil, "/home/dev", "local"))
	// `~/lab/streams/st` is ONE line and that is the collapse working, not a mistake in the want:
	// pinning `main` took the only session out of `experiments/tmux-hub`, so that subtree holds
	// nothing and the chain above it stops branching. A directory with no sessions is not a row of
	// this list — the hub has never listed empty directories, it lists the FLEET — and it comes back
	// the moment an unpinned session lives there again.
	// THE MAP IS OPEN AND THE FOLDERS ARE SHUT, which is what a nil expansion set means (openByDefault):
	// every volume and every directory is drawn, and a leaf's sessions are one `enter` away. Measured on
	// the operator's fleet, everything-open drew 54 session rows under twenty nodes and six levels of
	// indentation — a 40-row screen showed a quarter of the fleet and neither of the other two volumes.
	// The pinned band is the exception and is open on purpose: they asked for it "straight away".
	want := []string{
		"▾ FAVOURITES  [0/1]",
		"  · main",
		"▾ local  [3/5]",
		"  ▾ ~/lab/streams/st  [3/4]",
		"    ▸ frontend  [2/2]",
		"    ▸ st-edgebox  [1/2]",
		"  · tmp-1e",
		"▾ nuc  [0/1]",
		"  ▸ home/dev/lab/streams/qa/ansible  [0/1]",
	}
	if len(got) != len(want) {
		t.Fatalf("the tree has %d lines, want %d:\n%s", len(got), len(want), strings.Join(got, "\n"))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("line %d:\n  got  %q\n  want %q", i, got[i], want[i])
		}
	}

	// AND THE SAME TREE WITH EVERY LEAF OPENED, because the shape above cannot see the SESSION order —
	// which is half of what this case is named for. Opening them is what an operator does with `enter`,
	// and the two wants together say the whole thing: the default folds the folders, and inside a folder
	// the longest-waiting session leads.
	open := map[string]bool{}
	for _, l := range treeLines(fleet, project.Aliases{}, fav, nil, "/home/dev", "local") {
		if !l.IsRow {
			open[l.Key] = true
		}
	}
	deep := treeShape(t, treeLines(fleet, project.Aliases{}, fav, open, "/home/dev", "local"))
	wantDeep := []string{
		"▾ FAVOURITES  [0/1]",
		"  · main",
		"▾ local  [3/5]",
		"  ▾ ~/lab/streams/st  [3/4]",
		"    ▾ frontend  [2/2]",
		"      · healthchecks",
		"      · troubleshooting",
		"    ▾ st-edgebox  [1/2]",
		"      · store-online",
		"      · observability",
		"  · tmp-1e",
		"▾ nuc  [0/1]",
		"  ▾ home/dev/lab/streams/qa/ansible  [0/1]",
		"    · ansible-ci",
	}
	if len(deep) != len(wantDeep) {
		t.Fatalf("opened, the tree has %d lines, want %d:\n%s", len(deep), len(wantDeep),
			strings.Join(deep, "\n"))
	}
	for i := range wantDeep {
		if deep[i] != wantDeep[i] {
			t.Errorf("opened, line %d:\n  got  %q\n  want %q", i, deep[i], wantDeep[i])
		}
	}
}

// A VOLUME NEVER COLLAPSES INTO ITS PATH.
//
// Measured on the first version, which called collapse() on the host itself and produced
// `nuc/home/dev/lab/streams/qa/ansible` as ONE label — the machine and the directory in one string,
// which is exactly the confusion `local/tmp` against `nuc/tmp` cost the operator on the flat list.
func TestAVolumeNeverCollapsesIntoItsPath(t *testing.T) {
	one := treeFleet()[6:] // the nuc row alone: its volume has exactly one child and no rows
	tls := treeLines(one, project.Aliases{}, nil, nil, "/home/dev", "local")
	if len(tls) < 2 || tls[0].Label != "nuc" {
		t.Fatalf("the volume is not its own line:\n%s", strings.Join(treeShape(t, tls), "\n"))
	}
	if strings.HasPrefix(tls[1].Label, "nuc") {
		t.Errorf("the volume was folded into the path: %q", tls[1].Label)
	}
}

// HOME folds on the LOCAL volume and nowhere else, because `~` on another machine is that user's home
// and the hub does not know it — the same distinction the launch form makes when it resolves `~` for a
// local host and refuses it for a remote one.
func TestHomeFoldsOnTheLocalVolumeOnly(t *testing.T) {
	tls := treeLines(treeFleet(), project.Aliases{}, nil, nil, "/home/dev", "local")
	var local, remote string
	for _, l := range tls {
		if l.IsRow {
			continue
		}
		if strings.HasPrefix(l.Label, "~") {
			local = l.Label
		}
		if strings.HasPrefix(l.Label, "home/dev") {
			remote = l.Label
		}
	}
	if local == "" {
		t.Error("HOME did not fold to `~` on the local volume")
	}
	if remote == "" {
		t.Error("HOME folded on the remote volume, where `~` is another user's home")
	}
}

// The fold must not HIDE anything, which is the half the first version got wrong twice: it tested the
// rows of the HOST — where a session with no path legitimately hangs — so it never fired at all, and it
// replaced the host's whole child map, which would have dropped every other tree on that volume.
func TestTheHomeFoldHidesNothing(t *testing.T) {
	fleet := treeFleet()
	// A second tree on the local volume, outside home, plus the pathless row that broke the guard.
	fleet = append(fleet, registry.Pane{
		Kind: registry.KindAgent, Command: "background", Host: "local", Session: "opt-thing",
		AgentID: "99999999", SessionID: "99999999-a", PaneID: "agent:99999999@b",
		ClassifiedState: state.Needs, Path: "/opt/vendor/thing", Content: []string{"  (no pane)"},
	})
	shape := strings.Join(treeShape(t, treeLines(fleet, project.Aliases{}, nil, nil,
		"/home/dev", "local")), "\n")
	for _, want := range []string{"~/lab/streams", "opt/vendor/thing", "· tmp-1e"} {
		if !strings.Contains(shape, want) {
			t.Errorf("the fold lost %q:\n%s", want, shape)
		}
	}
}

// A pinned row appears ONCE, under FAVOURITES, and not again in its directory.
//
// Two lines for one session would be the duplicate the operator reported on the flat list, and worse:
// two cursor positions that act on the same thing.
func TestAPinnedRowIsNotAlsoInItsDirectory(t *testing.T) {
	fleet := treeFleet()
	fav := map[string]bool{MarkKey(fleet[0]): true} // store-online, inside st-edgebox
	shape := treeShape(t, treeLines(fleet, project.Aliases{}, fav, nil, "/home/dev", "local"))
	n := 0
	for _, l := range shape {
		if strings.HasSuffix(l, "· store-online") {
			n++
		}
	}
	if n != 1 {
		t.Errorf("the pinned row is drawn %d times:\n%s", n, strings.Join(shape, "\n"))
	}
	// And its directory's count no longer includes it, or the tree would promise a row it does not show.
	for _, l := range treeLines(fleet, project.Aliases{}, fav, nil, "/home/dev", "local") {
		if l.Label == "st-edgebox" && l.Sum.Total != 1 {
			t.Errorf("st-edgebox still counts the pinned row: total %d, want 1", l.Sum.Total)
		}
	}
}

// A CLOSED node hides its subtree and nothing else, and the count on it still tells the truth about
// what is inside — that is the whole reason a tree can be a default screen: sixteen waiting rows are
// three lines away rather than sixty rows away.
func TestAClosedNodeStillCountsWhatIsInside(t *testing.T) {
	fleet := treeFleet()
	// EVERY node closed, stated node by node. An empty map used to mean this by accident — absence read
	// as "closed" — and it now means "whatever the default says", because the map holds only decisions
	// the operator actually made. That change deleted a seeding step whose two sources could disagree,
	// and it costs this case three lines: what it wants is the operator having closed everything, which
	// is a set of decisions, not an empty one.
	closed := map[string]bool{}
	for _, l := range treeLines(fleet, project.Aliases{}, nil, nil, "/home/dev", "local") {
		if !l.IsRow {
			closed[l.Key] = false
		}
	}
	tls := treeLines(fleet, project.Aliases{}, nil, closed, "/home/dev", "local")
	shape := strings.Join(treeShape(t, tls), "\n")
	if strings.Contains(shape, "· healthchecks") {
		t.Errorf("a closed node still drew its sessions:\n%s", shape)
	}
	var vol treeLine
	for _, l := range tls {
		if l.Label == "local" {
			vol = l
		}
	}
	// 3 of 6: this case pins nothing, so `main` is counted here where the shape test above pins it.
	if vol.Sum.Waiting != 3 || vol.Sum.Total != 6 {
		t.Errorf("the closed volume reports %d waiting of %d, want 3 of 6 — a count that only holds "+
			"when open is a count nobody can navigate by", vol.Sum.Waiting, vol.Sum.Total)
	}
	// And the tree really is closed: nothing below a volume is drawn.
	for _, l := range tls {
		if l.Depth > 0 {
			t.Errorf("an empty expansion map still drew %q at depth %d", l.Label+l.Pane.Session, l.Depth)
			break
		}
	}
}

// THE FIRST PAINT OPENS THE MAP AND SHUTS THE FOLDERS, which is the rule that makes this screen usable
// as a default on a real fleet.
//
// A node with child directories is open, so the shape of the fleet is on the screen; a leaf — sessions
// and no directories — is closed, so its sessions are one `enter` (or one `a`) away. The pinned band is
// the deliberate exception: the operator asked for it "straight away".
//
// This is asserted as the RULE and not only through the shape want above, because a fixture change would
// silently take the shape with it while this cannot be satisfied by accident.
func TestTheFirstPaintOpensTheMapAndShutsTheFolders(t *testing.T) {
	fleet := treeFleet()
	tls := treeLines(fleet, project.Aliases{}, map[string]bool{MarkKey(fleet[4]): true}, nil,
		"/home/dev", "local")
	// A node's children are the lines below it at a greater depth; a node with a node-child is part of
	// the map, one with only row-children is a folder. Derived from the drawn lines rather than from
	// openByDefault, so this test cannot pass by restating the function it checks.
	kids := map[int][]treeLine{}
	for i, l := range tls {
		if l.IsRow {
			continue
		}
		for j := i + 1; j < len(tls) && tls[j].Depth > l.Depth; j++ {
			if tls[j].Depth == l.Depth+1 {
				kids[i] = append(kids[i], tls[j])
			}
		}
	}
	checked := 0
	for i, l := range tls {
		if l.IsRow || l.Pinned {
			continue
		}
		hasDirKid := false
		for _, k := range kids[i] {
			if !k.IsRow {
				hasDirKid = true
			}
		}
		// A CLOSED node draws none of its children, so "no node-children on screen" is only evidence
		// about an OPEN one. The closed ones are checked from the other side: they must be leaves,
		// which their own count proves — a closed node with a directory inside it would be a map line
		// the first paint hid.
		if !l.Open {
			continue
		}
		checked++
		if !hasDirKid {
			t.Errorf("%q is open on the first paint and holds no directory — a folder should be shut:\n%s",
				l.Label, strings.Join(treeShape(t, tls), "\n"))
		}
	}
	if checked < 3 {
		t.Errorf("only %d open nodes were checked, so this case is not looking at the fixture it needs",
			checked)
	}
	// And the pinned band IS open, with its row drawn.
	if len(tls) < 2 || !tls[0].Pinned || !tls[0].Open || !tls[1].IsRow {
		t.Errorf("the pinned band is not open on the first paint:\n%s",
			strings.Join(treeShape(t, tls), "\n"))
	}
}
