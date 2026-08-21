package ui

import (
	"context"
	"testing"

	"github.com/DawnBreather/tmux-hub/internal/broadcast"
	"github.com/DawnBreather/tmux-hub/internal/hub"
	"github.com/DawnBreather/tmux-hub/internal/launch"
)

// Three paths create a pane that runs `claude`, and `LoginPayload` is only a fix at the ones that
// actually SEND it: a builder that returns the right string and a create that never passes it are the
// same screen. `payload_test.go` pins what the builder produces; this file pins what reaches tmux.
//
// The defect it guards is the one reported on dev-air: a pane inherits the tmux CLIENT's environment,
// the hub's remote client is `ssh <host> tmux …` — non-interactive — and that PATH does not contain
// `claude`. The bare argv died with "command not found", nothing held the pane, tmux destroyed
// pane → window → session, and the operator was shown `invalid window id: ""`. So a create carrying
// `plan.Command` instead of the wrapper is precisely the broken product, and the assertion is an
// EQUALITY against the builder rather than a substring, because every substring of the wrapper that
// looks like the command is also a substring of the bare argv.

func TestEveryPaneTheLaunchCreatesRunsTheLoginPayload(t *testing.T) {
	for _, c := range []struct {
		name string
		verb string
		spec launch.Spec
	}{
		{"into a new window", "new-window", launch.Spec{
			Host: "local", CWD: "/srv/api", Model: "opus"}},
		{"into a new session", "new-session", launch.Spec{
			Host: "local", CWD: "/srv/api", Model: "opus", NewSession: true, SessionName: "api"}},
	} {
		r := &recordingRunner{paneID: "%42", windowID: "@7"}
		stamper := broadcast.NewStamper(r, broadcast.Instance("test"))
		keeper := broadcast.NewKeeper(stamper)
		m := model{
			ctx:     context.Background(),
			run:     r,
			stamper: stamper,
			keeper:  keeper,
			hosts:   []hub.Host{{Label: "local", Socket: "/tmp/test.sock"}},
		}

		msg := m.launch(c.spec)()
		lmsg, ok := msg.(launchMsg)
		if !ok {
			t.Fatalf("%s: the launch answered with %T, want launchMsg", c.name, msg)
		}
		if lmsg.err != nil {
			t.Fatalf("%s: the launch failed: %v", c.name, lmsg.err)
		}

		// The uuid is generated INSIDE the launch, and Adopt is the only place it surfaces — which is
		// why the expected payload is rebuilt from the run rather than written down: a literal here
		// would be a second copy of `Spec.Build`'s flag order, and the two would drift.
		id, ok := keeper.Session("local", "%42")
		if !ok || id == "" {
			t.Fatalf("%s: the created pane was not adopted, so the launch's own uuid is not "+
				"recoverable and the payload cannot be rebuilt", c.name)
		}
		plan, err := c.spec.Build(id)
		if err != nil {
			t.Fatalf("%s: rebuilding the plan: %v", c.name, err)
		}
		want, err := LoginPayload(plan.Argv)
		if err != nil {
			t.Fatalf("%s: LoginPayload refused the launch's own argv: %v", c.name, err)
		}
		// The pole that keeps the equality below from being vacuous: if the wrapper were the bare
		// command, the assertion would hold for the shape that could not find `claude` at all.
		if want == plan.Command {
			t.Fatalf("%s: this test cannot fail — the wrapper equals the bare argv", c.name)
		}

		create := r.argvOf(c.verb)
		if create == nil {
			t.Fatalf("%s: no %s was issued at all; calls: %v", c.name, c.verb, r.calls)
		}
		// LAST, because all three tmux helpers append the payload after every flag they add.
		if got := create[len(create)-1]; got != want {
			t.Errorf("%s: the pane is not given the login wrapper:\n  got  %s\n  want %s",
				c.name, got, want)
		}
	}
}

// `LoginPayload`'s guard is a WHITELIST, so the question it raises is not "is today's argv safe" but
// "can the form OFFER a value the payload cannot carry". It can only offer two lists — the launch form
// cycles `launch.Models` and `launch.PermissionModes` and types nothing else into a flag — so those two
// lists are the whole vocabulary that reaches the wrapper, and crossing them is a complete answer.
//
// The cost of getting this wrong is paid far from here: a menu value holding a space, a `%` or a quote
// builds a plan that `Validate` accepts, and the refusal then arrives at the moment the operator presses
// enter, on a remote host, as a sentence about an argument they did not type. This makes it a build
// failure instead.
//
// The count is printed and floored because a matcher over an emptied list passes having checked nothing
// — the shape this repo has been bitten by, and here the lists are what a future edit shrinks.
func TestEveryModelAndModeTheFormCanOfferSurvivesThePayload(t *testing.T) {
	// The empty string is a real menu position, not padding: it is what the form sends before the
	// operator cycles either field, and it is the only value that changes the argv's LENGTH.
	models := append([]string{""}, launch.Models...)
	modes := append([]string{""}, launch.PermissionModes...)

	dir := t.TempDir()
	const uuid = "9c1d0e7b-3a5f-4c62-9d18-2b7e6f0a4c31"

	checked := 0
	for _, m := range models {
		for _, pm := range modes {
			spec := launch.Spec{Host: "local", Local: true, CWD: dir, Model: m, PermissionMode: pm}
			plan, err := spec.Build(uuid)
			if err != nil {
				t.Errorf("model %q + permission mode %q: the form can offer this pair and Build "+
					"refuses it: %v", m, pm, err)
				continue
			}
			if _, err := LoginPayload(plan.Argv); err != nil {
				t.Errorf("model %q + permission mode %q: the form can offer this pair and the pane "+
					"cannot be given it: %v", m, pm, err)
			}
			checked++
		}
	}

	// A floor rather than an exact number, so adding a model is not a red build — but a floor that a
	// shortened list cannot clear, since `checked == 0` and `checked == 1` are what "the guard looked at
	// nothing" looks like.
	if checked < 16 {
		t.Errorf("only %d pairs were checked: %d models × %d modes. Either a menu list was emptied or "+
			"this guard stopped covering the vocabulary", checked, len(models), len(modes))
	}
	t.Logf("checked %d pairs: %d models × %d permission modes", checked, len(models), len(modes))

	// The pole. Without it the loop above is green against a payload builder that refuses nothing, which
	// is the one outcome that would let the whole class back in.
	for _, bad := range []string{"my model", "opus%", "opus'", "$MODEL"} {
		plan := launch.Plan{Argv: []string{"claude", "--model", bad}}
		if _, err := LoginPayload(plan.Argv); err == nil {
			t.Errorf("a model of %q was accepted, so this test cannot fail — the wrapper would carry a "+
				"character no login shell can be handed unquoted", bad)
		}
	}
}

// The restart is the third producer, and it is the one with no screen of its own: `r` respawns the
// pane in place, so a payload that cannot find `claude` replaces a working session with an empty pane.
func TestTheRestartRespawnsUnderTheLoginPayload(t *testing.T) {
	const uuid = "3f2a1b09-f68c-4baf-98fd-68d4fd1c3da4"

	r := &recordingRunner{paneID: "%42", windowID: "@7"}
	stamper := broadcast.NewStamper(r, broadcast.Instance("test"))
	m := model{
		ctx:     context.Background(),
		run:     r,
		stamper: stamper,
		keeper:  broadcast.NewKeeper(stamper),
		hosts:   []hub.Host{{Label: "local", Socket: "/tmp/test.sock"}},
	}

	msg := m.doRestartCmd("local", "%9", "$3", uuid)()
	rm, ok := msg.(restartMsg)
	if !ok {
		t.Fatalf("the restart answered with %T, want restartMsg", msg)
	}
	if rm.err != nil {
		t.Fatalf("the restart failed: %v", rm.err)
	}

	want, err := LoginPayload([]string{"claude", "--resume", uuid})
	if err != nil {
		t.Fatalf("LoginPayload refused a plain uuid: %v", err)
	}
	respawn := r.argvOf("respawn-pane")
	if respawn == nil {
		t.Fatalf("nothing was respawned; calls: %v", r.calls)
	}
	// One element and the whole wrapper. The equality carries both, which is why it is preferred to a
	// `Contains`: the payload's words passed as separate arguments would satisfy every substring test
	// and tmux would run only the first of them. It also pins the argument as the FULL uuid — the
	// difference between this producer and the door, where `attach`, `logs` and `stop` resolve the
	// short id only.
	if got := respawn[len(respawn)-1]; got != want {
		t.Errorf("the respawned pane is not given the login wrapper:\n  got  %s\n  want %s", got, want)
	}
}
