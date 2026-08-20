import subprocess, os
# reuse tile.py's line classifier without running its demo tail
_here = os.path.dirname(os.path.abspath(__file__))
src=open(os.path.join(_here,"tile.py")).read().split("target = sys.argv")[0]
ns={}; exec(compile(src,"tile","exec"),ns); kind, trunc = ns["kind"], ns["trunc"]

# real data: live1's actual content lines
plain = subprocess.run(["tmux","capture-pane","-p","-t","live1"],capture_output=True,text=True,timeout=20).stdout
content = [l.strip() for l in plain.split("\n") if kind(l) in ("content","spinner")]

PANES = [  # (host, session, pane, cmd, state, content)
 ("local","live1","%0","claude","needs",  content),
 ("nuc","trainings","%3","claude","idle",  ["● Ran 42 tests, 3 failed","⎿  FAIL auth_test.go:88","● Want me to fix them?"]),
 ("nuc","work","%7","claude","works",     ["● Bash(go build ./...)","✻ Brewed for 46s"]),
 ("nuc","work","%8","bash","idle",        ["FAIL pkg/bar 1.2s","ok   pkg/foo 0.4s"]),
 ("eu","worker","%1","claude","quiet",    ["● Waiting on the migration","✻ Churned for 12m"]),
 ("side-desk","dev","%2","claude","error", ["● API returned 500","⎿  connection refused"]),
]
GLYPH={"needs":"⚑","quiet":"✱","idle":"▸","works":"·","error":"✗","gone":"✝"}

def render(W,H,marked=("%0","%3")):
    out=[]
    inbox_w = 26 if W>=100 else W
    tiles_w = W - inbox_w - 1
    body_h  = H - 2                       # one header, one status footer
    rows=[]
    last_sess=None
    for host,sess,pid,cmd,st,_ in PANES:
        key=(host,sess)
        if key!=last_sess:
            rows.append(("hdr", f"{host} {sess}")); last_sess=key
        m = "◆" if pid in marked else " "
        rows.append(("pane", f"{m}{GLYPH[st]} {pid:<3} {cmd:<7}{st}"))
    head = f"tmux-hub  {len(PANES)} panes / {len({p[0] for p in PANES})} hosts"
    out.append(trunc(head, W).ljust(W))
    for i in range(body_h):
        left = ""
        if i < len(rows):
            k,t = rows[i]
            left = t if k=="pane" else t.upper()
            left = trunc(left, inbox_w)
        line = left.ljust(inbox_w)
        if tiles_w > 20:
            # one tile column, tallest pane first
            tl=[]
            sel=[p for p in PANES if p[2] in marked]
            per = max(3, (body_h - len(sel)*2)//max(1,len(sel)))
            for host,sess,pid,cmd,st,cont in sel:
                tl.append(f"┌─ {host} {sess} {pid} ".ljust(tiles_w-1,"─")+"┐")
                for c in cont[-per:]:
                    tl.append("│"+trunc(c,tiles_w-3).ljust(tiles_w-2)+"│")
                tl.append("└"+"─"*(tiles_w-2)+"┘")
            line += " " + (trunc(tl[i],tiles_w) if i < len(tl) else "").ljust(tiles_w)
        out.append(line[:W])
    foot=f"local up · nuc up · eu degraded:old-version · side-desk up   → 2 marked"
    out.append(trunc(foot,W).ljust(W))
    return out

for W,H,label in ((80,24,"80x24  — the size live1 actually is"),(160,32,"160x32 — inbox + tiles")):
    print(f"\n{'='*W}\n### {label}\n{'='*W}")
    for l in render(W,H): print(l)
