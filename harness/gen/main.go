// Command gen turns a declared topology into the container fleet that realises it.
//
//	go run ./harness/gen -topology harness/topology/basic.toml
//	docker compose -f harness/.out/compose.yaml up -d --build --wait
//
// Nothing it writes is committed: the compose file is derived, and the keypairs are secrets in
// a public repository (spec §4.2). `harness/.gitignore` is what enforces that.
package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

func main() {
	topology := flag.String("topology", "harness/topology/basic.toml", "the declared topology to realise")
	outDir := flag.String("out", "harness/.out", "where to write the compose file and the keys")
	flag.Parse()

	if err := run(*topology, *outDir); err != nil {
		fmt.Fprintln(os.Stderr, "harness/gen:", err)
		os.Exit(1)
	}
}

func run(topology, outDir string) error {
	top, err := LoadFile(topology)
	if err != nil {
		return err
	}
	out := Generate(top)

	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return err
	}
	composePath := filepath.Join(outDir, "compose.yaml")
	if err := os.WriteFile(composePath, []byte(out.Compose), 0o644); err != nil {
		return err
	}

	for _, rel := range sortedPaths(out.Files) {
		path := filepath.Join(outDir, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(path, []byte(out.Files[rel]), 0o644); err != nil {
			return err
		}
	}

	made, err := keypairs(filepath.Join(outDir, "keys"), out.Keys)
	if err != nil {
		return err
	}

	fmt.Printf("%s: %d machines, %d networks, %d edges\n",
		composePath, len(top.Machines), len(top.Networks), len(top.Edges))
	fmt.Printf("%d files, %d keypairs (%d newly generated)\n", len(out.Files), len(out.Keys), made)
	fmt.Printf("bring it up: docker compose -f %s up -d --build --wait\n", composePath)
	return nil
}

// keypairs generates the user keys, and REUSES one that is already there.
//
// Reuse is not an optimisation: `docker compose up` after a regeneration must not invalidate
// the authorized_keys a running container already installed, and a fresh key every run would
// make a re-run of one test case a different fleet from the last.
//
// A pair is reused only when BOTH halves are present. Reuse decided from the private key alone
// never repairs a `keys/` directory that lost a `.pub` — ssh-keygen writes two files and a run
// interrupted between them leaves one, and a hand-tidy is the other way in — and the container
// start-up then dies under `set -e` at `cat /harness/keys/<name>.pub >> …`, which is a fleet that
// will not come up for a reason no message names. The half-pair is removed rather than passed to
// ssh-keygen, which prompts on stdin when its target exists.
func keypairs(dir string, names []string) (int, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return 0, err
	}
	var made int
	for _, name := range names {
		path := filepath.Join(dir, name)
		if whole, err := completePair(path); err != nil {
			return made, err
		} else if whole {
			continue
		}
		cmd := exec.Command("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-C",
			"harness-"+name, "-f", path)
		if outBytes, err := cmd.CombinedOutput(); err != nil {
			return made, fmt.Errorf("ssh-keygen for %q: %v: %s", name, err,
				strings.TrimSpace(string(outBytes)))
		}
		made++
	}
	return made, nil
}

// completePair answers whether both halves of a keypair are on disk, and clears the way when only
// one is: the start-up copies the private key and appends the public one, so either half missing
// is a container that dies at start-up.
func completePair(path string) (bool, error) {
	_, priv := os.Stat(path)
	_, pub := os.Stat(path + ".pub")
	if priv == nil && pub == nil {
		return true, nil
	}
	for _, half := range []string{path, path + ".pub"} {
		if err := os.Remove(half); err != nil && !os.IsNotExist(err) {
			return false, fmt.Errorf("clearing the half-written keypair %q: %v", half, err)
		}
	}
	return false, nil
}

func sortedPaths(files map[string]string) []string {
	out := make([]string, 0, len(files))
	for path := range files {
		out = append(out, path)
	}
	sort.Strings(out)
	return out
}
