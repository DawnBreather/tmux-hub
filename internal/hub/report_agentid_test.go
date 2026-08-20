package hub

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/DawnBreather/tmux-hub/internal/registry"
)

// The machine-readable report has to carry the SHORT id, because it is the only argument the
// `claude` verbs accept and the report is the only surface a script can read.
//
// The row carries it (`registry.Pane.AgentID`) and the tile prints it, but the report did not — so
// `tmux-hub -status | jq` could tell you a background job wanted attention and could not tell you
// what to type to reach it. Measured on the fleet the moment this was written: 7 background rows on
// one host, all carrying an id, and 7 interactive rows carrying none.
func TestTheReportCarriesTheIdTheVerbsAccept(t *testing.T) {
	rows := []registry.Pane{
		{Kind: registry.KindAgent, Host: "nuc", Session: "sonar-troubleshooting",
			AgentID: "799d2bbc", SessionID: "799d2bbc-0000-0000-0000-000000000000",
			PaneID: "agent:799d2bbc@1a2b3c4d", Command: "background"},
		// An interactive session carries no short id — measured 0 of 7 on the same host — so the
		// field must be OMITTED rather than empty-stringed, or a script cannot tell "no id" from
		// "id is the empty string".
		{Kind: registry.KindAgent, Host: "nuc", Session: "scratch",
			SessionID: "9f9f9f9f-0000", PaneID: "agent:9f9f9f9f@5e6f7a8b", Command: "interactive"},
		// A tmux pane has no agent id either.
		{Kind: registry.KindPane, Host: "local", Session: "api", Window: "fix", PaneID: "%0",
			Command: "claude"},
	}
	raw, err := json.Marshal(BuildReport(nil, rows))
	if err != nil {
		t.Fatal(err)
	}
	var got Report
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Panes) != 3 {
		t.Fatalf("%d rows in the report", len(got.Panes))
	}
	if got.Panes[0].AgentID != "799d2bbc" {
		t.Errorf("the background row's short id is %q — a script cannot build "+
			"`claude attach <id>` without it", got.Panes[0].AgentID)
	}
	if got.Panes[1].AgentID != "" || got.Panes[2].AgentID != "" {
		t.Errorf("a row with no short id reported one: %q / %q",
			got.Panes[1].AgentID, got.Panes[2].AgentID)
	}
	// Omitted, not empty: `agent_id` must appear exactly once in the document.
	if n := strings.Count(string(raw), `"agent_id"`); n != 1 {
		t.Errorf(`"agent_id" appears %d times, want 1 — it must be omitted where there is no id`, n)
	}
}
