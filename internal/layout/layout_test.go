package layout

import "testing"

func TestBuildTreeSingleLeaf(t *testing.T) {
	rects := []Rect{{ID: 7, Left: 0, Top: 0, Cols: 80, Rows: 24}}
	n, err := BuildTree(rects)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !n.IsLeaf() {
		t.Fatalf("expected leaf, got split %q", n.Split)
	}
	if n.LeafID != 7 {
		t.Fatalf("LeafID = %d, want 7", n.LeafID)
	}
}

func TestBuildTreeVerticalSplit(t *testing.T) {
	// Two panes side by side: left cols 0..39, divider at col 40, right cols 41..80.
	rects := []Rect{
		{ID: 1, Left: 0, Top: 0, Cols: 40, Rows: 24},
		{ID: 2, Left: 41, Top: 0, Cols: 40, Rows: 24},
	}
	n, err := BuildTree(rects)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n.Split != SplitVertical {
		t.Fatalf("Split = %q, want vertical", n.Split)
	}
	if got := n.Children[0].LeafID; got != 1 {
		t.Fatalf("children[0].LeafID = %d, want 1", got)
	}
	if got := n.Children[1].LeafID; got != 2 {
		t.Fatalf("children[1].LeafID = %d, want 2", got)
	}
	// ratio = children[0] cols / content cols = 40 / (40+40) = 0.5
	if n.Ratio < 0.49 || n.Ratio > 0.51 {
		t.Fatalf("Ratio = %f, want ~0.5", n.Ratio)
	}
}

func TestBuildTreeHorizontalSplit(t *testing.T) {
	// Two panes stacked: top rows 0..11, divider row 12, bottom rows 13..24.
	rects := []Rect{
		{ID: 1, Left: 0, Top: 0, Cols: 80, Rows: 12},
		{ID: 2, Left: 0, Top: 13, Cols: 80, Rows: 12},
	}
	n, err := BuildTree(rects)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n.Split != SplitHorizontal {
		t.Fatalf("Split = %q, want horizontal", n.Split)
	}
	if n.Children[0].LeafID != 1 || n.Children[1].LeafID != 2 {
		t.Fatalf("children order wrong: %d,%d", n.Children[0].LeafID, n.Children[1].LeafID)
	}
}

func TestBuildTreeEmptyErrors(t *testing.T) {
	if _, err := BuildTree(nil); err == nil {
		t.Fatal("expected error for empty rects")
	}
}

func TestBuildTreeNonGuillotineErrors(t *testing.T) {
	// Pinwheel layout: no full-span vertical or horizontal cut exists.
	rects := []Rect{
		{ID: 1, Left: 0, Top: 0, Cols: 20, Rows: 10},   // top:    x 0..19, y 0..9
		{ID: 2, Left: 20, Top: 0, Cols: 10, Rows: 20},  // right:  x 20..29, y 0..19
		{ID: 3, Left: 10, Top: 20, Cols: 20, Rows: 10}, // bottom: x 10..29, y 20..29
		{ID: 4, Left: 0, Top: 10, Cols: 10, Rows: 20},  // left:   x 0..9,  y 10..29
		{ID: 5, Left: 10, Top: 10, Cols: 10, Rows: 10}, // center: x 10..19, y 10..19
	}
	if _, err := BuildTree(rects); err == nil {
		t.Fatal("expected error for non-guillotine pinwheel layout")
	}
}

func TestBuildTreeNestedThreeWay(t *testing.T) {
	// Left column (id 1) spanning full height; right side split top/bottom (ids 2,3).
	// Vertical cut at col 40 (divider), right region cols 41..80.
	// Right region horizontal cut at row 12 (divider), bottom rows 13..24.
	rects := []Rect{
		{ID: 1, Left: 0, Top: 0, Cols: 40, Rows: 24},
		{ID: 2, Left: 41, Top: 0, Cols: 40, Rows: 12},
		{ID: 3, Left: 41, Top: 13, Cols: 40, Rows: 12},
	}
	n, err := BuildTree(rects)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n.Split != SplitVertical {
		t.Fatalf("root Split = %q, want vertical", n.Split)
	}
	if !n.Children[0].IsLeaf() || n.Children[0].LeafID != 1 {
		t.Fatalf("children[0] should be leaf 1")
	}
	right := n.Children[1]
	if right.Split != SplitHorizontal {
		t.Fatalf("right Split = %q, want horizontal", right.Split)
	}
	if right.Children[0].LeafID != 2 || right.Children[1].LeafID != 3 {
		t.Fatalf("right children wrong: %d,%d", right.Children[0].LeafID, right.Children[1].LeafID)
	}
}

func TestLeavesCanonicalOrder(t *testing.T) {
	rects := []Rect{
		{ID: 1, Left: 0, Top: 0, Cols: 40, Rows: 24},
		{ID: 2, Left: 41, Top: 0, Cols: 40, Rows: 12},
		{ID: 3, Left: 41, Top: 13, Cols: 40, Rows: 12},
	}
	n, _ := BuildTree(rects)
	got := Leaves(n)
	want := []int{1, 2, 3} // depth-first, children[0] before children[1]
	if len(got) != len(want) {
		t.Fatalf("len(Leaves) = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("Leaves = %v, want %v", got, want)
		}
	}
}

func TestFlattenRoundTrip(t *testing.T) {
	cases := [][]Rect{
		{{ID: 1, Left: 0, Top: 0, Cols: 80, Rows: 24}},
		{
			{ID: 1, Left: 0, Top: 0, Cols: 40, Rows: 24},
			{ID: 2, Left: 41, Top: 0, Cols: 39, Rows: 24},
		},
		{
			{ID: 1, Left: 0, Top: 0, Cols: 40, Rows: 24},
			{ID: 2, Left: 41, Top: 0, Cols: 39, Rows: 12},
			{ID: 3, Left: 41, Top: 13, Cols: 39, Rows: 11},
		},
	}
	for i, rects := range cases {
		n, err := BuildTree(rects)
		if err != nil {
			t.Fatalf("case %d: BuildTree: %v", i, err)
		}
		bb := boundingBox(rects)
		got := Flatten(n, bb)
		assertSameRects(t, i, got, rects)
	}
}

func boundingBox(rects []Rect) Rect {
	minL, minT := rects[0].Left, rects[0].Top
	maxR, maxB := rects[0].Left+rects[0].Cols-1, rects[0].Top+rects[0].Rows-1
	for _, r := range rects[1:] {
		minL = min(minL, r.Left)
		minT = min(minT, r.Top)
		maxR = max(maxR, r.Left+r.Cols-1)
		maxB = max(maxB, r.Top+r.Rows-1)
	}
	return Rect{Left: minL, Top: minT, Cols: maxR - minL + 1, Rows: maxB - minT + 1}
}

func assertSameRects(t *testing.T, caseIdx int, got, want []Rect) {
	t.Helper()
	byID := map[int]Rect{}
	for _, r := range got {
		byID[r.ID] = r
	}
	if len(got) != len(want) {
		t.Fatalf("case %d: got %d rects, want %d", caseIdx, len(got), len(want))
	}
	for _, w := range want {
		g, ok := byID[w.ID]
		if !ok {
			t.Fatalf("case %d: missing rect id %d", caseIdx, w.ID)
		}
		// Allow ±1 cell tolerance from integer ratio rounding.
		if abs(g.Left-w.Left) > 1 || abs(g.Top-w.Top) > 1 ||
			abs(g.Cols-w.Cols) > 1 || abs(g.Rows-w.Rows) > 1 {
			t.Fatalf("case %d id %d: got %+v, want %+v", caseIdx, w.ID, g, w)
		}
	}
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}
