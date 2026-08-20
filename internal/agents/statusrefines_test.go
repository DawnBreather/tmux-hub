package agents

import (
	"encoding/json"
	"testing"
)

// `status` REFINES `state`; IT IS NOT ANOTHER NAME FOR IT — and reading it as one made a parked
// session read `works`.
//
// Reported as "billing-cicd is not working right now but shows as works", and reproduced on the
// installed binary before anything was changed. The row carried `agent_word=working` with
// `agent_pid=1060612`, so the listing was claiming a live worker; pid 1060612 was
// `claude bg-spare --bg-spare /tmp/cc-daemon-1000/…/spare/8cd8f269.claim.sock`, a pre-warmed process
// parked on a claim socket. A pid says this host can SEE a process. It never says the process is
// working.
//
// The listing's own record said so too, in a field the parser only read when `state` was empty:
//
//	{"id":"30f3382b", "kind":"background", "state":"working", "status":"idle", "pid":1060612}
//
// THE CENSUS is the table below, taken from 34 live records in one run, because a test built from
// the two branches an author remembers is how the `busy`/`idle` half of this vocabulary went missing
// the first time. Every (kind, state, status) triple the fleet actually produced is here with its
// count, so a version that starts sending a new combination shows up as a row nobody wrote.
func TestStatusRefinesStateAcrossEveryLiveCombination(t *testing.T) {
	for _, c := range []struct {
		seen   int // how many of the 34 records carried this triple
		kind   string
		state  string
		status string
		want   string
		why    string
	}{
		{14, "background", "done", "", "done", "the commonest row: a finished job, no status at all"},
		{5, "background", "working", "busy", "works", "state and status agree, and this is the majority of live work"},
		{4, "background", "blocked", "", "needs", "waiting on the operator"},
		{3, "background", "failed", "", "error", ""},
		{2, "background", "done", "idle", "done",
			"a FINISHED job whose worker is unoccupied is still finished — `done` is the more specific of the two"},
		{2, "interactive", "", "", "", "neither field: unknown, and the caller must not read it as a state"},
		{1, "background", "stopped", "", "done", "the operator's own `claude stop`"},
		{1, "interactive", "", "idle", "idle", "the version that sends only `status`, folded into State by the parser"},
		{1, "background", "working", "idle", "idle",
			"THE REPORTED CASE: the worker is parked, so the session is idle rather than working"},
		{1, "background", "blocked", "idle", "needs",
			"`needs` is never demoted — burying a session that is waiting for the operator is the one " +
				"direction this repo refuses, and an unoccupied worker is exactly what waiting looks like"},
	} {
		t.Run(c.state+"/"+c.status, func(t *testing.T) {
			// Built through Parse, not by hand: the fold from `status` into `State` lives there, and a
			// hand-made Session would test this file's belief about the parser instead of the parser.
			rec := map[string]any{"sessionId": "u-" + c.state + c.status, "kind": c.kind}
			if c.state != "" {
				rec["state"] = c.state
			}
			if c.status != "" {
				rec["status"] = c.status
			}
			b, err := json.Marshal([]map[string]any{rec})
			if err != nil {
				t.Fatal(err)
			}
			ss, err := Parse(b)
			if err != nil {
				t.Fatal(err)
			}
			if len(ss) != 1 {
				t.Fatalf("Parse returned %d sessions for one record", len(ss))
			}
			if got := ss[0].Attention(); got != c.want {
				t.Errorf("state=%q status=%q -> %q, want %q (%d of 34 live records; %s)",
					c.state, c.status, got, c.want, c.seen, c.why)
			}
		})
	}
}

// The raw `status` survives parsing even when `state` is present, which is what the refinement reads
// and what lets `--status` publish the premise behind its answer.
//
// Before this, `status` was consulted ONLY when `state` was empty, so the field that carried the
// contradiction was thrown away before anything could see it — and no amount of looking at
// `agent_word` would have explained the row.
func TestParseKeepsTheRawStatusBesideTheState(t *testing.T) {
	const live = `[{"id":"30f3382b","sessionId":"30f3382b-f68c-4baf-98fd-68d4fd1c3da4",
	  "kind":"background","name":"20260818--cicd","state":"working","status":"idle","pid":1060612}]`
	ss, err := Parse([]byte(live))
	if err != nil {
		t.Fatal(err)
	}
	if len(ss) != 1 {
		t.Fatalf("parsed %d records, want 1", len(ss))
	}
	s := ss[0]
	if s.State != "working" {
		t.Errorf("State = %q, want %q — the lifecycle word must arrive unfolded", s.State, "working")
	}
	if s.Status != "idle" {
		t.Errorf("Status = %q, want %q — the field the refinement reads was dropped", s.Status, "idle")
	}
	if s.PID != 1060612 {
		t.Errorf("PID = %d, want 1060612", s.PID)
	}
	if got := s.Attention(); got != "idle" {
		t.Errorf("Attention = %q, want idle — this is the record the operator reported", got)
	}
}
