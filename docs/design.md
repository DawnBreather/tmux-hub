# tmux-hub — design

Status: agreed in brainstorming, then corrected three times — a measurement pass against live tmux
3.7b and 3.2a, an adversarial pass on the read path, and a safety pass on the write path. Each
broke mechanisms this document had already recorded as guarantees; §11 and §12 are the current
boundary between the two. §16 states the experience commitments, each backed by a measurement
rather than an intention.

**All write-path measurements are 3.7b only.** The safety pass had no ssh access, so nothing about
`send-keys`, `paste-buffer`, pane options or the guard has been re-measured on 3.2a. Re-measure
before implementing §7.

Date: 2026-08-11

## 1. Purpose

A TUI control panel over every tmux session the user can reach — local and remote — built for
**orchestrating many Claude Code sessions** mixed with plain shells.

The driving need is *conducting*: send input to several sessions, and know which one needs the
user next. **The unit is a Claude session, not a tmux pane** — most are hosted in a pane, but a background
agent has no pane at all and reports its own state as a fact. §17 covers that surface; **§22 is what
makes those rows actionable**, by opening a pane on demand (`claude attach <id>`, run in a tmux
session the hub creates on that row's own host). Two things follow. Most of the waiting lives there —
**57 of 65 rows** are `background` — 87% — measured 2026-08-16 with `claude agents --json --all` on all
three claude-bearing hosts in one snapshot (§22.1) — and the hub can give
such a session a **terminal** but cannot **send** it text: `i` on an agent row is deferred (§22.4),
so a blocked agent is answered by hand in the pane the hub just opened. Not "connect to a remote tmux" (`ssh -t host tmux attach` already does that), and
not session management (`sesh`, `tmuxinator` already do that). The gap is a single view of
sessions across hosts, with attention state and multi-target input.

## 2. Non-goals

- No terminal emulator. Overview is snapshots; real work happens in a real `tmux attach`.
- No agent installed on remote hosts. ssh + tmux are already there — and where the hub reaches
  Claude sessions, so is the `claude` CLI (§3 measured one host, `eu`, with tmux and none, and the
  producer tolerates it). The hub installs nothing and runs no process of its own on a far side, but
  exec'ing `claude attach` **starts Claude's daemon if it is cold** — measured inside the
  0.33–0.95 s that call costs (§22.3). That is a side effect of an explicit keypress, never of
  observing.
- No custom network protocol, no TCP listener, no daemon **of the hub's own** — and no client of
  anyone else's protocol either. Claude Code does run a daemon behind the sessions the hub reaches
  (`/tmp/cc-daemon-$uid/sha256(realpath($CLAUDE_CONFIG_DIR))[:8]/control.sock`, NDJSON, protocol 1,
  18 operations), and the hub depends on it for two things: the roster that is the only liveness
  signal for a paneless session, and the door `claude attach` opens. It **execs the verb and never
  speaks the protocol** — the CLI reads `$CLAUDE_CONFIG_DIR/daemon/control.key` (0600, compared with
  `timingSafeEqual`) on the caller's behalf, while opening the socket directly answers `EAUTH`, so
  speaking it would mean holding a per-host secret read over ssh. That is why `i` on an agent row is
  deferred rather than built (§22.2, §22.4): this line is the reason, not an obstacle.
- No "wait for completion". An interactive process has no completion; the dashboard is the report.
- No scripted/headless broadcast API in v1. Targets are chosen in the UI, on screen.

## 3. Measured foundations

Everything below was measured on tmux 3.7b (local) and 3.2a (remote host `nuc`) during design.
These are the facts the architecture rests on; each is load-bearing.

### Transport

| fact | consequence |
|---|---|
| tmux speaks only `AF_UNIX`. `-S tcp:h:p` is treated as a file path; `inet` strings in the binary come from statically linked libevent | no remote access without an external pipe; ssh is the pipe |
| a pure byte relay of a tmux socket carries `list-panes`, `capture-pane`, `send-keys` | the whole dashboard + broadcast path needs only a byte pipe |
| the same relay does **not** carry `attach` / `-C` — an attached client passes its terminal fd over `SCM_RIGHTS` | attach must go through `ssh -t`, not the forwarded socket |
| one ssh process provides both ControlMaster (`-M -S ctl`) and a unix forward (`-L sock:remote`) | one TCP connection, one handshake, both capabilities |
| median latency: local socket **2 ms**; via forward **485 ms**; fresh `ssh host tmux …` **2087 ms**; exec via existing master **506 ms** vs **3217 ms** fresh | forward for polling, master for attach; never a fresh ssh per call |
| batching N commands in one invocation: one round trip + ~230 µs/command locally | batch per host per tick, but bound the batch |
| parallel `tmux -S sock …` invocations over one forward do not corrupt each other | a worker pool is safe |
| a stale local forward socket makes ssh **refuse** to bind (it neither reuses nor clobbers) | startup must probe-then-reap, not blindly unlink |
| a **stalled** connection does not stay stalled: measured through a relay that held the TCP open and stopped forwarding, `ServerAliveInterval=5` with `CountMax=3` made ssh exit in **19.5 s** (`rc=255`, `Timeout, server … not responding`) — so the spec's 15 s interval means ~45-60 s. During that window a tmux call can hang, which is what the per-call deadline is for | the keepalive turns an indefinite stall into a bounded one; the deadline covers the window before it fires. Measured on a forward, but the mechanism is ssh's connection rather than the forward, and §5's master carries the same `ServerAliveInterval=15` — so this row is still live guidance for the master, and it is the one `run.go`'s deadline comment points at |
| after the drop the **socket file is left behind**, and `tmux -S <it> ls` then fails in **8 ms** with `no server running` — the same words as a reachable host whose tmux is simply not started | so the most realistic failure produces tmux's most misleading message, and the dial gets it right: nothing listens on the stale path, so `ECONNREFUSED` reads as "the tunnel is down" rather than "no server over there" (§5) |
| `ServerAliveInterval=15` + default `ServerAliveCountMax=3` drops the tunnel after ~45 s unresponsive — verified at a 5 s interval, where ssh exited after **19.5 s** of a real stall | tunnel loss is the *normal* outcome of a blip; reconnect is routine, not an error |
| `ExitOnForwardFailure` covers the local bind only; `ssh -O check` proves the master lives, not that the forward works | health is only ever the positive tmux probe |
| `tmux -S sock ls` returns **rc=1 "no server running"** on a live server with zero sessions; killing the last session tears the server down | `ls` cannot be the readiness predicate |
| `tmux -V` succeeds with no server running | it is a valid host-membership probe |

### The incident (why the capability gate exists)

Querying `#{client_activity}` or `#{client_created}` on **tmux 3.2a with no attached client
segfaults the entire server**, destroying every session on it. No guard idiom helps —
`#{q:…}`, `#{?…,y,n}`, `x#{…}y`, `#{t:…}` all crash. With a client attached they are fine.
Other `client_*` variables (`client_name`, `client_tty`, `client_pid`, `client_width`, …) are
harmless.

This happened for real during the measurement pass and killed two live Claude Code sessions on
`nuc`. It establishes the rule that shapes the whole transport: **a read can destroy a server**,
so "read-only" is not a safety property here.

### Polling

| fact | consequence |
|---|---|
| `#{history_size}` and `#{history_bytes}` are blind to in-place redraw — a Claude spinner writing 10×/s leaves both frozen while `#{window_activity}` advances every sample | so `history_size` cannot be the activity signal ALONE — and `window_activity` cannot be it either, being per-window (§12). Per-pane activity is the OR of three: `history_size`, `cursor_y`, and a changed zone, which is what sees the redraw this row is about |
| `#{window_activity}` is a unix timestamp of last output; an idle pane's value stays frozen (verified negative pole) | "quiet for N s" is `now - window_activity`, a duration, not a counter diff |
| `capture-pane -S -8` returns 8 lines of scrollback **plus the entire visible screen** | the zone must be an explicit absolute range, anchored at `#{cursor_y}` (see §6) |
| capture on an ALT-screen pane returns the live alt screen; with `-S` it glues unrelated pre-app scrollback on top; `-a` returns the pre-app screen and silently ignores `-S`; `-J` truncates logical lines to width in ALT panes | ALT panes are captured with no `-S`, never with `-a`, and never fed to `classify()` |
| Claude Code is **not** alt-screen (`#{alternate_on}` = NORM) | its tiles are honest captures — the primary use case is the easy case |
| Claude's spinner glyph and "Churned for Ns" **persist in scrollback** of an idle pane | never key `works` on the spinner — it would pin every finished session as working |
| the literal `esc to interrupt` **does not exist** in the Claude Code 2.1.227 bundle: the hint is composed at render time from a keybinding chord (a mode named `interrupt`/`interrupt_agents` plus `keyCase:"lower"`), while `Do you want to proceed?` and `indicator:"bypass permissions"` **are** real literals | `works` is keyed on the ACTIVITY DELTA, which is structural and version-independent; the rendered hint is corroboration only. `needs` may key on its literal, which is the most reliable marker available |
| `pane_last_activity`, `pane_activity`, `pane_written`, `pane_bytes` are **absent from the 3.7b binary**; `pane_unseen_changes` exists but tracks an attached client's viewing state and stays 0 for a detached hub. `history_bytes` is per-pane but blind to in-place redraw (a `\r` spinner at 10 Hz left it at 500 for 7.5 s while `window_activity` ticked every sample) | tmux offers no per-pane activity FIELD, which is true and was read as "there is no per-pane activity signal" — one field short of the answer. The hub keeps its own, because the thing that sees a redraw is not a field at all: it is the CAPTURE, which is fetched every tick anyway, so comparing this tick's zone with the last is free. `history_size` covers output that scrolled away, `cursor_y` covers movement on screen, the zone covers a redraw that moves neither |
| **the whole remote path, measured end to end on a live host** (`nuc`, tmux 3.2a, over ssh): tunnel ready in **2.22 s** waiting on the fact; `SO_PEERCRED` on the dial returned **exactly the ssh pid the hub spawned**; the 9-field delta over the forward **501 ms**; the identity probe returned `nuc-dev\|3.2a\|…\|/tmp/tmux-1000/default`, so both corroborating fields differ from local | §5's transport and its identity mechanism are verified, not inferred |
| **attach cannot go over the forwarded socket, even from a real pty** — measured, `tmux -S <forwarded> attach` fails `open terminal failed: not a terminal`, while `ssh -S <ctl> -t host tmux attach` created a real client on the remote server | a remote attach MUST use the master; a hub that builds the local form for a remote pane fails on every remote pane, and blames the terminal while doing it |
| **ssh joins its command arguments into one string and hands that to the REMOTE user's shell**, which expands the target before tmux sees it. Measured over a live control master: `ssh -S <ctl> nuc 'echo -t $0'` printed `-t bash`, and the argv the hub built — `ssh -S <ctl> -t nuc tmux attach -t $0` — failed `can't find session: bash`, rc=1, 0 clients, while `… -t '$0'` attached: 1 client, and the remote server's own status line on screen | the remote attach TARGET is shell-quoted (`internal/ui`'s `shellQuote`). This is a SECOND shell and it is not the one `shellJoin` answers to — that one is on THIS machine, between tmux and the payload. The remote path has both, and only the near one was handled: remote attach had never worked, for any session |
| **A QUOTED PROGRAM NAME IS LEGAL POSIX AND A PARSE ERROR IN A SHELL THAT IS NOT.** ssh hands the command to the remote user's LOGIN shell, and a new host on this fleet was a MacBook whose login shell is Nushell. Measured over a live master: the payload the seam built, `'tmux' 'list-panes' '-a' '-F' '#{pane_id}'`, answered `Error: nu::parser::parse_mismatch` — and answered it at **rc=0**, so the hub read a poll that had succeeded and found no panes. With the program name bare, the same shell runs it and tmux answers `no server running on /private/tmp/tmux-501/default` | the remote payload quotes every ARGUMENT and leaves the program NAME bare (`ShellJoinCommand`). The name is a literal this program chooses, so there is nothing in it to expand; the arguments are where that risk lives and they stay quoted. A name that is not a bare word is quoted anyway, because that direction merely fails on an exotic shell while the other would be an injection. The host had sat at `connecting` for as long as it was enabled, which is the cost of a remote failure that returns rc=0 |
| **the outer tmux takes the prefix.** Measured with a real client on a pty and both servers on the default `C-b`: `C-b d` detached the **OUTER** session, leaving the nested one attached; `C-b C-b d` (send-prefix) reached the inner; and with the outer session on `C-Space`, plain `C-b d` reached the inner | a distinct prefix works only when the hub CREATES the session. Running inside the user's own tmux — the common case — it cannot change their prefix, so the hub must SAY that leaving an attached session takes `C-b C-b d`, before `a` is pressed |
| a session **name** does not survive a rename (`has-session -t <old>` → rc=1 immediately after), while `#{session_id}` (`$N`) still resolves | attach targets the session id; a name is only a fallback |
| classifying a socket as **live** can only be done by waiting to NOT receive an EOF | the dial is a diagnostic, never a precondition: on the happy path it added its full timeout per host per tick |
| the batch's label query is `list-panes -a`, which returns **every** pane | the label reader no longer counts anything — one self-framing record per pane (§6) — so a filtered delta list cannot shift it. It once could: the reader expected one line fewer per label block and misread the next block's first line as a frame, observed live as `bad frame line "%1|zsh"`. The ZONE and FULL readers still frame by a count derived from the delta, so exclusion still happens after the batch |
| the six mode indicators are a **closed set of literals** in the bundle — `manual mode`, `plan mode`, `accept edits`, `bypass permissions`, `don't ask`, `auto mode`, each with a symbol and colour | the footer is recognisable in every mode; knowing only two made the footer line classify as CONTENT in the other four and leak into every tile |
| `isSidechain` is **0 of 1890** records in a session that ran many subagents — a Task fan-out's work lives in its own `subagents/agent-*.jsonl`, not in the parent transcript | "one pane, one state" holds: while a subagent runs the parent sits on `stop_reason: tool_use`, so the pane reads `works`, which is what it is. The pane cannot name WHICH subagent, and the operator does not act on subagents |
| **a `SessionStart` hook fires and its line carries the whole join.** Verified end to end with a project-local `settings.json` in a throwaway directory: the hook ran, `$TMUX_PANE` was `%0` — exactly the pane the session runs in — `$TMUX` gave the socket, and the stdin JSON carried `cwd, hook_event_name, model, session_id, source, **transcript_path**` | §17's level 2 needs no correlation scheme at all: one hook line names the pane, the socket, the session and its transcript. `transcript_path` in the payload closes pane → session → transcript in a single step |
| the session does not start until a chain of local dialogs is answered — the trust dialog, then MCP-server approval — and **`SessionStart` fires only after them** | a pane can be `needs` before any session exists, and a hook cannot report that. The screen is the only source for the startup chain |
| Claude renders at least **two** dialog shapes: a numbered choice (`❯ 1. Yes, I trust this folder`) and a multi-select checkbox list (`❯ [✔] context7`, footer `Space to select · Enter to confirm`) | matching only the numbered one missed the MCP-approval dialog entirely — a session waiting on the user before it has started |
| **a real Claude choice prompt renders `❯ <n>. …` with the cursor ON the selected option** (`cursor_y=17` of a 30-row pane) and `alternate_on=NORM`. Captured free from its own trust dialog, which needs no API call | `needs` keys on that SHAPE, not on a digit at line start — the earlier `^\s*1\.` missed every real prompt because the selection cursor precedes the number. It is also why the zone is cursor-anchored |
| the footer `Enter to confirm · Esc to cancel` is **composed at render time**, while `Do you want to proceed?` and `Yes, I trust this folder` **are** literals in the bundle | key on the shape, corroborate with the literals, never rely on the footer |
| the state machine's temporal behaviour, measured end to end on a pane that printed, waited, then failed: `works` at 2 s → `error` at 6 s; a pane that printed once goes `works` → `idle` at 6 s → `quiet` at 100 s | a transient `works` on a freshly-failed pane is correct, not a defect: it self-corrects within one freshness window |
| **`ExitOnForwardFailure` is load-bearing, not hygiene.** Measured: with a plain file in the socket's place ssh exits `rc=255` (`unix_listener: cannot bind … Address already in use / Could not request local forwarding`); **without** the option ssh stays UP with a dead forward, and `tmux -S <that path> ls` then answers `no server running` — the same words as a reachable host with no tmux | a silently forwardless master is the worst state available, because tmux's own message makes it look like an empty host |
| **unlinking a LIVE forwarded socket orphans its ssh.** Measured: the process survives, the path becomes undialable, and a new tunnel binds it happily while the first ssh runs on forever | so "unlink before spawning" must be **probe-then-act**: dial first, adopt a live tunnel, unlink only what is dead — and kill the owning ssh by the pid the hub persisted. Unconditional unlinking leaks one ssh per restart |
| a live forwarded socket **can** be dialled by a second process, so adoption is real; a second `ssh -L` on the same path gets `rc=255` | reconciliation must adopt rather than re-spawn |
| `flock(LOCK_EX\|LOCK_NB)` refuses a second holder with `EAGAIN` and grants it once the first closes | the single-instance rule holds across processes, verified |

**The five rows above about forwarded sockets are history, not instructions.** Every measurement in
them stands; the design they were measured for does not. §5 no longer forwards the remote socket —
one ssh master carries both the polling and the attach — because a real poll cycle costs **698 ms**
through a forward against **327 ms** as one invocation over the master, and the master is required
for `attach` regardless. So `ExitOnForwardFailure`, unlink-versus-adopt, the orphaned-ssh leak, the
`EADDRINUSE` retry loop and the `start-server` squatter all describe a mechanism the hub does not
have. They are kept because a design that deletes the record of what it rejected invites the
rejection to come back.

| with `XDG_RUNTIME_DIR` unset, `filepath.Join("", "tmux-hub")` is `tmux-hub` — **not absolute** | the startup assertion is required, not defensive |
| non-Claude prompts: `[y/N]` and `(y/n)` read `needs`; a bare `Rebase onto main? Proceed?` did **not** until a positional rule was added; a REPL at `>>>` reads `idle`, which is the right meaning ("ready for the next thing"); a `Password:` prompt is not detected | `needs` also fires when the zone's LAST non-blank line ends in `?` — position is what keeps that precise, since a question at the cursor is being asked while the same words earlier are just output |
| **a git host does not fail — it hangs.** Measured spawn observables: DNS miss `rc=255` in 3.4 s (`Could not resolve hostname`), auth refusal `rc=255` in 0.14 s (`Permission denied (publickey,password)`), and `github.com` **neither**: it accepted the connection, authenticated with the user's key, and `ssh -N -M -L …` sat there past 40 s with a live tunnel to nowhere, because `ExitOnForwardFailure` only covers the LOCAL bind | the concrete case behind "process liveness is never health": a spawn that succeeds proves nothing at all |
| the membership probe keyed on **stdout** was verified on three real hosts: `nuc` rc=0 with `tmux 3.2a`, `hermes-ws` **rc=0 with no version** (`command not found: tmux`), `github.com` rc=1 | an rc-keyed probe admits `hermes-ws`, which has no tmux; `^tmux \S+` in stdout excludes it |
| the wildcard and unroutable `Host` patterns (`.host`, `machine/.host`, `unix/*`, `vsock%*`) are **not** in `~/.ssh/config` — they live in a systemd drop-in reached through the SYSTEM config's `Include /etc/ssh/ssh_config.d/*.conf`. This machine's own config is 15 `Host` lines → **20 names** after expanding one multi-name line, with no wildcards, no `Match` blocks and no `Include` | a parser reading only the user's config never sees the junk; one reading the system chain must filter it. Either way the positive `tmux -V` probe eliminates it |
| one host is reached through `ProxyCommand docker exec -i … nc %h %p` | a non-trivial transport the hub inherits for free by using ssh, and a reason not to reimplement reachability |
| the 10-field delta format, **including `#{cursor_y}` and `#{session_id}`**, works verbatim on 3.2a | the zone anchor is not a 3.7b-only feature |
| **each invocation is a round trip**: one label query 501 ms, one full capture 513 ms. Issuing them separately made a tick cost 4+N round trips — measured 4.22 s for two cycles — against **1.38 s** per cycle once labels, all zones and the wanted fulls went into one batch | a tick is **two** round trips per host: the delta, then everything else |
| `#{window_activity}` advances for an in-place spinner **on 3.2a too** (645 → 647 → 650 with `history_size` frozen at 0, while an idle pane's stayed put) | the delta-based `works` signal is not version-specific |
| **`#{n:X}` works on 3.2a**: `#{n:session_name}` for a session named `p` returned `1`, while an unknown variable returned empty in the same invocation | the length-framed label format (§6) is safe on the older half of the fleet. This had to be measured rather than assumed, and the failure would have been total: tmux answers an unknown variable with an EMPTY field at rc=0, so on a version without `n:` every length would parse as "" and every remote host would go dark. Confirmed end to end by the remote label test against a real 3.2a host |
| `FreshFor` must exceed the tick duration, or a slow tick makes a working pane read `idle` — observed at a 2.1 s cycle with a 4 s threshold, corrected by the batching above | the freshness window belongs to the poll interval, not to a constant |
| `window_bell_flag` is set when a pane rings the bell, and `monitor-silence` makes tmux itself flag a silent window | two attention signals tmux already computes, which the hub can read instead of deriving |
| `allow-passthrough` defaults to **off**, so an OSC 9 desktop notification written by a pane is swallowed by tmux | an out-of-band `needs` signal cannot rely on OSC 9 (§14.2); the terminal bell and `display-message` do work |
| `needs` and `idle` are both invisible to every delta variable (activity frozen in both) | a delta-gated capture is slowest to notice the highest-priority state — so there is no gate: range capture makes capturing every pane every tick affordable (§6) |
| a real 80×24 Claude pane holds **6 content lines out of 25**, and the bottom 10 are pure chrome (rules, empty prompt, constant footer) | a "last N lines" tile shows no information; the tile is derived from content lines, and it is a *different* window from the classification zone |
| `capture-pane -S <h-6> -E <h-1>` returns exactly those lines, identical to the slice of a full capture, at **18 %** of the bytes | the classification zone is cheap enough to take for every pane, every tick |
| a batch framing + capturing **80** panes costs 5.1 ms of tmux time and one round trip; a realistic ANSI pane is 3614 B (1.59× plain) | tmux is never the bottleneck; bytes on the wire are, which is what the zone bound fixes |
| a batch of `capture-pane` is **not** framed — tmux concatenates screens with only newlines | the hub must emit its own delimiter between captures |
| a tmux batch **aborts at the first failing sub-command**: `send-keys -t %999 … ; send-keys -t %0 …` delivered nothing to `%0`, rc=1 | delivery must be a per-target fact, never the batch rc |
| a pane whose process exits is destroyed with its scrollback before the next poll (`remain-on-exit` off by default) | an `error` state read from a capture reads evidence that no longer exists |
| a session named `a\|b` shifts every field of a `\|`-delimited format string, silently | field order and bounded parsing, not a rarer delimiter |

### Broadcast

| fact | consequence |
|---|---|
| `send-keys -t <session>` delivers to whichever pane is **active** — proven: the intended pane got nothing, another pane got the text, rc=0 | target only `#{pane_id}` (`%NN`) |
| `#{pane_id}` is per-server and restarts at `%0` after a server restart | `%NN` is stable within a server lifetime, **not** across a reconnect |
| `send-keys -l` silently truncates text **ending** in `;` (`'done;'` → `done`; `';'` → empty), rc=0; mid-string `;` is safe; escaping is asymmetric and unusable | the text primitive is `send-keys -H <hex>`, immune, present in 3.2a and 3.7b |
| without `-l` the word `Enter` becomes a keypress; `-l`/`-H` apply to **all** arguments | a trailing Enter is always a separate invocation |
| Claude Code enables bracketed paste (`#{bracket_paste_flag}` = 1); `send-keys -l` with an embedded newline **submits early** — paragraph one executes | multi-line goes through `paste-buffer -p -r` |
| `paste-buffer` needs **both** flags: `-p` wraps in bracketed-paste markers, `-r` stops LF becoming CR. Verified both poles: with `-p -r` nothing executes until Enter; without `-p` the line executes immediately | both flags are load-bearing, not stylistic |
| named buffers persist server-wide until `delete-buffer` | the buffer is deleted in the same batch that pastes it |
| **`if -F` with no `-t` reads the pane option from the server's CURRENT pane.** With `%0` stamped and `%1` not, `if -F '#{==:#{@h},<uuid>}' 'paste-buffer … -t %1 ; display -p -t %1 "OK #{pane_id}"'` printed `OK %1` and pasted into `%1`; `if -F -t %1 …` on the same state answers `REFUSED` | the `if` needs its own `-t`, separately from each sub-command's. Neither implies the other, and the failure is a fail-open that reports success |
| **`#{window_activity}` cannot advance inside the invocation that writes.** Three consecutive guarded sends read the identical timestamp before and after the paste (`B 1786487832 / A 1786487832`); a separate read at **+50 ms** already showed it moved. `#{history_size}` stayed `0` across a delivery that was plainly on screen | the delivery witness is a SECOND read, never the same batch. A same-invocation witness reports every delivery as unwitnessed, which is worse than none |
| `paste-buffer` **does** deliver from inside an `if -F` sub-command chain (verified by capturing the target pane, not by the confirmation) | the guard idiom and the text primitive compose; the confirmation alone was not evidence and had to be checked against the pane |
| **The whole write path, verified against a live Claude Code pane** (throwaway session, private socket): `#{bracket_paste_flag}`=1; a three-line payload ending in `;` landed **whole** in the input box (`❯ first line…` with the rest indented), **nothing submitted**, no work markers; `window_activity` `1786489714` → `1786489766` on a later read while `BEFORE`/`SENT` inside the batch both read `1786489714`; `paste-buffer -d` left no buffers | `load-buffer -` + `paste-buffer -d -p -r` is correct for the real target, both witnesses work on it, and the same-invocation witness is confirmed useless against an agent and not only against `cat` |
| **`pane_current_command` reports `claude`, not `bash`** — in both launch shapes: as the pane command (`pane_pid` *is* claude) and started from a shell (`pane_pid` is the shell, claude is a child) | corrects §7's original justification. The field is still not the key: it names the FOREGROUND process, so it becomes `bash`/`git` when the agent shells out, and it cannot separate an interactive agent from `claude bg-pty-host` |
| **In the pane-command shape `#{pane_pid}` IS the claude process**, so identification that walks only DESCENDANTS finds nothing — measured against a live session as "not identified", with a plain `sleep` correctly refused by both forms | the walk must test the root itself before its descendants; a descendants-only walk is blind to `tmux new-window claude` |
| `respawn-pane -k` keeps the pane id and the `@hub_*` option but **replaces `pane_pid`** (`702400` → `702406`) | the token must rotate because the OPTION survives, not because the pid does; a `pane_pid` change is itself a usable respawn signal |
| a server restart changes `#{pid}:#{start_time}` (`702399:1786489650` → `702664:1786489651`) and the new server's first pane is **`%0` again** | the server epoch is a sound selection invalidator, and pane-id recycling after a restart is real rather than theoretical |
| **`less` with `#{bracket_paste_flag}`=0 turned a pasted three-line prompt into KEYSTROKES** — it opened its own help screen. A payload containing `q` would have quit it; `!cmd` runs a shell command. `bash`, `vim` and the python REPL all reported 1 and took the same payload as inert text with nothing executed | the flag is the only available signal for whether a send is SAFE, and it is now part of the delta. A 0 is a reason to CONFIRM, not a refusal, and a 1 is not a guarantee either — vim in a modal swap-file dialog consumed the paste |
| **the poll had a hard ceiling at ~61 panes**: 60 worked, 62 failed with tmux's own `command too long`, and because a batch aborts at its first failure the tick returned NOTHING — so a healthy host read **`down`**. After chunking at 40 panes per invocation: 150 panes cost 29 ms for zones and 45 ms with every full capture | "one round trip whatever the pane count" was false above 61 panes and is now `1 + ceil(n/40)`. A hub-side argv-length problem must never present as a host being unreachable |
| pane-count scaling after the fix, local socket: 60 panes 11.6 ms, 75 panes 12.9 ms, 100 panes 17.0 ms, 150 panes 29.5 ms (zones only) | the 1.2 s poll interval has two orders of magnitude of headroom locally; the constraint was never time, it was argv length |
| **a narrower attach does NOT discard scrollback, and the reflow is reversible.** A 200-wide session at 1933 of a 2000 `history-limit`, attached at 100: oldest line unchanged, `history_size` inflated to **3113** — past the limit — with no eviction. A line went 201 bytes → 101 → **201** when a wide client attached again (`history_size` 139 → 248 → 140) | §8's modal warning was built on a claim that is false in every part. What IS true: the session stays at the narrow width after detach (`window-size latest`), so the hub restores it instead of warning |
| **The transport, verified end to end against a real host** (`nuc`, tmux 3.2a, private remote socket): the socket file appeared at **+1000 ms** and the dial was accepted-and-held at the same moment, with `tmux -S` answering `3.2a` — the REMOTE version, which is what proves the forward reaches the far end | readiness is a dial that is accepted **and held**, plus a tmux answer. The version is the assertion that the socket is not a local squatter |
| pointed at a path with **no remote server**, the same tunnel came up, the file appeared, and the dial was **accepted then closed** while `tmux` said `server exited unexpectedly`. `ExitOnForwardFailure=yes` did **not** make ssh exit, because the LOCAL bind succeeded — the remote connect fails later, per channel (`channel 2: open failed: connect failed`) | `TransportEmpty` is a real, reachable state and not a synthetic one, and `ExitOnForwardFailure` covers the local bind only. A host with no tmux server is distinguishable from an unreachable one |
| a second `ssh -L` on a **live** forwarded path: `unix_listener: cannot bind to path …: Address already in use` / `Could not request local forwarding.` while the first master and its tunnel stayed healthy | a live socket must be ADOPTED, never rebuilt: the rebuild cannot succeed |
| **unlinking a live forwarded socket does not stop its ssh.** Verified on a real host: the file was removed, the master stayed alive (`ssh -O check` → `Master running`), the path became undialable, and a new `ssh -L` then bound the freed path while the old master kept running — one leaked process per restart | this is why reconciliation probes before it acts. It was the plan's central assumption and it is now measured rather than assumed |
| `ssh -S <ctl> -O check` reports the pid that **owns the control socket**, which need not be the process the hub spawned: with two masters sharing one `ctl` path, `-O check` named the second while the recorded pid was the first. A `SIGTERM` to the recorded pid reaped that one and left the other running | reconciliation must survey the **ctl** path as well as the socket, and a spawn must refuse a ctl path that already has a live master. Killing by recorded pid is correct for our own child and says nothing about a stranger on the same path |
| **Claude Code renders its prompt as `❯` followed by U+00A0**, a no-break space — and Go's regexp `\s` is ASCII-only, so it does not match it. Measured through the real classifier on a live capture: `❯<NBSP>[✔] context7` (the MCP approval dialog) classified **`quiet`**, and `❯<NBSP>1. Yes, I trust this folder` passed only because a LITERAL matched, not the shape | fold every Unicode space to ASCII at the seam (`lines.Normalize`) rather than patching one pattern. A shape-based rule that silently depends on the space being ASCII is a rule that fails on rewording *and* on the real screen |
| the same live capture, read through the real `ZoneRange`: `cursor_y=19` on a 24-row pane gives the zone `14..19` **inclusive**, whose last line IS the input box | cursor anchoring works as designed; the bounds are inclusive because they feed `capture-pane -S/-E`, and reading them as a Go slice range is an off-by-one that makes the zone miss the prompt |
| a 24-character prefix of pasted text stayed contiguous on one screen line at widths **100, 60 and 40** — including an unbreakable 70-character path, since the wrap happens past the prefix | the screen witness holds down to ~30 columns; it needs only that the prefix fit the pane's usable width, which the hub's own layout already guarantees (below 100 columns it renders the inbox, not tiles) |
| **`window_activity` confirmed only 2 of 6 back-to-back sends** (read at +250 ms, tmux 3.2a) while the payload was on the pane **6 of 6**; with a ≥1 s gap before the send it does advance | the SCREEN is the primary witness and activity the secondary. One-second resolution is not a corner case for a broadcast — writing to several panes inside one second is the normal path |
| a single invocation `display -p -t %NN 'ACT …' ; capture-pane -p -t %NN` returns the activity line first, 6 of 6 | both observables cost one round trip together; the parse still keys on the marker, not the position |
| the `if -F` **without `-t`** fail-open reproduces identically on tmux **3.2a** (`OK %1`, payload delivered to the unstamped pane) | the guard's `-t` is a version-independent requirement, not a 3.7b quirk |
| `display -p 'LIT-%0'` returns `LIT-%0` **on 3.2a** but comes back **empty on 3.7b** | the no-literal-`%` rule must be enforced by the hub for every host, since a template that works on one server silently returns nothing on another |
| `show -gv @hub_x` prints nothing on 3.2a where 3.7b says `invalid option`; both are "not set globally" | an option-invisibility check must accept either, and key on the empty value rather than on the error text |
| remote prerequisites, measured: `nuc` has python3, claude and tmux 3.2a; `eu` has python3 and tmux 3.2a but **no claude** | the process-walk script's `python3` dependency holds on both, and the agents producer must tolerate a host with no claude — which its `command -v claude \|\| echo '[]'` already does |
| `send-keys` works on a pane with no attached client | detached sessions are first-class |

### Lifecycle

| fact | consequence |
|---|---|
| `new-window -t <sess> -c <dir> -P -F '#{pane_id}' '<cmd>'` | returns the new pane's id on stdout, e.g. `%2` |
| `new-session -d -s <name> -c <dir> -P -F '#{pane_id} #{session_name}'` | returns `%3 my proj` — works with a space in the session name |
| `-c` with a space in the path | works; it is its own argv element, no quoting needed |
| `new-window -c /nope-does-not-exist` | **rc=0, and the pane is CREATED with cwd `$HOME`**. Measured on tmux 3.7b: a window asked for a nonexistent path inside a session created with `-c /tmp` landed in `/home/dev`. tmux neither rejects a nonexistent `-c` directory nor warns. By contrast `kill-window -t @999` DOES return rc=1 with `can't find window: @999` |
| a command with quoted arguments, as one string | works: `'sh -c "echo hello world; sleep 302"'` → screen shows `hello world` |
| is a pane id reused after the pane dies? | **no** — killed `%3`, next pane was `%4`. Monotonic within one server's life |
| `respawn-pane -k -t %N '<newcmd>'` | keeps `pane_id`, keeps `@hub_*` options, keeps cwd; `pane_pid` changes (222976 → 222983) |
| `remain-on-exit on`, command exits 7 | pane **stays**: `#{pane_dead}=1`, `#{pane_dead_status}=7`, listed by `list-panes -a` |
| `remain-on-exit off` (default), command exits | pane is destroyed; `display -p -t <gone>` → **rc=0, empty** |
| `display -p -t %999 '#{pane_id}'` (never existed) | **rc=0, empty** — tmux does not error with no client attached |
| `#{pane_start_command}` | quoted in output (`"sleep 301"`); immutable for the pane's life, changes only on respawn |
| `#{pane_current_command}` as a persisted key | unusable — walks `zsh` → `claude` → `zsh` |
| `claude --session-id <fresh-uuid> -p …` | rc=0; transcript lands at `~/.claude/projects/<slug>/<uuid>.jsonl` |
| `claude --session-id <existing-uuid>` | **rc=1**, `Error: Session ID <uuid> is already in use.` |
| `claude --resume <uuid> -p …` | rc=0, continues that conversation (transcript grew to 32 lines) |
| `claude --permission-mode` choices | `acceptEdits`, `auto`, `bypassPermissions`, `manual`, `dontAsk`, `plan` |
| `claude --model` values | aliases `fable`, `opus`, `sonnet`, or a full model name |
| `claude --resume <uuid>` after `respawn-pane -k` SIGKILLed the interactive claude | **rc=0, and it recalled the token stored before the kill** (`MARKER42`); transcript grew 20 → 49 lines. Restart-with-continuity survives a hard kill |
| a SECOND `claude --resume <uuid>` while an interactive one holds that session | **rc=0 — no lock, no error**, and §22 measured the damage: `is_error=false`, the same id, and the text appended into the transcript file the LIVE process held open on **5 descriptors**. There is **no engine-side refusal**, so "resume it in place" is a silent two-writer corruption of the conversation, and §22.1 retires it. The restart-with-continuity rows above are safe only because the holder is dead first |
| interactive `claude --resume <uuid>` in a tmux pane | `pane_current_command` = `claude`; `pane_start_command` = `"claude --resume <uuid>"` |
| `pane_start_command` for a pane created with NO command | **empty**; `pane_current_command` = `zsh` |
| `claude --resume <nonexistent>` in a pane | does **not** exit: `pane_dead=0`, no status — it draws an interactive session picker and waits |

### Attach / resize

| fact | consequence |
|---|---|
| with `window-size latest`, a differently-sized client attaching **reflows existing scrollback**; it is **width-only** and it **permanently discards content** (~28 % of a Claude session's scrollback at stock `history-limit`), and it does **not** revert on detach | the warning says "permanently discards", and damage is not self-healing |
| `resize-window` **alone** pins a session — it silently sets `window-size manual` | any resize the hub performs must save and restore the prior value. Only ONE resize survives in the design — restoring the width the hub itself changed by attaching (§8); the pin-on-warning flow that needed this is gone, because the scrollback loss it protected against does not happen |
| `set -t <sess>` scoping is isolated; `window-size` and `prefix` do not leak globally | the hub can configure its own session safely |
| a same-sized client still changes `#{window_height}` because of the status line | the mismatch check compares **width only** |
| with `$TMUX` set, `new-window … tmux attach -t <inner>` is refused; `env -u TMUX tmux attach` works. The remote path `ssh -t host tmux attach` needs no workaround — ssh does not forward `$TMUX` | one workaround, local path only, with a comment saying why the remote path lacks it |
| ALT-screen panes are largely protected from the reflow hazard | the warning is graded by `#{alternate_on}`, free from the poll data |

### Possession (§20)

Measured against tmux 3.7b locally and 3.2a on a remote host, with a real attached client
obtained by nesting one server inside a pane of another — so "what the operator sees" is
read out of the outer pane's `capture-pane`.

| fact | consequence |
|---|---|
| `switch-client -t $N` then `select-window -t @N` moves the attached client and lands on that window; returning with `switch-client -t <own $N>` restores it | possession on one server needs no rendering, no borrowed window and no cleanup |
| `switch-client -t <sess>:<win>` also works as ONE command | but it needs NAME targets, so it is not used — §7's rename rule stands |
| `switch-client` with no attached client fails `no current client` | the only failure mode, and unreachable when the hub received the keystroke that triggers it |
| `$TMUX` = `socket,server_pid,session_id`, and the session id is a **bare number** (`0`) while the server names it `$0` | the `$` must be prepended; `shapeFor` refuses a bare `0` for `switch-client`, so the seam catches the omission |
| **`link-window` makes a window appear in both sessions with the same `window_id` and `window_linked=1`; `kill-window` on the borrowed copy destroys it in EVERY session and the pane's process dies** | `link-window` is forbidden and a source-level test enforces it (§20) |
| a linked window's `window_flags` is `*`, identical to an owned one, and the default `window-status-format` omits `window_linked` | the operator cannot tell a borrowed window from their own — which is what makes the above a footgun rather than a quirk |
| remote attach run in a **local** window: killing that window leaves the remote agent's process alive and its session present, clients → 0 | closing a hub-created window is safe; closing a borrowed one is not. The hub only creates windows it owns |
| the real bubbletea TUI redraws with no artefact after its client switches away and back, and **keeps polling while off-screen** (header went `1 session` → `2 sessions` across an absence) | possession costs the hub nothing; today's attach blocks it for the duration |
| local `capture-pane -p -e` is 0.6–0.8 ms (~1200/s); through an ssh-forwarded socket it is **480 ms — 2.1 fps**, because each invocation opens a new connection | a poll-per-frame viewport is free locally and unusable remotely — which is why the hub renders neither |
| Claude Code runs **inline**: `alternate_on=0`. `vim` sets it to 1, and `capture-pane` reads the alternate screen correctly anyway | a snapshot is what the user sees; `Alt` remains the honest flag for "a full-screen app lives here" |
| `capture-pane -p -e` carries styling — 180 SGR sequences over one Claude screen, only **14 distinct** | had a viewport been built, translating its palette would have been bounded work. It was not built |
| `send-keys` is faithful: `Up`→`ESC[A`, `Down`→`ESC[B`, `C-x`→`^X`, `Escape`→`ESC`, and `-l` passes literal UTF-8 including Cyrillic | raw key passthrough was never the hard part; the hard part was that nothing should need it |
| both `cursor_x` and `cursor_y` exist as formats; the hub reads only `y` | noted for §18/§19 work that wants a real cursor position, not needed by §20 |
| control mode (`tmux -C attach`) delivers `%output` events AND answers commands on the same connection — verified locally, `capture-pane` returned real pane content | the event-driven design that would pay for itself remotely |
| **the same control-mode connection over an ssh-forwarded socket could not be made to answer in three attempts** — locally identical code works | an explicit UNKNOWN. Remote possession does not depend on it (§20 uses a local window), and any future remote viewport must settle this first |
| reached through a **symlink** to its socket, a server reports the same `#{socket_path}` and the same `#{pid}` as through the real path, while the two paths compare unequal as strings | "same server" compares what the server says about itself, never the spelling in `--host` |
| `Validate` refused the remote container **twice**: on `ssh`'s own `-t <dest>`, then on the remote `tmux attach -t $3` — both inside one multi-token argument, which `validateArg` reads as a tmux sub-command chain | the seam needs a third change, and it was found by asking it instead of assuming — as with the lifecycle verbs |
| keying "is this a tmux chain?" on the PAYLOAD's first token fails both ways: a real chain often begins with a flag (`paste-buffer -b b '-t @4 ; display …'`), and `display -p 'OK %s'` would stop being checked | the discriminator is the OUTER verb: the trailing argument of `new-window`/`new-session`/`respawn-pane`/`split-window`/`run-shell` is opaque, everything else is judged |
| `ssh -o RequestTTY=yes` attaches a remote session exactly as `-t` does (measured against a real server) | not needed — with the opaque rule the existing attach command passes the seam with no flag reworded. Recorded so the workaround is not reinvented |
| **tmux hands a single trailing argument to `$SHELL -c`, so joining an argv with spaces is a re-parse.** Measured on 3.7b: `new-window … "tmux attach -t $3"` reached the far side as `attach -t` (rc=1, "-t expects an argument"), `-t $0` as the shell's own name (`zsh`), `-t $10` as a bare `0` — a session *named* `0`. Two real servers, same payload quoted element by element: the target's client count went **0 → 1**, and unquoted its window died at status 1 | the window payload is shell-quoted PER ELEMENT (`internal/ui`'s `shellJoin`). Quoting is also what keeps it one opaque trailing argument, so the exemption above still applies and the forbidden-format scan still covers it |

### Command-layer traps (adversarial pass)

Every one of these was measured on 3.7b against a throwaway server. Each defeats a mechanism
that a first reading of the tmux manual makes look correct.

| fact | consequence |
|---|---|
| **`display -p` runs its argument through `strftime`.** `display -p 'CONFIRM-%2'` returns an **empty string** with rc=0 — the *whole* message is dropped, not just the token. (`%%2` → `%2`, `%Y` → `2026`, `%n` → newline.) | a confirmation carrying a `%NN` pane id silently confirms nothing; **no literal `%` may appear in any `display -p` template**, ever. Identity is emitted through the format layer (`'OK #{pane_id}'` → `OK %2`), which is safe even when the value itself contains `%` |
| **`display -p` with no `-t` reports the server's *current* pane.** In `send-keys -t %0 … ; display -p 'OK #{pane_id}' ; send-keys -t %1 … ; display -p 'OK #{pane_id}'` **both** lines printed `OK %1` | `-t %NN` is mandatory on **every** sub-command in a batch, confirmations and guards included; without it a guard evaluates against the wrong pane and can fail *open* |
| **`capture-pane -p` with no `-S` emits exactly `#{pane_height}` lines**, trailing blanks included (verified at heights 2, 4, 5, 7) | captures can be framed by **declared length**, which pane content cannot forge — unlike a text marker |
| **`#{pane_id}` is monotonic within a server** (`%5` killed → next pane `%6`, never `%5`) but resets to `%0` when the server restarts | an existence pre-flight cannot catch recycling: the stale `%0` *exists* and points at a different session |
| **A per-pane user option survives as a durable token.** `set -p -t %0 @hub_uuid <uuid>`, then `list-panes -a -F '#{pane_id} [#{@hub_uuid}]'` → `%0 [a1b2…]` / `%1 []`; it is invisible globally (`show -gv @hub_uuid` → `invalid option`) | a send can be *guarded* by identity in the same invocation: `if -t %0 -F '#{==:#{@hub_uuid},<uuid>}' 'send-keys …' 'display -p -t %0 "REFUSED #{pane_id}"'` — measured working in both directions |
| **A batch aborts before its cleanup tail.** `load-buffer ; paste-buffer -t %999 ; … ; delete-buffer` on a missing pane left `tmux-hub-2: 42 bytes: "secret prompt…"` as the **most recent** buffer, ahead of the user's own | cleanup must never be a tail sub-command of a fallible batch |
| **tmux never errors on an unknown format variable** — `#{no_such_variable}` → empty, rc=0, field count preserved; a typo (`window_activty`) is indistinguishable from an empty value; `#{?no_such_variable,YES,NO}` → `NO` | a version allowlist is **unfalsifiable**: a wrong entry yields empty, and an empty `window_activity` parses as 0 = 1970 = eternally stale. A required-field assertion against a real pane is the only checkable form |
| **`pane_current_command` is free text**: a process named `weird\|name` (via `exec -a`) shifted the design's own format so `session_name` parsed as `name` and `window_name` as `claude-beta\|bash`, rc=0, field count 10 | only *one* free-text field can be last; the delta format must carry **no** free text at all |
| **`#{s/\|/!/:…}` is a trap**: unbracketed, it is an alternation of two empty branches, matches everywhere, and turned `bash` into `b!a!s!h!`. The bracketed form `#{s/[\|]/!/:…}` is correct | if sanitising is used at all, the character class is mandatory |
| **`send-keys -H` takes raw *bytes*, not codepoints**: `-H e9` delivers the single byte `0xE9`, not the UTF-8 `C3 A9` | true, and insufficient — this single-byte measurement is what made `-H` look usable. See the write-path table below: it takes one byte per **argument**, so an encoder producing one string delivers nothing |
| **`remain-on-exit` is a *window* option.** `set -t <session> remain-on-exit on` covered window 0 only; a pane in window 1 vanished with no `pane_dead`. Where it does apply, the user's own `exit` leaves `Pane is dead (status 0, …)` and the window persists forever | it is not usable as a session-wide mitigation, and it permanently changes sessions the hub merely observes — see §6 for what replaces it |
| **tmux appends `[dead]` to `#{window_name}`** for a dead pane's window | label rendering must tolerate the suffix |
| **A Claude Code pane and a plain shell both report `pane_current_command=bash`**, because Claude runs under a shell | `pane_current_command` cannot identify the agent pane; the process tree under `#{pane_pid}` must be walked |

### Write-path traps (safety pass)

These were measured after the write path had already been written up as safe. Three of them
defeat mechanisms §11 claimed were structurally impossible.

| fact | consequence |
|---|---|
| **`send-keys -H` takes one byte per *argument*.** Measured: `-H 41 42 31` → `AB1`; **`-H 414232` → nothing, rc=0, empty stderr**; `-H 4142 33` → only `3` (the 4-digit argument silently dropped); `-H zz 41 42 34` → `AB4` (invalid hex silently dropped); `-H 041` → `A` (a 3-digit argument delivers a *different* byte) | the obvious `hex.EncodeToString([]byte(s))` produces ONE argument and therefore **delivers nothing while reporting success**. `-H` is deleted from the design; both single-line and multi-line go through the buffer path |
| **`set -t <pane> @opt v` with no `-p` lands at *session* scope.** Measured: after `set -t %0 @hub_uuid SESSION-VALUE`, pane `%1` — never stamped — resolves the value, and the guard answers `GUARD-PASSED %1` | one missing character turns the guard into a session-wide **fail-open**. Options inherit downward (`-g` does it server-wide); `show -gv` proving no upward leak proves nothing about this |
| **An empty `-t` value fails open.** `send-keys -t '' -l X` → rc=0, delivered to the server's *current* pane. A well-formed but stale id fails closed (`-t %999` → rc=1 `can't find pane`) | the dangerous case is a present flag with an empty value, which is exactly what an empty format field yields; the seam must reject a `-t` that is not `^%[0-9]+$` |
| **`mkdir -p -m 0700` does not change an existing directory's mode** (measured: 0777 before, 0777 after), and it follows a symlink | 0700 is a property of a directory the hub CREATED and nothing more — so §5 words it that way and asserts nothing about one it finds. The `lstat`/ownership/mode refusals this row used to call for are ruled out of scope, not pending |
| **`load-buffer -b <n> -` from stdin plus `paste-buffer -d -p -r` works for a single line too** — measured delivered, and `list-buffers` empty afterwards | one primitive covers both cases, which removes four traps at once: byte-vs-codepoint, argv shape, silent drop on invalid hex, and `-l`'s trailing-`;` truncation |
| **`pane_id` and pane options move together** through `swap-pane`, `break-pane` and `join-pane`/`move-pane` — measured, id never changes while the option follows, and the option is never dropped while the id survives | a token bound to a pane survives the user rearranging their layout |
| **`respawn-pane` keeps the id, the token and `pane_pid`'s shell while replacing the process** — and the commoner variant needs no tmux command at all, because `pane_pid` is the *shell* and stays constant as the agent process comes and goes | a pane-bound token proves pane identity, **not process identity**: an agent that exited leaves a stamped pane that is now a shell |
| **A nested command does not inherit the enclosing `if`'s target.** Measured: `if -t %3 -F 1 'send-keys -l X'` delivered to the *current* pane, and a crossed pair (`if -t %3` … `send-keys -t %4`) delivered to the unstamped `%4` and confirmed `OK %4` | every sub-command needs its own `-t`, and the confirmation must echo the **token** rather than only the id, since a wrong pane cannot produce a matching random token |
| **`buffer-limit` does not evict named buffers.** With `buffer-limit 3` and three user buffers, two hub buffers took the list to five with nothing dropped | the paste path cannot destroy the user's copy history |
| `new-window -t <sess> -c <dir> -P -F '#{pane_id}' '<cmd>'` returns the new pane's id on stdout (e.g. `%2`) | identity at birth for hub-created panes: the uuid and pane id are known in the same call |
| `new-session -d -s <name> -c <dir> -P -F '#{pane_id} #{session_name}'` returns `%3 my proj` | works with a space in the session name |
| `-c` with a space in the path works; it is its own argv element, no quoting needed | simplified launch code |
| a command with quoted arguments, as one string: `'sh -c "echo hello world; sleep 302"'` → screen shows `hello world` | one-string command shape works |
| **pane id is never reused after the pane dies** — killed `%3`, next pane was `%4`; monotonic within one server's life | `%NN` is an exact key while the hub runs and the wrong key on disk; a restarted server numbers from `%0` again |
| `respawn-pane -k -t %N '<newcmd>'` keeps `pane_id`, keeps `@hub_*` options, keeps cwd; `pane_pid` changes (222976 → 222983) | the surviving stamp is a hazard: the token still matches while the agent behind it is a different process; identity must be explicitly invalidated on respawn |
| `remain-on-exit on`, command exits 7: pane stays with `#{pane_dead}=1`, `#{pane_dead_status}=7`, listed by `list-panes -a` | dead panes are observable |
| `remain-on-exit off` (default), command exits: pane is destroyed; `display -p -t <gone>` → **rc=0, empty** | without `remain-on-exit` a pane whose command exits vanishes, so "exited with code 7" is not a state the hub can observe — it is a row that vanishes |
| `display -p -t %999 '#{pane_id}'` (never existed) → **rc=0, empty** | liveness is emptiness: check a pane exists by asking for its own id and treating empty as gone |
| `#{pane_start_command}` quoted in output (`"sleep 301"`); immutable for the pane's life, changes only on respawn | the corroborator for a persisted hiding key |
| `#{pane_current_command}` as a persisted key is unusable — walks `zsh` → `claude` → `zsh` | it names the foreground process |
| `#{pane_start_command}` for a pane created with NO command is the **empty string** | the commonest thing a user hides; for those keys the corroborator carries no information and the match rests on position alone |
| `claude --session-id <fresh-uuid> -p …` → rc=0; transcript lands at `~/.claude/projects/<slug>/<uuid>.jsonl` | how a hub-created agent is launched |
| `claude --session-id <existing-uuid>` → **rc=1**, `Error: Session ID <uuid> is already in use.` | a hub that reuses an id fails loudly instead of silently forking a conversation |
| `claude --resume <uuid> -p …` → rc=0, continues that conversation (transcript grew to 32 lines) | restart-with-continuity |
| `claude --resume <uuid>` after `respawn-pane -k` SIGKILLed the interactive claude: **rc=0, and it recalled the token stored before the kill** (`MARKER42`); transcript grew 20 → 49 lines | restart-with-continuity survives a hard kill |
| a SECOND `claude --resume <uuid>` while an interactive one holds that session → **rc=0 — no lock, no error** | unlike `--session-id`, `--resume` does not refuse concurrent use |
| interactive `claude --resume <uuid>` in a tmux pane: `pane_current_command` = `claude`; `pane_start_command` = `"claude --resume <uuid>"` | a hub-created agent's start command contains the session's uuid, so its hiding key is unique to that launch and can never match a future one |
| the daemon's `reply` op delivered multi-line text to a session **blocked with no tty, pane or viewer** — `ok` in 13 ms, the question answered, the agent continued | a paneless session CAN be answered without a terminal, which is why §22 records the capability even though it defers the path |
| the same op fired at a session sitting on a **Write permission prompt**, carrying the text `ABSOLUTELY DO NOT … DENY`: **the file was created, 2 of 2 times**, the refusal text appears nowhere in the transcript, and the daemon answered `{"ok":true}` | firing text at that state does not answer a question — it discards the text and presses Enter, approving the pending call. So a send is **whitelisted** on `tempo == "blocked"` with a `block` present, never gated on detecting danger, and `ok:true` is a socket ack that must never be recorded as `delivered` (§22.4) |
| that state is **invisible**: `list` polled for 136 s while the prompt was pending reported `working`, an empty `needs`, and no `block` — through the daemon's own `list`; whether `claude agents --json --all` reports it differently is unmeasured (§22.10) | `working` conflates computing with waiting on a permission dialog, so a danger detector has nothing to detect — the whitelist is the only safe shape |
| one `new-session -d -s <n> -P -F '#{session_id}\|#{window_id}\|#{pane_id}\|#{pid}\|#{start_time}'` against a socket with **no server running**, on tmux **3.2a** and **3.7b** alike → all five fields at rc=0, the server started, and `display -p '#{pid}:#{start_time}'` byte-identical | the coordinates AND the server epoch come from the call a possession already makes, so nothing waits a tick to learn where it landed (§20); and starting a server is a consequence of creating a session rather than a separate act |
| `claude --resume <nonexistent>` in a pane does **not** exit: `pane_dead=0`, no status — it draws an interactive session picker and waits | a wrong resume uuid does not produce a dead pane |

### Hosts

- `~/.ssh/config` `Host` lines may carry **several names** (`Host hermes-ws web-ws crater-ws …`
  is six hosts) — measured, 15 lines expand to 20 names on this machine. Wildcard and unroutable
  patterns (`.host`, `machine/.host`, `unix/*`, `vsock%*`) are **not** in the user's config at all:
  they come from a systemd drop-in reached through the *system* config's
  `Include /etc/ssh/ssh_config.d/*.conf`. So a parser that reads only `~/.ssh/config` needs no
  filter, and one that follows the system chain does. Either way the positive probe eliminates
  them, so the filter is about keeping the picker's list short and readable.
- A failing `ssh -o ConnectTimeout=5 host tmux -V` returns promptly enough to probe ~15 hosts
  in parallel.

## 4. Architecture

```
                      ┌──────────────────────────────────────┐
   ~/.ssh/config ───► │ hosts      selection + tags (toml)   │
                      └──────────────┬───────────────────────┘
                                     ▼
   ┌─────────┐   spawn/supervise  ┌──────────┐   Run(ctx, host, args…)  ┌─────────┐
   │ hostset │ ─────────────────► │ hostagent│ ──────────────────────►  │ tmuxcli │
   └─────────┘   status+reason    └────┬─────┘   (rc, stdout, stderr)   └────┬────┘
                                      │                                     │
                                      ▼                                     ▼
                                 ┌──────────┐   snapshots+deltas      ┌──────────┐
                                 │ registry │ ◄────────────────────── │ classify │
                                 └────┬─────┘                        └──────────┘
                                      ▼
                                 ┌──────────┐        ┌────────┐
                                 │    ui    │ ─────► │ attach │
                                 └──────────┘        └────────┘
```

**`Run(ctx, host, args…) (stdout, stderr string, rc int)` is the only seam through which a TMUX
argv reaches tmux or ssh**, and that is what makes the failure taxonomy testable without a live
remote: host state is a pure function of `(rc, stderr, masterCheck)`.

It is **not** the only place the hub shells out, and this section used to say it was. Five
production files build their own process: `internal/agents/agents.go:148` (`claude agents`),
`internal/proc/walker.go:50` (the process walk), `internal/ui/attach.go:79,87` (the attach),
`internal/ui/launchform.go:209` (a directory probe) and `cmd/tmux-hub/main.go:496` (the membership
probe). None carries a tmux argv, none passes `Validate`, and `internal/tmux/guard_test.go` bans
`RunRaw(` outside `internal/hub/master.go` — it does not ban `exec.Command`. §22 makes that gap
load-bearing: its operational rules (always `agents --json --all`; always `--debug-file` where the
verb accepts it, which `agents` does not while `logs` DOES; judge `agents` by exit code alone) are rules
about how a **`claude`** argv is built, so they need one place to be enforced, and today there is no
such place.

| module | responsibility | knows nothing about |
|---|---|---|
| `tmuxcli` | build tmux argv, parse tmux output, per-version format allowlist | ssh, hosts, UI |
| `hostagent` | one ssh master's lifecycle: spawn, wait-on-fact, capability probe, backoff, status | UI, tmux semantics beyond the probe |
| `hostset` | ssh-config parsing, `tmux -V` probe, `hosts.toml`, socket labels | tmux internals |
| `registry` | merge all hosts' panes into one ordered list; attention state; selection | I/O of any kind |
| `classify` | `(bottomLines, deltas, now) → State`, pure | I/O, tmux |
| `ui` | bubbletea: grid, inbox, selection, confirmation, history view | ssh, tmux |
| `attach` | build and run the attach command; suspend or new-window | polling, registry |

## 5. Transport

One long-lived ssh **master** per enabled host, spawned and supervised by the hub. No port
forward:

```
ssh -N -M -S $RT/cm-<8hex>-<label> \
    -o BatchMode=yes -o ServerAliveInterval=15 \
    <host>
```

Every tmux command for that host then runs **through** the master:

```
ssh -o BatchMode=yes -o ProxyCommand=false -S $RT/cm-<8hex>-<label> <host> <the tmux command line>
```

- `$RT` = `$XDG_RUNTIME_DIR/tmux-hub`, created at 0700. Never `/tmp`. The subdirectory is
  **blast radius, not a security control**: startup reconciliation and `--stop-masters` both
  ENUMERATE `$RT` and `ssh -O exit` everything in it named `cm-<8hex>-*`, whoever created it,
  so a destructive sweep has to run in a directory the hub owns rather than in the one every
  other application keeps its runtime files in. The 0700 is the mode the hub REQUESTS when it
  creates the directory — `os.MkdirAll` applies the process umask, so a permissive umask can
  widen it — and nothing is checked about one it finds — see the assertion list below for what
  this design deliberately does not do. On systemd `/run/user/$UID` is itself 0700, so the
  subdirectory changes what a sweep can reach, not who can read a socket.
- `<label>` = the sanitised alias and `<8hex>` = `sha256(alias)[:8]`, so an alias legally
  containing `/` cannot break the path and two aliases cannot collide. (There is no `.ctl`
  suffix; earlier drafts of this section showed one, and `ControlPathFor` has never emitted it.)
- Both `-o` flags belong to the per-command line and both close a way for a MISSING master
  to look like a working one: `BatchMode=yes` stops a fallback connection reaching a password
  prompt (it fails fast instead), and `ProxyCommand=false` stops the fallback happening at all
  — measured in both directions below. `ProxyCommand=false` must never be added to the master
  spawn above, which legitimately uses the config's `ProxyJump`.
- Local host: same code, no ssh process — the command runs as `tmux -S <local socket> …`.

### Why there is no forward, measured

The earlier design forwarded the remote tmux socket (`-L $RT/<label>.sock:<remote>`) so that
everything above the socket path could be the same code as for the local server. That reuse is
the only thing it bought, and it costs a factor of two:

| | median, over a real host |
|---|---|
| one `list-panes -a -F <delta format>` through a forwarded socket | 349 ms |
| the same through the ssh master | **323 ms** |
| **the product's real poll cycle** (`FetchDeltas` + `FetchSnapshot`), forward | 1432 ms, 4 invocations |
| the same code, same host, over the master | **1337 ms**, 4 invocations |
| 8 concurrent invocations through one master | 338 ms — the same as one |

**Speed is not the reason, and the honest number is 7%.** An earlier draft of this section claimed
a factor of two, measured on a cycle assembled by hand into a single ssh command line. The product
does not assemble it that way: `FetchSnapshot` issues the labels, the zones and the full captures as
separate invocations, so a cycle is four round trips over either transport and the master wins only
its per-invocation margin. Driving the real batch code against a real host, the difference is
1337 ms against 1432 ms.

The factor of two is **available and not taken**. ssh's payload is an arbitrary shell command line,
so those four invocations can become one; a forwarded socket cannot do that, because each
`tmux -S <forwarded>` invocation opens its own channel. Merging them would take a remote tick from
~1.3 s to ~0.33 s, which is worth having and is not part of this change.

What the master gives for free is concurrency — 8 simultaneous invocations cost what one does — and
it is needed regardless: measured (§8), a forwarded socket cannot carry an `attach` at all, because
the client passes its terminal fd over `SCM_RIGHTS` and a forward drops ancillary data.

### What the forward's absence deletes

This is the point of the change, and it is larger than the latency. Every item below was a
measured hazard of the forward, and none of them can occur when there is no local socket file:

- **The `start-server` squatter.** `tmux -S <forward path> start-server` starts a *local* server
  on the forward path and answers as itself: `rc=0 version=3.7b host=cachyos` with no remote and
  no ssh at all. A powered-off host read `up-empty` and never backed off; reconciliation adopted
  every dead socket; the squatter's file made `bind()` fail `EADDRINUSE`, so
  `ExitOnForwardFailure=yes` killed ssh instantly and the retry re-created the squatter — a
  permanent loop behind a green UI. Worst, the recorded `#{version}` was the *local* tmux, which
  is the capability gate keyed to the wrong version: the incident's exact precondition.
- **Leaked ssh per restart.** Unlinking a live forwarded socket does not stop its ssh — measured,
  the process survives, the path becomes undialable, and a new tunnel binds it while the old ssh
  runs forever.
- **`local-squatter` false negatives.** A real remote whose hostname equals the local one — a
  cloned VM, a container, `localhost` — failed a hostname comparison forever and read `down`.
- **Symlink globs.** `$RT/*.sock` matches a symlink, so every path needed `lstat` + `S_ISSOCK`.
- **Remote socket path discovery.** The forward had to know the remote socket path *before* a
  server existed, so it could not ask tmux and had to derive
  `${TMUX_TMPDIR:-/tmp}/tmux-$(id -u)/default`. Running tmux on the far side removes the question:
  tmux finds its own socket. A `socket` override in `hosts.toml` becomes what it always should
  have been — extra arguments to the remote tmux (`-L other`), not a path the hub computes.

### Health is two independent observations, and neither parses a message

A tmux server with zero sessions reports itself as absent — `ls`, `list-panes -a` and `display -p`
all return rc=1 — so "no server" and "no sessions" are one observation. Over the master the three
states separate by exit status alone, measured against a real host:

| state | probe | result |
|---|---|---|
| host unreachable | `ssh … <host> 'tmux -V'` | **rc=255**, ssh's own message (`Could not resolve hostname …`) |
| host reachable, no tmux server | `ssh … <host> 'tmux list-panes -a'` | **rc=1**, `error connecting to /tmp/tmux-1000/<name> (No such file or directory)` |
| server up | the same | **rc=0**, and one call also yields `#{version}\|#{pid}\|#{start_time}` — measured `3.2a\|1350358\|1786622421` |

**Host truth** is `tmux -V` (rc=0 ⇒ the host answers and has tmux). **Server truth** is any real
tmux command (rc=1 ⇒ no server). On the master path there is no dial, no `SO_PEERCRED` and no
socket stat — those existed to learn what the forward was hiding, and `explainFailure` now returns
before dialling for a remote target. The `--host label=/path` escape hatch this section keeps still
dials, because a bare socket is the one shape with nothing else to ask; it fills `Host.Peer`, which
no production reader consumes.

`ssh -O check` reports `Master running (pid=…)`, which says the master lives and **not** that tmux
answers. It was never health and still is not.

**A master outlives the hub, and that is turned into an asset.** Measured: spawned and left, an
`ssh -N -M` is reparented to pid 1 and survives its parent. Spawning one costs **1.55 s** to become
checkable (1530/1551/1606 ms over three trials, ~28 failed `-O check`s at 50 ms apart) while
adopting a live one costs **3.6 ms** — so the hub leaves its masters running on exit and adopts them
on the next start, which makes the second run of the day effectively free. The 1.55 s is also why
spawning can never sit on the first-paint path: §16 promises 50 ms, and five hosts spawned before
the first frame would miss it by thirty times.

Deliberately leaving processes running requires a way to end them, or the hub accumulates one ssh
per host the user ever enabled. Three, all one `ssh -O exit`: disabling a host in the picker stops
its master, `--stop-masters` stops every master under `$RT`, and a master whose alias is in no
`hosts.toml` entry is stopped at startup. That last one is the only reconciliation this design
needs — there is no socket to unlink and no pid to persist, because a master is addressed by the
control path the hub chose.

**A missing master is invisible unless you forbid the fallback.** Measured: pointed at a control
path that is not a live master, `ssh -S <path> <host> 'tmux -V'` returns **rc=0** with the right
answer — ssh silently opens its own connection. So a dead master does not raise an error, it raises
the *latency*: the same probe that costs 323 ms through the master cost 7.0 s to this fleet's slowest
host without one.

`-o ProxyCommand=false` closes it, and both halves are measured. Through a live master it is a
no-op — rc=0, `tmux 3.2a`, the multiplexed path unaffected. With the master absent the same command
returns **rc=255** (`Connection closed by UNKNOWN port 65535`) where without the flag it returns
rc=0 and the right answer. So the per-command invocation carries it and a dead master fails closed.

Two constraints on that flag. It goes on the per-command invocation **only**, never on the master
spawn: the master legitimately uses the ssh config's `ProxyJump` (this machine has
`Host *.internal ProxyJump nuc`), and blocking it there would break every proxied host. And ssh's
message is opaque, so the hub translates it rather than passing it on — the operator needs to read
that a named host's master is not running, not `UNKNOWN port 65535`.

The supervisor still **asserts** presence (`ssh -O check`, re-spawn if absent) rather than waiting
to be told, because asserting costs 3.6 ms and the alternative is a tick that fails before it
repairs itself.

### The seam still sees the tmux arguments

Running tmux on the far side must not blind the guard that makes the write path safe. `Validate`
enforces the `-t` shape over argv (§7), and an ssh payload is one opaque string, so the order is
fixed: **validate the tmux arguments, then wrap them.** Never the other way round.

The wrapping is shell quoting, and it is the trap this project has now paid for twice — the remote
path has **two** shells, tmux's `$SHELL -c` on this machine and the login shell on the far side, and
a `$N` session id is expanded by either one given the chance (§20). One `shellQuote`, applied per
element, at both levels.

`#{pid}` + `#{start_time}` remain the **server epoch**, recorded at adopt and compared every poll;
a changed epoch invalidates every selection (§7).

**`$RT` is asserted, because the failure is silent and the symptom is nonsense.** With
`XDG_RUNTIME_DIR` unset — a user unit without `pam_systemd`, a container, `su`, cron —
`filepath.Join("", "tmux-hub")` is a **relative** path, so the hub creates its control sockets in
whatever directory it happened to start in and then works, unpredictably, until the user runs it
from somewhere else and every host loses its master — which, per the hazard above, does not fail
but silently costs a fresh connection per command. That is a DX bug with a baffling symptom, so
startup asserts and refuses with the remedy named:

- `XDG_RUNTIME_DIR` set and absolute — *"XDG_RUNTIME_DIR is not set, so I have nowhere predictable
  to put per-host sockets. Set it to /run/user/$UID or equivalent."* Both checks run **before**
  the `tmux-hub` join, which is the whole point of them: joining first turns a missing variable
  into a directory created wherever the process started.

**What this design deliberately does NOT assert about `$RT`**, ruled and recorded rather than left
as an unkept promise: it is not checked for being a symlink, for its ownership, or for being
group- or world-accessible. Earlier drafts of this section promised all three; nothing implemented
them, and a doc that asserts a control the code does not have is worse than an honest absence,
because it stops the next reader checking. Security is out of scope for this project at this stage.
The measured fact behind the retired promise still stands and is why the 0700 is worded as it is:
`mkdir -p -m 0700` does **not** change an existing directory's mode (0777 stayed 0777) and it
follows a symlink, so 0700 can only ever be a property of the directory the hub CREATED. On
systemd `/run/user/$UID` is 0700 already, so no socket became more reachable when this was
written down.

Starting a tmux server on a host that has none is **never a side effect of probing**, and it now
happens on two explicit actions rather than one: the offer on an `up-empty` host ("no tmux server
here — start one?"), and §22's `a` on a background agent row, which creates a session on that row's
host and thereby starts the server. Measured on a private socket: with no server, `ls` answers rc=1
`error connecting to … (No such file or directory)` — the signature the health table above reads as
`up-empty` — and one `new-session -d` then returns rc=0, starts the server (`%0 3.7b <pid>`) and
makes `ls` answer rc=0. So an operator can take a host from `up-empty` to `up` by pressing `a` on a
row that has no pane, which is a cost §22.9's confirmation names as a LINE alongside the abandoned turn — decision 2, taken: the host cost never opens the dialog by itself.

**Capability verification replaces capability declaration.** A version allowlist cannot be
checked: tmux never errors on an unknown format variable, so a wrong entry silently yields an
empty field with the field count intact, and an empty `window_activity` parses as 0 — every pane
on that host reads as last-active in 1970. So at connect the hub runs its **exact poll format**
against a real pane and requires every structural field to be non-empty; a hole marks the host
`degraded:format` and **names the missing field**. Verified sound on 3.7b: all ten fields
non-empty for four live panes and for a dead one. The version table is still kept — parsed as
`(major, minor, suffix)`, because `3.2a` and `next-3.x` are not orderable strings — but its role
is to *predict* which fields to expect, while the assertion is what *establishes* them. Unknown
version fails closed to the minimal set. `client_*` remains excluded unconditionally at every
version.

**Capability gate.** The probe's `#{version}` selects a format-variable allowlist. The hub
emits only variables on the list for that version. `client_*` is never emitted at all — the hub
is never an attached client, so those variables carry nothing it needs. This is the structural
answer to the incident, and it is an allowlist rather than a denylist so an unknown version
degrades to the minimal set instead of to a crash.

**Deadlines.** Every invocation runs under a context deadline (poll ~2 s, capture ~5 s) and the
process is killed on expiry. A stalled tunnel therefore cannot freeze the UI — the measured
failure mode that makes this mandatory.

**Host states**, each with an explicit exit:

| state | meaning | exit |
|---|---|---|
| `connecting` | ssh spawned, dial not yet accepted | dial + `#{version}` rc=0 → `up`; deadline → `down` |
| `up` | dial held, `#{version}` rc=0 | poll failure → re-probe |
| `up-empty` | dial accepted then EOF — host reachable, no tmux server | a server appears → `up`; offers "start a server" |
| `degraded:old-version` | version below the full allowlist | version change on reconnect |
| `degraded:stalled` | per-call deadline expired | teardown + rebuild, capped backoff |
| `down:<reason>` | unreachable / auth / no tmux / forward refused | capped backoff re-probe, plus a manual reconnect key |

Reason strings are user-facing and carry the fix, not just the breakage
(`no tmux on host — install tmux or disable this host`, not `probe failed`).

**Startup reconciliation is the normal path**, not an edge case: enumerate `$RT/*.sock`, run the
probe on each, adopt a live tunnel, unlink and rebuild a dead one, reap sockets for hosts that
are no longer enabled. This makes crash recovery and normal start the same code.

**Single instance:** exclusive `flock` on `$RT/hub.lock` at startup, before any transport work.
On contention, name the holding pid and refuse.

**Shutdown:** `ssh -O exit` each master, unlink each socket, release the lock. Attach windows
created inside the user's tmux are left alone — they belong to the user, not the hub.

**Scheduler:** a bounded worker pool caps in-flight invocations; each host has a poll phase
offset so ticks do not align; each host has its own interval with a ≥250 ms floor (slower for
`degraded`). One slow host cannot delay another host's tick or the UI.

## 6. Polling and the attention model

**Cheap delta — one invocation per host per tick.** "Free text last" is not sufficient: only the
*final* field is protected by a bounded split, and `pane_current_command` is free text too — a
process named `weird|name` shifted `session_name` to `name` and `window_name` to
`claude-beta|bash` with rc=0 and the correct field count. So the delta format carries **no free
text at all**, only character-restricted values:

```
list-panes -a -F '#{pane_id}|#{window_activity}|#{history_size}|#{pane_dead}|#{?alternate_on,ALT,NORM}|#{window_width}|#{pane_height}|#{pane_pid}|#{cursor_y}|#{session_id}|#{bracket_paste_flag}|#{pid}|#{start_time}|#{window_id}|#{pane_index}|#{pane_dead_status}|#{window_index}'
```

Injection is impossible here rather than defended against: every field is `%NN`, an integer, or
a fixed token.

**Labels carry their own lengths, in ONE sub-command.** An earlier version of this section claimed
that one trailing free-text field per sub-command made a bounded split "provably enough". That was
false, and the counter-example is the value this design now needs most: a pane's working directory.
tmux refuses a newline in a session or window name (`invalid session name: a\nb`) and *escapes* one
in `pane_start_command` (`"sleep 300\n#x"`, two characters), but `pane_current_path` is whatever the
filesystem is called, and tmux emits it **raw** — so the line splits. The old reader framed blocks by
counting lines against a pane count taken from a *different* invocation, so one such value shifted
every later block: measured, it truncated the path at the newline and then died with `label line
without a delimiter: "второй"`, taking the whole host's tick with it.

So each value is preceded by its own **byte** length, and there is one sub-command rather than one
per label:

```
… ';' list-panes -a -F '#{pane_id}|#{n:session_name}|#{session_name}|#{n:window_name}|#{window_name}|…'
```

`#{n:X}` is the raw byte count — measured, a 42-byte 32-rune path reports 42 — so the reader consumes
exactly those bytes and never looks for a boundary a value could forge. This kills the class rather
than an instance: there is no block to shift, and no count from another invocation to disagree with,
so a pane created or destroyed between the two calls cannot mis-frame anything either. A stream that
does not line up is an **error naming the pane it broke on**, where the line-counting reader answered
with a plausible wrong value — a session name that was really a path. `#{q:}` is not an alternative:
it escapes `| % $ # \ ' " ;` identically on 3.2a and 3.7b, but not newlines.

These are **not** separate invocations. Each invocation is a round trip — measured 501 ms for one
label query against a real host — so labels, every pane's zone and the wanted full captures all go
into ONE batch after the delta. A tick is therefore two round trips per host whatever the pane
count; issuing them separately cost 4+N and made the dashboard permanently behind. `window_name` may carry a tmux-appended `[dead]` suffix; rendering tolerates it.

### Two windows, not one: the classification zone and the tile

These were conflated in an earlier draft and they are different extractions with different sizes.
Measured on the live 80×24 Claude pane: **6 of 25 lines are content**; the other 19 are rule
lines, an empty prompt box, a constant footer and blanks, and you must skip **10 lines from the
bottom** to reach any content at all.

| window | what it reads | size | why |
|---|---|---|---|
| **classification zone** | the bottom ~6 lines | tiny | the live state markers live only here: the prompt box, and `esc to interrupt` in the footer region |
| **tile** | the last K *content* lines, after dropping rule / prompt / footer / blank | K rows of the tile | a "last N lines" tile of an idle Claude pane shows a box and a footer — **zero** information |

**They cannot come from the same capture**, and assuming they could was a defect caught by
running the code: the tile came back empty for exactly the panes that matter. The zone is the
*tail*, and on an idle Claude pane the tail is chrome by construction — measured on the live
pane, absolute lines 18–23 of 24 are `rule / ❯ / rule / footer / footer / blank`, so
`ContentTail` over the zone returns **nothing**. Content lives *above* the chrome.

So the two windows are two different reads, with different cost profiles:

| window | capture | taken for |
|---|---|---|
| classification zone | `capture-pane -S <h-6> -E <h-1>` — ~650 B | **every** pane, every tick |
| tile content | the full visible screen — ~3.6 KB | only panes whose tile is on screen (≈6–12) |

The line-kind classifier is shared; the captures are not. Content is 10–18 % of a pane's bytes,
which is why the tile keeps only the last `ContentLines` of it.

**This is not a Claude Code special case.** A prototype renderer was run against four other pane
kinds, each verified non-empty first. The raw "last N lines" tile failed on **every** non-ALT
pane, for the same structural reason: once a command returns, the bottom of a shell pane is a
prompt and blank space.

| pane | last-6-lines tile | content-lines tile |
|---|---|---|
| `git log --stat` | prompt + blanks | the log |
| a build run | prompt + blanks | `[6/6] compiling …`, `ok pkg/foo`, **`FAIL pkg/bar assertion failed`** |
| `vmstat` | prompt + blanks | the table |
| `less` (**ALT**) | its content | the same content |

Two things fall out. The build case is the strongest argument for the whole tile design: the tile
surfaces the failure line, which is the entire reason someone glances at it. And for an ALT pane
the two renderings are **identical**, because a full-screen app has no chrome to strip — so one
renderer serves both and no branch is needed beyond skipping `classify()`.

Known imperfection, stated rather than hidden: a rich shell prompt (`tmux-hub  main`) classifies
as content and appears in the tile. It is informative and harmless, but it means the classifier is
a heuristic (§12), not a parser.

### Capture is range-bounded, so nothing is stale

`capture-pane -S <start> -E <end>` returns **exactly** those lines — verified identical to the
corresponding slice of a full capture, at **18 %** of its bytes. That changes what the hub can
afford: it captures **every pane's classification zone on every tick**, so there is no delta gate,
no slow sweep, and no window in which a pane that just started waiting for input goes unnoticed.
The measured hole in the earlier design — `needs` and `idle` are invisible to every delta variable,
so a delta-gated capture is slowest to see the highest-priority state — is not mitigated, it is
removed **for panes**. It is not removed for the population that holds most of the waiting. A
background agent row has no screen to capture, so its attention state is only ever what the listing
says, and §22 measured a session sitting on a Write permission prompt reporting `working`
with an empty `needs` and no `block` for **136 s** — through the daemon's own `list`; whether
`claude agents --json --all` reports it differently is unmeasured. There is no second observation to
correct it, because the screen witness that makes the claim above true does not exist for a paneless
session. §22.1 puts the scale on that: **57 of 65 rows** are `background`, measured 2026-08-16 with
`claude agents --json --all` on all three claude-bearing hosts in one snapshot. Whether every waiting row is among them is WITHDRAWN at that
denominator and carries a §22.10 probe. So the guarantee this section makes is "no pane waits unnoticed", and the hub's
highest-priority state remains, for agent rows, exactly as good as the listing.

**The zone is anchored at `#{cursor_y}`, not at the pane's bottom.** Anchoring at the bottom reads
an EMPTY zone for any pane whose output has not filled the screen. Measured live, and it made the
two most urgent states invisible: a pane that printed `Do you want to proceed?` three rows down and
slept returned `[""]` from its zone and classified as `idle`, and so did one that printed
`FAIL … assertion failed`. Every plain shell has that shape, and §1 says the sessions are mixed.
The cursor is where output actually ended; for a pane that *does* fill its screen — Claude Code
always does — the two anchors return the same lines, so nothing is lost by preferring it.

Budget, measured (realistic ANSI-heavy pane = 3614 B full, ~650 B for a 6-line zone; batch of 80
panes framed and captured = 5.1 ms of tmux time, one round trip):

| panes/host | hosts | zone bytes/tick | at 1 s |
|---|---|---|---|
| 5 | 3 | ~10 KB | 10 KB/s |
| 20 | 3 | ~39 KB | 38 KB/s |
| 20 | 8 | ~104 KB | 102 KB/s |
| 50 | 8 | ~260 KB | 254 KB/s |

Full-pane captures are additional and are taken only for the tiles actually on screen (≈6–12),
~3.6 KB each. Above roughly 8 hosts × 20 panes the tick interval stretches and the delta gate
returns — but as a **scale** feature with a visible "sampling" indicator, never as the default,
because a gate that can be wrong about `needs` is the one thing this tool must not have.

ALT panes are captured with no `-S` (a range would splice pre-app scrollback) and are not passed
to `classify()`.

**Batched captures are framed by declared length, not by a marker.** tmux does not frame them at
all, and a text marker is forgeable by the pane's own content — not hypothetically: the hub's
primary use is orchestrating Claude Code sessions, *including sessions developing the hub*, so
the literal marker string appears on screen in normal use, and the hub types operator text into
panes that echo it. Measured: a pane printing `--TMUXHUB-0002--` mis-attributed its own content
to the next tile and left the classifier reading a truncated screen. Instead, `capture-pane -p`
with no `-S` emits **exactly `#{pane_height}` lines** (verified at heights 2, 4, 5, 7), so each
capture is preceded in the same invocation by its own length:

```
… ';' display -p -t %N '#{pane_id} #{pane_height}' ';' capture-pane -p -t %N  ';' …
```

The reader consumes exactly the declared count and **rejects the whole batch** if any block does
not. Content cannot forge a count, and a mid-batch resize becomes a detected anomaly instead of
silent mis-attribution. A 128-bit random nonce per invocation may be added as a cross-check — a
nonce is never typed into any pane — but the count is the mechanism.

**Every `display -p` template the hub emits contains no literal `%`.** `display -p` runs its
argument through `strftime`, and a `%` token makes it return an **empty string with rc=0** — the
whole message, not just the token. Identity is emitted through the format layer
(`'#{pane_id} #{pane_height}'`), never by interpolating `%NN` into the string. An empty
`display -p` line is treated as a hub bug and fails loudly; it is never read as "absent".

**A vanished pane's evidence lives in the hub, not in tmux.** A pane whose process exits is
destroyed with its scrollback before the next poll, so the obvious fix would be
`remain-on-exit on`. It is rejected on two independent grounds, both measured. It **does not
work**: `remain-on-exit` is a *window* option, so `set -t <session>` covered window 0 only and a
pane in window 1 still vanished with no `pane_dead` — the mitigation would give false coverage
over exactly the case it was added for. And where it *does* apply it **cannot be un-noticed**:
the user's own `exit` then leaves `Pane is dead (status 0, …)` and the window persists, so
zombie windows accumulate, the session never dies and the server never exits — a permanent
behaviour change to sessions the hub merely observes. Both grounds are
about panes the hub did not create, and that is the whole scope of the rejection — on a window the
hub made itself it is set deliberately, because there it is the only thing that keeps a dying
command's message on screen (§19 for the launch pane, §20 for the possession window). Instead the
registry keeps each pane's **last capture and last delta**; a pane present in tick *N* and
absent in tick *N+1* becomes a `gone` entry carrying its final screen, held until the user
dismisses it. The hub therefore reports "died, and here is what it last said" without changing
anything on the host.

`#{pane_dead}` / `#{pane_dead_status}` are still read from the poll, because a user who has
`remain-on-exit on` themselves gets the better signal for free.

### States

| | meaning | signal |
|---|---|---|
| ⚑ `needs` | waiting on the user | prompt/choice pattern in the zone |
| ✱ `quiet` | suspiciously silent | `now - <this pane's own last change> > N` (§12) |
| ▸ `idle` | finished, prompt empty | prompt present, no live work marker, activity frozen |
| · `works` | working | this pane's own output changing — `history_size`, `cursor_y` or its zone — or the **live** `esc to interrupt` marker |
| ✗ `error` | probably failed | error pattern in fresh lines, or `#{pane_dead}` = 1 |
| ? `unknown` | a source named the session but not its state | a Claude version that reports neither `state` nor `status` (§17) |
| ✓ `done` | the job ENDED | the agent listing says `done` or `completed`; never produced by `classify`, which reads a pane |
| ✝ `gone` | pane disappeared between ticks | absent from the poll; carries its last cached screen |

Inbox sorts by **how much the pane wants the user**: `needs`, `error`, `quiet`, `idle`, `works`,
`unknown`, `done`, `gone`. `error` sat below `works` in an earlier draft and read wrong the first
time a screen showed real states — a failed pane is something to act on and a busy one is not. With
bypass-permissions on, `needs` is rare and the working pair is `idle` ("finished — give it the next
thing") and `quiet` ("hung or dead").

**`done` is a rank of its own, and this is where §22.6 left that choice.** `agents.Attention()`
folded `done` and `completed` onto `idle`, so a background job that had ENDED printed the word this
table reserves for a live session with a prompt — `▸ idle`, the same glyph — and shared its rank, so
it sat among the rows waiting to be typed into. Two facts lost to one fold: what the row is, and
where it belongs. It ranks BELOW `unknown` because a row whose state could not be read wants a look
and a finished one wants nothing, and ABOVE `gone` because its output is still there to read. It is
unreachable from `classify`: a background job that ended leaves no pane, and an interactive one
leaves a shell, so no captured screen can carry this state — it arrives only from a listing.

`classify(zone, deltas, now, pollInterval) → State` is pure. The freshness window
that separates `works` from `idle` is **two poll intervals** with a 4 s floor, not a constant: at a
measured 2.1 s cycle against a fixed 4 s window a plainly busy pane read `idle`, and that is the
one distinction the inbox is built on. It keys `works` on `esc to interrupt`,
**never** on the spinner glyph or "Churned for Ns" — both persist in an idle pane's scrollback
and would pin every finished session as working. Cold start is defined from a single sample:
capture markers, plus `now - window_activity` as an absolute rather than a diff.

Patterns live in a config file, not in code, and are calibrated on fixtures captured from real
panes with **both poles asserted**: a waiting pane must read `needs`, a working pane must not.

## 7. Broadcast

Input goes only to explicitly selected, on-screen tiles. Tags only *select*; a tag is never
itself a target.

### A pane target is never resolved from a session — and after §22 a session is a target in its own right

Two write paths exist, and this section defines the first. For a pane target, polling,
`classify()`, the states and the guard are all per-pane, so the pane list is a list of **panes**,
grouped visually under their session. §17 already widened the inbox itself to sessions; §22 adds
the second write path — the daemon's `reply` to a background session that HAS no pane, no tty and
no viewer. Everything below (the token, the epoch, the buffer, the `capture-pane` witness) belongs
to the pane path, and §22 states clause by clause which of it transfers.

What is forbidden is therefore not "a session as a target". It is **resolving a session down to a
pane**, because the only available resolution rule — the active pane — is measured to be actively
dangerous: in a Claude session where the user had split the window for a quick command,
`pane_active` was the user's shell, and the broadcast produced

```
bash-5.3$ please refactor the auth module and run the tests
bash: please: command not found
```

The prompt executed as a shell command line. `please` is harmless; a prompt starting `rm the
stale fixtures…` or `git reset…` is not, and the multi-line path is worse because
`paste-buffer -r` preserves LF, which readline outside bracketed-paste mode accepts as
end-of-line.

An earlier draft said the hub cannot tell the two panes apart because **both report
`pane_current_command=bash`**. That is measured **false**: a pane running an agent reports
`claude` in both launch shapes — as the pane's own command (`tmux new-window claude`, where
`pane_pid` *is* claude) and started from a shell (`pane_pid` is the shell, claude is a child).
So that field would in fact separate an agent pane from an idle shell.

It is still not the thing to key on, for three reasons that survive the correction:

- it names the **foreground** process, so it changes to `bash`, `git` or `npm` whenever the agent
  shells out for a tool — it answers "what is running right now", not "is this an agent's pane";
- it cannot separate an interactive agent from `claude bg-pty-host` or `claude bg-spare`, since
  all three are named `claude`;
- one process-tree rule covers both launch shapes and a wrapper (`sh -c 'exec claude'`), where a
  field lookup needs a different answer per shape.

So the agent pane is identified **positively**, by looking for the `claude` process at or under
`#{pane_pid}` — **at**, because in the pane-command shape `pane_pid` is claude itself and a walk
over descendants alone finds nothing (measured against a live session: not identified). The
binding is to *that* pane's id. Never to `pane_active`, which flips the moment the user clicks
elsewhere; pane ids are monotonic within a server, so a binding to the identified pane survives
splitting, swapping and break-out.

### Targeting: identity checked in the same invocation as the send

Existence is not identity. `#{pane_id}` restarts at `%0` when a server restarts, so a stale `%0`
**exists** and points at a different session — an existence pre-flight passes and the prompt is
delivered to the wrong pane. Over ssh this is invisible: the tunnel and master survive a remote
tmux restart and the poll returns a full pane list.

So every selected pane carries a token, and every send is **guarded by that token inside the same
invocation**, which removes the TOCTOU window on the *option*:

```
set -p -t %NN @hub_<instance> <uuid>                 # re-stamped every tick, see below
if -t %NN -F '#{==:#{@hub_<instance>},<uuid>}' \
   'paste-buffer -d -p -r -b <buf> -t %NN ; display -p -t %NN "OK #{pane_id} #{@hub_<instance>}"' \
   'display -p -t %NN "REFUSED #{pane_id}"'
```

Three details are load-bearing, each from a measured failure:

- **`-p` is mandatory on the stamp.** `set -t <pane>` with no `-p` lands at *session* scope, and a
  pane that was never stamped then resolves the value and passes the guard — measured
  `GUARD-PASSED %1`. One missing character is a session-wide fail-open. `-g` is server-wide.
- **Every sub-command carries its own `-t`.** A nested command does not inherit the `if`'s target:
  measured, a crossed pair delivered to the unstamped pane and confirmed `OK` for it. The
  confirmation therefore echoes the **token** as well as the id, because a wrong pane cannot
  produce a matching random token.
- **The `if` itself carries `-t`, and that is a second fail-open rather than a restatement of the
  first.** `if -F '#{==:#{@hub_x},<uuid>}'` with no `-t` evaluates the *pane* option against the
  server's **current** pane — the one the user last touched — not against the pane named inside the
  sub-commands. Measured on 3.7b: with `%0` stamped and `%1` not, the guard passed and the payload
  was pasted into `%1`, which printed `OK %1`. So the option is read from one pane while the text
  goes to another, and the check reports success. `if -F -t %1 …` on the same state answers
  `REFUSED`. Both the `if` and each sub-command need their own `-t`; neither implies the other.
- **The token is re-stamped from the identification, every tick, for selected panes only.** A
  pane-bound token proves *pane* identity, not *process* identity: measured, `respawn-pane -k`
  keeps the pane id **and the token** while replacing the process — `pane_pid` does change
  (`702400` → `702406`), so it is the option's survival and not the pid's that defeats the guard.
  The commoner case needs no tmux command at all: when claude is started from a shell, `pane_pid`
  is the shell and never changes as the agent comes and goes.
  So the poll re-stamps each selected pane from its own process walk and issues `set -pu` the
  moment the walk stops finding an agent. The guard then means "identified as an agent no more
  than one tick ago", and a vanished agent produces `REFUSED` by construction.
- **That claim needs a bound, so a round is PER HOST with a deadline of its own.** A round that
  never comes back leaves the last completed one standing as the hub's answer, and the answer it
  leaves standing is "yes, this is an agent" — the one direction this must never fail in. So one
  round is in flight per host rather than one for the fleet, and each carries a deadline of the
  walk's own bound plus one stamp. A host whose round overruns it has every pane reported
  **unidentified**, i.e. it costs its own panes a confirmation dialog rather than costing every
  other host its freshness. Both halves were measured wrong first: with a single in-flight flag
  and no deadline, a stalled walk plus one 5-second stamp per selected pane held the round for over
  a minute, and for that whole minute no pane on **any** host was re-stamped while every one of
  them still read as identified.

Independently, the **server epoch** (`#{pid}` + `#{start_time}`, §5) rides the per-pane delta — a
server variable resolves inside a per-pane format, so it costs no extra round trip — and is recorded
per target at the moment of selection. A change is one of the confirmation clauses below rather than
a silent purge of the selection: the user chose those panes, and a dialog naming the restart is more
use than a selection that empties itself. The token guard refuses anyway, since a fresh server
carries none of the hub's options.

**The seam rejects malformed targets.** An empty `-t` value fails *open* — measured,
`send-keys -t '' -l X` returns rc=0 and delivers to the server's current pane, i.e. the pane the
user last touched — while a stale `%999` fails closed. Since an empty format field is
indistinguishable from a real one (§3), `Run()` refuses any argv whose `-t` value is not
`^%[0-9]+$`, and the guard builder refuses an empty expected token. Both can only be hub defects,
so they are named and refused rather than handled.

### Text primitives

**One primitive for all text**, single line and multi line alike:

```
load-buffer -b tmux-hub-<instance>-<seq> -          # payload on stdin: no argv quoting at all
paste-buffer -d -p -r -b <same> -t %NN             # -d deletes on paste, -p brackets, -r keeps LF
```

`send-keys -H` was the earlier choice and is **deleted**. It takes **one byte per argument**, so the
obvious `hex.EncodeToString([]byte(s))` builds a single argument and delivers **nothing** at rc=0
with an empty stderr — while the guarded idiom still prints its `OK`. Measured, along with three
adjacent silent failures (`-H 4142 33` drops the 4-digit argument; `-H zz 41` drops invalid hex;
`-H 041` delivers a different byte). One primitive instead of two removes the byte-vs-codepoint
question, the argv-shape trap, the silent drop on malformed hex, and `-l`'s trailing-`;`
truncation, all at once — and the buffer path was measured working for a single line.

`set-buffer` is not used: its payload is argv and inherits the truncation. Buffer names are
namespaced per hub instance (§11).

| other keys | command |
|---|---|
| submit | a **separate** `send-keys -t %NN Enter`, after the witness read (~150 ms), guarded and confirmed like the paste |
| interrupt / cancel | `send-keys -t %NN C-c` / `Escape` — separate hotkeys, not expressible as text |

**Buffer cleanup is atomic with the paste, never a tail sub-command.** The earlier form ended
the batch with `delete-buffer`, which is unreachable when the batch aborts — measured: one
missing pane left `tmux-hub-2: 42 bytes: "secret prompt…"` as the **most recent** buffer, ahead
of the user's own, so their next `prefix ]` in any session on that host pastes the hub's prompt.
A vanished pane is an expected condition, so the leak is routine rather than exotic. Hence
`paste-buffer -d` (delete after paste) so cleanup cannot be skipped, a Go `defer` issuing
`delete-buffer` as its **own** invocation regardless of rc, and a sweep of
`list-buffers -F '#{buffer_name}'` for `tmux-hub-*` at connect and at shutdown. `load-buffer`
runs *after* the pre-flight, not before.

**Delivery needs a witness, not a confirmation.** A `display -p -t %NN` fires whenever the pane
resolves and the guard passed — it cannot see whether any bytes arrived. Measured with the old
`-H` primitive, the hub printed `OK %0` having delivered nothing, which is the worse direction:
the operator then waits on an agent that never got the prompt, or sends a follow-up ("yes, go
ahead") into a state that does not exist.

The witness cannot live in the same invocation, and this corrects an earlier draft of this section
that said it could. `#{window_activity}` tracks the pane's **output**, and the pasted bytes are a
*write to the pty* — the process cannot have answered while the command batch that wrote them is
still running. Measured on 3.7b, three consecutive sends read the identical timestamp before and
after the paste inside one `if` (`B 1786487832 / A 1786487832`), i.e. a same-invocation witness
reports `sent-unwitnessed` for **every** delivery and is therefore worse than none. `#{history_size}`
is no substitute: it stayed at `0` across a delivery that was plainly on screen, because the output
never scrolled.

So the guard invocation records `activity_before` (a valid reading — it is taken before the write)
and the witness is a **separate, later read**. Measured latency: activity had already advanced at
**+50 ms**, and at every interval up to 2 s. The hub therefore re-reads at ~150 ms and, failing
that, lets the next ordinary tick answer — the tick already reads `window_activity`, so the witness
costs no extra round trip.

The granularity of `window_activity` then turned out to decide which observable is *primary*, and
an earlier draft had it backwards. It has one-second resolution, and a broadcast writes to several
panes inside one second, so the send and the pane's previous output land in the same tick of that
clock. Measured on tmux 3.2a over six back-to-back sends read at +250 ms: activity confirmed
**2 of 6** while the text was on the pane **6 of 6**. So the primary witness is the **screen** — the
pane's captured tail contains a prefix of what was sent, which is direct evidence and
granularity-free — and activity is the secondary one, earning its place only for a pane that
redraws without showing the text, such as a password prompt. Delivery is witnessed by **either**;
a pane satisfying neither is `sent-unwitnessed`, which is a real answer rather than a failure.

Both are read in **one** invocation (`display -p -t %NN 'ACT #{window_activity}' ; capture-pane -p
-t %NN`), whose line order was stable 6 times out of 6 — but the parse keys on the `ACT` marker
rather than on the first line, because an ordering that merely happens to hold is not one to
depend on.

**The screen witness needs a BASELINE, and the baseline belongs inside the guard chain.** Activity
is compared against a reading taken before the paste; the screen was not, and "the screen contains
the prefix" is satisfied by text the *previous* send left there — which is exactly what the history
view's re-send produces, so a paste that delivered nothing reads as `delivered` on the strength of
the payload already being on the pane. The guard invocation therefore captures the screen between
two token-bearing markers (`BEFORE … #{@hub_<instance>}` / `AFTER … #{@hub_<instance>}`) and
immediately before `paste-buffer`, so nothing can arrive in between, and the witness requires the
occurrence count to **rise**. The markers carry the token because the captured screen is arbitrary
text: a pane can display a line that looks like a marker, but not one carrying an unguessable token,
and `SENT`/`REFUSED` are read only outside the captured block.

That yields **three** outcomes, not two — and each clause is per write path, since a session
target has no pane, no token and no separate Enter to confirm:

| outcome | pane target | session target — the shape a FUTURE `i` takes, NOT semantics this product has (§22.4 defers it) |
|---|---|---|
| `delivered` | the confirmation came back for the right pane with the right token, MORE of the text is on the screen than before (or activity advanced), and the Enter that follows was confirmed too | the socket accepted the reply AND `subscribe`, opened before the write, carried back the target's own screen bytes showing it — a later read of the target's screen, i.e. this section's primary witness on the other transport |
| `sent-unwitnessed` | confirmation matched but neither observable moved — legitimate for a non-echoing pane such as a password prompt — **or** the text landed and the Enter was refused, which is a prompt sitting unsent | the socket answered `ok:true` and `subscribe` did not confirm. There is no second observable on this path — a paneless session has no `window_activity` — so this is the DEFAULT reading of an ack, not an exception |
| `refused` | the guard failed, the token mismatched, or the pane was gone | the whitelist refused before any write — the LISTING must say `blocked` AND `state.json` must carry a `block`, and the text must be non-empty and the id in the roster. Measured, that predicate admitted **1 of 6** blocked rows, and the `block` must be re-read INSIDE the send: read on the slow sweep it passes exactly the pending-Write-prompt state the whitelist exists to exclude (§22.4). A refusal on STATE rather than on identity, and the only one of the three the hub decides for itself |

`delivered` covers the Enter because the alternative is a report that is literally true and
operationally wrong: a prompt pasted into an agent's input box and never submitted has been
delivered and will do nothing, and the operator waits on an answer nothing is computing. The
record also carries `submitted` as its own field, since the three words cannot express it.

**The submit and interrupt keystrokes are guarded and confirmed exactly like a paste.** `if -F`
exits **0 when its condition fails**, so an unconfirmed keystroke that the guard REFUSED comes back
as rc=0 with an empty stderr — reported as delivered, telling the operator an agent was stopped
while it is still running. Both therefore carry the same `display -p -t %NN 'SENT #{pane_id}
#{@hub_<instance>}'` tail and return an outcome rather than an error. There is no witness for a
keypress and there cannot be a cheap one — it leaves no text on the screen to look for — so
`delivered` here means "the key reached the pane the hub still identifies", which is the half that
could otherwise hurt someone else.

Two traps in getting even the confirmation right, both measured: `-t %NN` is mandatory on the
confirmation (without it `display -p` names the server's *current* pane — a two-target batch
printed `OK %1` twice), and the template must contain no literal `%` (`strftime`, §3). The hub
asserts the echoed id **and** token against what it intended before writing history.

`history.jsonl` records the outcome per target, using these three words rather than a boolean.

**Confirmation triggers on state change, not on target count.** "> 1 target" turned out to be
neither necessary nor sufficient: every dangerous *single*-target send is one where something
changed since selection, and the trigger fires on the safe common case of two freshly identified
agents. The rule is a disjunction, all of it computable from the two poll snapshots the registry
already holds — no extra tmux read:

```
confirm if ANY target:
  · the hub cannot identify as an agent
  · was identified at selection and is not now          (the exited-agent case)
  · changed session or window since selection
  · is on a host whose server epoch changed
  · whose previous send was not witnessed as delivered
  · came from the history view rather than the input box
  · or simply: targets > 1
```

Not disableable. A fresh single target sends immediately — the common case does not pay for the
rare one, and "fresh" is now checked rather than assumed.

**Self-exclusion.** The hub identifies itself from its environment (`$TMUX_PANE`, and the socket
+ session parsed out of `$TMUX`), not by asking tmux, and excludes its own pane and session from
tiles and from every target set. Otherwise, running inside tmux, the hub polls the socket it
lives on and can broadcast into itself.

**`history.jsonl`** (`$XDG_STATE_HOME/tmux-hub/`) records time, host, `%NN`, session/window
names as seen, the text, and the confirmed set. It ships with a reader: a history view with
re-send-to-current-selection, plus size-based rotation. A write-only log is not a feature.

## 8. Attach

**Verified end to end by a spike**, because everything in this section was theory until it was
run: a bubbletea program inside a tmux pane (so `$TMUX` was set, exactly as for the hub) pressed
`a`, `tea.ExecProcess` handed the real terminal to `tmux attach -t <target>` on a *different*
socket, the target's own content filled the screen and `list-clients` on it showed
`/dev/pts/67`; detaching returned to the TUI with the callback reporting no error, `list-clients`
went empty, and `q` exited cleanly. `env -u TMUX` on the child is required — plain `tmux attach`
is refused with `$TMUX` set.

That result is why **suspend-and-exec survives as the fallback**: it works in both contexts, so a
hub with no session of its own still has a way to attach. It is no longer the primary path. §20
shipped, and `a` now dispatches on the hub's own coordinates and the target's server
(`internal/ui`'s `decidePossession`) — for a pane on the hub's own server it moves the client
instead of taking the terminal, and for a pane on another server the `new-window` container is not
an enhancement but the *only* mechanism, because `switch-client` cannot cross servers.

| situation | mechanism |
|---|---|
| the hub is **not** inside tmux — `$TMUX` unset, so it has no session of its own | `tea.ExecProcess` hands the terminal over: `env -u TMUX tmux attach -t <$N>` for a local pane, `env -u TMUX ssh -S <ctl> -t host tmux attach -t '<$N>'` for a remote one — the remote target quoted and the local one bare, for the reason below. There is no client to switch and no session to hold a window, so taking the terminal is the honest answer |
| the pane is on the hub's **own** server — equal `#{pid}:#{start_time}` | `switch-client -t $N` then `select-window -t @N`, in that order (select-window addresses a window in the session the client is displaying). The hub keeps running and keeps polling; the way back is the user's own `last-session` and needs no hub |
| the pane is on **another** server — a remote host, or a second socket on this machine | the same attach argv, reused element for element and shell-quoted per element, inside a `new-window` of the hub's own session. The argv runs inside `sh -c '<argv>; s=$?; printf …; read _'`, so a payload that dies leaves a live shell holding the window with its own message still on screen. No option is set on the window: `new-window` is what starts the payload, so a `remain-on-exit on` that follows it is a race (measured below) |
| a **local** pane whose server epoch is unknown | the full-screen fallback, because the window path does not strip `$TMUX` and tmux refuses same-socket nesting |
| an agent row with no pane, or a remote host with no `ssh=`/`ctl=` | refused, with the note naming exactly the field that is missing |

A remote attach needs the **master, never the forwarded socket**, in either container — the full
screen or the window. Measured, attaching over the forward fails `open terminal failed: not a
terminal` even from a real pty, because the client passes its terminal fd over `SCM_RIGHTS` and a
forward drops ancillary data. So a remote host needs
its ssh destination and control path for attach even though polling only needs the socket.

**The remote path has two shells, and the target is quoted for the FAR one.** ssh joins its command
arguments into a single string and hands that to the remote user's shell, so a `$N` session id is
expanded before tmux ever sees it: measured over a live master, `ssh -S <ctl> nuc 'echo -t $0'`
printed `-t bash`, and the argv this section used to describe failed `can't find session: bash`
(rc=1, 0 clients) while the same argv with the target quoted attached — 1 client, the remote status
line on screen. So remote attach had never worked, for **any** session, and the fix is one
`shellQuote` on the target. Nothing else in the argv is quoted: ssh's own `-S`, control path, `-t`
and destination are consumed by ssh and never shown to a shell. The **local** form must stay bare —
`exec` runs it with no shell at all, so a quoted target there makes tmux look for a session literally
named `'$0'`. This is a different shell from the one §20's `shellJoin` answers to, which is the
*local* `$SHELL -c` that tmux runs a `new-window` payload through; on the window path both apply to
the same element and they compose, the outer layer quoting the inner one whole (§20).

The spike verified the suspend/exec/restore lifecycle, **not** interactive key routing through
nested tmux: it drove the pane with `send-keys`, which writes to the pane's stdin and bypasses the
outer client's key interpretation. §3 settled the routing separately and by measurement: with both
servers on the default prefix the **outer** tmux wins, `C-b d` detaches the outer session and
`C-b C-b d` (send-prefix) reaches the inner one.

**The prefix is the user's, and the hub does not touch it.** No code writes one — a distinct
prefix would work only for a session the hub CREATES (§3), and the hub normally runs inside the
user's own tmux, where it cannot change their binding. So the hub *says* what works instead: the
header hint names the way back for the path `a` would take on the row under the cursor, before `a`
is pressed. Both hint strings assume the default `C-b`, which is the assumption a hub that changes
nothing is entitled to; a user who has rebound their prefix reads their own key in place of it.

Attach goes through the master channel (measured 506 ms vs 3217 ms fresh). It cannot go over the
forwarded socket at all.

**Before attaching**, note the width — and that is *all*, because the reason this section had a
modal dialog turned out not to exist.

An earlier draft warned that attaching from a narrower terminal "PERMANENTLY DISCARDS part of this
session's scrollback (~28% at stock history-limit; it does not come back on detach)", and offered
`[Enter] attach anyway / [p] pin session to 120 / [Esc] cancel`. **Measured, that is false in
every part:**

| claim | measured |
|---|---|
| scrollback is discarded | **no.** A 200-wide session filled to 1933 of a 2000 `history-limit`, attached at 100, kept its oldest line (`L01033`) and its line count (677 → 678). `history_size` inflated to **3113** — *past* the limit — with no eviction, because the limit applies when lines are ADDED, not when a rewrap inflates the count |
| it does not come back on detach | **it does.** tmux stores logical lines and rewraps them: one line went 201 bytes at 200 cols → 101 at 100 → **201 again** when a wide client attached, with `history_size` 139 → 248 → 140. The reflow is reversible |
| the session recovers by itself | **no, and this is the one real effect.** After the narrow client detaches the session stays at the narrow width (`window-size latest` sizes to the most recent client), so everything printed afterwards is formatted for it |

So the whole apparatus goes: no modal, no `[p] pin`, no `resize-window`, and no saving and
restoring `window-size` (§11's entry for it is gone too — it existed to serve this).

**Neither replacement is built yet, and both are recorded here rather than claimed.** The
informational line — for a person who may still want to know their terminal is narrower —

```
nuc:trainings is 120 wide, your terminal is 200 — the session will reflow to 200 and stay there.
```

and the cleaner answer, which removes the annoyance instead of describing it: record
`#{window_width}` before handing over the terminal and, if the width changed, issue one
`resize-window -x <original>` afterwards. That is only safe because the reflow is reversible, which
is the same measurement that killed the dialog, and `resize-window` switches the session to
`window-size manual`, so the prior value has to be recorded and restored with it (§11). **No Go
file issues a `resize-window` or renders that line today** — verified by grep, and stated because
this paragraph previously read as a description of shipped behaviour.

## 9. Hosts and configuration

Candidates come from parsing `~/.ssh/config` — multi-name `Host` lines expanded, wildcard
patterns and unroutable names dropped. Membership is decided by a **positive** probe run in
parallel, never by name heuristics: `github.com` eliminates itself, and `tmux -V` succeeds on a
host with no running server so a fresh box is still admitted.

**Membership keys on the probe's output, not its exit code.** Measured, and it caught a defect in
this design's own first probe: `ssh host 'tmux -V; echo …; id -u'` returned **rc=0 for
`hermes-ws`, which has no tmux at all** — the shell's status belongs to the last command, and
`tmux -V`'s own 127 (`command not found`) was swallowed. An rc-keyed probe therefore admits hosts
that will fail mysteriously later. The test is that stdout matches `^tmux \S+`, and the parsed
version is what the capability check in §5 then verifies. (Same family as a `| head` swallowing
`$?`, which bit the same measurement one command later.)

Version spread is real rather than hypothetical — measured across this machine's own hosts: local
**3.7b**, `side-desk` **3.4**, `nuc` and `eu` **3.2a**, `hermes-ws` none.

**A timeout is a THIRD state, not an exclusion, and it is 40% of this fleet.** Measured across three
consecutive probes each, with `ConnectTimeout=6`:

| host | probe latencies | answered |
|---|---|---|
| `eu` | 5.4 s, 9.1 s, 15.7 s, 18.4 s | `tmux 3.2a` every time |
| `web-app` | 4.4 s, 7.4 s, 19.6 s | `tmux 3.2a` every time |
| `web-db` | 3.0 s, 2.9 s, 2.9 s | steady |
| `nuc` | 2.4 s, 2.0 s, 2.0 s | steady |

So two of the five usable hosts swing about 4× and straddle any fixed timeout, while three are
rock-steady. Keying membership on "did it answer within N" makes those two a coin flip, and the flip
is then written into `hosts.toml` as if it were a decision. Both were read as "the host is gone" on
first encounter — twice, by two different readers — which is what makes this worth a state rather
than a tuning exercise.

**Because the answer for such a host depends on the moment, the picker re-probes on demand and says
when it last asked.** One key, and a timestamp beside the reason. Without it, correcting a coin flip
means quitting the hub and starting again, and the file the user is editing is a snapshot of a
moment nobody recorded.
The picker therefore distinguishes three outcomes, not two:

| outcome | the picker offers | the reason says |
|---|---|---|
| answered with a version | a tick box, ticked or not | `tmux 3.4` |
| **timed out** | a tick box, and the user may enable it anyway | `no answer in 10 s — this host is slow rather than absent; enable it anyway, or raise --probe-timeout` |
| answered with something else | no tick box | the remedy for that something (below) |

And `enabled` in `hosts.toml` is the user's decision, so a later probe that times out must not
silently un-enable a host they chose. A host they enabled that stops answering is a `connecting` row
on the dashboard with its reason, which is what §10 already specifies for a host that stops
answering — not a membership question re-asked behind their back.

**Exclusion reasons are the probe's actual output**, each carrying its remedy. Measured, with
timings, since these decide how long a first run can take:

| observed | wall | shown as |
|---|---|---|
| `tmux 3.4` on stdout | 216–7648 ms | admitted |
| `zsh:1: command not found: tmux` | ~220 ms | `no tmux — install it, or leave this host off` |
| `ssh: Could not resolve hostname …` | 271–3253 ms | `DNS does not resolve — stale ssh config entry?` |
| `Invalid command: tmux -V` (github.com) | 873 ms | `not a shell host — a git remote` |
| `remote:` with rc=128 (gitlab.com) | 867 ms | `not a shell host — a git remote` |

Ten hosts probed concurrently took **7.65 s** wall, all of it the two slowest hosts. So probing
can never gate the UI — see §16.

`~/.config/tmux-hub/hosts.toml` (`$XDG_CONFIG_HOME/tmux-hub/`) holds `enabled`, `tags`, and
`tmux_args` — what §5 turned the `socket` override into, extra arguments for the *remote* tmux
(`-L other`) rather than a path this machine computes. It is CONFIG rather than state: it holds the
user's own decisions, while `hidden.json` and `history.jsonl` are what the hub derived for itself and
stay under `$XDG_STATE_HOME`. **`tmux_args` is consumed**: `hostset.Entry.TmuxArgs` becomes
`hub.Host.TmuxArgs`, `Host.Target()` carries it into `tmux.Target`, and the seam's `build` inserts
it after the socket/ssh decision and before the verb — so `tmux_args = ["-L", "work"]` polls the
server on that label. The override is validated like every other argv that reaches tmux, because a
hand-edited file is not a trusted caller: a `#{client_activity}` hidden in it would segfault the far
server exactly as one in a format string would. `tags` is still persisted and not yet consumed —
the picker writes it and nothing reads it. The file is **generated** by the picker
and hand-editable; a generated file cannot drift from what the picker understands. A file that
cannot be parsed **stops the program** rather than reading as an empty one, because an empty
host list is indistinguishable from a first run and the next save would then overwrite it.

An enabled entry becomes a host with no socket at all, addressed over the master at
`ControlPathFor($RT, alias)` — the same path `Ensure`, `Stop` and the startup reconcile derive,
which is what lets one run adopt the master the last one left.

The picker is a first-class screen, always reachable from the dashboard with `p`, and shown at
startup when `hosts.toml` has decided nothing yet — that is what makes zero configuration a
working configuration. An enabled host that has *gone* missing is reported on its own row with
its remedy instead: deciding it at startup would need the probe §16 keeps off that path, and a
picker opened over a host that is merely slow would open on every start (two of five usable
hosts swing fourfold, above). Each excluded host shows its reason and its remedy.

Empty states are specified screens, not blank panels: no local tmux, local tmux with no server,
no hosts enabled, host up with zero sessions.

## 10. UI

```
┌─ inbox (panes) ───────┐┌─ nuc trainings %3 ───────┐┌─ local live1 %0 ─────────┐
│ nuc trainings         ││ ● Ran tests, 3 failed    ││ ❯ ▏                      │
│  ⚑ %3 claude    needs ││ Do you want to proceed?   ││ ⏵⏵ bypass permissions on │
│ local live1           ││  1. Yes  2. No           ││                          │
│  ▸ %0 claude    idle  ││ ▏                        ││                          │
│ nuc work              ││                          ││                          │
│  · %7 claude    works ││                          ││                          │
│    %8 bash      idle  ││                          ││                          │
│ st worker             ││                          ││                          │
│  ✱ %1 claude    quiet ││                          ││                          │
└───────────────────────┘└──────────────────────────┘└──────────────────────────┘
 hosts: local up · nuc up · st degraded:old-version        → 2 selected
```

Rows are **panes**, indented under a session header that is a label rather than a target (§7).
The identified agent pane is marked; a sibling shell in the same window is visible but is not
what a session-level action would hit, because there are no session-level actions.

- Inbox: every pane on every host, sorted by attention state — `Needs`, `Error`, `Quiet`, `Idle`,
  `Works` (§6), then host, session, pane id so the order is stable between ticks. The comparator is
  `registry.SortByAttention`, and it is exported for a reason: anything that builds a pane list
  without going through `Registry.Update` shows rows in construction order, which is what the mockup
  generator did until measured — every screen in `docs/ui-mockup.html` was in fixture order, and
  attention ordering is the one property the dashboard exists for.
- **The footer is one line, and a note outranks the host status.** Measured at 80×24 with a note
  showing: `local up · st degraded:format · nuc up` is not on screen at all, so a degraded host is
  invisible for as long as the note is. The note wins deliberately — it answers something the user
  just did, and ambient status does not — but the earlier claim here that host status is *always*
  visible was false, and a wrong sentence about a safety-relevant field stops the next reader from
  checking. Carried in `docs/known-issues.md`.
- Grid: tiles rendering each pane's **content lines** (§6), not its bottom N lines.
- Keys, and the layout's width thresholds, are in §16 — they are experience commitments with
  numbers behind them, not incidental choices.
- **The hub excludes its own pane**, read from `$TMUX_PANE`. Without it the hub polls the pane it
  runs in, captures its own screen and renders that screen in a tile whose content is its own
  screen — an infinite mirror, observed live, plus a full-screen capture per tick spent on itself.
  A host whose only pane is the hub reads `up-empty`, not `down`.
- **A host that stops answering keeps its panes**, each marked `stale` with its last screen — it
  must not make sessions vanish, and it must not leave them looking live on the strength of a
  CAPTURE nobody has refreshed either (observed: a killed tunnel left a row reading `works` from
  exactly that). The age goes in the tile header, not the row, because `(last seen …)` truncates
  to `(las` in a 28-column inbox.
  **Two consequences of there being a SECOND producer, both of which a reported defect had to
  teach.** First, a stale pane is still a join target for the listing. `MarkHostStale` marks a
  host's pane rows and deliberately spares its agent rows — an agent row's liveness has nothing to
  do with the tmux tunnel — so skipping a stale pane left the listing row with nowhere to fold and
  put one session on screen twice: a `stale` row from the pane producer beside a live row from the
  listing, the pair appearing and merging as the host flapped. The operator reported that as a
  duplicate whose status blinked, and it was one session drawn once by each producer. Second,
  `stale` yields to a FRESH listing word on the row, because that word comes from the producer that
  is still answering: measured on the fleet, a host whose tmux socket had gone quiet was reporting
  `working` with a pid for that very session. A row with a fresh fact therefore says what the
  session is doing, a row without one still says its host is gone, and the host line names the host
  and the reason in both cases. A RETIRED pane (`state.Gone`) stays excluded from the join, which is
  the case the exclusion was for: folding a live fact into a corpse would have it read `works` for
  the whole freshness window while the session that is genuinely alive — the one with a door on it —
  never got a row at all.
- **Hosts are polled concurrently**: a remote tick is ~1.4 s against a local ~5 ms, so a serial
  loop made the dashboard update at the speed of the slowest host. The registry is guarded, and so
  is the poller's host list — each tick snapshots it under `Poller.mu`, works on its own copy, and
  merges back only the fields it owns, so a host added from the UI goroutine mid-tick cannot cost
  the tick its status writes. **`-race` proves only what the tests actually run concurrently**: the
  suite was 16/16 green while that list was unguarded, because no test called `Add` against a live
  `Tick`. It does now.
- Host status is a **positive** assertion with a reason, never the absence of an error. A host
  that vanished must never silently drop its sessions out of the list; they stay, marked stale
  with an age.
- **Hiding panes**: `x` hides the pane under the cursor (or every selected pane when there is a
  selection), `X` toggles showing all hidden panes. The selection is the user's stated subject,
  so hiding acts on it when present — the same rule the send path uses. Hidden panes that are
  waiting for the user (`needs` state) are automatically resurfaced, marked with why they came
  back (§18). The footer counts how many panes are hidden and how many of those are blocked, so
  the user knows a hidden pane is waiting for them. While `X` is on, each row that is only on the
  screen because of it carries `[x]` — the key that hid it — and the footer says `X shows all rows`
  instead of a count of what it is keeping off, because the two states must not read alike: the
  gesture exists to answer "what did I hide?", and a screen where a hidden row and a visible one
  render identically answers nothing. A resurfaced row keeps `[↑]` alone, which says strictly more.
- **Two views**: the inbox files rows under `HOST SESSION`, and `v` switches it to filing them under
  the PROJECT §21 derives — the same vocabulary the project list and the aliases use, so the two
  screens name the same things the same way. It changes the HEADERS and the inline rows, never the
  ORDER: the dashboard exists to put what wants the operator first, and a view that gathered each
  project's rows together would bury a waiting session inside a quiet project. A project's header
  therefore comes round twice when the sort brings its rows round twice, marked `(cont.)` exactly as a
  session's header already is (§21.11, known-issues S2). Two consequences worth stating. Pane-less
  AGENT rows take no header in the host view — their name IS the row — and they DO take one in the
  project view, because there a header gathers many rows and those rows are most of what the fleet has
  (40 of 43 on the machine this was built on), so skipping them would have grouped only the three rows
  with panes. And below 100 columns there are no headers at all, so the project view changes the ROW
  there — `local/sess1` becomes `billing-iac` — or `v` would do nothing at the one size §16 commits
  to. The choice is not persisted, deliberately: it is a way of LOOKING, one keystroke to change and
  named in the header while it is on, which is the standing `X` already has.
- **Favourites**: `f` pins the session under the cursor, or on the project list the PROJECT under it,
  and pinned rows sit above everything else. It is a SPLIT rather than a re-sort: the attention order
  is untouched inside each half, so a pinned row that is waiting still leads the pinned band and an
  unpinned one that is waiting still leads the rest. Three decisions worth stating. `f` acts on the
  CURSOR and never on the selection — unlike `x`, because pinning is a statement about one thing the
  operator keeps coming back to, and a selection-wide pin could not be undone by pressing `f` again.
  The key is the SESSION and not the pane, so a pinned session survives a window being split — and
  WHICH fields identify it depends on whether the hub knows its Claude uuid. Where it does, the uuid is
  the whole key, because the other candidates all change under a session that GAINS A PANE: the door
  creates a tmux session named `<name>-<short id>`, the join folds the pane-less row into that pane,
  and the row goes from `{agent, local, 20260817-cicd}` to `{pane, local, 20260817-cicd-30f3382b}`.
  Version 1 keyed on those three and the pin came off at exactly that moment — reported from real use
  as "after attaching to a favourite it stops being in the list", which is what dropping out of the
  pinned band looks like on a 45-row fleet. The uuid is also GLOBAL, so a shared `~/.claude` (§22.12)
  pins one session rather than one per host. Where there is no uuid — a shell, a `cat`, an agent a walk
  found and never adopted — the key stays `(kind, host, name)`, and there the same name on two hosts is
  two sessions, because a name identifies a session ON a host while a uuid identifies the session. A v1
  file is REFUSED rather than migrated: mapping a name to a uuid needs the fleet, which a reader does
  not have, so it says so and asks for the pins again. A row with no session name and no uuid is
  refused rather than keyed on `("", host)`, which would match every other nameless row there. And the star and the ORDER come from one
  predicate (`isFavourite`), because a screen whose marker and order disagreed would be worse than
  either alone — the operator could not tell whether the band at the top was the pinned one.
  `internal/fav` is a separate set from `internal/hide` on purpose: the two fail in OPPOSITE
  directions, since an unreadable hidden set must leave everything VISIBLE while an unreadable
  favourites set must leave the ORDINARY order, and one file would make one of those choices for the
  other.
- **Lifecycle operations**: `n` opens the launch form to create a new Claude Code session in a fresh
  tmux window, or in a new SESSION named after the working directory's last path segment — and when
  that name is already taken on the host, the session becomes `<name>-<short id>`, carrying the uuid
  the launch already generates for `claude --session-id`, so it is unique by construction and one
  retry always succeeds. `new-session -s <name>` is rc=1 `duplicate session: <name>` otherwise, and
  the launch used to hand that sentence back with no remedy, which made a directory whose basename was
  already a session name impossible to launch into TWICE — reported as "creating a new session does
  not work", and it was, every time. The fallback is §22.3's door shape through the same function
  (`launch.SessionNameWithID`), so the two paths cannot drift into two conventions, and the note names
  the session it created because one the operator did not name is one they cannot find. Two more things
  the form owes a person typing a path: a leading `~` is RESOLVED against the hub's own home for a
  local host, because tmux neither expands nor refuses it — measured on both fleet versions,
  `new-session -c '~/somedir'` is rc=0 with the pane's cwd at HOME rather than the directory, a session
  in the wrong place at rc=0 — and for a remote host it is refused with the reason, since `~` there is
  the far user's home and the hub has not asked for it. A SPACE reaches the field: bubbletea reports one
  as `KeySpace`, the typing arm named only `KeyRunes`, and the character was dropped, so a directory
  called `with space` arrived as `withspace` and the launch refused a path that does not exist. All
  three text fields answer that question through one function (`typedText`), because the same rule had
  already been got wrong in the OTHER direction elsewhere — folded together it inserted the space twice
  (§19); `R` restarts the selected pane by respawning it with `claude --resume <uuid>`,
  preserving the conversation (the hub knows the session ID for agents it created via Adopt); `K`
  kills the selected pane after confirmation. Kill always confirms, naming what is running (identified
  agent / dead pane / unidentified), so "Kill this?" with no subject cannot destroy the wrong window.
  Restart requires exactly one selected pane and that the hub knows its session ID (hub-launched agents only).

Restart requires exactly one selected pane and that the hub knows its session ID (hub-launched agents only). `R` stays **pane-only**, and that is measured rather than cautious: `--resume` of a session whose process is still alive has **no engine-side refusal** — it returned `is_error=false`, kept the same id, and appended into the transcript that process held open on 5 descriptors (§22.1). What makes `R` safe is `respawn-pane -k`, which kills the holder in the same invocation. So `R` must never be extended to a pane-less agent row even though the hub does know those rows' `SessionID` (§17's producer supplies it): there the answer is `a`, which wakes a session whose worker is dead through `claude attach` and replays the transcript (§22.3).
  Measured: `respawn-pane -k` keeps `pane_id` and the `@hub_*` stamp while changing `pane_pid`, so identity
  must be invalidated explicitly after restart — the stamp survives but the process is different.

## 11. Killed problem classes

The point of the design. Each row is a class that cannot occur, and the mechanism that makes it
impossible — not a check that catches it.

| class | mechanism |
|---|---|
| "the hub crashed a server by reading it" | `client_*` never emitted at any version; version table fails closed on unknown; and the emitted format is **verified against a real pane at connect**, because an allowlist alone is unfalsifiable |
| "a format field was silently empty and read as 1970 / alive / absent" | connect-time required-field assertion naming the missing field → `degraded:format` |
| "text landed in the wrong pane" | targets are `%NN`; session and window names are labels only, never targets; there is no **tmux**-session-level action. §22's `a` on a pane-less row is the one action addressed at a *Claude* session id, and it carries no text — it opens a terminal — while `i` to an agent row is deferred (§22.4), which is what keeps every text path targeting a `%NN` |
| "the pane id was recycled and the send hit a stranger" | per-pane `@hub_<instance>` token, re-stamped each tick from the process walk and checked **inside the same invocation** as the send (`if -F '#{==:…}'`), plus server-epoch comparison invalidating selections |
| "the prompt executed as a shell command" | the agent pane is identified by walking `#{pane_pid}`'s process tree, never by `pane_active` or `pane_current_command` (both panes report `bash`) |
| "the confirmation lied" | no literal `%` in any `display -p` template (strftime eats it, rc=0); `-t %NN` mandatory on confirmations; echoed id asserted equal to the intended id |
| "a healthy host read as down forever" | the hub compares the dial's `SO_PEERCRED` peer against the ssh pid **it spawned**, so identity never depends on a self-reported hostname that a cloned VM or `localhost` legitimately shares |
| "my other machine's hub broke this one" | remote state is namespaced per hub instance — option `@hub_<instance>`, buffer prefix `tmux-hub-<instance>-`, and a connect sweep that touches only its own prefix — because a laptop and a desktop pointed at one host is a normal setup, and `flock` is per-machine |
| "the crash-repair path destroyed scrollback" | a `mutations.json` entry carries the server epoch; on a mismatch it is never applied, only surfaced |
| "probing created what it was probing for" | no `start-server` on the probe path; socket unlinked before spawn, under the lock |
| "the hub's prompt ended up in the user's paste buffer" | `paste-buffer -d` (atomic), `delete-buffer` in a `defer` as its own invocation, `tmux-hub-*` swept at connect and shutdown |
| "a pane's own text forged the capture framing" | captures framed by **declared length** (`capture-pane -p` with no `-S` emits exactly `#{pane_height}` lines); a block that does not consume its count rejects the batch |
| "a pane started waiting for input and the hub noticed late" | no delta gate exists to be wrong: every pane's classification zone is captured every tick, affordable because the zone is 18 % of a capture |
| "the tile was on screen and told the user nothing" | the tile renders content lines, not the bottom N lines — measured, the bottom 10 lines of an idle Claude pane are entirely chrome |
| "a host with no tmux was admitted and failed later" | membership keys on stdout matching `^tmux \S+`, never on the probe's exit code |
| "discovering hosts blocked the first run for 8 seconds" | the local dashboard paints before any network work; host rows arrive independently |
| "my text became keypresses" / "my text got truncated" / "my text silently did not arrive" | **one** primitive: `load-buffer -` from stdin, `paste-buffer -d -p -r`. No argv in the payload path at all. `send-keys -H` is deleted — it takes one byte per argument, so the obvious encoder delivers nothing at rc=0 |
| "the confirmation said OK and nothing arrived" | delivery requires `#{window_activity}` to have advanced between a pre-read and the confirmation, in the same invocation; the **send** outcome is three-valued (`delivered` / `sent-unwitnessed` / `refused`). `history.jsonl`'s `Outcome` column carries a **fourth**, `launched`, written by §19's launch path (`ui.model.launch`, its `history.Entry` with `Outcome: "launched"`), and `RenderHistory`'s `switch e.Outcome` has no `default`, so a launched entry renders `?`. A consumer of that log must enumerate the writers, not these three words |
| "the guard passed on a pane it never stamped" | `-p` mandatory on every stamp (a scope-less `set` lands at session scope and passes for every pane in it); the negative-control test lives in the **same window** as the positive; a stamp is followed by asserting `show -g` and `show -t <sess>` both answer `invalid option` |
| "the send went to a different pane than the guard checked" | every sub-command carries its own `-t` (a nested command does not inherit the `if`'s target), and the confirmation echoes the **token**, which a wrong pane cannot produce |
| "an empty target sent to whatever pane the user last touched" | the seam refuses a `-t` value that is not `^%[0-9]+$`, and the guard builder refuses an empty token |
| "the agent had exited and the prompt ran as a shell command" | the token is re-stamped every tick from the process walk and cleared the moment the walk stops finding an agent, so the guard means "identified within one tick" |
| "the first paragraph of my prompt executed" | multi-line goes through `paste-buffer -p -r`; Enter is always a separate, explicit act |
| "I broadcast to something I couldn't see" | targets must be selected tiles, drawn on screen; tags select, never target; no headless send API |
| "the batch said rc=0 but half the targets missed" | per-target confirmation emitted in the same invocation; `history.jsonl` records the confirmed set |
| "a pane name broke the parser" | the delta format carries **no free text at all**; labels come from sibling sub-commands with exactly one trailing free-text field each |
| "the host looked healthy while every read failed" | health is only the positive probe; process liveness and socket existence are never health |
| "a slow host froze the UI" | per-call deadline + kill; bounded worker pool; per-host phase offsets |
| "identity inference failed and matched a forwarded socket against the local process table" (the one Critical defect, 97 of 3117 local pids answered "agent here") | **for hub-created panes only**: the hub generates the uuid, passes `claude --session-id <uuid>`, and `new-window -P -F '#{pane_id}'` returns the pane id in the same call, so the pane↔session binding is known. The process-tree walk in `internal/proc` is never consulted. Foreign panes still need the walk; the kill is scoped honestly |
| "a stale socket from a crashed run blocked the host forever" | startup reconciliation is the normal path: probe → adopt or unlink → rebuild |
| "two hubs fought over one tunnel" | `flock` before any transport work |
| "the hub sent input to itself" | self-identified from `$TMUX_PANE`/`$TMUX`, excluded from tiles and targets |
| "sockets landed somewhere unpredictable and every host went down" | `XDG_RUNTIME_DIR` asserted set and absolute at startup with the remedy named, because an unset value silently yields a **relative** path; the label carries a hash so two aliases cannot collide |
| "a dead pane took its error message with it" | the registry caches every pane's last capture, so the evidence outlives the pane without mutating tmux |
| "attach silently destroyed scrollback" | width-only comparison, explicit warning naming permanence, pin offered |
| "the hub's resize left the session pinned forever" | prior `window-size` recorded before any resize, restored on unpin |
| "a config file drifted from what the tool understands" | `hosts.toml` is generated by the picker |
| "the failure taxonomy is untestable without a live remote" | one `Run()` seam; host state is a pure function of `(rc, stderr, masterCheck)` |

### Mutations the hub makes to the user's tmux — and their reversal

The hub writes options into the user's tmux in exactly **two** places, both only on an explicit
user action, and never as a side effect of observing. (Creating and destroying panes, windows and
sessions is the *subject* of §19's lifecycle keys, not a side effect, so it is not counted here.)

| mutation | scope | reversal |
|---|---|---|
| `set -p -t %NN @hub_<instance>` | one pane, **re-stamped every tick while selected** | `set -pu` on deselect and the instant the process walk stops finding an agent; invisible globally, changes no tmux behaviour, and it is what makes the guard mean what §11 says |
| `set -w -t @NN remain-on-exit on` | one window, **only the launch pane the hub itself created** (§19). §20's possession window was the second such place and no longer is: the option was set after `new-window`, which is what starts the payload, so it raced a payload that died first (measured on 3.7b, 12 trials: `false` survived 6, lost 6). That window keeps itself open through its payload instead, which is also the only way the payload's own message reaches the operator — a dead pane shows tmux's banner and nothing else | none needed; the window belongs to the hub and the operator closes it. Never `-g` and never on a window the user made, because there it accumulates zombie windows forever (§6) |

The re-stamp moves the first row from selection-time to per-tick, for selected panes only. That is
the same admitted mutation at a higher frequency, and it is the price of the guard proving *process*
identity rather than pane identity. So "the poll path is pure" is precise: pure for every pane the
user has not selected, and for selected panes the only write is a token the user's tmux does not
act on.

The pin's recovery design is recorded here for whenever it is built, and is **not** in the code
today (no Go file writes `resize-window` or reads `mutations.json`). A `mutations.json` entry is
keyed by session **name**, because `session_id` and every pane option reset on a server restart. It therefore also carries the **server epoch**, and on a mismatch it is
never applied — only surfaced ("I pinned `nuc:trainings` before crashing and cannot prove this is
the same session"). Applying it blind would write `window-size` to a session the hub never pinned,
and §3 measured that a later differently-sized attach then permanently discards ~28 % of that
session's scrollback. A repair path that can destroy data must fail loud, not silent.

Deliberately **not** done: `remain-on-exit` on a window the hub did not create (§6 — it is a
*window* option, so it cannot cover a session, and on the user's own windows it accumulates zombies
forever), `remain-on-exit` on §20's possession window either — a payload wrapped in a shell that
outlives a failure keeps that window without a race and without a corpse, and the option on top would
only break the promise the wrapper's own last line makes, since on failure the keypress it asks for
would leave a dead pane instead of closing the window — `start-server` on a remote host during probing (§5 — it squats the forward path and
wedges recovery; offered as an explicit action instead), and any global (`-g`) option anywhere.

Also **not** done, and this table used to claim otherwise: the `resize-window` pin. §8's warning
flow that needed it is gone — the scrollback loss it protected against does not happen — and the
width-restore-on-detach described there is designed but **not implemented**; no Go file issues a
`resize-window`. It is listed as an open item rather than as a mutation, because a mutation the code
does not make is the one kind of entry that cannot be audited by reading the code. The same goes
for the `prefix C-Space` this table listed: no code writes a prefix, and §8 says why it never could
for a session the hub did not create.

Named paste buffers are a **transient** mutation, not a free one: a batch that aborts skips its
`delete-buffer` tail and leaves the payload as the most recent buffer (measured). Hence
`paste-buffer -d`, a `defer`red `delete-buffer` in its own invocation, and a `tmux-hub-*` sweep
at connect and shutdown (§7).

Recorded mutations would live in `$XDG_STATE_HOME/tmux-hub/mutations.json` so a crash between set
and restore is repairable at next startup rather than permanent. Neither option the hub actually
writes needs that file: a pane token is re-stamped or unset on the next tick, and `remain-on-exit`
sits on a window the hub created and the operator closes.

Consequence worth stating plainly: **the poll path is pure.** Every command the hub issues while
merely watching is a read; both option writes above sit behind an explicit user action (select a
pane; create a window with `n` or with `a` on another server's pane). So the hub cannot change a
host by observing it — which, after the incident that opens §3, is the property that matters most.

### Supported tmux versions

**3.2a and newer**, and that is a decision rather than an observation: the fleet is
kept current, and both the local machine and every ssh host are ours to upgrade.
Measured on 3.2a (`nuc`, `eu`) and 3.7b (local), with every write-path primitive
verified on both.

What the floor buys is the removal of a whole line of defence. §3's incident — a
`#{client_activity}` read that segfaulted a 3.2a server — is why the seam carries a
forbidden-format list, and that list stays, because the cost is one string compare
and the failure it prevents is somebody's lost work. But there is no version
allowlist, no per-version capability table and no graceful degradation for older
servers: a host below the floor is a host to upgrade, and the field assertion
already reports one that does not answer as expected.

## 12. What is *not* killed structurally

Honest list. These stay heuristic or unresolved, and the design must not read as if they were
guaranteed.

### The standing write capability — recorded, not a design driver

Stated once so it is not a surprise later: **while tmux-hub runs, any process running as you can
execute code on every connected host.** `$RT/<label>.sock` is a bearer capability
(`tmux -S <sock> new-window 'curl … | sh'`) ambient to the uid, and the ControlMaster socket §8
needs for attach is another. This is inherent to being an ssh-based control panel and is **not**
being designed against at this stage — the project's priorities are the experience dimensions in
§16.

Two consequences are kept, but for **experience** reasons rather than safety ones, which is why
they live here as a note and in §16 as commitments:

- **`enabled` means "connect on first use", not "connect at startup"** — kept because it is
  *faster to first paint* (§16), which is the reason that survives the reprioritisation.
- **`readonly = true` may skip the forward** and poll through the master at 506 ms against 485 ms.
  Kept as an option a sysadmin can want on a production host, not as a mandate.

| problem | why it survives | containment |
|---|---|---|
| `classify()` is pattern-matching against a TUI that can change its wording | Claude Code's screen is not an API | patterns in config, not code; fixtures with both poles; a wrong classification only mis-sorts the inbox — it can **never** cause a send, because sends require an explicit selection and a token match |
| identifying *which* pane runs the agent | no tmux field distinguishes it (both panes report `pane_current_command=bash`) | process-tree walk under `#{pane_pid}`; when the walk is inconclusive the pane is shown unmarked and a send to it requires the same confirmation as a multi-target send |

process-tree walk under `#{pane_pid}`; when the walk is inconclusive the pane is shown unmarked and a send to it requires the same confirmation as a multi-target send. §22 adds three `claude` verbs to this population and the walk's exclusion list is a **blacklist**: `internal/proc`'s `daemonRoles` names `bg-pty-host`, `bg-spare`, `--bg-pty-host`, `--bg-spare` and `mcp` (verified), so `attach`, `logs` and `stop` all read as interactive agents by default. That is correct for `attach` — a prompt typed into that pane does reach the session — and wrong for the other two, which print and exit. Unmeasured and worth saying so: `SessionID` joins a pane to an agent row through `CLAUDE_CODE_SESSION_ID` read from the agent's CHILDREN (`internal/proc/session.go`), and whether a `claude attach` client's children carry it decides whether the pane §22 creates merges with its own agent row or appears beside it |
| `#{window_activity}` is per **window**, not per pane | tmux exposes no per-pane output timestamp among the verified fields | **This row's ruling of "low stakes: only durations" was WRONG, and its own reasoning was the fix.** It is true that every pane's zone is captured every tick — but `Classify` returned `works` from the TIMESTAMP before it read anything from that capture, so a pane sharing a window with a sibling printing more often than `FreshFor` could never reach `error`, `quiet` or `idle`: pinned at `works`, rank 4, the bottom of the inbox, with `needs` surviving only because it is tested first. Measured: two panes in one window both report `act=1786827863`, then both `…865`, including the one sitting untouched on `fatal: not a git repository`. **Fixed** — the hub now keeps each pane's own last-changed tick (`registry.markActivity`) from three signals: `history_size`, `cursor_y`, and the ZONE, which is the capture this row already named. First sight seeds from the window timestamp, so the cold answer stays defined. |
| a broadcast is irreversible | sending into a live process cannot be undone | token-guarded send, confirmation for >1 target and for unidentified panes, targets on screen, per-target confirmed log, `C-c` hotkey |
| the dashboard is up to ~500 ms + interval stale | that is the RTT | every tile shows its own age; a stale tile is visibly stale |
| version skew between local client and remote server | the hub drives the local tmux binary against a remote server | required-field assertion at connect; `degraded:old-version` / `degraded:format` visible per host with the field named |
| the `up-empty` discriminator reads a transport-level signal | dial-accepted-then-EOF is sshd behaviour, not a contract | corroborated by the identity assertion, which fails independently; **Open (§14.3)** |
| a foreign pane's agent exited with an error, but the hub cannot distinguish that from the pane being closed | `remain-on-exit` is a **window** option, and the hub only sets it on windows it creates. A foreign pane exits with `remain-on-exit off` (the default), so it is destroyed before any tick can see `pane_dead` | the hub will not pretend to report what it cannot observe. Hub-created panes do report their exit code (§19) |
| **a live remote attach is verified by hand, never by a gate** | it needs an ssh control master to a real host, and the suite must not assume one. A faked far side is worse than none here: the defect that shipped was the *remote user's shell* expanding the target, which only a real remote shell performs | the two-shell quoting is proved locally through **real** shells — the window payload runs through `sh` twice against an `ssh` shim that prints its own argv, and the argv unit test writes both literals by hand (`'$0'` remote, `$0` local). The end-to-end run is recorded as a measurement in §3, and it is what found the defect: unquoted, `can't find session: bash` with 0 clients; quoted, 1 client and the remote status line on screen. Read this row as "the gate covers the quoting, not the attach" |
| remote directory verification and completion require `ssh=` | without an ssh master, a forwarded socket carries tmux but not a shell — the hub cannot list directories or verify a path exists before the launch | the launch form shows a hint when `ssh=` is absent and accepts the typed path anyway (refusing would be ceremony). tmux's own error is what the user sees if the path does not exist |

One entry left this table and one came back, and both movements were driven by measurement rather
than by argument. `%NN` recycling moved to §11, because a token guard evaluated **in the same
invocation as the send** removes the window rather than narrowing it. Socket adoption moved to §11
and then partly back here: `#{host}` + `#{socket_path}` were defeated end-to-end by an
unprivileged same-uid process, so they are corroboration, and the guarantee now rests on
`SO_PEERCRED` plus `lstat`. A table that only ever moves rows toward "guaranteed" is not being
checked.

### Remote possession assumes the remote *default* socket

The remote-attach command the hub reuses for possession (§20) is
`ssh -S <ctl> -t <dest> tmux attach -t '<target>'` — with **no `-S` on the remote side**, so it
targets the default socket of the remote machine. (The quotes around the target are the far-side
shell's, §8; they say nothing about which socket that shell's tmux talks to.) But
`--host label=/run/user/1000/nuc.sock`
means the operator forwarded *some* socket, and the hub never learns the path that socket has
on the far side. If the agents live on a non-default remote socket, remote possession lands on
the wrong server or fails.

This is pre-existing behaviour of attach rather than something possession introduces, and it is
accepted for now rather than fixed: closing it means a new `--host` field (an `rsock=`) carrying
the remote path. Recorded here so a reader does not mistake "remote possession works" for
"remote possession works for any socket".

## 13. Testing

| unit | how |
|---|---|
| `internal/tmux` | argv/parse table tests; integration against a real local tmux on a **private** socket — `-S <t.TempDir()>/tmux.sock`, so the socket dies with the test |
| `internal/state` | golden fixtures from real panes; **both poles required** per state |
| `internal/hostset` | the probe's `Runner` is a FUNC seam, not a fake `ssh` on `PATH`: the state table is driven by `(stdout, stderr, rc)` triples with no network and no `PATH` to leak between tests. Membership keys on stdout, never rc (§9) |
| `internal/registry` | pure table tests, including name collisions and delimiter injection |
| `internal/ui` | assertions on the string `View()` returns, driving the model directly through `Update`. **No `teatest`** — a screen that is defined, tested and never called is this project's signature defect, and only a frame test that reads `View()` fails against a stub |

Every trap in §3 that a future edit could reintroduce gets a test whose failure names it. These
are not hypothetical regressions — each one was a real defect in an earlier draft of this design:

| assertion | catches |
|---|---|
| no emitted format string contains `client_*`, at every version branch | the incident |
| a send of a known string must **arrive**, asserted by capturing the target pane — not by the command's rc | `send-keys -H` delivering nothing at rc=0, and any future primitive with the same shape |
| after a stamp, `show -g @hub_<inst>` **and** `show -t <sess> @hub_<inst>` must both answer `invalid option` | a scope-less `set` landing at session scope and failing open |
| the negative-control pane for the guard test is in the **same window** as the positive | a session-scoped mis-set passing both poles when the control is in another session |
| a batch whose guard `-t` and send `-t` are deliberately crossed must be **refused**, and the echoed token must match | a nested command not inheriting the `if`'s target |
| `Run()` refuses a `-t` value that is not `^%[0-9]+$`, and refuses an empty expected token | an empty target delivering to the user's current pane at rc=0 |
| a pane whose agent process has exited must produce `REFUSED`, not `OK` | the token proving pane identity rather than process identity |
| startup refuses an unset or relative `XDG_RUNTIME_DIR` **before** joining `tmux-hub`, and a sweep reaches nothing outside `$RT` | a relative socket path created wherever the process started, and a destructive sweep enumerating the shared runtime directory. The symlink/ownership/mode refusals this row used to demand are ruled out of scope (§5) |
| no `display -p` template contains a literal `%`; every template yields a non-empty result | strftime swallowing a whole confirmation, rc=0 |
| a 3-target broadcast batch must return **3 distinct** pane ids | a missing `-t` on a confirmation (all ids identical) and the strftime defect (all ids empty) |
| a fixture with `\|` in `pane_current_command`, `session_name` **and** `window_name` at once | delimiter shift, in the one arrangement where a single bounded split is not enough |
| a pane printing the framing marker verbatim; and a pane resized mid-batch | forged framing — the batch must be **rejected**, not mis-attributed |
| a batch whose first target is a dead pane must leave **zero** `tmux-hub-*` buffers | the paste-stack leak |
| a stamped pane and an unstamped pane must produce `OK` and `REFUSED` respectively | the token guard, both poles |
| the poll format run against a live pane must fill every structural field | an unfalsifiable allowlist entry silently emptying a field |
| the hex encoder for a non-ASCII string must emit its UTF-8 bytes | `-H` taking bytes, not codepoints |
| a remote host's `tmux -V` answer must be the REMOTE version | a local server answering for a far one. The `down:local-squatter` classification this row used to demand is deleted with the forward (§5): with no forward path there is no local socket to squat, so what survives is the assertion that the version came from the far end |

Non-negotiable dev rule, from the incident: **tests and probes never touch a tmux server the
process did not create.** Every tmux invocation in the test suite carries an explicit `-L`/`-S`
private socket. A tmux command with no socket flag talks to the developer's own live server;
CI-visible lint should reject one in the repo.

## 14. Open questions

Each of these is a measurement, not a matter of taste. None blocks step 1 of §15.

1. ~~Per-pane activity variable — does one exist?~~ **Answered: no.** `pane_last_activity`,
   `pane_activity`, `pane_written` and `pane_bytes` are absent from the 3.7b binary;
   `pane_unseen_changes` exists but tracks an attached client's viewing state and is always 0 for a
   detached hub; `history_bytes` is per-pane but blind to in-place redraw. So `window_activity`
   stays the delta, `quiet` durations are window-wide, and `history_bytes` is available as a
   per-pane "did this one scroll" secondary.
2. Whether `needs` warrants an out-of-band signal, and how acknowledge/mute state is persisted.

§22 changes the population this question is about: **57 of 65 rows** are `background` (§22.1,
   2026-08-16, `--all`, three hosts, one snapshot), so the rows that would trigger the signal mostly have no window — and
   `window_bell_flag`, the terminal bell and `display-message` are all window/client observables.
   For the rows that actually wait the trigger has to come from the agents producer, which §22
   measured silent (`working`, no `block`) for 136 s on a session that was waiting —
   through the daemon's own `list`; whether `claude agents --json --all` reports it differently is unmeasured (§22.10).
   `window_bell_flag` stays available for pane rows only.
   Partly answered: **OSC 9 is out** — `allow-passthrough` defaults to off, so tmux swallows a
   desktop notification written by a pane. The terminal bell and `display-message` do work, and
   `window_bell_flag` is a signal tmux already computes that the hub could read rather than
   derive.
3. Does the `up-empty` discriminator (dial-accepted-then-EOF, §5) hold across sshd versions, and
   what does it look like when the remote socket exists but its server is wedged?
4. Selection-versus-navigation: is there an interaction where the user broadcasts to something
   they believed deselected?
   *(Answered elsewhere in this round: §14.3's `up-empty` discriminator is measured and
   implemented, and the nested-prefix question is measured — the outer tmux wins, so the hub tells
   the user `C-b C-b d`.)*
5. ~~Does `classify()` hold against a real Claude prompt?~~ **Answered.** Captured free from
   Claude's own trust dialog: it renders `❯ <n>. …` with the cursor on the selected option, so
   `needs` keys on that shape. Verified end to end — the hub reports `needs` for a live Claude
   waiting on that dialog, with an idle pane beside it. Non-Claude panes measured too: `[y/N]` and
   `(y/n)` read `needs`, a REPL at `>>>` reads `idle` (correctly — "ready for the next thing"), and
   a free-form `Rebase onto main? Proceed?` needed the positional rule in §6. A credential prompt is detected too, by a
   closed set of words (`password`, `passphrase`, `pin`, `one-time code`, `verification code`,
   `2fa`, `otp`, `token`) appearing in a line that **ends with a colon** — both halves matter, since
   ssh's real prompt is `Enter passphrase for key '…':` so the word is not at the end, while "any
   line ending in a colon" is most output.
6. ~~Is the inbox the right primary screen, or is a "next" view better?~~ **Answered: keep the
   inbox.** Rendering both from the same live data (`prototypes/views.py`) showed that the inbox
   plus the focused pane's tile already *is* the next view with context — the alternative adds a
   waiting count and loses the overview.
7. The remaining threshold — the 1.2 s poll interval — is still armchair
   numbers. `--log-states` writes every transition with how long the previous state was held, which
   is the data that settles them, and it is a property of the user's work rather than of tmux.
8. ~~Should the hub refuse to send to a pane it cannot identify as an agent?~~ **Decided: no.**
   Restricting sends to identified agent panes would shrink the worst outcome, but it would also
   remove a capability the tool obviously should have — typing into a plain shell is a normal thing
   to want from a terminal control panel, and §1 says the sessions are *mixed*. An unidentified
   pane is sent to like any other; what identification buys is the confirmation trigger (§7) and
   the tile label, not permission.

**Already answered by measurement**, recorded so they are not re-litigated: the server-identity
tuple (`#{host}` + `#{socket_path}`, epoch from `#{pid}` + `#{start_time}`); a batched
`load-buffer -b … -` *does* consume stdin; pane content *can* forge a text delimiter, so framing
is by declared length; `capture-pane -p` with no `-S` emits exactly `#{pane_height}` lines;
`capture-pane -S <h-6> -E <h-1>` is exact and costs 18 % of a full capture, which is what makes
gate-free polling affordable (§6); the tile must be derived rather than truncated, because only
6 of 25 lines of a real Claude pane are content; and the host probe must key on stdout rather
than rc (§9).


### Open: `quiet` and `idle` are the same observation for an agent pane

Measured through the real classifier on a live capture. A Claude Code pane that has
finished and is waiting reads **`quiet`**, not `idle`, because `Classify` reaches the
`ActivityAge > QuietAfter` branch and nothing above it fires. That is not a bug in
the patterns — it is that the two states are indistinguishable from pixels:

| §6's intent | what the screen shows |
|---|---|
| `idle` — prompt present, ready for the next thing | an input box and no output for 180 s (`QuietAfter`) |
| `quiet` — silent too long: hung, or finished quietly | an input box and no output for 180 s (`QuietAfter`) |

Claude renders its input box **at all times**, including while working, so "a prompt
is present" cannot separate them either. The consequence is a UX one and it is the
reason this is written down: after `QuietAfter` **every** long-lived pane becomes
`✱ quiet`, which sorts *above* `idle` in the inbox — so the inbox fills with panes
that are merely old, and the state that means "look at me" loses its meaning.

Three candidate answers, and the choice needs a day of real use behind
`--log-states` rather than a guess:

1. **Let §17 decide it.** `claude agents --json` reports `done` against `working` as
   a FACT, so an agent pane joined to a session should take its state from there and
   the pixel `quiet` should never apply to it. This is the design's own layering rule
   and it is the favourite — and the join it needs is now WIRED (`joinAdoptedSessions`
   on the agents poll, so a pane the hub created carries its session id and
   `UpdateAgents` folds the listing's fact into that row). It was built, tested and
   uncalled for the whole time this option read "unbuilt", which is why the option
   looked more expensive than it was. What remains is scope, not machinery: the wire
   covers panes the hub CREATED, and a pane the operator started by hand still needs
   the process walk to report its session id.
2. **Raise `QuietAfter` a long way** (say 30 min) so `quiet` really means abnormal.
   Cheap, and it only moves the problem.
3. **Drop `quiet` for panes with a prompt** and keep it for panes without one. Clean
   for shells, wrong for agents, since an agent's box is always there.

### `QuietAfter` is grounded; the quiet-vs-idle *semantics* still are not

The threshold no longer needs a day of watching — the distribution was already on
disk. Measured over **59 450 transcripts / 210 491 gaps** in
`~/.claude/projects/*/*.jsonl`, splitting on the assistant's own `stop_reason`:

| silence after… | median | p90 | p95 | p99 | p99.9 |
|---|---|---|---|---|---|
| `tool_use` — work in progress | 0.7 s | 9.0 s | 15.8 s | **90.9 s** | 570 s |
| `end_turn` — the turn is finished | 0.1 s | 0.6 s | 2.5 s | 21.9 s | — |

The working tail is what `QuietAfter` must clear, and 90 s sits **exactly at its
p99**: 1.03% of working gaps exceed it, 0.57% exceed 120 s, 0.30% exceed 180 s.
These are an **upper bound** on screen silence rather than an estimate, because
Claude renders a spinner and a token count continuously while a tool is pending, so
`window_activity` advances far more often than a transcript entry appears.

**`QuietAfter` moves to 180 s.** It clears the working p99 by 2x, takes the
false-quiet rate from 1.03% to 0.30%, and — because §14 above shows every
long-lived pane eventually reads `quiet` — it also keeps the inbox from filling
with panes that are merely old. That is a smaller change than it looks: nothing
depends on `QuietAfter` except the `quiet` state itself.

**What the same data cannot settle** is the semantic overlap, and now the reason is
known rather than suspected. The gap after `end_turn` is *shorter* than the gap
during work (median 0.1 s), so no threshold separates finished from working — and
that is not censoring, which was the first guess: 99.1% of `end_turn`s do have a
following entry. It is that the following entry is almost always injected by the
harness (a system reminder, a queued turn), so the gap measures **injection
latency, not human latency**. The observable exists and measures the wrong thing.

That closes the question in one direction: option 1 is not merely the favourite,
it is the only one the data can support. `quiet` versus `idle` for an agent pane
has to come from §17's producer, because the screen and the transcript both lack the
observable that would distinguish them.

## 15. Sequencing

1. `Run()` seam + `tmuxcli` + required-field assertion, local host only. Length-framed captures,
   free-text-free delta format. Dashboard read-only, no broadcast.
2. `classify()` + fixtures + pane-level inbox + agent-pane identification.
3. `hostagent` + reconciliation + identity assertion + host states, on the fake-ssh test table.
4. Attach (both contexts) + resize warning.
5. Broadcast: token stamp and guarded send first, then single target, then multi with
   confirmation and per-target confirmed delivery.
6. Picker, history view, empty states.

Read-only value ships first, and nothing can write into a pane until the token guard, the
`%`-free confirmation and per-target delivery all exist — in that order, because each of the
three was measured to fail silently on its own.

## 16. Experience commitments

Sections 1–15 make the tool correct and §17 widens what it is about. This one makes it worth opening. Each commitment is a
number the implementation must hold, not an aspiration.

### Value before infrastructure

Measured: the local tmux socket answers in **2 ms**; probing ten ssh hosts concurrently took
**7.65 s** wall. Those two facts settle the startup design.

| commitment | number |
|---|---|
| a usable dashboard of local sessions is on screen before any network work starts | first paint < 50 ms, no host probing on the critical path. Measured: `status` end to end against the local server, two poll cycles and process start included, is **5 ms** |
| remote hosts fold in as they answer, each with its own row appearing when ready | no blocking "discovering hosts…" screen, ever |
| a host that takes 7 s to answer delays only its own row | per-host rows, per-host status, no aggregate spinner |
| zero configuration is a working configuration | `tmux-hub` with no `hosts.toml` shows local sessions and offers the picker; it never *requires* it |

The reason this is a commitment and not a preference: needing to complete host discovery before
seeing anything makes the first run cost 8 s to show what `tmux ls` shows in 2 ms. A tool that is
slower than the thing it replaces on the commonest case does not get a second run.

### Every error carries its fix

The probe table in §9 is the pattern for the whole tool: the string the user reads names the
remedy, not the breakage. `no tmux — install it, or leave this host off`, not `probe failed`.
`nuc is 120 wide, your terminal is 200 — attaching permanently discards scrollback`, not
`size mismatch`. `degraded:format — window_activity came back empty on this host`, not
`degraded`. A status with no remedy is a bug report addressed to the wrong person.

### The terminal it actually runs in

`live1` — the session this design was written in — is **80×24**. That is the size to hold, not a
degraded case.

The list **scrolls** to keep the cursor visible. It did not, and with 30 panes on a 24-row terminal
pressing `j` past the bottom moved a cursor nobody could see.

| width | layout |
|---|---|
| any width | the inbox has the WHOLE width; the details band is pinned to the bottom of the body |
| < 100 cols | rows are inline — the host and session share the pane's row instead of taking a header |
| 100+ | rows are grouped under `HOST SESSION` headers, and surplus width becomes tile COLUMNS in the band |

**The band used to sit beside the inbox at 100 columns and above, and that cost the operator the one
field that identifies a session to a person.** The inbox was pinned to `InboxWidth` (28), which leaves
about fifteen columns for a name after the point, the mark, the glyph and a six-column state word.
Measured on one fleet at two widths:

```
 80 cols  > ⚑ needs  20260809--рендеринг-карты
100 cols  > ⚑ needs  20260809--рендери ┌─ local sess1 %1 ──────────────…
```

So a WIDER terminal showed LESS of the name while the tile beside it held an almost empty box across
seventy columns — the same non-monotonicity §16 already forbids in the footer, in the body.
`TestWiderNeverShowsLessOfAName` walks 80 → 200 and refuses any step that shows less than a narrower
one; it needs no knowledge of the layout to make that assertion.

**The band's height is a pure function of the terminal's**, never of the focused row's content: a band
that sized itself to what it holds would move its own top edge on every `j`, so the list above would
shift between one keystroke and the next. It takes a third of the body, at least 5 rows and at most
12, and it gives way entirely when the list would be left fewer than 3 — which is what makes a 10-row
terminal show three sessions rather than two. `inboxHeight` is the one place that arithmetic lives,
for the reason `bodyHeight` is: the renderer and `InboxViewport` both read it, and when the first
version of this change taught only the renderer, `A` selected a pane seven rows below the fold.

A layout prototype was rendered at both sizes against real pane content, and it corrected two
things this section had asserted:

- **At 80×24, per-session header rows cost more than they give.** Six panes across five sessions
  spent **5 of 11** body rows on headers, with 12 rows left empty. So at narrow widths the
  host/session belongs *inline* on the pane row, truncated from the left (the tail of a path is
  more identifying than its head); grouped headers appear only when there is vertical room.
- **Tile width must be bounded.** At 160 cols the naive "inbox + tiles" split produced a 130-col
  tile holding 30-col content — 100 wasted columns per row. Extra width becomes additional tile
  **columns**, not wider tiles.

The bound is a setting rather than a constant, and deliberately so: content-line lengths on the
one pane measured were median 19 / max 30 columns, which is far too small a sample to fix a
number, and Claude Code wraps at its *source* pane width — so a pane running at 200 columns
produces content lines up to 200. The default ships from a wider sample; what the design fixes is
that the number exists and that surplus width turns into columns.

Tiles truncate **ANSI-aware**: measured overhead is 1.59×, and a byte-wise cut lands inside an
escape sequence and bleeds colour into the rest of the screen. Rendering re-emits `SGR 0` at every
tile boundary. Truncation is also **display-width** aware, not byte- or rune-count aware, since
tile content routinely contains double-width glyphs.

### The operator's loop, in keystrokes

The workflow this tool exists for is *one agent finished, give it the next thing, check the
others*. That must cost single keys:

```
j/k     move in the inbox            space   mark
Enter   send to marked               i       open input
!       interrupt marked             q       quit
a       go to it — a jump on this server, a window for another (§20)
h       history / re-send            n       launch new agent
R       restart (resume session)     K       kill (confirms)
x       hide pane                    X       toggle show hidden
```

`Enter` on exactly one marked pane sends immediately; more than one requires the confirmation of
§7. Re-send from history to the *current* selection is the highest-value action in a broadcast
tool and is free once the log exists — which is why §7 ships the reader with the writer.

### What a sysadmin needs from it

- **Answerability**: every host's state is a sentence with a cause, and `p` re-probes on demand.
  Nothing is ever merely absent from the list.
- **Config in git**: `hosts.toml` is generated, diffable, and carries no secrets — ssh already
  owns authentication.
- **An audit trail**: `history.jsonl` records who was sent what, to which `%NN` on which host,
  and which targets *confirmed*. It is the only record that a prompt reached a machine.
- **A read-only mode** (`--watch`, and `readonly = true` per host) that is read-only in the
  **capability**, not in the UI: such a host gets no forwarded socket and is polled through the
  ssh master. Measured cost 506 ms against 485 ms. A UI lock over a socket that still grants
  arbitrary remote execution would be a lie told to the person who most wanted the guarantee
  (§12).
- **A transition log** (`--log-states <file>`): one JSONL line per state change, carrying how long
  the previous state was held. Transitions rather than samples, because a sample per tick is ~72 000
  mostly-unchanged rows a day while the durations are what the thresholds need. It is how §14.7
  gets settled.
- **Scriptable without opening the write path**: `tmux-hub status --json` runs one poll cycle and
  prints host states, panes, and attention states. It is the read path with a different renderer,
  so it inherits purity for free and can feed a monitor or a shell prompt. Broadcast stays
  interactive-only (§2) — the asymmetry is deliberate: reading is safe to automate, writing into
  a live agent is exactly what must keep a human and an on-screen target in the loop.

### Two levels, not one

Free-form exploring and declared guarantees both have to be pleasant, or the safe path loses:

- **exploring** — mark a tile, type, Enter. No config, no names, no ceremony.
- **declared** — a tagged host set in `hosts.toml` plus re-send from history, so a repeated
  operation is a recalled record rather than retyped text.

Requiring the declared form for a one-line "run the tests again" would lose the commonest case,
and a tool that loses the commonest case gets routed around.

## 17. Claude Code's own surface — the unit is a session, not a pane

Checked against the installed CLI (2.1.227) after the rest of this design was written, and it
changes §1 rather than refining it.

Re-measured in §22 on **2.1.233 (local) and 2.1.224 (`nuc`, the oldest in this fleet); every door and flag was taken on BOTH, and the protocol observations in §22.2 and §22.4 on the local machine only, not re-measured on 2.1.224** — §22 adds three shipped-but-undocumented verbs and a daemon to this surface; the corrections that version forced are marked inline below.

**`claude agents --json --all` lists background agents, and `blocked`/`done` arrive as a FACT.**
Measured on this machine: 23 sessions, all `kind: background`, states drawn from
`blocked | working | done | failed`, at **~200 ms** per call. Each entry carries `sessionId`, `cwd`,
`name`, `startedAt`. Seven were live at the time of measuring and **three were `blocked`** — that
is, waiting for the user.

**`--all` is not optional, and the listing is AUTHORITATIVE for state.** The rule stands on the
population and not on a disagreement: bare `--json` hid **17 of 31** local rows (13 `done`, 4 `failed`),
where an earlier draft justified it by the listing's `state` disagreeing with the file's. Measured, they
agree on 24 of 25 rows, and the one divergence is semantic rather than stale — a file 0.3 minutes old
saying `blocked` about work blocked on something external while the listing said `working` about a
daemon that was computing (§22.5, §22.6). `working` is the weak half of the vocabulary — see the
blind-spot table below.

**The short `id` may be manufactured, so nothing gates on it.** `internal/agents/agents.go:111-112`
back-fills `ID` from `SessionID[:8]` when the listing omits it, so an interactive row carries a
plausible short id with no daemon door behind it. Every action gates on `Kind == background`
(§22.3).

**And the backing file carries more than a state.** `~/.claude/jobs/<short-id>/state.json` holds,
for a blocked session:

`~/.claude` is a default, not the address: the daemon behind these files is keyed on the **config dir** — its socket is `/tmp/cc-daemon-$uid/sha256(realpath($CLAUDE_CONFIG_DIR))[:8]/control.sock` and its key is `$CLAUDE_CONFIG_DIR/daemon/control.key` (§22.2) — so one host can carry several disjoint populations and rosters at once, which is exactly how §22's probes ran beside the operator's own sessions. A hub that hard-codes `~/.claude` on a host whose operator sets `CLAUDE_CONFIG_DIR` reports the wrong set confidently. Where the jobs directory itself lands under a non-default config dir is **not measured**.

```
state:  "blocked"
needs:  "sync 13 appsets in ArgoCD; confirm first real merge on 14 restricted repos"
detail: "13 appsets need manual ArgoCD sync; 14 repos unreachable by CI token"
intent: "давай раскатаем Dockerfile goldens - по всему флоту"
tokens: 7388   updatedAt: …   resumeSessionId: …
```

So Claude Code already produces the semantics this design derives from pixels. But the sentence that
says *what the user must do* is the rarest field in the file: over 25 real `state.json` files `needs`
is present in **6 of 32** files, while `detail` is present in **31 of 32** (25 local + 7 `nuc`,
2026-08-15; at most one file lacks `detail`, and whether any lacks both is unmeasured) — and `detail` is the line the agent
wrote about itself, a status, not an ask. The sample above is the minority case. `detail` also
**survives a reap and goes stale**: one row still read `Done. Goal met` after its worker was reaped,
with no timeline entry (§22.5). So a tile shows `detail`, prefers `needs` where it exists, falls back to
the listing's own word and then to `unknown` — four rungs, because `detail` is 31 of 32 and 2 of the 6
local interactive rows carry neither `state` nor `status`, which `Attention()` renders `unknown` — says which
one it is showing, and ages it.

For my own live session the file was 0.2 min old, so it is current rather than a post-mortem — but
freshness is not liveness. A session killed while blocked leaves a **byte-identical** `state.json`;
only the daemon's roster entry disappears, and the file's `pid` is **ABSENT** from all 25 local files —
not null, a different parse — including
two live `working` sessions (§22.5).

### What that means for the design

**The two populations do not overlap — on this CLI version.** All local entries are
`kind: background`; the interactive `claude` in a tmux pane (verified: `live1`, pid 2543332) appears
in **none** of them. But that is version-dependent, and the surface moved twice in three patch
releases — measured across four machines:

| host | CLI | entries | kinds | has `state` | has `status` | has `pid` |
|---|---|---|---|---|---|---|
| local | **2.1.233** (2026-08-15) | bare 13 / `--all` **31** | 25 background + 6 interactive | interactive 0/6; background NOT COUNTED (§22.10) | interactive 4/6 | 3 of 25 background |
| `side-desk` | **2.1.226** (2026-08-16) | bare 4 / `--all` **25** | 25 background, 0 interactive | 25/25 | 0/25 — the key is ABSENT | absent, as is `status`: seven keys, not nine |
| `nuc` | **2.1.224** (2026-08-15) | bare 11 / `--all` **14** | 7 background + 7 interactive | background 7/7, interactive 0/7 | interactive 7/7 | **9** (2026-08-13) |
| `eu` | no version output | 0 | — | — | — | — |

Two things this table does not say, both measured later on **2.1.233** locally and **2.1.224** on `nuc` (§22). Every `entries` count was taken with bare `--json`, which omits rows the daemon has reaped — so the column is what the default filter returns, not the population, and only `--json --all` names a set. And `has pid` is the LISTING's field: `state.json`'s own `pid` is **ABSENT** from all 25 local files — not null — including two live `working` sessions, so the file supplies neither the pane join nor liveness.

So on 2.1.224 the listing **does** include interactive sessions, each with a `pid` — which would be
a direct join key to a pane, since the delta already carries `#{pane_pid}`. On 2.1.226 and 2.1.227
it does not. A consumer must therefore:

- read `state` **or** `status`, whichever is present, and tolerate neither;

- **pass `--debug-file` where the verb accepts it.** A measured failure wrote nothing to stdout and nothing to stderr; the reason existed only there, so without it a failure is invisible and the hub reports a wrong answer confidently. Measured on both versions: `claude logs` ACCEPTS the flag and writes the file even for a bogus id (620 B locally, 278 B on `nuc`), `claude agents` REJECTS it (rc=1 in ~165 ms, `error: unknown option '--debug-file'`, no file, 6 of 6 runs), and `attach` is UNTESTED — so the listing is judged by exit code alone, `logs` is the reading that can be instrumented, and §22.3's payload rests on the untested cell (§22.6);
- tolerate `kind: interactive` as well as `background`;
- **use `pid` when it is there** — it is the cheapest pane↔session join available — and not depend
  on it;
- never assume the population: a version that omits interactive sessions means the pane path in
  §§6–7 is still required, not a fallback.

- and never read "listed" as "reachable": `attach`, `logs` and `stop` are **background** verbs, and the reason is arithmetic rather than intent: `kind=background` carries an `id` **32 of 32** while interactive rows carry one **0 of 13**, both hosts, both directions (§22.8), so an interactive session appearing in a 2.1.224 listing, pid and all, has no argument to pass and therefore no door the hub can open. The only route into that population is the operator typing `/background` inside the session itself — which the hub cannot do, because every option of `claude agents` either configures newly dispatched sessions or filters the listing (§22.8). The sessions the hub cannot type into are exactly the ones it cannot background, which is why the pane path in §§6–7 is a requirement and not a fallback.

It also means the whole of §17 sits on a surface that is not stable across patch versions, which is
the argument for keeping level 1 self-sufficient (below) rather than an argument against using it.

With that caveat:

| population | how the hub sees it | state quality |
|---|---|---|
| interactive session in a tmux pane | `capture-pane` + `classify()` | heuristic (§12) |
| background agent | `claude agents --json` / `state.json` | **fact**, with a `needs` sentence |

**The inbox is therefore incomplete today, and by more than it first looked.** The producer works
over ssh — measured, `nuc` answered in 2.8 s on a fresh connection and `side-desk` in 0.5 s — and
across three machines **nine** sessions were `blocked`, i.e. waiting for the user: 3 local, 1 on
`nuc`, 5 on `side-desk`. The dashboard could show none of them, because they are not panes. An empty
set is clean (`--cwd /nonexistent` → `rc=0`, `[]`), and a host without `claude` behaves like a host
without tmux: the probe must key on output, not on the exit code (`eu` returned nothing for
`claude --version`). That is a gap in §1's premise,
not a missing feature: the unit of attention is a **Claude session**, which may be *hosted* in a
tmux pane or may be a background agent with no pane at all.

### Implemented

The second producer is in (`internal/agents`), wired as a **separate 20 s timer** rather than into
the tick, and it closed the blind spot: against this machine the dashboard now shows three
previously invisible `blocked` sessions beside its one pane.

One thing it did not get right, and the fix is one argument at TWO sites: the producer CALLED bare `claude agents --json` at `internal/agents/agents.go:138` (the local argv) and `:150` (the ssh shell string), which omits every terminal row (§22.6) — measured 2026-08-15, locally bare returns 13 rows against `--all`'s **31**, with 17 sessionIds present only under `--all` (13 `done`, 4 `failed`). A fix naming one site leaves the ssh fetcher on the narrow call, so the blind spot is narrowed, not closed, until BOTH carry `--all`.

Four things the implementation had to get right, three of which the tests caught:

- **A session leaving the listing is done with, not stale.** The listing is the whole truth about
  its own population, unlike a pane list that a broken tunnel can silently empty.

That holds **only with `--all`**: bare `--json` omits rows the daemon has reaped (§22.6), so under the narrower call a row can leave the listing because its worker was killed mid-turn — and its `state.json` then sits on disk saying `Done. Goal met` about work that was abandoned (§22.5). Reaped is a departure the hub must announce, not a completion it may infer from an absence.
- **`MarkHostStale` must not touch agent rows.** A dropped tmux tunnel says nothing about whether
  Claude's sessions are running; they have their own producer and their own failure to report
  (`Host.AgentsReason`, which never changes `Status` — a host without `claude` is not unhealthy).
- **An unreported state becomes `Unknown`, a state of its own**, ranked between `works` and `gone`.
  Flattening it into `idle` would lie about a session that might be waiting, and a version
  reporting neither `state` nor `status` is measured, not hypothetical.
- **Pane-less rows render differently**, which only became obvious on screen: each session's name is
  unique, so giving them session headers made the list half headers; their name is the row, their
  tile carries `state / kind / since / id` instead of an empty box, and the header counts *sessions*
  because most of them are no longer panes.
- **A session header is drawn only over a group of TWO ROWS OR MORE**, and a row with no header
  above it carries the name the header would have held, then its pane id where that id says
  something (see the pane-id rule below), then its command. A header
  over one row says nothing the row does not — and once §22.3's door existed this became most of the
  fleet, because the door gives every session it opens a tmux session of its own. Reported from real
  use about the pinned band: "они не выглядят переименованными, также они выглядят как отдельные
  проекты" — measured at 150×45, four pinned conversations drew four headers with a nameless
  `%3   sh` under each, so the name the operator had typed was on the screen and not on the row
  their cursor was on. The rule is about the GROUP and not about the row's kind, which is what makes
  it survive the door: a session that gains a pane changes kind, and the size of its group does not.
  Two consequences worth stating, because each is a case that had to be decided: the count is over
  EVERY row rather than the visible window, so scrolling cannot change a row's shape; and the rule
  does NOT apply in the PROJECT view, where the header answers the question the view was opened to
  ask and the row never says it. There a conversation leads with its name anyway, since a project
  header says which project and nothing about which session.
- **A row that no header speaks for states its HOST, in the shape `local/tmp  %20`.** This is the
  clause the rule above was missing, and it cost a report: "I opened the tmux-hub and it feels like
  I see duplicates now". Nothing was duplicated. The list is ordered by ATTENTION and not grouped
  (§21.11), so a group of one lands wherever its state puts it — and two LOCAL singletons landed
  between the two panes of a nuc session, under its header, which then came back as `(cont.)`:

  ```
  NUC TMUX-HUB-DEMO
  >  ✱ quiet  %1   claude
     ✱ quiet  tmp  %20  claude                ← on LOCAL
     ✱ quiet  20260817-cicd-30f3382b  %19     ← on LOCAL
  NUC TMUX-HUB-DEMO  (cont.)
     ✱ quiet  %0   bash
  ```

  Two hosts under one header, and a header the operator read as the same session twice. The
  argument for dropping the header — "it says nothing the row does not" — was FALSE while the row
  omitted the host, so the fix is to make the argument true rather than to restore the header or to
  reorder the list; grouping within an attention band would cost §21.11.1's longest-wait-first rule.
  The shape is the one the narrow band already uses for the same job, so there is one convention for
  "this row carries its own location" and not two. Both headerless shapes go through ONE function
  (`lonelyName`), because the pane-less row had its own copy of the row-building code and was
  therefore the second instance of the same defect, found only by grepping for the shape. The guard
  is an INVARIANT and not a frame: for every row, the nearest header above it names its group or the
  row names its own host.
- **Where a group's run ENDS is marked, with a `┄┄ other sessions ┄┄` row.** The row saying where it
  lives answers "which session is this"; it cannot answer "did the group above finish", because
  indentation under a header reads as membership no matter what the row says. Reported together with
  the above, in the same sentence: the operator read `NUC TMUX-HUB-DEMO` + rows + `NUC TMUX-HUB-DEMO
  (cont.)` as one session listed twice. The break is drawn once per interruption — the returning
  header says `(cont.)` itself — and never at the top of the visible list, where there is no header on
  screen to be separated from. **A row and its prefix fit together or neither is drawn** — the loop
  tests the WHOLE cost (`rowPrefixCost`) before appending anything, because testing it after let the
  prefix take the last row and leave its own row undrawn: measured, three panes at 120 columns, the
  last line at height 6 was `┄┄ other sessions ┄┄` with nothing under it, and with the group second the
  last line at height 4 was `LOCAL PAIR`. The header half predates the break and had been shipping;
  one rule closes both, and it is the rule `extraAbove` already states. It COSTS A SCREEN ROW, so
  `extraAbove` counts it: that function
  replaced a `[]bool` "is there a header" with a count of extra rows precisely because there are now
  two kinds, and `A` selects what the viewport counts while the operator acts on what the renderer
  drew. Dashed and not solid, because `─` belongs to the tile box and the footer rule.
- **An id is on a row only when it tells that row from another, and the KEY is the label the row
  draws.** Measured on the operator's fleet: 60 rows across 54 sessions, so **49 of the 60 rows carried
  a pane id that distinguished nothing** — reported as "я не понимаю %1, %5, %3". Three row shapes print
  it, so it is ONE function (`rowIdentity`); the TILE is not one of them and always shows the id,
  because it names the one pane the operator chose.
  The rule is `collidingLabels`: more than one row drawing one `(host, display name)`. It was first
  written as `(host, session)` multiplicity, which an adversarial review found to be a PROXY that is
  wrong in the direction that hides a collision — and the hiding case was live on the operator's own
  fleet: two Claude sessions on `local` both called `20260818--cicd`, both `⚑ needs`, both `background`,
  different uuids (`1b0cacf2`, `30f3382b`), and neither row carried an id because a pane-less row's id
  was never drawn at all. Two sessions asking for input, indistinguishable, with `a` and `K` acting on
  whichever the cursor happened to be on. WHICH id depends on the kind, and each is the id that keys
  the operator's next command: tmux's `%N` for a pane, the SHORT Claude id for a pane-less row — §22.6
  measured that `claude logs <full uuid>` answers `No job matching` while the short id resolves, and it
  is the id the door appends to the session it creates. A listing record with NO job id still has a
  uuid, and without that fallback the rule is half-blind for exactly the rows that need it — measured
  on the live fleet through the keyword filter after the first version shipped, one of a colliding pair
  carried `bce0212b` and its `interactive` twin carried nothing, because `claude agents` reports no id
  for that kind. That is the same fact §22.11's fold rests on. So the id is `AgentID` when the listing
  gave one and `launch.ShortID(Claude())` otherwise — through `Claude()`, because which field carries
  the uuid differs by kind, and through ONE truncation shared with the door's session namer so a name
  the door builds and an id a row shows cannot disagree about how much of a uuid a person reads. The state word is deliberately NOT part of the
  key, though it would spare an id on a pair that differs by state: a state changes on a tick and a
  row's shape must not flicker with it, and erring toward showing the id is the safe direction.
  Both poles are tested — forcing the id always-on and forcing it never-drawn each kill tests — and a
  third case pins the KEY, with two rows that share a label and differ in session, because without it
  reverting to `(host, session)` passes everything else.
- **The hub's own windows are not rows of the fleet.** Reported as "что за LOCAL 0, LOCAL 15" — two
  sessions the operator did not recognise on their own screen, which measured out as the hub itself:
  session `0` held `tmux-hub` in window 0 and the attach window `nuc/tmux-hub-demo` in window 1;
  session `15` held `tmux-hub` and two more attach windows. Five of sixty rows were the hub watching
  itself and the doors it had opened, under headers named `0` and `15` because a bare `tmux` numbers
  its sessions. The `sh` rows were the worse half: an attach window is a VIEW of a session that is
  already a row of the same list, so those were the only true duplicates on the screen — and the least
  recognisable, since the row shows the `sh` wrapping the ssh rather than what it is looking at.
  Both keys are derived from the PANE, never from the process that made it: the hub is a pane whose
  `#{pane_current_command}` is this program's own name (measured — those panes report an EMPTY
  `#{pane_start_command}`, because the operator typed the name into a shell, so the start command
  cannot be the key), and an attach window is one the hub NAMED, through the same `attachWindowName`
  §22's dedup already calls. The attach rule also requires the window to share a tmux session with a
  hub pane, which is where the hub puts them by construction; without that clause an operator who
  named a window `nuc/api` by hand would lose the row. Filtering happens in `setFleet`, the ONE writer
  of the fleet — there are two producers, so a rule about membership had two places to be forgotten —
  and the HEADER says how many were dropped (`4 windows of its own not listed`), because a session
  count that silently disagrees with `tmux ls` trades one confusion for another.
  A THIRD clause, earned by an adversarial review: the pane must also SAY of itself that it is an
  attach (`looksLikeAnAttach` over `#{pane_start_command}`). Without it this fleet loses a row — `nuc/api`
  is a real pane on nuc, so the hub would name an attach window for it `nuc/api`, and an operator with
  their own window of that name inside the session the hub runs in matched both earlier clauses. The
  reviewer's remedy, a per-instance option stamped on created windows, is the one §22 already measured
  and rejected (a command sent after `new-window` loses a race against its own payload, `false`
  surviving 6 of 12 trials), which is why the NAME is the mark at all. `pathWindow` is the only
  possession path that creates a window and it is defined as an ssh attach, so the start command is
  decisive; if that command's shape changes the clause stops matching and the windows come back as
  rows, which is the safe direction. The words are read as FIELDS after tmux's quoting is stripped,
  because tmux word-quotes the value and `tmux` and `attach` are never adjacent in it.
  What stays open, deliberately: an operator running a DIFFERENT program called `tmux-hub` loses that
  pane's row — no signal separates two programs of one name, and the cost is one curious row against
  `tmux ls`. The false negative paired with it does NOT exist: measured on tmux **3.7b and 3.2a**,
  `#{pane_current_command}` for a binary named `tmux-hub-enterprise-edition` reports all 27 characters,
  and the 15-character truncation is the KERNEL's `comm` (`tmux-hub-enterp`), which tmux does not read.
  `--status` is deliberately NOT filtered: it builds its own registry and reports the raw poll, which
  is the surface you would use to diagnose the filter itself, so hiding rows there would hide the
  evidence. The header's sentence is what reconciles the two numbers.

### What to change

1. **§1's unit widens** from "every tmux session" to "every Claude session you can reach, plus
   every other pane". A pane that is not an agent is still shown — §14.8 settled that — but the
   inbox's spine is sessions.

**"You can reach" is now measured, and the edge is not where it looks.** A background session is reachable whatever its state — one `claude attach <id>` served four row shapes and wakes a session whose worker is dead (§22.3) — while an interactive session in a terminal the hub cannot reach has no door at all (§22.8). So the reachable set is neither "the sessions with panes" nor "the sessions in the listing": it is every background session, plus every pane the hub can see.
2. **A second source, not a second dashboard.** `claude agents --json` becomes another host-like
   producer feeding the same registry: pane-hosted panes keep their screen-derived state, background
   agents arrive with `state` and `needs` already known. One inbox, two provenances, and the row
   says which.
3. **Where a fact exists, the heuristic must not override it — and one state has no fact.** §12
   lists `classify()` as the only heuristic in the design; where the daemon has concluded
   (`blocked`, `done`) it is not needed, and that is the largest single improvement available here,
   at a 200 ms subprocess. It is **not** dispensable for background rows. A session pending on a
   Write permission prompt reported `working`, an empty `needs` and no `block` across 136 s of
   polling (§22.4) — through the daemon's own `list`; whether `claude agents --json --all` reports it differently is unmeasured (§22.10) — so the fact source is silent about a row that is waiting for the operator. The
   only account of that state is the target's own screen — the daemon's `subscribe` returns raw pty
   bytes, and `claude logs <id>` prints recent terminal output for a live row (§22.4, §22.6). A
   pane-less row therefore keeps a screen-derived path for exactly the case the fact source cannot
   see, and the fact wins everywhere else.
4. **The tile for those rows is composed by the hub, and it is not one sentence.** `detail` (25 of
   31 of 32 files) is the line the agent wrote about itself; `needs` (6 of 32) is the ask where it exists.
   Neither says what the operator most needs to know, so the hub says it: `reapedMidWorkAt` is the
   **last-alive** time, not the reap time — measured 06-21 against a 07-14 reap — so the tile reads
   "last alive <that>, reaped <firstTerminalAt>"; a stale `detail` on a reaped row read `Done. Goal
   met`, and the row never says it was reaped; and reaped and blocked are **disjoint** sets, so
   neither is evidence of the other (§22.5). §6's *pixel* tile work does not extend to them because
   the material is different, not because there is less of it.


### Interactive panes have a fact source too — and it needs no configuration

Three things were verified read-only against the live `claude` in pane `%2`:

**1. The process carries its own identity.** `/proc/<pid>/environ` holds
`CLAUDE_CODE_SESSION_ID`, `CLAUDE_PID`, `CLAUDECODE=1` and `CLAUDE_CODE_ENTRYPOINT=cli`, alongside
`TMUX=/tmp/tmux-1000/default,2204419,2` and `TMUX_PANE=%2`. So §7's "walk the process tree looking
for a `claude`" gets a much better answer: the environment says *which session* this is, and
`CLAUDECODE=1` identifies an agent process positively rather than by executable name.

**2. That id joins the pane to a live transcript.** `CLAUDE_CODE_SESSION_ID` resolved to
`~/.claude/projects/<slug>/<session-id>.jsonl` — **44 MB, 24 413 lines, last written 10.3 minutes
ago**. Append-only and current, so a tail is cheap even though the file is not.

**3. Hooks can announce a transition, and the join key already exists.** The bundle documents ten
events — `PermissionRequest` (*before* the permission prompt), `PreToolUse`, `PostToolUse`,
`PostToolUseFailure`, `Notification`, `Stop` ("including clear, resume, compact"), `PreCompact`,
`PostCompact`, `UserPromptSubmit`, `SessionStart` — with stdin JSON carrying `session_id`.
(`SubagentStop` and `SessionEnd` occur as strings in the bundle but are absent from that table;
treat them as unverified.) A command hook is a child of the agent process, so it inherits
`TMUX_PANE` and `TMUX` — the hub needs no correlation scheme it has to invent.

`PermissionRequest` is the interesting one: it fires **before** the prompt renders, so a hook makes
"this session is about to want you" a fact that arrives *ahead of* the screen the classifier reads.

### Two levels, and the first one must not require the second

This is where the design has to be careful, because the hook path is an installation step and the
tool has to be worth running before anyone takes it:

- **Level 1 — works on a bare machine.** Screen scraping plus `claude agents --json`. No config, no
  settings edit, no restart of anything. Everything in §§5–10 stays exactly as it is.
- **Level 2 — opt in, strictly better.** A hook set appending one JSONL line per transition, which
  the hub tails and prefers wherever it has a line. `classify()` becomes the fallback rather than
  the source, which is what §12 wants.

And the hook config is **generated, not documented**: a `tmux-hub hooks` subcommand prints the
settings.json fragment to paste. A printed fragment cannot drift from what the tailer parses; a
paragraph of prose describing it can.


### Every source has a blind spot, so freshness is never inherited

The unexpected result of measuring the transcript was about the *other* sources. Two sessions whose
`state.json` said `working` had a last `stop_reason` of `end_turn` with the transcript untouched for
**29 and 31 minutes**. So `state: working` means "the daemon has not concluded otherwise", not "this
is producing output now" — the fact source goes stale exactly like the heuristic one.

Worse than stale, and measured after this paragraph was written: `working` is also what a session pending a **Write permission prompt** reports — across 136 s of polling, with an empty `needs` and no `block` (§22.4), through the daemon's own `list`; whether `claude agents --json --all` reports it differently is unmeasured (§22.10). `working` conflates computing with waiting on a permission dialog, so a row that wants the operator can read as busy indefinitely, and a pane-less row has no screen behind it to correct the reading unless the hub asks for one.

| source | gives | blind to | freshness |
|---|---|---|---|
| pane screen (§6) | needs / error / idle / works | nothing, but by heuristic | `window_activity`, live |
| transcript `stop_reason` | **works, as a fact** | `needs` vs `idle` (both `end_turn`) | file mtime |
| `state.json` / `claude agents --json --all` | **needs vs done, as a fact** (`blocked`/`done`), plus `detail` on 31 of 32 rows and a `needs` sentence on 6 of 32 (2026-08-15) | current activity — measured stale by ~30 min; and a **pending permission prompt**, reported as `working` with no `block` across 136 s (§22.4, via the daemon's own `list`; unmeasured for the call the hub polls, §22.10) | `updatedAt`, which is last-change |
| the daemon's roster | **liveness, as a fact** — and the only source of it: the file's `pid` is **absent** from all 25 local files, including two live `working` sessions, and the LISTING's own `pid` is non-null on only 3 of 25 background rows and on none of the 7 it calls `working` or `blocked` | anything semantic — it says a worker exists, not what it wants | the roster read itself |
| `subscribe` / `claude logs <id>` | the target's **own screen**, raw pty bytes — the only witness of a delivered reply (§22.4) and the only account of a permission prompt | a row the daemon no longer holds: `logs` answers rc=1 in <0.2 s with `Couldn't read logs for <id> — job not found — it may have already exited`, while an id the daemon never had answers `No job matching '<id>'. Run 'claude agents' to list running sessions.` — TWO shapes, measured 2026-08-16, and nothing anywhere may MATCH on either text | the bytes' own read time |
| hooks | the **transition**, ahead of the screen | anything between transitions | the line's own timestamp |

Two rules fall out, and they are the same rule twice:

- **Layer the sources so each one's blind spot is another's strength**, rather than picking a
  winner. `works` from the transcript, `needs`/`done` from the daemon, the transition from a hook if
  one is installed, and the screen wherever nothing better exists.

Two additions §22 forces on that assignment: **liveness from the daemon's roster** and from nothing else, and — for a pane-less row — "the screen" means `subscribe`'s pty bytes or `claude logs <id>`, not `capture-pane`. It is the source of last resort for the permission-prompt state the daemon reports as `active`.
- **Never inherit freshness from a source.** Every value carries its own age — file mtime,
  `updatedAt`, `window_activity`, the hook line's timestamp — and a stale fact loses to a fresh
  heuristic. This is the same discipline §5 applies to hosts: a positive assertion with its own
  evidence, never the absence of a contradiction.

### Still unmeasured, with the experiment named

1. ~~**Can a transcript tail distinguish waiting from working?**~~ **Measured — half of it can, and
   the negative half is the useful one.** The discriminator is the last `assistant` record's
   `message.stop_reason`, which is the API's own account of why the turn ended. Run against a
   labelled set of 18 sessions (labels taken from each `state.json`):

   | `stop_reason` | meaning | observed on |
   |---|---|---|
   | `tool_use` | mid-turn, a tool was called → **working** | only the session that was demonstrably working, file mtime 0.0 min |
   | `end_turn` | the turn completed → **not working** | 2 `blocked` + 12 `done` + 2 nominally `working` |
   | `stop_sequence` | — | 1 `blocked` |

   So it gives `works` as a **fact**, and it is **blind to `needs` versus `idle`**: both are
   `end_turn`, because "the turn ended" is the same event whether the model asked a question or
   finished the job. That distinction is semantic and stays with the screen, a hook
   (`PermissionRequest` / `Notification`), or the daemon's own `state`.
2. **`/proc` is local-only.** Reading a remote pane's environment needs
   `ssh -S ctl host cat /proc/<pane_pid>/environ`, which the master can do and `#{pane_pid}` already
   supplies — but it is another round trip per pane, so it belongs in the slow sweep, not the tick.
3. ~~**Whether any mode breaks §6's classifier**~~ **Answered for the modes.** The six indicators
   are a closed set of literals (`manual mode`, `plan mode`, `accept edits`, `bypass permissions`,
   `don't ask`, `auto mode`), and the classifier knew two — so in the other four the footer read as
   content and leaked into the tile. Fixed. `--print` and the streaming flags render no TUI at all,
   so there is no pane to classify: unproblematic rather than measured.
4. ~~**One pane running a Task fan-out**~~ **Answered: one pane, one state holds.** `isSidechain`
   is 0 of 1890 records in a session that ran many subagents — their work lives in its own
   `subagents/agent-*.jsonl`. While a subagent runs the parent sits on `stop_reason: tool_use`, so
   the pane reads `works`, which is exactly what it is. The pane cannot say *which* subagent, and
   the operator does not act on subagents.

### Open, and they are the next things to measure

1. ~~**How does the operator ACT on a blocked background agent from the hub?**~~ **Answered — §22.**
   Two doors exist and both are hidden from every `--help` listing. `claude attach <id>` gives the
   session a terminal: four row shapes (reaped mid-work, silently reaped while blocked, live,
   settled) served by the SAME single call in **0.33–0.95 s** including a cold daemon start, and it
   *wakes* a session whose worker is dead. The daemon's `reply` hands a prompt to a running
   background agent with no tty, pane or viewer — `ok` in 13 ms, the question answered, the agent
   continued. So `a` on a background agent row becomes a **fifth** §20 dispatch path (§22.3) — the
   ordinal §20's own enumeration uses — while `i` on an agent row is DEFERRED (§22.4): `reply` and
   `subscribe` are protocol operations with no CLI verb behind them, and §22.2 rules that the hub
   execs verbs.
   **`--resume` is not the path this question assumed.** A plain `--resume` of a session whose pid is
   alive returned `is_error=false`, kept the same id, and wrote into the transcript that process held
   open on 5 descriptors — there is no engine-side refusal (§22.1). Resuming in place is a fork
   hazard, not a way in, and the id to resume with is `resumeSessionId`, which is a separate field
   from `sessionId`.
2. **Interface stability.** `--json` is a documented CLI flag; `state.json` is internal and its
   shape can change without notice.

§22 adds a rung between those two. `claude attach|logs|stop` are shipped verbs absent from every listing of **commands**, though each prints its own usage when named — the same text, byte for byte, on 2.1.233 and 2.1.224 — and the `control.sock` protocol beneath them is undocumented entirely — the hub depends on the verbs and deliberately not on the protocol (§22.2). Grepping the help for "attach" finds an unrelated `--cloud` flag, which is a wrong answer rather than no answer. §22's own span is **2.1.233 (local) and 2.1.224 (`nuc`, the oldest in this fleet); every door and flag was taken on BOTH, and the protocol observations in §22.2 and §22.4 on the local machine only, not re-measured on 2.1.224**, and `cliVersion` is present on only 28 of 32 files, so a pin against the file is undefined for four of them. Prefer the flag, treat the file as an optimisation, and pin the
   `cliVersion` field the file itself records (`2.1.226` in the sample, against a 2.1.227 CLI —
   already a version skew inside one machine).
3. **Freshness semantics — and the elsewhere is named.** A blocked session's `state.json` was 39.9 h
   old, because nothing has happened since it blocked. `updatedAt` is "last change", not a
   heartbeat — which is exactly the blocked-since duration the inbox wants. **Liveness comes from
   the daemon's roster, and from nothing else** (§22.5): the file's `pid` is **absent** from all 25 local
   files including two live `working` sessions, and the listing's own `pid` is non-null on only 3 of 25
   background rows and on none of the 7 it calls `working` or `blocked`, and a session killed while blocked leaves a byte-identical
   file, so neither the pid nor the mtime can be asked.
4. **Modes.** `--bg/--background`, `--agent/--agents`, plan mode, `--print` with
   `--output-format stream-json`, `--include-partial-messages`, `--forward-subagent-text`: whether
   any of them changes the bottom of an interactive pane enough to break §6's classifier is not yet
   measured.
5. **Subagents inside one pane.** A pane running a Task-tool fan-out hosts several concurrent
   activities under one screen. Whether "one pane, one state" is adequate there, and whether the
   screen distinguishes "the main loop wants me" from "a subagent is still running", is unmeasured.

## 18. Hiding — permanent noise removal

A host accumulates panes that are not agents and never will be: a log tail, `htop`, a build watcher. They are permanent noise. Hiding is the user saying "never show me this again", not "not right now".

### Why the persisted key is not a pane id

`%12` is monotonic and never reused **within one server's life** (measured: killed `%3`, next pane was `%4`), which makes it an exact key while the hub runs and the wrong key on disk. A restarted tmux server numbers from `%0` again, so a persisted `%3` names a different pane in the next generation.

### The persisted key

```
{host, session_name, window_index, pane_index, pane_start_command}
```

**The window is its INDEX, and this section used to say `window_name`.** That was the defect, not a
mis-transcription of it: tmux ships `automatic-rename on`, so a window's name follows the command
running in it — measured on a private socket, one window went `zsh` → `sleep` → `tail` across three
commands while `window_index` stayed `0` and `window_id` stayed `@0`. A name-keyed mark therefore
un-hid itself the moment the operator ran anything in the pane, which is the opposite of this
section's own promise. `window_id` is not the alternative: `@N` does not survive a server restart, and
surviving one is the entire reason this key is not a pane id.

**The file carries a version, and refusing an old one is the safe direction.** `hidden.json` is
`{"v":2,"keys":[…]}`. A record written under the previous shape has no window index at all, and an
absent JSON field arrives as the zero value rather than as "absent" — so a silent upgrade would read
every old record as **window 0**, a real window, and could hide a pane the operator never hid. Losing
marks annoys; gaining one loses work. So a file that is not the current version is refused entirely,
which shows everything once and says why, through the same path a malformed file already takes.

**This key cannot name a row with no pane, and §22 makes those rows the majority** — 57 of 65 under
`--all`, 2026-08-16 (§22.1). `hide.KeyOf` copies the row's fields with no `Kind` guard, and an agent row carries
`PaneID: "agent:<shortid>@<cwd digest>"`, an empty `Window`, `WindowIndex` 0, `Index` 0 and an empty
`StartCommand` — so `x` on a background agent today persists `{host, <agent name>, 0, 0, ""}`: no
window, no index, no corroborator, and a name the producer chose. Hiding a paneless row therefore
either refuses, naming the reason, or keys on the `SessionID` the row already carries, which is the
only field that identifies it. That is a decision this section still has to make: the fallback is a
key whose sole distinguishing field is a name a future session can share.

The start command is the corroborator because it is immutable for the pane's life (measured: `#{pane_start_command}` = `"sleep 301"`, changes only on `respawn-pane`). `pane_current_command` is not: it walks `zsh` → `claude` → `zsh`.

### The one rule that makes a wrong match safe

A marked pane whose state is `Needs` is shown anyway. This is **load-bearing**, not a courtesy.

**The net is only as good as the state, and §22 measures a hole in it.** For a pane the `Needs` comes
from pixels, which is what makes this rule dependable. For an agent row it can only come from the
producer, and a session sitting on a Write permission prompt polled `working` with an empty
`needs` and no `block` for **136 s** — through the daemon's own `list`; whether `claude agents --json --all` reports it differently is unmeasured (§22.10) — with no screen to fall back on. The same hole reaches PANE
rows the moment §14's join is wired, because `Pane.State()`
(`registry.Pane.State`) prefers `AgentState` for ten minutes. So the rule holds
where the state is the screen's, and hiding must not be extended to a row whose state is the
producer's until the producer's silence stops counting as `works`. A key can only mis-match a pane sharing a window, an index and a start command with the pane the user hid — and even then the mis-matched pane cannot be hidden while it is *blocked*, which is the only state where hiding loses work.

This resurface rule, not the corroborator, is the actual safety net. The corroborator carries no information for the commonest case: measured, `#{pane_start_command}` is the **empty string** for a pane created with no command — the plain shell. For those keys the match rests on position alone, so a `pane_index` shift can move a mark to a sibling shell. That is acceptable because the resurface rule prevents the one dangerous consequence.

### Hidden panes are still polled

The cost is accepted deliberately: a pane that is not polled cannot resurface, and a blocked agent that cannot resurface is exactly the loss the rule above prevents.

### Fail toward visible

A malformed or unreadable hidden-set file is a warning and an empty set, never a fatal error and never a guess.

### A hub-created agent's mark cannot persist

Measured: an interactive agent's start command is `"claude --resume <uuid>"` — it contains the session's uuid,

**Inverted for a row with no pane.** The protection here is the uuid *inside* the start command; a
background agent row has no start command at all — empty, per the key above — so its mark carries
nothing that expires and outlives the session it was taken against. §22's own attach pane keeps the
protection, since its start command is `claude attach <id>` and therefore id-bearing, but the ROW the
operator sees before pressing `a` does not. so its key is unique to that launch and can never match a future one. Hiding an agent is therefore effective for the life of that pane and no longer. This is the right behaviour: a stale mark on an agent is the dangerous kind.

## 19. Lifecycle — creating, restarting, resuming and killing

### The verbs

- **Create a window** in an existing session
- **Create a session**
- **Restart a pane in place** (`respawn-pane`)
- **Resume a dead session** (`claude --resume <uuid>`)
- **Kill a pane**

Each on any host the hub watches, local or over a forwarded socket.

**The unit is a pane, and after §22 not every row has one.** The `K` key kills a pane
(`internal/ui/lifecycle.go:232` calls `tmux.KillPane`). A window is destroyed when its last pane
dies, and a **tmux** session when its last window dies — so killing panes transitively kills windows
and tmux sessions. It does **not** reach a Claude session: §22 measured that the session behind
`claude attach` keeps running whether that pane lives or dies, so killing the pane the operator was
just working in ends the terminal and nothing else. A background agent row has no pane to kill at
all, and nothing stopped the attempt when this was written — `killSelected`
(`internal/ui/lifecycle.go:202-238`) had no
`Kind` guard, matches the row by `{Host, PaneID}` and issues `kill-pane -t agent:<shortid>`, whose
failure is counted with no reason shown. `K` on such a row must refuse and name why; the only thing
that would stop that session is `claude stop <id>`, which §22.9 decision 3 rules the hub does NOT offer — `K` on a pane-less row refuses and names the command instead. `tmux.KillWindow` and `tmux.KillSession` exist in `internal/tmux/lifecycle.go`, are tested, and have no production callers. Scoped as future work or not needed — see `docs/known-issues.md`.

### Identity at birth, and what it kills

The hub generates the uuid and passes `claude --session-id <uuid>`, and `new-window -P -F '#{pane_id}'` hands back the pane id in the same call. So for a hub-created agent the pane↔session binding is **known**, and the process-tree walk in `internal/proc` — the code where this project's one Critical defect lived (a forwarded socket walked against the local process table; 97 of 3117 local pids answer "agent here") — is never consulted.

**This kills a problem class for hub-created panes only.** Foreign panes still need the walk. The kill is recorded in §11.

### Why `--session-id` is never reused

Measured: `claude --session-id <existing-uuid>` → **rc=1**, `Error: Session ID <uuid> is already in use.` A hub that reuses an id fails loudly instead of silently forking a conversation. Restart-with-continuity is `--resume <uuid>`.

**But `--resume` has no such refusal, and that is the sharper hazard.** Measured in §22: a plain
`--resume` of a session whose pid was ALIVE returned `is_error=false`, kept the same id, and wrote
into the transcript file that process held open on **5 descriptors**. So the engine is loud about a
reused id and silent about a forked conversation — the guard has to be the hub's. It cannot be
`state.json`'s `pid`, which is **absent** from all 25 local files including two live `working` sessions;
liveness is the daemon's roster (§22.6). And for a session that may still be alive the right verb is
not `--resume` at all but `claude attach <id>`, which §22 measured *reviving* a session whose worker
had been SIGKILLed — `Waking session <id>…`, a pre-warmed spare claimed, the transcript replayed —
rather than forking its transcript.

### Restart in place keeps more than it looks

`respawn-pane -k` keeps `pane_id`, `@hub_*` options and cwd, and changes `pane_pid` (measured: 222976 → 222983). The surviving stamp is a hazard as well as a convenience: the token still matches, so the guarded write path still trusts the pane, while the *agent* behind it is a different process. Identity must therefore be explicitly invalidated on respawn, and it is: the restart path calls the keeper's per-pane forget before re-stamping, with a test that goes red without it.

### Dead panes need `remain-on-exit`

Without it a pane whose command exits is destroyed, so "claude exited with code 7" is not a state the hub can ever observe — it is a row that vanishes. The hub sets `remain-on-exit on` **per window, on windows it creates**, never globally: a global set would change the user's own windows' behaviour. **One** window qualifies: the launch pane here. §20's possession window used to be the second and is not any more — the option was set after `new-window`, which is what starts the payload, so it lost the race to a payload that died first (measured on 3.7b over a private socket, 12 trials each: a payload of `false` survived 6 and was lost 6). That window holds itself open through its payload instead, and §20 records why keeping both would be worse than keeping neither.

The difference between the two is what the pane is for. Here the hub wants to *observe* an exit code, so a dead pane carrying `#{pane_dead_status}` is exactly the wanted state. There the operator has to *read* a message, and a dead pane cannot deliver one: measured, its visible screen holds only tmux's `Pane is dead (status 255, …)` banner with the payload's own line pushed into the scrollback, where neither the operator nor a `capture-pane -p` looks.

**Consequence, recorded in §12 as an honest limitation:** for a foreign pane the hub cannot distinguish "the agent exited" from "the pane was closed", and will not pretend to.

### Liveness is emptiness

`display -p -t <gone> '#{pane_id}'` returns **rc=0 and an empty string** (measured on both a stale id and a never-existed one).

That is the liveness of a **pane**. A background agent row has no pane, and its liveness is the
daemon's roster — never `state.json`, whose `pid` is **absent** from all 25 local files including two live
`working` sessions, and which stays **byte-identical** when a session is killed while blocked; only
the roster entry goes (§22.5, §22.6). So a lifecycle verb checks a pane exists by asking for its own id and treating empty as gone.

### Why the hub validates a directory tmux accepts

Measured in §3: `new-window -c /nope-does-not-exist` → **rc=0, and the pane is CREATED with cwd `$HOME`**. On tmux 3.7b, a window asked for a nonexistent path inside a session created with `-c /tmp` landed in `/home/dev`. tmux neither rejects a nonexistent `-c` directory nor warns.

A typo'd directory would therefore silently start a coding agent in the user's HOME, outside any project, with no error anywhere. The hub checks a directory before creating the window: locally with `os.Stat`, remotely over the ssh master with `ssh -S <ctl> host test -d <dir>`. When the check cannot run at all (e.g. no ssh master for a remote host), the hub says so plainly rather than pretending to validate.

## 20. Possession — working in a pane without leaving the hub

The hub can read every pane and write into the ones the user selected, and until now
that was the whole relationship: to actually work in an agent the operator pressed `a`,
which ran `tmux attach` and took the terminal away from the hub. Possession removes that
trade. The operator lands in the real pane, the hub keeps running, and coming back is one
tmux keystroke.

### The key is `a`, which becomes a dispatcher

No new binding. `a` already means "take me to this thing", and it keeps meaning that with
five honest paths — a jump, a window, today's full-screen attach, today's refusal, and a
session the hub makes on the row's own host so one of the first three can then run (§22) —
reached by seven routings, in the order `decidePossession` asks them. The fifth is not a
fourth destination. It is the only routing that answers by MAKING the thing the others
found missing, and it then re-enters this same table for the pane it made, so there is one
decision and not a special case beside it:

| the row | what `a` does |
|---|---|
| an agent row that is not background — an interactive session held in a terminal the hub cannot reach | a refusal naming the remedy: `/background` typed in that session makes it reachable (§22.8) |
| the attach itself refuses: a remote host with no `ctl=`, a local host with no socket, a pane with no session yet | today's refusal, which already names the missing thing |
| the hub is not inside tmux (`$TMUX` empty) | today's full-screen attach — there is no client to switch and no session to hold a window |
| the pane's server reports the same `#{pid}:#{start_time}` as the hub's own, and neither is unknown | `switch-client` + `select-window` — possession, below |
| pane on a host with `ssh=`/`ctl=`, i.e. another machine | the existing remote attach, in a new window of the hub's session |
| pane on a LOCAL host that is nevertheless a different server — both epochs known and unequal, which is a second socket on this machine | the same window, holding that socket's own `tmux -S <socket> attach` |
| pane on a local host where either epoch is unknown | today's full-screen attach — an unknown epoch cannot rule out the hub's OWN socket, and the window path does not strip `$TMUX`, which tmux refuses for same-socket nesting |

The last two rows are the ones the fix in `28843c4` separated: `switch-client` cannot cross
servers, so a second local socket takes the window path exactly as a remote host does — but
only once both servers have identified themselves.

### Making the pane the row is missing

A `background` agent row is the one row where the missing thing is something the hub can make. One
call, on the row's own host, through the same runner every other tmux command uses:

```
new-session -d -s <the agent's name> \
            -P -F '#{session_id}|#{window_id}|#{pane_id}|#{pid}|#{start_time}' \
            <payload>
# <payload> is §20's `sh -c` wrapper around `claude attach <id> --debug-file <path>`
```

Seven things are load-bearing, and each is measured.

**The read-back is why this is one call.** That `new-session` answers
`$0|@0|%0|<pid>|<start_time>` at rc=0 — the session, window and pane ids AND the server's
`#{pid}:#{start_time}`, which is exactly what the table above decides on and the same value
`tmux.ServerEpoch` reads. Measured on two of the **three** tmux versions this fleet runs — 3.2a and 3.7b,
with the 3.4 on §17's third machine not re-taken (§22.10): on tmux 3.2a, against a
socket with no server at all (`error connecting to … (No such file or directory)`), the one call
returned `$0|@0|%0|2316153|1786824836`, started the server, and `display -p '#{pid}:#{start_time}'`
answered `2316153:1786824836` — byte-identical; on 3.7b over a private socket it answered
`$0|@0|%0|1503455|1786800069`, same shape. Without the read-back the created pane carries an
empty epoch, and an empty epoch on a LOCAL host takes the full-screen attach
(`internal/ui/possession.go:92-95`), so the commonest §22 row — a background agent on this machine —
would take the terminal and block the hub, which is the regression this section exists to prevent.
Waiting a tick instead is worse twice over: the create is invisible to the hub until the poll lands,
and a second `a` inside that window makes a second session.

**The decision lives in `AttachCmd`, not beside it.** That function already answers "what command
takes over this row" and already branches on `p.Kind`, because `SessionID` carries different things
per kind. A background row is a third such case, so the branch, the pane path and the interactive
refusal sit in one switch a reader sees at once, and `decidePossession` keeps asking exactly one
question (`possession.go:42`). A pre-check beside it would give reachability two readers.

**The gate is `Kind == KindAgent` AND `Command == "background"`, never a non-empty id.**
`internal/agents/agents.go:111-112` back-fills `ID` from `SessionID[:8]`, so an interactive row
carries a plausible short id with no daemon door behind it — a gate written `ID != ""` reads
correctly and fails at runtime. And "background" is not in `Kind` at all: `Kind` is provenance only
(`"pane"` / `"agent"`, `registry.go:34-35`) while Claude's own kind is assigned to `Command`
(`internal/registry/registry.go`, `p.Command = s.Kind`). So `Command` is the second dual-purpose field on the row beside `SessionID` —
the running command for a pane, the session kind for an agent — which is the other reason the branch
belongs where the first ambiguity is already documented.

**It is asked before the refusals, and that costs the refusals nothing.** Creating the session needs
the same host coordinates an attach does, so a local host with no socket still fails `ErrNoSocket`
and a remote host with no `ctl=` still fails `no master to send through`
(`internal/tmux/run.go:365,372`) — from the same function, with the same sentences naming the same
missing field, before anything is created. Only the ORDER changes, because `AttachCmd` refuses every agent row unconditionally today
(`internal/ui/attach.go:36`) and that call is the first question `decidePossession` asks.

**The session is named after the AGENT, not after the id**, because the status line is what answers
"where am I" (below) and §17 established that the name is the only thing that identifies an agent to
a person. A duplicate is refused rather than reused — measured on both versions,
`new-session -s alpha` against an existing `alpha` is rc=1 `duplicate session: alpha` — so the name
is disambiguated before the call, and a collision must never surface as a failed attach.

**The payload carries §20's existing wrapper, for §20's existing reason.** Measured on a private
socket, a session whose payload exits is destroyed with its pane and window
(`remain-on-exit off`, the default; §19 sets it on the launch window only). So a bare payload would evaporate the apparatus
on success — which is wanted — and evaporate the DIAGNOSTIC on failure, which is the exact defect
the wrapper was built for. The wrapper gives both: exit 0 leaves nothing behind, a failure holds a
live shell with its message on screen, and §22 measured one shape that exits 1 with a single stderr
line. `--debug-file` is on the payload because §22 measured a `claude` failure that wrote nothing to
stdout and nothing to stderr, and the wrapper's message names that path — and it belongs on the
PAYLOAD rather than on every `claude` exec, because §22.6 measured that `claude agents` rejects the
flag outright. `new-session` is already in the seam's opaque-trailing-argument list (below), so no
`Validate` change is needed — but for a REMOTE row the payload passes **three** shells, not two: the
runner joins the whole tmux command line into one ssh argument (`internal/tmux/run.go:393-397`), the
far tmux hands the trailing argument to the far `$SHELL -c`, and that `sh -c` re-splits the wrapper.
The test owes a real shell at each level, as the window path's already does.

**The re-entry terminates by construction.** What the create hands back is a `KindPane` row, and this
routing is reachable only from an agent row, so the table is asked at most twice and the second pass
cannot reach this row again.

**Nothing is kept.** No id, no name, no owner mark. The payload's exit destroys the pane, then the
window, then the session, and until then the created session is an ordinary row that `K` kills like
any other. Closing it costs the Claude session nothing (§22.2, measured: it keeps running either
way). Starting a server the host did not have is a consequence of creating a session rather than a
separate act, so §5's offer to start one answers a different question.

Possession is a property of running inside tmux. When the hub is started from a plain terminal, both
the jump and the window paths are impossible, so `a` falls back to the full-screen attach. That is
why the change cannot regress a hub started outside tmux. The fifth routing is not a property of
running inside tmux either — `new-session -d` needs no client and no session of the hub's own — so
outside tmux `a` on a background row still makes the pane and then hands it to the full-screen
attach, which is what a found pane gets there too.

§16's key table changes with it: `a` is no longer only "attach (full-screen)" — for the common case
it is a jump that leaves the hub running. The header hint §8 added
(`nested: leave an attached session with C-b C-b d`) becomes **path-specific**: it is still true of the WINDOW path's inner
tmux, on another machine or on a second local socket alike, and misleading for a same-server jump,
where the way back is `C-b L` and nothing is detached. There is no such thing as a remote *jump*
after this section: a target on another server always gets a window, because `switch-client` cannot
cross servers.

`hintFor` gains the fifth path, and this is the one hint that must promise LESS than the others. For
a jump the way back is `C-b L`; for a window it is `C-b C-b d`. For a background agent row **neither
is knowable before the keypress**, because which applies depends on the server the created session
lands on, and that server's epoch is read by the create itself. So this row's hint names the ACTION
and not a return key — `a → make a pane for it and go there` — and the return key is named once the
pane exists. A hint that guessed `C-b L` would name the wrong key on every remote row, and the header
has no room for both: `known-issues` S3 records that the window hint already truncates at 86 runes
against §16's 80 columns. §16's key table gains the same clause — `a` on a background agent row makes
the pane first.

### This was already parked in §8

The window-instead-of-takeover idea is not new here. §8's attach table already carries the
row *"`$TMUX` set, later — optionally `new-window` in the hub session instead, so the hub
stays live and attached sessions become tabs."* §20 implements that row and adds the
same-server case, which needs no attach at all.

### The hub does not render the pane — tmux does

Three designs were considered and all three were wrong in the same way: they had the hub
draw the pane itself, by polling `capture-pane -p -e` at some rate, or by consuming
`%output` from a control-mode connection, or by embedding a VT parser.

They are wrong because **tmux already is the terminal emulator**, and `capture-pane` is
its rendered state. Writing a second emulator over the first buys nothing. §22 does not reopen this. The
background daemon's `subscribe` returns raw pty bytes, and §22 records them as the only witness that a
reply landed — a later read of the target's own screen, which is §7's primary witness. **Reading them
is not drawing them**, nothing renders them, and §22 defers the path that would read them at all, so
there is still no second emulator and this ban is untouched. The
measurements that settled it are in §3: a local capture is 0.6–0.8 ms (so cost was never
the objection), Claude Code does not use the alternate screen (`alternate_on=0`, so a
snapshot really is what the user sees), and a capture through an ssh-forwarded socket is
**480 ms — 2.1 frames per second**, because each invocation opens a new connection. The
naive remote viewport was dead on arrival, and the correct answer for both local and
remote turned out not to involve drawing at all.

### `link-window` is forbidden, and a test enforces it

The obvious tmux-native mechanism is `link-window`: borrow the target's window into the
hub's own session. Measured, it is a loaded gun.

A linked window is **indistinguishable from an owned one**: after `link-window` its
`window_flags` is `*`, the same as any current window, and the default
`window-status-format` (`#I:#W#{?window_flags,#{window_flags}, }`) does not include
`window_linked`. So the operator sees an ordinary window in their own list — and
`kill-window` on that borrowed copy **destroys the window in every session and kills the
pane's process**. Measured: `work:agent` vanished from the work session and the agent's
pid was gone. The operator's own muscle memory (`C-b &`, "close this window") would
silently kill a live Claude session in someone else's session, behind a tmux confirmation
whose wording is identical to closing their own window.

`internal/tmux` therefore never gains a `LinkWindow` verb, and a source-level test greps
the production tree for `link-window` and fails if it appears — the same shape as the test
that keeps `#{client_activity}` out (§3).

### Same server: `switch-client`

When the target pane lives on the hub's own tmux server, possession is two commands:

```
switch-client -t $S        # the target's session, by id
select-window  -t @N       # the target's window, by id
```

Both are targeted **by id, never by name**, for the reason §7 already gives: a name does
not survive a rename. `switch-client -t <session>:<window>` does work as one command
(measured) but requires name targets, so it is not used.

Returning is `switch-client -t <the hub's own session>`, or the operator's own `C-b L`
(last-session) without involving the hub at all.

The hub learns its own coordinates from `$TMUX`, which is
`socket,server_pid,session_id` — the third field is a **bare number**, while every tmux
session target needs the `$` sigil, so `0` must become `$0`. Forgetting it would target a
session *named* `0`; the seam catches that, because `shapeFor` requires `^\$[0-9]+$` for
`switch-client`.

Applicability is an **equality, not an inference** — but not an equality of the strings the
user typed. `--host label=/path` carries whatever spelling the operator chose, and measured,
a symlink to a socket reaches the same server while comparing unequal. tmux canonicalises
for us: reached through a symlink, a server reports the same `#{socket_path}` and the same
`#{pid}` as through the real path. So "same server" compares what each server says about
itself, and the hub already carries a per-host `Epoch` of `pid:start_time` for the restart
check — one read of its own server closes it. This follows the rule `Host.LocalProc`
establishes: the hub never guesses which machine or server something is on, and now it does
not trust a spelling either.

### Other server: the existing attach, in a new local window

A remote pane cannot be reached by `switch-client`: it is a different tmux server. The
existing attach ARGV is reused element for element; only its container changes. Instead of
taking the hub's terminal, it runs in a new window of the hub's own session:

```
new-window -t $S -n <label>   'sh' '-c' '<the argv below>; s=$?; [ "$s" -eq 0 ] || { printf "\n[tmux-hub] the attach exited %s — press enter to close this window\n" "$s"; read _; }'
                              # where the argv is 'ssh' '-S' '<ctl>' '-t' '<dest>' 'tmux' 'attach' '-t' '$3'
                              # on success (exit 0) the window closes silently; on failure it waits for enter
```

**One command, and the wrapper is why.** `new-window` exits 0 whatever the payload then does,
so a host whose ssh master has died created the window, the payload printed `Control socket
connect(...): Connection refused` and exited, the default `remain-on-exit off` closed the
window with the message inside it — and the hub reported `back from api:review`. Before §20
that same failure came back through `attachedMsg` as `attach failed: …`, so possession had
quietly dropped a diagnostic the full-screen path always had.

The first fix for that was a second command, `set -w -t <@N read back with -P -F>
remain-on-exit on`, and it was **wrong in a way that only a real server shows**: `new-window`
is what STARTS the payload, so the option can only arrive after it and win on time. Measured on
tmux 3.7b over a private socket, 12 trials each: a payload of `false` survived 6 and was lost 6;
one that spawns a shell first survived 12. The sign of that race is a property of the machine,
and the remote failures this path exists to show are the fast ones (`ssh: Could not resolve
hostname …` needs no round trip at all). There is no earlier place to put the option, so the
mechanism had to change rather than be retimed.

It got worse than a coin flip, and this is the half the option could not fix at any timing:
when it DID win, the window survived with a **dead** pane, and measured, a dead pane's visible
screen holds only tmux's own `Pane is dead (status 255, …)` banner with the payload's line
pushed one row into the scrollback — where neither the operator nor the hub's own
`capture-pane -p` looks. So the option bought a window that stayed and still could not say why.

The payload keeps the window on failure. `sh -c '<argv>; s=$?; [ "$s" -eq 0 ] || { printf …; read _; }'`
leaves a LIVE shell in the pane after a failing attach: nothing can close the window, nothing clears
the screen ssh wrote on, the status is stated, and enter closes it — which is also why no option is
set here, since `remain-on-exit on` would answer that keypress with a corpse instead of a closed
window. On success (exit 0) the window closes silently, which is what a detach did before §20 and
is the case that happens every time. The wrapper exists to make failures visible; a success has
nothing to report, and a keypress on every successful jump is ceremony charged to the commonest case.
`internal/e2e` asserts both arms against a real server: a failing payload keeps its window with
`#{pane_dead}` = 0, and a succeeding payload closes the window within milliseconds.

Every element is **shell-quoted**, and that is not cosmetic. tmux hands a single trailing
argument to `$SHELL -c`, so joining the argv with spaces is a re-parse: measured on 3.7b,
`attach -t $3` reaches the far side as `attach -t` (rc=1), `-t $0` as the shell's own name,
and `-t $10` as a bare `0` — which attaches to whatever session is *named* `0`, the very
outcome `shapeFor`'s `^\$[0-9]+$` rule exists to prevent, manufactured *after* `Validate`
approved the argv. Quoting keeps the payload one trailing argument, so it stays exempt (below)
and stays inside the forbidden-format scan. The wrapper adds a SECOND level of the same
quoting, by the same `shellJoin`, because there are two shells on this machine: the default-shell
re-splits `sh -c <script>`, and that `sh` re-splits the script into the attach argv — a defect at
either level is a payload that reaches ssh altered, which is why the unit test asks a real `sh` at
both. For a remote target there is a THIRD shell, on the far side, and it is the one §8 describes:
the target element arrives here **already quoted** for it, so what the two local levels have to do
is deliver those quotes to ssh rather than consume them. They compose without a special case,
because `shellJoin` quotes each element whole — the id ends up wrapped twice, and the test asserts
what ssh receives (`'$1'`) rather than what the payload looks like. The wrapper's own syntax (`$?`,
`read`) is handed to `sh` and never to `$SHELL`, so an
operator whose default-shell is fish or csh changes nothing. Two other things are load-bearing: the quoting
must survive an embedded `'` (closed, escaped, reopened), and only the ARGV travels — the
TMUX-stripped environment `AttachCmd` builds does not, which is harmless because ssh does not
forward `TMUX` and because a second local socket is a different server, where (measured)
`server_client_check_nested` keys on the client's tty against panes on the TARGET server.

Returning is then a local window switch — **no detach at all**. That retires the wart §8
records: today's remote attach hides the outer session (`withoutTMUX`), so leaving the
inner one requires `C-b C-b d`, and a user who types `C-b d` out of habit is thrown out of
the hub instead. With the attach in its own window there is nothing to detach from — but the
inner tmux is still nested inside the hub's, so an operator who *does* want to detach it
needs `C-b C-b d`, which is what the header hint says on this path.

On a pane the hub made for a `claude attach` (§22) there are **three** ways out and the hub owns one.
`claude attach`'s own help names `←` (back to the agent view) and `Ctrl+Z` (drop to the shell): both stay
INSIDE the pane, so neither returns to the hub, and neither is a tmux key — `C-b C-b d` and `C-b L` are
unaffected and there is no prefix to collide with. `Ctrl+Z` is the one worth stating, because everywhere
else it means "suspend and give me my shell back" and here it gives a shell in a session the hub created,
with the possession still on and the way back still the tmux key the hint named. Leaving by any of the
three costs the Claude session nothing — §22 measured that it keeps running whether the pane lives or
dies — so the hub intercepts none of them.

`AttachCmd` needs no edit **for the container** — the payload quoting happens where the container
is built, so the one builder of the attach argv stays the one builder. It did need one for the far
side: its remote target is `shellQuote`d for the remote user's shell (§8), which is a defect of
attach itself rather than of possession, and it is fixed in the one builder for both paths at once.
**The seam** needed a change too, and this was
found by asking it rather than assuming — the same way the lifecycle verbs were. `Validate` refused the
container twice over: first on `ssh`'s own `-t` (which means "give me a tty", not a tmux
target), then on the remote tmux's `-t $3`. Both live inside one multi-token argument, which
`validateArg` treats as a tmux sub-command chain.

The fix keys on the OUTER verb, because keying on the payload is wrong in both directions —
also measured. A real chain often begins with a FLAG continuing the outer command
(`paste-buffer -b b '-t @4 ; display …'`), so "the payload starts with a tmux verb" skips
real chains; and it would stop checking `display -p 'OK %s'`, whose `%` is exactly the
strftime trap §3 records. So: for the verbs whose trailing argument tmux hands to a shell —
`new-window`, `new-session`, `respawn-pane`, `split-window`, `run-shell` — that one argument
is **opaque**, and the target and `%` rules do not apply to it. The forbidden-FORMAT scan
still does, because `Validate` runs it over every argument first; a format hidden inside a
shell payload is still refused, and that case belongs in `internal/tmux/run_test.go`, one of
the four PATHS `guard_test.go`'s source scan exempts for exactly this reason. Paths, not base
names: `internal/e2e/guard_test.go` also exists, and a base-name exemption gave it a pass
from both bans that nobody decided to give it.

Closing that window is safe, and this asymmetry is why the design is acceptable where
`link-window` was not. Measured: killing the local window left the remote agent's process
**alive** and its session **present**, with the client count dropping to zero. Killing a
linked window killed the agent. Same keystroke, opposite consequence — so the hub only ever
creates windows it owns.

### What the operator sees

Nothing is added to the dashboard, because tmux already answers "where am I" more loudly
than the hub could: the status line's left segment changes from `[hub]` to `[work]`, with
that session's window list beside it. In a window holding a remote attach the operator
additionally sees the remote tmux's **own** status line carrying the remote hostname —
measured, `[ag] 0:sh* … "nuc-dev"` — so "another session" and "another machine" are visually
distinct without the hub drawing anything. On the §22 routing that left segment reads the name the
HUB chose, which is the second reason the created session is named after the agent rather than after
its id: the status line IS the answer to "where am I", and `[3f2a1b09]` answers it worse than
`[refactor-parser]`.

No marker column is added for "possessable", and after §22 the reason is no longer that the
fact is constant per host — it is not. Two paneless rows on one host now differ:
`background` has a door and an interactive session held in a terminal the hub cannot reach
has none, as a property of the world (§22.8). The row does not say which, either — the kind
lands in `Command` (`internal/registry/registry.go`, `p.Command = s.Kind`) and the agent row's format drops
that column (`internal/ui/render.go:104-111`), so it is visible only in the tile
(`render.go:194`). The column still is not added, because it would spend a column on
every row for a fact that changes once per run; the refusal carries the reason instead,
which is the rule the rest of the interface already follows. What does NOT survive is the
second half of the old argument — that the header hint also changes on this row.
`hintFor` returns `""` for every row when the hub is not inside tmux, and `pathRefuse`
falls to the `default` arm and gets the same ambient nested-session hint a pane row gets
(`internal/ui/possession.go:285-299`), so the hint carries nothing about the refusal. Only
the tile does, and only for the focused row.

### The hub keeps no state, and keeps polling

There is nothing to clean up: no borrowed window, no saved geometry, no restored name, no
unlink on a tick, no recovery after a crash. The hub keeps no possession state at all —
not even where the operator was. That value travels in the message that reports the jump
finished (`possessedMsg.from`), and the note it produces ("back from api:review") persists
until the next keystroke replaces it.

§22's routing CREATES something rather than borrowing it, and it is still not state the hub keeps: no
id is recorded, no name, no owner mark. It does not need to be. The payload's exit destroys the pane,
then the window, then the session — measured, with `remain-on-exit` off, which is the default and
which §19 sets on the launch window alone — and until then the created session is an ordinary row,
killed by `K` like any other. That is acceptable for the same asymmetry that makes the window path
acceptable: the hub only ever creates containers it owns, and closing one costs the Claude session
nothing (§22).

That message carries `from` even when it carries an error, for the cases where both are
true. **One** path reaches that state: `switch-client` lands and `select-window` is then refused
because the window was killed between the poll and the keypress. The window path used to be the
second — `new-window` succeeded while `remain-on-exit` could not be set on the window it made —
and it lost that state along with the option: a `new-window` either creates the window (and, with
no `-d`, makes it current, so the operator moved) or creates nothing at all. Where the state does
arise the operator HAS moved, so the note names both halves ("moved into api:review, but …")
rather than "cannot go there", which would deny a move that happened and leave the
operator's `C-b L` returning them from somewhere the hub said they never reached. Which half
failed rides in the error rather than in the note's template — naming one of them there made
the note describe a select-window that the window path never issues.

§22 gives that state a **second** path, and a different shape: the create landed and the go-there did
not. `new-session` either makes the session or makes nothing, but it runs BEFORE the jump or the
window, so "creates nothing at all" stops covering the pair. Here the operator has NOT moved and
something exists, which is the reverse of the first case, so the note must name what was left
behind — `made <name> on <host> for it, but could not go there` — and never "cannot go there", which
would hide a session the operator now owns and cannot find. One way in is already recorded: §12's
remote-socket gap, since the hub's remote tmux command line carries no `-S` while the attach targets
the far default socket.

Measured with the real binary in a nested harness: while its pane was not displayed the
hub **kept polling** — a window created during the operator's absence was already listed on
return (the header went `1 session` → `2 sessions`) — and the full-screen TUI **redrew
without a single artefact** when the client came back. Today's attach cannot say either:
it blocks the hub for the duration.

## 21. Projects and aliases — the second question the fleet is asked

The dashboard sorts by attention across the whole fleet, which is right when the question is "who
needs me" and wrong when it is "what is the state of the thing I am working on" — because the answer
is nineteen rows deep in a list of twenty-one. Measured: 21 rows, 5 waiting, 7 groups. A group where
three of four sessions want you is invisible in a globally sorted list.

### 21.1 What a project is

A project is derived from a row's **working directory**, host-qualified, and overridden by rules the
operator writes.

**The path comes from two places, and the row's KIND decides which.**

| row kind | source | note |
|---|---|---|
| `KindPane` | `#{pane_current_path}`, carried by the label format (§6) into `registry.Pane.Path` | a path not yet read is `Pending`, never `Unassigned` |
| `KindAgent` | `agents.Session.CWD` — present in **25 of 25** real `state.json` files (2026-08-14), and `cwd` is one of the listing's own nine keys on both 2.1.233 and 2.1.224 (§22.6) | never aged: an agent's cwd is fixed at session start |

Naming the agent source is not a detail. **57 of 65 rows have no pane at all** (2026-08-16, `--all`,
three hosts, §22.1), so a design that fetched only `pane_current_path` would put every row this screen exists to
show into the bucket §21.2 pins last.

**The unit is a PANE, not a session.** Measured: two sessions over two project directories produced
5 panes and 3 distinct (session, path) pairs. Panes of one session scatter, so a session cannot
carry the answer.

**The identity is (host, path).** Measured: `/home/dev/.claude-mem/observer-sessions` exists on both
`local` and `nuc` and means different things there. Merging across hosts is an explicit act in the
file, never inferred.

**The default is not the git root, and the fallback is the last path segment.** Measured on the
21-row fleet: grouping by git root gives **15 groups with 10 singletons** — a grouping that does not
pay for itself. Six prefix rules give **6 groups and leave 2 rows**; those two reach a 7th group
through the basename fallback. Nothing is unassigned on this fleet, but a row with no path at all
would be, and that bucket is real.

**Nothing stores a project.** It is derived per frame by a pure function: no `Pane.Project` field to
go stale, no invalidation, no cache to be wrong.

### 21.2 The two screens, and the attention cell

**The project list**, reached with `P`. Sorted by waiting, then broken, then unknown, then size,
then name; the unassigned bucket pinned last whatever its size.

**Inside a project and on the dashboard, the waiting block sorts by how long each row has waited,
longest first** (§21.11.1, built). The tiebreak lives in `registry.SortByAttention`, not in a screen,
so every surface orders identically and a fixture cannot produce an order production cannot.

**The attention cell**, defined rather than implied:

- Up to three facts, in this order: `⚑ n` waiting, `✗ n` **broken**, `? n` unknown, then `of N`.
- **"Broken" is `Error` only.** `Gone` is not broken — §6 keeps a vanished pane with its last screen
  and it is not something to act on — and it gets no fact of its own; it is inside `of N`.
- **Blank when there is nothing to say.** Never a state glyph as filler: `·` is `Works` and `?` is
  `Unknown`, so a filler would assert a state.
- **Width: 16 columns.** Measured with the product's own width arithmetic, the widest cell this fleet
  produces is **14** (`⚑ 3  ? 1  of 4`). Two-digit counts on all three facts would need 22, so the
  overflow rule is required rather than theoretical: **drop from the right** — unknown first, then
  broken — and mark the loss by appending `+` to the last fact kept. A cell that fits exactly is
  never marked.

**Inside a project**: the dashboard, narrowed. Every key keeps its meaning.

Frames: none are targets yet, and the reason is worth stating rather than leaving a dangling
citation. The rev3 set was generated inside a `git archive HEAD` tree — the right recipe, so the row
order came from `registry.SortByAttention`, the glyphs from `state.State.Glyph()` and every width from
`internal/lines` — but it lives under `.superpowers/`, which is gitignored, so a reader cannot open it
from a checkout and no gate can diff it. It is also stale against two corrections this section makes:
it prints the pre-N6 census (`(empty) 11`, now 2) and `Projects — 7`, which §21.13's closing paragraph
overturns in favour of 8. A regenerated set belongs under `docs/` when it is produced. **Any frames for
this screen are at 80 columns only** — §16's other two width bands are not specified for it.

### 21.3 The action set, and what it may not promise

A blocked, pane-less agent row can be **shown**, and **opened** through §22's `claude attach`. It can
not be *answered* from the hub as designed, and §22.4 says what that would cost: the write path needs
a token, and the token comes from a process walk over a PANE. No pane, no walk, no token, no send.
"Never" would be false about the world — `reply` reached a session with no tty, no pane and no viewer,
`ok` in 13 ms — and true only about this product.

**What it CAN show, and from where.** Per-session state lives in `~/.claude/jobs/<id>/state.json`.
Measured across 25 real files:

| field | present | what it is |
|---|---|---|
| `detail` | **25/25** (2026-08-14); **31/32** over 32 files (2026-08-15, §22.5) | one informative line the agent wrote |
| `respawnFlags` | 25/25 (2026-08-14); 32/32 (2026-08-15, §22.5) | the respawn recipe, a flag list |
| `resumeSessionId` | 25/25 (2026-08-14); 32/32 (2026-08-15, §22.5) — presence counted, difference NOT | **separate from `sessionId`** — not always the id you resume with |
| `state` | 25/25 (2026-08-14); 32/32 (2026-08-15) | the FILE says `blocked` on 5 of 25; the LISTING says `blocked` on 5 locally and 6 fleet-wide, and `block.questions` is on 1 of those 6 (§22.5) |
| `needs` | **4/25** (2026-08-14); **6/32** (2026-08-15, §22.5) | richer, present on some blocked sessions only |
| `intent` | 25/25 (2026-08-14) — not re-measured on 2026-08-15 | **the OPERATOR's own prompt** |

So the tile shows **`detail`**, with `needs` preferred when present, then the listing's own word, then `unknown` — four rungs, because `detail` is 31 of 32 and 2 of the 6 local interactive rows carry neither `state` nor `status` (§22.5). `intent` must never be shown as
the agent's question: it is the operator's own words. The label is a status line the agent wrote, not
a question it asked.

**This is a new read, and the cost is stated rather than hidden**: one batched read per host on the
slow sweep (~2.4 s over ssh, so never on the tick and never on the paint path), and a parse of a
schema this project has not previously depended on.

**The recipe as shown.** `respawnFlags` plus `resumeSessionId`. Real recipes measured 477-574
characters and do not fit a 48-column tile, so the tile shows the first line and the fact that there
is more; the full recipe belongs behind a copy action, never truncated into something the operator
might retype wrongly. A hand-built `--resume <sessionId>` must never be shown — the id to resume with
is `resumeSessionId`.

### 21.4 Keys

| where | key | what |
|---|---|---|
| dashboard | `P` | the project list |
| project list | `j`/`k`, `enter` | move, open |
| | `a` | open the project with the cursor on the row that wants you — the FIRST waiting row, which is the longest wait since §21.11.1 orders them; a project where nothing waits opens on its first row rather than refusing |
| | `N` | name this project |
| | `esc` | back to all sessions |
| inside a project | all dashboard keys | unchanged |
| | `a` | pane row: attach. Agent row: §22's door |
| | `N` | name this session |
| | `tab` | next project, cycling at the end; with no filter on it opens the first |
| | `esc` | clear the filter |

`ctrl+c` still quits from every screen — handled above the mode dispatch, so a new mode inherits it.

### 21.5 The filter, and the one thing it must not do

**The filter narrows what is shown and what `A` takes. It never prunes the selection.**

`visibleRows()` is the only input to `sel.Prune`. Putting the project into it would make a
re-resolution silently drop a mark: a pane the operator `cd`s in changes path, changes project,
leaves the filter, and its mark disappears mid-compose. So

- `visibleRows()` keeps its exact meaning — the hidden set — and stays `Prune`'s only input;
- `rowsForScreen()` is `visibleRows()` narrowed to the active group, and the renderer and `A` both
  call it, so `A` still means "select what is on screen";
- a marked row outside the filter is **named**: `1 selected row is not in this project — enter
  still sends to it`.

The filter is `struct{ on bool; group string }`, never a bare string: `"u:"` is the unassigned
bucket's real id and `on == false` is "no filter", so `enter` on that bucket cannot render as `esc`.

### 21.6 The count, as an invariant

**Once grouping has resolved, the sum over all groups equals the dashboard's number, and every row is
in exactly one group** — including a row whose path could not be read, which is `Pending` and still
counted. Before resolution the sum is not claimed at all (§21.9.1). This is a test, not a convention:
two screens one keystroke apart must not disagree.

### 21.7 Where the pane path comes from

It is a LABEL, not a delta field: §6's rule is that the delta carries only character-restricted
values and a path is unbounded. It arrives through the one length-framed label format §6 specifies,
as `registry.Pane.Path`.

**`#{=N:pane_current_path}` was the earlier proposal and is REFUSED.** That modifier neutralises a
newline by DELETING control bytes — which silently rewrites the path, so two directories differing
only by a newline would merge into one project, the exact failure the framing exists to prevent.
`#{n:X}` plus the raw value keeps the path byte-exact, and a stream that does not line up is an error
naming the pane rather than a plausible wrong group. Measured before it shipped: `#{n:X}` answers
correctly on **3.2a** as well as 3.7b, and of the labels in the table only `pane_current_path` can
put a raw newline on the wire at all.

Two things the derivation must still handle, both measured:

- **Strip a trailing ` (deleted)`** — tmux appends it for a pane whose directory was removed, and its
  length matches, so the value is trusted and must be handled by the reader of the path rather than
  by the framing.
- **`list-panes -a` can emit one pane twice** after `link-window`, so a pane-keyed reader must
  tolerate a repeated id.

Never a per-pane `display -p`: 9.9 s for 30 panes over ssh against 332 ms for one `list-panes`.

### 21.8 The durable pane key

**H1 is fixed, and not the way this section first proposed.** The hide key's window component is now
`window_index` (§18), because the window's NAME follows the running command under
`automatic-rename`. The minted `@hub_pane` option remains a candidate for a later round, and the
limit that keeps it out of the key today has to be stated: it survives every structural operation
**and** `respawn-pane`, but it is **absent on a new server** — so it answers within-server identity
and loses across-restart identity, which is the opposite of the trade a persisted key needs.

Rules that would come with minting, measured and kept for that round:

- Read a mark only through `#{@hub_pane}`. `show-options -v` on an unset option is rc=1 with
  `invalid option:` on 3.7b but rc=0 and empty on 3.2a — half the fleet would parse an error as a
  value.
- A **pane** option and a **session** option of the same name collide, the pane's shadowing the
  session's. Session-scoped marks need a different name.
- Scope the promise with the server's fingerprint `#{pid}:#{start_time}`.
- Never persist or target a concatenated `session:window.pane` string: a session name keeps `:` and
  `.` verbatim, and `-t "we:ird.name:0"` fails with `can't find window: ird`.
- §19's "exactly two places where the hub writes options" becomes three, with `set -pu @hub_pane` as
  the reversal — and minting is lazy at the first mark or explicit, never eager on poll.

### 21.9 Empty and degraded states

§9 requires empty states to be specified screens. Five exist, and two of them are not empty:

1. **Nothing resolved yet.** §16 forbids blocking the first paint, so this state necessarily exists.
   The header states the REAL session count and says only the grouping is unknown; a header reading
   "0 sessions" while 21 exist would lie for the whole window. §21.6's invariant is not claimed here.
2. **No rules yet, and paths resolved.** Every row groups by its basename fallback — on this fleet
   that is 15 groups, not one bucket. The remedy names the file and `N`.
3. **Rows whose path could not be read** (`Pending`). These land in one bucket, and "write
   projects.toml" is **not** the remedy — the remedy is that the path was unreadable, named per host.
4. **No sessions at all** — `n` starts one, `p` adds a host.
5. **A host that did not answer** is a fact about the fleet, not an absence: its rows keep their last
   state and the group line says how many are stale.

### 21.10 What this does not do

- It does not let the operator answer a pane-less agent. That is a property of the world; the screen
  says so rather than offering a key that fails.
- It does not group by git root — measured, that grouping does not pay for itself here.
- It does not add a triage state. The state machine re-ranks a row when reality changes; what was
  missing is ordering within the waiting block and an honest count.

**Its five prerequisites are all fixed, and each was a defect this screen would have inherited:**

| | what it would have done to this screen |
|---|---|
| N2 | the label reader framed blocks by COUNT, so the first path with a newline shifted every later block — no path could be fetched at all |
| N1 | `ActivityAge` was per-WINDOW, so a broken pane with a chatty sibling could never be counted in `✗ n` |
| N6 | `Attention()` knew half the vocabulary, so `? n` mostly reported "I do not know", which defeats the screen |
| N4 | two sessions sharing a `sessionId` collapsed to one row, so an alias keyed on it landed on two sessions in two projects, silently |
| N5 | the config dialect returned a value containing `"`, `\` or a newline WRONG and without erroring, and a project name is free text a person typed |

### 21.11 Decisions taken

1. **The waiting block sorts by how long a row has waited, using a new registry field.** `BUILT`:
   `registry.Pane.StateSince` is the tick a row entered its state, and `SortByAttention` orders the
   `Needs` block by it, oldest first. Scoped to that block on purpose — every other rank keeps
   host/session/pane id, which is what makes the list stable between ticks. A zero `StateSince` is
   UNKNOWN, not the beginning of time, so a row the registry has never dated cannot outrank a real
   wait. Chosen over reading `updatedAt`/`firstTerminalAt` from `state.json` because those exist only
   for agent rows, and two classes of row sorting by different rules on one screen is a thing the
   operator would have to be taught.
   **The consequence that shipped with it:** the detail tile said `since:` over `p.Activity`, which is
   the session's START for an agent row and the pane's LAST OUTPUT for a pane row — and this decision
   adds a third clock. Three meanings cannot share one word, so that label is now `started:`.
2. **`x` on a waiting pane keeps its behaviour and gains a sentence.** `BUILT`: `hideSubject`
   reads the mark BACK rather than assuming the direction — the same key un-marks — and says
   `hidden: <name> stays while it is waiting, and goes when it stops asking`, counting the rows when
   the subject is a selection. A row that was NOT waiting vanishes the moment it is marked, which is
   its own feedback, so it gets no sentence in a channel four other things want. Before it, `x`
   wrote a mark that
   takes effect the moment the pane stops asking, and says nothing — so the key that reads as "not
   now" silently means "forever, once it goes quiet". The behaviour is right: it matches §18 and it
   preserves the resurface rule, which is the only safety net a wrong hide has. What is wrong is the
   silence. `x` on a row whose state is `Needs` must report what it just recorded, naming when it
   takes effect.
3. **Project names and aliases live in their own `$XDG_CONFIG_HOME/tmux-hub/projects.toml`**, beside
   `hosts.toml`, following §9: decisions in config, derived data in state. Chosen over one combined
   file for a reason about failure rather than tidiness — an unparseable `hosts.toml` must stop the
   program, because an empty host list is indistinguishable from a first run and the next save would
   overwrite it, while an unparseable `projects.toml` must lose names and keep the fleet. One file
   would force one rule, and it would be the harsher one: a typo in a project name would refuse to
   start the hub.
   **Its own FILE, not its own PARSER.** An earlier revision required a second reader because
   `internal/hostset`'s dialect corrupted 5 of 19 measured values silently. That dialect is fixed —
   the reader is now `strconv.Unquote`, the exact inverse of the `%q` the writer uses — so a second
   parser would be a duplicate, and the duplicate would have been written by copying the defective
   half.

### 21.12 The naming screen (`N`)

**Built for SESSIONS and for PROJECTS**, and the project half needed the gap this section left to be
closed rather than argued: a project name has to become a prefix RULE, a rule needs a PREFIX, and a
derived group is keyed on `(host, last path segment)` — not a prefix. Rows in such a group can sit
under different ancestors (`/a/st` and `/b/st`), so there may be nothing safe to write.

**The answer is a MEASUREMENT rather than a rule about how shallow an ancestor may be.** The
candidate prefix is the longest common ancestor of the group's OWN rows, cut at a path boundary; the
prospective rule is then APPLIED to the whole fleet and refused unless it captures exactly the rows
it was asked to name, with the refusal listing what it would have swallowed
(`naming this project would need the prefix /w, which also takes /w/other — those rows are not in
it`). So the dangerous case is rejected by trying it, not by reasoning about it. A group of panes in
ONE directory — the common case — names cleanly; a group that is the same basename in two places is
refused and told to name a narrower directory.

Two consequences worth stating. A group that is already NAMED reuses its own rule's prefix, so
naming twice REWRITES rather than stacking — `Parse` refuses two rules for one prefix, and a second
one would produce a file the reader rejects. And the rewritten set is PARSED before it is written, so
a rule set the reader would refuse can never reach the file: otherwise the operator would have to
hand-edit TOML to recover from having typed a name.

1. `BUILT` **The surface is a fixed six-row overlay at the foot** — separator, subject, `now:`, field, reason,
   keys — always six, so nothing beneath it moves; the base renders at `height - 6`. Inline edit is
   ruled out by `InboxViewport`'s per-row arithmetic and by the name column being 16-17 display
   columns at width ≥ 100, so the tail of what you type would be invisible. A chrome prompt is ruled
   out because one row cannot hold subject, text, reason and keys. **The reason row is INSIDE the
   overlay**, so naming adds no claimant to the footer.
2. `BUILT` **The subject is the row under the cursor, never the selection**, and it is CAPTURED at the keystroke rather than re-derived per frame, so a list re-sorting under a probe cannot move it between opening the overlay and pressing enter: the subject must be visible on
   screen at the moment of commit, and a marked set is not.
3. `BUILT` **`N` names a PROJECT as well as a session, and the same overlay serves both** — only the subject
   row differs. What does not transfer is rule 2: on the project list a group cannot be "the row under
   the cursor" while the list re-sorts under a probe, so the subject is captured at the keystroke and
   named in the overlay.
4. `BUILT` **Duplicates are checked fleet-wide, case-folded, at commit, against the file RE-READ for the
   write** — which is also what keeps another writer's entries, since a save built from the in-memory set alone would drop everything that arrived meanwhile — never against the in-memory copy the screen was drawn from.
5. `BUILT` **Un-naming is `N`, `ctrl+u`, `enter`.** The field opens pre-filled with the current alias only,
   never with a derived name, so committing an untouched field cannot silently freeze a derived name
   into the file.
6. `BUILT` **One `displayName(row)`, precedence alias → Claude's `name` → tmux session name**, with a `» `
   marker when the name shown is the operator's own. Every surface calls it, so no screen can show a
   different name from another.
7. `BUILT` **Nothing derived is stored** — not the row's project, not a derived name, not a name source, not
   a multiplicity count, not a dormancy flag.

An alias names a **session**, never a pane: the operator names the work, and the work outlives any
one pane. **A wrong alias has no safety net** — §18's hide has one, since a wrongly hidden pane that
starts waiting comes back, while a wrongly named session stays selectable and writable under the
wrong name. So the naming path refuses an ambiguous subject at the moment of naming rather than
repairing it later.

### 21.13 Decided while building, and where

Two of the items this section listed as "left to the plan" had to be answered to write the
code, so they are answered here rather than left as folklore in a commit message.

1. **`projects.toml`'s grammar** is `[[project]]` records with `name`, `prefix` and an
   optional `host`, read through the one dialect (§21.11.3, `internal/conf`). The longest
   matching prefix wins; a host-qualified rule beats an any-host rule of the same prefix,
   being the more specific statement; and a prefix matches on a **path boundary**, so
   `/home/dev/lab/st` cannot swallow `/home/dev/lab/streams`. The same prefix claimed twice
   under the same host scope is refused **at parse time, naming both `prefix =` lines** —
   "equal length is an error" reduces to exactly this, because a path has one prefix of each
   length, so two different equal-length prefixes can never both match. Refusing it per
   frame would put the tie in front of a renderer that has to guess, where the operator
   cannot see it.
2. **The group-id encoding** is namespaced by kind: `n:<name>`, `d:<host>\0<segment>`, `p:`
   and `u:`. The bucket ids are real, which is what §21.5's filter needs. The **collision
   rule** is computed over the whole SET (`project.Labels`), because a collision is a
   property of the set and not of one group: two derived groups with the same last path
   segment on different hosts are qualified with `@host`, and a label with no collision is
   left bare so a distinction that is not there costs no columns.

**One place the implementation departs from this section's own arithmetic.** §21.1 rules the
identity is (host, path) and cites a measurement — `~/.claude-mem/observer-sessions` exists
on `local` and `nuc` and means different things there — while the group count in that same
paragraph treats those two rows as ONE group. Both cannot hold. The rule has the measurement
behind it, so derived groups are host-qualified and that fleet has 8 groups rather than 7.

### 21.14 Left to the plan, and NOT decided here

A plan author must not invent these; they are named so the next design conversation is short.

1. ~~The alias store schema~~ — **decided, built, and then RE-KEYED, because the first key came off
   the moment the product renamed the thing.** An alias names a SESSION, so which fields identify it
   depends on whether the hub knows its Claude uuid. Where it does, the key is
   `(Claude session id, cwd)` — no host and no row kind, because the product changes both by itself:
   the door makes a tmux session called `<name>-<short id>` and the join folds the pane-less row into
   that pane, so the row goes agent → pane; and the shared-store dedup (§22.12) attributes a session
   that two hosts report to the fleet-first one, so the Host moves under the row. The first key carried
   both, and the report was that sessions the operator had named stopped showing their names — measured
   against the operator's own file, two builds differing only by this key: 5 of 6 names shown before
   against 6 of 6 after, the lost one being a session the dedup had moved from `side-desk` to `local`,
   whose row read Claude's own `Count critical actions in actions.yaml`. This is §12's favourite defect
   one surface along, and it takes the same answer: key on the identity that survives every transition
   the product performs, not on the identity the operator reads. The cwd STAYS in the key because the
   uuid alone is not unique either — measured, one `sessionId` carried two sessions in two directories
   (N4) — and an alias keyed on the uuid alone would land on both, silently, which §21.12 says has no
   safety net; it is stable across both transitions above because the door creates its session with
   `-c` at the row's own path. Where there is no uuid the key stays `(kind, host, tmux session name)`,
   and there the same name on two hosts is two sessions: the two rules are not inconsistent, they
   answer different questions. Unlike the favourites file, this one needs NO migration — every record
   already carries its `id` and its `cwd`, so the reader IGNORES the `host` an older `N` wrote rather
   than refusing the record, and the writer stops emitting it, which clears the stale field the next
   time `N` saves. Stored as `[[alias]]` records in the same `projects.toml`, read by the same dialect.
2. **What the other dashboard keys do on the list — DECIDED, and it is a DIVISION rather than a
   list, which is why it fits in two branches instead of twenty.** A key whose subject is the FLEET
   works here and goes through the SAME entry point the dashboard uses, so there is one
   implementation per key rather than two that drift: `h` (the send log), `p` (the host set) and `n`
   (a new session, defaulting to the host under the cursor). A key whose subject is a PANE has none
   on a list of projects — `i ! R K x X A C space r` — so it NAMES what it needs
   (`i acts on a session — press enter to open a project first`) rather than doing nothing, because
   "nothing happened" is what a broken key looks like. Anything else answers too, naming itself.
   `ctrl+c` quits, inherited from above the mode dispatch and asserted rather than assumed.
   On the list itself: `P`, `esc`, `enter`, `j`/`k` and `a` are built. `tab` is built in BROWSE — it
   walks to the next project from inside one — and does nothing on the list, where there is no
   "current project" to step from. It CYCLES at the last project and opens the first when no filter
   is on. `N` on the list names the PROJECT, writing a `[[project]]` rule whose prefix is
   verified against the whole fleet before it is saved (§21.12).
3. **The answer channel on a full-screen list — DECIDED for the project list.** One row at the foot,
   three claimants, in this order: a NOTE, then the FILE's warning, then the key line. Each rank has a
   reason — a note answers something the operator just pressed, and a screen that swallows an answer is
   indistinguishable from a key that did nothing; the file's warning stays true until the file is
   edited, where the key line is the same on every screen and can be learned once; and the key line is
   the resting state, because on a screen just opened what to press is the most useful thing to say.
   They SHARE the row through `lines.Fit`, so a lower rank is dropped with a `+N` that says something
   else is waiting rather than being silently replaced — which is the whole difference from picking one
   and discarding the rest. An unhandled key on the list also answers, naming itself. **The PICKER now follows the same
   rule**, and it had the very defect this channel exists to prevent: `foot := note; if foot == "" {
   foot = warn }` REPLACES, so the `hosts.toml` warning — which the comment beside it says "must not
   vanish on the next `j`" — vanished the moment any keystroke produced a note. Its precedence was
   right and its mechanism was not; it shares the row through `lines.Fit` now. What is still open there
   is where `killed 3, failed 1` and the host-status line go, and §21.9.5's per-host stale count.
4. **The shared viewport — DONE for this list.** `windowStart` is the scroller for a list whose rows
   all cost ONE row, which this list's do. The dashboard has its own — `inboxWindow` — because a
   session header there takes a row no pane occupies, so its window is a WALK over per-row costs. It
   was a division by an estimated cost (`inboxCapacity`, one header per two panes, deliberately
   conservative) until §16's group-of-one rule made a header the exception: measured on 45 single-pane
   sessions at 40 rows, the renderer drew 25 and `A` selected 12, and the two-panes-per-session
   control was 12 drawn against 9 selected, so the estimate had been wrong in the same direction
   before that rule existed. Two scrollers are not two answers to one question — they are one answer
   each to uniform rows and to variable rows, and reusing the uniform one by estimating the variable
   cost is what drifted. The list also SAYS how many rows are off screen — a row the operator cannot
   scroll to is a row they cannot act on, which is the same failure as not drawing it. What is still
   open is a select-all over the list, which has no key yet.
5. **§16's other two width bands — CHECKED for EVERY screen, by property rather than by frame.**
   `internal/ui/monotonic_test.go` renders the dashboard, compose, confirm, the naming overlay, the
   project list and the picker at every width from 20 to 220 and asserts two things that need no
   knowledge of any layout: **nothing overflows** the terminal, and **nothing goes backwards** —
   widening it must never show LESS. 402 and 366 renders respectively. The second is the instrument the
   footer's defect earned: a layout that composes a part at one width and renders it at another is
   non-monotonic, which is a CLASS, and reverting the footer fix makes it fail on THREE screens at the
   100-column band boundary — exactly where a per-band branch hides. Frames are still the record of what
   a screen looks like; these are the record of what every screen must never do. Originally
   CHECKED for the project list only, which is how the footer's residue survived. It is asserted at 80, 100, 160 and
   200 columns: nothing overflows, both projects and both attention cells stay readable, and the cells
   line up in ONE column at every band. One defect came out of it — the list drew its own separator
   capped at 80, so a 200-column terminal read as a narrow screen with debris to the right of the rule;
   it uses the shared `separator(width)` now. **The naming overlay is checked at the same four widths**
   — six rows exactly, nothing over the terminal, the key row still on the overlay with a long subject,
   a long typed name AND a long refusal all at once. One defect there too: the `name:` field truncated
   from the RIGHT, hiding the characters being typed, which is the exact failure §21.12 cites as the
   reason inline editing was ruled out — the overlay had reproduced it. It shows the TAIL with a
   leading ellipsis now, so the caret is visible at every width, asserted over five texts and every
   room from 0 to 20. What is still unspecified is any FRAME for either screen beyond 80 columns.
6. **The five naming frames ARE targets now.** They are built from the product in
   `mockup_frames_test.go` — which carries no build tag, so their promises run in `go test ./...`
   rather than only under `-tags mockup` — and each carries `want`/`deny` rather than a caption,
   because a caption is prose and prose cannot go red. The five: the overlay just opened on an unnamed
   row (field EMPTY, `now:` saying whose name is showing and where it comes from), the same row once
   named (field opens with the alias and nothing else), a duplicate refused (the reason INSIDE the
   overlay, the field keeping what was typed), un-naming (field cleared while `now:` still shows the
   name), and the dashboard afterwards (the row reading by the operator's name, with `»`). 24
   assertions, and the floor refuses to pass below the exact count — dropping the scene fails it at
   34 of 58, measured. **A guard came with them:** `docs/` is served publicly, so
   `TestThePublishedMockupNamesNoPrivateHost` reads the operator's own `~/.ssh/config` and fails if
   the document names any of their hosts — rather than carrying a list of private aliases, which
   would be the same leak one directory over. Two exceptions, each with its reason already on record:
   `nuc`, which the document always carried, and `github.com`, a public service. Calibrated by
   planting a real alias, and it names it.

### 21.15 What the review of this design could not check

Stated so a reader does not mistake silence for coverage. Five lenses ran: internal consistency,
citations against the code, numbers against the raw measurements, the absence-of-a-screen lens, and
plan-readiness. **Not run:** a pty lens (no agent drove a real TUI), a concurrency lens on the poll
path this adds reads to, a performance lens, and a test-design lens on how §21.6's invariant would be
written.

### 21.16 The name in the attached session's status line

A name given on the dashboard is also what the SESSION says about itself, so an operator working
inside it sees the name they chose rather than the one the door made — `alias (original)`, the
original kept in parentheses because a session has two names and both are worth having.

The hub publishes ONE FACT and lets tmux compose:

```
set -t $3 @hub_alias 'billing-cicd'
set -t $3 status-left '[#{?@hub_alias,#{@hub_alias} (#{session_name}),#{session_name}}] '
set -t $3 status-left-length 40
```

Every constant there was measured on BOTH versions of this fleet — local 3.7b and nuc 3.2a,
identical — and three of them are not what reading the manual suggests.

- **A user option, not `rename-session`.** Renaming would destroy the original name, which is the
  half the operator asked to keep; it would change what `tmux ls` and their own scripts see; and
  un-naming would need the previous name remembered, which is state that drifts. A WINDOW name is
  worse still: `automatic-rename` fights it, and this repo has already paid for that (the hide key
  had to move off window names, known-issues H1).
- **The conditional is load-bearing twice.** It lets the format be written ONCE per session rather
  than rewritten as names come and go, and it makes un-naming free — unsetting `@hub_alias` falls
  back to `[session]` with no second write. Measured: `[billing-cicd (probe)]` with the option set,
  `[probe]` after `set -u`.
- **`status-left-length` defaults to TEN columns**, and that is what makes it part of the write
  rather than an afterthought: a real attached client with the alias and the format and no room drew
  `[billing-c` and stopped. Three correct options and a broken screen, which is why the e2e case
  asserts the line a client DRAWS and not the options the hub wrote.
- **The alias is inserted literally.** Measured against a real client: an alias containing
  `#{session_name}`, `#H` or `%` draws as itself, so operator text cannot be read as a format and
  needs no escaping. tmux's willingness is not the whole answer, though: `internal/tmux`'s seam
  refuses a literal `%` outside a `-t` value, for a reason that belongs to `display -p` (whose
  argument goes through strftime) and not to an option value. The seam stays strict, because the
  failure it prevents is a silent one, and the PUBLISHER skips a value it would refuse — measured,
  `set -t $1 @hub_alias fine-name ; set -t $2 @hub_alias 50% done` is refused WHOLE, so one name
  with a `%` in it used to leave every other session on that host unnamed. The skipped name gets the
  note, and it still names its row on the dashboard.
- **The operator's own status line is theirs.** The hub writes the drawing only while it is tmux's
  own default, an empty session-level value, or already the hub's format. Otherwise it publishes
  `@hub_alias` alone — that option collides with nothing, and the hub therefore treats the NAME as
  its own: it unsets an `@hub_alias` it finds on a session it has no alias for, which is right for
  the option it owns and would be wrong for any option it shares — and says so, with the line to
  add. tmux's
  default is `[#{session_name}] ` INCLUDING the trailing space, and comparing without it would find
  no match on a default configuration, decide the operator had customised it, and turn the feature
  off silently.
- **Only differences are written, and the poll reads back what the server holds** (three more rows
  in `labelFormats`, which works because option lookup walks pane → window → session → global). So
  the steady state costs no tmux commands, and a restarted hub costs none either: the server already
  holds the answer, which is stronger than a cache that has to be kept in step. `#{n:@hub_alias}`
  frames it like every other label — 12 for `billing-cicd`, 0 when unset.
- **The address is the session ID (`$3`), never the name.** A name is precisely what this feature
  changes, so it cannot also be how the session is addressed; `internal/tmux`'s seam enforces the
  `$` sigil, and teaching it that a scope-less `set` is session-scoped turned up that the same scan
  could not see the CLUSTERED `-pu` a live unstamp path uses. Reading clusters then went one token too
  far: the scan stops at the option NAME now, because everything after it is the option and its value
  and a value may look exactly like a cluster — measured, `set -t p @hub_alias -wip` is rc=0 on 3.7b
  and reads back as `-wip`, while the scan read the `w` and demanded a window target, refusing an
  alias the server accepts. `--` is not the escape either (`too many arguments`), and `-t` is the only
  flag `set` has that takes a value, so skipping its argument is the whole of the parse.
- **A row with no tmux session publishes nothing.** A pane-less conversation has nowhere to put an
  option; its alias lives in `projects.toml` until the door gives it a pane, and the agents poll —
  which is what folds the row into that pane — is one of the two moments the publisher runs. The
  other is the naming gesture itself, so the operator does not wait a tick to see it.

### 21.17 Two narrowings: `*` and `/`

A 45-row fleet is read two ways the sort cannot serve: "only the things I care about" and "the thing
whose name I remember". Both are FILTERS, and both are separate keys.

- **`*`** shows only what is pinned. It REFUSES on a fleet with nothing pinned and names the key that
  pins — turning it on there would empty the screen, and an empty list is indistinguishable from a
  fleet that went away.
- **`/`** opens a keyword field. The list narrows while the keys arrive, `enter` keeps the keyword and
  hands the keys back, `esc` restores whatever was applied when the field opened — cancelling is
  lossless, exactly as it is for a name.
- **`esc`** in browse widens EVERYTHING: the project filter, `*` and the keyword. An operator who
  pressed two keys should not have to remember which one this key undoes.

**Neither is a mode of `v`, and that is the design decision.** `v` answers "how are rows GROUPED"
(§10) while these answer "which rows are SHOWN". One three-position cycle would make "only my
favourites, grouped by project" unreachable, and that combination is the reason to have both.

**They live in `rowsForScreen`, never in `visibleRows`.** That function is the only input to
`sel.Prune`, so a narrowing placed there would silently drop a MARK the moment a row stopped
matching — the rule §21.5 already states for the project filter, now with two more reasons to hold.
A mark the screen cannot show is still counted and still receives the send.

**The keyword matches every name a row has**, not the one on screen: the alias, the tmux session
name, Claude's own name, the window, the host, the project label and the path. An alias HIDES the
derived name, and the moment an operator reaches for search they are usually reaching for the word
they remember rather than the one they typed — so `cicd` still finds a row renamed `прод-выкатка`.
Folded with `strings.ToLower`, which is Unicode-aware, because this fleet names sessions after the
prompt that started them and half of them are Cyrillic.

**THE FILESYSTEM VIEW** — hosts as volumes, directories as directories, sessions as files —
is §23. It reads the narrowings this section defines rather than defining any of its own: the
tree paints the same narrowed set the dashboard does, and `/` can now be opened from it.

**A PINNED ROW LEADS WITH ITS NAME AND CARRIES ITS ADDRESS**, in the shape
`★⚑ needs  billing-cicd @local:~/lab/streams/orbits/billing-iac`. Asked for as "favourites always at
the top, and the record under FAVOURITES special/short — `{status} name @host:path`".

The first half was already true: `favouritesFirst` lifts every pinned row above every other one,
stable inside each attention band, and it does it in the ONE function that produces the painted set so
the renderer, `A`, the cursor and the report cannot disagree about what "visible" means. Nothing to
build.

The second half is a row SHAPE, and the argument for it is that a pinned row answers a different
question. An unpinned row leads with where it lives (`local/api`) because the operator is scanning a
fleet they did not choose; a pinned row is one they chose, so its question is not "what is this" but
"which one" — the name first, then the address. The PATH is on it, and the rule that keeps an id off
an ordinary row (a field that distinguishes nothing is a column nobody can read, §21.17) does not
apply for two measured reasons: there are SIX of these rows and not sixty, and the path is what tells
two favourites of one name in two checkouts apart, which a person cannot resolve from the name.

Three consequences, each measured on the operator's own four favourites (73–100 columns whole, 65–92
with HOME folded, so one of them cannot fit at the 80 §16 commits to):

  - **HOME folds to `~`**, which is what the operator writes and what saves the eight columns that
    decide the fit. HOME arrives as a FRAME VALUE (`Frame.Home`, read once at startup) and is not read
    from the environment inside the renderer: that function's output is diffed byte for byte to prove a
    refactor moved no frame, so an environment read in it would make the published document depend on
    the machine that generated it — the same rule that keeps `time.Now()` out of the generator.
  - **The path YIELDS and the name does not**, and the path is cut from the LEFT with `…` because the
    last segments name the checkout. The first version sized it against a hand-counted eleven columns
    of chrome where the real figure is twelve, so the row came out one column too wide and the OUTER
    truncate cut the path from the right as well — `…reverse-engineeri…`, losing the only part a
    person reads. The caller measures its own prefix now and hands the remainder in; a constant that
    counts somebody else's columns drifts the moment they change.
  - **A pinned row takes NO HEADER**, for the same reason a group of one does not: it carries its own
    address. `headerlessRows`, `extraAbove` and `rowPrefixCost` are all told this, or the viewport
    would count a header the renderer never draws — and a pinned row is excluded from its group's SIZE
    as well, so an unpinned sibling does not lose its own header to it.

A row whose cwd the listing did not report falls back to `host/name` rather than inventing an address.

**`n` OPENS IN THE DIRECTORY THE CURSOR WAS IN.** Asked for as "when creating a new session, create it
straight in the corresponding directory (project) — set the path by default, with the operator able to
override it". The field is pre-filled from the cursor row's own cwd, which travels with the host the
same row chose, so the pair cannot name a directory that does not exist on that machine. The row's cwd
and NOT the project label: a label is only the last path segment (§21.12) and a launch needs a path. A
row whose cwd the listing did not report leaves the field empty rather than inventing a default.

**And `ctrl+u` clears the field, which is what makes the pre-fill an OFFER.** Without it a sixty-column
path can only be removed one backspace at a time, so the convenience becomes something to delete by
hand; the form's own key line names it, because a key that cannot be guessed and cannot be discovered
is not an override. It is the same key the naming overlay and the keyword field use — one gesture for
"empty this field" across the product. Every e2e case that types a directory now presses it first,
which is also what an operator does.

**It matches by SUBSTRING, and falls back to a SUBSEQUENCE — fzf's matcher — only when the substring
pass keeps nothing.** Asked for as "давай добавим поиск по ключевым словам? fzf", and the order is the
answer: measured on the operator's own 64 rows, a bare subsequence floods the list, because almost any
short subsequence exists somewhere in a sentence.

| keyword  | substring | subsequence | |
|----------|-----------|-------------|---|
| `sec`    | 2 | **24** | unusable as a primary matcher |
| `ci`     | 14 | 26 | |
| `test`   | 4 | 17 | |
| `gis`    | 2 | 10 | |
| `opssch` | **0** | 4 | the gesture fzf exists for: typing across a separator |
| `won`    | **0** | 1 | |

Every flooding query above HAS substring hits, so the fallback cannot fire for it — the bad cases are
excluded BY CONSTRUCTION rather than by a match-score threshold nobody could justify. `opssch` finds
`20260701-ops-schdev3-…` and `envoy-ops-svcdev4-…`, which substring search cannot reach at all.

**The order is never touched.** Ranking by match score is what fzf does instead, and it would fight
the one property this screen exists for: the list is ordered by ATTENTION (§21.11), longest-waiting
first inside its band. A loose answer that reordered the fleet would trade "who needs me" for "what I
typed". The real `fzf` binary is not used for the same reason plus two others — it is a full-screen
picker, so it takes the terminal from a screen whose whole model is the fleet staying visible; it
would be the only external dependency besides tmux, ssh and claude; and it cannot see state, so it
would rank by its own score and hand back a name. What fzf is wanted for here is the MATCHER, and the
field, the live narrowing and the count already exist.

**A loose answer SAYS it is loose**, on both surfaces: the open field reads `nothing contains it`
and the kept footer reads `like "opssch" · 2 of 64`. The field's line is a PRIORITY LIST and not a
subtraction, which cost one round to get right — the first draft reserved the whole footer with
`width - Width(foot)` and threw all of it away when the field had under eight columns left, so at the
committed 80 the frame carried neither the admission nor the count; and the first sentence ran 42
columns, which at 60 left the field four (`…sch`). The admission outranks the count in that list,
because a count after a subsequence pass is the misleading half on its own. Without that, `2 of 64`
after a subsequence pass reads as two rows containing the word — the same class as `hiddenStats`
returning a zero, and the reason `filterTally` carries `Loose` as its own field rather than encoding
it in the query string. When NEITHER pass finds anything the screen is empty and is NOT marked loose:
a claim of resemblance with no rows behind it would be a third thing that is not true.

**The footer says which narrowings are on and what they cost** — `in proj-one · ★ only · "cicd" ·
1 of 45` — and the count is always there. **The PROJECT filter is a claimant of that same line**,
which it was not when these two landed: measured on a fleet of 8, `enter` on a project drew 3 rows
under a header reading `tmux-hub  3 sessions` and a footer reading `local up · nuc up`, so the one
surface able to say the screen was narrowed instead reported a number indistinguishable from a
smaller fleet — and `tab`, which walks between projects, gave the operator nothing to tell them which
one they had arrived at. It leads the sentence, because it is the narrowing they arrived through and
the one that changes which question the screen answers. Its label comes off the first SHOWN row
rather than from a walk of the rules, since under this filter every shown row belongs to that project;
when the filter empties the list there is no row to read it from, and that case has the note instead,
which names `esc`. The two predicates that answer "is the list narrowed" — `model.narrowed()` and
`filterTally.on()` — cannot be one function, since one is a view model the renderer holds and the
other a question about the model, so a test asserts they agree over every combination. The project
filter having been added to one and not the other is exactly the defect above. That shape is `hiddenTally`'s, for the reason that one exists: `X`'s first
version answered a changed meaning by returning a ZERO, which reads exactly like a fleet with nothing
hidden. Count always; word differently. The keyword is bounded through `shortSubject`, because a
session name here runs to 88 columns and an unbounded interpolation is this repo's oldest layout
defect. When a narrowing empties the list, the note says WHICH one did it and that `esc` widens.

**The field is a footer CLAIMANT rather than a row of its own.** A new row would have to come out of
the body, and the body's height is shared by the renderer and `InboxViewport` through one function —
the last thing that took a row without telling both made `A` select panes seven rows below the fold.
A claimant costs no arithmetic and degrades through the same `lines.Fit` as everything else there.

**Every key inside the field is text**, which is why it is a MODE: `j` types a `j`. The e2e case sends
each keystroke on its own, because `send-keys -l "other"` arrives as ONE key message whose `String()`
is the whole word — a test that types a word cannot tell a field that takes text from one that took
`o` as a command.

### 21.22 Three defects the review found in the fix that had just landed

An adversarially verified review of §21.21's own work: 23 findings, 7 survived refutation, and three of
those were defects in code written an hour earlier. Two shared one root cause and it was a comment that
lied.

- **A CURSOR WRITE MUST CARRY ITS HINT.** The launch pointed the cursor at the pane it made with
  `rowCursor{key: …}` and left the hint at ZERO. `cursorIndex` falls back to the hint when the key names
  no row on screen, so an operator standing on row five pressed enter and their place jumped to the TOP
  — and with a project filter on, `a` then acted on `rows[0]` instead of the pane they had just made,
  which is the dangerous direction: they attach somewhere else and nothing says so. Both reproduced. The
  comment at the site claimed the fallback kept "its remembered position", which the code made false —
  and a false comment is worse than none, because it stops the next reader checking. `cursorTo` states
  it is the ONLY writer of the cursor for exactly this reason; the launch was the second writer.
- **A LAUNCH A NARROWING HIDES SAYS SO.** With the hint fixed the cursor still names a row that is not
  on the screen when the launch lands outside the active filter, so the note now reads `launched: %7 —
  the filter hides it, esc shows the whole fleet` rather than pointing at a remedy (`a goes there`) that
  would act on something else.
- **THE WRITE AND READ BOUNDARIES REFUSE THE SAME CHARACTERS, THROUGH ONE PREDICATE.** `Check` refused a
  line break, a carriage return, a tab and every control character; `firstLine` cut only at a line break
  or a carriage return. So a tab in a hand-edited `projects.toml` — or one written by a build older than
  the refusal — reached the row and expanded to a variable width, misaligning the column the eye runs
  down. `refusedInAName` is the one predicate both ends read now, which is the same lesson as every other
  rule in this document that used to have two copies.

**And one test that could not fail.** `TestJourneyFavouriteNarrowShowsOnlyPinnedRows` asserted that the
pinned row was still on screen after `*` — true of a filter that filters NOTHING, because the fleet was
one pinned row. Deleting the narrowing from `rowsForScreen` left it green. It now creates an UNPINNED row
and asserts its ABSENCE, which is the half that can fail, and the mutant dies naming the row it kept.


### 21.21 Journeys, and the three things a flow owed the operator

254 cases tested KEYS and SURFACES; the longest pressed five keys inside one screen, and not one
created a session and then attached to it. So the flows an operator actually performs — `n` to a
running session, the door to a folded row, a name to the status line, a project to a broadcast — had no
coverage at the level where usability lives. Eight journeys close that, each asserting two things per
step: that the step produced the state, and that the SCREEN said what happened and what to do next.

Three defects came out of walking them, and a storyboard of the flagship journey — a frame after every
keystroke at 80x24 and 100x30 — is what found the first.

- **THE CURSOR GOES TO WHAT WAS JUST CREATED.** Measured at 80x24 on a 34-session fleet: after enter
  the hub said `launched: %1` and the new row was not on the screen AT ALL for one poll, then arrived
  twelve rows below a cursor that had not moved. A list ordered by attention puts a fresh session low
  precisely because it is not asking for anything, so the flow was "create it, then hunt for it". The
  launch now points the cursor at the pane it made — set BEFORE the row exists, because the cursor is
  keyed on a row's identity and catches the row the moment the poll brings it in — and the note names
  the remedy instead of only a pane id: `launched: %1 — a goes there`.
- **THE SEARCH FIELD SAYS WHAT THE KEYWORD HAS DONE.** While the field has focus the footer belongs to
  it, and it showed the word being typed and nothing about its effect, so an operator could not tell a
  keyword that had narrowed the fleet to one row from one that matched nothing until they committed it.
  The list narrows LIVE, so the number is already true on the screen above: `search: рендеринг▏  ·
  1 of 4  ·  enter: keep · esc: cancel`. The count goes before the keys because it answers the
  operator's question while the keys are a reminder, and the fitting drops from the tail.
- **THE NAMING OVERLAY SAYS `(yours)`.** It already explained a name the operator did NOT choose —
  `(not yours — Claude's own name)` — and said nothing at all for one they did, so "this is mine" had
  to be read from the ABSENCE of the other clause. The row's `»` marker carries the same fact, and one
  question should not need two conventions.

**What a journey taught about the HARNESS, which is worth more than any of the three.** The vantage is
PER STEP: `a` on a row of another server opens a window in the hub's own session and tmux makes it
ACTIVE, so `capture-pane -t ui` follows the ATTACH while `-t ui:0` stays on the hub. Four of the five
first-run failures were that one fact — a journey waiting twenty-five seconds for a row to fold while
reading the woken session, and a `walkTo` that typed sixty `j` presses into a Claude REPL. The rule the
journeys now carry in comments: name the WINDOW for the hub's own screen, name the SESSION for what the
operator is looking at, and say which one a step means.

**Observed and NOT changed: a launched row is renamed by Claude a few seconds later.** The hub names
the tmux session after the directory (`billing-iac`), and the row reads by that until Claude reports its
own name for the conversation — measured, `20260814--tmux-hub-development-calm-whale` — after which the
row reads by that instead. It looks like drift and it is the design: §17 makes Claude's name the thing
that identifies an agent to a person, because on this fleet it is usually the prompt. Showing both would
spend the width the name already yields at 80 columns. The remedy is the one the product already has and
the operator asked for first: `N` pins a name that nothing changes.


### 21.20 Two things a name and a door owed the frame

Both came out of an adversarially verified review of this session's own work: eighteen findings, seven
survived refutation, and of those two were product defects. A third — that a `~ ` with a trailing space
would land a session in HOME at rc=0 — was REFUTED by measurement, and how is worth recording: the
reviewer ran `expandTilde` and tmux separately and missed `Spec.Validate` sitting between them, which
refuses the path with `cwd must be absolute, got "~ "` before tmux ever sees it. Two measurements of
two components are not a measurement of the path through them.

- **A NAME IS A ONE-LINE OBJECT, enforced on the way in and defended on the way out.** Measured:
  `Aliases.Check` accepted a name holding a newline, `DisplayName` returned it whole, and the dashboard
  drew **26 lines on a 24-row terminal** — the name's tail became a row of its own. The frame's whole
  invariant is one screen row per fleet row, and a name is the one field on that row that comes from
  OUTSIDE the program: an operator can paste one, and `projects.toml` is a file they may edit by hand.
  So `Check` refuses a line break, a carriage return or a tab with a sentence naming the character's
  position, and `DisplayName` cuts at the first line and marks the loss — because a file written by an
  older build already holds what the refusal now prevents. A `\r` is the worse half: it returns the
  cursor to column 0 and the row overwrites itself, which is the defect the footer's host reasons
  already paid for.
- **A background row with NO SHORT ID cannot be woken, and the door says so.** Measured on such a row:
  `wakePayload("")` builds `claude attach ''` — the verb with an empty argument — and `wakeName`
  returns the row's plain name, which is the SAME name for every row of that name, so two of them share
  one door and the second `a` walks the operator into the first one's session. The id is the only field
  guaranteed unique among background rows, so without it there is nothing to make the name unique with,
  and find-or-create would hand over somebody else's session. `agents.Session.ID` is documented absent
  on some versions of the vendor's listing, so this is a row the fleet can produce. The refusal names
  the row and the remedy (`claude agents`) inside the part that fits at 80 columns.


### 21.19 Four things the screen was not saying

Read from 51 captures of the real binary at 80×24, 120×40 and 200×50, on a fleet of 37 sessions whose
names are the prompts that started them and run to 88 columns. Every item is a frame, not an opinion,
and each now has a scene in the generated mockup — because the four defects were INVISIBLE in that
document: none of its fifty scenes carried a long name, a project view, or a pane with nothing
captured, so the album that found them is what the mockup lacked.

- **A cut line says that it was cut.** Three surfaces stopped mid-word at the committed size: a list
  row (`…-troubleshooting ⑂ дав`), a tile's header (`…troubleshooti┐`), and a confirmation's own
  warning (`• this pane does not accept pasted text — it will read the prompt as keypresse`). The
  third is the expensive one: that sentence is the operator's only notice that a send may be read as
  keystrokes, and it lost its object while reading as complete. `lines.TruncateMarked` spends the last
  CELL on `…` and never claims a cut that did not happen; `lines.Truncate` keeps its contract for the
  one caller a marker would be wrong for — a tile's BODY is a copy of another program's screen, and a
  character added there puts words in its mouth.
- **A pane with nothing captured gets CONTEXT, not an empty box.** Three of the 24 rows §16 commits to
  went to a header, one blank line and a border. The tile now falls back to path, window, size and
  what is running — the fields a row does not carry and an operator chooses between rows on. It is a
  FALLBACK: when there IS a capture it stays, because the tile is how `Do you want to proceed? ❯ 1.
  Yes` is read without leaving the hub, and the guard test asserts both halves.
- **`v` changes the screen at 80 columns.** It used to change one word in the header and nothing else:
  the inline shape draws no group headers, and an agent row has no `where` column — deliberately,
  since the name needs every column — so on a fleet that is 40/43 agent rows the project view showed
  nothing, and rows of one project were not even adjacent. The project view now takes the GROUPED
  shape at every width, which answers the question the view was opened to ask, and fits 80 columns
  because the band moved to the bottom and the list has the whole width.
- **The project list's count reads as a sentence.** A project with nothing to report drew
  `album.QUTp1q      of 5` — half a phrase, while the row above read `⚑ 3  of 9` and parsed at a
  glance. The word `none` is the missing subject and is true of exactly the three facts that cell
  reports; it yields to width before `of N` does, because the count is the part the header is checked
  against. It is a WORD and not a glyph, which is the rule that cell already had: `·` is Works and `?`
  is Unknown, so a glyph filler would assert a state.

**What was NOT changed, and why.** `MaxTileWidth` stays at 72 even though a 200-column terminal leaves
128 columns unused and cuts the tile's header where the list row above shows the name in full. The cap
carries a measurement of its own — a single full-width tile at 160 columns held 30-column content in a
130-column box — and the name it cuts is on the screen one row up. So the honest fix there was the
marker, not the width.


### 21.18 One window per attach, and the operator's own session tree reads like the hub

The operator's report, in their words: attaching — "in particular, to one session several times" —
creates a connection visible in `C-b w`, and the names on that screen differ from the ones on the
dashboard. Both are true, and the first is worse than it looks.

**Measured on their own server before this section existed.** Session `0` held windows 1–5, all five
named `nuc`, all five an ssh attach to ONE remote session, which reported `attached=5` with clients at
135x48, 130x43, 135x48, 153x43 and 153x43. `window-size` is `latest` on both ends, so **each new
attach RESIZED the shared session**: it stood at 153x42 while two of those clients were smaller, which
leaves the older windows drawing a session wider than their own terminal. So a second attach does not
merely add clutter — it degrades the first one.

- **`a` is idempotent on the window path.** Before opening a window the hub asks the server whether one
  is already showing that target, and goes there instead. The note says which happened, because "you
  are already there" and "nothing happened" look identical on a screen the hub does not draw.
- **THE KEY IS THE WINDOW'S NAME**, which is also what `C-b w` shows, and that is the design rather
  than a shortcut. A marker option would have to be written AFTER `new-window`, and §20 already
  measured why nothing may be: `new-window` is what starts the payload, so a second command loses a
  race it can only win on time (`false` survived 6 of 12 trials). The name is set by the create
  itself, so no window ever exists unmarked. Measured on BOTH fleet versions, 3.7b and 3.2a:
  `new-window -n <name>` and `new-session -n <name>` turn `automatic-rename` off by themselves, so
  the name survives — an un-named sibling was renamed to `sleep` and `bash` within three seconds,
  which is exactly why every door session read `sh` in the session tree.
- **The window is named `<host>/<the name the dashboard shows>`** — the shape the hub's own narrow band
  already uses for a row. The host leads because this path only ever serves a server that is not the
  hub's own, so which machine is the first thing that distinguishes one of these windows from another.
- **The DOOR's window carries the row's name too, and its SESSION keeps the name it had.** That
  session name is the door's dedup key: `new-session` answering `duplicate session:` is what makes a
  second `a` find rather than create (§22.3), so renaming it would make a second `a` create a second
  session. The window is therefore where a name becomes visible outside the hub, and it is the line
  `C-b w` prints under the session.
- **A window the hub did not make is never renamed.** The gate is the pane's own start command
  (`registry.AttachedSessionID`), derived rather than remembered, so it still answers after a restart
  — the argument §22.3 makes for the join it already serves. A window the operator renamed is one they
  have taken over.
- **The republish rides the alias poll**, differences only, so a fleet whose names are in step costs no
  tmux commands. That is also what repairs the sessions an older hub left reading `sh`, and it sets
  `automatic-rename off` beside the rename, because a window made before `-n` was passed is still
  being renamed by tmux.
- **It asks ONCE for a given name, and then concedes the window — because the hub is not the only
  program that writes one.** Reported as a status line that shimmers, and measured on the operator's
  own server: three windows alternating several times a second between the alias they had typed and the
  raw Claude session name (`frontend-troubleshooting` ↔ `20260810--troubleshooting`), with
  `automatic-rename` OFF, so tmux was not the other writer — Claude Code names the window after its own
  session. Differences-only is not the fix for that, it is the ENGINE: two writers plus "write whenever
  it differs" is a loop by construction, and it runs at poll rate. So the hub remembers the name it last
  asked a window to take and does not ask twice; a newly typed alias is a new name and gets one fresh
  attempt. The operator still reads the alias where they asked for it, on the dashboard, and the loser of
  that race is the `C-b w` label rather than anything actionable.
- **A name is bounded at 80 columns** — the width §16 commits to, so a name that fits the dashboard
  fits here — and in COLUMNS, since a CJK name is two per rune. Not the footer's 20: a footer shares
  its line with other claimants and a window name competes with nothing. Measured against the real
  fleet, the longest door session there is 59 columns, so a tighter bound would stop the window name
  from matching the SESSION name printed directly above it.
- **A LEADING `-` is removed, and that rule is tmux's flag parser rather than the hub's seam.**
  Measured: `rename-window -t @1 -wip` answers rc=1 `unknown flag -w` and leaves the name alone, and
  `--` is not an escape (`invalid flag --`), because the name is a POSITIONAL argument there. A session
  name does not have the problem — `new-session -s -wip` is rc=0 — so the rule lives with the window
  namer and not in launch's sanitiser. `RenameWindows` skips such a name as well, since every rename on
  a host travels in one invocation and the worst element would otherwise cost the others their names.

**What it costs, stated rather than hidden.** An alias typed AFTER a window was opened, or a window the
operator renamed themselves, makes the next `a` open a second one: the name no longer matches, so the
lookup finds nothing. The republish keeps door windows in step, and for an attach window the old one is
still on screen under the name it was given, which is visible and explicable. What is NOT done: the
operator's `C-b w` binding is left alone. Rebinding it to `choose-tree -F …` would put a format nobody
maintains into their key table, against tmux's own default.


## 22. Reaching a session that has no pane

Most of what the hub looks after has no pane, and every key in §7, §19 and §20 is written about panes.
This section gives a pane-less Claude session a terminal by exec'ing a verb the CLI already ships,
retires the repairs that look like the same idea, and says what it may not promise.

**Three standing rules before the first figure.** (1) **Version span:** everything below was measured on
**claude 2.1.233** (local) and **2.1.224** (`nuc`), the oldest here — a 9-patch spread, in §3's idiom for
tmux 3.7b/3.2a — and a figure from one host or version says so. §17's table holds four machines but only
**three carry claude at all** — the fourth returned nothing for `claude --version` and has no claude
installed (design.md:187, :1922). **All three were probed on 2026-08-16**: the third runs claude 2.1.226 and
tmux 3.4, so the claude spread is 2.1.224–2.1.233 over three versions and the tmux spread is **three**
versions, not two. Probing it changed four figures and refuted one claim, each recorded where it lives
(§22.6, §22.8, §22.10). "Both versions" is therefore a statement about the two hosts a given measurement
touched, never about the fleet, and the phrase is now used only where the third host was not asked.

**And `claude --version` is not the version that will serve the attach.** Measured 2026-08-16 from the live
process table: the CLI reports **2.1.233** while a running `bg-pty-host` is executing
`~/.local/share/claude/versions/**2.1.232**`, with 2.1.229, 2.1.231, 2.1.232 and 2.1.233 all installed and
four `bg-pty-host`/`bg-spare` pairs alive. Every attach in the journal came `via: spare` (3 of 3), so an
attach CLAIMS a pre-warmed spare rather than starting a process — which is why it is warm and sub-second, and
why the version behind a background session can be older than the one you just measured. An earlier revision
of this document asserted that §22's figures were taken on 2.1.232 and a correction replaced that on the
grounds that 2.1.232 is "on neither host". The span above is right and the grounds were **wrong**: the
version is installed and running. What is true is narrower and more useful — a per-version claim about the
CLI does not transfer to a spare, so §22.3's door is measured against whatever version the daemon warmed.
(2) **Protocol provenance, stated once here and nowhere else:** the daemon facts in §22.2 (socket, key,
operation count) and every `reply`, `subscribe`, `has`, `await-ack` and `permission-response` observation
in §22.4 were taken on the **LOCAL machine only**, on scratch sessions in an isolated
`CLAUDE_CONFIG_DIR`, and **none was re-measured on 2.1.224** — §22.2's at 2.1.233-class, §22.4's at one
CLI version and not re-taken on 2026-08-15. Acceptable because these facts are the *reason* for a
refusal, never an interface the hub calls. (3) **No census number here is a constant:** the job count
moved 25 → 26 while the probes ran, local interactive rows moved 6 → 5 inside one session, and `--all`
moved one machine 13 → 31 rows inside one command. Every number carries its date and its call.

### 22.1 Why this is not "resume it" and not "restart it"

**Three mechanisms are retired, each by a measurement rather than by taste.**

| retired | why |
|---|---|
| `claude --resume <uuid>` as a way in | **no engine-side refusal**: a second resume of a session whose process is alive returned `is_error=false`, kept the same id, and appended into the transcript the LIVE process held open on **5 descriptors** — a silent two-writer corruption. A second reason, categorical rather than statistical: the id `--resume` takes is `resumeSessionId`, a field SEPARATE from `sessionId` and non-empty in **32 of 32** `state.json` files (§22.5), so a hub passing the row's own id would be resuming the wrong thing anyway |
| killing the holder and respawning | it ends a process in somebody's terminal and abandons the turn in flight, and buys nothing `attach` does not. `R` stays pane-only for the mirror reason: `respawn-pane -k` kills the holder in one invocation (`internal/ui/lifecycle.go:15-23`), and `tmux.PaneAlive` stays unwired for a future path that must refuse while a pane holds the session |
| `--teleport` | **not a terminal mechanism at all**: a local↔cloud handoff over `GET /v1/code/sessions/<id>/teleport-events`, state on a git branch `claude/teleport-<last8>`, copy-not-move, nothing in its path naming a pid, tty, socket or pane. That argument is fleet-independent, so the Bedrock gate that also blocks it here — `claude -p --teleport <uuid>` exits 1 with `Cloud sessions aren't available with Amazon Bedrock`, one local probe, not re-taken — is deliberately **not** the reason: a gate can lift without the mechanism becoming a door |

**Whether the door fails open the same way is OPEN, and saying so corrects an earlier revision.**
`--resume` was measured to fail open against a live holder; nobody asked the same of `attach`. Three routes
reach two concurrent clients on one id — the operator's own terminal, two hub instances on two machines as
one uid (the single-instance lock is per machine), and the hub's own second press, the only one §22.3
closes. **Both halves are UNVERIFIED, viewing as much as typing** (§22.10). An earlier revision reported
the viewing half measured safe on two concurrent attaches and five attach/detach cycles; no probe in this
evidence base took either, so the sentence is withdrawn rather than softened and the §22.10 row it had
closed is back. `attach` is claimed neither exclusive nor safe until it is taken.

**The population, dated and scoped to the call that produced it.** Measured 2026-08-16 in ONE snapshot
across all three claude-bearing hosts: **65 rows, 57 of them `background`** (local 26 of which 25; `nuc` 14
of which 7; §17's third machine 25 of which 25) — **87%**, against the 48% the published "10 of 21 rows"
asserts. This is the census's third revision and each was right for its call and scope: 21 rows / 10
background came from bare `--json` on one host, 45 / 32 from `--all` on two hosts at different moments, and
the figure above from `--all` on three hosts at one moment. The third host had never been probed until
2026-08-16 and is the strongest population argument in the document: bare returns **4** rows there against
`--all`'s **25**, so the default call hides **84%** of it. That figure came
from bare `claude agents --json`, which returned 13 locally and 11 on `nuc` in the same round, and §22.6
forbids that call: the motivating census was drawn from the population §22 declares invalid, understating
the case by 17 rows on one machine. Its companion half, "and every waiting row is among them", is
**UNVERIFIED** at the new denominator and needs a different probe, not a re-run (§22.10): one keyed on rows
the listing calls `blocked` cannot see the counterexample class, because a session on a pending Write
permission prompt polled `working/active` for **136 s** (§22.4).

**What §22 adds over typing the verb by hand** — for a local row the feature is one command — is knowing
**which** of 45 sessions wants attention and carrying the **remote leg** (id, host, far tmux server, ssh
master). So it must make doing it by hand possible: the tile prints `p.SessionID`, the full UUID
(`internal/ui/render.go:273-274`), not the argument `claude attach` takes, so §22.5 puts the short id on
screen.

**Today the hub refuses, in these words.** `AttachCmd` returns `"%q is a Claude session, not a tmux pane
— there is nothing to attach to until it runs in one"` (`internal/ui/attach.go:37-38`), prefixed `cannot
attach: ` (`internal/ui/possession.go:43`); that is what §22.3 replaces. Two neighbours are wrong in the
same area: `R` says `this pane has no known session — only hub-launched agents can be restarted`
(`internal/ui/lifecycle.go:44`), a *derived* refusal since the keeper has no entry and `lifecycle.go`
contains **no occurrence of `KindAgent`**; and marking an agent row is accepted, ending at `the hub holds
no identity token for this pane` (`internal/broadcast/send.go:84`, again `:345`).

### 22.2 The doors the CLI already provides, and the one the hub refuses to use

**Three verbs exist and none is documented.** Measured on **all three** claude-bearing hosts — 2.1.233,
2.1.226 and 2.1.224 — usage text byte-identical on each:

| verb | its own help text, verbatim |
|---|---|
| `claude attach <id>` | `Open the background session in this terminal. ← returns to agent view, Ctrl+Z drops back to your shell. The session keeps running either way.` |
| `claude logs <id>` | `Print the background session's recent terminal output.` |
| `claude stop <id>` | `Stop a background session. Its conversation is kept; resume it later with `claude attach <id>`.` |

The text is load-bearing three times: it names the two ways out of the pane §22.3 creates, it says the
session survives the terminal, and the vendor's own `stop` text names **attach** as the resume path — the
independent reason §22.1's retirement of `--resume` is not a preference. The verbs are absent from the
command INDEX and self-describing when named; grepping the top-level help for "attach" gets one hit
belonging to an unrelated flag (`--cloud … or attach to an existing one by session ID`), a *wrong* answer
rather than none. That adds a rung to §17's stability ladder between "`--json` is documented" and
"`state.json` is internal": an undocumented verb whose usage text is unchanged over nine patch releases is
better evidence of stability than "undocumented" suggests.

**Underneath is somebody else's daemon**: `control.sock` at
`/tmp/cc-daemon-$uid/sha256(realpath($CLAUDE_CONFIG_DIR))[:8]/`, NDJSON, protocol 1, **18 operations**;
the shared secret at `$CLAUDE_CONFIG_DIR/daemon/control.key`, mode **0600**, compared with
**`timingSafeEqual`** — and opening the socket directly without it answers **`EAUTH`**, which is what makes
the key the gate. **The ruling: the hub execs the verb and never speaks the protocol.** `attach` needs no
secret because the CLI reads the caller's own key directory; the protocol would mean reading and holding a
per-host secret **over ssh** for every host, to gain nothing the verb does not do. That decides three other
things: `i` is deferred (§22.4), §11's killed class "text landed in the wrong pane" survives, and §20's ban
on a second terminal emulator is untouched.

**Closure, and it is not a list of spellings.** Banning `control.sock`, `control.key` and `cc-daemon` as
literals is routed around by construction — the path is *computed* from `filepath.Join` and
`$CLAUDE_CONFIG_DIR` — and `internal/tmux/guard_test.go`'s calibration asserts an EXACT violation count, so
adding bans without planting them turns it red for an unrelated reason. The invariant with teeth is a ban
on a **unix-socket dial outside the ssh and tmux doors**, anchored on the quoted form (the `link-window`
ban is quote-anchored because the bare word fired on the prose explaining it), with a **floor** on files
scanned and a planted violation in the calibration.

**`~/.claude` is a default, not an address.** Socket, key and roster are keyed on `CLAUDE_CONFIG_DIR`, so
one host can carry several disjoint populations and a hub hard-coding `~/.claude` reports the wrong set
with no error anywhere. Two consequences. **The door's environment must be the LISTING's environment**: the
id comes from `ssh <host> claude agents --json` (a non-interactive shell,
`internal/agents/agents.go:155-180`) while the verb runs from a far tmux server's `$SHELL -c`, so
`CLAUDE_CONFIG_DIR` is carried explicitly into the payload when the listing was taken under one, or the
door queries a different daemon and answers `No job matching` for an id the hub listed a second earlier.
And **where the per-job state directory lands under a non-default config dir is not measured** — it was
found at `~/.claude/jobs/<short id>/state.json`, keyed on the listing's own `id`, under the default dir
only. (`~/.claude/agents/` is the agent-DEFINITIONS directory, 6 `.md` files, not a session store.)
**Closing a pane costs the Claude session nothing** — the verb's help says it, measured both ways round —
which licenses §20's "Nothing is kept" for the session §22.3 creates and is the fact `K` and §19 turn on.

### 22.3 The door: `a` on a background row makes the pane the row is missing

**The door is `claude attach <id>`, run inside a pane the hub creates on THAT ROW's host** — no new key, no
new transport, no new possession mechanism, no fork and no kill; the created pane re-enters §20's table.

**It wakes a session whose worker is dead, and that is why one call is enough.** After SIGKILL to both
worker and pty-host the same call printed `Waking session <id>…`, claimed a pre-warmed spare, replayed the
transcript and gave a live terminal. Across four row shapes — reaped mid-work, silently reaped while
blocked, live, settled — one call gave a live terminal in **0.33–0.95 s** including a cold daemon start.
Three limits: **local only**, so `a` on a `nuc` row adds an ssh leg and a `new-session`, UNVERIFIED; the
WAKE was one probe on 2.1.233, **not re-taken on 2.1.224**, where 7 of the 32 background rows live; and
every timing was taken with the verb **in an interactive terminal**, while this design runs it as the
trailing payload of `new-session -d`, in a pty no client has joined — whether a full-screen TUI verb
starts, draws or refuses there is UNVERIFIED. Starting the daemon is a side effect of a **keypress, never
of observing**; whether the 20 s listing or `claude logs` also cold-starts one is UNVERIFIED, and if the
listing does, the hub starts a daemon on every host by watching, which §5 forbids for tmux servers.

**The gate is `Kind == KindAgent` AND the row's own kind word `background`, never a non-empty id.** Three
measured reasons, the third being why two earlier revisions specified a gate that could never fire.
(1) `agents.Parse` back-fills `ID` from `SessionID[:8]` (`internal/agents/agents.go:111-112`), so an
interactive row carries a plausible short id with no daemon behind it. (2) The honest form is a
**mechanism**: `kind=background` ⟺ `id` present, closed both ways on both hosts (§22.8). (3) "background"
is not in `Kind`, which is provenance only (`"pane"`/`"agent"`, `internal/registry/registry.go:34-35`);
Claude's kind is assigned to `Command` (`internal/registry/registry.go`, `p.Command = s.Kind`), making `Command` the **second** dual-purpose
field beside `SessionID` — so the branch belongs in `AttachCmd`, whose comment documents the first.

**Two fields the row lacks, both fields rather than parses.** `UpdateAgents` (`internal/registry/registry.go`, `UpdateAgents`) copies
Kind, Host, PaneID, Session, Command, Path, SessionID, ClassifiedState, StateSince, Activity, SeenAt,
Stale. Not `agents.Session.ID`, which survives only inside the row key `agent:<id>@<8 hex sha256(cwd)>`
(`internal/registry/registry.go`, `agentRowID`) — so `registry.Pane` gains `AgentID`. And not the listing's raw state WORD:
`p.ClassifiedState = state.FromWord(s.Attention())` (`internal/registry/registry.go`, `state.FromWord(s.Attention())`) and `Attention()` REWRITES it
(`internal/agents/agents.go:44-67`: blocked→needs, working/running/busy→works, done/completed→done,
idle→idle, failed/error→error, else `""`→Unknown), so a listing `failed` is indistinguishable from a listing `error`
and nothing downstream can gate on `failed`. So `registry.Pane` also gains `AgentWord`, unfolded. Each
carries a comment naming **both** producers and one test that goes *through* the producer, never a fixture.

**Possession takes over on the pane the create RETURNS, which is why this is one call.** The `new-session`
carries `-P -F '#{session_id}|#{window_id}|#{pane_id}|#{pid}|#{start_time}'`, read back in the same
invocation, because an empty epoch on a LOCAL host takes the full-screen attach
(`internal/ui/possession.go:89-95`) — so the commonest row here, a background agent on this machine, would
take the terminal and block the hub. What is unchanged from §20 is the *table*, not the row.

**The created session is named `<agent name>-<short id>`.** The name answers "where am I"
(`[refactor-parser-3f2a1b09]` does, `[3f2a1b09]` does not) and the short id is the only field guaranteed
unique among background rows, since two rows on this fleet share a name and a cwd (`internal/registry/registry.go`, `agentRowID`'s own comment). The
name goes through `launch.SessionNameFrom`, which replaces `.`, `:` and `%` — the first two because
tmux's TARGET syntax uses them (a session called `my.app` exists and `has-session -t my.app` answers
`can't find pane: app`), the third because `internal/tmux`'s seam refuses a literal `%` anywhere but a
`-t` value, so the `new-session` would never run at all. An agent here is named after the PROMPT that
started it, so `make it 50% faster` was the door refusing to open with no remedy the operator could act
on. The
name is a pure function of the row, so a second `a` makes no second door: `new-session` with a duplicate
`-s` is rc=1 `duplicate session: <name>`, **measured on both tmux versions**. But **rc=1 is not evidence of
a duplicate** — the same call answers rc=1 when the far tmux is missing, when the ssh master died between
poll and keypress, and when the socket path is wrong. So the hub reads tmux's **own words**: only
`duplicate session: <name>` means find-or-create; any other rc=1 is reported as itself. Find-or-create
keeps **no state anywhere**, the only version that keeps §20's "Nothing is kept" literally true; its cost
is that a pre-existing session of that name is entered as if it were the door, named here rather than
detected. Beyond that it is an ordinary row: `K` kills it, and the payload's exit destroys pane, then
window, then session under `remain-on-exit off`. Note the asymmetry — the created session is killable and
the agent row it was made for is not (§22.9.3).

**The payload is §20's `sh -c` wrapper, and one flag on it is UNVERIFIED.** One measured `claude` failure
exits 1 with a single stderr line, so the wrapper holds a shell open and the message stays on screen
instead of evaporating with the pane; another wrote nothing to stdout or stderr, so the reason existed only
in a debug file. §22.6 measures the flag per verb: `logs` accepts `--debug-file`, `agents` rejects it, and
**whether `attach` accepts it was never measured** — the payload rests on that, so it is measured before the
flag is written. Where carried, the path is fixed rather than left to a caller:
`${TMPDIR:-/tmp}/tmux-hub/attach-<short id>.log` on the **target** host, one per row, truncated on each
create so its contents belong to the attempt the operator is looking at, and named verbatim in the
wrapper's failure sentence. (A design choice; nothing measured it.)

**An unexpected rc=1 is a SHAPE, not a bug, and the hub does not retry.** One shape could not be reproduced
— `state == "failed"` with neither reap field, where the CLI refuses and exits 1 — so the design assumes
such refusals exist: report the one stderr line as the note, leave the shell holding it, do not re-issue.
The **bogus**-id shape is measured and identical for all three verbs on both versions: an id the daemon does
not hold answers **rc=1**, and with TWO different sentences depending on whether the daemon ever had the
id: a short id it has forgotten gives `Couldn't read logs for <id> — job not found — it may have already
exited`, and an id it never had — including a full session uuid, which the verbs do not resolve — gives
`No job matching '<id>'. Run 'claude agents' to list running sessions.`. An earlier revision of this
section recorded the first wording as gone, on the strength of probing a BOGUS id only; both exist,
passed through **verbatim** since an outer reader of an error string can only lose the remedy it carries. A
**STOPPED** id is a different shape and is UNVERIFIED (§22.10): the id under the cursor can be up to **20 s**
old (`AgentInterval`), so the hub will offer ids the roster has since dropped, and what `claude attach` does
with one was never measured.

| the row | what `a` says |
|---|---|
| an agent row whose kind is not `background` | §22.8 owns it — the sentence quotes the kind the row carries and names the `/background` remedy |
| a `background` row whose host's tmux tunnel is down | `<host>'s tunnel is down; "<name>" is still running there — press p to re-probe` — branched on `Target().Remote()` before any dial, because `MarkHostStale` deliberately leaves agent rows live, so the pane path's socket message would describe a mechanism this row does not have |
| the create landed and the jump did not | `made <name> on <host> for it, but could not go there` — never `cannot go there`, which would hide a session the operator now owns and cannot find |

**Which `claude` the door runs is decided by the HUB's own environment, and that is measured rather
than assumed.** A pane tmux creates inherits the environment of the CLIENT that asked for it — the hub
— and not the server's:

```
env -i PATH=/SERVER-PATH … tmux -S s new-session -d -s watched cat     # the server
env PATH=/CLIENT-PATH      tmux -S s new-session -d "sh -c 'echo $PATH'"
→ PANE_PATH=/CLIENT-PATH:/usr/bin:/bin
```

`tmux set-environment -g PATH …` after the fact does NOT reach it, and `-e PATH=…` on `new-session` did
not override it on 3.7b either. Two consequences. The payload needs `sh`, so a hub started with a PATH
that has no `/bin` produces a pane that exits **127 with an empty screen** — and since the door
deliberately leaves `remain-on-exit` off, that pane takes its window and session with it inside 200 ms,
so every observation afterwards says nothing was ever created. And the pane's own shell runs next: a
login shell's rc files can rewrite the PATH the hub handed it, which is how a test with a fake `claude`
first in the hub's PATH still ran the operator's real one.

**Three ways out of that pane, and the hub owns none.** The verb's help names `←` (agent view) and `Ctrl+Z`
(shell); both stay *inside* the pane, neither is a tmux key, so `C-b C-b d` and `C-b L` are unaffected. Say
what `Ctrl+Z` does here: everywhere else it means "suspend and give me my shell back", and here it gives a
shell inside a session the hub created and named. One process-walk consequence lands in the same commit:
`internal/proc`'s exclusion list is a blacklist of five daemon roles (`internal/proc/proc.go:39-44`), so
`attach`, `logs` and `stop` all read as interactive agents — right for `attach`, wrong for the other two,
which print and exit, so a pane running `claude logs <id>` would be stamped and would accept a broadcast
that goes nowhere. `logs` and `stop` join `daemonRoles` in whichever commit first runs them.

**Whether pressing `a` answers the row or duplicates it is UNVERIFIED.** A listing row whose `sessionId`
matches a polled pane's `ClaudeSession` updates *that pane* and never becomes an agent row
(`internal/registry/registry.go`, the absorb branch in `UpdateAgents`), and `Pane.State()` then prefers the agent fact for ten minutes — so if an attach
client's children carry `CLAUDE_CODE_SESSION_ID` the created pane merges with the row; if not, the operator
gets two rows for one session, one killable and one not.

**Half of that is no longer a question, and it was not the half it looked like.** The absorb branch above had
no production caller at all: `SetClaudeSession` and `proc.SessionID` were both reachable only from their own
tests, so the paragraph reasoned from a branch that could not execute and the answer would have been
"duplicates it" whatever the attach client exports. The wire exists now (`joinAdoptedSessions`, on the agents
poll), and the door's own population is the easy case: every pane `a` creates is created BY THE HUB, so its
session id is known at birth from `Adopt` and needs no environment variable at all. What the
`CLAUDE_CODE_SESSION_ID` probe still decides is the OTHER population — a pane the operator opened by hand —
and that one waits on the process walk recording a session id, which it does not do today.

**The confirmation gate is the listing's own word, and it is `failed` OR `done` — the sentence this
paragraph used to carry was REFUTED by measurement.** It read "waking a live, blocked, working or settled
row costs nothing measured", and the settled half is false: on 2026-08-17 `claude attach <id>` against a row
this hub's own dashboard called `done` woke it, and within the minute its `state.json` went `done` →
`working` with a fresh `detail` — because the record carries `respawnFlags: ["--reply-on-resume", …]`, so
the replay does not merely redraw, it answers. Over the whole local store **18 of 26** records carry that
flag and **10 of the 15** reading `done` do, which makes it two rows in three rather than a corner. The hub
cannot tell WHICH, because `respawnFlags` sits in the same far-host file as the reap stamps that §22.5 rules
no code reads — so `done` and `completed` join the gate and the dialog says *may* where it cannot say
*will*. (That measurement was taken by probing the operator's own live fleet, which woke one finished
session and left it reading `failed` after it was stopped again: §22.10 carries the rule that a door probe
uses a session the prober created.)

`blocked`, `working`, `busy` and `idle` stay outside the gate on the original argument, which survives: a
blocked session is waiting for a person and waking it is how the person answers, a working one keeps working
whether a pane watches it or not, and a confirmation for a free action teaches the operator to press `enter`
without reading. Two honest limits. The gate is **one-directional on four rows**: every row carrying `reapedMidWorkAt` reported `failed`
in both its `state.json` and the listing (**4 of 4** locally, 2026-08-15; the fifth such file, on `nuc`, was
not cross-checked), so `failed` is a SUPERSET of the reaped rows and nothing measures `failed` ⇒ reaped. And
it is **blind to the other reap**: a session killed while BLOCKED left a byte-identical `state.json` with
only the roster entry gone, so it carries no reap field and reads `blocked`, which this gate sends straight
through — one of the four shapes the headline measurement names. Gating on `reapedMidWorkAt` instead is
refused for a different reason: it lives on the FAR host in a file no code reads (§22.5).

**Three costs, and the dialog is not pricing `enter`.** Costs 1 and 2 were measured on the SIGKILL that
reaped the session, so they are **already sunk** when the dialog appears: `esc` neither restores the turn
nor reaps the orphan. The dialog says what the operator is walking into.

1. **The work in flight was abandoned, and a killed turn can still report success.** Measured: the
   transcript kept a dangling `tool_use` with no result and no retry, then `No response requested.`, and
   the row settled `done`.
2. **A process it started may still be alive** — measured, the `sleep 300` from the killed tool call
   survived, reparented to init. The dialog names the **class and the host and never a pid**, because no
   source carries a usable one: `state.json` has no `pid` at all, the listing's `pid` is non-null on 3 of 25
   local background rows and on **none** of the 7 it calls `working` or `blocked`, `UpdateAgents` copies no
   pid, and the process walk is skipped for this population.
3. **The host may gain a tmux server** — `up-empty` → `up`; measured on 3.2a, one `new-session -d -P -F`
   against a socket with no server returned all five fields at rc=0 and started it. Second explicit action
   in the design that can do that, and a cost of EVERY press rather than of a confirmed one (§22.9.2).

**What the dialog must NOT do is name the turn from `detail`.** `detail` is the field that is there — 31 of
32 files against `needs` in 6 of 32 — and on a reaped row it goes stale: one read `Done. Goal met` about
work that was abandoned, with no timeline entry. So the dialog says **when** the work stopped and never
**what** it was, and the two stamps are the two fields §22.5 separates: `last alive <reapedMidWorkAt>,
reaped <firstTerminalAt>`. `detail` appears only under "its last words", with its age. Naming the abandoned
turn exactly would mean tailing the transcript — **44 MB / 24 413 lines** for one live session in §17 —
refused rather than deferred silently.

The block as BUILT, measured at 80 columns, and it differs from the sketch this section first carried in
three ways that are all the same way — it says only what the hub holds. The two stamps (`last alive`,
`reaped`) and `detail` come from `state.json` on the row's own host, which §22.5 rules no code reads, so
they are absent rather than invented; the footer names `any other key` rather than `esc`, because any key
that is not `enter` cancels and naming one of them is a half-truth on the line the operator's finger is on;
and the costs are WRAPPED rather than truncated, since `lines.Truncate` emits no ellipsis and a cut cost
would read as a complete one.

```
wake cicd on local?  the listing says failed
  • the turn it was running was abandoned, and a killed turn can still report
    success — so anything it claimed to finish may be unfinished
  • a process its last tool call started may still be running on local — the hub
    cannot see it and will not kill it
  • waking it replays the transcript, and most background sessions are set to
    reply when they resume — so it may pick the work up again and spend tokens
enter wakes it  ·  any other key leaves it alone
```

A SETTLED row gets a different block, because nothing was abandoned and no tool call was interrupted:

```
wake cicd on local?  the listing says done
  • waking it replays the transcript, and most background sessions are set to
    reply when they resume — so it may pick the work up again and spend tokens
  • local has no tmux server; making a pane starts one
enter wakes it  ·  any other key leaves it alone
```

Rules rather than layout: the body is **sized from the room left**, never by subtracting a fixed height
(§22.9); every claimant is one part of one list, so a loss is marked; and the block names no cost it has not
measured — whether replaying a transcript is **billed** is UNVERIFIED, carried in §22.10 and not in the UI.
Where nothing is spent the note still says what happened — `made refactor-parser-3f2a1b09 on nuc for it`,
plus `started a tmux server there` when it did. **One press of `a` also writes one line to `history.jsonl`**
— §16 calls that file the only record that a prompt reached a machine, and this is the only §22 action that
abandons anything, so it gets a fifth outcome beside `launched`, `woken`. Two build facts: `RenderHistory`
switches on three words and initialises `glyph := "?"` (`internal/ui/render.go:768-776`), so `woken` joins
that switch in the same commit; and `history.Entry` has no column for a session id or a stamp, so those
values land inside `Text` and the only assertion available is a substring.

**Five test rules, each pinning a claim a green suite has hidden before** — the plan's Global Constraints
carry the gates, the red-then-green rule and the two-producers rule; these five are §22's own. (1) The
dialog's cost lines asserted **by count and by content on the dialog's own surface** at 80/100/160/200, never
by `Contains` over a screen two surfaces print to. (2) The confirm table read from `AgentWord`, not
`ClassifiedState`, driven through `Update`. (3) An **accept pole** — `a` on a background row RUNNING the verb
— because today's tree refuses every agent row and a refusal-only suite is green against a tree with no
feature. (4) A concurrent test in the fetch's own commit that snapshots under the lock, merges back by key
and by field, and asserts the value **survived** rather than that `-race` stayed quiet. (5) A scripted fake
`claude` on a temporary `PATH` that can succeed, exit 1 with one stderr line, or write only to
`--debug-file`, since no such double exists and the one real-claude e2e case begins with a `t.Skipf` that
reports PASS. A §22 e2e case **fails**, never skips, if `CLAUDE_CONFIG_DIR` is not inside the test's own
`t.TempDir()` — with its limit named: a fresh temp dir has no daemon and no roster, so all that guard can
drive against a real `claude` is a refusal, which is why the fake carries the door.

### 22.4 `i` on an agent row is deferred, and the danger is measured rather than feared

**The interim answer is what this section already built:** `a` gives a pane-less session a real terminal, so
a blocked agent is answered by hand in the pane the hub just opened.

**The mechanism forces the deferral, not caution.** `attach`, `logs` and `stop` are CLI verbs; `reply` and
`subscribe` — the two operations a send needs — are protocol operations with **no verb behind them**, so
shipping `i` today ships the hub's first protocol client *and* its first per-host secret read over ssh:
`$CLAUDE_CONFIG_DIR/daemon/control.key`, mode 0600, compared with `timingSafeEqual` (§22.2). The capability
is real and recorded because the path is deferred rather than impossible: `reply` delivered multi-line text
verbatim to a session blocked with **no tty, no pane and no viewer** — `ok` in **13 ms**, the question
answered, the agent continued.

**The danger is a measurement, and it is why a future `i` is whitelisted rather than blacklisted.** A
session sitting on a Write permission prompt was sent text reading `ABSOLUTELY DO NOT … DENY`. The file was
**created 2 of 2 times**, the refusal text appears **nowhere** in the transcript, and the daemon answered
`{"ok":true}` both times. The ruling rests on the OUTCOME — a send into that state approves a pending write
and loses the text. The mechanism is a hypothesis, written as one: "it discards the text and presses Enter"
is equally consistent with a prompt reading the first keystroke as a menu selection and with a reply op
writing a newline first, and an author who believes the first will conclude that suppressing the trailing
Enter makes the send safe. Detection is unavailable too:

- **The fact source is silent about that state.** Polling reported `working/active`, an empty `needs` and no
  `block` across **136 s** — through the daemon's own `list`, the source §22.2 forbids the hub to use.
  Whether the call §22.6 mandates reports it differently is UNVERIFIED, and that probe must be taken bare as
  well, since bare is what the hub keeps calling until the `--all` commit lands.
- **`{"ok":true}` is a socket ack, not a delivery.** Five replies across a respawn gave **4 acks and 3
  deliveries**; `has` answered `alive:true ready:true` after a SIGKILL; `await-ack` accepted a deliberately
  wrong nonce; `permission-response` is dead either way (`ok:true` for the nonexistent id `deadbeef`, zero
  `requestId` in any response). The only witness is `subscribe`'s raw **pty bytes**, a later read of the
  target's own screen, which is §7's primary witness. Nothing draws them, so §20's ban on a second emulator
  is untouched *because* this path is deferred.

**What would have to exist first**, each measurably absent: a verb for the write; a source that tells
*waiting* from *computing* (neither of the two the hub may read is that source — `block.questions` is on
**1 of the 6** rows the fleet's listing calls `blocked`, and the permission prompt reads `working/active`);
a witness (until one exists §7's `delivered` is unreachable and every send records `sent-unwitnessed`, which
§7 already calls the DEFAULT reading of an ack); and a refusal at MARK time, with words.

**The whitelist as it stands passes the state it exists to exclude — an ORDER problem, not a redundancy.**
Stated honestly the predicate is "the LISTING says `blocked` AND `state.json` carries a `block`": it admits
**1 of 6** measured blocked rows and presupposes the per-host `state.json` read §21.3 costed at ~2.4 s over
ssh and that no code performs. The only read §22.5 specifies rides the SLOW sweep, so a row that carried a
`block` at sweep time and has since moved onto a Write permission prompt satisfies both conjuncts — and
firing text at that state created the file 2 of 2 times. A future `i` re-reads the `block` inside the send,
or names the window it is gating on stale data. (`tempo == "blocked"` is not a second condition: §22.5
measures `tempo` a deterministic projection of the listing word, and it is not in the listing at all.)

**The refusal, and it must branch on the INTENT.** Refusing at the mark rather than after a confirmation is
right, and one sentence cannot serve every selection-driven key: `K` and `!` both require a selection, so a
single sentence about typing would answer a kill with a remedy about answering a question. The `i` sentence
is

`<name> is a Claude session with no pane — the hub cannot type into it; press a to open a terminal and answer it there`

pinned by a frame assertion on the string `View()` returns at 80 columns, which must fail against a build
with the `Kind` branch removed; `K` gets its own sentences (§22.9.3). None of this exists today: `space`
marks an agent row with no `Kind` branch (`internal/ui/model.go:1162-1185`), `i` needs only a non-empty
selection (`model.go:1431-1436`), and the confirmation then offers two reasons derived from fields
`UpdateAgents` never sets — `this pane cannot be identified as an agent`
(`internal/broadcast/confirm.go:37`) and `this pane does not accept pasted text — it will read the prompt as
keypresses` (`:43`) — both about a *pane*, for a row that has none, before pressing through lands on
`send.go:84`. A grep over the agent-row refusal paths returns **four sentences across five sites** —
`confirm.go:37`, `confirm.go:43`, `send.go:84` and `:345`, and `lifecycle.go:44` — and the last is
cursor-driven, so a mark-time refusal structurally cannot reach it.

### 22.5 What the row shows, and what the tile shows

**The row is state, then name, then whichever marker applies.** Below 100 columns
(`internal/ui/render.go:123-134`) and at 100 and above (`:157-159`) the shape is identical — point, mark,
glyph, a six-column state word, the display name, and one marker from `rowMarkers`: `[↑]` when the row was
hidden and came back because it is asking, `[x]` when it is hidden and only on the screen because `X` is on.
The two are disjoint by construction (`hide.Set.Hidden` is `marked && !waiting`, `Resurfaced` is
`marked && waiting`), and one function serves all three row shapes because each of them used to append its
own marker — a second marker would otherwise have had three places to be forgotten, and the shape it would
have missed is the one below 100 columns.
No id, no kind, no cwd. An agent row emits no session header (`lastGroup = ""`) for the reason the code
gives — its NAME is the row, and a header each made the list half headers — and **not** because names are
unique: `internal/registry/registry.go`, `agentRowID`'s own comment records two rows sharing a name and a cwd. Every frame here is built at **80
first**, the size §16 commits to, then 100, 160 and 200, because "checked at the promised size" is a claim
per BAND and 160 is the top of band two rather than the tile-grid band. A requirement, not a description of
the suite: `TestAgentRowsGetNoSessionHeader` runs at width 70 today.

**The tile's sentence comes from `detail`, not from `needs`.** Census of
`~/.claude/jobs/<short id>/state.json`, 2026-08-15, over **32 files (25 local + 7 `nuc`)**, 0 unparsable:

| field | present | what it is |
|---|---|---|
| `state` | 32/32 | the WORK's own word, not the listing's — see below |
| `respawnFlags` | 32/32 | the respawn recipe, a flag list |
| `resumeSessionId` | 32/32 | a field SEPARATE from `sessionId`; how often the two differ was not counted (§22.10) |
| `tempo` | 32/32 | a deterministic projection of the listing word over 25 rows: done→idle, failed→idle, blocked→blocked, working→active. Carries nothing new |
| `detail` | **31/32** | one informative line the agent wrote |
| `needs` | **6/32** | the richer ask, on some blocked sessions only |
| `cliVersion` | 28/32 | absent on 4, so a version correlation is undefined for them |
| `reapedMidWorkAt` | 5/32 | last-alive, not reap time — see below |
| `block` | **1/32** | the questions, where they exist at all |
| `pid` | **0 of the 25 local files** | **absent**, not null — the reason liveness is the listing's. The 7 `nuc` files were not counted for it (§22.10) |

This **corrects §21.3's 25-file census by citation, not by overwrite**; §21.3's `intent` row (25/25, **the
OPERATOR's own prompt**, never to be shown as the agent's question) was not re-measured and stands on the
earlier census, that ruling being about what the field means. Two consequences. `tempo` adds nothing, so
every `state/tempo` pair in this document is **one fact written twice** wearing the look of two sources
agreeing, and it mixes two freshnesses — write the listing word alone. And **`state.json` is a
background-row artefact**: its directory is keyed on the listing's `id`, which 0 of 13 interactive rows carry
(§22.8), so `detail`, `needs` and `block` are unreachable *forever* for an interactive agent row.

So the tile's sentence is four rungs: `needs` where present, else `detail`, else the listing's own word, else
`unknown`. The third exists for a shape the census did **not** find — from 31/32 and 6/32 the only sound
statement is that at most one file lacks `detail`, and whether any lacks both is UNMEASURED. The fourth
exists because **2 of the 6 local interactive rows carry neither `state` nor `status`** — derived from
`status` on 4 of 6 and `state` on 0 of 6 in the `--all` call of 2026-08-15 — `Attention()`'s default returns
`""`, and that renders `unknown`, a state §17 already ruled one of its own. A tile keyed on `needs` alone
falls to `detail` in **26 of 32** cases, and with the fallback the tile is never blank, so `!= ""` passes on
all 32 and cannot tell which rung supplied the text. Assert the literal.

**The listing is authoritative for state, and the file's own `state` can mean something else entirely.** The
two agree on **24 of 25** local rows (2026-08-15). The disagreement is job `77ef6f5e` — file `blocked` against
listing `working` — on a file **0.3 minutes old**, so not staleness: `tempo: active`, `inFlight {tasks: 5}`,
no `block`, `needs` null, and a `detail` reading `staging/prod deploys blocked: docs have lies, 264 still
broken 8.4d`. The work is blocked on something outside the machine while the daemon computes. Both are true
and only one is what an inbox sorts by, so the tile's `state:` line is the LISTING's word — **which is
`AgentWord`, not `ClassifiedState`** (§22.3): the pane-less tile prints `p.State().String()` at
`render.go:261`, inside the block at `:256-263`, so a listing
`done` renders `idle`, `blocked` renders `needs` and `failed` renders `error`, and a tile wired to the
classified field would never print a word the listing said. The file's `state` is never printed as the state;
its contribution is `detail` and `block` — and `blocked` in the listing is not evidence that a question is
waiting, since `block.questions` is on **1 of the 6** rows the listing calls `blocked` and `block` on **1 of
32** files, so the tile shows what is being asked only where `block` is present.

**Reaped is a departure the hub must announce, because the row never does.** `reapedMidWorkAt` is the
**last-alive** time, not the reap time — measured 06-21 against a 07-14 reap; what writes it, and why it holds
a last-alive time, is not measured (§22.10). The reap time is `firstTerminalAt`, which the 32-file census did
not count, so the tile reads `last alive <reapedMidWorkAt>, reaped <firstTerminalAt>` and never "reaped at"
over the first field. A stale `detail` on a reaped row read `Done. Goal met` with no timeline entry, so a row
that lost work reads finished. Reaped and blocked are **disjoint** on the rows measured (§22.3's 4 of 4).
The announcement needs a durable artefact or it cannot be tested, and a row absent from the listing is
DELETED (`internal/registry/registry.go`, the agent-row deletion in `UpdateAgents`): so
when a row leaves while its last `AgentWord` was not `done`, the hub writes a `history.jsonl` record and keeps
the row one tick reading `left the listing while <word>`. §6's *pixel* tile work does not extend to these
rows because the material is different, not because there is less of it.

**Liveness is the listing's own population, never a pid.** `state.json` carries no `pid` at all (absent in 25
of 25 local files, including two live `working` sessions), and a session killed while blocked left a
**byte-identical** file with only the roster entry gone — one kill, one machine, not repeated; the weaker form
is enough and is checkable read-only. The LISTING's `pid` is a different field and is **not** deleted: §17
rules it "the cheapest pane↔session join available" and `nuc`'s own table row shows it populated. So the
ruling is narrow — **the pid is never a liveness answer, and its only sanctioned use is the pane↔session
join**, which must fall back, being non-null on 3 of 25 local background rows and on none of the 7 the
listing calls `working` or `blocked`. `agents.Session.PID` is declared at `internal/agents/agents.go:37`,
parsed at `:116-117`, and read by nothing: a dead-code fact, not an argument about nulls.

**The producer, or this is a screen with no producer.** The tile prints the short `id` the door takes, which
no field on the row carries today (`render.go:274` prints the full `SessionID`) — it is what lets an operator
run the verb by hand when the door refuses, and the key to `~/.claude/jobs/<short id>/`. Everything on the
tile except state, kind, name and start comes from a file nothing reads yet: the listing's nine keys (§22.6)
contain no `detail`, no `needs`, no `block` and no reap field; grepped, `state.json`, `.claude/jobs`,
`reapedMidWork`, `block.questions` and `resumeSessionId` return **zero** hits in `*.go`, and the only
home-relative readers are `statedir.Path`, `configdir.Path` and `sshConfigPaths`. So the read is specified
here: **one batched read per host on the SLOW sweep**, at the ~2.4 s over ssh §21.3 costed, never on the 20 s
listing tick and never on the paint path — so the tile carries a different freshness from the row, and says
which. Every §22 timestamp renders relative to the frame's own fixed instant, never `time.Now()`: the mockup
is byte-reproducible from one instant, and that is the only instrument that can prove a refactor moved no
frame.

**Hiding a pane-less row is REFUSED, and the decision is taken now because it has a deadline.** `hide.KeyOf`
has no `Kind` branch (`internal/hide/hide.go:63`), so `x` on an agent row persists `{Host, Session: Claude's
NAME, WindowIndex: 0, PaneIndex: 0, Start: ""}` — three of five components zero-valued because `UpdateAgents`
sets none of them — and two agent rows sharing a name on one host share a key. §18's expiry protection also
inverts: the row has no start command, so its mark carries nothing that expires and outlives the session it
was taken against. So `x` refuses with its reason. `hidden.json` is at v2, and a key change costs no second
migration only while v2 has not reached an operator's disk.

**SHIPPED, and then CORRECTED by a review of the commit that shipped it.** The refusal's reason is
LIFETIME, not collision: a pane-less row's mark carries nothing that expires, so it outlives the session
it was taken against. The first version gave the collision as the reason, which was true then — but the
guard refused only the AGENT subject, so `x` on the colliding PANE still hid the row beside it, and one
press of it took a two-row screen to `0 sessions · 2 hidden` with the roles swapped. The answer was not
a second guard: **`Kind` is in `hide.Key` now (v3)**, so a pane-less row and a pane can no longer produce
the same key and neither direction needs noticing. The free-migration window this section names is what
paid for that — no `hidden.json` existed on the operator's disk, checked before the bump. What the guard
still enforces is the POLICY, and the shipped sentence says the policy's reason:
`nothing hidden — <name> has no pane; a mark would never expire`, 68 columns against the 77 an
80-column footer leaves a shared claimant, with a nameless row falling back to the short id and then to
`this row`. A mixed selection refuses whole, the rule `K` follows.

**What the collision was, recorded because the key now makes it unreachable.** The guard is at the top of
`hideSubject`'s toggle, after the subject is built and before any mark is written, so ONE guard covers both
the selection and the cursor — the cursor is the commonest gesture and a guard in the selection branch alone
would have left `x` live on it. Measured on the way in, and each of these is now a test:
a real tmux pane at **window 0, index 0** of a session with the agent's name shares the agent row's key, so
the collision **crosses the `Kind` boundary in both directions**; two NAMELESS agent rows on one host also
share a key, so on a version reporting no name every pane-less row on that host collapses onto one; and one
press of `x` on a two-row screen left `0 sessions · 2 hidden`, taking with it the pane nobody had marked.
The sentence this section first specified — `nothing about this row survives a restart to hide it by` —
is 93 columns once the row is named, against the 77 an 80-column footer leaves a claimant that shares the
row, and the mechanism matters less to the operator than the consequence. What ships is
`nothing hidden — <name> has no pane; a mark would hide another row` at 72 columns, and a nameless row falls
back to the short id. A mixed selection refuses whole, the rule `K` follows. §14.2's out-of-band signal loses its whole population here too: `window_bell_flag`, the
terminal bell and `display-message` are window and client observables, so for a pane-less row the signal is a
producer-side trigger or nothing — and the producer is measured silent for 136 s on the state that most
warrants it.

### 22.6 How the hub calls the other program

**Always `--all`.** Measured 2026-08-15, with the bare-versus-`--all` contrast taken in the SAME invocation
on each host so "supported" is distinguishable from "silently ignored": accepted rc=0 with empty stderr on
**2.1.233** and **2.1.224**. Locally bare returned **13 rows** and `--all` **31**, with **17** sessionIds
present only under `--all` — 13 `done` and 4 `failed`; a fourteenth `done` row escapes that count only
because it shares its sessionId with an interactive row bare does list. On `nuc`, bare **11** against `--all`
**14**, of which **7** background. So the default filter hides completed and failed work in bulk rather than
one reaped row, and §17's invariant "a session leaving the listing is done with, not stale" holds **only**
under `--all`. `claude agents --json --all` costs **222 ms** locally, so §17's separate 20 s timer stands —
but that is the TIME cost only; whether the call also cold-starts a daemon is UNVERIFIED (§22.10), and
**until that is answered no rule in §22 may rest on the listing being free**. The producer CALLED bare
at **two** sites: `internal/agents/agents.go:138` (local argv) and `:150` (a shell string over ssh) — a fix
naming one leaves the ssh fetcher on the narrow call, the half that covers most of the fleet.

**The listing is nine keys on two of the three hosts and SEVEN on the third, so the schema is not
identical across the spread.** 2.1.233 and 2.1.224 both return `cwd`, `id`, `kind`, `name`, `pid`,
`sessionId`, `startedAt`, `state`, `status` — exactly `agents.raw`'s nine declared fields
(`internal/agents/agents.go:73-83`). **2.1.226 returns seven: no `pid` and no `status`** (measured
2026-08-16). An earlier revision of this paragraph generalised "identical across the fleet's spread" from the
two hosts it had measured, which is the same error as the version claim in standing rule 1 — a claim from two
points refuted by the third. What survives is the portability that matters: the fields the hub READS are
`state` or `status`, whichever is present, and `agents.Parse` already tolerates neither, so a seven-key host
parses correctly. The nine-field struct is a superset, not a contract. `pid` here is the LISTING's field, not the
file's (§22.5).

**DECISION, the operator's, taken 2026-08-15 and then REPLACED the same day: `--all` is taken WHOLE and
nothing is filtered.** The first ruling dropped `done` and `completed` to control noise — `--all` adds 17 rows
to a 21-row inbox, **+81%**, 13 of them `done`. The operator then named a better instrument for the same
problem: **order by last activity, newest first**, at which point old rows sink on their own and a filter buys
nothing that costs a capability. So `done` is **shown** and a settled session is reachable by pressing `a`,
which the filter had taken away.

Recorded as a replacement rather than as a correction because the first ruling was right about the
PROBLEM — thirteen finished rows at the top of an inbox is noise — and wrong about the instrument. **Ordering
is the structural answer and filtering is the instance-by-instance one**, which is why this section carries
both and not only the winner: a reader who meets only the ordering rule cannot tell whether hiding was
considered.

Where the order goes, and where it must NOT: attention rank stays the primary key and the longest wait stays
first inside the waiting block (§21.11.1) — recency as the primary key would sink a session that has waited
three hours below one that printed a log line, which inverts §1. Recency replaces the **alphabetical
host/session/pane tiebreak for every other rank**, and it is bucketed to the minute, because `markActivity`
moves on nearly every tick for a working pane.

**The bucket's original reason is gone and it stays for a smaller one.** It was written to protect the CURSOR:
`m.cursor` was an INDEX into the on-screen list, so a fine-grained recency order moved the row out from under
the operator's hand between looking and pressing. That class is now closed structurally rather than avoided —
the cursor names a ROW (`rowCursor`, `internal/ui/model.go:130`), its position is derived at every read, and
`internal/ui/cursor_identity_test.go` holds the reorder under a held cursor and requires the next keystroke to
act on the same pane. What survives is READABILITY: a list that re-sorts every second cannot be read while it
moves, and that argument is about the eye rather than about the keyboard, so the bucket is kept deliberately
and could be narrowed without breaking anything. The same fix removed `clampCursor` and both of its
list-assignment callers, which is the honest measure of the change: there is no stored position left for a
future writer of the pane list to forget to correct, and the agents poll (§22.1) was already such a writer.

**And a limit the ordering ruling did not state, measured on the shipped code 2026-08-17.** Recency needs
HISTORY, and a fresh process has none: in a single `--status` invocation every agent row is first-sight, so
`markStateEntry` stamps them all with one `now`, `lastKnownChange` returns the same value for every one, and
the whole list falls through to the `(host, session, pane_id)` tiebreak. Verified over 41 real rows: all six
state ranks came out alphabetical, and the five `error` rows sorted by that tuple exactly. So the rule that
an old row sinks does nothing on the first frame, and nothing at all for a one-shot report — it starts to
differentiate only after the hub has watched a row's listing word change. **N7's justification rests on this
rule, so it is weaker than written for exactly the case it was invoked for**: a `failed` row from two hours
ago is not below a fresh one until this hub instance has seen both become failed.

The instrument that would close it is measured and already named elsewhere in this section:
`state.json`'s **`firstTerminalAt`** is when a row BECAME terminal, and it is present on **19 of 19** rows
the listing calls `done` or `failed`, carrying **19 distinct values** — enough to order every terminal row on
the first frame. Reading it belongs to the `state.json` task of the door plan rather than to the comparator,
which has no file access; until then the limit above stands and is stated rather than discovered.

**One consequence of showing `done` that the filter had hidden:** `agents.Attention()` folded `done`,
`completed` AND `idle` onto one output (`internal/agents/agents.go:56-67`), and `state.FromWord(s.Attention())`
is what sets an agent row's rank (`internal/registry/registry.go`, `state.FromWord(s.Attention())`). So a FINISHED background row and a
LIVE interactive session shared one rank, and only the recency tiebreak separated them — a live session
quiet for an hour sorted below a job that finished a minute ago. **ANSWERED, in §6 where the state table
lives: `done` earned a rank of its own**, below `unknown` and above `gone`, and `Attention()` no longer folds
`done`/`completed` onto `idle`. The fold also cost the WORD, which was the larger half: the row printed `▸
idle` for a job that had ended, and `idle` is defined as a prompt waiting for the next thing. Nothing in this section may derive a rank or a population from `Attention()` and assume the two are
distinguishable. Not a version hedge: the
vocabulary splits by KIND on the NEWEST host too. In the local `--all` call of 2026-08-15 that returned 31
rows, the 6 interactive ones carried `status` (**4 of 6**) and not `state` (**0 of 6**) — and that interactive
count itself moved 6 → 5 later the same day, so the denominator dates the call; on `nuc` the split is total
(§22.8) and what the LOCAL background rows carry was not counted (§22.10). So anything reading that helper as
if it separated a finished row from a live one is wrong on **2.1.233 as well**, and a reader who version-gates
the reasoning keeps the defect on the machine they are typing on. The guard is a fixture per measured WORD,
the shape `internal/agents/attention_test.go` already has.

**`Kind` in the row key is now mandatory rather than prudent, and that is the bill this ruling pays.** With
nothing filtered the collision §22.11 measures is LIVE: 31 rows collapse to **30** keys and one of the
operator's rows disappears with no error anywhere. The earlier filter would have removed that instance by
accident — `--all` minus `done` measured 17 rows and 17 keys — and removing an instance is not closing a class,
since a `failed` row can carry the same interactive twin. So the key change and the `--all` change land in one
commit, and `internal/registry/registry.go`, `agentRowID`'s own comment — which still asserts the collision "does not occur on
this fleet" — is corrected in the same breath.

Two questions the earlier filter left open are closed by its withdrawal: nothing is collapsed, so there is no
collapse to carry a count for (§16's promise that nothing is ever merely absent from the list is kept by
showing the rows); and a `failed` row — permanent under `--all`, at rank `Error`, second only to `Needs` — gets
**no dismissal**, the operator's ruling of 2026-08-15, with the cost stated rather than discovered: such a row
stays until its session is stopped or woken, and on the measured fleet that is 4 rows of 31.

**`--debug-file` per verb, measured, one cell open.** A `claude` failure wrote nothing to stdout and nothing
to stderr — the reason existed only in the debug file — so without it a failure is invisible. Measured on
both versions: `claude agents --json --all --debug-file <path>` is **REJECTED**, rc=1 in ~165 ms with
`error: unknown option '--debug-file'` and no file written, 6/6 runs; `claude logs <id> --debug-file <path>`
is **ACCEPTED** and writes the file even for a bogus id (620 B local, 278 B on `nuc`). So the reading that
CAN be instrumented is `logs`, `agents` is judged by EXIT CODE alone, and **`attach` was not tested for the
flag** — which §22.3's payload rests on. `design.md:363-364` states the opposite for `logs` and is on the
correction list; that site frames it as an operational RULE, which is why it is the one that matters.

**One seam, and it is not an argv scan.** `internal/tmux/guard_test.go` bans `RunRaw(` outside one file and
does not ban `exec.Command`, and the tree's `claude` invocations sit at `agents.go:138`, `:150`,
`internal/ui/lifecycle.go:69` and `internal/launch/launch.go:159`. Only the first is an argv: `:150` is
`name: "ssh"` with `claude` buried in a shell string, `:69` is a string handed to `respawn-pane`, and
`launch.go` builds a single string for tmux's shell-command argument. A scan for the literal `"claude"` in
argv position therefore MISSES `:150` — the exact site a partial fix must not leave behind — and both verbs
that accept `--debug-file` are shell-string sites, so an argv-only constructor cannot carry the flag to
either. The seam is one constructor per verb returning **both** forms, with a scan over the word `claude`
inside string literals as well as argv position and a **FLOOR** on sites found.

**A row leaves the listing for two reasons and the hub may only infer one.** `internal/registry/registry.go`, the agent-row deletion in `UpdateAgents` deletes an
agent row that has left, on the ground that the listing is the whole truth about its own population. Under
`--all` that holds — but only while the hub can tell an empty listing from an absent one, and today it
cannot: `Host.AgentsReason` carries `agents: claude is not installed here`, `agents: deadline exceeded after
30s` and every ssh failure, and reaches only the machine-readable `--status` report
(`internal/hub/report.go:19,46`); the model merges it into `m.hosts` and **nothing rendered
it**; and `fetcherFor` (`internal/hub/poll.go:461-474`) returns nil — no rows AND no reason — for a host
reached only by a forwarded socket. §16 promises nothing is ever merely absent from the list, so
`AgentsReason` reaches the host line in the same commit as `--all`, or widening the call makes the silence
bigger.

**SHIPPED, one commit late.** `--all` landed alone and the reason stayed unrendered, which is exactly the
bigger silence this paragraph warned about: the host's tmux panes drew normally while every pane-less row it
should have had was missing. The reason is now a claimant of the footer's own `Fit` list rather than a line of
its own, in a THREE-tier order — a host that is not up shows its transport reason first, because a listing
that could not run on an unreachable host is a consequence and not a second fault; then any host whose
LISTING failed, because a screen that looks complete and is not outranks an informational reason on a healthy
host; then an up host's own reason. A host with a transport reason does not also get a listing one: two
parenthetical clauses on one identity read as a run-on, and the transport reason is why the listing failed.
The renderer adds NO prefix — every string the fetcher stores already begins with `agents:`
(`agents.ErrNotInstalled`, `agents: deadline exceeded after …`, `agents listing: …`), and the first version of
this added one, printing `agents: agents: …` past a green test whose fixture was invented rather than taken
from the producer. What is still silent is the third case above: `fetcherFor` returns nil for a
forwarded-socket host, so there is no reason to render.

**`claude logs <id>` is a live-row reading, and an offer no key reaches is not an offer.** For an id the
daemon no longer holds it answers rc=1 **in under 0.2 s** with the sentence §22.3 quotes, printed through and
never re-read — cheap, and still unable to explain the row you most want explained, which is why it is
offered on a live row only. So either §16's keystroke block and §21.4's key table gain a row for it, with a
frame at 80 columns, or both offers are deleted and a dead row is recorded as having no account. A ruling
that names an action reachable by no key is this repo's signature defect.

### 22.7 What §7's pane apparatus transfers, clause by clause

`docs/design.md:844` requires this list by name: everything §7 builds belongs to the pane path, and §22 says
which of it moves. **This subsection number is REUSED.** Earlier drafts numbered a "what this removes from
the earlier plan" table §22.7; that table lived in
this section's implementation plan (an internal build artefact, not published) as a DIFFERENT
list of seven rows, and the
retirements it no longer carries — `--resume`, `--teleport`, `i`/`reply`, `permission-response` and `ok:true`
read as `delivered` — are stated in §22.1 and §22.4, which rule on them. One consequence a reader will look
for here: the drop of `job not found — it may have already exited` as a matchable string is the plan's row
"matching on `job not found — …`", which keeps rc=1 as the durable signal. There is no §22.7 row left to
carry it.

| §7's apparatus | transfers? | why |
|---|---|---|
| the identity **token** | **No** | the token comes from a process walk over a PANE; no pane, no walk, no token — and `internal/proc`'s walk is skipped for any row where `Kind != KindPane`, so the value was never observable rather than merely uncopied. That is why `space` on an agent row ends at `the hub holds no identity token for this pane`, and why §22.4's refusal moves to MARK time |
| the **epoch** | **No** for the row, **Yes** for the pane `a` creates | an epoch identifies a tmux server and a session has none. The created pane gets one from the `new-session -P -F` read-back, mandatory (§22.3) because an empty epoch takes the full-screen path |
| the paste **buffer** and its Enter | **No** | both are `send-keys`/`paste-buffer` against a `%NN`. §11's killed class survives precisely because every text path targets a `%NN`, and an agent row's `PaneID` is `agent:<id>@<hex>` — stopped today by a missing token rather than by a target-shape check, so the shape check is what §22.4's closure has to add |
| the `capture-pane` **witness** | **No, and no substitute the hub may read** | the only session-side witness is `subscribe`'s raw pty bytes, which §22.2 forbids and §20 bans drawing. So §7's `delivered` is unreachable here and `sent-unwitnessed` is the DEFAULT reading of an ack — which makes §7's session-target column the shape a FUTURE `i` takes rather than semantics this product has, and it must be marked so in §7: an unmarked column for a deferred path reads as implemented |

What the four "No"s buy is structural: no protocol client, no per-host secret over ssh, no second terminal
emulator — three failure classes with nowhere to happen rather than three rules to remember. The fourth, two
writers on one transcript, is closed for `--resume` only: whether `attach` is exclusive is UNVERIFIED
(§22.1), so it is not claimed as a closed class.

### 22.8 What stays impossible, as a property of the world

**An interactive Claude session has no door, and the reason is arithmetic rather than policy: there is no
argument to pass.** Measured 2026-08-15 with `claude agents --json --all` on **2.1.233** and **2.1.224**,
closed in **both directions on both hosts**: `kind: background` ⟺ the row carries an `id`. Background rows
carry it **57 of 57** (25 local, 7 on `nuc`, 25 on §17's third machine, added 2026-08-16); interactive rows
carry it **0 of 13** (0 of 6 local, 0 of 7 on `nuc`; the third machine has no interactive rows, so it cannot
extend the negative half). On `nuc` the vocabulary splits with it: background 7/7 carry `state`, interactive 7/7 carry `status`
and 0/7 carry `state`. `attach`, `logs` and `stop` all take that `id` and all three exist on both versions
with byte-identical usage — so for an interactive row the verb has nothing to be called with. That is
stronger than the sentence it replaces: "`attach` is a background verb" is a fact about the vendor's intent,
and intent moves in a patch release; "the row carries no id" is a fact about the bytes on the wire, the same
at both ends of a nine-patch spread. (Both denominators are that one call: the local interactive count moved
6 → 5 later the same day.)

The route in exists and is not the hub's: `/background`, typed by the operator INSIDE that session. (That no
`claude agents` option could do it instead is a `--help` census nobody took, so it is left UNVERIFIED rather
than asserted; what is measured is that the row carries no id.) `a` on a non-background agent row therefore
refuses, carrying the fix rather than the breakage:

`<name> is kind "<kind>" and has no background id — type /background in it`

**That is SHORTER than the sentence this section first specified, and the reason is measured.** The
original was 190 characters (`… and Claude gives no background id for it, so there is nothing for the
hub to open. Type /background in that session and it becomes reachable.`); the footer gives a note one
line, so at **80 columns** — the size §16 calls the one to hold, not a degraded case — it was cut after
`so there is nothing for the hub to`, losing `/background` entirely. Keeping the label and losing the
action is the oldest defect class in `docs/known-issues.md` (S1, class L2), and §16's own rule is that a
status with no remedy is a bug report sent to the wrong person. So the kind and the remedy lead, the
explanation moves into `wakeable`'s comment, and a test bounds the sentence at 78 columns.

The kind is quoted from the row rather than described, because the gate is `Command == "background"` and
§17's own rule is to tolerate words this fleet has not shown — a row whose kind is empty or a future word must
not be told it is "interactive", a claim the hub never measured. And it says *Claude gives no id*, not *the
hub has no id*: `agents.Parse` back-fills `ID` from `SessionID[:8]` (`internal/agents/agents.go:111-112`), so
the hub does hold one, and a gate written `ID != ""` reads correctly and fails at runtime.

**The tests** are the plan's (Task 5); what belongs here is their LIMIT. The biconditional is tabled over a
CAPTURED real listing, reading the literal listing word and never `agents.Attention()`, with a floor on rows
— and a frozen capture can only fail if somebody edits the fixture, so it cannot see a future CLI where an
interactive row carries an `id`, the one event that would make the gate wrong. The refusal is pinned by a
frame assertion on the string `View()` returns at 80 columns, **paired with an accept pole** (§22.3), since
today's tree already refuses every agent row with a sentence of the same shape
(`internal/ui/attach.go:37-38`).

No marker column is added for "has a door". §22.8 changes that decision's JUSTIFICATION rather than its
answer: the fact is no longer constant per host — two pane-less rows on one host now differ — but the kind is
visible in the tile, the row format drops that column, and the refusal names cause and remedy at the moment
of pressing. One of the two reasons design.md gives is measurably false and is on the correction list:
`hintFor(pathRefuse)` falls to `default` (`internal/ui/possession.go:285-299`), so the header hint carries
nothing about the refusal — and the surviving reason is weaker than it sounds, since the tile is the FOCUSED
row only.

### 22.9 The decisions that are the operator's

**All six are now settled: four answered by the operator on 2026-08-16, two struck by measurement.** They are
kept as questions with their costs, because the answer to each is a CHOICE and a reader who meets only the
outcome cannot tell what it was chosen against. §22.6's terminal-row filter was a seventh, answered and then
replaced the same day by the ordering rule — that history is in §22.6.

1. **Should `a` confirm only when the listing's word is `failed`, and otherwise go straight through?**
   **ANSWERED: yes.** Waking a live or settled row is cheap — one call, 0.33–0.95 s locally — while waking a
   reaped one walks into an abandoned turn. Answering *no* means confirming every press, which trains the
   operator to press through the one dialog that matters. Two measured costs accepted with it: the gate is
   blind to a session reaped while BLOCKED, and it is one-directional on four rows (§22.3).
2. **Does the host cost gate the dialog independently of `failed`?** **ANSWERED: no — it is a LINE in the
   dialog and never a reason to open one.** This answer walks into a hole the question had already measured,
   and the hole is named here rather than left for the implementer to find: an `up-empty` host has no tmux
   panes, so every Claude row on it is pane-less, and a `working` or `done` row there is exactly the case
   decision 1 sends through with no dialog — so the tmux server the keypress starts on someone else's machine
   would be named **nowhere**. §5's own offer to start a server is the only other action that does this, and it
   asks.

   **The resolution keeps both halves and costs no keypress: the note names it AFTER the fact.** The dialog
   stays as decision 1 leaves it, and a press that took a host from `up-empty` to `up` reports
   `made <name> on <host> — <host> had no tmux server, so this started one`. Nothing is confirmed that the
   operator did not ask about, and nothing the hub started is unannounced. This is the only clause in §22.9
   that is a design decision of the author's rather than the operator's, and it is marked so it can be
   overruled in one line.
3. **Is `claude stop <id>` offered?** **ANSWERED: no — and so the refusal has to say the whole truth.**
   The verb exists on both versions with identical usage, so declining it is a policy, not a capability gap,
   and the policy is deliberate: the hub does not get a destructive power it does not need. The cost accepted
   with it is that after §22 `n` launches a background session, `a` reaches it, and NOTHING in the
   hub ends one — so `K` on a pane-less row must refuse with two sentences, because one is wrong for half the
   population:
   - background: `<name> has no pane to kill, and the hub does not stop Claude sessions — run "claude stop <id>" on <host>; its conversation is kept, and a reopens it.`
   - interactive: `<name> has no pane to kill, and the hub has no id to stop it with — end it in the terminal that holds it.`

   `K` refused nothing when this was written: `confirmKill` (`internal/ui/lifecycle.go:109`) and `killSelected` (`:202`) had
   no `Kind` guard at any layer — `lifecycle.go` contains no occurrence of `KindAgent` — and the kill issues
   `kill-pane -t agent:<shortid>@<hash>` (`internal/tmux/lifecycle.go:97`), which tmux parses as session
   `agent`, with the failure counted into `killMsg.failed` and no reason shown. Either answer puts the `Kind`
   guard BEFORE the confirmation.
4. **Does a `failed` agent row get a dismissal?** **ANSWERED: no.** Under `--all` a `failed` row never leaves
   the listing, so it is a permanent resident at rank `Error` — 4 of 31 rows locally on 2026-08-15 — in an
   inbox §16 sizes at 24 lines. *Yes* would need a key and a store, and `x` is taken: §22.5 refuses hiding for
   a pane-less row. *No* accepts that reaped work stays high in the inbox until the daemon forgets it, and what
   makes that bearable is the SEVENTH answer rather than this one: the ordering rule of §22.6 sinks a row as it
   ages, so a `failed` row from two hours ago sits below one from two minutes ago without anything being
   hidden. If that turns out not to be enough, this decision is the one to reopen, and the cost is one key and
   one persisted set.
5. ~~**Does the wake confirmation name the pid it cannot kill?**~~ — **withdrawn by measurement.** An earlier
   draft recommended *yes, with its host*, on the strength of the orphaned `sleep 300`. No source carries a
   usable pid — `state.json` has none at all, and the listing's `pid` is non-null on 3 of 25 local background
   rows and on none of the 7 it calls `working` or `blocked` — so the dialog names the class and the host
   instead (§22.3, cost 2). Struck rather than deleted, for the reason decision 6 gives.
6. ~~**Does `i` to an agent row confirm the way a pane send does?**~~ — **withdrawn by §22.4.** There is no
   `i` on an agent row to confirm. Struck through rather than deleted, because a silently removed open
   decision reads as an oversight and §7's session-target column sends a reader looking for it. The withdrawal
   is only real once §22.4's mark-time refusal ships: today `i` on an agent row is ACCEPTED.

**And a sizing rule, at its measured size.** N6 (`docs/known-issues.md:14`) sizes the dashboard as
`height - len(body)` with a body that CAN reach ~21 rows — 8 targets, 3 payload lines, 3 reasons, i.e. a
multi-target broadcast. `a` acts on one row, so this body is separator, header, one target, the cost lines and
a footer, and `height - 7` at 24 rows never approaches N6's ~15-row threshold: the cost lines are not added to
21. The reachable failure is the other one — every line goes through `lines.Truncate` with no marker, so
counting the cost lines at 80×24 passes while the first, which quotes the abandoned turn, is silently cut at
column 80. The body is sized from the room LEFT and the frame asserts that line's CONTENT.

### 22.10 Carried forward UNMEASURED

Stated so a reader does not mistake silence for coverage. Every item carries the probe that settles it; those
probes run on scratch sessions in an isolated `CLAUDE_CONFIG_DIR` and on a private tmux socket — the
operator's own sessions are never attached, woken, replied to, stopped or resumed. **One** item earlier drafts
carried here is now CLOSED and lives where it is load-bearing: `id` on `nuc`'s non-background rows (0 of 7,
§22.8). Two more are HALF closed and stay below with the closed half named: the file's `pid` (absent on the 25
local files, the 7 `nuc` files not counted) and the dropped-id shape (a bogus id measured, a stopped id not).

| unmeasured | the probe that settles it |
|---|---|
| ~~**Whether `claude attach` accepts `--debug-file`.**~~ **CLOSED 2026-08-16: it ACCEPTS it.** `claude attach deadbeef --debug-file <path>` answers rc=1 with the bad-id sentence and WRITES the file — 620 B locally on 2.1.233, 278 B on `nuc` at 2.1.224. So §22.3's payload may carry the flag, which is what it assumed, and `agents` remains the only one of the three that refuses it | nothing left to probe; the flag goes on §22.3's payload |
| **Whether `claude attach` starts, draws or refuses with NO client attached.** Every attach figure in §22 was taken with the verb in an interactive terminal, while the design runs it as the trailing payload of `new-session -d`, inside a pty no client has joined. **What changed on 2026-08-16 is that the vendor answers this itself:** `~/.claude/daemon/attach-journal/<gestureId>.json` records `attachMs`, `attachCold`, `interactiveReached`, `via` and `msgsRenderedAtFirstPaint` per gesture, so the probe needs no screen-scraping — and reading a FILE is what §22.5 already does, unlike the socket protocol §22.2 forbids. Three of the operator's own past gestures, read without touching a session: **613, 727 and 773 ms**, `attachCold` false 3/3, `via: spare` 3/3, `interactiveReached` true 3/3, `attempt: 2` 3/3, and first paint rendered the whole transcript (908, 2194 and 1363 messages, EXCEEDING the jsonl count on two of the three) | run the payload under `new-session -d` on a private socket and read the journal entry it writes rather than the pane |
| **What two attach clients do — VIEWING as much as TYPING.** `--resume` fails open against a live holder and `attach` was never asked; an earlier revision claimed the viewing half safe and is withdrawn (§22.1) | two concurrent attaches to one scratch session: record whether the second is refused and what each client sees, then type in both and read the transcript for the two-writer shape §22.1 measured on `--resume` |
| **What `claude attach` does with an id the roster has DROPPED — and this is the MAJORITY case, not an edge.** Measured 2026-08-16: **25** job directories under `~/.claude/jobs/` against **3** workers in `roster.json`, so **22 of 25** ids the hub can put under the cursor are on disk and absent from the roster. The 20 s `AgentInterval` staleness is the smaller half of the problem. The rc=1 refusal was measured with a BOGUS id, which is a third shape again | attach to one of the 22 under a scratch config, or `claude stop` a scratch session and attach to its id; record rc, stderr and latency |
| ~~**Whether OBSERVING starts a daemon.**~~ **MEASURED 2026-08-16, and the answer has two halves.** Under a scratch `CLAUDE_CONFIG_DIR` with no roster, `claude agents --json --all` starts NOTHING: rc=0, zero rows, and no `/tmp/cc-daemon-$uid/<hash>` directory and no new `claude daemon run` process afterwards. But the live supervisor on this machine — `supervisorPid` in `~/.claude/daemon/roster.json`, holding 3 workers — records its own origin as `claude daemon run --origin transient --spawned-by {"label":"claude agents",…}`, so on a config that HAS work the listing does spawn it. So the hub's 20 s poll is free on a host with nothing to supervise and can start a transient daemon on a host that has rows — which is every host the hub cares about. §5's property is not preserved, and the rule that no §22 claim may rest on the listing being free STANDS for hosts with work. The path formula is confirmed while we are here: the directory is `sha256(realpath($CLAUDE_CONFIG_DIR))[:8]`, computed `254924d8` for `~/.claude`, which is the live directory | still open: whether a transient daemon EXITS when idle, and therefore whether a 20 s poll respawns one per tick |
| **Whether the mandated call reports a pending Write-permission prompt as `working`.** The 136 s of silence came through the daemon's own `list`, the source §22.2 forbids | drive a scratch session onto a Write prompt and poll `claude agents --json --all` — and bare, which is what the hub calls until the `--all` commit lands — for the same duration |
| **Whether every waiting row is a `background` row.** The published "10 of 21 rows" carried that half and §22.1 withdraws it at the 45-row denominator; a probe keyed on the listing's `blocked` cannot see the counterexample class, since a Write prompt polls `working/active` | for each row whose listing `state` is `blocked`, print `kind`, over both hosts' `--all` listings; then repeat over the rows the tile would call waiting, so the pending-prompt class is in the denominator |
| **WHICH id an attach client puts in `CLAUDE_CODE_SESSION_ID`.** It decides whether the pane `a` creates MERGES with its own agent row or appears beside it (`internal/registry/registry.go`, the absorb branch in `UpdateAgents`); two rows for one session is a screen where one is killable and one is not. **Narrowed 2026-08-16 by reading `/proc`, no session touched:** the variable is real and an INTERACTIVE session exports it to its children (measured on a shell spawned by one, carrying that session's own uuid beside `CLAUDECODE=1` and `CLAUDE_CODE_ENTRYPOINT=cli`), while **none of the 9 live background processes carries it** — four `bg-pty-host`/`bg-spare` pairs and the daemon all have none of the session vars. So the walker will find whatever `attach` sets, and the open question is only which id that is | attach in a scratch pane, read the child's environ, and compare the value against the row's own `sessionId` |
| **Where the jobs directory lands under a non-default `CLAUDE_CONFIG_DIR`.** §22.5's whole tile reads from there | set the variable, dispatch one background session, locate its `state.json` |
| **The remote cost of `a`, and the WAKE behaviour on 2.1.224.** 0.33–0.95 s is local and the wake was one probe on 2.1.233; `nuc` carries 7 of the 32 background rows | time the full routing once against `nuc`, and SIGKILL a scratch worker there before attaching |
| **Whether a wake's transcript replay is billed.** §22.3's dialog authorises it and cannot price it | compare the account's usage immediately before and after one scratch wake |
| ~~**Whether any `state.json` lacks BOTH `needs` and `detail`.**~~ **CLOSED 2026-08-16: exactly one does.** Over the 32 files, one local job (`5a2ca44a`) carries neither, and every one of `nuc`'s 7 carries `detail`. So the tile's third rung has exactly one real instance out of 32 — it is not hypothetical, and it is not common | nothing left to probe; the rung stays |
| ~~**`pid` over the 7 `nuc` files, and how often `resumeSessionId` differs from `sessionId`.**~~ **CLOSED 2026-08-16.** `pid` is usable on **0 of the 7** `nuc` files, so the absent-`pid` result now covers **0 of 32** fleet-wide and the roster-liveness ruling rests on the whole census. `resumeSessionId` differs from `sessionId` on **3 of 25** local files and **0 of 7** on `nuc` — **3 of 32** — which is a RATE where §22.1's retirement of `--resume` needs only the categorical fact that the fields are separate | nothing left to probe |
| **What writes `reapedMidWorkAt`, and why it holds a LAST-ALIVE time.** The 06-21-against-a-07-14-reap gap is measured; the mechanism was asserted in an earlier revision and never probed | dispatch a scratch background session, SIGKILL its worker, and read `state.json` before and after the next daemon start, recording which write moves that field and which moves `firstTerminalAt` |
| ~~**What the LOCAL background rows carry, `state` or `status`.**~~ **CLOSED 2026-08-16:** local background rows carry `state` **25 of 25** and `status` **3 of 25**. With `nuc`'s 7/7 and 0/7 and the third host's 25/25 and 0/25, `state` is present on every background row measured anywhere — 57 of 57 — and `status` is the interactive word plus a minority of background rows on one host | nothing left to probe; §17's table carries the per-host cells |
| **One row shape that could not be reproduced:** `state == "failed"` carrying NEITHER reap field, where the CLI refuses and exits 1 | construct it deliberately under a scratch config dir, or record it the first time the fleet produces one |
| ~~**The third `claude`-bearing host.**~~ **CLOSED 2026-08-16, and it changed four figures.** It is reachable, runs **claude 2.1.226** and **tmux 3.4**, and all three verbs are present with byte-identical usage — so the door is portable across THREE claude versions. `--all` is accepted with the contrast in the same run: bare **4** rows against **25**, the widest gap measured. Background rows carry `id` 25 of 25 and `state` 25 of 25, and it has no interactive rows. Its listing is **seven** keys, not nine, which refuted this document's "identical across the fleet's spread" (§22.6). The fourth machine has no claude installed and returned nothing for `claude --version` (design.md:187, :1922), so it is not a gap and no probe can close it | the tmux side is still open: 3.4 is a third tmux version and §20's epoch read-back was taken on 3.2a and 3.7b only |

**A rule this section earned rather than a gap it records.** The measurement that refuted §22.3's cost
sentence was taken by running `claude attach <id>` against a row of the operator's OWN live fleet. It woke a
finished session, `--reply-on-resume` made it start a turn nobody asked for, and stopping it again left the
row reading `failed` where it had read `done` — a small, permanent change to somebody's dashboard, made by a
probe. **So a door probe uses a session the prober created, or a bogus id.** The bogus id answers the
cheaper half of the question anyway: rc=1 with `No job matching '<id>'. Run 'claude agents' to list running
sessions.` is exactly the refusal shape §22.3 needs, and it touches nothing.

### 22.11 The registry key, and what `--all` makes true

**§22.6's own obligation breaks the row key, and the residue was written down before it happened.**
`registry.agentRowID` is `"agent:" + s.ID + "@" + hex(sha256(s.CWD)[:4])`
(`internal/registry/registry.go`, `agentRowID`) — unique by construction rather than repaired on collision — and the
comment above its call site closes: "two sessions sharing an id AND a cwd would still merge. That does not
occur on this fleet" (`internal/registry/registry.go`, `agentRowID`'s own comment). Measured 2026-08-15 by mirroring that function over the real
local listing:

| call | rows | distinct keys | rows lost |
|---|---|---|---|
| `claude agents --json` — today's producer | 13 | 13 | 0 |
| `claude agents --json --all` — what §22.6 mandates | 31 | **30** | **1, silently** |
| `--all` minus `done` | 17 | 17 | 0 |

So that sentence is now **false**, because of the flag §22.6 requires. **The ruling: `Kind` joins the key.**
(And a SECOND ruling was added on 2026-08-17, below: the two records that made the collision are folded
before they reach the key, because they are one conversation. The key still needs `Kind` for every pair
the fold does not touch.)
The colliding pair is one row `kind: background`, `state: done`, `id: 3ec21f39` against one row
`kind: interactive`, `status: idle`, carrying no `id` — so `agents.Parse` back-fills `3ec21f39` from
`sessionId[:8]` — with the SAME cwd; `Kind` is the one field that differs and the row already carries it
(`internal/registry/registry.go`, `p.Command = s.Kind`). **The argument for `Kind` over a longer id, over the name and over detect-and-suffix
lived in this section's implementation plan** (an internal build artefact, not published; Task 2) with the test written out,
and is not repeated here; `git grep '§22\.11'` over design.md returns nothing, so nothing outside this
document depends on the argument sitting in it.

Two things this document keeps. **The provenance of that table is a pure function, not the product**: it
mirrors `agentRowID` and never ran `UpdateAgents`, so the branch that decides whether the collision happens at
all is unexercised — the two rows share their FULL `sessionId` (31 rows, **30** distinct sessionIds), and
`internal/registry/registry.go`, the absorb branch in `UpdateAgents` absorbs any listing row whose `sessionId` matches a polled pane's `ClaudeSession` and
`continue`s, so if that interactive session sits in a tmux pane the hub polls, both rows take the pane branch,
no agent row is created for either, and the background row — the only one with a door — leaves the agent
population entirely. That is a second defect with the same cause, and `Kind` in the key does not touch it. And
**`Kind`'s own cost**: the kind is Claude's, §22.8's refusal tells the operator to change it by typing
`/background`, and when they do the key MOVES — the old row is deleted rather than kept (`internal/registry/registry.go`, the agent-row deletion in `UpdateAgents`),
the new one is a first sight so `StateSince` resets, its selection entry is orphaned, and the state log's
history splits into two identities. Accepted, because the two rows are genuinely different subjects and the
transition is rare, operator-initiated and announced by the operator's own keystroke.

**What the test must count** — the test itself is the plan's. `agentRowID` lands in `Key{Host, PaneID}` at
`internal/registry/registry.go`, `agentRowID` and `registry.Pane` has **no `ID` field**, so a distinct-key count must count **`PaneID`**;
`r.panes` is a map, so a row count IS a distinct-key count and `rows in == rows out` is the arithmetic that
caught N4 ("12 sessions in, 11 rows out"). The repo has solved this shape once: `project.AliasKey` carries
`Kind` "so a pane row and an agent row **can never** collide however their fields line up"
(`internal/project/alias.go:30-31`) — a key of five fields, `{Kind, Host, Session, ID, CWD}`, whose agent
branch is `{Kind, Host, ID: p.SessionID, CWD: p.Path}` at `:44`. Two artefacts key on the PaneID, not four:
`internal/hub/statelog.go` and the selection (`internal/ui/model.go:1183-1185`) both take
`registry.Key{Host, PaneID}`, while `hide.KeyOf` keys on the session NAME.

**One commit, and it carries the prose.** The key change, the `--all` change, the two new `Pane` fields
(§22.3) and the correction of `internal/registry/registry.go`, `agentRowID`'s own comment land together, with N4's residue sentence in
`docs/known-issues.md:175` corrected in the same breath — because a comment asserting that a collision does not
occur on this fleet is worse than no comment at all: it is what stops the next reader checking. The plan's
Global Constraints carry the gate list; the baseline it lands on, measured on `a4a08e2` with a clean tree, is
18 packages with unit rc=0 **ok=18, FAIL=0, no-test-files=0**; `go vet ./...`, `-tags e2e` and `-tags mockup`
all rc=0; `-race` rc=0 with 0 `DATA RACE`; e2e rc=0 **PASS=53 FAIL=0 SKIP=1**, the one skip being
`TestE2EARemoteHostIsPolledOverOneMaster` with `HUB_E2E_HOST` unset — the ONLY remote case, which with
`HUB_E2E_HOST=nuc` passes in 3.94 s and leaves the far host clean: `no server running on
/tmp/tmux-1000/hube2e`.

**One conversation can be reported TWICE, and then it is one row.** Reported from real use on
2026-08-17 — `xmap-universal-reader` appearing twice — and measured: `claude agents --json --all` gave
session `7ef2fe7e-c88d-46da-ac9d-0b57f170c3d8` two records on one host, `background` carrying
`id: 7ef2fe7e` and `interactive` carrying `id: None, state: None`, at the same cwd and under the same
name. §22.11's ruling kept both, because its concern was LOSING one; the operator's concern is seeing
one thing twice, and the interactive twin carries nothing to act on — no id, so no door and no argument
for any `claude` verb, and no state word, so it renders `? unknown`.

So the records are folded before they reach the key, keeping the one with an ID, and the key goes on
doing its job for everything the fold does not touch. The fold's key is **(sessionId, cwd, name)** and
not the sessionId alone, and that is the whole care in it: §22.11 also measured two records under one
sessionId with DIFFERENT cwd and name, which are two genuinely different sessions, and collapsing those
is the silent loss the key was changed to prevent.

**The door's own pane was the third row, and the join that should have absorbed it lived only in
memory.** `Adopt` records pane → session id in the Keeper, so the run that pressed `a` folds the row;
every run after it does not, and the operator then sees the session twice — once as the pane, once as
the listing row. Two facts on the SERVER survive a restart, and the payload is the precise one:
`#{pane_start_command}` still reads

```
"'sh' '-c' ''\''claude'\'' '\''attach'\'' '\''7ef2fe7e'\''; s=$?; [ \"\$s\" -eq 0 ] || { … }'"
```

so the id in it is the argument the door passed. `UpdateAgents` indexes live panes by that id and
absorbs on it when the uuid join finds nothing, writing the uuid onto the pane so everything else keyed
on it — a favourite, `R`, the history — finds the row afterwards. The match is ANCHORED at the start of
the payload, because a pane that merely mentions the verb (a shell where somebody typed it, a `claude
logs` on the same id) must not swallow a listing row; and tmux's OWN double quotes are stripped along
with the two shells' single ones, which the first version did not do — it passed a unit test whose
fixture had been quoted by hand and changed nothing on the fleet. Measured after: the reported session
went from **three rows to one**, and it is the PANE that survives, which is the row that can be
attached to, sent to and killed.

### 22.12 When `~/.claude` is shared, a session belongs to no single host

**Measured on this fleet, 2026-08-17, and it breaks the model every row above rests on: the operator's
`~/.claude` is SHARED between two machines.** `~/.claude/daemon/roster.json` and
`~/.claude/jobs/<id>/state.json` are byte-identical on `local` (`cachyos`) and on `side-desk` — same
md5, different `/etc/machine-id` — so `claude agents --json --all` returns the SAME 26 sessions on
each host, with identical `sessionId`, `startedAt`, `name` and `cwd`. §17's producer keys a row per
host, so the dashboard showed **71 rows for 45 things**, every background session twice, and one
session named `20260817-cicd` appeared FOUR times because two different sessions carry that name.

**The two rows disagreed, and the disagreement is the signal.** Liveness is machine-local: the roster
names `workers["30f3382b"] = {pid: 149575, ptySock: /tmp/cc-daemon-1000/…/pty/30f3382b.sock}`, and
`supervisorPid` 149549 was alive on `side-desk` and absent on `local`. So the same session read

```
agent:30f3382b@ee42d26c  local        state failed    ← the store says it runs; no such pid here
agent:30f3382b@ee42d26c  side-desk    state working   ← the worker is on this machine
```

**`failed` is the only word a host produces about its OWN ignorance.** Every other word is read out of
the shared store and is therefore identical everywhere — measured over all 26 sessions on both hosts
in one round: 16 `done`/`done`, 5 `failed`/`failed`, 5 `blocked`/`blocked`, zero disagreements once the
live one had settled. Neither listing distinguishes owner from stranger by itself: `claude agents
--json` without `--all` returns 6 rows here and 5 there and shows the same `blocked` sessions on both,
because a session killed while blocked leaves a `state.json` no reader can tell from a live one
(§22.3 measured that reap).

**The ruling: rows are collapsed by their PaneID, and the collapse needs no new identity.**
`agentRowID` hashes the short id, Claude's kind and the cwd and mentions no host, so two AGENT rows
sharing a PaneID are the same session by construction — which is also what keeps a background job and
its interactive continuation in one cwd as two rows (§22.11), and two sessions differing only in cwd
as two. PANE rows are excluded and that is the load-bearing half: a pane id is unique only within one
tmux server, so `%1` exists on every host and grouping panes this way would collapse the whole
dashboard onto one machine. Measured after: **44 rows, 41 agent rows, 0 PaneIDs on more than one
host**, 26 of them carrying `also_on`.

**Which host the surviving row names decides where the door knocks**, so `failed` loses to any host
that can see the session alive, and among claims that agree the FLEET order wins — the operator's own
preference, local first, so a wake goes next door rather than across an ssh leg. Attributing the row
to the `failed` host would send a wake to a machine with no worker while another machine runs one,
which is two workers against one transcript. Three consequences worth stating:

1. **The fleet order has to be given to the registry, not learned.** `TickAgents` fans out
   CONCURRENTLY, so the order `UpdateAgents` is called in is a race — measured with rank learned on
   first sight, a remote host won all 26 shared rows while `local` was polled first. `SetHostOrder`
   is called before the fan-out, and the comparison ends in a label compare so that two hosts the
   fleet does not name cannot decide a row by map iteration order.
2. **A shared store is consistent only EVENTUALLY, and the rule prefers the stale claim.** Right after
   `claude stop`, `local` said `failed` from a re-read roster while `side-desk` still said `working`
   from its unsynced copy, so the row read `works` for a session already gone. Deliberate: a stale
   `works` corrects itself on the next sync, while preferring `failed` hides live work for as long as
   the work runs, which is the defect the operator reported.
3. **`registry.Pane` gains `AlsoOn`**, the other claimants in fleet order, carried into `--status` as
   `also_on` — without it a reader cannot tell a session that lives on one machine from one a shared
   home makes visible on several, and the host the row names is only the one the hub chose.

**AND THE SAME RULE HAS TO REACH A PANE ROW, which it did not.** The dedup above protects a row that has
no pane: `beats` prefers the claimant that is not `failed`. A session WITH a pane takes a different path —
`UpdateAgents` folds the word onto the pane row, once per host, with the last host to answer winning — and
that path had no preference at all. Measured on the operator's fleet: `~/.claude` is shared with
`side-desk`, whose listing carries every session including the ones whose worker runs here, and it called
**all three live sessions `failed` while agreeing with local on all 26 finished ones — 3 of 3 against 0 of
26**, because `failed` is decided by looking for a pid in `roster.json` and a pid means nothing on another
machine.

What the operator saw, and reported: `seedtool-development`, whose own screen read `✻ Waiting for 1
dynamic workflow to finish` and whose own machine reported `working`, rendered as **`error`** — fifteen
times out of fifteen with the remote host in the poll, and `works` in the same code with only the local
host polled. A session with a workflow running looked like a failure on the one screen that is supposed to
say what wants the operator.

**AND THE RULE IS ABOUT WHO SPEAKS, NOT ABOUT WHICH WORD.** The first version refused `failed`
specifically, which fixed the instance in front of it and left the class open: the very next report was
`billing-cicd`, answered by the operator seconds earlier, whose own machine said `working` with a pid while
the pid-less host still held `blocked` from before the answer — so the row read **`needs` while the session
worked**. Same shape, different word.

The discriminator is in the listing and needs no threshold: **a record carries a `pid` exactly when the
host reporting it can see the worker.** Measured across the fleet — on the owning machine every `working`
session carries one (6 of 6) and every finished one carries none; on the host that shares `~/.claude` and
runs nothing, NOT ONE of 31 records carries a pid, and it reported `blocked` or `failed` for six sessions
the owner reported `working`. So a record without a pid is a claim about the STORE and one with a pid is a
claim about a process. `betterClaim` compares the pid first and keeps the `failed` clause underneath it for
the case where nobody has one — a finished session, where the two claims differ only in when the store was
read.

The fold refuses any claim that would bury the owner's FROM THE SAME ROUND, through the same predicate the
dedup asks, so the two paths cannot drift again. Scoped to one round on
purpose: when the session really ends, every host says `failed` in the next round and the row follows —
which is asserted beside the two-order case, because the agent polls fan out concurrently and a test that
fixes one order proves nothing about the race. Verified on the live fleet: 8 of 8 `works` after, against
15 of 15 `error` before, with exactly one row in 58 changing.

**`AgentWord` lands in the same commit, and the dedup is why it cannot wait.** `state.FromWord(
s.Attention())` maps `failed` AND `error` onto `state.Error`, so a rule written against the folded
state would fire on a session whose WORK errored as readily as on one whose worker is missing. The
field carries the listing's word unfolded, with both producers named — the agent branch and the
absorb branch — and one test each that goes through the producer.

## 23. The fleet as a filesystem — the screen the hub opens on

**THE FILESYSTEM VIEW (`t`): hosts are volumes, directories are directories, sessions are files.**
Asked for in those words, and it is more expressive than the flat model rather than a rearrangement of
it: today a "project" is the LAST PATH SEGMENT of a row's cwd (§21.12), so the hierarchy above it is
thrown away. Measured on the operator's fleet, `st-edgebox`, `frontend` and `tundra-security-server`
are three unrelated labels that are one family — `~/lab/streams/st`, 14 sessions of which 5 want the
operator — and that node does not exist in the flat model at all.

It is a MODE and not a third `Grouping`, for the reason Grouping's own definition gives: a grouping
changes the headers and never the ORDER, and this changes the order into a hierarchy. `t` opens it and
`t` or `esc` leaves — and both clear the NOTE, because a sentence set on one screen is read on the
next, where it is false.

**The head line is the screen's ONLY legend, and it is a priority list.** It was one sentence handed to
`Truncate`, which at 60 columns read `enter opens, a goes to what wait` — cut mid-word, unmarked, which
is the oldest defect class in this document (keeping the label and losing the action). Through
`lines.Fit` the order is what must survive first — the census, then the two keys that get the operator
OUT and IN, then the rest — and the drop is marked: measured, 60 columns says
`7 sessions · 4 asking · enter opens · esc leaves +4`, 80 adds `a goes to what waits` and says `+3`,
100 adds `n new session here` and says `+2`, and 120 carries all eight parts. The separator is the
footer's own `" · "` and not a wider one: at the 80 columns §16 commits to, five columns per gap costs
this line the whole `a goes to what waits` clause. And there is no NOTE duplicating it — `t` used to
set one naming these very keys, which cost the footer the fleet's health at 60 and 80 columns, the M1
defect paid to repeat a line already on the screen.

**The order inside a node is ATTENTION, not the alphabet**, and that is the one judgement this design
makes against the metaphor: files in a directory do not reorder themselves, and this screen exists to
put what wants the operator first. A node's children come in `project.MoreUrgent` order — extracted
from `Summarise`'s sort closure for this — and its sessions in `registry.SortByAttention` order, so the
tree, the project list and the dashboard answer "which of these first" by calling the same functions
rather than by three comments agreeing. Node counts are `project.Summary`, rolled up over the subtree,
so `waiting` cannot come to mean two things on two screens.

**The tree makes the viewport arithmetic SIMPLER**, which is half the reason it is worth building.
Every drawn line is exactly one thing — a node or a row — so the window is a slice and `A` selecting
the rows in it cannot disagree with what was painted. The grouped list needs `extraAbove`,
`rowPrefixCost` and two calibrated guards to keep the orphan-prefix class out; a tree cannot have that
class.

Five decisions, each measured and each guarded by a test a mutant kills:

  - **a VOLUME never collapses.** The first version collapsed the host too and printed
    `nuc/home/dev/lab/streams/qa/ansible` as one label — the machine and the directory in one string,
    which is the confusion `local/tmp` against `nuc/tmp` already cost the operator.
  - **a chain with no branching collapses to one line.** Their fleet is four levels deep before
    anything branches and 17 of 30 projects hold one session, so without this more than half the tree
    is ceremony over a single leaf — the objection that nearly ruled the view out as a default.
  - **HOME folds to `~` on the LOCAL volume only**, because `~` elsewhere is that user's home; the
    volume is found by `Host.IsLocalServer`, the same question the launch form asks before resolving a
    `~` at all. The guard got this wrong twice, both found by looking at the printed tree rather than
    by reading: it tested the rows of the HOST, where a pathless session legitimately hangs, so it
    never fired; and it replaced the host's whole child map, which would have dropped every other tree
    on that volume.
  - **a pinned row appears ONCE**, under FAVOURITES, and its directory's count drops it — two lines for
    one session would be the duplicate already reported on the flat list, with two cursor positions
    acting on one thing. A directory left with nothing then disappears, which is right: the hub lists
    the FLEET and has never listed empty directories.
  - **directories carry a trailing slash**, `ls -F` style, and that is a disambiguator as well as a
    convention: the state glyph for `idle` is `▸`, the same character a closed node uses, so
    `▸ idle tmp-1e` and `▸ ~/lab/streams` began identically.

The cursor is keyed on the LINE and not on an index, because a node opening under the operator's hand
changes every index below it — the defect the dashboard's cursor already had. `treeTo` is its only
writer. Forcing the volumes open was tried and removed: it made `enter` and `h` on a volume do nothing at
all, and a key that silently fails is what a broken key looks like.

**THE FIRST PAINT OPENS THE MAP AND SHUTS THE FOLDERS**, and that is the decision that makes this screen
usable as a default. A node with child directories is open, so the shape of the fleet is on the screen; a
LEAF — a directory holding sessions and no directories — is closed, so its sessions are one key away. The
operator asked for exactly this in their own words: the pinned band "straight away", and "everything else
by navigation". The pinned band is the deliberate exception and is always open, because a band of
finished favourites is still the answer to "which sessions do I care about" where a directory of finished
work is not.

Measured on their own fleet at 120x40, which is what settled it. Everything-open drew **54 session rows
under twenty nodes and six levels of indentation**: the screen showed a quarter of the fleet, and neither
of the other two volumes was on it at all. Opening "whatever has waiting work" was tried next and is a
weaker version of the same idea — it opened the leaf holding two asking sessions AND the six finished
ones beside it, which is the screen the rule exists to avoid. The rule that shipped draws every volume
and every directory in about twenty lines, with the counts saying where the work is:
`▸ st-edgebox/  ⚑ 2  of 8` beside `▸ frontend/  ⚑ 2  of 2`.

Nothing is more than one key away, and that cost the `a` key a fix: it used to open the node ONE level
and scan the drawn lines for a waiting row, which found nothing the moment the folders were shut — the
key silently doing nothing, on the gesture the head line advertises on every frame. It takes the target
from the FLEET now (`panesUnder`, the same function the directory tile uses, so the screen cannot name a
session the key refuses to reach) and opens the path FROM the target: the node keys are built as
`host:` and `parent + "/" + segment`, so the target's own path prefixes ARE its ancestors' keys and
`openPathTo` needs no tree walk. The first press then seeds the expansion set from what is on the screen
rather than from all-open, or the first `h` would collapse a tree the operator never expanded.

**The expansion set holds only what the OPERATOR decided**, and an absent key means "whatever the rule
says". It used to be SEEDED on first use so that a first `h` would close one node instead of collapsing a
tree nobody had expanded — and the seed was built from the whole fleet while the screen draws the narrowed
rows, so with a filter on it held eight entries for a tree drawing two nodes. A review found the
divergence; making absence mean the default deletes the seed rather than aligning it, which is the shape
this document prefers: two sources of one answer is the coupling, and the property the seed existed for
survives.

**What a frame costs, measured, so the next efficiency argument has a number.** This document's standing
figure — a whole `View()` at 60 µs on a 45-row fleet, against a 250 ms paint interval — is now true of the
FLAT screen only. On a fleet shaped like the operator's (54 sessions, three volumes, six levels, twenty
directories) `treeLines` is 103 µs and `RenderTreeScreen` 74 µs, so a tree frame is about 180 µs: three
times the flat screen and 0.07% of the interval. `treeLines` is the term to watch and `internal/ui/
treebench_test.go` is where to watch it. Memoising `rollUp` for the life of one build took it from 137 µs,
which is the only optimisation of this branch that paid for itself — the child sort was asking the same
subtrees for their totals O(n log n) times.

**A DIRECTORY UNDER THE CURSOR GETS A TILE** (`nodeTile`), because the cursor is on a directory for most
of this screen's lines and the band was blank for every one of them — three of twenty-four rows at the
committed size, on the screen the hub opens on. It is not `RenderTile` and must not be: that tile's body
is a copy of another program's screen and its head names a pane, and a directory has neither. What a
directory has is an ADDRESS and a ROLL-UP, plus the names of the sessions inside it — taken from the
fleet rather than from the drawn lines, so a CLOSED node names them too, which is the whole permission to
close one. The path is worth a line of its own because the label above is folded (`~`) and collapsed
(one line for three nodes) while a launch needs the real directory. Marked panes still win the band:
§7 requires a send's target to be a tile the operator can see, and a folder summary must not stand in
front of it. Two smaller things the tile taught: `… and N more` was short by one, because the row the
tally replaces stops being listed; and its session lines go through `rowIdentity` like the list's rows do,
after the pinned band's tile listed `ansible-ci-ops` twice with nothing to tell the two apart — which is
the report this whole screen came from.

**The census on the head line is the FLEET's**, not the screen's. It counted the drawn rows at first, so
closing a directory took it from `54 sessions · 13 asking` to `3 sessions · 1 asking` — the operator's own
fold reported as work going away, on the one line that says how much there is. A closed node is the point
of this screen, so a count that shrinks when you close one is a count nobody can navigate by. This is the
same defect the hidden-row counts had, where a mode answered by returning a zero.

**IT IS THE DEFAULT, and `--view=flat` is the way back.** The operator asked for it as one and the
conditions they named are met: counts on nodes, single-child collapse, and `a` reaching the
longest-waiting row inside a closed node. The flag exists because a default is not a deletion — the
flat list is the screen every existing test drives and the one an operator with sixty sessions in one
directory may still prefer, so `--view=flat` selects it for a run and `t` toggles between the two
within one. The e2e suite pins the flat view explicitly on every case that predates this screen, which
is what makes a failure there a statement about the case rather than about the default.


### 23.1 An overlay returns to the screen that raised it

A second screen that can raise an overlay makes every overlay's exit a question it was never asked.
Each of the five exited to `modeBrowse` BY NAME, which was correct while the dashboard was the only
screen that could raise one, and a silent teleport the moment a second could: `i` on the filesystem
view opened the composer, `esc` kept the draft as promised, and the operator was standing on the
dashboard with the tree gone and nothing on screen saying why.

The mechanism is two functions and a predicate, and the reason it cannot be got wrong is that it takes
no argument:

  - `raise(overlay)` records the screen as `m.underlay` and sets the mode. The screen is `m.mode` at
    the moment of the keystroke, so there is nothing for a caller to pass and therefore nothing to
    pass wrongly — the shape this design already reached for when a cursor write forgot its hint.
  - `dismiss()` returns to `m.underlay`.
  - `isOverlay()` is the list of modes that are drawn OVER a screen, and the PICKER is one of them:
    its own renderer draws a base above the rule and its comment says so, so it was reachable from the
    project list and from the filesystem view while returning the operator to the dashboard. Two lines
    of the project list's own keys said `m.mode = modeBrowse` before raising the picker and the launch
    form — the only coherent thing to do while every overlay exited by name, and a teleport off the
    list once `dismiss` existed. Both are gone.
  - `isOverlay()` is also what makes `raise` safe to NEST. The composer's `enter` raises the confirm
    dialog, and the screen to return to afterwards is the one under the COMPOSER, not the composer —
    so `raise` leaves the underlay alone when the current mode is itself an overlay. `dismiss` also
    refuses to land on one, asserted rather than trusted, because an overlay dismissed into an overlay
    is a screen with no way out.

**The PAINT needed the same answer, and it was a separate defect.** Every overlay renderer built its
background by calling `Render` — the dashboard — so an overlay raised from the tree drew the flat list
underneath it. One function answers it now for all five: `backdrop(f)` switches on `Frame.Screen`, the
screen the overlay was raised over, and the Frame carries what each of those screens needs to draw
itself (`Tree` and `TreeCursor` for the filesystem view, a `ProjectView` for the project list).

`Frame.Screen` was a BOOLEAN called `OnTree` for an hour, and an adversarial review found the live cost:
the project LIST raises the naming overlay too — `N` on a project — `raise` recorded `modeProjects`
correctly, and `backdrop` read every non-tree screen as the dashboard. So naming a project replaced the
list the operator was reading, including the row being named, with a list of sessions, while `dismiss`
afterwards returned them to the list: the picture and the return disagreed. A boolean can only name one
screen. The lesson is the one this document keeps relearning — a mechanism that admits every case beats
a flag for the case in hand — and the pre-rendering shortcut is refused for a measured reason: each
overlay asks for its background at a REDUCED height, so a backdrop rendered once at full height and cut
to fit would lose the underlying screen's own footer, which is the M1 defect. The keyword field has no renderer of its own at all — it is painted in the screen's
own footer from `Frame.Searching` — so for that one mode the whole frame is chosen by the underlay,
which is why `View` computes the screen once and dispatches the paint on it.

The commit path needed it too, not only the cancel path: naming a session from the tree and SAVING it
used to return to the dashboard while cancelling returned to the tree, so committing cost the operator
strictly more than abandoning — the same inversion this document recorded when cancelling a project
name threw the operator off the list.

### 23.2 The keys, and why most of them are not new code

Everything that acts on a SESSION reuses the dashboard's own function with the tree's subject. The
subject is the only difference, and it is a real one: the dashboard's cursor is an index into
`rowsForScreen` and the tree's is a line of a hierarchy, so `cursorRow()` on this screen names a row
the operator is not pointing at.

| Key | What it does here | Where the rule lives |
| --- | --- | --- |
| `j` `k` `enter` `l` `h` | move, open, close, go out | `treeKey`, and only here |
| `a` | on a row, go to it; on a node, open it and land on the longest-waiting row inside | `goTo`, shared |
| `space` | mark the row under the tree's cursor | `mark`, shared |
| `A` | select the rows this screen PAINTED | `treeRowsOnScreen`, built from the paint's own two functions |
| `C` | clear the selection | inline, two fields |
| `i` `!` `R` `K` | act on the SELECTION | `selectionKey`, shared |
| `x` | hide the selection, or the cursor's row | `hideSubjectsFrom` + `hidePanes`, shared |
| `X` | show hidden rows, marked | one field |
| `f` | pin the cursor's row | `toggleFavouriteSessionOf`, shared |
| `N` | name the cursor's row | `openNaming`, shared |
| `n` | create a session HERE | `openLaunchFormFor`, shared |
| `/` | the keyword field, over this screen | `openSearch`, shared |
| `q` | quit | `tea.Quit` |
| `esc` `t` | leave for the dashboard | `treeKey` |

Three of those rows earned their shape:

1. **`A` asks the paint's own arithmetic.** This document has twice recorded a viewport estimate that
   disagreed with what was drawn — the operator marked half of what they could see. On this screen the
   question is trivial because every drawn line costs exactly one row, so `treeRowsOnScreen` calls
   `treeListHeight` and `windowStart`, which are the two functions `RenderTreeScreen` itself calls,
   and the assertion is `count == drawn` rather than a bound.
2. **`n` PRE-FILLS THE DIRECTORY**, which is the gesture the metaphor was asked for in the operator's
   own words: creating a session in a project should set the path by itself, with the field still
   editable. On a node it is that node's REAL absolute directory — `nodeAddress` reads it back from
   the node's key, and the `~` fold relabels a node without rekeying it, so the pre-filled path is the
   one tmux needs rather than the one the screen draws. On a session row it is that row's own cwd, and
   the tmux session to put a window beside comes from `paneSessionTarget`, which answers `""` for an
   agent row because Claude's uuid in `SessionID` is not a tmux `$N`.
3. **`q` quits from here, and that is not optional.** This is the first screen an operator sees, and a
   `q` that did nothing on it would leave them with no way out of the program without knowing `esc`
   first.

`i` `!` `R` and `K` needed no subject from this screen at all: each reads the selection and refuses an
empty one with its own sentence. Sharing them is the point — a second copy of "exactly one selected
row" or of the mixed-selection refusal is a second chance to get one of them wrong, and this document
has a whole entry about two predicates that answered "is the list narrowed" with different clause
lists.

**Every key that cannot act says so.** A directory line refuses `space`, `x`, `f`, `N` by naming what
it is and what to press instead, and the favourites band refuses `n` because it is not a directory. A
key that silently does nothing is what a broken key looks like, which is the rule the project list
already follows.

**What the tree deliberately does NOT carry:** `h` here means "close, or go out" — the vim reading —
so the history log stays a dashboard key, and `p`, `P` and `v` stay there too. `v` cycles a GROUPING of
a flat list, which this screen has replaced with a hierarchy; the other two are one `esc` away. The
head line names the keys that exist here, so nothing is hidden by being absent.
