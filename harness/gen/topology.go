package main

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/DawnBreather/tmux-hub/internal/conf"
)

// Topology is a declared fleet: spec §4's file, loaded.
//
// It is the same vocabulary the derived graph uses, on purpose — two schemas would put a
// translation in every test, and this repository has paid three times for one rule living in
// two places. The generator builds the fleet from this; the oracle reads the same bytes.
//
// The json tags are a WIRE FORMAT, not decoration: `Generate` writes this struct to
// `.out/fleet.json` so a consumer — the harness's own self-test today, the oracle of
// "derived graph == declared topology" later — can read what was built without importing this
// command, which is `package main` and therefore unimportable, and without growing a second
// TOML reader.
type Topology struct {
	Name     string    `json:"name"`
	Machines []Machine `json:"machines"`
	Edges    []Edge    `json:"edges"`
	// Networks is every network any machine joins, in first-declaration order, so an address
	// plan derived from it is stable across runs and reviewable in a diff.
	Networks []string `json:"networks"`
}

// Machine is one container. Every field is a row of spec §4.1's table.
type Machine struct {
	ID       string   `json:"id"`
	Aliases  []string `json:"aliases"`
	Hostname string   `json:"hostname,omitempty"`
	Shell    string   `json:"shell"`
	Tmux     string   `json:"tmux,omitempty"`
	Networks []string `json:"networks"`
	Hub      bool     `json:"hub,omitempty"`
	Declares []string `json:"declares,omitempty"`
	// Line is where the record was read from, for a refusal an author can act on. It is not part
	// of the wire format: a consumer that keyed on it would be keyed on the file's formatting.
	Line int `json:"-"`
}

// Edge annotates a pair of machines that already share a network: how slow the link is, and
// whose key the far end accepts. Reachability comes from the networks; an edge never creates
// it, which is why an edge whose endpoints share nothing is a refusal rather than a wire.
type Edge struct {
	From  string `json:"from"`
	To    string `json:"to"`
	Delay string `json:"delay,omitempty"`
	Key   string `json:"key,omitempty"`
	Line  int    `json:"-"`
}

// The tmux versions this harness can build, and the image each comes from. The plan's ruling:
// PACKAGED tmux only, because 3.7b is newer than any distribution and the requirement is
// version heterogeneity rather than a specific version. Measured 2026-08-20 against the
// distributions themselves — debian trixie publishes 3.5a-3, ubuntu jammy 3.2a-4ubuntu0.2.
//
// A version outside this table is refused at load, so the ruling is mechanical rather than a
// comment: adding a source-built image later adds one row here.
var baseFor = map[string]string{
	"3.2a": "ubuntu:22.04",
	"3.5a": "debian:trixie",
}

// noTmuxBase is the image for a machine that declares no tmux — a real case, since the probe
// has to report a machine that answers ssh and has no tmux, and §3.1 lists TmuxVersion as
// "what `tmux -V` answered, or absent".
const noTmuxBase = "ubuntu:22.04"

var shellKinds = map[string]bool{"posix": true, "nushell": true}

// LoadFile reads a topology from disk.
func LoadFile(path string) (Topology, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Topology{}, err
	}
	return Load(string(raw))
}

// Load parses and VALIDATES a topology. Generate cannot fail, so everything a bad file could
// do is refused here, with a sentence naming the row to edit.
func Load(content string) (Topology, error) {
	var top Topology

	if err := conf.Scan(onlyTable(content, "[fleet]", "fleet"), "fleet",
		func() {},
		func(key, value string, line int) error {
			switch key {
			case "name":
				v, err := str(value)
				if err != nil {
					return err
				}
				top.Name = v
			default:
				return fmt.Errorf("[fleet] has no key %q", key)
			}
			return nil
		}); err != nil {
		return Topology{}, err
	}

	var m *Machine
	if err := conf.Scan(onlyTable(content, "[[machine]]", "machine"), "machine",
		func() {
			top.Machines = append(top.Machines, Machine{})
			m = &top.Machines[len(top.Machines)-1]
		},
		func(key, value string, line int) error {
			m.Line = line
			switch key {
			case "id":
				return assign(&m.ID, value)
			case "hostname":
				return assign(&m.Hostname, value)
			case "shell":
				return assign(&m.Shell, value)
			case "tmux":
				return assign(&m.Tmux, value)
			case "aliases":
				return assignList(&m.Aliases, value)
			case "networks":
				return assignList(&m.Networks, value)
			case "declares":
				return assignList(&m.Declares, value)
			case "hub":
				v, err := conf.Bool(value)
				if err != nil {
					return err
				}
				m.Hub = v
			default:
				return fmt.Errorf("[[machine]] has no key %q", key)
			}
			return nil
		}); err != nil {
		return Topology{}, err
	}

	var e *Edge
	if err := conf.Scan(onlyTable(content, "[[edge]]", "edge"), "edge",
		func() {
			top.Edges = append(top.Edges, Edge{})
			e = &top.Edges[len(top.Edges)-1]
		},
		func(key, value string, line int) error {
			e.Line = line
			switch key {
			case "from":
				return assign(&e.From, value)
			case "to":
				return assign(&e.To, value)
			case "delay":
				return assign(&e.Delay, value)
			case "key":
				return assign(&e.Key, value)
			default:
				return fmt.Errorf("[[edge]] has no key %q", key)
			}
		}); err != nil {
		return Topology{}, err
	}

	if err := top.validate(); err != nil {
		return Topology{}, err
	}
	return top, nil
}

// onlyTable masks content down to ONE table so conf.Scan can read it.
//
// The topology carries three tables and conf.Scan reads one, REFUSING any other `[[…]]`
// header rather than skipping it — deliberately, since a hosts file read as an empty projects
// file is indistinguishable from a first run. So each pass blanks the lines it does not want,
// which Scan skips, and which leaves every remaining line at its original number: a refusal
// still names the line its author has to edit. The wanted header is rewritten to `[[name]]`
// because that is the only shape Scan matches, and `[fleet]` is a single TOML table; the
// rewrite is in memory and the file on disk is ordinary TOML.
func onlyTable(content, header, table string) string {
	lines := strings.Split(content, "\n")
	out := make([]string, len(lines))
	keep := false
	for i, raw := range lines {
		text := strings.TrimSpace(raw)
		switch {
		case strings.HasPrefix(text, "["):
			keep = text == header
			if keep {
				out[i] = "[[" + table + "]]"
			}
		case keep:
			out[i] = raw
		}
	}
	return strings.Join(out, "\n")
}

// str parses a quoted value, and adds the one hint the dialect needs.
//
// `internal/conf` treats only a line that BEGINS with `#` as a comment, so a `#` inside a
// value is safe — which means a comment written AFTER a value is part of that value and the
// file is refused. The refusal is then about quoting, which is true and unhelpful, so the
// cause is named instead. Every error here carries its fix, not just the breakage.
func str(value string) (string, error) {
	v, err := conf.String(value)
	if err != nil {
		if i := strings.Index(value, "#"); i > 0 {
			return "", fmt.Errorf("%v — this dialect has no trailing comments, so %q is part of the value; put the comment on its own line",
				err, strings.TrimSpace(value[i:]))
		}
		return "", err
	}
	return v, nil
}

func assign(dst *string, value string) error {
	v, err := str(value)
	if err != nil {
		return err
	}
	*dst = v
	return nil
}

func assignList(dst *[]string, value string) error {
	v, err := conf.StringArray(value)
	if err != nil {
		if i := strings.Index(value, "#"); i > 0 {
			return fmt.Errorf("%v — this dialect has no trailing comments, so %q is part of the value; put the comment on its own line",
				err, strings.TrimSpace(value[i:]))
		}
		return err
	}
	*dst = v
	return nil
}

// validate refuses everything Generate would otherwise realise wrongly and silently. Each
// refusal names the machine or the edge, because a topology is edited by hand.
func (t *Topology) validate() error {
	if t.Name == "" {
		return fmt.Errorf("[fleet] has no name")
	}

	byID := map[string]*Machine{}
	for i := range t.Machines {
		m := &t.Machines[i]
		if m.ID == "" {
			return fmt.Errorf("line %d: a [[machine]] has no id", m.Line)
		}
		if _, dup := byID[m.ID]; dup {
			return fmt.Errorf("line %d: machine %q is declared twice", m.Line, m.ID)
		}
		byID[m.ID] = m

		if len(m.Aliases) == 0 {
			m.Aliases = []string{m.ID}
		}
		var named bool
		for _, a := range m.Aliases {
			if a == m.ID {
				named = true
			}
		}
		if !named {
			return fmt.Errorf("line %d: machine %q is not among its own aliases %v — a declaration naming the id would resolve to nothing",
				m.Line, m.ID, m.Aliases)
		}
		if m.Shell == "" {
			m.Shell = "posix"
		}
		if !shellKinds[m.Shell] {
			return fmt.Errorf("line %d: machine %q asks for shell %q; this harness builds %s",
				m.Line, m.ID, m.Shell, list(keysOf(shellKinds)))
		}
		if m.Tmux != "" && baseFor[m.Tmux] == "" {
			return fmt.Errorf("line %d: machine %q asks for tmux %q, which no base image packages; the ruling is packaged tmux only, so %s — a source build costs minutes per image for a property two versions already give",
				m.Line, m.ID, m.Tmux, list(keysOf(baseFor)))
		}
		if len(m.Networks) == 0 {
			return fmt.Errorf("line %d: machine %q joins no network, so nothing can reach it", m.Line, m.ID)
		}
		for _, n := range m.Networks {
			if !contains(t.Networks, n) {
				t.Networks = append(t.Networks, n)
			}
		}
	}

	// An alias names one machine. Two machines under one alias is a topology that cannot be
	// realised — a stanza would resolve to whichever the generator wrote last.
	owner := map[string]string{}
	for _, m := range t.Machines {
		for _, a := range m.Aliases {
			if prev, dup := owner[a]; dup {
				return fmt.Errorf("alias %q names both %q and %q; an alias resolves to one machine", a, prev, m.ID)
			}
			owner[a] = m.ID
		}
	}
	for _, m := range t.Machines {
		for _, d := range m.Declares {
			if owner[d] == "" {
				return fmt.Errorf("line %d: machine %q declares %q, which is no machine's alias", m.Line, m.ID, d)
			}
		}
	}

	link := make([]string, len(t.Edges))
	for i := range t.Edges {
		e := &t.Edges[i]
		for _, end := range []string{e.From, e.To} {
			if byID[end] == nil {
				return fmt.Errorf("line %d: edge names %q, which is no machine's id", e.Line, end)
			}
		}
		if e.From == e.To {
			return fmt.Errorf("line %d: edge %q -> %q is a machine to itself; a cycle is declared with `declares`, not with an edge",
				e.Line, e.From, e.To)
		}
		if e.Delay != "" {
			if _, err := time.ParseDuration(e.Delay); err != nil {
				return fmt.Errorf("line %d: edge %q -> %q has delay %q: %v", e.Line, e.From, e.To, e.Delay, err)
			}
		}
		on := shared(byID[e.From].Networks, byID[e.To].Networks)
		if len(on) == 0 {
			return fmt.Errorf("line %d: edge %q -> %q share no network, so there is no link for it to describe",
				e.Line, e.From, e.To)
		}
		if len(on) > 1 {
			return fmt.Errorf("line %d: edge %q -> %q share %v, so which link the delay belongs to is ambiguous",
				e.Line, e.From, e.To, on)
		}
		link[i] = on[0]
	}

	// One delay per (machine, network), and no edge in somebody else's qdisc.
	//
	// A delay is realised as netem on ONE interface, and netem holds back everything that LEAVES
	// it — so that qdisc is in the path of every connection the machine makes on that network: the
	// replies at the far end, the requests at the near end, and a round trip is both. Two
	// consequences, and until 2026-08-20 only the first was refused:
	//
	//  1. Two DELAYED edges onto one interface: the second `tc qdisc replace` silently replaces
	//     the first, so one of the two figures is simply not in the fixture.
	//  2. Any OTHER edge with an endpoint on that interface — including one that declares no delay
	//     at all — is delayed by a figure it never declared. Measured: `a -> b delay=180ms` plus a
	//     bare `c -> b` on one network was ACCEPTED, and c's traffic from b came back 180 ms late
	//     while `c -> b` declared nothing. That is the same silent wrongness as (1), and the README
	//     rejects near-end placement on exactly this argument without noting the far end's mirror.
	//
	// Two passes, because the affected edge may be the undelayed one and may come first in the file.
	claimed := map[string]int{}
	for i := range t.Edges {
		e := &t.Edges[i]
		if e.Delay == "" {
			continue
		}
		key := e.To + " on " + link[i]
		if prev, dup := claimed[key]; dup {
			p := t.Edges[prev]
			return fmt.Errorf("line %d: edges %q -> %q and %q -> %q both delay %s, which is one interface; the second would silently replace the first",
				e.Line, p.From, p.To, e.From, e.To, key)
		}
		claimed[key] = i
	}
	for i := range t.Edges {
		e := &t.Edges[i]
		// The far end first, so an edge that declares nothing is diagnosed by the interface its own
		// replies leave through rather than by its requests.
		for _, end := range []string{e.To, e.From} {
			key := end + " on " + link[i]
			owner, held := claimed[key]
			if !held || owner == i {
				continue
			}
			o := t.Edges[owner]
			return fmt.Errorf("line %d: edge %q -> %q %s, but %q -> %q puts a %s qdisc on %s, which is in this edge's path — netem holds back everything that leaves an interface, so this edge's round trip would carry %s it does not declare; put the two links on separate networks",
				e.Line, e.From, e.To, declaredDelay(e.Delay), o.From, o.To, o.Delay, key, o.Delay)
		}
	}
	return nil
}

// declaredDelay reads an edge's delay for a refusal, so that "declares no delay" and "declares
// 5ms" are the same clause rather than two messages.
func declaredDelay(d string) string {
	if d == "" {
		return "declares no delay"
	}
	return "declares delay " + d
}

// Base is the image a machine is built from: its tmux version decides, because that version
// is the distribution's.
func (m Machine) Base() string {
	if b := baseFor[m.Tmux]; b != "" {
		return b
	}
	return noTmuxBase
}

// HasSSHConfig answers whether this machine holds a `~/.ssh/config` at all. It is one function
// because two places need the answer — the generator writes the file, the start-up copies it —
// and a machine that declares nothing must have NO file rather than an empty one (`nuc`
// measured: it has none, so a hop may genuinely offer nothing).
func (m Machine) HasSSHConfig() bool { return len(m.Declares) > 0 }

// HasMaterial answers whether the generator writes any per-machine file for this machine, and
// therefore whether its service mounts a directory at all. Without it a machine that declares
// nothing would mount a path the generator never wrote, and docker would CREATE that directory
// on the host — a root-owned empty directory appearing under `.out/`, which reads as an artefact
// the generator produced.
func (m Machine) HasMaterial() bool { return m.HasSSHConfig() || m.Hub }

func shared(a, b []string) []string {
	var out []string
	for _, x := range a {
		if contains(b, x) {
			out = append(out, x)
		}
	}
	return out
}

func contains(hay []string, needle string) bool {
	for _, h := range hay {
		if h == needle {
			return true
		}
	}
	return false
}

// keysOf answers in sorted order, because it feeds a refusal an operator reads and a message
// whose word order changes between runs is a message nobody can grep for.
func keysOf[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func list(items []string) string { return strings.Join(items, " or ") }
