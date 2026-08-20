package tmux

import (
	"context"
	"fmt"
	"strings"
)

// requiredFields are every structural field the hub depends on. They are checked
// by VALUE at connect, not declared in a per-version allowlist: tmux never errors
// on an unknown format variable, so a wrong or unsupported name silently yields
// an empty field with the count intact — and an empty window_activity parses as
// 0, i.e. every pane on that host reads as last active in 1970.
var requiredFields = []string{
	"pane_id",
	"window_activity",
	"history_size",
	"pane_dead",
	"window_width",
	"pane_height",
	"pane_pid",
	"cursor_y",
	"session_id",
	// window_id is `@N`, and it is the target of BOTH things that ask "which
	// window" — §7's "has this pane moved since you selected it" and §20's
	// select-window after a jump. Its absence fails OPEN in the worst way of any
	// field here: `select-window -t ''` is not refused, so a jump lands on
	// whatever window the client happened to be on, which is a window the
	// operator did not pick and the hub then reports as the one they did.
	"window_id",
	// window_index is the window component of a PERSISTED hide key (§18), which is
	// what puts it here rather than beside pane_index, whose absence has the same
	// shape. Its absence fails loudly either way — an empty field makes ParseDelta
	// error, so no host can quietly read the wrong window — but a named answer at
	// connect is worth one line against the same complaint arriving once per tick.
	"window_index",
	"session_name",
	"window_name",
	"pane_current_command",
	"version",
}

// FieldReport is the outcome of the connect-time assertion.
type FieldReport struct {
	Version string
	Missing []string // named, so the UI can say which field is absent
}

// AssertFields runs the hub's own fields against a real pane and requires each
// to come back non-empty.
func AssertFields(ctx context.Context, r Runner, t Target, paneID string) (FieldReport, error) {
	return assertFieldsWith(ctx, r, t, paneID, requiredFields)
}

func assertFieldsWith(ctx context.Context, r Runner, t Target, paneID string, fields []string) (FieldReport, error) {
	if paneID == "" {
		return FieldReport{}, fmt.Errorf("no pane on %s to verify fields against", t.Label)
	}
	parts := make([]string, len(fields))
	for i, f := range fields {
		parts[i] = "#{" + f + "}"
	}
	res, err := r.Run(ctx, t, "display", "-p", "-t", paneID, strings.Join(parts, "|"))
	if err != nil {
		return FieldReport{}, err
	}
	if res.RC != 0 {
		return FieldReport{}, fmt.Errorf("display rc=%d: %s", res.RC, res.Stderr)
	}
	line := strings.TrimRight(res.Stdout, "\n")
	vals := strings.Split(line, "|")
	if len(vals) != len(fields) {
		return FieldReport{}, fmt.Errorf("assertion returned %d values for %d fields: %q",
			len(vals), len(fields), line)
	}
	rep := FieldReport{}
	for i, f := range fields {
		if strings.TrimSpace(vals[i]) == "" {
			rep.Missing = append(rep.Missing, f)
			continue
		}
		if f == "version" {
			rep.Version = vals[i]
		}
	}
	return rep, nil
}
