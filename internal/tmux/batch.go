package tmux

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

// Snapshot is everything one tick needs from one host.
type Snapshot struct {
	Deltas []Delta
	Labels map[string]Labels
	Zones  []Capture
	Fulls  map[string]Capture
}

// PanesPerBatch bounds how many panes go into one tmux invocation.
//
// tmux refuses a command that is too long, and the refusal takes the WHOLE batch
// with it — a batch aborts at its first failing sub-command. Measured on 3.7b: 60
// panes worked, 62 failed with `command too long`, and because the tick then
// returned nothing the host read **down** — a healthy machine reported unreachable
// because the hub built too long an argv. 40 keeps a wide margin under that ~6 KB
// ceiling while still costing one round trip per 40 panes.
const PanesPerBatch = 40

// FetchSnapshot takes everything after the delta in as few round trips as the
// command-length ceiling allows: the labels, every pane's classification zone, and
// a full capture of the panes named in wantFull.
//
// The cost is 1 + ceil(panes/PanesPerBatch) + ceil(fulls/PanesPerBatch) round
// trips, not one — an earlier version claimed one whatever the pane count, and that
// claim is what broke above 61 panes.
//
// The deltas are passed in rather than fetched: re-fetching them here cost a
// third round trip per tick, measured at ~500 ms against a real host.
//
// This is not a micro-optimisation. The earlier shape issued one invocation for
// the delta, one per label query, and one per full capture — measured against a
// real host over ssh at 501 ms each, so a tick with six visible tiles cost about
// four and a half seconds and the dashboard was permanently behind. tmux charges
// per round trip, not per command: a batch of 80 framed captures costs 5.1 ms of
// its time.
//
// Everything is framed by declared length, computed from the delta the caller
// already holds, because tmux does not frame sub-command output and a pane's own
// content can forge a text marker.
func FetchSnapshot(ctx context.Context, r Runner, t Target, ds []Delta, wantFull map[string]bool) (Snapshot, error) {
	snap := Snapshot{Deltas: ds}
	if len(ds) == 0 {
		return snap, nil
	}

	// Labels go in their own invocation: ONE `list-panes -a`, a fixed-size argv
	// whatever the pane count, so it never contributes to the length ceiling and
	// never needs chunking.
	if err := fetchLabels(ctx, r, t, &snap); err != nil {
		return snap, err
	}

	for _, chunk := range chunkDeltas(ds, PanesPerBatch) {
		zs, err := fetchZoneChunk(ctx, r, t, chunk)
		if err != nil {
			return snap, err
		}
		snap.Zones = append(snap.Zones, zs...)
	}

	var fullOrder []Delta
	for _, d := range ds {
		if wantFull[t.Label+"\x00"+d.PaneID] {
			fullOrder = append(fullOrder, d)
		}
	}
	snap.Fulls = make(map[string]Capture, len(fullOrder))
	for _, chunk := range chunkDeltas(fullOrder, PanesPerBatch) {
		cs, err := fetchFullChunk(ctx, r, t, chunk)
		if err != nil {
			return snap, err
		}
		for _, c := range cs {
			snap.Fulls[c.PaneID] = c
		}
	}
	return snap, nil
}

// chunkDeltas splits panes into batches small enough for one invocation.
func chunkDeltas(ds []Delta, size int) [][]Delta {
	if size < 1 {
		size = 1
	}
	var out [][]Delta
	for i := 0; i < len(ds); i += size {
		out = append(out, ds[i:min(i+size, len(ds))])
	}
	return out
}

// fetchLabels reads every per-pane label into the snapshot.
//
// It is FetchLabels and nothing else. There used to be a second reader here — one
// `list-panes` per label, blocks framed by `lines[pos : pos+len(ds)]` — and two
// readers of one wire format is one too many: the batch one framed by a count from
// a DIFFERENT invocation, so a newline in any value, or a pane created between the
// two calls, shifted every later block by a line. See labelFormat for the
// measurements. Keeping the wrapper rather than inlining the call keeps the
// snapshot assembly readable as a list of fetches.
func fetchLabels(ctx context.Context, r Runner, t Target, snap *Snapshot) error {
	ls, err := FetchLabels(ctx, r, t)
	if err != nil {
		return err
	}
	snap.Labels = ls
	return nil
}

// fetchZoneChunk reads one chunk of classification zones.
func fetchZoneChunk(ctx context.Context, r Runner, t Target, ds []Delta) ([]Capture, error) {
	var args []string
	for i, d := range ds {
		if i > 0 {
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
	lines := strings.Split(res.Stdout, "\n")
	pos := 0
	out := make([]Capture, 0, len(ds))
	for _, d := range ds {
		s, e := ZoneRange(d.CursorY, d.PaneHeight, ZoneLines)
		c, next, err := readFramed(lines, pos, d, e-s+1)
		if err != nil {
			return nil, fmt.Errorf("%w (rc=%d, stderr=%q)", err, res.RC, res.Stderr)
		}
		out = append(out, c)
		pos = next
	}
	return out, nil
}

// fetchFullChunk reads one chunk of whole-screen captures.
func fetchFullChunk(ctx context.Context, r Runner, t Target, ds []Delta) ([]Capture, error) {
	var args []string
	for i, d := range ds {
		if i > 0 {
			args = append(args, ";")
		}
		args = append(args,
			"display", "-p", "-t", d.PaneID, "#{pane_id} #{pane_height}", ";",
			"capture-pane", "-p", "-e", "-t", d.PaneID)
	}
	res, err := r.Run(ctx, t, args...)
	if err != nil {
		return nil, err
	}
	lines := strings.Split(res.Stdout, "\n")
	pos := 0
	out := make([]Capture, 0, len(ds))
	for _, d := range ds {
		c, next, err := readFramed(lines, pos, d, d.PaneHeight)
		if err != nil {
			return nil, fmt.Errorf("%w (rc=%d, stderr=%q)", err, res.RC, res.Stderr)
		}
		out = append(out, c)
		pos = next
	}
	return out, nil
}

// readFramed consumes one `#{pane_id} #{pane_height}` frame line plus want rows.
// A frame naming a different pane means the reader and the batch disagree, which
// is a hub bug and is reported rather than guessed at.
func readFramed(lines []string, pos int, d Delta, want int) (Capture, int, error) {
	if pos >= len(lines) {
		return Capture{}, pos, fmt.Errorf("pane %s: batch ended before its frame", d.PaneID)
	}
	f := strings.Fields(lines[pos])
	pos++
	if len(f) != 2 {
		return Capture{}, pos, fmt.Errorf("pane %s: bad frame line %q", d.PaneID, lines[pos-1])
	}
	if f[0] != d.PaneID {
		return Capture{}, pos, fmt.Errorf("frame out of order: got %s, want %s", f[0], d.PaneID)
	}
	h, err := strconv.Atoi(f[1])
	if err != nil {
		return Capture{}, pos, fmt.Errorf("pane %s: bad height %q", d.PaneID, f[1])
	}
	if pos+want > len(lines) {
		return Capture{}, pos, fmt.Errorf("pane %s: declared %d rows, only %d remain",
			d.PaneID, want, len(lines)-pos)
	}
	return Capture{
		PaneID: d.PaneID,
		Lines:  lines[pos : pos+want],
		Height: h,
		Stale:  h != d.PaneHeight,
	}, pos + want, nil
}
