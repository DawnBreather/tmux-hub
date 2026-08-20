//go:build e2e

package e2e

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestE2ETheBinaryReadsTheDocumentedHostsPath asks the real binary where it looks for
// hosts.toml, because that is the only question that was ever wrong.
//
// The defect it guards had no symptom. `hostset.DefaultPath` resolved to
// $XDG_STATE_HOME/tmux-hub/hosts.toml while README.md and docs/design.md §9 both
// documented ~/.config/tmux-hub/hosts.toml, so a user who followed the documentation
// hand-edited a file that was never opened — and an absent host list is a legitimate
// empty fleet by design (§16), so nothing complained. Every unit test passed
// throughout, because each of them passes an explicit path.
//
// The probe is a hosts.toml the parser MUST refuse. That makes "was this file read?"
// answerable with no network, no tmux server and no master: `unknown key "nonsense"`
// can only be produced by parsing this file, so its presence proves the read and its
// absence proves the path was ignored. A file the reader merely finds and accepts
// would prove neither — an empty fleet looks the same as a file nobody opened, which
// is the whole reason the defect survived.
func TestE2ETheBinaryReadsTheDocumentedHostsPath(t *testing.T) {
	bin := buildBinary(t)
	const broken = "[[host]]\nalias = \"nuc\"\nnonsense = 1\n"
	const parseErr = `unknown key "nonsense"`

	// Two arms over one binary, and the second is what makes the first mean something:
	// the same file at the state path must NOT be read. Without it this test would have
	// passed before the fix as well, since a run that reads neither location is
	// indistinguishable from a run that reads the wrong one.
	for _, arm := range []struct {
		name    string
		dir     func(cfg, state string) string
		wantErr bool
	}{
		{"the documented config path", func(cfg, _ string) string { return cfg }, true},
		{"the state path it used to read", func(_, state string) string { return state }, false},
	} {
		t.Run(arm.name, func(t *testing.T) {
			cfg, state := t.TempDir(), t.TempDir()
			dir := filepath.Join(arm.dir(cfg, state), "tmux-hub")
			if err := os.MkdirAll(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "hosts.toml"), []byte(broken), 0o600); err != nil {
				t.Fatal(err)
			}

			// `status` is the scriptable path — one poll cycle, no pty — and it reads the
			// host list through the same `-hosts` flag the dashboard does.
			cmd := exec.Command(bin, "status")
			// A hermetic environment, and TMUX_TMPDIR is the load-bearing part: it sends
			// the LOCAL server's socket into an empty temp directory, so the poll finds no
			// server instead of reading the operator's own. TMUX is cleared for the same
			// reason — tmux.LocalSocket prefers it when the hub runs inside tmux, and this
			// suite does.
			cmd.Env = append(os.Environ(),
				"XDG_CONFIG_HOME="+cfg,
				"XDG_STATE_HOME="+state,
				"XDG_RUNTIME_DIR="+t.TempDir(),
				"TMUX_TMPDIR="+t.TempDir(),
				"TMUX=",
			)
			out, err := cmd.CombinedOutput()

			if got := strings.Contains(string(out), parseErr); got != arm.wantErr {
				t.Fatalf("hosts.toml under %s: parse error present = %v, want %v\n"+
					"exit: %v\noutput:\n%s", arm.dir(cfg, state), got, arm.wantErr, err, out)
			}
			// The refusal must also be FATAL, not a warning: a monitor that reports on
			// fewer hosts than it was configured with is worse than one that fails.
			if arm.wantErr && err == nil {
				t.Errorf("the binary read a host list it could not parse and still exited 0:\n%s", out)
			}
		})
	}
}
