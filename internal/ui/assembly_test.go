// The ASSEMBLY floor for internal/ui: it builds the model the way Run does — through
// `build`, with a fake tmux underneath — and drives it end to end.
//
// It is deliberately NOT the same job as `cmd/tmux-hub/wiring_test.go`, which this file was
// called `wiring_test.go` until it was renamed for exactly that confusion. That one is a
// REACHABILITY floor: "is this package linked from the program", a question only package
// `main` can ask. This one asks whether a model assembled the production way actually WORKS,
// which is where the defect it exists for was caught — every ui test used to hand-build a
// `model{…}` with just the fields it needed, and a model whose sender could not send
// satisfied every one of them.
//
// Two files named `wiring` doing different jobs is how one of them gets deleted as a
// duplicate by someone in a hurry.
package ui

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/DawnBreather/tmux-hub/internal/broadcast"
	"github.com/DawnBreather/tmux-hub/internal/history"
	"github.com/DawnBreather/tmux-hub/internal/hub"
	"github.com/DawnBreather/tmux-hub/internal/proc"
	"github.com/DawnBreather/tmux-hub/internal/registry"
	"github.com/DawnBreather/tmux-hub/internal/state"
	"github.com/DawnBreather/tmux-hub/internal/tmux"

	"github.com/DawnBreather/tmux-hub/internal/project"
)

// fakeTmux is a tmux small enough to test against and faithful in the four things
// the write path depends on: a pane option is per PANE, `if -F` runs one branch or
// the other, a paste appends to the pane's screen, and a `display -p` template is
// EXPANDED rather than echoed.
//
// It interprets the sub-commands of a chain by their verb rather than matching the
// chain as a whole, so it cannot silently agree with a hub that reordered them.
type fakeTmux struct {
	mu       sync.Mutex
	calls    []string          // every invocation, joined, in order
	opts     map[string]string // pane -> the value `set -p` wrote
	screen   map[string]string // pane -> what it shows
	keys     map[string][]string
	bufs     map[string][]byte
	activity int64
	// serverPID and startTime are what `#{pid}` and `#{start_time}` expand to, i.e.
	// this fake server's own epoch. They are expanded by name rather than left to
	// the catch-all below, because the catch-all answers with a pane option: the
	// epoch would read as ":" for every server, so `decidePossession` would find no
	// locality match and the jump path — the one screen §20 exists for — would be
	// unreachable from any test while every test still passed.
	serverPID int
	startTime int64
	// refuseOpt makes `set -p` fail, so the "cannot stamp" path is reachable.
	refuseOpt bool
}

func newFakeTmux(panes ...string) *fakeTmux {
	f := &fakeTmux{opts: map[string]string{}, screen: map[string]string{},
		keys: map[string][]string{}, bufs: map[string][]byte{}, activity: 1000,
		serverPID: fakeServerPID, startTime: fakeServerStart}
	for _, p := range panes {
		f.screen[p] = ""
	}
	return f
}

// The fake server's identity, and the same numbers `hubTMUX` names — so a model
// built the way production builds it decides that a pane carrying `fakeEpoch` is
// on the hub's OWN server. One place for all three, because the jump path is an
// equality between them: spelled out separately they drift, and a drift makes
// every possession test quietly exercise the window path instead.
const (
	fakeServerPID   = 4242
	fakeServerStart = 1786510794
	fakeEpoch       = "4242:1786510794"
	// hubTMUX is $TMUX as the hub's own pane sees it: socket, server pid, and the
	// BARE session number the third field always carries.
	hubTMUX = "/tmp/tmux-1000/default,4242,0"
	// hubSession is what hub.SelfSessionID() must make of hubTMUX's third field.
	hubSession = "$0"
)

func (f *fakeTmux) Run(ctx context.Context, t tmux.Target, args ...string) (tmux.Result, error) {
	return f.RunInput(ctx, t, nil, args...)
}

func (f *fakeTmux) RunInput(_ context.Context, t tmux.Target, stdin []byte, args ...string) (tmux.Result, error) {
	if t.Socket == "" {
		return tmux.Result{}, tmux.ErrNoSocket
	}
	// The real seam validates before running, and a test that skipped it would not
	// notice a template the production code can never send.
	if err := tmux.Validate(args); err != nil {
		return tmux.Result{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, strings.Join(args, " "))

	switch args[0] {
	case "set":
		pane, name, val := "", "", ""
		unset := false
		rest := []string{}
		for i := 1; i < len(args); i++ {
			switch args[i] {
			case "-p":
			case "-pu":
				unset = true
			case "-t":
				if i+1 < len(args) {
					pane = args[i+1]
					i++
				}
			default:
				rest = append(rest, args[i])
			}
		}
		if len(rest) > 0 {
			name = rest[0]
		}
		if len(rest) > 1 {
			val = rest[1]
		}
		if f.refuseOpt {
			return tmux.Result{RC: 1, Stderr: "can't set option: " + name}, nil
		}
		if unset {
			delete(f.opts, pane)
			return tmux.Result{}, nil
		}
		f.opts[pane] = val
		return tmux.Result{}, nil

	case "load-buffer":
		name := flagValue(args, "-b")
		f.bufs[name] = stdin
		return tmux.Result{}, nil

	case "delete-buffer":
		delete(f.bufs, flagValue(args, "-b"))
		return tmux.Result{}, nil

	case "list-sessions":
		// A launch into a new WINDOW has to be told which session, and it asks. A fake that
		// answers nothing is a server with no sessions, which the launch correctly refuses —
		// measured, three launch tests started failing with `has no tmux session to put a window
		// in` the moment the hard-coded `$0` was replaced by this question, which is the fake
		// telling the truth about itself. One session is what these fixtures mean.
		return tmux.Result{Stdout: "$0\n"}, nil

	case "list-buffers":
		if len(f.bufs) == 0 {
			// A server with no buffers answers rc=1, which Sweep reads as an empty
			// list rather than a failure.
			return tmux.Result{RC: 1}, nil
		}
		var names []string
		for n := range f.bufs {
			names = append(names, n)
		}
		return tmux.Result{Stdout: strings.Join(names, "\n") + "\n"}, nil

	case "if":
		// if -F -t %N '#{==:#{@hub_x},TOK}' THEN ELSE
		pane := flagValue(args, "-t")
		cond, then, els := "", "", ""
		for i := 1; i < len(args); i++ {
			if args[i] == "-F" || args[i] == "-t" {
				if args[i] == "-t" {
					i++
				}
				continue
			}
			switch {
			case cond == "":
				cond = args[i]
			case then == "":
				then = args[i]
			default:
				els = args[i]
			}
		}
		want := condToken(cond)
		branch := els
		if f.opts[pane] == want && want != "" {
			branch = then
		}
		return tmux.Result{Stdout: f.runChain(splitChain(branch))}, nil
	}
	return tmux.Result{Stdout: f.runChain(splitArgs(args))}, nil
}

// runChain executes sub-commands in order and concatenates what they print, which
// is what tmux does and what makes the hub's framing necessary.
func (f *fakeTmux) runChain(subs [][]string) string {
	var out []string
	for _, sub := range subs {
		if len(sub) == 0 {
			continue
		}
		pane := flagValue(sub, "-t")
		switch sub[0] {
		case "display":
			tmpl := ""
			for i := 1; i < len(sub); i++ {
				if sub[i] == "-t" {
					i++
					continue
				}
				if !strings.HasPrefix(sub[i], "-") {
					tmpl = sub[i]
				}
			}
			out = append(out, f.expand(tmpl, pane))
		case "capture-pane":
			out = append(out, f.screen[pane])
		case "paste-buffer":
			name := flagValue(sub, "-b")
			f.screen[pane] += string(f.bufs[name])
			f.activity++
			// -d deletes on paste, which is what stops an aborted batch leaving the
			// payload as the user's most recent buffer.
			if hasFlag(sub, "-d") {
				delete(f.bufs, name)
			}
		case "send-keys":
			key := sub[len(sub)-1]
			f.keys[pane] = append(f.keys[pane], key)
			f.activity++
		}
	}
	return strings.Join(out, "\n")
}

// expand resolves the three format variables the write path uses. A fake that
// echoed the template would make every confirmation check pass on text the hub
// itself wrote.
func (f *fakeTmux) expand(tmpl, pane string) string {
	out := strings.Trim(tmpl, "'")
	out = strings.ReplaceAll(out, "#{pane_id}", pane)
	out = strings.ReplaceAll(out, "#{window_activity}", strconv.FormatInt(f.activity, 10))
	out = strings.ReplaceAll(out, "#{pid}", strconv.Itoa(f.serverPID))
	out = strings.ReplaceAll(out, "#{start_time}", strconv.FormatInt(f.startTime, 10))
	// Any remaining #{...} is the per-pane option.
	for {
		i := strings.Index(out, "#{")
		if i < 0 {
			break
		}
		j := strings.Index(out[i:], "}")
		if j < 0 {
			break
		}
		out = out[:i] + f.opts[pane] + out[i+j+1:]
	}
	return out
}

func (f *fakeTmux) paneScreen(pane string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.screen[pane]
}

func (f *fakeTmux) sentKeys(pane string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.keys[pane]...)
}

func (f *fakeTmux) buffers() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.bufs)
}

// argvsSince returns the invocations recorded after the nth, in order. Order is
// the whole point for the possession path: select-window addresses a window in
// the session the client is already displaying, so a pair that arrives the other
// way round lands somewhere nobody picked.
func (f *fakeTmux) argvsSince(n int) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if n > len(f.calls) {
		n = len(f.calls)
	}
	return append([]string(nil), f.calls[n:]...)
}

func (f *fakeTmux) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func (f *fakeTmux) called(substr string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if strings.Contains(c, substr) {
			n++
		}
	}
	return n
}

func flagValue(args []string, flag string) string {
	for i, a := range args {
		if a == flag && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

func hasFlag(args []string, flag string) bool {
	for _, a := range args {
		if a == flag {
			return true
		}
	}
	return false
}

// condToken pulls the expected value out of `#{==:#{@hub_x},TOK}`.
func condToken(cond string) string {
	i := strings.LastIndex(cond, ",")
	if i < 0 {
		return ""
	}
	return strings.TrimSuffix(cond[i+1:], "}")
}

// splitChain splits a sub-command string on `;`, respecting single quotes exactly as
// tmux does — a template contains spaces and semicolons are inside no template here.
func splitChain(chain string) [][]string {
	var out [][]string
	for _, part := range strings.Split(chain, " ; ") {
		if toks := splitTokens(part); len(toks) > 0 {
			out = append(out, toks)
		}
	}
	return out
}

// splitTokens is a whitespace split that keeps a '…' span together.
func splitTokens(s string) []string {
	var out []string
	var cur strings.Builder
	quoted := false
	flush := func() {
		if cur.Len() > 0 {
			out = append(out, cur.String())
			cur.Reset()
		}
	}
	for _, r := range s {
		switch {
		case r == '\'':
			quoted = !quoted
			cur.WriteRune(r)
		case r == ' ' && !quoted:
			flush()
		default:
			cur.WriteRune(r)
		}
	}
	flush()
	return out
}

// splitArgs splits an argv list on the `;` elements tmux uses to batch commands.
func splitArgs(args []string) [][]string {
	var out [][]string
	cur := []string{}
	for _, a := range args {
		if a == ";" {
			out = append(out, cur)
			cur = []string{}
			continue
		}
		cur = append(cur, a)
	}
	return append(out, cur)
}

// walkAll identifies every pid it is asked about, so a test pane can be an agent
// without a real agent running in it.
type walkAll struct{}

func (walkAll) Walk(_ context.Context, pids []int) (map[int]int, error) {
	out := map[int]int{}
	for _, p := range pids {
		out[p] = p + 1
	}
	return out, nil
}

// blockingWalker is a host that stopped answering, which is the case the identity
// path has to bound. It deliberately ignores its context: the whole point is a walk
// that does NOT bound itself, so the bound has to come from the caller. The release
// channel exists so the goroutine ends with the test rather than with the binary.
type blockingWalker struct{ release <-chan struct{} }

func (b blockingWalker) Walk(context.Context, []int) (map[int]int, error) {
	<-b.release
	return nil, errors.New("proc: the test released the walk")
}

// collect runs a command and every command a batch of them contains, returning up to
// want messages or whatever arrived inside the window.
//
// Concurrently and bounded, both for reasons this package's batches actually have: a
// member may be a tea.Tick that answers only after PollInterval, and a member may be
// a host whose round is never coming back — which is exactly what these tests are
// about, so waiting for all of them is not an option.
func collect(t *testing.T, cmd tea.Cmd, want int, within time.Duration) []tea.Msg {
	t.Helper()
	out := make(chan tea.Msg, 32)
	var run func(tea.Cmd)
	run = func(c tea.Cmd) {
		if c == nil {
			return
		}
		go func() {
			msg := c()
			if batch, ok := msg.(tea.BatchMsg); ok {
				for _, sub := range batch {
					run(sub)
				}
				return
			}
			if msg != nil {
				out <- msg
			}
		}()
	}
	run(cmd)

	var got []tea.Msg
	deadline := time.After(within)
	for len(got) < want {
		select {
		case msg := <-out:
			got = append(got, msg)
		case <-deadline:
			return got
		}
	}
	return got
}

// identMsgFor finds one host's report among what a batch produced.
func identMsgFor(msgs []tea.Msg, host string) (identMsg, bool) {
	for _, msg := range msgs {
		if got, ok := msg.(identMsg); ok && got.host == host {
			return got, true
		}
	}
	return identMsg{}, false
}

// livePane is one selectable, writable pane as the tick would have left it.
func livePane(paneID, session string) registry.Pane {
	return registry.Pane{
		Kind: registry.KindPane, Host: hub.LocalLabel, PaneID: paneID,
		Session: session, Window: "w", Command: "claude",
		// The ids are what §7's "has it moved" clause compares; the names are what
		// the history log records.
		SessionID: hubSession, WindowID: "@" + strings.TrimPrefix(paneID, "%"),
		ClassifiedState: state.Idle, PanePID: os.Getpid(),
		// The epoch is the fake server's own, so a pane from this helper really is
		// on the hub's server and `a` really takes the jump path.
		Bracketed: true, Epoch: fakeEpoch,
	}
}

// onHost moves a pane to another host, so a test can have two servers in one hub.
func onHost(p registry.Pane, host string) registry.Pane {
	p.Host = host
	return p
}

// builtModel is the model as Run builds it, with a fake tmux and a walk that
// identifies anything. Nothing here hand-assembles a model: that is the point.
func builtModel(t *testing.T, f *fakeTmux, hist *history.Log, panes ...registry.Pane) model {
	t.Helper()
	inTmux(t)
	m := build(context.Background(), f, nil, true, nil, hist, nil)
	m.walk = func(hub.Host) proc.Walker { return walkAll{} }
	// The tick fills these two; everything else is exactly what Run built.
	m.panes = panes
	return m
}

// inTmux pins $TMUX for the test, so build's reads of the hub's own coordinates
// answer the same way whether or not `go test` was itself run from a tmux pane.
// Without it the floor is ambient: outside tmux `hub.SelfSocket()` is empty, the
// epoch read never happens, and deleting it would leave every test green.
func inTmux(t *testing.T) {
	t.Helper()
	t.Setenv("TMUX", hubTMUX)
}

// press drives one keystroke and runs whatever command it returned, feeding the
// resulting message back in — which is what bubbletea does.
//
// IT SEES ONE COMMAND LEVEL, and that is a real blind spot rather than a detail: the message
// fed back in may itself return a command, and press DISCARDS it. Both live effects of the
// picker's `enter` hang there — the save's message returns the connect — so a mutation that
// launched a connect against an unwritten file passed a test written with press, and the
// arm came back green. When the behaviour under test hangs off a second command, drive
// `Update` a step at a time and assert on the command you get back (see
// `enterAndConnect` and `TestAFailedSaveStopsNothingAndSaysSo`).
func press(t *testing.T, m model, key tea.KeyMsg) (model, tea.Msg) {
	t.Helper()
	next, cmd := m.Update(key)
	m = next.(model)
	if cmd == nil {
		return m, nil
	}
	msg := cmd()
	if msg == nil {
		return m, nil
	}
	next, _ = m.Update(msg)
	return next.(model), msg
}

func runes(s string) tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)} }

// THE test the branch did not have: build the model the way Run does and send once,
// end to end, through a fake tmux. It fails against the zero-value construction that
// shipped — `&broadcast.Sender{}` holds a nil Stamper, so the first Enter panicked
// inside Send — and it fails again if any of the four pieces (instance, stamper,
// sender, history log) is not wired.
func TestBuiltModelSendsEndToEnd(t *testing.T) {
	f := newFakeTmux("%0")
	path := filepath.Join(t.TempDir(), "history.jsonl")
	hist, err := history.Open(path, 1<<20)
	if err != nil {
		t.Fatalf("history.Open: %v", err)
	}
	defer hist.Close()

	m := builtModel(t, f, hist, livePane("%0", "work"))

	// space selects the pane AND identifies it, which is what puts a token on it.
	m, _ = press(t, m, runes(" "))
	if m.sel.Len() != 1 {
		t.Fatalf("space selected %d panes", m.sel.Len())
	}
	if f.opts["%0"] == "" {
		t.Fatal("selecting a pane did not stamp it — no token means every send is refused")
	}

	// i, type, enter. A single freshly identified target needs no confirmation, so
	// enter sends.
	m, _ = press(t, m, runes("i"))
	if m.mode != modeCompose {
		t.Fatalf("mode = %v, want compose", m.mode)
	}
	const text = "please run the tests"
	for _, r := range strings.Split(text, "") {
		next, _ := m.composeKey(runes(r))
		m = next.(model)
	}
	next, cmd := m.composeKey(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(model)
	if m.mode == modeConfirm {
		t.Fatalf("a fresh single target asked for confirmation: %v", m.pending)
	}
	if cmd == nil {
		t.Fatal("enter produced no command, so nothing was sent")
	}
	msg := cmd()
	sent, ok := msg.(sentMsg)
	if !ok {
		t.Fatalf("enter produced %T, want sentMsg", msg)
	}

	// The assertions are on the PANE, not on what the hub said: a confirmation fires
	// whether or not any bytes arrived.
	if got := f.paneScreen("%0"); !strings.Contains(got, text) {
		t.Errorf("the text never reached the pane: %q", got)
	}
	if keys := f.sentKeys("%0"); len(keys) != 1 || keys[0] != "Enter" {
		t.Errorf("keys sent = %v, want exactly one Enter — a pasted prompt nobody submits does nothing", keys)
	}
	if len(sent.results) != 1 {
		t.Fatalf("results = %+v, want one", sent.results)
	}
	r := sent.results[0]
	if r.Outcome != broadcast.Delivered {
		t.Errorf("Outcome = %s (%s), want delivered", r.Outcome, r.Reason)
	}
	if !r.Submitted {
		t.Error("Submitted is false, so the prompt is sitting unsent in the input box")
	}
	if f.buffers() != 0 {
		t.Errorf("%d paste buffer(s) survived the send", f.buffers())
	}
	// The payload travels on stdin, never as an argument.
	if n := f.called(text); n != 0 {
		t.Errorf("the payload appeared in argv %d times", n)
	}

	// And it is on disk, because the history view and the re-send read it from there.
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading the history log: %v", err)
	}
	var e history.Entry
	if err := json.Unmarshal([]byte(strings.TrimSpace(string(raw))), &e); err != nil {
		t.Fatalf("the history log holds %q: %v", raw, err)
	}
	if e.Text != text || e.Outcome != string(broadcast.Delivered) || !e.Submitted {
		t.Errorf("history entry = %+v", e)
	}
	if e.PaneID != "%0" || e.SessionName != "work" {
		t.Errorf("the entry does not identify the target: %+v", e)
	}

	// The note names the count, so "delivered" can never stand for "reached nothing".
	after, _ := m.Update(sent)
	if note := after.(model).note; !strings.Contains(note, "1 delivered") {
		t.Errorf("note = %q, want the count in it", note)
	}
}

// THE test §20 did not have: press `a` on a pane of the hub's own server against a
// model built the way Run builds it, and read the argv off the seam.
//
// Every possession test before this one hand-built a `model{…}` and ASSIGNED
// selfSession and selfEpoch, so the two reads that produce them were exercised by
// nothing. Both ways of breaking them shipped green:
//
//   - drop the epoch read and `selfEpoch` stays empty. decidePossession then finds
//     no locality match, every local target falls back to the pre-§20 full-screen
//     attach, and the jump path is dead code with no test to notice.
//   - hand the two values over in the wrong order and it compiles, because they are
//     both strings. `switch-client -t 4242:1786510794` is then refused at the seam,
//     so every single `a` dies — and, again, no test looked.
//
// So the assertions are: what build READ (both fields, by value, from $TMUX and
// from tmux itself), that it asked tmux at all, and the two commands the keystroke
// produces IN ORDER.
func TestBuiltModelJumpsIntoAPaneOnItsOwnServer(t *testing.T) {
	f := newFakeTmux("%0")
	m := builtModel(t, f, nil, livePane("%0", "work"))

	if m.selfSession != hubSession {
		t.Fatalf("build read selfSession = %q, want %q from $TMUX's third field",
			m.selfSession, hubSession)
	}
	if m.selfEpoch != fakeEpoch {
		t.Fatalf("build read selfEpoch = %q, want %q — the hub cannot recognise its "+
			"own server, so every local target falls back to the full-screen attach",
			m.selfEpoch, fakeEpoch)
	}
	if n := f.called("#{pid}:#{start_time}"); n != 1 {
		t.Fatalf("build asked tmux for its own epoch %d times, want exactly 1", n)
	}
	// The two fields must not be interchangeable: a session target is `$N` and an
	// epoch is `pid:start_time`, so a swap is visible here before any command runs.
	if m.selfSession == m.selfEpoch {
		t.Fatal("selfSession and selfEpoch hold the same string, so a swap would be invisible")
	}

	before := f.callCount()
	m, msg := press(t, m, runes("a"))
	got, ok := msg.(possessedMsg)
	if !ok {
		t.Fatalf("a produced %T, want a possessedMsg — the jump never ran", msg)
	}
	if got.err != nil {
		t.Fatalf("the jump failed: %v", got.err)
	}
	if got.from != "work:w" {
		t.Errorf("from = %q, want the session and window names", got.from)
	}
	// The pair, in order, by ID. select-window addresses a window in the session the
	// client is now displaying, so a reversed pair lands on the wrong window.
	want := []string{"switch-client -t $0", "select-window -t @0"}
	if sent := f.argvsSince(before); !reflect.DeepEqual(sent, want) {
		t.Fatalf("a sent %q, want %q", sent, want)
	}
	// Fed back through Update — which is what bubbletea does — the message becomes
	// the line the operator reads on return. Nothing took the terminal: the
	// full-screen path answers with an attachedMsg from tea.ExecProcess instead.
	if m.note != "back from work:w" {
		t.Errorf("note = %q, want it to name where the operator was", m.note)
	}
}

// `!` must interrupt, whatever is in the input box. A non-empty composer is the
// normal resting state — esc keeps the draft, and so does a refused send — so
// inferring the act from it wrote the draft into every pane the user was trying to
// stop. This is the exact sequence that did it.
// The dialog is the case that matters, and it has to be FORCED. Measured while
// calibrating this test: with one freshly identified target the rule finds no reason
// to ask, so `!` interrupts without ever reaching confirmKey — and the test passed
// against the mutation it is named after. Two targets guarantee the dialog, and the
// mode is asserted rather than branched on.
func TestInterruptNeverSendsTheDraft(t *testing.T) {
	f := newFakeTmux("%0", "%1")
	m := builtModel(t, f, nil, livePane("%0", "work"), livePane("%1", "other"))
	m, _ = press(t, m, runes(" "))
	m = m.cursorTo(1)
	m, _ = press(t, m, runes(" "))

	// i, type, esc — the draft is deliberately kept.
	m, _ = press(t, m, runes("i"))
	for _, r := range strings.Split("delete all the files", "") {
		next, _ := m.composeKey(runes(r))
		m = next.(model)
	}
	next, _ := m.composeKey(tea.KeyMsg{Type: tea.KeyEsc})
	m = next.(model)
	if m.composer.Empty() {
		t.Fatal("esc lost the draft, so this test no longer covers the case")
	}

	// `!` then enter, through the confirmation dialog — which two targets guarantee.
	next, _ = m.Update(runes("!"))
	m = next.(model)
	if m.mode != modeConfirm {
		t.Fatalf("mode = %v, want confirm — this test only covers C2 through the dialog", m.mode)
	}
	if m.pendingAct != actionInterrupt {
		t.Fatalf("the dialog recorded act %v, want interrupt", m.pendingAct)
	}
	next, cmd := m.confirmKey(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(model)
	if cmd == nil {
		t.Fatal("the confirmed interrupt produced no command at all")
	}
	msg := cmd()
	sent, ok := msg.(sentMsg)
	if !ok {
		t.Fatalf("! produced %T, want sentMsg", msg)
	}
	if sent.act != actionInterrupt {
		t.Errorf("act = %v, want interrupt", sent.act)
	}
	for _, pane := range []string{"%0", "%1"} {
		if got := f.paneScreen(pane); strings.Contains(got, "delete all") {
			t.Fatalf("! WROTE THE DRAFT into %s, which it was meant to interrupt: %q", pane, got)
		}
		if keys := f.sentKeys(pane); len(keys) != 1 || keys[0] != "C-c" {
			t.Errorf("%s got keys %v, want one C-c", pane, keys)
		}
	}
	if len(sent.results) != 2 {
		t.Fatalf("results = %+v, want two", sent.results)
	}
	for _, r := range sent.results {
		if r.Outcome != broadcast.Delivered {
			t.Errorf("%s: %s (%s)", r.Target.PaneID, r.Outcome, r.Reason)
		}
	}
}

// The same guarantee at the smallest possible scale, so a change to what opens the
// dialog cannot make the case above unreachable again: the act comes from the model,
// and a full input box is the normal resting state.
func TestConfirmDispatchesOnTheRecordedActNotTheComposer(t *testing.T) {
	for _, c := range []struct {
		act  action
		want action
	}{
		{actionInterrupt, actionInterrupt},
		{actionSend, actionSend},
	} {
		f := newFakeTmux("%0")
		m := builtModel(t, f, nil, livePane("%0", "work"))
		m, _ = press(t, m, runes(" "))
		m.composer.Insert("a draft the user left in the box")
		m.mode, m.pending, m.pendingAct = modeConfirm, []broadcast.Reason{broadcast.ReasonMultiple}, c.act

		_, cmd := m.confirmKey(tea.KeyMsg{Type: tea.KeyEnter})
		if cmd == nil {
			t.Fatalf("act %v produced no command", c.act)
		}
		sent, ok := cmd().(sentMsg)
		if !ok {
			t.Fatalf("act %v produced %T", c.act, sent)
		}
		if sent.act != c.want {
			t.Errorf("a confirmation for act %v ran act %v", c.act, sent.act)
		}
	}

	// And a dialog with no act recorded must do NOTHING, because there is no answer
	// that cannot write into a live agent.
	m := builtModel(t, newFakeTmux("%0"), nil, livePane("%0", "work"))
	m.composer.Insert("still a draft")
	m.mode, m.pendingAct = modeConfirm, actionNone
	got, cmd := m.confirmKey(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd != nil {
		t.Error("a confirmation with no recorded act ran something")
	}
	if got.(model).note == "" {
		t.Error("it did nothing and said nothing")
	}
}

// A guard REFUSAL exits 0, so an interrupt the guard blocked must not read as
// delivered — the operator would believe an agent was stopped.
func TestInterruptReportsAGuardRefusal(t *testing.T) {
	f := newFakeTmux("%0")
	m := builtModel(t, f, nil, livePane("%0", "work"))
	m, _ = press(t, m, runes(" "))

	// The pane's option changes out of band: a respawn, another hub, or a server
	// restart recycling the id.
	f.mu.Lock()
	f.opts["%0"] = "somebody-elses-token"
	f.mu.Unlock()

	cmd := m.interrupt("C-c")
	sent := cmd().(sentMsg)
	if len(sent.results) != 1 {
		t.Fatalf("results = %+v", sent.results)
	}
	if sent.results[0].Outcome != broadcast.Refused {
		t.Errorf("Outcome = %s (%s), want refused", sent.results[0].Outcome, sent.results[0].Reason)
	}
	if keys := f.sentKeys("%0"); len(keys) != 0 {
		t.Errorf("keys reached a pane the guard refused: %v", keys)
	}
	if note := summarise(actionInterrupt, sent.results, sent.dropped); !strings.Contains(note, "refused") {
		t.Errorf("note = %q, want it to say refused", note)
	}
}

// Every browse-mode key: the mode it leaves the model in and whether it produces a
// command. Nothing in this package named `!` or Interrupt before, which is how the
// interrupt key came to send the draft.
func TestBrowseKeysProduceTheRightModeAndCommand(t *testing.T) {
	for _, c := range []struct {
		key      string
		selected bool // press space first
		wantMode uiMode
		wantCmd  bool
		wantAct  action
		check    func(t *testing.T, m model)
	}{
		{key: " ", wantMode: modeBrowse, wantCmd: true, check: func(t *testing.T, m model) {
			if m.sel.Len() != 1 {
				t.Errorf("space selected %d panes, want 1", m.sel.Len())
			}
			if _, ok := m.atSelection[SelectionKey{Host: hub.LocalLabel, PaneID: "%0"}]; !ok {
				t.Error("space recorded no snapshot, so no 'changed since selection' clause can fire")
			}
		}},
		{key: "A", wantMode: modeBrowse, wantCmd: true, check: func(t *testing.T, m model) {
			if m.sel.Len() != 1 {
				t.Errorf("A selected %d panes, want the one on screen", m.sel.Len())
			}
		}},
		{key: "C", selected: true, wantMode: modeBrowse, check: func(t *testing.T, m model) {
			if m.sel.Len() != 0 {
				t.Errorf("C left %d panes selected", m.sel.Len())
			}
			if len(m.atSelection) != 0 {
				t.Error("C left a snapshot behind")
			}
		}},
		{key: "i", selected: true, wantMode: modeCompose},
		{key: "i", wantMode: modeBrowse, check: func(t *testing.T, m model) {
			if m.note == "" {
				t.Error("i with nothing selected was silent")
			}
		}},
		{key: "!", selected: true, wantMode: modeBrowse, wantCmd: true, wantAct: actionInterrupt},
		{key: "!", wantMode: modeBrowse, check: func(t *testing.T, m model) {
			if m.note == "" {
				t.Error("! with nothing selected was silent — the same guard i has")
			}
		}},
		{key: "h", wantMode: modeBrowse, check: func(t *testing.T, m model) {
			if m.note == "" {
				t.Error("h without a history log said nothing")
			}
		}},
	} {
		name := c.key
		if c.selected {
			name += " with a selection"
		}
		t.Run(name, func(t *testing.T) {
			f := newFakeTmux("%0")
			m := builtModel(t, f, nil, livePane("%0", "work"))
			if c.selected {
				m, _ = press(t, m, runes(" "))
			}
			next, cmd := m.Update(runes(c.key))
			got := next.(model)
			if got.mode != c.wantMode {
				t.Errorf("%q left mode %v, want %v", c.key, got.mode, c.wantMode)
			}
			if c.wantCmd && cmd == nil {
				t.Errorf("%q produced no command", c.key)
			}
			if !c.wantCmd && cmd != nil && got.mode == modeBrowse {
				t.Errorf("%q produced a command and should not have", c.key)
			}
			// A key that opens the dialog must record WHICH act it is for, or the
			// enter that follows has to guess.
			if got.mode == modeConfirm && got.pendingAct == actionNone {
				t.Errorf("%q opened the dialog with no act recorded", c.key)
			}
			if c.wantAct != actionNone && cmd != nil {
				if sent, ok := cmd().(sentMsg); ok && sent.act != c.wantAct {
					t.Errorf("%q produced act %v, want %v", c.key, sent.act, c.wantAct)
				}
			}
			if c.check != nil {
				c.check(t, got)
			}
		})
	}
}

// `A` selects the panes on screen and no others, in the GROUPED layout where the
// session headers make the arithmetic non-trivial. Measured before the fix: 22
// selected against 14 drawn at every width ≥ 100.
func TestSelectAllSelectsOnlyPanesOnScreen(t *testing.T) {
	var panes []registry.Pane
	for i := 0; i < 40; i++ {
		p := livePane(paneName(i), "s"+paneName(i))
		panes = append(panes, p)
	}
	f := newFakeTmux()
	m := builtModel(t, f, nil, panes...)
	m.width, m.height = 120, 24
	m = m.cursorTo(0)

	next, _ := m.Update(runes("A"))
	after := next.(model)
	if after.sel.Len() == 0 {
		t.Fatal("A selected nothing")
	}
	out := Render(Frame{Panes: after.panes, Hosts: after.hosts, Width: after.width, Height: after.height, Cursor: after.cursorIndex(), Marked: after.markedSet(), Note: "", Hint: "", HiddenCount: 0, BlockedCount: 0, Resurfaced: nil, Aliases: project.Aliases{}})
	for _, p := range panes {
		if !after.sel.Has(selKey(p)) {
			continue
		}
		if !strings.Contains(out, p.PaneID) {
			t.Errorf("A selected %s, which is not on screen:\n%s", p.PaneID, out)
		}
	}
}

func paneName(i int) string { return "%" + strconv.Itoa(100+i) }

// targetStates is the PRODUCER of everything the confirmation rule reads. Four of
// the seven reasons were unreachable in the shipped binary because it filled three
// fields out of eleven, and confirm_test cannot notice that: it builds TargetState
// by hand. So this asserts on the producer, field by field.
func TestTargetStatesFillsEveryFieldFromTheRegistry(t *testing.T) {
	f := newFakeTmux("%0")
	pane := livePane("%0", "work")
	m := builtModel(t, f, nil, pane)
	m, _ = press(t, m, runes(" "))
	// A second round, so identification predates the selection exactly as it does
	// after a tick. One host means identify returns that host's command directly.
	if cmd := m.identify(); cmd != nil {
		cmd()
	}
	m.mark(pane) // deselect
	m.mark(pane) // and select again, now that the walk has run
	m.lastOutcome[selKey(pane)] = broadcast.Unwitnessed
	m.fromHistory = true

	ts := m.targetStates()
	if len(ts) != 1 {
		t.Fatalf("targetStates returned %d states", len(ts))
	}
	s := ts[0]
	for _, c := range []struct {
		field string
		ok    bool
	}{
		{"Host", s.Host == hub.LocalLabel},
		{"PaneID", s.PaneID == "%0"},
		{"IdentifiedNow", s.IdentifiedNow},
		{"IdentifiedAtSelection", s.IdentifiedAtSelection},
		{"SessionAtSelection", s.SessionAtSelection == pane.SessionID},
		{"SessionNow", s.SessionNow == pane.SessionID},
		{"WindowAtSelection", s.WindowAtSelection == pane.WindowID},
		{"WindowNow", s.WindowNow == pane.WindowID},
		{"EpochAtSelection", s.EpochAtSelection == pane.Epoch},
		{"EpochNow", s.EpochNow == pane.Epoch},
		{"LastOutcome", s.LastOutcome == broadcast.Unwitnessed},
		{"FromHistory", s.FromHistory},
		{"Bracketed", s.Bracketed},
	} {
		if !c.ok {
			t.Errorf("%s is not populated from the registry: %+v", c.field, s)
		}
	}

	// And the four reasons that were unreachable must now fire from this producer.
	for _, c := range []struct {
		name   string
		want   broadcast.Reason
		mutate func(m *model)
	}{
		{"the agent exited", broadcast.ReasonAgentGone, func(m *model) {
			m.keeper.Forget(hub.LocalLabel)
		}},
		{"the pane moved", broadcast.ReasonMoved, func(m *model) {
			m.panes[0].WindowID = "@99"
		}},
		{"the server restarted", broadcast.ReasonEpochChanged, func(m *model) {
			m.panes[0].Epoch = "9999:1786599999"
		}},
		{"the last send was not witnessed", broadcast.ReasonLastUnwitnessed, func(m *model) {
			m.lastOutcome[selKey(pane)] = broadcast.Unwitnessed
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newFakeTmux("%0")
			m := builtModel(t, f, nil, livePane("%0", "work"))
			m, _ = press(t, m, runes(" "))
			if cmd := m.identify(); cmd != nil {
				cmd()
			}
			m.mark(m.panes[0])
			m.mark(m.panes[0])
			c.mutate(&m)
			if !hasReason(broadcast.Needed(m.targetStates()), c.want) {
				t.Errorf("Needed() = %v, want it to include %q",
					broadcast.Needed(m.targetStates()), c.want)
			}
		})
	}
}

func hasReason(rs []broadcast.Reason, want broadcast.Reason) bool {
	for _, r := range rs {
		if r == want {
			return true
		}
	}
	return false
}

// ctrl+c leaves the program from EVERY screen. It is not a shortcut but the universal
// "get me out", and a program that swallows it reads as hung.
//
// Measured in a pty before this held, on the first-run picker: `q` did nothing, `ctrl+c`
// did nothing, and the shell's `echo EXITED-rc=$?` never printed — so the program could
// not be left from its own first screen, which is the one an auto-opening picker makes a
// new user meet first. `esc` worked and the key line advertised it, so it was not a trap;
// "read the footer to escape" is simply not the standard for a first screen.
//
// Sub-tested per mode so a failure names the screen. That is the point of the table: the
// handler lives in ONE place, above the mode dispatch, and a per-mode assertion is what
// proves the test would still notice if it were moved back into five.
func TestCtrlCQuitsFromEveryScreen(t *testing.T) {
	for _, mode := range []struct {
		name string
		mode uiMode
	}{
		{"browse", modeBrowse},
		{"compose", modeCompose},
		{"confirm", modeConfirm},
		{"history", modeHistory},
		{"launch", modeLaunch},
		{"picker", modePicker},
	} {
		t.Run(mode.name, func(t *testing.T) {
			m := model{
				mode:    mode.mode,
				panes:   []registry.Pane{livePane("%0", "work")},
				pending: []broadcast.Reason{broadcast.ReasonMultiple},
				history: []history.Entry{{Text: "an old prompt", Outcome: "delivered"}},
				picker:  []PickerRow{{Alias: "eu", Version: "3.2a", Usable: true}},
				width:   80, height: 24,
			}
			_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
			if cmd == nil {
				t.Fatalf("%s mode swallowed ctrl+c: no command", mode.name)
			}
			// The command has to BE the quit, not merely be non-nil — every other key on
			// these screens also returns a command, so "produced something" would pass
			// against a picker that re-probed instead of quitting.
			if _, ok := cmd().(tea.QuitMsg); !ok {
				t.Errorf("%s mode answered ctrl+c with %T, want tea.QuitMsg", mode.name, cmd())
			}
		})
	}
}

// `q` is deliberately NOT the same rule, and this pins the asymmetry so nobody "fixes" it
// into symmetry later: two of the overlays are text entry where `q` must type a literal
// `q`, so one rule with no exceptions (ctrl+c always quits) beats a second rule needing a
// per-screen table. On the dashboard `q` still quits.
func TestQQuitsOnlyFromTheDashboard(t *testing.T) {
	base := func(mode uiMode) model {
		return model{
			mode: mode, panes: []registry.Pane{livePane("%0", "work")},
			picker: []PickerRow{{Alias: "eu", Version: "3.2a", Usable: true}},
			width:  80, height: 24,
		}
	}
	if _, cmd := base(modeBrowse).Update(runes("q")); cmd == nil {
		t.Error("q no longer quits the dashboard")
	} else if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Errorf("q on the dashboard answered %T, want tea.QuitMsg", cmd())
	}
	// In compose it is text, and the draft is the proof — asserting "did not quit" alone
	// would pass against a mode that dropped the keystroke entirely.
	next, _ := base(modeCompose).Update(runes("q"))
	after := next.(model) // a variable, because Text has a pointer receiver
	if got := after.composer.Text(); got != "q" {
		t.Errorf("q in compose left the draft %q, want it typed as text", got)
	}
}

// A note is the only answer some keystrokes give, so every mode has to show it.
// Measured: three of the four dropped it, which made `r` with an empty selection a
// silent no-op.
func TestEveryModeShowsItsNote(t *testing.T) {
	const sentinel = "SENTINEL-NOTE"
	for _, mode := range []struct {
		name string
		mode uiMode
	}{
		{"browse", modeBrowse},
		{"compose", modeCompose},
		{"confirm", modeConfirm},
		{"history", modeHistory},
	} {
		t.Run(mode.name, func(t *testing.T) {
			m := model{
				mode: mode.mode, note: sentinel,
				panes:   []registry.Pane{livePane("%0", "work")},
				pending: []broadcast.Reason{broadcast.ReasonMultiple},
				history: []history.Entry{{Text: "an old prompt", Outcome: "delivered"}},
				width:   80, height: 24,
			}
			if !strings.Contains(m.View(), sentinel) {
				t.Errorf("%s mode dropped the note:\n%s", mode.name, m.View())
			}
		})
	}
}

// The confirmation screen has to name the act, the payload and the targets. It named
// none of them: it read "Confirm send to 1 target(s)" for an interrupt, and on the
// re-send path — where the user never passes through the input box — it showed no
// payload at all.
func TestConfirmScreenNamesTheActThePayloadAndTheTargets(t *testing.T) {
	f := newFakeTmux("%0", "%1")
	m := builtModel(t, f, nil, livePane("%0", "work"), livePane("%1", "other"))
	m, _ = press(t, m, runes(" "))
	m = m.cursorTo(1)
	m, _ = press(t, m, runes(" "))

	// pendingText, not the composer: the dialog's payload is the text the SEND will carry, and
	// those stopped being the same thing when `r` stopped staging a log entry in the composer.
	// This case builds the dialog's state by hand because it is about the SCREEN; the producers
	// are covered by TestHistoryResendAlwaysConfirms and TestACancelledResendLeavesTheDraftAlone,
	// and end to end by TestE2EUIHistoryResendSaysTheTextCameFromTheHistoryView.
	m.pendingText = "rebase onto main"
	m.mode, m.pending, m.pendingAct = modeConfirm, []broadcast.Reason{broadcast.ReasonMultiple}, actionSend
	out := m.View()
	for _, want := range []string{"Confirm send", "rebase onto main", "%0", "%1", "2 targets"} {
		if !strings.Contains(out, want) {
			t.Errorf("the confirm screen does not show %q:\n%s", want, out)
		}
	}

	m.pendingAct = actionInterrupt
	out = m.View()
	if !strings.Contains(out, "interrupt") {
		t.Errorf("the confirm screen calls an interrupt something else:\n%s", out)
	}
	if strings.Contains(out, "rebase onto main") {
		t.Errorf("the confirm screen showed a send's payload for an interrupt:\n%s", out)
	}
}

// A write that resolved no target must not report success. Measured before the fix:
// summarise(nil) == "delivered".
func TestASendThatReachedNothingSaysSo(t *testing.T) {
	if got := summarise(actionSend, nil, 0); strings.Contains(got, "delivered") {
		t.Errorf("summarise(nil) = %q", got)
	}
	if got := summarise(actionSend, nil, 2); !strings.Contains(got, "2 selected panes") {
		t.Errorf("summarise with two dropped targets = %q, want the count", got)
	}
	// The denominator includes what could not be resolved, so 1 of 3 cannot read as
	// "1 target".
	got := summarise(actionSend, []broadcast.Result{{Outcome: broadcast.Delivered}}, 2)
	if !strings.Contains(got, "3 targets") || !strings.Contains(got, "2 unresolved") {
		t.Errorf("summarise = %q, want 3 targets and 2 unresolved", got)
	}
}

// A selected pane whose host has left the hub's list cannot be written to, and the
// send must say that rather than reporting nothing at all.
func TestSendWithNoResolvableTargetReportsIt(t *testing.T) {
	f := newFakeTmux("%0")
	m := builtModel(t, f, nil, livePane("%0", "work"))
	m, _ = press(t, m, runes(" "))
	m.hosts = nil // the host went away between the selection and the send

	msg := m.send("anything at all")()
	sent, ok := msg.(sentMsg)
	if !ok {
		t.Fatalf("send produced %T", msg)
	}
	if len(sent.results) != 0 || sent.dropped != 1 {
		t.Fatalf("results = %+v, dropped = %d, want none and 1", sent.results, sent.dropped)
	}
	if got := f.paneScreen("%0"); got != "" {
		t.Errorf("something was written anyway: %q", got)
	}
	after, _ := m.Update(sent)
	if note := after.(model).note; !strings.Contains(note, "nothing was sent") {
		t.Errorf("note = %q, want it to say nothing was sent", note)
	}
}

// The connect sweep runs, and it clears buffers a previous hub left whatever its
// instance id was — a hub that crashed cannot clean up after itself.
func TestConnectSweepClearsAPreviousHubsBuffers(t *testing.T) {
	f := newFakeTmux("%0")
	f.bufs["tmux-hub-deadbeef-7"] = []byte("secret prompt from a hub that died")
	f.bufs["mine"] = []byte("the user's own buffer")

	m := builtModel(t, f, nil, livePane("%0", "work"))
	cmd := m.sweep()
	if cmd == nil {
		t.Fatal("no sweep at connect")
	}
	msg := cmd()
	if got, ok := msg.(sweptMsg); !ok || got.removed != 1 {
		t.Fatalf("sweep reported %+v, want one buffer removed", msg)
	}
	if _, still := f.bufs["tmux-hub-deadbeef-7"]; still {
		t.Error("the stale hub buffer survived — the user's next paste is someone's prompt")
	}
	if _, gone := f.bufs["mine"]; !gone {
		t.Error("the sweep took a buffer the user named")
	}
	after, _ := m.Update(msg)
	if note := after.(model).note; !strings.Contains(note, "stale paste buffer") {
		t.Errorf("note = %q, want it to name what was cleared", note)
	}
}

// And the sweep must actually be among the commands Init returns. Asserting on
// m.sweep() alone cannot see it being left out of the wiring — measured, dropping it
// from Init left the test above green.
//
// The context is cancelled so the other two commands in the batch (the tick and the
// agent listing) fail fast instead of running a real `claude`.
func TestInitIncludesTheConnectSweep(t *testing.T) {
	f := newFakeTmux("%0")
	f.bufs["tmux-hub-deadbeef-7"] = []byte("a payload from a hub that died")

	inTmux(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	m := build(ctx, f, nil, true, nil, nil, nil)
	m.walk = func(hub.Host) proc.Walker { return walkAll{} }

	init := m.Init()
	if init == nil {
		t.Fatal("Init returned no commands at all")
	}
	batch, ok := init().(tea.BatchMsg)
	if !ok {
		t.Fatalf("Init returned %T, want a batch", init())
	}
	swept := false
	for _, c := range batch {
		if c == nil {
			continue
		}
		if _, is := c().(sweptMsg); is {
			swept = true
		}
	}
	if !swept {
		t.Error("no sweep among Init's commands — a previous hub's payload stays as the " +
			"user's most recent paste buffer")
	}
	if _, still := f.bufs["tmux-hub-deadbeef-7"]; still {
		t.Error("Init ran a sweep that removed nothing")
	}
}

// Identification has to ride the TICK, and asserting on m.identify() alone cannot see
// it being left out of the batch — the same blind spot the connect sweep had, where
// dropping the sweep from Init left its own test green.
//
// Without it a pane is stamped once at selection and never re-stamped or unstamped.
// The token would then mean "identified at some point" rather than "identified no more
// than one tick ago" (docs/design.md §7), so a pane whose agent has exited would still
// read as identified, still carry its token, and take a paste plus Enter at a shell
// prompt.
func TestTheTickReIdentifiesAndReStamps(t *testing.T) {
	f := newFakeTmux("%0")
	m := builtModel(t, f, nil, livePane("%0", "work"))
	m, _ = press(t, m, runes(" ")) // selected, so a round stamps it

	// Undo what the SELECTION's own round did, so only the tick can put it back.
	f.mu.Lock()
	delete(f.opts, "%0")
	f.mu.Unlock()
	m.keeper.Forget(hub.LocalLabel)

	_, cmd := m.Update(tickMsg{hosts: m.hosts, panes: m.panes})
	if cmd == nil {
		t.Fatal("the tick produced no commands at all")
	}
	// Two: the next tick and the identification. The tick's own timer answers after
	// PollInterval, which is why this waits rather than running them in series.
	msgs := collect(t, cmd, 2, 3*time.Second)
	if _, ok := identMsgFor(msgs, hub.LocalLabel); !ok {
		t.Errorf("no identification among the tick's commands (%d msgs: %v) — a pane is "+
			"then stamped once at selection and never re-stamped or unstamped", len(msgs), msgs)
	}
	if f.opts["%0"] == "" {
		t.Error("the tick did not re-stamp the selected pane, so its token stops rotating")
	}
	if !m.keeper.Identified(hub.LocalLabel, "%0") {
		t.Error("the tick did not re-identify the pane")
	}
}

// One host that stopped answering must not stop identification on the others. It did:
// a single in-flight flag for the fleet, plus a round that waited on every host, meant
// one stalled walk froze re-identification everywhere — and while it is frozen every
// pane on every host keeps reading as identified from the last completed round, which
// is the one direction this code must never fail in.
func TestAStalledHostDoesNotStopIdentificationEverywhere(t *testing.T) {
	f := newFakeTmux("%0", "%1")
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })

	stuck := hub.Host{Label: "stuck", Socket: filepath.Join(t.TempDir(), "stuck.sock"),
		SSHDest: "stuck", ControlPath: filepath.Join(t.TempDir(), "stuck.ctl")}
	inTmux(t)
	m := build(context.Background(), f, []hub.Host{stuck}, true, nil, nil, nil)
	m.walk = func(h hub.Host) proc.Walker {
		if h.Label == stuck.Label {
			return blockingWalker{release}
		}
		return walkAll{}
	}
	localPane := livePane("%0", "work")
	stuckPane := onHost(livePane("%1", "remote-work"), stuck.Label)
	m.panes = []registry.Pane{localPane, stuckPane}
	// A remote host is walked only while something on it is selected, so both panes
	// are selected here — which is also the case that costs the most: `A` on a stalled
	// host is one 5-second stamp per pane after a walk that never returns.
	m.mark(localPane)
	m.mark(stuckPane)

	cmd := m.identify()
	if cmd == nil {
		t.Fatal("no round started at all")
	}
	// Ask for two so the window is spent waiting for the stalled host's report; the
	// local one must arrive anyway, and it must arrive ALONE.
	msgs := collect(t, cmd, 2, time.Second)
	if _, ok := identMsgFor(msgs, hub.LocalLabel); !ok {
		t.Fatalf("the local host did not report while another host was stalled: %v", msgs)
	}
	if _, ok := identMsgFor(msgs, stuck.Label); ok {
		t.Fatal("the stalled host answered, so this test no longer covers the case")
	}
	if !m.keeper.Identified(hub.LocalLabel, "%0") || f.opts["%0"] == "" {
		t.Error("the local pane was neither identified nor stamped while another host stalled")
	}

	// bubbletea feeds each message back, and only the reporting host is cleared.
	for _, msg := range msgs {
		next, _ := m.Update(msg)
		m = next.(model)
	}
	if !m.identBusy[stuck.Label] {
		t.Fatal("the stalled host's round is not recorded as out, so what follows proves nothing")
	}
	if m.identBusy[hub.LocalLabel] {
		t.Fatal("the local host's report did not clear its own round")
	}

	// And the crux: the next tick can re-identify the healthy host while the stalled
	// one is still out. With one flag for the fleet this returns nil, and every host's
	// token stops rotating for as long as one host hangs.
	again := m.identify()
	if again == nil {
		t.Fatal("no host could be re-identified while one host was stalled — the token " +
			"then means 'identified at some point' for hosts that are answering fine")
	}
	msgs = collect(t, again, 2, time.Second)
	if _, ok := identMsgFor(msgs, hub.LocalLabel); !ok {
		t.Errorf("the second round did not reach the healthy host: %v", msgs)
	}
	if _, ok := identMsgFor(msgs, stuck.Label); ok {
		t.Error("a second round started on a host that already had one out")
	}
}

// A remote host without `ctl=` is READ-ONLY, and this asserts the whole chain that
// makes it so: no walker, therefore no identification, therefore no stamp, therefore a
// refusal at the guard. The README used to say such a host still accepted sends,
// "confirmed rather than sent straight out", which it structurally cannot do — the
// dialog would come up and the send behind it would be refused every time.
func TestARemoteHostWithoutAControlPathIsReadOnly(t *testing.T) {
	far := hub.Host{Label: "far", Socket: filepath.Join(t.TempDir(), "far.sock"),
		SSHDest: "far"}
	if w := walkerFor(far); w != nil {
		t.Fatalf("a remote host with no ctl= got a walker (%T)", w)
	}
	// With ctl= it does get one, or the assertion above would hold for any host.
	withCtl := far
	withCtl.ControlPath = filepath.Join(t.TempDir(), "far.ctl")
	if walkerFor(withCtl) == nil {
		t.Error("a remote host WITH ctl= got no walker either, so nothing above is proved")
	}

	// End to end, through the real walkerFor that build wires in.
	f := newFakeTmux("%0")
	inTmux(t)
	m := build(context.Background(), f, []hub.Host{far}, false, nil, nil, nil)
	pane := onHost(livePane("%0", "work"), far.Label)
	m.panes = []registry.Pane{pane}
	m.mark(pane)
	if cmd := m.identify(); cmd != nil {
		collect(t, cmd, 1, 3*time.Second)
	}
	if m.keeper.Identified(far.Label, "%0") {
		t.Error("a pane on a host with no way to walk it was identified anyway")
	}
	if f.opts["%0"] != "" {
		t.Errorf("a pane on a host with no walker was stamped: %q", f.opts["%0"])
	}
	sent, ok := m.send("anything at all")().(sentMsg)
	if !ok {
		t.Fatal("the send produced no sentMsg")
	}
	if len(sent.results) != 1 || sent.results[0].Outcome != broadcast.Refused {
		t.Fatalf("results = %+v, want one refusal", sent.results)
	}
	if got := f.paneScreen("%0"); got != "" {
		t.Errorf("text reached a pane on a read-only host: %q", got)
	}
}

// A round that overruns its deadline must come back reporting its panes UNIDENTIFIED.
// Two things would otherwise be wrong at once: the round never returns, so that host is
// never identified again for the rest of the session; and until it does, Identified
// keeps answering from the last completed round, so a pane whose agent has exited still
// reads as an agent and still carries its token.
func TestAHostWhoseRoundTimesOutReadsAsUnidentified(t *testing.T) {
	f := newFakeTmux("%0")
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })

	m := builtModel(t, f, nil, livePane("%0", "work"))
	m, _ = press(t, m, runes(" "))
	if !m.keeper.Identified(hub.LocalLabel, "%0") {
		t.Fatal("the first round did not identify the pane, so there is no stale yes to go stale")
	}

	// The host stops answering, under a deadline a test can wait out.
	m.walk = func(hub.Host) proc.Walker { return blockingWalker{release} }
	m.identTimeout = 200 * time.Millisecond

	cmd := m.identify()
	if cmd == nil {
		t.Fatal("no round started")
	}
	msgs := collect(t, cmd, 1, 3*time.Second)
	if len(msgs) == 0 {
		t.Fatal("the round never came back: a stalled host has to bound itself, or " +
			"identification stops on it for the rest of the session")
	}
	got, ok := identMsgFor(msgs, hub.LocalLabel)
	if !ok {
		t.Fatalf("the round reported %v, want an identMsg for the local host", msgs)
	}
	if got.err == "" {
		t.Error("a round that timed out reported success")
	}
	if m.keeper.Identified(hub.LocalLabel, "%0") {
		t.Error("a pane on a host that stopped answering still reads as identified — the " +
			"hub would paste into it on the strength of an answer it can no longer check")
	}

	next, _ := m.Update(got)
	m = next.(model)
	if m.identBusy[hub.LocalLabel] {
		t.Error("the timed-out round is still recorded as out, so the host is locked out for good")
	}
	if m.identWarning() == "" {
		t.Error("nothing says why every pane there is unidentified, and the dialog is " +
			"where the user would read it")
	}
}
