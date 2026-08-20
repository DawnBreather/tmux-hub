#!/usr/bin/env python3
"""Score drawn terminal frames against the bytes View() really prints.

Written for the mockup-fidelity benchmark in ../docs/mockup-authoring.md, and
kept because it is the tool that rule needs: a *target* frame (a screen we drew
for a feature that does not exist yet) is only worth approving if, once the
feature ships, the real frame can be diffed against it. Eyeballing two 24x101
screens does not find a column that moved by one.

    python3 framediff.py truth.txt candidate.txt [candidate2.txt ...]

Every number is a diff. There is no judgement here on purpose: the benchmark's
authors included the person reading the scores.

Columns:

    lines   the candidate's line count (the truth's is printed in the header)
    wide    widest line in display columns
    exact   lines identical to the truth at the same index, trailing space included
    exact~  the same, ignoring trailing space
    line~   mean per-line similarity at the same index, 0..1
    whole   similarity of the whole screen as one string, 0..1  <- rank by this
    tok     share of the truth's identifiers (pane ids, names, state words) present
    inv     identifiers the candidate invented that the truth does not contain

Rank by `whole`. `exact` alone cannot tell a blank screen from a real one: an
empty candidate is padded to the truth's length and collects a free match on
every blank row the truth has — measured, a 24-line blank frame scored 11 of 24
`exact` against a real dashboard. Keep a blank file in the comparison as a
control and check that it lands at `whole` 0.000.
"""
import difflib
import pathlib
import sys
import unicodedata


def width(s):
    """Display columns, counting wide East-Asian glyphs as two."""
    return sum(2 if unicodedata.east_asian_width(c) in "WF" else 1 for c in s)


def lines(path):
    return pathlib.Path(path).read_text().rstrip("\n").split("\n")


def content_tokens(ls):
    """The identifiers a reviewer would check: pane ids, names, state words."""
    out = set()
    for line in ls:
        for ch in "│┌└┐┘─":
            line = line.replace(ch, " ")
        for tok in line.split():
            if any(c.isalnum() for c in tok):
                out.add(tok)
    return out


def score(truth, cand):
    n = len(truth)
    padded = cand + [""] * max(0, n - len(cand))
    per_line = [difflib.SequenceMatcher(None, truth[i], padded[i]).ratio() for i in range(n)]
    tt, ct = content_tokens(truth), content_tokens(cand)
    return {
        "lines": len(cand),
        "wide": max((width(l) for l in cand), default=0),
        "exact": sum(1 for i in range(n) if padded[i] == truth[i]),
        "exact~": sum(1 for i in range(n) if padded[i].rstrip() == truth[i].rstrip()),
        "line~": sum(per_line) / n,
        "whole": difflib.SequenceMatcher(None, "\n".join(truth), "\n".join(cand)).ratio(),
        "tok": len(tt & ct) / len(tt) if tt else 0.0,
        "inv": len(ct - tt),
    }


def main(argv):
    if len(argv) < 2:
        sys.exit("usage: framediff.py truth.txt candidate.txt [candidate2.txt ...]")
    truth = lines(argv[0])
    print(f"truth {argv[0]}: {len(truth)} lines, widest {max(width(l) for l in truth)} cols\n")
    print(f"{'candidate':<28} {'lines':>5} {'wide':>5} {'exact':>6} {'exact~':>7} "
          f"{'line~':>6} {'whole':>6} {'tok':>6} {'inv':>4}")
    rows = [(score(truth, lines(p)), pathlib.Path(p).name) for p in argv[1:]]
    for s, name in sorted(rows, key=lambda r: -r[0]["whole"]):
        print(f"{name:<28} {s['lines']:>5} {s['wide']:>5} {s['exact']:>6} {s['exact~']:>7} "
              f"{s['line~']:>6.3f} {s['whole']:>6.3f} {s['tok']:>6.3f} {s['inv']:>4}")


if __name__ == "__main__":
    main(sys.argv[1:])
