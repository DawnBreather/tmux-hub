package ui

import (
	"bufio"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// docs/ is mounted read-only into a Caddy container and served at a public URL
// (`deploy/ui-draft/docker-compose.yml`), so a frame is PUBLISHED the moment the generator
// writes it — no commit and no deploy step. The picker's fleet comment states the rule that
// follows ("the aliases are FICTIONAL, and that is a hard rule for this file") and enforces it
// by having been careful. This enforces it.
//
// It reads the operator's own ~/.ssh/config rather than carrying a list of their host names,
// because a list of private aliases in a test file is the same leak one directory over.
//
// TWO exceptions, and they are exactly the two the picker fleet's comment already argues for:
// `nuc`, which was in the published document before any of this and is the one real host the
// frames have always named; and `github.com`, which is a public service rather than somebody's
// host. The guard finding precisely those two and nothing else, over eighteen aliases, is what
// says it is looking.
func TestThePublishedMockupNamesNoPrivateHost(t *testing.T) {
	doc := filepath.Join("..", "..", "docs", "ui-mockup.html")
	raw, err := os.ReadFile(doc)
	if err != nil {
		t.Skipf("no published document to check: %v", err)
	}
	published := string(raw)

	cfg := filepath.Join(os.Getenv("HOME"), ".ssh", "config")
	f, err := os.Open(cfg)
	if err != nil {
		// A machine with no ssh config cannot leak its hosts. Skipped rather than passed
		// silently, so the reason is on the record.
		t.Skipf("no ~/.ssh/config on this machine: %v", err)
	}
	defer f.Close()

	var aliases []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fs := strings.Fields(sc.Text())
		if len(fs) < 2 || !strings.EqualFold(fs[0], "Host") {
			continue
		}
		for _, a := range fs[1:] {
			// Patterns are not names, and a one- or two-character alias would match inside
			// ordinary words — `eu` is a substring of `background`, which is how a naive
			// version of this check raises a false alarm on a CSS property.
			if strings.ContainsAny(a, "*?!") || len(a) < 3 || allowedInPublic(a) {
				continue
			}
			aliases = append(aliases, a)
		}
	}
	// The FLOOR comes before any skip. It used to sit after a `len(aliases) == 0` skip, which
	// gave the softest treatment to the strictly WORST case: zero checked was a skip — and
	// CLAUDE.md records that `t.Skip` reports PASS — while four checked was a hard failure.
	// A config whose every Host line is a pattern would have passed having checked nothing,
	// which is the exact shape the comment below names.
	if len(aliases) < 5 {
		t.Fatalf("only %d concrete aliases survived filtering, so this guard checked almost "+
			"nothing — a config of patterns must not read as a clean document", len(aliases))
	}

	for _, a := range aliases {
		// Whole-word, for the substring reason above — but the hyphen is NOT part of the
		// boundary. With `-` in the negated class an alias published next to one was missed:
		// measured, `acme-box` inside `/srv/acme-box-data` passed while `host: acme-box up`
		// failed, and this product builds every ControlMaster path as `cm-<digest>-<alias>`
		// and puts it in operator-facing text (`no live ssh master at …`), so the
		// hyphen-adjacent shape is the one a frame is most likely to carry. Over this
		// machine's 17 aliases both matchers return the identical hits, so the relaxation
		// costs no false alarm.
		re := regexp.MustCompile(`(?i)(?:^|[^\w])` + regexp.QuoteMeta(a) + `(?:$|[^\w])`)
		if re.MatchString(published) {
			t.Errorf("docs/ui-mockup.html names %q, which is a host from this machine's "+
				"~/.ssh/config — that document is served publicly, so every alias in a frame "+
				"must be invented", a)
		}
	}
	t.Logf("checked %d aliases from %s against the published document", len(aliases), cfg)
}

// allowedInPublic names the two aliases a published frame may carry, each with its reason on
// the record above.
func allowedInPublic(a string) bool {
	return strings.EqualFold(a, "nuc") || strings.EqualFold(a, "github.com")
}
