package tmux

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// rec builds one wire record the way labelFormat asks tmux to: every value preceded
// by its own BYTE length. Building it here rather than hardcoding a string is what
// keeps these tests honest about the format — the fixture below is real tmux output,
// and this helper must produce the same bytes for it.
func rec(id string, vals ...string) string {
	out := id
	for _, v := range vals {
		out += "|" + strconv.Itoa(len(v)) + "|" + v
	}
	return out + "\n"
}

// A value may contain the delimiter, and now also a NEWLINE. The old reader split on
// lines and cut on the first `|`, so the pipe survived and the newline did not; the
// length prefix makes both a non-event.
func TestALabelValueMayContainTheDelimiterAndANewline(t *testing.T) {
	// The values are PADDED to the table's length rather than listed one per field, so this
	// fixture cannot go stale the way it did when three session options joined the table: a
	// hand-written record silently stops lining up, and the reader then reports a mis-frame on
	// a stream that is only short. The fields under test are the leading ones, in table order.
	pad := func(vals ...string) []string {
		for len(vals) < len(labelFormats) {
			vals = append(vals, "")
		}
		return vals
	}
	got, err := parseLabelRecords(
		rec("%0", pad("my|session", "w|x", "sleep", `sh -c "tail -f log | grep boom"`, "/a|b/c")...) +
			rec("%3", pad("plain", "win", "bash", "", "/tmp/пу|ть\nвторой")...))
	if err != nil {
		t.Fatalf("parseLabelRecords: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d records, want 2: %#v", len(got), got)
	}
	if got["%0"].Session != "my|session" {
		t.Errorf("%%0 Session = %q, want %q", got["%0"].Session, "my|session")
	}
	if want := `sh -c "tail -f log | grep boom"`; got["%0"].StartCommand != want {
		t.Errorf("%%0 StartCommand = %q, want %q", got["%0"].StartCommand, want)
	}
	// The record AFTER a newline-bearing value is the one a shifted frame destroys.
	if want := "/tmp/пу|ть\nвторой"; got["%3"].Path != want {
		t.Errorf("%%3 Path = %q, want %q", got["%3"].Path, want)
	}
	if got["%3"].Session != "plain" || got["%3"].Command != "bash" {
		t.Errorf("%%3 = %#v, want the fields of the second record", got["%3"])
	}
	// An empty value is normal: a pane started with no command reports
	// pane_start_command as "" (measured), which is length 0.
	if got["%3"].StartCommand != "" {
		t.Errorf("%%3 StartCommand = %q, want empty", got["%3"].StartCommand)
	}
}

// Every field in labelFormats must appear in the format string, with its length
// beside it. This is the floor the table exists for: a label added to the table and
// not asked for was a real defect — pane_start_command joined the table, nothing read
// the table, and registry.Pane.StartCommand was empty on every real poll while every
// unit test passed.
func TestTheFormatAsksForEveryFieldInTheTable(t *testing.T) {
	f := labelFormat()
	if !strings.HasPrefix(f, "#{pane_id}|") {
		t.Errorf("format does not lead with the pane id: %q", f)
	}
	for _, lf := range labelFormats {
		for _, want := range []string{"#{" + lf.field + "}", "#{n:" + lf.field + "}"} {
			if !strings.Contains(f, want) {
				t.Errorf("format is missing %s — a field in the table that is not asked "+
					"for reads as empty on every poll: %q", want, f)
			}
		}
	}
	// And the format must survive the invariant checker, which bans the two segfaulting
	// client_* variables and refuses a bare % anywhere but a -t value. `#{n:X}` has no
	// %, but asserting it beats assuming it.
	if err := Validate([]string{"list-panes", "-a", "-F", f}); err != nil {
		t.Errorf("Validate rejects the label format: %v", err)
	}
}

// A stream that does not line up must ERROR rather than hand back a plausible wrong
// value. That is the whole difference from the line-counting reader, which answered a
// shifted frame with a session name that was really a path.
func TestAMisframedStreamIsAnErrorAndNotData(t *testing.T) {
	for _, c := range []struct{ name, in string }{
		{"length runs past the record", "%0|999|one|3|win|5|sleep|0||5|/tmp/\n"},
		{"a length that is not a number", "%0|x|one|3|win|5|sleep|0||5|/tmp/\n"},
		{"no separator after a value", "%0|3|oneX3|win|5|sleep|0||5|/tmp/\n"},
		{"trailing bytes after the last value", "%0|3|one|3|win|5|sleep|0||4|/tmp/junk\n"},
		{"a record that does not start with a pane id", "0|3|one|3|win|5|sleep|0||4|/tmp\n"},
		{"a missing field", "%0|3|one|3|win\n"},
	} {
		if _, err := parseLabelRecords(c.in); err == nil {
			t.Errorf("%s: parsed without error, want a refusal", c.name)
		}
	}
}

func TestFetchLabelsWithHostileNames(t *testing.T) {
	tgt := testServer(t)
	for _, args := range [][]string{
		{"rename-session", "-t", "one", "a|b"},
		{"rename-window", "-t", "a|b", "w|x"},
	} {
		full := append([]string{"-S", tgt.Socket}, args...)
		if out, err := exec.Command("tmux", full...).CombinedOutput(); err != nil {
			t.Fatalf("%v: %v: %s", args, err, out)
		}
	}
	r := NewExec(5 * time.Second)
	got, err := FetchLabels(context.Background(), r, tgt)
	if err != nil {
		t.Fatalf("FetchLabels: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1", len(got))
	}
	for id, l := range got {
		if l.Session != "a|b" {
			t.Errorf("%s Session = %q, want %q", id, l.Session, "a|b")
		}
		if l.Window != "w|x" {
			t.Errorf("%s Window = %q, want %q", id, l.Window, "w|x")
		}
		if l.Command == "" {
			t.Errorf("%s Command is empty", id)
		}
	}
}

// A pane's working directory is the one label whose value the operator's FILESYSTEM
// controls, not tmux, and a directory name may hold a raw newline and a `|`. Both are
// measured, on tmux 3.7b:
//
//	session_name         refused  — `invalid session name: a\nb`
//	window_name          refused  — `invalid window name: w\nx`
//	pane_start_command   accepted, but tmux EMITS it escaped: `"sleep 300\n#x"`, two
//	                     characters, so the record stays on one line
//	pane_current_path    accepted AND emitted RAW: the line splits
//
// So the path is the only value that can put a newline on the wire, which is why the
// label reader has to frame records by a length the value cannot forge rather than by
// counting lines.
//
// This test asserts the CONSEQUENCE for a neighbour: a hostile directory on ONE pane
// must not corrupt any OTHER pane's labels. That is the shape of the defect — a shifted
// frame turned a session name into a path and produced a phantom pane with no error.
func TestAHostileWorkingDirectoryDoesNotCorruptItsNeighbours(t *testing.T) {
	tgt := testServer(t)

	// A directory whose name carries both hazards, plus a multi-byte name so a
	// byte-vs-rune length mistake shows up as corruption rather than passing.
	base := t.TempDir()
	hostile := filepath.Join(base, "пу|ть\nвторой")
	if err := os.MkdirAll(hostile, 0o755); err != nil {
		t.Fatalf("mkdir hostile: %v", err)
	}
	tame := filepath.Join(base, "tame")
	if err := os.MkdirAll(tame, 0o755); err != nil {
		t.Fatalf("mkdir tame: %v", err)
	}

	// testServer's session is `one`. Add two more so the hostile pane sits in the
	// MIDDLE of the listing: a frame shifted by one line corrupts what FOLLOWS, so a
	// hazard on the last pane would hide the defect.
	for _, args := range [][]string{
		{"new-session", "-d", "-s", "hostile", "-c", hostile, "sleep", "300"},
		{"new-session", "-d", "-s", "zlast", "-c", tame, "sleep", "300"},
	} {
		full := append([]string{"-S", tgt.Socket}, args...)
		if out, err := exec.Command("tmux", full...).CombinedOutput(); err != nil {
			t.Fatalf("%v: %v: %s", args, err, out)
		}
	}

	got, err := FetchLabels(context.Background(), NewExec(5*time.Second), tgt)
	if err != nil {
		t.Fatalf("FetchLabels: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d panes, want 3 — a shifted frame invents or loses panes: %#v", len(got), got)
	}

	// Every pane must carry a plausible session name. The defect's signature is a
	// session name that is really a path, so this is the assertion that catches it.
	bySession := map[string]Labels{}
	for id, l := range got {
		if strings.ContainsAny(l.Session, "/\n") {
			t.Errorf("%s Session = %q — that is a PATH in the session column, which is a "+
				"shifted frame", id, l.Session)
		}
		bySession[l.Session] = l
	}
	for _, want := range []string{"one", "hostile", "zlast"} {
		if _, ok := bySession[want]; !ok {
			var have []string
			for s := range bySession {
				have = append(have, s)
			}
			t.Fatalf("session %q is missing; the reader produced %q", want, have)
		}
	}

	// And the hostile value itself must arrive intact, newline and pipe and all.
	if p := bySession["hostile"].Path; p != hostile {
		t.Errorf("hostile pane Path = %q, want %q", p, hostile)
	}
	if p := bySession["zlast"].Path; p != tame {
		t.Errorf("the pane AFTER the hostile one has Path = %q, want %q", p, tame)
	}
	// The frame-shift signature is a value belonging to ANOTHER pane or a PATH, not a
	// process name. Asserting the exact word raced the shell: with the command given as one
	// argv WORD tmux ran it through a shell, so #{pane_current_command} read `zsh` until the
	// exec landed — measured, it flaked under the load of the full suite while passing 5 of
	// 5 in isolation. The argv is now split so tmux execs directly, AND the assertion is
	// about the property this test exists for.
	if c := bySession["zlast"].Command; strings.ContainsAny(c, "/\n") || c == "" {
		t.Errorf("the pane AFTER the hostile one has Command = %q — a path or an empty "+
			"value there is a shifted frame", c)
	}
}
