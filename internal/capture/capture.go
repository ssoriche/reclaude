// Package capture orchestrates a save: list panes, resolve sessions, build
// layout trees, and assemble a snapshot.
package capture

import (
	"context"
	"log"
	"strings"

	"github.com/ssoriche/reclaude/internal/layout"
	"github.com/ssoriche/reclaude/internal/session"
	"github.com/ssoriche/reclaude/internal/snapshot"
	"github.com/ssoriche/reclaude/internal/wezterm"
)

// normalizeCWD converts WezTerm's file://<host>/<path> to a plain path.
func normalizeCWD(cwd string) string {
	if !strings.HasPrefix(cwd, "file://") {
		return cwd
	}
	rest := strings.TrimPrefix(cwd, "file://")
	// rest is "<host>/<path>" or "/<path>" (empty host). Cut at the first '/'.
	if i := strings.IndexByte(rest, '/'); i >= 0 {
		return rest[i:]
	}
	return "/" + rest
}

// Lister lists WezTerm panes.
type Lister interface {
	List(ctx context.Context) ([]wezterm.Pane, error)
}

// SessionResolver maps tty -> Proc (which panes run claude) and locates a
// cwd's on-disk session.
type SessionResolver interface {
	Procs(ctx context.Context) (map[string]session.Proc, error)
	Locate(cwd, resumeID string) (session.ClaudeSession, bool)
}

// Capture orchestrates one snapshot.
type Capture struct {
	lister   Lister
	resolver SessionResolver
	stamp    string
	weztermV string
}

// New returns a Capture. stamp is the CapturedAt/CapturedAt-derived value.
func New(l Lister, r SessionResolver, stamp, weztermVersion string) *Capture {
	return &Capture{lister: l, resolver: r, stamp: stamp, weztermV: weztermVersion}
}

// Run lists panes, resolves sessions, and assembles a Snapshot.
func (c *Capture) Run(ctx context.Context) (*snapshot.Snapshot, error) {
	panes, err := c.lister.List(ctx)
	if err != nil {
		return nil, err
	}
	procs, err := c.resolver.Procs(ctx)
	if err != nil {
		return nil, err
	}
	return c.assemble(panes, procs), nil
}

func (c *Capture) assemble(panes []wezterm.Pane, procs map[string]session.Proc) *snapshot.Snapshot {
	// Group: window -> tab -> panes, preserving first-seen order.
	type tabKey struct{ win, tab int }
	winOrder := []int{}
	tabOrder := map[int][]int{}
	byTab := map[tabKey][]wezterm.Pane{}
	winTitle := map[int]string{}
	winWorkspace := map[int]string{}
	activeTabByWin := map[int]int{} // window -> tab id containing the active pane

	for _, p := range panes {
		if _, ok := winTitle[p.WindowID]; !ok {
			winOrder = append(winOrder, p.WindowID)
			winTitle[p.WindowID] = p.WindowTitle
			winWorkspace[p.WindowID] = p.Workspace
		}
		k := tabKey{p.WindowID, p.TabID}
		if _, ok := byTab[k]; !ok {
			tabOrder[p.WindowID] = append(tabOrder[p.WindowID], p.TabID)
		}
		byTab[k] = append(byTab[k], p)
		if p.IsActive {
			activeTabByWin[p.WindowID] = p.TabID
		}
	}

	snap := &snapshot.Snapshot{
		Version:        1,
		CapturedAt:     c.stamp,
		WeztermVersion: c.weztermV,
	}
	windows := make([]snapshot.Window, 0, len(winOrder))
	for _, win := range winOrder {
		w := snapshot.Window{Title: winTitle[win], Workspace: winWorkspace[win]}
		activeTab, hasActive := activeTabByWin[win]
		for i, tabID := range tabOrder[win] {
			tp := byTab[tabKey{win, tabID}]
			w.Tabs = append(w.Tabs, c.buildTab(tp, procs))
			if hasActive && tabID == activeTab {
				w.ActiveTabIndex = i
			}
		}
		windows = append(windows, w)
	}
	snap.Windows = windows
	return snap
}

func (c *Capture) buildTab(panes []wezterm.Pane, procs map[string]session.Proc) snapshot.Tab {
	rects := make([]layout.Rect, 0, len(panes))
	leaves := map[int]snapshot.Leaf{}
	for _, p := range panes {
		rects = append(rects, layout.Rect{
			ID: p.PaneID, Left: p.LeftCol, Top: p.TopRow, Cols: p.Size.Cols, Rows: p.Size.Rows,
		})
		leaves[p.PaneID] = c.leafFor(p, procs)
	}
	// node is the tab's layout tree; order is the leaf pane ids in canonical
	// (left-to-right, depth-first) order, used to place active/zoomed indices.
	var node *snapshot.LayoutNode
	var order []int
	if tree, err := layout.BuildTree(rects); err == nil {
		node = snapshot.FromLayout(tree, leaves)
		order = layout.Leaves(tree)
	} else if len(rects) > 0 {
		// The real geometry isn't a guillotine layout — almost always because a
		// pane was zoomed at capture time, so wezterm reports overlapping rects.
		// We can't recover the true split geometry, but we must not drop panes
		// (each may hold a Claude session), so preserve every pane in an
		// approximate evenly-stacked layout.
		log.Printf("reclaude: tab layout not guillotine (%d panes), preserving all panes in an approximate stacked layout: %v", len(panes), err)
		node, order = degenerateTab(rects, leaves)
	}

	tab := snapshot.Tab{Layout: node}
	idx := make(map[int]int, len(order))
	for i, id := range order {
		idx[id] = i
	}
	for _, p := range panes {
		if p.IsActive {
			tab.ActivePaneIndex = idx[p.PaneID]
		}
		if p.IsZoomed {
			z := idx[p.PaneID]
			tab.ZoomedPaneIndex = &z
		}
		if p.TabTitle != "" {
			tab.Title = p.TabTitle
		}
	}
	return tab
}

// degenerateTab builds a left-leaning chain of even vertical splits that
// preserves every pane, for tabs whose reported geometry isn't a guillotine
// layout. The geometry is approximate, but no pane (and no Claude session) is
// lost. It returns the layout node and the leaf pane ids in canonical order
// (which, for this left-leaning chain, is simply the input rect order).
func degenerateTab(rects []layout.Rect, leaves map[int]snapshot.Leaf) (*snapshot.LayoutNode, []int) {
	order := make([]int, len(rects))
	for i, r := range rects {
		order[i] = r.ID
	}
	return degenerateChain(rects, leaves), order
}

func degenerateChain(rects []layout.Rect, leaves map[int]snapshot.Leaf) *snapshot.LayoutNode {
	leaf := leaves[rects[0].ID]
	if len(rects) == 1 {
		return &snapshot.LayoutNode{Pane: &leaf}
	}
	return &snapshot.LayoutNode{
		Split: string(layout.SplitVertical),
		Ratio: 1.0 / float64(len(rects)),
		Children: []*snapshot.LayoutNode{
			{Pane: &leaf},
			degenerateChain(rects[1:], leaves),
		},
	}
}

func (c *Capture) leafFor(p wezterm.Pane, procs map[string]session.Proc) snapshot.Leaf {
	cwd := normalizeCWD(p.CWD)
	leaf := snapshot.Leaf{CWD: cwd, Cols: p.Size.Cols, Rows: p.Size.Rows}
	tty := strings.TrimPrefix(p.TTYName, "/dev/")
	proc, running := procs[tty]
	if !running {
		return leaf
	}
	if sess, ok := c.resolver.Locate(cwd, proc.ResumeID); ok {
		leaf.Claude = &snapshot.ClaudeSession{
			SessionID:  sess.SessionID,
			ProjectDir: sess.ProjectDir,
			Resumable:  true,
		}
	} else {
		// Claude is running on this pane's tty but no local transcript was
		// found (stale --resume, foreign CLAUDE_CONFIG_DIR, a fresh session
		// mid-init, etc). Mark it so restore relaunches claude bare rather
		// than silently dropping the pane's claude-ness.
		leaf.Claude = &snapshot.ClaudeSession{Resumable: false}
	}
	return leaf
}
