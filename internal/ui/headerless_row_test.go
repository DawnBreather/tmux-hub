package ui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/DawnBreather/tmux-hub/internal/project"
	"github.com/DawnBreather/tmux-hub/internal/registry"
	"github.com/DawnBreather/tmux-hub/internal/state"
)

// A row must never sit under a header that does not speak for it.
//
// Reported as "I opened the tmux-hub and it feels like I see duplicates now", and the feeling was
// right about the screen while being wrong about the cause — nothing was duplicated. The list is
// ordered by ATTENTION rather than grouped, so a group of ONE lands wherever its state puts it,
// and two such rows landed between the two rows of a nuc session:
//
//	NUC TMUX-HUB-DEMO
//	>  ✱ quiet  %1   claude
//	   ✱ quiet  tmp  %20  claude                ← on LOCAL
//	   ✱ quiet  20260817-cicd-30f3382b  %19     ← on LOCAL
//	NUC TMUX-HUB-DEMO  (cont.)
//	   ✱ quiet  %0   bash
//
// Two hosts under one header, and the header returning as `(cont.)` — which reads as the same
// session twice. The group-of-one rule that dropped those headers argued that "a header over a
// single row says nothing the row does not"; that was false while the row omitted its HOST, and
// this test is the sentence made checkable.
//
// It is expressed as the INVARIANT and not as the four strings above, because the same defect has
// two shapes in one loop — the group-of-one row and the pane-less agent row, which had its own
// copy of the row-building code — and a test written against one screen would have covered one of
// them. For every row: the nearest header above it names its group, or the row names its own host.
func TestNoRowSitsUnderAHeaderThatDoesNotSpeakForIt(t *testing.T) {
	now := time.Now()
	// The nuc session with TWO panes: it earns a header, and that header is the one the other
	// rows were inheriting. Quiet throughout, so attention ordering is by recency and the
	// interleaving below is deterministic.
	nucA := agentPane("nuc", "tmux-hub-demo", "logs", "%7", 1, state.Quiet, "one")
	nucA.Activity, nucA.StateSince = now.Add(-1*time.Minute), now.Add(-1*time.Minute)
	nucB := toolPane("nuc", "tmux-hub-demo", "asks", "%8", "bash", 0, state.Quiet, "two")
	nucB.Activity, nucB.StateSince = now.Add(-4*time.Minute), now.Add(-4*time.Minute)
	// Two LOCAL sessions of one pane each, landing between the nuc pair by recency. These are
	// the rows the nuc header was claiming.
	loneA := agentPane("local", "tmp", "claude", "%20", 20, state.Quiet, "three")
	loneA.Activity, loneA.StateSince = now.Add(-2*time.Minute), now.Add(-2*time.Minute)
	loneB := agentPane("local", "cicd-30f3382b", "claude", "%19", 19, state.Quiet, "four")
	loneB.Activity, loneB.StateSince = now.Add(-3*time.Minute), now.Add(-3*time.Minute)
	// The SECOND shape: a session with no pane at all. It never takes a header either, and it
	// had its own copy of the row-building code — so the invariant has to reach it.
	paneLess := registry.Pane{
		Kind: registry.KindAgent, Command: "interactive", Host: "local",
		Session: "frontend-postmortem", SessionID: "9f9f9f9f-0000",
		PaneID: "agent:9f9f9f9f@5e6f7a8b", ClassifiedState: state.Idle,
		Content: []string{"  (no pane)"},
	}

	fleet := []registry.Pane{nucA, nucB, loneA, loneB, paneLess}
	m := base(t, 120, 24, fleet...)
	screen := m.View()
	body := strings.Split(screen, "\n")

	// A header is a line EQUAL to a group label the renderer would compose, so the test needs no
	// heuristic about upper case or leading spaces — groupLabelOf is the one place a row becomes a
	// header string, and calling it is what keeps this check honest if the label's shape changes.
	headers := map[string]bool{}
	for _, p := range fleet {
		headers[groupLabelOf(p, project.Aliases{}, ByHost, nil)] = true
	}
	isHeader := func(l string) (string, bool) {
		l = strings.TrimSuffix(strings.TrimRight(l, " "), "  (cont.)")
		return l, headers[l]
	}

	for _, p := range fleet {
		needle := rowNeedle(p, fleet)
		at, row := -1, ""
		for i, l := range body {
			if strings.ContainsRune(l, '─') { // a tile line or the footer rule, never an inbox row
				continue
			}
			if _, yes := isHeader(l); yes {
				continue
			}
			if strings.Contains(l, needle) {
				at, row = i, strings.TrimRight(l, " ")
				break
			}
		}
		if at < 0 {
			t.Fatalf("no inbox row names %q:\n%s", needle, screen)
		}
		// The nearest header ABOVE the row is what a person reads it under.
		above := ""
		for i := at - 1; i >= 0; i-- {
			if h, yes := isHeader(body[i]); yes {
				above = h
				break
			}
		}
		mine := groupLabelOf(p, project.Aliases{}, ByHost, nil)
		if above == mine {
			continue // the header speaks for this row
		}
		if !strings.Contains(row, p.Host+"/") {
			t.Errorf("the row %q sits under the header %q, which belongs to another session, and "+
				"the row does not say it is on %q — so the screen reads as a duplicate of that "+
				"session.\nits own group would be %q\nwhole screen:\n%s",
				row, above, p.Host, mine, screen)
		}
	}
}

// The pane-less row is the shape that had a SECOND copy of the row-building code, so it gets its
// own assertion beside the invariant: a fleet whose rows are all pane-less satisfies the invariant
// vacuously (no header is ever drawn), and that is exactly the fleet the door produces.
func TestAPaneLessRowSaysWhichHostItIsOn(t *testing.T) {
	paneLess := registry.Pane{
		Kind: registry.KindAgent, Command: "interactive", Host: "nuc",
		Session: "frontend-postmortem", SessionID: "9f9f9f9f-0000",
		PaneID: "agent:9f9f9f9f@5e6f7a8b", ClassifiedState: state.Idle,
		Content: []string{"  (no pane)"},
	}
	m := base(t, 120, 24, paneLess)
	row := inboxRow(t, m.View(), "frontend-postmortem")
	if !strings.Contains(row, "nuc/frontend-postmortem") {
		t.Errorf("a row no header speaks for does not say which host it is on: %q", row)
	}
}

// interleavedFleet is a header's run broken by rows that do not belong to it: two panes of one
// session with singletons between them, which is what attention ordering produces and what the
// operator was reading as a duplicate.
func interleavedFleet(groups int) []registry.Pane {
	out := make([]registry.Pane, 0, groups*4)
	for g := 0; g < groups; g++ {
		sess := fmt.Sprintf("pair%d", g)
		a := agentPane("local", sess, "one", fmt.Sprintf("%%%02d0", g), 0, state.Quiet, "a")
		b := agentPane("local", sess, "two", fmt.Sprintf("%%%02d1", g), 1, state.Quiet, "b")
		s1 := agentPane("nuc", fmt.Sprintf("lone%02da", g), "w", fmt.Sprintf("%%%02d2", g), 2, state.Idle, "c")
		s2 := agentPane("nuc", fmt.Sprintf("lone%02db", g), "w", fmt.Sprintf("%%%02d3", g), 3, state.Idle, "d")
		out = append(out, a, s1, s2, b)
	}
	return out
}

// The break row is drawn where the group's run ENDS, and not at the top of the list where there is
// no header above it to be separated from.
func TestABreakSeparatesAHeaderlessRowFromTheGroupAbove(t *testing.T) {
	f := Frame{Panes: interleavedFleet(1), Width: 120, Height: 24, Aliases: project.Aliases{}}
	body := strings.Split(Render(f), "\n")

	var iHeader, iBreak, iLone = -1, -1, -1
	for i, l := range body {
		switch {
		case strings.Contains(l, "LOCAL PAIR0") && iHeader < 0:
			iHeader = i
		case strings.Contains(l, "other sessions") && iBreak < 0:
			iBreak = i
		case strings.Contains(l, "nuc/lone00a") && iLone < 0:
			iLone = i
		}
	}
	if iHeader < 0 || iBreak < 0 || iLone < 0 {
		t.Fatalf("header=%d break=%d lone=%d — one of the three is missing:\n%s",
			iHeader, iBreak, iLone, Render(f))
	}
	if !(iHeader < iBreak && iBreak < iLone) {
		t.Errorf("the break is not between the header and the row that left its group: "+
			"header=%d break=%d lone=%d\n%s", iHeader, iBreak, iLone, Render(f))
	}

	// AND NOT at the top: a fleet whose first row is headerless has no header on screen above it,
	// so a break there would separate it from nothing. extraAbove makes the same exception, and if
	// the two disagree the viewport counts a row the renderer did not draw.
	lone := agentPane("nuc", "alone", "w", "%9", 9, state.Idle, "x")
	top := Render(Frame{Panes: []registry.Pane{lone}, Width: 120, Height: 24, Aliases: project.Aliases{}})
	if strings.Contains(top, "other sessions") {
		t.Errorf("a break was drawn at the top of the list, above the first row:\n%s", top)
	}
}

// The break costs a screen row, so the viewport has to count it. This is the assertion that failed
// when the same arithmetic was an ESTIMATE: `A` claimed twelve panes on a screen showing twenty-five.
func TestTheBreakRowIsCountedByTheViewport(t *testing.T) {
	for _, height := range []int{10, 14, 18, 24, 30, 40} {
		panes := interleavedFleet(12)
		for _, cursor := range []int{0, 5, 20, 47} {
			f := Frame{Panes: panes, Width: 120, Height: height, Cursor: cursor,
				Aliases: project.Aliases{}}
			first, count := InboxViewport(f)
			out := Render(f)

			drawn := 0
			for _, p := range panes {
				if drawsRow(out, rowNeedle(p, panes)) {
					drawn++
				}
			}
			if count != drawn {
				t.Errorf("height %d cursor %d: the viewport claims %d rows and the renderer drew "+
					"%d — A would act on rows nobody can see:\n%s", height, cursor, count, drawn, out)
			}
			for i := first; i < first+count; i++ {
				if !drawsRow(out, rowNeedle(panes[i], panes)) {
					t.Fatalf("height %d cursor %d: the viewport claims pane %d (%s) is on screen "+
						"and it is not:\n%s", height, cursor, i, panes[i].PaneID, out)
				}
			}
		}
	}
}

// The LAST row of the list is a ROW, never a prefix waiting for one.
//
// Found by an adversarial review of the break, and it turned out to be one case of two: a header can
// be orphaned the same way, which predates the break. Measured on the pre-fix tree with three panes
// at 120 columns — pair first, then a singleton: at height 6 the last line was `┄┄ other sessions ┄┄`
// with nothing under it, and with the singleton first, at height 4 it was `LOCAL PAIR`. Both announce
// sessions that are not on the screen, and the second one had been shipping for weeks.
//
// Walked over BOTH orderings and every height that can hold a body, because which prefix lands on the
// cutoff depends on where the group sits in the attention order — one fixture proves one of the two.
func TestTheLastRowOfTheListIsNeverAPrefixWithoutItsRow(t *testing.T) {
	pairA := agentPane("local", "pair", "one", "%00", 0, state.Quiet, "a")
	pairB := agentPane("local", "pair", "two", "%01", 1, state.Quiet, "b")
	lone := agentPane("nuc", "lonely", "w", "%02", 2, state.Idle, "c")
	for _, order := range [][]registry.Pane{
		{pairA, pairB, lone}, // the break lands on the cutoff
		{lone, pairA, pairB}, // the header does
	} {
		for h := 4; h <= 16; h++ {
			for _, cursor := range []int{0, 1, 2} {
				f := Frame{Panes: order, Width: 120, Height: h, Cursor: cursor,
					Aliases: project.Aliases{}}
				last := ""
				for _, l := range strings.Split(Render(f), "\n") {
					s := strings.TrimRight(l, " ")
					// The inbox only: every tile line and the footer rule carry the box rule, which
					// no row draws, and the title line is the header of the screen rather than a row.
					if s == "" || strings.ContainsRune(s, '─') || strings.ContainsRune(s, '│') ||
						strings.HasPrefix(s, "tmux-hub") {
						continue
					}
					last = s
				}
				if strings.Contains(last, "other sessions") {
					t.Errorf("height %d cursor %d order %s: the list ends with a BREAK and no row "+
						"under it, so it promises sessions that are not on the screen:\n%s",
						h, cursor, order[0].Session, Render(f))
				}
				if g := groupLabelOf(order[1], project.Aliases{}, ByHost, nil); last == g ||
					last == groupLabelOf(order[0], project.Aliases{}, ByHost, nil) {
					t.Errorf("height %d cursor %d order %s: the list ends with the HEADER %q and no "+
						"row under it:\n%s", h, cursor, order[0].Session, last, Render(f))
				}
			}
		}
	}
}

// TWO ROWS THAT DRAW THE SAME LABEL MUST NOT DRAW THE SAME LINE.
//
// Measured on the operator's own fleet, and it had been true for as long as pane-less rows have
// existed: two Claude sessions on `local` both called `20260818--cicd`, both `⚑ needs`, both
// `background`, different uuids (`1b0cacf2`, `30f3382b`) — and neither row carried an id, because an
// agent row's id was never drawn at all. Two sessions asking for input, indistinguishable, and `a`
// or `K` acts on whichever one the cursor happens to be on.
//
// The old rule keyed the id on `(host, session)` multiplicity, which is a proxy for "would these read
// the same" and is wrong in the direction that HIDES a collision. Keying on the drawn label fixes it,
// and the id an agent row shows is the SHORT Claude id — the one `claude logs` takes and the one the
// door appends to the session it creates.
func TestTwoRowsWithOneLabelAreToldApartOnTheRow(t *testing.T) {
	twin := func(id, uuid string, st state.State) registry.Pane {
		return registry.Pane{
			Kind: registry.KindAgent, Command: "background", Host: "local",
			Session: "20260818--cicd", AgentID: id, SessionID: uuid,
			PaneID: "agent:" + id + "@ee42d26c", ClassifiedState: st,
			Content: []string{"  (no pane)"},
		}
	}
	a := twin("1b0cacf2", "1b0cacf2-aaaa", state.Needs)
	b := twin("30f3382b", "30f3382b-bbbb", state.Needs)
	// AND A THIRD SHAPE, which is the one the live fleet actually had: `claude agents` reports NO job
	// id for an `interactive` record, so this twin has an empty AgentID and its identity has to come
	// from the uuid. The first version of this rule shipped without the fallback and produced exactly
	// one row of a colliding pair carrying an id, which is worse than neither.
	noJobID := twin("", "a112a9b8-cccc", state.Idle)
	noJobID.Command = "interactive"
	noJobID.PaneID = "agent:a112a9b8@ee42d26c"
	// A THIRD row whose label nothing else draws: it must stay clean, or the rule has become
	// "always show the id" and the operator is back to reading %N on every line.
	alone := registry.Pane{
		Kind: registry.KindAgent, Command: "interactive", Host: "local",
		Session: "20260818--alone", AgentID: "9f9f9f9f", SessionID: "9f9f9f9f-cccc",
		PaneID: "agent:9f9f9f9f@dddd", ClassifiedState: state.Idle,
		Content: []string{"  (no pane)"},
	}

	// Both bands: the collision is the same at 80 columns, where the inline shape had its own copy
	// of the row and dropped the id entirely.
	for _, w := range []int{80, 120} {
		m := base(t, w, 24, a, b, noJobID, alone)
		screen := m.View()
		for _, p := range []registry.Pane{a, b, noJobID} {
			want := p.AgentID
			if want == "" {
				want = "a112a9b8" // the head of its uuid, which is all this record has
			}
			if !drawsRow(screen, want) {
				t.Errorf("width %d: %s shares its label with another row and does not say which "+
					"session it is — `a` and `K` cannot be aimed:\n%s", w, want, screen)
			}
		}
		if drawsRow(screen, alone.AgentID) {
			t.Errorf("width %d: %s is the only row with its label and carries an id anyway, so the "+
				"rule is not 'only when it disambiguates':\n%s", w, alone.AgentID, screen)
		}
	}
}

// collidingLabels keys on what the row DRAWS, and this is the cell where that differs from keying on
// the tmux session — without it, reverting the key to `(host, session)` passes every other test.
//
// Two rows, one label, two sessions: a pane joined to the Claude session `orders` (so its row reads
// by `orders`, not by the tmux session `orders-30f3382b` the door made) beside a pane-less Claude
// session of the same name that the join did not absorb. `(host, session)` sees two different
// sessions and draws no id; the screen draws one label twice.
func TestTheCollisionRuleKeysOnTheDrawnLabelAndNotTheSession(t *testing.T) {
	joined := agentPane("local", "orders-30f3382b", "w", "%5", 5, state.Quiet, "joined")
	joined.AgentName = "orders"
	orphan := registry.Pane{
		Kind: registry.KindAgent, Command: "background", Host: "local",
		Session: "orders", AgentID: "75e74e49", SessionID: "75e74e49-aaaa",
		PaneID: "agent:75e74e49@bbbb", ClassifiedState: state.Needs,
		Content: []string{"  (no pane)"},
	}
	fleet := []registry.Pane{joined, orphan}

	colliding := collidingLabels(fleet, project.Aliases{})
	if !colliding[[2]string{"local", "orders"}] {
		t.Fatalf("two rows draw `local/orders` and the rule does not see a collision: %v", colliding)
	}
	if colliding[[2]string{"local", "orders-30f3382b"}] {
		t.Errorf("the rule is keying on the tmux session, which no row draws: %v", colliding)
	}
	// And on the screen: each row says which of the two it is, in the id its own kind is keyed by.
	screen := base(t, 120, 24, fleet...).View()
	for _, want := range []string{"%5", "75e74e49"} {
		if !drawsRow(screen, want) {
			t.Errorf("two rows read `local/orders` and %q is not on either of them:\n%s", want, screen)
		}
	}
}
