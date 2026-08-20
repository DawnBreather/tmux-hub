package hub

import (
	"encoding/json"
	"os"
	"sync"
	"time"

	"github.com/DawnBreather/tmux-hub/internal/registry"
	"github.com/DawnBreather/tmux-hub/internal/state"
)

// StateLog records every state TRANSITION to a JSONL file.
//
// It exists because several thresholds in this design are guesses: QuietAfter is
// 90 s, the poll interval is 1.2 s, and the freshness floor is 4 s, all chosen
// from the armchair. What settles them is how long a real session actually sits
// in each state — how long an agent works before it wants you, how long "quiet"
// runs before it means "hung" rather than "finished" — and that is a property of
// the user's work, not of tmux.
//
// Transitions rather than samples: a sample per tick would be ~72 000 lines a
// day of mostly-unchanged rows, while transitions are what the durations are
// computed from.
type StateLog struct {
	mu   sync.Mutex
	f    *os.File
	last map[registry.Key]entry
}

type entry struct {
	State state.State
	Since time.Time
}

type record struct {
	At      string  `json:"at"`
	Host    string  `json:"host"`
	Pane    string  `json:"pane"`
	Session string  `json:"session"`
	Command string  `json:"command"`
	From    string  `json:"from"`
	To      string  `json:"to"`
	HeldSec float64 `json:"held_sec"`
}

// OpenStateLog appends to path, creating it if needed.
func OpenStateLog(path string) (*StateLog, error) {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	return &StateLog{f: f, last: map[registry.Key]entry{}}, nil
}

func (l *StateLog) Close() error {
	if l == nil {
		return nil
	}
	return l.f.Close()
}

// Observe writes a line for every pane whose state differs from the last one
// seen, carrying how long the previous state was held. A nil log does nothing,
// so the caller needs no branch.
func (l *StateLog) Observe(panes []registry.Pane, now time.Time) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()

	enc := json.NewEncoder(l.f)
	for _, p := range panes {
		k := registry.Key{Host: p.Host, PaneID: p.PaneID}
		prev, seen := l.last[k]
		if seen && prev.State == p.State() {
			continue
		}
		from := ""
		held := 0.0
		if seen {
			from = prev.State.String()
			held = now.Sub(prev.Since).Seconds()
		}
		_ = enc.Encode(record{
			At: now.UTC().Format(time.RFC3339), Host: p.Host, Pane: p.PaneID,
			Session: p.Session, Command: p.Command,
			From: from, To: p.State().String(), HeldSec: held,
		})
		l.last[k] = entry{State: p.State(), Since: now}
	}
}
