package ui

import (
	"testing"

	"github.com/DawnBreather/tmux-hub/internal/project"
	"github.com/DawnBreather/tmux-hub/internal/registry"
	"github.com/DawnBreather/tmux-hub/internal/state"
)

// THE HUB IS NOT THE ONLY PROGRAM THAT WRITES A WINDOW NAME, so it asks once and then concedes.
//
// Reported as a tmux status line that "shimmers" on some sessions, and measured on the operator's own
// server: three windows alternating several times a second between the alias they had typed and the
// raw Claude session name — `frontend-troubleshooting` ↔ `20260810--troubleshooting` — with
// `automatic-rename` OFF, so tmux was not the other writer. Claude Code names the window after its own
// session; the hub's differences-only rule saw a name that had drifted and wrote the alias back; each
// side undid the other every poll.
//
// The differences-only rule is not the fix here, it is the ENGINE: two writers plus "write whenever it
// differs" is a loop by construction. So the second attempt at the same name is withheld. The operator
// still reads the alias where they asked for it — on the dashboard.
func TestTheHubAsksForAWindowNameOnceAndThenConcedes(t *testing.T) {
	// A door-made pane: the gate is its start command, which is what says the hub made this window.
	row := registry.Pane{
		Kind: registry.KindPane, Host: "local", PaneID: "%7", WindowID: "@3",
		Session: "20260810--troubleshooting-e19579bb", Window: "20260810--troubleshooting",
		// The shape tmux really reports for a door pane, copied from the tests that already parse
		// it rather than written from memory: this field is what `AttachedSessionID` reads, and it
		// is the gate that says the hub made this window.
		Command: "sh", StartCommand: `sh -c 'claude attach e19579bb'`,
		ClaudeSession:   "e19579bb-1111-2222-3333-444455556666",
		ClassifiedState: state.Works,
	}
	aliases := project.Aliases{}
	// The key comes from AliasKeyOf, never hand-built: CLAUDE.md records that the field carrying the
	// uuid differs BY KIND, and a hand-made key is how a mark comes off the row it was meant for.
	aliases.Set(project.AliasKeyOf(row), "frontend-troubleshooting")

	want := doorWindowName(row, aliases)
	if want == "" {
		t.Skip("the fixture produces no wanted name, so this test would assert nothing")
	}

	asked := map[string]string{}

	// FIRST poll: the name has drifted, so the hub asks.
	first := windowRenames([]registry.Pane{row}, aliases, asked)
	if len(first) != 1 || first[0].Name != want {
		t.Fatalf("the first poll produced %+v, want one rename to %q", first, want)
	}

	// SECOND poll, with the window STILL carrying the other program's name — which is exactly what
	// the operator's server showed. Asking again is the shimmer.
	second := windowRenames([]registry.Pane{row}, aliases, asked)
	if len(second) != 0 {
		t.Errorf("the second poll asked again (%+v) — two writers and a differences-only rule is a "+
			"loop, and this is the poll where it starts", second)
	}

	// A NEW alias is a new name, and gets one fresh attempt: conceding a name must not mean
	// conceding the window forever.
	aliases.Set(project.AliasKeyOf(row), "renamed-by-the-operator")
	third := windowRenames([]registry.Pane{row}, aliases, asked)
	if len(third) != 1 {
		t.Errorf("a newly typed alias produced %d renames, want 1 — the concession is per NAME, not "+
			"per window", len(third))
	}

	// And once the window is carrying the name, there is nothing to ask for at all. The name is
	// recomputed rather than reusing `want` from above, which is stale by now: the alias changed one
	// assertion ago, and an expected value carried forward is how a test asserts yesterday's rule.
	settled := row
	settled.Window = doorWindowName(settled, aliases)
	if got := windowRenames([]registry.Pane{settled}, aliases, map[string]string{}); len(got) != 0 {
		t.Errorf("a window already carrying a hub name produced %+v, want nothing", got)
	}
}
