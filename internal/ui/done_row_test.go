package ui

import (
	"strings"
	"testing"

	"github.com/DawnBreather/tmux-hub/internal/lines"
	"github.com/DawnBreather/tmux-hub/internal/registry"
	"github.com/DawnBreather/tmux-hub/internal/state"
)

// The screen has to say `done`, and at 80 columns — the size §16 commits to, and the only band
// where the inbox takes the inline shape. A finished background job read `▸ idle` here, which is
// the same word and the same glyph a live session waiting for input gets: the operator could
// not tell from the row whether there was anything to type into.
func TestAFinishedRowReadsDoneAndNotIdle(t *testing.T) {
	finished := registry.Pane{
		Kind: registry.KindAgent, Command: "background", Host: "nuc",
		Session: "envoy-hotfix", AgentID: "84dc5a2e", SessionID: "84dc5a2e-cccc",
		PaneID: "agent:84dc5a2e@1a2b3c4d", ClassifiedState: state.Done,
		Content: []string{"  (no pane)"},
	}
	live := registry.Pane{
		Kind: registry.KindAgent, Command: "interactive", Host: "local",
		Session: "scratch", SessionID: "9f9f9f9f-0000",
		PaneID: "agent:9f9f9f9f@5e6f7a8b", ClassifiedState: state.Idle,
		Content: []string{"  (no pane)"},
	}
	m := base(t, 80, 24, finished, live)
	screen := m.View()

	// The INBOX row, not any line naming the session: the focused tile draws its header from the
	// same name, and taking the last match read the tile and failed on a screen that was right.
	finRow, liveRow := inboxRow(t, screen, "envoy-hotfix"), inboxRow(t, screen, "scratch")
	if !strings.Contains(finRow, "done") {
		t.Errorf("the finished row does not read done: %q", strings.TrimRight(finRow, " "))
	}
	// The row's own line, not the whole screen: the live row prints `idle` two lines away, so a
	// Contains over the screen would pass with the fold still in place.
	if strings.Contains(finRow, "idle") {
		t.Errorf("the finished row still reads idle: %q", strings.TrimRight(finRow, " "))
	}
	if !strings.Contains(liveRow, "idle") {
		t.Errorf("the live row must keep reading idle: %q", strings.TrimRight(liveRow, " "))
	}
	// And the ordering the rank buys, on the screen: the live session is above the finished job.
	if strings.Index(screen, "scratch") > strings.Index(screen, "envoy-hotfix") {
		t.Errorf("the finished job is drawn above the live session:\n%s", screen)
	}
}

// The first use of the fact the fold was hiding. `K` on a pane-less BACKGROUND row prints the
// command that ends it — `claude stop <short id>` — and for a job that has already ENDED that is
// advice to stop something that is not running. Until `done` existed the hub could not tell,
// because a finished job and a live one were both `idle`.
func TestKOnAFinishedJobDoesNotOfferToStopIt(t *testing.T) {
	finished := registry.Pane{
		Kind: registry.KindAgent, Command: "background", Host: "nuc",
		Session: "envoy-hotfix", AgentID: "84dc5a2e", SessionID: "84dc5a2e-cccc",
		PaneID: "agent:84dc5a2e@1a2b3c4d", ClassifiedState: state.Done,
	}
	m := base(t, 80, 24, finished)
	m.sel.Toggle(selKey(finished))

	after := key(t, m, "K")

	if after.mode == modeConfirm {
		t.Error("K offered a pane-less row for killing; the guard must precede the dialog")
	}
	for _, want := range []string{"nothing killed", "envoy-hotfix", "is done"} {
		if !strings.Contains(after.note, want) {
			t.Errorf("the refusal does not name %q: %q", want, after.note)
		}
	}
	if strings.Contains(after.note, "claude stop") {
		t.Errorf("the refusal tells the operator to stop a job that has ended: %q", after.note)
	}
	// It must still say where to READ it, or the sentence takes an option away without
	// offering the one that is left.
	if !strings.Contains(after.note, "claude logs "+finished.AgentID) {
		t.Errorf("the refusal does not say how to read a finished job: %q", after.note)
	}
	// 80 columns, minus Fit's 3-column marker budget for a claimant sharing the row (N6).
	if w := lines.Width(after.note); w > 80-3 {
		t.Errorf("the refusal is %d columns wide against a budget of 77: %q", w, after.note)
	}
	// And a RUNNING background row must keep its stop command — the branch has to split on the
	// state, not replace one sentence with another.
	running := finished
	running.ClassifiedState, running.Session = state.Works, "live-job"
	m2 := base(t, 80, 24, running)
	m2.sel.Toggle(selKey(running))
	if note := key(t, m2, "K").note; !strings.Contains(note, "claude stop "+running.AgentID) {
		t.Errorf("a running job lost its stop command: %q", note)
	}
}

// The tile's `id:` line must carry the id the VERBS take.
//
// §22 has the tile put the short id on screen so the operator can run `claude logs <id>` or
// `claude attach <id>` by hand, and measured, the full uuid does not resolve: `claude logs
// <full uuid>` answers `No job matching`. The tile printed `p.SessionID` — the uuid — so the one
// string on that screen that looks like something to copy was the one that fails.
//
// `K` already prints the usable id, so this was a second and unusable copy rather than the only
// one; that is why it reads as a nit and is still worth one line.
func TestTheTileCarriesTheIdTheVerbsAccept(t *testing.T) {
	row := registry.Pane{
		Kind: registry.KindAgent, Command: "background", Host: "nuc",
		Session: "envoy-hotfix", AgentID: "84dc5a2e",
		SessionID: "84dc5a2e-4f01-4690-bbd4-29d42779a154",
		PaneID:    "agent:84dc5a2e@1a2b3c4d", ClassifiedState: state.Done,
	}
	// 160 columns: the grouped band, where the tile is drawn beside the list.
	screen := base(t, 160, 24, row).View()

	if !strings.Contains(screen, "id:    "+row.AgentID) {
		t.Errorf("the tile does not carry the short id %q:\n%s", row.AgentID, screen)
	}
	if strings.Contains(screen, row.SessionID) {
		t.Errorf("the tile prints the full uuid, which `claude logs` answers `No job matching` "+
			"for:\n%s", screen)
	}
}

// A row with no short id — an interactive session, measured 0 of 13 carrying one — must fall back
// to what it does have rather than showing an empty label. The uuid is not usable as a verb
// argument, but it is the identity the operator can match against `claude agents` output.
func TestTheTileFallsBackToTheUuidWhenThereIsNoShortId(t *testing.T) {
	row := registry.Pane{
		Kind: registry.KindAgent, Command: "interactive", Host: "local",
		Session: "scratch", SessionID: "9f9f9f9f-0000-0000-0000-000000000001",
		PaneID: "agent:9f9f9f9f@5e6f7a8b", ClassifiedState: state.Idle,
	}
	screen := base(t, 160, 24, row).View()
	if !strings.Contains(screen, row.SessionID) {
		t.Errorf("a row with no short id shows no id at all:\n%s", screen)
	}
}
