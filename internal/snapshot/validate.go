package snapshot

import "fmt"

// ValidateLayout recursively checks that a LayoutNode tree is well-formed: a
// leaf has a non-nil Pane and no children; a split has a nil Pane, exactly
// two non-nil children, and a Split value of "vertical" or "horizontal".
// Consumers that walk a LayoutNode (e.g. restore) should call this before
// relying on invariants like "every split has two children" to avoid panics
// on malformed or hand-edited snapshots.
func ValidateLayout(n *LayoutNode) error {
	if n == nil {
		return fmt.Errorf("layout node is nil")
	}
	if n.Pane != nil {
		if len(n.Children) != 0 {
			return fmt.Errorf("leaf node has %d children, want 0", len(n.Children))
		}
		return nil
	}
	if n.Split != "vertical" && n.Split != "horizontal" {
		return fmt.Errorf("node is neither a leaf (Pane set) nor a valid split (got Split=%q)", n.Split)
	}
	if len(n.Children) != 2 {
		return fmt.Errorf("split node has %d children, want 2", len(n.Children))
	}
	for i, child := range n.Children {
		if child == nil {
			return fmt.Errorf("split node child %d is nil", i)
		}
		if err := ValidateLayout(child); err != nil {
			return fmt.Errorf("child %d: %w", i, err)
		}
	}
	return nil
}
