package tmux

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestZoneRange(t *testing.T) {
	cases := []struct {
		name         string
		cy, h, zone  int
		wantS, wantE int
	}{
		{"cursor at the bottom of a full screen", 23, 24, 6, 18, 23},
		{"cursor at the bottom, tall pane", 49, 50, 6, 44, 49},
		{"cursor near the TOP: the zone follows it, not the pane bottom", 3, 24, 6, 0, 5},
		{"cursor mid-screen", 10, 24, 6, 5, 10},
		{"pane shorter than the zone", 3, 4, 6, 0, 3},
		{"one-row pane", 0, 1, 6, 0, 0},
		{"cursor past the pane is clamped", 99, 12, 6, 6, 11},
	}
	for _, c := range cases {
		s, e := ZoneRange(c.cy, c.h, c.zone)
		if s != c.wantS || e != c.wantE {
			t.Errorf("%s: ZoneRange(cy=%d,h=%d,zone=%d) = (%d,%d), want (%d,%d)",
				c.name, c.cy, c.h, c.zone, s, e, c.wantS, c.wantE)
		}
	}
}

func fillPane(t *testing.T, tgt Target, target string, cmd string) {
	t.Helper()
	full := append([]string{"-S", tgt.Socket}, "send-keys", "-t", target, "-l", cmd)
	if out, err := exec.Command("tmux", full...).CombinedOutput(); err != nil {
		t.Fatalf("send-keys: %v: %s", err, out)
	}
	full = append([]string{"-S", tgt.Socket}, "send-keys", "-t", target, "Enter")
	if out, err := exec.Command("tmux", full...).CombinedOutput(); err != nil {
		t.Fatalf("send-keys Enter: %v: %s", err, out)
	}
	time.Sleep(1500 * time.Millisecond)
}

// The zone must be exactly the tail of the visible screen. `-S -N` is NOT the
// last N lines: it returns N lines of scrollback PLUS the whole screen
// (design.md §3), which is why the range is computed from pane_height.
func TestFetchZonesIsExactlyTheTail(t *testing.T) {
	tgt := testServer(t)
	fillPane(t, tgt, "one", "for i in $(seq 1 80); do echo LINE-$i; done")

	r := NewExec(10 * time.Second)
	ds, err := FetchDeltas(context.Background(), r, tgt)
	if err != nil {
		t.Fatalf("FetchDeltas: %v", err)
	}
	caps, err := FetchZones(context.Background(), r, tgt, ds)
	if err != nil {
		t.Fatalf("FetchZones: %v", err)
	}
	if len(caps) != len(ds) {
		t.Fatalf("got %d captures for %d panes", len(caps), len(ds))
	}
	if n := len(caps[0].Lines); n != ZoneLines {
		t.Fatalf("zone has %d lines, want %d", n, ZoneLines)
	}
	full, err := FetchFull(context.Background(), r, tgt, ds[0])
	if err != nil {
		t.Fatalf("FetchFull: %v", err)
	}
	want := full.Lines[len(full.Lines)-ZoneLines:]
	for i := range want {
		if strings.TrimRight(want[i], " ") != strings.TrimRight(caps[0].Lines[i], " ") {
			t.Fatalf("zone line %d = %q, want %q", i, caps[0].Lines[i], want[i])
		}
	}
}

// A pane whose own text reproduces the framing marker must not be able to
// confuse the demux. Framing is by declared length, so content cannot forge it.
func TestFetchZonesSurvivesForgedMarker(t *testing.T) {
	tgt := testServer(t)
	fillPane(t, tgt, "one", `printf '%s\n' "--TMUXHUB-0001--" "--TMUXHUB-0002--" "real line"`)

	r := NewExec(10 * time.Second)
	ds, err := FetchDeltas(context.Background(), r, tgt)
	if err != nil {
		t.Fatalf("FetchDeltas: %v", err)
	}
	caps, err := FetchZones(context.Background(), r, tgt, ds)
	if err != nil {
		t.Fatalf("FetchZones: %v", err)
	}
	if len(caps) != 1 || caps[0].PaneID != ds[0].PaneID {
		t.Fatalf("demux went wrong: %+v", caps)
	}
	if len(caps[0].Lines) != ZoneLines {
		t.Fatalf("zone has %d lines, want %d", len(caps[0].Lines), ZoneLines)
	}
}

// FetchFull is used for on-screen tiles. capture-pane with no -S emits exactly
// pane_height lines (design.md §3), which is the property the demux relies on.
func TestFetchFullEmitsExactlyPaneHeight(t *testing.T) {
	tgt := testServer(t)
	r := NewExec(10 * time.Second)
	ds, err := FetchDeltas(context.Background(), r, tgt)
	if err != nil {
		t.Fatalf("FetchDeltas: %v", err)
	}
	c, err := FetchFull(context.Background(), r, tgt, ds[0])
	if err != nil {
		t.Fatalf("FetchFull: %v", err)
	}
	if len(c.Lines) != ds[0].PaneHeight {
		t.Fatalf("full capture has %d lines, want pane_height %d", len(c.Lines), ds[0].PaneHeight)
	}
}
