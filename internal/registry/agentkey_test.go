package registry

import (
	"strings"
	"testing"
	"time"

	"github.com/DawnBreather/tmux-hub/internal/agents"
)

// MEASURED on the operator's own fleet, and this is a lost row rather than a cosmetic
// clash: `claude agents --json` on one host reported TWO different sessions carrying the
// same `sessionId` — same `pid`, same `startedAt`, different `cwd` and different `name`,
// and NEITHER carried an `id` field. `agents.Parse` therefore back-fills both ids from
// `SessionID[:8]`, and keying a row on that id alone put both records on one `*Pane`,
// where the second overwrote the first's Session, Command, SessionID, Activity and
// ClassifiedState. End to end through the product: 12 sessions in, 11 rows out, and the
// survivor was the second one — the first was invisible on the dashboard.
//
// The 8-character truncation is NOT the cause: the full ids are identical too. So
// `(host, sessionId)` genuinely collides, and `cwd` is what separates them —
// `(host, sessionId, cwd)` was unique for 21 of 21 rows under bare `--json` where `(host, sessionId)` was
// unique for only 20.
//
// A row the operator never sees cannot be attended to, and nothing waiting going unseen
// is the whole purpose of the hub.
func TestTwoSessionsSharingAnIDAreTwoRows(t *testing.T) {
	now := time.Now()
	// Both records exactly as measured: one id, one pid, one startedAt, two cwds.
	ss := []agents.Session{
		{ID: "5a485bc4", SessionID: "5a485bc4-4f01-4690-bbd4-29d42779a154",
			Kind: "interactive", Name: "visualized-explanation",
			CWD: "/home/dev/lab/jira-tickets/OPSPROJ-4969-DR-plan-verification"},
		{ID: "5a485bc4", SessionID: "5a485bc4-4f01-4690-bbd4-29d42779a154",
			Kind: "interactive", Name: "nuc side of st",
			CWD: "/home/dev/lab/streams/st/frontend"},
	}
	r := New()
	r.UpdateAgents("nuc", ss, now)
	rows := r.Panes()

	if len(rows) != 2 {
		var names []string
		for _, p := range rows {
			names = append(names, p.Session)
		}
		t.Fatalf("registry holds %d rows from 2 sessions %v — one session is invisible "+
			"on the dashboard, and a row nobody sees cannot be answered", len(rows), names)
	}

	// Assert the CONSEQUENCE, not the key's shape: both names must survive, so a future
	// key change that merges them differently still fails here.
	seen := map[string]bool{}
	for _, p := range rows {
		seen[p.Session] = true
	}
	for _, want := range []string{"visualized-explanation", "nuc side of st"} {
		if !seen[want] {
			t.Errorf("session %q is missing from the fleet", want)
		}
	}
}

// The normal case must not gain a duplicate from the fix: one session, one row, however
// many times it is reported.
func TestOneSessionStaysOneRowAcrossPolls(t *testing.T) {
	now := time.Now()
	s := agents.Session{ID: "3d392b30", SessionID: "3d392b30-1a0a-4b95-b1bb-55fa203eb0d4",
		Kind: "background", Name: "rca-and-reproducing", CWD: "/home/dev/lab/streams/st"}
	r := New()
	r.UpdateAgents("nuc", []agents.Session{s}, now)
	r.UpdateAgents("nuc", []agents.Session{s}, now.Add(time.Second))
	if got := len(r.Panes()); got != 1 {
		t.Errorf("one session reported twice produced %d rows, want 1 — the key is not "+
			"stable across polls, so every tick would grow the fleet", got)
	}
}

// A session with no cwd at all must still get a row rather than colliding with every
// other cwd-less session on the host.
func TestSessionsWithNoCWDDoNotCollapseIntoOne(t *testing.T) {
	now := time.Now()
	ss := []agents.Session{
		{ID: "aaaaaaaa", SessionID: "aaaaaaaa-0000-0000-0000-000000000000", Name: "first"},
		{ID: "bbbbbbbb", SessionID: "bbbbbbbb-0000-0000-0000-000000000000", Name: "second"},
	}
	r := New()
	r.UpdateAgents("local", ss, now)
	if got := len(r.Panes()); got != 2 {
		t.Errorf("two cwd-less sessions produced %d rows, want 2", got)
	}
}

// An agent row's project comes from Claude's own `cwd`, and 32 of 45 rows under `--all` on
// 2026-08-15 have no pane at all (docs/design.md §21.1, §22.1). So a row
// whose Path is empty lands in the unassigned bucket that §21.2 pins LAST, and the screen
// built to show what wants the operator shows none of it.
//
// This test goes through agents.Parse and UpdateAgents rather than hand-building a Pane,
// because a hand-built row CANNOT see this class: every project fixture set `Path` itself,
// so every one of them passed while the producer never wrote the field. That is the third
// time this repository has shipped the same shape — a loader that forgets a field passes
// every test that hand-builds the struct.
func TestAnAgentRowCarriesClaudesWorkingDirectory(t *testing.T) {
	raw := []byte(`[
	  {"sessionId":"aaaaaaaa-1111-4000-8000-000000000001","kind":"background",
	   "name":"st migration","cwd":"/home/dev/lab/streams/st","state":"blocked"},
	  {"sessionId":"bbbbbbbb-2222-4000-8000-000000000002","kind":"background",
	   "name":"maps","cwd":"/home/dev/lab/streams/maps","state":"working"}
	]`)
	ss, err := agents.Parse(raw)
	if err != nil {
		t.Fatalf("agents.Parse: %v", err)
	}
	if len(ss) != 2 {
		t.Fatalf("parsed %d sessions, want 2", len(ss))
	}
	r := New()
	r.UpdateAgents("nuc", ss, time.Now())

	byName := map[string]Pane{}
	for _, p := range r.Panes() {
		byName[p.Session] = p
	}
	for name, want := range map[string]string{
		"st migration": "/home/dev/lab/streams/st",
		"maps":         "/home/dev/lab/streams/maps",
	} {
		got, ok := byName[name]
		if !ok {
			t.Fatalf("session %q is missing from the fleet", name)
		}
		if got.Path != want {
			t.Errorf("%q Path = %q, want %q — without it every pane-less row is "+
				"unassigned, which is the bucket the project list pins last",
				name, got.Path, want)
		}
	}
}

// §22.11's ruling, at the level it belongs to: the KEY separates a background record from an
// interactive one that shares its id and cwd. That is what stopped 31 rows becoming 30 keys and losing
// one of the operator's sessions.
//
// The ROWS no longer do, and the two facts are not in conflict. Such a pair is one conversation seen
// under two kinds — measured on the fleet, same sessionId, same cwd, same name — and the operator
// reported it as a duplicate, so `oneRecordPerConversation` folds the pair before it reaches the key,
// keeping the record with an ID because that is the one with a door, a state and an argument for every
// `claude` verb. The key still has to separate what the fold does NOT touch: two records under one
// sessionId with different names, and any future shape nobody has measured yet. A key that could not
// tell them apart would lose a row in silence, which is the failure this test exists for.
func TestTheKeySeparatesABackgroundRecordFromAnInteractiveOne(t *testing.T) {
	const cwd = "/w/project-hub"
	const uuid = "3ec21f39-ad9f-4083-91e8-d257cbb22b30"
	bg := agents.Session{SessionID: uuid, ID: "3ec21f39", Kind: "background",
		Name: "same name on purpose", CWD: cwd, State: "failed"}
	inter := bg
	inter.Kind, inter.State = "interactive", "idle"

	// Keyed on Kind, which is what `agentRowID` hashes beside the cwd — the two share a Name on
	// purpose, so a mutant hashing `s.Name` instead of `s.Kind` cannot pass.
	if a, b := agentRowID(bg), agentRowID(inter); a == b {
		t.Fatalf("both records key to %q, so one row would be unreachable", a)
	}

	// And the ROWS: one conversation, one row, and it is the one that can be acted on.
	r := New()
	r.UpdateAgents("local", []agents.Session{bg, inter}, time.Now())
	rows := r.Panes()
	if len(rows) != 1 {
		t.Fatalf("%d rows for one conversation: %+v", len(rows), rows)
	}
	if rows[0].Command != "background" || rows[0].AgentID != "3ec21f39" {
		t.Errorf("the surviving row is not the actionable one: kind %q id %q",
			rows[0].Command, rows[0].AgentID)
	}
	if rows[0].Path != cwd || rows[0].SessionID != uuid {
		t.Errorf("the surviving row lost its path or id: %q %q", rows[0].Path, rows[0].SessionID)
	}
}

// The row key needs a stable string even when the listing reports no short id, and that fallback
// lives HERE rather than in the producer.
//
// `agents.Parse` used to invent `ID` from the uuid's first 8 characters, which gave every consumer
// an 8-character look-alike that `claude attach|logs|stop` refuses. The invention moved into this
// key builder, where "stable" is the only requirement — so these are the tests that keep it stable.
func TestARowKeyIsStableWhenTheListingReportsNoShortId(t *testing.T) {
	now := time.Unix(1786450000, 0)
	mk := func() *Registry {
		r := New()
		r.UpdateAgents("nuc", []agents.Session{
			// Neither carries an `id`: this is 2.1.224's interactive shape, measured.
			{SessionID: "1ff133f7-c34a-4c60-91e5-b0048842cc66", Kind: "interactive",
				Name: "one", CWD: "/w/a", State: "idle"},
			{SessionID: "5a485bc4-4f01-4690-bbd4-29d42779a154", Kind: "interactive",
				Name: "two", CWD: "/w/b", State: "idle"},
		}, now)
		return r
	}
	rows := mk().Panes()
	if len(rows) != 2 {
		t.Fatalf("%d rows for two id-less sessions, want 2 — the key collapsed them", len(rows))
	}
	if rows[0].PaneID == rows[1].PaneID {
		t.Fatalf("two id-less sessions share one row key: %q", rows[0].PaneID)
	}
	for _, p := range rows {
		if p.AgentID != "" {
			t.Errorf("row %q carries a short id the listing never gave: %q — every verb that takes "+
				"one answers `No job matching` for it", p.Session, p.AgentID)
		}
		if !strings.HasPrefix(p.PaneID, "agent:") || len(p.PaneID) < 16 {
			t.Errorf("row key %q is not the agent shape", p.PaneID)
		}
	}
	// STABLE across ticks: the same input must produce the same key, or a hide mark, an alias and
	// the selection all follow the row to a different name every 20 seconds.
	again := mk().Panes()
	for i := range rows {
		if rows[i].PaneID != again[i].PaneID {
			t.Errorf("the key moved between two identical listings: %q then %q",
				rows[i].PaneID, again[i].PaneID)
		}
	}
}
