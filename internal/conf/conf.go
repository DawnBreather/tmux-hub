// Package conf is the one config dialect the hub reads and writes.
//
// It is a deliberately small subset of TOML — a `[[table]]` array of records whose
// values are strings, bools and arrays of strings — and it exists as its own package for
// a reason that is about duplication rather than tidiness. §21.11.3 rules that project
// names live in their own FILE beside `hosts.toml`, and an earlier revision of that
// section also demanded their own PARSER, because the dialect then corrupted a value
// containing `"`, `\` or a newline silently. That is fixed, so a second parser would be a
// duplicate — and the duplicate would have been written by copying whichever half was
// still wrong.
//
// The one invariant, and everything else follows from it: **Quote and String are exact
// inverses.** Quote is `%q` (Go's quoted-string syntax) and String is strconv.Unquote, so
// there is no value an operator can type that the pair cannot carry. The overlap with
// TOML is exact for everything a keyboard produces — both agree on `\" \\ \n \t \r` and
// `\uXXXX`. `%q` would emit `\x00` for a NUL byte, which TOML does not define; Unquote
// reads it back, so a file stays self-consistent even there.
package conf

import (
	"bufio"
	"fmt"
	"strconv"
	"strings"
)

// Quote renders a value for writing. Every writer must use this rather than %q
// directly, so the inverse relationship with String is a property of the package and
// not of each call site remembering.
func Quote(v string) string { return strconv.Quote(v) }

// String parses a quoted value. It is the exact inverse of Quote.
//
// It used to be `s[1:len(s)-1]` — the bytes verbatim, with no unescaping — so a value
// carrying a quote came back WRONG and came back without an error, because the trailing
// `"` of an escaped value still satisfied the check. Measured: `wat"ever` round-tripped
// to `wat\"ever`.
func String(s string) (string, error) {
	v, err := strconv.Unquote(s)
	if err != nil {
		return "", fmt.Errorf("expected quoted string, got %q", s)
	}
	return v, nil
}

// Bool parses a boolean.
func Bool(s string) (bool, error) {
	switch s {
	case "true":
		return true, nil
	case "false":
		return false, nil
	default:
		return false, fmt.Errorf("expected true or false, got %q", s)
	}
}

// StringArray parses an array of quoted strings.
//
// It TOKENISES rather than splitting on commas, because a comma is a legal character
// inside a value and so is a quote. An earlier version toggled an `inQuote` flag on every
// `"` byte, escaped or not, so a value containing a quote flipped the flag at the wrong
// place: measured, `["a\", \"b", "second"]` came back as ONE element holding the
// separator, silently losing the other. That is data loss in an array, not merely a wrong
// string.
func StringArray(s string) ([]string, error) {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "[") || !strings.HasSuffix(s, "]") {
		return nil, fmt.Errorf("expected array [...], got %q", s)
	}
	inner := strings.TrimSpace(s[1 : len(s)-1])
	if inner == "" {
		return nil, nil
	}

	var out []string
	for i := 0; i < len(inner); {
		for i < len(inner) && (inner[i] == ' ' || inner[i] == '\t') {
			i++
		}
		if i >= len(inner) || inner[i] != '"' {
			return nil, fmt.Errorf("array element %d is not a quoted string: %q",
				len(out)+1, inner[i:])
		}
		// Walk to the closing quote, stepping OVER an escaped one. That is the whole
		// difference from a toggle: a `\"` is two bytes of the value, not a boundary.
		end := i + 1
		for end < len(inner) {
			if inner[end] == '\\' {
				end += 2
				continue
			}
			if inner[end] == '"' {
				break
			}
			end++
		}
		if end >= len(inner) {
			return nil, fmt.Errorf("array element %d is not terminated: %q", len(out)+1, inner[i:])
		}
		v, err := String(inner[i : end+1])
		if err != nil {
			return nil, err
		}
		out = append(out, v)
		i = end + 1
		for i < len(inner) && (inner[i] == ' ' || inner[i] == '\t') {
			i++
		}
		if i < len(inner) {
			if inner[i] != ',' {
				return nil, fmt.Errorf("array element %d is followed by %q, not a comma",
					len(out), inner[i:])
			}
			i++
		}
	}
	return out, nil
}

// Scan walks a file of `[[table]]` records.
//
// start is called at each header; set is called for every key inside a record, with the
// value still quoted so the caller chooses which of the parsers above applies. Scan owns
// the framing — blank lines, comments, the header match, a key that arrives before any
// header — and it prefixes every error with the line number, so no caller has to count
// lines and none can report them differently.
//
// set also RECEIVES the line, which is not the same thing as Scan prefixing errors with
// it: a caller that detects a conflict between two records — the same prefix claimed
// twice — has to name both lines, and only it knows they conflict. Without this the
// caller re-scans the text for headers and points at the record rather than at the line
// the operator has to edit.
func Scan(content, table string, start func(), set func(key, value string, line int) error) error {
	header := "[[" + table + "]]"
	sc := bufio.NewScanner(strings.NewReader(content))
	line := 0
	open := false
	for sc.Scan() {
		line++
		text := strings.TrimSpace(sc.Text())
		// A `#` inside a VALUE is safe because only a line that BEGINS with one is a
		// comment. That is a property of the writer: Quote escapes nothing about `#`,
		// and a quoted value never starts a line.
		if text == "" || strings.HasPrefix(text, "#") {
			continue
		}
		if text == header {
			open = true
			start()
			continue
		}
		// A different table is refused rather than skipped: silently ignoring
		// `[[host]]` in a projects file would read a hosts file as an empty project
		// set, and an empty set is indistinguishable from a first run.
		if strings.HasPrefix(text, "[[") {
			return fmt.Errorf("line %d: %s is not a %s record", line, text, header)
		}
		key, value, ok := strings.Cut(text, "=")
		if !ok {
			return fmt.Errorf("line %d: expected key = value, got %q", line, text)
		}
		if !open {
			return fmt.Errorf("line %d: key %q before any %s section",
				line, strings.TrimSpace(key), header)
		}
		if err := set(strings.TrimSpace(key), strings.TrimSpace(value), line); err != nil {
			return fmt.Errorf("line %d: %v", line, err)
		}
	}
	return sc.Err()
}
