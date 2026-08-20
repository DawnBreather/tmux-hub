package ui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/DawnBreather/tmux-hub/internal/project"
	"github.com/DawnBreather/tmux-hub/internal/registry"
	"github.com/DawnBreather/tmux-hub/internal/state"
)

// The inbox groups by HOST and session, and `v` switches it to grouping by PROJECT — the same
// vocabulary §21's project list and aliases already use, so the two screens name the same things the
// same way.
//
// What it does NOT do is re-sort. The dashboard exists to put what wants the operator first, and a
// view that gathered each project's rows together would bury a waiting session inside a quiet
// project. So the rows keep their attention order and the HEADERS change, with the repeat marked
// `(cont.)` exactly as a session's header already is when the sort brings it round twice.

func groupingFixture(t *testing.T, cols int) model {
	t.Helper()
	iac := agentRow("aaaaaaaa", "iac-one", "background", state.Needs)
	iac.Path = "/w/billing-iac"
	other := agentRow("bbbbbbbb", "map-one", "background", state.Works)
	other.Path = "/w/render-map"
	p := pane("local", "win", "claude", 1, "claude", state.Idle)
	p.Path = "/w/billing-iac"
	// TWO panes in `sess1`, because a host header is what a session with SIBLINGS is for: a
	// session holding one row takes no header, since the row now carries the name the header
	// would have held. This test's discriminator between the two views is the header, so the
	// fixture has to be a fleet that has one.
	q := pane("local", "win", "bash", 2, "bash", state.Quiet)
	q.Path = "/w/billing-iac"
	return base(t, cols, 40, iac, other, p, q)
}

func TestVSwitchesTheInboxFromHostsToProjects(t *testing.T) {
	m := groupingFixture(t, 120)

	byHost := m.View()
	if !strings.Contains(byHost, "LOCAL SESS1") {
		t.Fatalf("the default view does not group by host and session:\n%s", byHost)
	}
	if strings.Contains(byHost, "BILLING-IAC") {
		t.Errorf("the host view already shows a project header:\n%s", byHost)
	}

	out, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("v")})
	byProject := out.(model).View()
	if !strings.Contains(byProject, "BILLING-IAC") {
		t.Errorf("`v` did not group by project:\n%s", byProject)
	}
	if strings.Contains(byProject, "LOCAL SESS1") {
		t.Errorf("the project view still carries a host+session header:\n%s", byProject)
	}

	// And back, because a view the operator cannot leave is a trap.
	out2, _ := out.(model).Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("v")})
	if !strings.Contains(out2.(model).View(), "LOCAL SESS1") {
		t.Error("a second `v` did not come back to the host view")
	}
}

// A mode that changes what a screen MEANS has to say so. Without it the operator reads a different
// set of headers and has to infer the view from them, which is the defect `X` had: a mode whose only
// evidence was the thing it changed.
func TestTheHeaderSaysWhichViewIsOn(t *testing.T) {
	m := groupingFixture(t, 120)
	if head := strings.SplitN(m.View(), "\n", 2)[0]; strings.Contains(head, "project") {
		t.Errorf("the default view announces itself as per-project: %q", head)
	}
	out, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("v")})
	head := strings.SplitN(out.(model).View(), "\n", 2)[0]
	if !strings.Contains(head, "per-project") {
		t.Errorf("the header does not say the view changed: %q", head)
	}
}

// `v` HAS TO CHANGE THE SCREEN at 80 columns, the size §16 commits to — and the way it does that
// changed, which is why this test's mechanism is worth stating.
//
// The inline shape (below 100 columns) draws no group headers, so the project view used to reach the
// ROW: it replaced `host/session` with the project label in the pane row's "where" column. Measured
// on a real fleet, that left the view invisible anyway, because an AGENT row has no "where" column at
// all — it is state plus name, deliberately, since the name needs every column it can get — and 40 of
// 43 rows on that fleet were agent rows. Rows of one project were not even adjacent.
//
// So the project view now takes the GROUPED shape at every width: one header per project, which is
// what the view was opened to ask. The assertion is therefore about the SCREEN and not about the row,
// and the label is matched case-insensitively because a header upper-cases a derived name — asserting
// the lower-case form would be asserting the surface's transformation rather than the label.
func TestTheProjectViewChangesTheScreenAtEightyColumns(t *testing.T) {
	m := groupingFixture(t, 80)
	byHost := m.View()
	if !strings.Contains(byHost, "local/sess1") {
		t.Fatalf("the inline row does not carry host/session in the HOST view:\n%s", byHost)
	}

	out, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("v")})
	byProject := out.(model).View()
	if !strings.Contains(strings.ToLower(byProject), "billing-iac") {
		t.Errorf("`v` changed nothing at 80 columns, the size the project commits to:\n%s", byProject)
	}
	// And the host/session pair is gone, because the grouped row does not carry it — the header does
	// the saying now, once per project instead of once per row.
	if strings.Contains(byProject, "local/sess1") {
		t.Errorf("the row still names the host and session in the project view:\n%s", byProject)
	}
	// The rows of one project are ADJACENT, which is the half a header cannot fake: a view that
	// labelled rows without gathering them would leave the operator scanning for them.
	var seen int
	for _, l := range strings.Split(byProject, "\n") {
		if strings.Contains(strings.ToUpper(l), "BILLING-IAC") {
			seen++
		}
	}
	if seen != 1 {
		t.Errorf("the project label appears %d times, want exactly one header:\n%s", seen, byProject)
	}
}

// The ORDER is untouched: the view relabels, it does not gather. A project view that grouped rows
// together would bury a waiting session inside a quiet project, which is the one thing the dashboard
// exists to prevent.
func TestTheProjectViewDoesNotReorderTheRows(t *testing.T) {
	m := groupingFixture(t, 120)
	order := func(m model) []string {
		var out []string
		for _, p := range m.rowsForScreen() {
			out = append(out, p.Session)
		}
		return out
	}
	before := order(m)
	out, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("v")})
	if after := order(out.(model)); strings.Join(after, ",") != strings.Join(before, ",") {
		t.Errorf("`v` reordered the rows: %v -> %v", before, after)
	}
}

// A project header comes round twice when the sort brings that project's rows round twice, and the
// repeat is MARKED — the same rule a session header already follows, because a repeat with nothing
// saying why reads as a bug (known-issues S2).
func TestARepeatedProjectHeaderIsMarked(t *testing.T) {
	// Two rows of one project with a row of another BETWEEN them by attention. The states are chosen
	// from the ranking rather than guessed — Needs, Error, Quiet, Idle, Works, Unknown, Done, Gone —
	// so `error` really does sort between `needs` and `idle`. A fixture whose middle row sorted LAST
	// produced one header and no repeat, which is a fixture that cannot test what it says.
	iac := agentRow("aaaaaaaa", "iac-one", "background", state.Needs)
	iac.Path = "/w/billing-iac"
	other := agentRow("bbbbbbbb", "map-one", "background", state.Error)
	other.Path = "/w/render-map"
	iac2 := agentRow("cccccccc", "iac-two", "background", state.Idle)
	iac2.Path = "/w/billing-iac"
	m := base(t, 120, 40, iac, other, iac2)
	m.groupBy = byProject

	screen := m.View()
	if n := strings.Count(screen, "BILLING-IAC"); n != 2 {
		t.Fatalf("the project header appears %d times, want 2 (the sort brings it round twice):\n%s",
			n, screen)
	}
	if !strings.Contains(screen, "(cont.)") {
		t.Errorf("the repeat is not marked, so it reads as a rendering bug:\n%s", screen)
	}
}

// A row whose project cannot be derived still gets a header rather than falling under the previous
// one — which would file it under somebody else's project.
func TestARowWithNoProjectGetsItsOwnHeader(t *testing.T) {
	iac := agentRow("aaaaaaaa", "iac-one", "background", state.Needs)
	iac.Path = "/w/billing-iac"
	nowhere := agentRow("bbbbbbbb", "nowhere", "background", state.Works)
	nowhere.Path = ""
	m := base(t, 120, 40, iac, nowhere)
	m.groupBy = byProject

	screen := m.View()
	if !strings.Contains(strings.ToLower(screen), "unassigned") {
		t.Errorf("a row with no path is not told where it landed:\n%s", screen)
	}
}

// A Frame built by hand with the project view and NO labels still says where each row landed.
//
// The model always supplies a label for every painted row — `groupLabels` walks the same set the
// renderer draws — so this path is unreachable from the product and reachable from every harness that
// builds a Frame directly, which is most of this package. A header of one blank space would read as a
// rendering fault, and the mockup would publish it.
func TestTheProjectViewNamesARowTheFrameDidNotLabel(t *testing.T) {
	rows := []registry.Pane{
		agentRow("aaaaaaaa", "iac-one", "background", state.Needs),
		pane("local", "win", "claude", 1, "claude", state.Idle),
	}
	for _, cols := range []int{80, 120} {
		screen := Render(Frame{Panes: rows, Width: cols, Height: 24, GroupBy: ByProject,
			Aliases: project.Aliases{}})
		if !strings.Contains(strings.ToLower(screen), "unassigned") {
			t.Errorf("%d cols: a row with no label is filed under nothing at all:\n%s", cols, screen)
		}
	}
}

// `v` on the project LIST does nothing to the list — its rows ARE projects — and says so rather than
// leaving the operator to wonder whether the key arrived.
func TestVOnTheProjectListSaysItIsAlreadyPerProject(t *testing.T) {
	m := groupingFixture(t, 120)
	m.mode = modeProjects
	out, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("v")})
	after := out.(model)
	if after.groupBy != byHost {
		t.Error("`v` on the project list changed the dashboard's view behind the operator's back")
	}
	if !strings.Contains(after.note, "per project already") {
		t.Errorf("`v` on the project list does not say why nothing happened: %q", after.note)
	}
	if !strings.Contains(after.note, "DASHBOARD") {
		t.Errorf("and it does not say where the key DOES work: %q", after.note)
	}
}
