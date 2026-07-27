package snapshot

import "testing"

func TestValidateLayoutValidLeaf(t *testing.T) {
	n := &LayoutNode{Pane: &Leaf{CWD: "/a"}}
	if err := ValidateLayout(n); err != nil {
		t.Fatalf("valid leaf: %v", err)
	}
}

func TestValidateLayoutValidNestedSplit(t *testing.T) {
	n := &LayoutNode{
		Split: "vertical",
		Ratio: 0.5,
		Children: []*LayoutNode{
			{Pane: &Leaf{CWD: "/a"}},
			{
				Split: "horizontal",
				Ratio: 0.5,
				Children: []*LayoutNode{
					{Pane: &Leaf{CWD: "/b"}},
					{Pane: &Leaf{CWD: "/c"}},
				},
			},
		},
	}
	if err := ValidateLayout(n); err != nil {
		t.Fatalf("valid nested split: %v", err)
	}
}

func TestValidateLayoutSplitWithOneChildErrors(t *testing.T) {
	n := &LayoutNode{
		Split:    "vertical",
		Children: []*LayoutNode{{Pane: &Leaf{CWD: "/a"}}},
	}
	if err := ValidateLayout(n); err == nil {
		t.Fatal("expected error for split with 1 child")
	}
}

func TestValidateLayoutSplitWithNilChildErrors(t *testing.T) {
	n := &LayoutNode{
		Split:    "vertical",
		Children: []*LayoutNode{{Pane: &Leaf{CWD: "/a"}}, nil},
	}
	if err := ValidateLayout(n); err == nil {
		t.Fatal("expected error for split with nil child")
	}
}

func TestValidateLayoutNeitherPaneNorChildrenErrors(t *testing.T) {
	n := &LayoutNode{}
	if err := ValidateLayout(n); err == nil {
		t.Fatal("expected error for node with neither Pane nor Split/Children")
	}
}

func TestValidateLayoutUnknownSplitValueErrors(t *testing.T) {
	n := &LayoutNode{
		Split: "diagonal",
		Children: []*LayoutNode{
			{Pane: &Leaf{CWD: "/a"}},
			{Pane: &Leaf{CWD: "/b"}},
		},
	}
	if err := ValidateLayout(n); err == nil {
		t.Fatal("expected error for unknown split value")
	}
}

func TestValidateLayoutNilNodeErrors(t *testing.T) {
	if err := ValidateLayout(nil); err == nil {
		t.Fatal("expected error for nil node")
	}
}
