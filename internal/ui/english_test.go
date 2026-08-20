package ui

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
	"unicode"
)

// Production UI strings are English. That is a standing rule of this repository, and it was
// broken for two whole files — `launchform.go` carried 167 Cyrillic codepoints and
// `render.go` 26, the latter being the dashboard footer, so the mixed language was on the
// MAIN screen rather than in a corner (known-issues L1).
//
// Fixing the two files fixes two files. This makes the class impossible: it parses every
// non-test source file in the REPOSITORY and fails on a non-Latin STRING LITERAL, which is
// what reaches a screen. It lives in this package because this is where screens are drawn,
// but its subject is the whole product.
//
// Deliberately narrow in two ways, and both are the point:
//
//   - LITERALS only, not comments. A comment is for the next reader and this repository's
//     are long and argued; forbidding a Cyrillic word inside one would be a rule about
//     prose, not about the product.
//   - Every PRODUCTION file in the repository, not just this package — an operator-facing
//     message can be built anywhere, and the first version of this guard scanned only
//     `internal/ui`, which made the commit's claim to close the CLASS an overstatement.
//     Test files stay out, because a hostile Cyrillic directory name is exactly the DATA
//     some of them are built from (`пу|ть`, `с кириллицей`) — data that must stay, since it
//     is what proves the wire format carries it.
func TestNoProductionStringIsNonEnglish(t *testing.T) {
	var files []string
	// The repository root is two levels up from internal/ui, and walking from there is what
	// makes this a claim about the PRODUCT rather than about one package.
	root := filepath.Join("..", "..")
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			// Nothing under a dot-directory is built.
			if name := d.Name(); name != "." && name != ".." && strings.HasPrefix(name, ".") {
				return fs.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(path, ".go") && !strings.HasSuffix(path, "_test.go") {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	scanned := 0
	for _, name := range files {
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		scanned++
		ast.Inspect(f, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			for _, r := range lit.Value {
				// Any non-Latin script, not only Cyrillic: the rule is "English", and the
				// next slip will not necessarily be in the same alphabet as the last one.
				if r > unicode.MaxASCII && unicode.IsLetter(r) && !unicode.Is(unicode.Latin, r) {
					pos := fset.Position(lit.Pos())
					t.Errorf("%s:%d: a production string is not English: %s",
						pos.Filename, pos.Line, lit.Value)
					return false
				}
			}
			return true
		})
	}
	// A floor, because a scan that walked nothing would pass silently — the shape this
	// repository has been bitten by before: a check that finds nothing is indistinguishable
	// from a check that looked at nothing. Twenty is well under the real count and well over
	// any single package's.
	if scanned < 20 {
		t.Fatalf("scanned only %d source files; the guard is not looking at the product", scanned)
	}
}
