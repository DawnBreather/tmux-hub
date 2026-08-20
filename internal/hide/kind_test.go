package hide

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DawnBreather/tmux-hub/internal/registry"
	"github.com/DawnBreather/tmux-hub/internal/state"
)

// The key carries KIND, and without it a pane-less row and a real tmux pane could share one.
//
// An agent row has no window, no index and no start command, so three of the five components were
// zero-valued and the key degenerated to (host, name). A pane at window 0, index 0 of a session with
// that name produced the IDENTICAL key. The `x` guard added in this branch refused the agent
// direction; this closes the pane direction too, and closes it structurally — with `Kind` in the key
// the two cannot collide, so neither direction needs a guard to notice.
//
// Measured before: one press of `x` on a legitimate pane took a two-row screen to `0 sessions ·
// 2 hidden`, taking the pane-less row nobody had marked.
func TestAnAgentRowAndAPaneCannotShareAKey(t *testing.T) {
	agent := registry.Pane{
		Kind: registry.KindAgent, Host: "nuc", Session: "deploy-audit",
		AgentID: "84dc5a2e", PaneID: "agent:84dc5a2e@1a2b3c4d",
		ClassifiedState: state.Error,
	}
	// The shape that used to collide: window 0, index 0, no start command.
	pane := registry.Pane{
		Kind: registry.KindPane, Host: "nuc", Session: "deploy-audit",
		Window: "0", WindowIndex: 0, Index: 0, PaneID: "%0",
		Command: "claude", ClassifiedState: state.Works,
	}
	if KeyOf(agent) == KeyOf(pane) {
		t.Errorf("an agent row and a pane still share one key:\n  %+v", KeyOf(agent))
	}
	if KeyOf(agent).Kind != registry.KindAgent || KeyOf(pane).Kind != registry.KindPane {
		t.Errorf("the key does not carry the kind: %+v / %+v", KeyOf(agent), KeyOf(pane))
	}
}

// Hiding the pane must not hide the row beside it, which is the direction the guard could not see:
// the guard refuses an AGENT subject, and a pane is a legitimate subject.
func TestHidingAPaneLeavesTheAgentRowVisible(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "hidden.json"))
	if err != nil {
		t.Fatal(err)
	}
	agent := registry.Pane{
		Kind: registry.KindAgent, Host: "nuc", Session: "deploy-audit",
		AgentID: "84dc5a2e", PaneID: "agent:84dc5a2e@1a2b3c4d", ClassifiedState: state.Error,
	}
	pane := registry.Pane{
		Kind: registry.KindPane, Host: "nuc", Session: "deploy-audit",
		Window: "0", WindowIndex: 0, Index: 0, PaneID: "%0",
		Command: "claude", ClassifiedState: state.Works,
	}
	if err := s.Toggle(pane); err != nil {
		t.Fatal(err)
	}
	if !s.Hidden(pane) {
		t.Error("the pane the operator hid is not hidden")
	}
	if s.Hidden(agent) {
		t.Error("hiding a pane hid the pane-less row beside it")
	}
	if s.Marked(KeyOf(agent)) {
		t.Error("the agent row's key was marked by a press on the pane")
	}
}

// The file's version has to move with the key's SHAPE, or a record written under the old shape reads
// as a real key under the new one: an absent `kind` arrives as "", and "" is not a kind any row has,
// so an old record would match nothing — but the reverse of that argument is what the version exists
// for, and this package refuses rather than reasons about it.
func TestAV2FileIsRefusedWithAReasonNamingTheChange(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hidden.json")
	body := `{"v":2,"keys":[{"host":"nuc","session":"deploy-audit","window_index":0,` +
		`"pane_index":0,"start":""}]}`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if s.Warning() == "" {
		t.Fatal("a v2 file was accepted silently; a key whose shape changed must un-hide visibly")
	}
	if !strings.Contains(s.Warning(), "v2") || !strings.Contains(s.Warning(), "v3") {
		t.Errorf("the warning does not name both versions: %q", s.Warning())
	}
	// And nothing from it survived: gaining a mark hides a pane nobody hid.
	pane := registry.Pane{Kind: registry.KindPane, Host: "nuc", Session: "deploy-audit"}
	if s.Hidden(pane) {
		t.Error("a mark from the old shape survived the version change")
	}
}
