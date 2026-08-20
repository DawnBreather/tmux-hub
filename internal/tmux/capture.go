package tmux

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

// ZoneLines is how many lines of a pane's tail carry its live state markers:
// the prompt box, and 'esc to interrupt' in the footer region. Measured on a
// real Claude Code pane (design.md §6).
const ZoneLines = 6

// Capture is one pane's screen text.
type Capture struct {
	PaneID string
	Lines  []string
	Height int  // pane_height as reported at capture time
	Stale  bool // the pane was resized between the delta and the capture
}

// ZoneRange converts a pane's cursor row into absolute capture bounds.
//
// Two things are wrong with the obvious alternatives. `-S -N` cannot be used at
// all: it returns N lines of scrollback PLUS the entire visible screen. And
// anchoring at the pane's BOTTOM reads an empty zone for any pane whose output
// has not filled the screen — measured, a pane that printed three lines and
// slept returned [""], so a shell asking "Do you want to proceed?" classified as
// idle. The cursor is where output actually ended, and for a pane that DOES fill
// its screen (Claude Code always does) the two anchors give the same lines.
func ZoneRange(cursorY, paneHeight, zone int) (start, end int) {
	if paneHeight < 1 {
		paneHeight = 1
	}
	if cursorY < 0 {
		cursorY = 0
	}
	if cursorY > paneHeight-1 {
		cursorY = paneHeight - 1
	}
	if zone > paneHeight {
		zone = paneHeight
	}
	start = cursorY - zone + 1
	if start < 0 {
		start = 0
	}
	return start, start + zone - 1
}

// FetchZones captures every pane's classification zone in one invocation.
// Each capture is preceded by a frame line declaring the pane id and the pane's
// current height, so a block can be attributed without a text marker (which a
// pane's own content can forge) and a mid-batch resize is detected.
func FetchZones(ctx context.Context, r Runner, t Target, ds []Delta) ([]Capture, error) {
	if len(ds) == 0 {
		return nil, nil
	}
	var args []string
	for _, d := range ds {
		if len(args) > 0 {
			args = append(args, ";")
		}
		s, e := ZoneRange(d.CursorY, d.PaneHeight, ZoneLines)
		args = append(args,
			"display", "-p", "-t", d.PaneID, "#{pane_id} #{pane_height}", ";",
			"capture-pane", "-p", "-e", "-t", d.PaneID,
			"-S", strconv.Itoa(s), "-E", strconv.Itoa(e))
	}
	res, err := r.Run(ctx, t, args...)
	if err != nil {
		return nil, err
	}
	// A batch aborts at its first failing sub-command, so a non-zero RC means
	// the tail of the batch never ran. Parse what arrived and report the rest.
	caps, perr := demux(res.Stdout, ds)
	if perr != nil {
		return caps, fmt.Errorf("demux (rc=%d, stderr=%q): %w", res.RC, res.Stderr, perr)
	}
	return caps, nil
}

func demux(stdout string, ds []Delta) ([]Capture, error) {
	lines := strings.Split(stdout, "\n")
	var out []Capture
	pos := 0
	for _, d := range ds {
		if pos >= len(lines) {
			break // the batch aborted before reaching this pane
		}
		frame := strings.Fields(lines[pos])
		pos++
		if len(frame) != 2 {
			return out, fmt.Errorf("pane %s: bad frame line %q", d.PaneID, lines[pos-1])
		}
		if frame[0] != d.PaneID {
			return out, fmt.Errorf("frame out of order: got %s, want %s", frame[0], d.PaneID)
		}
		h, err := strconv.Atoi(frame[1])
		if err != nil {
			return out, fmt.Errorf("pane %s: bad height %q", d.PaneID, frame[1])
		}
		s, e := ZoneRange(d.CursorY, d.PaneHeight, ZoneLines)
		want := e - s + 1
		if pos+want > len(lines) {
			return out, fmt.Errorf("pane %s: declared %d lines, only %d remain",
				d.PaneID, want, len(lines)-pos)
		}
		c := Capture{
			PaneID: d.PaneID,
			Lines:  lines[pos : pos+want],
			Height: h,
			Stale:  h != d.PaneHeight,
		}
		pos += want
		out = append(out, c)
	}
	return out, nil
}

// FetchFull captures a pane's whole visible screen, for an on-screen tile.
// capture-pane with no -S emits exactly pane_height lines.
func FetchFull(ctx context.Context, r Runner, t Target, d Delta) (Capture, error) {
	res, err := r.Run(ctx, t, "capture-pane", "-p", "-e", "-t", d.PaneID)
	if err != nil {
		return Capture{}, err
	}
	if res.RC != 0 {
		return Capture{}, fmt.Errorf("capture-pane rc=%d: %s", res.RC, res.Stderr)
	}
	lines := strings.Split(res.Stdout, "\n")
	// capture-pane's output ends with a newline, which Split turns into a
	// trailing empty element.
	if n := len(lines); n > 0 && lines[n-1] == "" {
		lines = lines[:n-1]
	}
	return Capture{PaneID: d.PaneID, Lines: lines, Height: d.PaneHeight}, nil
}
