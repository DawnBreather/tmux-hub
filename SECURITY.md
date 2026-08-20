# Security

## Reporting a vulnerability

Report privately through GitHub's [security advisories](https://github.com/DawnBreather/tmux-hub/security/advisories/new)
rather than in a public issue. Include what you ran, what happened, and the version
(`tmux-hub --version`). Expect an acknowledgement within a week.

## What this program can do, so you can judge the blast radius

`tmux-hub` reads and writes real terminals. Three things are worth knowing before you point it
at anything you care about:

- **It types into panes.** A broadcast pastes your text into every selected pane and submits it.
  Each write is guarded by that pane's own identity token, so a pane that stopped being the
  target you selected is refused rather than written to — but a pane that is still itself will
  receive whatever you send.
- **It runs `tmux` and `ssh` as you.** Remote hosts are reached over an ssh ControlMaster socket
  you configure; the hub never asks for a password and stores no credentials. Anything it can
  reach is anything your ssh configuration can reach.
- **It never runs `tmux` without an explicit socket.** Every invocation carries `-L` or `-S`, so
  the program cannot reach a server it was not pointed at. Tests use a private socket and kill
  only what they created.

## Supported versions

The latest release. This is a single-maintainer project; older tags get no backports.
