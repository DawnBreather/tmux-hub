// Package hostset turns ~/.ssh/config into a set of hosts the hub polls.
//
// Two rules carry the whole design (docs/design.md §9). Candidacy is syntactic and
// generous: anything that could be a machine gets to try. MEMBERSHIP is a positive
// probe — `github.com` eliminates itself by answering `Invalid command: tmux -V`,
// and a name blacklist would need maintaining forever while a probe does not.
package hostset

import (
	"bufio"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Candidate is a name from an ssh config. Skip is empty for something worth
// probing, and otherwise says why it is not.
type Candidate struct {
	Alias string
	Skip  string

	// Via names the OBSERVER whose config declared this alias, and it is EMPTY for the root's
	// own — the machine the hub runs on has no name for itself in this vocabulary, and there is
	// exactly one root (fleet spec §3.1).
	//
	// It is on the candidate rather than on a wrapper because an alias is per-observer
	// vocabulary: `web` here and `web` on a hop are different machines (fleet spec §2.1), so a
	// bare alias is not an identity and a list of aliases from two observers cannot be merged by
	// name. Every consumer that shows or keys a candidate needs the observer beside it.
	Via string

	// Recipe is the transport `ssh -G` resolved for this alias ON its observer: the subset of
	// ssh's answer any consumer reads — `hostname`, `user`, `port`, `identityfile`, and
	// `proxyjump`/`proxycommand` when present. Empty when nothing resolved it, which for a
	// candidate from a hop means the round trip failed and Skip says what the hop said.
	//
	// The proxy keys are load-bearing rather than informational. A proxied connection reports
	// the JUMP host's key (fleet spec §2.2.1, measured: `ssh -v -J nuc dev-air` prints ONE
	// `Server host key` line and it is `nuc`'s), so under §2.3's set-intersection merging a
	// fingerprint attributed to the destination would fuse two machines into one node. This map
	// is where a consumer learns the connection was not direct.
	//
	// A REPEATED KEY KEEPS ITS FIRST VALUE — except `identityfile`, which carries EVERY line ssh
	// printed, joined by newlines in ssh's own try order. It is the only key that repeats (measured
	// 2026-08-20 on OpenSSH 10.3p1 over the whole 79-line answer) and the only one whose answer is a
	// set: it repeats when the stanza configures no IdentityFile, where ssh prints its five built-in
	// defaults, and it repeats once per directive when the stanza configures several.
	//
	// An earlier cut of this comment said the count "does not survive this map, and a consumer that
	// must have it has to re-resolve". `fleet.Diagnose` is exactly such a consumer and it does not
	// re-resolve — it parses this value AS the set — so the two halves of one rule disagreed and the
	// hop's commonest stanza came back Blocked, demanding a key ssh would never have needed. The
	// producer now answers the question the key asks; `recipeSetKeys` in remote.go holds the reason
	// and the measurement.
	Recipe map[string]string
}

// ParseSSHConfig reads the user's config and the system's, following the system
// config's Include — which is where systemd's ssh-proxy drop-in lives, and it
// contributes patterns that must be dropped without looking like patterns.
//
// A file the reader could not finish comes back as a ROW with a reason, in the same shape a pattern
// does, rather than as a shorter list: see unreadPast.
func ParseSSHConfig(userPath, systemPath string) []Candidate {
	var out []Candidate
	seen := map[string]bool{}
	systemIncludes, err := includesOf(systemPath)
	if err != nil {
		out = append(out, Candidate{Alias: systemPath, Skip: unreadPast(systemPath, err)})
	}
	for _, path := range append([]string{userPath}, systemIncludes...) {
		names, err := hostNames(path)
		for _, name := range names {
			if seen[name] {
				continue
			}
			seen[name] = true
			out = append(out, Candidate{Alias: name, Skip: skipReason(name)})
		}
		if err != nil {
			out = append(out, Candidate{Alias: path, Skip: unreadPast(path, err)})
		}
	}
	return out
}

// unreadPast is the reason a row carries when a config reader stopped before the end of a file, and
// it exists because bufio.Scanner reports that through Err() — which both readers below ignored, so
// everything declared after an over-long line came back as an ABSENCE. On a hop that is worse than
// on the root: `RemoteCandidates` then answers what a hop with no config answers, so a whole machine
// list goes missing with no row and no reason (fleet spec §3.2 invariant 4).
//
// It REPORTS rather than recovers, on purpose. bufio cannot resume after ErrTooLong, and enlarging
// the buffer moves the horizon instead of closing it — so what is owed is a row saying the read
// stopped, which is the same shape an Include horizon uses for the same reason.
//
// Two branches because the error is not always the limit: a hop's config is scanned from a string,
// where ErrTooLong is the only error there is, while the root's own files are scanned from disk and
// can fail for reasons of their own. Each branch names a remedy, since a reason without one is a
// complaint.
func unreadPast(source string, err error) string {
	if errors.Is(err, bufio.ErrTooLong) {
		return "a line in " + source + " is longer than the " +
			strconv.Itoa(bufio.MaxScanTokenSize/1024) + " KiB this reader takes, so nothing " +
			"declared after it was read — split that line, or declare the machines it hides in " +
			"the root's own ~/.ssh/config"
	}
	return "reading " + source + " stopped early (" + err.Error() + "), so anything declared after " +
		"that point is unread — fix that file, or declare the machines it hides in the root's own " +
		"~/.ssh/config"
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

func hostNames(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil // a missing config is the common case, not an error
	}
	defer f.Close()
	return hostNamesIn(f)
}

// hostNamesIn is the whole of this package's understanding of an ssh config's Host lines, and it
// takes a reader rather than a path so that a config read OFF A HOP goes through the same rule.
// Two copies of "what counts as a declared machine" would drift the day one of them changed, and
// the copy on the remote path is the one nobody would notice drifting.
//
// It returns the scanner's error beside the names, because a scan that STOPPED and a config that
// declares nothing more are otherwise the same answer — see unreadPast.
func hostNamesIn(r io.Reader) ([]string, error) {
	var out []string
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, rest, ok := cutKeyword(line)
		if !ok || !strings.EqualFold(key, "host") {
			continue
		}
		out = append(out, configFields(rest)...)
	}
	return out, sc.Err()
}

// configFields splits the rest of a keyword line into the names it declares, honouring the DOUBLE
// QUOTES ssh's own parser accepts there.
//
// It replaces strings.Fields for a measured reason. On OpenSSH 10.3p1, 2026-08-20, a config holding
// `Host "web"` answers `ssh -F cfg -G web` with that stanza's `hostname` — so ssh strips the quotes
// and the alias is `web`. `strings.Fields` kept them, and `"web"` is a name no probe can use: ssh
// itself answers `ssh -G '"web"'` with rc=255 `hostname contains invalid characters`.
//
// The quoted form that carries a space is the sharper half, and it is the same failure direction as
// the `Host = leaf` separator bug this file already fixed: `Host "my host"` became the two garbage
// names `"my` and `host"`, each arriving as a row whose refusal blames the operator's config for
// syntax ssh accepts. It is now ONE name, `my host`, refused once for the space — which is honest,
// because ssh refuses that alias too, with the same rc=255 sentence.
//
// A quote never opens a name by itself, so `Host ""` declares nothing rather than a candidate with
// an empty alias; and an UNBALANCED quote is read generously, as this package's own doc says
// candidacy is (`Host "web` yields `web`), because the probe is what decides membership.
func configFields(rest string) []string {
	var out []string
	var name strings.Builder
	quoted, started := false, false
	flush := func() {
		if started {
			out = append(out, name.String())
			name.Reset()
			started = false
		}
	}
	for _, r := range rest {
		switch {
		case r == '"':
			quoted = !quoted
		case !quoted && (r == ' ' || r == '\t'):
			flush()
		default:
			name.WriteRune(r)
			started = true
		}
	}
	flush()
	return out
}

// cutKeyword splits an ssh config line into its keyword and the rest of the line.
//
// It exists because ssh accepts THREE separators there and this package read one. Measured on
// OpenSSH 10.3p1, 2026-08-20, from a single config holding all three stanzas: `Host<TAB>tabbed`,
// `Host=equals` and `Host spaced` each resolve, and `ssh -F cfg -G <alias>` answered the right
// `hostname` for all three. A `strings.Cut(line, " ")` sees only the last.
//
// Both failure directions were live, and only one of them is an absence. A tab or a bare `=` made
// the line INVISIBLE, so a hop whose Host lines use one enumerates to nothing and `RemoteCandidates`
// answers exactly what a hop with no config answers — the silent horizon fleet spec §3.2 invariant 4
// forbids, arriving through the parser rather than through a budget. A spaced `Host = leaf`, by
// contrast, was read as TWO names, so the picker gained a candidate literally called `=`: a machine
// that does not exist, carrying a refusal that blames the operator for a separator ssh accepts.
//
// The keyword may not be empty (a line starting with the separator is not a keyword line), and
// neither may the rest: `Host` alone declares nothing, and ssh itself refuses it.
func cutKeyword(line string) (key, rest string, ok bool) {
	i := strings.IndexAny(line, " \t=")
	if i <= 0 {
		return "", "", false
	}
	rest = strings.TrimLeft(line[i:], " \t")
	rest = strings.TrimLeft(strings.TrimPrefix(rest, "="), " \t")
	if rest == "" {
		return "", "", false
	}
	return line[:i], rest, true
}

// includesOf expands the system config's Include globs. It does not recurse: one
// level is what OpenSSH ships and what this machine has.
func includesOf(systemPath string) ([]string, error) {
	f, err := os.Open(systemPath)
	if err != nil {
		return nil, nil
	}
	defer f.Close()
	globs, scanErr := includeGlobsIn(f)
	var out []string
	for _, glob := range globs {
		matches, _ := filepath.Glob(glob)
		out = append(out, matches...)
	}
	return out, scanErr
}

// includeGlobsIn returns the Include patterns a config names, unexpanded. It is separate from the
// globbing because a config read off a HOP can be scanned for them but not expanded: expanding
// needs the hop's filesystem, and the crawl reads a hop once. The remote path reports the
// unexpanded glob as a horizon instead (see RemoteCandidates).
func includeGlobsIn(r io.Reader) ([]string, error) {
	var out []string
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if strings.HasPrefix(line, "#") {
			continue
		}
		key, rest, ok := cutKeyword(line)
		if !ok || !strings.EqualFold(key, "include") {
			continue
		}
		out = append(out, configFields(rest)...)
	}
	return out, sc.Err()
}
