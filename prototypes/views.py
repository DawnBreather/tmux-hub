#!/usr/bin/env python3
"""Compare two candidate primary screens from the SAME live data.

The design (§10) makes the inbox the primary: every pane, sorted by how much it
wants you. The open question (§14) is whether that is right, or whether a
"next" view — one pane at a time, the one that wants you most — reads better
when you are orchestrating many agents.

Data comes from `tmux-hub status` so this renders exactly what the hub sees.

    ./tmux-hub status | python3 prototypes/views.py
    ./tmux-hub --host nuc=/run/…/nuc.sock status | python3 prototypes/views.py
"""
import json
import sys

GLYPH = {"needs": "⚑", "error": "✗", "quiet": "✱", "idle": "▸", "works": "·", "gone": "✝"}
RANK = ["needs", "error", "quiet", "idle", "works", "gone"]


def load():
    d = json.load(sys.stdin)
    panes = d.get("panes") or []
    panes.sort(key=lambda p: (RANK.index(p["state"]) if p["state"] in RANK else 9,
                              p["host"], p["session"], p["pane_id"]))
    return d.get("hosts") or [], panes


def inbox(hosts, panes, w=78):
    out = [f"tmux-hub  {len(panes)} pane{'' if len(panes) == 1 else 's'}"]
    for p in panes:
        out.append(f" {GLYPH.get(p['state'],'?')} {p['command']:<8} {p['state']:<6} "
                   f"{p['host']}/{p['session']} {p['pane_id']}"[:w])
    top = panes[0] if panes else None
    if top:
        out.append("")
        out.append(f"┌─ {top['host']} {top['session']} {top['pane_id']} ".ljust(w - 1, "─") + "┐")
        for l in (top.get("content") or [])[-6:]:
            out.append("│" + l[:w - 3].ljust(w - 3) + "│")
        out.append("└" + "─" * (w - 3) + "┘")
    out.append(" ".join(f"{h['label']} {h['status']}" for h in hosts))
    return out


def nextview(hosts, panes, w=78):
    if not panes:
        return ["nothing is waiting for you."]
    p = panes[0]
    waiting = [q for q in panes if q["state"] in ("needs", "error")]
    out = [f"{GLYPH.get(p['state'],'?')} {p['state'].upper()}   "
           f"{p['host']}/{p['session']} {p['pane_id']}  ({p['command']})",
           ""]
    for l in (p.get("content") or [])[-12:]:
        out.append("  " + l[:w - 2])
    out += ["",
            f"  {len(waiting)} waiting · {len(panes)} panes total",
            "  n next · a attach · i send · l list"]
    return out


def main():
    hosts, panes = load()
    a, b = inbox(hosts, panes), nextview(hosts, panes)
    print("=" * 40 + " INBOX (the design's §10) " + "=" * 14)
    print("\n".join(a))
    print()
    print("=" * 40 + " NEXT (the alternative)  " + "=" * 14)
    print("\n".join(b))


if __name__ == "__main__":
    main()
