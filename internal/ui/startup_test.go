package ui

import (
	"context"
	"strings"
	"testing"

	"github.com/DawnBreather/tmux-hub/internal/hostset"
	"github.com/DawnBreather/tmux-hub/internal/hub"
)

// WithPicker is the picker's ONE production door, and until this file the only thing
// asserting it was a grep: `cmd/tmux-hub/wiring_test.go`'s call-site floor proves
// `ui.WithPicker(` is invoked, which is a different question from whether it does
// anything. Four mutations survived the whole suite — the `open` branch deleted, `open`
// forced either way, and `withKept` dropped — while this package's twenty-odd picker
// tests stayed green, because every one of them sets `m.mode = modePicker` by hand and
// calls `m.withKept(...)` directly. The screen was exhaustively tested through a door
// nobody opened.
//
// So these drive the real option through the real constructor, one effect at a time.
// They live HERE rather than in `cmd/tmux-hub` because the effects are fields of an
// unexported struct: reaching them from main's package would need an exported observer
// whose only caller is a test, which is the shape this repo's floor exists to punish.
// main's own decision — WHEN to pass `open` — is asserted in
// cmd/tmux-hub's TestThePickerOpensOnlyWhenTheFileHasDecidedNothing.

// startupModel builds the model the way Run does, with the picker wired as main wires
// it. TMUX is cleared so the constructor's own server lookup makes no tmux call.
func startupModel(t *testing.T, kept []hostset.Entry, open bool) model {
	t.Helper()
	t.Setenv("TMUX", "")
	ports := PickerPorts{
		Probe: func() ([]hostset.Candidate, []hostset.Result, error) { return nil, nil, nil },
		Save:  func([]hostset.Entry) error { return nil },
		Stop:  func(string) error { return nil },
	}
	// `reserved` is what main passes: this machine's own label plus every --host entry.
	// The one label matters here — a candidate called `local` must not be tickable, or the
	// picker writes a file the next startup exits 1 on.
	m := build(context.Background(), newFakeTmux(), nil, false, nil, nil, nil,
		WithPicker(ports, kept, []string{hub.LocalLabel}, open))
	m.width, m.height = 100, 24
	return m
}

// Effect one and two: `open` puts the picker on screen and arms the one probe that runs
// without a keystroke. Both are what §9's "shown at startup when hosts.toml has decided
// nothing" means, and §16 is why the probe is armed rather than run — ten hosts took
// 7.65 s and the first paint is promised in 50 ms, so Init asks after painting.
func TestWithPickerOpensTheScreenAndArmsTheFirstProbe(t *testing.T) {
	m := startupModel(t, nil, true)

	if m.mode != modePicker {
		t.Fatalf("mode = %v, want modePicker — a first run would show the dashboard and the "+
			"operator would never learn the picker exists", m.mode)
	}
	if !m.pickerAutoProbe {
		t.Error("pickerAutoProbe is false, so Init asks nothing and the screen a person " +
			"meets first lists zero of this machine's twenty candidates")
	}
	// The frame, because that is where any of this is visible.
	if out := m.View(); !strings.Contains(out, "Hosts") {
		t.Errorf("the first frame is not the picker:\n%s", out)
	}

	// And the other direction: a configured hub opens on the dashboard.
	d := startupModel(t, []hostset.Entry{{Alias: "nuc", Enabled: true}}, false)
	if d.mode == modePicker {
		t.Error("mode is modePicker with the file already decided — the picker would cover " +
			"the dashboard on every start")
	}
	if d.pickerAutoProbe {
		t.Error("a configured hub armed the startup probe, which spends 7.65 s re-answering " +
			"a question the file already holds")
	}
}

// Effect three, and the review called it the worst of the four: `withKept` installs
// hosts.toml's ticks. Without it the picker opens with nothing ticked, and `enter` then
// writes `enabled = false` over every host the user had — the same destruction loadKept
// refuses for a malformed file ("the next save would then overwrite it"), reached
// through another door.
func TestWithPickerCarriesTheFilesTicksOntoTheScreen(t *testing.T) {
	kept := []hostset.Entry{
		{Alias: "nuc", Enabled: true, Tags: []string{"work"}},
		{Alias: "eu", Enabled: false},
	}
	m := startupModel(t, kept, true)

	if len(m.pickerKept) != 2 {
		t.Fatalf("pickerKept = %+v, want both entries — this is what a save merges into, so "+
			"dropping it loses every tag the user wrote", m.pickerKept)
	}
	got := map[string]bool{}
	for _, e := range m.pickerKept {
		got[e.Alias] = e.Enabled
	}
	if !got["nuc"] || got["eu"] {
		t.Errorf("the file's decisions did not arrive: %+v", m.pickerKept)
	}
	// The tags are the half a row cannot show, and therefore the half a save built from
	// rows alone would silently drop.
	for _, e := range m.pickerKept {
		if e.Alias == "nuc" && len(e.Tags) != 1 {
			t.Errorf("nuc arrived without its tags: %+v", e)
		}
	}
}

// The ports are the third field WithPicker sets, and a nil one is not a crash by design —
// the key that needs it says why it cannot act. This asserts the wiring arrived, which is
// what makes `r`, `space` and `enter` do anything at all.
func TestWithPickerWiresThePortsThatMakeTheKeysWork(t *testing.T) {
	m := startupModel(t, nil, true)
	if m.pickerPorts.Probe == nil || m.pickerPorts.Save == nil || m.pickerPorts.Stop == nil {
		t.Fatal("a port is nil: `r` cannot probe, `enter` cannot save, and a host turned " +
			"off leaves its ssh master running for the rest of the day")
	}
	// A host with no ports at all is the state `p` was in before main wired it: the
	// screen opens and every key on it explains that it cannot act.
	bare := build(context.Background(), newFakeTmux(), []hub.Host{}, false, nil, nil, nil)
	if bare.pickerPorts.Probe != nil {
		t.Error("a hub built without WithPicker has a prober, so this test proves nothing")
	}
}
