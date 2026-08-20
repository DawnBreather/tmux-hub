package registry

import (
	"testing"
	"time"

	"github.com/DawnBreather/tmux-hub/internal/agents"
	"github.com/DawnBreather/tmux-hub/internal/state"
	"github.com/DawnBreather/tmux-hub/internal/tmux"
)

// A Claude session can be listed by SEVERAL hosts, and then it is still one session.
//
// Measured on this fleet, 2026-08-17: `~/.claude` is shared between `local` and `side-desk` —
// `~/.claude/daemon/roster.json` and `~/.claude/jobs/<id>/state.json` are byte-identical on both
// (same md5) — so `claude agents --json --all` returns the SAME 26 sessions on each, with identical
// `sessionId`, `startedAt` and `cwd`. Keyed per host, every background session became TWO rows: 26
// sessions produced 52 rows, and a LIVE one showed two contradictory states, because liveness is
// decided per machine from `roster.json`'s `workers[<id>].pid` and that pid exists on one host only.
//
//	agent:30f3382b@ee42d26c  local        state failed   ← no such pid here
//	agent:30f3382b@ee42d26c  side-desk    state working  ← the worker is here
//
// The dedup is not a heuristic: `agentRowID` hashes the short id, Claude's kind and the cwd and is
// host-independent by construction, so two AGENT rows sharing a PaneID are the same session.

func sess(id, name, kind, cwd, word string, started time.Time) agents.Session {
	return agents.Session{
		ID: id, SessionID: id + "-f68c-4baf-98fd-68d4fd1c3da4", Name: name, Kind: kind,
		CWD: cwd, State: word, StartedAt: started,
	}
}

func agentRows(r *Registry) []Pane {
	var out []Pane
	for _, p := range r.Panes() {
		if p.Kind == KindAgent {
			out = append(out, p)
		}
	}
	return out
}

func TestASessionListedByTwoHostsIsOneRow(t *testing.T) {
	now := time.Unix(1786450000, 0)
	one := []agents.Session{sess("30f3382b", "20260817-cicd", "background", "/w/iac", "blocked", now)}

	r := New()
	r.SetHostOrder([]string{"local", "side-desk"})
	r.UpdateAgents("local", one, now)
	r.UpdateAgents("side-desk", one, now)

	rows := agentRows(r)
	if len(rows) != 1 {
		for _, p := range rows {
			t.Logf("  row %s %s %v", p.Host, p.PaneID, p.ClassifiedState)
		}
		t.Fatalf("%d rows for one session — a shared ~/.claude makes every host report every "+
			"session, so the operator sees each one once per host", len(rows))
	}
	if rows[0].AgentID != "30f3382b" || rows[0].Session != "20260817-cicd" {
		t.Errorf("the surviving row is not the session: %+v", rows[0])
	}
}

// Which host the row is attributed to is not cosmetic: it is where the door will knock. A `failed`
// claim is the one word a host produces about ITS OWN ignorance — every other word comes from the
// shared store and is therefore identical everywhere — so it loses to any host that can see the
// session alive. Attributing the row to the `failed` host would send a wake to a machine with no
// worker while another machine is running one.
func TestAFailedClaimLosesToAHostThatSeesTheSessionAlive(t *testing.T) {
	now := time.Unix(1786450000, 0)
	dead := []agents.Session{sess("30f3382b", "20260817-cicd", "background", "/w/iac", "failed", now)}
	live := []agents.Session{sess("30f3382b", "20260817-cicd", "background", "/w/iac", "working", now)}

	for _, c := range []struct{ name, first, second string }{
		{"the failed host polls first", "local", "side-desk"},
		{"the live host polls first", "side-desk", "local"},
	} {
		r := New()
		r.SetHostOrder([]string{"local", "side-desk"})
		if c.first == "local" {
			r.UpdateAgents("local", dead, now)
			r.UpdateAgents("side-desk", live, now)
		} else {
			r.UpdateAgents("side-desk", live, now)
			r.UpdateAgents("local", dead, now)
		}
		rows := agentRows(r)
		if len(rows) != 1 {
			t.Fatalf("%s: %d rows", c.name, len(rows))
		}
		if rows[0].Host != "side-desk" {
			t.Errorf("%s: the row went to %q, but the worker is on side-desk — a wake sent to a "+
				"host with no worker would start a SECOND one against one transcript",
				c.name, rows[0].Host)
		}
		if rows[0].ClassifiedState != state.Works {
			t.Errorf("%s: the row reads %v, so the screen hides live work behind one host's "+
				"ignorance", c.name, rows[0].ClassifiedState)
		}
	}
}

// When every host says `failed` the session really is not running anywhere, the claims agree, and
// the row must still be ONE — attributed to the host the FLEET names first, which is the operator's
// own preference and puts the door next door rather than across an ssh leg.
//
// The order comes from the fleet, never from the order the listings arrived in, and the second half
// of this case is the one that matters: the hosts are polled CONCURRENTLY, so arrival order is a
// race. Measured on the real fleet before it was fixed — with rank learned on first sight, a remote
// host won all 26 shared rows while `local` was first in the fleet.
func TestWhenEveryHostSaysFailedTheRowTakesTheFleetsFirstHost(t *testing.T) {
	now := time.Unix(1786450000, 0)
	dead := []agents.Session{sess("30f3382b", "20260817-cicd", "background", "/w/iac", "failed", now)}

	for _, c := range []struct {
		name  string
		first string
	}{
		{"the fleet's first host answers first", "local"},
		{"the fleet's first host answers LAST", "side-desk"},
	} {
		r := New()
		r.SetHostOrder([]string{"local", "side-desk"})
		if c.first == "local" {
			r.UpdateAgents("local", dead, now)
			r.UpdateAgents("side-desk", dead, now)
		} else {
			r.UpdateAgents("side-desk", dead, now)
			r.UpdateAgents("local", dead, now)
		}
		rows := agentRows(r)
		if len(rows) != 1 {
			t.Fatalf("%s: %d rows", c.name, len(rows))
		}
		if rows[0].Host != "local" {
			t.Errorf("%s: host = %q, want the fleet's first host — the answer must not depend on "+
				"which concurrent poll finished first", c.name, rows[0].Host)
		}
	}
}

// A host the fleet does not name still has to produce a stable answer: two unranked hosts tie on
// rank, and without a further tiebreak `r.panes` being a map would decide the row differently on
// every tick.
func TestAnUnrankedHostDoesNotDecideTheRowByMapOrder(t *testing.T) {
	now := time.Unix(1786450000, 0)
	dead := []agents.Session{sess("30f3382b", "20260817-cicd", "background", "/w/iac", "failed", now)}

	for i := 0; i < 20; i++ {
		r := New()
		r.UpdateAgents("zulu", dead, now)
		r.UpdateAgents("alpha", dead, now)
		rows := agentRows(r)
		if len(rows) != 1 || rows[0].Host != "alpha" {
			t.Fatalf("run %d: %d rows, host %q — an unranked pair must answer the same every time",
				i, len(rows), rows[0].Host)
		}
	}
}

// The row names the other hosts that can reach the session, because the operator has to be able to
// tell a shared store from a machine of its own — and because the door's refusal says where else to
// try.
func TestTheRowNamesTheOtherHostsThatCanReachIt(t *testing.T) {
	now := time.Unix(1786450000, 0)
	one := []agents.Session{sess("30f3382b", "20260817-cicd", "background", "/w/iac", "blocked", now)}

	r := New()
	r.SetHostOrder([]string{"local", "side-desk", "nuc"})
	r.UpdateAgents("local", one, now)
	r.UpdateAgents("side-desk", one, now)
	r.UpdateAgents("nuc", one, now)

	rows := agentRows(r)
	if len(rows) != 1 {
		t.Fatalf("%d rows", len(rows))
	}
	if got := rows[0].AlsoOn; len(got) != 2 || got[0] != "side-desk" || got[1] != "nuc" {
		t.Errorf("AlsoOn = %v, want the other two claimants in the order the hub polled them", got)
	}
	// And a session only one host knows about claims nothing.
	r2 := New()
	r2.UpdateAgents("local", one, now)
	if got := agentRows(r2)[0].AlsoOn; len(got) != 0 {
		t.Errorf("a session one host listed reports AlsoOn = %v", got)
	}
}

// The control that keeps the dedup from eating the fleet: a PANE id is unique only within one tmux
// server, so `%1` exists on every host. Merging pane rows by PaneID would collapse the whole
// dashboard into one machine's panes.
func TestPaneRowsFromDifferentHostsAreNeverMerged(t *testing.T) {
	now := time.Unix(1786450000, 0)
	delta := []tmux.Delta{{PaneID: "%1", Activity: now.Unix(), PaneHeight: 24, WindowWidth: 80,
		PanePID: 7, SessionID: "$0", WindowID: "@1"}}
	labels := map[string]tmux.Labels{"%1": {Session: "work", Window: "w", Command: "claude"}}
	caps := []tmux.Capture{{PaneID: "%1", Height: 24, Lines: []string{"❯"}}}
	zones := map[string]tmux.Capture{"%1": {PaneID: "%1", Height: 24, Lines: []string{"❯"}}}

	r := New()
	r.UpdateAgents("local", nil, now)
	r.Update("local", delta, labels, caps, zones, now, time.Second)
	r.Update("nuc", delta, labels, caps, zones, now, time.Second)

	var panes int
	for _, p := range r.Panes() {
		if p.Kind == KindPane {
			panes++
		}
	}
	if panes != 2 {
		t.Fatalf("%d pane rows for two hosts each running %%1 — a pane id is unique per SERVER, "+
			"so the two are different panes", panes)
	}
}

// Two sessions that differ in cwd share neither key nor row, on one host or on several: agentRowID
// hashes the cwd exactly so they cannot collide, and the dedup must not undo that.
func TestTwoSessionsInDifferentDirectoriesStayTwoRows(t *testing.T) {
	now := time.Unix(1786450000, 0)
	two := []agents.Session{
		sess("30f3382b", "20260817-cicd", "background", "/w/iac", "blocked", now),
		sess("30f3382b", "20260817-cicd", "background", "/w/other", "blocked", now),
	}

	r := New()
	r.SetHostOrder([]string{"local", "side-desk"})
	r.UpdateAgents("local", two, now)
	r.UpdateAgents("side-desk", two, now)

	if got := len(agentRows(r)); got != 2 {
		t.Errorf("%d rows for two sessions sharing an id and a name but not a cwd", got)
	}
}

// A background record and an interactive one for the SAME conversation are one row, and the shared
// store does not change that: the fold happens per host, before the cross-host dedup, so each host
// contributes one row and the dedup then makes those one.
//
// This case asserted the opposite when it was written, on §22.11's ruling that `Kind` in the key keeps
// them apart. The key still does — TestTheKeySeparatesABackgroundRecordFromAnInteractiveOne — and the
// records are folded before they reach it, because the operator reported the pair as a duplicate and
// the interactive twin carries no id, no state and no door.
func TestABackgroundRecordAndItsInteractiveTwinAreOneRowOnEveryHost(t *testing.T) {
	now := time.Unix(1786450000, 0)
	two := []agents.Session{
		sess("3ec21f39", "20260817-cicd", "background", "/w/iac", "done", now),
		sess("3ec21f39", "20260817-cicd", "interactive", "/w/iac", "idle", now),
	}
	// sess() derives the uuid from the id, so both records carry one sessionId — which is what makes
	// them one conversation rather than two.
	if two[0].SessionID != two[1].SessionID {
		t.Fatalf("the fixture's records are not one conversation: %q vs %q",
			two[0].SessionID, two[1].SessionID)
	}

	r := New()
	r.SetHostOrder([]string{"local", "side-desk"})
	r.UpdateAgents("local", two, now)
	r.UpdateAgents("side-desk", two, now)

	rows := agentRows(r)
	if len(rows) != 1 {
		for _, p := range rows {
			t.Logf("  %s %s kind=%q", p.Host, p.PaneID, p.Command)
		}
		t.Fatalf("%d rows for one conversation reported twice by two hosts", len(rows))
	}
	if rows[0].Command != "background" {
		t.Errorf("the surviving row is the twin without an id: kind %q", rows[0].Command)
	}
}
