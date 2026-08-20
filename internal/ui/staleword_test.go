package ui

import (
	"strings"
	"testing"
	"time"

	"github.com/DawnBreather/tmux-hub/internal/registry"
	"github.com/DawnBreather/tmux-hub/internal/state"
)

// `stale` MUST NOT MASK A FRESH LISTING WORD, because the two come from different producers and only
// one of them has stopped answering.
//
// `Stale` is set when the TMUX poll for a host fails. The listing is a separate producer over a
// separate path, and measured on the live fleet it was still reporting `working` with a pid for a
// session whose host's tmux socket had gone quiet. The row used to be the only place that could say
// the host was unreachable, so it said `stale` unconditionally; the host line and the tile header say
// it now, and the row's one column is better spent on the fact the operator came for.
//
// The pair of poles is the whole test: a stale row with NO fresh word must still read `stale`, or
// this change would have removed the signal instead of ranking it.
// Every case sets ClassifiedState explicitly: `state.Needs` is the enum's ZERO value, so leaving it
// out claims the pane is asking a permission question — which `stateAt` refuses to override, by
// design, and the row then reads `needs` for reasons that have nothing to do with staleness.
func TestStaleDoesNotMaskAFreshListingWord(t *testing.T) {
	now := time.Now()
	for _, c := range []struct {
		name string
		pane registry.Pane
		want string
	}{
		{
			// The reported case: the tmux tunnel is down, the listing said `working` moments ago.
			name: "a stale row whose listing word is fresh reads the session's state",
			pane: registry.Pane{Kind: registry.KindPane, Host: "nuc", PaneID: "%4",
				Session: "ansible-e04c609c", Command: "sh", Stale: true, ClassifiedState: state.Idle,
				AgentState: state.Works, AgentWord: "working", AgentPID: 480403, AgentSeenAt: now},
			want: "works",
		},
		{
			name: "a stale row with no listing word at all still reads stale",
			pane: registry.Pane{Kind: registry.KindPane, Host: "nuc", PaneID: "%5",
				Session: "plain", Command: "sh", Stale: true, ClassifiedState: state.Idle},
			want: "stale",
		},
		{
			// Eleven minutes is past the freshness window, so the fact has expired and the row is
			// back to having nothing better to say than that its host is gone.
			name: "a stale row whose listing word has expired reads stale",
			pane: registry.Pane{Kind: registry.KindPane, Host: "nuc", PaneID: "%6",
				Session: "expired", Command: "sh", Stale: true, ClassifiedState: state.Idle,
				AgentState: state.Works, AgentWord: "working",
				AgentSeenAt: now.Add(-11 * time.Minute)},
			want: "stale",
		},
		{
			name: "a row that is not stale is unaffected",
			pane: registry.Pane{Kind: registry.KindPane, Host: "nuc", PaneID: "%7",
				Session: "live", Command: "sh", ClassifiedState: state.Needs},
			want: "needs",
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := stateWord(c.pane); got != c.want {
				t.Errorf("stateWord = %q, want %q (stale=%v fresh=%v)",
					got, c.want, c.pane.Stale, c.pane.AgentFactFresh())
			}
		})
	}
}

// AND IT REACHES THE SCREEN, which the unit above cannot show: a word is only a fix once the frame
// carries it.
func TestTheScreenShowsAStaleRowsFreshState(t *testing.T) {
	now := time.Now()
	row := registry.Pane{Kind: registry.KindPane, Host: "nuc", PaneID: "%4",
		Session: "ansible-e04c609c", Window: "ansible", Command: "sh", Stale: true,
		ClassifiedState: state.Idle,
		Path:            "/home/dev/lab/streams/ansible",
		AgentState:      state.Works, AgentWord: "working", AgentPID: 480403, AgentSeenAt: now}

	screen := base(t, 100, 24, row).View()
	if !strings.Contains(screen, "works") {
		t.Errorf("the screen does not say `works` for a row whose listing reported it "+
			"working:\n%s", screen)
	}
	if strings.Contains(screen, "stale") {
		t.Errorf("the screen still says `stale`, so the fresh fact is masked on the surface the "+
			"operator reads:\n%s", screen)
	}
}
