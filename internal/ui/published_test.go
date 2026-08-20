package ui

import (
	"bufio"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// A generated frame is PUBLISHED the moment the generator writes it: `docs/ui-mockup.html` is
// committed, and while this project was private it was also bind-mounted into a web server, so
// there was no commit and no deploy step between writing a frame and serving it. The picker's fleet comment states the rule that
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
	// NOTHING TO LEAK IS NOT THE SAME AS A GUARD THAT DID NOT LOOK, and which of the two you are
	// in is decided by the count — so the count is always printed, and zero is the only case that
	// skips. An earlier version demanded five concrete aliases and FAILED below that, which was
	// calibrated to the author's own 17-alias machine: on a contributor's laptop with a two-host
	// config that is a red suite for a reason that has nothing to do with their change, and the
	// aliases it refused to check are exactly the ones it would have been checking. One to four
	// aliases is not a vacuous run — it checks every alias that exists.
	if len(aliases) == 0 {
		t.Skip("no concrete Host aliases in this machine's ssh config, so it has none to leak")
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
