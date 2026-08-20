package tmux

import (
	"context"
	"strings"
	"testing"
)

// Every constant here was measured on BOTH versions of this fleet — local 3.7b and nuc 3.2a — and
// the numbers differ from what reading the manual would suggest, which is why they are pinned.
func TestTheStatusLineFactsAreTheMeasuredOnes(t *testing.T) {
	// The composing format, and the `#{?…}` conditional is load-bearing in two directions: it shows
	// the plain session name when no alias is set, so the hub can write the format ONCE, and it
	// makes un-naming free — unsetting @hub_alias falls back with no second write.
	// Measured: `[billing-cicd (probe)]` with the option set, `[probe]` after `set -u`.
	if !strings.Contains(AliasStatusLeft, "#{?"+AliasOption+",") {
		t.Errorf("the format does not fall back when the alias is unset: %q", AliasStatusLeft)
	}
	if !strings.Contains(AliasStatusLeft, "#{session_name}") {
		t.Errorf("the format does not carry the ORIGINAL name, which is the half the operator "+
			"asked for in parentheses: %q", AliasStatusLeft)
	}
	// The format VERBATIM, because that is the string measured against a real attached client — it
	// drew `[billing-cicd (20260813--a-very-long-session-name-7ef2fe7e)]` — and a constant asserted
	// only by its shape is a constant nobody proved.
	const measured = "[#{?@hub_alias,#{@hub_alias} (#{session_name}),#{session_name}}] "
	if AliasStatusLeft != measured {
		t.Errorf("AliasStatusLeft = %q, want the measured %q", AliasStatusLeft, measured)
	}
	// tmux's own default, WITH the trailing space. Measured identical on 3.2a and 3.7b, and the
	// space is the whole reason this is a constant: comparing against `[#{session_name}]` finds no
	// match on a default config, so the guard would decide the operator had customised it and the
	// feature would do nothing, silently.
	if DefaultStatusLeft != "[#{session_name}] " {
		t.Errorf("DefaultStatusLeft = %q, want tmux's own default including the trailing space",
			DefaultStatusLeft)
	}
}

// The guard: the hub writes the status line only while it is tmux's own default or already the
// hub's, and NEVER over the operator's.
func TestTheStatusLineIsWrittenOnlyWhenItIsNotTheOperators(t *testing.T) {
	for _, c := range []struct {
		what    string
		current string
		mine    bool
	}{
		{"tmux's default", DefaultStatusLeft, true},
		{"tmux's default without the trailing space, in case a version drops it",
			strings.TrimSpace(DefaultStatusLeft), true},
		{"empty, which is what a session-level read returns before anyone writes", "", true},
		{"the hub's own format, so a second poll writes nothing", AliasStatusLeft, true},
		{"the operator's own", "#[fg=green]MY OWN #S", false},
		{"the operator's own, already mentioning the alias themselves",
			"#[fg=green]#{@hub_alias} #S", false},
	} {
		if got := StatusLeftIsOurs(c.current); got != c.mine {
			t.Errorf("StatusLeftIsOurs(%s) = %v, want %v", c.what, got, c.mine)
		}
	}
}

// A composed status line needs ROOM. tmux's `status-left-length` defaults to 10 on both versions —
// measured, and confirmed against a real attached client, which drew `[billing-c` and stopped. So
// the length is part of the write, not an afterthought: without it the feature looks broken.
func TestTheLengthIsWideEnoughForTheComposition(t *testing.T) {
	const alias = "billing-cicd"
	const session = "20260813--a-very-long-session-name-7ef2fe7e"
	got := AliasStatusLeftLength(alias, session)
	want := len("[" + alias + " (" + session + ")] ")
	if got < want {
		t.Errorf("AliasStatusLeftLength = %d, want at least %d — the drawn line is %q",
			got, want, "["+alias+" ("+session+")] ")
	}
	// And it is CAPPED, because status-left takes its room from the window list beside it: a
	// 200-column name must not push the windows off a narrow terminal.
	if wide := AliasStatusLeftLength(strings.Repeat("x", 300), session); wide > 120 {
		t.Errorf("an absurd alias asked for %d columns of status line", wide)
	}
}

// The writes are ONE tmux invocation per host, because a poll that named three sessions should not
// pay three round trips — measured: `set -t $1 … ';' set -t $2 …` works on both versions.
func TestSessionOptionsAreWrittenInOneInvocation(t *testing.T) {
	r := &fakeRunner{}
	err := SetSessionOptions(context.Background(), r, target(),
		[]SessionOption{
			{Session: "$1", Option: AliasOption, Value: "one"},
			{Session: "$2", Option: AliasOption, Unset: true},
		})
	if err != nil {
		t.Fatalf("SetSessionOptions: %v", err)
	}
	if r.calls != 1 {
		t.Fatalf("%d invocations for two writes, want 1: %q", r.calls, r.last)
	}
	joined := strings.Join(r.last, " ")
	for _, want := range []string{
		"set -t $1 " + AliasOption + " one",
		"; set -u -t $2 " + AliasOption,
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("argv is missing %q:\n%q", want, joined)
		}
	}
	// And it goes through Validate, so a wrong-shaped session target cannot reach tmux.
	if err := Validate(r.last); err != nil {
		t.Errorf("the argv this builds does not pass the seam: %v", err)
	}
}

// Nothing to write means no invocation at all: in the steady state a poll costs zero tmux commands,
// which is what makes it safe to run this on every tick.
func TestNoWritesMeansNoInvocation(t *testing.T) {
	r := &fakeRunner{}
	if err := SetSessionOptions(context.Background(), r, target(), nil); err != nil {
		t.Fatalf("SetSessionOptions: %v", err)
	}
	if r.calls != 0 {
		t.Errorf("an empty write list ran tmux %d times", r.calls)
	}
}
