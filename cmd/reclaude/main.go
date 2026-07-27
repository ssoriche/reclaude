// Command reclaude captures and restores a WezTerm window/tab/split-pane
// layout, resuming each pane's Claude conversation on restore.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/ssoriche/reclaude/internal/app"
	"github.com/ssoriche/reclaude/internal/daemon"
	iexec "github.com/ssoriche/reclaude/internal/exec"
	"github.com/ssoriche/reclaude/internal/snapshot"
	"github.com/ssoriche/reclaude/internal/wezterm"
)

// historyKeep is how many history entries save/daemon-run retain.
// daemonInterval is how often the daemon captures while running.
const (
	historyKeep    = 20
	daemonInterval = 5 * time.Minute
)

func main() {
	os.Exit(run(context.Background(), os.Args[1:], os.Stdout, os.Stderr))
}

// run dispatches on args[0] and returns a process exit code.
func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		_, _ = fmt.Fprintln(stderr, "usage: reclaude <save|restore|list|status|daemon>")
		return 2
	}
	switch args[0] {
	case "save":
		return cmdSave(ctx, stdout, stderr)
	case "restore":
		return cmdRestore(ctx, stdout, stderr)
	case "list":
		return cmdList(stdout, stderr)
	case "status":
		return cmdStatus(ctx, stdout, stderr)
	case "daemon":
		return cmdDaemon(ctx, args[1:], stdout, stderr)
	default:
		_, _ = fmt.Fprintf(stderr, "unknown command: %s\n", args[0])
		return 2
	}
}

// captureFunc produces a snapshot; it is the injectable seam shared by
// runSave and runStatus.
type captureFunc func(ctx context.Context) (*snapshot.Snapshot, error)

// cmdSave is the thin wrapper that wires the real capture/store from the
// environment; runSave is the testable core.
func cmdSave(ctx context.Context, stdout, stderr io.Writer) int {
	now := time.Now()
	capturer, store, err := app.NewCapture(ctx, now)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return 1
	}
	return runSave(ctx, stdout, stderr, now, capturer.Run, store)
}

// runSave captures the current WezTerm layout and writes it to the store.
// If WezTerm is not running, this is a friendly no-op (exit 0). now is used
// to derive the history filename, matching the CapturedAt the capture
// itself was stamped with.
func runSave(ctx context.Context, stdout, stderr io.Writer, now time.Time, capture captureFunc, store *snapshot.Store) int {
	snap, err := capture(ctx)
	if errors.Is(err, wezterm.ErrNotRunning) {
		_, _ = fmt.Fprintln(stdout, "WezTerm is not running; nothing to capture.")
		return 0
	}
	if err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return 1
	}
	if err := store.Save(snap, app.FileStamp(now), historyKeep); err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return 1
	}
	_, _ = fmt.Fprintf(stdout, "saved %d window(s)\n", len(snap.Windows))
	return 0
}

// cmdRestore is the thin wrapper that wires the real store/restorer from the
// environment; runRestore is the testable core.
func cmdRestore(ctx context.Context, stdout, stderr io.Writer) int {
	store, err := app.NewStore()
	if err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return 1
	}
	r := app.NewRestore()
	return runRestore(ctx, stdout, stderr, store, r.Run)
}

// runRestore reads the latest snapshot from store and runs restorer against
// it. A missing latest.json is a friendly message on stderr with exit 1,
// not a crash.
func runRestore(ctx context.Context, stdout, stderr io.Writer, store *snapshot.Store, restorer func(context.Context, *snapshot.Snapshot) error) int {
	snap, err := store.Latest()
	if errors.Is(err, os.ErrNotExist) {
		_, _ = fmt.Fprintln(stderr, "no saved session found; nothing to restore.")
		return 1
	}
	if err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return 1
	}
	if err := restorer(ctx, snap); err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return 1
	}
	_, _ = fmt.Fprintf(stdout, "restored %d window(s)\n", len(snap.Windows))
	return 0
}

// cmdList is the thin wrapper that wires the real store from the
// environment; runList is the testable core.
func cmdList(stdout, stderr io.Writer) int {
	store, err := app.NewStore()
	if err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return 1
	}
	return runList(stdout, stderr, store)
}

// runList prints each history entry with its window/tab/session counts.
func runList(stdout, stderr io.Writer, store *snapshot.Store) int {
	matches, err := store.HistoryFiles()
	if err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return 1
	}
	if len(matches) == 0 {
		_, _ = fmt.Fprintln(stdout, "no history entries found.")
		return 0
	}
	for _, m := range matches {
		data, err := os.ReadFile(m)
		if err != nil {
			_, _ = fmt.Fprintln(stderr, err)
			continue
		}
		var snap snapshot.Snapshot
		if err := json.Unmarshal(data, &snap); err != nil {
			_, _ = fmt.Fprintln(stderr, err)
			continue
		}
		windows, tabs, sessions := summarize(&snap)
		_, _ = fmt.Fprintf(stdout, "%s  %d window(s), %d tab(s), %d claude session(s)\n",
			filepath.Base(m), windows, tabs, sessions)
	}
	return 0
}

// cmdStatus is the thin wrapper that wires the real capture from the
// environment; runStatus is the testable core.
func cmdStatus(ctx context.Context, stdout, stderr io.Writer) int {
	capturer, _, err := app.NewCapture(ctx, time.Now())
	if err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return 1
	}
	return runStatus(ctx, stdout, stderr, capturer.Run)
}

// runStatus captures the current layout and prints a summary WITHOUT
// writing it to the store. Same ErrNotRunning handling as runSave.
func runStatus(ctx context.Context, stdout, stderr io.Writer, capture captureFunc) int {
	snap, err := capture(ctx)
	if errors.Is(err, wezterm.ErrNotRunning) {
		_, _ = fmt.Fprintln(stdout, "WezTerm is not running; nothing to capture.")
		return 0
	}
	if err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return 1
	}
	windows, tabs, sessions := summarize(snap)
	_, _ = fmt.Fprintf(stdout, "%d window(s), %d tab(s), %d claude session(s)\n", windows, tabs, sessions)
	return 0
}

// cmdDaemon dispatches the daemon install/uninstall/run subcommands.
func cmdDaemon(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		_, _ = fmt.Fprintln(stderr, "usage: reclaude daemon <install|uninstall|run>")
		return 2
	}
	dir, err := app.StateDir()
	if err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return 1
	}
	plistPath := filepath.Join(dir, daemon.Label+".plist")
	logPath := filepath.Join(dir, "daemon.log")
	uid := strconv.Itoa(os.Getuid())

	switch args[0] {
	case "install":
		bin, err := os.Executable()
		if err != nil {
			_, _ = fmt.Fprintln(stderr, err)
			return 1
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			_, _ = fmt.Fprintln(stderr, err)
			return 1
		}
		if err := daemon.Install(ctx, iexec.OSRunner{}, plistPath, bin, logPath, uid); err != nil {
			_, _ = fmt.Fprintln(stderr, err)
			return 1
		}
		_, _ = fmt.Fprintln(stdout, "daemon installed:", plistPath)
		return 0
	case "uninstall":
		if err := daemon.Uninstall(ctx, iexec.OSRunner{}, plistPath, uid); err != nil {
			_, _ = fmt.Fprintln(stderr, err)
			return 1
		}
		_, _ = fmt.Fprintln(stdout, "daemon uninstalled")
		return 0
	case "run":
		store, err := app.NewStore()
		if err != nil {
			_, _ = fmt.Fprintln(stderr, err)
			return 1
		}
		// A fresh Capture is built on every tick (not hoisted out of the
		// closure) so CapturedAt/wezterm_version reflect the moment of that
		// tick rather than freezing at daemon startup.
		capture := func(ctx context.Context) (*snapshot.Snapshot, error) {
			capturer, _, err := app.NewCapture(ctx, time.Now())
			if err != nil {
				return nil, err
			}
			return capturer.Run(ctx)
		}
		d := daemon.New(capture, store, func() string { return app.FileStamp(time.Now()) }, historyKeep)
		sigCtx, stop := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
		defer stop()
		d.Run(sigCtx, daemonInterval, func(err error) { _, _ = fmt.Fprintln(stderr, err) })
		return 0
	default:
		_, _ = fmt.Fprintf(stderr, "unknown daemon subcommand: %s\n", args[0])
		return 2
	}
}

// summarize counts windows, tabs, and claude-resumable panes in a snapshot.
func summarize(snap *snapshot.Snapshot) (windows, tabs, sessions int) {
	windows = len(snap.Windows)
	for _, w := range snap.Windows {
		tabs += len(w.Tabs)
		for _, t := range w.Tabs {
			sessions += countSessions(t.Layout)
		}
	}
	return
}

func countSessions(node *snapshot.LayoutNode) int {
	if node == nil {
		return 0
	}
	if node.Pane != nil {
		if node.Pane.Claude != nil {
			return 1
		}
		return 0
	}
	n := 0
	for _, c := range node.Children {
		n += countSessions(c)
	}
	return n
}
