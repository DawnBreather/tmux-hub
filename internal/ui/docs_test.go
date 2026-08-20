package ui

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// The design document is the spec and the authority (CLAUDE.md), and it is served publicly out of
// `docs/`. It has been damaged by editing passes in ways no test could see, and every one of these
// checks exists because the damage was found by hand:
//
//   - a prose paragraph spliced INTO §17's four-machine table, so the table rendered as one row, a
//     paragraph, then three orphans;
//   - the subsection "Making the pane the row is missing" present TWICE with contradictory counts,
//     "Six things are load-bearing" against "Five";
//   - eight edit directives (`Add: "…"`) sitting in the prose as instructions nobody carried out,
//     three of them spliced mid-sentence so the sentence they broke read as two;
//   - §22 cited seventy-seven times while no `## 22.` heading existed;
//   - a table row missing its closing pipe, so its last cell was never closed;
//   - two list items numbered 6 in §14, which made a citation to §14.6 ambiguous.
//
// Each is mechanical, so each is a test rather than a habit. This lives beside `english_test.go` and
// `published_test.go`, which already scan the repository from this package.
//
// What is deliberately NOT checked: a code span crossing a line break. This document does that 42
// times on purpose and CommonMark converts the newline to a space, so a per-line backtick rule would
// accuse the document of its own convention. Parity over the whole file is checked instead, which
// catches a span left open without firing on a wrapped one.
func docFiles(t *testing.T) map[string][]string {
	t.Helper()
	root := filepath.Join("..", "..", "docs")
	out := map[string][]string{}
	for _, name := range []string{"design.md", "known-issues.md"} {
		b, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		out[name] = strings.Split(string(b), "\n")
	}
	// A floor, because a scan that read nothing passes every check below.
	if len(out) < 2 {
		t.Fatalf("scanned %d documents; the guard is not looking at the docs", len(out))
	}
	for name, lines := range out {
		if len(lines) < 100 {
			t.Fatalf("%s has %d lines; that is not the document this guard is for", name, len(lines))
		}
	}
	return out
}

var directive = regexp.MustCompile(`^(Add|Insert|Replace|Append|Delete|Change|Rewrite)\b[: ]`)

// A directive is an instruction to edit the document, left in the document. It is not a defect of
// style: three of the eight found by hand were spliced mid-sentence, so the prose around them was
// broken too, and one asserted a mechanism §22 had already retired.
func TestNoDocCarriesAnUnappliedEditDirective(t *testing.T) {
	for name, lines := range docFiles(t) {
		for i, l := range lines {
			if directive.MatchString(l) {
				t.Errorf("docs/%s:%d is an edit directive, not prose: %.90s", name, i+1, l)
			}
			// Not "XXX": `docs/known-issues.md` writes TOML's `\uXXXX` escape notation, so that
			// marker is a false-alarm generator on this corpus.
			for _, marker := range []string{"TODO", "TBD", "FIXME"} {
				if strings.Contains(l, marker) {
					t.Errorf("docs/%s:%d carries %s: %.90s", name, i+1, marker, l)
				}
			}
		}
	}
}

func TestNoHeadingAppearsTwiceInOneDoc(t *testing.T) {
	for name, lines := range docFiles(t) {
		seen := map[string]int{}
		for i, l := range lines {
			if !strings.HasPrefix(l, "#") {
				continue
			}
			h := strings.TrimSpace(l)
			if prev, dup := seen[h]; dup {
				t.Errorf("docs/%s:%d repeats the heading first seen at :%d — %.70s",
					name, i+1, prev, h)
				continue
			}
			seen[h] = i + 1
		}
	}
}

// A section NUMBER must appear once per document, which the heading check above cannot see: two
// sections numbered 21.15 with different titles are two distinct headings and one ambiguous number.
//
// It caught exactly that, an hour after the number was written: `### 21.15 What the review of this
// design could not check` already existed when `### 21.15 The name in the attached session's status
// line` was appended, and six code comments then cited a §21.15 that resolved to either. A citation
// that RESOLVES is not a citation that is right, and this is the mechanical half of that — the other
// half, whether the number points at the section the author meant, has no test and needs the heading
// read out loud before it is quoted.
func TestNoSectionNumberAppearsTwiceInOneDoc(t *testing.T) {
	num := regexp.MustCompile(`^#+\s+(\d+(?:\.\d+)*)\.?\s+\S`)
	total := 0
	for name, lines := range docFiles(t) {
		seen := map[string]int{}
		for i, l := range lines {
			m := num.FindStringSubmatch(strings.TrimSpace(l))
			if m == nil {
				continue
			}
			if prev, dup := seen[m[1]]; dup {
				t.Errorf("docs/%s:%d numbers a section %s, which :%d already used — every "+
					"citation of §%s now resolves to either one: %.60s",
					name, i+1, m[1], prev, m[1], strings.TrimSpace(l))
				continue
			}
			seen[m[1]] = i + 1
		}
		total += len(seen)
	}
	// A FLOOR across the documents rather than per file: known-issues.md numbers its sections
	// S1/H1/N3 by design, so a per-file floor is a false alarm on it — while zero numbered
	// headings anywhere would mean this check looked at nothing and said clean.
	if total < 20 {
		t.Errorf("only %d numbered headings adjudicated across the documents — the matcher is "+
			"not reading them", total)
	}
}

// cellCount counts a markdown row's cells with escaped pipes and code spans excluded, because both
// legitimately contain a `|` that is content rather than a separator.
func cellCount(l string) int {
	s := regexp.MustCompile("`[^`]*`").ReplaceAllString(l, "X")
	s = strings.ReplaceAll(s, `\|`, "X")
	return strings.Count(s, "|") - 1
}

func isSeparator(l string) bool {
	if !strings.HasPrefix(l, "|") {
		return false
	}
	return strings.Trim(strings.ReplaceAll(l, "|", ""), "-: ") == ""
}

// Two failures in one walk, because both are about a table's shape: a paragraph landing between two
// rows, and a row whose cell count does not match its header. The second found a row in §14 whose
// closing pipe was missing, which markdown renders by swallowing the last cell.
func TestEveryTableIsWellFormed(t *testing.T) {
	tables := 0
	for name, lines := range docFiles(t) {
		for i := 0; i < len(lines); i++ {
			if !strings.HasPrefix(lines[i], "|") || i+1 >= len(lines) || !isSeparator(lines[i+1]) {
				continue
			}
			tables++
			want := cellCount(lines[i])
			j := i + 2
			for ; j < len(lines); j++ {
				l := lines[j]
				if !strings.HasPrefix(l, "|") {
					// A blank line ends the table. Anything else between rows was spliced in.
					if strings.TrimSpace(l) != "" && j+1 < len(lines) && strings.HasPrefix(lines[j+1], "|") {
						t.Errorf("docs/%s:%d is prose spliced INTO a table: %.80s", name, j+1, l)
						continue
					}
					break
				}
				if got := cellCount(l); got != want {
					t.Errorf("docs/%s:%d has %d cells where its header has %d: %.80s",
						name, j+1, got, want, l)
				}
			}
			i = j
		}
	}
	// A floor: the two documents carry dozens of tables, so a walk that found a handful has a
	// broken detector rather than a clean corpus.
	if tables < 40 {
		t.Fatalf("found only %d tables; the table detector is not working", tables)
	}
}

// A code span left OPEN swallows the rest of the document as code. Parity is the check that sees it
// without firing on a span deliberately wrapped across a line.
func TestEveryCodeSpanCloses(t *testing.T) {
	for name, lines := range docFiles(t) {
		inFence, total := false, 0
		for _, l := range lines {
			if strings.HasPrefix(strings.TrimSpace(l), "```") {
				inFence = !inFence
				continue
			}
			if !inFence {
				total += strings.Count(l, "`")
			}
		}
		if inFence {
			t.Errorf("docs/%s: a fenced block is never closed", name)
		}
		if total%2 != 0 {
			t.Errorf("docs/%s: %d backticks outside fenced blocks, an odd number, so a code span is left open",
				name, total)
		}
	}
}

var (
	sectionHeading = regexp.MustCompile(`^## (\d+)\.`)
	subHeading     = regexp.MustCompile(`^### (\d+\.\d+)`)
	listItem       = regexp.MustCompile(`^(\d+)\. `)
	sectionRef     = regexp.MustCompile(`§(\d+)(?:\.(\d+))?`)
)

// §22 was cited seventy-seven times while the document ended at §21, so the authority document
// pointed at nothing for two days. A subsection reference resolves either to a `### N.M` heading or
// to item M of a numbered list inside `## N.` — §14 uses the list form, and that is legitimate.
func TestEverySectionReferenceResolves(t *testing.T) {
	lines := docFiles(t)["design.md"]
	sections := map[string]bool{}
	subs := map[string]bool{}
	items := map[string]bool{} // "14.8" -> §14's list has an item 8
	current := ""
	for _, l := range lines {
		if m := sectionHeading.FindStringSubmatch(l); m != nil {
			current = m[1]
			sections[current] = true
			continue
		}
		if m := subHeading.FindStringSubmatch(l); m != nil {
			subs[m[1]] = true
			continue
		}
		if m := listItem.FindStringSubmatch(strings.TrimLeft(l, " ")); m != nil && current != "" {
			items[current+"."+m[1]] = true
		}
	}
	if len(sections) < 20 {
		t.Fatalf("found only %d sections; the heading matcher is not working", len(sections))
	}
	refs := 0
	for i, l := range lines {
		for _, m := range sectionRef.FindAllStringSubmatch(l, -1) {
			refs++
			if !sections[m[1]] {
				t.Errorf("docs/design.md:%d cites §%s and no `## %s.` heading exists", i+1, m[1], m[1])
				continue
			}
			if m[2] == "" {
				continue
			}
			key := m[1] + "." + m[2]
			if !subs[key] && !items[key] {
				t.Errorf("docs/design.md:%d cites §%s, which is neither a `### %s` heading nor an item in §%s's list",
					i+1, key, key, m[1])
			}
		}
	}
	if refs < 100 {
		t.Fatalf("found only %d section references; the reference matcher is not working", refs)
	}
}

var citation = regexp.MustCompile(`((?:internal|cmd|docs)/[\w./-]+\.(?:go|md)):(\d+)(?:-(\d+))?`)

// Line citations rot every time the file they name is edited, and this document carried three that
// pointed at the wrong construct — `agents.go:94` at a `json.Unmarshal` error return, `:127` at an
// interface's method line, `registry.go:189` at nothing in particular. Pointing PAST the end of the
// file is the half a test can prove; naming the wrong line inside it cannot be caught here, which is
// why the corrections plan verifies quoted text by hand.
func TestEveryLineCitationPointsInsideItsFile(t *testing.T) {
	root := filepath.Join("..", "..")
	lengths := map[string]int{}
	checked := 0
	for name, lines := range docFiles(t) {
		for i, l := range lines {
			for _, m := range citation.FindAllStringSubmatch(l, -1) {
				path := m[1]
				n, ok := lengths[path]
				if !ok {
					b, err := os.ReadFile(filepath.Join(root, path))
					if err != nil {
						t.Errorf("docs/%s:%d cites %s, which does not exist", name, i+1, path)
						lengths[path] = -1
						continue
					}
					n = strings.Count(string(b), "\n") + 1
					lengths[path] = n
				}
				if n < 0 {
					continue
				}
				hi := m[2]
				if m[3] != "" {
					hi = m[3]
				}
				v, err := strconv.Atoi(hi)
				if err != nil {
					continue
				}
				checked++
				if v > n {
					t.Errorf("docs/%s:%d cites %s:%s but that file has %d lines",
						name, i+1, path, hi, n)
				}
			}
		}
	}
	if checked < 50 {
		t.Fatalf("checked only %d line citations; the citation matcher is not working", checked)
	}
}

// A refuted figure surviving at one site is worse than never having measured it, because the
// document then disagrees with itself and the reader cannot tell which half is current. Each string
// here was published, measured false, and corrected everywhere except inside §22, which quotes them
// deliberately as the figures it withdraws.
func TestNoRefutedFigureSurvivesOutsideTheSectionThatWithdrawsIt(t *testing.T) {
	lines := docFiles(t)["design.md"]
	start := -1
	for i, l := range lines {
		if strings.HasPrefix(l, "## 22.") {
			start = i
			break
		}
	}
	if start < 0 {
		t.Fatal("no `## 22.` section; this guard has nothing to scope against")
	}
	refuted := []struct{ text, why string }{
		{"10 of 21", "the census was a bare `--json` count; under `--all` it is 32 of 45 (§22.1)"},
		{"21 real rows", "same census"},
		{"null in 25", "`state.json`'s `pid` is ABSENT, not null (§22.5)"},
		// NOT the bare string: 2.1.232 is installed and a live `bg-pty-host` runs it
		// (measured 2026-08-16). The refuted claim is the ATTRIBUTION — §22's figures
		// were taken on 2.1.233 and 2.1.224, so a sentence saying they were measured on
		// 2.1.232 mis-states its own provenance.
		{"measured on **2.1.232**", "§22's figures were taken on 2.1.233 and 2.1.224"},
		{"Re-measured on **2.1.232**", "same mis-attribution"},
		{"agents.go:94", "the back-fill is at :111-112"},
		{"agents.go:105-106", "the back-fill moved to :111-112 when `--all` and the ssh payload landed"},
		{"lifecycle.go:150-172", "`killSelected` is at :202-238 since the Kind guard landed"},
		{"lifecycle.go:180", "`tmux.KillPane` is called at :232"},
		{"registry.go:227-232", "the absorb branch is at :245-250"},
		{"registry.go:288-289", "the agent-row deletion is at :316"},
		// NOT "still calls bare": the two sites that carried it are INSIDE §22, which this
		// loop deliberately does not scan, so listing it here would arm nothing and read as
		// cover. What retired the claim is `--all` in the fetcher, and §22.6 says so in prose.
		{"agents.go:127", "that is the `Fetcher` interface's method line, not a call site"},
		{"registry.go:189", "`p.Command = s.Kind` is at :270"},
		// NOT "job not found": measured 2026-08-16, that wording is REAL — it is what `claude logs`
		// answers for a short id the daemon has forgotten, while an id it never had (including a full
		// session uuid) gets `No job matching`. An earlier revision of this list forbade the string on
		// the strength of probing a BOGUS id only, which over-generalised one measurement into a rule
		// and then enforced it. The real rule — that no CODE may match on either sentence — is a rule
		// about code and lives in §22.6's prose, where a grep over prose cannot help.
		{"`working/active`", "`tempo` is a projection of the listing word; write the listing word alone (§22.5)"},
	}
	for i := 0; i < start; i++ {
		for _, r := range refuted {
			if strings.Contains(lines[i], r.text) {
				t.Errorf("docs/design.md:%d still carries the refuted %q — %s", i+1, r.text, r.why)
			}
		}
	}
}
