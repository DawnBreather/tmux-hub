// Package configdir owns where the hub keeps the DECISIONS a user makes.
//
// It is deliberately a second package beside internal/statedir rather than a
// parameter on it, and the duplication is the point: XDG splits a user's own
// choices ($XDG_CONFIG_HOME) from data a program derived for itself
// ($XDG_STATE_HOME), and that split is a rule about which file goes where, not a
// detail of how a path is joined. Two packages make the wrong answer unspellable at
// the call site — `configdir.Path("history.jsonl")` reads as obviously wrong, while
// one helper taking a "kind" argument would accept it silently.
//
// The file this exists for is hosts.toml, which is the user's host list: which hosts
// they keep, their tags, and any tmux_args override. docs/design.md §9 makes it
// explicitly hand-editable, so it belongs where a person looks for editable
// configuration. hidden.json, history.jsonl and states.jsonl stay in statedir,
// because the hub derived all three and nobody edits them by hand.
//
// It was one package for a while, and hosts.toml was the file that paid for it:
// $XDG_STATE_HOME/tmux-hub/hosts.toml was read while both the README and §9
// documented ~/.config/tmux-hub/hosts.toml. An absent hosts.toml is a legitimate
// empty fleet by design, so following the documentation produced a file that was
// never read and never complained about.
package configdir

import (
	"os"
	"path/filepath"
)

// Path returns the absolute path for a named config file.
//
// The last-resort return is the bare name, i.e. the current directory: a hub that
// cannot find a home directory still runs, it just keeps its config where it was
// started. That is statedir.Path's rule too, and for the same reason — the hub
// starting is worth more than the file being in the canonical place.
func Path(name string) string {
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return name
		}
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "tmux-hub", name)
}
