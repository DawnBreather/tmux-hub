package ui

import (
	"strings"
	"testing"

	"github.com/DawnBreather/tmux-hub/internal/hub"
	"github.com/DawnBreather/tmux-hub/internal/lines"
	"github.com/DawnBreather/tmux-hub/internal/project"
	"github.com/DawnBreather/tmux-hub/internal/registry"
	"github.com/DawnBreather/tmux-hub/internal/state"
)

// TWO PROPERTIES, EVERY SCREEN, EVERY WIDTH. Neither needs to know anything about a layout,
// which is what makes them worth more than the defects they find:
//
//   - NOTHING OVERFLOWS. No row may be wider than the terminal.
//   - NOTHING GOES BACKWARDS. Widening the terminal must never show LESS. A layout that
//     composes a part at one width and renders it at another is non-monotonic, and that is a
//     class rather than a bug: measured on the footer, where a wider terminal showed FEWER
//     hosts because the fleet string was built for the whole row and then fitted as one part.
//
// The second is three lines per screen and it is the reason this file exists. §16 commits to
// three width bands; a per-band branch nobody walked is exactly where the footer's defect hid.
//
// `tokens` are the facts each screen is supposed to carry. Counting how many are PRESENT is a
// coarse proxy for "how much is shown", and coarse is the point: a precise measure would need
// to know the layout, and then it would only test the layout it knows.
type widthCase struct {
	name   string
	tokens []string
	render func(cols int) string
}

func monotonicCases(t *testing.T) []widthCase {
	t.Helper()
	hosts := []hub.Host{
		{Label: "local", Status: hub.Up},
		{Label: "nuc", Status: hub.DegradedFormat, Reason: "window_activity came back blank"},
		{Label: "dead", Status: hub.Down, Reason: "no live ssh master"},
	}
	fleet := []registry.Pane{
		{Kind: registry.KindPane, Host: "local", Session: "api", Window: "fix", PaneID: "%0",
			Command: "claude", ClassifiedState: state.Needs, Path: "/w/api"},
		{Kind: registry.KindPane, Host: "nuc", Session: "deploy", Window: "audit", PaneID: "%1",
			Command: "claude", ClassifiedState: state.Error, Path: "/w/deploy"},
		{Kind: registry.KindAgent, Host: "nuc", Session: "a-long-agent-session-name",
			SessionID: "id-1", ClassifiedState: state.Works, Path: "/w/deploy"},
	}
	base := func(cols int) Frame {
		return Frame{Panes: fleet, Hosts: hosts, Width: cols, Height: 24,
			Note: "sent to 2 panes, 1 refused: no bracketed paste on that pane",
			Hint: "a → a window with the attach, C-b C-b d leaves the inner tmux"}
	}
	hostTokens := []string{"local", "nuc", "dead"}
	paneTokens := []string{"%0", "%1"}

	pickerRows := []PickerRow{
		{Alias: "nuc", Kept: true, Usable: true, Version: "3.2a"},
		{Alias: "staging-2", Kept: true, Usable: true, Version: "3.2a"},
		{Alias: "orbits.example", Usable: false,
			Reason: "not a shell host — this is a git remote, so leave it off"},
	}

	return []widthCase{
		{"dashboard", append(append([]string{}, hostTokens...), paneTokens...),
			func(cols int) string { return Render(base(cols)) }},
		{"compose", hostTokens,
			func(cols int) string { return RenderCompose(base(cols), "a prompt being typed") }},
		{"confirm", hostTokens,
			func(cols int) string {
				return RenderConfirm(base(cols), ConfirmView{
					Action: "write", Note: "one target changed since you chose it"})
			}},
		{"naming a session", []string{"name this session", "now:", "enter"},
			func(cols int) string {
				return RenderNaming(base(cols), namingForm{subject: fleet[0],
					input: Composer{}, reason: "that name is already taken elsewhere"})
			}},
		{"project list", []string{"api", "deploy", "of 1", "of 2"},
			func(cols int) string {
				rows := project.Summarise(project.Rules{}, fleet)
				return strings.Join(RenderProjects(Frame{Width: cols, Height: 24,
					Note: "a note about something just pressed",
					Projects: ProjectView{Rows: rows,
						Warn:    "projects.toml: line 4: expected quoted string",
						Pinned:  map[string]bool{rows[0].Group.ID: true},
						FavNote: "1 favourite project"}}), "\n")
			}},
		{"picker", []string{"nuc", "staging-2", "orbits.example"},
			func(cols int) string {
				return strings.Join(RenderPicker(pickerRows, cols, 24, 0), "\n")
			}},
	}
}

func TestNoScreenOverflowsItsTerminal(t *testing.T) {
	for _, c := range monotonicCases(t) {
		for cols := 20; cols <= 220; cols += 3 {
			for i, line := range strings.Split(c.render(cols), "\n") {
				if w := lines.Width(line); w > cols {
					t.Errorf("%s at %d cols: row %d is %d wide: %q", c.name, cols, i, w, line)
				}
			}
		}
	}
}

// The instrument the footer's defect earned. It knows nothing about any layout: it only refuses
// a width that shows less than a narrower one.
func TestNoScreenShowsLessOnAWiderTerminal(t *testing.T) {
	for _, c := range monotonicCases(t) {
		prev, prevCols := -1, 0
		for cols := 40; cols <= 220; cols += 3 {
			out := c.render(cols)
			shown := 0
			for _, tok := range c.tokens {
				if strings.Contains(out, tok) {
					shown++
				}
			}
			if prev >= 0 && shown < prev {
				t.Errorf("%s: %d cols shows %d of %d facts where %d cols showed %d — widening "+
					"the terminal must never show LESS:\n%s",
					c.name, cols, shown, len(c.tokens), prevCols, prev, out)
			}
			prev, prevCols = shown, cols
		}
	}
}
