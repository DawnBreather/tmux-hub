import subprocess, re, sys, unicodedata

RULE   = re.compile(r'^[─-╿\-=_]{8,}\s*$')
PROMPT = re.compile(r'^\s*[❯>]\s*\S?\s*$')
FOOTER = re.compile(r'(bypass permissions|shift\+tab|for agents|new task\?|/clear to save|tokens\b|esc to interrupt)')
SPIN   = re.compile(r'^\s*[✻✽✢·*]\s')

def kind(l):
    s = l.strip()
    if not s: return "blank"
    if RULE.match(s): return "rule"
    if PROMPT.match(l): return "prompt"
    if FOOTER.search(l): return "footer"
    if SPIN.match(l): return "spinner"
    return "content"

def dwidth(s):
    return sum(2 if unicodedata.east_asian_width(c) in "WF" else 1 for c in s)

def trunc(s, w):
    out, n = [], 0
    for c in s:
        cw = 2 if unicodedata.east_asian_width(c) in "WF" else 1
        if n + cw > w: return "".join(out) + "…"
        out.append(c); n += cw
    return "".join(out)

def tile(lines, w, h, mode):
    if mode == "raw":
        body = lines[-h:]
    else:
        body = [l for l in lines if kind(l) in ("content", "spinner")][-h:]
    body = [trunc(l.strip(), w - 2) for l in body]
    body += [""] * (h - len(body))
    top = "┌" + "─" * (w - 2) + "┐"
    bot = "└" + "─" * (w - 2) + "┘"
    rows = [top] + ["│" + l.ljust(w - 2)[:w-2] + "│" for l in body] + [bot]
    return rows

target = sys.argv[1] if len(sys.argv) > 1 else "live1"
plain = subprocess.run(["tmux","capture-pane","-p","-t",target],capture_output=True,text=True,timeout=20).stdout
lines = plain.split("\n")

for w,h in ((38,8),(56,10)):
    a = tile(lines, w, h, "raw")
    b = tile(lines, w, h, "content")
    print(f"\n### tile {w}x{h}     LEFT: last {h} lines (design v1)      RIGHT: content lines (design v2)")
    for x,y in zip(a,b):
        print("  " + x + "   " + y)

k = [kind(l) for l in lines]
from collections import Counter
print("\nline kinds in the live pane:", dict(Counter(k)))
