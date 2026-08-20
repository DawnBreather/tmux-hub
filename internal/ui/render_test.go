package ui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/DawnBreather/tmux-hub/internal/hub"
	"github.com/DawnBreather/tmux-hub/internal/lines"
	"github.com/DawnBreather/tmux-hub/internal/registry"
	"github.com/DawnBreather/tmux-hub/internal/state"

	"github.com/DawnBreather/tmux-hub/internal/project"
)

func samplePanes() []registry.Pane {
	return []registry.Pane{
		{Host: "local", PaneID: "%0", Session: "live1", Command: "claude", ClassifiedState: state.Needs,
			Content:  []string{"● Ran 42 tests, 3 failed", "Do you want to proceed?"},
			Activity: time.Unix(1786450000, 0)},
		{Host: "nuc", PaneID: "%7", Session: "work", Command: "claude", ClassifiedState: state.Works,
			Content:  []string{"● Bash(go build ./...)", "✻ Brewed for 46s"},
			Activity: time.Unix(1786450010, 0)},
	}
}

func TestLayoutForWidth(t *testing.T) {
	cases := []struct {
		w    int
		want Layout
	}{{80, InboxOnly}, {99, InboxOnly}, {100, InboxOneTile}, {159, InboxOneTile}, {160, InboxGrid}}
	for _, c := range cases {
		if got := LayoutFor(c.w); got != c.want {
			t.Errorf("LayoutFor(%d) = %v, want %v", c.w, got, c.want)
		}
	}
}

// At 80 columns, per-session header rows cost more than they give: six panes
// across five sessions spent 5 of 11 body rows on headers. So the host and
// session go inline on the pane row (docs/design.md §16).
func TestNarrowInboxPutsHostSessionInline(t *testing.T) {
	rows := RenderInbox(samplePanes(), 80, 10, 0, nil, true, nil, nil, nil, project.Aliases{})
	joined := strings.Join(rows, "\n")
	if strings.Contains(joined, "LOCAL LIVE1") {
		t.Fatal("narrow layout must not spend a row on a session header")
	}
	if !strings.Contains(rows[0], "local/live1") {
		t.Fatalf("row 0 = %q, want host/session inline", rows[0])
	}
	// The pane id is NOT inline here, and that is the rule rather than an omission: `live1` puts
	// one row on this screen, so its id distinguishes nothing (rowPaneID). The pole that keeps
	// this from reading as "the id is gone" is below — give the session a sibling and it returns.
	if strings.Contains(rows[0], "%0") {
		t.Errorf("row 0 = %q carries a pane id that distinguishes nothing", rows[0])
	}
	sib := samplePanes()[0]
	sib.PaneID, sib.ClassifiedState = "%77", state.Quiet
	withSibling := RenderInbox(append(samplePanes(), sib), 80, 12, 0, nil, true, nil, nil, nil,
		project.Aliases{})
	joined = strings.Join(withSibling, "\n")
	for _, want := range []string{"%0", "%77"} {
		if !strings.Contains(joined, want) {
			t.Errorf("two panes of one session at 80 columns and %s is missing, so the two rows "+
				"are indistinguishable:\n%s", want, joined)
		}
	}
}

func TestEveryRenderedLineFitsTheWidth(t *testing.T) {
	for _, w := range []int{60, 80, 120, 200} {
		out := Render(Frame{Panes: samplePanes(), Hosts: nil, Width: w, Height: 20, Cursor: 0, Marked: nil, Note: "", Hint: "", HiddenCount: 0, BlockedCount: 0, Resurfaced: nil, Aliases: project.Aliases{}})
		for i, l := range strings.Split(out, "\n") {
			if got := lines.Width(l); got > w {
				t.Errorf("width %d: line %d is %d cells: %q", w, i, got, l)
			}
		}
	}
}

// Surplus width must become tile columns, not a wider tile: at 160 columns a
// naive split produced a 130-column tile holding 30-column content.
func TestTileWidthIsBounded(t *testing.T) {
	rows := RenderTile(samplePanes()[0], 200, 6, project.Aliases{})
	for _, l := range rows {
		if got := lines.Width(l); got > MaxTileWidth {
			t.Fatalf("tile line is %d cells, want <= %d: %q", got, MaxTileWidth, l)
		}
	}
}

func TestTileRendersContentNotChrome(t *testing.T) {
	p := samplePanes()[0]
	rows := RenderTile(p, 60, 6, project.Aliases{})
	joined := strings.Join(rows, "\n")
	if !strings.Contains(joined, "Ran 42 tests") {
		t.Fatalf("tile lost its content: %q", joined)
	}
}

func TestMarkedPaneIsVisiblyMarked(t *testing.T) {
	marked := map[string]bool{"local\x00%0": true}
	rows := RenderInbox(samplePanes(), 80, 10, 0, marked, true, nil, nil, nil, project.Aliases{})
	if !strings.Contains(rows[0], "◆") {
		t.Fatalf("row 0 = %q, want a mark glyph", rows[0])
	}
}

func TestNarrowRowNamesWhatIsRunning(t *testing.T) {
	rows := RenderInbox(samplePanes(), 80, 10, 0, nil, true, nil, nil, nil, project.Aliases{})
	if !strings.Contains(rows[0], "claude") {
		t.Fatalf("row 0 = %q, want the command named — at 80 cols it identifies the pane better than its id", rows[0])
	}
}

func TestNarrowLayoutFillsSpareRowsWithATile(t *testing.T) {
	// One pane on a 24-row terminal must not leave 21 rows blank.
	one := samplePanes()[:1]
	out := Render(Frame{Panes: one, Hosts: nil, Width: 80, Height: 24, Cursor: 0, Marked: nil, Note: "", Hint: "", HiddenCount: 0, BlockedCount: 0, Resurfaced: nil, Aliases: project.Aliases{}})
	if !strings.Contains(out, "Ran 42 tests") {
		t.Fatalf("a single pane on a 24-row terminal should show its content:\n%s", out)
	}
	if strings.Contains(out, "1 sessions") {
		t.Fatal(`header says "1 sessions"`)
	}
	if !strings.Contains(out, "1 session") {
		t.Fatalf("header should count the pane:\n%s", out)
	}
}

// The details band is PINNED, so a full list does not take it away — the list scrolls instead. This
// case used to assert the opposite ("no room for a tile, so none was drawn"), which was true when the
// tile lived on whatever rows the list had left over; a band whose position depends on how many
// sessions there are is not a place the operator's eye can learn.
//
// The band does yield on a screen too short for both: below a 4-row list `detailsHeight` answers 0,
// and then the screen is all list. That end is asserted in TestTheBandYieldsOnAScreenTooShortForBoth.
func TestAFullInboxStillGetsItsBand(t *testing.T) {
	many := make([]registry.Pane, 0, 20)
	for i := 0; i < 20; i++ {
		p := samplePanes()[0]
		p.PaneID = fmt.Sprintf("%%%d", i)
		many = append(many, p)
	}
	out := Render(Frame{Panes: many, Hosts: nil, Width: 80, Height: 24, Cursor: 0, Marked: nil, Note: "", Hint: "", HiddenCount: 0, BlockedCount: 0, Resurfaced: nil, Aliases: project.Aliases{}})
	if !strings.Contains(out, "┌─") {
		t.Fatalf("a full list took the details band away:\n%s", out)
	}
	// And the list is what gave up the rows, not the frame: the screen is still 24 rows.
	if got := len(strings.Split(out, "\n")); got != 24 {
		t.Errorf("the frame is %d rows", got)
	}
}

func TestTheBandYieldsOnAScreenTooShortForBoth(t *testing.T) {
	many := make([]registry.Pane, 0, 20)
	for i := 0; i < 20; i++ {
		p := samplePanes()[0]
		p.PaneID = fmt.Sprintf("%%%d", i)
		many = append(many, p)
	}
	// A 7-row screen leaves a 5-row body, and a band there would leave the list one row. The band
	// gives way rather than the list.
	out := Render(Frame{Panes: many, Width: 80, Height: 7, Aliases: project.Aliases{}})
	if strings.Contains(out, "┌─") {
		t.Fatalf("a band was drawn on a screen with no room for a list:\n%s", out)
	}
}

func TestTileDoesNotPadToItsGivenHeight(t *testing.T) {
	// An empty box reads as a broken tile; spare screen rows read as spare room.
	p := samplePanes()[0] // two content lines
	rows := RenderTile(p, 60, 20, project.Aliases{})
	if len(rows) != 4 {
		t.Fatalf("tile has %d rows for 2 content lines, want 4 (top, 2, bottom):\n%s",
			len(rows), strings.Join(rows, "\n"))
	}
}

// Pressing j past the bottom moved a cursor nobody could see: with 30 panes on a
// 24-row terminal the cursor row simply left the screen.
func TestListScrollsToKeepTheCursorVisible(t *testing.T) {
	panes := make([]registry.Pane, 0, 30)
	for i := 0; i < 30; i++ {
		panes = append(panes, registry.Pane{
			Host: "local", PaneID: fmt.Sprintf("%%%d", i), Session: fmt.Sprintf("s%d", i),
			Command: "claude", ClassifiedState: state.Idle,
		})
	}
	for _, cursor := range []int{0, 1, 15, 28, 29} {
		out := Render(Frame{Panes: panes, Hosts: nil, Width: 80, Height: 24, Cursor: cursor, Marked: nil, Note: "", Hint: "", HiddenCount: 0, BlockedCount: 0, Resurfaced: nil, Aliases: project.Aliases{}})
		// By the needle the ROW carries: this fixture is one pane per session, so the id is not
		// drawn (rowPaneID) and the row leads with `local/sN`. The trailing space is what keeps
		// `local/s1` from matching the row for `local/s15`.
		want := rowNeedle(panes[cursor], panes) + " "
		found := false
		for _, l := range strings.Split(out, "\n") {
			if strings.HasPrefix(strings.TrimSpace(l), ">") && strings.Contains(l+" ", want) {
				found = true
			}
		}
		if !found {
			t.Errorf("cursor %d is not on screen:\n%s", cursor, out)
		}
	}
}

func TestScrollTopKeepsTheCursorInRange(t *testing.T) {
	for _, c := range []struct{ cursor, n, height int }{
		{0, 30, 22}, {21, 30, 22}, {22, 30, 22}, {29, 30, 22}, {5, 6, 22}, {0, 1, 22},
	} {
		panes := make([]registry.Pane, c.n)
		for i := range panes {
			panes[i] = registry.Pane{Kind: registry.KindPane, Host: "local",
				PaneID: fmt.Sprintf("%%%d", i), Session: "s", Command: "sh"}
		}
		first, _ := inboxWindow(panes, c.cursor, c.height, project.Aliases{}, ByHost, nil, true, nil)
		if c.cursor < first || c.cursor >= first+c.height {
			t.Errorf("inboxWindow(cursor=%d,n=%d,h=%d) = %d — cursor falls outside [%d,%d)",
				c.cursor, c.n, c.height, first, first, first+c.height)
		}
	}
}

// A host that stops answering must not leave its panes looking live. Observed:
// killing the tunnel left a row still reading "works".
func TestStalePaneSaysSoAndCarriesItsAge(t *testing.T) {
	p := samplePanes()[0]
	p.ClassifiedState = state.Works
	p.Stale = true
	p.StaleSince = time.Unix(1786450000, 0)
	p.SeenAt = time.Unix(1786449900, 0)

	rows := RenderInbox([]registry.Pane{p}, 90, 6, 0, nil, true, nil, nil, nil, project.Aliases{})
	if strings.Contains(rows[0], "works") {
		t.Fatalf("a stale pane still reads works: %q", rows[0])
	}
	if !strings.Contains(rows[0], "stale") {
		t.Fatalf("row does not say stale: %q", rows[0])
	}
	// The age is NOT on the row: in the 28-column inbox "(last seen 15:04:05)"
	// truncated to "(las", which reads as a rendering bug. It goes in the tile
	// header, where there is room.
	if strings.Contains(rows[0], "last seen") {
		t.Errorf("the age must not be on the row, where it truncates: %q", rows[0])
	}
	tile := RenderTile(p, 70, 6, project.Aliases{})
	if !strings.Contains(tile[0], "last seen") {
		t.Errorf("the tile header should carry the age: %q", tile[0])
	}
}

// With the hub inside tmux and both servers on the default prefix, C-b d
// detaches the OUTER session — measured — so the user's first attempt to leave
// an attached session throws them out of the hub and leaves the inner one
// attached. The header says what actually works, before `a` is pressed.
//
// Through View(), not through Render. Render stopped reading $TMUX when the hint
// became path-specific: it is handed the string. So the two tests here used to set
// $TMUX for nothing, hand the very string they asserted straight in, and pass
// against a View() that had stopped calling hintFor at all — the phrasing was
// checked and the WIRING was not, which is what the names actually promise.
//
// The state is one production reaches: $TMUX set (so the hub is nested and
// hub.SelfSessionID answers `$0`) and no epoch of its own, which is what a failed
// ServerEpoch read leaves. decidePossession then falls back to the full-screen
// attach for a local target, and that path is the one this phrasing is true for.
func TestNestedHubSaysHowToLeaveAnAttachedSession(t *testing.T) {
	t.Setenv("TMUX", "/tmp/tmux-1000/default,123,0")
	m := nestedHubModel()
	if got := m.pathForCursor(); got != pathFullScreen {
		t.Fatalf("the row under the cursor takes path %v, not the one this phrasing is "+
			"about — the assertion below would be checking the wrong hint", got)
	}
	out := m.View()
	if !strings.Contains(out, "C-b C-b d") {
		t.Fatalf("nested hub does not say how to detach:\n%s", strings.SplitN(out, "\n", 2)[0])
	}
}

// And it must be CONDITIONAL: outside tmux there is no outer session to be thrown
// out of, so the warning would be advice about a key that does nothing. The
// positive half is what keeps an absence from passing on an empty screen.
func TestUnnestedHubDoesNotSayIt(t *testing.T) {
	t.Setenv("TMUX", "")
	out := nestedHubModel().View()
	if !strings.Contains(out, "live1") {
		t.Fatalf("the dashboard did not render at all, so the absence below proves "+
			"nothing:\n%s", out)
	}
	if strings.Contains(out, "C-b") {
		t.Fatalf("the hint is only true when nested:\n%s", strings.SplitN(out, "\n", 2)[0])
	}
}

// nestedHubModel is the hub with a local host it can attach to and no epoch of its
// own. selfSession is filled because $TMUX being set is exactly what fills it in
// production: a model with $TMUX set and an empty selfSession is a state the hub
// cannot be in, and asserting on one proves nothing about the product.
func nestedHubModel() model {
	return model{
		panes: samplePanes(),
		hosts: []hub.Host{{Label: "local", Socket: "/tmp/tmux-1000/default", LocalProc: true}},
		width: 120, height: 20,
		selfSession: "$0",
	}
}

// The hint has to say the way back for the path `a` will actually take. One
// phrase for all paths was true for exactly one of them, and it named a detach
// that a jump does not perform.
func TestTheHintNamesTheWayBackForThisRow(t *testing.T) {
	t.Setenv("TMUX", "/tmp/tmux-1000/default,4242,0")
	for _, tc := range []struct {
		path possessionPath
		want string
		deny string
	}{
		{pathJump, "C-b L", "C-b C-b d"},
		{pathWindow, "C-b C-b d", ""},
	} {
		got := hintFor(tc.path)
		if !strings.Contains(got, tc.want) {
			t.Errorf("hintFor(%v) = %q, want it to name %q", tc.path, got, tc.want)
		}
		if tc.deny != "" && strings.Contains(got, tc.deny) {
			t.Errorf("hintFor(%v) = %q, must not name %q — this path detaches nothing",
				tc.path, got, tc.deny)
		}
	}
	// Outside tmux there is nothing nested to warn about.
	t.Setenv("TMUX", "")
	if got := hintFor(pathJump); got != "" {
		t.Errorf("hintFor outside tmux = %q, want empty", got)
	}
}

// And it has to reach the screen. A hint that is computed and not rendered is the
// defect this repo has shipped before: four interface modes were fully covered
// and never drawn.
func TestTheHintIsOnTheScreenViewReturns(t *testing.T) {
	t.Setenv("TMUX", "/tmp/tmux-1000/default,4242,0")
	m := newTestModel(t, &possessionRecorder{})
	m.selfEpoch = "999:111"
	m = m.cursorTo(0) // the local pane: a jump
	if v := m.View(); !strings.Contains(v, "C-b L") {
		t.Fatalf("View() lacks the jump hint:\n%s", v)
	}
	m = m.cursorTo(1) // the remote pane: a window
	// The phrase, not the keystroke. `C-b C-b d` also appears in hintFor's DEFAULT
	// branch, which serves pathFullScreen AND pathRefuse — so asserting the
	// keystroke would still pass if decidePossession regressed to either of those
	// for this row. "a window with the attach" exists in one branch only, which is
	// the assertion the mockup document already makes (flows_test.go, frame 2).
	v := m.View()
	if !strings.Contains(v, "a window with the attach") || !strings.Contains(v, "C-b C-b d") {
		t.Fatalf("View() lacks the window hint:\n%s", v)
	}
	if strings.Contains(v, "nested: leave an attached session") {
		t.Fatalf("View() shows the ambient nested warning, so this row is not on the window path:\n%s", v)
	}
}

// Surplus width must become tile COLUMNS. A single full-width column wasted the
// screen: at 160 columns it drew one 130-column tile holding 30-column content.
func TestWideScreenLaysTilesInColumns(t *testing.T) {
	panes := make([]registry.Pane, 0, 4)
	for i := 0; i < 4; i++ {
		panes = append(panes, registry.Pane{
			Host: "local", PaneID: fmt.Sprintf("%%%d", i), Session: fmt.Sprintf("s%d", i),
			Command: "claude", ClassifiedState: state.Idle,
			Content: []string{fmt.Sprintf("● answer %d", i)},
		})
	}
	marked := map[string]bool{}
	for _, p := range panes {
		marked[MarkKey(p)] = true
	}
	out := Render(Frame{Panes: panes, Hosts: nil, Width: 220, Height: 30, Cursor: 0, Marked: marked, Note: "", Hint: "", HiddenCount: 0, BlockedCount: 0, Resurfaced: nil, Aliases: project.Aliases{}})
	// Two tile tops on one line is the whole point of a grid.
	twoOnALine := false
	for _, l := range strings.Split(out, "\n") {
		if strings.Count(l, "┌─") >= 2 {
			twoOnALine = true
		}
	}
	if !twoOnALine {
		t.Fatalf("no line holds two tiles, so this is still a single column:\n%s", out)
	}
	for i, l := range strings.Split(out, "\n") {
		if got := lines.Width(l); got > 220 {
			t.Fatalf("line %d is %d cells wide", i, got)
		}
	}
}

// agentStart is the fixture's session start time, named so the tile assertion can spell
// out the value it expects instead of matching a label and accepting anything after it.
var agentStart = time.Unix(1786450000, 0)

func agentRow(id, name, kind string, st state.State) registry.Pane {
	return registry.Pane{Kind: registry.KindAgent, Host: "local",
		PaneID: "agent:" + id, AgentID: id, Session: name, Command: kind, ClassifiedState: st,
		SessionID: id + "-full", Activity: agentStart}
}

// Giving every pane-less session a header of its own made the list half headers,
// because each one's name is unique. Their name IS the row.
func TestAgentRowsGetNoSessionHeader(t *testing.T) {
	rows := RenderInbox([]registry.Pane{
		agentRow("1ff133f7", "dockerfile goldens across the fleet", "background", state.Needs),
		agentRow("4ca5ffa9", "Access Miro board specification", "background", state.Works),
	}, 70, 8, 0, nil, false, nil, nil, nil, project.Aliases{})
	joined := strings.Join(rows, "\n")
	if strings.Contains(joined, "LOCAL DOCKERFILE") {
		t.Fatalf("an agent row took a header:\n%s", joined)
	}
	if !strings.Contains(rows[0], "dockerfile goldens") {
		t.Fatalf("row 0 does not name the session: %q", rows[0])
	}
	if !strings.Contains(rows[0], "needs") {
		t.Fatalf("row 0 does not carry the state: %q", rows[0])
	}
	// Two sessions, two rows — no headers in between.
	if strings.TrimSpace(rows[1]) == "" || !strings.Contains(rows[1], "Miro") {
		t.Fatalf("row 1 = %q, want the second session", rows[1])
	}
}

// A session with no pane has no screen, so the tile must carry what IS known
// instead of drawing an empty box.
func TestTileForAPaneLessSessionSaysWhatIsKnown(t *testing.T) {
	rows := RenderTile(agentRow("1ff133f7", "dockerfile goldens", "background", state.Needs), 60, 8, project.Aliases{})
	joined := strings.Join(rows, "\n")
	// `started:` rather than `since:`, and with its VALUE: a label-only assertion
	// passes against a formatted zero time, and the word matters because an agent row's
	// Activity is the session's start while a pane row's is its last output (§21.11.1).
	for _, want := range []string{"state: needs", "kind:  background",
		"started: " + agentStart.Format("2006-01-02 15:04"), "id:"} {
		if !strings.Contains(joined, want) {
			t.Errorf("tile lacks %q:\n%s", want, joined)
		}
	}
	if strings.Contains(joined, "agent:1ff133f7") {
		t.Error("the tile header should not pretend a session id is a pane id")
	}
}

// The renderer and InboxViewport both answer "how many pane rows are on screen" —
// one by drawing them, one by arithmetic — and `A` selects the panes InboxViewport
// names. The moment the two disagree, `A` selects panes the user cannot see, which
// is the only thing it exists to prevent. Both get their row budget from
// bodyHeight, and this holds them to it.
//
// The assertion that matters is per PANE, not per count: every pane the viewport
// claims must appear in what the renderer drew. A count comparison passes whenever
// the two numbers happen to match, and the numbers cannot match in the grouped
// layouts — session headers take body rows, so a 28-row body draws 14 panes when
// every session has one — which is exactly why the earlier version of this test used
// pane-less AGENT rows, the one fixture that takes no headers at all. It therefore
// removed the only thing that makes the renderer and InboxViewport disagree, and the
// bug it is named after lived under it.
//
// GROUPED panes at width ≥ 100 are the case that breaks it, and the fixtures below
// span the whole range: one pane per session is the worst case (headers == panes),
// two per session is the common one, and agent rows are the no-header end.
func TestRendererAndViewportAgreeOnTheBody(t *testing.T) {
	// Session and pane names are three digits wide, so no name is a substring of
	// another and "did the renderer draw this pane" is answerable from its output.
	grouped := func(perSession int) []registry.Pane {
		out := make([]registry.Pane, 0, 200)
		for i := 0; i < 200; i++ {
			out = append(out, registry.Pane{
				Kind: registry.KindPane, Host: "local",
				PaneID:  fmt.Sprintf("%%%03d", i),
				Session: fmt.Sprintf("s%03d", i/perSession),
				Command: "claude", ClassifiedState: state.Idle,
			})
		}
		return out
	}
	agents := make([]registry.Pane, 0, 200)
	for i := 0; i < 200; i++ {
		agents = append(agents, agentRow(fmt.Sprintf("%03d", i), fmt.Sprintf("s%03d", i),
			"background", state.Idle))
	}
	// An agent row is found by its unique session name. A pane row is found by EITHER its id or
	// its `host/session`, and the two are complementary rather than redundant: rowPaneID draws the
	// id only when the session puts more than one row on the screen, and a row whose session puts
	// one up leads with `host/session` instead. So the shared-session fixture is counted by id,
	// where the ids are what tell the rows apart, and the one-pane-per-session fixture by name,
	// which is unique there. Keying on the id alone was correct until the id stopped being drawn
	// for 49 rows out of 60, and then this helper reported ONE row drawn out of fourteen.
	drew := func(out string, p registry.Pane) bool {
		if p.Kind == registry.KindAgent {
			return strings.Contains(out, p.Session)
		}
		return drawsRow(out, p.PaneID) || drawsRow(out, p.Host+"/"+p.Session)
	}

	for _, c := range []struct {
		name          string
		panes         []registry.Pane
		width, height int
		cursor        int
		// wantRows is the number of PANE rows on screen, written out rather than derived: the
		// screen is one header row, the LIST, a blank row, the details band pinned to the bottom,
		// and one footer row — and in a grouped layout the session headers take their share of the
		// list. Re-measured when the band moved to the bottom, and each cell follows from
		// `inboxHeight`: a 24-row screen has a 22-row body, a 7-row band and a 14-row list, and a
		// 30-row screen has 28, 9 and 18.
		wantRows int
	}{
		{"inbox-only, no headers at 80 columns", grouped(1), 80, 24, 0, 14},
		{"inbox-only, scrolled to the cursor", grouped(1), 80, 24, 40, 14},
		// A session holding ONE pane takes no header — the row carries the name the header
		// would have held — so these three cells are the whole LIST rather than half of it,
		// which is what the agent-row cell below has always been. They were 9, 9 and 12 when
		// every session cost a header row, and the pane-per-session fixture is the shape the
		// door produces: it gives each session it opens a tmux session of its own.
		{"inbox+tile, one pane per session", grouped(1), 120, 30, 0, 18},
		{"inbox+tile, one pane per session, scrolled", grouped(1), 120, 30, 40, 18},
		// The CONTROL: two panes per session keep their header, so 18 list rows hold 6 headers
		// and 12 panes. This cell is what says the rule is "a group of one" and not "no headers".
		{"inbox+tile, two panes per session", grouped(2), 120, 30, 0, 12},
		{"inbox+grid, one pane per session, scrolled", grouped(1), 200, 40, 60, 25},
		{"inbox+grid, agent rows take no header", agents, 200, 40, 60, 25},
		{"the shortest screen that draws a body", grouped(1), 80, 4, 0, 2},
		{"too short for a body at all", grouped(1), 80, 2, 0, 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			panes := c.panes
			f := Frame{Panes: panes, Hosts: nil, Width: c.width, Height: c.height,
				Cursor: c.cursor, Marked: nil, Note: "", Hint: "", HiddenCount: 0,
				BlockedCount: 0, Resurfaced: nil, Aliases: project.Aliases{}}
			first, count := InboxViewport(f)
			out := Render(f)

			drawn := 0
			for _, p := range panes {
				if drew(out, p) {
					drawn++
				}
			}
			if drawn != c.wantRows {
				t.Errorf("the renderer drew %d panes on a %d-row screen, want %d:\n%s",
					drawn, c.height, c.wantRows, out)
			}

			// THE guarantee: everything the viewport claims is on screen. `A` selects
			// exactly this window, so a claimed pane the renderer did not draw is a
			// pane the user is about to write into without looking at it.
			for i := first; i < first+count; i++ {
				if !drew(out, panes[i]) {
					t.Fatalf("InboxViewport claims pane %d (%s) is visible and the renderer"+
						" did not draw it — A would select %d panes off screen",
						i, panes[i].PaneID, first+count-i)
				}
			}
			// EXACTLY what was drawn, in both directions. Over-claiming makes `A` select rows
			// off screen; under-claiming makes it select half of what the operator can see, and
			// the second is what a conservative ESTIMATE of the header count produced once a
			// group of one stopped taking a header — 12 of 25 at every width, measured. The
			// window is walked from the rows now, so there is nothing left to be approximate
			// about and the assertion can be equality.
			if count != drawn {
				t.Errorf("InboxViewport claims %d visible where %d were drawn", count, drawn)
			}
			if count == 0 {
				return
			}
			// The window must also START where the renderer starts, or A selects a
			// window the right size in the wrong place.
			if first > 0 && drew(out, panes[first-1]) {
				t.Errorf("the renderer drew pane %d, which is above InboxViewport's window",
					first-1)
			}
		})
	}
}

// Pane rows keep their grouping while agent rows do not, in one list.
func TestPaneRowsStillGroupWhenAgentsAreMixedIn(t *testing.T) {
	panes := []registry.Pane{
		agentRow("aaaa", "an agent", "background", state.Needs),
		{Kind: registry.KindPane, Host: "local", PaneID: "%0", Session: "live1",
			Command: "claude", ClassifiedState: state.Idle},
		{Kind: registry.KindPane, Host: "local", PaneID: "%1", Session: "live1",
			Command: "bash", ClassifiedState: state.Idle},
	}
	rows := RenderInbox(panes, 70, 10, 0, nil, false, nil, nil, nil, project.Aliases{})
	joined := strings.Join(rows, "\n")
	if strings.Count(joined, "LOCAL LIVE1") != 1 {
		t.Fatalf("the two panes of one session want exactly one header:\n%s", joined)
	}
	if strings.Contains(joined, "LOCAL AN AGENT") {
		t.Fatalf("the agent still took a header:\n%s", joined)
	}
}

func TestADeadPaneSaysItsExitCode(t *testing.T) {
	// state.Error already covers "failed, or the pane is dead" and #{pane_dead}
	// already reaches it — but the row said nothing about WHY, and an exit code
	// is the difference between a finished job and a crash.
	p := pane("local", "api", "claude", 0, `"claude"`, state.Error)
	p.Dead, p.DeadStatus = true, 7
	m := modelWith(t, p)

	out := m.View()
	if !strings.Contains(out, "exited 7") {
		t.Fatalf("a dead pane must carry its exit code:\n%s", out)
	}
}

func TestADeadPaneThatExitedCleanlySaysSo(t *testing.T) {
	// Exit 0 is "the agent finished", not "the agent failed". Same row, and it
	// must not read as an error.
	p := pane("local", "api", "claude", 0, `"claude"`, state.Error)
	p.Dead, p.DeadStatus = true, 0
	m := modelWith(t, p)

	out := m.View()
	if !strings.Contains(out, "exited 0") {
		t.Fatalf("a dead pane that exited cleanly must say so:\n%s", out)
	}
}

// The inbox is sorted by attention and scanned by attention, so the state has to
// be in ONE column — and it was not: a pane row put it fourth (after the pane id
// and the command) while an agent row put it second, right after the glyph. Two
// column layouts in one list, so the eye has nothing to run down.
//
// Asserted on the COLUMN INDEX rather than on the text, because that is the
// property: whatever the words are, they have to start in the same place.
func TestTheStateWordIsInOneColumnForBothRowKinds(t *testing.T) {
	pane := agentPane("local", "live1", "claude", "%2", 0, state.Quiet, "  ❯ ")
	agent := registry.Pane{
		Kind: registry.KindAgent, Host: "local", Session: "20260808--gis-offline-maps",
		ClaudeSession: "7ef2fe7e-c88d-46da-ac9d-0b57f170c3d8", ClassifiedState: state.Works,
	}

	for _, w := range []int{80, 120} {
		m := base(t, w, 24, pane, agent)
		var paneCol, agentCol = -1, -1
		// By the SURFACE and then by the state word. The discriminator used to be the pane id
		// (`%2`) plus the state, because the tile header carries the id too — and the pane row
		// stopped drawing its id at all once rowPaneID landed, since this session puts one row
		// up. inboxRow already knows how to skip the tile (no inbox row draws the box rule), so
		// the state word alone is enough inside it. The session NAME cannot be the
		// discriminator: the wide inbox is 29 columns, so at 120 it truncates to "202".
		for _, line := range []string{inboxRow(t, m.View(), "quiet"), inboxRow(t, m.View(), "works")} {
			switch {
			case strings.Contains(line, "quiet"):
				paneCol = displayCol(line, "quiet")
			case strings.Contains(line, "works"):
				agentCol = displayCol(line, "works")
			}
		}
		if paneCol < 0 || agentCol < 0 {
			t.Fatalf("width %d: could not find both rows in\n%s", w, m.View())
		}
		if paneCol != agentCol {
			t.Errorf("width %d: the state word starts at column %d on a pane row and %d on an "+
				"agent row, so there is no column to scan:\n%s", w, paneCol, agentCol, m.View())
		}
	}
}

// displayCol is where a substring starts in COLUMNS, not bytes. The state glyphs
// are multi-byte and not the same length — `✱` is three bytes and `·` is two — so
// a byte index reports two rows that line up perfectly as one column apart. That
// is what this test's first version measured.
func displayCol(line, sub string) int {
	i := strings.Index(line, sub)
	if i < 0 {
		return -1
	}
	return lines.Width(line[:i])
}

// A pane whose capture is EMPTY gets CONTEXT in its tile instead of an empty box.
//
// Measured on a real fleet at 80x24: a `sleep` pane, a cleared prompt, anything that draws nothing,
// left three of the twenty-four rows the project commits to holding one blank line under a header
// that only repeated the row above. The four lines that replace it are the ones the operator is
// choosing between rows on and that the row itself does not carry.
//
// It is a FALLBACK and the second half of this test is what keeps it one: when the pane HAS a
// capture, that capture is the most valuable thing on the screen — the tile is how an operator reads
// `Do you want to proceed? ❯ 1. Yes` without leaving the hub — so the context must not replace it.
func TestAPaneTileWithNoCaptureCarriesContextInstead(t *testing.T) {
	bare := registry.Pane{Kind: registry.KindPane, Host: "local", Session: "scratch", Window: "shell",
		WindowIndex: 2, PaneID: "%7", Command: "sleep", Path: "/w/render-map",
		Width: 120, Height: 40, ClassifiedState: state.Idle}

	got := strings.Join(RenderTile(bare, 72, 8, project.Aliases{}), "\n")
	for _, want := range []string{"/w/render-map", "2 shell", "120x40", "sleep"} {
		if !strings.Contains(got, want) {
			t.Errorf("the tile of a pane with no capture does not carry %q:\n%s", want, got)
		}
	}
	// Not an empty box: the body must hold something, or the three rows are spent on nothing.
	if n := strings.Count(got, "\n"); n < 4 {
		t.Errorf("the tile is %d lines, which is the empty box this closes:\n%s", n+1, got)
	}

	// THE OTHER HALF: a capture wins. Nothing here may push the pane's own screen off the tile.
	live := bare
	live.Content = []string{"  Do you want to proceed?", "  ❯ 1. Yes", "    2. No"}
	out := strings.Join(RenderTile(live, 72, 8, project.Aliases{}), "\n")
	if !strings.Contains(out, "Do you want to proceed?") {
		t.Errorf("the pane's own screen is missing from its tile:\n%s", out)
	}
	for _, mustNot := range []string{"path:", "window:", "size:", "running:"} {
		if strings.Contains(out, mustNot) {
			t.Errorf("the context %q displaced the pane's own capture, which is what the tile is "+
				"for:\n%s", mustNot, out)
		}
	}
}

// A ROW that had to be cut says so, and one that fits does not.
//
// On this fleet a session is named after the prompt that started it — measured at 88 columns — so at
// the 80 the project commits to the name is what yields. Before this the row simply stopped:
//
//	>  ⚑ needs  20260803--store-online-takes-too-long-to-ci-cd-troubleshooting ⑂ дав
//
// which reads as a name that ENDS there. The one-cell marker is the difference between "this is the
// name" and "the name goes on", and it is paid for by the name itself — the least load-bearing part
// of the row, which is why truncation already eats it.
//
// The negative half is what stops the marker becoming furniture: a row that fits must carry no `…`,
// or the mark would stop meaning anything.
func TestARowThatWasCutSaysSoAndOneThatFitsDoesNot(t *testing.T) {
	long := agentRow("30f3382b", "20260803--store-online-takes-too-long-to-ci-cd-troubleshooting "+
		"⑂ давай раскатаем Dockerfile goldens по всему флоту", "background", state.Needs)
	short := agentRow("45dfef2f", "cicd", "background", state.Idle)

	narrow := base(t, 80, 24, long, short).View()
	cut := inboxRow(t, narrow, "20260803")
	if !strings.Contains(cut, "…") {
		t.Errorf("a row cut at 80 columns does not say it was cut: %q", cut)
	}
	if lines.Width(cut) > 80 {
		t.Errorf("the marked row is %d columns on an 80-column screen: %q", lines.Width(cut), cut)
	}
	if fits := inboxRow(t, narrow, "cicd"); strings.Contains(fits, "…") {
		t.Errorf("a row that fits carries a truncation mark, so the mark says nothing: %q", fits)
	}

	// And at a width where the long name FITS, the mark is gone — the same row, no marker, which is
	// the property a fixed fixture cannot show.
	wide := base(t, 200, 50, long, short).View()
	if w := inboxRow(t, wide, "20260803"); strings.Contains(w, "…") {
		t.Errorf("the row is marked as cut at 200 columns where it fits: %q", w)
	}
}
