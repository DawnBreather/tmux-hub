package configdir

import (
	"testing"

	"github.com/DawnBreather/tmux-hub/internal/statedir"
)

func TestPathHonoursXDGConfigHome(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/xdg")
	if got, want := Path("hosts.toml"), "/xdg/tmux-hub/hosts.toml"; got != want {
		t.Fatalf("Path = %q, want %q", got, want)
	}
}

func TestPathFallsBackToHome(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", "/home/u")
	if got, want := Path("hosts.toml"), "/home/u/.config/tmux-hub/hosts.toml"; got != want {
		t.Fatalf("Path = %q, want %q", got, want)
	}
}

func TestPathReturnsBareNameWhenHomeIsUnreadable(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", "")
	// With no XDG_CONFIG_HOME and no HOME, os.UserHomeDir() fails and the function
	// returns the bare name. Consequence if this ever breaks: the host list lands in
	// the process's cwd instead of the config dir. Same rule as statedir.Path, and
	// asserted separately because "the two packages agree" is not something either
	// one's own test can see.
	if got, want := Path("hosts.toml"), "hosts.toml"; got != want {
		t.Fatalf("Path = %q, want %q", got, want)
	}
}

// The split is the invariant, so it gets its own assertion: config and state must not
// resolve to the same directory. Both packages join `tmux-hub` onto a base, so a
// copy-paste that left `XDG_STATE_HOME` in this file would produce two helpers that
// agree on everything and silently put the user's decisions back where nobody
// documented them — which is the defect this package was split out to fix.
func TestConfigAndStateAreDifferentDirectories(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/xdg/config")
	t.Setenv("XDG_STATE_HOME", "/xdg/state")
	cfg, st := Path("hosts.toml"), statedir.Path("hosts.toml")
	if cfg == st {
		t.Fatalf("config and state resolved to the same path %q", cfg)
	}
	if got, want := cfg, "/xdg/config/tmux-hub/hosts.toml"; got != want {
		t.Errorf("configdir.Path = %q, want %q", got, want)
	}
	if got, want := st, "/xdg/state/tmux-hub/hosts.toml"; got != want {
		t.Errorf("statedir.Path = %q, want %q", got, want)
	}

	// And the two must not collapse when only ONE of the variables is set, which is
	// the common real environment: systemd sets XDG_RUNTIME_DIR and a login shell
	// often sets neither of these. Falling back to $HOME must still separate them.
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_STATE_HOME", "")
	t.Setenv("HOME", "/home/u")
	if Path("hosts.toml") == statedir.Path("hosts.toml") {
		t.Fatalf("with both variables unset the two dirs collapsed to %q", Path("hosts.toml"))
	}
}
