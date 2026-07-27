package snapshot

import "github.com/ssoriche/reclaude/internal/layout"

// FromLayout converts a geometric layout tree plus a leaf-payload map into a
// serializable LayoutNode. A missing leaf id yields an empty Leaf (defensive).
func FromLayout(n *layout.Node, leaves map[int]Leaf) *LayoutNode {
	if n == nil {
		return nil
	}
	if n.IsLeaf() {
		leaf := leaves[n.LeafID]
		return &LayoutNode{Pane: &leaf}
	}
	return &LayoutNode{
		Split: string(n.Split),
		Ratio: n.Ratio,
		Children: []*LayoutNode{
			FromLayout(n.Children[0], leaves),
			FromLayout(n.Children[1], leaves),
		},
	}
}
