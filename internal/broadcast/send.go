package broadcast

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/DawnBreather/tmux-hub/internal/tmux"
)

// Outcome is what happened to one target. It is three-valued on purpose: a
// confirmation fires whenever the pane resolves and the guard passed, so a boolean
// would report success for a send that delivered nothing — measured, the hub
// printed `OK %0` having delivered nothing at all, and the operator then waits on
// an agent that never got the prompt.
type Outcome string

const (
	Delivered   Outcome = "delivered"
	Unwitnessed Outcome = "sent-unwitnessed"
	Refused     Outcome = "refused"
)

// Target is one pane to write to.
type Target struct {
	Host   string
	Tmux   tmux.Target
	PaneID string
}

// Result is the per-target record. ActivityBefore and EchoBefore are carried so the
// witness can be a later read, which is the only place it can work — and so it has
// something to compare against, which is the only way a repeated payload can be
// told from a new one.
type Result struct {
	Target         Target
	Outcome        Outcome
	Reason         string
	ActivityBefore int64
	// EchoBefore is how many times the payload's prefix was already on the screen
	// BEFORE the paste, read inside the same invocation. Without it, text left by
	// the previous send satisfies the witness — and re-sending the same prompt is
	// exactly what the history view exists for.
	EchoBefore int
	Token      string
	// Submitted says the Enter that follows a paste was sent AND confirmed. It is a
	// field of its own because `delivered` cannot carry the difference: a prompt
	// sitting unsent in an agent's input box has been delivered and will do nothing.
	Submitted bool
}

// WitnessDelay is how long to wait before the second read. 150 ms is enough for the
// observable that actually decides: measured, the pane's screen shows the text at
// +250 ms in 6 of 6 rapid sends. It is NOT enough to cross a one-second boundary,
// which is why activity is the secondary observable rather than the primary — see
// Witness. The next ordinary tick is the backstop for either.
const WitnessDelay = 150 * time.Millisecond

// Sender is the write path.
type Sender struct {
	run  tmux.InputRunner
	st   *Stamper
	inst Instance
	seq  atomic.Uint64
}

func NewSender(r tmux.InputRunner, st *Stamper, i Instance) *Sender {
	return &Sender{run: r, st: st, inst: i}
}

// Send puts text into one pane, guarded by that pane's token inside the same
// invocation. It never returns Delivered: only Witness can, because the witness
// cannot be read here (see WitnessDelay).
func (s *Sender) Send(ctx context.Context, tg Target, text string) (Result, error) {
	res := Result{Target: tg, Outcome: Refused}

	tok, ok := s.st.Token(tg.Host, tg.PaneID)
	if !ok || tok == "" {
		// Building a guard from an empty expected value is the fail-open this
		// refuses: every unstamped pane would satisfy it.
		res.Reason = "the hub holds no identity token for this pane"
		return res, nil
	}
	res.Token = tok

	buf := s.inst.Buffer(s.seq.Add(1))

	// The payload travels on stdin, so no quoting layer touches it. `load-buffer`
	// runs AFTER the token check has been decided and BEFORE the paste, and the
	// deferred delete is its own invocation so it cannot be skipped by a batch
	// that aborts.
	if r, err := s.run.RunInput(ctx, tg.Tmux, []byte(text),
		"load-buffer", "-b", buf, "-"); err != nil {
		return res, err
	} else if r.RC != 0 {
		res.Reason = "load-buffer: " + r.Stderr
		return res, nil
	}
	defer func() {
		// Its own invocation, regardless of what happened: a batch that aborts
		// skips its tail, which is how a payload became the user's most recent
		// paste buffer.
		_, _ = s.run.RunInput(context.Background(), tg.Tmux, nil, "delete-buffer", "-b", buf)
	}()

	// Both the `if` and every sub-command carry their own -t. Measured separately:
	// without the sub-command's, a crossed pair delivered to the unstamped pane and
	// confirmed OK for it; without the `if`'s, the guard read the option from the
	// server's CURRENT pane and pasted into an unstamped one, printing OK %1.
	//
	// No literal % appears in any template: display -p runs through strftime, so
	// identity is emitted as #{pane_id} and the token as #{@hub_<instance>}.
	//
	// The BASELINE capture is inside the chain, between the two markers, and that
	// placement is the whole point: a screen captured in an earlier invocation could
	// have gained the text in between, and one captured after the paste cannot say
	// what was already there. Both markers carry the token, so a pane whose own
	// content looks like a marker cannot shift the parse.
	opt := s.inst.Option()
	then := strings.Join([]string{
		"display -p -t " + tg.PaneID + " 'BEFORE #{pane_id} #{window_activity} #{" + opt + "}'",
		"capture-pane -p -t " + tg.PaneID,
		"display -p -t " + tg.PaneID + " 'AFTER #{pane_id} #{" + opt + "}'",
		"paste-buffer -d -p -r -b " + buf + " -t " + tg.PaneID,
		"display -p -t " + tg.PaneID + " 'SENT #{pane_id} #{" + opt + "}'",
	}, " ; ")
	els := "display -p -t " + tg.PaneID + " 'REFUSED #{pane_id}'"

	out, err := s.run.RunInput(ctx, tg.Tmux, nil,
		"if", "-F", "-t", tg.PaneID,
		"#{==:#{"+opt+"},"+tok+"}", then, els)
	if err != nil {
		return res, err
	}

	g := parseGuardOutput(out.Stdout, tok)
	if reason, ok := g.confirms(tg.PaneID, tok, out.Stderr); !ok {
		res.Reason = reason
		return res, nil
	}

	res.Outcome = Unwitnessed
	res.ActivityBefore = g.before
	res.EchoBefore, _ = echoCount(g.baseline, text)
	res.Reason = "awaiting the witness"
	return res, nil
}

// Witness decides whether anything actually arrived, from two observables read in
// ONE invocation:
//
//   - the pane's tail now contains a prefix of what was sent;
//   - window_activity advanced past what the guard invocation recorded.
//
// The SCREEN is checked first, and the order is measured rather than aesthetic. On
// six back-to-back sends against tmux 3.2a the screen confirmed **6 of 6** while
// activity confirmed **2 of 6**: window_activity has one-second resolution, and a
// broadcast writes to several panes inside one second, so the send and the pane's
// previous output land in the same tick of that clock. Activity is therefore the
// SECONDARY observable — it earns its place only for a pane that redraws without
// showing the text, such as a password prompt.
//
// A pane that satisfies neither is Unwitnessed, which is a real answer and not a
// failure: a prompt that echoes nothing looks exactly like this.
func (s *Sender) Witness(ctx context.Context, r Result, text string) Result {
	if r.Outcome == Refused {
		return r
	}
	select {
	case <-ctx.Done():
		return r
	case <-time.After(WitnessDelay):
	}

	// One invocation for both. Measured on 3.2a, the ACT line comes first 6 times
	// out of 6, so the split is reliable — but it is keyed on the marker rather
	// than on the position, because an ordering that happens to hold is not one to
	// depend on.
	res, err := s.run.RunInput(ctx, r.Target.Tmux, nil,
		"display", "-p", "-t", r.Target.PaneID, "ACT #{window_activity}",
		";", "capture-pane", "-p", "-t", r.Target.PaneID)
	if err != nil || res.RC != 0 {
		r.Outcome = Unwitnessed
		r.Reason = "the witness read did not come back: " + firstLine(res.Stderr)
		return r
	}
	act, screen := splitWitness(res.Stdout)

	// MORE of the payload's prefix than before, not merely SOME: the same prompt
	// sent twice leaves the first copy on the screen, and a paste that delivered
	// nothing would then read as delivered on the strength of the previous one.
	if n, usable := echoCount(screen, text); usable && n > r.EchoBefore {
		r.Outcome, r.Reason = Delivered, "the text is on the pane"
		return r
	}
	if act > r.ActivityBefore {
		r.Outcome, r.Reason = Delivered, "activity advanced"
		return r
	}
	r.Outcome = Unwitnessed
	r.Reason = "the pane does not show the text and produced no new output"
	return r
}

// splitWitness separates the activity line from the captured screen. It finds the
// marker rather than trusting the first line, so a pane whose own content begins
// with something ACT-shaped cannot shift the parse.
func splitWitness(out string) (int64, string) {
	lines := strings.Split(out, "\n")
	for i, l := range lines {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(l), "ACT "); ok {
			act, _ := strconv.ParseInt(strings.TrimSpace(rest), 10, 64)
			return act, strings.Join(append(lines[:i:i], lines[i+1:]...), "\n")
		}
	}
	return 0, out
}

// echoPrefixRunes is how much of the payload the screen check looks for. Short
// enough to survive wrapping at any width a tile supports, long enough that finding
// it is evidence rather than coincidence.
const echoPrefixRunes = 24

// echoCount counts how many times the first meaningful run of the sent text appears
// on a screen. A whole-text match would fail on any pane that wraps or reformats —
// Claude Code renders a prompt inside its own input box — so the check is a prefix.
//
// It returns a COUNT, and usable=false when the prefix is too short to be evidence
// of anything. The slice is by RUNE: a byte slice can split a multi-byte character,
// and the composer is rune-disciplined for exactly that reason.
func echoCount(screen, text string) (int, bool) {
	line := strings.TrimSpace(strings.SplitN(text, "\n", 2)[0])
	rs := []rune(line)
	if len(rs) > echoPrefixRunes {
		rs = rs[:echoPrefixRunes]
		line = string(rs)
	}
	if len(rs) < 4 {
		return 0, false
	}
	return strings.Count(screen, line), true
}

// guardEcho is what the guard invocation said, including the pane's screen as it was
// immediately before the paste.
type guardEcho struct {
	before   int64
	paneID   string
	token    string
	refused  bool
	baseline string
}

// confirms decides whether the echo proves the write reached the intended pane, and
// names the failure otherwise. It is one function because three call sites need the
// same four checks in the same order.
func (g guardEcho) confirms(wantPane, wantTok, stderr string) (string, bool) {
	switch {
	case g.refused:
		return "the guard refused: the pane is no longer the one that was selected", false
	case g.paneID == "":
		return "no confirmation came back: " + firstLine(stderr), false
	case g.paneID != wantPane:
		// Corroboration: makes the failure message name the pane. The token check
		// below is load-bearing; this one costs nothing and improves diagnostics.
		return fmt.Sprintf("confirmation named %s, not %s", g.paneID, wantPane), false
	case g.token != wantTok:
		// Load-bearing: a wrong pane cannot produce a matching random token, so
		// this is what makes the guard sound. The pane ID check above is
		// corroboration.
		return "confirmation carried a different token", false
	}
	return "", true
}

// parseGuardOutput reads the confirmation lines, and the baseline screen that sits
// between two of them.
//
// The BEFORE and AFTER markers carry the token, and the baseline is the lines
// between them. That is what makes the parse safe against arbitrary pane content: a
// screen can hold a line that looks like a marker, but not one carrying an
// unguessable token — and SENT/REFUSED are read only OUTSIDE the baseline block, so
// nothing a pane displays can forge either.
//
// It asserts nothing itself; the caller compares the echoed id and token against
// what it intended.
func parseGuardOutput(stdout, tok string) guardEcho {
	rows := strings.Split(stdout, "\n")
	out := guardEcho{}
	start, end := -1, -1
	for i, l := range rows {
		f := strings.Fields(strings.TrimSpace(l))
		switch {
		case start < 0 && len(f) == 4 && f[0] == "BEFORE" && f[3] == tok:
			out.before, _ = strconv.ParseInt(f[2], 10, 64)
			start = i
		case start >= 0 && end < 0 && len(f) == 3 && f[0] == "AFTER" && f[2] == tok:
			end = i
		}
	}
	if start >= 0 && end > start {
		out.baseline = strings.Join(rows[start+1:end], "\n")
	}
	for i, l := range rows {
		if start >= 0 && end > start && i > start && i < end {
			continue // inside the captured screen, which is the pane's own text
		}
		f := strings.Fields(strings.TrimSpace(l))
		switch {
		case len(f) >= 3 && f[0] == "SENT":
			out.paneID, out.token = f[1], f[2]
		case len(f) >= 1 && f[0] == "REFUSED":
			out.refused = true
		}
	}
	return out
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// keystroke sends one key to one pane, guarded exactly like a paste and confirmed
// the same way.
//
// The confirmation tail is not decoration. `if -F` exits 0 when its condition FAILS
// as well as when it holds, so a guard REFUSAL used to come back as rc=0 with a nil
// error — and an interrupt the guard blocked was reported as delivered, which tells
// the operator an agent was stopped when it is still running.
//
// There is no witness and there cannot be a cheap one: a keypress leaves no text on
// the screen to look for, so `delivered` here means "the key went to the pane the hub
// still identifies", which is the part that could otherwise hurt someone else.
func (s *Sender) keystroke(ctx context.Context, tg Target, key, what string) Result {
	res := Result{Target: tg, Outcome: Refused}
	tok, ok := s.st.Token(tg.Host, tg.PaneID)
	if !ok || tok == "" {
		res.Reason = "the hub holds no identity token for this pane"
		return res
	}
	res.Token = tok

	opt := s.inst.Option()
	// Both the `if` and the sub-commands carry their own -t, for the two measured
	// reasons in Send. Between the paste and the Enter the pane could have been
	// replaced, and an Enter into the wrong pane executes whatever is sitting at
	// that prompt.
	then := "send-keys -t " + tg.PaneID + " " + key +
		" ; display -p -t " + tg.PaneID + " 'SENT #{pane_id} #{" + opt + "}'"
	out, err := s.run.RunInput(ctx, tg.Tmux, nil,
		"if", "-F", "-t", tg.PaneID, "#{==:#{"+opt+"},"+tok+"}", then,
		"display -p -t "+tg.PaneID+" 'REFUSED #{pane_id}'")
	if err != nil {
		res.Reason = err.Error()
		return res
	}
	if reason, ok := parseGuardOutput(out.Stdout, tok).confirms(tg.PaneID, tok, out.Stderr); !ok {
		res.Reason = reason
		return res
	}
	res.Outcome = Delivered
	res.Reason = what + " reached the pane the hub identified"
	return res
}

// Submit presses Enter, always as its own invocation and never as part of the
// payload. A newline inside the text is what made send-keys execute paragraph one.
//
// It returns a Result rather than an error for the same reason Send does: rc=0 is
// not evidence, so "no error" cannot mean "submitted".
func (s *Sender) Submit(ctx context.Context, tg Target) Result {
	return s.keystroke(ctx, tg, "Enter", "Enter")
}

// Interrupt sends a control key. It is a separate hotkey rather than text because
// C-c and Escape are not expressible as a payload, and it is guarded for the same
// reason Submit is.
func (s *Sender) Interrupt(ctx context.Context, tg Target, keyName string) Result {
	switch keyName {
	case "C-c", "Escape":
	default:
		return Result{Target: tg, Outcome: Refused,
			Reason: fmt.Sprintf("%q is not an interrupt key", keyName)}
	}
	return s.keystroke(ctx, tg, keyName, keyName)
}
