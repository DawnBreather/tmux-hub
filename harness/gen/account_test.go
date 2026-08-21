package main

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The harness account name appears in at least four places (compose.go constant, harness_test.go
// literal, discovery_test.go literals, and the Dockerfile), and none of them can import from the
// others — the Dockerfile cannot read a Go constant. So this guard reads the canonical value from
// compose.go's `account` constant and verifies every other occurrence matches it, with a floor on
// how many it checked so an empty set cannot pass.
func TestEveryOccurrenceOfTheHarnessAccountNameAgrees(t *testing.T) {
	root := filepath.Join("..", "..")

	// Step 1: Read the canonical account name from compose.go.
	composePath := filepath.Join(root, "harness/gen/compose.go")
	composeBytes, err := os.ReadFile(composePath)
	if err != nil {
		t.Fatalf("read compose.go: %v", err)
	}
	accountRe := regexp.MustCompile(`account\s*=\s*"(\w+)"`)
	m := accountRe.FindSubmatch(composeBytes)
	if m == nil {
		t.Fatal("compose.go does not declare `account = \"...\"`")
	}
	canonical := string(m[1])
	t.Logf("canonical account name from compose.go: %q", canonical)

	// Step 2: Find all other occurrences and verify they match.
	type occurrence struct {
		file string
		line int
		text string
	}
	var found []occurrence

	// Scan files that should mention the account.
	checks := map[string][]string{
		"internal/e2e":  {"harness_test.go", "discovery_test.go"},
		"harness/image": {"Dockerfile"},
	}

	quotedRe := regexp.MustCompile(fmt.Sprintf(`"(%s)"`, canonical))
	dockerRe := regexp.MustCompile(fmt.Sprintf(`\b(%s)\b`, canonical))

	for dir, files := range checks {
		fullDir := filepath.Join(root, dir)
		for _, fname := range files {
			path := filepath.Join(fullDir, fname)
			f, err := os.Open(path)
			if err != nil {
				t.Fatalf("open %s: %v", path, err)
			}
			scanner := bufio.NewScanner(f)
			lineNum := 0
			for scanner.Scan() {
				lineNum++
				line := scanner.Text()
				trimmed := strings.TrimSpace(line)
				if strings.HasPrefix(trimmed, "//") {
					continue
				}

				var matched bool
				if strings.HasSuffix(fname, ".go") {
					matched = quotedRe.MatchString(line)
				} else {
					// Dockerfile: look for account in useradd, passwd, /home/ contexts.
					if strings.Contains(line, "useradd") || strings.Contains(line, "passwd") ||
						strings.Contains(line, "/home/") {
						matched = dockerRe.MatchString(line)
					}
				}

				if matched {
					found = append(found, occurrence{
						file: filepath.Join(dir, fname),
						line: lineNum,
						text: line,
					})
				}
			}
			f.Close()
			if err := scanner.Err(); err != nil {
				t.Fatalf("scan %s: %v", path, err)
			}
		}
	}

	if len(found) < 3 {
		t.Fatalf("found %d occurrences of %q, want at least 3 (harness_test.go, discovery_test.go, "+
			"Dockerfile) — the guard matched nothing, or a file moved", len(found), canonical)
	}

	t.Logf("checked %d occurrences of the harness account name %q, all match", len(found), canonical)

	// Step 3: Verify no OTHER account name appears in those same contexts.
	// This catches the case where someone changed compose.go but left old literals elsewhere.
	alternates := []string{"admin", "user", "testuser", "ubuntu", "debian"}
	for _, alt := range alternates {
		if alt == canonical {
			continue
		}
		altQuoted := fmt.Sprintf(`"%s"`, alt)
		for dir, files := range checks {
			fullDir := filepath.Join(root, dir)
			for _, fname := range files {
				path := filepath.Join(fullDir, fname)
				content, err := os.ReadFile(path)
				if err != nil {
					continue
				}
				if bytes.Contains(content, []byte(altQuoted)) {
					t.Errorf("%s contains %q but the canonical account is %q",
						filepath.Join(dir, fname), alt, canonical)
				}
			}
		}
	}
}
