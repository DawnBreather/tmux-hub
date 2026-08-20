package agents

import "testing"

// `ID` is the listing's OWN short id or nothing, because it is the only string
// `claude attach|logs|stop` accepts and a manufactured one is refused.
//
// Parse used to back-fill `ID` from `SessionID[:8]` when the listing gave none, so every consumer
// downstream saw an 8-character string that LOOKS like an id and answers `No job matching`. Measured
// on the live fleet: 57 background rows carry a real id, and 8 interactive rows carried a
// manufactured one — the report, the tile and `K` could each hand the operator a command that fails.
//
// The back-fill existed only so the registry's row KEY had a stable string. That belongs to the key
// builder, which is where it lives now.
func TestParseDoesNotInventAShortId(t *testing.T) {
	const body = `[
	  {"id":"799d2bbc","sessionId":"799d2bbc-4f01-4690-bbd4-29d42779a154","kind":"background",
	   "name":"real","cwd":"/w/a","state":"failed"},
	  {"sessionId":"1ff133f7-c34a-4c60-91e5-b0048842cc66","kind":"interactive",
	   "name":"no id of its own","cwd":"/w/b","status":"idle"}
	]`
	ss, err := Parse([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	if len(ss) != 2 {
		t.Fatalf("parsed %d sessions, want 2", len(ss))
	}
	if ss[0].ID != "799d2bbc" {
		t.Errorf("the listing's own id was lost: %q", ss[0].ID)
	}
	if ss[1].ID != "" {
		t.Errorf("a manufactured id reached ID: %q — the verbs answer `No job matching` for it, "+
			"so anything that prints it hands the operator a command that fails", ss[1].ID)
	}
	// The uuid is still there: it is the identity, just not an argument.
	if ss[1].SessionID != "1ff133f7-c34a-4c60-91e5-b0048842cc66" {
		t.Errorf("the session id was lost: %q", ss[1].SessionID)
	}
}
