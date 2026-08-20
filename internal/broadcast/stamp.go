package broadcast

import (
	"context"
	"fmt"
	"sync"

	"github.com/DawnBreather/tmux-hub/internal/tmux"
)

// Stamper owns the per-pane identity tokens.
//
// The token exists because existence is not identity: #{pane_id} restarts at %0
// when a server restarts, so a stale %0 EXISTS and points at a different session.
// Over ssh that is invisible — the tunnel and the master survive a remote tmux
// restart and the poll returns a full pane list — so a pre-flight that only checks
// existence delivers the prompt to the wrong pane.
type Stamper struct {
	run  tmux.Runner
	inst Instance

	mu     sync.Mutex
	tokens map[string]string // "host\x00%NN" -> token
}

func NewStamper(r tmux.Runner, i Instance) *Stamper {
	return &Stamper{run: r, inst: i, tokens: map[string]string{}}
}

func key(host, paneID string) string { return host + "\x00" + paneID }

// Stamp writes a fresh token onto one pane and remembers it.
//
// `-p` is not optional. Measured: `set -t <pane>` with no -p lands at SESSION
// scope, and a pane that was never stamped then resolves the value and passes the
// guard — one missing character is a session-wide fail-open. (`-g` would be
// server-wide, which is worse.)
//
// A fresh token every time is equally load-bearing: a pane-bound token proves
// PANE identity, not PROCESS identity. `respawn-pane` keeps the id, the token and
// even `pane_pid` while replacing the process, and the commoner case needs no tmux
// command at all, because `pane_pid` is the shell and does not change as the agent
// comes and goes. Rotating on every re-stamp is what makes the guard mean
// "identified as an agent no more than one tick ago".
func (s *Stamper) Stamp(ctx context.Context, t tmux.Target, paneID string) (string, error) {
	tok := NewToken()
	res, err := s.run.Run(ctx, t, "set", "-p", "-t", paneID, s.inst.Option(), tok)
	if err != nil {
		return "", err
	}
	if res.RC != 0 {
		return "", fmt.Errorf("broadcast: cannot stamp %s on %s: %s",
			paneID, t.Label, res.Stderr)
	}
	s.mu.Lock()
	s.tokens[key(t.Label, paneID)] = tok
	s.mu.Unlock()
	return tok, nil
}

// Unstamp removes the option and forgets the token, so the guard refuses by
// construction.
//
// A non-zero rc is deliberately not an error: the pane being gone is the
// commonest reason to unstamp — the agent exited — and treating the routine case
// as a failure would put an error on screen every time someone finished a task.
func (s *Stamper) Unstamp(ctx context.Context, t tmux.Target, paneID string) error {
	s.mu.Lock()
	delete(s.tokens, key(t.Label, paneID))
	s.mu.Unlock()

	_, err := s.run.Run(ctx, t, "set", "-pu", "-t", paneID, s.inst.Option())
	return err
}

// Token returns what the hub believes is stamped on a pane. The guard is built
// from this value, so a pane the hub has no token for cannot be sent to at all.
func (s *Stamper) Token(host, paneID string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tok, ok := s.tokens[key(host, paneID)]
	return tok, ok
}
