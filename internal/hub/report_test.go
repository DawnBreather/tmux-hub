package hub

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/DawnBreather/tmux-hub/internal/registry"
	"github.com/DawnBreather/tmux-hub/internal/state"
)

func TestBuildReportIsMachineShaped(t *testing.T) {
	hosts := []Host{{Label: "local", Status: Up, Version: "3.7b"}}
	panes := []registry.Pane{{
		Host: "local", PaneID: "%0", Session: "live1", Window: "w0", Command: "claude",
		ClassifiedState: state.Needs, Activity: time.Unix(1786450000, 0),
		Content: []string{"● Ran tests", "Do you want to proceed?"},
	}}
	rep := BuildReport(hosts, panes)
	b, err := json.Marshal(rep)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	s := string(b)
	for _, want := range []string{`"label":"local"`, `"status":"up"`, `"state":"needs"`, `"pane_id":"%0"`} {
		if !strings.Contains(s, want) {
			t.Errorf("report JSON missing %s: %s", want, s)
		}
	}
}

func TestBuildReportStripsANSI(t *testing.T) {
	panes := []registry.Pane{{
		Host: "local", PaneID: "%0", ClassifiedState: state.Idle,
		Content: []string{"\x1b[38;5;231m●\x1b[39m plain answer"},
	}}
	rep := BuildReport(nil, panes)
	got := rep.Panes[0].Content[0]
	if strings.Contains(got, "\x1b") {
		t.Fatalf("report content = %q, want no escape sequences", got)
	}
	if got != "● plain answer" {
		t.Fatalf("report content = %q, want the text intact", got)
	}
}

func TestBuildReportCarriesTheHostReason(t *testing.T) {
	hosts := []Host{{Label: "ghost", Status: Down, Reason: "socket is not there"}}
	rep := BuildReport(hosts, nil)
	if rep.Hosts[0].Reason == "" {
		t.Fatal("a Down host must carry its reason into the report")
	}
}
