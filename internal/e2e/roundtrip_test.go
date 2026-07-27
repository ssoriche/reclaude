// Package e2e exercises the real cross-package capture -> snapshot store ->
// restore round trip. Unlike the package-local tests in internal/capture and
// internal/restore (which each only ever see their own package's fakes), this
// test wires capture's real output into restore's real input, guarding
// against regressions like ratio inversion or leaf-ordering mismatches that a
// single package's tests can't see because they don't cross the boundary.
package e2e

import (
	"context"
	"math"
	"testing"

	"github.com/ssoriche/reclaude/internal/capture"
	"github.com/ssoriche/reclaude/internal/restore"
	"github.com/ssoriche/reclaude/internal/session"
	"github.com/ssoriche/reclaude/internal/snapshot"
	"github.com/ssoriche/reclaude/internal/wezterm"
)

// fakeLister returns a canned pane list, mimicking wezterm.Client.List.
type fakeLister struct{ panes []wezterm.Pane }

func (f fakeLister) List(context.Context) ([]wezterm.Pane, error) { return f.panes, nil }

// fakeResolver mimics session.Resolver: procs marks which ttys run claude,
// and locate maps "<cwd>|<resumeID>" to the on-disk session found there.
type fakeResolver struct {
	procs  map[string]session.Proc
	locate map[string]session.ClaudeSession
}

func (f fakeResolver) Procs(context.Context) (map[string]session.Proc, error) {
	return f.procs, nil
}

func (f fakeResolver) Locate(cwd, resumeID string) (session.ClaudeSession, bool) {
	sess, ok := f.locate[cwd+"|"+resumeID]
	return sess, ok
}

// splitCall records one SplitPane invocation.
type splitCall struct {
	parent  int
	dir     wezterm.Direction
	percent int
	cwd     string
	newID   int
}

// sendCall records one SendText invocation.
type sendCall struct {
	pane int
	text string
}

// fakeTerminal implements restore.Terminal, handing out sequential pane ids
// and recording every call so the test can assert on the reconstructed
// geometry, not just that Run returned without error.
type fakeTerminal struct {
	nextID int

	spawnedWindowCWD string
	rootPane         int

	splits    []splitCall
	sends     []sendCall
	activates []int
}

func (f *fakeTerminal) newID() int {
	f.nextID++
	return f.nextID
}

func (f *fakeTerminal) SpawnWindow(_ context.Context, cwd string) (int, error) {
	id := f.newID()
	f.spawnedWindowCWD = cwd
	f.rootPane = id
	return id, nil
}

func (f *fakeTerminal) SpawnTab(_ context.Context, _ int, _ string) (int, error) {
	return f.newID(), nil
}

func (f *fakeTerminal) WindowIDForPane(context.Context, int) (int, error) { return 1, nil }

func (f *fakeTerminal) SplitPane(_ context.Context, paneID int, dir wezterm.Direction, percent int, cwd string) (int, error) {
	id := f.newID()
	f.splits = append(f.splits, splitCall{parent: paneID, dir: dir, percent: percent, cwd: cwd, newID: id})
	return id, nil
}

func (f *fakeTerminal) SendText(_ context.Context, paneID int, text string) error {
	f.sends = append(f.sends, sendCall{pane: paneID, text: text})
	return nil
}

func (f *fakeTerminal) ActivatePane(_ context.Context, paneID int) error {
	f.activates = append(f.activates, paneID)
	return nil
}

func (f *fakeTerminal) SetTabTitle(context.Context, int, string) error    { return nil }
func (f *fakeTerminal) SetWindowTitle(context.Context, int, string) error { return nil }
func (f *fakeTerminal) ZoomPane(context.Context, int) error               { return nil }

// TestCaptureRestoreRoundTrip builds one window/one tab with a nested
// guillotine layout (a vertical split whose right child is a horizontal
// split, giving 3 leaves: A | (B over C)), runs it through capture, proves
// the on-disk (JSON) representation round-trips via snapshot.Store, then
// feeds the round-tripped snapshot into restore and asserts the reconstructed
// call sequence matches the original geometry: split directions, percents,
// per-leaf cwds, the Claude resume command landing on the right pane, and the
// originally-active pane ending up activated.
func TestCaptureRestoreRoundTrip(t *testing.T) {
	// Geometry mirrors the nested case in internal/layout/layout_test.go
	// (TestBuildTreeNestedThreeWay): leaf A spans the full height on the
	// left; the right region is split top (B) / bottom (C); every cut
	// leaves the conventional one-divider-cell gap. The splits are
	// deliberately asymmetric (not a 50/50 bisection) so that this test
	// actually distinguishes ratio from (1-ratio): with a 0.5 ratio, a
	// ratio-inversion bug would be invisible because round(ratio*100) ==
	// round((1-ratio)*100).
	const (
		paneA = 10 // left column, full height, active, plain shell
		paneB = 20 // right-top, running a resumable Claude session
		paneC = 30 // right-bottom, plain shell
	)
	panes := []wezterm.Pane{
		{
			// A: cols 0..19 (20 wide). Divider at col 20; right region starts at 21.
			WindowID: 0, TabID: 0, PaneID: paneA,
			LeftCol: 0, TopRow: 0, Size: wezterm.Size{Cols: 20, Rows: 24},
			CWD: "file://host/repo/a", TTYName: "/dev/ttys001",
			IsActive: true, WindowTitle: "Main", Workspace: "default",
		},
		{
			// B: right region, top rows 0..7 (8 tall). Divider at row 8.
			WindowID: 0, TabID: 0, PaneID: paneB,
			LeftCol: 21, TopRow: 0, Size: wezterm.Size{Cols: 59, Rows: 8},
			CWD: "file://host/repo/b", TTYName: "/dev/ttys002",
		},
		{
			// C: right region, bottom rows 9..23 (15 tall).
			WindowID: 0, TabID: 0, PaneID: paneC,
			LeftCol: 21, TopRow: 9, Size: wezterm.Size{Cols: 59, Rows: 15},
			CWD: "file://host/repo/c", TTYName: "/dev/ttys003",
		},
	}
	resolver := fakeResolver{
		procs: map[string]session.Proc{"ttys002": {}},
		locate: map[string]session.ClaudeSession{
			"/repo/b|": {SessionID: "sess-abc", ProjectDir: "/proj/b"},
		},
	}

	// --- capture ---
	cap := capture.New(fakeLister{panes: panes}, resolver, "2026-07-23T14:02:11.000Z", "20240101-abcdef")
	snap, err := cap.Run(context.Background())
	if err != nil {
		t.Fatalf("capture.Run: %v", err)
	}
	if len(snap.Windows) != 1 || len(snap.Windows[0].Tabs) != 1 {
		t.Fatalf("expected 1 window/1 tab, got %+v", snap.Windows)
	}
	root := snap.Windows[0].Tabs[0].Layout
	if root == nil || root.Split != "vertical" {
		t.Fatalf("expected a vertical root split, got %+v", root)
	}
	if len(root.Children) != 2 || root.Children[1].Split != "horizontal" {
		t.Fatalf("expected root.Children[1] to be a horizontal split, got %+v", root.Children)
	}

	// --- on-disk round trip via the real Store ---
	store := snapshot.NewStore(t.TempDir())
	if err := store.Save(snap, "20260723T140211.000Z", 20); err != nil {
		t.Fatalf("store.Save: %v", err)
	}
	roundTripped, err := store.Latest()
	if err != nil {
		t.Fatalf("store.Latest: %v", err)
	}

	// --- restore, against the round-tripped snapshot ---
	term := &fakeTerminal{}
	rst := restore.New(term, func(string) bool { return true })
	if err := rst.Run(context.Background(), roundTripped); err != nil {
		t.Fatalf("restore.Run: %v", err)
	}

	// The window is spawned rooted at leaf A's cwd (the first leaf reached by
	// always descending Children[0]).
	if term.spawnedWindowCWD != "/repo/a" {
		t.Fatalf("SpawnWindow cwd = %q, want %q", term.spawnedWindowCWD, "/repo/a")
	}

	// Exactly two splits: the root vertical split, then the nested horizontal
	// split of its right child. This is the regression guard for dual-DFS
	// ordering: if capture and restore ever disagree on leaf order, either the
	// split count, the directions, or the cwds below will diverge.
	if len(term.splits) != 2 {
		t.Fatalf("expected 2 SplitPane calls (one nested split), got %d: %+v", len(term.splits), term.splits)
	}

	// Expected percents, derived straight from the physical geometry above
	// (independent of the production ratio formula, so this is a real oracle
	// rather than a tautology): a split's --percent is the NEW pane's share of
	// the ORIGINAL pane's area. Root: right region is 59 of 79 content columns
	// (A=20, 1 divider, right=59 -> content=79). Nested: C is 15 of 23 content
	// rows (B=8, 1 divider, C=15 -> content=23).
	rootWantPercent := int(math.Round(59.0 / 79.0 * 100))   // ~74.68 -> 75
	nestedWantPercent := int(math.Round(15.0 / 23.0 * 100)) // ~65.22 -> 65

	rootSplit := term.splits[0]
	if rootSplit.parent != term.rootPane {
		t.Fatalf("root split parent = %d, want the spawned window's root pane %d", rootSplit.parent, term.rootPane)
	}
	if rootSplit.dir != wezterm.DirRight {
		t.Fatalf("root split dir = %q, want %q (vertical split -> --right)", rootSplit.dir, wezterm.DirRight)
	}
	if abs(rootSplit.percent-rootWantPercent) > 1 {
		t.Fatalf("root split percent = %d, want %d (+-1); a value near %d would indicate a ratio-inversion bug",
			rootSplit.percent, rootWantPercent, 100-rootWantPercent)
	}
	if rootSplit.cwd != "/repo/b" {
		t.Fatalf("root split cwd = %q, want %q (leaf B, the first leaf of the right subtree)", rootSplit.cwd, "/repo/b")
	}
	panePane := rootSplit.newID // the pane now holding the right subtree (B, before it's split further)

	nestedSplit := term.splits[1]
	if nestedSplit.parent != panePane {
		t.Fatalf("nested split parent = %d, want %d (the pane that held the right subtree)", nestedSplit.parent, panePane)
	}
	if nestedSplit.dir != wezterm.DirBottom {
		t.Fatalf("nested split dir = %q, want %q (horizontal split -> --bottom)", nestedSplit.dir, wezterm.DirBottom)
	}
	if abs(nestedSplit.percent-nestedWantPercent) > 1 {
		t.Fatalf("nested split percent = %d, want %d (+-1); a value near %d would indicate a ratio-inversion bug",
			nestedSplit.percent, nestedWantPercent, 100-nestedWantPercent)
	}
	if nestedSplit.cwd != "/repo/c" {
		t.Fatalf("nested split cwd = %q, want %q (leaf C)", nestedSplit.cwd, "/repo/c")
	}

	// Pane ids in the restored terminal: A is the window's root pane, B is the
	// pane that received the first split (rootSplit.newID, later split again),
	// C is the pane created by the nested split.
	paneIDA := term.rootPane
	paneIDB := panePane
	paneIDC := nestedSplit.newID

	// Only the Claude pane (B) should get a launch command; the plain-shell
	// leaves (A and C) get no SendText call at all.
	if len(term.sends) != 1 {
		t.Fatalf("expected exactly 1 SendText call (only B has a Claude session), got %d: %+v", len(term.sends), term.sends)
	}
	got := term.sends[0]
	if got.pane != paneIDB {
		t.Fatalf("resume sent to pane %d, want pane %d (B)", got.pane, paneIDB)
	}
	if want := "claude --resume sess-abc\n"; got.text != want {
		t.Fatalf("resume text = %q, want %q", got.text, want)
	}
	for _, s := range term.sends {
		if s.pane == paneIDA || s.pane == paneIDC {
			t.Fatalf("plain-shell pane %d unexpectedly received a launch command %q", s.pane, s.text)
		}
	}

	// The originally-active pane (A) must end up activated. It is the only
	// activation in this single-tab window.
	if len(term.activates) == 0 {
		t.Fatal("expected at least one ActivatePane call")
	}
	if last := term.activates[len(term.activates)-1]; last != paneIDA {
		t.Fatalf("final ActivatePane target = %d, want %d (pane A, the originally-active pane)", last, paneIDA)
	}
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}
