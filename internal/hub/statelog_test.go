package hub

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/DawnBreather/tmux-hub/internal/registry"
	"github.com/DawnBreather/tmux-hub/internal/state"
)

func TestStateLogWritesOnlyTransitionsWithDurations(t *testing.T) {
	path := filepath.Join(t.TempDir(), "states.jsonl")
	l, err := OpenStateLog(path)
	if err != nil {
		t.Fatalf("OpenStateLog: %v", err)
	}
	t0 := time.Unix(1786450000, 0)
	pane := func(s state.State) []registry.Pane {
		return []registry.Pane{{Host: "local", PaneID: "%0", Session: "live1",
			Command: "claude", ClassifiedState: s}}
	}
	l.Observe(pane(state.Works), t0)
	l.Observe(pane(state.Works), t0.Add(2*time.Second)) // unchanged: no line
	l.Observe(pane(state.Works), t0.Add(4*time.Second)) // unchanged: no line
	l.Observe(pane(state.Needs), t0.Add(30*time.Second))
	l.Observe(pane(state.Idle), t0.Add(45*time.Second))
	if err := l.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer f.Close()
	var recs []record
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var r record
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			t.Fatalf("bad line %q: %v", sc.Text(), err)
		}
		recs = append(recs, r)
	}
	if len(recs) != 3 {
		t.Fatalf("got %d records, want 3 (one per transition, none for the unchanged ticks)", len(recs))
	}
	if recs[0].From != "" || recs[0].To != "works" {
		t.Errorf("first record = %+v, want an entry into works with no prior", recs[0])
	}
	if recs[1].From != "works" || recs[1].To != "needs" || recs[1].HeldSec != 30 {
		t.Errorf("second record = %+v, want works->needs held 30s", recs[1])
	}
	if recs[2].From != "needs" || recs[2].To != "idle" || recs[2].HeldSec != 15 {
		t.Errorf("third record = %+v, want needs->idle held 15s", recs[2])
	}
	if recs[0].Session != "live1" || recs[0].Command != "claude" {
		t.Errorf("records must carry what the pane was: %+v", recs[0])
	}
}

// A nil log must be usable so callers need no branch.
func TestNilStateLogIsSafe(t *testing.T) {
	var l *StateLog
	l.Observe([]registry.Pane{{Host: "h", PaneID: "%0"}}, time.Now())
	if err := l.Close(); err != nil {
		t.Fatalf("Close on nil = %v", err)
	}
}
