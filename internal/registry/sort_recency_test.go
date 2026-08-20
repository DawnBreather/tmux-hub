package registry

import (
	"github.com/DawnBreather/tmux-hub/internal/agents"
	"testing"
	"time"

	"github.com/DawnBreather/tmux-hub/internal/state"
)

// Attention stays the primary key. Recency first would sink a session that has waited three
// hours below one that printed a log line, which is the opposite of §1.
func TestRecencyNeverOutranksAttention(t *testing.T) {
	now := time.Now()
	rows := []Pane{
		{Host: "h", Session: "chatty", PaneID: "%1",
			ClassifiedState: state.Works, Activity: now},
		{Host: "h", Session: "waiting", PaneID: "%2",
			ClassifiedState: state.Needs, Activity: now.Add(-3 * time.Hour)},
	}
	SortByAttention(rows)
	if rows[0].Session != "waiting" {
		t.Errorf("order is %q then %q; the row that needs the operator must come first",
			rows[0].Session, rows[1].Session)
	}
}

// Within one rank, newer activity comes first — which is what lets a `failed` row from two hours
// ago sit below one from two minutes ago instead of being hidden.
//
// THE NAMES FIGHT THE OLD TIEBREAK ON PURPOSE. The tiebreak being replaced is alphabetical by
// host, then session, then pane id, so a fixture named "old" and "fresh" passes against the
// UNMODIFIED product — "fresh" sorts first by accident and the test never sees recency at all.
// Here the older row sorts FIRST alphabetically, so only the recency rule can reorder them.
func TestWithinARankTheNewestComesFirst(t *testing.T) {
	now := time.Now()
	rows := []Pane{
		{Host: "h", Session: "aaa-two-hours-old", PaneID: "%1",
			ClassifiedState: state.Error, Activity: now.Add(-2 * time.Hour)},
		{Host: "h", Session: "zzz-two-minutes-old", PaneID: "%2",
			ClassifiedState: state.Error, Activity: now.Add(-2 * time.Minute)},
	}
	SortByAttention(rows)
	if rows[0].Session != "zzz-two-minutes-old" {
		t.Errorf("order is %q then %q; the newest row in a rank comes first",
			rows[0].Session, rows[1].Session)
	}
}

// STABILITY, and this is the test the task exists to satisfy. `markActivity` moves on nearly
// every tick for a working pane, and m.cursor is an INDEX into the on-screen list — its own
// comment records that a changing list makes that index name a different pane from the one the
// operator is looking at. So the order must not move for activity inside one minute.
func TestOrderIsStableWithinOneMinute(t *testing.T) {
	base := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
	// The names are in the OPPOSITE order to the activity times, so a per-second recency rule
	// would visibly reorder them and the alphabetical tiebreak cannot mask the movement.
	build := func(offset time.Duration) []Pane {
		return []Pane{
			{Host: "h", Session: "aaa", PaneID: "%1",
				ClassifiedState: state.Works, Activity: base.Add(offset)},
			{Host: "h", Session: "mmm", PaneID: "%2",
				ClassifiedState: state.Works, Activity: base.Add(20 * time.Second)},
			{Host: "h", Session: "zzz", PaneID: "%3",
				ClassifiedState: state.Works, Activity: base.Add(50 * time.Second)},
		}
	}
	first := build(0)
	SortByAttention(first)
	want := []string{first[0].Session, first[1].Session, first[2].Session}

	// Advance alpha's activity by seconds, inside the same minute bucket.
	second := build(40 * time.Second)
	SortByAttention(second)
	for i, w := range want {
		if second[i].Session != w {
			t.Fatalf("the order moved inside one minute: %q then %q then %q, was %v",
				second[0].Session, second[1].Session, second[2].Session, want)
		}
	}
}

// A zero Activity is UNKNOWN, not the beginning of time — the same rule the Needs block already
// applies to StateSince. A row the registry has never stamped must not outrank a real one.
func TestAnUnstampedRowDoesNotOutrankAStampedOne(t *testing.T) {
	// Again the names fight the alphabetical tiebreak: the UNSTAMPED row sorts first by name, so
	// only the zero-Activity rule can move the stamped one above it.
	rows := []Pane{
		{Host: "h", Session: "aaa-unstamped", PaneID: "%1", ClassifiedState: state.Works},
		{Host: "h", Session: "zzz-stamped", PaneID: "%2",
			ClassifiedState: state.Works, Activity: time.Now().Add(-time.Hour)},
	}
	SortByAttention(rows)
	if rows[0].Session != "zzz-stamped" {
		t.Errorf("order is %q then %q; a known activity time outranks an unknown one",
			rows[0].Session, rows[1].Session)
	}
}

// Two WAITING rows that entered the state on the same tick must keep the stable alphabetical order,
// NOT be reordered by recency. This is the whole first frame rather than a corner case: UpdateAgents
// stamps every first-sight row with one `now`, so equal StateSince is the common case, and without an
// explicit exclusion the recency block reorders the waiting block newest-first — the exact inverse of
// longest-wait-first. The names run against the recency order so only the exclusion can produce the
// expected result.
func TestTwoWaitingRowsWithTheSameEntryTimeKeepTheStableOrder(t *testing.T) {
	entered := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
	rows := []Pane{
		{Host: "h", Session: "aaa-older-activity", PaneID: "%1", ClassifiedState: state.Needs,
			StateSince: entered, Activity: entered.Add(-time.Hour)},
		{Host: "h", Session: "zzz-newer-activity", PaneID: "%2", ClassifiedState: state.Needs,
			StateSince: entered, Activity: entered},
	}
	SortByAttention(rows)
	if rows[0].Session != "aaa-older-activity" {
		t.Errorf("order is %q then %q; inside the waiting block recency must not reorder anything",
			rows[0].Session, rows[1].Session)
	}
}

// The recency order must work on what the PRODUCER writes, not on a hand-built field. It does not for
// Activity alone: UpdateAgents sets `p.Activity = s.StartedAt` for a pane-less row and re-assigns the
// same constant every poll, so ordering agent rows by Activity orders them by BIRTH. What moves is
// StateSince, and this test goes through UpdateAgents twice to produce two rows that differ in it.
//
// The birth times are deliberately INVERTED against the state changes: the row that changed state
// later was born earlier. A comparator keyed on Activity puts them the wrong way round.
func TestAnAgentRowThatChangedStateLaterSortsFirstThroughTheProducer(t *testing.T) {
	t0 := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
	t1 := t0.Add(30 * time.Minute)
	older := agents.Session{SessionID: "aaaaaaaa-0000-0000-0000-000000000001", ID: "aaaaaaaa",
		Kind: "background", Name: "changed-long-ago", CWD: "/w/a", State: "failed",
		StartedAt: t0.Add(2 * time.Hour)} // born LATER
	newer := agents.Session{SessionID: "bbbbbbbb-0000-0000-0000-000000000002", ID: "bbbbbbbb",
		Kind: "background", Name: "changed-just-now", CWD: "/w/b", State: "working",
		StartedAt: t0} // born EARLIER

	r := New()
	r.UpdateAgents("local", []agents.Session{older, newer}, t0)
	// `newer` now moves to the same rank as `older`, so its StateSince is stamped at t1.
	newer.State = "failed"
	r.UpdateAgents("local", []agents.Session{older, newer}, t1)

	rows := r.Panes()
	if len(rows) != 2 {
		t.Fatalf("%d rows, want 2: %+v", len(rows), rows)
	}
	if rows[0].Session != "changed-just-now" {
		t.Errorf("order is %q then %q; the row whose state changed most recently comes first — "+
			"ordering by Activity would invert this, because for a pane-less row Activity is the "+
			"session's birth and never moves", rows[0].Session, rows[1].Session)
	}
}
