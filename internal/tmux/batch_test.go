package tmux

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// A whole tick in two round trips, whatever the pane count. The earlier shape
// issued one invocation per label query and one per full capture — 501 ms each
// over ssh, so six tiles cost four and a half seconds.
func TestFetchSnapshotGetsEverythingInOneBatch(t *testing.T) {
	tgt := testServer(t)
	// three panes with different content and heights
	for i, cmd := range []string{
		`sh -c 'echo AAA; sleep 300'`,
		`sh -c 'echo BBB; echo Do you want to proceed?; sleep 300'`,
	} {
		full := []string{"-S", tgt.Socket, "new-window", "-d", "-t", "one", cmd}
		if out, err := exec.Command("tmux", full...).CombinedOutput(); err != nil {
			t.Fatalf("new-window %d: %v: %s", i, err, out)
		}
	}
	time.Sleep(1500 * time.Millisecond)

	r := NewExec(15 * time.Second)
	ds, err := FetchDeltas(context.Background(), r, tgt)
	if err != nil {
		t.Fatalf("FetchDeltas: %v", err)
	}
	if len(ds) < 3 {
		t.Fatalf("want at least 3 panes, got %d", len(ds))
	}
	want := map[string]bool{tgt.Label + "\x00" + ds[1].PaneID: true}

	snap, err := FetchSnapshot(context.Background(), r, tgt, ds, want)
	if err != nil {
		t.Fatalf("FetchSnapshot: %v", err)
	}
	if len(snap.Deltas) != len(ds) {
		t.Errorf("deltas: got %d, want %d", len(snap.Deltas), len(ds))
	}
	if len(snap.Labels) != len(ds) {
		t.Errorf("labels: got %d, want one per pane (%d)", len(snap.Labels), len(ds))
	}
	for id, l := range snap.Labels {
		if l.Session == "" || l.Command == "" {
			t.Errorf("pane %s has an empty label: %+v", id, l)
		}
	}
	if len(snap.Zones) != len(ds) {
		t.Errorf("zones: got %d, want one per pane (%d)", len(snap.Zones), len(ds))
	}
	// The zone is cursor-anchored, so a pane whose output sits at the top must
	// still yield its text rather than blank rows.
	var sawText bool
	for _, z := range snap.Zones {
		if strings.Contains(strings.Join(z.Lines, "\n"), "BBB") {
			sawText = true
		}
	}
	if !sawText {
		t.Error("no zone contained the text a pane printed near the top")
	}
	if len(snap.Fulls) != 1 {
		t.Fatalf("fulls: got %d, want exactly the one requested", len(snap.Fulls))
	}
	f := snap.Fulls[ds[1].PaneID]
	if len(f.Lines) != ds[1].PaneHeight {
		t.Errorf("full capture has %d rows, want pane_height %d", len(f.Lines), ds[1].PaneHeight)
	}
}

// Framing is by declared length, so a pane printing the frame's own shape cannot
// shift the demux.
func TestFetchSnapshotSurvivesAPaneImitatingAFrame(t *testing.T) {
	tgt := testServer(t)
	ids := []string{}
	r := NewExec(15 * time.Second)
	ds0, err := FetchDeltas(context.Background(), r, tgt)
	if err != nil {
		t.Fatalf("FetchDeltas: %v", err)
	}
	for _, d := range ds0 {
		ids = append(ids, d.PaneID)
	}
	// print something that looks exactly like a frame line for another pane
	full := []string{"-S", tgt.Socket, "send-keys", "-t", ids[0], "-l", "printf '%s 24\\n' '%99'"}
	if out, err := exec.Command("tmux", full...).CombinedOutput(); err != nil {
		t.Fatalf("send-keys: %v: %s", err, out)
	}
	exec.Command("tmux", "-S", tgt.Socket, "send-keys", "-t", ids[0], "Enter").Run()
	time.Sleep(1200 * time.Millisecond)

	snap, err := FetchSnapshot(context.Background(), r, tgt, ds0, nil)
	if err != nil {
		t.Fatalf("FetchSnapshot: %v", err)
	}
	if len(snap.Zones) != len(snap.Deltas) {
		t.Fatalf("demux shifted: %d zones for %d panes", len(snap.Zones), len(snap.Deltas))
	}
	for i, z := range snap.Zones {
		if z.PaneID != snap.Deltas[i].PaneID {
			t.Fatalf("zone %d is attributed to %s, want %s", i, z.PaneID, snap.Deltas[i].PaneID)
		}
	}
}

// Past ~61 panes the single-batch form died with tmux's own `command too long`,
// and because a batch aborts at its first failure the WHOLE tick returned nothing —
// so a healthy host read `down`. Measured on 3.7b: 60 panes worked, 62 failed.
//
// This asserts a count well past that ceiling, and it asserts every pane came back
// rather than merely that no error was returned: the old failure produced an error
// AND an empty registry, so either half alone would have missed it.
func TestFetchSnapshotPastTheCommandLengthCeiling(t *testing.T) {
	const panes = 75
	tgt := testServerWithPanes(t, panes)
	r := NewExec(30 * time.Second)

	ds, err := FetchDeltas(context.Background(), r, tgt)
	if err != nil {
		t.Fatalf("FetchDeltas: %v", err)
	}
	if len(ds) != panes {
		t.Fatalf("FetchDeltas returned %d panes, want %d", len(ds), panes)
	}

	want := map[string]bool{}
	for _, d := range ds {
		want[tgt.Label+"\x00"+d.PaneID] = true
	}
	snap, err := FetchSnapshot(context.Background(), r, tgt, ds, want)
	if err != nil {
		t.Fatalf("FetchSnapshot with %d panes: %v", panes, err)
	}
	if len(snap.Zones) != panes {
		t.Errorf("got %d zones, want %d", len(snap.Zones), panes)
	}
	if len(snap.Fulls) != panes {
		t.Errorf("got %d full captures, want %d", len(snap.Fulls), panes)
	}
	if len(snap.Labels) != panes {
		t.Errorf("got %d label sets, want %d", len(snap.Labels), panes)
	}
	// The frames must still line up: a chunked reader that lost its place would
	// attribute one pane's screen to another, which is worse than failing.
	for _, z := range snap.Zones {
		if _, ok := snap.Labels[z.PaneID]; !ok {
			t.Errorf("zone for %s has no labels — the chunks disagree", z.PaneID)
		}
	}
}

func testServerWithPanes(t *testing.T, n int) Target {
	t.Helper()
	tgt := testServer(t)
	for i := 1; i < n; i++ {
		out, err := exec.Command("tmux", "-S", tgt.Socket, "new-window", "-d", "cat").CombinedOutput()
		if err != nil {
			t.Fatalf("new-window %d: %v: %s", i, err, out)
		}
	}
	return tgt
}

// The floor under fetchLabels: the argv it ACTUALLY issues must be the one format
// derived from the table. Two hardcoded lists used to face each other here and
// #{pane_start_command} was added to the table while nothing read the table, so
// registry.Pane.StartCommand was empty on every real poll and every unit test still
// passed. Asserting the string labelFormat() returns is not enough — a caller can
// build its own — so this asserts what went over the wire.
func TestFetchLabelsIssuesTheOneFormatFromTheTable(t *testing.T) {
	r := &fakeRunner{}
	// The runner answers nothing, so the parse yields an empty map. The argv it
	// issued is what this test is about.
	_ = fetchLabels(context.Background(), r, Target{Socket: "/tmp/x"}, &Snapshot{})

	assertArgv(t, r.last, "list-panes", "-a", "-F", labelFormat())

	// And the format it issued must name every field, so a table entry cannot be
	// added and left unfetched.
	for _, lf := range labelFormats {
		if !strings.Contains(r.last[3], "#{"+lf.field+"}") {
			t.Errorf("the issued format omits #{%s}: %q", lf.field, r.last[3])
		}
		if !strings.Contains(r.last[3], "#{n:"+lf.field+"}") {
			t.Errorf("the issued format omits the LENGTH of %s, so that value could "+
				"shift the frame: %q", lf.field, r.last[3])
		}
	}
}
