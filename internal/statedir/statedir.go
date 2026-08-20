// Package statedir owns where the hub keeps state that must survive a restart.
//
// One owner, because there are now two such files (the send log and the hidden
// set) and a second copy of the XDG rules is a second place to get them wrong.
package statedir

import (
	"os"
	"path/filepath"
)

// Path returns the absolute path for a named state file.
//
// The last-resort return is the bare name, i.e. the current directory: a hub
// that cannot find a home directory still runs, it just keeps state where it
// was started. That matches what history.DefaultPath did before this package.
func Path(name string) string {
	base := os.Getenv("XDG_STATE_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return name
		}
		base = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(base, "tmux-hub", name)
}
