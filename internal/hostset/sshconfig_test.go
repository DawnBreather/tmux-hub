package hostset

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseSSHConfigDropsWhatIsNotAMachine(t *testing.T) {
	dir := t.TempDir()
	user := filepath.Join(dir, "config")
	os.WriteFile(user, []byte(`
Host nuc
    HostName nuc-clouddesk.DawnBreather.net
Host github.com dev.github.com orbits.github.com
    User git
Host web-app web-db
    User deploy
Host *.internal
    ProxyJump nuc
`), 0o600)

	system := filepath.Join(dir, "ssh_config")
	os.WriteFile(system, []byte("Include "+dir+"/conf.d/*.conf\n"), 0o644)
	os.MkdirAll(filepath.Join(dir, "conf.d"), 0o755)
	// systemd's drop-in, verbatim in shape. `.host` and `machine/.host` carry NO
	// wildcard character, so a filter that only drops `*` and `?` admits them as
	// ordinary machines — measured on this machine, where they are what
	// /etc/ssh/ssh_config.d/20-systemd-ssh-proxy.conf contributes.
	os.WriteFile(filepath.Join(dir, "conf.d", "20-systemd-ssh-proxy.conf"), []byte(`
Host .host machine/.host unix/* vsock/* machine/* vsock-mux/*
    ProxyCommand /usr/lib/systemd/systemd-ssh-proxy %h %p
`), 0o644)

	got := ParseSSHConfig(user, system)
	kept := map[string]bool{}
	skipped := map[string]string{}
	for _, c := range got {
		if c.Skip == "" {
			kept[c.Alias] = true
		} else {
			skipped[c.Alias] = c.Skip
		}
	}

	for _, want := range []string{"nuc", "github.com", "dev.github.com", "orbits.github.com", "web-app", "web-db"} {
		if !kept[want] {
			t.Errorf("%s should be a candidate (the PROBE decides membership, not the name)", want)
		}
	}
	for _, gone := range []string{"*.internal", ".host", "machine/.host", "unix/*", "vsock/*", "machine/*", "vsock-mux/*"} {
		if kept[gone] {
			t.Errorf("%s must not be a candidate", gone)
		}
		if skipped[gone] == "" {
			t.Errorf("%s was dropped with no reason recorded", gone)
		}
	}
}

// Multi-name Host lines expand, which is how five of this machine's twenty
// candidates exist at all.
func TestParseSSHConfigExpandsMultiNameHostLines(t *testing.T) {
	dir := t.TempDir()
	user := filepath.Join(dir, "config")
	os.WriteFile(user, []byte("Host a b c\n    User x\n"), 0o600)
	got := ParseSSHConfig(user, filepath.Join(dir, "absent"))
	if len(got) != 3 {
		t.Fatalf("got %d candidates, want 3: %+v", len(got), got)
	}
}

// A missing system config is the common case and must not be an error.
func TestParseSSHConfigToleratesAMissingFile(t *testing.T) {
	if got := ParseSSHConfig(filepath.Join(t.TempDir(), "nope"), filepath.Join(t.TempDir(), "nope")); len(got) != 0 {
		t.Fatalf("got %+v, want none", got)
	}
}

// Not a gate: a look at this machine's real files, for when the fixture and reality
// disagree. Measured when this was written: 20 candidates, 0 dropped from the user
// config (it has no wildcard Host lines) and 10 dropped from the systemd drop-in.
func TestParseSSHConfigAgainstTheRealFiles(t *testing.T) {
	if os.Getenv("HOSTSET_REAL") == "" {
		t.Skip("HOSTSET_REAL unset")
	}
	home, _ := os.UserHomeDir()
	got := ParseSSHConfig(filepath.Join(home, ".ssh", "config"), "/etc/ssh/ssh_config")
	for _, c := range got {
		t.Logf("%-24s %s", c.Alias, c.Skip)
	}
}
