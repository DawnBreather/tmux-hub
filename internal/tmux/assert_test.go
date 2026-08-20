package tmux

import (
	"context"
	"strings"
	"testing"
	"time"
)

// Every field DeltaFormat reads has to be either ASSERTED at connect or exempt
// for a stated reason, and the reason has to be that its absence fails SAFE.
//
// This is the check requiredFields was missing. tmux never errors on an unknown
// format variable — it returns an empty value with the field count intact — so an
// unanswered field is silently zero, and whether that matters is decided by which
// way the zero fails. window_id was in DeltaFormat and not in requiredFields
// while §20 jumps to `@N`: on a server that does not answer it, a select-window
// whose -t value is the EMPTY STRING is not refused, so every jump landed on
// whatever window the client was already on and the hub reported it as the window
// the operator picked. (Said in prose because gofmt rewrites two single quotes
// inside a doc comment into a typographic quote — run.go carries the same note.)
//
// The list of names is PARSED from DeltaFormat rather than written out here, which
// is the part that makes this a floor: a field added to the format has to be
// classified, and cannot arrive unclassified and unnoticed.
func TestEveryDeltaFieldIsEitherAssertedOrExemptForAReason(t *testing.T) {
	// Each exemption names why an empty value is harmless — never "we forgot".
	exempt := map[string]string{
		"pid": "half of the server epoch. Deliberately NOT required (delta.go): on a " +
			"tmux that does not know it the epoch clause goes inert and the token guard " +
			"still refuses after a restart, so requiring it would turn an old but usable " +
			"host into an unusable one",
		"start_time": "the other half of the epoch, same reason",
		"pane_dead_status": "legitimately empty for a LIVE pane, so a non-emptiness " +
			"assertion would report every healthy host as missing a field",
		"alternate_on": "read as #{?alternate_on,ALT,NORM}, which yields NORM for a name " +
			"tmux does not know — a non-emptiness check structurally cannot see it, so this " +
			"one needs a different instrument rather than a place on the list",
		"bracket_paste_flag": "an unanswered flag reads as NOT bracketed, and §7 makes " +
			"an unbracketed target a reason to CONFIRM rather than to send — it fails toward " +
			"asking, which is the safe direction",
		"pane_index": "orders rows within a window. An empty value reads as 0 for every " +
			"pane, which is a cosmetic ordering defect and reaches no write",
	}

	required := map[string]bool{}
	for _, f := range requiredFields {
		required[f] = true
	}

	var gated, unclassified []string
	for _, name := range formatNames(DeltaFormat) {
		switch {
		case required[name]:
			gated = append(gated, name)
		case exempt[name] != "":
			// Stated and accepted.
		default:
			unclassified = append(unclassified, name)
		}
	}
	if len(unclassified) > 0 {
		t.Errorf("DeltaFormat reads %v, which requiredFields does not assert and no "+
			"exemption above explains. Either add it to requiredFields or say here why an "+
			"empty value is harmless", unclassified)
	}
	// A floor, because an empty parse would satisfy everything above: the format is
	// 16 fields today and this is the number that reach a decision the hub makes.
	if len(gated) < 10 {
		t.Errorf("only %d of DeltaFormat's fields are asserted at connect (%v) — the "+
			"parse above is reading the wrong thing", len(gated), gated)
	}
	// And no exemption may outlive its field, or the list becomes a place to hide a
	// name nothing reads.
	inFormat := map[string]bool{}
	for _, n := range formatNames(DeltaFormat) {
		inFormat[n] = true
	}
	for name := range exempt {
		if !inFormat[name] {
			t.Errorf("%q is exempted but DeltaFormat no longer reads it", name)
		}
	}
}

// formatNames pulls the variable names out of a tmux format string. `?name,a,b` is
// a conditional, so the name is what precedes the first comma, and the leading `?`
// is not part of it.
func formatNames(format string) []string {
	var out []string
	rest := format
	for {
		i := strings.Index(rest, "#{")
		if i < 0 {
			return out
		}
		rest = rest[i+2:]
		j := strings.Index(rest, "}")
		if j < 0 {
			return out
		}
		name := strings.TrimPrefix(rest[:j], "?")
		if k := strings.Index(name, ","); k >= 0 {
			name = name[:k]
		}
		out = append(out, name)
		rest = rest[j+1:]
	}
}

func TestAssertFieldsPassesOnRealServer(t *testing.T) {
	tgt := testServer(t)
	r := NewExec(10 * time.Second)
	ds, err := FetchDeltas(context.Background(), r, tgt)
	if err != nil {
		t.Fatalf("FetchDeltas: %v", err)
	}
	rep, err := AssertFields(context.Background(), r, tgt, ds[0].PaneID)
	if err != nil {
		t.Fatalf("AssertFields: %v", err)
	}
	if len(rep.Missing) != 0 {
		t.Fatalf("Missing = %v, want none", rep.Missing)
	}
	if !strings.HasPrefix(rep.Version, "3.") && !strings.HasPrefix(rep.Version, "next-") {
		t.Fatalf("Version = %q, want something tmux-shaped", rep.Version)
	}
}

// tmux never errors on an unknown format variable: it returns an empty value
// with the field count intact (design.md §3). So a wrong field name can only be
// caught by asserting the value is non-empty. This test injects a bogus name and
// requires it to be reported by name.
func TestAssertFieldsNamesAMissingField(t *testing.T) {
	tgt := testServer(t)
	r := NewExec(10 * time.Second)
	ds, err := FetchDeltas(context.Background(), r, tgt)
	if err != nil {
		t.Fatalf("FetchDeltas: %v", err)
	}
	rep, err := assertFieldsWith(context.Background(), r, tgt, ds[0].PaneID,
		[]string{"pane_id", "no_such_variable", "pane_height"})
	if err != nil {
		t.Fatalf("assertFieldsWith: %v", err)
	}
	if len(rep.Missing) != 1 || rep.Missing[0] != "no_such_variable" {
		t.Fatalf("Missing = %v, want [no_such_variable]", rep.Missing)
	}
}
