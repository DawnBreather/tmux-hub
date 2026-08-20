package registry

import (
	"testing"
	"time"

	"github.com/DawnBreather/tmux-hub/internal/agents"
	"github.com/DawnBreather/tmux-hub/internal/tmux"
)

// One conversation must be one row, and it was three.
//
// Reported from real use: `xmap-universal-reader` appears twice. Measured on the fleet, the session
// `7ef2fe7e-c88d-46da-ac9d-0b57f170c3d8` produced THREE rows, for two independent reasons:
//
//	agent:7ef2fe7e@3e87e939  interactive  no id     ← the listing reports the conversation twice
//	agent:7ef2fe7e@50f83b0c  background   7ef2fe7e
//	%5  session 20260813--…-7ef2fe7e  sh -c 'claude attach 7ef2fe7e'  ← the pane the door made
//
// The first two are the SAME record seen under two kinds, with the same sessionId, cwd and name; the
// third is the pane the door created, which should have absorbed the listing row and did not, because
// the adoption that links them lived only in the Keeper's memory and the hub had been restarted.

func session(id, uuid, kind, name, cwd, word string, at time.Time) agents.Session {
	return agents.Session{ID: id, SessionID: uuid, Kind: kind, Name: name, CWD: cwd,
		State: word, StartedAt: at}
}

// ── one conversation, two listing records ─────────────────────────────────────────────────────────

// The listing reports one conversation twice — once as the background job and once as the interactive
// session in front of it — and the two carry the same sessionId, cwd and name. That is ONE thing to
// the operator, and the row that survives is the one with an id: it has a door, a state, and the
// argument every `claude` verb takes, while the interactive twin carries none of the three.
func TestOneConversationReportedTwiceIsOneRow(t *testing.T) {
	now := time.Unix(1786450000, 0)
	uuid := "7ef2fe7e-c88d-46da-ac9d-0b57f170c3d8"
	cwd := "/home/dev/lab/streams/experiments/xmap-reverse-engineering"
	name := "20260813--gis-offline-maps-universal-reader-emulator"

	for _, order := range []string{"interactive first", "background first"} {
		ss := []agents.Session{
			session("", uuid, "interactive", name, cwd, "", now),
			session("7ef2fe7e", uuid, "background", name, cwd, "working", now),
		}
		if order == "background first" {
			ss[0], ss[1] = ss[1], ss[0]
		}
		r := New()
		r.SetHostOrder([]string{"local"})
		r.UpdateAgents("local", ss, now)

		rows := r.Panes()
		if len(rows) != 1 {
			for _, p := range rows {
				t.Logf("  %s %s %q kind=%q id=%q", p.Kind, p.PaneID, p.Session, p.Command, p.AgentID)
			}
			t.Fatalf("%s: %d rows for one conversation", order, len(rows))
		}
		if rows[0].AgentID != "7ef2fe7e" {
			t.Errorf("%s: the surviving row carries no id (%q), so it has no door and no `claude "+
				"stop` argument", order, rows[0].AgentID)
		}
		if rows[0].Command != "background" {
			t.Errorf("%s: the surviving row's kind is %q", order, rows[0].Command)
		}
	}
}

// The CONTROL, and it is the case §22.11 measured: two records under one sessionId that are genuinely
// different sessions — same pid, same startedAt, DIFFERENT cwd and name. Those must both survive, and
// they are why the collapse cannot key on the sessionId alone.
func TestTwoDifferentSessionsUnderOneSessionIDBothSurvive(t *testing.T) {
	now := time.Unix(1786450000, 0)
	uuid := "7ef2fe7e-c88d-46da-ac9d-0b57f170c3d8"
	r := New()
	r.SetHostOrder([]string{"local"})
	r.UpdateAgents("local", []agents.Session{
		session("", uuid, "interactive", "one", "/w/one", "", now),
		session("", uuid, "interactive", "two", "/w/two", "", now),
	}, now)

	if got := len(r.Panes()); got != 2 {
		t.Errorf("%d rows for two sessions that share only a sessionId — this is the shape that "+
			"made 31 rows into 30 keys and lost one of the operator's sessions", got)
	}
}

// A record with no cwd and no name still cannot be folded into another one: the collapse must key on
// what it HAS, not on what two records both lack.
func TestRecordsWithNothingToCompareAreNotFoldedTogether(t *testing.T) {
	now := time.Unix(1786450000, 0)
	r := New()
	r.SetHostOrder([]string{"local"})
	r.UpdateAgents("local", []agents.Session{
		session("aaaaaaaa", "aaaaaaaa-0000", "background", "", "", "working", now),
		session("bbbbbbbb", "bbbbbbbb-0000", "background", "", "", "working", now),
	}, now)
	if got := len(r.Panes()); got != 2 {
		t.Errorf("%d rows: two records with different ids were folded together", got)
	}
}

// ── the door's pane, after a restart ─────────────────────────────────────────────────────────────

// doorPane is a pane the door created: named `<claude name>-<short id>` and running the wrapped verb.
// Both facts live on the tmux server, so both survive a hub restart — which the Keeper's adoption does
// not.
func doorPane() []tmux.Delta {
	return []tmux.Delta{{PaneID: "%5", Activity: 1786450000, PaneHeight: 24, WindowWidth: 80,
		PanePID: 7, SessionID: "$3", WindowID: "@7"}}
}

// doorStart is the start command the door leaves on the pane, quoted exactly as TMUX REPORTS IT —
// copied off the operator's own server, not written from imagination. The outer double quotes are
// tmux's own, added around any command containing a space, and the first version of this fixture
// lacked them: the code passed the test and did nothing on the fleet.
func doorStart(id string) string {
	return `"'sh' '-c' ''\''claude'\'' '\''attach'\'' '\''` + id +
		`'\''; s=$?; [ \"\$s\" -eq 0 ] || { printf x; read _; }'"`
}

func doorLabels(id, sessionName, start string) map[string]tmux.Labels {
	return map[string]tmux.Labels{"%5": {Session: sessionName, Window: "sh", Command: "sh",
		StartCommand: start}}
}

// The pane the door made absorbs its listing row WITHOUT the Keeper, because a restarted hub has no
// adoption and the operator then sees the session twice — once as the pane, once as the row.
//
// What identifies the pane is the verb it is running: `claude attach <short id>` is the door's own
// payload, and the id in it is the argument the door passed. That is a fact on the server, not in the
// hub's memory.
func TestTheDoorsPaneAbsorbsItsRowWithoutAnAdoption(t *testing.T) {
	now := time.Unix(1786450000, 0)
	uuid := "7ef2fe7e-c88d-46da-ac9d-0b57f170c3d8"
	name := "20260813--gis-offline-maps-universal-reader-emulator"

	r := New()
	r.SetHostOrder([]string{"local"})
	// A fresh hub: the pane exists, and NOTHING has told the registry what it is running.
	r.Update("local", doorPane(),
		doorLabels("7ef2fe7e", name+"-7ef2fe7e", doorStart("7ef2fe7e")),
		[]tmux.Capture{{PaneID: "%5", Height: 24, Lines: []string{"❯"}}},
		map[string]tmux.Capture{"%5": {PaneID: "%5", Height: 24, Lines: []string{"❯"}}},
		now, time.Second)
	r.UpdateAgents("local", []agents.Session{
		session("7ef2fe7e", uuid, "background", name, "/w/xmap", "working", now),
	}, now)

	rows := r.Panes()
	if len(rows) != 1 {
		for _, p := range rows {
			t.Logf("  %s %s %q", p.Kind, p.PaneID, p.Session)
		}
		t.Fatalf("%d rows: the pane the door made and the row it was made for are still two things",
			len(rows))
	}
	if rows[0].Kind != KindPane || rows[0].PaneID != "%5" {
		t.Errorf("the surviving row is not the pane: %+v", rows[0])
	}
	// And the row carries the session it is running, so everything keyed on the uuid — the pin, the
	// restart, the history — finds it.
	if rows[0].ClaudeSession != uuid {
		t.Errorf("the pane does not know which session it is running: %q", rows[0].ClaudeSession)
	}
	if rows[0].AgentName != name {
		t.Errorf("the pane did not take the session's name: %q", rows[0].AgentName)
	}
}

// A pane running something else entirely is not claimed. The match is on the door's own payload shape,
// so a pane that merely MENTIONS an id — a shell where somebody typed the command, a log tail — must
// not swallow a listing row.
func TestAPaneThatOnlyMentionsAnIDIsNotClaimed(t *testing.T) {
	now := time.Unix(1786450000, 0)
	uuid := "7ef2fe7e-c88d-46da-ac9d-0b57f170c3d8"
	for _, c := range []struct{ name, start string }{
		{"a shell that logged the id", "sh -c 'echo claude attach 7ef2fe7e >> notes.txt'"},
		{"a different verb", `'sh' '-c' ''\''claude'\'' '\''logs'\'' '\''7ef2fe7e'\'''`},
		{"a different id", `'sh' '-c' ''\''claude'\'' '\''attach'\'' '\''deadbeef'\'''`},
		{"no start command at all", ""},
	} {
		r := New()
		r.SetHostOrder([]string{"local"})
		r.Update("local", doorPane(),
			doorLabels("7ef2fe7e", "something-else", c.start),
			[]tmux.Capture{{PaneID: "%5", Height: 24, Lines: []string{"❯"}}},
			map[string]tmux.Capture{"%5": {PaneID: "%5", Height: 24, Lines: []string{"❯"}}},
			now, time.Second)
		r.UpdateAgents("local", []agents.Session{
			session("7ef2fe7e", uuid, "background", "the session", "/w/xmap", "working", now),
		}, now)

		if got := len(r.Panes()); got != 2 {
			t.Errorf("%s: %d rows — the pane swallowed a listing row it is not running", c.name, got)
		}
	}
}

// A RETIRED pane does not claim its session either, for the reason the uuid join already refuses one:
// folding a live listing row into a corpse hands the corpse a fresh fact and the session — the row
// with a door — never appears.
func TestARetiredDoorPaneStopsClaimingItsSession(t *testing.T) {
	now := time.Unix(1786450000, 0)
	uuid := "7ef2fe7e-c88d-46da-ac9d-0b57f170c3d8"
	name := "the session"

	r := New()
	r.SetHostOrder([]string{"local"})
	r.Update("local", doorPane(),
		doorLabels("7ef2fe7e", name+"-7ef2fe7e", doorStart("7ef2fe7e")),
		[]tmux.Capture{{PaneID: "%5", Height: 24, Lines: []string{"❯"}}},
		map[string]tmux.Capture{"%5": {PaneID: "%5", Height: 24, Lines: []string{"❯"}}},
		now, time.Second)
	// The pane goes away.
	r.Update("local", nil, nil, nil, nil, now.Add(time.Second), time.Second)
	r.UpdateAgents("local", []agents.Session{
		session("7ef2fe7e", uuid, "background", name, "/w/xmap", "working", now),
	}, now.Add(2*time.Second))

	var agentRows int
	for _, p := range r.Panes() {
		if p.Kind == KindAgent {
			agentRows++
		}
	}
	if agentRows != 1 {
		t.Errorf("%d agent rows: a session whose door pane has gone must get its own row back",
			agentRows)
	}
}

// The session options reach the ROW, and this goes through Update rather than building a Pane,
// because a loader that forgets a field passes every test that hand-builds the struct — this repo's
// signature defect, and `Path` shipped that way once (docs/design.md §21.1).
//
// They are session options read from a pane format, which works because option lookup walks
// pane → window → session → global (measured on 3.2a and 3.7b).
func TestTheSessionsOwnOptionsReachTheRow(t *testing.T) {
	now := time.Unix(1786450000, 0)
	r := New()
	r.SetHostOrder([]string{"local"})
	r.Update("local",
		[]tmux.Delta{{PaneID: "%5", Activity: 1786450000, PaneHeight: 24, WindowWidth: 80,
			PanePID: 7, SessionID: "$3", WindowID: "@7"}},
		map[string]tmux.Labels{"%5": {
			Session: "20260817-cicd-30f3382b", Window: "sh", Command: "sh",
			SessionAlias: "billing-cicd", StatusLeft: "[#{session_name}] ", StatusLeftLength: "10",
		}},
		[]tmux.Capture{{PaneID: "%5", Height: 24, Lines: []string{"❯"}}},
		map[string]tmux.Capture{"%5": {PaneID: "%5", Height: 24, Lines: []string{"❯"}}},
		now, time.Second)

	rows := r.Panes()
	if len(rows) != 1 {
		t.Fatalf("%d rows", len(rows))
	}
	for _, c := range []struct{ what, got, want string }{
		{"SessionAlias", rows[0].SessionAlias, "billing-cicd"},
		{"StatusLeft", rows[0].StatusLeft, "[#{session_name}] "},
		{"StatusLeftLength", rows[0].StatusLeftLength, "10"},
	} {
		if c.got != c.want {
			t.Errorf("%s = %q, want %q — the publisher compares against these, so an unread "+
				"one makes it write the same options on every tick", c.what, c.got, c.want)
		}
	}
}
