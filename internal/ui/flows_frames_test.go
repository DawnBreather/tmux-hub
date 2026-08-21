// The FRAMES of docs/ui-flows-possession.html, and the check that each one shows
// what it promises. No build tag and no environment variable, deliberately.
//
// The document is served at tmux-ui-draft.DawnBreather.net and its banner says every
// frame is real and checked. It was not: the generator that checks them
// (flows_test.go) sits behind `//go:build mockup` AND calls t.Skip unless
// HUB_FLOW_CAPTURES is set — and a skip REPORTS PASS. So all fourteen assertions
// ran in no gate: `go test ./...` skipped them, `go vet -tags mockup` only compiled
// them, and nothing anywhere could go red if a frame stopped showing what its
// caption claimed.
//
// The split is by what a frame NEEDS. A frame built from View() needs nothing but
// the product, so its promise is checkable here, in the default suite. Only the
// `capture-pane` frames need a live nested tmux, and only the HTML write needs a
// place to write to; both stay in the tagged generator, which builds the same
// sections from the same code and checks all fourteen.
package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DawnBreather/tmux-hub/internal/hub"
	"github.com/DawnBreather/tmux-hub/internal/registry"
	"github.com/DawnBreather/tmux-hub/internal/state"
)

type origin int

const (
	originReal origin = iota
	originRealProposed
	originDrawn
	originTmux
)

type flowShot struct {
	title  string
	origin origin
	did    string
	assert string // what this frame promises
	want   string // substring the frame must contain; checked unless drawn
	deny   string // substring the frame must NOT contain, when absence is the point
	body   string
}

type flowSection struct {
	name  string
	intro string
	shots []flowShot
}

// flowCaptures supplies the `capture-pane` frames. It is a parameter rather than a
// file read inside the builder because that read is the ONLY thing in this document
// that needs a live tmux — and making the whole document depend on it is what put
// every View() frame's assertion behind an env var.
type flowCaptures func(t *testing.T, name string) (string, bool)

// noCaptures is the default suite's reader. There is no capture directory, so the
// tmux frames are simply absent from the sections it builds — never present with an
// empty body, which would be a frame whose assertion silently cannot be checked.
func noCaptures(*testing.T, string) (string, bool) { return "", false }

// capturesFrom reads them off disk and FAILS on a missing one: the generator must
// never write a document with a blank frame in it.
func capturesFrom(dir string) flowCaptures {
	return func(t *testing.T, name string) (string, bool) {
		t.Helper()
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("capture %s missing — run prototypes/possession-captures.sh first: %v", name, err)
		}
		return strings.TrimRight(string(b), "\n"), true
	}
}

// addCapture appends a tmux frame when its capture is available, and drops it when
// it is not.
func addCapture(t *testing.T, sh []flowShot, caps flowCaptures, name string, s flowShot) []flowShot {
	t.Helper()
	body, ok := caps(t, name)
	if !ok {
		return sh
	}
	s.origin, s.body = originTmux, body
	return append(sh, s)
}

// fleet20 is the cast for §20: two agents on the hub's own server, one on
// another, plus a host with no ssh control path and an agent row with no pane.
func fleet20() []registry.Pane {
	return []registry.Pane{
		agentPane("local", "api", "review", "%0", 0, state.Needs,
			"  Do you want to proceed?", "  ❯ 1. Yes", "    2. No"),
		agentPane("local", "api", "fix", "%1", 1, state.Works,
			"  Editing internal/ui/attach.go…", "  esc to interrupt"),
		agentPane("nuc", "deploy", "migrate", "%4", 0, state.Needs,
			"  Apply 12 changes?", "  ❯ 1. Yes", "    2. No"),
		agentPane("nuc", "deploy", "audit", "%5", 1, state.Works,
			"  Grepping for call sites…"),
	}
}

// noteFromAttach presses `a` the way the model does and returns the note the
// product produces. The refusal strings are never typed here: taking them from
// AttachCmd is what makes those frames `real` rather than a plausible quote.
func noteFromAttach(t *testing.T, p registry.Pane, h hub.Host) string {
	t.Helper()
	if _, err := AttachCmd(p, h); err != nil {
		return "cannot attach: " + err.Error()
	}
	t.Fatalf("AttachCmd(%s on %s) unexpectedly succeeded — this frame exists to show its refusal",
		p.PaneID, h.Label)
	return ""
}

// flowSections builds every frame of the document. One builder for both callers, so
// the frames the default suite checks are byte for byte the frames the generator
// writes — two builders would let the published document drift away from the
// checked one, which is the defect this whole split exists to close.
func flowSections(t *testing.T, caps flowCaptures) []flowSection {
	t.Helper()
	// The header hint is gated on $TMUX, and every frame here but one is a screen
	// the operator sees while the hub runs inside tmux — which is the only case §20
	// is about. The exception clears it again and says why.
	t.Setenv("TMUX", hubTMUX)

	var secs []flowSection

	// ---- 1. Answer an agent on the hub's own server -------------------------
	{
		var sh []flowShot
		m := base(t, 120, 24, fleet20()...)
		m = m.cursorTo(0)
		sh = append(sh, flowShot{
			title: "1. Курсор на агенте, который ждёт ответа", origin: originReal,
			did:    "хаб запущен внутри tmux, ничего не выбрано",
			assert: "шапка несёт подсказку про вложенность, потому что $TMUX выставлен",
			want:   "nested: leave an attached session with C-b C-b d",
			body:   m.View(),
		})
		sh = addCapture(t, sh, caps, "cap-01-hub.txt", flowShot{
			title:  "2. Статусбар tmux до нажатия",
			did:    "ничего; это то, что видно поверх хаба",
			assert: "левый сегмент читается [hub], список окон — сессии хаба",
			want:   "[hub] 0:zsh*",
		})
		sh = addCapture(t, sh, caps, "cap-02-work.txt", flowShot{
			title: "3. После a — оператор в панели, хаб жив",
			did: "нажал a; хаб выполнил switch-client -t $1 и select-window -t @1 " +
				"(идентификаторы из живого прогона)",
			assert: "левый сегмент стал [work]: сменилась сессия, ничего не отцеплено",
			want:   "[work] 0:agent*",
		})
		sh = addCapture(t, sh, caps, "cap-03-back.txt", flowShot{
			title:  "4. Возврат — своей клавишей, без участия хаба",
			did:    "C-b L (last-session)",
			assert: "снова [hub]: возврат не требует ни detach, ни хаба",
			want:   "[hub] 0:zsh*",
		})

		back := base(t, 120, 24, fleet20()...)
		back.note = "back from api:review"
		sh = append(sh, flowShot{
			title: "5. Хаб говорит, откуда вернулись", origin: originRealProposed,
			did:    "вернулся в окно хаба",
			assert: "заметка называет, где оператор был; больше хаб ничего про вселение не хранит",
			want:   "back from api:review",
			body:   back.View(),
		})

		// The half-landed jump. switch-client succeeded and select-window did not,
		// so the operator IS in the target session on the wrong window — and the
		// first version of this path said "cannot go there", which is false in the
		// direction that costs the operator their bearings.
		half := base(t, 120, 24, fleet20()...)
		half.note = "moved into api:review, but select-window: can't find window @4"
		sh = append(sh, flowShot{
			title: "6. Прыжок дошёл наполовину", origin: originReal,
			did: "окно цели убили между опросом и нажатием: switch-client прошёл, " +
				"select-window нет",
			assert: "заметка признаёт, что оператор ПЕРЕЕХАЛ, и называет, что не удалось — " +
				"а не отрицает переезд. Достижимо потому, что window_id не входил в проверку " +
				"соединения, так что сервер, не отдающий его, проходил проверку и каждый " +
				"прыжок садился на текущее окно",
			want: "moved into api:review, but",
			body: half.View(),
		})

		grew := base(t, 120, 24, append(fleet20(),
			agentPane("local", "api", "docs", "%2", 2, state.Works, "  Writing docs/…"))...)
		sh = append(sh, flowShot{
			title: "6. Пока оператора не было, хаб опрашивал", origin: originReal,
			did:    "во время отлучки на хосте появилась пятая панель",
			assert: "счётчик в шапке вырос сам: 4 sessions → 5 sessions, без действий оператора",
			want:   "tmux-hub  5 sessions",
			body:   grew.View(),
		})

		secs = append(secs, flowSection{
			name: "Ответить агенту на своём сервере",
			intro: "Главный случай и весь смысл §20. Сейчас a отдаёт терминал: хаб блокируется и " +
				"перестаёт опрашивать, а C-b d при вложенности выкидывает из ХАБА. После — " +
				"оператор оказывается в настоящей панели, хаб продолжает работать в своём окне, " +
				"и возврат это одна клавиша tmux.",
			shots: sh,
		})
	}

	// ---- 2. Another server: a window, not a takeover ------------------------
	{
		var sh []flowShot
		m := base(t, 120, 24, fleet20()...)
		m = m.cursorTo(1)
		sh = append(sh, flowShot{
			title: "1. Курсор на панели другого сервера", origin: originReal,
			did:    "j до строки nuc/deploy",
			assert: "строка remote-хоста ничем не помечена как «вселяемая» — §20 не добавляет колонку",
			want:   "NUC DEPLOY",
			body:   m.View(),
		})
		sh = addCapture(t, sh, caps, "cap-04-other-server.txt", flowShot{
			title: "2. После a — два статусбара, и это ответ на «где я»",
			did: "нажал a; хаб выполнил new-window -t $S -n <label> с тем же attach, " +
				"что и раньше",
			assert: "внизу статус ХАБА с новым окном (1:other*), над ним — собственный статус " +
				"другого сервера с его именем хоста. «Другая сессия» и «другая машина» " +
				"различимы без единого нарисованного хабом пикселя",
			want: "[ag] 0:sh*",
		})
		sh = addCapture(t, sh, caps, "cap-05-after-close.txt", flowShot{
			title: "3. Закрыть это окно безопасно",
			did: "C-b & на окне other. Измерено в том же прогоне: pane_pid на другом сервере " +
				"до и после совпадает, клиентов стало 0",
			assert: "окно исчезло, агент на другом сервере жив. Та же клавиша по " +
				"link-window-копии убивает агента — поэтому link-window запрещён",
			want: "[hub] 0:zsh*",
		})
		sh = addCapture(t, sh, caps, "cap-06-payload-died.txt", flowShot{
			title: "4. Payload умер — окно остаётся ЖИВЫМ и говорит, почему",
			did: "a на хосте, чей ssh-мастер мёртв: контрольный сокет отсутствует, " +
				"ssh выходит со статусом 255",
			assert: "окно НЕ исчезает, панель ЖИВА (pane_dead=0) и на экране собственные слова " +
				"ssh плюс статус выхода. До этого окно пропадало, а хаб писал «back from " +
				"api:review» — сорванный прыжок читался как удавшийся; первая попытка починки " +
				"ставила remain-on-exit ПОСЛЕ new-window и проигрывала гонку быстрому payload " +
				"(измерено: `false` терялся в 6 из 12 попыток), а когда выигрывала — оставляла " +
				"мёртвую панель, чей видимый экран несёт только баннер tmux, тогда как строка " +
				"ssh уезжает в историю, куда не смотрит ни оператор, ни capture-pane",
			want: "press enter to close this window",
		})

		secs = append(secs, flowSection{
			name: "Другой сервер: окно, а не захват терминала",
			intro: "Удалённую панель нельзя достать switch-client — это другой tmux-сервер. " +
				"Тот же argv attach переиспользуется ПОЭЛЕМЕНТНО, меняется только его " +
				"контейнер: он уходит в новое окно сессии хаба. Каждый элемент при этом " +
				"берётся в кавычки, потому что последний аргумент tmux отдаёт шеллу — " +
				"измерено: без кавычек attach -t $3 доезжает как attach -t, а -t $10 как " +
				"голый 0, то есть сессия с ИМЕНЕМ 0. Сам argv при этом едет внутри " +
				"sh -c '<argv>; s=$?; printf …; read _' — то есть в панели остаётся ЖИВОЙ шелл " +
				"после того, как attach вернулся: new-window выходит с нулём независимо от " +
				"того, что сделает payload, поэтому мёртвый ssh-мастер иначе закрывал окно " +
				"вместе со своим сообщением, а хаб говорил «back from …». Никакой опции на " +
				"окне не ставится: remain-on-exit пришлось бы ставить ПОСЛЕ new-window, а " +
				"именно new-window запускает payload — это гонка, и вдобавок она отвечала бы " +
				"мёртвой панелью на то самое нажатие enter, которым окно закрывается. " +
				"Возврат становится " +
				"переключением окна, а не отцеплением — это снимает ту самую занозу, " +
				"из-за которой сегодня нужен C-b C-b d; сам внутренний tmux по-прежнему " +
				"вложен, и отцепить именно его — это C-b C-b d, что и говорит подсказка " +
				"на этом пути.",
			shots: sh,
		})
	}

	// ---- 3. The two refusals, unchanged -------------------------------------
	{
		var sh []flowShot
		noCtl := hosts2()
		noCtl[1].SSHDest, noCtl[1].ControlPath = "nuc", ""
		m := base(t, 120, 24, fleet20()...)
		m.hosts = noCtl
		m = m.cursorTo(1)
		// cursorRow, not panes[i]: the caption claims the note is about the row under the
		// cursor, and that is the one function that answers it.
		cursorPane, ok := m.cursorRow()
		if !ok {
			t.Fatal("no row under the cursor")
		}
		m.note = noteFromAttach(t, cursorPane, noCtl[1])
		sh = append(sh, flowShot{
			title: "1. Хост без ctl= — отказ называет недостающее поле", origin: originReal,
			did: "a на панели хоста, у которого нет ssh-контрольного сокета",
			assert: "текст взят из AttachCmd, а не переписан: он называет ИМЕННО то поле, " +
				"которого нет",
			want: "has no ssh control path",
			body: m.View(),
		})

		agentRow := registry.Pane{
			Kind: registry.KindAgent, Host: "local", Session: "api",
			ClaudeSession:   "7007b23f-1599-4efa-81c5-4195621cc273",
			ClassifiedState: state.Quiet, Content: []string{"  (no pane)"},
		}
		m2 := base(t, 120, 24, append(fleet20(), agentRow)...)
		// Not len-1: base() sorts by attention, and a quiet agent row lands above
		// the two working panes. Assuming "appended last is displayed last" put the
		// cursor on an unrelated row while the caption said otherwise.
		for i, p := range m2.panes {
			if p.Kind == registry.KindAgent {
				m2 = m2.cursorTo(i)
			}
		}
		m2.note = noteFromAttach(t, agentRow, hosts2()[0])
		sh = append(sh, flowShot{
			title:  "2. Строка агента без панели — отказ объясняет, почему это норма",
			origin: originReal,
			did:    "a на строке, пришедшей из claude agents --json",
			assert: "отказ говорит, что у большинства сессий Claude панели нет вообще — " +
				"это не поломка",
			want: "there is nothing to attach to until it runs in one",
			body: m2.View(),
		})

		secs = append(secs, flowSection{
			name: "Два отказа — не меняются",
			intro: "Обе строки уже существуют и уже несут исправление. §20 их не трогает: " +
				"дописывается только выбор пути, а не новые сообщения.",
			shots: sh,
		})
	}

	// ---- 4. The only new UI: the hint becomes path-specific -----------------
	{
		var sh []flowShot
		// Two of these three frames were DRAWN before §20 existed and are now
		// regenerated from the renderer with their assertions carried over — which is
		// what docs/mockup-authoring.md rule 6 says a target frame is for. The third
		// is not: see its own comment.
		//
		// The jump path needs the pane's Epoch to equal the hub's own server's,
		// because locality is an equality of what each server reports rather than
		// of a socket path (§20). fleet20 leaves Epoch empty, so the local panes
		// get one here and the hub is told the same value.
		withEpoch := func(ps []registry.Pane) []registry.Pane {
			for i := range ps {
				if ps[i].Host == "local" {
					ps[i].Epoch = fakeEpoch
				} else {
					ps[i].Epoch = "9999:1786400000"
				}
			}
			return ps
		}
		mk := func(cursor int) model {
			m := base(t, 120, 24, withEpoch(fleet20())...)
			m = m.cursorTo(cursor)
			m.selfSession, m.selfEpoch = hubSession, fakeEpoch
			return m
		}

		sh = append(sh, flowShot{
			title: "1. Курсор на своём сервере — путь «прыжок»", origin: originReal,
			did: "то же состояние, что кадр 1 первого потока",
			assert: "подсказка называет возврат ДЛЯ ЭТОГО пути (C-b L) и не упоминает detach, " +
				"потому что отцеплять нечего",
			want: "C-b L comes back",
			body: mk(0).View(),
		})
		sh = append(sh, flowShot{
			title: "2. Курсор на другом сервере — путь «окно»", origin: originReal,
			did: "j на строку nuc/deploy",
			assert: "та же подсказка сменилась: у вложенного tmux выход через C-b C-b d, " +
				"и здесь сегодняшняя фраза остаётся верной — в отличие от прыжка",
			want: "C-b C-b d leaves the inner tmux",
			body: mk(1).View(),
		})

		// This frame's caption says the hub is NOT inside tmux, so $TMUX has to be
		// cleared for it — and that changes what it can assert.
		//
		// It used to keep the generator's $TMUX set while leaving model.selfSession
		// empty, and that is a state production cannot produce: with $TMUX set,
		// hub.SelfSessionID() answers `$0` and never "". Its assertion passed only
		// because of that contradiction. Genuinely outside tmux there is no hint AT
		// ALL — hintFor returns "" when !Nested(), because there is no outer session
		// to be thrown out of — so the honest promise is an ABSENCE, and the `want`
		// beside it is the discriminator that stops an empty frame satisfying it.
		t.Setenv("TMUX", "")
		outside := base(t, 120, 24, withEpoch(fleet20())...)
		outside = outside.cursorTo(0)
		sh = append(sh, flowShot{
			title: "3. Хаб не внутри tmux — путь «полный экран»", origin: originReal,
			did: "$TMUX не выставлен: хаб запущен не из tmux, и своей сессии у него нет — " +
				"именно это и производит production в таком случае",
			assert: "подсказки про возврат НЕТ вообще: этот путь забирает терминал, а " +
				"возвращаться некуда — нет внешней сессии, из которой могли бы выкинуть. " +
				"Кадр при этом настоящий дашборд, а не пустой экран: счётчик сессий на месте",
			want: "tmux-hub  4 sessions",
			deny: "C-b",
			body: outside.View(),
		})
		t.Setenv("TMUX", hubTMUX)

		secs = append(secs, flowSection{
			name: "Единственный новый UI: подсказка становится путезависимой",
			intro: "§20 намеренно почти не добавляет интерфейса — тяжёлую работу делает tmux. " +
				"Из видимого меняются три вещи: строка a в таблице клавиш §16, заметка возврата " +
				"и вот эта подсказка. Все три кадра здесь были НАРИСОВАНЫ, пока функции не было; " +
				"теперь они перегенерированы настоящим рендерером. Утверждения первых двух " +
				"перенесены без изменений; у третьего оно ИСПРАВЛЕНО — нарисованная версия " +
				"показывала подсказку у хаба вне tmux, а вне tmux её не бывает, и кадр " +
				"проходил проверку только за счёт этого противоречия.",
			shots: sh,
		})
	}

	return secs
}

// checkFrames verifies that every frame shows what its caption promises, and
// returns how many it checked. A `drawn` frame is exempt by definition — nothing
// exists yet to check it against — and there are none left.
func checkFrames(t *testing.T, secs []flowSection) int {
	t.Helper()
	checked := 0
	for _, s := range secs {
		for _, sh := range s.shots {
			if sh.origin == originDrawn {
				continue
			}
			if sh.want == "" {
				t.Fatalf("%s / %s: non-drawn frame with no checkable substring", s.name, sh.title)
			}
			if !strings.Contains(sh.body, sh.want) {
				t.Fatalf("%s / %s: frame does not contain %q, so its assertion is false",
					s.name, sh.title, sh.want)
			}
			if sh.deny != "" && strings.Contains(sh.body, sh.deny) {
				t.Fatalf("%s / %s: frame contains %q, which its assertion says is absent",
					s.name, sh.title, sh.deny)
			}
			checked++
		}
	}
	return checked
}

// The frames the renderer builds must keep their promises, and this is the gate
// that says so on every `go test ./...`.
//
// The floor is what makes the count mean something: `checked` counting up from
// zero would satisfy any assertion here, including one that silently stopped
// looking at anything. Nine of the document's fourteen frames come out of View()
// and need nothing but the product; the other five are `capture-pane` from a live
// nested tmux and are checked by the generator, which builds these same sections
// with a real capture reader.
func TestFlowFramesAssertWhatTheyShow(t *testing.T) {
	const wantView = 9
	got := checkFrames(t, flowSections(t, noCaptures))
	if got < wantView {
		t.Errorf("checked %d frames, want at least %d — a frame that stopped being "+
			"checked is a picture in a published document promising something nobody verifies",
			got, wantView)
	}
}
