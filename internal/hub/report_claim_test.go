package hub

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/DawnBreather/tmux-hub/internal/registry"
	"github.com/DawnBreather/tmux-hub/internal/state"
)

// THE REPORT CARRIES THE CLAIM THE STATE IS QUOTING: the listing's own word and the pid the host gave
// for it.
//
// It is here because of what a day of wrong states cost to diagnose. Two sessions were shown wrongly —
// one `error` while a workflow ran, one `needs` after the operator had answered it — and finding out why
// took three hours of probing: run the classifier over the real screen to prove the pixels could not
// have said it, ask each host for its own listing, compare the words, and finally notice that only one
// host reported a pid. Every one of those facts was already in the row. None of them was in the report.
//
// A pid is the load-bearing one: a record carries one exactly when the host reporting it can see the
// process, so `agent_pid` absent beside a state is the reader's warning that the word came from a
// machine that shares `~/.claude` and nothing else. With these two fields the same diagnosis is one
// `tmux-hub --status | jq` away.
func TestTheReportCarriesTheClaimItsStateIsQuoting(t *testing.T) {
	now := time.Now()
	rows := []registry.Pane{
		// The shape that was wrong twice: a pane row for a session whose owner is running the worker.
		{Kind: registry.KindPane, Host: "local", Session: "seedtool-development-77ef6f5e",
			Window: "seedtool-development", PaneID: "%28", Command: "sh",
			ClaudeSession: "77ef6f5e-a719-4cf9-8dbd-722e986f2604",
			AgentState:    state.Works, AgentWord: "working", AgentPID: 237624, AgentSeenAt: now},
		// The same session as a host that only shares the store would report it: a word, no pid.
		{Kind: registry.KindAgent, Host: "side-desk", Session: "20260818--cicd",
			AgentID: "30f3382b", SessionID: "30f3382b-0000", PaneID: "agent:30f3382b@ee42d26c",
			Command: "background", ClassifiedState: state.Needs, AgentWord: "blocked"},
		// A plain tmux pane that is nobody's Claude session quotes no listing at all, so BOTH fields
		// must be omitted — a reader has to be able to tell that from "the listing said nothing".
		{Kind: registry.KindPane, Host: "local", Session: "0", Window: "tmux-hub", PaneID: "%0",
			Command: "tmux-hub", ClassifiedState: state.Works},
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
		t.Fatalf("the report has %d panes, want 3", len(got.Panes))
	}

	owner, sharer, plain := got.Panes[0], got.Panes[1], got.Panes[2]
	if owner.AgentWord != "working" || owner.AgentPID != 237624 {
		t.Errorf("the owner's row reports word %q pid %d, want \"working\" and 237624 — without the pid "+
			"a reader cannot tell a claim about a process from a claim about the store",
			owner.AgentWord, owner.AgentPID)
	}
	if sharer.AgentWord != "blocked" {
		t.Errorf("the sharer's row reports word %q, want \"blocked\"", sharer.AgentWord)
	}
	if sharer.AgentPID != 0 {
		t.Errorf("the sharer's row reports pid %d — the host that cannot see the worker must show none",
			sharer.AgentPID)
	}
	if plain.AgentWord != "" || plain.AgentPID != 0 {
		t.Errorf("a pane that is nobody's Claude session reports word %q pid %d, want neither",
			plain.AgentWord, plain.AgentPID)
	}

	// OMITTED, not zeroed, and the JSON text is the only place that difference is visible: a reader
	// distinguishing "no listing" from "the listing said nothing" reads the keys, not the struct.
	text := string(raw)
	if strings.Count(text, `"agent_word"`) != 2 {
		t.Errorf("`agent_word` appears %d times, want 2 — one per row that quotes a listing:\n%s",
			strings.Count(text, `"agent_word"`), text)
	}
	if strings.Count(text, `"agent_pid"`) != 1 {
		t.Errorf("`agent_pid` appears %d times, want 1 — only the host that could see the worker:\n%s",
			strings.Count(text, `"agent_pid"`), text)
	}
}
