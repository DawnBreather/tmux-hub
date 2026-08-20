package registry

import (
	"testing"
	"time"

	"github.com/DawnBreather/tmux-hub/internal/agents"
	"github.com/DawnBreather/tmux-hub/internal/state"
	"github.com/DawnBreather/tmux-hub/internal/tmux"
)

func sample(now time.Time) ([]tmux.Delta, map[string]tmux.Labels, []tmux.Capture, map[string]tmux.Capture) {
	ds := []tmux.Delta{
		{PaneID: "%0", Activity: now.Unix(), PaneHeight: 24, WindowWidth: 80, PanePID: 1},
		{PaneID: "%1", Activity: now.Add(-10 * time.Minute).Unix(), PaneHeight: 24, WindowWidth: 80, PanePID: 2},
	}
	ls := map[string]tmux.Labels{
		"%0": {Session: "live1", Window: "w0", Command: "claude"},
		"%1": {Session: "work", Window: "w1", Command: "bash"},
	}
	zones := []tmux.Capture{
		{PaneID: "%0", Lines: []string{"● Ran tests", "Do you want to proceed?", "❯"}, Height: 24},
		{PaneID: "%1", Lines: []string{"● done", "❯"}, Height: 24},
	}
	fulls := map[string]tmux.Capture{
		"%0": {PaneID: "%0", Height: 24, Lines: []string{"● Ran tests", "Do you want to proceed?", "❯"}},
		"%1": {PaneID: "%1", Height: 24, Lines: []string{"● done", "❯"}},
	}
	return ds, ls, zones, fulls
}

func TestUpdateSortsByAttention(t *testing.T) {
	now := time.Unix(1786450000, 0)
	r := New()
	ds, ls, zones, fulls := sample(now)
	r.Update("local", ds, ls, zones, fulls, now, 0)

	got := r.Panes()
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
	if got[0].State() != state.Needs {
		t.Fatalf("first pane state = %v, want Needs", got[0].State())
	}
	if got[1].State() != state.Quiet {
		t.Fatalf("second pane state = %v, want Quiet", got[1].State())
	}
	if got[0].Session != "live1" || got[0].Command != "claude" {
		t.Fatalf("labels not merged: %+v", got[0])
	}
}

// A pane that disappears must not vanish from the list: its last screen is the
// only remaining evidence of why it died, because tmux destroys a pane and its
// scrollback together.
func TestVanishedPaneBecomesGoneAndKeepsItsLastScreen(t *testing.T) {
	now := time.Unix(1786450000, 0)
	r := New()
	ds, ls, zones, fulls := sample(now)
	r.Update("local", ds, ls, zones, fulls, now, 0)

	// %1 disappears on the next tick.
	r.Update("local", ds[:1], map[string]tmux.Labels{"%0": ls["%0"]},
		zones[:1], map[string]tmux.Capture{"%0": fulls["%0"]}, now.Add(time.Second), 0)

	var gone Pane
	var found bool
	for _, p := range r.Panes() {
		if p.PaneID == "%1" {
			gone, found = p, true
		}
	}
	if !found {
		t.Fatal("%1 dropped out of the registry entirely")
	}
	if gone.State() != state.Gone {
		t.Fatalf("%%1 state = %v, want Gone", gone.State())
	}
	if len(gone.Content) == 0 {
		t.Fatal("%1 lost its last screen, which is the only evidence it left")
	}
}

func TestContentIsChromeStripped(t *testing.T) {
	now := time.Unix(1786450000, 0)
	r := New()
	ds, ls, zones, _ := sample(now)
	fulls := map[string]tmux.Capture{
		"%0": {PaneID: "%0", Height: 24, Lines: []string{
			"● the answer",
			"────────────────────────────────",
			"❯",
			"  ⏵⏵ bypass permissions on (shift+tab to cycle)",
		}},
		"%1": {PaneID: "%1", Height: 24, Lines: []string{"● other"}},
	}
	r.Update("local", ds, ls, zones, fulls, now, 0)
	for _, p := range r.Panes() {
		for _, l := range p.Content {
			if l == "❯" {
				t.Fatalf("chrome leaked into Content: %q", p.Content)
			}
		}
	}
}

// The defect this signature exists to prevent: the classification zone of an
// idle Claude pane is entirely chrome, so deriving Content from it leaves the
// tile empty for exactly the panes that matter. Measured on the live pane.
func TestZoneAloneYieldsNoContentButAFullCaptureDoes(t *testing.T) {
	now := time.Unix(1786450000, 0)
	idleZone := []tmux.Capture{{PaneID: "%0", Height: 24, Lines: []string{
		"────────────────────────────────",
		"❯",
		"────────────────────────────────",
		"  ⏵⏵ bypass permissions on (shift+tab to cycle)",
		"      new task? /clear to save 142k tokens",
		"",
	}}}
	ds := []tmux.Delta{{PaneID: "%0", Activity: now.Unix(), PaneHeight: 24, WindowWidth: 80}}
	ls := map[string]tmux.Labels{"%0": {Session: "live1", Command: "claude"}}

	r := New()
	r.Update("local", ds, ls, idleZone, nil, now, 0)
	if got := r.Panes()[0].Content; len(got) != 0 {
		t.Fatalf("zone-only Content = %q, want empty — the zone is chrome", got)
	}

	full := map[string]tmux.Capture{"%0": {PaneID: "%0", Height: 24, Lines: []string{
		"● Hi! What you need?",
		"",
		"────────────────────────────────",
		"❯",
	}}}
	r.Update("local", ds, ls, idleZone, full, now.Add(time.Second), 0)
	if got := r.Panes()[0].Content; len(got) != 1 || got[0] != "● Hi! What you need?" {
		t.Fatalf("full-capture Content = %q, want the answer line", got)
	}

	// Content must survive a tick with no full capture, so a tile that scrolls
	// off screen does not go blank and a Gone pane keeps its last screen.
	r.Update("local", ds, ls, idleZone, nil, now.Add(2*time.Second), 0)
	if got := r.Panes()[0].Content; len(got) != 1 {
		t.Fatalf("Content = %q after a tick with no full capture, want it retained", got)
	}
}

// A host going away must keep its panes on display, with their last screen, and
// must not leave them looking live.
func TestMarkHostStaleKeepsPanesAndMarksThem(t *testing.T) {
	now := time.Unix(1786450000, 0)
	r := New()
	ds, ls, zones, fulls := sample(now)
	r.Update("local", ds, ls, zones, fulls, now, 0)

	r.MarkHostStale("local", now.Add(time.Minute))
	got := r.Panes()
	if len(got) != 2 {
		t.Fatalf("panes = %d, want both kept", len(got))
	}
	for _, p := range got {
		if !p.Stale {
			t.Errorf("%s is not marked stale", p.PaneID)
		}
		if len(p.Content) == 0 {
			t.Errorf("%s lost its last screen", p.PaneID)
		}
	}
	// And a host that answers again clears the mark.
	r.Update("local", ds, ls, zones, fulls, now.Add(2*time.Minute), 0)
	for _, p := range r.Panes() {
		if p.Stale {
			t.Errorf("%s is still stale after the host answered", p.PaneID)
		}
	}
}

// A stale mark on one host must not touch another.
func TestMarkHostStaleIsScopedToItsHost(t *testing.T) {
	now := time.Unix(1786450000, 0)
	r := New()
	ds, ls, zones, fulls := sample(now)
	r.Update("local", ds, ls, zones, fulls, now, 0)
	r.Update("nuc", ds, ls, zones, fulls, now, 0)

	r.MarkHostStale("nuc", now.Add(time.Minute))
	for _, p := range r.Panes() {
		if p.Host == "local" && p.Stale {
			t.Errorf("local/%s was marked stale by nuc going away", p.PaneID)
		}
		if p.Host == "nuc" && !p.Stale {
			t.Errorf("nuc/%s should be stale", p.PaneID)
		}
	}
}

// A capture whose pane was resized between the delta and the batch came back for
// a different range than was asked for, so it must not become the zone the
// classifier reads. The flag was computed and thrown away before this.
func TestStaleCaptureDoesNotReplaceTheZone(t *testing.T) {
	now := time.Unix(1786450000, 0)
	ds := []tmux.Delta{{PaneID: "%0", Activity: now.Unix(), PaneHeight: 24, CursorY: 23, SessionID: "$0"}}
	ls := map[string]tmux.Labels{"%0": {Session: "s", Command: "claude"}}

	good := []tmux.Capture{{PaneID: "%0", Height: 24, Lines: []string{"● the real answer", "❯"}}}
	r := New()
	r.Update("local", ds, ls, good, nil, now, 0)
	first := r.Panes()[0].Zone
	if len(first) == 0 {
		t.Fatal("the good capture did not become the zone")
	}

	// Same pane, but the capture reports a different height: mid-batch resize.
	resized := []tmux.Capture{{PaneID: "%0", Height: 40, Stale: true,
		Lines: []string{"rows from the wrong range"}}}
	r.Update("local", ds, ls, resized, nil, now.Add(time.Second), 0)
	if got := r.Panes()[0].Zone; len(got) != len(first) || got[0] != first[0] {
		t.Fatalf("a stale capture replaced the zone: %q", got)
	}
}

// Claude's own listing is a FACT about state, so classify() is not consulted —
// and a session whose state the listing did not report becomes Unknown rather
// than being flattened into idle, which would lie about a session that might be
// waiting (docs/design.md §17).
func TestUpdateAgentsMapsClaudesOwnWords(t *testing.T) {
	now := time.Unix(1786450000, 0)
	r := New()
	r.UpdateAgents("local", []agents.Session{
		{ID: "1ff133f7", SessionID: "1ff133f7-aaaa", Kind: "background",
			Name: "dockerfile goldens", State: "blocked", StartedAt: now.Add(-time.Hour)},
		{ID: "4ca5ffa9", SessionID: "4ca5ffa9-bbbb", Kind: "background",
			Name: "miro board", State: "working", StartedAt: now.Add(-time.Minute)},
		{ID: "84dc5a2e", SessionID: "84dc5a2e-cccc", Kind: "background",
			Name: "envoy hotfix", State: "done", StartedAt: now.Add(-time.Hour)},
		{ID: "deadbeef", SessionID: "deadbeef-dddd", Kind: "interactive",
			Name: "a version that reports no state", StartedAt: now},
	}, now)

	got := r.Panes()
	if len(got) != 4 {
		t.Fatalf("got %d rows, want 4", len(got))
	}
	// Keyed on Claude's own session id, NOT on the row's PaneID. That id is the
	// identity this listing is about, where PaneID is the registry's internal row
	// key — it gained a cwd digest when two real sessions were found sharing one
	// short id, and a test that spells the key out asserts the key's shape rather
	// than the mapping it is named after.
	bySession := map[string]Pane{}
	for _, p := range got {
		bySession[p.SessionID] = p
	}
	for id, want := range map[string]state.State{
		"1ff133f7-aaaa": state.Needs,
		"4ca5ffa9-bbbb": state.Works,
		// `done`, and NOT idle: idle is a live session with a prompt, and this job ended.
		"84dc5a2e-cccc": state.Done,
		"deadbeef-dddd": state.Unknown,
	} {
		p, ok := bySession[id]
		if !ok {
			var have []string
			for k := range bySession {
				have = append(have, k)
			}
			t.Fatalf("session %s is missing; the listing produced %v", id, have)
		}
		if p.State() != want {
			t.Errorf("%s state = %v, want %v", id, p.State(), want)
		}
		if p.Kind != KindAgent {
			t.Errorf("%s kind = %q, want %q", id, p.Kind, KindAgent)
		}
	}
	// needs sorts first, unknown near the end.
	if got[0].State() != state.Needs {
		t.Errorf("first row = %v, want the blocked one", got[0].State())
	}
}

// A session that leaves the listing is DONE WITH, not stale: the listing is the
// whole truth about its own population, unlike a pane list that a broken tunnel
// can silently empty.
func TestAgentsLeavingTheListingAreDropped(t *testing.T) {
	now := time.Unix(1786450000, 0)
	r := New()
	two := []agents.Session{
		{ID: "aaaa1111", SessionID: "aaaa1111-x", Kind: "background", Name: "a", State: "working"},
		{ID: "bbbb2222", SessionID: "bbbb2222-x", Kind: "background", Name: "b", State: "blocked"},
	}
	r.UpdateAgents("local", two, now)
	if len(r.Panes()) != 2 {
		t.Fatalf("got %d", len(r.Panes()))
	}
	r.UpdateAgents("local", two[:1], now.Add(time.Minute))
	got := r.Panes()
	// The SURVIVOR is named by Claude's session id, not by the row key, for the
	// reason spelled out in TestUpdateAgentsMapsClaudesOwnWords.
	if len(got) != 1 || got[0].SessionID != "aaaa1111-x" {
		t.Fatalf("got %+v, want only the surviving session", got)
	}
}

// Agent rows and pane rows share one list without colliding, and a host going
// away marks its PANES stale while leaving agent rows alone.
func TestAgentAndPaneRowsCoexist(t *testing.T) {
	now := time.Unix(1786450000, 0)
	r := New()
	ds, ls, zones, fulls := sample(now)
	r.Update("local", ds, ls, zones, fulls, now, 0)
	r.UpdateAgents("local", []agents.Session{
		{ID: "0", SessionID: "0-x", Kind: "background", Name: "an agent", State: "blocked"},
	}, now)
	if n := len(r.Panes()); n != 3 {
		t.Fatalf("got %d rows, want 2 panes + 1 agent", n)
	}
	// A pane id of "%0" and an agent short id of "0" must not collide.
	ids := map[string]bool{}
	for _, p := range r.Panes() {
		if ids[p.PaneID] {
			t.Fatalf("duplicate key %q", p.PaneID)
		}
		ids[p.PaneID] = true
	}
	r.MarkHostStale("local", now.Add(time.Minute))
	for _, p := range r.Panes() {
		if p.Kind == KindAgent && p.Stale {
			t.Error("an agent row was marked stale by a tmux host going away")
		}
	}
}

// A session with a pane must produce ONE row, not two, and the row must carry the
// FACT rather than the pixel guess. State is "working" rather than "blocked"
// because state.Needs is the ZERO VALUE — if the assignment is deleted the test
// would still pass on the uninitialized AgentState, which is exactly what it
// exists to catch.
func TestAgentWithAPaneDoesNotDuplicateTheRow(t *testing.T) {
	r := New()
	now := time.Now()
	// A pane the poll found, already joined to its Claude session.
	r.Update("local", []tmux.Delta{{PaneID: "%3", SessionID: "$0", PaneHeight: 24}},
		map[string]tmux.Labels{"%3": {Session: "work"}}, nil, nil, now, time.Second)
	r.SetClaudeSession("local", "%3", "1ff133f7-c34a-4c60-91e5-b0048842cc66")

	r.UpdateAgents("local", []agents.Session{{
		SessionID: "1ff133f7-c34a-4c60-91e5-b0048842cc66",
		ID:        "1ff133f7", Name: "goldens", Kind: "interactive", State: "working",
	}}, now)

	panes := r.Panes()
	if len(panes) != 1 {
		t.Fatalf("got %d rows, want 1 — the session and its pane were not joined: %+v",
			len(panes), panes)
	}
	if panes[0].PaneID != "%3" {
		t.Errorf("the surviving row is %q, want the pane", panes[0].PaneID)
	}
	if panes[0].State() != state.Works {
		t.Errorf("state = %v, want works — the CLI's fact must win over the pixels",
			panes[0].State())
	}
}

// A session with NO pane still gets its own row: most of them have none, which is
// the whole reason the producer exists.
func TestAgentWithoutAPaneStillGetsARow(t *testing.T) {
	r := New()
	r.UpdateAgents("local", []agents.Session{{
		SessionID: "4ca5ffa9-e6ed-45f2-aa6c-3dd4a76946d8",
		ID:        "4ca5ffa9", Name: "erp", Kind: "background", State: "working",
	}}, time.Now())
	panes := r.Panes()
	if len(panes) != 1 || panes[0].Kind != KindAgent {
		t.Fatalf("got %+v, want one agent row", panes)
	}
}

// A stale fact must not beat a live guess: the CLI was measured 30 minutes behind.
func TestAStaleAgentStateYieldsToThePixels(t *testing.T) {
	r := New()
	old := time.Now().Add(-30 * time.Minute)
	r.Update("local", []tmux.Delta{{PaneID: "%3", SessionID: "$0", PaneHeight: 24}},
		map[string]tmux.Labels{"%3": {Session: "work"}}, nil, nil, time.Now(), time.Second)
	r.SetClaudeSession("local", "%3", "1ff133f7-c34a-4c60-91e5-b0048842cc66")
	r.UpdateAgents("local", []agents.Session{{
		SessionID: "1ff133f7-c34a-4c60-91e5-b0048842cc66", State: "done",
	}}, old)

	if got := r.Panes()[0].State(); got == state.Idle {
		t.Error("a 30-minute-old `done` overrode the live classification")
	}
}

// The three fields the write path's pre-flight reads must survive the poll. Each is
// a clause of the confirmation rule that cannot fire without it, and a clause that
// cannot fire is indistinguishable from one that decided everything is fine:
// pane_pid roots the process walk, bracket_paste_flag says whether a paste is read
// as text or as keypresses, and the epoch says whether this pane id still names the
// pane it named.
func TestUpdateCarriesThePreFlightFields(t *testing.T) {
	now := time.Unix(1786450000, 0)
	r := New()
	ds := []tmux.Delta{{
		PaneID: "%0", Activity: now.Unix(), PaneHeight: 24, WindowWidth: 80,
		PanePID: 4179803, Bracketed: true, Epoch: "4179801:1786510794",
		SessionID: "$0", WindowID: "@3",
	}}
	ls := map[string]tmux.Labels{"%0": {Session: "live1", Window: "w0", Command: "claude"}}
	r.Update("local", ds, ls, nil, nil, now, 0)

	got := r.Panes()
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1", len(got))
	}
	if got[0].PanePID != 4179803 {
		t.Errorf("PanePID = %d, want the delta's — the process walk has no root without it",
			got[0].PanePID)
	}
	if !got[0].Bracketed {
		t.Error("Bracketed was dropped — a pane that reads a paste as keypresses looks safe")
	}
	if got[0].Epoch != "4179801:1786510794" {
		t.Errorf("Epoch = %q, want the delta's — two empty epochs compare EQUAL, so the "+
			"restart clause would silently pass for every target", got[0].Epoch)
	}
	if got[0].WindowID != "@3" {
		t.Errorf("WindowID = %q, want the delta's — a window NAME follows the foreground "+
			"process, so comparing names reports a pane as moved when it has not", got[0].WindowID)
	}
}

// The new fields Index, StartCommand and DeadStatus flow through from delta and labels
// and must reach the registry via the real apply path.
func TestRegistryCarriesIndexStartCommandAndDeadStatus(t *testing.T) {
	now := time.Unix(1786450000, 0)
	ds := []tmux.Delta{
		{PaneID: "%2", Activity: now.Unix(), PaneHeight: 24, WindowWidth: 80,
			Index: 1, Dead: false, DeadStatus: 0},
		{PaneID: "%5", Activity: now.Unix(), PaneHeight: 24, WindowWidth: 80,
			Index: 3, Dead: true, DeadStatus: 7},
	}
	ls := map[string]tmux.Labels{
		"%2": {Session: "live1", Window: "w0", Command: "claude",
			StartCommand: `"sleep 301"`},
		"%5": {Session: "done", Window: "w1", Command: "bash",
			StartCommand: `"tail -f log"`},
	}

	r := New()
	r.Update("local", ds, ls, nil, nil, now, 0)

	got := r.Panes()
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}

	// Find each pane and check its fields
	for _, p := range got {
		if p.PaneID == "%2" {
			if p.Index != 1 {
				t.Errorf("%%2 Index = %d, want 1", p.Index)
			}
			if p.StartCommand != `"sleep 301"` {
				t.Errorf("%%2 StartCommand = %q, want %q", p.StartCommand, `"sleep 301"`)
			}
			if p.DeadStatus != 0 {
				t.Errorf("%%2 DeadStatus = %d, want 0", p.DeadStatus)
			}
		}
		if p.PaneID == "%5" {
			if p.Index != 3 {
				t.Errorf("%%5 Index = %d, want 3", p.Index)
			}
			if p.StartCommand != `"tail -f log"` {
				t.Errorf("%%5 StartCommand = %q, want %q", p.StartCommand, `"tail -f log"`)
			}
			if p.DeadStatus != 7 {
				t.Errorf("%%5 DeadStatus = %d, want 7", p.DeadStatus)
			}
		}
	}
}

// TestSortByAttentionOrdersASliceInPlace covers the comparator through its
// exported door rather than through Update, because a fixture that builds a pane
// list by hand is now a real caller: the mockup generator assigns straight to
// model.panes, and before this existed every screen it produced showed rows in
// construction order. The expected order is written out rather than derived, and
// it encodes the ranking that matters — quiet and error outrank works, because a
// finished-or-hung agent is something to act on and a busy one is not.
func TestSortByAttentionOrdersASliceInPlace(t *testing.T) {
	panes := []Pane{
		{Host: "local", Session: "api", PaneID: "%1", ClassifiedState: state.Works},
		{Host: "nuc", Session: "deploy", PaneID: "%4", ClassifiedState: state.Quiet},
		{Host: "local", Session: "api", PaneID: "%0", ClassifiedState: state.Needs},
		{Host: "local", Session: "api", PaneID: "%2", ClassifiedState: state.Works},
		{Host: "local", Session: "ops", PaneID: "%3", ClassifiedState: state.Idle},
		{Host: "nuc", Session: "deploy", PaneID: "%5", ClassifiedState: state.Error},
	}
	SortByAttention(panes)

	want := []string{"%0", "%5", "%4", "%3", "%1", "%2"}
	got := make([]string, 0, len(panes))
	for _, p := range panes {
		got = append(got, p.PaneID)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order = %v, want %v", got, want)
		}
	}
}

// A field that is fetched and not carried is dead, and this repo has shipped that
// defect twice: #{pane_start_command} joined the label table while nothing read the
// table, and every unit test passed because each one built snap.Labels itself. So the
// working directory gets a test that goes through Update rather than through a
// hand-built Pane — and it asserts the hostile value, because the path is the one
// label whose bytes the operator's filesystem chooses.
func TestUpdateCarriesTheWorkingDirectory(t *testing.T) {
	now := time.Unix(1786450000, 0)
	ds, ls, zones, fulls := sample(now)
	hostile := "/tmp/пу|ть\nвторой"
	ls["%0"] = tmux.Labels{Session: "live1", Command: "claude", Path: hostile}
	ls["%1"] = tmux.Labels{Session: "work", Command: "bash", Path: "/tmp/tame"}

	r := New()
	r.Update("local", ds, ls, zones, fulls, now, 0)
	byID := map[string]Pane{}
	for _, p := range r.Panes() {
		byID[p.PaneID] = p
	}
	if got := byID["%0"].Path; got != hostile {
		t.Errorf("%%0 Path = %q, want %q — the project a session belongs to is unknowable "+
			"without it", got, hostile)
	}
	if got := byID["%1"].Path; got != "/tmp/tame" {
		t.Errorf("%%1 Path = %q, want %q", got, "/tmp/tame")
	}
}
