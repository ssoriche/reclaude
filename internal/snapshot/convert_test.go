package snapshot

import (
	"testing"

	"github.com/ssoriche/reclaude/internal/layout"
)

func TestFromLayoutAttachesLeafPayloads(t *testing.T) {
	tree := &layout.Node{
		Split:    layout.SplitVertical,
		Ratio:    0.5,
		Children: [2]*layout.Node{{LeafID: 1}, {LeafID: 2}},
	}
	leaves := map[int]Leaf{
		1: {CWD: "/a"},
		2: {CWD: "/b", Claude: &ClaudeSession{SessionID: "s"}},
	}
	got := FromLayout(tree, leaves)
	if got.Split != string(layout.SplitVertical) {
		t.Fatalf("split = %q", got.Split)
	}
	if got.Children[0].Pane.CWD != "/a" {
		t.Fatalf("child0 cwd = %q", got.Children[0].Pane.CWD)
	}
	if got.Children[1].Pane.Claude.SessionID != "s" {
		t.Fatalf("child1 session missing")
	}
}

func TestFromLayoutMissingLeafYieldsEmptyLeaf(t *testing.T) {
	tree := &layout.Node{LeafID: 99}
	got := FromLayout(tree, map[int]Leaf{})
	if got.Pane == nil {
		t.Fatal("expected non-nil empty Leaf for missing leaf id")
	}
	if *got.Pane != (Leaf{}) {
		t.Fatalf("expected empty Leaf, got %+v", *got.Pane)
	}
}
