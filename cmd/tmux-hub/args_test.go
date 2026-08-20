package main

import (
	"reflect"
	"testing"
)

// Go's flag package stops parsing at the first NON-FLAG argument. So with the
// subcommand consumed after flag.Parse(), `tmux-hub status --host smoke=/path`
// left every flag after `status` unparsed — measured against the real binary,
// which then polled the LOCAL server and reported it under the label `local`
// while the user had asked about another host. The only tell was that label, in
// JSON nobody reads closely. Consuming the subcommand BEFORE parsing is what
// makes both orders mean the same thing.
func TestSplitSubcommandConsumesStatusSoLaterFlagsStillParse(t *testing.T) {
	rest, status := splitSubcommand([]string{"status", "--no-local", "--host", "smoke=/tmp/s"})
	if !status {
		t.Error("status subcommand not recognised")
	}
	want := []string{"--no-local", "--host", "smoke=/tmp/s"}
	if !reflect.DeepEqual(rest, want) {
		t.Errorf("rest = %q, want %q — flags after the subcommand must still reach flag.Parse", rest, want)
	}
}

func TestSplitSubcommandLeavesFlagsBeforeItAlone(t *testing.T) {
	rest, status := splitSubcommand([]string{"--no-local", "status"})
	if !status {
		t.Error("a trailing status subcommand must still be recognised")
	}
	if want := []string{"--no-local"}; !reflect.DeepEqual(rest, want) {
		t.Errorf("rest = %q, want %q", rest, want)
	}
}

func TestSplitSubcommandIsANoOpWithoutIt(t *testing.T) {
	in := []string{"--host", "a=/b"}
	rest, status := splitSubcommand(in)
	if status {
		t.Error("status must not be set when the subcommand is absent")
	}
	if !reflect.DeepEqual(rest, in) {
		t.Errorf("rest = %q, want %q unchanged", rest, in)
	}
}

// A stray positional is refused rather than ignored: `tmux-hub statuss` should
// say so, not start the TUI as though nothing were typed.
func TestSplitSubcommandKeepsAnUnknownPositionalForTheCallerToRefuse(t *testing.T) {
	rest, status := splitSubcommand([]string{"statuss"})
	if status {
		t.Error("a typo must not be read as the status subcommand")
	}
	if want := []string{"statuss"}; !reflect.DeepEqual(rest, want) {
		t.Errorf("rest = %q, want %q so the caller can refuse it", rest, want)
	}
}
