// Package hostset turns ~/.ssh/config into a set of hosts the hub polls.
//
// Two rules carry the whole design (docs/design.md §9). Candidacy is syntactic and
// generous: anything that could be a machine gets to try. MEMBERSHIP is a positive
// probe — `github.com` eliminates itself by answering `Invalid command: tmux -V`,
// and a name blacklist would need maintaining forever while a probe does not.
package hostset

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
)

// Candidate is a name from an ssh config. Skip is empty for something worth
// probing, and otherwise says why it is not.
type Candidate struct {
	Alias string
	Skip  string
}

// ParseSSHConfig reads the user's config and the system's, following the system
// config's Include — which is where systemd's ssh-proxy drop-in lives, and it
// contributes patterns that must be dropped without looking like patterns.
func ParseSSHConfig(userPath, systemPath string) []Candidate {
	var out []Candidate
	seen := map[string]bool{}
	for _, path := range append([]string{userPath}, includesOf(systemPath)...) {
		for _, name := range hostNames(path) {
			if seen[name] {
				continue
			}
			seen[name] = true
			out = append(out, Candidate{Alias: name, Skip: skipReason(name)})
		}
	}
	return out
}

// skipReason drops what cannot be a host the hub reaches by name.
//
// Wildcards are the obvious half. The other half is measured: systemd's drop-in
// declares `.host` and `machine/.host`, which contain no wildcard character at all,
// so a wildcard-only filter offers them to the probe as ordinary machines.
func skipReason(name string) string {
	switch {
	case strings.ContainsAny(name, "*?!"):
		return "a pattern, not a host"
	case strings.ContainsAny(name, "/%"):
		return "a systemd ssh-proxy pattern, not a host reachable by name"
	case name == ".host" || strings.HasPrefix(name, "."):
		return "systemd's local-machine alias, not a remote host"
	}
	return ""
}

func hostNames(path string) []string {
	f, err := os.Open(path)
	if err != nil {
		return nil // a missing config is the common case, not an error
	}
	defer f.Close()
	var out []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, rest, ok := strings.Cut(line, " ")
		if !ok || !strings.EqualFold(key, "host") {
			continue
		}
		out = append(out, strings.Fields(rest)...)
	}
	return out
}

// includesOf expands the system config's Include globs. It does not recurse: one
// level is what OpenSSH ships and what this machine has.
func includesOf(systemPath string) []string {
	f, err := os.Open(systemPath)
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		key, rest, ok := strings.Cut(line, " ")
		if !ok || !strings.EqualFold(key, "include") {
			continue
		}
		matches, _ := filepath.Glob(strings.TrimSpace(rest))
		out = append(out, matches...)
	}
	return out
}
