//go:build mockup

// This file WRITES docs/ui-flows-possession.html. The frames themselves, and the
// check that each one shows what it promises, are in flows_frames_test.go — which
// carries no build tag, because that check needs neither this tag nor a live tmux
// and used to run in no gate at all.
//
//	HUB_FLOW_CAPTURES=<dir> go test -tags mockup -run TestGenerateFlows ./internal/ui/
//
// What is left here is the part that genuinely needs both: the five `capture-pane`
// frames from a real nested tmux (prototypes/possession-captures.sh), and writing
// the file.
//
// It is separate from mockup_test.go on purpose, and so is its output. That file
// and docs/ui-mockup.html contain only bytes the shipped program printed, and
// mixing a proposal into them would let us approve a layout the program does not
// produce. Here proposals are allowed — and every frame declares which of four
// things it is:
//
//	real                    the product's own bytes, end to end
//	real-render-proposed    the real renderer's layout carrying a string the
//	                        product does not produce yet
//	real tmux               capture-pane from a live nested tmux
//	drawn                   composed, seeded from a real frame
//
// A picture that promises something it does not show is worse than no picture: it
// is a test that cannot fail, in document form. The measured rule this follows is
// in docs/mockup-authoring.md.
package ui

import (
	"fmt"
	"html"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func (o origin) label() (string, string) {
	switch o {
	case originReal:
		return "настоящее", "байты продукта целиком — <code>View()</code> на этом состоянии"
	case originRealProposed:
		return "настоящий рендер, предложенная строка",
			"раскладку напечатал настоящий рендерер; строка внутри — предложение, продукт её пока не производит"
	case originTmux:
		return "настоящий tmux", "<code>capture-pane</code> из живого вложенного tmux, " +
			"<code>prototypes/possession-captures.sh</code>"
	default:
		return "нарисовано", "составлено из настоящего кадра подстановкой одной строки — " +
			"утверждение под ним ещё нечем проверить"
	}
}

func TestGenerateFlows(t *testing.T) {
	capDir := os.Getenv("HUB_FLOW_CAPTURES")
	if capDir == "" {
		t.Skip("HUB_FLOW_CAPTURES unset: run prototypes/possession-captures.sh <dir> first")
	}
	// The same builder the default suite uses, with a capture reader that really
	// reads. One builder for both, so the document that gets published cannot drift
	// from the frames anything checks.
	secs := flowSections(t, capturesFrom(capDir))

	// Every frame, including the five this tag exists for. The default suite got
	// nine of them; a mismatch here means a capture frame lost its assertion.
	checked := checkFrames(t, secs)

	out := filepath.Join(repoRootFromTest(t), "docs", "ui-flows-possession.html")
	if err := os.WriteFile(out, []byte(renderFlowsHTML(secs)), 0o644); err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, s := range secs {
		n += len(s.shots)
	}
	if checked != n {
		t.Fatalf("wrote %d frames but checked %d — every frame in this document is "+
			"supposed to be checked", n, checked)
	}
	fmt.Printf("wrote %s: %d потоков, %d кадров, %d утверждений проверено\n", out, len(secs), n, checked)
}

func renderFlowsHTML(secs []flowSection) string {
	var b strings.Builder
	total, drawn := 0, 0
	for _, s := range secs {
		for _, sh := range s.shots {
			total++
			if sh.origin == originDrawn {
				drawn++
			}
		}
	}
	b.WriteString(`<!doctype html><html lang="ru"><head><meta charset="utf-8">
<title>tmux-hub — §20 вселение, потоки</title><style>
:root{--bg:#12131a;--panel:#181a22;--ink:#dfe3ee;--dim:#8b93a7;--line:#282b36;--acc:#7aa2f7;
 --warn:#e0af68;--drawn:#c678dd;--real:#98c379}
*{box-sizing:border-box}
body{margin:0;background:var(--bg);color:var(--ink);
 font:15px/1.6 -apple-system,BlinkMacSystemFont,"Segoe UI",Inter,system-ui,sans-serif}
header{padding:36px 40px 22px;border-bottom:1px solid var(--line)}
h1{margin:0 0 8px;font-size:24px;font-weight:600;letter-spacing:-.01em}
header p{margin:0 0 10px;color:var(--dim);max-width:76ch}
.banner{margin:18px 0 0;padding:12px 16px;border:1px solid var(--drawn);border-radius:8px;
 color:var(--ink);max-width:76ch;background:rgba(198,120,221,.08)}
nav{position:sticky;top:0;z-index:5;background:rgba(18,19,26,.94);backdrop-filter:blur(8px);
 border-bottom:1px solid var(--line);padding:12px 40px;display:flex;gap:8px;flex-wrap:wrap}
nav a{color:var(--dim);text-decoration:none;font-size:13px;padding:5px 11px;
 border:1px solid var(--line);border-radius:999px}
nav a:hover{color:var(--ink);border-color:var(--acc)}
main{padding:8px 40px 80px}
section{padding-top:40px}
h2{font-size:19px;margin:0 0 6px;font-weight:600}
.intro{color:var(--dim);max-width:78ch;margin:0 0 26px}
.shot{margin:0 0 30px;border:1px solid var(--line);border-radius:10px;overflow:hidden;
 background:var(--panel)}
.shot.is-drawn{border-color:var(--drawn)}
.shot > figcaption{padding:14px 18px;border-bottom:1px solid var(--line)}
.t{font-weight:600;font-size:15px}
.tag{display:inline-block;font-size:11px;letter-spacing:.04em;text-transform:uppercase;
 padding:2px 8px;border-radius:999px;margin-left:10px;vertical-align:2px;
 border:1px solid var(--real);color:var(--real)}
.tag.drawn{border-color:var(--drawn);color:var(--drawn)}
.src{color:var(--dim);font-size:12px;margin-top:6px}
.meta{color:var(--dim);font-size:13px;margin-top:5px}
.assert{font-size:13px;margin-top:8px;border-left:2px solid var(--acc);padding-left:10px;
 color:var(--ink)}
.assert b{color:var(--acc);font-weight:600}
pre{margin:0;padding:18px;overflow-x:auto;background:#0d0e13;
 font:12.5px/1.45 "SF Mono",ui-monospace,"JetBrains Mono",Menlo,Consolas,monospace;
 color:#c8cee0;white-space:pre;tab-size:8}
footer{padding:26px 40px 60px;border-top:1px solid var(--line);color:var(--dim);font-size:13px;
 max-width:82ch}
code{background:#000;padding:1px 5px;border-radius:4px;font-size:12.5px}
</style></head><body>
<header><h1>§20 вселение — потоки</h1>
<p>Это ревью-поверхность §20, и функция <b>реализована и влита</b> — документ начинался до неё,
с нарисованными кадрами, и ни одного из них не осталось. Он отдельно от
<code>docs/ui-mockup.html</code> намеренно: там нет ни одного выдуманного экрана, и подмешивать
предложение туда нельзя — иначе можно одобрить раскладку, которой программа не выдаёт.</p>
<p>Каждый кадр помечен, чем он является, и несёт <b>утверждение</b>, которое обещает. Утверждения
кадров из <code>View()</code> проверяет ОБЫЧНЫЙ прогон тестов — <code>go test ./...</code>, без
тега и без переменной окружения (<code>TestFlowFramesAssertWhatTheyShow</code>); кадры
<code>capture-pane</code> и запись этого файла проверяет генератор, у которого для этого есть
живой вложенный tmux. Разделено именно так, потому что раньше проверка была целиком за тегом
и за <code>HUB_FLOW_CAPTURES</code>, а <code>t.Skip</code> отчитывается как PASS — то есть все
утверждения этого документа не гонялись нигде. Картинка, обещающая то, чего в ней нет, — это
тест, который не может упасть, в виде документа.</p>`)
	banner := fmt.Sprintf(`Из <b>%d</b> кадров нарисовано <b>%d</b>. §20 почти не добавляет
интерфейса, потому что «где я» tmux отвечает громче, чем это сделал бы хаб — остальное
настоящие байты <code>View()</code> и настоящий <code>capture-pane</code> из живого
вложенного tmux.`, total, drawn)
	if drawn == 0 {
		banner = fmt.Sprintf(`<b>Ни одного нарисованного кадра не осталось.</b> Документ начинался
с трёх — единственного нового UI, подсказки в шапке; §20 реализована, и все три перегенерированы
настоящим рендерером. Это и есть работа целевого кадра: быть одобренным, а потом умереть.
Утверждения первых двух перенесены дословно; у третьего оно <b>исправлено</b> — нарисованная
версия показывала подсказку у хаба вне tmux, а вне tmux её не бывает, и кадр проходил проверку
только за счёт этого противоречия. Все <b>%d</b> кадра проверены, из них 9 обычным прогоном
тестов.`, total)
	}
	fmt.Fprintf(&b, `<p class="banner">%s</p></header>
<nav>`, banner)
	for i, s := range secs {
		fmt.Fprintf(&b, `<a href="#f%d">%s</a>`, i, html.EscapeString(s.name))
	}
	b.WriteString("</nav><main>")
	for i, s := range secs {
		fmt.Fprintf(&b, `<section id="f%d"><h2>%d. %s</h2><p class="intro">%s</p>`,
			i, i+1, html.EscapeString(s.name), html.EscapeString(s.intro))
		for _, sh := range s.shots {
			name, expl := sh.origin.label()
			cls, tagCls := "", ""
			if sh.origin == originDrawn {
				cls, tagCls = " is-drawn", " drawn"
			}
			fmt.Fprintf(&b, `<figure class="shot%s"><figcaption>
<div class="t">%s<span class="tag%s">%s</span></div>
<div class="src">%s</div>
<div class="meta">%s</div>
<div class="assert"><b>Кадр неверен, если:</b> %s</div>
</figcaption><pre>%s</pre></figure>`,
				cls, html.EscapeString(sh.title), tagCls, html.EscapeString(name), expl,
				html.EscapeString(sh.did), html.EscapeString(sh.assert), toHTML(sh.body))
		}
		b.WriteString("</section>")
	}
	fmt.Fprintf(&b, `</main><footer>%d потоков, %d кадров, из них нарисовано %d (§20 реализована).
Пересобрать: <code>prototypes/possession-captures.sh /tmp/flows</code>, затем
<code>HUB_FLOW_CAPTURES=/tmp/flows go test -tags mockup -run TestGenerateFlows ./internal/ui/</code>.
Правило, по которому построен документ, и числа за ним — в <code>docs/mockup-authoring.md</code>.
Кадры, которые раньше были нарисованы, теперь настоящие, и их утверждения проверяются:
девять кадров из <code>View()</code> — обычным <code>go test ./...</code>, пять
<code>capture-pane</code> — этим генератором. Сравнить любую пару кадр-против-цели можно
<code>prototypes/framediff.py</code> — расхождение это дефект либо в коде, либо в одобренной
цели, и в обоих случаях не вопрос вкуса.</footer>
</body></html>`, len(secs), total, drawn)
	return b.String()
}
