package tmux

import (
	"context"
	"strings"
)

// The operator's alias, published to the tmux server so the session says its own name from INSIDE
// (docs/design.md §21.16). Everything in this file is a measured fact about tmux rather than a choice:
// each constant was taken on BOTH versions of this fleet — local 3.7b and nuc 3.2a — and two of them
// are not what the manual suggests.
const (
	// AliasOption is where the alias lives. A user option is the right surface because it does not
	// COMPETE with anything: `rename-session` would destroy the original name, which is the half the
	// operator asked to keep in parentheses, and it would need remembered state to undo; a window
	// name is fought over by tmux's own automatic-rename, which this repo has already paid for (the
	// hide key had to move off window names, known-issues H1).
	AliasOption = "@hub_alias"

	// AliasStatusLeft composes `alias (original)` and falls back to the plain session name.
	//
	// The conditional is load-bearing twice. It lets the hub write this format ONCE per session
	// rather than rewriting it as names come and go, and it makes UN-naming free: unsetting
	// AliasOption drops back to `[session]` with no second write, so there is no state to keep in
	// step. Measured on both versions: `[billing-cicd (probe)]` with the option set, `[probe]`
	// after `set -u`.
	//
	// The alias is inserted LITERALLY — measured against a real attached client, an alias containing
	// `#{session_name}`, `#H` or `%` draws as itself — so operator text cannot be read as a format
	// and needs no escaping.
	AliasStatusLeft = "[#{?" + AliasOption + "," + aliasRef + " (#{session_name}),#{session_name}}] "

	// aliasRef is AliasOption as a format REFERENCE, so the format above reads as the string tmux
	// receives rather than as concatenation.
	aliasRef = "#{" + AliasOption + "}"

	// DefaultStatusLeft is tmux's OWN default, and the trailing space is the point: measured
	// identical on 3.2a and 3.7b as `[#{session_name}] `. Comparing against the same string without
	// it finds no match on a default configuration, so the guard below would conclude the operator
	// had customised their status line and the feature would do nothing at all, silently — the worst
	// direction for a guard to err.
	DefaultStatusLeft = "[#{session_name}] "

	// StatusLeftLengthOption is the other half of drawing anything. tmux defaults it to TEN
	// columns on both versions, which a real client confirmed by drawing `[billing-c` and stopping,
	// so a composition without it is a feature that looks broken rather than one that looks off.
	StatusLeftLengthOption = "status-left-length"

	// StatusLeftOption is the option the composition goes in.
	StatusLeftOption = "status-left"

	// maxStatusLeft caps the room the hub will ask for. status-left takes its columns from the
	// window list beside it, so an absurd name must not push the windows off a narrow terminal.
	maxStatusLeft = 120
)

// StatusLeftIsOurs reports whether the hub may write this session's status line.
//
// True for tmux's own default, for an empty value (which is what a SESSION-level read returns before
// anyone has written one), and for the hub's own format — so a second poll writes nothing. False for
// anything else, including a format that already mentions the alias itself: an operator who has
// wired it up has made a decision, and overwriting it would be the hub reaching into their
// configuration rather than filling in a default.
func StatusLeftIsOurs(current string) bool {
	switch strings.TrimSpace(current) {
	case "", strings.TrimSpace(DefaultStatusLeft), strings.TrimSpace(AliasStatusLeft):
		return true
	}
	return false
}

// AliasStatusLeftLength is how many columns the composition needs, capped.
func AliasStatusLeftLength(alias, session string) int {
	n := len("[" + alias + " (" + session + ")] ")
	if n > maxStatusLeft {
		return maxStatusLeft
	}
	return n
}

// SessionOption is one option to set on one session, or to UNSET when Unset is true.
//
// Session is a tmux session ID (`$3`) and not a name: a name changes under the operator — the door
// renames, and `N` is exactly the gesture this feature exists for — while the id does not, and the
// seam in run.go requires the `$` sigil so a missing one cannot silently address a session NAMED 3.
type SessionOption struct {
	Session string
	Option  string
	Value   string
	Unset   bool
}

// SetSessionOptions applies every write in ONE tmux invocation, and runs tmux not at all when there
// is nothing to write.
//
// Both halves matter for a call on every poll: the steady state is zero commands, and a poll that
// named three sessions pays one round trip rather than three. Measured on both versions: `set …
// ';' set …` in one argv applies both.
func SetSessionOptions(ctx context.Context, r Runner, t Target, ws []SessionOption) error {
	if len(ws) == 0 {
		return nil
	}
	var args []string
	for _, w := range ws {
		if len(args) > 0 {
			args = append(args, ";")
		}
		args = append(args, "set")
		if w.Unset {
			args = append(args, "-u")
		}
		args = append(args, "-t", w.Session, w.Option)
		if !w.Unset {
			args = append(args, w.Value)
		}
	}
	_, err := r.Run(ctx, t, args...)
	return err
}

// ValidOptionValue reports whether a value may travel to tmux as an OPTION VALUE, and it answers by
// asking the seam itself rather than restating its rule — a second copy of "which characters are
// refused" would drift from Validate the first time Validate changed.
//
// It exists because operator text reaches this path. `Validate` refuses a literal `%` anywhere but a
// `-t` value, for a reason that is about `display -p` (whose argument goes through strftime, so
// `%2` silently becomes an empty string) and not about option values, which tmux inserts verbatim —
// measured, an alias of `50% off` draws as `50% off`. The seam stays strict all the same, because
// the failure it prevents is a SILENT one; the caller is what has to know that one unpublishable
// name must not take a whole batch down with it.
func ValidOptionValue(v string) error {
	return Validate([]string{"set", "-t", "$0", "@hub_probe", v})
}
