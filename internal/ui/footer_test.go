package ui

import (
	"strings"
	"testing"

	"github.com/DawnBreather/tmux-hub/internal/hub"
	"github.com/DawnBreather/tmux-hub/internal/lines"
	"github.com/DawnBreather/tmux-hub/internal/registry"
	"github.com/DawnBreather/tmux-hub/internal/state"

	"github.com/DawnBreather/tmux-hub/internal/project"
)

func fleetWithAVerboseReason() []hub.Host {
	return []hub.Host{
		{Label: "local", Status: hub.Up},
		{Label: "nuc", Status: hub.DegradedFormat,
			Reason: "polls but one field is empty: window_activity came back blank"},
		{Label: "dead", Status: hub.Down, Reason: "no live ssh master"},
	}
}

// A verbose reason on a HEALTHY-ish host hid the host that was DOWN: the footer is one line,
// reasons are inline, and a hard truncate at 80 columns — the size §16 commits to — cut
// mid-word and lost `dead down` entirely. So the one positive assertion about fleet health
// lost the failing host first (known-issues S1, class L2).
func TestTheFooterNeverLosesAHostToAnotherHostsReason(t *testing.T) {
	for _, cols := range []int{80, 100, 120} {
		got := footerLine(fleetWithAVerboseReason(), 0, hiddenTally{}, cols)
		if lines.Width(got) > cols {
			t.Errorf("%d cols: footer is %d wide: %q", cols, lines.Width(got), got)
		}
		for _, host := range []string{"local", "nuc", "dead"} {
			if !strings.Contains(got, host) {
				t.Errorf("%d cols: %q lost host %q — a status line that omits the broken "+
					"host is worse than one that omits why", cols, got, host)
			}
		}
		if !strings.Contains(got, "down") {
			t.Errorf("%d cols: %q does not say the down host is down", cols, got)
		}
	}
}

// The reason that survives first is the one on a host that is NOT up: that is the actionable
// half, and the defect was keeping the label while losing the action.
func TestTheFooterKeepsAFailingHostsReasonBeforeAHealthyOnes(t *testing.T) {
	hosts := []hub.Host{
		{Label: "a", Status: hub.Up, Reason: "a reason nobody needs about a host that is fine"},
		{Label: "b", Status: hub.Down, Reason: "no live ssh master"},
	}
	got := footerLine(hosts, 0, hiddenTally{}, 60)
	if !strings.Contains(got, "no live ssh master") {
		t.Errorf("footer = %q dropped the DOWN host's reason", got)
	}
	if strings.Contains(got, "nobody needs") {
		t.Errorf("footer = %q kept a healthy host's reason at the expense of room", got)
	}
}

// The counts survive a REASON being dropped: a footer that silently loses "3 marked" makes
// the next enter a surprise. Given room for both, both are there.
func TestTheFooterKeepsTheCountsEvenWhenItMustDropReasons(t *testing.T) {
	got := footerLine(fleetWithAVerboseReason(), 3, hiddenTally{Marked: 2, Waiting: 1}, 100)
	for _, want := range []string{"3 marked", "2 hidden", "1 of them waiting for input"} {
		if !strings.Contains(got, want) {
			t.Errorf("footer = %q lost %q", got, want)
		}
	}
	if lines.Width(got) > 100 {
		t.Errorf("footer is %d wide: %q", lines.Width(got), got)
	}
}

// And where they cannot BOTH fit, the priority is explicit: the hosts win and the count tail
// drops MARKED. This test previously asserted the opposite — that every count survives at 80
// — and it was wrong, not the code: S1's ruling is that "a status line that omits the broken
// host is worse than one that omits why", and `N of them waiting for input` is a detail of
// `N hidden` rather than a fact of its own. What must never happen is a SILENT loss, so the
// `+N` is asserted here.
func TestWhereHostsAndCountsCannotBothFitTheHostsWin(t *testing.T) {
	got := footerLine(fleetWithAVerboseReason(), 3, hiddenTally{Marked: 2, Waiting: 1}, 80)
	if lines.Width(got) > 80 {
		t.Fatalf("footer is %d wide: %q", lines.Width(got), got)
	}
	for _, h := range fleetWithAVerboseReason() {
		if !strings.Contains(got, h.Label) {
			t.Errorf("host %q lost to a count: %q", h.Label, got)
		}
	}
	if strings.Contains(got, "1 of them waiting for input") {
		t.Errorf("the tail count survived at the expense of a host: %q", got)
	}
	if !strings.Contains(got, "+") {
		t.Errorf("a count was dropped SILENTLY: %q", got)
	}
	if !strings.Contains(got, "3 marked") {
		t.Errorf("the marked count — the one an enter depends on — dropped before the "+
			"hidden detail: %q", got)
	}
}

// A fleet too wide for its own labels degrades by design rather than by byte count: the
// dropped hosts are COUNTED, so the operator knows the line is not the whole fleet.
func TestAFleetWiderThanTheTerminalSaysHowManyItDropped(t *testing.T) {
	var hosts []hub.Host
	for _, l := range []string{"alpha", "bravo", "charlie", "delta", "echo", "foxtrot", "golf"} {
		hosts = append(hosts, hub.Host{Label: l, Status: hub.Up})
	}
	got := footerLine(hosts, 0, hiddenTally{}, 40)
	if lines.Width(got) > 40 {
		t.Errorf("footer is %d wide: %q", lines.Width(got), got)
	}
	if !strings.Contains(got, "+") {
		t.Errorf("footer = %q dropped hosts without saying how many", got)
	}
	if !strings.Contains(got, "alpha") {
		t.Errorf("footer = %q lost the first host", got)
	}
}

// The header is the same class as the footer: title, count and hint concatenated, then hard
// truncated — so at 80 columns, the size §16 commits to, the hint lost its SENTENCE and kept
// its label (`… C-b C-b d leaves the inne`). The keystroke is the only actionable part
// (known-issues S3, class L2).
func TestTheHeaderDropsTheHintWholeRatherThanCuttingIt(t *testing.T) {
	panes := []registry.Pane{
		pane("local", "w", "claude", 0, "claude", state.Needs),
		pane("local", "w", "claude", 1, "claude", state.Works),
	}
	hint := "a → a window with the attach, C-b C-b d leaves the inner tmux"
	for _, cols := range []int{80, 100, 120} {
		out := Render(Frame{Panes: panes, Hosts: hosts2(), Width: cols, Height: 24, Cursor: 0, Marked: nil, Note: "", Hint: hint, HiddenCount: 0, BlockedCount: 0, Resurfaced: nil, Aliases: project.Aliases{}})
		head := strings.SplitN(out, "\n", 2)[0]
		if lines.Width(head) > cols {
			t.Errorf("%d cols: header is %d wide: %q", cols, lines.Width(head), head)
		}
		// The count is the identity and must always be there.
		if !strings.Contains(head, "2 sessions") {
			t.Errorf("%d cols: header lost the session count: %q", cols, head)
		}
		// The hint is either WHOLE or absent — never a fragment, because a cut sentence
		// keeps the label and loses the keystroke.
		if strings.Contains(head, "C-b") && !strings.Contains(head, hint) {
			t.Errorf("%d cols: the hint was CUT rather than dropped: %q", cols, head)
		}
		// And dropping it must not ANNOUNCE a loss: `+1` beside the session count reads as
		// "one session is missing", which is a lie about the fleet on the line that carries
		// the count. Measured — that is what the first version of this fix printed.
		if strings.Contains(head, "+") {
			t.Errorf("%d cols: the header claims something is missing: %q", cols, head)
		}
	}
}

// At a width where the hint does fit, it must actually be shown — or the fix would be
// "never show the hint", which is not a fix.
func TestTheHintIsShownWhenThereIsRoom(t *testing.T) {
	panes := []registry.Pane{pane("local", "w", "claude", 0, "claude", state.Needs)}
	hint := "a → jump into the pane, C-b L comes back"
	out := Render(Frame{Panes: panes, Hosts: hosts2(), Width: 120, Height: 24, Cursor: 0, Marked: nil, Note: "", Hint: hint, HiddenCount: 0, BlockedCount: 0, Resurfaced: nil, Aliases: project.Aliases{}})
	if head := strings.SplitN(out, "\n", 2)[0]; !strings.Contains(head, hint) {
		t.Errorf("the hint fits at 120 columns and is not shown: %q", head)
	}
}

// Rows sort by ATTENTION across sessions, so one session's header can come round twice —
// `LOCAL API`, then `NUC DEPLOY`, then `LOCAL API` again when a quieter pane of the first
// session arrives. The order is correct and is the point; what reads as a bug is a header
// that appears twice with nothing saying why (known-issues S2).
func TestARepeatedSessionHeaderSaysItIsAContinuation(t *testing.T) {
	// api holds the waiting pane and a quiet one; deploy sits between them by attention.
	rows := []registry.Pane{
		{Kind: registry.KindPane, Host: "local", Session: "api", PaneID: "%0",
			Command: "claude", ClassifiedState: state.Needs},
		{Kind: registry.KindPane, Host: "nuc", Session: "deploy", PaneID: "%1",
			Command: "claude", ClassifiedState: state.Error},
		{Kind: registry.KindPane, Host: "local", Session: "api", PaneID: "%2",
			Command: "tail", ClassifiedState: state.Quiet},
	}
	out := Render(Frame{Panes: rows, Hosts: hosts2(), Width: 100, Height: 24, Cursor: 0, Marked: nil, Note: "", Hint: "", HiddenCount: 0, BlockedCount: 0, Resurfaced: nil, Aliases: project.Aliases{}})

	// The order is untouched: the waiting pane first, the broken one second. Found by whatever
	// each row DRAWS — the two `local/api` panes share a session so they carry their ids, and the
	// lone `nuc/deploy` row carries its name instead (rowPaneID).
	iNeeds := strings.Index(out, rowNeedle(rows[0], rows))
	iError := strings.Index(out, rowNeedle(rows[1], rows))
	iQuiet := strings.Index(out, rowNeedle(rows[2], rows))
	if !(iNeeds < iError && iError < iQuiet) {
		t.Fatalf("the attention order moved: needs=%d error=%d quiet=%d\n%s",
			iNeeds, iError, iQuiet, out)
	}
	// The header does come round twice — that is the sort working.
	first := strings.Index(out, "LOCAL API")
	second := strings.Index(out[first+1:], "LOCAL API")
	if second < 0 {
		t.Fatalf("the header appears once; this fixture is supposed to repeat it:\n%s", out)
	}
	// And the second one says so, so it does not read as a duplicate.
	line := out[first+1+second:]
	if end := strings.Index(line, "\n"); end >= 0 {
		line = line[:end]
	}
	if !strings.Contains(strings.ToLower(line), "cont") {
		t.Errorf("the repeated header is %q with nothing marking it as a continuation", line)
	}
	_ = line
	// The FIRST one must NOT be marked, or the mark would stop meaning anything.
	head := out[first:]
	if end := strings.Index(head, "\n"); end >= 0 {
		head = head[:end]
	}
	if strings.Contains(strings.ToLower(head), "cont") {
		t.Errorf("the first header is marked as a continuation: %q", head)
	}
}

// THE COUNTS MUST NOT EVICT A HOST. footerLine reserved the counts' full width before any
// host was considered, so at 80 columns — the size §16 commits to — `dead down` vanished
// while the three identities were only 42 of the 80. That is the very defect S1 records, in
// the code that declares it fixed, and none of the three tests above could see it: they use
// counts 0,0,0 or assert only the counts and the width.
func TestTheCountsNeverEvictAHost(t *testing.T) {
	for _, c := range []struct {
		name                    string
		hosts                   []hub.Host
		marked, hidden, blocked int
		cols                    int
	}{
		{"a verbose reason plus every count", fleetWithAVerboseReason(), 3, 2, 1, 80},
		{"five hosts, modest counts", []hub.Host{
			{Label: "local", Status: hub.Up}, {Label: "nuc", Status: hub.Up},
			{Label: "eu", Status: hub.Up}, {Label: "jj", Status: hub.Up},
			{Label: "dead", Status: hub.Down, Reason: "no live ssh master"},
		}, 0, 3, 1, 80},
		{"one long label and one down host", []hub.Host{
			{Label: "aggregator-eu-west-1", Status: hub.Up},
			{Label: "dead", Status: hub.Down, Reason: "no live ssh master"},
		}, 3, 2, 1, 80},
	} {
		got := footerLine(c.hosts, c.marked, hiddenTally{Marked: c.hidden, Waiting: c.blocked}, c.cols)
		if lines.Width(got) > c.cols {
			t.Errorf("%s: %d wide: %q", c.name, lines.Width(got), got)
		}
		for _, h := range c.hosts {
			if !strings.Contains(got, h.Label) {
				t.Errorf("%s: host %q was evicted by the counts:\n  %q", c.name, h.Label, got)
			}
		}
		if !strings.Contains(got, "down") {
			t.Errorf("%s: the down host's STATUS is gone:\n  %q", c.name, got)
		}
	}
}

// When the counts alone are as wide as the terminal there is no room to reserve, and the
// footer must still name the fleet rather than printing a line about the operator's own
// selection with no hosts on it.
func TestCountsWiderThanTheTerminalDoNotEraseTheFleet(t *testing.T) {
	hosts := []hub.Host{{Label: "local", Status: hub.Up}, {Label: "dead", Status: hub.Down}}
	for _, cols := range []int{20, 30, 40, 55} {
		got := footerLine(hosts, 3, hiddenTally{Marked: 2, Waiting: 1}, cols)
		if lines.Width(got) > cols {
			t.Errorf("%d cols: %d wide: %q", cols, lines.Width(got), got)
		}
		if !strings.Contains(got, "local") {
			t.Errorf("%d cols: the fleet is not named at all: %q", cols, got)
		}
	}
}

// The mark has to SURVIVE the width, and it did not: it was appended to a full label and then
// hard-truncated at the inbox's fixed 28 columns, so for any HOST SESSION label at or over 28
// the repeated header rendered byte-identically to the first — the whole S2 fix invisible —
// and at 17-27 columns it rendered as the fragment "  (co".
//
// `lines.Fit` is NOT the tool here: it drops from the tail, so it would drop the mark and then
// truncate the label to the same 28 columns. The mark has to OUTRANK the name's tail.
func TestTheContinuationMarkSurvivesALongSessionName(t *testing.T) {
	for _, session := range []string{
		"payments-checkout-service", // 25 + "LOCAL " = past the 28-column inbox
		"payments-checkout",         // 17: the fragment case
		"api",                       // short: the mark fits whole
	} {
		rows := []registry.Pane{
			{Kind: registry.KindPane, Host: "local", Session: session, PaneID: "%0",
				Command: "claude", ClassifiedState: state.Needs},
			{Kind: registry.KindPane, Host: "nuc", Session: "deploy", PaneID: "%1",
				Command: "claude", ClassifiedState: state.Error},
			{Kind: registry.KindPane, Host: "local", Session: session, PaneID: "%2",
				Command: "tail", ClassifiedState: state.Quiet},
		}
		// 120 columns puts the inbox in its grouped layout, where the column is 28 wide
		// whatever the terminal is — which is why a wide terminal does not save the mark.
		out := Render(Frame{Panes: rows, Hosts: hosts2(), Width: 120, Height: 24, Cursor: 0, Marked: nil, Note: "", Hint: "", HiddenCount: 0, BlockedCount: 0, Resurfaced: nil, Aliases: project.Aliases{}})
		if !strings.Contains(strings.ToLower(out), "cont") {
			t.Errorf("session %q: no continuation mark anywhere in the frame:\n%s", session, out)
			continue
		}
		// And never a FRAGMENT of it: "(co" says nothing and looks like corruption.
		for _, line := range strings.Split(out, "\n") {
			low := strings.ToLower(line)
			if !strings.Contains(low, "(c") {
				continue
			}
			if !strings.Contains(low, "(cont.)") {
				t.Errorf("session %q: the mark is a fragment: %q", session, strings.TrimRight(line, " "))
			}
		}
	}
}

// A note REPLACED the host line, so the one positive assertion about fleet health vanished at
// the exact moment the operator was acting — a host in `degraded:format` invisible while the
// answer to their keystroke showed (known-issues M1). The priority was right and the mechanism
// was replacement, which is the third instance of that pair on this branch.
func TestANoteSharesTheFooterWithTheHostLine(t *testing.T) {
	hosts := []hub.Host{
		{Label: "local", Status: hub.Up},
		{Label: "nuc", Status: hub.DegradedFormat, Reason: "window_activity came back blank"},
	}
	panes := []registry.Pane{pane("local", "w", "claude", 0, "claude", state.Needs)}

	// At a comfortable width both are there, and the note comes FIRST because it answers
	// something just pressed.
	out := Render(Frame{Panes: panes, Hosts: hosts, Width: 120, Height: 24,
		Note: "sent to 2 panes"})
	foot := lastNonBlank(out)
	if !strings.Contains(foot, "sent to 2 panes") {
		t.Errorf("the note is not on the footer: %q", foot)
	}
	if !strings.Contains(foot, "nuc") {
		t.Errorf("the host line vanished behind a note: %q — a host in degraded:format is "+
			"invisible exactly when the operator is acting", foot)
	}
	if strings.Index(foot, "sent to") > strings.Index(foot, "local") {
		t.Errorf("the note is not first: %q", foot)
	}

	// At 80 — the size §16 commits to — they may not both fit, and then the loss is MARKED
	// rather than silent.
	narrow := Render(Frame{Panes: panes, Hosts: hosts, Width: 80, Height: 24,
		Note: "sent to 2 panes, 1 refused: no bracketed paste on that pane"})
	nfoot := lastNonBlank(narrow)
	if lines.Width(nfoot) > 80 {
		t.Errorf("the footer is %d wide at 80: %q", lines.Width(nfoot), nfoot)
	}
	if !strings.Contains(nfoot, "sent to 2 panes") {
		t.Errorf("the note lost to the host line at 80: %q", nfoot)
	}
	if !strings.Contains(nfoot, "nuc") && !strings.Contains(nfoot, "+") {
		t.Errorf("the host line was dropped SILENTLY: %q", nfoot)
	}
}

// With no note the footer is the host line alone, unmarked — the resting state must not carry
// a `+N` for something that was never there.
func TestWithNoNoteTheFooterIsJustTheFleet(t *testing.T) {
	out := Render(Frame{Panes: []registry.Pane{pane("local", "w", "claude", 0, "claude", state.Needs)},
		Hosts: hosts2(), Width: 120, Height: 24})
	foot := lastNonBlank(out)
	if strings.Contains(foot, "+") {
		t.Errorf("the resting footer claims something is missing: %q", foot)
	}
	if !strings.Contains(foot, "local") {
		t.Errorf("the resting footer does not name the fleet: %q", foot)
	}
}

func lastNonBlank(screen string) string {
	rows := strings.Split(screen, "\n")
	for i := len(rows) - 1; i >= 0; i-- {
		if strings.TrimSpace(rows[i]) != "" {
			return rows[i]
		}
	}
	return ""
}

// THE FOOTER MUST DEGRADE, NOT VANISH. `hostLine` composes the fleet for the FULL width and
// the result is then handed to Fit as ONE part, so when a note shares the row the fleet can
// only be dropped WHOLE — at 80 columns, the size §16 commits to, M1's symptom is back in a
// different shape: not "replaced" but "all or nothing".
//
// What must happen instead is one list of parts sized together, so a long note costs the fleet
// its REASONS and then its last hosts, one at a time, with the loss counted.
func TestTheFooterDegradesTheFleetRatherThanDroppingItWhole(t *testing.T) {
	hosts := []hub.Host{
		{Label: "local", Status: hub.Up},
		{Label: "nuc", Status: hub.DegradedFormat, Reason: "window_activity came back blank"},
		{Label: "dead", Status: hub.Down, Reason: "no live ssh master"},
	}
	panes := []registry.Pane{pane("local", "w", "claude", 0, "claude", state.Needs)}

	for _, note := range []string{
		"sent to 2 panes",
		"sent to 2 panes, 1 refused: no bracketed paste on that pane",
	} {
		out := Render(Frame{Panes: panes, Hosts: hosts, Width: 80, Height: 24, Note: note})
		foot := lastNonBlank(out)
		if lines.Width(foot) > 80 {
			t.Errorf("note %q: footer is %d wide: %q", note, lines.Width(foot), foot)
		}
		if !strings.Contains(foot, "sent to 2 panes") {
			t.Errorf("note %q: the note is not on the footer: %q", note, foot)
		}
		// At least the FIRST host survives beside any note that itself fits — the fleet
		// degrades by dropping reasons and trailing hosts, never by disappearing.
		if !strings.Contains(foot, "local") {
			t.Errorf("note %q: the whole fleet vanished rather than degrading: %q", note, foot)
		}
	}
}

// And the footer must be MONOTONIC in width: widening the terminal can never show LESS of the
// fleet. A pre-composed fleet string makes that fail, because the composition is sized for a
// width the row does not have.
func TestTheFooterNeverShowsLessFleetOnAWiderTerminal(t *testing.T) {
	hosts := []hub.Host{
		{Label: "local", Status: hub.Up},
		{Label: "nuc", Status: hub.DegradedFormat, Reason: "window_activity came back blank"},
		{Label: "dead", Status: hub.Down, Reason: "no live ssh master"},
	}
	panes := []registry.Pane{pane("local", "w", "claude", 0, "claude", state.Needs)}
	note := "sent to 2 panes, 1 refused: no bracketed paste on that pane"

	prev := -1
	for cols := 60; cols <= 200; cols += 4 {
		foot := lastNonBlank(Render(Frame{Panes: panes, Hosts: hosts, Width: cols,
			Height: 24, Note: note}))
		if lines.Width(foot) > cols {
			t.Fatalf("%d cols: footer is %d wide: %q", cols, lines.Width(foot), foot)
		}
		shown := 0
		for _, h := range hosts {
			if strings.Contains(foot, h.Label) {
				shown++
			}
		}
		if prev >= 0 && shown < prev {
			t.Errorf("%d cols shows %d hosts where the narrower terminal showed %d: %q",
				cols, shown, prev, foot)
		}
		prev = shown
	}
}

// realTransportReason is the shape a `down` host's Reason actually holds: the remedy sentence
// internal/tmux's explainTransport writes, then ssh's raw stderr in brackets.
//
// Every fact about it was measured against the real producers rather than invented. explainTransport
// (internal/tmux/run.go:416) builds `host "x" was not reached and nothing was sent: no live ssh
// master at … — respawn it with …` and appends `[ssh said: <stderr>]`; hub's reasonFor drops the
// send-shaped preamble, because a poll sent nothing; and ssh's stderr is MULTI-LINE, with `\r\n`
// endings, because it writes one line per failed attempt.
func realTransportReason() string {
	return "no live ssh master at " +
		"/tmp/hub-dead — respawn it with `ssh -N -M -S /tmp/hub-dead dead`, then retry " +
		"[ssh said: ssh: connect to host dead port 22: No route to host\r\n" +
		"kex_exchange_identification: read: Connection reset by peer\r\n" +
		"banner exchange: Connection to 10.0.0.9 port 22: invalid format]"
}

// The footer is ONE line, and a reason it did not write must not be able to change that.
//
// Measured before the fix, at 80, 120 and 200 columns alike: a two-line reason short enough to fit
// produced `local up · dead down (dial failed\nretry later)` — a two-line footer, which pushes the
// frame past the terminal's height and puts a row off the bottom of the screen. A `\r` is worse
// than a `\n` there: it returns the cursor to column 0 and the rest of the footer overwrites what
// the row already drew.
func TestAReasonWithLineBreaksCannotBreakTheFooter(t *testing.T) {
	for _, c := range []struct{ name, reason string }{
		{"short enough to fit", "dial failed\nretry later"},
		{"ssh's own multi-line stderr", realTransportReason()},
		{"carriage returns only", "dial failed\rretry later"},
	} {
		hosts := []hub.Host{{Label: "local", Status: hub.Up},
			{Label: "dead", Status: hub.Down, Reason: c.reason}}
		for _, cols := range []int{80, 120, 200} {
			got := footerLine(hosts, 0, hiddenTally{}, cols)
			if strings.ContainsAny(got, "\n\r") {
				t.Errorf("%s at %d cols: the footer is not one line: %q", c.name, cols, got)
			}
			if lines.Width(got) > cols {
				t.Errorf("%s at %d cols: %d wide: %q", c.name, cols, lines.Width(got), got)
			}
			if !strings.Contains(got, "dead down") {
				t.Errorf("%s at %d cols: the down host lost its status: %q", c.name, cols, got)
			}
		}
	}
}

// A reason too long to fit must be MARKED, not dropped in silence — the same contract lines.Fit
// keeps with its `+N`, and the rule this file already states for the counts: "what must never
// happen is a SILENT loss".
//
// Measured before the fix: the real 320-column transport reason was reverted whole at 80, 120 AND
// 200 columns, so at no width could the operator learn that the hub had anything to say about why
// `dead` was down.
func TestAReasonTooLongToFitIsMarkedRatherThanDroppedInSilence(t *testing.T) {
	hosts := []hub.Host{{Label: "local", Status: hub.Up},
		{Label: "dead", Status: hub.Down, Reason: realTransportReason()}}
	for _, cols := range []int{80, 120, 200} {
		got := footerLine(hosts, 0, hiddenTally{}, cols)
		if lines.Width(got) > cols {
			t.Fatalf("%d cols: %d wide: %q", cols, lines.Width(got), got)
		}
		if !strings.Contains(got, "…") {
			t.Errorf("%d cols: the reason went without a word: %q", cols, got)
		}
		if !strings.Contains(got, "no live ssh master") {
			t.Errorf("%d cols: what survives of the reason does not name the fault: %q", cols, got)
		}
	}

	// The control that says the marker means something: a reason that FITS carries no marker.
	short := []hub.Host{{Label: "local", Status: hub.Up},
		{Label: "dead", Status: hub.Down, Reason: "no live ssh master"}}
	if got := footerLine(short, 0, hiddenTally{}, 80); strings.Contains(got, "…") {
		t.Errorf("a reason that fits whole was marked as cut: %q", got)
	}
}

// An UP host's reason is informational, so it keeps the all-or-nothing rule: a fragment of
// `polls but one field is empty: window_…` is noise where a fragment of a DOWN host's reason is
// the only thing on the screen about a host the operator cannot reach.
func TestAHealthyHostsReasonIsNeverShownInFragments(t *testing.T) {
	hosts := []hub.Host{
		{Label: "a", Status: hub.Up, Reason: "a reason nobody needs about a host that is fine"},
		{Label: "b", Status: hub.Down, Reason: "no live ssh master"},
	}
	got := footerLine(hosts, 0, hiddenTally{}, 60)
	if strings.Contains(got, "nobody") {
		t.Errorf("a healthy host's reason appeared in fragments: %q", got)
	}
	if !strings.Contains(got, "no live ssh master") {
		t.Errorf("and the actionable one is gone: %q", got)
	}
}
