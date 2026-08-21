package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/DawnBreather/tmux-hub/internal/hub"
	"github.com/DawnBreather/tmux-hub/internal/tmux"
)

// This file holds the assertion whose ABSENCE was the whole defect.
//
// docs/design.md §9 promised hosts.toml an optional socket override, and
// hostset.Entry.TmuxArgs was parsed, written, and round-trip tested — with no reader
// anywhere in production. So a user who hand-edited `tmux_args` into the file got
// something that saved and reloaded perfectly and changed nothing about what ran. A
// round trip structurally cannot tell: it only ever asks whether the writer and the
// reader agree, and they did.
//
// What settles it is a chain with a REAL PROCESS at the far end: a file on disk →
// hostsFrom → Host.Target() → the production seam → the argv a process was actually
// spawned with. Every link is a place the field was dropped or could be dropped again,
// and asserting the spawned argv means no link can be green on its own.

// sshArgvShim puts an `ssh` on PATH that appends its argv, one element per line, to a
// file, and returns that file's path. It is how the argv is read back from a real
// process without a live master.
//
// PATH is set with t.Setenv rather than on the command, because exec.Command resolves
// argv[0] against the PROCESS environment when it is constructed — a cmd.Env would not
// change which binary is found.
func sshArgvShim(t *testing.T) string {
	t.Helper()
	out := filepath.Join(t.TempDir(), "argv")
	dir := t.TempDir()
	shim := "#!/bin/sh\nfor a in \"$@\"; do printf '%s\\n' \"$a\" >> " + out + "; done\n"
	if err := os.WriteFile(filepath.Join(dir, "ssh"), []byte(shim), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return out
}

// pollThroughTheSeam writes a hosts.toml, reads it the way main does, and polls the one
// host it enables through the production seam. It returns the argv `ssh` was spawned
// with, minus argv[0].
//
// The command is the real poll shape, and the Target is NOT hand-built: Host.Target()
// is the only converter on purpose, because a hand-built Target is exactly how a field
// gets dropped at one call site and nowhere else.
func pollThroughTheSeam(t *testing.T, file string) []string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "hosts.toml")
	if err := os.WriteFile(path, []byte(file), 0o644); err != nil {
		t.Fatal(err)
	}
	hosts, err := hostsFrom(path, nil)
	if err != nil {
		t.Fatalf("hostsFrom: %v", err)
	}
	if len(hosts) != 1 {
		t.Fatalf("got %d hosts, want the one the file enables", len(hosts))
	}

	record := sshArgvShim(t)
	if _, err := tmux.NewExec(10*time.Second).Run(context.Background(), hosts[0].Target(),
		"list-panes", "-a", "-F", "#{pane_id}"); err != nil {
		t.Fatalf("Run through the seam: %v", err)
	}
	raw, err := os.ReadFile(record)
	if err != nil {
		t.Fatalf("the shim recorded no argv, so nothing was spawned: %v", err)
	}
	return strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n")
}

func assertSpawnedArgv(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("argv length = %d, want %d\ngot:  %q\nwant: %q", len(got), len(want), got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("argv[%d] = %q, want %q\nfull: %q", i, got[i], want[i], got)
		}
	}
}

// The override is hand-written into the file, and it must come out the far end of the
// seam in the position tmux reads it.
func TestASocketOverrideInTheHostsFileReachesTmux(t *testing.T) {
	rt := runtimeDir(t)

	// Hand-written rather than built with SaveHosts, because the user this protects
	// edits the file in a text editor. Going through the writer would put the round
	// trip back inside the very chain the round trip failed to check.
	got := pollThroughTheSeam(t, "[[host]]\n"+
		"alias = \"nuc\"\n"+
		"enabled = true\n"+
		"tmux_args = [\"-L\", \"work\"]\n")

	// The whole argv, exactly. A substring check on the payload would pass while the
	// override sat after the verb, where tmux refuses it, or in place of the control
	// path, where the poll would leave the master behind. The control path is the one
	// element derived rather than written out: hostsFrom builds it from
	// XDG_RUNTIME_DIR, and the master lifecycle derives it the same way or adopts
	// nothing.
	assertSpawnedArgv(t, got, []string{"-o", "BatchMode=yes", "-o", "ProxyCommand=false",
		"-S", hub.ControlPathFor(rt, "nuc"), "nuc",
		`tmux '-u' '-L' 'work' 'list-panes' '-a' '-F' '#{pane_id}'`})
}

// The other half of the same wire: a file with no `tmux_args` spawns the argv it always
// did. A test that only proves the override TRAVELS cannot see one invented out of
// nothing — an empty argument between the destination and the payload would make the
// far shell run `tmux` with an empty command name.
//
// `-u` is in both expectations because it is in every argv this codebase builds: the
// label format frames values by the byte length tmux reports, and a client with no
// UTF-8 locale emits one `_` per non-ASCII character while that length keeps reporting
// the stored size — which took a whole host dark (known-issues U1). These two cases are
// the only ones that read the argv off a REAL SPAWNED PROCESS, so they are where the
// flag being present end to end is actually established rather than argued.
func TestAHostWithNoOverrideStillSpawnsThePlainArgv(t *testing.T) {
	rt := runtimeDir(t)
	got := pollThroughTheSeam(t, "[[host]]\nalias = \"nuc\"\nenabled = true\n")
	assertSpawnedArgv(t, got, []string{"-o", "BatchMode=yes", "-o", "ProxyCommand=false",
		"-S", hub.ControlPathFor(rt, "nuc"), "nuc",
		`tmux '-u' 'list-panes' '-a' '-F' '#{pane_id}'`})
}
