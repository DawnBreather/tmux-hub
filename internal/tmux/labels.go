package tmux

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

// Labels are a pane's free-text descriptors.
type Labels struct {
	Session string
	Window  string
	Command string
	// StartCommand is #{pane_start_command}: the command the pane was created with,
	// quoted by tmux (`"sleep 301"`). It is STABLE rather than immutable: respawn-pane
	// rewrites it with the pane id unchanged (measured, known-issues N3), so the hide
	// key (docs/design.md §18) uses it as a corroborator whose loss un-hides a pane —
	// the safe direction — never as identity. pane_current_command is not usable
	// for that: it walks zsh → claude → zsh.
	StartCommand string
	// Path is #{pane_current_path}: the pane's working directory, which is what a
	// project is identified by (docs/design.md §21). It is the one label whose value
	// the operator's FILESYSTEM controls rather than tmux, and it is why the wire
	// format below carries lengths — see labelFormat.
	Path string
	// The three below are per-SESSION options rather than pane facts, read here
	// because option lookup walks pane → window → session → global, so a pane format
	// answers them and the poll already runs one (measured on 3.2a and 3.7b: a pane
	// record reports its session's `@hub_alias`, and `#{n:@hub_alias}` frames it —
	// 12 for `billing-cicd`, 0 when unset). Reading rather than remembering is what
	// lets the hub write only DIFFERENCES, so the steady state costs no tmux commands
	// and a restarted hub needs no cache: the server already holds the answer.
	//
	// SessionAlias is the operator's name as the tmux server currently knows it.
	SessionAlias string
	// StatusLeft and StatusLeftLength are read to answer "may the hub write here",
	// which is a question about the OPERATOR's configuration — see
	// tmux.StatusLeftIsOurs.
	StatusLeft       string
	StatusLeftLength string
}

// labelFormats is the single place a label is declared. The field name, the
// assignment and therefore the wire format all come from one row, so a label
// cannot be added and left unfetched — which happened once, when
// #{pane_start_command} joined the table and nothing read the table, leaving
// registry.Pane.StartCommand empty on every real poll while every unit test
// passed, because each one built snap.Labels itself.
var labelFormats = []struct {
	field  string // the tmux variable name, without the #{} wrapper
	assign func(*Labels, string)
}{
	{"session_name", func(l *Labels, v string) { l.Session = v }},
	{"window_name", func(l *Labels, v string) { l.Window = v }},
	{"pane_current_command", func(l *Labels, v string) { l.Command = v }},
	{"pane_start_command", func(l *Labels, v string) { l.StartCommand = v }},
	{"pane_current_path", func(l *Labels, v string) { l.Path = v }},
	{AliasOption, func(l *Labels, v string) { l.SessionAlias = v }},
	{StatusLeftOption, func(l *Labels, v string) { l.StatusLeft = v }},
	{StatusLeftLengthOption, func(l *Labels, v string) { l.StatusLeftLength = v }},
}

// labelFormat is the one format every label is read through:
//
//	#{pane_id}|#{n:session_name}|#{session_name}|#{n:window_name}|#{window_name}|…
//
// Every value is preceded by its own byte length, and there is ONE invocation, so
// a record is a record however many newlines its values contain. That kills a
// class rather than an instance, and the class had two live members:
//
//   - The reader FRAMED BLOCKS BY COUNT. Each label used to be its own
//     `list-panes -a -F` and the blocks were sliced `lines[pos : pos+len(ds)]`, so
//     one value holding a newline shifted every later block by a line. Measured: a
//     path label as a non-last block turned a session name into a path and produced
//     a phantom pane WITH NO ERROR; as the last block it errored and took the whole
//     host down.
//   - The count came from a DIFFERENT invocation (the deltas fetch), so a pane
//     created or destroyed between the two calls mis-framed everything as well.
//     There is no count to disagree with now.
//
// Why a length rather than an escape or a delimiter: no delimiter can be forbidden
// to a value the operator's filesystem names, and `#{q:}` escapes `| % $ # \ ' " ;`
// identically on 3.2a and 3.7b but NOT newlines. `#{n:X}` is the raw BYTE count —
// measured, a 42-byte 32-rune path reported 42 — so the reader consumes exactly
// those bytes and never looks for a boundary the value could forge.
//
// THE LENGTH IS THE STORED SIZE, NOT THE EMITTED SIZE, and those differ for a client
// with no locale: tmux then substitutes one `_` per non-ASCII CHARACTER, so a 9-byte
// value came out as 7 bytes with `n:` still saying 9, the reader walked into the next
// field, and the host went dark with the error below. Measured on dev-air 2026-08-20;
// the table and the fix are on `forceUTF8` in run.go, which puts `-u` on every
// invocation this package builds. Nothing here needs to know about it — that is the
// point of fixing it at the seam — but the framing rule is only sound while the
// stream is UTF-8, so the two comments belong to each other.
//
// Which values can actually carry a newline, measured on tmux 3.7b:
//
//	session_name        refused    `invalid session name: a\nb`
//	window_name         refused    `invalid window name: w\nx`
//	pane_start_command  accepted, but tmux emits it ESCAPED: `"sleep 300\n#x"`
//	pane_current_path   accepted AND emitted raw — the line splits
//
// So the path is the only live member, which is why the framing had to land with
// it rather than after it.
func labelFormat() string {
	var b strings.Builder
	b.WriteString("#{pane_id}")
	for _, lf := range labelFormats {
		fmt.Fprintf(&b, "|#{n:%s}|#{%s}", lf.field, lf.field)
	}
	return b.String()
}

// parseLabelRecords walks the byte stream labelFormat produces.
//
// It cannot silently mis-frame: every field's length says where the next
// separator must be, so a stream that does not line up is an ERROR naming the
// pane it broke on, where the line-counting reader answered with a plausible
// wrong value.
func parseLabelRecords(stdout string) (map[string]Labels, error) {
	out := make(map[string]Labels)
	rest := stdout
	for rest != "" {
		id, after, ok := strings.Cut(rest, "|")
		if !ok {
			return nil, fmt.Errorf("label record without a delimiter: %q", clip(rest))
		}
		// A pane id is `%N`. Checking it is the cheap detector for a stream that has
		// come apart for a reason the lengths cannot see, and it costs one compare.
		if !strings.HasPrefix(id, "%") {
			return nil, fmt.Errorf("label record starts with %q, which is not a pane id", clip(id))
		}
		var l Labels
		for i, lf := range labelFormats {
			digits, body, ok := strings.Cut(after, "|")
			if !ok {
				return nil, fmt.Errorf("%s/%s: no length field", id, lf.field)
			}
			n, err := strconv.Atoi(digits)
			if err != nil || n < 0 {
				return nil, fmt.Errorf("%s/%s: length %q is not a count", id, lf.field, clip(digits))
			}
			if len(body) < n {
				return nil, fmt.Errorf("%s/%s: length %d exceeds the %d bytes left",
					id, lf.field, n, len(body))
			}
			lf.assign(&l, body[:n])
			after = body[n:]
			if i == len(labelFormats)-1 {
				break
			}
			if !strings.HasPrefix(after, "|") {
				return nil, fmt.Errorf("%s/%s: value is not followed by a separator but by %q",
					id, lf.field, clip(after))
			}
			after = after[1:]
		}
		switch {
		case after == "":
			rest = ""
		case strings.HasPrefix(after, "\n"):
			rest = after[1:]
		default:
			// The lengths added up but the record did not end. That is a format and a
			// parser that disagree, which must never be read as data.
			return nil, fmt.Errorf("%s: record does not end after its last value, %q follows",
				id, clip(after))
		}
		out[id] = l
	}
	return out, nil
}

// clip bounds an error's quoted fragment. A path may be arbitrarily long and a
// mis-framed stream is the whole output, so an unbounded %q turns one bad poll
// into an unreadable log line.
func clip(s string) string {
	const max = 60
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}

// FetchLabels reads every label for every pane on the server in ONE invocation.
//
// One call is not an optimisation, it is what removes the framing problem: with a
// single record per pane there are no blocks to shift and no count from another
// invocation to disagree with.
func FetchLabels(ctx context.Context, r Runner, t Target) (map[string]Labels, error) {
	res, err := r.Run(ctx, t, "list-panes", "-a", "-F", labelFormat())
	if err != nil {
		return nil, err
	}
	if res.RC != 0 {
		return nil, fmt.Errorf("list-panes rc=%d: %s", res.RC, res.Stderr)
	}
	return parseLabelRecords(res.Stdout)
}
