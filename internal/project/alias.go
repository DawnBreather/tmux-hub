package project

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/DawnBreather/tmux-hub/internal/conf"
	"github.com/DawnBreather/tmux-hub/internal/registry"
)

// AliasKey identifies the SESSION an alias names.
//
// An alias names a session and never a pane: the operator names the work, and the work
// outlives any one pane (docs/design.md §21.12). The shape therefore differs by what the hub
// KNOWS about the row, which is the schema §21.14 left to the plan:
//
//	a conversation the hub has a uuid for   (Claude session id, cwd)
//	anything else                           (host, tmux session name)
//
// The first shape drops the HOST and the row's KIND, and that is the whole substance of this
// type. Both of those move under the row while the operator watches, by the product's own
// doing: the door makes a tmux session called `<name>-<short id>` and the join folds the
// pane-less row into that pane, so the row goes agent → pane; and the dedup attributes a
// session that a shared `~/.claude` reports on two hosts to the fleet-first one, so the Host
// moves (§22.12). A key made of what the operator READS came off at both, and the report was
// "the sessions we named no longer show their names". The uuid survives both, and it is global,
// so one `~/.claude` shared between machines names one session rather than one per host.
//
// The two shapes are not inconsistent — they answer different questions. A tmux session name is
// unique only per host, so `work` on two hosts really is two sessions and must be nameable
// apart; a uuid is one conversation wherever it is reported from.
//
// The uuid shape still carries the CWD, because the uuid alone is not unique either — measured,
// one `sessionId` on `nuc` carried two different sessions in different directories (N4). An
// alias keyed on the uuid alone would land on both, silently, and §21.12 is explicit that a
// wrong alias has no safety net: §18's hide has one, since a wrongly hidden pane that starts
// waiting comes back, while a wrongly named session stays selectable and writable under the
// wrong name. The cwd is stable across the transitions above because the door creates its
// session with `-c <the row's path>`, so the pane it makes reports the cwd the listing row had.
//
// Kind stays in the second shape so a pane row and a uuid row can never collide however their
// fields line up.
type AliasKey struct {
	Kind    string
	Host    string // only for a row with no uuid: a tmux session name is unique per host
	Session string // tmux session name, for a row with no uuid
	ID      string // Claude session id
	CWD     string // the conversation's working directory, which the id does not pin
}

// AliasKeyOf is the one place a row becomes an AliasKey, so the reader and the writer cannot
// disagree about what identifies a session.
func AliasKeyOf(p registry.Pane) AliasKey {
	// registry.Pane.Claude is the one place that knows WHICH field carries the uuid, and the
	// answer differs by kind — an agent row keeps it in SessionID, a pane row that has been
	// joined to a conversation in ClaudeSession, and a pane row's own SessionID is tmux's `$3`.
	if id := p.Claude(); id != "" {
		return AliasKey{Kind: registry.KindAgent, ID: id, CWD: p.Path}
	}
	return AliasKey{Kind: registry.KindPane, Host: p.Host, Session: p.Session}
}

// Aliases is the operator's names for sessions. The zero value is valid and empty.
type Aliases struct {
	byKey map[AliasKey]string
}

// Len is how many names are stored, for a footer or a test.
func (a Aliases) Len() int { return len(a.byKey) }

// Set stores a name, or REMOVES it when the name is blank.
//
// Blank means remove because that is how §21.12 rule 5 un-names a row — `N`, `ctrl+u`,
// `enter` — and storing a blank would leave a row named nothing, which reads on screen as a
// row whose name failed to load.
func (a *Aliases) Set(k AliasKey, name string) {
	name = strings.TrimSpace(name)
	if name == "" {
		delete(a.byKey, k)
		return
	}
	if a.byKey == nil {
		a.byKey = map[AliasKey]string{}
	}
	a.byKey[k] = name
}

// Get returns the operator's name for a key, if there is one.
func (a Aliases) Get(k AliasKey) (string, bool) {
	v, ok := a.byKey[k]
	return v, ok
}

// DisplayName is the ONE answer to "what is this row called", and every surface must call it
// so no screen can show a different name from another (§21.12 rule 6).
//
// Precedence: the operator's alias, then Claude's own name, then the tmux session name. The
// second return says whether the name shown is the OPERATOR's, which is what lets a screen
// mark it — and it is false for a derived name, or the marker would stop meaning anything.
//
// It never returns empty. A nameless row cannot be spoken about, so a row with nothing else
// falls back to its id.
func (a Aliases) DisplayName(p registry.Pane) (string, bool) {
	if v, ok := a.byKey[AliasKeyOf(p)]; ok {
		// CUT AT THE FIRST LINE, defensively. Check refuses a multi-line name at the write boundary,
		// and this is the read boundary: projects.toml is a file the operator may edit by hand, and a
		// build older than that refusal may already have written one. A name with a newline in it adds
		// a ROW to the dashboard — measured, 26 lines on a 24-row terminal — so the frame cannot be
		// left depending on the file being well formed. The same rule the footer's claimants have.
		return firstLine(v), true
	}
	// AgentName is Claude's own word for the session when a pane row has been joined to
	// one; for an agent row the same name is already in Session.
	for _, cand := range []string{p.AgentName, p.Session} {
		if strings.TrimSpace(cand) != "" {
			return cand, false
		}
	}
	if p.PaneID != "" {
		return p.PaneID, false
	}
	if p.SessionID != "" {
		return p.SessionID, false
	}
	return "(unnamed)", false
}

// Check reports whether a name may be committed for a key.
//
// Duplicates are refused FLEET-WIDE and CASE-FOLDED, because two rows reading the same on
// one screen is exactly the confusion a name exists to remove. A row may keep its OWN name:
// refusing that would make `N`, `enter` a dead end on a row that is already named.
//
// A blank name is always allowed — it is how a name is removed.
func (a Aliases) Check(k AliasKey, name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil
	}
	// A NAME IS A ONE-LINE OBJECT, and this is the write boundary where that is enforced.
	//
	// Measured: a name containing a newline was accepted here, reached DisplayName, and the DASHBOARD
	// GAINED A ROW — 26 lines on a 24-row terminal, drawn as `>  ▸ idle   » два` and then `имени` on
	// its own line. The frame's whole invariant is one screen row per fleet row, and a name is the one
	// field on it that comes from outside the program: an operator can paste one, and projects.toml is
	// a file they may edit by hand. A `\r` is worse than a `\n` — it returns the cursor to column 0 and
	// the row overwrites itself, which this repo has already paid for in the footer's host reasons.
	if i := strings.IndexFunc(name, refusedInAName); i >= 0 {
		return fmt.Errorf("a name is one line and this one is not (a line break at character %d) — "+
			"type it on one line", i+1)
	}
	folded := strings.ToLower(name)
	for other, existing := range a.byKey {
		if other == k {
			continue
		}
		if strings.ToLower(existing) == folded {
			return fmt.Errorf("%q is already the name of another session (%s) — two rows "+
				"reading the same is what a name is for removing", existing, aliasWhere(other))
		}
	}
	return nil
}

// aliasWhere says where the session a refusal is about lives, in the terms that key HAS. A uuid key
// has no host, so the old sentence ("another session on %s", Host) named nothing at all for exactly
// the keys this file now mostly holds — the directory is what a person can act on, and the uuid is
// the fallback for a conversation whose cwd the listing did not report.
func aliasWhere(k AliasKey) string {
	if k.ID == "" {
		return k.Host
	}
	if k.CWD != "" {
		return k.CWD
	}
	return k.ID
}

// Keys returns the stored keys in a stable order, for writing.
func (a Aliases) Keys() []AliasKey {
	out := make([]AliasKey, 0, len(a.byKey))
	for k := range a.byKey {
		out = append(out, k)
	}
	sort.Slice(out, func(i, j int) bool {
		x, y := out[i], out[j]
		if x.Host != y.Host {
			return x.Host < y.Host
		}
		if x.Kind != y.Kind {
			return x.Kind < y.Kind
		}
		if x.Session != y.Session {
			return x.Session < y.Session
		}
		if x.ID != y.ID {
			return x.ID < y.ID
		}
		return x.CWD < y.CWD
	})
	return out
}

// ParseAll reads both sections of projects.toml.
//
// Neither is mandatory: a file with only rules, only aliases, or nothing at all is valid,
// because the first `N` writes the file into existence and the first prefix rule may arrive
// long after.
func ParseAll(content string) (Rules, Aliases, error) {
	rules, err := Parse(stripSection(content, "alias"))
	if err != nil {
		return Rules{}, Aliases{}, err
	}
	aliases, err := parseAliases(stripSection(content, "project"))
	if err != nil {
		return Rules{}, Aliases{}, err
	}
	return rules, aliases, nil
}

// stripSection blanks out every record of one table so the other's parser sees only its own.
//
// Blanks rather than deletes, so the LINE NUMBERS in an error still match the file the
// operator is looking at — an error naming line 7 of a file whose line 7 is something else is
// worse than no line number.
func stripSection(content, table string) string {
	header := "[[" + table + "]]"
	out := strings.Split(content, "\n")
	in := false
	for i, l := range out {
		t := strings.TrimSpace(l)
		switch {
		case t == header:
			in = true
			out[i] = ""
		case strings.HasPrefix(t, "[["):
			in = false
		case in:
			out[i] = ""
		}
	}
	return strings.Join(out, "\n")
}

func parseAliases(content string) (Aliases, error) {
	var as Aliases
	type rec struct {
		host, session, id, cwd, name string
		line                         int
	}
	var recs []rec
	var cur *rec
	flush := func() {
		if cur != nil {
			recs = append(recs, *cur)
		}
	}
	err := conf.Scan(content, "alias",
		func() { flush(); cur = &rec{} },
		func(key, value string, line int) error {
			s, err := conf.String(value)
			if err != nil {
				return err
			}
			if cur.line == 0 {
				cur.line = line
			}
			switch key {
			case "host":
				cur.host = s
			case "session":
				cur.session = s
			case "id":
				cur.id = s
			case "cwd":
				cur.cwd = s
			case "name":
				cur.name = s
			default:
				return fmt.Errorf("unknown key %q", key)
			}
			return nil
		})
	if err != nil {
		return Aliases{}, err
	}
	flush()

	for _, r := range recs {
		where := fmt.Sprintf("line %d", r.line)
		switch {
		case r.name == "":
			return Aliases{}, fmt.Errorf("%s: an alias needs a name", where)
		case r.session == "" && r.id == "":
			return Aliases{}, fmt.Errorf("%s: alias %q needs either a session (a tmux "+
				"session name) or an id (a Claude session id)", where, r.name)
		case r.session != "" && r.id != "":
			return Aliases{}, fmt.Errorf("%s: alias %q names both a session and an id, and "+
				"they identify different things — keep one", where, r.name)
		case r.id == "" && r.host == "":
			return Aliases{}, fmt.Errorf("%s: alias %q names a tmux session, so it needs a "+
				"host — a session name is not unique across the fleet", where, r.name)
		}
		k := AliasKey{Kind: registry.KindPane, Host: r.host, Session: r.session}
		if r.id != "" {
			// `host` is READ and IGNORED for a uuid record, deliberately. Every alias the operator
			// has typed so far carries one, because the key used to hold it; refusing those records
			// would cost them every name they have given, and a uuid is global anyway. RenderAll no
			// longer writes the field, so the first `N` clears it from the file.
			k = AliasKey{Kind: registry.KindAgent, ID: r.id, CWD: r.cwd}
		}
		as.Set(k, r.name)
	}
	return as, nil
}

// RenderAll writes both sections back.
func RenderAll(rules []Rule, as Aliases) string {
	var b strings.Builder
	b.WriteString(Render(rules))
	for _, k := range as.Keys() {
		name, _ := as.Get(k)
		b.WriteString("[[alias]]\n")
		// No `host` for a uuid record: the reader ignores it, and a field nobody reads is a claim
		// the next reader of this file would believe. A tmux session name still needs one.
		if k.ID != "" {
			fmt.Fprintf(&b, "id = %s\n", conf.Quote(k.ID))
			if k.CWD != "" {
				fmt.Fprintf(&b, "cwd = %s\n", conf.Quote(k.CWD))
			}
		} else {
			fmt.Fprintf(&b, "host = %s\n", conf.Quote(k.Host))
			fmt.Fprintf(&b, "session = %s\n", conf.Quote(k.Session))
		}
		fmt.Fprintf(&b, "name = %s\n\n", conf.Quote(name))
	}
	return b.String()
}

// LoadAll reads the file. An absent file is empty and NOT an error.
//
// Like Load, a parse failure returns empty values AND the error: an unparseable
// projects.toml must lose names and keep the fleet (§21.11.3).
func LoadAll(path string) (Rules, Aliases, error) {
	raw, err := os.ReadFile(path)
	switch {
	case os.IsNotExist(err):
		return Rules{}, Aliases{}, nil
	case err != nil:
		return Rules{}, Aliases{}, err
	}
	return ParseAll(string(raw))
}

// Save writes both sections atomically, so a crash mid-write leaves the previous file rather
// than a truncated one — this file holds decisions the operator typed.
func Save(path string, rules []Rule, as Aliases) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".projects-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.WriteString(RenderAll(rules, as)); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// firstLine is a value cut at its first line break, so a one-line surface stays one line whatever a
// file holds. It marks the loss, because a name silently shortened is a name the operator cannot
// match against what they typed.
func firstLine(v string) string {
	// The SAME set Check refuses, which is the whole point of a write boundary and a read boundary
	// being about one rule. They disagreed: Check refused a tab and every control character while this
	// cut only at a newline or a carriage return, so a tab in a hand-edited projects.toml — or one
	// written by a build older than that refusal — reached the row and expanded to a variable width,
	// misaligning the column the eye runs down.
	if i := strings.IndexFunc(v, refusedInAName); i >= 0 {
		return strings.TrimRight(v[:i], " \t") + " …"
	}
	return v
}

// refusedInAName is the ONE predicate for "this character cannot be in a name", read by the refusal at
// the write boundary and by the cut at the read boundary. Two copies of it drifted the first time one
// of them was written.
func refusedInAName(r rune) bool { return r == '\n' || r == '\r' || r == '\t' || r < 0x20 }
