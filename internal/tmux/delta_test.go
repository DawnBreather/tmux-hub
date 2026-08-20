package tmux

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestParseDelta(t *testing.T) {
	in := "%0|1786450154|153|0|NORM|80|24|12345|23|$0|1|9|100|@0|0||0\n" +
		"%7|1786450160|0|1|ALT|200|50|999|49|$3|0|9|100|@1|2|7|1\n"
	got, err := ParseDelta(in)
	if err != nil {
		t.Fatalf("ParseDelta: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
	if got[0].PaneID != "%0" || got[0].Activity != 1786450154 || got[0].HistorySize != 153 {
		t.Errorf("got[0] = %+v", got[0])
	}
	if got[0].CursorY != 23 || got[1].CursorY != 49 {
		t.Errorf("cursor_y not parsed: %d, %d", got[0].CursorY, got[1].CursorY)
	}
	if got[0].SessionID != "$0" || got[1].SessionID != "$3" {
		t.Errorf("session_id not parsed: %q, %q", got[0].SessionID, got[1].SessionID)
	}
	if got[0].Dead || got[0].Alt || got[0].PaneHeight != 24 || got[0].PanePID != 12345 {
		t.Errorf("got[0] = %+v", got[0])
	}
	if !got[1].Dead || !got[1].Alt || got[1].WindowWidth != 200 {
		t.Errorf("got[1] = %+v", got[1])
	}
}

// The delta format must carry no free text, so no value can contain the
// delimiter. A field count other than the declared one is a bug in the hub, not
// data to be salvaged (design.md §6).
func TestParseDeltaRejectsWrongFieldCount(t *testing.T) {
	// Built from deltaFields rather than written out, because the literal version
	// passed for the WRONG REASON the moment a field was added: its "one too many"
	// line became the correct length and only errored because the extra token would
	// not parse as an integer. Every field here is `0`, so a refusal can only be
	// about the count.
	short := strings.Repeat("0|", deltaFields-2) + "0"
	long := strings.Repeat("0|", deltaFields) + "0"
	for _, in := range []string{short + "\n", long + "\n"} {
		if _, err := ParseDelta(in); err == nil {
			t.Errorf("ParseDelta(%d fields) = nil error, want a refusal about the count",
				strings.Count(in, "|")+1)
		} else if !strings.Contains(err.Error(), "fields") {
			t.Errorf("ParseDelta refused for the wrong reason: %v", err)
		}
	}
}

func TestParseDeltaSkipsBlankLines(t *testing.T) {
	got, err := ParseDelta("\n%0|1|2|0|NORM|80|24|1|2|$0|1|9|100|@0|0||0\n\n")
	if err != nil {
		t.Fatalf("ParseDelta: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1", len(got))
	}
}

// A session or window name containing the delimiter must not be able to shift
// fields, because the delta format does not select any name at all. This test
// creates the hostile name and proves the parse is unaffected.
func TestDeltaFormatIsImmuneToNamesWithDelimiter(t *testing.T) {
	tgt := testServer(t)
	if out, err := exec.Command("tmux", "-S", tgt.Socket,
		"rename-session", "-t", "one", "a|b").CombinedOutput(); err != nil {
		t.Fatalf("rename-session: %v: %s", err, out)
	}
	if out, err := exec.Command("tmux", "-S", tgt.Socket,
		"rename-window", "-t", "a|b", "w|x").CombinedOutput(); err != nil {
		t.Fatalf("rename-window: %v: %s", err, out)
	}
	r := NewExec(5 * time.Second)
	got, err := FetchDeltas(context.Background(), r, tgt)
	if err != nil {
		t.Fatalf("FetchDeltas: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1", len(got))
	}
	if !strings.HasPrefix(got[0].PaneID, "%") || got[0].PaneHeight == 0 {
		t.Fatalf("parsed badly: %+v", got[0])
	}
}

// The bracketed-paste flag is the only signal that says whether a send is SAFE, so
// it is parsed rather than ignored. Measured: `less` reports 0 and turns a pasted
// prompt into keystrokes; bash, vim and the python REPL report 1 and take the same
// payload as inert text.
func TestParseDeltaReadsTheBracketedPasteFlag(t *testing.T) {
	got, err := ParseDelta("%0|1|2|0|NORM|80|24|1|2|$0|1|9|100|@0|0||0\n" +
		"%1|1|2|0|NORM|80|24|1|2|$0|0|9|100|@0|0||0\n")
	if err != nil {
		t.Fatalf("ParseDelta: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d deltas", len(got))
	}
	if !got[0].Bracketed {
		t.Error("flag 1 did not parse as bracketed")
	}
	if got[1].Bracketed {
		t.Error("flag 0 parsed as bracketed — a pane that would interpret a paste as keys")
	}
}

// The epoch is what invalidates a pane id after a server restart, so an EMPTY
// epoch is the failure that matters: two empty strings compare equal, so the
// clause would silently pass for every target. Asserted against a real server
// rather than logged, because an unknown format variable comes back empty with the
// field count intact — the measured way this fails silently.
func TestEpochIsPresentAndIdenticalForEveryPaneOnAServer(t *testing.T) {
	tgt := testServerWithPanes(t, 3)
	ds, err := FetchDeltas(context.Background(), NewExec(10*time.Second), tgt)
	if err != nil {
		t.Fatalf("FetchDeltas: %v", err)
	}
	if len(ds) < 3 {
		t.Fatalf("got %d panes, want 3", len(ds))
	}
	for _, d := range ds {
		if d.Epoch == "" {
			t.Fatalf("pane %s has no epoch — the restart check cannot work", d.PaneID)
		}
		if !strings.Contains(d.Epoch, ":") {
			t.Errorf("epoch %q is not pid:start_time", d.Epoch)
		}
		if d.Epoch != ds[0].Epoch {
			t.Errorf("pane %s reports epoch %q, pane %s reports %q — one server, one epoch",
				d.PaneID, d.Epoch, ds[0].PaneID, ds[0].Epoch)
		}
	}
}

// A dead pane's exit code is the difference between "the agent exited" and
// "the agent exited BADLY", and the row has to say which.
func TestDeltaCarriesIndexAndDeadStatus(t *testing.T) {
	// Helper to build a delta format line with overrides.
	deltaLine := func(overrides map[string]string) string {
		defaults := map[string]string{
			"pane_id":          "%0",
			"window_activity":  "1786450154",
			"history_size":     "153",
			"pane_dead":        "0",
			"alternate_on":     "NORM",
			"window_width":     "80",
			"pane_height":      "24",
			"pane_pid":         "12345",
			"cursor_y":         "23",
			"session_id":       "$0",
			"bracket_paste":    "1",
			"pid":              "9",
			"start_time":       "100",
			"window_id":        "@0",
			"pane_index":       "0",
			"pane_dead_status": "",
			"window_index":     "0",
		}
		for k, v := range overrides {
			defaults[k] = v
		}
		return defaults["pane_id"] + "|" +
			defaults["window_activity"] + "|" +
			defaults["history_size"] + "|" +
			defaults["pane_dead"] + "|" +
			defaults["alternate_on"] + "|" +
			defaults["window_width"] + "|" +
			defaults["pane_height"] + "|" +
			defaults["pane_pid"] + "|" +
			defaults["cursor_y"] + "|" +
			defaults["session_id"] + "|" +
			defaults["bracket_paste"] + "|" +
			defaults["pid"] + "|" +
			defaults["start_time"] + "|" +
			defaults["window_id"] + "|" +
			defaults["pane_index"] + "|" +
			defaults["pane_dead_status"] + "|" +
			defaults["window_index"]
	}

	line := deltaLine(map[string]string{"pane_index": "2", "pane_dead": "1",
		"pane_dead_status": "7", "window_index": "3"})
	ds, err := ParseDelta(line + "\n")
	if err != nil {
		t.Fatalf("ParseDelta: %v", err)
	}
	d := ds[0]
	if d.Index != 2 {
		t.Errorf("Index = %d, want 2", d.Index)
	}
	if !d.Dead {
		t.Error("Dead = false, want true")
	}
	if d.DeadStatus != 7 {
		t.Errorf("DeadStatus = %d, want 7", d.DeadStatus)
	}
	// window_index is the window component of a PERSISTED hide key, so reading it
	// from the wrong position would hide the wrong pane across a restart. Asserting a
	// value distinct from pane_index is what catches a swap of the two.
	if d.WindowIndex != 3 {
		t.Errorf("WindowIndex = %d, want 3 — and pane_index is 2, so a 2 here means the "+
			"two indices are swapped", d.WindowIndex)
	}
}

func TestParseDeltaJoinsTheEpochHalves(t *testing.T) {
	got, err := ParseDelta("%0|1|2|0|NORM|80|24|1|2|$0|1|702399|1786489650|@0|0||0\n")
	if err != nil {
		t.Fatalf("ParseDelta: %v", err)
	}
	if got[0].Epoch != "702399:1786489650" {
		t.Errorf("Epoch = %q, want the two halves joined", got[0].Epoch)
	}
	// A tmux that knows neither name yields an empty epoch rather than a bare ":",
	// so an absent epoch cannot be mistaken for a present one.
	empty, err := ParseDelta("%0|1|2|0|NORM|80|24|1|2|$0|1|||@0|0||0\n")
	if err != nil {
		t.Fatalf("ParseDelta: %v", err)
	}
	if empty[0].Epoch != "" {
		t.Errorf("Epoch = %q, want empty when tmux knows neither name", empty[0].Epoch)
	}
}

// Against a real server, so the format string is checked rather than the parser.
func TestBracketedPasteFlagAgainstARealPane(t *testing.T) {
	tgt := testServer(t)
	ds, err := FetchDeltas(context.Background(), NewExec(10*time.Second), tgt)
	if err != nil {
		t.Fatalf("FetchDeltas: %v", err)
	}
	if len(ds) == 0 {
		t.Fatal("no panes")
	}
	// The value depends on what the pane runs, so this asserts only that the field
	// PARSED — an unknown format variable would have come back empty and the field
	// count would have held, which is the measured way this fails silently.
	t.Logf("pane %s bracketed=%v", ds[0].PaneID, ds[0].Bracketed)
}
