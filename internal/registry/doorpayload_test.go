package registry

import (
	"strings"
	"testing"
	"time"

	"github.com/DawnBreather/tmux-hub/internal/agents"
	"github.com/DawnBreather/tmux-hub/internal/tmux"
)

// The door's payload CHANGED, and the join that recovers a door pane's session reads that payload off
// the server — so the change reaches into a pane's identity.
//
// `ui.LoginPayload` runs the verb under `sh -lc` rather than `sh -c`, because a remote pane inherits
// the ssh CLIENT's environment: on dev-air, whose login shell is nushell, the non-login PATH does not
// hold `claude`, the pane died before the hub could read its window id, and the operator was shown
// `invalid window id: ""`. The wrapper is what fixes that.
//
// The consequence for THIS package is the one worth a test: a pane a PREVIOUS build created still
// carries `sh -c` for as long as it lives, so the hub that fixes the new panes must keep resolving the
// old ones. Matching only the new form would split every already-open door session into two rows —
// the pane and the listing row it should have absorbed — at the moment the hub is upgraded.
//
// The fixtures are the bytes TMUX ITSELF reports, measured 2026-08-20 on tmux 3.7b through BOTH paths
// the product uses: the local one, where the payload is one argv element handed straight to tmux, and
// the remote one, where it goes through `tmux.ShellJoinCommand` and a far shell first. The two agree
// exactly — tmux wraps the whole command in double quotes of its own and backslash-escapes the inner
// ones. This matters because an imagined fixture has already cost this join a whole cycle once: the
// first version omitted tmux's outer quotes, passed its test, and matched nothing on the fleet.
func loginDoorStart(id string) string {
	return `"sh -lc \"claude attach ` + id +
		` || { echo; echo tmux-hub: the command above failed — press enter to close this pane; ` +
		`read x; }\""`
}

// Both wrappers resolve, and the pole that makes the pair meaningful is that they resolve to the SAME
// id: a regex that accepted the new shape and returned the wrong capture would pass a presence check.
func TestBothDoorWrappersResolveToTheirSession(t *testing.T) {
	for _, c := range []struct{ name, start string }{
		{"the login wrapper this build creates", loginDoorStart("30f3382b")},
		{"the wrapper a previous build left open", doorStart("30f3382b")},
		// tmux quotes what it stores, so an UNQUOTED reading is not a shape the fleet produces — but
		// the stripper is what makes the two above equal, and a case with nothing to strip is the
		// only one that says the anchor itself is right rather than the stripping.
		{"nothing for the stripper to remove", "sh -lc claude attach 30f3382b || { read x; }"},
	} {
		if got := AttachedSessionID(c.start); got != "30f3382b" {
			t.Errorf("%s: resolved to %q, want the short id\n  from %s", c.name, got, c.start)
		}
	}
	// And neither wrapper is a licence to claim anything mentioning the verb. `-lc` gets the same
	// negatives `sh -c` already had, because the flag is the only thing that changed.
	for _, c := range []struct{ name, start string }{
		{"a login shell running another verb",
			`"sh -lc \"claude logs 30f3382b || { read x; }\""`},
		{"a login shell that merely logs the command",
			`"sh -lc \"echo claude attach 30f3382b >> notes.txt\""`},
		{"a login shell with no verb at all", `"sh -lc \"read x\""`},
	} {
		if got := AttachedSessionID(c.start); got != "" {
			t.Errorf("%s: claimed %q, so a pane that is not a door would swallow a listing row\n"+
				"  from %s", c.name, got, c.start)
		}
	}
}

// The parser is half the claim. What the operator sees is the JOIN: the pane the door made must absorb
// the listing row it was made for, with no adoption in memory, or a restarted hub draws the session
// twice — which is the defect §22.11 recorded and this recovery exists to prevent.
//
// Asserted for the NEW wrapper here; TestTheDoorsPaneAbsorbsItsRowWithoutAnAdoption asserts the same
// property for the old one, so the pair covers an upgraded hub's whole fleet.
func TestAPaneMadeByTheLoginWrapperAbsorbsItsRow(t *testing.T) {
	now := time.Unix(1786450000, 0)
	uuid := "7ef2fe7e-c88d-46da-ac9d-0b57f170c3d8"
	name := "20260813--gis-offline-maps-universal-reader-emulator"

	r := New()
	r.SetHostOrder([]string{"local"})
	// A fresh hub: the pane is there and nothing has told the registry what it runs. `Command` is `sh`
	// rather than `claude`, which is the measured cost of the wrapper — the pane's own process IS a
	// shell — and the reason the join may not be keyed on the command name.
	r.Update("local", doorPane(),
		doorLabels("7ef2fe7e", name+"-7ef2fe7e", loginDoorStart("7ef2fe7e")),
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
		t.Fatalf("%d rows: the pane the login wrapper made did not absorb the row it was made for",
			len(rows))
	}
	if rows[0].Kind != KindPane || rows[0].PaneID != "%5" {
		t.Errorf("the surviving row is not the pane: %+v", rows[0])
	}
	// The uuid is what every persisted mark is keyed on — the pin, the alias, the restart — so a pane
	// that absorbed the row and did not take its session would lose all three.
	if rows[0].ClaudeSession != uuid {
		t.Errorf("the pane does not know which session it runs: %q", rows[0].ClaudeSession)
	}
	if rows[0].AgentName != name {
		t.Errorf("the pane did not take the session's name: %q", rows[0].AgentName)
	}
}

// The wrapper's own text must not be readable as a door for somebody else. It names `tmux-hub` and it
// holds the pane with `read`, and both are strings a person might have in a shell — so the anchor has
// to be the whole head of the command, which is what this asserts from the other side.
func TestTheWrappersOwnFailureLineIsNotADoorForAnotherSession(t *testing.T) {
	// The failure line quotes no id at all, which is what makes it safe; if it ever gains one, this
	// case says so rather than a silent mis-join on the fleet.
	line := loginDoorStart("30f3382b")
	tail := line[strings.Index(line, "||"):]
	if strings.Contains(tail, "attach") {
		t.Errorf("the wrapper's tail repeats the verb, so a longer payload could resolve twice: %s",
			tail)
	}
	// A pane running the failure line ALONE — a person who pasted it — is not a door.
	if got := AttachedSessionID(`"sh -lc \"` + strings.TrimPrefix(tail, "|| ") + `\""`); got != "" {
		t.Errorf("the failure line alone resolved to %q", got)
	}
}
