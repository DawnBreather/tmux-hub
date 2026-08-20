package hostset

import (
	"path/filepath"
	"reflect"
	"testing"
)

// The file holds USER DECISIONS, so a value that goes in must come back out. The
// writer uses %q — Go's quoted-string syntax — and the reader returned
// `s[1:len(s)-1]`, the bytes verbatim with no unescaping, so a value carrying a
// quote, a backslash or a newline came back WRONG and came back without an error:
// the trailing `"` of an escaped value still satisfied the reader's own check.
//
// Latent while every alias came from an ssh config, live the moment the file holds
// free text a person typed — which is what a project name and a session alias are.
func TestEveryValueSurvivesTheRoundTrip(t *testing.T) {
	for _, c := range []struct{ name, value string }{
		{"plain", "nuc"},
		{"a quote", `wat"ever`},
		{"a backslash", `back\slash`},
		{"both, the shape %q escapes", `he said "hi\there"`},
		{"a newline", "two\nlines"},
		{"a tab", "a\tb"},
		{"the array separator itself", `a", "b`},
		{"a bracket", "[not a table]"},
		{"an equals sign", "k = v"},
		{"a comment marker", "# not a comment"},
		{"a lone cyrillic word", "путь"},
	} {
		path := filepath.Join(t.TempDir(), "hosts.toml")
		want := []Entry{{
			Alias:    c.value,
			Enabled:  true,
			Tags:     []string{c.value, "second"},
			TmuxArgs: []string{"-L", c.value},
		}}
		if err := SaveHosts(path, want); err != nil {
			t.Fatalf("%s: SaveHosts: %v", c.name, err)
		}
		got, err := LoadHosts(path)
		if err != nil {
			t.Errorf("%s: LoadHosts: %v", c.name, err)
			continue
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s: round trip changed the value\n got: %#v\nwant: %#v", c.name, got, want)
		}
	}
}

// And a file the writer could never have produced must be REFUSED rather than
// half-read. Returning a wrong string silently is the failure this whole dialect
// had; an error names the line and keeps the operator's other decisions.
func TestAMalformedValueIsRefusedRatherThanGuessed(t *testing.T) {
	for _, c := range []struct{ name, body string }{
		{"unterminated string", "[[host]]\nalias = \"nuc\nenabled = true\n"},
		{"no quotes at all", "[[host]]\nalias = nuc\n"},
		{"an escape that means nothing", `[[host]]` + "\nalias = \"a\\qb\"\n"},
		{"an unterminated array", "[[host]]\nalias = \"s\"\ntags = [\"a\", \"b\"\n"},
	} {
		if _, err := parseTOML(c.body); err == nil {
			t.Errorf("%s: parsed without error, want a refusal", c.name)
		}
	}
}
