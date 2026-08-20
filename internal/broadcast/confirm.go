package broadcast

// TargetState is everything the confirmation rule looks at. Every field comes
// from the two poll snapshots the registry already holds, so asking the question
// costs no extra tmux read.
type TargetState struct {
	Host   string
	PaneID string

	IdentifiedNow         bool
	IdentifiedAtSelection bool

	SessionAtSelection string
	SessionNow         string
	WindowAtSelection  string
	WindowNow          string

	EpochAtSelection string
	EpochNow         string

	LastOutcome Outcome
	FromHistory bool

	// Bracketed is #{bracket_paste_flag} for the target, straight off the delta.
	// Measured: `less` reports 0 and turned a pasted three-line prompt into
	// KEYSTROKES, opening its help screen — a payload containing `q` would have
	// quit it and `!cmd` would have run a shell command. bash, vim and the python
	// REPL report 1 and took the same payload as inert text.
	Bracketed bool
}

// Reason is why the hub is asking, in words the user can act on.
type Reason string

const (
	ReasonMultiple         Reason = "more than one target is selected"
	ReasonUnidentified     Reason = "this pane cannot be identified as an agent"
	ReasonAgentGone        Reason = "the agent that was here has exited — this pane is now a shell"
	ReasonMoved            Reason = "this pane changed session or window since you selected it"
	ReasonEpochChanged     Reason = "the tmux server restarted, so pane ids may name different panes"
	ReasonLastUnwitnessed  Reason = "the previous send to this pane was never witnessed arriving"
	ReasonFromHistory      Reason = "this came from the history view rather than the input box"
	ReasonNoBracketedPaste Reason = "this pane does not accept pasted text — it will read the prompt as keypresses"
	ReasonAgentRunning     Reason = "this pane is running an identified agent"
	ReasonPaneDead         Reason = "this pane is dead (its command has exited)"
)

// Needed returns the reasons to confirm, empty when the send may go straight out.
//
// It is a disjunction rather than a target count, because "> 1 target" is neither
// necessary nor sufficient: every dangerous SINGLE-target send is one where
// something changed since selection, and the count rule fires on the safe common
// case of two freshly identified agents. A fresh single target sends immediately,
// so the common case does not pay for the rare one — and "fresh" is now checked
// rather than assumed.
//
// Not disableable. The one clause a user would want to switch off is the one that
// catches the exited-agent case, where the pane is now a shell and the prompt
// becomes a command line.
func Needed(ts []TargetState) []Reason {
	var out []Reason
	seen := map[Reason]bool{}
	add := func(r Reason) {
		if !seen[r] {
			seen[r] = true
			out = append(out, r)
		}
	}

	if len(ts) > 1 {
		add(ReasonMultiple)
	}
	for _, s := range ts {
		switch {
		case s.IdentifiedAtSelection && !s.IdentifiedNow:
			// Reported specifically rather than as "unidentified": this is the
			// case where a prompt lands at a shell prompt, and the user needs to
			// know it USED to be an agent.
			add(ReasonAgentGone)
		case !s.IdentifiedNow:
			add(ReasonUnidentified)
		}
		if s.SessionNow != s.SessionAtSelection || s.WindowNow != s.WindowAtSelection {
			add(ReasonMoved)
		}
		if s.EpochNow != s.EpochAtSelection {
			add(ReasonEpochChanged)
		}
		if s.LastOutcome == Unwitnessed || s.LastOutcome == Refused {
			add(ReasonLastUnwitnessed)
		}
		if s.FromHistory {
			add(ReasonFromHistory)
		}
		// A 0 is a reason to ask, not a refusal: `cat` also reports 0 and merely
		// echoes, so refusing outright would block a legitimate target. A 1 is not a
		// guarantee either — vim in a modal swap-file dialog consumed the paste — so
		// this clause narrows the risk rather than removing it.
		if !s.Bracketed {
			add(ReasonNoBracketedPaste)
		}
	}
	return out
}

// KillReasons returns the confirmation reasons for killing a pane. Unlike send
// confirmations, kill ALWAYS confirms — killing is destructive and cannot be undone,
// so there is no "clean single target" fast path. The reason tells the user what
// they are about to destroy.
func KillReasons(identified, dead bool) []Reason {
	var out []Reason
	switch {
	case identified:
		out = append(out, ReasonAgentRunning)
	case dead:
		out = append(out, ReasonPaneDead)
	default:
		out = append(out, ReasonUnidentified)
	}
	return out
}
