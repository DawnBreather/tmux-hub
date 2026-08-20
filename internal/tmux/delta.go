package tmux

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

// DeltaFormat selects only character-restricted values: a pane id, integers and
// a fixed token. Injection through a session, window or command name is not
// defended against here — it is impossible, because no such name is selected.
// Labels come from LabelFormats (see labels.go), each with exactly one trailing
// free-text field.
const DeltaFormat = "#{pane_id}|#{window_activity}|#{history_size}|" +
	"#{pane_dead}|#{?alternate_on,ALT,NORM}|#{window_width}|#{pane_height}|#{pane_pid}|" +
	"#{cursor_y}|#{session_id}|#{bracket_paste_flag}|#{pid}|#{start_time}|#{window_id}|" +
	"#{pane_index}|#{pane_dead_status}|#{window_index}"

const deltaFields = 17

// Delta is one pane's cheap per-tick state.
type Delta struct {
	PaneID      string
	Activity    int64 // #{window_activity}: unix seconds of last output in this WINDOW
	HistorySize int
	Dead        bool
	Alt         bool
	WindowWidth int
	PaneHeight  int
	PanePID     int
	// CursorY anchors the classification zone. Anchoring at the pane's BOTTOM
	// reads an empty zone for any pane whose output has not filled the screen —
	// measured: a pane printing three lines then sleeping returned [""] from a
	// bottom-anchored zone and so classified as idle while it was asking a
	// question. Claude Code fills its screen so both anchors agree there.
	CursorY int
	// SessionID is `$N` and is what ATTACH targets. A session NAME does not
	// survive a rename — measured, `has-session -t orig` fails rc=1 right after
	// one, while `display -t $0` still resolves — so attaching by name breaks
	// whenever a session is renamed between a poll and the keypress.
	SessionID string
	// WindowID is `@N`, and it is what "has this pane moved" has to be asked about.
	// A window NAME is not identity: automatic-rename follows the FOREGROUND
	// process, so measured against a live pane, the name changed from `bash` to
	// `claude` between two polls with the pane never moving — and §7's rule then
	// reported "this pane changed session or window since you selected it" for an
	// agent shelling out to git. The id changes only when the pane really moves.
	WindowID string
	// Index is #{pane_index}: the pane's 0-based index within its window.
	Index int
	// WindowIndex is #{window_index}: the window's 0-based position in its session.
	// It is the window component of a persisted hide key, because the window's NAME
	// is not identity — tmux ships `automatic-rename on`, so the name follows the
	// running command (measured: one window went zsh → sleep → tail across three
	// commands while the index stayed 0). #{window_id} is not usable either: `@N`
	// does not survive a server restart, and surviving one is why the hide key
	// exists (docs/design.md §18).
	WindowIndex int
	// DeadStatus is #{pane_dead_status}: the exit code of the command in a dead pane.
	// It is empty for a live pane.
	DeadStatus int
	// Bracketed says the pane's application advertises bracketed paste.
	//
	// It is collected because it is the only signal that says whether a send is
	// SAFE, and it costs nothing here. Measured: `less` with
	// #{bracket_paste_flag}=0 turned a three-line pasted prompt into KEYSTROKES and
	// opened its help screen — a payload containing `q` would have quit it and one
	// containing `!cmd` would have run a shell command. `bash`, `vim` and the
	// python REPL all reported 1 and took the same payload as inert text.
	//
	// A 1 is not a guarantee either, which is why §7 treats a 0 as a reason to
	// CONFIRM rather than as a refusal: an application can be in a modal state that
	// does not honour the mode.
	Bracketed bool
	// Epoch is the server's `#{pid}:#{start_time}`, the same value for every pane
	// on one server. A restart changes it and hands out `%0` again — measured,
	// `702399:1786489650` → `702664:1786489651` with the new server's first pane
	// called `%0` (docs/design.md §3) — so a changed epoch means every pane id the
	// hub remembers may now name a different pane, and §7 makes it a reason to
	// confirm.
	//
	// It rides the delta because a server variable resolves inside a per-pane
	// format, so it costs no extra round trip. On a tmux that does not know these
	// two names the field comes back empty for every pane and the epoch clause is
	// inert — the token guard still refuses after a restart, because a fresh server
	// carries none of the hub's options. That is why the two names are NOT in
	// requiredFields: a secondary check must not turn an old tmux into an
	// unusable host.
	Epoch string
}

// ParseDelta parses DeltaFormat output. A wrong field count is a hub bug and is
// reported, never salvaged.
func ParseDelta(stdout string) ([]Delta, error) {
	var out []Delta
	for i, line := range strings.Split(stdout, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		f := strings.Split(line, "|")
		if len(f) != deltaFields {
			return nil, fmt.Errorf("delta line %d: got %d fields, want %d: %q",
				i+1, len(f), deltaFields, line)
		}
		d := Delta{PaneID: f[0], Alt: f[4] == "ALT", Dead: f[3] == "1",
			SessionID: f[9], Bracketed: f[10] == "1", WindowID: f[13]}
		// Joined rather than kept apart: the two halves are never used separately,
		// and an empty half must not read as a match for another empty half from a
		// different field.
		if f[11] != "" || f[12] != "" {
			d.Epoch = f[11] + ":" + f[12]
		}
		var err error
		if d.Activity, err = strconv.ParseInt(f[1], 10, 64); err != nil {
			return nil, fmt.Errorf("delta line %d: window_activity: %w", i+1, err)
		}
		for _, p := range []struct {
			dst *int
			src string
			nm  string
		}{
			{&d.HistorySize, f[2], "history_size"},
			{&d.WindowWidth, f[5], "window_width"},
			{&d.PaneHeight, f[6], "pane_height"},
			{&d.PanePID, f[7], "pane_pid"},
			{&d.CursorY, f[8], "cursor_y"},
			{&d.Index, f[14], "pane_index"},
			{&d.WindowIndex, f[16], "window_index"},
		} {
			v, err := strconv.Atoi(p.src)
			if err != nil {
				return nil, fmt.Errorf("delta line %d: %s: %w", i+1, p.nm, err)
			}
			*p.dst = v
		}
		// pane_dead_status is empty for a live pane, so tolerate an empty string as 0.
		if f[15] != "" {
			if v, err := strconv.Atoi(f[15]); err != nil {
				return nil, fmt.Errorf("delta line %d: pane_dead_status: %w", i+1, err)
			} else {
				d.DeadStatus = v
			}
		}
		out = append(out, d)
	}
	return out, nil
}

// FetchDeltas runs one list-panes for every pane on the server.
func FetchDeltas(ctx context.Context, r Runner, t Target) ([]Delta, error) {
	res, err := r.Run(ctx, t, "list-panes", "-a", "-F", DeltaFormat)
	if err != nil {
		return nil, err
	}
	if res.RC != 0 {
		return nil, fmt.Errorf("list-panes rc=%d: %s", res.RC, res.Stderr)
	}
	return ParseDelta(res.Stdout)
}
