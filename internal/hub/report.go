package hub

import (
	"github.com/DawnBreather/tmux-hub/internal/lines"
	"github.com/DawnBreather/tmux-hub/internal/registry"
)

// HostReport and PaneReport are the machine-readable shape of one poll cycle.
// This is the read path with a different renderer, so it inherits the poll
// path's purity for free. Broadcast is deliberately absent: reading is safe to
// automate, writing into a live agent is not (docs/design.md §16).
type HostReport struct {
	Label   string `json:"label"`
	Status  string `json:"status"`
	Reason  string `json:"reason,omitempty"`
	Version string `json:"version,omitempty"`
	// AgentsReason explains an absent Claude listing without implying the host is
	// unhealthy: no `claude` installed is not a fault of a tmux server.
	AgentsReason string `json:"agents_reason,omitempty"`
}

type PaneReport struct {
	// Kind says where the row came from: a tmux pane whose state was read off a
	// screen, or a Claude session whose state its own CLI reported as a fact.
	Kind   string `json:"kind"`
	Host   string `json:"host"`
	PaneID string `json:"pane_id"`
	// AgentID is the listing's SHORT id, and it is the only argument `claude attach|logs|stop`
	// accepts — measured, the full uuid answers `No job matching`. It is here because this report
	// is the only surface a script can read: without it `-status` could say a background job
	// wanted attention and not what to type to reach it. Omitted rather than empty for a row that
	// has none, so a reader can tell "no id" from "the id is blank" — an interactive session has
	// no short id at all (measured 0 of 7 on one host, 0 of 13 across two).
	AgentID string `json:"agent_id,omitempty"`
	// AlsoOn names the other hosts whose listing reported this same session. It is in the report
	// because the hub now COLLAPSES those rows into one, so without it a reader could not tell a
	// session that lives on one machine from one a shared `~/.claude` makes visible on several —
	// and the host this row names is only the one the hub chose to act through.
	AlsoOn   []string `json:"also_on,omitempty"`
	Session  string   `json:"session"`
	Window   string   `json:"window"`
	Command  string   `json:"command"`
	State    string   `json:"state"`
	Activity int64    `json:"activity_unix"`
	Content  []string `json:"content,omitempty"`
	// AgentWord and AgentPID are the CLAIM the row's state is quoting: the listing's own word for this
	// session, unfolded, and the pid the reporting host gave for it.
	//
	// They are here because the question "why does this row say that" cost three hours the day two
	// sessions were shown wrongly, and every hour of it was spent rebuilding by hand what the row
	// already held: which host spoke, what it said, and whether it could see the worker at all. A pid
	// is the last of those — a record carries one exactly when the host reporting it can see the
	// process, so `pid: null` beside a state is the reader's warning that the word came from a machine
	// sharing `~/.claude` and nothing else.
	//
	// Omitted when empty rather than zeroed, so a reader can tell "this row quotes no listing" — a
	// plain tmux pane that is nobody's Claude session — from "the listing said nothing".
	AgentWord   string `json:"agent_word,omitempty"`
	AgentStatus string `json:"agent_status,omitempty"`
	AgentPID    int    `json:"agent_pid,omitempty"`
}

type Report struct {
	Hosts []HostReport `json:"hosts"`
	Panes []PaneReport `json:"panes"`
}

func BuildReport(hosts []Host, panes []registry.Pane) Report {
	r := Report{}
	for _, h := range hosts {
		r.Hosts = append(r.Hosts, HostReport{
			Label: h.Label, Status: h.Status.String(), Reason: h.Reason, Version: h.Version,
			AgentsReason: h.AgentsReason,
		})
	}
	for _, p := range panes {
		// The JSON renderer strips ANSI: a monitor or a shell prompt consuming
		// this wants text, and escape sequences would be noise it has to undo.
		// The TUI keeps the escapes, because it is rendering to a terminal.
		content := make([]string, 0, len(p.Content))
		for _, l := range p.Content {
			content = append(content, lines.StripANSI(l))
		}
		r.Panes = append(r.Panes, PaneReport{
			Kind: p.Kind, Host: p.Host, PaneID: p.PaneID, AgentID: p.AgentID, AlsoOn: p.AlsoOn, Session: p.Session, Window: p.Window,
			Command: p.Command, State: p.State().String(),
			Activity: p.Activity.Unix(), Content: content,
			AgentWord: p.AgentWord, AgentStatus: p.AgentStatus, AgentPID: p.AgentPID,
		})
	}
	return r
}
