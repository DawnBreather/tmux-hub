package broadcast

import "testing"

func fresh() TargetState {
	return TargetState{
		PaneID: "%1", Host: "local",
		IdentifiedNow: true, IdentifiedAtSelection: true,
		SessionAtSelection: "$0", SessionNow: "$0",
		WindowAtSelection: "@0", WindowNow: "@0",
		EpochAtSelection: "1:99", EpochNow: "1:99",
		LastOutcome: Delivered, FromHistory: false, Bracketed: true,
	}
}

// "> 1 target" turned out to be neither necessary nor sufficient: every dangerous
// SINGLE-target send is one where something changed since selection, and the
// count rule fires on the safe common case of two freshly identified agents.
func TestOneFreshTargetSendsWithoutAsking(t *testing.T) {
	if got := Needed([]TargetState{fresh()}); len(got) != 0 {
		t.Errorf("Needed = %v, want nothing for a fresh single target", got)
	}
}

func TestTwoTargetsAlwaysAsk(t *testing.T) {
	got := Needed([]TargetState{fresh(), fresh()})
	if !hasReason(got, ReasonMultiple) {
		t.Errorf("Needed = %v, want ReasonMultiple", got)
	}
}

// Each clause on its own must trigger, or a disjunction with a broken arm looks
// exactly like a working one.
func TestEachClauseTriggersAlone(t *testing.T) {
	cases := []struct {
		name string
		want Reason
		mut  func(*TargetState)
	}{
		{"never identified", ReasonUnidentified, func(s *TargetState) {
			// BOTH false. With IdentifiedAtSelection left true this is the
			// exited-agent case instead, which is reported separately — so the
			// clause under test would never be reached and the case would pass
			// against a broken implementation.
			s.IdentifiedAtSelection, s.IdentifiedNow = false, false
		}},
		{"agent exited", ReasonAgentGone, func(s *TargetState) {
			s.IdentifiedAtSelection, s.IdentifiedNow = true, false
		}},
		{"moved session", ReasonMoved, func(s *TargetState) { s.SessionNow = "$7" }},
		{"moved window", ReasonMoved, func(s *TargetState) { s.WindowNow = "@7" }},
		{"server restarted", ReasonEpochChanged, func(s *TargetState) { s.EpochNow = "2:100" }},
		{"last send unwitnessed", ReasonLastUnwitnessed, func(s *TargetState) {
			s.LastOutcome = Unwitnessed
		}},
		{"from history", ReasonFromHistory, func(s *TargetState) { s.FromHistory = true }},
		{"no bracketed paste", ReasonNoBracketedPaste, func(s *TargetState) { s.Bracketed = false }},
	}
	for _, c := range cases {
		s := fresh()
		c.mut(&s)
		got := Needed([]TargetState{s})
		if !hasReason(got, c.want) {
			t.Errorf("%s: Needed = %v, want %v", c.name, got, c.want)
		}
	}
}

// The exited-agent case is the one a count rule misses entirely, and it is the
// one where the pane is now a SHELL — the measured 'bash: please: command not
// found' accident.
func TestTheExitedAgentCaseIsNotJustUnidentified(t *testing.T) {
	s := fresh()
	s.IdentifiedAtSelection, s.IdentifiedNow = true, false
	got := Needed([]TargetState{s})
	if !hasReason(got, ReasonAgentGone) {
		t.Fatalf("Needed = %v, want ReasonAgentGone specifically", got)
	}
}

// Reasons must be human sentences: a dialog that says "reason 3" is a dialog
// people dismiss.
func TestReasonsReadAsSentences(t *testing.T) {
	for _, r := range []Reason{ReasonMultiple, ReasonUnidentified, ReasonAgentGone,
		ReasonMoved, ReasonEpochChanged, ReasonLastUnwitnessed, ReasonFromHistory,
		ReasonNoBracketedPaste, ReasonAgentRunning, ReasonPaneDead} {
		if len(r) < 12 {
			t.Errorf("reason %q is too terse to be read", r)
		}
	}
}

// Kill confirmation reasons must always be present - killing always confirms.
func TestKillReasonsAreAlwaysReturned(t *testing.T) {
	// For an identified pane, must return ReasonAgentRunning
	identified := KillReasons(true, false)
	if !hasReason(identified, ReasonAgentRunning) {
		t.Errorf("kill of identified pane must include ReasonAgentRunning, got %v", identified)
	}

	// For a dead pane, must return ReasonPaneDead
	dead := KillReasons(false, true)
	if !hasReason(dead, ReasonPaneDead) {
		t.Errorf("kill of dead pane must include ReasonPaneDead, got %v", dead)
	}

	// For an unidentified live pane, must return ReasonUnidentified
	unidentified := KillReasons(false, false)
	if !hasReason(unidentified, ReasonUnidentified) {
		t.Errorf("kill of unidentified pane must include ReasonUnidentified, got %v", unidentified)
	}
}

func hasReason(got []Reason, want Reason) bool {
	for _, r := range got {
		if r == want {
			return true
		}
	}
	return false
}
