//go:build e2e

package e2e

import (
	"strings"
	"testing"
	"time"
)

// `t` opens the filesystem view on the real binary, and `esc` leaves it.
//
// What only the real binary can show: that `t` reaches the key router at all in browse mode, that the
// tree paints inside the same chrome as the dashboard, and that the way out works — a screen with no
// way out traps the operator, which is why the history view has a case of its own for exactly this.
//
// Every key is sent on its own, because `send-keys -l "t"` arrives as ONE key message whose String() is
// the whole string: a test that types a word cannot tell a router that took `t` as a command from one
// that took it as text.
func TestE2EUITreeOpensWithTAndLeavesWithEsc(t *testing.T) {
	ui, _, ids, _ := hubWith(t, 120, 40, 2, "sleep 300")

	// The dashboard first, so the tree's arrival is a CHANGE rather than the first thing seen.
	screenHas(t, ui, "sessions", "the dashboard must paint before the view is switched")

	send(t, ui, "t")
	screenHas(t, ui, "esc leaves", "`t` must open the filesystem view and say how to leave it")
	s := capturePane(t, ui, "ui")
	// A volume line for the watched host, with the trailing slash `ls -F` gives a directory.
	if !strings.Contains(s, "scratch/") {
		t.Errorf("the tree does not show the watched host as a volume:\n%s", s)
	}
	// The fleet footer survives the view change: a change of screen must not cost the operator the
	// one positive statement about host health.
	if !strings.Contains(s, "scratch up") {
		t.Errorf("the tree lost the fleet footer:\n%s", s)
	}
	// And the sessions are in there somewhere, by the id their rows carry — two panes of one session,
	// so rowIdentity draws both ids.
	for _, id := range ids {
		if !strings.Contains(s, id) {
			t.Errorf("the tree does not show the watched pane %s:\n%s", id, s)
		}
	}

	// h closes the volume, which is the key that was silently doing nothing when the volume was
	// forced open — the defect a unit test found and this case would have found next.
	send(t, ui, "h")
	waitUntil(t, "the volume to close", 10*time.Second, func() bool {
		c, err := paneScreen(t, ui, "ui")
		return err == nil && !strings.Contains(c, ids[0])
	})

	send(t, ui, "Escape")
	waitUntil(t, "esc to return to the dashboard", 10*time.Second, func() bool {
		c, err := paneScreen(t, ui, "ui")
		return err == nil && strings.Contains(c, "tmux-hub  ") && !strings.Contains(c, "asking")
	})
}

// THE HUB OPENS ON THE FILESYSTEM VIEW, which is what the operator asked for.
//
// Every other case in this suite passes `--view=flat`, because two hundred of them are written against
// the attention-ordered list — so this is the one case that launches with no flag at all and therefore
// the only one that can tell what the DEFAULT is. Without it the flag would be tested and the default
// would not, which is the shape of a claim nothing asserts.
func TestE2EUITheHubOpensOnTheFilesystemView(t *testing.T) {
	ui, _, _, _ := hubWithView(t, 120, 40, 2, "sleep 300", "")

	// The tree's own title, which the flat list does not have.
	screenHas(t, ui, "asking", "the hub must open on the filesystem view by default")
	screenHas(t, ui, "esc leaves", "and it must say how to leave it")
	s := capturePane(t, ui, "ui")
	if !strings.Contains(s, "scratch/") {
		t.Errorf("the opening screen shows no volume, so it is not the tree:\n%s", s)
	}

	// And `--view=flat` really does the other thing, or the flag is decoration.
	flat, _, _, _ := hubWithView(t, 120, 40, 2, "sleep 300", "flat")
	screenHas(t, flat, "tmux-hub  ", "`--view=flat` must open the attention-ordered list")
	if fs := capturePane(t, flat, "ui"); strings.Contains(fs, "esc leaves") {
		t.Errorf("`--view=flat` opened the tree anyway:\n%s", fs)
	}
}

// `q` quits from the tree with a clean exit, because the tree is the default screen and an operator
// must be able to quit from it without knowing `esc` first.
//
// §23.2 specifies this: "`q` quits from here, and that is not optional." A `q` that did nothing would
// leave the operator with no way out of the program.
func TestE2EUITreeQQuits(t *testing.T) {
	ui, _, _, _ := hubWithView(t, 120, 40, 2, "sleep 300", "")

	screenHas(t, ui, "asking", "the hub must open on the tree by default")
	send(t, ui, "q")
	screenHas(t, ui, "EXITED-rc=0", "`q` from the tree must quit cleanly")
}

// `t` toggles between the tree and the dashboard in both directions, and the two screens are
// distinguishable by what only one of them can print.
//
// The tree has "asking" (the census) and "esc leaves"; the dashboard has "tmux-hub  " (the grouped
// header with trailing spaces) and no "esc leaves". This asserts that `t` reaches the mode dispatch.
func TestE2EUITreeTTogglesBothWays(t *testing.T) {
	ui, _, _, _ := hubWith(t, 120, 40, 2, "sleep 300")

	// Start on the dashboard with --view=flat.
	screenHas(t, ui, "tmux-hub  ", "must start on the dashboard with --view=flat")

	// Toggle to the tree.
	send(t, ui, "t")
	screenHas(t, ui, "asking", "`t` from the dashboard must open the tree")
	s := capturePane(t, ui, "ui")
	if strings.Contains(s, "tmux-hub  ") {
		t.Errorf("`t` did not leave the dashboard:\n%s", s)
	}

	// Toggle back to the dashboard.
	send(t, ui, "t")
	screenHas(t, ui, "tmux-hub  ", "`t` from the tree must return to the dashboard")
	s = capturePane(t, ui, "ui")
	if strings.Contains(s, "esc leaves") {
		t.Errorf("`t` did not leave the tree:\n%s", s)
	}
}

// A CLOSED directory still shows what is inside: the tile at the bottom names sessions whose rows are
// not drawn, and the directory line shows a count.
//
// This is §23's claim that makes the tree viable as default: with leaf directories closed by default,
// the operator can see what is where without opening everything. The tile shows session names for a
// closed directory, which no other arrangement gives.
func TestE2EUITreeClosedDirectoryShowsContents(t *testing.T) {
	ui, _, _, _ := hubWithView(t, 120, 40, 2, "sleep 300", "")

	screenHas(t, ui, "asking", "the hub must open on the tree")

	// The tree opens with directories closed (the new default: map open, folders shut).
	// Look for a closed directory marker ▸ with a count.
	s := capturePane(t, ui, "ui")
	if !strings.Contains(s, "▸") {
		t.Errorf("the tree shows no closed directories (no ▸ marker):\n%s", s)
	}
	if !strings.Contains(s, "of") {
		t.Errorf("the tree shows no counts on directories:\n%s", s)
	}

	// The tile at the bottom must name sessions inside the directory under the cursor.
	// The tile shows "N sessions" and session names for a directory.
	if !strings.Contains(s, "session") {
		t.Errorf("the tile does not name sessions:\n%s", s)
	}
}

// `enter` on a closed directory opens it, revealing sessions, and `j` moves onto a session row.
//
// What only the real terminal can show: that the navigation reaches a session row, and that the
// tile changes from a directory summary to a pane tile when the cursor lands on a session. This is
// the path an operator takes when `a` says nothing is waiting.
func TestE2EUITreeEnterThenJLandsOnSession(t *testing.T) {
	ui, _, _, _ := hubWithView(t, 120, 40, 2, "sleep 300", "")

	screenHas(t, ui, "asking", "the hub must open on the tree")

	// Walk to a closed directory (▸ with count).
	for i := 0; i < 30; i++ {
		row := cursorRow(t, ui)
		if strings.Contains(row, "▸") && strings.Contains(row, "of") {
			break
		}
		send(t, ui, "j")
		time.Sleep(80 * time.Millisecond)
	}

	// Press `enter` to open it.
	send(t, ui, "Enter")
	time.Sleep(400 * time.Millisecond)

	// Walk down to land on a session row.
	send(t, ui, "j")
	time.Sleep(200 * time.Millisecond)

	// The tile should now show a pane tile (starts with "┌─" and the host name).
	s := capturePane(t, ui, "ui")
	if !strings.Contains(s, "┌─") {
		t.Errorf("the cursor did not land on a session (no pane tile):\n%s", s)
	}
}

// `a` on a directory with no waiting sessions says so, rather than moving silently or doing nothing.
//
// This fixture's panes run `sleep 300`, so they classify as quiet/works, never needs. `a` on a node
// goes to the longest-WAITING session, so the product correctly refuses and says why. A key that
// silently does nothing is what a broken key looks like.
func TestE2EUITreeAOnDirectoryWithNothingWaitingSaysSo(t *testing.T) {
	ui, _, _, _ := hubWithView(t, 120, 40, 2, "sleep 300", "")

	screenHas(t, ui, "asking", "the hub must open on the tree")

	// Walk to a closed directory (▸ with count).
	for i := 0; i < 30; i++ {
		row := cursorRow(t, ui)
		if strings.Contains(row, "▸") && strings.Contains(row, "of") {
			break
		}
		send(t, ui, "j")
		time.Sleep(80 * time.Millisecond)
	}

	// Press `a` - it should refuse with a reason.
	send(t, ui, "a")
	time.Sleep(300 * time.Millisecond)

	// The footer must say nothing is waiting.
	s := capturePane(t, ui, "ui")
	if !strings.Contains(s, "nothing") || !strings.Contains(s, "waiting") {
		t.Errorf("`a` did not say nothing is waiting:\n%s", s)
	}
}

// `enter` on a directory opens it if closed, closes it if open, revealing and hiding the sessions inside.
//
// What only the real terminal can show: that the key reaches the router in tree mode, and that the
// directory's expansion state toggles. With the new default (map open, folders closed), sessions
// appear only after opening.
func TestE2EUITreeEnterTogglesDirectory(t *testing.T) {
	ui, _, _, _ := hubWithView(t, 120, 40, 2, "sleep 300", "")

	screenHas(t, ui, "asking", "the hub must open on the tree")

	// Walk to find a closed directory (▸) with sessions inside.
	for i := 0; i < 30; i++ {
		row := cursorRow(t, ui)
		if strings.Contains(row, "▸") && strings.Contains(row, "of") {
			break
		}
		send(t, ui, "j")
		time.Sleep(80 * time.Millisecond)
	}

	row := cursorRow(t, ui)
	if !strings.Contains(row, "▸") {
		t.Skip("no closed directories found to test toggle")
	}

	// Press `enter` to open it.
	send(t, ui, "Enter")
	time.Sleep(400 * time.Millisecond)

	// The directory should now be open (▾).
	row = cursorRow(t, ui)
	if !strings.Contains(row, "▾") {
		t.Errorf("`enter` did not open the directory:\n%s", row)
	}

	// Press `enter` again to close it.
	send(t, ui, "Enter")
	time.Sleep(400 * time.Millisecond)

	// The directory should be closed (▸) again.
	row = cursorRow(t, ui)
	if !strings.Contains(row, "▸") {
		t.Errorf("`enter` did not close the directory back:\n%s", row)
	}
}

// `/` opens the keyword field and narrows the tree, `esc` closes the field and restores the full tree,
// and the footer carries the query and the count while the field is open.
//
// The keyword field is painted by the tree's own footer from Frame.Searching, not by a separate renderer,
// so this asserts that the field can reach the tree at all — a layout defect could leave the backdrop
// frozen while the field takes input.
func TestE2EUITreeKeywordNarrowsAndRestores(t *testing.T) {
	ui, _, _, _ := hubWithView(t, 120, 40, 2, "sleep 300", "")

	screenHas(t, ui, "asking", "the hub must open on the tree")

	// Open the keyword field and type a query that should match the test sessions.
	send(t, ui, "/")
	time.Sleep(200 * time.Millisecond)
	sendLiteral(t, ui, "sleep")
	time.Sleep(300 * time.Millisecond)

	// The footer must show the query and a count.
	s := capturePane(t, ui, "ui")
	if !strings.Contains(s, "sleep") {
		t.Errorf("the footer does not show the query:\n%s", s)
	}
	if !strings.Contains(s, "of") {
		t.Errorf("the footer does not show a count:\n%s", s)
	}

	// `esc` closes the field.
	send(t, ui, "Escape")
	time.Sleep(300 * time.Millisecond)
	s = capturePane(t, ui, "ui")
	// The query is gone from the footer (no quotes around it).
	if strings.Contains(s, "\"sleep\"") {
		t.Errorf("the filter field did not close:\n%s", s)
	}
	// The tree is restored - the volume line should still be there.
	if !strings.Contains(s, "scratch/") {
		t.Errorf("the tree was not restored after filter closed:\n%s", s)
	}
}

// `space` then `i` opens the composer with the tree as its backdrop, and `esc` returns to the tree
// rather than to the dashboard.
//
// This is §23.1: an overlay raised from the tree must return to the tree, and the backdrop must show
// a tree line. Without this the operator would be teleported to the dashboard with nothing on screen
// saying why.
func TestE2EUITreeComposeReturnsToTree(t *testing.T) {
	ui, _, _, _ := hubWithView(t, 120, 40, 2, "sleep 300", "")

	screenHas(t, ui, "asking", "the hub must open on the tree")

	// Walk to a closed directory, open it with `enter`, then walk down to a session.
	for i := 0; i < 30; i++ {
		row := cursorRow(t, ui)
		if strings.Contains(row, "▸") && strings.Contains(row, "of") {
			send(t, ui, "Enter")
			time.Sleep(400 * time.Millisecond)
			send(t, ui, "j")
			time.Sleep(200 * time.Millisecond)
			break
		}
		send(t, ui, "j")
		time.Sleep(80 * time.Millisecond)
	}

	// Check we're on a session row (pane tile visible).
	s := capturePane(t, ui, "ui")
	if !strings.Contains(s, "┌─") {
		t.Skip("did not land on a session row (no pane tile visible)")
	}

	// Mark the session and open the composer.
	send(t, ui, "space")
	time.Sleep(200 * time.Millisecond)
	send(t, ui, "i")
	screenHas(t, ui, "enter: send", "`i` must open the composer")

	// The backdrop must show a tree line — a directory or volume with a trailing slash.
	s = capturePane(t, ui, "ui")
	if !strings.Contains(s, "/") {
		t.Errorf("the composer backdrop does not show a tree line with '/':\n%s", s)
	}

	// `esc` returns to the tree, not the dashboard.
	send(t, ui, "Escape")
	time.Sleep(400 * time.Millisecond)
	s = capturePane(t, ui, "ui")
	if !strings.Contains(s, "asking") {
		t.Errorf("esc from the composer did not return to the tree:\n%s", s)
	}
	if strings.Contains(s, "tmux-hub  ") {
		t.Errorf("esc from the composer returned to the dashboard instead:\n%s", s)
	}
}

// `n` on a directory node pre-fills the launch form with that directory's path, which is the gesture the
// whole metaphor was asked for: "creating a new session should create it in the corresponding directory".
//
// This is the operator's own words and the reason the filesystem view exists. A unit test cannot reach
// the form's initial field value, because it is set once at construction — so driving the real binary is
// the only way to see what an operator would type into.
func TestE2EUITreeNOnDirectoryPreFillsPath(t *testing.T) {
	ui, _, _, work := hubWithView(t, 120, 40, 2, "sleep 300", "")

	screenHas(t, ui, "asking", "the hub must open on the tree")

	// Walk past the volume to find an INDENTED directory (a directory inside the volume).
	// A directory line has ▸ or ▾, a trailing slash, and starts with spaces (indented).
	for i := 0; i < 40; i++ {
		row := cursorRow(t, ui)
		if (strings.Contains(row, "▸") || strings.Contains(row, "▾")) &&
			strings.Contains(row, "/") &&
			strings.HasPrefix(row, " ") {
			break
		}
		send(t, ui, "j")
		time.Sleep(80 * time.Millisecond)
	}

	// Press `n` to open the launch form.
	send(t, ui, "n")
	screenHas(t, ui, "enter: create", "`n` must open the launch form")

	// The form must show the real absolute directory path.
	s := capturePane(t, ui, "ui")
	if !strings.Contains(s, "dir:") {
		t.Errorf("the launch form does not show a dir field:\n%s", s)
	}
	// The dir field should contain the test's work directory path.
	if !strings.Contains(s, work) {
		t.Errorf("the dir field does not show the directory's real path %q:\n%s", work, s)
	}

	// Leave the form without creating anything.
	send(t, ui, "Escape")
	screenHas(t, ui, "asking", "esc must return to the tree")
}

// `n` on a volume opens the launch form with that host selected and an empty dir field.
//
// A volume is a host, and the form's first field is the host. The dir field starts empty rather than
// refusing, which lets the operator choose the machine first and then type the directory. Only the
// FAVOURITES band, which has no address, refuses `n`.
func TestE2EUITreeNOnVolumeOpensFormWithHost(t *testing.T) {
	ui, _, _, _ := hubWithView(t, 120, 40, 2, "sleep 300", "")

	screenHas(t, ui, "asking", "the hub must open on the tree")

	// Walk to the volume line (not indented, has ▾ and trailing slash).
	// The first such line is the volume.
	for i := 0; i < 30; i++ {
		row := cursorRow(t, ui)
		if strings.Contains(row, "▾") && strings.Contains(row, "/") && !strings.HasPrefix(row, " ") {
			break
		}
		send(t, ui, "j")
		time.Sleep(80 * time.Millisecond)
	}

	// Press `n` to open the launch form.
	send(t, ui, "n")
	screenHas(t, ui, "enter: create", "`n` must open the launch form")

	// The form must show the host field with the volume's name.
	s := capturePane(t, ui, "ui")
	if !strings.Contains(s, "host:") {
		t.Errorf("the launch form does not show a host field:\n%s", s)
	}
	if !strings.Contains(s, "scratch") {
		t.Errorf("the host field does not name the volume:\n%s", s)
	}
	// The dir field should be empty (or show a placeholder, but not a specific path).
	if !strings.Contains(s, "dir:") {
		t.Errorf("the launch form does not show a dir field:\n%s", s)
	}

	// Leave the form without creating anything.
	send(t, ui, "Escape")
	screenHas(t, ui, "asking", "esc must return to the tree")
}

// THE FIRST RUN ENDS ON THE DEFAULT SCREEN.
//
// With no hosts.toml the picker opens without a keystroke (§9), so on a first run the operator meets the
// picker before they meet any screen at all — and the two startup options interact: the picker is raised,
// and the view flag decides which screen is UNDERNEATH it. Measured through the options before this was
// fixed: dismissing the first-run picker landed on the flat list, on the one run where the operator has
// never seen either screen, with that list drawn behind the picker as well.
//
// Driving the real binary is what makes this a claim about the product: the option wiring has its own unit
// case, and this one asserts what an operator sees after pressing `esc`.
func TestE2EUITreeTheFirstRunPickerLeavesTheOperatorOnTheTree(t *testing.T) {
	f := pickerHub(t, pickerOpts{
		cols: 120, rows: 40, probeTimeout: "1ms",
		sshConfig: pickerTwoCandidates,
		view:      "tree",
	})
	// No hostsTOML at all, which is the first run: the picker is already on the screen.
	screenHas(t, f.ui, "Hosts —", "with no hosts.toml the picker must open by itself")
	// The screen UNDER it is the filesystem view, not the flat list — the head line says so, and the
	// dashboard's own title is the needle that must be absent.
	screenHas(t, f.ui, "enter opens", "the screen under the first-run picker must be the filesystem view")

	send(t, f.ui, "Escape")
	screenHas(t, f.ui, "esc leaves", "esc from the first-run picker must land on the filesystem view")
	s := capturePane(t, f.ui, "ui")
	if strings.Contains(s, "tmux-hub  ") {
		t.Errorf("esc from the first-run picker landed on the flat dashboard:\n%s", s)
	}
}
