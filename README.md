# reclaude

`reclaude` is a Go CLI that captures the full WezTerm window/tab/split-pane
layout — along with each pane's running `claude` conversation — and restores
it later (e.g. after a reboot) by recreating the windows, tabs, and splits
and resuming each conversation with `claude --resume`.

It drives WezTerm entirely through `wezterm cli` subcommands; there is no
Lua/plugin dependency. A background daemon can periodically snapshot the
current layout so a restore is always available even if you forget to save
manually.

## Install

```bash
go build -o ~/.local/bin/reclaude ./cmd/reclaude
```

Make sure `~/.local/bin` is on your `PATH`. `reclaude` also shells out to
`wezterm`, `ps`, and (for the daemon) `launchctl` on macOS or `systemctl` on
Linux, all of which are expected to already be on `PATH`. macOS and Linux are
supported.

## Commands

```
reclaude save                       # capture the current WezTerm layout
reclaude restore                    # recreate the last saved layout
reclaude list                       # list saved history entries
reclaude status                     # show a summary without saving
reclaude daemon install             # install the periodic-snapshot service
reclaude daemon uninstall           # remove the service
reclaude daemon run                 # run the capture loop in the foreground
```

- `reclaude save` lists every WezTerm pane, resolves which panes are running
  `claude` (and which session they can resume), reconstructs each tab's
  split-pane tree, and writes the result to the snapshot store. If WezTerm
  isn't running, this is a no-op (exit 0) rather than an error.
- `reclaude restore` reads the last saved snapshot and rebuilds it: spawning
  windows, splitting panes to match the saved tree, and sending
  `claude --resume <session-id>` into any pane that was running a resumable
  Claude session (a bare shell is left alone).
- `reclaude status` runs the same capture as `save` but only prints a
  window/tab/session summary — it never writes to disk.
- `reclaude list` prints each entry in the history directory with its
  window/tab/session counts.
- `reclaude daemon run` is what the LaunchAgent / systemd unit actually executes: it
  captures on an interval and only writes a new history entry when the
  content (ignoring the timestamp/wezterm-version fields) actually changed.

## Where snapshots live

```
~/.local/state/reclaude/
  latest.json          # the most recent snapshot; restore always reads this
  history/
    <timestamp>.json   # rotating history, newest N kept (20 by default)
  daemon.log           # daemon stdout/stderr, once installed
```

Claude session resolution maps each pane's tty to its `claude` process (via
`ps`), then names the session that process is running from Claude's
per-process registry, `~/.claude/sessions/<pid>.json`. That covers sessions
opened through the `--resume` picker, by name (`--resume <name>`), or switched
with `/resume`. When a process has no registry record, it falls back to a
`--resume <id>` in argv, then to matching the process start time against
transcript start times in `~/.claude/projects/<slug>/*.jsonl`.

## Installing the daemon

```bash
reclaude daemon install
```

Either way, the service runs the same `reclaude` binary with `daemon run`,
starts on login, restarts if it crashes, and logs to
`~/.local/state/reclaude/daemon.log`. Re-run `install` after rebuilding or
moving the binary.

**macOS:** writes a LaunchAgent plist (`net.reclaude.daemon.plist`) into
`~/.local/state/reclaude/` and bootstraps it with
`launchctl bootstrap gui/<uid> <plist>` (`RunAtLoad`/`KeepAlive`).

**Linux:** writes a systemd user unit to
`${XDG_CONFIG_HOME:-~/.config}/systemd/user/reclaude.service`, then runs
`systemctl --user daemon-reload`, `enable`, and `restart` (`Restart=always`,
`WantedBy=default.target`). Your current `PATH` is baked into the unit, because
the user manager's default `PATH` often lacks `wezterm`. Check it with
`systemctl --user status reclaude`.

Under a service manager `WEZTERM_UNIX_SOCKET` is unset, and `wezterm cli`'s
own GUI lookup (a symlink keyed by `$DISPLAY`) often misses. So when that
variable is unset, reclaude looks for the one live `gui-sock-*` in
`$XDG_RUNTIME_DIR/wezterm` or `~/.local/share/wezterm` and uses that. If
several WezTerm GUIs are running it errors out; set `WEZTERM_UNIX_SOCKET` to
pick one. Every `wezterm cli` call passes `--no-auto-start`, so reclaude never
starts a headless mux server and captures its empty layout by mistake.

To remove it:

```bash
reclaude daemon uninstall
```

On macOS this runs `launchctl bootout gui/<uid>/net.reclaude.daemon` and
deletes the plist. On Linux it runs `systemctl --user disable --now
reclaude.service`, deletes the unit, and reloads the user manager.

## Restoring after a reboot

The daemon starts on login and keeps running, so it can begin capturing
again before you've had a chance to restore. Since `reclaude restore` only
ever reads `latest.json`, if WezTerm auto-reopens its own windows at login
(macOS's "reopen windows when logging back in", or a Linux desktop's session
restore / autostart), the
daemon's next tick will happily snapshot that fresh/empty post-login layout
and overwrite `latest.json` with it — burying the session you actually wanted
under a snapshot of nothing.

To avoid this:

- Run `reclaude restore` **before** opening any WezTerm windows after a
  reboot, or
- Disable "Reopen windows when logging back in" in macOS System
  Settings (or WezTerm autostart / session restore on Linux) so WezTerm
  doesn't restart on its own and race the daemon.

If `latest.json` has already been overwritten, the pre-reboot session isn't
lost: every snapshot is also kept in `~/.local/state/reclaude/history/`
(newest N, see `reclaude list`). There is currently no `reclaude restore
<timestamp>` to restore directly from a history entry — that's a deferred
feature — but you can manually copy the desired history file over
`latest.json` and then run `reclaude restore`.

## Manual end-to-end verification procedure

This is a manual smoke test to run by hand after making changes that touch
capture or restore — it exercises real WezTerm, `claude`, and the filesystem,
so it is not part of the automated test suite.

1. Open a WezTerm window; split it (e.g. one vertical + one horizontal split
   → 3 panes); start `claude` in two of the panes and leave the third as a
   plain shell.
2. Open a second WezTerm window with a fresh tab and one `claude`.
3. Run `reclaude save`. Confirm `~/.local/state/reclaude/latest.json` exists
   and its tree/leaf structure matches what you built (eyeball window/tab/
   split counts and cwds; the two claude panes should have a non-null
   `claude` object with a `session_id`).
4. Close all WezTerm windows.
5. Run `reclaude restore`.
6. Verify: windows/tabs/splits are recreated with the right cwds; the claude
   panes come back with their conversations resumed (i.e. `claude --resume`
   ran and the prior conversation is visible); the shell pane is a bare
   shell.
7. Record the observed result (pass/fail with notes) in the PR description.

## Development

- Single statically-linked Go binary, standard library only.
- `internal/exec` is the only package that shells out (`os/exec`); every
  other package takes an injected `Runner` so tests never invoke a real
  `wezterm`/`ps`/`launchctl`/`systemctl`.
- `internal/layout` is pure geometry with no I/O.

```bash
go build ./...
go vet ./...
go test ./...
gofmt -l .
```
