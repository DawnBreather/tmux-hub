//go:build mockup

// This file is a GENERATOR, not a test of behaviour. It builds model states,
// captures what View() really prints, and writes docs/ui-mockup.html so the
// whole interface can be reviewed at once.
//
//	go test -tags mockup -run TestGenerateMockup ./internal/ui/
//
// Every screen in the output is the renderer's own bytes. Nothing here is drawn
// by hand — a mockup that invents its screens would let us approve a layout the
// program does not produce, which is the same defect as a test that asserts on a
// render helper nothing calls.
package ui

import (
	"fmt"
	"html"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/DawnBreather/tmux-hub/internal/fav"
	"github.com/DawnBreather/tmux-hub/internal/fleetcache"
	"github.com/DawnBreather/tmux-hub/internal/project"
	"time"

	"github.com/DawnBreather/tmux-hub/internal/broadcast"
	"github.com/DawnBreather/tmux-hub/internal/history"
	"github.com/DawnBreather/tmux-hub/internal/hostset"
	"github.com/DawnBreather/tmux-hub/internal/hub"
	"github.com/DawnBreather/tmux-hub/internal/registry"
	"github.com/DawnBreather/tmux-hub/internal/state"
)

// The screen fixtures — agentPane, toolPane, hosts2, base — live in
// fixtures_test.go, which carries no build tag. They had to leave this file: an
// assertion that needs one of them to build a screen was reachable only under
// `-tags mockup`, and flows_test.go's frame assertions were behind that tag AND an
// env var, so nothing ran them.
//
// `shot` and `scene` left for the same reason, to mockup_frames_test.go: a scene
// declared under this tag makes every builder that returns one unreachable from the
// default suite, so the picker's frames could carry an assertion nothing ran. That
// file also holds the check, and the `want`/`deny` fields the check reads.

// fleet is the cast used by most scenes: four agents across two hosts, a build
// watcher and a log tail as the noise, in a spread of states.
func mockupFleet() []registry.Pane {
	return []registry.Pane{
		agentPane("local", "api", "review", "%0", 0, state.Needs,
			"  Do you want to proceed?", "  ❯ 1. Yes", "    2. No"),
		agentPane("local", "api", "fix", "%1", 1, state.Works,
			"  Reading internal/tmux/batch.go…", "  esc to interrupt"),
		toolPane("local", "api", "watch", "%2", "make", 2, state.Works, "  ok  internal/tmux  6.4s"),
		toolPane("local", "ops", "logs", "%3", "tail", 3, state.Idle, "  10.0.0.4 GET /healthz 200"),
		agentPane("nuc", "deploy", "migrate", "%4", 0, state.Quiet,
			"  Applied 3 migrations.", "  ❯ "),
		agentPane("nuc", "deploy", "audit", "%5", 1, state.Works, "  Grepping for call sites…"),
	}
}

// mockupNow is the instant every scene is stamped at, and it is FIXED.
//
// This document is meant to be diffable: the way to check that a refactor moved no frame is
// to regenerate it and see an empty diff. With time.Now() every regeneration differed in six
// timestamp lines, so the check could not distinguish "nothing changed" from "something did"
// — measured while doing exactly that, where the only lines that moved were the stamps.
var mockupNow = time.Date(2026, 8, 15, 19, 30, 1, 0, time.Local)

func TestGenerateMockup(t *testing.T) {
	var scenes []scene

	// ---- 1. The dashboard at three widths -----------------------------------
	{
		var sh []shot
		for _, w := range []int{80, 120, 190} {
			m := base(t, w, 24, mockupFleet()...)
			m.note = ""
			sh = append(sh, shot{
				title: fmt.Sprintf("Дашборд, %d колонок — %s", w, LayoutFor(w)),
				did:   "хаб только запущен, ничего не выбрано",
				look: "читается ли состояние фронта одним взглядом: кто ждёт ответа, кто работает, " +
					"кто затих. Хватает ли 80 колонок, и что даёт лишняя ширина.",
				w: w, h: 24, screen: m.View(),
			})
		}
		scenes = append(scenes, scene{
			name: "Дашборд",
			intro: "Одна и та же группа панелей на трёх ширинах. Раскладка выбирается сама: " +
				"до 100 колонок только инбокс, до 160 инбокс и одна плитка, дальше сетка.",
			shots: sh,
		})
	}

	// ---- 2. Broadcast, the happy path ---------------------------------------
	{
		var sh []shot
		m := base(t, 120, 24, mockupFleet()...)
		sh = append(sh, shot{title: "1. Ничего не выбрано", did: "исходное состояние",
			look: "видно ли, что делать дальше, без подсказки", w: 120, h: 24, screen: m.View()})

		m.sel.Toggle(SelectionKey{Host: "local", PaneID: "%1"})
		m.sel.Toggle(SelectionKey{Host: "nuc", PaneID: "%5"})
		m = m.cursorTo(1)
		sh = append(sh, shot{title: "2. Выбраны две панели на разных хостах",
			did: "space на двух строках", look: "заметна ли отметка выбора и счётчик", w: 120, h: 24, screen: m.View()})

		m.mode = modeCompose
		sh = append(sh, shot{title: "3. Ящик ввода, пустой", did: "нажал i",
			look: "ясно ли, куда уйдёт текст и сколько цели", w: 120, h: 24, screen: m.View()})

		m.composer.Insert("прочитай docs/design.md §18 и скажи, где ключ скрытия\nдержится на позиции")
		sh = append(sh, shot{title: "4. Набранный многострочный промпт", did: "печатал, alt+enter для новой строки",
			look: "видно ли, что enter отправит, а не добавит строку", w: 120, h: 24, screen: m.View()})

		// pendingText is what the dialog will SEND, and composeKey sets it from the draft when
		// enter is pressed. These scenes set the state by hand, so they must set it too — without
		// it the dialog draws with no payload, which is a screen the program cannot produce.
		m.mode, m.pendingAct, m.pendingText = modeConfirm, actionSend, m.composer.Text()
		m.pending = []broadcast.Reason{broadcast.ReasonMultiple}
		sh = append(sh, shot{title: "5. Подтверждение: больше одной цели", did: "нажал enter",
			look: "названы ли цели и причина; понятно ли, что подтверждаю", w: 120, h: 24, screen: m.View()})

		m2 := base(t, 120, 24, mockupFleet()...)
		m2.note = "отправлено в 2 панели · подтверждено на экране"
		sh = append(sh, shot{title: "6. Отправлено", did: "подтвердил",
			look: "говорит ли итог, что текст реально доехал, а не просто был послан",
			w:    120, h: 24, screen: m2.View()})

		scenes = append(scenes, scene{name: "Рассылка промпта",
			intro: "Главный сценарий: выбрать несколько агентов и отправить им один промпт. " +
				"Правило проекта — писать можно только в то, что видно на экране.",
			shots: sh})
	}

	// ---- 3. Removing the noise ----------------------------------------------
	{
		var sh []shot
		m := base(t, 120, 24, mockupFleet()...)
		sh = append(sh, shot{title: "1. Шум в списке", did: "исходное: make и tail не агенты и никогда ими не станут",
			look: "сколько внимания забирают строки, которые никогда не потребуют ответа", w: 120, h: 24, screen: m.View()})

		noise := m.panes[2]
		if err := m.hidden.Toggle(noise); err != nil {
			t.Fatal(err)
		}
		logs := m.panes[3]
		if err := m.hidden.Toggle(logs); err != nil {
			t.Fatal(err)
		}
		sh = append(sh, shot{title: "2. Две панели скрыты", did: "x на каждой",
			look: "видно ли в подвале, что они скрыты, а не пропали", w: 120, h: 24, screen: m.View()})

		m.panes[3].ClassifiedState = state.Needs
		sh = append(sh, shot{title: "3. Скрытая панель просит ввода — и всплыла",
			did:  "агент в скрытой панели задал вопрос",
			look: "понятно ли, почему строка вернулась; заметна ли пометка [↑]", w: 120, h: 24, screen: m.View()})

		m.showHidden = true
		sh = append(sh, shot{title: "4. Показать скрытое", did: "нажал X",
			look: "отличимы ли скрытые от обычных, пока они показаны", w: 120, h: 24, screen: m.View()})

		scenes = append(scenes, scene{name: "Убрать шум",
			intro: "Хост копит панели, которые не агенты: сборка, логи, htop. Скрытие постоянное — " +
				"«больше не показывай», а не «не сейчас». Одно правило: панель, которая ждёт ответа, " +
				"всплывает сама.",
			shots: sh})
	}

	// ---- 4. Launching an agent ----------------------------------------------
	{
		var sh []shot
		m := base(t, 120, 24, mockupFleet()...)
		m.mode = modeLaunch
		m = m.cursorTo(0)
		m = m.openLaunchForm()
		sh = append(sh, shot{title: "1. Форма запуска: каталог строки под курсором уже подставлен",
			did:  "нажал n",
			look: "видно ли все пять решений сразу и какое из них требует ввода", w: 120, h: 24, screen: m.View()})

		// THE PRE-FILL, on a fleet that has a cwd — `mockupFleet()` sets none, so the scene above shows the
		// empty case and this one shows the case an operator actually meets. Without it the feature is
		// invisible in this document and the next refactor can drop it without moving a frame.
		withPath := agentPane("nuc", "api", "logs", "%9", 9, state.Quiet, "  ❯ ")
		withPath.Path = "/home/dev/lab/streams/orbits/billing-iac"
		pre := base(t, 120, 24, withPath)
		pre = pre.cursorTo(0)
		pre = pre.openLaunchForm()
		sh = append(sh, shot{title: "2. Тот же экран, когда у строки есть каталог",
			did: "нажал n на строке из проекта billing-iac", w: 120, h: 24, screen: pre.View(),
			look: "подставлен ли путь и названа ли клавиша, которой его стирают"})

		m.launchForm.focused = 1
		m.launchForm.dirInput.Clear()
		m.launchForm.dirInput.Insert("/home/dev/lab/streams/experiments/tmux-hub")
		m.launchForm.spec.CWD = m.launchForm.dirInput.Text()
		sh = append(sh, shot{title: "2. Введён каталог", did: "tab на поле каталога, напечатал путь",
			look: "понятно ли, где фокус", w: 120, h: 24, screen: m.View()})

		m.launchForm.dirInput.Clear()
		m.launchForm.dirInput.Insert("/home/dev/lab/typo")
		m.launchForm.spec.CWD = m.launchForm.dirInput.Text()
		m.launchForm.err = "directory does not exist: /home/dev/lab/typo"
		sh = append(sh, shot{title: "3. Каталога нет", did: "enter с опечаткой в пути",
			look: "несёт ли сообщение ИСПРАВЛЕНИЕ, а не только факт поломки. " +
				"tmux такой путь принял бы молча и запустил агента в $HOME",
			w: 120, h: 24, screen: m.View()})

		m.launchForm.err = ""
		m.launchForm.focused, m.launchForm.modelIndex, m.launchForm.permIndex = 2, 1, 3
		m.launchForm.spec.Model, m.launchForm.spec.PermissionMode = "sonnet", "acceptEdits"
		sh = append(sh, shot{title: "4. Выбор модели и режима прав", did: "tab, стрелки влево-вправо",
			look: "понятно ли, что это перебор из закрытого набора, а не ввод", w: 120, h: 24, screen: m.View()})

		scenes = append(scenes, scene{name: "Запустить агента",
			intro: "Хаб сам придумывает uuid сессии и передаёт его claude, поэтому у созданной панели " +
				"личность известна с рождения. Полная форма каждый раз, без профилей.",
			shots: sh})
	}

	// ---- 5. Restart and kill ------------------------------------------------
	{
		var sh []shot
		m := base(t, 120, 24, mockupFleet()...)
		m = m.cursorTo(1)
		m.sel.Toggle(SelectionKey{Host: "local", PaneID: "%1"})
		m.mode, m.pendingAct = modeConfirm, actionKill
		m.pending = []broadcast.Reason{broadcast.ReasonAgentRunning}
		sh = append(sh, shot{title: "1. K по живому агенту", did: "выбрал панель с агентом, нажал K",
			look: "названа ли ЖЕРТВА и сказано ли, что в ней работает агент", w: 120, h: 24, screen: m.View()})

		dead := mockupFleet()
		dead[1].Dead, dead[1].DeadStatus, dead[1].ClassifiedState = true, 7, state.Error
		dead[1].Content = []string{"Pane is dead (status 7, Wed Aug 12 13:21:15 2026)"}
		m2 := base(t, 120, 24, dead...)
		m2 = m2.cursorTo(1)
		m2.sel.Toggle(SelectionKey{Host: "local", PaneID: "%1"})
		m2.mode, m2.pendingAct = modeConfirm, actionKill
		m2.pending = []broadcast.Reason{broadcast.ReasonPaneDead}
		sh = append(sh, shot{title: "2. K по мёртвой панели", did: "агент вышел с кодом 7, нажал K",
			look: "та же привычка подтверждения — но без намёка, что рискую работой", w: 120, h: 24, screen: m2.View()})

		m3 := base(t, 120, 24, dead...)
		m3.note = "рестарт: claude --resume 7007b23f-1599-4efa-81c5-4195621cc273 · разговор сохранён"
		sh = append(sh, shot{title: "3. Рестарт с непрерывностью", did: "нажал R",
			look: "видно ли, что разговор продолжится, а не начнётся заново", w: 120, h: 24, screen: m3.View()})

		scenes = append(scenes, scene{name: "Рестарт и убийство",
			intro: "Разрушающее действие всегда подтверждается, и подтверждение называет, что именно " +
				"погибнет. Рестарт идёт через --resume, поэтому разговор не теряется.",
			shots: sh})
	}

	// ---- 6. History ---------------------------------------------------------
	{
		var sh []shot
		m := base(t, 120, 24, mockupFleet()...)
		now := mockupNow
		m.history = []history.Entry{
			{At: now.Add(-2 * time.Minute), Host: "local", PaneID: "%1", SessionName: "api", WindowName: "fix",
				Text: "прочитай §18 и скажи, где ключ держится на позиции", Outcome: "delivered", Submitted: true},
			{At: now.Add(-19 * time.Minute), Host: "nuc", PaneID: "%5", SessionName: "deploy", WindowName: "audit",
				Text: "перепроверь список вызовов", Outcome: "sent-unwitnessed",
				Reason: "экранный свидетель не сработал на коротком тексте", Submitted: false},
			{At: now.Add(-46 * time.Minute), Host: "local", PaneID: "%0", SessionName: "api", WindowName: "review",
				Text: "да", Outcome: "refused", Reason: "токен панели не совпал", Submitted: false},
		}
		m.mode = modeHistory
		sh = append(sh, shot{title: "1. История отправок", did: "нажал h",
			look: "различимы ли исходы: доехало, доехало но не подтверждено, отказано", w: 120, h: 24, screen: m.View()})

		m.histCursor = 1
		sh = append(sh, shot{title: "2. Курсор на неподтверждённой", did: "j",
			look: "видна ли причина, по которой исход не «доехало»", w: 120, h: 24, screen: m.View()})

		// The entry under the cursor is the payload, which is what `r` puts there — and it does
		// NOT go through the composer, so a cancelled re-send cannot eat a draft.
		m.mode, m.pendingAct, m.fromHistory = modeConfirm, actionSend, true
		m.pendingText = m.history[m.histCursor].Text
		m.sel.Toggle(SelectionKey{Host: "local", PaneID: "%1"})
		m.pending = []broadcast.Reason{broadcast.ReasonFromHistory}
		sh = append(sh, shot{title: "3. Переотправка всегда спрашивает", did: "r на записи",
			look: "сказано ли, что текст пришёл из истории, а не набран сейчас", w: 120, h: 24, screen: m.View()})

		scenes = append(scenes, scene{name: "История и переотправка",
			intro: "Каждая отправка записана с исходом. Переотправка целится в ТЕКУЩИЙ выбор, " +
				"а не в панель из записи, поэтому спрашивает всегда.",
			shots: sh})
	}

	// ---- 7. Degraded and edges ---------------------------------------------
	{
		var sh []shot
		down := hosts2()
		down[1].Status, down[1].Reason = hub.Down, "dial /run/user/1000/nuc.sock: connect: connection refused"
		st := mockupFleet()
		for i := range st {
			if st[i].Host == "nuc" {
				st[i].Stale, st[i].StaleSince = true, mockupNow.Add(-4*time.Minute)
			}
		}
		m := base(t, 120, 24, st...)
		m.hosts = down
		sh = append(sh, shot{title: "1. Хост упал", did: "ssh-форвард умер",
			look: "остались ли его панели в списке помеченными, вместо тихого исчезновения; " +
				"названа ли причина", w: 120, h: 24, screen: m.View()})

		ro := hosts2()
		ro[1].SSHDest, ro[1].ControlPath = "", ""
		m2 := base(t, 120, 24, mockupFleet()...)
		m2.hosts = ro
		m2 = m2.cursorTo(4)
		m2.note = "хост nuc без ssh= — читать можно, attach и проверку каталога нельзя"
		sh = append(sh, shot{title: "2. Удалённый хост только для чтения", did: "выбрал панель на хосте без ssh=",
			look: "честно ли сказано, ЧТО именно нельзя и почему", w: 120, h: 24, screen: m2.View()})

		dead := mockupFleet()
		dead[4].Dead, dead[4].DeadStatus, dead[4].ClassifiedState = true, 7, state.Error
		dead[4].Content = []string{"Pane is dead (status 7, Wed Aug 12 13:21:15 2026)"}
		dead[5].Dead, dead[5].DeadStatus, dead[5].ClassifiedState = true, 0, state.Error
		dead[5].Content = []string{"Pane is dead (status 0, Wed Aug 12 13:22:02 2026)"}
		m3 := base(t, 120, 24, dead...)
		sh = append(sh, shot{title: "3. Мёртвые панели с кодом выхода", did: "два агента вышли: один с 7, другой с 0",
			look: "различимы ли «упал» и «закончил»; читается ли exited 0 как отказ", w: 120, h: 24, screen: m3.View()})

		m4 := base(t, 120, 24, mockupFleet()...)
		m4.note = "select a pane with space first — a prompt needs a target"
		sh = append(sh, shot{title: "4. i без выбора", did: "нажал i, ничего не выбрав",
			look: "несёт ли отказ исправление, а не только «нельзя»", w: 120, h: 24, screen: m4.View()})

		m5 := base(t, 120, 24)
		sh = append(sh, shot{title: "5. Ни одной панели", did: "tmux запущен, но пуст",
			look: "понятно ли, что делать; не выглядит ли как поломка", w: 120, h: 24, screen: m5.View()})

		m6 := base(t, 40, 6, mockupFleet()...)
		sh = append(sh, shot{title: "6. Слишком узкий терминал", did: "сжал окно до 40×6",
			look: "деградирует честно или ломается", w: 40, h: 6, screen: m6.View()})

		// ОДИН ПРОИЗВОДИТЕЛЬ УПАЛ, ВТОРОЙ ОТВЕЧАЕТ — и до этого кадра такого в документе не было.
		// Поллинг tmux у хоста не отвечает (все его строки помечены), но листинг claude по-прежнему
		// называет одну из его сессий работающей и даёт pid. Это тот самый экран, с которого пришёл
		// отчёт «сессия показывается дважды, и статус мигает»: пока строка со свежим фактом не
		// сворачивалась в панельную, сессия рисовалась двумя строками — `works` от листинга и
		// `stale` от панели.
		//
		// `AgentSeenAt` берётся от ЧАСОВ, а не от `mockupNow`, и документ от этого не перестаёт быть
		// побайтово воспроизводимым: до кадра доходит только БУЛЕВО «факт свежий», а `mockupNow`
		// пятидневной давности сделал бы его просроченным и кадр показывал бы ровно то, что было до
		// правки. Ни одной метки времени эта строка не печатает.
		split := mockupFleet()
		named := false
		for i := range split {
			if split[i].Host != "nuc" {
				continue
			}
			split[i].Stale, split[i].StaleSince = true, mockupNow.Add(-4*time.Minute)
			if !named {
				split[i].AgentState, split[i].AgentWord = state.Works, "working"
				split[i].AgentPID, split[i].AgentSeenAt = 480403, time.Now()
				named = true
			}
		}
		if !named {
			t.Fatal("ни одна строка фикстуры не на хосте nuc — сцена проверяла бы пустоту")
		}
		m7 := base(t, 120, 24, split...)
		m7.hosts = down
		sh = append(sh, shot{title: "7. Хост не отвечает, а листинг отвечает",
			did:  "tmux у nuc замолчал, claude agents по-прежнему называет одну сессию",
			look: "одна ли строка у сессии, и говорит ли она то, что известно лучше всего",
			w:    120, h: 24, screen: m7.View(),
			want: []string{"works", "stale"}})

		scenes = append(scenes, scene{name: "Деградация и края",
			intro: "Состояния, в которых интерфейс обязан оставаться честным: упавший хост, " +
				"хост только для чтения, мёртвые панели, пустой список, тесное окно.",
			shots: sh})
	}

	// ---- 8. First paint, before the network answers -------------------------
	//
	// §16's headline commitment — "a usable dashboard of local sessions is on
	// screen before any network work starts" — had no screen at all, so the one
	// promise the tool is judged on first was the one nobody could look at.
	{
		var s []shot
		coming := hosts2()
		coming[1].Status, coming[1].Version = hub.Connecting, ""
		local := []registry.Pane{
			agentPane("local", "api", "review", "%0", 0, state.Needs,
				"  Do you want to proceed?", "  ❯ 1. Yes", "    2. No"),
			agentPane("local", "api", "fix", "%1", 1, state.Works,
				"  Reading internal/tmux/batch.go…", "  esc to interrupt"),
		}
		m := base(t, 120, 24, local...)
		m.hosts = coming
		s = append(s, shot{title: "1. Первая отрисовка: локальное уже есть, nuc ещё едет",
			did: "хаб запущен секунду назад; локальный сокет ответил за 2 мс, ssh-проба ещё идёт",
			look: "можно ли работать ПРЯМО СЕЙЧАС, не дожидаясь сети; видно ли, что nuc не упал, " +
				"а именно подключается — и не выглядит ли это как поломка",
			w: 120, h: 24, screen: m.View()})

		all := []hub.Host{
			{Label: "local", Socket: "/tmp/tmux-1000/default", Status: hub.Up, Version: "3.7b", LocalProc: true},
			{Label: "nuc", Socket: "/run/user/1000/nuc.sock", Status: hub.Connecting, SSHDest: "nuc"},
			{Label: "st", Socket: "/run/user/1000/st.sock", Status: hub.UpEmpty, SSHDest: "st",
				ControlPath: "/home/dev/.ssh/cm-st"},
			{Label: "old", Socket: "/run/user/1000/old.sock", Status: hub.DegradedFormat,
				Reason: "window_activity came back empty on this host", SSHDest: "old"},
			{Label: "dead", Socket: "/run/user/1000/dead.sock", Status: hub.Down,
				Reason: "dial /run/user/1000/dead.sock: connect: connection refused", SSHDest: "dead"},
		}
		m2 := base(t, 120, 24, local...)
		m2.hosts = all
		s = append(s, shot{title: "2. Все пять статусов хоста в одной строке",
			did: "пять хостов в разных состояниях: up, connecting, up-empty, degraded:format, down",
			look: "каждый ли статус — ПОЛОЖИТЕЛЬНОЕ утверждение с причиной, а не отсутствие ошибки; " +
				"влезают ли пять хостов в 120 колонок и что обрезается первым",
			w: 120, h: 24, screen: m2.View()})

		m3 := base(t, 80, 24, local...)
		m3.hosts = all
		s = append(s, shot{title: "3. Те же пять на 80 колонках",
			did:  "тот же список в терминале, который держим по §16",
			look: "что остаётся от причин отказа, когда ширины нет",
			w:    80, h: 24, screen: m3.View()})

		scenes = append(scenes, scene{name: "Первая отрисовка и статусы хостов",
			intro: "§16 обещает работоспособный дашборд до любой сетевой работы: локальный сокет " +
				"отвечает за 2 мс, десять ssh-хостов конкурентно — 7.65 с. Ни одного экрана на это " +
				"обещание не было, как и на три статуса из пяти.",
			shots: s})
	}

	// ---- 9. Every confirmation reason ---------------------------------------
	//
	// Ten reasons exist and four had a screen. §12 records that four of seven
	// reasons were once unreachable in production because the producer filled 3
	// fields of 11 — invisible precisely because it failed in the safe direction,
	// always asking. A screen per reason is how that stays visible.
	{
		var s []shot
		type row struct {
			title  string
			reason broadcast.Reason
			look   string
		}
		rows := []row{
			{"1. Панель не опознана как агент", broadcast.ReasonUnidentified,
				"сказано ли, что именно неизвестно, и что делать"},
			{"2. Агент вышел, панель осталась", broadcast.ReasonAgentGone,
				"различимо ли это от «панель мертва» — здесь панель жива, а агента в ней нет"},
			{"3. Панель сменила сессию или окно", broadcast.ReasonMoved,
				"понятно ли, что цель уехала ПОСЛЕ выбора, и что подтверждение относится к новому месту"},
			{"4. tmux-сервер перезапустился", broadcast.ReasonEpochChanged,
				"названа ли причина, по которой id панелей больше ничего не значат"},
			{"5. Прошлая отправка не подтвердилась экраном", broadcast.ReasonLastUnwitnessed,
				"видно ли, что это про ПРОШЛУЮ отправку, а не про эту"},
			{"6. Панель не принимает вставку", broadcast.ReasonNoBracketedPaste,
				"сказано ли, чем это грозит — текст уйдёт как нажатия клавиш"},
		}
		for _, r := range rows {
			m := base(t, 120, 24, mockupFleet()...)
			m = m.cursorTo(1)
			m.sel.Toggle(SelectionKey{Host: "local", PaneID: "%1"})
			m.mode, m.pendingAct = modeConfirm, actionSend
			m.pending = []broadcast.Reason{r.reason}
			s = append(s, shot{title: r.title, did: "enter в ящике ввода",
				look: r.look, w: 120, h: 24, screen: m.View()})
		}

		m := base(t, 120, 24, mockupFleet()...)
		m = m.cursorTo(1)
		m.sel.Toggle(SelectionKey{Host: "local", PaneID: "%1"})
		m.sel.Toggle(SelectionKey{Host: "nuc", PaneID: "%5"})
		m.mode, m.pendingAct = modeConfirm, actionSend
		m.pending = []broadcast.Reason{
			broadcast.ReasonMultiple, broadcast.ReasonUnidentified, broadcast.ReasonMoved,
			broadcast.ReasonEpochChanged, broadcast.ReasonLastUnwitnessed,
			broadcast.ReasonNoBracketedPaste, broadcast.ReasonAgentGone,
		}
		s = append(s, shot{title: "7. Семь причин сразу — сколько влезает",
			did: "две цели, у каждой свои сомнения",
			look: "§14 записал, что тело диалога дорастает до ~21 строки и обрезается снизу, " +
				"унося причины, пока enter всё ещё отправляет. Видно ли это здесь",
			w: 120, h: 24, screen: m.View()})

		scenes = append(scenes, scene{name: "Все причины подтверждения",
			intro: "Разрушающее и неоднозначное всегда подтверждается, и подтверждение называет " +
				"причину. Причин десять; экран был у четырёх. Шесть ниже не показывались никогда — " +
				"а §12 помнит, как четыре причины из семи были недостижимы в продукте и это было " +
				"невидимо, потому что отказывало в безопасную сторону.",
			shots: s})
	}

	// ---- 10. Thirty panes on twenty-four rows -------------------------------
	//
	// §16: "The list scrolls to keep the cursor visible. It did not, and with 30
	// panes on a 24-row terminal pressing `j` past the bottom moved a cursor
	// nobody could see." The fix shipped; the screen never existed.
	{
		var s []shot
		var many []registry.Pane
		states := []state.State{state.Works, state.Works, state.Quiet, state.Idle, state.Works}
		for i := 0; i < 30; i++ {
			host, sess := "local", "api"
			if i >= 15 {
				host, sess = "nuc", "deploy"
			}
			p := agentPane(host, sess, fmt.Sprintf("w%02d", i), fmt.Sprintf("%%%d", i), i%5,
				states[i%len(states)], fmt.Sprintf("  step %d of 30…", i))
			if i == 22 {
				p.ClassifiedState = state.Needs
				p.Content = []string{"  Overwrite internal/ui/render.go?", "  ❯ 1. Yes", "    2. No"}
			}
			many = append(many, p)
		}
		m := base(t, 120, 24, many...)
		s = append(s, shot{title: "1. Тридцать панелей, курсор в начале",
			did:  "хост накопил тридцать панелей",
			look: "видно ли, сколько всего, и что список не влезает",
			w:    120, h: 24, screen: m.View()})

		m2 := base(t, 120, 24, many...)
		m2 = m2.cursorTo(27)
		s = append(s, shot{title: "2. Курсор на 28-й строке — список прокручен",
			did: "двадцать семь раз j",
			look: "виден ли курсор ВООБЩЕ (без прокрутки он ушёл бы за экран), и понятно ли, " +
				"что выше есть строки",
			w: 120, h: 24, screen: m2.View()})

		m3 := base(t, 80, 24, many...)
		m3 = m3.cursorTo(27)
		s = append(s, shot{title: "3. То же на 80 колонках",
			did:  "тот же курсор в узкой раскладке, где под плитку остаётся меньше строк",
			look: "остаётся ли плитка сфокусированной панели, когда список забрал вертикаль",
			w:    80, h: 24, screen: m3.View()})

		scenes = append(scenes, scene{name: "Тридцать панелей на 24 строках",
			intro: "Прокрутка списка — исправленный дефект §16: без неё j за нижнюю границу двигал " +
				"курсор, которого не видно. Экрана на это не было ни до, ни после починки.",
			shots: s})
	}

	// ---- 11. The hub as the operator actually sees it -----------------------
	//
	// Every screen above is a hub running OUTSIDE tmux, because that is what a
	// generator gets by default — and it is the rarer case. §16 says the terminal
	// to hold is `live1`, the tmux session this design was written in. Inside tmux
	// the header carries a hint that changes with the row under the cursor (§20),
	// so the line the operator reads on every single frame appeared on none of
	// them.
	{
		var s []shot
		t.Setenv("TMUX", "/tmp/tmux-1000/default,4242,0")
		const selfEpoch = "4242:1786500000"
		withEpoch := func(ps []registry.Pane) []registry.Pane {
			for i := range ps {
				if ps[i].Host == "local" {
					ps[i].Epoch = selfEpoch
				} else {
					ps[i].Epoch = "9999:1786400000"
				}
			}
			return ps
		}
		mk := func(w, cursor int) model {
			m := base(t, w, 24, withEpoch(mockupFleet())...)
			m = m.cursorTo(cursor)
			m.selfSession, m.selfEpoch = "$0", selfEpoch
			return m
		}
		s = append(s, shot{title: "1. Курсор на панели своего сервера — 120 колонок",
			did:  "обычная работа: хаб в своём окне tmux, курсор на локальном агенте",
			look: "говорит ли шапка, что сделает a и чем вернуться, не отнимая места у списка",
			w:    120, h: 24, screen: mk(120, 0).View()})
		s = append(s, shot{title: "2. Курсор на панели другого сервера",
			did:  "j до строки nuc/deploy",
			look: "заметно ли, что подсказка сменилась, и не читается ли это как мусор в шапке",
			w:    120, h: 24, screen: mk(120, 1).View()})
		s = append(s, shot{title: "3. То же на 80×24 — размер, который держим",
			did: "тот же курсор в live1",
			look: "остаётся ли подсказка читаемой, когда её длина сравнима с шириной; " +
				"что обрезается — она или счётчик сессий",
			w: 80, h: 24, screen: mk(80, 0).View()})

		s = append(s, shot{title: "4. Подсказка оконного пути на 80 колонках — она не влезает",
			did: "курсор на nuc/deploy в live1. Полная строка 86 рун при ширине 80",
			look: "обрезается посреди слова («leaves the inne»), но клавиша — единственное, " +
				"что нужно нажать — уцелела. Достаточно ли этого, или подсказку надо " +
				"укоротить до размера, который держим",
			w: 80, h: 24, screen: mk(80, 1).View()})

		s = append(s, shot{title: "5. Для сравнения: тот же экран снаружи tmux",
			did:  "хаб запущен из обычного терминала",
			look: "исчезает ли подсказка целиком (должна: отцеплять нечего) и возвращается ли строка списку",
			w:    120, h: 24, screen: func() string { t.Setenv("TMUX", ""); return mk(120, 0).View() }()})

		scenes = append(scenes, scene{name: "Хаб внутри tmux — то, что видно всегда",
			intro: "Хаб почти всегда запущен из панели tmux, и тогда шапка несёт путезависимую " +
				"подсказку: для прыжка на своём сервере возврат это C-b L, для окна с attach — " +
				"C-b C-b d. Ни один из экранов выше её не показывал, потому что генератор по " +
				"умолчанию работает вне tmux.",
			shots: s})
	}

	// ---- 12. The picker ------------------------------------------------------
	//
	// A NEW screen, and it was in none of the eleven scenes above — so this
	// document's claim to hold every screen was stale from the moment the picker
	// shipped. Its frames and their assertions are built in
	// mockup_frames_test.go, which carries no build tag: the assertions then run in
	// `go test ./...` rather than only here, where nothing but a regeneration would
	// ever notice them going false.
	//
	// It goes last rather than first, though chronologically it is the first screen
	// a person meets, because the section anchors of this document are published and
	// inserting a scene at the front would silently repoint every one of them.
	scenes = append(scenes, pickerScene(t))

	// ---- 13. Naming a session ------------------------------------------------
	//
	// The five frames §21.14 called "not targets yet", for the reason it gave: they carried
	// no assertion, and a frame with no assertion is decoration. They are built in
	// mockup_frames_test.go, which carries no build tag, so their promises run in
	// `go test ./...` rather than only here.
	scenes = append(scenes, namingScene(t))

	// ---- 14. Ширина, обрезка и проектный вид ---------------------------------
	//
	// Этот блок добавлен последним, потому что четыре правки UI оказались НЕВИДИМЫ в этом
	// документе: ни одна из пятидесяти сцен не показывала ни проектный вид, ни плитку панели
	// без захвата, ни имя длиннее строки. Правки были сделаны по кадрам живого хаба на флоте
	// из тридцати семи сессий, где имена — это промпты и доходят до 88 колонок; документ же
	// собран на фикстурах с короткими именами и потому не двигался ни на строку. Сцена,
	// которой нет, — это проверка, которой нет.
	scenes = append(scenes, widthScene(t))
	scenes = append(scenes, hubOwnScene(t))
	scenes = append(scenes, treeScene(t))

	// ---- 17. Что стоит за хопами --------------------------------------------
	//
	// Раздел «discovered» пикера: машины, которые объявляет ЧУЖОЙ ssh-конфиг — те, до
	// которых у корня нет прямой дороги. Он появляется здесь потому, что правка, которой
	// нет в этом документе, регрессирует без диффа: пятьдесят с лишним сцен не содержали
	// ни одной строки, объявленной не корнем.
	//
	// Сцена ДОБАВЛЕНА В КОНЕЦ, а не рядом с пикером, по причине, записанной у сцены 12:
	// якоря разделов этого документа опубликованы, и вставка в середину молча переставила
	// бы каждый следующий.
	scenes = append(scenes, discoveredScene(t))

	// EVERY `want` AND `deny` IS CHECKED HERE, before the document is written.
	//
	// `assertHTML` prints them beside each frame under the words "проверяется в go test
	// ./internal/ui/", and that sentence was FALSE: the default suite's guard walks only the picker
	// and naming scenes, which are the two built in the untagged file — everything in this generator
	// is behind the `mockup` tag and therefore invisible to it. Measured the day this was added: a
	// favourites scene claimed `▾ FAVOURITES` while its own frame had no band at all, because its
	// fixture pinned nothing, and nothing anywhere failed. A published claim that a check exists is
	// worse than no claim, because it stops the reader checking.
	checked := 0
	for _, sc := range scenes {
		for _, sh := range sc.shots {
			for _, w := range sh.want {
				checked++
				if !strings.Contains(sh.screen, w) {
					t.Errorf("%s — %s: the frame does not contain %q:\n%s", sc.name, sh.title, w,
						sh.screen)
				}
			}
			for _, d := range sh.deny {
				checked++
				if strings.Contains(sh.screen, d) {
					t.Errorf("%s — %s: the frame contains %q, which it promises not to:\n%s",
						sc.name, sh.title, d, sh.screen)
				}
			}
		}
	}
	// A FLOOR, because a loop that found nothing would pass having checked nothing — the shape this
	// repo names "a check that ends in I found nothing is indistinguishable from I looked at nothing".
	if checked < 60 {
		t.Errorf("only %d assertions were checked across %d scenes, so this gate is no longer "+
			"reading the scenes", checked, len(scenes))
	}

	if t.Failed() {
		// NOT WRITTEN. The document is served publicly the moment it lands on disk (docs/ is
		// bind-mounted read-only into a running Caddy container), so a frame that does not match the
		// claim printed beside it must not be published at all — there is no commit step to catch it.
		t.Fatal("the frames do not match their own assertions, so the document was not written")
	}

	out := filepath.Join(repoRootFromTest(t), "docs", "ui-mockup.html")
	if err := os.WriteFile(out, []byte(renderHTML(scenes)), 0o644); err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, s := range scenes {
		n += len(s.shots)
	}
	fmt.Printf("wrote %s: %d сценариев, %d экранов\n", out, len(scenes), n)
}

func repoRootFromTest(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd() // internal/ui
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Dir(filepath.Dir(wd))
}

var sgr = regexp.MustCompile(`\x1b\[[0-9;]*m`)

// toHTML keeps the renderer's bytes and only makes them safe to embed. ANSI is
// stripped rather than translated: lipgloss emits no colour without a TTY, so
// translating would invent styling the reviewer would then judge.
func toHTML(s string) string { return html.EscapeString(sgr.ReplaceAllString(s, "")) }

// assertHTML prints what a frame promises, so a reviewer reads the claim beside the
// picture instead of taking the caption's word for it. The strings are the literals
// TestMockupFramesAssertWhatTheyShow checks in the default suite, so this block cannot
// describe a check that is not running.
func assertHTML(sh shot) string {
	if len(sh.want) == 0 && len(sh.deny) == 0 {
		return ""
	}
	var parts []string
	for _, w := range sh.want {
		parts = append(parts, "<code>"+html.EscapeString(w)+"</code>")
	}
	for _, d := range sh.deny {
		parts = append(parts, "не содержит <code>"+html.EscapeString(d)+"</code>")
	}
	return `
<div class="meta">проверяется в <code>go test ./internal/ui/</code>: ` +
		strings.Join(parts, ", ") + `</div>`
}

func renderHTML(scenes []scene) string {
	var b strings.Builder
	total := 0
	for _, s := range scenes {
		total += len(s.shots)
	}
	b.WriteString(`<!doctype html><html lang="ru"><head><meta charset="utf-8">
<title>tmux-hub — интерфейс целиком</title><style>
:root{--bg:#12131a;--panel:#181a22;--ink:#dfe3ee;--dim:#8b93a7;--line:#282b36;--acc:#7aa2f7;--warn:#e0af68}
*{box-sizing:border-box}
body{margin:0;background:var(--bg);color:var(--ink);
 font:15px/1.6 -apple-system,BlinkMacSystemFont,"Segoe UI",Inter,system-ui,sans-serif}
header{padding:36px 40px 22px;border-bottom:1px solid var(--line)}
h1{margin:0 0 8px;font-size:24px;font-weight:600;letter-spacing:-.01em}
header p{margin:0;color:var(--dim);max-width:70ch}
nav{position:sticky;top:0;z-index:5;background:rgba(18,19,26,.94);backdrop-filter:blur(8px);
 border-bottom:1px solid var(--line);padding:12px 40px;display:flex;gap:8px;flex-wrap:wrap}
nav a{color:var(--dim);text-decoration:none;font-size:13px;padding:5px 11px;border:1px solid var(--line);border-radius:999px}
nav a:hover{color:var(--ink);border-color:var(--acc)}
main{padding:8px 40px 80px}
section{padding-top:40px}
h2{font-size:19px;margin:0 0 6px;font-weight:600}
.intro{color:var(--dim);max-width:78ch;margin:0 0 26px}
.shot{margin:0 0 30px;border:1px solid var(--line);border-radius:10px;overflow:hidden;background:var(--panel)}
.shot > figcaption{padding:14px 18px;border-bottom:1px solid var(--line)}
.t{font-weight:600;font-size:15px}
.meta{color:var(--dim);font-size:13px;margin-top:5px}
.meta b{color:var(--warn);font-weight:600}
.look{color:var(--dim);font-size:13px;margin-top:7px;border-left:2px solid var(--acc);padding-left:10px}
pre{margin:0;padding:18px;overflow-x:auto;background:#0d0e13;
 font:12.5px/1.45 "SF Mono",ui-monospace,"JetBrains Mono",Menlo,Consolas,monospace;
 color:#c8cee0;white-space:pre;tab-size:8}
.dim{color:var(--dim)}
footer{padding:26px 40px 60px;border-top:1px solid var(--line);color:var(--dim);font-size:13px;max-width:80ch}
code{background:#000;padding:1px 5px;border-radius:4px;font-size:12.5px}
</style></head><body>
<header><h1>tmux-hub — интерфейс целиком</h1>
<p>Каждый экран ниже — это байты, которые печатает <code>View()</code>. Ничего не нарисовано руками:
макет с выдуманными экранами позволил бы одобрить раскладку, которой программа не выдаёт.
Собрано генератором <code>internal/ui/mockup_test.go</code>.</p></header>
<nav>`)
	for i, s := range scenes {
		fmt.Fprintf(&b, `<a href="#s%d">%s</a>`, i, html.EscapeString(s.name))
	}
	b.WriteString("</nav><main>")
	for i, s := range scenes {
		fmt.Fprintf(&b, `<section id="s%d"><h2>%d. %s</h2><p class="intro">%s</p>`,
			i, i+1, html.EscapeString(s.name), html.EscapeString(s.intro))
		for _, sh := range s.shots {
			fmt.Fprintf(&b, `<figure class="shot"><figcaption>
<div class="t">%s</div>
<div class="meta"><b>%d×%d</b> · %s</div>
<div class="look">%s</div>%s
</figcaption><pre>%s</pre></figure>`,
				html.EscapeString(sh.title), sh.w, sh.h, html.EscapeString(sh.did),
				html.EscapeString(sh.look), assertHTML(sh), toHTML(sh.screen))
		}
		b.WriteString("</section>")
	}
	fmt.Fprintf(&b, `</main><footer>%d сценариев, %d экранов.
Пересобрать: <code>go test -tags mockup -run TestGenerateMockup ./internal/ui/</code>.
Экраны ПРЕДЛОЖЕННОЙ функции живут отдельно и помечены покадрово:
<a href="ui-flows-possession.html" style="color:#7aa2f7">§20 вселение — потоки</a>.
Здесь их нет намеренно: макет с выдуманными экранами позволил бы одобрить раскладку,
которой программа не выдаёт.
Экраны без цвета намеренно: lipgloss не раскрашивает вывод без терминала, а перевод ANSI
в CSS придумал бы оформление, которое потом пришлось бы оценивать как настоящее.</footer>
</body></html>`, len(scenes), total)
	return b.String()
}

// longFleet is a fleet whose names are the length real ones are: on this fleet a session is named
// after the prompt that started it, measured at 88 columns, and every layout question about
// truncation is invisible against a fixture called `api`.
//
// The paths are what the project view groups by, and they are real shapes: two sessions of one
// project, one of another, and one pane whose capture is EMPTY — a `sleep`, a cleared prompt, a
// program that draws nothing — which is the case that used to draw a three-row box with nothing in it.
func longFleet() []registry.Pane {
	const long = "20260803--store-online-takes-too-long-to-ci-cd-troubleshooting"
	a := agentPane("local", long, "review", "%0", 0, state.Needs,
		"  Do you want to proceed?", "  ❯ 1. Yes", "    2. No")
	a.Path = "/w/billing-iac"
	a.AgentName = long
	b := agentPane("local", "20260809--рендеринг-карты-и-офлайн-тайлы", "map", "%1", 1, state.Works,
		"  Reading internal/tmux/batch.go…")
	b.Path = "/w/billing-iac"
	c := agentPane("nuc", "20260817-cicd", "deploy", "%4", 0, state.Quiet, "  Applied 3 migrations.")
	c.Path = "/w/render-map"
	// No Content at all: the tile has nothing of the pane's own screen to show.
	d := toolPane("local", "scratch", "shell", "%7", "sleep", 0, state.Idle)
	d.Content = nil
	d.Path = "/w/render-map"
	d.Width, d.Height = 120, 40
	return []registry.Pane{a, b, c, d}
}

// hubOwnScene is the round that answered three questions the operator asked about their own screen:
// what `%1` is, why the host appears twice, and what `LOCAL 0` is. None of the sixty-odd scenes above
// could show any of them — every fixture here is one pane per session with short names, and the hub's
// own windows are not in any fixture at all — so the frames the fixes were judged on lived only in a
// capture. That is the gap this scene closes.
// treeScene is the filesystem view — hosts as volumes, directories as directories, sessions as files.
//
// It is the ONLY place this shape is published, and it is published before the screen is reachable on
// purpose: the operator asked for it as a default view, and a frame is what that decision should be
// made on. The fixture is the operator's own fleet in miniature and HOME is a LITERAL, so the document
// stays byte-reproducible on any machine.
func treeScene(t *testing.T) scene {
	t.Helper()
	var sh []shot
	fleet := treeFleet()

	// 1-3. The tree at the three width bands §16 commits to. The fixture is treeFleet(): a branching
	// family (st holding two directories), a collapsed chain (experiments/tmux-hub), a row with no
	// path (tmp-1e), and a second volume (nuc). The default expansion opens the MAP (volumes and
	// directories with children) and shuts the FOLDERS (leaf directories holding only sessions).
	for _, w := range []int{80, 120, 200} {
		m := base(t, w, 24, fleet...)
		m.home = "/home/dev"
		out, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("t")})
		sh = append(sh, shot{
			title: fmt.Sprintf("%d. Дерево, папки закрыты (%d колонок)", len(sh)+1, w),
			did:   "нажал t",
			look:  "видна ли структура — тома, каталоги, закрытые папки со счётчиками",
			w:     w, h: 24, screen: out.(model).View(),
			want: []string{"enter opens", "esc leaves", "▾ local/", "▸ frontend/", "▾ nuc/"},
		})
	}

	// 4. Everything FULLY OPEN: all nodes expanded to show session rows. This is NOT the default
	// (§23 measured it: 54 rows under 20 nodes, a quarter of the fleet on screen), but it is the
	// contrast that makes the default's decision visible.
	open := base(t, 120, 24, fleet...)
	open.home = "/home/dev"
	openOut, _ := open.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("t")})
	open = openOut.(model)
	// Force everything open. The ORDER matters and the first version got it backwards: it assigned an
	// EMPTY map first and then walked `treeShown()`, which by then was reading that empty map — so only
	// the two volumes were visible to be marked, and the scene titled "fully open" published a tree
	// with LESS open than the default. The lines are taken from the default walk (a nil map, where every
	// node including a closed leaf is drawn) and only then does the map replace it.
	opened := map[string]bool{}
	for _, l := range open.treeShown() {
		if !l.IsRow {
			opened[l.Key] = true
		}
	}
	open.treeOpen = opened
	for _, l := range open.treeShown() {
		if !l.IsRow {
			open.treeOpen[l.Key] = true
		}
	}
	sh = append(sh, shot{
		title: "4. То же, раскрытое целиком",
		did:   "нажал enter на каждом узле",
		look:  "видно ли, ГДЕ ждут — глифы состояния, имена сессий",
		w:     120, h: 24, screen: open.View(),
		want: []string{"⚑ needs  healthchecks", "✓ done   main", "▾ st-edgebox/"},
	})

	// 5. THE FAVOURITES BAND: pinned rows at the top, appearing once (not duplicated in their
	// directories). The favourites node is always open by design (§23).
	pinned := base(t, 120, 24, fleet...)
	pinned.home = "/home/dev"
	// A REAL favourites store and the RETURN VALUE kept, both of which the first version dropped:
	// `toggleFavouriteSessionOf(p, false)` means "there is nothing under the cursor" and only sets a
	// note, and the result was discarded anyway — so the scene pinned nothing while its title, its
	// caption and its `want` all promised a band. The path is a temp dir and never appears in a frame,
	// so the document stays byte-reproducible.
	favs, err := fav.Open(filepath.Join(t.TempDir(), "favourites.json"))
	if err != nil {
		t.Fatal(err)
	}
	pinned.favs = favs
	for i := 0; i < 2; i++ {
		pinned = pinned.toggleFavouriteSessionOf(fleet[i], true)
	}
	pinnedOut, _ := pinned.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("t")})
	sh = append(sh, shot{
		title: "5. Закреплённые: своя полоса наверху",
		did:   "нажал f на двух строках, потом t",
		look: "видны ли закреплённые строки под FAVOURITES, и пропали ли они из каталогов — " +
			"одна строка дважды это дубликат",
		w: 120, h: 24, screen: pinnedOut.(model).View(),
		want: []string{"▾ FAVOURITES", "store-online @local:~/lab/streams/st/st-edgebox",
			"▸ frontend/"},
		// The pinned rows are NOT also drawn in their directories, which is what makes the band a
		// move rather than a copy: `frontend` is closed here, so its own row cannot be the match.
		deny: []string{"          ⚑ needs  healthchecks"},
	})

	// 6. DIRECTORY TILE: when the cursor is on a directory node, the band shows the node's address,
	// roll-up, and the sessions inside it — including for a CLOSED node, which is the whole permission
	// to close one (§23). This is nodeTile, not RenderTile (which is for panes).
	tileM := base(t, 120, 24, fleet...)
	tileM.home = "/home/dev"
	tileOut, _ := tileM.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("t")})
	tileM = tileOut.(model)
	// Walk to a directory node that has sessions in it.
	tls := tileM.treeShown()
	dirAt := -1
	for i, l := range tls {
		if !l.IsRow && l.Path != "" && l.Sum.Total > 0 {
			dirAt = i
			break
		}
	}
	if dirAt >= 0 {
		tileM = tileM.treeTo(tls, dirAt)
		sh = append(sh, shot{
			title: "6. Плитка каталога: адрес, счёт, сессии внутри",
			did:   "курсор на узле каталога",
			look: "видно ли, что внутри ЗАКРЫТОГО узла — это разрешение его закрыть. " +
				"И цел ли путь, который форме запуска понадобится",
			w: 120, h: 24, screen: tileM.View(),
			// No `… and N more` here: at this height the band is tall enough to list every session
			// inside, and the tally only appears when the tile has to give a row up — which the launch
			// form's scene below shows, because the form takes twelve rows from the band.
			want: []string{"┌─ local ~/lab/streams ", "5 sessions · 3 asking · /home/dev/lab/streams",
				"⚑ needs  store-online"},
		})
	}

	// 7. COMPOSER OVER THE TREE: the backdrop behaviour, where Frame.Screen makes the tree visible
	// under an overlay. Before this the overlay behaviour was invisible in this document.
	compose := base(t, 120, 24, fleet...)
	compose.home = "/home/dev"
	composeOut, _ := compose.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("t")})
	compose = composeOut.(model)
	// Opened everything, walked to a session, marked it, and pressed `i` — the keys, not the fields.
	// The first version set `mode = modeCompose` by hand, which skips `raise`, leaves `underlay` at the
	// dashboard, and therefore published the DASHBOARD as the backdrop under a scene titled "over the
	// tree". A scene that sets state instead of pressing keys cannot show a mechanism whose whole
	// content is what the keys record.
	composeOpen := map[string]bool{}
	for _, l := range compose.treeShown() {
		if !l.IsRow {
			composeOpen[l.Key] = true
		}
	}
	compose.treeOpen = composeOpen
	for _, l := range compose.treeShown() {
		if !l.IsRow {
			compose.treeOpen[l.Key] = true
		}
	}
	composeLines := compose.treeShown()
	for i, l := range composeLines {
		if l.IsRow {
			compose = compose.treeTo(composeLines, i)
			break
		}
	}
	markOut, _ := compose.Update(tea.KeyMsg{Type: tea.KeySpace})
	compose = markOut.(model)
	iOut, _ := compose.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("i")})
	compose = iOut.(model)
	if compose.mode != modeCompose {
		t.Fatalf("the composer scene did not reach the composer: mode %v, note %q", compose.mode,
			compose.note)
	}
	compose.composer.Insert("прочитай docs/design.md §23 и скажи, где правило про backdrop")
	sh = append(sh, shot{
		title: "7. Ящик ввода над деревом",
		did:   "выбрал строку, нажал i, печатал",
		look: "видно ли дерево ПОД оверлеем — это backdrop, новое поведение Frame.Screen, " +
			"и ни один экран выше его не показывал",
		w: 120, h: 24, screen: compose.View(),
		want: []string{"прочитай docs/design.md", "▾ frontend/", "enter: send"},
		// THE DASHBOARD IS NOT UNDER IT, which is the whole content of the backdrop rule — and the
		// needle is the program's own title line, which only the flat screen draws.
		deny: []string{"tmux-hub  "},
	})

	// 8. LAUNCH FORM FROM A DIRECTORY: the gesture §23 was asked for — "when creating a new session,
	// create it in the corresponding directory". The directory is pre-filled.
	launch := base(t, 120, 24, fleet...)
	launch.home = "/home/dev"
	launchOut, _ := launch.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("t")})
	launch = launchOut.(model)
	tls = launch.treeShown()
	dirAt = -1
	for i, l := range tls {
		if !l.IsRow && l.Path != "" {
			dirAt = i
			break
		}
	}
	if dirAt >= 0 {
		launch = launch.treeTo(tls, dirAt)
		launchOut, _ = launch.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("n")})
		sh = append(sh, shot{
			title: "8. Форма запуска, открытая с узла каталога",
			did:   "нажал n на каталоге",
			look: "подставлен ли путь каталога, и видно ли дерево позади — " +
				"это жест, ради которого метафору и спросили",
			w: 120, h: 24, screen: launchOut.(model).View(),
			want: []string{"Start a Claude Code session", "host:  local", "/home/dev/lab/streams",
				"enter: create"},
		})
	}

	// 9. KEYWORD SEARCH: the `/` key narrows the tree live, the footer says the query, and the tree
	// still shows STRUCTURE — a flat list would be pointlessly drawn as a tree.
	search := base(t, 120, 24, fleet...)
	search.home = "/home/dev"
	searchOut, _ := search.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("t")})
	search = searchOut.(model)
	searchOut, _ = search.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("/")})
	search = searchOut.(model)
	for _, r := range "frontend" {
		search.search.Insert(string(r))
	}
	sh = append(sh, shot{
		title: "9. Поиск по ключевому слову",
		did:   "нажал /, набрал frontend",
		look: "сузился ли список, видно ли слово и счёт, несёт ли дерево структуру — " +
			"плоский список был бы зря нарисован деревом",
		w: 120, h: 24, screen: search.View(),
		want: []string{"search: frontend", "2 of 7", "▸ ~/lab/streams/st/frontend/"},
	})

	return scene{
		name: "Файловая система: тома, каталоги, сессии",
		intro: "Хосты — тома, каталоги — каталоги, сессии — файлы. Сегодня «проект» это только " +
			"последний сегмент пути, и вся иерархия выбрасывается: `st-edgebox`, `frontend` и " +
			"`tundra-security-server` — три несвязанные метки, которые на самом деле одна семья " +
			"`~/lab/streams/st`. Порядок внутри узла — по ВНИМАНИЮ, а не по алфавиту: это единственное " +
			"решение против метафоры, и оно за то, ради чего экран существует. Узлы несут свёрнутый " +
			"счётчик, поэтому закрытое дерево отвечает на «кто меня ждёт» в четыре строки. " +
			"Первая отрисовка открывает КАРТУ (тома и ветви) и закрывает ПАПКИ (листья с сессиями) — " +
			"это решение, которое делает экран пригодным как дефолтный (§23).",
		shots: sh,
	}
}

func hubOwnScene(t *testing.T) scene {
	t.Helper()
	var sh []shot

	// A header's run broken by rows that are not in it: the shape the operator read as a duplicate.
	broken := base(t, 120, 24, interleavedFleet(3)...)
	sh = append(sh, shot{title: "1. Заголовок больше не присваивает себе чужие строки",
		did: "флот, где одиночки падают внутрь чужой группы", w: 120, h: 24, screen: broken.View(),
		look: "понятно ли, где группа кончилась, и на каком хосте живёт каждая строка"})

	// The same fleet at 80, where there are no headers at all: one convention, both bands.
	narrow := base(t, 80, 24, interleavedFleet(3)...)
	sh = append(sh, shot{title: "2. Тот же флот на 80 колонках — заголовков нет вовсе",
		did: "та же выборка, узкая полоса", w: 80, h: 24, screen: narrow.View(),
		look: "одна ли конвенция на обеих ширинах"})

	// The hub's own windows, dropped from the fleet with the header saying so. `self` has to be set
	// by hand: under `go test` the binary is the test binary, so nothing here is named tmux-hub.
	own := base(t, 120, 24, hubFurnitureFleet()...)
	own.self = "tmux-hub"
	own.setFleet(hubFurnitureFleet())
	sh = append(sh, shot{title: "3. Хаб перестал показывать сам себя и свои двери",
		did: "флот, где половина строк — окна самого хаба", w: 120, h: 24, screen: own.View(),
		look: "сходится ли счёт сессий с тем, что скажет tmux ls"})

	// Two rows drawing ONE label, which no other scene has: the adversarial review found this class
	// and the live fleet had it — two Claude sessions both called `20260818--cicd`, both waiting.
	twin := func(id string, st state.State) registry.Pane {
		return registry.Pane{
			Kind: registry.KindAgent, Command: "background", Host: "local",
			Session: "20260818--cicd", AgentID: id, SessionID: id + "-aaaa",
			PaneID: "agent:" + id + "@ee42d26c", ClassifiedState: st,
			Content: []string{"  (no pane)"},
		}
	}
	// The third twin is the shape the live fleet had: an `interactive` record, which `claude agents`
	// reports with NO job id, so its identity has to come from its uuid. Without it in this document
	// the fallback is published nowhere and the next refactor can drop it without moving a frame.
	noJob := twin("", state.Idle)
	noJob.Command, noJob.SessionID = "interactive", "a112a9b8-cccc"
	noJob.PaneID = "agent:a112a9b8@ee42d26c"
	clash := base(t, 120, 24, twin("1b0cacf2", state.Needs), twin("30f3382b", state.Needs), noJob,
		agentPane("local", "tmp", "claude", "%20", 20, state.Quiet, "one"))
	sh = append(sh, shot{title: "4. Две строки с одним именем теперь различимы",
		did: "две claude-сессии, названные одинаково", w: 120, h: 24, screen: clash.View(),
		look: "видно ли, на какую из двух подействует `a`, не сходив в плитку"})

	// THE PINNED BAND, which no other scene shows: favouritesFirst lifts these rows to the top, and a
	// pinned row leads with its NAME and carries `@host:path` because the operator chose it and the
	// question is which one. HOME is a LITERAL here, not os.UserHomeDir(): this document is diffed
	// byte for byte, so a value read from the environment would make it depend on the machine.
	const mockHome = "/home/dev"
	pinned := func(name, dir string, st state.State) registry.Pane {
		return registry.Pane{
			Kind: registry.KindPane, Host: "local", Session: name, Window: "w",
			PaneID: "%" + name[:1], SessionID: "$1", Command: "claude",
			ClaudeSession: name + "-uuid", AgentName: name, Path: mockHome + dir,
			ClassifiedState: st, Content: []string{"  ❯ "},
		}
	}
	favRows := []registry.Pane{
		pinned("xmap-universal-reader", "/lab/streams/experiments/xmap-reverse-engineering", state.Needs),
		pinned("billing-cicd", "/lab/streams/orbits/billing-iac", state.Needs),
		pinned("seedtool-development", "/lab/streams/st/st-edgebox", state.Works),
		agentPane("nuc", "envoy-ops-svcdev4-ci", "w", "%7", 7, state.Quiet, "not pinned"),
	}
	favSet := map[string]bool{}
	for _, r := range favRows[:3] {
		favSet[MarkKey(r)] = true
	}
	for _, w := range []int{80, 120} {
		sh = append(sh, shot{
			title: fmt.Sprintf("%d. Закреплённые: имя впереди, потом адрес (%d колонок)", 5+len(sh)-3, w),
			did:   "три закреплённые строки и одна нет", w: w, h: 24,
			screen: Render(Frame{Panes: favRows, Hosts: hosts2(), Width: w, Height: 24,
				Aliases: project.Aliases{}, Favourites: favSet, Home: mockHome}),
			look: "уступает ли путь первым и цел ли последний сегмент"})
	}

	return scene{name: "Своё против чужого: заголовки, id панелей и окна хаба",
		intro: "Оператор спросил три вещи про свой экран: что такое `%1`, почему хост виден дважды " +
			"и что такое `LOCAL 0`. Ответ на все три — одно: список упорядочен по вниманию, а " +
			"рисовался так, будто сгруппирован. Строка теперь называет свой хост, разрыв группы " +
			"помечен, id панели показывается только там, где он что-то различает, а окна самого " +
			"хаба вообще не строки флота.",
		shots: sh}
}

func widthScene(t *testing.T) scene {
	t.Helper()
	var sh []shot

	byHost := base(t, 80, 24, longFleet()...)
	sh = append(sh, shot{title: "1. Имя длиннее строки — обрезка теперь помечена",
		did: "флот, где сессия названа промптом", w: 80, h: 24, screen: byHost.View(),
		look: "видно ли, что имя продолжается, а не кончилось"})

	byProject := base(t, 80, 24, longFleet()...)
	byProject.groupBy = ByProject
	sh = append(sh, shot{title: "2. Тот же экран по проектам, 80 колонок", did: "нажал v",
		w: 80, h: 24, screen: byProject.View(),
		look: "отвечает ли вид на свой же вопрос — под каким проектом эта строка"})

	wide := base(t, 200, 50, longFleet()...)
	sh = append(sh, shot{title: "3. Двести колонок — что даёт лишняя ширина", did: "тот же флот",
		w: 200, h: 50, screen: wide.View(),
		look: "куда уходит ширина и обрезан ли заголовок плитки"})

	empty := base(t, 80, 24, longFleet()...)
	// The cursor goes on the pane that has NOTHING captured, found by its id rather than by an
	// index: the list is sorted by attention, so the last row is whichever row wants the operator
	// least — which is not the same question.
	for i, r := range empty.rowsForScreen() {
		if r.PaneID == "%7" {
			empty = empty.cursorTo(i)
		}
	}
	sh = append(sh, shot{title: "4. Плитка панели, у которой нечего показать",
		did: "курсор на панели без захвата", w: 80, h: 24, screen: empty.View(),
		look: "несёт ли рамка что-нибудь, чего нет в самой строке"})

	// The search FIELD while it has focus, which no scene showed — so the count that now travels with
	// it was invisible in this document, exactly like the four defects the block above exists for.
	field := base(t, 80, 24, longFleet()...)
	field.mode = modeSearch
	field.search.Insert("рендеринг")
	sh = append(sh, shot{title: "5. Поле поиска: что слово уже сделало", did: "нажал / и печатает",
		w: 80, h: 24, screen: field.View(),
		look: "видно ли, сузилось ли до чего-то полезного, ещё до enter"})

	// The keyword read LOOSELY, which no other scene can show: `opssch` is in none of these names as
	// a substring and is a subsequence of one, so this is the only frame in the document where the
	// field admits that the rows below merely resemble what was typed.
	loose := base(t, 80, 24, looseFleet()...)
	loose.mode = modeSearch
	for _, r := range "opssch" {
		loose.search.Insert(string(r))
	}
	sh = append(sh, shot{title: "7. Слово, которого нет ни в одном имени", did: "нажал / и набрал opssch",
		w: 80, h: 24, screen: loose.View(),
		look: "признаётся ли экран, что строки лишь ПОХОЖИ на набранное"})

	list := base(t, 80, 24, longFleet()...)
	list.mode = modeProjects
	sh = append(sh, shot{title: "6. Список проектов — счёт без фактов", did: "нажал P",
		w: 80, h: 24, screen: list.View(),
		look: "читается ли счёт как фраза, когда сказать больше нечего"})

	return scene{name: "Ширина, обрезка и проектный вид",
		intro: "Четыре правки, каждая по кадрам живого хаба: помеченная обрезка там, где строка " +
			"кончается не сама; проектный вид, который на 80 колонках раньше менял одно слово в " +
			"шапке; плитка панели без захвата; и счёт в списке проектов, читавшийся как половина " +
			"фразы.",
		shots: sh}
}

// discoveredScene is the picker with machines behind its hops.
//
// EVERY NAME HERE IS INVENTED, which is the same hard rule pickerFleet states and for the same
// reason: `docs/` is bind-mounted read-only into a running Caddy container and served publicly, so a
// frame is published the moment it is written. The identity files are invented too — a real key path
// names a real machine's credential.
//
// The rows are built through the PRODUCT: `crawled` diagnoses each declaration with the real
// fleet.Diagnose against a home directory that holds none of the keys, and the graph folds them. A
// hand-written row would let this document publish a state and a remedy the program does not produce.
func discoveredScene(t *testing.T) scene {
	t.Helper()
	cands, results := pickerFleet()
	rows := PickerRowsFor(cands, results, nil, nil, pickerAsked)

	// A home with no keys at all, so every recipe naming one is genuinely Blocked. t.TempDir() and
	// not a literal: a path from this machine would make the frame depend on the machine.
	home := t.TempDir()
	store := newFleetStore()
	store.Observe(crawled("depot-a", []hostset.Candidate{
		{Alias: "vault-b", Via: "depot-a", Recipe: map[string]string{
			"hostname": "vault-b.internal", "user": "dev", "identityfile": "~/.ssh/depot-only"}},
		{Alias: "edge-eu-1", Via: "depot-a", Recipe: map[string]string{
			"hostname": "edge-eu-1.internal", "user": "dev", "proxyjump": "depot-a"}},
		{Alias: "shelf-*", Via: "depot-a",
			Skip: "a pattern rather than a machine, so there is nothing to ask — declare the host it " +
				"stands for in that machine's own ~/.ssh/config"},
	}, home)...)
	snap := store.Snapshot()
	// A remembered round trip for the hop, which is the only measurement that bounds anything behind
	// it — the section prints the hop beside the label so the figure is never read as the machine's.
	facts := func(k fleetcache.Key) (fleetcache.Facts, bool) {
		if k.Alias == "depot-a" {
			return fleetcache.Facts{RTT: 180 * time.Millisecond, LastSeen: mockupNow}, true
		}
		return fleetcache.Facts{}, false
	}
	found := DiscoveredRowsFor(snap.Nodes, snap.Candidates, facts)

	narrow := pickerModel(t, 80, 24, rows, hosts2(), pickerLocalOnly()...)
	narrow.discovered = found
	wide := pickerModel(t, 120, 40, rows, hosts2(), pickerLocalOnly()...)
	wide.discovered = found

	// A machine whose ~/.ssh/config holds three entries rather than twenty. The candidate list then
	// wants three rows, and the section may have the rest — the rule that stops a taller terminal
	// showing less than a shorter one.
	fewCands, fewResults := cands[:3], results[:3]
	few := pickerModel(t, 120, 40, PickerRowsFor(fewCands, fewResults, nil, nil, pickerAsked),
		hosts2(), pickerLocalOnly()...)
	few.discovered = found

	return scene{
		name: "Что стоит за хопами",
		intro: "Раздел пикера про машины, до которых у этой машины прямой дороги нет: их объявляет " +
			"ssh-конфиг ХОПА, прочитанный по уже открытому мастеру. Ни одна из них не становится " +
			"узлом графа — узел делает только собственное завершённое рукопожатие корня, а через " +
			"прокси приходит ключ ПОСРЕДНИКА, — поэтому продукт здесь это не строки, на которые " +
			"можно нажать, а диагноз с одной командой на каждую. Порядок берётся из запомненного " +
			"времени отклика И ОКРУГЛЯЕТСЯ ДО КОРЗИНЫ: на живом флоте одна проба давала 5,4 / 9,1 / " +
			"15,7 / 18,4 с, и список, отсортированный по самой цифре, переставлялся бы между двумя " +
			"открытиями экрана — строка, которую человек отметил, оказалась бы другой машиной.",
		shots: []shot{
			{
				title: "1. Обещанные 80×24 — одна машина целиком, остальные посчитаны",
				did:   "нажал p; хаб прочитал конфиг единственного включённого хопа",
				look: "видно ли, что это НЕ кандидаты корня (у них галочки выше), и доходит ли " +
					"КОМАНДА до экрана целиком: раздел уступает список галочек, но заголовок " +
					"держит общее число, а обрезанное посчитано отдельной строкой.",
				w: 80, h: 24,
				screen: narrow.View(),
				want: []string{
					"Behind your hops — 3 machines your hosts declare",
					// The ACT survives the squeeze and the ` …` says the sentence does not: at this
					// size one machine's whole remedy would take six of the seven body rows.
					"  blocked   edge-eu-1 @depot-a",
					"give this machine a direct route",
					" …",
					"↓ 2 machines not shown — a taller terminal shows more",
					"space: keep this host · enter: save and connect · esc: cancel · r: probe again",
				},
				// Ни одна строка раздела не несёт галочки: нажать на них нечего, и коробка тут
				// была бы обещанием, которого продукт не выполняет.
				deny: []string{"[x] vault-b", "[ ] vault-b", "[x] edge-eu-1"},
			},
			{
				title: "2. 120×40 — все три с причинами",
				did:   "то же на большом терминале",
				look: "у каждой строки есть слово состояния, имя хопа, корзина времени и средство: " +
					"ключ, которого здесь нет, прокси, который подменяет личность, и шаблон, " +
					"который не машина. `unreachable` не встречается нигде — это не средство.",
				w: 120, h: 40,
				screen: wide.View(),
				want: []string{
					"Behind your hops — 3 machines your hosts declare",
					"  blocked   edge-eu-1 @depot-a",
					// WHOLE here, where the same sentence was cut at 80: the taller terminal is what
					// buys it, and the two frames side by side are the evidence for that.
					"so the hub cannot tell which machine answered",
					// The hop's own round trip, bucketed. Every row carries it because a machine
					// behind a hop cannot be nearer than the hop, and the hop's name is on the row so
					// the figure is not read as the far machine's own.
					"<250ms",
					"↓ 2 machines not shown — a taller terminal shows more",
				},
				// Nothing is cut mid-sentence at this width, so the marker that says one was must not
				// appear — and `unreachable` names no act, which invariant 4 forbids anywhere.
				deny: []string{"unreachable", " …"},
			},
			{
				title: "3. Короткий список кандидатов — раздел берёт то, что списку не нужно",
				did:   "то же на машине, где в ~/.ssh/config всего три записи",
				look: "видно ли все три машины с их средствами целиком. Это ПРАВИЛО, а не удача: " +
					"список галочек прокручивается, поэтому на флоте из двадцати он забирает свою " +
					"половину, а на флоте из трёх ему нужно три строки — и оставлять остальное " +
					"пустым, печатая «1 machine not shown», было бы кадром, где БОЛЬШИЙ терминал " +
					"показывает МЕНЬШЕ.",
				w: 120, h: 40,
				screen: few.View(),
				want: []string{
					"Behind your hops — 3 machines your hosts declare",
					"  blocked   vault-b @depot-a",
					"run `ssh-copy-id dev@vault-b.internal`",
					"  candidate shelf-* @depot-a",
					"  blocked   edge-eu-1 @depot-a",
				},
				// Every machine is shown, so neither the cut marker nor the compression marker may be
				// on this frame — and those two negatives are what make the positives above mean
				// "all three", rather than "at least one".
				deny: []string{"unreachable", " …", "not shown"},
			},
		},
	}
}
