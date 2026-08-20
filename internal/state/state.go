// Package state turns a pane's captured tail into one attention state.
//
// Classify is pure, and it is the only heuristic part of tmux-hub: Claude
// Code's screen is not an API. A wrong classification mis-sorts the inbox and
// can never cause a send, because sends require an explicit selection.
package state

import (
	"regexp"
	"strings"
	"time"

	"github.com/DawnBreather/tmux-hub/internal/lines"
)

type State int

// The order is the inbox order, and it is the answer to one question: how much
// does this pane want me? Error sat below Works in an earlier draft, which read
// wrong on a screen full of real states — a failed pane is something to act on
// and a busy one is not.
const (
	Needs   State = iota // waiting on the user
	Error                // failed, or the pane is dead. The dead half is reachable only where remain-on-exit is on, which the hub sets on its own windows and never globally. An agent that fails to start does not necessarily die: `claude --resume <nonexistent>` draws a session picker and waits with pane_dead=0, so the dead-pane path covers a narrower set of failures than its name suggests, and a failed launch is more often a live pane in an unexpected mode.
	Quiet                // silent for longer than QuietAfter: hung, or finished quietly
	Idle                 // prompt present, nothing running — ready for the next thing
	Works                // actively producing output; nothing to do
	Unknown              // a source reported the session but not its state
	Done                 // finished. Reachable only from an agent listing, never from Classify: it is a fact a pane cannot show, since a background job that ended leaves no pane and an interactive one that ended leaves a shell. It sits BELOW Unknown deliberately — a row whose state could not be read wants a look, and a finished one wants nothing — and above Gone, because its output is still there to read. Before it existed, `done` folded onto Idle, so a job that had ENDED carried the word §6's state table pairs with the signal "prompt present, no live work marker" — §14 glosses that intent as "prompt present, ready for the next thing" — and shared a rank with a session waiting for input.
	Gone                 // the pane vanished between ticks
)

// QuietAfter is how long a pane must be silent before it is suspicious.
//
// 180 s is grounded rather than guessed. Over 210 491 inter-event gaps from 59 450
// real Claude Code transcripts, split on the assistant's own `stop_reason`, silence
// while work is in progress has a p99 of **90.9 s** — so the old 90 s sat exactly
// at the working tail and read 1.03% of working moments as quiet. 180 s clears that
// p99 by 2x and takes the rate to 0.30%. Both figures are an UPPER BOUND on screen
// silence, because Claude renders a spinner continuously while a tool is pending,
// so window_activity advances far more often than a transcript entry appears.
//
// It also matters for the inbox: after QuietAfter every long-lived pane becomes
// `quiet`, which sorts above `idle`, so a threshold at the working tail fills the
// inbox with panes that are merely old (docs/design.md §14).
const QuietAfter = 180 * time.Second

// FreshForFloor is the smallest freshness window. window_activity has one-second
// resolution, so anything under about two seconds cannot distinguish "wrote just
// now" from "wrote during the previous tick".
const FreshForFloor = 4 * time.Second

// FreshFor is how recently a pane must have produced output to count as working.
// It scales with how often the host is ACTUALLY polled, because a constant is
// wrong in the direction that matters: at a measured 2.1 s cycle against a fixed
// 4 s window a plainly busy pane read `idle`, and idle-versus-works is the
// distinction the inbox is built on. Two intervals gives a tick room to be late
// without the pane flickering.
func FreshFor(pollInterval time.Duration) time.Duration {
	if w := 2 * pollInterval; w > FreshForFloor {
		return w
	}
	return FreshForFloor
}

func (s State) String() string {
	switch s {
	case Needs:
		return "needs"
	case Quiet:
		return "quiet"
	case Idle:
		return "idle"
	case Works:
		return "works"
	case Error:
		return "error"
	case Unknown:
		return "unknown"
	case Done:
		return "done"
	default:
		return "gone"
	}
}

// Glyph is the inbox marker.
func (s State) Glyph() string {
	switch s {
	case Needs:
		return "⚑"
	case Quiet:
		return "✱"
	case Idle:
		return "▸"
	case Works:
		return "·"
	case Error:
		return "✗"
	case Unknown:
		return "?"
	case Done:
		// One column, like every other glyph in this table (measured with lines.Width).
		return "✓"
	default:
		return "✝"
	}
}

// FromWord maps another source's own word for a state onto ours. An unrecognised
// or missing word becomes Unknown rather than a guess: a Claude Code version that
// reports neither `state` nor `status` is a measured case (docs/design.md §17),
// and calling that `idle` would be a lie about a session that might be waiting.
func FromWord(w string) State {
	switch w {
	case "needs":
		return Needs
	case "error":
		return Error
	case "quiet":
		return Quiet
	case "idle":
		return Idle
	case "works":
		return Works
	case "done":
		// A word of its own rather than a fold onto idle. The two mean opposite things to
		// an operator: idle is a live session with a prompt, done is a job that ended.
		return Done
	default:
		return Unknown
	}
}

// Rank is the inbox sort order: needs first, gone last.
func (s State) Rank() int { return int(s) }

// Input is everything Classify may look at.
type Input struct {
	Zone        []string // ZoneLines rows ending at the pane's cursor row
	ActivityAge time.Duration
	// PollInterval is how long it actually took to come back to this host. Zero
	// means "unknown", which falls back to the floor.
	PollInterval time.Duration
	Dead         bool
	Alt          bool
	Command      string
}

var (
	// A live "working" hint. It is corroboration, never the primary signal:
	// Claude Code composes this phrase at render time from a keybinding chord
	// (the literal "esc to interrupt" does not exist anywhere in the 2.1.227
	// bundle, while "Do you want to proceed?" and the "bypass permissions"
	// indicator do), so it can change without notice. The primary signal for
	// works is ActivityAge, which is structural and version-independent.
	// The spinner glyph and "Churned for Ns" persist in scrollback and must
	// never be used for this.
	workingRe = regexp.MustCompile(`esc to interrupt|to interrupt`)
	// A question or a choice awaiting an answer.
	//
	// The load-bearing pattern is the SHAPE, not the words: an interactive chooser
	// renders a selection cursor on a numbered option, and that survives rewording.
	// The earlier `^\s*1\.\s` required the digit at line start and therefore missed
	// every real prompt — measured on Claude Code's own trust dialog, whose zone
	// ends `❯ 1. Yes, I trust this folder`, so the most urgent state went
	// undetected. `Do you want to proceed?` and `Yes, I trust this folder` are real
	// literals in the bundle and are kept as corroboration; the composed footer
	// ("Enter to confirm · Esc to cancel") is not a literal and is not relied on.
	// Claude renders at least TWO dialog shapes, both measured on live startup
	// dialogs: a numbered choice (`❯ 1. Yes, I trust this folder`) and a
	// multi-select checkbox list (`❯ [✔] context7`, footer "Space to select ·
	// Enter to confirm"). Matching only the numbered one missed the MCP-approval
	// dialog entirely, which is a session waiting on the user before it has even
	// started.
	needsRe = regexp.MustCompile(`(?im)(^\s*[❯>]\s*\d+\.\s+\S|^\s*[❯>]\s*\[[ xX✔✓×]\]|space to select|do you want to proceed|\bi trust this folder\b|\bwould you like\b|\[y/n\]|\(y/n\)|press enter to (confirm|continue))`)
	// A credential prompt is a session waiting on a human as much as a question is.
	// The rule keys on a closed set of words rather than on a trailing colon,
	// because "ordinary output ending in a colon" is most output — measured, a bare
	// `Password:` read as idle and a colon rule would have caught half the screen.
	// A credential keyword ANYWHERE in a line that ends with a colon. Both halves
	// matter: ssh's real prompt is `Enter passphrase for key '…':`, so the keyword
	// is not at the end, and "any line ending in a colon" is most output.
	secretRe = regexp.MustCompile(`(?i)\b(password|passphrase|pin|one-?time code|verification code|2fa|otp|token)\b.*:\s*$`)
	errorRe  = regexp.MustCompile(`(?i)(^|\s)(FAIL|FAILED|ERROR|Traceback|panic:|fatal:)(\s|:|$)`)
)

// asksAQuestion reports whether the zone's LAST non-blank line ends in a question
// mark. Position is what makes this precise rather than loose: the line at the
// cursor is where a prompt lives, so a question there is one being asked, while
// the same words earlier in the zone are just output. Measured need for it: a
// pane sitting on `Rebase onto main? Proceed?` read `idle`, and that is the
// commonest shape of a shell waiting on a person.
func asksAQuestion(zone []string) bool {
	for i := len(zone) - 1; i >= 0; i-- {
		l := strings.TrimRight(lines.Normalize(zone[i]), " \t")
		if l == "" {
			continue
		}
		return strings.HasSuffix(l, "?")
	}
	return false
}

// waitsForSecret reports whether the zone's last non-blank line is a credential
// prompt. Same positional discipline as asksAQuestion: at the cursor it is being
// asked, earlier in the zone it is just output.
func waitsForSecret(zone []string) bool {
	for i := len(zone) - 1; i >= 0; i-- {
		l := strings.TrimRight(lines.Normalize(zone[i]), " \t")
		if l == "" {
			continue
		}
		return secretRe.MatchString(l)
	}
	return false
}

// Classify maps one sample to a state. It works from a single sample so that a
// cold start has a defined answer: the markers come from the capture, and
// ActivityAge is an absolute age rather than a diff against a previous tick.
func Classify(in Input) State {
	if in.Dead {
		return Error
	}
	if in.Alt {
		// A full-screen app's rendering carries no chrome to strip, so only the
		// timestamp is meaningful.
		if in.ActivityAge > QuietAfter {
			return Quiet
		}
		return Works
	}

	bare := make([]string, 0, len(in.Zone))
	for _, l := range in.Zone {
		bare = append(bare, lines.Normalize(l))
	}
	joined := strings.Join(bare, "\n")

	// needs outranks works: a pane that is asking a question wants the user even
	// if it also printed something a moment ago. The question text is a real
	// literal in Claude Code's bundle, so this is the most reliable marker there
	// is — it goes first.
	if needsRe.MatchString(joined) || asksAQuestion(bare) || waitsForSecret(bare) {
		return Needs
	}
	// works comes from the DELTA first. Keying it only on rendered text made a
	// working pane read idle whenever the text was absent or reworded, and
	// idle-versus-works is the distinction the whole inbox rests on.
	if in.ActivityAge >= 0 && in.ActivityAge < FreshFor(in.PollInterval) {
		return Works
	}
	if workingRe.MatchString(joined) {
		return Works
	}
	if errorRe.MatchString(joined) {
		return Error
	}
	if in.ActivityAge > QuietAfter {
		return Quiet
	}
	return Idle
}
