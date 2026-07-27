# reclaude — Claude/WezTerm Session Manager

**Status:** Design approved, pending spec review
**Date:** 2026-07-23
**Author:** dev

## Problem

Multiple WezTerm windows are open, each running one or more `claude` sessions
across tabs and split panes. On reboot (including ungraceful ones), that entire
working set is lost: window/tab/split layout, the working directory of each
pane, and — most importantly — the running Claude conversations.

`reclaude` captures this state and rebuilds it after a reboot, resuming each
Claude conversation from its real session history via `claude --resume <id>`.

## Scope

**In scope (alpha):**
- WezTerm only.
- Capture and restore of full layout: windows → tabs → nested split panes.
- Resolve each pane's running Claude session and resume it on restore.
- Restore non-Claude panes as bare shells in their correct working directory.
- Periodic background capture (launchd) as a crash-safe net.
- Manual `save` and `restore` commands.

**Out of scope (alpha, YAGNI):**
- Ghostty (and any non-WezTerm terminal).
- Scrollback / pane buffer contents.
- Restoring arbitrary non-Claude running commands (e.g. `vim`, `tail -f`).
- Environment-variable capture.
- Merge-into-existing-layout restore (restore always spawns fresh).

## Approach

A single statically-linked Go binary, `reclaude`, that drives the `wezterm cli`
subcommands. All terminal manipulation goes through `wezterm cli`; no plugin,
no Lua. Session correlation is done by using WezTerm's per-pane `tty_name` to
find (via `ps`) which panes run `claude`, then locating that pane's transcript
on disk at `~/.claude/projects/<slug-of-cwd>/<session-id>.jsonl` (newest
top-level `.jsonl`, or an explicit `--resume <id>` when its file exists).
(`claude` does not hold the transcript open, so `lsof` cannot find it.)

Go is chosen for: a single binary with no runtime deps (trivial launchd
daemon), first-class JSON, and easy unit testing of the one real algorithm
(split-tree reconstruction).

## Components

Each package has a single responsibility and a well-defined interface.

| Package | Responsibility | Depends on |
|---|---|---|
| `wezterm` | Thin wrapper over `wezterm cli` (list / spawn / split-pane / send-text / activate-pane / activate-tab / set-tab-title / set-window-title / zoom-pane). All shelling-out to WezTerm lives here, behind an interface. | `os/exec` |
| `session` | Resolve a pane's `tty_name` → `claude` pid (via `ps`) and its cwd → on-disk `~/.claude/projects/<slug>/*.jsonl` → session ID + project dir. Shells to `ps` behind an interface. | `os/exec` |
| `layout` | Pure logic: build the guillotine split-tree from pane rectangles and flatten it back. No I/O. | none |
| `snapshot` | Data model + persistence: write/read JSON, atomic `latest.json` update, rotated history. | `layout`, `session` |
| `capture` | Orchestrate a save: list panes → resolve sessions → build trees → write snapshot. | `wezterm`, `session`, `layout`, `snapshot` |
| `restore` | Walk a snapshot: spawn windows/tabs, recurse splits, send `claude --resume`. | `wezterm`, `snapshot` |
| `daemon` | Periodic capture loop; install/uninstall/run the launchd LaunchAgent. | `capture` |
| `cli` (main) | Argument parsing and command dispatch. | all of the above |

Interface boundaries that matter for testing: `wezterm` and `session` each
expose a small Go interface whose only concrete implementation shells out via
`os/exec`. Tests inject fakes that return recorded fixture output, so unit
tests never require a live WezTerm or `ps`.

## CLI Surface

```
reclaude save              # capture one snapshot now → latest.json
reclaude restore           # rebuild windows/tabs/splits from latest.json
reclaude list              # show snapshots (timestamp, #windows, #sessions)
                           # inspection/crash-recovery only; restore reads latest.json.
                           # A restore-a-specific-snapshot arg is deferred (see below).
reclaude status            # what is capturable right now (dry-run of save)
reclaude daemon install    # install launchd agent (periodic save)
reclaude daemon uninstall  # remove launchd agent
reclaude daemon run        # foreground capture loop (what launchd invokes)
```

Snapshots live under `~/.local/state/reclaude/`:
- `latest.json` — most recent snapshot (what `restore` reads).
- `history/<RFC3339-timestamp>.json` — rotated history, last N kept (default
  20). Timestamps use millisecond precision (and a numeric suffix on the rare
  exact collision) so a manual `save` and a daemon `save` in the same second
  cannot overwrite each other. History is for inspection and crash-recovery
  only; see the note on `list`/`restore` below.
- `daemon.log` — daemon output.

## Data Model

The snapshot JSON mirrors WezTerm's hierarchy: windows → tabs → pane-tree.

```jsonc
{
  "version": 1,
  "captured_at": "2026-07-23T14:02:11Z",
  "wezterm_version": "0-unstable-2026-03-31",
  "windows": [
    {
      "title": "Build reclaude",
      "workspace": "default",
      "active_tab_index": 0,
      "tabs": [
        {
          "title": "",
          "active_pane_index": 1,
          "zoomed_pane_index": null,           // index into flattened leaves, or null
          "layout": { /* tree node, see below */ }
        }
      ]
    }
  ]
}
```

### Layout tree node

A node is either a split (with two children) or a leaf (with a pane):

```jsonc
// split node
{
  "split": "vertical",        // "vertical" | "horizontal"
  "ratio": 0.5,               // fraction of the region given to children[0]
  "children": [ <node>, <node> ]
}
// children[0] is the left (vertical) / top (horizontal) region;
// children[1] is the right / bottom region. See "child/pane mapping"
// under the restore flow for how this maps onto wezterm split-pane.

// leaf node
{
  "split": null,
  "pane": { /* leaf pane, see below */ }
}
```

`split` direction semantics match `wezterm cli split-pane`:
- `"vertical"` → a vertical divider, children are left/right.
- `"horizontal"` → a horizontal divider, children are top/bottom.

### Leaf pane

```jsonc
{
  "cwd": "/Users/dev/projects/alpha",
  "claude": {                           // null if pane was not running Claude
    "session_id": "0b3f...",
    "project_dir": "/Users/dev/.claude/projects/-Users-dev-projects-alpha/",
    "resumable": true                   // re-checked at restore time
  },
  "cols": 183,
  "rows": 17
}
```

Decisions baked into the model:
- **cwd is a plain absolute path.** WezTerm reports `file://<host>/<path>`; the
  `capture` step strips the scheme and host to a plain path.
- **Non-Claude panes** carry `claude: null` and are still restored (bare shell
  in the right cwd) so layout fidelity is preserved.
- **`resumable`** is stored for information but authoritatively re-checked at
  restore time (the `.jsonl` may have been deleted meanwhile).
- **Ratios, not absolute geometry.** Splits store a fraction so restore adapts
  to the window size available at relaunch. `cols`/`rows` are retained only as
  a hint / debugging aid.
- **`active_pane_index` / `zoomed_pane_index`** are indices into a tab's leaves
  in **canonical leaf order** — the deterministic left-to-right, depth-first
  traversal of the layout tree (`children[0]` before `children[1]`). This
  ordering is a first-class production operation (`layout.Leaves(node) []leaf`),
  not test-only; capture uses it to record the indices and restore uses it to
  resolve which pane to activate/zoom. `wezterm_version` is captured purely as
  an informational/debugging aid and is not consumed by restore.

## Capture Flow (`reclaude save`)

1. `wezterm cli list --format json` → all panes with `window_id`, `tab_id`,
   `pane_id`, `left_col`, `top_row`, `size.{cols,rows}`, `cwd`, `tty_name`,
   `title`, `window_title`, `workspace`, `is_active`, `is_zoomed`.
   - If this command fails (WezTerm not running), `save` is a no-op that prints
     a clear message and exits 0. Nothing to capture is not an error.
2. Resolve sessions **per pane**, once panes are known:
   - `ps -axo pid,tty,command` → identify `claude` processes and their ttys
     (joined to panes by matching `tty_name`, WezTerm's `/dev/ttysNNN`, to the
     claude process's controlling tty), plus an optional `--resume <id>` hint
     parsed from that process's argv.
   - Claude does **not** hold its session `.jsonl` open, so `lsof` cannot find
     it — the transcript is located on disk instead. For a pane whose tty is
     running claude, compute `slug := SlugFor(pane.cwd)` (every character that
     is not an ASCII letter or digit becomes `-`) and look in
     `~/.claude/projects/<slug>/`:
     - if the `--resume` hint is present and `<slug>/<hint>.jsonl` exists,
       that is the session (the hint can be stale — e.g. the transcript was
       deleted — so it is re-verified by file existence, not trusted blindly);
     - otherwise, the **most-recently-modified top-level** `*.jsonl` in that
       directory is the active session. Subagent transcripts live under
       `<session-id>/subagents/` and are ignored by scanning
       non-recursively.
   - `session_id` is the filename stem; `project_dir` is `<slug>`'s full path.
3. Group panes: `window_id` → `tab_id` → list of pane rectangles.
4. For each tab, `layout.BuildTree(rects)` reconstructs the guillotine
   split-tree (pure function; see below).
5. Assemble the snapshot struct. Write `history/<ts>.json`, then atomically
   replace `latest.json` (write temp + rename). Rotate `history/` to the last N.

### Edge cases

- **A claude pid maps to no pane** (tty not in the WezTerm list): ignored — it
  is not in a capturable WezTerm pane.
- **No local transcript found** for a pane whose tty is running claude (stale
  `--resume` hint, a non-default `CLAUDE_CONFIG_DIR`, a fresh session
  mid-init, or a git-worktree cwd whose transcript was written under a
  different cwd slug): captured as a **bare-relaunch marker** — `claude` is
  non-null but carries an empty `session_id`/`project_dir` and
  `resumable: false`, so restore relaunches `claude` bare in that pane rather
  than silently downgrading it to a plain shell. This is a known alpha
  limitation, not a bug: reclaude only knows the mechanism above, not
  Claude's actual internal session bookkeeping.
- **Multiple live claude sessions share one cwd** (e.g. two panes both `cd`'d
  into the same repo, neither resumed): both resolve to the same newest
  top-level `.jsonl` in that cwd's project directory. Disambiguating them
  precisely is deferred past the alpha.
- **cwd on a network mount** (as in the sample): stored as the plain path; no
  special handling — restore just `--cwd`s into it.

## Restore Flow (`reclaude restore`)

Reads `latest.json` and rebuilds it.

1. **Collision guard.** By default, restore spawns fresh windows rather than
   merging into the current layout. Each restored window is placed into a single
   new workspace named `reclaude-<short-timestamp>` (all restored windows share
   it, regardless of the per-window `workspace` recorded at capture — the
   captured `workspace` is retained in the snapshot as metadata but the alpha
   consolidates restore into one fresh workspace to keep the collision guard
   simple). Restore prints the workspace it created so the windows are easy to
   find. (A `--force`/merge mode and faithful multi-workspace restore are
   explicitly deferred.)
2. For each window: `wezterm cli spawn --new-window --cwd <first-leaf-cwd>`
   → capture the returned root pane id. `<first-leaf-cwd>` is the cwd of the
   first leaf in canonical order (see below).
3. Recurse the layout tree. **Child/pane mapping:** an existing pane id
   corresponds to `children[0]` (the left/top region, which retains the
   original pane); `wezterm cli split-pane` creates a *new* pane that becomes
   `children[1]` (the right/bottom region), and `--percent N` sizes that **new**
   `children[1]` pane. Therefore at each split node:
   `wezterm cli split-pane --pane-id <children[0]-pane-id>
   --<right|bottom> --percent <round((1 - ratio) * 100)>
   --cwd <first-leaf-cwd-of-children[1]-subtree>` → capture the returned new
   pane id as `children[1]`'s pane. Then recurse: descend into `children[0]`
   reusing the parent pane id, and into `children[1]` using the new pane id.
   - `--<right|bottom>`: `split: "vertical"` → `--right`; `split: "horizontal"`
     → `--bottom` (WezTerm's directional split flags; the new pane lands there).
   - **cwd for a non-leaf child:** the newly-created pane physically *is* one
     leaf of the child subtree — specifically the first leaf reached by
     descending `children[0]` from that child (canonical order). Spawn it with
     that leaf's cwd, so no extra `--cwd` correction is needed once the subtree
     is built.
   - The one-cell divider WezTerm inserts per split is accounted for in the
     ratio math (see the algorithm section); restore passes a percent and lets
     WezTerm place the divider.
4. Additional tabs in a window: `wezterm cli spawn --window-id <wid> --cwd …`
   to create the tab, then build that tab's tree as in step 3.
5. Per leaf, re-check resumability, then `wezterm cli send-text --no-paste
   --pane-id <id> <text>`:
   - Claude + resumable → `claude --resume <session_id>\n`
   - Claude + not resumable (`.jsonl` gone) → `claude\n`
   - Non-Claude pane → send nothing (leaves a bare shell in the right cwd).
6. Finishing touches: `set-tab-title`, `set-window-title`, re-`zoom-pane` for a
   zoomed pane, `activate-tab` / `activate-pane` to restore focus.

### Edge cases

- **`latest.json` missing or unparsable:** restore exits non-zero with a clear
  message; no partial spawning.
- **A `split-pane` or `spawn` call fails mid-restore:** log the failure, skip
  that subtree, continue restoring the rest. A partial restore is better than
  aborting everything; the summary reports what failed.
- **Resume of a session errors** (claude rejects the id): the `send-text`
  already ran; failure surfaces inside that pane, not to `reclaude`. `resumable`
  pre-check minimizes this.
- **Very small target window** makes a `--percent` split infeasible: WezTerm
  clamps; we accept its result rather than failing.

## The Split-Tree Algorithm (`layout` package)

The single real algorithm. WezTerm only ever produces **guillotine** layouts —
every split cleanly bisects its region — which makes reconstruction from
rectangles well-defined.

**`BuildTree(rects) → node`:**
> Given a set of pane rectangles (each `left`, `top`, `cols`, `rows`), find a
> single full-span cut line — vertical (a shared x that no rect straddles) or
> horizontal (a shared y that no rect straddles) — that partitions the rects
> into two non-empty groups. Emit a split node recording direction and ratio
> (position of the cut within the region), then recurse on each group. A single
> rectangle is a leaf.

Determinism: when both a vertical and a horizontal cut are available, prefer
vertical first (documented, stable ordering). The `ratio` is computed from the
cut coordinate relative to the enclosing bounding box.

**Divider-cell accounting.** WezTerm consumes one row/column for the divider at
each split, so the two child regions plus one divider cell sum to the parent
extent (e.g. a 183-col region split vertically yields children whose cols sum to
182). `BuildTree` detects the cut as the gap between the two groups' extents and
computes `ratio` from `children[0]`'s extent over the *content* extent
(parent minus the one divider cell). The `Flatten`/round-trip test models the
same one-cell gap so it asserts against the correct geometry.

**`Leaves(node) → []leaf`** returns the tab's leaves in canonical order
(left-to-right, depth-first; `children[0]` before `children[1]`). This is a
production operation used by capture (to record `active_pane_index` /
`zoomed_pane_index`) and restore (to resolve activate/zoom targets).

**`Flatten(node) → rects`** is the geometric inverse of `BuildTree`, used only
in tests to assert round-trip correctness. (`Leaves` returns panes in order;
`Flatten` reconstructs rectangles — they are distinct operations.)

## Daemon

- `reclaude daemon run` — foreground loop. Every N seconds (default 60) run
  `capture`. Compute a **content hash over the meaningful layout/session fields
  only** — the `windows` tree and its pane/session data — explicitly excluding
  volatile fields (`captured_at`, `wezterm_version`). If the content hash equals
  the last write's, skip writing (no history churn). Errors are logged and the
  loop continues.
- `reclaude daemon install` — write a LaunchAgent plist to
  `~/Library/LaunchAgents/net.reclaude.daemon.plist` with `RunAtLoad` and
  `KeepAlive`, `ProgramArguments` = the installed binary path + `daemon run`,
  stdout/stderr → `~/.local/state/reclaude/daemon.log`; then
  `launchctl bootstrap gui/<uid>` it.
- `reclaude daemon uninstall` — `launchctl bootout` and remove the plist.
- The daemon **only captures.** Restore is always a manual command — no
  window-storm on login.

## Testing Strategy

- **`layout`** (the heart): pure unit tests over hand-built rect sets — single
  pane, side-by-side (2-way), stacked (2-way), nested 3-way, deep asymmetric
  trees — plus a round-trip property: `Flatten(BuildTree(rects))` equals the
  input set. TDD drives this package.
- **`session`** and **`wezterm`**: the `os/exec` calls sit behind interfaces;
  tests inject fakes returning recorded fixture output (real captured
  `wezterm cli list` and `ps` samples), plus `t.TempDir()`-backed fixtures for
  the `~/.claude/projects/<slug>/*.jsonl` lookup. No live processes needed.
- **`snapshot`**: serialize/deserialize round-trip, atomic-write behavior,
  history rotation to N.
- **`capture` / `restore`**: table tests wiring fake `wezterm`/`session`
  implementations; assert the sequence of `wezterm cli` calls a given snapshot
  produces (restore) and the snapshot a given fake pane list produces (capture).
- **Manual end-to-end** (documented in README, not CI): open several windows
  with splits and Claude sessions, `save`, close them, `restore`, eyeball
  fidelity and confirm conversations resume. Genuine window spawning is not
  asserted in CI.

## Open Questions / Deferred

- Merge-into-existing-layout restore (`--force`) — deferred.
- Ghostty support — deferred to a post-alpha iteration; the `wezterm` package
  boundary is where a second terminal backend would slot in.
- Configurable snapshot interval and history depth — hardcoded defaults for
  alpha; promote to a config file if needed.
- `reclaude restore <timestamp>` to restore a specific history snapshot —
  deferred; alpha `restore` reads only `latest.json`.
- Faithful multi-workspace restore (mapping each window back to its captured
  workspace) — deferred; alpha consolidates into one fresh workspace.
