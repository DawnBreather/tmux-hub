package ui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/DawnBreather/tmux-hub/internal/tmux"
)

// The exact string, not a property. The payload is the whole fix — a pane on dev-air could not find
// `claude` because it inherits the ssh CLIENT's non-login PATH — so the assertion is what lands on
// the far machine, character for character. Measured on dev-air (nu login shell) and nuc (POSIX):
// `claude` reaches its prompt, and a missing tool prints the shell's own line plus this sentence and
// holds the pane with `#{pane_dead}` still 0.
func TestLoginPayloadRunsTheArgvUnderALoginShellAndHoldsAFailure(t *testing.T) {
	got, err := LoginPayload([]string{"claude", "--session-id", "3f2a1b09-dead-4beef-8888-999999999999"})
	if err != nil {
		t.Fatalf("LoginPayload refused a plain argv: %v", err)
	}
	want := `sh -lc "claude --session-id 3f2a1b09-dead-4beef-8888-999999999999 || { echo; ` +
		`echo tmux-hub: the command above failed — press enter to close this pane; read x; }"`
	if got != want {
		t.Errorf("payload\n got %q\nwant %q", got, want)
	}
}

// The property the transport needs, asserted over every argv shape the three callers build.
//
// A `'` anywhere in the payload becomes close-quote, backslash-quote, reopen-quote when `internal/tmux` quotes it for ssh, and that is a
// nushell PARSE ERROR at rc=0 — a create the hub reads as successful with no pane behind it. `$` and
// backtick are the same class one shell along.
func TestLoginPayloadCarriesNothingANonPosixLoginShellCannotParse(t *testing.T) {
	argvs := [][]string{
		{"claude", "--session-id", "3f2a1b09-dead-4bee-8888-999999999999", "--model", "sonnet"},
		{"claude", "--session-id", "x", "--permission-mode", "bypassPermissions"},
		{"claude", "attach", "3f2a1b09"},
		{"claude", "--resume", "3f2a1b09-dead-4bee-8888-999999999999"},
	}
	for _, argv := range argvs {
		got, err := LoginPayload(argv)
		if err != nil {
			t.Fatalf("LoginPayload(%q) refused a plain argv: %v", argv, err)
		}
		for _, bad := range []string{`'`, `$`, "`", `\`} {
			if strings.Contains(got, bad) {
				t.Errorf("payload for %q holds %q, which a far login shell may reinterpret: %q",
					argv, bad, got)
			}
		}
	}
}

// Calibrated on BOTH poles, which is the only reason this file can claim the nushell defect is
// closed: the shape the hub USED to send does produce close-quote, backslash-quote, reopen-quote through the real transport, and the new
// one does not. Without the first half this test passes against any string at all.
func TestTheTransportNeverEscapesAQuoteInsideALoginPayload(t *testing.T) {
	create := func(payload string) string {
		return tmux.ShellJoinCommand("tmux", []string{
			"new-session", "-d", "-s", "demo", "-c", "/tmp", payload,
		})
	}

	// The pole: §20's wrapper, which is right for the LOCAL window path and unparseable by nu.
	old := create(WindowPayload([]string{"claude", "attach", "3f2a1b09"}))
	if !strings.Contains(old, `'\''`) {
		t.Fatalf("this test cannot fail: the old payload no longer escapes a quote either: %q", old)
	}

	payload, err := LoginPayload([]string{"claude", "attach", "3f2a1b09"})
	if err != nil {
		t.Fatalf("LoginPayload refused a plain argv: %v", err)
	}
	if line := create(payload); strings.Contains(line, `'\''`) {
		t.Errorf("the transport escaped a quote, which nushell reads as the end of the string: %q", line)
	}
}

// The guard, per refused character, and it must NAME the argument — a refusal that says only
// "invalid command" leaves the operator with a form they cannot get past.
func TestLoginPayloadRefusesAnArgumentAShellWouldReinterpret(t *testing.T) {
	cases := []struct {
		name string
		argv []string
	}{
		{"a quote, which POSIX quoting turns into '\\'' and nu refuses", []string{"claude", "it's"}},
		{"a dollar, which every shell expands", []string{"claude", "$HOME"}},
		{"a backtick", []string{"claude", "`id`"}},
		{"a backslash", []string{"claude", `a\b`}},
		{"a double quote, which would close the payload's own", []string{"claude", `say"x`}},
		{"a space, which would split one argument into two", []string{"claude", "two words"}},
		{"a semicolon, which would start a second command", []string{"claude", "a;b"}},
		{"a percent, which the tmux seam refuses outside a -t value", []string{"claude", "50%"}},
		{"an empty word, which would vanish in the join", []string{"claude", ""}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := LoginPayload(c.argv)
			if err == nil {
				t.Fatalf("LoginPayload accepted %q and produced %q", c.argv, got)
			}
			// The argument as the refusal renders it, so a backslash or a quote — the two the
			// operator is least able to guess at — are compared in the form they are printed in.
			if arg := c.argv[len(c.argv)-1]; arg != "" &&
				!strings.Contains(err.Error(), fmt.Sprintf("%q", arg)) {
				t.Errorf("the refusal does not quote the offending argument: %v", err)
			}
			if !strings.Contains(err.Error(), "argument 2") {
				t.Errorf("the refusal does not say WHICH argument: %v", err)
			}
		})
	}
	if _, err := LoginPayload(nil); err == nil {
		t.Error("LoginPayload accepted an empty argv, so a pane would be created running nothing")
	}
}
