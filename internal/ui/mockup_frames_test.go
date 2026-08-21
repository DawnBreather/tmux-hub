// The FRAMES of docs/ui-mockup.html that carry an assertion, and the gate that
// checks them. No build tag, deliberately — the same split flows_frames_test.go
// makes, for the same reason.
//
// mockup_test.go is behind `//go:build mockup`, so anything expressed there runs
// in no default gate: `go test ./...` never compiles it and `go vet -tags mockup`
// only type-checks it. A frame built from View() needs nothing but the product, so
// its promise is checkable HERE, where `go test ./...` can go red. Only the HTML
// write stays tagged.
//
// The types live here rather than in the generator for the same reason the screen
// fixtures had to move to fixtures_test.go: a `scene` declared under the tag makes
// every builder that returns one unreachable from the default suite.
package ui

import (
	"fmt"
	"html"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/DawnBreather/tmux-hub/internal/fleet"
	"github.com/DawnBreather/tmux-hub/internal/hostset"
	"github.com/DawnBreather/tmux-hub/internal/hub"
	"github.com/DawnBreather/tmux-hub/internal/project"
	"github.com/DawnBreather/tmux-hub/internal/registry"
	"github.com/DawnBreather/tmux-hub/internal/state"
)

type shot struct {
	title  string // what the screen is
	did    string // what the user just did to get here
	look   string // what we are evaluating about it
	w, h   int
	screen string

	// want and deny are what this frame PROMISES, checked by
	// TestMockupFramesAssertWhatTheyShow. They are here and not in the caption
	// because a caption is prose and prose cannot go red: docs/mockup-authoring.md
	// rule 4 asks each frame to carry its own assertion, and rule 8 asks that the
	// assertion be checked somewhere it can fail.
	//
	// deny is not decoration. Three of these frames are about an ABSENCE — no tick
	// box on a host that cannot work — and an empty screen satisfies every absence,
	// so each negative is paired with a positive on the same frame.
	want []string
	deny []string
}

type scene struct {
	name  string
	intro string
	shots []shot
}

// pickerFleet is the probe round behind the picker's frames: twenty candidates, of
// which five answer with tmux.
//
// **The aliases are FICTIONAL, and that is a hard rule for this file rather than a
// preference.** `docs/` is mounted read-only into a running Caddy container
// (`deploy/ui-draft/docker-compose.yml`) and served at a public URL, so a frame is
// published the moment it is written — no commit and no deploy step. Only `nuc` is kept,
// because the document already contained it; every other name here is invented, and
// `github.com` stays only because it is a public service rather than somebody's host.
// Checked before writing: `eu`, `web-app`, `web-db`, `side-desk` and `studio-ws` — all
// real hosts of this machine, all named in the approved target frame — appeared ZERO
// times in the published file, so using them would have added five private aliases to a
// public page.
//
// What is borrowed from the approved target frame (picker_test.go's targetFrameRows,
// checked line for line by TestThePickerBodyMatchesTheApprovedTargetFrame) is its
// SHAPE: twenty candidates, five usable, one per exclusion class, one version that
// differs from the rest. docs/mockup-authoring.md rule 1 asks for the nearest real
// frame as the seed, and the shape is the part of it that carries the layout — a name
// is not what any of these frames is evidence about. Every alias is ≤ 13 columns, the
// picker's name column, so the substitution moves no padding.
//
// The ORDER differs from the target frame's, and that is the ssh config's order rather
// than a property of the picker: the disputed host sits fourth here so that one 120×24
// frame can hold every row shape at once. It cannot, otherwise — the first attempt put
// that row seventh, six rows of budget showed five of them, and the frame promised a
// `[!]` that was not on screen. The check caught that, which is the whole reason it is
// in the default suite.
//
// The reasons are `hostset.reasonFor`'s own strings, verbatim, because their LENGTH is
// what the 80-column frame is about — a paraphrase would wrap somewhere else and the
// frame would stop being evidence. Those are the product's own words and the point of
// the screen, so they are the one thing here that is never invented.
func pickerFleet() ([]hostset.Candidate, []hostset.Result) {
	results := []hostset.Result{
		{Alias: "nuc", Version: "3.2a", Usable: true},
		{Alias: "staging-2", Version: "3.2a", Usable: true},
		{Alias: "lab-nuc", Version: "3.2a", Usable: true},
		{Alias: "build-01", Reason: "no tmux — install it there, or leave this host off"},
		{Alias: "github.com", Reason: "not a shell host — this is a git remote, so leave it off"},
		{Alias: "dev-box", Version: "3.4", Usable: true},
		{Alias: "office-nas", Version: "3.2a", Usable: true},
	}
	for len(results) < 20 {
		results = append(results, hostset.Result{
			Alias:  fmt.Sprintf("stale%02d", len(results)),
			Reason: "DNS does not resolve — a stale ssh config entry? fix or remove it",
		})
	}
	cands := make([]hostset.Candidate, len(results))
	for i, r := range results {
		cands[i] = hostset.Candidate{Alias: r.Alias}
	}
	return cands, results
}

// pickerAsked is the moment the probe behind these frames ran. It is FIXED rather than
// time.Now() because a timed-out row prints it, and a clock in a generated document
// rewrites the file on every run — the diff would then always be non-empty and would
// stop meaning anything.
var pickerAsked = time.Date(2026, 8, 13, 9, 41, 7, 0, time.UTC)

// localOnly is the fleet a FIRST run has: the local server and nothing else. base()
// hands out two hosts, which would put `nuc up` in the footer of a screen whose whole
// subject is that no remote host has been chosen yet.
func localOnly() []hub.Host {
	return []hub.Host{{Label: "local", Socket: "/tmp/tmux-1000/default",
		Status: hub.Up, Version: "3.7b", LocalProc: true}}
}

// pickerBackdrop is the dashboard the overlay sits on for the frames where the user
// pressed `p` on a hub that is already polling. Two panes rather than a full fleet:
// the picker takes half the screen, so a longer cast would only be a list cut off by
// the rule, and the frame is about the overlay.
func pickerBackdrop() []registry.Pane {
	return []registry.Pane{
		agentPane("local", "api", "review", "%0", 0, state.Needs,
			"  Do you want to proceed?", "  ❯ 1. Yes", "    2. No"),
		agentPane("nuc", "deploy", "migrate", "%4", 0, state.Quiet,
			"  Applied 3 migrations.", "  ❯ "),
	}
}

// pickerLocalOnly is the first run's backdrop: the local server's own panes and
// nothing else. It is not an empty dashboard, because §16's commitment is that the
// local fleet is on screen before any network work starts — and the picker is the
// screen where that is visible, since it is up precisely while the probe is out.
func pickerLocalOnly() []registry.Pane {
	return []registry.Pane{
		agentPane("local", "api", "review", "%0", 0, state.Needs,
			"  Do you want to proceed?", "  ❯ 1. Yes", "    2. No"),
		agentPane("local", "api", "fix", "%1", 1, state.Works,
			"  Reading internal/tmux/batch.go…", "  esc to interrupt"),
	}
}

// pickerModel puts the picker up over a dashboard, through the same clamp production
// uses. The cursor is not assigned directly anywhere here: clampPickerCursor is what
// keeps it off a row that refuses every key, and a frame that set the cursor by hand
// would show a position production cannot open on.
func pickerModel(t *testing.T, w, h int, rows []PickerRow, hosts []hub.Host, panes ...registry.Pane) model {
	t.Helper()
	m := base(t, w, h, panes...)
	m.hosts = hosts
	m.mode = modePicker
	m.picker = rows
	return m.clampPickerCursor()
}

// pickerWithBehind is pickerModel plus the section of machines the hops declare, installed through
// the product's own `WithDiscovered` so the frame is evidence about the option an operator's run uses
// and not about a field this file set.
//
// It exists because the published document had no frame of this feature at all — 25 commits of it —
// and this repository has a written lesson about exactly that: a byte-reproducible mockup blind to a
// class cannot show a regression in it, so the next refactor moves the frame and the diff is empty.
func pickerWithBehind(t *testing.T, w, h int, rows []PickerRow, hosts []hub.Host, behind []DiscoveredRow, panes ...registry.Pane) model {
	t.Helper()
	m := pickerModel(t, w, h, rows, hosts, panes...)
	WithDiscovered(behind)(&m)
	return m
}

// behindTheHops is the fixture for that frame, and every field of it is the shape the real crawl
// produces rather than an invention. The two states are the only two the shipped crawl can reach
// (docs/design.md §9's own "shipped today" column): a machine whose recipe names no key this side
// holds, and a machine standing behind a proxy that cannot be identified at all. The remedy strings
// are `fleet.Diagnose`'s own words for those two cases, which is what makes the frame worth reading —
// the section's entire product is the act, so a frame that carried invented prose would prove nothing.
//
// Names are invented on purpose: docs/ is served publicly by a live Caddy at the moment of writing, so
// no real host of this machine may appear here. Reasons are real, because they are the program's.
func behindTheHops() []DiscoveredRow {
	return []DiscoveredRow{
		{
			Label: "lab-gpu", Observer: "staging-2", State: fleet.Blocked,
			Reason: "run `ssh-copy-id build@lab-gpu.internal`, or copy one of ~/.ssh/id_rsa, " +
				"~/.ssh/id_ecdsa, ~/.ssh/id_ecdsa_sk, ~/.ssh/id_ed25519, ~/.ssh/id_ed25519_sk to " +
				"this machine — the recipe names no key that is here",
		},
		{
			Label: "vault-01", Observer: "staging-2", State: fleet.Blocked,
			Reason: "give this machine a direct route — bastion-a stands between it and " +
				"vault-01.internal, and a proxied handshake reports the proxy's host key rather " +
				"than vault-01.internal's, so the hub cannot tell which machine answered",
		},
	}
}

// pressJUntil walks the cursor down with the real key handler until it rests on
// `alias`, and FAILS rather than looping if it never gets there. It drives `j` instead
// of assigning pickerCursor because the scroll window is what the 80-column frame is
// evidence about, and that window is computed from the cursor the keys produce.
func pressJUntil(t *testing.T, m model, alias string) model {
	t.Helper()
	for i := 0; i <= len(m.picker); i++ {
		if m.picker[m.pickerCursor].Alias == alias {
			return m
		}
		next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
		m = next.(model)
	}
	t.Fatalf("j never reached %q in %d rows", alias, len(m.picker))
	return m
}

// pickerScene is §9's screen in the three states the design names, plus the empty one
// it also names. Every frame is what View() prints.
func pickerScene(t *testing.T) scene {
	t.Helper()
	cands, results := pickerFleet()

	// 1. Nothing decided yet — the first run, hosts.toml absent. `kept` is nil, so
	// every usable host takes the probe's answer as its default, which is what makes
	// zero configuration a working configuration (§9).
	firstRun := PickerRowsFor(cands, results, nil, nil, pickerAsked)

	// 2. A mixed fleet — hosts.toml has decisions and the probe now disagrees with
	// one of them. Four row shapes on one screen, which is the point of the frame:
	// kept and answering, kept and merely SLOW, kept and now unusable (`[!]`), kept
	// and switched off, and a candidate the file says nothing about.
	kept := []hostset.Entry{
		{Alias: "nuc", Enabled: true},
		{Alias: "staging-2", Enabled: true},
		{Alias: "build-01", Enabled: true},
		{Alias: "lab-nuc", Enabled: false},
	}
	slow := append([]hostset.Result(nil), results...)
	for i := range slow {
		if slow[i].Alias == "staging-2" {
			slow[i] = hostset.Result{Alias: "staging-2", TimedOut: true, Reason: slowReason}
		}
	}
	mixed := PickerRowsFor(cands, slow, kept, nil, pickerAsked)

	// 3. Every host excluded — a real state, not a failure. No row offers a box and
	// the cursor has nowhere better than row 0.
	var noneCands []hostset.Candidate
	var noneResults []hostset.Result
	for i, r := range results {
		if r.Usable {
			continue
		}
		noneCands, noneResults = append(noneCands, cands[i]), append(noneResults, r)
	}
	none := PickerRowsFor(noneCands, noneResults, nil, nil, pickerAsked)

	return scene{
		name: "Выбор хостов — первый запуск",
		intro: "Экран, который человек встречает первым: кандидаты из ~/.ssh/config, что ответил " +
			"каждый, и галочка у тех, кого можно взять. Он же всегда доступен по p. Пробы у него " +
			"ТРИ исхода, а не два: хост, ответивший версией; хост, не ответивший вовремя — он " +
			"медленный, а не отсутствующий, и галочку сохраняет; и хост, ответивший чем-то другим " +
			"— у такого галочки нет, потому что завтра ответ будет тем же. Хосты здесь — те же " +
			"двадцать кандидатов той же ФОРМЫ, что в утверждённом целевом кадре, но имена " +
			"выдуманы: docs/ раздаётся публично живым Caddy, поэтому кадр публикуется в момент " +
			"записи, и настоящих хостов этой машины здесь быть не должно. Реальны только строки " +
			"причин — это слова самой программы.",
		shots: []shot{
			{
				title: "1. Ничего ещё не решено — первый запуск, 110×40",
				did:   "hosts.toml нет, хаб сам открыл экран и опросил всех кандидатов",
				look: "понятно ли без подсказки, что делать: у каждого исключённого хоста есть " +
					"причина И средство, у пригодных галочка уже стоит. Сзади дашборд — только " +
					"локальный сервер, и его панели УЖЕ на экране, пока проба ещё идёт (§16).",
				w: 110, h: 40,
				screen: pickerModel(t, 110, 40, firstRun, localOnly(), pickerLocalOnly()...).View(),
				want: []string{
					"Hosts — 20 candidates in ~/.ssh/config, 5 answer with tmux",
					"› [x] nuc           tmux 3.2a",
					"  [x] office-nas    tmux 3.2a",
					"      github.com    not a shell host — this is a git remote, so leave it off",
					"      build-01      no tmux — install it there, or leave this host off",
					"space: keep this host · enter: save and connect · esc: cancel · r: probe again",
					// §16, on the same frame: the local fleet is already listed and counted
					// while the picker is up, so probing has not gated it.
					"tmux-hub  2 sessions",
					">  ⚑ needs  %0   claude",
				},
				// A host whose answer will read the same tomorrow gets no box at all —
				// and the ticks above are the positive half, so an empty frame cannot
				// satisfy this pair.
				deny: []string{"[x] github.com", "[ ] github.com", "[x] build-01"},
			},
			{
				title: "2. Смешанный парк — файл уже решил, а проба теперь спорит, 120×24",
				did:   "нажал p на работающем дашборде. staging-2 в этот раз не ответил, build-01 потерял tmux",
				look: "различимы ли четыре разных состояния строки: взят и отвечает, взят и всего лишь " +
					"МЕДЛЕННЫЙ (галочка остаётся, рядом время последнего вопроса), взят и теперь " +
					"непригоден ([!], но выключить его всё ещё можно), взят и выключен.",
				w: 120, h: 24, screen: pickerModel(t, 120, 24, mixed, hosts2(), pickerBackdrop()...).View(),
				want: []string{
					// A timeout is tallied apart from both the usable and the excluded,
					// because it is a third outcome and not a softer exclusion (§9).
					"Hosts — 20 candidates in ~/.ssh/config, 4 answer with tmux, 1 timed out",
					"› [x] nuc           tmux 3.2a",
					"  [x] staging-2     no answer in 10s — this host is slow rather than absent;",
					"(asked 09:41:07)",
					"  [ ] lab-nuc       tmux 3.2a",
					"  [!] build-01      no tmux — install it there, or leave this host off",
				},
				deny: []string{"[ ] github.com", "[x] github.com"},
			},
			{
				title: "3. То же на 80 колонках — размер, который держим",
				did:   "курсор доведён до build-01, чтобы спорная строка была на экране",
				look: "уцелевает ли СРЕДСТВО, а не только жалоба, когда причина шире колонки: " +
					"«raise --probe-timeout» — единственное, что тут можно нажать, и перенос " +
					"идёт по словам, а не обрезанием.",
				w: 80, h: 24, screen: pressJUntil(t, pickerModel(t, 80, 24, mixed, hosts2(), pickerBackdrop()...), "build-01").View(),
				want: []string{
					// The remedy on its own line, indented to the gutter — the whole of it,
					// not the part that happened to fit.
					"                    enable it anyway, or raise --probe-timeout (asked 09:41:07)",
					"› [!] build-01      no tmux — install it there, or leave this host off",
				},
				// The measured pre-wrap cut, verbatim: `…rather than absent; ena`, the
				// remedy gone from the most important row on the screen. It cannot be a
				// substring of this frame unless the wrap stopped happening.
				deny: []string{"absent; ena", "[ ] github.com"},
			},
			{
				title: "4. Что стоит ЗА взятыми хостами — 100×32",
				did: "хаб спросил каждый взятый хост, что объявляет ЕГО ~/.ssh/config, и " +
					"разрешил транспорт каждой найденной машины на том же хосте",
				look: "несёт ли каждая строка ДЕЙСТВИЕ, а не диагноз: у первой машины это " +
					"конкретный ssh-copy-id, у второй — что между нами стоит прокси, и потому " +
					"её вообще нельзя опознать (отпечаток пришёл бы от прокси, а не от неё). " +
					"Список кандидатов при этом остаётся хозяином экрана: галочки на месте, " +
					"курсор виден, а секция взяла ровно столько, сколько осталось.",
				w: 100, h: 32,
				screen: pickerWithBehind(t, 100, 32, mixed, hosts2(), behindTheHops(), pickerBackdrop()...).View(),
				want: []string{
					// The heading names the TOTAL, which is what lets the section leave a machine
					// out honestly: the overlay is smaller than the terminal (pickerSplit decides
					// it), so at this size the section's share is five rows and it spends them on
					// ONE machine and the whole of its remedy rather than on two half-remedies.
					"Behind your hops — 2 machines your hosts declare",
					// The row says WHOSE config the machine came out of, because a name from a
					// hop's vocabulary is not addressable from here.
					"blocked   lab-gpu @staging-2",
					// And the ACT, whole — the half fleet.Diagnose puts first precisely so one
					// line can carry it, down to the last key it would offer.
					"run `ssh-copy-id build@lab-gpu.internal`",
					"no key that is here",
					// The candidate list is still the screen's subject, cursor and ticks intact.
					"› [x] nuc           tmux 3.2a",
					"space: keep this host · enter: save and connect · esc: cancel · r: probe again",
				},
				// It must not claim what it did not name: a marker naming a count over a section
				// that named nobody. The remedy being WHOLE is asserted positively above, by its
				// last clause — a deny on the wrap itself would forbid the wrapping, which is the
				// feature (an earlier draft of this needle did exactly that and the generator
				// refused to publish, which is the guard doing its job).
				deny: []string{"2 machines not shown", "[x] github.com"},
			},
			{
				title: "5. Тот же экран на 120×44 — обе машины целиком",
				did:   "то же состояние, терминал выше: секции хватает места на вторую машину",
				look: "растёт ли ответ вместе с терминалом — вторая машина появляется целиком, " +
					"и вместе с ней исчезает нужда в строке «сколько не показано»: заголовок " +
					"по-прежнему называет общее число, так что читателю не нужно её искать.",
				w: 120, h: 44,
				screen: pickerWithBehind(t, 120, 44, mixed, hosts2(), behindTheHops(), pickerBackdrop()...).View(),
				want: []string{
					"Behind your hops — 2 machines your hosts declare",
					"blocked   lab-gpu @staging-2",
					"blocked   vault-01 @staging-2",
					"run `ssh-copy-id build@lab-gpu.internal`",
					// The proxy diagnosis, which is the other of the two states the shipped crawl
					// can reach — and the only one whose remedy is not a command, because there
					// is no command: the operator has to give the machine a route.
					"give this machine a direct route — bastion-a stands between it and",
					"› [x] nuc           tmux 3.2a",
				},
				// Both machines are drawn, so no row may say any is hidden.
				deny: []string{"machines not shown", "machine not shown", "[x] github.com"},
			},
			{
				title: "4. Ни один хост не подходит",
				did:   "проба ответила по каждому кандидату, и ни один не назвал версию tmux",
				look: "читается ли это как названное состояние, а не как сломанный экран: " +
					"счётчик говорит «0 answer with tmux», у каждой строки причина со средством, " +
					"галочки нет ни у кого, r и esc работают.",
				w: 120, h: 24, screen: pickerModel(t, 120, 24, none, localOnly(), pickerLocalOnly()...).View(),
				want: []string{
					"Hosts — 15 candidates in ~/.ssh/config, 0 answer with tmux",
					"      github.com    not a shell host — this is a git remote, so leave it off",
					"space: keep this host · enter: save and connect · esc: cancel · r: probe again",
				},
				// No box of any kind, and the reasons above are the positive half.
				deny: []string{"[x]", "[ ]", "[!]"},
			},
			{
				title: "5. Показывать пока нечего",
				did:   "~/.ssh/config нет вообще — кандидатов ноль",
				look: "пустое состояние — это экран, а не пустая рамка: строка говорит, что " +
					"именно сделает r.",
				w: 120, h: 24, screen: pickerModel(t, 120, 24, nil, localOnly(), pickerLocalOnly()...).View(),
				want: []string{
					"Hosts — nothing to show yet; r asks every candidate in ~/.ssh/config",
					"space: keep this host · enter: save and connect · esc: cancel · r: probe again",
				},
				deny: []string{"[x]", "[ ]", "[!]"},
			},
		},
	}
}

// checkScenes verifies that every frame carrying a promise keeps it, and returns how
// many promises it checked — assertions, not frames, because a frame with five wants
// and a frame with one are not the same amount of evidence.
func checkScenes(t *testing.T, scenes []scene) int {
	t.Helper()
	checked := 0
	for _, s := range scenes {
		for _, sh := range s.shots {
			if len(sh.want) == 0 && len(sh.deny) == 0 {
				continue
			}
			if len(sh.want) == 0 {
				t.Fatalf("%s / %s: only negatives — an empty screen satisfies every "+
					"absence, so a deny needs a positive on the same frame", s.name, sh.title)
			}
			for _, w := range sh.want {
				if !strings.Contains(sh.screen, w) {
					t.Errorf("%s / %s (%d×%d): frame does not contain %q, so its caption is false:\n%s",
						s.name, sh.title, sh.w, sh.h, w, sh.screen)
				}
				checked++
			}
			for _, d := range sh.deny {
				if strings.Contains(sh.screen, d) {
					t.Errorf("%s / %s (%d×%d): frame contains %q, which its caption says is absent:\n%s",
						s.name, sh.title, sh.w, sh.h, d, sh.screen)
				}
				checked++
			}
		}
	}
	return checked
}

// repoRoot is this file's own copy of the generator's helper, because the generator's is
// behind the mockup tag and a default-suite test cannot see it.
func repoRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd() // internal/ui
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Dir(filepath.Dir(wd))
}

// The PUBLISHED bytes must be the frames the product prints, and this is the gate for
// that — a separate question from whether the frames keep their promises.
//
// It exists because proving the promise check discriminating exposed a gap it does not
// cover: editing a count line and a remedy directly in docs/ui-mockup.html left
// TestMockupFramesAssertWhatTheyShow green, since that test builds frames from View()
// and never reads the file. So a hand-edit of the document — or a regeneration that
// silently stopped including a frame — had nothing to answer to, and the file is
// SERVED, read-only, out of a running container at a public URL. The document being
// wrong is exactly the failure that matters.
//
// Only the picker's frames are checked, and only these: they are the deterministic ones.
// The rest of the document embeds `time.Now()`-relative timestamps (known-issues C2), so
// a whole-file comparison would fail on the clock rather than on drift, and a gate that
// cries wolf is worse than none. When C2 is fixed this should widen to every scene.
func TestThePublishedMockupHoldsTheFramesTheProductPrints(t *testing.T) {
	path := filepath.Join(repoRoot(t), "docs", "ui-mockup.html")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("cannot read the published document: %v", err)
	}
	published := html.UnescapeString(string(b))

	// BOTH scenes. The gate used to walk only the picker's, on the stated ground that they are
	// "the deterministic ones" — measured false for the naming scene, which is byte-identical
	// across builds and already in sync, so excluding it left five frames this branch declared
	// targets with no guard against the SERVED page drifting from the product. And docs/ is
	// bind-mounted read-only into a running Caddy container, so a drifted frame is published
	// with no commit step.
	scenes := []scene{pickerScene(t), namingScene(t)}
	total := 0
	for _, sc := range scenes {
		total += len(sc.shots)
	}
	if total == 0 {
		t.Fatal("the scenes built no frames, so this check would pass by looking at nothing")
	}
	for _, sc := range scenes {
		for _, sh := range sc.shots {
			// Compare the frame's own lines rather than the whole block, so a failure names
			// the row that drifted instead of printing two screens and leaving the diff to
			// the reader.
			for _, line := range strings.Split(sh.screen, "\n") {
				line = strings.TrimRight(line, " ")
				if line == "" {
					continue
				}
				if !strings.Contains(published, line) {
					t.Errorf("%s / %s (%d×%d): docs/ui-mockup.html does not contain this line "+
						"the renderer prints, so the published document is stale — regenerate "+
						"it with `go test -tags mockup -run TestGenerateMockup ./internal/ui/`:"+
						"\n  %q", sc.name, sh.title, sh.w, sh.h, line)
				}
			}
		}
	}
}

// The picker's frames in docs/ui-mockup.html must show what their captions claim, and
// this is the gate that says so on every `go test ./...`.
//
// The floor is what makes the count mean something. `checked` counting up from zero
// satisfies any assertion about frames, including one that stopped looking at
// anything — which is exactly how ui-flows-possession.html's fourteen assertions ran
// in no gate for the document's whole life while its banner said they were checked.
func TestMockupFramesAssertWhatTheyShow(t *testing.T) {
	// The floor rises with the naming scene's five frames, whose assertions are the whole
	// reason §21.14 called them "not targets yet".
	// The exact count as a FLOOR: adding an assertion is safe and REMOVING two is caught,
	// which is the direction that matters — a frame quietly losing its promise is a picture
	// in a published document that nobody verifies.
	const floor = 34 + 24
	got := checkScenes(t, []scene{pickerScene(t), namingScene(t)})
	if got < floor {
		t.Errorf("checked %d assertions, want at least %d — a frame that stopped being "+
			"checked is a picture in a published document promising something nobody verifies",
			got, floor)
	}
}

// namingScene builds the five frames §21.12 designs for `N`, and each one carries its own
// assertion — because docs/mockup-authoring.md rule 4 asks for that and rule 8 asks that the
// assertion be checked somewhere it can fail. §21.14 listed these five as "not targets yet"
// for exactly the missing-assertion reason; this is what makes them targets.
//
// **Fictional hosts.** `docs/` is served publicly, so only `nuc` — already in the published
// document — is real; `staging-2` and `lab-nuc` are invented, as in the picker's fleet.
func namingScene(t *testing.T) scene {
	t.Helper()
	const w, h = 100, 24

	// The tile's `id:` line carries AgentID, the id the verbs take, so the published document
	// shows a string an operator can actually paste after `claude logs`. It also stops this
	// document publishing a real session uuid five times: the uuid stays only as the fallback for
	// a row that has no short id, and this fixture has one. `docs/` is served publicly, so a
	// fixture that need not be real should not be.
	unnamed := registry.Pane{Kind: registry.KindAgent, Host: "nuc",
		AgentID: "5a485bc4", SessionID: "5a485bc4-0000-0000-0000-000000000000",
		Path:    "/w/dr-plan",
		Session: "verify-the-dr-plan", ClassifiedState: state.Needs}
	other := registry.Pane{Kind: registry.KindPane, Host: "staging-2", Session: "deploy",
		PaneID: "%3", Command: "claude", ClassifiedState: state.Works}

	withAlias := func(name string) project.Aliases {
		var a project.Aliases
		a.Set(project.AliasKeyOf(unnamed), name)
		return a
	}
	frame := func(al project.Aliases, nf namingForm) string {
		return RenderNaming(Frame{Panes: []registry.Pane{unnamed, other}, Hosts: hosts2(),
			Width: w, Height: h, Cursor: 0, Aliases: al}, nf)
	}
	typed := func(s string) Composer {
		var c Composer
		c.Insert(s)
		return c
	}

	var sh []shot

	// 1. Just opened on an UNNAMED row. The field is EMPTY — committing an untouched field
	// must not freeze a derived name into the file — and `now:` says the name is not the
	// operator's and where it does come from.
	sh = append(sh, shot{
		title: "1. Открыли N на строке без имени", did: "нажал N",
		look: "поле ПУСТОЕ, а `now:` говорит, чьё имя сейчас видно и откуда оно",
		w:    w, h: h,
		screen: frame(project.Aliases{}, namingForm{subject: unnamed}),
		want: []string{"name this session:", "nuc", "verify-the-dr-plan",
			"not yours", "Claude's own name", "name: ▏", "ctrl+u"},
		// Nothing pre-filled, and no marker claiming the name is the operator's.
		deny: []string{"name: verify", "» verify-the-dr-plan"},
	})

	// 2. The same row once named. The field opens with the ALIAS and nothing else.
	sh = append(sh, shot{
		title: "2. То же, когда имя уже есть", did: "нажал N на названной строке",
		look: "поле открывается ИМЕНЕМ ОПЕРАТОРА, а не производным",
		w:    w, h: h,
		screen: frame(withAlias("план восстановления"), namingForm{
			subject: unnamed, input: typed("план восстановления")}),
		want: []string{"name: план восстановления▏", "now:", "план восстановления"},
		deny: []string{"not yours"},
	})

	// 3. A duplicate refused. The reason is INSIDE the overlay — naming adds no claimant to
	// the footer — and the field keeps what was typed so it can be edited rather than retyped.
	dup := withAlias("план восстановления")
	reason := ""
	if err := dup.Check(project.AliasKeyOf(other), "План Восстановления"); err != nil {
		reason = err.Error()
	}
	sh = append(sh, shot{
		title: "3. Имя занято", did: "ввёл имя, которое уже есть у другой сессии, и нажал enter",
		look: "отказ ВНУТРИ оверлея, и поле сохраняет набранное",
		w:    w, h: h,
		screen: frame(dup, namingForm{subject: other, input: typed("План Восстановления"),
			reason: reason}),
		want: []string{"already the name", "name: План Восстановления▏"},
		deny: []string{"name: ▏"},
	})

	// 4. Un-naming: ctrl+u has cleared the field on a row that HAS a name, so `now:` still
	// shows the operator's name while the field is empty — the state in which enter removes it.
	sh = append(sh, shot{
		title: "4. Снятие имени", did: "нажал N, затем ctrl+u",
		look: "поле пусто, `now:` ещё показывает имя — в этом состоянии enter его снимает",
		w:    w, h: h,
		screen: frame(withAlias("план восстановления"), namingForm{subject: unnamed}),
		want:   []string{"name: ▏", "now:", "план восстановления", "remove the name"},
		deny:   []string{"not yours"},
	})

	// 5. The dashboard AFTER naming, which is what the operator actually lives with: the row
	// reads by their name and the `»` says the name is theirs.
	sh = append(sh, shot{
		title: "5. Дашборд после переименования", did: "нажал enter в оверлее",
		look: "строка читается по имени оператора, и маркер говорит, что имя ЕГО",
		w:    w, h: h,
		screen: Render(Frame{Panes: []registry.Pane{unnamed, other}, Hosts: hosts2(),
			Width: w, Height: h, Aliases: withAlias("план восстановления")}),
		// The CONTRAST is the point of naming two rows: one reads by the operator's name with the
		// `»`, the other by its DERIVED name with none. It used to be asserted as the other row's
		// session HEADER (`STAGING-2 DEPLOY`), which a lone pane no longer takes — its row carries
		// the session name itself now, so the contrast is asserted where both halves of it are.
		want: []string{"» план восстановления", "deploy"},
		// The raw name is gone from the row it named, and the marker is not on the other row.
		deny: []string{"» deploy"},
	})

	return scene{
		name: "Имя сессии — N",
		intro: "Оверлей ровно в шесть строк у подвала: разделитель, субъект, `now:`, поле, " +
			"причина, клавиши. Всегда шесть, поэтому ничего под ним не съезжает, когда " +
			"появляется отказ. Субъект — строка под курсором, снятая В МОМЕНТ нажатия: " +
			"список пересортируется под пробой, и субъект, уехавший между открытием и enter, " +
			"назвал бы не то, на что смотрели. Поле открывается ИМЕНЕМ ОПЕРАТОРА и никогда " +
			"производным — иначе нетронутый enter вморозил бы в файл слово Claude или имя " +
			"tmux-сессии. Двойники ловятся по всему парку, без учёта регистра, в момент " +
			"записи и по ПЕРЕЧИТАННОМУ файлу: другой хаб мог занять имя, пока экран открыт.",
		shots: sh,
	}
}
