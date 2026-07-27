package restore

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/ssoriche/reclaude/internal/snapshot"
	"github.com/ssoriche/reclaude/internal/wezterm"
)

func TestLaunchCommand(t *testing.T) {
	resumable := &snapshot.Leaf{Claude: &snapshot.ClaudeSession{SessionID: "abc"}}
	if got := launchCommand(resumable, true); got != "claude --resume abc\n" {
		t.Fatalf("resumable = %q", got)
	}
	if got := launchCommand(resumable, false); got != "claude\n" {
		t.Fatalf("not resumable = %q", got)
	}
	plain := &snapshot.Leaf{}
	if got := launchCommand(plain, false); got != "" {
		t.Fatalf("non-claude = %q, want empty", got)
	}
}

func TestIsResumableChecksFile(t *testing.T) {
	leaf := &snapshot.Leaf{Claude: &snapshot.ClaudeSession{SessionID: "abc", ProjectDir: "/p"}}
	exists := func(path string) bool { return path == "/p/abc.jsonl" }
	if !isResumable(leaf, exists) {
		t.Fatal("should be resumable when jsonl exists")
	}
	if isResumable(leaf, func(string) bool { return false }) {
		t.Fatal("should not be resumable when jsonl missing")
	}
	if isResumable(&snapshot.Leaf{}, exists) {
		t.Fatal("non-claude leaf is never resumable")
	}
}

type fakeTerm struct {
	calls    []string
	nextID   int
	winID    int
	splitErr error // when set, SplitPane fails instead of succeeding
}

func (f *fakeTerm) SpawnWindow(_ context.Context, cwd string) (int, error) {
	f.nextID++
	f.winID = 100 // canned window id returned by WindowIDForPane
	f.calls = append(f.calls, "spawn-window "+cwd)
	return f.nextID, nil
}
func (f *fakeTerm) SpawnTab(_ context.Context, windowID int, cwd string) (int, error) {
	f.nextID++
	f.calls = append(f.calls, fmt.Sprintf("spawn-tab win=%d %s -> %d", windowID, cwd, f.nextID))
	return f.nextID, nil
}
func (f *fakeTerm) WindowIDForPane(context.Context, int) (int, error) { return f.winID, nil }
func (f *fakeTerm) SplitPane(_ context.Context, pane int, dir wezterm.Direction, pct int, cwd string) (int, error) {
	if f.splitErr != nil {
		f.calls = append(f.calls, fmt.Sprintf("split-error %d %s %d %s", pane, dir, pct, cwd))
		return 0, f.splitErr
	}
	f.nextID++
	f.calls = append(f.calls, fmt.Sprintf("split %d %s %d %s -> %d", pane, dir, pct, cwd, f.nextID))
	return f.nextID, nil
}
func (f *fakeTerm) SendText(_ context.Context, pane int, text string) error {
	f.calls = append(f.calls, fmt.Sprintf("send %d %q", pane, text))
	return nil
}
func (f *fakeTerm) ActivatePane(_ context.Context, pane int) error {
	f.calls = append(f.calls, fmt.Sprintf("activate %d", pane))
	return nil
}
func (f *fakeTerm) SetTabTitle(_ context.Context, pane int, title string) error {
	f.calls = append(f.calls, fmt.Sprintf("tab-title %d %q", pane, title))
	return nil
}
func (f *fakeTerm) SetWindowTitle(_ context.Context, pane int, title string) error {
	f.calls = append(f.calls, fmt.Sprintf("win-title %d %q", pane, title))
	return nil
}
func (f *fakeTerm) ZoomPane(_ context.Context, pane int) error {
	f.calls = append(f.calls, fmt.Sprintf("zoom %d", pane))
	return nil
}

func containsCall(calls []string, substr string) bool {
	for _, c := range calls {
		if strings.Contains(c, substr) {
			return true
		}
	}
	return false
}

func TestRestoreVerticalSplitSequence(t *testing.T) {
	snap := &snapshot.Snapshot{
		Version: 1,
		Windows: []snapshot.Window{{
			Title: "W",
			Tabs: []snapshot.Tab{{
				Layout: &snapshot.LayoutNode{
					Split: "vertical", Ratio: 0.5,
					Children: []*snapshot.LayoutNode{
						{Pane: &snapshot.Leaf{CWD: "/a", Claude: &snapshot.ClaudeSession{SessionID: "s1"}}},
						{Pane: &snapshot.Leaf{CWD: "/b"}},
					},
				},
			}},
		}},
	}
	term := &fakeTerm{}
	r := New(term, func(string) bool { return true })
	if err := r.Run(context.Background(), snap); err != nil {
		t.Fatalf("Run: %v", err)
	}
	joined := strings.Join(term.calls, "\n")
	if !strings.Contains(joined, "spawn-window /a") {
		t.Fatalf("expected window spawned in /a:\n%s", joined)
	}
	// children[1] new pane on the right at percent round((1-0.5)*100)=50, cwd /b
	if !strings.Contains(joined, "--right 50 /b") {
		t.Fatalf("expected right split at 50 into /b:\n%s", joined)
	}
	if !strings.Contains(joined, `send 1 "claude --resume s1\n"`) {
		t.Fatalf("expected resume sent to pane 1:\n%s", joined)
	}
}

func TestRestoreSecondTabUsesSpawnTab(t *testing.T) {
	zoom := 0
	snap := &snapshot.Snapshot{
		Version: 1,
		Windows: []snapshot.Window{{
			Title: "W",
			Tabs: []snapshot.Tab{
				{Layout: &snapshot.LayoutNode{Pane: &snapshot.Leaf{CWD: "/t1"}}},
				{ZoomedPaneIndex: &zoom, ActivePaneIndex: 0,
					Layout: &snapshot.LayoutNode{Pane: &snapshot.Leaf{CWD: "/t2"}}},
			},
		}},
	}
	term := &fakeTerm{}
	r := New(term, func(string) bool { return true })
	if err := r.Run(context.Background(), snap); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !containsCall(term.calls, "spawn-tab win=100 /t2") {
		t.Fatalf("expected spawn-tab for second tab into window 100: %v", term.calls)
	}
	if !containsCall(term.calls, "zoom") {
		t.Fatalf("expected zoom for second tab's zoomed pane: %v", term.calls)
	}
}

func TestRestoreNestedTreeCanonicalOrder(t *testing.T) {
	// root: vertical split of A | (B over C)
	snap := &snapshot.Snapshot{
		Version: 1,
		Windows: []snapshot.Window{{
			Tabs: []snapshot.Tab{{
				Layout: &snapshot.LayoutNode{
					Split: "vertical", Ratio: 0.5,
					Children: []*snapshot.LayoutNode{
						{Pane: &snapshot.Leaf{CWD: "/a", Claude: &snapshot.ClaudeSession{SessionID: "sA"}}},
						{
							Split: "horizontal", Ratio: 0.5,
							Children: []*snapshot.LayoutNode{
								{Pane: &snapshot.Leaf{CWD: "/b", Claude: &snapshot.ClaudeSession{SessionID: "sB"}}},
								{Pane: &snapshot.Leaf{CWD: "/c", Claude: &snapshot.ClaudeSession{SessionID: "sC"}}},
							},
						},
					},
				},
			}},
		}},
	}
	term := &fakeTerm{}
	r := New(term, func(string) bool { return true })
	if err := r.Run(context.Background(), snap); err != nil {
		t.Fatalf("Run: %v", err)
	}
	want := []string{
		"spawn-window /a",
		"split 1 --right 50 /b -> 2",
		`send 1 "claude --resume sA\n"`,
		"split 2 --bottom 50 /c -> 3",
		`send 2 "claude --resume sB\n"`,
		`send 3 "claude --resume sC\n"`,
		"activate 1",
	}
	if len(term.calls) != len(want) {
		t.Fatalf("call count = %d, want %d: got %v", len(term.calls), len(want), term.calls)
	}
	for i, w := range want {
		if term.calls[i] != w {
			t.Fatalf("call[%d] = %q, want %q (full: %v)", i, term.calls[i], w, term.calls)
		}
	}
}

func TestRestoreHorizontalSplitUsesBottom(t *testing.T) {
	snap := &snapshot.Snapshot{
		Version: 1,
		Windows: []snapshot.Window{{
			Tabs: []snapshot.Tab{{
				Layout: &snapshot.LayoutNode{
					Split: "horizontal", Ratio: 0.5,
					Children: []*snapshot.LayoutNode{
						{Pane: &snapshot.Leaf{CWD: "/a"}},
						{Pane: &snapshot.Leaf{CWD: "/b"}},
					},
				},
			}},
		}},
	}
	term := &fakeTerm{}
	r := New(term, func(string) bool { return true })
	if err := r.Run(context.Background(), snap); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !containsCall(term.calls, "--bottom 50 /b") {
		t.Fatalf("expected bottom split at 50 into /b: %v", term.calls)
	}
}

func TestRestoreZoomIndexOutOfRange(t *testing.T) {
	outOfRange := 5
	snap := &snapshot.Snapshot{
		Version: 1,
		Windows: []snapshot.Window{{
			Tabs: []snapshot.Tab{{
				ZoomedPaneIndex: &outOfRange,
				Layout:          &snapshot.LayoutNode{Pane: &snapshot.Leaf{CWD: "/a"}},
			}},
		}},
	}
	term := &fakeTerm{}
	r := New(term, func(string) bool { return true })
	if err := r.Run(context.Background(), snap); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if containsCall(term.calls, "zoom") {
		t.Fatalf("expected no zoom call for out-of-range index: %v", term.calls)
	}
}

func TestRestoreSplitPaneErrorIsBestEffort(t *testing.T) {
	snap := &snapshot.Snapshot{
		Version: 1,
		Windows: []snapshot.Window{{
			Tabs: []snapshot.Tab{{
				Layout: &snapshot.LayoutNode{
					Split: "vertical", Ratio: 0.5,
					Children: []*snapshot.LayoutNode{
						{Pane: &snapshot.Leaf{CWD: "/a", Claude: &snapshot.ClaudeSession{SessionID: "sA"}}},
						{Pane: &snapshot.Leaf{CWD: "/b"}},
					},
				},
			}},
		}},
	}
	term := &fakeTerm{splitErr: errors.New("split failed")}
	r := New(term, func(string) bool { return true })

	var err error
	func() {
		defer func() {
			if p := recover(); p != nil {
				t.Fatalf("Run panicked: %v", p)
			}
		}()
		err = r.Run(context.Background(), snap)
	}()

	if err == nil {
		t.Fatal("expected a non-nil joined error when SplitPane fails")
	}
	if !containsCall(term.calls, `send 1 "claude --resume sA\n"`) {
		t.Fatalf("expected children[0] leaf to still be launched despite split failure: %v", term.calls)
	}
}

func TestRestoreActivatesCapturedActiveTabLast(t *testing.T) {
	// Two tabs; tab 0 is the one that was active when the snapshot was taken
	// (ActiveTabIndex: 0), but tab 1 is built LAST by Run. Without the fix,
	// applyFinishers would leave focus on tab 1 (whichever tab was built
	// last); the fix must re-activate tab 0's active pane afterward.
	snap := &snapshot.Snapshot{
		Version: 1,
		Windows: []snapshot.Window{{
			Title:          "W",
			ActiveTabIndex: 0,
			Tabs: []snapshot.Tab{
				{ActivePaneIndex: 0, Layout: &snapshot.LayoutNode{Pane: &snapshot.Leaf{CWD: "/t1"}}},
				{ActivePaneIndex: 0, Layout: &snapshot.LayoutNode{Pane: &snapshot.Leaf{CWD: "/t2"}}},
			},
		}},
	}
	term := &fakeTerm{}
	r := New(term, func(string) bool { return true })
	if err := r.Run(context.Background(), snap); err != nil {
		t.Fatalf("Run: %v", err)
	}
	// tab 0's root pane is the window's root pane, id 1 (from SpawnWindow).
	if len(term.calls) == 0 {
		t.Fatal("expected at least one call")
	}
	last := term.calls[len(term.calls)-1]
	if last != "activate 1" {
		t.Fatalf("final call = %q, want %q (tab 0's active pane, not tab 1's): %v", last, "activate 1", term.calls)
	}
}

func TestRestoreNonResumableSendsBareClaude(t *testing.T) {
	snap := &snapshot.Snapshot{
		Version: 1,
		Windows: []snapshot.Window{{
			Tabs: []snapshot.Tab{{
				Layout: &snapshot.LayoutNode{
					Pane: &snapshot.Leaf{CWD: "/a", Claude: &snapshot.ClaudeSession{SessionID: "sA", ProjectDir: "/p"}},
				},
			}},
		}},
	}
	term := &fakeTerm{}
	r := New(term, func(string) bool { return false })
	if err := r.Run(context.Background(), snap); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !containsCall(term.calls, `send 1 "claude\n"`) {
		t.Fatalf("expected bare claude launch when session file is missing: %v", term.calls)
	}
	if containsCall(term.calls, "--resume") {
		t.Fatalf("did not expect --resume when not resumable: %v", term.calls)
	}
}
