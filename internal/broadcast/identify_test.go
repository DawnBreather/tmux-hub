package broadcast

import (
	"context"
	"errors"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/DawnBreather/tmux-hub/internal/proc"
	"github.com/DawnBreather/tmux-hub/internal/tmux"
)

// fakeWalk stands in for the process walk. The walk itself is proc's business and
// tested there; what matters here is that the Keeper turns its answer into the
// right tmux writes.
type fakeWalk struct {
	agents map[int]int
	err    error
}

func (f fakeWalk) Walk(_ context.Context, _ []int) (map[int]int, error) {
	return f.agents, f.err
}

// panePIDs reads each pane's pane_pid off a live server, because the Keeper keys
// the walk on it.
func panePIDs(t *testing.T, tgt tmux.Target, ids []string) map[string]int {
	t.Helper()
	out, err := exec.Command("tmux", "-S", tgt.Socket, "list-panes", "-a",
		"-F", "#{pane_id} #{pane_pid}").Output()
	if err != nil {
		t.Fatalf("list-panes: %v", err)
	}
	got := map[string]int{}
	for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		f := strings.Fields(l)
		if len(f) != 2 {
			continue
		}
		pid, err := strconv.Atoi(f[1])
		if err != nil {
			t.Fatalf("pane_pid %q: %v", f[1], err)
		}
		got[f[0]] = pid
	}
	for _, id := range ids {
		if got[id] == 0 {
			t.Fatalf("no pane_pid for %s", id)
		}
	}
	return got
}

// option reads a pane option straight off the server, so the assertion is on tmux
// rather than on the hub's own memory of what it wrote.
func option(t *testing.T, tgt tmux.Target, paneID, name string) string {
	t.Helper()
	out, err := exec.Command("tmux", "-S", tgt.Socket, "display", "-p",
		"-t", paneID, "#{"+name+"}").Output()
	if err != nil {
		t.Fatalf("display: %v", err)
	}
	return strings.TrimSpace(string(out))
}

// The whole point of the Keeper: a pane the walk identifies gets a token, and the
// moment the walk stops finding an agent the option is REMOVED, so the guard
// refuses by construction rather than on the strength of a stale token.
func TestKeeperStampsAnIdentifiedPaneAndUnstampsWhenTheAgentGoes(t *testing.T) {
	inst := Instance("k1")
	tgt, ids := liveServer(t, 2)
	st := NewStamper(tmux.NewExec(10*time.Second), inst)
	k := NewKeeper(st)
	pids := panePIDs(t, tgt, ids)
	ctx := context.Background()

	refs := []PaneRef{
		{PaneID: ids[0], PanePID: pids[ids[0]], Stamp: true},
		{PaneID: ids[1], PanePID: pids[ids[1]], Stamp: true},
	}

	// Only the first pane runs an agent.
	walk := fakeWalk{agents: map[int]int{pids[ids[0]]: pids[ids[0]] + 1, pids[ids[1]]: 0}}
	if err := k.Refresh(ctx, walk, tgt, refs); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if !k.Identified(tgt.Label, ids[0]) {
		t.Error("the pane the walk found an agent in is not identified")
	}
	if k.Identified(tgt.Label, ids[1]) {
		t.Error("a pane with no agent was identified")
	}
	tok, ok := st.Token(tgt.Label, ids[0])
	if !ok || tok == "" {
		t.Fatal("no token was recorded for the identified pane")
	}
	if got := option(t, tgt, ids[0], inst.Option()); got != tok {
		t.Errorf("the pane carries %q, the hub remembers %q", got, tok)
	}
	if got := option(t, tgt, ids[1], inst.Option()); got != "" {
		t.Errorf("an unidentified pane was stamped with %q", got)
	}

	// A fresh token every tick: a pane-bound token proves PANE identity, so its
	// only claim to describing the process is that it was written after a walk that
	// found one.
	if err := k.Refresh(ctx, walk, tgt, refs); err != nil {
		t.Fatalf("Refresh again: %v", err)
	}
	tok2, _ := st.Token(tgt.Label, ids[0])
	if tok2 == tok {
		t.Error("the token was not rotated, so the guard cannot mean 'identified recently'")
	}

	// The agent exits. The option must go, or a prompt lands at a shell prompt with
	// the guard reporting success.
	if err := k.Refresh(ctx, fakeWalk{agents: map[int]int{}}, tgt, refs); err != nil {
		t.Fatalf("Refresh after exit: %v", err)
	}
	if k.Identified(tgt.Label, ids[0]) {
		t.Error("a pane whose agent exited is still identified")
	}
	if _, held := st.Token(tgt.Label, ids[0]); held {
		t.Error("the hub still holds a token for a pane with no agent")
	}
	if got := option(t, tgt, ids[0], inst.Option()); got != "" {
		t.Errorf("the option survived the agent: %q", got)
	}
}

// Identification is for every pane the hub can see; a token is only for panes that
// may be written to. Stamping every pane on the server would leave a hub option on
// panes nobody chose.
func TestKeeperStampsOnlyPanesMarkedForIt(t *testing.T) {
	inst := Instance("k2")
	tgt, ids := liveServer(t, 2)
	st := NewStamper(tmux.NewExec(10*time.Second), inst)
	k := NewKeeper(st)
	pids := panePIDs(t, tgt, ids)

	walk := fakeWalk{agents: map[int]int{pids[ids[0]]: 7, pids[ids[1]]: 8}}
	err := k.Refresh(context.Background(), walk, tgt, []PaneRef{
		{PaneID: ids[0], PanePID: pids[ids[0]], Stamp: true},
		{PaneID: ids[1], PanePID: pids[ids[1]]},
	})
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if !k.Identified(tgt.Label, ids[1]) {
		t.Error("a pane that is not a target must still be identified — that is what " +
			"makes 'was an agent at selection' answerable")
	}
	if _, held := st.Token(tgt.Label, ids[1]); held {
		t.Error("a pane nobody selected was stamped")
	}
	if got := option(t, tgt, ids[1], inst.Option()); got != "" {
		t.Errorf("an unselected pane carries %q", got)
	}
}

// A host with no way to run the walk identifies nothing and SAYS so. Silence would
// be indistinguishable from "walked and found no agents", and the difference
// decides whether the user is told why every target needs confirming.
func TestKeeperWithoutAWalkerIdentifiesNothingAndReportsWhy(t *testing.T) {
	tgt, ids := liveServer(t, 1)
	st := NewStamper(tmux.NewExec(10*time.Second), Instance("k3"))
	k := NewKeeper(st)

	err := k.Refresh(context.Background(), nil, tgt,
		[]PaneRef{{PaneID: ids[0], PanePID: 1234, Stamp: true}})
	if !errors.Is(err, proc.ErrNoTransport) {
		t.Errorf("Refresh with no walker = %v, want ErrNoTransport", err)
	}
	if k.Identified(tgt.Label, ids[0]) {
		t.Error("a pane on an unwalkable host was identified")
	}
}

// A failed walk must not leave a previous "yes" standing: the answer means
// "identified no more than one tick ago", so a tick that could not look identifies
// nothing and the token goes with it.
func TestKeeperDropsIdentityWhenTheWalkFails(t *testing.T) {
	inst := Instance("k4")
	tgt, ids := liveServer(t, 1)
	st := NewStamper(tmux.NewExec(10*time.Second), inst)
	k := NewKeeper(st)
	pids := panePIDs(t, tgt, ids)
	refs := []PaneRef{{PaneID: ids[0], PanePID: pids[ids[0]], Stamp: true}}
	ctx := context.Background()

	if err := k.Refresh(ctx, fakeWalk{agents: map[int]int{pids[ids[0]]: 42}}, tgt, refs); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if !k.Identified(tgt.Label, ids[0]) {
		t.Fatal("setup failed: the pane was not identified")
	}

	boom := errors.New("ssh: connection closed")
	if err := k.Refresh(ctx, fakeWalk{err: boom}, tgt, refs); !errors.Is(err, boom) {
		t.Errorf("Refresh = %v, want the walk's own error", err)
	}
	if k.Identified(tgt.Label, ids[0]) {
		t.Error("a failed walk left the pane identified")
	}
	if _, held := st.Token(tgt.Label, ids[0]); held {
		t.Error("a failed walk left a token behind, so a send would still be allowed")
	}
	if got := option(t, tgt, ids[0], inst.Option()); got != "" {
		t.Errorf("the option survived a failed walk: %q", got)
	}
}

// Forget is what a host going away must do to the hub's beliefs about it.
func TestForgetDropsOneHostOnly(t *testing.T) {
	k := NewKeeper(NewStamper(tmux.NewExec(time.Second), Instance("k5")))
	k.set("nuc", "%0", true)
	k.set("local", "%0", true)
	k.Forget("nuc")
	if k.Identified("nuc", "%0") {
		t.Error("Forget left the host it was called for")
	}
	if !k.Identified("local", "%0") {
		t.Error("Forget took another host's panes with it")
	}
}

// fakeRunner is a minimal Runner for unit tests that don't need real tmux.
type fakeRunner struct{}

func (f *fakeRunner) Run(_ context.Context, _ tmux.Target, _ ...string) (tmux.Result, error) {
	return tmux.Result{RC: 0}, nil
}

func TestAdoptIdentifiesWithoutAWalk(t *testing.T) {
	// The payoff of dictating the session id: for a pane the hub created, the
	// pane↔session binding is KNOWN. The process-tree walk — where this
	// project's one Critical defect lived (a forwarded socket walked against
	// the LOCAL process table, 97 of 3117 local pids answering "agent here") —
	// is never consulted for it.
	k := NewKeeper(NewStamper(&fakeRunner{}, Instance("t")))
	k.Adopt("nuc", "%7", "7007b23f-1599-4efa-81c5-4195621cc273")

	if !k.Identified("nuc", "%7") {
		t.Fatal("an adopted pane must be identified")
	}
}

func TestAdoptIsForgottenWithItsHost(t *testing.T) {
	// Otherwise a host that goes away leaves an identification behind, and the
	// write path would trust a pane on a server the hub can no longer see.
	k := NewKeeper(NewStamper(&fakeRunner{}, Instance("t")))
	k.Adopt("nuc", "%7", "session-a")
	k.Adopt("local", "%8", "session-b")

	k.Forget("nuc")

	if k.Identified("nuc", "%7") {
		t.Error("Forget left an adopted pane behind")
	}
	if !k.Identified("local", "%8") {
		t.Error("Forget took another host's adopted pane with it")
	}
}

func TestAdoptStoresSessionID(t *testing.T) {
	// Task 14 (restart) needs the session id to build `claude --resume <uuid>`.
	k := NewKeeper(NewStamper(&fakeRunner{}, Instance("t")))
	k.Adopt("nuc", "%7", "7007b23f-1599-4efa-81c5-4195621cc273")

	// Verify the session is stored
	k.mu.Lock()
	session := k.sessions[paneKey{"nuc", "%7"}]
	k.mu.Unlock()

	if session != "7007b23f-1599-4efa-81c5-4195621cc273" {
		t.Errorf("session = %q, want %q", session, "7007b23f-1599-4efa-81c5-4195621cc273")
	}

	// Verify Forget clears the session
	k.Forget("nuc")

	k.mu.Lock()
	_, exists := k.sessions[paneKey{"nuc", "%7"}]
	k.mu.Unlock()

	if exists {
		t.Error("Forget did not clear the session mapping")
	}
}
