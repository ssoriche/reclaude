// Package restore rebuilds a WezTerm layout from a snapshot.
package restore

import (
	"context"
	"errors"
	"fmt"
	"math"
	"path/filepath"

	"github.com/ssoriche/reclaude/internal/snapshot"
	"github.com/ssoriche/reclaude/internal/wezterm"
)

// launchCommand returns the text to send into a restored pane (with trailing
// newline), or "" to leave a bare shell.
func launchCommand(leaf *snapshot.Leaf, resumable bool) string {
	if leaf == nil || leaf.Claude == nil {
		return ""
	}
	if resumable {
		return fmt.Sprintf("claude --resume %s\n", leaf.Claude.SessionID)
	}
	return "claude\n"
}

// isResumable re-checks that the session's .jsonl still exists.
func isResumable(leaf *snapshot.Leaf, exists func(string) bool) bool {
	if leaf == nil || leaf.Claude == nil {
		return false
	}
	path := filepath.Join(leaf.Claude.ProjectDir, leaf.Claude.SessionID+".jsonl")
	return exists(path)
}

// Terminal is the subset of wezterm.Client that restore needs. It is complete
// as of this task; later tasks add no methods.
type Terminal interface {
	SpawnWindow(ctx context.Context, cwd string) (int, error)
	SpawnTab(ctx context.Context, windowID int, cwd string) (int, error)
	WindowIDForPane(ctx context.Context, paneID int) (int, error)
	SplitPane(ctx context.Context, paneID int, dir wezterm.Direction, percent int, cwd string) (int, error)
	SendText(ctx context.Context, paneID int, text string) error
	ActivatePane(ctx context.Context, paneID int) error
	SetTabTitle(ctx context.Context, paneID int, title string) error
	SetWindowTitle(ctx context.Context, paneID int, title string) error
	ZoomPane(ctx context.Context, paneID int) error
}

// Restore rebuilds layouts from a snapshot.
type Restore struct {
	term   Terminal
	exists func(string) bool
}

// New returns a Restore. exists re-checks session .jsonl presence.
func New(t Terminal, exists func(string) bool) *Restore {
	return &Restore{term: t, exists: exists}
}

// Run rebuilds every window in the snapshot. Failures in one subtree are
// collected (errors.Join) but do not abort the whole restore. Task 16 restores
// each window's first tab; Task 17 extends this to all tabs.
func (r *Restore) Run(ctx context.Context, snap *snapshot.Snapshot) error {
	if snap == nil {
		return nil
	}
	var errs []error
	for _, w := range snap.Windows {
		if len(w.Tabs) == 0 {
			errs = append(errs, fmt.Errorf("window %q: has no tabs", w.Title))
			continue
		}
		if w.Tabs[0].Layout == nil {
			errs = append(errs, fmt.Errorf("window %q: tab 0 has no layout", w.Title))
			continue
		}
		if err := snapshot.ValidateLayout(w.Tabs[0].Layout); err != nil {
			errs = append(errs, fmt.Errorf("window %q: tab 0 has invalid layout: %w", w.Title, err))
			continue
		}
		rootPane, err := r.term.SpawnWindow(ctx, firstLeaf(w.Tabs[0].Layout).CWD)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		tabPaneIDs := make([][]int, len(w.Tabs))
		paneIDs, err := r.buildTab(ctx, rootPane, w.Tabs[0].Layout)
		if err != nil {
			errs = append(errs, err)
		}
		tabPaneIDs[0] = paneIDs
		r.applyFinishers(ctx, w.Tabs[0], paneIDs)
		if w.Title != "" {
			_ = r.term.SetWindowTitle(ctx, rootPane, w.Title)
		}
		if len(w.Tabs) > 1 {
			winID, wErr := r.term.WindowIDForPane(ctx, rootPane)
			if wErr != nil {
				errs = append(errs, wErr)
			} else {
				for i, tab := range w.Tabs[1:] {
					if tab.Layout == nil {
						errs = append(errs, fmt.Errorf("window %q: tab %d has no layout", w.Title, i+1))
						continue
					}
					if err := snapshot.ValidateLayout(tab.Layout); err != nil {
						errs = append(errs, fmt.Errorf("window %q: tab %d has invalid layout: %w", w.Title, i+1, err))
						continue
					}
					tabPane, sErr := r.term.SpawnTab(ctx, winID, firstLeaf(tab.Layout).CWD)
					if sErr != nil {
						errs = append(errs, sErr)
						continue
					}
					ids, bErr := r.buildTab(ctx, tabPane, tab.Layout)
					if bErr != nil {
						errs = append(errs, bErr)
					}
					tabPaneIDs[i+1] = ids
					r.applyFinishers(ctx, tab, ids)
				}
			}
		}
		// Every applyFinishers call above focuses its own tab as it is built, so
		// window focus ends up on the LAST-built tab rather than the one that was
		// actually active when the snapshot was captured. Re-activate the
		// captured-active tab's active pane last so it wins.
		r.activateWindow(ctx, w, tabPaneIDs)
	}
	return errors.Join(errs...)
}

// buildTab recursively creates splits for a tab whose root pane already exists
// as parentPane, sends launch commands to every leaf, and returns the leaf
// pane ids in canonical order (Children[0] before Children[1]).
func (r *Restore) buildTab(ctx context.Context, parentPane int, node *snapshot.LayoutNode) ([]int, error) {
	if node.Pane != nil {
		return []int{parentPane}, r.launch(ctx, parentPane, node.Pane)
	}
	dir := wezterm.DirRight
	if node.Split == "horizontal" {
		dir = wezterm.DirBottom
	}
	// Non-"horizontal" values default to a vertical (--right) split deliberately;
	// Split is a closed enum from layout, and malformed values are rejected by
	// snapshot.ValidateLayout before we get here.
	percent := int(math.Round((1 - node.Ratio) * 100))
	newPane, err := r.term.SplitPane(ctx, parentPane, dir, percent, firstLeaf(node.Children[1]).CWD)
	if err != nil {
		// Can't create children[1]; still build children[0] into the parent.
		left, lerr := r.buildTab(ctx, parentPane, node.Children[0])
		return left, errors.Join(err, lerr)
	}
	left, lerr := r.buildTab(ctx, parentPane, node.Children[0])
	right, rerr := r.buildTab(ctx, newPane, node.Children[1])
	return append(left, right...), errors.Join(lerr, rerr)
}

func (r *Restore) launch(ctx context.Context, pane int, leaf *snapshot.Leaf) error {
	cmd := launchCommand(leaf, isResumable(leaf, r.exists))
	if cmd == "" {
		return nil
	}
	return r.term.SendText(ctx, pane, cmd)
}

// applyFinishers restores zoom and tab title using the canonical leaf-ordered
// pane ids returned by buildTab. Focus (ActivatePane) is handled separately by
// activateWindow, once per window after all of its tabs are built, so that the
// captured-active tab wins rather than whichever tab happened to be built
// last.
func (r *Restore) applyFinishers(ctx context.Context, tab snapshot.Tab, paneIDs []int) {
	if tab.ZoomedPaneIndex != nil && *tab.ZoomedPaneIndex >= 0 && *tab.ZoomedPaneIndex < len(paneIDs) {
		_ = r.term.ZoomPane(ctx, paneIDs[*tab.ZoomedPaneIndex])
	}
	if tab.Title != "" && len(paneIDs) > 0 {
		_ = r.term.SetTabTitle(ctx, paneIDs[0], tab.Title)
	}
}

// activateWindow focuses the pane that was active when the snapshot was
// captured: w.ActiveTabIndex selects the tab, and that tab's ActivePaneIndex
// selects the pane within tabPaneIDs (indexed in build order, matching
// w.Tabs). Out-of-range indices (e.g. from a tab that failed to build) are
// silently skipped, matching applyFinishers' bounds-checking style.
func (r *Restore) activateWindow(ctx context.Context, w snapshot.Window, tabPaneIDs [][]int) {
	if w.ActiveTabIndex < 0 || w.ActiveTabIndex >= len(w.Tabs) || w.ActiveTabIndex >= len(tabPaneIDs) {
		return
	}
	paneIDs := tabPaneIDs[w.ActiveTabIndex]
	activePane := w.Tabs[w.ActiveTabIndex].ActivePaneIndex
	if activePane < 0 || activePane >= len(paneIDs) {
		return
	}
	_ = r.term.ActivatePane(ctx, paneIDs[activePane])
}

// firstLeaf returns the first leaf reached by descending Children[0].
func firstLeaf(node *snapshot.LayoutNode) *snapshot.Leaf {
	for node.Pane == nil {
		node = node.Children[0]
	}
	return node.Pane
}
