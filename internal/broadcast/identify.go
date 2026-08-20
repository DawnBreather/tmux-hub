package broadcast

import (
	"context"
	"sync"

	"github.com/DawnBreather/tmux-hub/internal/proc"
	"github.com/DawnBreather/tmux-hub/internal/tmux"
)

// Keeper joins the process walk to the token: it decides which panes run an agent
// and keeps the token of every pane that may be a target in step with that answer.
//
// It is the only producer of the values Send's guard compares, and the reason it
// exists as a type rather than as a loop in the UI is that both halves have to
// move together. A stamp without a fresh identification means "this pane was an
// agent once"; an identification without a stamp cannot be sent to at all.
type Keeper struct {
	st *Stamper

	// identified is read on the UI thread (the confirmation rule asks whether a
	// target is an agent NOW) and written by the tick's goroutine, so it is
	// guarded rather than merely map-typed.
	mu         sync.Mutex
	identified map[paneKey]bool

	// sessions maps pane to claude session id for hub-created panes. Task 14
	// (restart) needs the session id to build `claude --resume <uuid>`.
	sessions map[paneKey]string
}

type paneKey struct{ host, paneID string }

func NewKeeper(st *Stamper) *Keeper {
	return &Keeper{
		st:         st,
		identified: map[paneKey]bool{},
		sessions:   map[paneKey]string{},
	}
}

// PaneRef is one pane as the identity walk sees it.
type PaneRef struct {
	PaneID  string
	PanePID int
	// Stamp says this pane may be a target, so its token must be current. Only
	// panes the user has selected are stamped: the token is what makes a send
	// possible at all, and writing one onto every pane on the server would leave a
	// hub option on panes nobody chose.
	Stamp bool
}

// Refresh walks the processes behind one host's panes and brings each pane marked
// Stamp into step with the answer: a fresh token for as long as an agent is there,
// and `set -pu` the moment the walk stops finding one (docs/design.md §7).
//
// Rotating on every tick is what makes the guard mean "identified as an agent no
// more than one tick ago". It costs one tmux write per SELECTED pane per tick, and
// deliberately not one batched invocation for all of them: a `;`-batch aborts at
// its first failing sub-command, so one vanished pane would silently skip the
// stamps of every pane after it.
//
// A walk that fails identifies nothing. That is the safe direction — an
// unidentified target is one the confirmation dialog names and asks about, where a
// wrongly identified one is written to in silence — and the error is returned for
// the caller to surface rather than swallowed into a false "identified".
//
// The host is t.Label and is not a separate argument, because the Stamper keys its
// tokens on the same field: two names for one host is how a token gets written
// under one key and looked up under another, which reads as "the hub holds no
// identity token for this pane" for every send.
func (k *Keeper) Refresh(ctx context.Context, w proc.Walker, t tmux.Target, panes []PaneRef) error {
	host := t.Label
	pids := make([]int, 0, len(panes))
	for _, p := range panes {
		if p.PanePID > 0 {
			pids = append(pids, p.PanePID)
		}
	}

	var found map[int]int
	err := proc.ErrNoTransport
	if w != nil {
		found, err = w.Walk(ctx, pids)
	}

	for _, p := range panes {
		agent := p.PanePID > 0 && found[p.PanePID] > 0
		k.set(host, p.PaneID, agent)
		if !p.Stamp {
			continue
		}
		if agent {
			if _, serr := k.st.Stamp(ctx, t, p.PaneID); serr != nil && err == nil {
				err = serr
			}
			continue
		}
		// No agent here now. Only unstamp a pane the hub actually holds a token
		// for: `set -pu` on every unidentified pane every tick would be a write per
		// pane per tick for no gain, and the guard already refuses a pane the hub
		// has no token for.
		if _, held := k.st.Token(host, p.PaneID); held {
			if uerr := k.st.Unstamp(ctx, t, p.PaneID); uerr != nil && err == nil {
				err = uerr
			}
		}
	}
	return err
}

// Identified reports whether the last walk found an agent at or under this pane.
//
// "Last walk" and not "ever": the answer must be recomputed rather than cached,
// because in the commoner launch shape `pane_pid` is the shell and does not change
// as the agent comes and goes, so an unchanged pid is not evidence the agent is
// still there.
//
// A nil Keeper identifies nothing, so a caller needs no branch — and the direction
// is the safe one: unidentified means the confirmation dialog asks.
func (k *Keeper) Identified(host, paneID string) bool {
	if k == nil {
		return false
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.identified[paneKey{host, paneID}]
}

// Session returns the Claude session ID for a pane that was Adopted, or empty if
// the pane is not known or was not adopted (i.e. it was identified through a
// process walk). A restart needs the session ID to build `claude --resume <uuid>`.
func (k *Keeper) Session(host, paneID string) (string, bool) {
	if k == nil {
		return "", false
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	id, ok := k.sessions[paneKey{host, paneID}]
	return id, ok
}

// AdoptedSession is one pane's half of the join: the pane, and the Claude session running in it.
type AdoptedSession struct {
	Host      string
	PaneID    string
	SessionID string
}

// SessionSnapshot copies every adopted pane's session out, so the caller can apply the join
// WITHOUT holding this lock.
//
// The obvious alternative — an `EachSession(func(...))` iterator — would run the caller's body with
// k.mu held, and the only caller takes the registry's lock inside it. The paint path already takes
// those two in the opposite order (`reg.Panes()` then `keeper.Identified()`), so the nested form is
// a deadlock in waiting rather than a matter of style.
//
// A pane identified by a process WALK has no session id here and so is absent: there is nothing to
// join it by. A nil Keeper answers nil, so no caller needs a branch.
func (k *Keeper) SessionSnapshot() []AdoptedSession {
	if k == nil {
		return nil
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	out := make([]AdoptedSession, 0, len(k.sessions))
	for key, id := range k.sessions {
		out = append(out, AdoptedSession{Host: key.host, PaneID: key.paneID, SessionID: id})
	}
	return out
}

// ForgetPane drops what the hub believes about one specific pane. Called after
// respawn-pane to invalidate identity, because measured: respawn-pane -k keeps
// pane_id and the @hub_* option while changing pane_pid. The stamp survives and
// the guarded write path still trusts the pane, but the process behind it is
// different.
func (k *Keeper) ForgetPane(host, paneID string) {
	if k == nil {
		return
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	key := paneKey{host, paneID}
	delete(k.identified, key)
	delete(k.sessions, key)
}

// Forget drops what the hub believes about one host's panes, so a host that
// stopped answering cannot leave a stale "yes, this is an agent" behind.
func (k *Keeper) Forget(host string) {
	k.mu.Lock()
	defer k.mu.Unlock()
	for kk := range k.identified {
		if kk.host == host {
			delete(k.identified, kk)
			delete(k.sessions, kk)
		}
	}
}

func (k *Keeper) set(host, paneID string, v bool) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if v {
		k.identified[paneKey{host, paneID}] = true
		return
	}
	delete(k.identified, paneKey{host, paneID})
}

// Adopt marks a pane as identified without a process walk. For a pane the hub
// created, both halves of its identity are known at birth (the pane id from
// `new-window -P`, the session id from the hub's own `NewSessionID`), so the
// walk — where this project's one Critical defect lived — is never needed.
//
// The claudeSession is stored for Task 14 (restart), which needs it to build
// `claude --resume <uuid>`.
func (k *Keeper) Adopt(host, paneID, claudeSession string) {
	k.mu.Lock()
	defer k.mu.Unlock()
	key := paneKey{host, paneID}
	k.identified[key] = true
	k.sessions[key] = claudeSession
}
