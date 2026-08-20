package agents

import "testing"

// Every word this fleet's Claude versions were MEASURED to report, and what the hub
// must make of each. The vocabulary is a version pair: 2.1.226+ says `working`/`done`
// in `state`, 2.1.224 says `busy`/`idle` in `status`, and both halves mean the same
// two things. The switch learned only the newer pair.
//
// Measured over 21 real sessions on two hosts: blocked 5, idle 7, working 3, busy 2,
// done 2, and 2 rows reporting neither field. Feeding `Attention()` that census
// returned "" — i.e. unknown — for ELEVEN of the 21, and the product's own JSON showed
// `kind=agent state=unknown` for 7 of 11 agent rows on a live run. Those rows render
// `?` on the dashboard.
//
// This table is the census, not a list of branches. The test that existed before it
// passed because both its fixtures said `working` and `done` — the two words the
// switch already named — so it could not see the other half of the vocabulary.
//
// `done` and `completed` used to answer "idle", which cost the operator two facts at once: the
// row printed the word §6's state table reserves for a LIVE session with a prompt, and it shared that
// state's rank, so a job that had ended sat among the sessions waiting to be typed into. They
// answer "done" now, and `idle` still answers "idle" — that word really does mean a live
// session on 2.1.224, so the separation has to be exact in both directions.
func TestAttentionCoversEveryWordTheFleetReports(t *testing.T) {
	for _, c := range []struct {
		word, want, why string
	}{
		{"blocked", "needs", "the whole point: this row is waiting for the operator"},
		{"working", "works", "2.1.226+ for producing output"},
		{"busy", "works", "2.1.224's word for the same thing, and measured on 2.1.233 interactive rows too"},
		{"running", "works", "already named, kept"},
		{"done", "done", "the job ENDED: no prompt, nothing to type into, nothing to wait for"},
		{"idle", "idle", "2.1.224's word for a LIVE interactive session, measured on both versions"},
		{"completed", "done", "the other spelling of the same fact"},
		{"failed", "error", "already named, kept"},
		{"error", "error", "already named, kept"},
		{"BLOCKED", "needs", "the switch lowercases, so case cannot decide a state"},
		{"Busy", "works", "same, for the word this fix adds"},
		{"", "", "a version reporting neither field is UNKNOWN, never a guess"},
		{"wat", "", "an unrecognised word is unknown, never the nearest match"},
	} {
		got := Session{State: c.word}.Attention()
		if got != c.want {
			t.Errorf("Attention(%q) = %q, want %q — %s", c.word, got, c.want, c.why)
		}
	}
}

// The consequence the census above is really about: a fleet of realistic rows must not
// come out mostly unknown. This asserts the OUTCOME rather than the branches, so a
// future switch that renames a case cannot pass by accident.
func TestARealisticCensusIsNotMostlyUnknown(t *testing.T) {
	// RE-MEASURED 2026-08-17 with `claude agents --json --all`, 28 records on the live
	// fleet — and the point of re-taking it is that the vendor's vocabulary GREW: `stopped`
	// was in no branch, so it fell to `default` and drew `? unknown`. A census is a snapshot,
	// and the words appear when a session happens to be in them.
	//
	// The two fields are not a version pair: `state` was on 27 of the 28 and `status` on the
	// 5 that also carry a pid, so both vocabularies are in this list.
	census := []string{
		"done", "done", "done", "done", "done", "done", "done", "done",
		"done", "done", "done", "done", "done", "done", "done", "done",
		"blocked", "blocked", "blocked", "blocked", "blocked",
		"failed", "failed", "failed",
		"working", "working",
		"stopped",
		"idle", "idle", "idle",
		"busy", "busy",
		"",
	}
	var unknown int
	for _, w := range census {
		if (Session{State: w}).Attention() == "" {
			unknown++
		}
	}
	// Only the row that reports NEITHER field may be unknown — one, in this census. Every
	// other unknown renders `?` on the dashboard, which says the hub cannot read the fleet.
	if unknown != 1 {
		t.Errorf("%d of %d rows unknown, want 1 — only a row reporting no state at all "+
			"may be unknown, and every other unknown renders `?` on the dashboard",
			unknown, len(census))
	}
}
