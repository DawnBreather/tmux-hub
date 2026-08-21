package ui

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/DawnBreather/tmux-hub/internal/fleetcache"
	"github.com/DawnBreather/tmux-hub/internal/hostset"
)

// THE TWO WIRES OF THE DISCOVERED SECTION, and neither had a test.
//
// Everything else about the section — the graph, the diagnosis, the buckets, the frame at 80 columns
// — is covered in discovered_test.go, and all of it is reached by a fixture that assigns
// `m.discovered` or calls `m.crawlBehind()` directly. So the two lines that make the feature happen
// on a real keystroke were unguarded, and a verifier proved it by deleting them: with
// `openPicker`'s crawl dispatch removed the whole default suite reported `ok` for internal/ui AND
// cmd/tmux-hub at rc=0, and with the probe handler's `learnFromProbe` call removed it did the same.
// A screen defined, tested and never called is this repository's signature defect; this is that
// defect with the screen already written.
//
// The two cases below are therefore deliberately about the CALL and its EFFECT rather than about the
// section's contents: press the key a person presses, and require that a hop was asked and that what
// the round measured was remembered. cmd/tmux-hub/wiring_test.go carries a per-symbol row for each of
// the same two wires, which is the cheap mechanical half — a row can prove a call exists and can
// never prove it does anything, and those are different questions.

// drainCmd runs a command and every command a `tea.Batch` inside it holds, and returns the messages.
//
// It exists because `openPicker` BATCHES the crawl beside the probe, so the keystroke's answer is a
// `tea.BatchMsg` rather than the message under test. `tea.Batch` also returns its single argument
// unchanged when there is only one, which is why this has to handle both shapes rather than assuming
// the batch — the same wrinkle statusline_test.go records.
//
// The depth bound is a guard against a command that batches itself: this is test scaffolding around
// a production dispatch, and an infinite walk here would look like a hung suite rather than a defect.
func drainCmd(t *testing.T, cmd tea.Cmd) []tea.Msg {
	t.Helper()
	var out []tea.Msg
	var walk func(tea.Cmd, int)
	walk = func(c tea.Cmd, depth int) {
		if c == nil {
			return
		}
		if depth > 4 {
			t.Fatalf("a command was still batching commands four levels down")
		}
		msg := c()
		if batch, ok := msg.(tea.BatchMsg); ok {
			for _, sub := range batch {
				walk(sub, depth+1)
			}
			return
		}
		out = append(out, msg)
	}
	walk(cmd, 0)
	return out
}

// crawlOf finds the crawl's own message among what a keystroke dispatched, and says so when there is
// none. The message TYPE is the needle rather than the screen, because the section paints from
// `m.discovered` and a fixture that had assigned that field would satisfy a screen-shaped assertion
// without any crawl having run.
func crawlOf(t *testing.T, msgs []tea.Msg) (discoveredMsg, bool) {
	t.Helper()
	for _, msg := range msgs {
		if d, ok := msg.(discoveredMsg); ok {
			return d, true
		}
	}
	return discoveredMsg{}, false
}

// `p` is what opens the picker (§9: the screen is always reachable from the dashboard), and the crawl
// runs on every opening. The key is driven rather than `openPicker` being called, because the wire
// under test is one line inside that function and the operator's path to it is the keystroke — a test
// that called `crawlBehind` itself is what left this unguarded in the first place.
//
// The assertion is that a HOP WAS ASKED, in the port's own recording, and that what it answered
// reaches the screen. A hop the file disabled must not be asked at all: it has no master to travel
// over, so a round would spend a whole connect timeout per alias on a machine nobody wants.
func TestOpeningThePickerAsksEveryMountedHopWhatItDeclares(t *testing.T) {
	m := base(t, 80, 24)
	m.store = newFleetStore()
	m.home = t.TempDir()
	m.pickerKept = []hostset.Entry{{Alias: "nuc", Enabled: true}, {Alias: "off", Enabled: false}}

	// The port is called from a tea.Cmd body, so the recorder takes a lock rather than care — the
	// rule CLAUDE.md states for anything reachable from both Update and a command.
	var mu sync.Mutex
	var asked []string
	m.pickerPorts.Behind = func(_ context.Context, hop string) ([]hostset.Candidate, error) {
		mu.Lock()
		defer mu.Unlock()
		asked = append(asked, hop)
		return []hostset.Candidate{{Alias: "leaf", Via: hop, Recipe: map[string]string{
			"hostname": "leaf.internal", "user": "dev", "identityfile": "~/.ssh/hop-only"}}}, nil
	}

	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'p'}})
	opened := next.(model)
	if opened.mode != modePicker {
		t.Fatalf("`p` left the hub on %v, so this case is not about the picker at all", opened.mode)
	}
	if cmd == nil {
		t.Fatal("`p` opened the picker and dispatched nothing — with one enabled hop and a Behind " +
			"port the crawl is what has to run, and the section can only ever be empty without it")
	}
	msg, ok := crawlOf(t, drainCmd(t, cmd))
	if !ok {
		t.Fatal("nothing the keystroke dispatched was a crawl round: the picker opens and the " +
			"machines behind the hops are never asked for, so `Behind your hops` never populates")
	}

	mu.Lock()
	got := strings.Join(asked, ",")
	mu.Unlock()
	if got != "nuc" {
		t.Errorf("the crawl asked %q, want only the enabled hop", got)
	}
	if len(msg.rows) != 1 || msg.rows[0].Label != "leaf" || msg.rows[0].Observer != "nuc" {
		t.Fatalf("the round produced %+v, want one row for leaf @nuc", msg.rows)
	}
	// End to end, because a round whose rows never reach the screen is the same defect one step
	// along: the operator's evidence is the frame, not the message.
	after, _ := opened.Update(msg)
	if screen := after.(model).View(); !strings.Contains(screen, "leaf") {
		t.Errorf("the machine the hop declared is not on the picker:\n%s", screen)
	}

	// THE POSITIVE CONTROL for the detector above: with no way to look behind a hop the same
	// keystroke must produce no crawl message. Without this, `crawlOf` matching nothing would be
	// indistinguishable from a message type that never existed, and the case would pass on a hub
	// where the picker dispatches something else entirely.
	blind := m
	blind.pickerPorts.Behind = nil
	_, blindCmd := blind.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'p'}})
	if _, found := crawlOf(t, drainCmd(t, blindCmd)); found {
		t.Error("a hub with no Behind port produced a crawl round anyway, so the message above " +
			"says nothing about the port")
	}
}

// The sibling wire, and it fails in the direction that looks like success: the section still paints,
// still orders, and simply forgets every figure the moment the hub exits — so the next opening waits
// for a probe before it can order anything, which is the whole cost fleetcache exists to remove.
//
// The round arrives as a MESSAGE, which is where the missing call was (the probe handler in
// model.go), and the effect is read out of a real cache through the reader production reads with.
// Asserting on the map handed to a stub `Learn` would have been an assertion about the argument; the
// file is what the next run opens.
func TestWhatAProbeRoundMeasuredIsRememberedByTheHub(t *testing.T) {
	cache := openFleetCache(t)
	m := pickerModel(t, 80, 24, samplePickerRows(t), hosts2())
	m.store = newFleetStore()
	m.pickerPorts.Facts, m.pickerPorts.Learn = cache.Facts, cache.Record

	// One host that answered and one that timed out, so the case cannot pass by filing everything:
	// the round's own filter is what keeps a deadline from being remembered as a distance.
	next, _ := m.Update(pickerProbedMsg{
		cands: []hostset.Candidate{{Alias: "nuc"}, {Alias: "slow"}},
		results: []hostset.Result{
			{Alias: "nuc", Version: "3.2a", Usable: true, Took: 40 * time.Millisecond},
			{Alias: "slow", TimedOut: true, Took: 10 * time.Second},
		},
	})
	if _, ok := next.(model); !ok {
		t.Fatalf("the probe handler answered %T", next)
	}

	if cache.Len() == 0 {
		t.Fatal("a probe round reached the model and nothing was remembered — every opening of the " +
			"picker then orders the discovered section by name until a fresh probe has answered")
	}
	f, ok := cache.Facts(fleetcache.Key{Alias: "nuc"})
	if !ok {
		t.Fatalf("the host that answered was not remembered; the cache holds %d machines", cache.Len())
	}
	if f.RTT != 40*time.Millisecond || f.TmuxVersion != "3.2a" {
		t.Errorf("what was remembered for nuc is %+v, want 40ms and 3.2a", f)
	}
	// The key is the ROOT's own alias and not a fingerprint, deliberately (a proxied handshake
	// reports the JUMP host's key), and it is the key the hop rows read to inherit their bound — so a
	// round filed under any other key would be remembered and still never order anything.
	if _, ok := cache.Facts(fleetcache.Key{Alias: "slow"}); ok {
		t.Error("the timed-out probe was filed as a round trip, so the operator's own deadline is " +
			"now this machine's distance")
	}
}
