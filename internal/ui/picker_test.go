package ui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/DawnBreather/tmux-hub/internal/hostset"
	"github.com/DawnBreather/tmux-hub/internal/hub"
	"github.com/DawnBreather/tmux-hub/internal/lines"
	"github.com/DawnBreather/tmux-hub/internal/state"
)

// lineContaining returns the ONE line of a screen that holds `want`. Exactly one,
// because a row assertion satisfied by some other line is the shape of test that
// passes against an unmodified product: this repo has already shipped a pressed key
// asserted by a string the neighbouring screen also carries.
func lineContaining(t *testing.T, view, want string) string {
	t.Helper()
	var hits []string
	for _, l := range strings.Split(view, "\n") {
		if strings.Contains(l, want) {
			hits = append(hits, l)
		}
	}
	switch len(hits) {
	case 1:
		return hits[0]
	case 0:
		t.Fatalf("no line of the screen holds %q:\n%s", want, view)
	}
	t.Fatalf("%d lines hold %q, so an assertion on one of them proves nothing:\n%s",
		len(hits), want, view)
	return ""
}

// answers is a prober that hands back one fixed round. It returns CANDIDATES and
// RESULTS, not rows, which is the shape that keeps "hosts.toml outranks the probe"
// inside the picker instead of in the caller's closure.
func answers(results ...hostset.Result) func() ([]hostset.Candidate, []hostset.Result, error) {
	cands := make([]hostset.Candidate, len(results))
	for i, r := range results {
		cands[i] = hostset.Candidate{Alias: r.Alias}
	}
	return func() ([]hostset.Candidate, []hostset.Result, error) { return cands, results, nil }
}

// The timeout reason exactly as hostset.reasonFor builds it, because its LENGTH is the
// subject of the 80-column assertion and a shortened paraphrase would not fail.
const slowReason = "no answer in 10s — this host is slow rather than absent; " +
	"enable it anyway, or raise --probe-timeout"

// longToken is a 70-column path with no space in it — the shape ssh's own errors put into
// a reason, since `hostset.reasonFor`'s two fall-through arms embed `firstLine(stderr)`
// verbatim and `firstLine` bounds nothing but the newline. A real
// `unix_listener: cannot bind to path …` measured 97 columns.
const longToken = "/run/user/1000/cm-0a1b2c3d-a-very-long-hostname.example.internal.sock"

// targetFrameRows is the fleet of the approved target frame: twenty candidates, of
// which five answer with tmux. The first six are the ones the frame shows; the
// fourteen behind them are what makes it scroll, and exactly one of those is usable
// so the count line reads `5 answer with tmux` rather than four.
func targetFrameRows() []PickerRow {
	rows := []PickerRow{
		{Alias: "nuc", Version: "3.2a", Usable: true, Enabled: true},
		{Alias: "web-app", Version: "3.2a", Usable: true, Enabled: true},
		{Alias: "eu", Version: "3.2a", Usable: true},
		{Alias: "side-desk", Version: "3.4", Usable: true, Enabled: true},
		{Alias: "github.com", Reason: "not a shell host — this is a git remote, so leave it off"},
		{Alias: "hermes-ws", Reason: "no tmux — install it there, or leave this host off"},
		{Alias: "web-db", Version: "3.2a", Usable: true, Enabled: true},
	}
	for len(rows) < 20 {
		rows = append(rows, PickerRow{
			Alias:  fmt.Sprintf("stale%02d", len(rows)),
			Reason: "DNS does not resolve — a stale ssh config entry? fix or remove it",
		})
	}
	return rows
}

// Every excluded host shows its reason ON SCREEN, because the picker is where a
// person decides, and "nuc is missing" without "DNS does not resolve" sends them
// to read logs the hub already read.
func TestThePickerShowsEveryReasonOnTheScreenViewReturns(t *testing.T) {
	rows := []PickerRow{
		{Alias: "nuc", Version: "3.2a", Usable: true, Enabled: true},
		{Alias: "side-desk", Version: "3.4", Usable: true},
		{Alias: "github.com", Reason: "not a shell host — this is a git remote, so leave it off"},
		{Alias: "hermes-ws", Reason: "no tmux — install it there, or leave this host off"},
	}
	m := base(t, 120, 24)
	m.mode = modePicker
	m.picker = rows
	view := m.View()
	for _, want := range []string{"nuc", "3.2a", "github.com", "git remote", "hermes-ws", "no tmux"} {
		if !strings.Contains(view, want) {
			t.Errorf("View() does not show %q:\n%s", want, view)
		}
	}
}

// A usable host that is off must be visibly off, and an unusable one must not look
// like a choice the user failed to make.
func TestThePickerDistinguishesOffFromUnusable(t *testing.T) {
	m := base(t, 120, 24)
	m.mode = modePicker
	m.picker = []PickerRow{
		{Alias: "eu", Version: "3.2a", Usable: true, Enabled: false},
		{Alias: "github.com", Reason: "not a shell host — this is a git remote, so leave it off"},
	}
	view := m.View()
	eu := lineContaining(t, view, "eu")
	gh := lineContaining(t, view, "github.com")
	if !strings.Contains(eu, "[ ]") {
		t.Errorf("a usable host that is off must show an empty box: %q", eu)
	}
	if strings.Contains(gh, "[ ]") || strings.Contains(gh, "[x]") {
		t.Errorf("an unusable host must not offer a box to tick: %q", gh)
	}
}

// Twenty candidates and six rows: the list scrolls, and it says so. Without this the
// cursor walks off the screen exactly as it did in the inbox before §16's fix.
func TestThePickerScrollsAndSaysHowMuchIsLeft(t *testing.T) {
	rows := make([]PickerRow, 20)
	for i := range rows {
		rows[i] = PickerRow{Alias: fmt.Sprintf("host%02d", i), Version: "3.4", Usable: true}
	}
	m := base(t, 120, 24)
	m.mode = modePicker
	m.picker = rows
	m.pickerCursor = 17
	view := m.View()
	if !strings.Contains(view, "host17") {
		t.Errorf("the cursor's row is off screen — the list does not scroll:\n%s", view)
	}
	if !strings.Contains(view, "more") {
		t.Errorf("the list is truncated without saying so:\n%s", view)
	}
}

// space toggles only what can be enabled.
func TestSpaceCannotEnableAnUnusableHost(t *testing.T) {
	m := base(t, 120, 24)
	m.mode = modePicker
	m.picker = []PickerRow{{Alias: "github.com", Reason: "not a shell host — a git remote"}}
	m.pickerCursor = 0
	after, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{' '}})
	if after.(model).picker[0].Enabled {
		t.Error("space enabled a host the probe said cannot work")
	}
}

// A refused toggle is not a silent no-op. `github.com` already carries its remedy on
// its own row, but the key still has to answer, because a key that does nothing and
// says nothing reads as a broken key.
func TestARefusedToggleSaysWhyOnScreen(t *testing.T) {
	m := base(t, 120, 24)
	m.mode = modePicker
	m.picker = []PickerRow{{Alias: "github.com", Reason: "not a shell host — a git remote"}}
	after, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{' '}})
	got := after.(model)
	if got.note == "" {
		t.Fatal("space on an unusable host was silent")
	}
	if !strings.Contains(got.View(), got.note) {
		t.Errorf("the note never reached the screen:\n%s", got.View())
	}
}

// A TIMED-OUT host keeps its tick box. This is the measurement the whole state
// exists for: `eu` answered `tmux 3.2a` at 5.4 s, 9.1 s, 15.7 s and 18.4 s and
// `web-app` at 4.4 s, 7.4 s and 19.6 s — two of five usable hosts straddling any
// fixed timeout — and both were read as "the host is gone" on first encounter by two
// different readers. A screen that answers a fourfold latency swing by withdrawing
// the tick box makes a coin flip look like a finding.
func TestATimedOutHostKeepsItsBoxAndReadsAsSlowRatherThanAbsent(t *testing.T) {
	m := base(t, 120, 24)
	m.mode = modePicker
	m.picker = []PickerRow{
		{Alias: "eu", Reason: slowReason, TimedOut: true},
		{Alias: "github.com", Reason: "not a shell host — this is a git remote, so leave it off"},
	}
	view := m.View()
	eu := lineContaining(t, view, "eu")
	if !strings.Contains(eu, "[ ]") {
		t.Errorf("a timed-out host lost its tick box, which reads as excluded: %q", eu)
	}
	if !strings.Contains(eu, "slow rather than absent") {
		t.Errorf("the row does not say the host is slow rather than absent: %q", eu)
	}
	// The discriminator: a host that answered something POSITIVELY else and was never
	// the user's still loses the box, or "keeps its box" would be satisfied by giving
	// every row one.
	if gh := lineContaining(t, view, "github.com"); strings.Contains(gh, "[") {
		t.Errorf("a host that answered it is not a shell still offered a box: %q", gh)
	}
}

// And the box is not decoration: the user may enable a slow host anyway, which is
// the second remedy its reason names.
func TestSpaceCanEnableATimedOutHost(t *testing.T) {
	m := base(t, 120, 24)
	m.mode = modePicker
	m.picker = []PickerRow{{Alias: "eu", Reason: slowReason, TimedOut: true}}
	after, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{' '}})
	got := after.(model)
	if !got.picker[0].Enabled {
		t.Fatal("space would not enable a host whose only fault is being slow")
	}
	if eu := lineContaining(t, got.View(), "eu"); !strings.Contains(eu, "[x]") {
		t.Errorf("the screen does not show the host as kept: %q", eu)
	}
}

// A timed-out row says WHEN it last asked, because that is the only row whose answer
// depends on the moment: the same host answers `tmux 3.2a` twenty seconds later. A
// reason with no timestamp beside it cannot be told from one measured an hour ago.
func TestATimedOutRowSaysWhenItLastAsked(t *testing.T) {
	asked := time.Date(2026, 8, 13, 14, 3, 22, 0, time.Local)
	m := base(t, 120, 24)
	m.mode = modePicker
	m.picker = []PickerRow{{
		Alias: "eu", Reason: "no answer in 10s — slow rather than absent",
		TimedOut: true, Asked: asked,
	}}
	if !strings.Contains(m.View(), "14:03:22") {
		t.Errorf("a timed-out row carries no timestamp, so its age is unknowable:\n%s", m.View())
	}
}

// The remedy has to survive the size §16 names as the one to hold. Measured before the
// row wrapped: at 80 columns the timed-out row read `…this host is slow rather than
// absent; ena` — the remedy gone from the single most important row on the screen (§9:
// 40% of this fleet straddles the timeout), leaving a bare complaint. The approved
// target frame is 120 wide, so the frame diff structurally cannot see this.
func TestTheRemedySurvivesAtEveryWidthTheDesignPromises(t *testing.T) {
	// 80 and 100 are the ones §16 promises and the ones the review measured broken. 30 is
	// not promised — it is here because it is the width where the version gutter no longer
	// leaves room to wrap into, and known-issues L2's rule is that what gets dropped there
	// must be the padding and never the action.
	for _, w := range []int{30, 80, 100, 120} {
		t.Run(fmt.Sprint(w), func(t *testing.T) {
			m := base(t, w, 24)
			m.mode = modePicker
			m.picker = []PickerRow{
				{Alias: "eu", Reason: slowReason, TimedOut: true},
				{Alias: "stale01", Reason: "DNS does not resolve — a stale ssh config entry? fix or remove it"},
				// The arm the width guard was missing. Every earlier fixture's longest
				// token was `--probe-timeout`, 15 columns against an `avail` of 60 — so
				// the guard was correct and could never fire, which looks exactly like
				// coverage. This reason carries a 70-column path, and it is not synthetic:
				// hostset.reasonFor's fall-through arms embed firstLine(stderr) verbatim.
				{Alias: "web-db", Reason: "no tmux version on stdout — " + longToken},
			}
			view := m.View()
			// Asserted against the screen with its line breaks collapsed, because the
			// remedy legitimately WRAPS and a contiguous-substring predicate would fail
			// against a correct product. It still cannot be satisfied by a truncating
			// renderer: truncation removes the bytes, and no amount of joining brings
			// them back.
			flat := strings.Join(strings.Fields(view), " ")
			// The row under the cursor, at every width. Both halves of its reason: the
			// explanation AND the two remedies, which are the part a cut eats.
			for _, want := range []string{"slow rather than absent", "raise --probe-timeout"} {
				if !strings.Contains(flat, want) {
					t.Errorf("at %d columns the cursor's row has lost %q:\n%s", w, want, view)
				}
			}
			// The rest of the fleet only at the widths §16 promises. At 30 columns one
			// wrapped reason fills the overlay, so the second row is legitimately BELOW
			// the fold — the screen says `↓ 1 more · j/k to move` and `j` reaches it,
			// which is the scroll working rather than a remedy lost. Same for the tally,
			// which no 30-column line can hold.
			if w >= 80 {
				for _, want := range []string{"DNS does not resolve", "fix or remove it", "1 timed out"} {
					if !strings.Contains(flat, want) {
						t.Errorf("at %d columns the screen has lost %q:\n%s", w, want, view)
					}
				}
			}
			// And it survived by WRAPPING, not by overflowing: a line wider than the
			// terminal is not a remedy anyone can read either.
			for i, l := range strings.Split(view, "\n") {
				if lines.Width(l) > w {
					t.Errorf("line %d is %d columns wide at width %d: %q", i+1, lines.Width(l), w, l)
				}
			}
		})
	}
}

// A token too long for any column is BROKEN, not overflowed and not dropped. Overflowing
// is the worse of the two failures it replaces: the terminal soft-wraps the line, so a
// View() of exactly 24 lines occupies 25 visual rows and the frame desynchronises — which
// is what joinToHeight and the body's padding exist to prevent. Measured at 80 columns
// before the break: a 61-column token gave an 81-column line, 70 gave 90, 120 gave 140.
func TestAnOverLongTokenIsBrokenRatherThanOverflowingOrVanishing(t *testing.T) {
	for _, w := range []int{80, 100, 120} {
		t.Run(fmt.Sprint(w), func(t *testing.T) {
			m := base(t, w, 24)
			m.mode = modePicker
			m.picker = []PickerRow{{Alias: "web-db", Reason: "no tmux version on stdout — " + longToken}}
			view := m.View()
			for i, l := range strings.Split(view, "\n") {
				if lines.Width(l) > w {
					t.Errorf("line %d is %d columns wide at width %d: %q", i+1, lines.Width(l), w, l)
				}
			}
			// And nothing was lost. Asserted with ALL whitespace removed rather than
			// collapsed, because a hard break puts a newline inside the token: the path
			// itself contains no space, so a screen that still holds every character holds
			// it contiguously here, and a screen that truncated does not.
			bare := strings.Join(strings.Fields(view), "")
			if !strings.Contains(bare, longToken) {
				t.Errorf("the path was cut rather than broken:\n%s", view)
			}
		})
	}
}

// A file with TWO defects reports both. Measured before (review N2): the two complaints
// joined ran to 165 columns and Render cuts the footer at the terminal width, so at 80 the
// reader learned about the duplicate and never about the nameless entry.
func TestAFileWithTwoDefectsReportsBothOfThem(t *testing.T) {
	m := base(t, 80, 24)
	m.mode = modePicker
	m.picker = []PickerRow{{Alias: "eu", Version: "3.2a", Usable: true, Enabled: true, Kept: true}}
	m = m.withKept([]hostset.Entry{
		{Alias: "eu", Enabled: true},
		{Alias: "eu", Enabled: false},
		{Alias: "", Enabled: true},
	})
	view := m.View()
	for _, want := range []string{"twice", "no alias", "enter rewrites it"} {
		if !strings.Contains(view, want) {
			t.Errorf("the complaint has lost %q at 80 columns:\n%s", want, view)
		}
	}
}

// And it fits by CONSTRUCTION, because the footer it lands in belongs to Render, which
// cuts at the terminal width — and Render is not this screen's to change, since the other
// callers of that line are screens whose frames are this branch's calibration targets.
func TestTheFileComplaintAlwaysFitsItsBudget(t *testing.T) {
	long := strings.Repeat("very-long-hostname.", 8) + "example.internal"
	for _, c := range []struct {
		name  string
		kept  []hostset.Entry
		wants []string
	}{
		{"one duplicate is named", []hostset.Entry{{Alias: "eu"}, {Alias: "eu"}}, []string{"eu", "twice"}},
		{"a very long alias", []hostset.Entry{{Alias: long}, {Alias: long}}, []string{"twice"}},
		{"many duplicates are counted", []hostset.Entry{
			{Alias: "a"}, {Alias: "a"}, {Alias: "b"}, {Alias: "b"}, {Alias: "c"}, {Alias: "c"},
		}, []string{"3 hosts twice"}},
		{"both defects at once", []hostset.Entry{
			{Alias: long}, {Alias: long}, {Alias: "b"}, {Alias: "b"}, {Alias: ""}, {Alias: ""}, {Alias: " "},
		}, []string{"2 hosts twice", "3 with no alias"}},
		{"only blanks", []hostset.Entry{{Alias: ""}, {Alias: ""}}, []string{"2 with no alias"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, warn := normaliseKept(c.kept)
			if lines.Width(warn) > KeptComplaintWidth {
				t.Errorf("the complaint is %d columns, over its %d budget, so the footer will cut it: %q",
					lines.Width(warn), KeptComplaintWidth, warn)
			}
			for _, want := range c.wants {
				if !strings.Contains(warn, want) {
					t.Errorf("the complaint does not name %q: %q", want, warn)
				}
			}
		})
	}
	// The control the cases above cannot be: a clean file complains about nothing, or
	// "every defect is named" would be satisfied by a function that always complains.
	if _, warn := normaliseKept([]hostset.Entry{{Alias: "eu"}, {Alias: "nuc"}}); warn != "" {
		t.Errorf("a clean file produced a complaint: %q", warn)
	}
}

// `r` asks every candidate again, and the answer replaces the rows. Without it,
// correcting a coin flip means quitting the hub and starting again.
func TestRAsksEveryCandidateAgain(t *testing.T) {
	m := base(t, 120, 24)
	m.mode = modePicker
	m.picker = []PickerRow{{Alias: "eu", Reason: slowReason, TimedOut: true}}
	m.pickerPorts = PickerPorts{Probe: answers(
		hostset.Result{Alias: "eu", Version: "3.2a", Usable: true})}
	got, _ := press(t, m, runes("r"))
	if eu := lineContaining(t, got.View(), "eu"); !strings.Contains(eu, "tmux 3.2a") {
		t.Errorf("the re-probe's answer never reached the screen: %q", eu)
	}
	if got.pickerBusy {
		t.Error("the picker is still marked busy after the answer came back, so r is dead from now on")
	}
}

// A host the user ENABLED is never un-enabled by a later probe. Membership is their
// decision; a timeout changes a host's STATUS, not whether they want it.
func TestAReProbeNeverUnEnablesAHostTheUserKept(t *testing.T) {
	m := base(t, 120, 24)
	m.mode = modePicker
	m.picker = []PickerRow{{Alias: "eu", Version: "3.2a", Usable: true, Enabled: true}}
	// The same host, the same minute, a different answer. This is measured, not
	// hypothetical: 5.4 s, 9.1 s, 15.7 s, 18.4 s on three consecutive probes.
	m.pickerPorts = PickerPorts{Probe: answers(
		hostset.Result{Alias: "eu", Reason: slowReason, TimedOut: true})}
	got, _ := press(t, m, runes("r"))
	if !got.picker[0].Enabled {
		t.Fatal("a probe that timed out un-enabled a host the user had kept")
	}
	if eu := lineContaining(t, got.View(), "eu"); !strings.Contains(eu, "[x]") {
		t.Errorf("the screen no longer shows the host as kept: %q", eu)
	}
}

// The half the test above CANNOT reach, and the one that was broken: the rule has to
// hold when nothing is on screen yet, i.e. on the first probe of the session, where
// the only record of the user's decision is `hosts.toml`. Measured before the rows
// were built inside the model: a timeout un-enabled a host the user had kept, wrote
// the flip to the file and killed its master — the precise sequence §9 forbids.
func TestTheFileOutranksTheProbeWithNoRowsOnScreenYet(t *testing.T) {
	var saved []hostset.Entry
	var stopped []string
	m := base(t, 120, 24)
	m.mode = modePicker
	m.picker = nil // the first probe of the screen
	m = m.withKept([]hostset.Entry{{Alias: "eu", Enabled: true, Tags: []string{"work"}}})
	m.pickerPorts = PickerPorts{
		Probe: answers(hostset.Result{Alias: "eu", Reason: slowReason, TimedOut: true}),
		Save:  func(e []hostset.Entry) error { saved = e; return nil },
		Stop:  func(a string) error { stopped = append(stopped, a); return nil },
	}
	got, _ := press(t, m, runes("r"))
	if len(got.picker) != 1 || !got.picker[0].Enabled {
		t.Fatalf("the probe alone un-enabled a host hosts.toml kept: %+v", got.picker)
	}
	if eu := lineContaining(t, got.View(), "eu"); !strings.Contains(eu, "[x]") {
		t.Errorf("the screen does not show the host as kept: %q", eu)
	}
	got, _ = press(t, got, tea.KeyMsg{Type: tea.KeyEnter})
	if len(saved) != 1 || !saved[0].Enabled {
		t.Errorf("the flip reached hosts.toml: %+v", saved)
	}
	if len(stopped) != 0 {
		t.Errorf("a master was stopped for a host the user never turned off: %v", stopped)
	}
}

// A probe that cannot run says so. The rows on screen are then the last ones that
// were true, which is why the note has to name the failure — otherwise the screen is
// indistinguishable from one where nothing changed.
func TestAFailedReProbeKeepsTheRowsAndNamesTheFailure(t *testing.T) {
	m := base(t, 120, 24)
	m.mode = modePicker
	m.picker = []PickerRow{{Alias: "eu", Version: "3.2a", Usable: true, Enabled: true}}
	m.pickerPorts = PickerPorts{
		Probe: func() ([]hostset.Candidate, []hostset.Result, error) {
			return nil, nil, fmt.Errorf("ssh: no such file or directory")
		},
	}
	got, _ := press(t, m, runes("r"))
	if len(got.picker) != 1 {
		t.Fatalf("a failed probe emptied the screen: %d rows", len(got.picker))
	}
	if !strings.Contains(got.View(), "no such file or directory") {
		t.Errorf("the failure never reached the screen:\n%s", got.View())
	}
}

// The cursor follows the HOST, not the index. If ~/.ssh/config loses a host above it
// between probes, an index lands the cursor on a different host and `space` toggles
// something the user was not looking at — §7's rule for panes, and the same reason.
func TestTheCursorFollowsTheHostAcrossAReProbe(t *testing.T) {
	m := base(t, 120, 24)
	m.mode = modePicker
	// Four rows down to three, and the one that vanishes is ABOVE the cursor. The
	// lengths matter: with a list that shrinks to exactly the cursor's index, clamping
	// alone lands back on the right host by accident, and an arm built that way stays
	// green against a product that has no idea which host it is pointing at.
	m.picker = []PickerRow{
		{Alias: "aaa", Version: "3.4", Usable: true},
		{Alias: "bbb", Version: "3.4", Usable: true},
		{Alias: "ccc", Version: "3.4", Usable: true},
		{Alias: "ddd", Version: "3.4", Usable: true},
	}
	m.pickerCursor = 2 // on ccc
	m.pickerPorts = PickerPorts{Probe: answers(
		hostset.Result{Alias: "bbb", Version: "3.4", Usable: true},
		hostset.Result{Alias: "ccc", Version: "3.4", Usable: true},
		hostset.Result{Alias: "ddd", Version: "3.4", Usable: true})}
	got, _ := press(t, m, runes("r"))
	if got.picker[got.pickerCursor].Alias != "ccc" {
		t.Fatalf("the cursor moved to %q when a host above it disappeared, want ccc",
			got.picker[got.pickerCursor].Alias)
	}
	// The consequence, which is the reason this matters: space must toggle the host the
	// user was looking at.
	got, _ = press(t, got, runes(" "))
	var on []string
	for _, r := range got.picker {
		if r.Enabled {
			on = append(on, r.Alias)
		}
	}
	if len(on) != 1 || on[0] != "ccc" {
		t.Errorf("space turned on %v, want exactly [ccc] — the host the user was looking at", on)
	}
}

// enter writes the user's decisions AND stops the ssh master of every host they
// turned off. Measured: an `ssh -N -M` is reparented to pid 1 and outlives the hub,
// so a host nobody wants would keep a connection open forever — the user said no,
// and nothing of theirs should keep running.
func TestEnterSavesTheDecisionsAndStopsTheMasterOfWhatWasTurnedOff(t *testing.T) {
	var saved []hostset.Entry
	var stopped []string
	m := base(t, 120, 24)
	m.mode = modePicker
	m.picker = []PickerRow{
		{Alias: "eu", Version: "3.2a", Usable: true, Enabled: true, Kept: true},
		{Alias: "nuc", Version: "3.2a", Usable: true, Enabled: true, Kept: true},
	}
	// What hosts.toml said when the screen opened. `eu` has a master because it was
	// enabled; `nuc` keeps its.
	m = m.withKept([]hostset.Entry{
		{Alias: "eu", Enabled: true, Tags: []string{"work"}},
		{Alias: "nuc", Enabled: true},
	})
	m.pickerPorts = PickerPorts{
		Save: func(e []hostset.Entry) error { saved = e; return nil },
		Stop: func(alias string) error { stopped = append(stopped, alias); return nil },
	}
	m.pickerCursor = 0
	m, _ = press(t, m, runes(" ")) // untick eu
	m, _ = press(t, m, tea.KeyMsg{Type: tea.KeyEnter})

	if m.mode != modeBrowse {
		t.Errorf("enter left the picker open (mode %v)", m.mode)
	}
	if len(stopped) != 1 || stopped[0] != "eu" {
		t.Errorf("stopped %v, want exactly [eu] — the host that was turned off, and no other", stopped)
	}
	// And the screen SAYS it. Ending a connection the user cannot see is exactly the
	// kind of act that has to be reported, or the hub quietly does more than it was
	// asked and the operator learns it from `ps`.
	if !strings.Contains(m.View(), "stopped the ssh master for eu") {
		t.Errorf("the screen never says a master was stopped:\n%s", m.View())
	}
	byAlias := map[string]hostset.Entry{}
	for _, e := range saved {
		byAlias[e.Alias] = e
	}
	if e, ok := byAlias["eu"]; !ok || e.Enabled {
		t.Errorf("hosts.toml records eu as %+v, want an entry that is off", e)
	} else if len(e.Tags) != 1 || e.Tags[0] != "work" {
		// A row carries only what the picker shows. Rebuilding the file from rows
		// alone would silently drop every `tags` the user wrote, which is the kind of
		// loss a generated file makes invisible.
		t.Errorf("saving dropped eu's tags: %+v", e)
	}
	if e := byAlias["nuc"]; !e.Enabled {
		t.Errorf("saving turned off a host the user left on: %+v", e)
	}
}

// THE ORDERING. A save that fails and is then retried must still stop the master.
// Measured against the first version, where the intended state was recorded when
// `enter` was pressed rather than when the write landed: untick `eu`, `enter` →
// `permission denied`, fix the state dir, `enter` again → the file is correct and
// `eu`'s ssh master is still alive, reparented to pid 1, with the screen never
// mentioning it. That defeats the one guarantee the Stop port exists for.
func TestASaveThatFailedThenSucceededStillStopsTheMasterExactlyOnce(t *testing.T) {
	var saved []hostset.Entry
	var stopped []string
	failNext := true
	m := base(t, 120, 24)
	m.mode = modePicker
	m.picker = []PickerRow{
		{Alias: "eu", Version: "3.2a", Usable: true, Enabled: true, Kept: true},
		{Alias: "nuc", Version: "3.2a", Usable: true, Enabled: true, Kept: true},
	}
	m = m.withKept([]hostset.Entry{{Alias: "eu", Enabled: true}, {Alias: "nuc", Enabled: true}})
	m.pickerPorts = PickerPorts{
		Save: func(e []hostset.Entry) error {
			if failNext {
				return fmt.Errorf("permission denied")
			}
			saved = e
			return nil
		},
		Stop: func(alias string) error { stopped = append(stopped, alias); return nil },
	}
	m.pickerCursor = 0
	m, _ = press(t, m, runes(" ")) // untick eu
	m, _ = press(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if !strings.Contains(m.View(), "permission denied") {
		t.Fatalf("the failed save was not reported:\n%s", m.View())
	}
	if len(stopped) != 0 {
		t.Fatalf("a master was stopped although the file was never written: %v", stopped)
	}

	// The user fixes the state dir and presses p, then enter — the real path back.
	failNext = false
	m, _ = press(t, m, runes("p"))
	m, _ = press(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if len(stopped) != 1 || stopped[0] != "eu" {
		t.Errorf("after the retry stopped=%v, want [eu] — the retry leaked the master", stopped)
	}
	if !strings.Contains(m.View(), "stopped the ssh master for eu") {
		t.Errorf("the retry never says the master was stopped:\n%s", m.View())
	}
	if len(saved) != 2 {
		t.Fatalf("the retry wrote %+v", saved)
	}

	// And a third enter stops nothing more: the file now says eu is off, so there is no
	// master left to end and `ssh -O exit` must not run again.
	m, _ = press(t, m, runes("p"))
	m, _ = press(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if len(stopped) != 1 {
		t.Errorf("stopped=%v after a third save; the master was ended twice", stopped)
	}
}

// esc changes nothing. A picker that wrote on the way out would make "look at the
// list" and "commit to it" the same gesture.
func TestEscLeavesTheFileAndTheMastersAlone(t *testing.T) {
	m := base(t, 120, 24)
	m.mode = modePicker
	m.picker = []PickerRow{{Alias: "eu", Version: "3.2a", Usable: true, Enabled: true, Kept: true}}
	m = m.withKept([]hostset.Entry{{Alias: "eu", Enabled: true}})
	m.pickerPorts = PickerPorts{
		Save: func([]hostset.Entry) error {
			t.Error("esc wrote hosts.toml")
			return nil
		},
		Stop: func(string) error {
			t.Error("esc stopped an ssh master")
			return nil
		},
	}
	m.pickerCursor = 0
	m, _ = press(t, m, runes(" ")) // untick it, then change your mind
	m, _ = press(t, m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.mode != modeBrowse {
		t.Errorf("esc left the picker open (mode %v)", m.mode)
	}
}

// A save that fails says so and does not claim the hosts were kept. It also must not
// stop any master: the file still enables that host, so ending its connection would
// leave the hub disagreeing with its own configuration.
func TestAFailedSaveStopsNothingAndSaysSo(t *testing.T) {
	m := base(t, 120, 24)
	m.mode = modePicker
	m.picker = []PickerRow{{Alias: "eu", Version: "3.2a", Usable: true, Kept: true}}
	m = m.withKept([]hostset.Entry{{Alias: "eu", Enabled: true}})
	m.pickerPorts = PickerPorts{
		Save: func([]hostset.Entry) error { return fmt.Errorf("permission denied") },
		Stop: func(string) error {
			t.Error("a master was stopped although the file was never written")
			return nil
		},
		Enable: func([]hostset.Entry) ([]hub.Host, error) {
			t.Error("hosts were connected although the file was never written")
			return nil, nil
		},
	}
	// Driven a step at a time rather than through `press`, because `press` discards the
	// command the second Update returns — which is exactly the one that must not exist
	// here. The two live effects hang off that command, so a test that cannot see it
	// cannot see them either.
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(model)
	if cmd == nil {
		t.Fatal("enter produced no command, so nothing was even attempted")
	}
	next, cmd = m.Update(cmd()) // pickerSavedMsg carrying the failure
	m = next.(model)
	if cmd != nil {
		// Connecting hosts the file does not record leaves the running hub disagreeing
		// with its own configuration — the same reason the save comes before the stops.
		t.Errorf("a failed save produced a follow-up command (%T), so something acted on a "+
			"decision that was never written", cmd())
	}
	if !strings.Contains(m.View(), "permission denied") {
		t.Errorf("the save failure never reached the screen:\n%s", m.View())
	}
}

// A host the user KEPT can always be turned off, whatever the probe now answers.
// Measured before `Kept` existed: `hermes-ws` enabled in hosts.toml with the probe
// answering `no tmux` had no box at all, so no key could turn it off — and `space`
// answered "cannot be enabled", which is the opposite of what the user was doing. The
// only remedy left was hand-editing the file, on the screen §9 calls the place a
// person decides.
func TestAHostTheUserKeptIsAlwaysUntickableWhateverTheProbeSaysNow(t *testing.T) {
	var saved []hostset.Entry
	m := base(t, 120, 24)
	m.mode = modePicker
	m.picker = []PickerRow{
		{Alias: "hermes-ws", Reason: "no tmux — install it there, or leave this host off",
			Enabled: true, Kept: true},
		// The discriminator: same answer, never the user's. This one still has no box,
		// so "an enabled host keeps its box" cannot be satisfied by giving every row one.
		{Alias: "github.com", Reason: "not a shell host — this is a git remote, so leave it off"},
	}
	m = m.withKept([]hostset.Entry{{Alias: "hermes-ws", Enabled: true}})
	m.pickerPorts = PickerPorts{Save: func(e []hostset.Entry) error { saved = e; return nil }}

	hermes := lineContaining(t, m.View(), "hermes-ws")
	if !strings.Contains(hermes, "[!]") {
		t.Errorf("a kept host whose probe now disagrees shows no box, so nothing can turn it off: %q", hermes)
	}
	if gh := lineContaining(t, m.View(), "github.com"); strings.Contains(gh, "[") {
		t.Errorf("a host that was never kept was given a box: %q", gh)
	}

	m.pickerCursor = 0
	m, _ = press(t, m, runes(" "))
	if m.picker[0].Enabled {
		t.Fatal("space would not turn off a host the user had kept")
	}
	if strings.Contains(m.note, "cannot be enabled") {
		t.Errorf("the key that turned the host off said %q", m.note)
	}
	// And now the OTHER half of the asymmetry: turning it off is always allowed, turning
	// it back on is not, because only a live probe answer grants that. Off, the row reads
	// like github.com — off and not enable-able, which is what it now is — and `space`
	// says so in a sentence that is finally true. Undo is esc, which is on screen.
	if h := lineContaining(t, m.View(), "hermes-ws"); strings.Contains(h, "[") {
		t.Errorf("a host that is off and cannot be enabled still offers a box: %q", h)
	}
	m, _ = press(t, m, runes(" "))
	if m.picker[0].Enabled {
		t.Error("space re-enabled a host the probe says has no tmux — the picker invented that state")
	}
	if !strings.Contains(m.note, "cannot be enabled") {
		t.Errorf("the refusal said %q, and here it is the true sentence", m.note)
	}
	// The decision still reaches the file, although the box has gone: `writable` is
	// deliberately wider than either key predicate, or turning a host off would be a
	// gesture hosts.toml never hears.
	m, _ = press(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if len(saved) != 1 || saved[0].Alias != "hermes-ws" || saved[0].Enabled {
		t.Errorf("the decision never reached hosts.toml: %+v", saved)
	}
}

// hosts.toml's reader keeps both of a duplicated alias deliberately, because
// validation belongs where a person can be told — and this screen is that place (§9).
// Measured before it told them: a file naming `eu` twice made the picker write BOTH
// entries, one `true` and one `false`, run `ssh -O exit` twice for one host, and report
// "1 host kept" when the user's decision was to keep none.
func TestADuplicateAliasIsCollapsedAndSaidOnTheScreen(t *testing.T) {
	var saved []hostset.Entry
	var stopped []string
	m := base(t, 120, 24)
	m.mode = modePicker
	m.picker = []PickerRow{{Alias: "eu", Version: "3.2a", Usable: true, Enabled: true, Kept: true}}
	m = m.withKept([]hostset.Entry{
		{Alias: "eu", Enabled: true, Tags: []string{"work"}},
		{Alias: "eu", Enabled: false},
	})
	m.pickerPorts = PickerPorts{
		Save: func(e []hostset.Entry) error { saved = e; return nil },
		Stop: func(a string) error { stopped = append(stopped, a); return nil },
	}
	if !strings.Contains(m.View(), "hosts.toml names eu twice") {
		t.Errorf("the screen says nothing about a file that names one host twice:\n%s", m.View())
	}

	m.pickerCursor = 0
	m, _ = press(t, m, runes(" ")) // untick eu
	m, _ = press(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if len(saved) != 1 {
		t.Fatalf("the picker wrote %d entries for one host: %+v", len(saved), saved)
	}
	if saved[0].Enabled {
		t.Errorf("the surviving entry disagrees with the user: %+v", saved[0])
	}
	if len(saved[0].Tags) != 1 || saved[0].Tags[0] != "work" {
		t.Errorf("collapsing the duplicate dropped the first entry's tags: %+v", saved[0])
	}
	if len(stopped) != 1 {
		t.Errorf("stopped %v — ssh -O exit ran once per duplicate rather than once per host", stopped)
	}
	if !strings.Contains(m.View(), "0 hosts kept") {
		t.Errorf("the count contradicts the user, who kept none:\n%s", m.View())
	}
	// The file has just been rewritten from the collapsed view, so the complaint is
	// repaired rather than permanent.
	if strings.Contains(m.View(), "names eu twice") {
		t.Errorf("the complaint survived the save that fixed the file:\n%s", m.View())
	}
}

// Same root, other half: the reader accepts `alias = ""`. Measured before this, such an
// entry was invisible on the picker (no candidate matches it, so it is never a row),
// survived every save, and was counted as a kept host — so a person read "2 hosts kept"
// on a machine with one and had no way to find the second.
func TestAnEntryWithNoAliasIsDroppedAndSaidOnTheScreen(t *testing.T) {
	var saved []hostset.Entry
	m := base(t, 120, 24)
	m.mode = modePicker
	m.picker = []PickerRow{{Alias: "eu", Version: "3.2a", Usable: true, Enabled: true, Kept: true}}
	m = m.withKept([]hostset.Entry{{Alias: "", Enabled: true}, {Alias: "eu", Enabled: true}})
	m.pickerPorts = PickerPorts{Save: func(e []hostset.Entry) error { saved = e; return nil }}
	if !strings.Contains(m.View(), "hosts.toml has 1 with no alias") {
		t.Errorf("the screen says nothing about an entry naming no host:\n%s", m.View())
	}
	m, _ = press(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	for _, e := range saved {
		if e.Alias == "" {
			t.Errorf("the nameless entry survived the save: %+v", saved)
		}
	}
	if !strings.Contains(m.View(), "1 host kept") {
		t.Errorf("the count still includes the entry nobody can find:\n%s", m.View())
	}
}

// The cursor opens on a row a key can act on. Verified live at 110×40 against this
// machine's real ~/.ssh/config: the first candidate is `orbits.github.com`, a git remote,
// so the picker opened with the cursor on a row that refuses every key — and the first
// thing a new user does on the first screen they ever see is press space and be told
// "cannot be enabled".
func TestTheCursorOpensOnARowAKeyCanActOn(t *testing.T) {
	m := base(t, 120, 24)
	m.picker = []PickerRow{
		{Alias: "orbits.github.com", Reason: "not a shell host — this is a git remote, so leave it off"},
		{Alias: "hermes-ws", Reason: "no tmux — install it there, or leave this host off"},
		{Alias: "web-db", Version: "3.2a", Usable: true},
	}
	got, _ := press(t, m, runes("p"))
	view := got.View()
	if row := lineContaining(t, view, "web-db"); !strings.HasPrefix(row, "›") {
		t.Errorf("the cursor did not open on the first row a key can act on: %q", row)
	}
	// The discriminator: not merely "somewhere", but off the excluded rows — otherwise a
	// product that never moved the cursor would satisfy the assertion above whenever the
	// first row happened to be usable.
	for _, excluded := range []string{"orbits.github.com", "hermes-ws"} {
		if row := lineContaining(t, view, excluded); strings.HasPrefix(row, "›") {
			t.Errorf("the cursor opened on a row that refuses every key: %q", row)
		}
	}
	// And the consequence, which is the whole point: the first keystroke does something.
	got, _ = press(t, got, runes(" "))
	if strings.Contains(got.note, "cannot be enabled") {
		t.Errorf("the first space of a first run was still refused: %q", got.note)
	}
	if !got.picker[2].Enabled {
		t.Errorf("space did not keep the host under the cursor: %+v", got.picker)
	}
}

// When every candidate is excluded there is nowhere better, so the cursor stays put
// rather than running off the end looking. That is a specified empty state (§9), not a
// failure, and the rows still carry their reasons.
func TestTheCursorStaysAtTheTopWhenEveryCandidateIsExcluded(t *testing.T) {
	m := base(t, 120, 24)
	m.picker = []PickerRow{
		{Alias: "orbits.github.com", Reason: "not a shell host — this is a git remote, so leave it off"},
		{Alias: "hermes-ws", Reason: "no tmux — install it there, or leave this host off"},
	}
	got, _ := press(t, m, runes("p"))
	if got.pickerCursor != 0 {
		t.Errorf("cursor = %d with nothing to act on, want 0", got.pickerCursor)
	}
	if row := lineContaining(t, got.View(), "orbits.github.com"); !strings.HasPrefix(row, "›") {
		t.Errorf("the cursor is not on the first row: %q", row)
	}
}

// enterAndConnect presses enter and runs BOTH commands it produces — the save, then the
// connect. `press` runs one level only, and the whole defect below was that the second
// level did not exist, so a test that stops after the first cannot see this fix.
func enterAndConnect(t *testing.T, m model) model {
	t.Helper()
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(model)
	if cmd == nil {
		t.Fatal("enter produced no command, so nothing was saved")
	}
	next, cmd = m.Update(cmd()) // pickerSavedMsg
	m = next.(model)
	if cmd == nil {
		t.Fatal("a successful save produced no follow-up command, so nothing connects")
	}
	next, _ = m.Update(cmd()) // pickerEnabledMsg
	return next.(model)
}

// `enter: save and connect` does BOTH. The key line is the only instruction on that
// screen and it promised two halves while delivering one: measured end to end in a pty,
// three candidates ticked, hosts.toml correct, and 17.5 s later the transport had been
// asked for five probes and nothing else — zero `ssh -O check`, zero master spawns, zero
// polls, until the hub was restarted.
func TestASavedHostJoinsTheFleetWithoutARestart(t *testing.T) {
	m := base(t, 120, 24)
	m.mode = modePicker
	m.picker = []PickerRow{{Alias: "eu", Version: "3.2a", Usable: true, Enabled: true}}
	m.pickerPorts = PickerPorts{
		Save: func([]hostset.Entry) error { return nil },
		Enable: func(kept []hostset.Entry) ([]hub.Host, error) {
			// main returns every host the file enables, beside the ones already polled —
			// deciding which are new is the model's job.
			return []hub.Host{
				{Label: "nuc", SSHDest: "nuc"},
				{Label: "eu", SSHDest: "eu", ControlPath: "/run/user/1000/cm-kg"},
			}, nil
		},
	}
	got := enterAndConnect(t, m)

	// The note names it, because the operator pressed a key and is owed an answer.
	if !strings.Contains(got.View(), "connecting to eu") {
		t.Errorf("the screen never says the host is being connected:\n%s", got.View())
	}
	// And the host line carries the whole fleet: the two that were already polled plus
	// the new one, as `connecting`. Read with the note cleared, because a note outranks
	// the host line by design and this assertion is about the line.
	got.note = ""
	line := lineContaining(t, got.View(), "connecting")
	for _, want := range []string{"local up", "nuc up", "eu connecting"} {
		if !strings.Contains(line, want) {
			t.Errorf("the host line is missing %q: %q", want, line)
		}
	}
}

// The negative half, and it is what stops a fix that folds hosts in unconditionally: a
// save that enables nothing new adds no rows. Such a fix passes the assertion above and
// doubles a host on every save, and the frame looks right the first time.
func TestASaveThatEnablesNothingNewAddsNoRows(t *testing.T) {
	m := base(t, 120, 24)
	m.mode = modePicker
	m.picker = []PickerRow{{Alias: "nuc", Version: "3.2a", Usable: true, Enabled: true, Kept: true}}
	m = m.withKept([]hostset.Entry{{Alias: "nuc", Enabled: true}})
	m.pickerPorts = PickerPorts{
		Save: func([]hostset.Entry) error { return nil },
		Enable: func([]hostset.Entry) ([]hub.Host, error) {
			// `nuc` is already in the fleet, and main's contract is to return every
			// enabled host rather than only the new ones — a port that pre-filtered would
			// make this test vacuous.
			return []hub.Host{{Label: "nuc", SSHDest: "nuc"}}, nil
		},
	}
	before := len(m.hosts)
	got := enterAndConnect(t, m)

	if len(got.hosts) != before {
		t.Errorf("the fleet went %d -> %d rows: a host already polled was added again",
			before, len(got.hosts))
	}
	// Counted per label as well, because a length check alone would pass on a list that
	// gained a second `nuc` and lost something else.
	var n int
	for _, h := range got.hosts {
		if h.Label == "nuc" {
			n++
		}
	}
	if n != 1 {
		t.Errorf("nuc appears %d times in the fleet, want 1 — hostFor answers with the first "+
			"match, so a write aimed at one copy lands on the other", n)
	}
	if strings.Contains(got.note, "connecting to") {
		t.Errorf("the screen claims to be connecting something new: %q", got.note)
	}
}

// The half m.hosts alone cannot prove: the host reaches the POLLER. Driven through a
// model built the way Run builds it, with a fake tmux underneath — then one real tick.
// A host missing from the poller does not merely go unpolled, it VANISHES at the next
// tick, because tickMsg replaces m.hosts wholesale with the poller's own list.
func TestASavedHostIsRegisteredWithThePollerAndSurvivesATick(t *testing.T) {
	f := newFakeTmux("%0")
	m := builtModel(t, f, nil, livePane("%0", "work"))
	m.mode = modePicker
	m.picker = []PickerRow{{Alias: "eu", Version: "3.2a", Usable: true, Enabled: true}}
	m.pickerPorts = PickerPorts{
		Save: func([]hostset.Entry) error { return nil },
		Enable: func([]hostset.Entry) ([]hub.Host, error) {
			return []hub.Host{{Label: "eu", Socket: "/tmp/tmux-hub-test-kg.sock"}}, nil
		},
	}
	got := enterAndConnect(t, m)
	if _, ok := hostFor(got.hosts, "eu"); !ok {
		t.Fatalf("eu never reached the fleet: %+v", got.hosts)
	}

	next, _ := got.Update(got.poll()())
	after := next.(model)
	if _, ok := hostFor(after.hosts, "eu"); !ok {
		t.Errorf("eu is gone after one tick, so it was never added to the poller — the row "+
			"would appear once and disappear: %+v", after.hosts)
	}
}

// A host the user turns off LEAVES the fleet, and stays gone across a tick. Stopping its
// master ended the connection and left the row: one tick — which replaces m.hosts wholesale
// from the poller — put it back reading `connecting (waiting for its ssh master)`, and once
// it had ever answered, `down` carrying a respawn command the operator must not run.
// Nothing would ever spawn that master again, so the status could never resolve.
//
// Driven through a model built the way Run builds it, because the poller is the half that
// makes it permanent and `m.hosts` alone cannot show that.
func TestAHostTurnedOffLeavesTheFleetAndStaysGone(t *testing.T) {
	f := newFakeTmux("%0")
	m := builtModel(t, f, nil, livePane("%0", "work"))
	m.poller.Add(hub.Host{Label: "eu", Socket: "/tmp/tmux-hub-test-kg.sock"})
	m.hosts = append(m.hosts, hub.Host{Label: "eu", Socket: "/tmp/tmux-hub-test-kg.sock"})
	m.mode = modePicker
	m.picker = []PickerRow{{Alias: "eu", Version: "3.2a", Usable: true, Enabled: true, Kept: true}}
	m = m.withKept([]hostset.Entry{{Alias: "eu", Enabled: true}})
	var stopped []string
	m.pickerPorts = PickerPorts{
		Save: func([]hostset.Entry) error { return nil },
		Stop: func(alias string) error { stopped = append(stopped, alias); return nil },
		Enable: func([]hostset.Entry) ([]hub.Host, error) {
			return nil, nil // nothing is enabled any more
		},
	}
	m.pickerCursor = 0
	m, _ = press(t, m, runes(" ")) // untick eu
	got := enterAndConnect(t, m)

	if len(stopped) != 1 || stopped[0] != "eu" {
		t.Fatalf("stopped %v, want [eu]", stopped)
	}
	if _, ok := hostFor(got.hosts, "eu"); ok {
		t.Errorf("eu is still in the fleet after being turned off: %+v", got.hosts)
	}
	// The half that makes it permanent: one real tick must not bring it back.
	next, _ := got.Update(got.poll()())
	after := next.(model)
	if _, ok := hostFor(after.hosts, "eu"); ok {
		t.Errorf("eu came back after one tick, so it was never removed from the poller — its "+
			"row would carry a status no poll can resolve: %+v", after.hosts)
	}
	// And the host that was never touched is still there, or "removed" would be satisfied
	// by dropping the whole fleet.
	if _, ok := hostFor(after.hosts, hub.LocalLabel); !ok {
		t.Errorf("the local host left too: %+v", after.hosts)
	}
}

// ONE save that both stops a master and connects a host, which no test did: the Stop tests
// left `Enable` nil and the Enable tests left `Stop` nil, so each half passed in a world
// where the other did not exist. With both wired, the connect's message landed on top of
// the save's — measured, `1 host kept in hosts.toml · stopped the ssh master for eu` became
// `connecting to jj` — so on any mixed save the operator was never told a connection had
// been ended.
func TestAMixedSaveReportsBothTheStopAndTheConnect(t *testing.T) {
	m := base(t, 120, 24)
	m.mode = modePicker
	m.picker = []PickerRow{
		{Alias: "nuc", Version: "3.2a", Usable: true, Enabled: true, Kept: true}, // turn off
		{Alias: "jj", Version: "3.4", Usable: true},                              // turn on
	}
	m = m.withKept([]hostset.Entry{{Alias: "nuc", Enabled: true}})
	var stopped []string
	m.pickerPorts = PickerPorts{
		Save: func([]hostset.Entry) error { return nil },
		Stop: func(alias string) error { stopped = append(stopped, alias); return nil },
		Enable: func([]hostset.Entry) ([]hub.Host, error) {
			return []hub.Host{{Label: "jj", SSHDest: "jj"}}, nil
		},
	}
	m.pickerCursor = 0
	m, _ = press(t, m, runes(" ")) // nuc off
	m.pickerCursor = 1
	m, _ = press(t, m, runes(" ")) // jj on
	got := enterAndConnect(t, m)

	if len(stopped) != 1 || stopped[0] != "nuc" {
		t.Fatalf("stopped %v, want [nuc]", stopped)
	}
	view := got.View()
	// Both halves of one keystroke, on one line.
	for _, want := range []string{
		"kept in hosts.toml",             // the save's confirmation
		"stopped the ssh master for nuc", // the act the user cannot otherwise see
		"connecting to jj",               // the connect
	} {
		if !strings.Contains(view, want) {
			t.Errorf("the note has lost %q:\n%s", want, view)
		}
	}
}

// The same collision on the failing path: a connect that fails must not erase the report of
// a master that was stopped.
func TestAFailedConnectDoesNotEraseTheStopReport(t *testing.T) {
	m := base(t, 120, 24)
	m.mode = modePicker
	m.picker = []PickerRow{{Alias: "nuc", Version: "3.2a", Usable: true, Enabled: true, Kept: true}}
	m = m.withKept([]hostset.Entry{{Alias: "nuc", Enabled: true}})
	m.pickerPorts = PickerPorts{
		Save: func([]hostset.Entry) error { return nil },
		Stop: func(string) error { return nil },
		Enable: func([]hostset.Entry) ([]hub.Host, error) {
			return nil, fmt.Errorf("XDG_RUNTIME_DIR is unset")
		},
	}
	m.pickerCursor = 0
	m, _ = press(t, m, runes(" "))
	got := enterAndConnect(t, m)
	view := got.View()
	for _, want := range []string{"stopped the ssh master for nuc", "could not connect", "XDG_RUNTIME_DIR is unset"} {
		if !strings.Contains(view, want) {
			t.Errorf("the note has lost %q:\n%s", want, view)
		}
	}
}

// A host whose master would not stop still leaves the fleet. Leaving the row behind is the
// worse of the two failures: the connection is gone either way, and a row nothing will ever
// poll again carries a status that cannot resolve.
func TestAHostTurnedOffLeavesEvenIfItsMasterWillNotStop(t *testing.T) {
	m := base(t, 120, 24)
	m.mode = modePicker
	m.picker = []PickerRow{{Alias: "nuc", Version: "3.2a", Usable: true, Enabled: true, Kept: true}}
	m = m.withKept([]hostset.Entry{{Alias: "nuc", Enabled: true}})
	m.pickerPorts = PickerPorts{
		Save: func([]hostset.Entry) error { return nil },
		Stop: func(string) error { return fmt.Errorf("ssh: connection refused") },
		Enable: func([]hostset.Entry) ([]hub.Host, error) {
			return nil, nil
		},
	}
	m.pickerCursor = 0
	m, _ = press(t, m, runes(" "))
	got := enterAndConnect(t, m)

	if _, ok := hostFor(got.hosts, "nuc"); ok {
		t.Errorf("nuc stayed in the fleet because its master would not stop: %+v", got.hosts)
	}
	if !strings.Contains(got.View(), "could not stop the ssh master for nuc") {
		t.Errorf("the failed stop was never reported:\n%s", got.View())
	}
}

// A connect that fails must not hide that the file WAS written, or the user goes back to
// re-tick a list already saved.
func TestAFailedConnectStillSaysTheFileWasSaved(t *testing.T) {
	m := base(t, 120, 24)
	m.mode = modePicker
	m.picker = []PickerRow{{Alias: "eu", Version: "3.2a", Usable: true, Enabled: true}}
	m.pickerPorts = PickerPorts{
		Save: func([]hostset.Entry) error { return nil },
		Enable: func([]hostset.Entry) ([]hub.Host, error) {
			return nil, fmt.Errorf("XDG_RUNTIME_DIR is unset")
		},
	}
	got := enterAndConnect(t, m)
	view := got.View()
	// Asserted on the save's own confirmation rather than on the word "saved". The note is
	// now built by appending, so the write's report is still on the line and says more than
	// an adjective did — and pinning a test to one word is how it came to fail against a
	// message that had improved.
	if !strings.Contains(view, "kept in hosts.toml") {
		t.Errorf("the failure hides that the file was written:\n%s", view)
	}
	if !strings.Contains(view, "could not connect") {
		t.Errorf("the failure is not named:\n%s", view)
	}
	if !strings.Contains(view, "XDG_RUNTIME_DIR is unset") {
		t.Errorf("the reason never reached the screen:\n%s", view)
	}
}

// `p` opens the picker from the dashboard, and the dashboard is still above the rule:
// the picker is an overlay, the same shape as the launch form, not a takeover.
func TestPOpensThePickerAsAnOverlayOverTheDashboard(t *testing.T) {
	m := base(t, 120, 24, agentPane("local", "api", "review", "%0", 0, state.Needs, "  Do you want to proceed?"))
	m.picker = []PickerRow{{Alias: "eu", Version: "3.2a", Usable: true, Enabled: true}}
	got, _ := press(t, m, runes("p"))
	if got.mode != modePicker {
		t.Fatalf("p left mode %v", got.mode)
	}
	view := got.View()
	for _, want := range []string{
		"tmux-hub  1 session", // the dashboard header
		"local up · nuc up",   // and its host line, below the panes
		"space: keep this host",
	} {
		if !strings.Contains(view, want) {
			t.Errorf("the picker is a takeover, not an overlay — %q is gone:\n%s", want, view)
		}
	}
}

// Every key the screen accepts is named on the screen, in English. The launch form's
// strings are Russian and that is a parked defect; nothing new inherits it.
func TestThePickerNamesItsKeysOnTheScreenInEnglish(t *testing.T) {
	m := base(t, 120, 24)
	m.mode = modePicker
	m.picker = []PickerRow{{Alias: "eu", Version: "3.2a", Usable: true}}
	keys := lineContaining(t, m.View(), "space:")
	for _, want := range []string{
		"space: keep this host", "enter: save and connect", "esc: cancel", "r: probe again",
	} {
		if !strings.Contains(keys, want) {
			t.Errorf("the key line does not name %q: %q", want, keys)
		}
	}
	for _, r := range keys {
		if r >= 'а' && r <= 'я' || r >= 'А' && r <= 'Я' {
			t.Fatalf("the key line is not in English: %q", keys)
		}
	}
}

// And it is on the LAST row whatever the fleet's size. Measured before the body was
// padded: one candidate put the key line on row 18 of 24 with six blank rows under it,
// four put it on 21. The approved frame has twenty candidates, so the diff cannot see
// a footer floating in the middle of a screen.
func TestTheKeyLineIsOnTheLastRowWhateverTheCandidateCount(t *testing.T) {
	for _, n := range []int{1, 4, 6, 7, 20} {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			rows := make([]PickerRow, n)
			for i := range rows {
				rows[i] = PickerRow{Alias: fmt.Sprintf("host%02d", i), Version: "3.4", Usable: true}
			}
			m := base(t, 120, 24)
			m.mode = modePicker
			m.picker = rows
			got := strings.Split(m.View(), "\n")
			if last := got[len(got)-1]; !strings.HasPrefix(last, "space: keep this host") {
				t.Errorf("with %d candidates the last row is %q, not the key line", n, last)
			}
		})
	}
}

// The count a person reads is the count worth PROBING. `ParseSSHConfig` answers 30
// candidates on this machine and ten of them are systemd's ssh-proxy patterns
// carrying a Skip: `30 candidates` would send someone looking for ten hosts that do
// not exist, and offering them a tick box would send a probe at a pattern.
func TestOnlyCandidatesWorthProbingBecomeRows(t *testing.T) {
	cands := []hostset.Candidate{
		{Alias: "nuc"},
		{Alias: "machine/.host", Skip: "a systemd ssh-proxy pattern, not a host reachable by name"},
		{Alias: "*", Skip: "a pattern, not a host"},
		{Alias: "github.com"},
	}
	results := []hostset.Result{
		{Alias: "nuc", Version: "3.2a", Usable: true},
		{Alias: "github.com", Reason: "not a shell host — this is a git remote, so leave it off"},
	}
	rows := PickerRowsFor(cands, results, nil, nil, time.Time{})
	if len(rows) != 2 {
		t.Fatalf("PickerRowsFor made %d rows from 4 candidates, want the 2 worth probing: %+v", len(rows), rows)
	}

	m := base(t, 120, 24)
	m.mode = modePicker
	m.picker = rows
	view := m.View()
	if !strings.Contains(view, "2 candidates in ~/.ssh/config") {
		t.Errorf("the count is not the count worth probing:\n%s", view)
	}
	if strings.Contains(view, "machine/.host") {
		t.Errorf("a systemd pattern is offered as a host to decide about:\n%s", view)
	}
}

// A label the fleet has already spoken for cannot be ticked, whatever the probe answered.
// `hostsFor` refuses both collisions FATALLY and main exits 1 on them — at a startup that
// happens before the TUI, so a machine with `Host local` in its ssh config could tick it,
// save, and then find the hub refusing to start with no way back but hand-editing TOML: a
// state the UI created and the UI could not undo.
func TestALabelTheFleetHasTakenCannotBeTicked(t *testing.T) {
	cands := []hostset.Candidate{{Alias: hub.LocalLabel}, {Alias: "prod"}, {Alias: "web-db"}}
	// The probe is deliberately HAPPY about all three. A reservation that only worked on a
	// host which had already failed would be no protection at all.
	results := []hostset.Result{
		{Alias: hub.LocalLabel, Version: "3.7b", Usable: true},
		{Alias: "prod", Version: "3.4", Usable: true},
		{Alias: "web-db", Version: "3.2a", Usable: true},
	}
	rows := PickerRowsFor(cands, results, nil, []string{hub.LocalLabel, "prod"}, time.Time{})

	m := base(t, 120, 24)
	m.mode = modePicker
	m.picker = rows
	view := m.View()

	// Located by REASON rather than by alias: `local` also appears in the dashboard's host
	// line above the rule, and an assertion on a line found by that needle would be about
	// the wrong line — which is exactly what lineContaining refuses to let happen.
	for _, want := range []string{
		"taken by this machine's own server", "already given to a --host entry",
	} {
		row := lineContaining(t, view, want)
		if strings.Contains(row, "[") {
			t.Errorf("a reserved label offers a box, so it can be ticked into a file that "+
				"stops startup: %q", row)
		}
	}
	// The discriminator: a candidate that collides with nothing is still tickable, or
	// "reserved labels are refused" would be satisfied by refusing everything.
	if row := lineContaining(t, view, "web-db"); !strings.Contains(row, "[x]") {
		t.Errorf("an uncontested host lost its box too: %q", row)
	}

	// And space cannot force it, since the reservation clears every field canEnable reads.
	m.pickerCursor = 0
	after, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{' '}})
	if after.(model).picker[0].Enabled {
		t.Error("space enabled a label the fleet has already given away")
	}
}

// And the model actually passes the reserved labels through. PickerRowsFor cannot enforce a
// rule it is not told about, so the wiring is its own assertion: driven from a real probe
// round rather than by calling the function, because the wiring is what the probe path uses.
func TestWithPickerRefusesALabelTheFleetHasTaken(t *testing.T) {
	m := base(t, 120, 24)
	m.mode = modePicker
	m.pickerReserved = []string{hub.LocalLabel}
	m.pickerPorts = PickerPorts{Probe: answers(
		hostset.Result{Alias: hub.LocalLabel, Version: "3.7b", Usable: true},
		hostset.Result{Alias: "web-db", Version: "3.2a", Usable: true})}
	got, _ := press(t, m, runes("r"))

	row, ok := pickerRowFor(got.picker, hub.LocalLabel)
	if !ok {
		t.Fatalf("the reserved candidate produced no row at all: %+v", got.picker)
	}
	if row.Usable || row.Enabled {
		t.Errorf("the model did not pass the reserved labels to PickerRowsFor: %+v", row)
	}
	if !strings.Contains(row.Reason, "taken by this machine's own server") {
		t.Errorf("the row carries no remedy: %+v", row)
	}
	// The discriminator: the other candidate came through the same call and is fine.
	if other, _ := pickerRowFor(got.picker, "web-db"); !other.Usable || !other.Enabled {
		t.Errorf("an uncontested candidate was refused too: %+v", other)
	}
}

func pickerRowFor(rows []PickerRow, alias string) (PickerRow, bool) {
	for _, r := range rows {
		if r.Alias == alias {
			return r, true
		}
	}
	return PickerRow{}, false
}

// And it never reaches hosts.toml, which is the property that actually protects startup.
func TestAReservedLabelIsNeverWrittenToTheFile(t *testing.T) {
	var saved []hostset.Entry
	rows := PickerRowsFor(
		[]hostset.Candidate{{Alias: hub.LocalLabel}, {Alias: "web-db"}},
		[]hostset.Result{
			{Alias: hub.LocalLabel, Version: "3.7b", Usable: true},
			{Alias: "web-db", Version: "3.2a", Usable: true},
		},
		nil, []string{hub.LocalLabel}, time.Time{})

	m := base(t, 120, 24)
	m.mode = modePicker
	m.picker = rows
	m.pickerPorts = PickerPorts{
		Save:   func(e []hostset.Entry) error { saved = e; return nil },
		Enable: func([]hostset.Entry) ([]hub.Host, error) { return nil, nil },
	}
	_ = enterAndConnect(t, m)

	for _, e := range saved {
		if e.Alias == hub.LocalLabel {
			t.Errorf("the reserved label reached hosts.toml as %+v — the next startup exits 1", e)
		}
	}
	if len(saved) != 1 || saved[0].Alias != "web-db" || !saved[0].Enabled {
		t.Errorf("the uncontested host did not survive the save: %+v", saved)
	}
}

// A host answering with a version is kept by DEFAULT — zero configuration is a
// working configuration — but hosts.toml OUTRANKS the probe, because that file is
// the user's decision and this screen is where they made it.
func TestHostsTomlOutranksTheProbesDefault(t *testing.T) {
	cands := []hostset.Candidate{{Alias: "nuc"}, {Alias: "eu"}, {Alias: "web-db"}}
	results := []hostset.Result{
		{Alias: "nuc", Version: "3.2a", Usable: true},
		{Alias: "eu", Version: "3.2a", Usable: true},
		{Alias: "web-db", Reason: slowReason, TimedOut: true},
	}
	kept := []hostset.Entry{{Alias: "eu", Enabled: false}, {Alias: "web-db", Enabled: true}}
	rows := PickerRowsFor(cands, results, kept, nil, time.Time{})

	want := map[string]bool{"nuc": true, "eu": false, "web-db": true}
	for _, r := range rows {
		if r.Enabled != want[r.Alias] {
			t.Errorf("%s enabled=%v, want %v (nuc by the probe, eu and web-db by the file)",
				r.Alias, r.Enabled, want[r.Alias])
		}
	}
}

// A probe answer still in flight when enter returned to the dashboard must not replace
// the save's note. The part §9 says has to be seen is which ssh master was stopped.
func TestAProbeStillInFlightDoesNotEraseTheSaveNote(t *testing.T) {
	const note = "1 host kept in hosts.toml · stopped the ssh master for eu"
	m := base(t, 120, 24)
	m.mode = modeBrowse
	m.note = note
	next, _ := m.Update(pickerProbedMsg{
		cands:   []hostset.Candidate{{Alias: "eu"}},
		results: []hostset.Result{{Alias: "eu", Version: "3.2a", Usable: true}},
	})
	got := next.(model)
	if got.note != note {
		t.Errorf("the probe replaced the save's note with %q", got.note)
	}
	if len(got.picker) != 1 {
		t.Errorf("the rows were not updated: %+v", got.picker)
	}
}

// The frame. Every line below the rule is one the approved target carries, and the
// target was seeded from the real launch-form overlay rather than composed — a screen
// written from prose scores 0.22–0.45 against what the renderer then produces, one
// seeded from a real frame scores 0.96–1.00 (docs/mockup-authoring.md).
//
// ONE line diverges from the target as originally approved, deliberately: the key line
// names `r: probe again`. §9 requires the re-probe ("one key, and a timestamp beside
// the reason") and that line's own assertion is "the keys are on screen" — a key that
// is not named is not on screen. The plan's target was amended to match in 42521ba.
func TestThePickerBodyMatchesTheApprovedTargetFrame(t *testing.T) {
	want := []string{
		"────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────",
		"Hosts — 20 candidates in ~/.ssh/config, 5 answer with tmux",
		"",
		"  [x] nuc           tmux 3.2a",
		"  [x] web-app       tmux 3.2a",
		"› [ ] eu            tmux 3.2a",
		"  [x] side-desk     tmux 3.4",
		"      github.com    not a shell host — this is a git remote, so leave it off",
		"      hermes-ws     no tmux — install it there, or leave this host off",
		"  ↓ 14 more · j/k to move",
		"",
		"space: keep this host · enter: save and connect · esc: cancel · r: probe again",
	}
	m := base(t, 120, 24)
	m.mode = modePicker
	m.picker = targetFrameRows()
	m.pickerCursor = 2

	got := strings.Split(m.View(), "\n")
	if len(got) != 24 {
		t.Fatalf("View() is %d lines at 120x24, want 24:\n%s", len(got), m.View())
	}
	body := got[len(got)-len(want):]
	for i := range want {
		if body[i] != want[i] {
			t.Errorf("body line %d:\n got %q\nwant %q", i+1, body[i], want[i])
		}
	}
}

// The rule the frame test cannot state on its own: the picker takes the SAME twelve
// rows the launch form takes, so the dashboard above it does not jump when one
// overlay replaces the other.
func TestThePickerAndTheLaunchFormLeaveTheDashboardTheSameRoom(t *testing.T) {
	baseH, bodyH := pickerSplit(24)
	if bodyH != 12 || baseH != 12 {
		t.Errorf("pickerSplit(24) = (%d, %d), want (12, 12) — the launch form's split", baseH, bodyH)
	}
	// A screen too short to hold both still leaves the dashboard a row, rather than
	// asking Render for a negative height.
	for _, h := range []int{2, 4, 6, 10, 13, 24, 40} {
		b, body := pickerSplit(h)
		if b < 1 || body < 1 || b+body != h {
			t.Errorf("pickerSplit(%d) = (%d, %d): the halves must be positive and sum to the height", h, b, body)
		}
	}
}

// The scroll window keeps the cursor's WHOLE block on screen, and for one-line rows it
// is behaviour-identical to the fixed-height arithmetic it replaced. Both halves
// matter: a wrapped remedy half off the bottom is the same defect as a cursor off the
// bottom, and a rewrite that quietly changed the uniform case would move every frame.
func TestTheScrollWindowKeepsTheCursorsWholeBlockOnScreen(t *testing.T) {
	uniform := make([][]string, 20)
	for i := range uniform {
		uniform[i] = []string{"row"}
	}
	for _, c := range []struct{ cursor, first, count int }{
		{0, 0, 6}, {2, 0, 6}, {5, 0, 6}, {6, 1, 6}, {17, 12, 6}, {19, 14, 6},
	} {
		if first, count := pickerLayout(uniform, c.cursor, 6); first != c.first || count != c.count {
			t.Errorf("pickerLayout(cursor=%d) = (%d, %d), want (%d, %d)",
				c.cursor, first, count, c.first, c.count)
		}
	}
	// A three-line block under the cursor: it is whole, and it costs the rows it needs.
	tall := [][]string{{"a"}, {"b"}, {"c1", "c2", "c3"}, {"d"}, {"e"}}
	first, count := pickerLayout(tall, 2, 4)
	if first != 1 || count != 2 {
		t.Errorf("pickerLayout with a 3-line block = (%d, %d), want (1, 2)", first, count)
	}
	var drawn int
	for i := first; i < first+count; i++ {
		drawn += len(tall[i])
	}
	if drawn > 4 {
		t.Errorf("the window draws %d lines into a budget of 4", drawn)
	}
}

// An alias longer than the name column left exactly ONE space before its reason, so
// `orbits.github.com not a shell host — this is a git remote…` read as a run-on beside the
// padded `nuc           tmux 3.2a`. Measured live at 110 columns on a real ~/.ssh/config,
// where 5 of 20 candidates overflow the column (known-issues L3).
func TestALongAliasDoesNotRunIntoItsReason(t *testing.T) {
	rows := []PickerRow{
		{Alias: "nuc", Kept: true, Usable: true, Version: "3.2a"},
		{Alias: "orbits.github.com", Usable: false,
			Reason: "not a shell host — this is a git remote, so there is nothing to poll"},
	}
	out := strings.Join(RenderPicker(rows, 110, 24, 0), "\n")
	for _, line := range strings.Split(out, "\n") {
		if !strings.Contains(line, "orbits.github.com") {
			continue
		}
		// The alias keeps its own row: the reason is not jammed one space after it.
		if strings.Contains(line, "orbits.github.com not a shell host") {
			t.Errorf("the alias runs into its reason:\n%q", line)
		}
	}
	// And the reason must still be THERE — moving it must not lose it.
	if !strings.Contains(out, "not a shell host") {
		t.Errorf("the reason disappeared:\n%s", out)
	}
	// A short alias keeps the aligned column, so the frames of every ordinary row do not
	// move: that alignment is what makes the list scannable.
	if !strings.Contains(out, "nuc           tmux 3.2a") {
		t.Errorf("the aligned column moved for a short alias:\n%s", out)
	}
}

// The column is measured in DISPLAY columns, not bytes: `%-13s` pads by byte length, so a
// non-ASCII alias silently shifted the version column for every row after it.
func TestTheAliasColumnAlignsByDisplayWidth(t *testing.T) {
	// CJK, not Cyrillic. `%-13s` pads by RUNE count — measured — so a four-rune Cyrillic
	// alias and a four-rune ASCII one come out the same width and the case could not fail.
	// `中文` is 2 runes and 4 DISPLAY columns, which is the shift Pad exists to stop.
	rows := []PickerRow{
		{Alias: "中文", Kept: true, Usable: true, Version: "3.7b"},
		{Alias: "abcd", Kept: true, Usable: true, Version: "3.7b"},
	}
	out := RenderPicker(rows, 110, 24, 0)
	var cols []int
	for _, line := range out {
		if i := strings.Index(line, "tmux 3.7b"); i >= 0 {
			cols = append(cols, lines.Width(line[:i]))
		}
	}
	if len(cols) != 2 {
		t.Fatalf("found %d version columns, want 2:\n%s", len(cols), strings.Join(out, "\n"))
	}
	if cols[0] != cols[1] {
		t.Errorf("a two-rune CJK alias put the version at column %d and a four-rune "+
			"ASCII one at %d — the padding counts runes, not display columns", cols[0], cols[1])
	}
}

// The picker's footer had the defect the project list's channel was built to avoid: a note
// REPLACED the file's warning, so a fact that stays true until hosts.toml is rewritten
// vanished behind a message that is gone on the next keystroke. The code's own comment says
// the warning "must not vanish on the next `j`" — and a note made it vanish immediately.
func TestThePickerFooterSharesItsRowRatherThanReplacingIt(t *testing.T) {
	m := modelWith(t)
	m.mode = modePicker
	m.width, m.height = 120, 24
	m.picker = []PickerRow{{Alias: "nuc", Kept: true, Usable: true, Version: "3.2a"}}
	m.pickerWarn = "hosts.toml: two entries for nuc — the later one wins"

	// With no note, the warning has the row, which is the resting state.
	if out := m.View(); !strings.Contains(out, "two entries for nuc") {
		t.Errorf("the file's warning is not shown at rest:\n%s", out)
	}

	// With a note, BOTH must be reachable: the note first, because it answers something just
	// pressed, and the warning either beside it or marked as dropped.
	m.note = "probed 7 hosts"
	out := m.View()
	if !strings.Contains(out, "probed 7 hosts") {
		t.Errorf("the note is not shown:\n%s", out)
	}
	if !strings.Contains(out, "two entries for nuc") && !strings.Contains(out, "+") {
		t.Errorf("the warning vanished behind a note with no sign it exists — it stays true "+
			"until the file is rewritten:\n%s", out)
	}
	for _, line := range strings.Split(out, "\n") {
		if lines.Width(line) > m.width {
			t.Errorf("a row is %d wide at %d: %q", lines.Width(line), m.width, line)
		}
	}
}

// At a width where they cannot both fit, the note wins and the loss is MARKED — the same
// priority the project list states, so the two screens cannot teach different habits.
func TestThePickerFooterMarksWhatItDrops(t *testing.T) {
	m := modelWith(t)
	m.mode = modePicker
	m.width, m.height = 60, 24
	m.picker = []PickerRow{{Alias: "nuc", Kept: true, Usable: true, Version: "3.2a"}}
	m.pickerWarn = "hosts.toml: two entries for nuc — the later one wins, which is probably not what was meant"
	m.note = "probed 7 hosts in 7.65s"
	out := m.View()
	if !strings.Contains(out, "probed 7 hosts") {
		t.Errorf("the note lost to the warning:\n%s", out)
	}
	if !strings.Contains(out, "+") {
		t.Errorf("the warning was dropped SILENTLY:\n%s", out)
	}
	for _, line := range strings.Split(out, "\n") {
		if lines.Width(line) > m.width {
			t.Errorf("a row is %d wide at %d: %q", lines.Width(line), m.width, line)
		}
	}
}

// The picker pre-fitted its own parts and handed the RESULT to Render as the note, where the
// footer fits again — so two dropped parts produced two independent `+1` markers on one row
// instead of one `+2`, and the marker stopped saying how many parts are waiting, which is the
// one thing lines.Fit's marker exists to say.
func TestThePickerFooterMarksOnceNotTwice(t *testing.T) {
	m := modelWith(t)
	m.mode = modePicker
	m.width, m.height = 80, 24
	m.picker = []PickerRow{{Alias: "nuc", Kept: true, Usable: true, Version: "3.2a"}}
	m.note = "kept 3 hosts in hosts.toml · stopped the ssh master for one of them"
	m.pickerWarn = "hosts.toml: two entries for nuc — the later one wins, probably not meant"
	m.hosts = []hub.Host{{Label: "local", Status: hub.Up},
		{Label: "nuc", Status: hub.DegradedFormat, Reason: "window_activity came back blank"}}

	out := m.View()
	var foot string
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "kept 3 hosts") {
			foot = line
		}
	}
	if foot == "" {
		t.Fatalf("the note is not on screen:\n%s", out)
	}
	if n := strings.Count(foot, "+"); n > 1 {
		t.Errorf("the footer carries %d markers: %q — one row, one count of what is "+
			"missing", n, strings.TrimRight(foot, " "))
	}
	if lines.Width(foot) > 80 {
		t.Errorf("the footer is %d wide: %q", lines.Width(foot), foot)
	}
}
