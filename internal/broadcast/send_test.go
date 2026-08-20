package broadcast

import (
	"context"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/DawnBreather/tmux-hub/internal/tmux"
)

func sender(t *testing.T, inst Instance) (*Sender, *Stamper, tmux.Target, []string) {
	t.Helper()
	tgt, ids := liveServer(t, 2)
	r := tmux.NewExec(10 * time.Second)
	st := NewStamper(r, inst)
	return NewSender(r.(tmux.InputRunner), st, inst), st, tgt, ids
}

func captured(t *testing.T, tgt tmux.Target, paneID string) string {
	t.Helper()
	out, err := exec.Command("tmux", "-S", tgt.Socket, "capture-pane", "-p", "-t", paneID).Output()
	if err != nil {
		t.Fatalf("capture-pane: %v", err)
	}
	return string(out)
}

// The assertion is on the PANE, not on the confirmation. Measured with the old
// -H primitive, the hub printed `OK %0` having delivered nothing — so a test that
// reads the confirmation would have passed on a completely broken write path.
func TestSendDeliversAndIsWitnessed(t *testing.T) {
	s, st, tgt, ids := sender(t, Instance("s1"))
	if _, err := st.Stamp(context.Background(), tgt, ids[0]); err != nil {
		t.Fatalf("Stamp: %v", err)
	}
	time.Sleep(1100 * time.Millisecond) // let window_activity settle in the past

	const text = "refactor the auth module; run the tests"
	res, err := s.Send(context.Background(), Target{Host: "test", Tmux: tgt, PaneID: ids[0]}, text)
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if res.Outcome == Refused {
		t.Fatalf("a stamped pane was refused: %s", res.Reason)
	}

	res = s.Witness(context.Background(), res, text)
	if res.Outcome != Delivered {
		t.Errorf("Outcome = %s (%s), want delivered", res.Outcome, res.Reason)
	}
	if got := captured(t, tgt, ids[0]); !strings.Contains(got, "refactor the auth module") {
		t.Errorf("the text did not arrive in the pane:\n%s", got)
	}
	// Nothing may execute: Enter is a separate act.
	if strings.Contains(captured(t, tgt, ids[0]), "command not found") {
		t.Error("the payload executed")
	}
}

// The witness must be a SECOND read. Measured three times on 3.7b: activity reads
// identical before and after the paste inside one invocation, because it tracks
// the pane's OUTPUT and the process cannot have answered while the batch that
// wrote to it is still running. So Send alone must never claim delivery.
func TestSendAloneNeverClaimsDelivery(t *testing.T) {
	s, st, tgt, ids := sender(t, Instance("s2"))
	if _, err := st.Stamp(context.Background(), tgt, ids[0]); err != nil {
		t.Fatalf("Stamp: %v", err)
	}
	res, err := s.Send(context.Background(), Target{Host: "test", Tmux: tgt, PaneID: ids[0]}, "x")
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if res.Outcome == Delivered {
		t.Error("Send claimed delivery without a second read — the witness cannot work there")
	}
	if res.ActivityBefore == 0 {
		t.Error("Send did not record activity_before, so Witness has nothing to compare")
	}
}

// An unstamped pane must be refused AND must receive nothing. Both halves matter:
// measured, a guard without its own -t passed while pasting into the unstamped
// pane, and printed OK for it.
func TestUnstampedPaneIsRefusedAndReceivesNothing(t *testing.T) {
	s, st, tgt, ids := sender(t, Instance("s3"))
	if _, err := st.Stamp(context.Background(), tgt, ids[0]); err != nil {
		t.Fatalf("Stamp: %v", err)
	}
	res, err := s.Send(context.Background(),
		Target{Host: "test", Tmux: tgt, PaneID: ids[1]}, "MUST-NOT-ARRIVE")
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if res.Outcome != Refused {
		t.Errorf("Outcome = %s, want refused", res.Outcome)
	}
	time.Sleep(300 * time.Millisecond)
	for _, id := range ids {
		if got := captured(t, tgt, id); strings.Contains(got, "MUST-NOT-ARRIVE") {
			t.Errorf("the payload reached %s despite the refusal:\n%s", id, got)
		}
	}
}

// A pane the hub holds no token for cannot be sent to at all — the guard would
// otherwise be built from an empty expected value, which is the fail-open §7 names.
func TestSendRefusesAPaneWithNoToken(t *testing.T) {
	s, _, tgt, ids := sender(t, Instance("s4"))
	res, err := s.Send(context.Background(), Target{Host: "test", Tmux: tgt, PaneID: ids[0]}, "x")
	if err == nil && res.Outcome != Refused {
		t.Errorf("Outcome = %s, want refused for an unknown token", res.Outcome)
	}
}

// After the agent goes, the guard must refuse by construction.
func TestUnstampMakesTheNextSendRefuse(t *testing.T) {
	s, st, tgt, ids := sender(t, Instance("s5"))
	if _, err := st.Stamp(context.Background(), tgt, ids[0]); err != nil {
		t.Fatalf("Stamp: %v", err)
	}
	if err := st.Unstamp(context.Background(), tgt, ids[0]); err != nil {
		t.Fatalf("Unstamp: %v", err)
	}
	res, _ := s.Send(context.Background(), Target{Host: "test", Tmux: tgt, PaneID: ids[0]}, "y")
	if res.Outcome != Refused {
		t.Errorf("Outcome = %s, want refused after unstamp", res.Outcome)
	}
}

// Multi-line is the case send-keys got wrong: with bracketed paste on, an embedded
// newline submits the first paragraph. Both flags on paste-buffer prevent it.
func TestMultiLineArrivesWholeAndUnexecuted(t *testing.T) {
	s, st, tgt, ids := sender(t, Instance("s6"))
	if _, err := st.Stamp(context.Background(), tgt, ids[0]); err != nil {
		t.Fatalf("Stamp: %v", err)
	}
	text := "line one of the prompt\nline two of the prompt\nline three;"
	res, err := s.Send(context.Background(), Target{Host: "test", Tmux: tgt, PaneID: ids[0]}, text)
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if res.Outcome == Refused {
		t.Fatalf("refused: %s", res.Reason)
	}
	time.Sleep(400 * time.Millisecond)
	got := captured(t, tgt, ids[0])
	for _, want := range []string{"line one of the prompt", "line two of the prompt", "line three;"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q from the pane:\n%s", want, got)
		}
	}
}

// A trailing semicolon is what send-keys -l silently ate. It must survive.
func TestTrailingSemicolonSurvives(t *testing.T) {
	s, st, tgt, ids := sender(t, Instance("s7"))
	if _, err := st.Stamp(context.Background(), tgt, ids[0]); err != nil {
		t.Fatalf("Stamp: %v", err)
	}
	if _, err := s.Send(context.Background(),
		Target{Host: "test", Tmux: tgt, PaneID: ids[0]}, "done;"); err != nil {
		t.Fatalf("Send: %v", err)
	}
	time.Sleep(400 * time.Millisecond)
	if got := captured(t, tgt, ids[0]); !strings.Contains(got, "done;") {
		t.Errorf("the trailing semicolon was eaten:\n%s", got)
	}
}

// The buffer must not survive the send, in EITHER direction. A batch that aborts
// left `tmux-hub-2: 42 bytes: "secret prompt…"` as the most recent buffer, ahead
// of the user's own, so their next `prefix ]` pastes the hub's prompt.
func TestNoBufferSurvivesASendOrARefusal(t *testing.T) {
	s, st, tgt, ids := sender(t, Instance("s8"))
	if _, err := st.Stamp(context.Background(), tgt, ids[0]); err != nil {
		t.Fatalf("Stamp: %v", err)
	}
	if _, err := s.Send(context.Background(),
		Target{Host: "test", Tmux: tgt, PaneID: ids[0]}, "delivered payload"); err != nil {
		t.Fatalf("Send: %v", err)
	}
	// The refusal path is the one that skipped cleanup.
	if _, err := s.Send(context.Background(),
		Target{Host: "test", Tmux: tgt, PaneID: "%999"}, "secret prompt"); err != nil {
		t.Logf("Send to a missing pane returned %v (expected)", err)
	}
	out, _ := exec.Command("tmux", "-S", tgt.Socket, "list-buffers",
		"-F", "#{buffer_name}").Output()
	if strings.Contains(string(out), "tmux-hub-") {
		t.Errorf("a hub buffer survived:\n%s", out)
	}
	if strings.Contains(string(out), "secret") {
		t.Errorf("the payload is still a buffer:\n%s", out)
	}
}

// Submit is a separate invocation ~50ms later, never part of the payload.
func TestSubmitIsSeparate(t *testing.T) {
	s, st, tgt, ids := sender(t, Instance("s9"))
	if _, err := st.Stamp(context.Background(), tgt, ids[0]); err != nil {
		t.Fatalf("Stamp: %v", err)
	}
	tg := Target{Host: "test", Tmux: tgt, PaneID: ids[0]}
	if _, err := s.Send(context.Background(), tg, "echo hi"); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if sub := s.Submit(context.Background(), tg); sub.Outcome != Delivered {
		t.Fatalf("Submit = %s (%s), want delivered", sub.Outcome, sub.Reason)
	}
	time.Sleep(400 * time.Millisecond)
	if got := captured(t, tgt, ids[0]); !strings.Contains(got, "echo hi") {
		t.Errorf("text missing after submit:\n%s", got)
	}
}

// The `if` needs its own -t, and proving it requires DISABLING the earlier layer
// that masks it: Send refuses outright when the hub holds no token, so a plain
// unstamped pane never reaches the guard at all. Measured — the mutation that
// drops the if's -t left TestUnstampedPaneIsRefusedAndReceivesNothing green.
//
// The case that does reach the guard is a token the hub remembers and the SERVER
// no longer agrees with, which is the real-world one: respawn-pane, an out-of-band
// unstamp, or a server restart recycling the id. With the if targeted, the guard
// reads the target's own option and refuses. Without it, the guard reads the
// server's CURRENT pane — stamped, and a different pane — passes, and the payload
// lands somewhere nobody selected.
func TestGuardReadsTheTargetsOwnOptionNotTheCurrentPanes(t *testing.T) {
	s, st, tgt, ids := sender(t, Instance("sg"))
	ctx := context.Background()

	// The target is stamped, so the hub holds a token for it and the early
	// "no token" refusal — which masked this guarantee entirely — cannot fire.
	tok, err := st.Stamp(ctx, tgt, ids[1])
	if err != nil {
		t.Fatalf("Stamp %s: %v", ids[1], err)
	}

	// The active pane is the stamped one whose option the untargeted guard would
	// read. Assert it rather than assume it, or the test silently stops
	// discriminating the day tmux picks another pane.
	act, err := exec.Command("tmux", "-S", tgt.Socket, "display", "-p", "#{pane_id}").Output()
	if err != nil {
		t.Fatalf("display: %v", err)
	}
	if strings.TrimSpace(string(act)) != ids[0] {
		t.Skipf("the active pane is %q, not %s — this test needs the decoy to be active",
			strings.TrimSpace(string(act)), ids[0])
	}

	// The decoy carries EXACTLY the token we are about to send with, and the target
	// no longer does. That is what makes the two implementations differ: an
	// untargeted guard reads the decoy, matches, and passes. Giving the decoy any
	// other value would make the guard fail for the wrong reason and the test would
	// pass whether or not the -t is there — measured, that is what the first
	// version of this test did.
	set := func(pane, val string) {
		t.Helper()
		if out, err := exec.Command("tmux", "-S", tgt.Socket, "set", "-p", "-t", pane,
			"@hub_sg", val).CombinedOutput(); err != nil {
			t.Fatalf("out-of-band set on %s: %v: %s", pane, err, out)
		}
	}
	set(ids[0], tok)                     // the decoy matches
	set(ids[1], "the-pane-was-replaced") // the target does not

	res, err := s.Send(ctx, Target{Host: "test", Tmux: tgt, PaneID: ids[1]}, "MUST-NOT-ARRIVE")
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if res.Outcome != Refused {
		t.Errorf("Outcome = %s (%s), want refused", res.Outcome, res.Reason)
	}
	time.Sleep(400 * time.Millisecond)
	for _, id := range ids {
		if got := captured(t, tgt, id); strings.Contains(got, "MUST-NOT-ARRIVE") {
			t.Errorf("the payload reached %s: the guard read the wrong pane's option\n%s", id, got)
		}
	}
}

// dropDeleteRunner forwards everything except a standalone delete-buffer, so a
// test can tell `paste-buffer -d` apart from the deferred cleanup. Both exist on
// purpose — the defer cannot run in a hub that was killed, and -d cannot run if
// the batch never reaches the paste — but with both in place either one alone
// makes the buffer vanish, so neither is tested unless the other is removed.
type dropDeleteRunner struct{ inner tmux.InputRunner }

func (d dropDeleteRunner) RunInput(ctx context.Context, t tmux.Target, stdin []byte, args ...string) (tmux.Result, error) {
	if len(args) > 0 && args[0] == "delete-buffer" {
		return tmux.Result{}, nil
	}
	return d.inner.RunInput(ctx, t, stdin, args...)
}

func (d dropDeleteRunner) Run(ctx context.Context, t tmux.Target, args ...string) (tmux.Result, error) {
	return d.inner.RunInput(ctx, t, nil, args...)
}

func TestPasteDeletesTheBufferWithoutTheDeferredCleanup(t *testing.T) {
	tgt, ids := liveServer(t, 1)
	real := tmux.NewExec(10 * time.Second)
	dropped := dropDeleteRunner{inner: real.(tmux.InputRunner)}

	st := NewStamper(dropped, Instance("pd"))
	s := NewSender(dropped, st, Instance("pd"))
	if _, err := st.Stamp(context.Background(), tgt, ids[0]); err != nil {
		t.Fatalf("Stamp: %v", err)
	}
	if _, err := s.Send(context.Background(),
		Target{Host: "test", Tmux: tgt, PaneID: ids[0]}, "atomic cleanup payload"); err != nil {
		t.Fatalf("Send: %v", err)
	}

	out, _ := exec.Command("tmux", "-S", tgt.Socket, "list-buffers",
		"-F", "#{buffer_name}").Output()
	if strings.Contains(string(out), "tmux-hub-") {
		t.Errorf("with the deferred delete removed, -d did not clean up:\n%s", out)
	}
}

// The CONSEQUENCE of -p and -r cannot be reproduced against the `cat` pane these
// tests use: cat interprets neither bracketed paste nor CR, so removing both flags
// changes nothing observable, and the multi-line arrival test stayed green under
// that mutation. The consequence is measured in docs/design.md section 3 against a
// real readline prompt — without -p the first paragraph EXECUTES — and what a unit
// test can honestly assert is that the flags are still there.
func TestPasteCarriesBothBracketedPasteFlags(t *testing.T) {
	tgt, ids := liveServer(t, 1)
	rec := &recordingRunner{inner: tmux.NewExec(10 * time.Second).(tmux.InputRunner)}
	st := NewStamper(rec, Instance("bf"))
	s := NewSender(rec, st, Instance("bf"))
	if _, err := st.Stamp(context.Background(), tgt, ids[0]); err != nil {
		t.Fatalf("Stamp: %v", err)
	}
	if _, err := s.Send(context.Background(),
		Target{Host: "test", Tmux: tgt, PaneID: ids[0]}, "a\nb"); err != nil {
		t.Fatalf("Send: %v", err)
	}
	// Isolate the paste-buffer SEGMENT. The chain also contains `display -p`, so
	// searching the whole string for " -p " finds another command's flag and the
	// check passes with -p removed — measured, that is what the first version did.
	var paste string
	for _, c := range rec.calls {
		for _, a := range c {
			for _, seg := range strings.Split(a, ";") {
				seg = strings.TrimSpace(seg)
				if strings.HasPrefix(seg, "paste-buffer") {
					paste = seg
				}
			}
		}
	}
	if paste == "" {
		t.Fatal("no paste-buffer was issued at all")
	}
	for _, flag := range []string{"-d", "-p", "-r"} {
		if !strings.Contains(paste+" ", flag+" ") {
			t.Errorf("paste-buffer lost %s: %q", flag, paste)
		}
	}
}

type recordingRunner struct {
	inner tmux.InputRunner
	calls [][]string
}

func (r *recordingRunner) RunInput(ctx context.Context, t tmux.Target, stdin []byte, args ...string) (tmux.Result, error) {
	r.calls = append(r.calls, args)
	return r.inner.RunInput(ctx, t, stdin, args...)
}

func (r *recordingRunner) Run(ctx context.Context, t tmux.Target, args ...string) (tmux.Result, error) {
	r.calls = append(r.calls, args)
	return r.inner.RunInput(ctx, t, nil, args...)
}

// Two sends in quick succession must BOTH be witnessed. This is the case the
// activity observable cannot answer: window_activity has one-second resolution, so
// measured against tmux 3.2a six back-to-back sends advanced it only 2 times in 6
// while the text arrived 6 times in 6. A broadcast writes to several panes inside
// one second, which makes that the common case rather than a corner.
func TestRapidSuccessiveSendsAreBothWitnessed(t *testing.T) {
	s, st, tgt, ids := sender(t, Instance("rs"))
	ctx := context.Background()
	if _, err := st.Stamp(ctx, tgt, ids[0]); err != nil {
		t.Fatalf("Stamp: %v", err)
	}
	tg := Target{Host: "test", Tmux: tgt, PaneID: ids[0]}

	for i, text := range []string{"first rapid payload", "second rapid payload"} {
		res, err := s.Send(ctx, tg, text)
		if err != nil {
			t.Fatalf("Send %d: %v", i, err)
		}
		res = s.Witness(ctx, res, text)
		if res.Outcome != Delivered {
			t.Errorf("send %d: Outcome = %s (%s), want delivered — the text is on the pane",
				i, res.Outcome, res.Reason)
		}
	}
}

// A pane that echoes nothing is Unwitnessed rather than Delivered or Refused. That
// is the third outcome doing its job: `cat -v` with the terminal's echo off is the
// closest stand-in for a password prompt.
func TestANonEchoingPaneIsUnwitnessedNotDelivered(t *testing.T) {
	tgt, _ := liveServer(t, 1)
	r := tmux.NewExec(10 * time.Second)
	// A pane that consumes input and prints nothing.
	if out, err := exec.Command("tmux", "-S", tgt.Socket, "new-window", "-d", "-P",
		"-F", "#{pane_id}", "sh -c 'stty -echo; head -c 200 >/dev/null'").Output(); err == nil {
		quiet := strings.TrimSpace(string(out))
		st := NewStamper(r, Instance("ne"))
		s := NewSender(r.(tmux.InputRunner), st, Instance("ne"))
		if _, err := st.Stamp(context.Background(), tgt, quiet); err != nil {
			t.Fatalf("Stamp: %v", err)
		}
		time.Sleep(1200 * time.Millisecond)
		res, err := s.Send(context.Background(),
			Target{Host: "test", Tmux: tgt, PaneID: quiet}, "silent payload text")
		if err != nil {
			t.Fatalf("Send: %v", err)
		}
		res = s.Witness(context.Background(), res, "silent payload text")
		if res.Outcome == Refused {
			t.Errorf("a stamped pane was refused: %s", res.Reason)
		}
		if res.Outcome == Delivered && !strings.Contains(res.Reason, "activity") {
			t.Errorf("a non-echoing pane reported %s via %q — the screen cannot show it",
				res.Outcome, res.Reason)
		}
	} else {
		t.Skipf("could not make a non-echoing pane: %v", err)
	}
}

// splitWitness is tested directly because the screen observable masks it: if the
// marker parse breaks, activity reads 0 and the screen check still answers, so
// nothing goes red — measured. A pure test is the only place this can fail.
func TestSplitWitnessFindsTheMarkerAnywhere(t *testing.T) {
	act, screen := splitWitness("ACT 1786490349\nfirst line\nsecond line\n")
	if act != 1786490349 {
		t.Errorf("act = %d", act)
	}
	if strings.Contains(screen, "ACT") {
		t.Errorf("the marker leaked into the screen: %q", screen)
	}
	if !strings.Contains(screen, "first line") || !strings.Contains(screen, "second line") {
		t.Errorf("screen lost content: %q", screen)
	}

	// Keyed on the marker, not the position: a pane whose own first line looks
	// ACT-shaped must not shift the parse.
	act2, screen2 := splitWitness("ACTUALLY not a marker\nACT 42\ntail\n")
	if act2 != 42 {
		t.Errorf("act = %d, want 42 — the marker must be found by prefix, not by line 0", act2)
	}
	if !strings.Contains(screen2, "ACTUALLY not a marker") {
		t.Errorf("the pane's own line was eaten: %q", screen2)
	}

	// No marker at all: activity unknown, screen intact — so the screen observable
	// still works and the activity one simply abstains.
	act3, screen3 := splitWitness("just a screen\nno marker here\n")
	if act3 != 0 {
		t.Errorf("act = %d, want 0 when there is no marker", act3)
	}
	if !strings.Contains(screen3, "just a screen") {
		t.Errorf("screen = %q", screen3)
	}
}

// stubWitness answers a witness read with a canned screen and activity, so the
// COMPARISON can be tested without a pane that refuses a paste.
type stubWitness struct {
	activity int64
	screen   string
}

func (s stubWitness) RunInput(_ context.Context, _ tmux.Target, _ []byte, _ ...string) (tmux.Result, error) {
	return tmux.Result{Stdout: "ACT " + strconv.FormatInt(s.activity, 10) + "\n" + s.screen}, nil
}

func (s stubWitness) Run(ctx context.Context, t tmux.Target, args ...string) (tmux.Result, error) {
	return s.RunInput(ctx, t, nil, args...)
}

// The screen witness needs MORE of the text than before, not SOME of it. Re-sending
// the same prompt is exactly what the history view exists for, so text the previous
// send left on the pane satisfies a "contains" check — and a paste that delivered
// nothing then reads as delivered, which is the `OK %0` failure this package was
// written to prevent.
func TestTheWitnessComparesTheEchoAgainstItsBaseline(t *testing.T) {
	const text = "run the migration and report back"
	ctx := context.Background()
	for _, c := range []struct {
		name string
		res  Result
		stub stubWitness
		want Outcome
	}{
		{
			name: "the text is on the pane and was NOT there before",
			res:  Result{Outcome: Unwitnessed, ActivityBefore: 500, EchoBefore: 0},
			stub: stubWitness{activity: 500, screen: text},
			want: Delivered,
		},
		{
			name: "the text is on the pane and was already there",
			res:  Result{Outcome: Unwitnessed, ActivityBefore: 500, EchoBefore: 1},
			stub: stubWitness{activity: 500, screen: text},
			want: Unwitnessed,
		},
		{
			name: "a second copy arrived beside the first",
			res:  Result{Outcome: Unwitnessed, ActivityBefore: 500, EchoBefore: 1},
			stub: stubWitness{activity: 500, screen: text + "\n" + text},
			want: Delivered,
		},
		{
			name: "nothing new on the screen, but the pane produced output",
			res:  Result{Outcome: Unwitnessed, ActivityBefore: 500, EchoBefore: 1},
			stub: stubWitness{activity: 501, screen: text},
			want: Delivered,
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := NewSender(c.stub, nil, Instance("wb"))
			got := s.Witness(ctx, c.res, text)
			if got.Outcome != c.want {
				t.Errorf("Outcome = %s (%s), want %s", got.Outcome, got.Reason, c.want)
			}
		})
	}
}

// The other half of the same guarantee, against a real server: the baseline is read
// INSIDE the guard chain, so a second send of the same text sees the first copy.
// Nothing can arrive between that capture and the paste, which is the only placement
// that makes the count comparison sound.
func TestSendRecordsTheBaselineFromInsideTheChain(t *testing.T) {
	s, st, tgt, ids := sender(t, Instance("bl"))
	ctx := context.Background()
	if _, err := st.Stamp(ctx, tgt, ids[0]); err != nil {
		t.Fatalf("Stamp: %v", err)
	}
	tg := Target{Host: "test", Tmux: tgt, PaneID: ids[0]}
	const text = "the very same prompt, twice"

	first, err := s.Send(ctx, tg, text)
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if first.EchoBefore != 0 {
		t.Errorf("EchoBefore = %d on an empty pane, want 0", first.EchoBefore)
	}
	if got := s.Witness(ctx, first, text); got.Outcome != Delivered {
		t.Fatalf("the first send was %s (%s)", got.Outcome, got.Reason)
	}

	second, err := s.Send(ctx, tg, text)
	if err != nil {
		t.Fatalf("Send again: %v", err)
	}
	if second.EchoBefore < 1 {
		t.Errorf("EchoBefore = %d, want at least 1 — the baseline did not see the first copy,"+
			" so a paste that delivered nothing would read as delivered", second.EchoBefore)
	}
	if got := s.Witness(ctx, second, text); got.Outcome != Delivered {
		t.Errorf("the second send was %s (%s), want delivered — a second copy DID arrive",
			got.Outcome, got.Reason)
	}
}

// A guard refusal exits 0 with an empty stderr, so an interrupt the guard blocked
// must not be reported as delivered: the operator would believe an agent was
// stopped. Measured before the fix — Interrupt returned a nil error and the UI
// pre-set the outcome to Delivered.
func TestInterruptRefusedByTheGuardIsNotDelivered(t *testing.T) {
	inst := Instance("ir")
	s, st, tgt, ids := sender(t, inst)
	ctx := context.Background()
	if _, err := st.Stamp(ctx, tgt, ids[0]); err != nil {
		t.Fatalf("Stamp: %v", err)
	}
	// The pane's option changes out of band: a respawn, another hub, or a server
	// restart recycling the id. The hub's memory is now stale.
	if out, err := exec.Command("tmux", "-S", tgt.Socket, "set", "-p", "-t", ids[0],
		inst.Option(), "0123456789abcdef0123456789abcdef").CombinedOutput(); err != nil {
		t.Fatalf("set: %v: %s", err, out)
	}

	res := s.Interrupt(ctx, Target{Host: "test", Tmux: tgt, PaneID: ids[0]}, "C-c")
	if res.Outcome != Refused {
		t.Errorf("Outcome = %s (%s), want refused", res.Outcome, res.Reason)
	}
	if !strings.Contains(res.Reason, "guard") {
		t.Errorf("Reason = %q, want it to name the guard", res.Reason)
	}
}

// A pane the hub holds no token for cannot be submitted to or interrupted, and the
// answer is a Refused OUTCOME rather than a Go error: the caller reports outcomes,
// and an error it had to translate is one it could forget to.
func TestSubmitAndInterruptRefuseAnUnstampedPane(t *testing.T) {
	s, _, tgt, ids := sender(t, Instance("us"))
	tg := Target{Host: "test", Tmux: tgt, PaneID: ids[0]}
	ctx := context.Background()

	if got := s.Submit(ctx, tg); got.Outcome != Refused {
		t.Errorf("Submit = %s (%s), want refused", got.Outcome, got.Reason)
	}
	if got := s.Interrupt(ctx, tg, "C-c"); got.Outcome != Refused {
		t.Errorf("Interrupt = %s (%s), want refused", got.Outcome, got.Reason)
	}
	if got := s.Interrupt(ctx, tg, "rm -rf /"); got.Outcome != Refused {
		t.Errorf("Interrupt with a key that is not an interrupt = %s, want refused", got.Outcome)
	}
}

// A prefix short enough to be a coincidence is not evidence, and a rune-slice must
// never split a character in half.
func TestEchoCountRefusesAShortPrefixAndSlicesByRune(t *testing.T) {
	if _, ok := echoCount("a screen with abc on it", "abc"); ok {
		t.Error("a three-character prefix was accepted as evidence")
	}
	n, ok := echoCount("проверка связи и ещё немного текста", "проверка связи и ещё")
	if !ok || n != 1 {
		t.Errorf("echoCount over multi-byte text = (%d, %v), want (1, true)", n, ok)
	}
	// 30 runes of multi-byte text: the slice must land on a character boundary, so
	// the prefix is still findable in the screen it came from.
	long := strings.Repeat("日", 30)
	if n, ok := echoCount(long, long); !ok || n != 1 {
		t.Errorf("echoCount on a 30-rune payload = (%d, %v), want (1, true) — a byte slice"+
			" would have split a character", n, ok)
	}
}

// guardEcho.confirms decides whether the write reached the pane it was aimed at, and
// until this test nothing exercised it: every existing refusal test goes through
// `g.refused`, which is tmux's own guard saying no. The case these checks exist for is
// the opposite one — tmux's guard PASSED and the confirmation still disagrees, which is
// the measured crossed-pair failure where a send landed on one pane and reported OK for
// another.
//
// Measured before this test existed: removing any of the three checks — the token, the
// pane id, or the empty confirmation — left the whole suite green, including the one the
// comment beside it calls load-bearing.
func TestTheEchoMustNameTheIntendedPaneAndCarryItsToken(t *testing.T) {
	const (
		pane = "%7"
		tok  = "0123456789abcdef0123456789abcdef"
	)
	for _, c := range []struct {
		name   string
		echo   guardEcho
		wantOK bool
		reason string
	}{
		{"the right pane with the right token", guardEcho{paneID: pane, token: tok}, true, ""},
		{"tmux's own guard refused", guardEcho{refused: true}, false, "guard refused"},
		{"nothing came back at all", guardEcho{}, false, "no confirmation"},
		{"another pane answered", guardEcho{paneID: "%3", token: tok}, false, "not %7"},
		{"the right pane, a different token", guardEcho{paneID: pane, token: "deadbeef"}, false, "different token"},
		{"the right pane, no token at all", guardEcho{paneID: pane}, false, "different token"},
	} {
		reason, ok := c.echo.confirms(pane, tok, "stderr text")
		if ok != c.wantOK {
			t.Errorf("%s: ok = %v, want %v (reason %q)", c.name, ok, c.wantOK, reason)
			continue
		}
		if !ok && !strings.Contains(reason, c.reason) {
			t.Errorf("%s: reason = %q, want it to mention %q", c.name, reason, c.reason)
		}
		if ok && reason != "" {
			t.Errorf("%s: a confirmed echo carried a reason %q", c.name, reason)
		}
	}
}
