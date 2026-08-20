package registry

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"
	"time"

	"github.com/DawnBreather/tmux-hub/internal/state"
)

// SortByAttention compares rows by a state it computes ONCE, before the sort.
//
// `Pane.State()` reads the wall clock — `time.Since(p.AgentSeenAt)` — and this file already states
// the rule at StateSince's own comment: a value computed from `now` must not be tracked. A
// comparator that re-reads the clock can see a row cross the freshness boundary between two
// comparisons, and `sort.Slice` with a comparator that changes its mind is not merely wrongly
// ordered: it is undefined, free to panic or to scramble the slice.
//
// Until the pane-to-session join was wired, `AgentSeenAt` was always zero — nothing called
// SetClaudeSession — so the comparator was deterministic BY ACCIDENT. Wiring the join removes that
// accident, which is why the snapshot lands in the same commit.

func fresh(host, id string, classified, agent state.State, seen time.Time) Pane {
	return Pane{
		Kind: KindPane, Host: host, PaneID: id, Session: "s", Window: "w",
		ClassifiedState: classified, AgentState: agent, AgentSeenAt: seen,
		ClaudeSession: id + "-session",
	}
}

// The behaviour the snapshot has to preserve: a fresh agent fact wins over the pixels, and a stale
// one yields — decided at the ONE instant the sort was started at.
func TestTheSortUsesTheStateAtOneInstant(t *testing.T) {
	now := time.Unix(1786450000, 0)
	// `works` classified, `needs` reported. Fresh, the row must sort as needs (rank 0); stale, as
	// works. Nothing else distinguishes the two rows, so the rank decides the order outright.
	agentSays := fresh("local", "%1", state.Works, state.Needs, now.Add(-time.Minute))
	pixels := fresh("local", "%2", state.Idle, state.Idle, time.Time{})

	rows := []Pane{pixels, agentSays}
	sortByAttentionAt(rows, now)
	if rows[0].PaneID != "%1" {
		t.Errorf("with a FRESH agent fact the reported `needs` must lead; got %q first", rows[0].PaneID)
	}

	// The same fixture, read well past the freshness window: the agent fact is no longer
	// authoritative, the row is `works` (rank below idle), and the order inverts.
	rows = []Pane{pixels, agentSays}
	sortByAttentionAt(rows, now.Add(agentStateFreshness+time.Second))
	if rows[0].PaneID != "%2" {
		t.Errorf("with a STALE agent fact the classified `idle` must lead; got %q first", rows[0].PaneID)
	}
}

// The boundary itself, so the comparison is `<` and not `<=` by accident.
//
// The relative pair below proves `<` over `<=`, and it CANNOT see the window being widened: both
// fixtures are offsets of `agentStateFreshness`, so raising the constant moves them with it. The
// absolute pair after it is what a widening edit has to break — 10m1s must read the pixels and 9m59s
// the listing, with the numbers written out rather than derived. A review found the omission.
func TestTheFreshnessBoundaryIsExclusive(t *testing.T) {
	now := time.Unix(1786450000, 0)
	p := fresh("local", "%1", state.Works, state.Needs, now.Add(-agentStateFreshness))
	if got := p.stateAt(now); got != state.Works {
		t.Errorf("a fact exactly agentStateFreshness old is %v; it must have expired to the "+
			"classification", got)
	}
	p.AgentSeenAt = now.Add(-agentStateFreshness + time.Nanosecond)
	if got := p.stateAt(now); got != state.Needs {
		t.Errorf("a fact one nanosecond inside the window is %v, want the agent's own report", got)
	}

	// The literals, so widening the window is a test change and not a silent one. Ten minutes is
	// what §17's measurement bought: the CLI was seen 30 minutes stale, and a fact that old is
	// worse than a live classification.
	for _, c := range []struct {
		age  time.Duration
		want state.State
		why  string
	}{
		{10*time.Minute + time.Second, state.Works, "a fact older than ten minutes yields to the pixels"},
		{9*time.Minute + 59*time.Second, state.Needs, "a fact inside ten minutes is authoritative"},
	} {
		q := fresh("local", "%9", state.Works, state.Needs, now.Add(-c.age))
		if got := q.stateAt(now); got != c.want {
			t.Errorf("a %s-old fact reads %v, want %v — %s", c.age, got, c.want, c.why)
		}
	}
}

// State() must keep answering for every existing caller — it is the public accessor and the whole
// UI reads it — so it is stateAt(now) and nothing else.
func TestStateStillAnswersForLiveCallers(t *testing.T) {
	p := fresh("local", "%1", state.Works, state.Needs, time.Now().Add(-time.Second))
	if got := p.State(); got != state.Needs {
		t.Errorf("State() = %v with a one-second-old agent fact, want needs", got)
	}
	p.AgentSeenAt = time.Time{}
	if got := p.State(); got != state.Works {
		t.Errorf("State() = %v with no agent fact, want the classification", got)
	}
}

// The mechanical half, and the one that survives a refactor: the comparator's own body must not
// read the clock. A behavioural test cannot state this — a wall-clock comparator gives the right
// answer almost always, and "almost always" is what makes the defect ship — so the guard reads the
// source of the function under test and refuses any clock call inside the sort.
func TestTheComparatorReadsNoClock(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "registry.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var checked int
	var offenders []string
	ast.Inspect(f, func(n ast.Node) bool {
		fn, ok := n.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "sortByAttentionAt" || fn.Body == nil {
			return true
		}
		checked++
		ast.Inspect(fn.Body, func(in ast.Node) bool {
			call, ok := in.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			pkg, ok := sel.X.(*ast.Ident)
			if !ok {
				return true
			}
			// time.Now and time.Since are the two DIRECT doors to the wall clock. A duration
			// constant or a comparison between two stored times is fine and common here.
			if pkg.Name == "time" && (sel.Sel.Name == "Now" || sel.Sel.Name == "Since") {
				offenders = append(offenders,
					fset.Position(call.Pos()).String()+": time."+sel.Sel.Name)
			}
			return true
		})
		// And the INDIRECT door, which is the one a rewrite actually takes: `State()` reads
		// time.Now inside itself, so a comparator calling it reads the clock while this file's
		// AST shows no `time.` at all. Calibrated — restoring `p.State()` here is exactly the
		// mutant that proved the direct check alone was blind to it.
		ast.Inspect(fn.Body, func(in ast.Node) bool {
			call, ok := in.(*ast.CallExpr)
			if !ok {
				return true
			}
			if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "State" {
				offenders = append(offenders,
					fset.Position(call.Pos()).String()+": .State(), which reads time.Now")
			}
			return true
		})
		return false
	})
	if checked != 1 {
		t.Fatalf("found %d functions named sortByAttentionAt; the guard is not reading the "+
			"function it is named for", checked)
	}
	if len(offenders) > 0 {
		t.Errorf("the comparator reads the wall clock at %s — it must compare the state "+
			"snapshotted before the sort, or it stops being a strict weak ordering",
			strings.Join(offenders, ", "))
	}
	// The other direction, so this cannot pass by reading nothing: State() DOES read the clock,
	// and the same walk must see it.
	var stateReadsClock bool
	ast.Inspect(f, func(n ast.Node) bool {
		fn, ok := n.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "State" || fn.Body == nil {
			return true
		}
		ast.Inspect(fn.Body, func(in ast.Node) bool {
			if sel, ok := in.(*ast.SelectorExpr); ok {
				if pkg, ok := sel.X.(*ast.Ident); ok && pkg.Name == "time" && sel.Sel.Name == "Now" {
					stateReadsClock = true
				}
			}
			return true
		})
		return false
	})
	if !stateReadsClock {
		t.Error("the control failed: State() no longer calls time.Now, so this guard would " +
			"pass against a file it cannot read")
	}
}
