package state

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func zone(t *testing.T, name string) []string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	return strings.Split(strings.TrimRight(string(b), "\n"), "\n")
}

func TestClassifyBothPoles(t *testing.T) {
	cases := []struct {
		fixture string
		age     time.Duration
		want    State
	}{
		// A waiting pane MUST read Needs.
		{"claude_needs.txt", 5 * time.Second, Needs},
		// A working pane MUST NOT read Needs or Idle.
		{"claude_works.txt", 1 * time.Second, Works},
		// An idle pane MUST NOT read Works, even though its scrollback still
		// contains a spinner line.
		{"claude_idle.txt", 5 * time.Second, Idle},
		// The same idle pane, silent long enough, is Quiet.
		{"claude_idle.txt", 10 * time.Minute, Quiet},
		// A shell that printed a failure and returned to its prompt.
		{"shell_idle.txt", 5 * time.Second, Error},
	}
	for _, c := range cases {
		got := Classify(Input{Zone: zone(t, c.fixture), ActivityAge: c.age})
		if got != c.want {
			t.Errorf("Classify(%s, age=%v) = %v, want %v", c.fixture, c.age, got, c.want)
		}
	}
}

func TestSpinnerTextAloneIsNotWorking(t *testing.T) {
	// The decisive negative pole: 'Churned for 6s' and the glyph persist in an
	// idle pane's scrollback, so keying Works on them pins every finished
	// session as working and defeats the whole inbox.
	//
	// The age must be stale for this to isolate what it names: a fresh pane is
	// Works on its delta alone, correctly, and would mask the text question.
	in := Input{Zone: []string{"✻ Churned for 6s", "", "❯"}, ActivityAge: 30 * time.Second}
	if got := Classify(in); got == Works {
		t.Fatal("spinner text alone must not read as Works")
	}
}

func TestDeadPaneIsError(t *testing.T) {
	in := Input{Zone: []string{"anything"}, Dead: true}
	if got := Classify(in); got != Error {
		t.Fatalf("dead pane = %v, want Error", got)
	}
}

func TestAltPaneIsNotClassifiedFromContent(t *testing.T) {
	// An alt-screen pane's capture is a full-screen app's rendering, which the
	// chrome/content classifier has no purchase on. It reports Works while its
	// activity is fresh, Quiet when it is not — never Needs or Error.
	in := Input{Zone: []string{"Do you want to proceed?"}, Alt: true, ActivityAge: time.Second}
	if got := Classify(in); got != Works {
		t.Fatalf("fresh alt pane = %v, want Works", got)
	}
	in.ActivityAge = 10 * time.Minute
	if got := Classify(in); got != Quiet {
		t.Fatalf("stale alt pane = %v, want Quiet", got)
	}
}

func TestRankOrdersTheInbox(t *testing.T) {
	// Sorted by how much the pane wants the user: waiting, then failed, then
	// suspiciously silent, then finished, then busy, then historical.
	order := []State{Needs, Error, Quiet, Idle, Works, Gone}
	for i := 1; i < len(order); i++ {
		if order[i-1].Rank() >= order[i].Rank() {
			t.Fatalf("%v must rank before %v", order[i-1], order[i])
		}
	}
}

// The primary works signal is the delta, not the rendered text. Claude Code
// composes "esc to interrupt" from a keybinding chord at render time — the
// literal is absent from the 2.1.227 bundle — so a pane that is plainly busy
// must read Works from its activity age even with no marker on screen.
func TestFreshOutputIsWorksWithoutAnyMarker(t *testing.T) {
	in := Input{Zone: []string{"some output", "more output"}, ActivityAge: time.Second}
	if got := Classify(in); got != Works {
		t.Fatalf("fresh output with no marker = %v, want Works", got)
	}
}

// And the reverse: stale output with no marker is not Works.
func TestStaleOutputIsNotWorks(t *testing.T) {
	in := Input{Zone: []string{"some output"}, ActivityAge: 30 * time.Second}
	if got := Classify(in); got == Works {
		t.Fatal("30s-old output must not read as Works")
	}
}

// A question outranks freshness: Claude prints the prompt and then waits, so the
// pane is both fresh and waiting, and the user needs to know it is waiting.
func TestNeedsOutranksFreshness(t *testing.T) {
	in := Input{Zone: []string{"● done", "Do you want to proceed?", "❯"}, ActivityAge: time.Second}
	if got := Classify(in); got != Needs {
		t.Fatalf("a fresh pane asking a question = %v, want Needs", got)
	}
}

// A real capture of Claude Code's own trust dialog, taken from a live pane with
// the cursor-anchored zone the hub actually reads. The earlier pattern required
// the digit at line start and missed it: the zone ends `❯ 1. Yes, I trust this
// folder`, so the most urgent state went undetected on a genuine prompt.
func TestRealClaudeChoicePromptIsNeeds(t *testing.T) {
	in := Input{Zone: zone(t, "claude_choice_real.txt"), ActivityAge: time.Second}
	if got := Classify(in); got != Needs {
		t.Fatalf("a live Claude choice prompt = %v, want Needs\nzone: %q",
			got, zone(t, "claude_choice_real.txt"))
	}
}

// The shape is what matters: a selection cursor on a numbered option, whatever
// the words are.
func TestChooserShapeIsNeedsWhateverTheWording(t *testing.T) {
	for _, last := range []string{
		"❯ 1. Yes, I trust this folder",
		"  ❯ 2. No, exit",
		"❯ 3. Something nobody has written yet",
	} {
		in := Input{Zone: []string{"some text", "", last}, ActivityAge: time.Second}
		if got := Classify(in); got != Needs {
			t.Errorf("Classify(%q) = %v, want Needs", last, got)
		}
	}
}

// And it must not fire on ordinary numbered output, which has no cursor.
func TestPlainNumberedOutputIsNotNeeds(t *testing.T) {
	in := Input{Zone: []string{"steps:", "1. build", "2. test", "3. ship"}, ActivityAge: 30 * time.Second}
	if got := Classify(in); got == Needs {
		t.Fatal("a numbered list with no selection cursor must not read as Needs")
	}
}

// A constant freshness window is wrong in the direction that matters: at a
// measured 2.1s cycle against a fixed 4s window a plainly busy pane read idle,
// and idle-versus-works is what the inbox is built on.
func TestFreshnessScalesWithThePollInterval(t *testing.T) {
	if got := FreshFor(0); got != FreshForFloor {
		t.Errorf("unknown interval should fall back to the floor, got %v", got)
	}
	if got := FreshFor(500 * time.Millisecond); got != FreshForFloor {
		t.Errorf("a fast cycle should still use the floor, got %v", got)
	}
	if got := FreshFor(3 * time.Second); got != 6*time.Second {
		t.Errorf("FreshFor(3s) = %v, want two intervals", got)
	}

	// A pane that wrote one cycle ago on a slow host is working, not idle.
	slow := Input{Zone: []string{"output"}, ActivityAge: 5 * time.Second, PollInterval: 4 * time.Second}
	if got := Classify(slow); got != Works {
		t.Fatalf("a pane one slow cycle old = %v, want Works", got)
	}
	// The same age on a fast host is not.
	fast := Input{Zone: []string{"output"}, ActivityAge: 5 * time.Second, PollInterval: time.Second}
	if got := Classify(fast); got == Works {
		t.Fatal("5s-old output on a 1s cycle must not read as Works")
	}
}

// A question at the CURSOR is one being asked. Position is what keeps this
// precise: the same words earlier in the zone are just output. Measured need:
// a pane sitting on "Rebase onto main? Proceed?" read idle.
func TestAQuestionAtTheCursorIsNeeds(t *testing.T) {
	yes := [][]string{
		{"● ran the rebase", "Rebase onto main? Proceed?"},
		{"Delete 4 files? [y/N]"},
		{"● here is the plan", "", "Want me to apply it?", ""},
	}
	for _, z := range yes {
		if got := Classify(Input{Zone: z, ActivityAge: 30 * time.Second}); got != Needs {
			t.Errorf("Classify(%q) = %v, want Needs", z, got)
		}
	}
	no := [][]string{
		// a question earlier in the zone, with output after it: not being asked now
		{"Should we refactor? probably", "● did it", "● done"},
		{"done, nothing to do"},
		{">>> "},
	}
	for _, z := range no {
		if got := Classify(Input{Zone: z, ActivityAge: 30 * time.Second}); got == Needs {
			t.Errorf("Classify(%q) = Needs, want anything else", z)
		}
	}
}

// Claude renders at least two dialog shapes. Matching only the numbered one
// missed the MCP-approval dialog, which is a session waiting on the user before
// it has even started — measured on a live startup chain.
func TestBothClaudeDialogShapesAreNeeds(t *testing.T) {
	shapes := [][]string{
		// numbered choice, from the trust dialog
		{"Security guide", "", "❯ 1. Yes, I trust this folder", "  2. No, exit"},
		// multi-select checkboxes, from the MCP approval dialog
		{"more in the MCP documentation.", "❯ [✔] context7", "  [✔] datadog"},
		// and its footer alone is enough
		{"  [✔] gitlab", "Space to select · Enter to confirm · Esc to reject all"},
	}
	for _, z := range shapes {
		if got := Classify(Input{Zone: z, ActivityAge: 30 * time.Second}); got != Needs {
			t.Errorf("Classify(%q) = %v, want Needs", z, got)
		}
	}
}

// A credential prompt is a session waiting on a human as much as a question is.
// Measured: a bare `Password:` read as idle. The rule keys on a closed set of
// words, not on a trailing colon, because most output ends in a colon.
func TestCredentialPromptIsNeeds(t *testing.T) {
	for _, last := range []string{
		"Password:", "Password: ", "Enter passphrase for key '/home/x/.ssh/id':",
		"One-time code:", "Verification code: ", "2FA token:",
	} {
		in := Input{Zone: []string{"connecting…", last}, ActivityAge: 30 * time.Second}
		if got := Classify(in); got != Needs {
			t.Errorf("Classify(%q) = %v, want Needs", last, got)
		}
	}
	// And it must not fire on ordinary output that merely ends in a colon, or
	// mentions a token in passing.
	for _, z := range [][]string{
		{"● wrote the following files:"},
		{"● the token budget is large", "● done"},
		{"Results:"},
		{"error: cannot open file:"},
	} {
		if got := Classify(Input{Zone: z, ActivityAge: 30 * time.Second}); got == Needs {
			t.Errorf("Classify(%q) = Needs, want anything else", z)
		}
	}
}

// A source that reports a session but not its state must not be flattened into
// idle: a Claude Code version reporting neither `state` nor `status` is measured,
// and calling that idle lies about a session that might be waiting.
func TestUnknownIsItsOwnStateAndSortsLow(t *testing.T) {
	if got := FromWord(""); got != Unknown {
		t.Fatalf("FromWord(\"\") = %v, want Unknown", got)
	}
	if got := FromWord("nonsense-from-a-future-version"); got != Unknown {
		t.Fatalf("an unrecognised word = %v, want Unknown", got)
	}
	for _, w := range []struct {
		word string
		want State
	}{{"needs", Needs}, {"error", Error}, {"quiet", Quiet}, {"idle", Idle}, {"works", Works}} {
		if got := FromWord(w.word); got != w.want {
			t.Errorf("FromWord(%q) = %v, want %v", w.word, got, w.want)
		}
	}
	// Unknown means "nothing to act on that we know of", so it sorts below works
	// and above gone.
	if !(Works.Rank() < Unknown.Rank() && Unknown.Rank() < Gone.Rank()) {
		t.Fatalf("unknown must sort between works and gone, got %d", Unknown.Rank())
	}
	if Unknown.Glyph() != "?" || Unknown.String() != "unknown" {
		t.Errorf("unknown renders as %q/%q", Unknown.Glyph(), Unknown.String())
	}
}

// Claude Code renders its prompt as `❯` followed by U+00A0, a NO-BREAK SPACE —
// measured off a live pane's capture. Go's regexp `\s` is ASCII-only and does not
// match U+00A0, so every SHAPE pattern here silently failed against the real
// screen. The numbered dialog only passed because a LITERAL ("i trust this
// folder") caught it, which is exactly backwards from the design's intent that the
// shape is load-bearing and the words are corroboration.
//
// So these cases use REWORDED text: a literal cannot rescue them, and only the
// shape can.
func TestNBSPSeparatedDialogsAreDetectedByShape(t *testing.T) {
	const nb = " "
	cases := []struct {
		name string
		zone []string
	}{
		{"numbered choice, reworded", []string{"Pick one:", "❯" + nb + "1. Carry on regardless"}},
		{"numbered choice, second option", []string{"❯" + nb + "2. Something else entirely"}},
		{"multi-select checkbox", []string{"Approve servers", "❯" + nb + "[✔] some-server"}},
		{"multi-select, unchecked", []string{"❯" + nb + "[ ] another-server"}},
	}
	for _, c := range cases {
		got := Classify(Input{Zone: c.zone, ActivityAge: 300 * time.Second,
			PollInterval: 2 * time.Second})
		if got != Needs {
			t.Errorf("%s: Classify = %s, want needs (zone %q)", c.name, got, c.zone)
		}
	}
}

// The same shapes with an ordinary space must keep working — the fix must widen
// what matches without narrowing it.
func TestOrdinarySpaceDialogsStillDetected(t *testing.T) {
	for _, zone := range [][]string{
		{"❯ 1. Carry on regardless"},
		{"❯ [✔] some-server"},
	} {
		if got := Classify(Input{Zone: zone, ActivityAge: 300 * time.Second,
			PollInterval: 2 * time.Second}); got != Needs {
			t.Errorf("Classify(%q) = %s, want needs", zone, got)
		}
	}
}

// And the widening must not turn ordinary output into a question. A line that
// merely contains a chevron is not a dialog.
func TestNBSPFoldingDoesNotOverMatch(t *testing.T) {
	const nb = " "
	for _, zone := range [][]string{
		{"commit 3f2a1b0 (HEAD -> main)"},
		{"total 48"},
		{"❯" + nb + " "},                       // an empty input box is not a question
		{"the value is 1. that is all"},        // a digit and a dot, no chevron
		{"[✔] done earlier in the transcript"}, // a checkbox with no cursor
	} {
		if got := Classify(Input{Zone: zone, ActivityAge: 300 * time.Second,
			PollInterval: 2 * time.Second}); got == Needs {
			t.Errorf("Classify(%q) = needs, but nothing there asks anything", zone)
		}
	}
}

// QuietAfter is pinned to the value the data supports, with the numbers in the
// test rather than imported from the code — a test whose expected value comes from
// the thing under test only asserts that assignment works.
//
// Grounded over 210 491 inter-event gaps from 59 450 real Claude Code transcripts,
// split on the assistant's own stop_reason: silence DURING work has a p99 of 90.9 s.
// So the threshold has to sit clear of ~91 s, or a pane that is plainly working
// reads as quiet about one time in a hundred.
func TestQuietAfterClearsTheMeasuredWorkingTail(t *testing.T) {
	const workingP99 = 91 * time.Second
	if QuietAfter <= workingP99 {
		t.Errorf("QuietAfter = %v, which is inside the measured working tail (p99 %v): "+
			"a working pane would read quiet", QuietAfter, workingP99)
	}
	// And it must not be so long that a genuinely hung pane goes unnoticed for a
	// coffee break. This is a judgement rather than a measurement, and it is here so
	// that raising the threshold again is a deliberate act.
	if QuietAfter > 10*time.Minute {
		t.Errorf("QuietAfter = %v is long enough to hide a hung pane", QuietAfter)
	}
}

// The behaviour either side of the threshold, asserted at literal times so a change
// to the constant cannot pass unnoticed.
func TestQuietBoundaryBehaviour(t *testing.T) {
	zone := []string{"$ ls", "file.txt", "$ "}
	for _, c := range []struct {
		age  time.Duration
		want State
	}{
		{100 * time.Second, Idle},  // inside the working tail: not yet suspicious
		{170 * time.Second, Idle},  // still under the threshold
		{200 * time.Second, Quiet}, // past it
		{1 * time.Hour, Quiet},
	} {
		got := Classify(Input{Zone: zone, ActivityAge: c.age, PollInterval: 2 * time.Second})
		if got != c.want {
			t.Errorf("ActivityAge %v: Classify = %s, want %s", c.age, got, c.want)
		}
	}
}
