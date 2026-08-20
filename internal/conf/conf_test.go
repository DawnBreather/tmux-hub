package conf

import (
	"reflect"
	"strings"
	"testing"
)

// The one invariant the package exists for: Quote and String are exact inverses, so
// there is no value an operator can type that the pair cannot carry. Before it, the
// reader returned the bytes verbatim and a value with a quote came back wrong WITHOUT an
// error — the trailing `"` of an escaped value still satisfied the check.
func TestQuoteAndStringAreInverses(t *testing.T) {
	for _, v := range []string{
		"", "nuc", `wat"ever`, `back\slash`, `he said "hi\there"`, "two\nlines", "a\tb",
		`a", "b`, "[not a table]", "k = v", "# not a comment", "путь", "🙂", "é",
		"  edge spaces  ", "]", "[", `\`, `"`, "\r\n",
	} {
		got, err := String(Quote(v))
		if err != nil {
			t.Errorf("String(Quote(%q)): %v", v, err)
			continue
		}
		if got != v {
			t.Errorf("round trip changed %q into %q", v, got)
		}
	}
}

// A value the writer could never have produced must be REFUSED, not half-read.
func TestStringRefusesWhatTheWriterCannotProduce(t *testing.T) {
	for _, s := range []string{``, `nuc`, `"unterminated`, `"a\qb"`, `'single'`, `"a" "b"`} {
		if v, err := String(s); err == nil {
			t.Errorf("String(%q) = %q, want a refusal", s, v)
		}
	}
}

func TestStringArrayCarriesEveryHazard(t *testing.T) {
	// Built through Quote so the fixture cannot disagree with the writer.
	vals := []string{`a", "b`, "second", "with\nnewline", `q"uote`}
	var parts []string
	for _, v := range vals {
		parts = append(parts, Quote(v))
	}
	got, err := StringArray("[" + strings.Join(parts, ", ") + "]")
	if err != nil {
		t.Fatalf("StringArray: %v", err)
	}
	if !reflect.DeepEqual(got, vals) {
		t.Errorf("got %#v\nwant %#v", got, vals)
	}
}

func TestStringArrayRefusesMalformedInput(t *testing.T) {
	for _, s := range []string{`"a"`, `[a, b]`, `["a"`, `["a" "b"]`} {
		if v, err := StringArray(s); err == nil {
			t.Errorf("StringArray(%q) = %#v, want a refusal", s, v)
		}
	}
	// A TRAILING COMMA is accepted, and that is deliberate rather than an oversight: TOML
	// permits one, and this writer never emits one, so refusing it would make the reader
	// stricter than the format for no gain. Asserted so the leniency is a decision on
	// record instead of a gap someone tightens later.
	if got, err := StringArray(`["a", ]`); err != nil || !reflect.DeepEqual(got, []string{"a"}) {
		t.Errorf(`StringArray(["a", ]) = %#v, %v; want ["a"], nil`, got, err)
	}
}

func TestStringArrayReadsAnEmptyArray(t *testing.T) {
	got, err := StringArray("[]")
	if err != nil || got != nil {
		t.Errorf("StringArray(\"[]\") = %#v, %v; want nil, nil", got, err)
	}
}

// Scan owns the framing, so every caller reports a bad line the same way.
func TestScanWalksRecordsAndOwnsTheFraming(t *testing.T) {
	var records [][2]string
	var cur int
	err := Scan("# a comment\n\n[[project]]\nname = \"one\"\n\n[[project]]\nname = \"two\"\n",
		"project",
		func() { cur = len(records); records = append(records, [2]string{}) },
		func(k, v string, line int) error {
			s, err := String(v)
			if err != nil {
				return err
			}
			records[cur] = [2]string{k, s}
			return nil
		})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	want := [][2]string{{"name", "one"}, {"name", "two"}}
	if !reflect.DeepEqual(records, want) {
		t.Errorf("got %#v, want %#v", records, want)
	}
}

// Each refusal names the LINE, because a file the operator edited by hand is where these
// errors come from and "somewhere in the file" is not a fix.
func TestScanRefusalsNameTheLine(t *testing.T) {
	for _, c := range []struct{ name, body, wantLine string }{
		{"a key before any record", "name = \"x\"\n", "line 1"},
		{"no delimiter", "[[project]]\nnonsense\n", "line 2"},
		{"a value the parser refuses", "[[project]]\nname = nope\n", "line 2"},
		// A hosts file read as a projects file must not read as EMPTY: an empty set is
		// indistinguishable from a first run, and the next save would overwrite it.
		{"a different table", "[[host]]\nalias = \"nuc\"\n", "line 1"},
	} {
		err := Scan(c.body, "project", func() {}, func(k, v string, line int) error {
			_, e := String(v)
			return e
		})
		if err == nil {
			t.Errorf("%s: parsed without error, want a refusal", c.name)
			continue
		}
		if !strings.Contains(err.Error(), c.wantLine) {
			t.Errorf("%s: error %q does not name %s", c.name, err, c.wantLine)
		}
	}
}
