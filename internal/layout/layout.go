// Package layout reconstructs a guillotine split-tree from WezTerm pane
// rectangles. It is pure geometry: no I/O and no other internal imports.
package layout

import (
	"fmt"
	"sort"
)

// Split direction of an interior node.
type Split string

const (
	SplitVertical   Split = "vertical"   // divider is vertical; children are left|right
	SplitHorizontal Split = "horizontal" // divider is horizontal; children are top/bottom
)

// Rect is a pane's cell geometry within a tab.
type Rect struct {
	ID   int
	Left int
	Top  int
	Cols int
	Rows int
}

// Node is a guillotine tree node: a leaf (Split == "") or an interior split.
type Node struct {
	Split    Split
	Ratio    float64  // fraction of content extent given to Children[0]
	Children [2]*Node // nil for a leaf
	LeafID   int      // valid only for a leaf
}

// IsLeaf reports whether n is a leaf node.
func (n *Node) IsLeaf() bool { return n.Split == "" }

// BuildTree reconstructs the guillotine split-tree for the given rects.
func BuildTree(rects []Rect) (*Node, error) {
	if len(rects) == 0 {
		return nil, fmt.Errorf("layout: BuildTree needs at least one rect")
	}
	return build(rects)
}

func build(rects []Rect) (*Node, error) {
	if len(rects) == 1 {
		return &Node{LeafID: rects[0].ID}, nil
	}
	// Prefer a vertical cut (documented, stable ordering), then horizontal.
	if left, right, ratio, ok := cut(rects, true); ok {
		return assemble(SplitVertical, ratio, left, right)
	}
	if top, bottom, ratio, ok := cut(rects, false); ok {
		return assemble(SplitHorizontal, ratio, top, bottom)
	}
	return nil, fmt.Errorf("layout: no guillotine cut found for %d rects", len(rects))
}

func assemble(dir Split, ratio float64, a, b []Rect) (*Node, error) {
	c0, err := build(a)
	if err != nil {
		return nil, err
	}
	c1, err := build(b)
	if err != nil {
		return nil, err
	}
	return &Node{Split: dir, Ratio: ratio, Children: [2]*Node{c0, c1}}, nil
}

// cut attempts a single full-span guillotine cut. When vertical is true it
// looks for an x boundary; otherwise a y boundary. Returns the two rect groups
// (first = left/top), the ratio of the first group's extent over content
// extent, and ok=false if no clean cut exists.
func cut(rects []Rect, vertical bool) (first, second []Rect, ratio float64, ok bool) {
	// Bounding box of the region.
	minStart, maxEnd := bounds(rects, vertical)
	// Candidate cut positions: the start coordinate of each rect (except the
	// region start). A clean cut has every rect entirely on one side of it.
	starts := map[int]struct{}{}
	for _, r := range rects {
		s := start(r, vertical)
		if s != minStart {
			starts[s] = struct{}{}
		}
	}
	candidates := make([]int, 0, len(starts))
	for s := range starts {
		candidates = append(candidates, s)
	}
	sort.Ints(candidates)

	for _, x := range candidates {
		var a, b []Rect
		clean := true
		for _, r := range rects {
			s, e := start(r, vertical), end(r, vertical)
			switch {
			case e < x: // wholly before the cut (accounting for divider cell)
				a = append(a, r)
			case s >= x: // wholly at/after the cut
				b = append(b, r)
			default: // straddles the cut line -> not a guillotine cut here
				clean = false
			}
		}
		if !clean || len(a) == 0 || len(b) == 0 {
			continue
		}
		// Reject gaps wider than a single divider cell: group a must end
		// exactly at x-2 (divider at x-1, group b starts at x).
		aEnd := end(a[0], vertical)
		for _, r := range a[1:] {
			if e := end(r, vertical); e > aEnd {
				aEnd = e
			}
		}
		if aEnd != x-2 {
			continue
		}
		// content extent = total minus the one divider cell at the cut.
		content := float64((maxEnd - minStart + 1) - 1)
		// first group occupies minStart..x-2 (divider at x-1), so its extent is
		// (x-2) - minStart + 1 = (x-1) - minStart.
		firstExtent := float64((x - 1) - minStart)
		return a, b, firstExtent / content, true
	}
	return nil, nil, 0, false
}

func bounds(rects []Rect, vertical bool) (minStart, maxEnd int) {
	minStart, maxEnd = start(rects[0], vertical), end(rects[0], vertical)
	for _, r := range rects[1:] {
		if s := start(r, vertical); s < minStart {
			minStart = s
		}
		if e := end(r, vertical); e > maxEnd {
			maxEnd = e
		}
	}
	return
}

func start(r Rect, vertical bool) int {
	if vertical {
		return r.Left
	}
	return r.Top
}

func end(r Rect, vertical bool) int {
	if vertical {
		return r.Left + r.Cols - 1
	}
	return r.Top + r.Rows - 1
}

// Leaves returns leaf IDs in canonical order: depth-first, Children[0] before
// Children[1]. Capture uses this to record active/zoomed pane indices; restore
// uses the same ordering to resolve them.
func Leaves(n *Node) []int {
	if n == nil {
		return nil
	}
	if n.IsLeaf() {
		return []int{n.LeafID}
	}
	return append(Leaves(n.Children[0]), Leaves(n.Children[1])...)
}

// Flatten is the geometric inverse of BuildTree, used only in tests. It lays
// the tree out within bb, inserting one divider cell at each split.
func Flatten(n *Node, bb Rect) []Rect {
	if n == nil {
		return nil
	}
	if n.IsLeaf() {
		r := bb
		r.ID = n.LeafID
		return []Rect{r}
	}
	if n.Split == SplitVertical {
		content := bb.Cols - 1 // one divider column
		firstCols := int(float64(content)*n.Ratio + 0.5)
		left := Rect{Left: bb.Left, Top: bb.Top, Cols: firstCols, Rows: bb.Rows}
		right := Rect{Left: bb.Left + firstCols + 1, Top: bb.Top, Cols: content - firstCols, Rows: bb.Rows}
		return append(Flatten(n.Children[0], left), Flatten(n.Children[1], right)...)
	}
	content := bb.Rows - 1 // one divider row
	firstRows := int(float64(content)*n.Ratio + 0.5)
	top := Rect{Left: bb.Left, Top: bb.Top, Cols: bb.Cols, Rows: firstRows}
	bottom := Rect{Left: bb.Left, Top: bb.Top + firstRows + 1, Cols: bb.Cols, Rows: content - firstRows}
	return append(Flatten(n.Children[0], top), Flatten(n.Children[1], bottom)...)
}
