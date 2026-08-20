package capture

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/ssoriche/reclaude/internal/session"
	"github.com/ssoriche/reclaude/internal/snapshot"
	"github.com/ssoriche/reclaude/internal/wezterm"
)

func TestNormalizeCWD(t *testing.T) {
	cases := map[string]string{
		"file://mac.local/Volumes/zr/proj": "/Volumes/zr/proj",
		"file:///Users/x/code":             "/Users/x/code",
		"/already/plain":                   "/already/plain",
	}
	for in, want := range cases {
		if got := normalizeCWD(in); got != want {
			t.Fatalf("normalizeCWD(%q) = %q, want %q", in, got, want)
		}
	}
}

type fakeLister struct {
	panes []wezterm.Pane
	err   error
}

func (f fakeLister) List(context.Context) ([]wezterm.Pane, error) { return f.panes, f.err }

// fakeResolver implements capture.SessionResolver. procs is the tty->Proc map
// (which ttys are running claude); locate answers the assignment for each pane
// whose tty is in procs, keyed by "<cwd>|<resumeID>".
type fakeResolver struct {
	procs   map[string]session.Proc
	locate  map[string]session.ClaudeSession
	procErr error
}

func (f fakeResolver) Procs(context.Context) (map[string]session.Proc, error) {
	return f.procs, f.procErr
}

func (f fakeResolver) Assign(panes []session.PaneProc) map[int]session.ClaudeSession {
	out := make(map[int]session.ClaudeSession, len(panes))
	for _, p := range panes {
		if sess, ok := f.locate[p.CWD+"|"+p.ResumeID]; ok {
			out[p.PaneID] = sess
		}
	}
	return out
}

// realAssignResolver pairs canned procs with the real attribution logic, so a
// capture-level test exercises the actual assignment rules rather than a stub.
type realAssignResolver struct {
	procs map[string]session.Proc
	real  *session.Resolver
}

func (r realAssignResolver) Procs(context.Context) (map[string]session.Proc, error) {
	return r.procs, nil
}

func (r realAssignResolver) Assign(panes []session.PaneProc) map[int]session.ClaudeSession {
	return r.real.Assign(panes)
}

// writeTranscript writes a transcript whose recorded session start is at,
// mirroring the untimestamped header Claude writes at the top of a .jsonl.
func writeTranscript(t *testing.T, dir, sessionID string, at time.Time) {
	t.Helper()
	body := `{"type":"last-prompt","sessionId":"` + sessionID + `"}` + "\n" +
		`{"type":"attachment","timestamp":"` + at.UTC().Format(time.RFC3339Nano) + `"}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, sessionID+".jsonl"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// Regression: two tabs in one directory, each running its own bare `claude`,
// were resolved independently and both took the newest transcript. A restore
// then resumed that one session twice and lost the other session entirely.
func TestCaptureGivesTabsSharingACWDDistinctSessions(t *testing.T) {
	root := t.TempDir()
	cwd := "/Volumes/proj"
	projectDir := filepath.Join(root, session.SlugFor(cwd))
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}

	base := time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)
	writeTranscript(t, projectDir, "sess-early", base.Add(2*time.Second))
	writeTranscript(t, projectDir, "sess-late", base.Add(time.Hour+2*time.Second))

	// Two tabs in one window, same cwd, neither launched with --resume.
	panes := []wezterm.Pane{
		{WindowID: 0, TabID: 0, PaneID: 1, Size: wezterm.Size{Cols: 80, Rows: 24},
			CWD: "file://h" + cwd, TTYName: "/dev/ttys003", IsActive: true, WindowTitle: "W"},
		{WindowID: 0, TabID: 1, PaneID: 2, Size: wezterm.Size{Cols: 80, Rows: 24},
			CWD: "file://h" + cwd, TTYName: "/dev/ttys004", WindowTitle: "W"},
	}
	resolver := realAssignResolver{
		procs: map[string]session.Proc{
			"ttys003": {StartedAt: base},
			"ttys004": {StartedAt: base.Add(time.Hour)},
		},
		real: session.New(nil, root),
	}

	snap, err := New(fakeLister{panes: panes}, resolver, "stamp", "ver").Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	got := map[int]string{}
	for i, tab := range snap.Windows[0].Tabs {
		if tab.Layout == nil || tab.Layout.Pane == nil || tab.Layout.Pane.Claude == nil {
			t.Fatalf("tab %d has no claude leaf: %+v", i, tab.Layout)
		}
		c := tab.Layout.Pane.Claude
		if !c.Resumable {
			t.Fatalf("tab %d is not resumable, want a distinct session", i)
		}
		got[i] = c.SessionID
	}

	if got[0] == got[1] {
		t.Fatalf("both tabs captured the same session %q", got[0])
	}
	if got[0] != "sess-early" || got[1] != "sess-late" {
		t.Fatalf("sessions = %v, want tab0=sess-early tab1=sess-late", got)
	}
}

func TestCaptureBuildsSnapshot(t *testing.T) {
	panes := []wezterm.Pane{
		{WindowID: 0, TabID: 0, PaneID: 1, LeftCol: 0, TopRow: 0,
			Size: wezterm.Size{Cols: 40, Rows: 24}, CWD: "file://h/a", TTYName: "/dev/ttys003",
			IsActive: true, WindowTitle: "W"},
		{WindowID: 0, TabID: 0, PaneID: 2, LeftCol: 41, TopRow: 0,
			Size: wezterm.Size{Cols: 40, Rows: 24}, CWD: "file://h/b", TTYName: "/dev/ttys004"},
	}
	lister := fakeLister{panes: panes}
	resolver := fakeResolver{
		procs: map[string]session.Proc{"ttys003": {}},
		locate: map[string]session.ClaudeSession{
			"/a|": {SessionID: "s1", ProjectDir: "/p/s1"},
		},
	}
	c := New(lister, resolver, "2026-07-23T14:02:11.000Z", "0-unstable")
	snap, err := c.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(snap.Windows) != 1 || len(snap.Windows[0].Tabs) != 1 {
		t.Fatalf("expected 1 window/1 tab, got %+v", snap.Windows)
	}
	root := snap.Windows[0].Tabs[0].Layout
	if root.Split != "vertical" {
		t.Fatalf("root split = %q", root.Split)
	}
	left := root.Children[0].Pane
	if left.Claude == nil || left.Claude.SessionID != "s1" || !left.Claude.Resumable {
		t.Fatalf("pane 1 should carry resumable session s1: %+v", left)
	}
	if root.Children[1].Pane.Claude != nil {
		t.Fatal("pane 2 should have no claude session (tty not running claude)")
	}
	if snap.Windows[0].Tabs[0].ActivePaneIndex != 0 {
		t.Fatalf("active pane index = %d, want 0", snap.Windows[0].Tabs[0].ActivePaneIndex)
	}
}

func TestCaptureClaudeRunningButNoLocalTranscriptIsBareRelaunchMarker(t *testing.T) {
	panes := []wezterm.Pane{
		{WindowID: 0, TabID: 0, PaneID: 1, LeftCol: 0, TopRow: 0,
			Size: wezterm.Size{Cols: 40, Rows: 24}, CWD: "file://h/a", TTYName: "/dev/ttys003"},
	}
	resolver := fakeResolver{
		procs:  map[string]session.Proc{"ttys003": {}},
		locate: map[string]session.ClaudeSession{}, // nothing is assignable
	}
	c := New(fakeLister{panes: panes}, resolver, "stamp", "v")
	snap, err := c.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	tab := snap.Windows[0].Tabs[0]
	leaf := tab.Layout.Pane
	if leaf == nil || leaf.Claude == nil {
		t.Fatalf("expected a Claude marker, got %+v", tab.Layout)
	}
	if leaf.Claude.Resumable {
		t.Fatal("Resumable should be false when no local transcript was located")
	}
	if leaf.Claude.SessionID != "" || leaf.Claude.ProjectDir != "" {
		t.Fatalf("expected empty SessionID/ProjectDir marker, got %+v", leaf.Claude)
	}
}

func TestCaptureMultiWindowMultiTabOrdering(t *testing.T) {
	panes := []wezterm.Pane{
		// Window 1 appears first in the pane list, but has no active pane.
		{WindowID: 1, TabID: 0, PaneID: 100, LeftCol: 0, TopRow: 0,
			Size: wezterm.Size{Cols: 40, Rows: 24}, CWD: "file://h/x", TTYName: "/dev/ttysA"},
		// Window 0's tab 0 carries the active pane.
		{WindowID: 0, TabID: 0, PaneID: 1, LeftCol: 0, TopRow: 0,
			Size: wezterm.Size{Cols: 40, Rows: 24}, CWD: "file://h/a", TTYName: "/dev/ttysB", IsActive: true},
		// Window 0's tab 1 is a second tab in the same window.
		{WindowID: 0, TabID: 1, PaneID: 2, LeftCol: 0, TopRow: 0,
			Size: wezterm.Size{Cols: 40, Rows: 24}, CWD: "file://h/b", TTYName: "/dev/ttysC"},
	}
	c := New(fakeLister{panes: panes}, fakeResolver{}, "stamp", "v")
	snap, err := c.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(snap.Windows) != 2 {
		t.Fatalf("expected 2 windows, got %d", len(snap.Windows))
	}
	// First-seen order: window 1 before window 0.
	if len(snap.Windows[0].Tabs) != 1 {
		t.Fatalf("window[0] (id 1) expected 1 tab, got %d", len(snap.Windows[0].Tabs))
	}
	if len(snap.Windows[1].Tabs) != 2 {
		t.Fatalf("window[1] (id 0) expected 2 tabs, got %d", len(snap.Windows[1].Tabs))
	}
	if snap.Windows[0].ActiveTabIndex != 0 {
		t.Fatalf("window[0] ActiveTabIndex = %d, want 0 (no active pane)", snap.Windows[0].ActiveTabIndex)
	}
	if snap.Windows[1].ActiveTabIndex != 0 {
		t.Fatalf("window[1] ActiveTabIndex = %d, want 0 (active pane is in its first tab)", snap.Windows[1].ActiveTabIndex)
	}
}

func TestCaptureZoomedPaneAndTitle(t *testing.T) {
	panes := []wezterm.Pane{
		{WindowID: 0, TabID: 0, PaneID: 1, LeftCol: 0, TopRow: 0,
			Size: wezterm.Size{Cols: 40, Rows: 24}, CWD: "file://h/a", TTYName: "/dev/ttys003"},
		{WindowID: 0, TabID: 0, PaneID: 2, LeftCol: 41, TopRow: 0,
			Size: wezterm.Size{Cols: 40, Rows: 24}, CWD: "file://h/b", TTYName: "/dev/ttys004",
			IsZoomed: true, TabTitle: "my-tab"},
	}
	c := New(fakeLister{panes: panes}, fakeResolver{}, "stamp", "v")
	snap, err := c.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	tab := snap.Windows[0].Tabs[0]
	if tab.ZoomedPaneIndex == nil || *tab.ZoomedPaneIndex != 1 {
		t.Fatalf("ZoomedPaneIndex = %v, want pointer to 1", tab.ZoomedPaneIndex)
	}
	if tab.Title != "my-tab" {
		t.Fatalf("Tab.Title = %q, want %q", tab.Title, "my-tab")
	}
}

func TestCaptureDegenerateLayoutPreservesAllPanes(t *testing.T) {
	// Pinwheel layout: no full-span vertical or horizontal cut exists, so
	// BuildTree fails and capture must fall back to an approximate stacked
	// layout that PRESERVES EVERY PANE (dropping panes would lose Claude
	// sessions — this is the zoomed-tab case in the wild).
	panes := []wezterm.Pane{
		{WindowID: 0, TabID: 0, PaneID: 1, LeftCol: 0, TopRow: 0, Size: wezterm.Size{Cols: 20, Rows: 10}, CWD: "/a", IsActive: true},
		{WindowID: 0, TabID: 0, PaneID: 2, LeftCol: 20, TopRow: 0, Size: wezterm.Size{Cols: 10, Rows: 20}, CWD: "/b"},
		{WindowID: 0, TabID: 0, PaneID: 3, LeftCol: 10, TopRow: 20, Size: wezterm.Size{Cols: 20, Rows: 10}, CWD: "/c"},
		{WindowID: 0, TabID: 0, PaneID: 4, LeftCol: 0, TopRow: 10, Size: wezterm.Size{Cols: 10, Rows: 20}, CWD: "/d"},
		{WindowID: 0, TabID: 0, PaneID: 5, LeftCol: 10, TopRow: 10, Size: wezterm.Size{Cols: 10, Rows: 10}, CWD: "/e"},
	}
	c := New(fakeLister{panes: panes}, fakeResolver{}, "stamp", "v")
	snap, err := c.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	tab := snap.Windows[0].Tabs[0]

	// Every pane must survive: collect all leaf cwds from the tree.
	var cwds []string
	var walk func(n *snapshot.LayoutNode)
	walk = func(n *snapshot.LayoutNode) {
		if n == nil {
			return
		}
		if n.Pane != nil {
			cwds = append(cwds, n.Pane.CWD)
			return
		}
		for _, ch := range n.Children {
			walk(ch)
		}
	}
	walk(tab.Layout)
	if len(cwds) != 5 {
		t.Fatalf("expected all 5 panes preserved, got %d: %v", len(cwds), cwds)
	}
	for _, want := range []string{"/a", "/b", "/c", "/d", "/e"} {
		if !slices.Contains(cwds, want) {
			t.Fatalf("pane %s dropped; got %v", want, cwds)
		}
	}
	// The produced tree must be structurally valid for restore.
	if err := snapshot.ValidateLayout(tab.Layout); err != nil {
		t.Fatalf("degenerate layout failed validation: %v", err)
	}
	// Canonical order is the input order, so the active pane (id 1) is index 0.
	if tab.ActivePaneIndex != 0 {
		t.Fatalf("active pane index = %d, want 0", tab.ActivePaneIndex)
	}
}

func TestCaptureRunPropagatesListError(t *testing.T) {
	wantErr := errors.New("list boom")
	c := New(fakeLister{err: wantErr}, fakeResolver{}, "stamp", "v")
	if _, err := c.Run(context.Background()); !errors.Is(err, wantErr) {
		t.Fatalf("Run error = %v, want %v", err, wantErr)
	}
}

func TestCaptureRunPropagatesResolveError(t *testing.T) {
	wantErr := errors.New("resolve boom")
	c := New(fakeLister{}, fakeResolver{procErr: wantErr}, "stamp", "v")
	if _, err := c.Run(context.Background()); !errors.Is(err, wantErr) {
		t.Fatalf("Run error = %v, want %v", err, wantErr)
	}
}
