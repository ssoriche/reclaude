# reclaude Session Manager Implementation Plan

> **For agentic workers:** REQUIRED: Use superpowers:subagent-driven-development (if subagents available) or superpowers:executing-plans to implement this plan. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build `reclaude`, a Go CLI that captures the full WezTerm window/tab/split-pane layout with each pane's running Claude session, and restores it after a reboot by resuming each conversation with `claude --resume`.

**Architecture:** A single statically-linked Go binary drives the `wezterm cli` subcommands (no plugin/Lua). Packages have one responsibility each; the one real algorithm — reconstructing a guillotine split-tree from pane rectangles — lives in a pure, fully-tested `layout` package. Terminal and OS calls sit behind small interfaces so everything unit-tests against recorded fixtures with no live processes.

**Tech Stack:** Go 1.26, standard library only (`os/exec`, `encoding/json`, `crypto/sha256`, `testing`). External binaries invoked at runtime: `wezterm`, `ps`, `lsof`, `launchctl`.

**Spec:** `docs/superpowers/specs/2026-07-23-reclaude-session-manager-design.md`

## Conventions for the implementing engineer

- **Formatting:** Go code is formatted with `gofmt` (tabs, not spaces). Run `gofmt -w .` before every commit. This overrides any general 4-space preference — gofmt output is canonical.
- **Go version idioms:** Go 1.26 — use `any` (not `interface{}`), generics where they clarify, `errors.Join`/`fmt.Errorf("%w", …)` for wrapping, `slices`/`maps` stdlib packages.
- **Module path:** `github.com/ssoriche/reclaude` (local module; need not resolve remotely).
- **Commits:** Conventional Commits (`feat:`, `test:`, `refactor:`, `docs:`, `chore:`). Commit after each green test cycle. Do not add co-author/credit trailers.
- **Testing:** `go test ./...`. Table-driven tests preferred. No test may invoke a real `wezterm`/`ps`/`lsof`/`launchctl` — inject fakes.
- **Skills:** Use @superpowers:test-driven-development for every task and @superpowers:verification-before-completion before claiming any task done.

## File Structure

```
go.mod
cmd/reclaude/main.go              # CLI entry: arg parse + dispatch only
internal/
  exec/
    runner.go                     # Runner interface + os/exec impl; FakeRunner for tests
    runner_test.go
  layout/
    layout.go                     # Rect, Node, BuildTree, Leaves, Flatten (pure geometry)
    layout_test.go
  wezterm/
    types.go                      # Pane (decoded from `cli list --format json`)
    client.go                     # Client: List/Spawn/SplitPane/SendText/Activate*/SetTitle*/Zoom
    client_test.go
  session/
    resolver.go                   # Resolver: tty -> claude pid -> open .jsonl -> session id
    resolver_test.go
  snapshot/
    model.go                      # Snapshot/Window/Tab/LayoutNode/Leaf/ClaudeSession + JSON tags
    convert.go                    # layout.Node + leaf map -> snapshot.LayoutNode; leaf ordering
    store.go                      # paths, atomic write latest.json, history rotation, read
    hash.go                       # content hash excluding volatile fields
    model_test.go
    convert_test.go
    store_test.go
    hash_test.go
  capture/
    capture.go                    # orchestrate save: list -> resolve -> build -> snapshot
    capture_test.go
  restore/
    restore.go                    # walk snapshot: spawn/split/send-text/finish
    restore_test.go
  daemon/
    daemon.go                     # Run loop with change-detection
    launchd.go                    # install/uninstall/plist for LaunchAgent
    daemon_test.go
    launchd_test.go
```

**Responsibility boundaries:**
- `exec.Runner` is the *only* place that shells out. Every other package that needs a subprocess takes a `Runner`.
- `layout` is pure geometry — no I/O, no other internal imports. It is the crux and gets the deepest tests.
- `snapshot` owns the serialized model and disk persistence. `layout` does **not** import `snapshot`; `snapshot` imports `layout`.
- `capture` and `restore` are thin orchestrators wiring the above together.

---

## Chunk 1: Scaffold + exec.Runner + layout package (the core algorithm)

### Task 1: Project scaffold

**Files:**
- Create: `go.mod`
- Create: `cmd/reclaude/main.go`

- [ ] **Step 1: Initialize the module**

Run:
```bash
go mod init github.com/ssoriche/reclaude
```
Expected: creates `go.mod` with `module github.com/ssoriche/reclaude` and a `go 1.26` line. If `go mod init` emits an older `go` directive, run `go mod edit -go=1.26` to set it.

- [ ] **Step 2: Create a minimal main that builds**

Create `cmd/reclaude/main.go`:
```go
package main

import (
	"fmt"
	"os"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: reclaude <save|restore|list|status|daemon>")
		os.Exit(2)
	}
	// Commands are wired in Chunk 6.
	fmt.Fprintf(os.Stderr, "unknown command: %s\n", os.Args[1])
	os.Exit(2)
}
```

- [ ] **Step 3: Verify it builds**

Run: `go build ./...`
Expected: no output, exit 0.

- [ ] **Step 4: Commit**

```bash
gofmt -w .
git add go.mod cmd/reclaude/main.go
git commit -m "chore: scaffold reclaude go module"
```

---

### Task 2: exec.Runner interface + fake

**Files:**
- Create: `internal/exec/runner.go`
- Test: `internal/exec/runner_test.go`

- [ ] **Step 1: Write the failing test for FakeRunner**

Create `internal/exec/runner_test.go`:
```go
package exec

import (
	"context"
	"errors"
	"testing"
)

func TestFakeRunnerReturnsCannedOutput(t *testing.T) {
	f := &FakeRunner{
		Responses: map[string]FakeResponse{
			"wezterm cli list --format json": {Stdout: []byte("[]")},
		},
	}
	out, err := f.Run(context.Background(), "wezterm", "cli", "list", "--format", "json")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(out) != "[]" {
		t.Fatalf("got %q, want %q", out, "[]")
	}
}

func TestFakeRunnerRecordsCalls(t *testing.T) {
	f := &FakeRunner{Responses: map[string]FakeResponse{"ps -axo pid,tty,command": {}}}
	_, _ = f.Run(context.Background(), "ps", "-axo", "pid,tty,command")
	if len(f.Calls) != 1 || f.Calls[0] != "ps -axo pid,tty,command" {
		t.Fatalf("calls not recorded: %v", f.Calls)
	}
}

func TestFakeRunnerUnknownCommandErrors(t *testing.T) {
	f := &FakeRunner{Responses: map[string]FakeResponse{}}
	_, err := f.Run(context.Background(), "nope")
	if !errors.Is(err, ErrNoFakeResponse) {
		t.Fatalf("want ErrNoFakeResponse, got %v", err)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/exec/`
Expected: FAIL — `undefined: FakeRunner`, `undefined: FakeResponse`, `undefined: ErrNoFakeResponse`.

- [ ] **Step 3: Implement runner.go**

Create `internal/exec/runner.go`:
```go
// Package exec is the single place reclaude shells out to external binaries.
package exec

import (
	"context"
	"errors"
	"os/exec"
	"strings"
)

// Runner runs an external command and returns its stdout.
type Runner interface {
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
}

// OSRunner is the production Runner backed by os/exec.
type OSRunner struct{}

func (OSRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	return cmd.Output()
}

// ErrNoFakeResponse is returned by FakeRunner for an unregistered command.
var ErrNoFakeResponse = errors.New("exec: no fake response registered for command")

// FakeResponse is the canned result for one command line.
type FakeResponse struct {
	Stdout []byte
	Err    error
}

// FakeRunner is a test double keyed by the full "name arg arg ..." command line.
type FakeRunner struct {
	Responses map[string]FakeResponse
	Calls     []string
}

func (f *FakeRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	// Join without trimming: payloads like send-text end in "\n" and must be
	// matched verbatim. No trailing space is produced when args is empty.
	key := strings.Join(append([]string{name}, args...), " ")
	f.Calls = append(f.Calls, key)
	resp, ok := f.Responses[key]
	if !ok {
		return nil, ErrNoFakeResponse
	}
	return resp.Stdout, resp.Err
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/exec/`
Expected: PASS (ok).

- [ ] **Step 5: Commit**

```bash
gofmt -w .
git add internal/exec/
git commit -m "feat: add exec.Runner interface with OS and fake implementations"
```

---

### Task 3: layout types + BuildTree for the base cases

**Files:**
- Create: `internal/layout/layout.go`
- Test: `internal/layout/layout_test.go`

Rectangle model: each pane rect has an integer `ID` (the WezTerm pane_id), `Left`/`Top` (cell offsets: `left_col`/`top_row` from `wezterm cli list`), and `Cols`/`Rows` (`size.cols`/`size.rows`). WezTerm consumes exactly one cell for each split divider, so two side-by-side panes' cols plus 1 equal the parent's cols.

- [ ] **Step 1: Write failing tests for the leaf and 2-way cases**

Create `internal/layout/layout_test.go`:
```go
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
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/layout/`
Expected: FAIL — `undefined: Rect`, `undefined: BuildTree`, etc.

- [ ] **Step 3: Implement layout.go (types + BuildTree)**

Create `internal/layout/layout.go`:
```go
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
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/layout/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
gofmt -w .
git add internal/layout/
git commit -m "feat: add layout.BuildTree for leaf and 2-way splits"
```

---

### Task 4: BuildTree nested cases + Leaves canonical ordering

**Files:**
- Modify: `internal/layout/layout.go` (add `Leaves`)
- Test: `internal/layout/layout_test.go`

- [ ] **Step 1: Write failing tests for nested layouts and Leaves order**

Append to `internal/layout/layout_test.go`:
```go
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
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/layout/`
Expected: FAIL — `undefined: Leaves` (nested BuildTree likely already passes via recursion; the `Leaves` reference fails to compile, which fails the whole package).

- [ ] **Step 3: Implement Leaves**

Append to `internal/layout/layout.go`:
```go
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
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/layout/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
gofmt -w .
git add internal/layout/
git commit -m "feat: support nested layouts and canonical leaf ordering"
```

---

### Task 5: Flatten + round-trip property test

**Files:**
- Modify: `internal/layout/layout.go` (add `Flatten`)
- Test: `internal/layout/layout_test.go`

`Flatten` is the geometric inverse of `BuildTree`, used only in tests. Given a node and the bounding `Rect` it should occupy, it reproduces the leaf rectangles (with the one-cell divider between children).

- [ ] **Step 1: Write the failing round-trip test**

Append to `internal/layout/layout_test.go`:
```go
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
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/layout/`
Expected: FAIL — `undefined: Flatten`.

- [ ] **Step 3: Implement Flatten**

Append to `internal/layout/layout.go`:
```go
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
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/layout/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
gofmt -w .
git add internal/layout/
git commit -m "test: add Flatten inverse and BuildTree round-trip property"
```

---

## Chunk 2: wezterm client + session resolver

### Task 6: wezterm.Pane type + Client.List

**Files:**
- Create: `internal/wezterm/types.go`
- Create: `internal/wezterm/client.go`
- Test: `internal/wezterm/client_test.go`
- Create fixture: `internal/wezterm/testdata/list.json`

- [ ] **Step 1: Save a real fixture**

Run (capture live output as a fixture; safe read-only command):
```bash
mkdir -p internal/wezterm/testdata
wezterm cli list --format json > internal/wezterm/testdata/list.json
```
Expected: a JSON array of pane objects. If WezTerm is not running, hand-write a 2-pane fixture using the fields documented in the spec (`window_id`, `tab_id`, `pane_id`, `size`, `left_col`, `top_row`, `cwd`, `tty_name`, `title`, `window_title`, `workspace`, `is_active`, `is_zoomed`).

- [ ] **Step 2: Write the failing test for List**

Create `internal/wezterm/client_test.go`:
```go
package wezterm

import (
	"context"
	"os"
	"testing"

	iexec "github.com/ssoriche/reclaude/internal/exec"
)

func TestClientListParsesFixture(t *testing.T) {
	data, err := os.ReadFile("testdata/list.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	f := &iexec.FakeRunner{Responses: map[string]iexec.FakeResponse{
		"wezterm cli list --format json": {Stdout: data},
	}}
	c := New(f)
	panes, err := c.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(panes) == 0 {
		t.Fatal("expected at least one pane")
	}
	p := panes[0]
	if p.PaneID == 0 && p.TTYName == "" {
		t.Fatalf("pane not decoded: %+v", p)
	}
}
```

- [ ] **Step 3: Run test to verify it fails**

Run: `go test ./internal/wezterm/`
Expected: FAIL — `undefined: New`, `undefined: Client`.

- [ ] **Step 4: Implement types.go and Client.List**

Create `internal/wezterm/types.go`:
```go
package wezterm

// Pane is one entry from `wezterm cli list --format json`.
type Pane struct {
	WindowID    int    `json:"window_id"`
	TabID       int    `json:"tab_id"`
	PaneID      int    `json:"pane_id"`
	Workspace   string `json:"workspace"`
	Size        Size   `json:"size"`
	Title       string `json:"title"`
	CWD         string `json:"cwd"` // file://host/path
	LeftCol     int    `json:"left_col"`
	TopRow      int    `json:"top_row"`
	TabTitle    string `json:"tab_title"`
	WindowTitle string `json:"window_title"`
	IsActive    bool   `json:"is_active"`
	IsZoomed    bool   `json:"is_zoomed"`
	TTYName     string `json:"tty_name"`
}

// Size is a pane's cell/pixel dimensions.
type Size struct {
	Rows int `json:"rows"`
	Cols int `json:"cols"`
}
```

Create `internal/wezterm/client.go`:
```go
// Package wezterm wraps the `wezterm cli` subcommands behind a Client.
package wezterm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	iexec "github.com/ssoriche/reclaude/internal/exec"
)

// ErrNotRunning indicates the WezTerm mux is unreachable (not running, or the
// binary is missing). Callers treat this as "nothing to capture", not a crash.
var ErrNotRunning = errors.New("wezterm: not running")

// Client drives `wezterm cli`.
type Client struct {
	run iexec.Runner
}

// New returns a Client using the given Runner.
func New(r iexec.Runner) *Client { return &Client{run: r} }

// List returns all panes across all windows/tabs. Any failure to execute the
// command (nonzero exit = mux down, or binary not found) is reported as
// ErrNotRunning; only a successful command with malformed JSON is a decode
// error.
func (c *Client) List(ctx context.Context) ([]Pane, error) {
	out, err := c.run.Run(ctx, "wezterm", "cli", "list", "--format", "json")
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrNotRunning, err)
	}
	var panes []Pane
	if err := json.Unmarshal(out, &panes); err != nil {
		return nil, fmt.Errorf("wezterm cli list: decode: %w", err)
	}
	return panes, nil
}
```

The Task 6 test still passes (the fixture path returns success). Add one test asserting the sentinel:
```go
func TestClientListNotRunning(t *testing.T) {
	f := &iexec.FakeRunner{Responses: map[string]iexec.FakeResponse{
		"wezterm cli list --format json": {Err: errors.New("exit status 1")},
	}}
	if _, err := New(f).List(context.Background()); !errors.Is(err, ErrNotRunning) {
		t.Fatalf("want ErrNotRunning, got %v", err)
	}
}
```
(Add `"errors"` to the test file's import block.)

- [ ] **Step 5: Run test to verify it passes**

Run: `go test ./internal/wezterm/`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
gofmt -w .
git add internal/wezterm/
git commit -m "feat: add wezterm.Client.List with fixture-based test"
```

---

### Task 7: wezterm mutation commands (Spawn/SplitPane/SendText/finishers)

**Files:**
- Modify: `internal/wezterm/client.go`
- Test: `internal/wezterm/client_test.go`

`spawn` and `split-pane` print the new pane id on stdout; parse it. All others return no meaningful stdout.

- [ ] **Step 1: Write failing tests for command construction + pane-id parsing**

Append to `internal/wezterm/client_test.go`:
```go
func TestSpawnNewWindowParsesPaneID(t *testing.T) {
	f := &iexec.FakeRunner{Responses: map[string]iexec.FakeResponse{
		"wezterm cli spawn --new-window --cwd /home/x": {Stdout: []byte("12\n")},
	}}
	c := New(f)
	id, err := c.SpawnWindow(context.Background(), "/home/x")
	if err != nil {
		t.Fatalf("SpawnWindow: %v", err)
	}
	if id != 12 {
		t.Fatalf("pane id = %d, want 12", id)
	}
}

func TestSplitPaneBuildsCommand(t *testing.T) {
	f := &iexec.FakeRunner{Responses: map[string]iexec.FakeResponse{
		"wezterm cli split-pane --pane-id 3 --right --percent 50 --cwd /w": {Stdout: []byte("4\n")},
	}}
	c := New(f)
	id, err := c.SplitPane(context.Background(), 3, DirRight, 50, "/w")
	if err != nil {
		t.Fatalf("SplitPane: %v", err)
	}
	if id != 4 {
		t.Fatalf("pane id = %d, want 4", id)
	}
}

func TestSendTextBuildsCommand(t *testing.T) {
	f := &iexec.FakeRunner{Responses: map[string]iexec.FakeResponse{
		"wezterm cli send-text --pane-id 5 --no-paste claude --resume abc\n": {},
	}}
	c := New(f)
	if err := c.SendText(context.Background(), 5, "claude --resume abc\n"); err != nil {
		t.Fatalf("SendText: %v", err)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/wezterm/`
Expected: FAIL — undefined methods and `DirRight`.

- [ ] **Step 3: Implement the mutation methods**

First, **edit the existing import block** at the top of `internal/wezterm/client.go` to add `strconv` and `strings`, so it reads exactly:
```go
import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	iexec "github.com/ssoriche/reclaude/internal/exec"
)
```
(`errors` is already present from Task 6's `ErrNotRunning`; this step adds `strconv` and `strings`.)

Then append the following declarations to `internal/wezterm/client.go` (no new `import` statement — Go requires all imports in the single block above):
```go
// Direction is the side the new pane lands on for a split.
type Direction string

const (
	DirRight  Direction = "--right"
	DirBottom Direction = "--bottom"
)

func (c *Client) spawnParse(ctx context.Context, args ...string) (int, error) {
	out, err := c.run.Run(ctx, "wezterm", args...)
	if err != nil {
		return 0, fmt.Errorf("wezterm %s: %w", strings.Join(args, " "), err)
	}
	id, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		return 0, fmt.Errorf("wezterm %s: parse pane id %q: %w", strings.Join(args, " "), out, err)
	}
	return id, nil
}

// SpawnWindow opens a new window in cwd; returns the new pane id.
func (c *Client) SpawnWindow(ctx context.Context, cwd string) (int, error) {
	return c.spawnParse(ctx, "cli", "spawn", "--new-window", "--cwd", cwd)
}

// SpawnTab opens a new tab in the given window; returns the new pane id.
func (c *Client) SpawnTab(ctx context.Context, windowID int, cwd string) (int, error) {
	return c.spawnParse(ctx, "cli", "spawn", "--window-id", strconv.Itoa(windowID), "--cwd", cwd)
}

// SplitPane splits paneID toward dir at percent; returns the new pane id.
func (c *Client) SplitPane(ctx context.Context, paneID int, dir Direction, percent int, cwd string) (int, error) {
	return c.spawnParse(ctx, "cli", "split-pane", "--pane-id", strconv.Itoa(paneID),
		string(dir), "--percent", strconv.Itoa(percent), "--cwd", cwd)
}

// SendText pastes text into a pane (no bracketed paste).
func (c *Client) SendText(ctx context.Context, paneID int, text string) error {
	_, err := c.run.Run(ctx, "wezterm", "cli", "send-text", "--pane-id", strconv.Itoa(paneID), "--no-paste", text)
	if err != nil {
		return fmt.Errorf("wezterm cli send-text: %w", err)
	}
	return nil
}
```

Also add finishing-touch methods (`ActivatePane`, `ActivateTab`, `SetTabTitle`, `SetWindowTitle`, `ZoomPane`), each a one-line wrapper over `c.run.Run` returning the wrapped error, following the exact SendText pattern. Include a test for `SetTabTitle` mirroring `TestSendTextBuildsCommand`.

```go
func (c *Client) ActivatePane(ctx context.Context, paneID int) error {
	_, err := c.run.Run(ctx, "wezterm", "cli", "activate-pane", "--pane-id", strconv.Itoa(paneID))
	return wrap("activate-pane", err)
}

func (c *Client) ActivateTab(ctx context.Context, paneID int) error {
	_, err := c.run.Run(ctx, "wezterm", "cli", "activate-tab", "--pane-id", strconv.Itoa(paneID))
	return wrap("activate-tab", err)
}

func (c *Client) SetTabTitle(ctx context.Context, paneID int, title string) error {
	_, err := c.run.Run(ctx, "wezterm", "cli", "set-tab-title", "--pane-id", strconv.Itoa(paneID), title)
	return wrap("set-tab-title", err)
}

func (c *Client) SetWindowTitle(ctx context.Context, paneID int, title string) error {
	_, err := c.run.Run(ctx, "wezterm", "cli", "set-window-title", "--pane-id", strconv.Itoa(paneID), title)
	return wrap("set-window-title", err)
}

func (c *Client) ZoomPane(ctx context.Context, paneID int) error {
	_, err := c.run.Run(ctx, "wezterm", "cli", "zoom-pane", "--pane-id", strconv.Itoa(paneID), "--zoom")
	return wrap("zoom-pane", err)
}

func wrap(cmd string, err error) error {
	if err != nil {
		return fmt.Errorf("wezterm cli %s: %w", cmd, err)
	}
	return nil
}

// WindowIDForPane returns the window_id that owns paneID, by listing panes and
// matching. Restore needs this to spawn additional tabs into the right window
// (spawn only returns a pane id, not a window id).
func (c *Client) WindowIDForPane(ctx context.Context, paneID int) (int, error) {
	panes, err := c.List(ctx)
	if err != nil {
		return 0, err
	}
	for _, p := range panes {
		if p.PaneID == paneID {
			return p.WindowID, nil
		}
	}
	return 0, fmt.Errorf("wezterm: pane %d not found", paneID)
}
```

- [ ] **Step 4: Add a test for WindowIDForPane**

Append to `internal/wezterm/client_test.go` (reuse the fixture; assert the first pane's id resolves to its window id):
```go
func TestWindowIDForPane(t *testing.T) {
	data, _ := os.ReadFile("testdata/list.json")
	f := &iexec.FakeRunner{Responses: map[string]iexec.FakeResponse{
		"wezterm cli list --format json": {Stdout: data},
	}}
	c := New(f)
	panes, _ := c.List(context.Background())
	want := panes[0].WindowID
	got, err := c.WindowIDForPane(context.Background(), panes[0].PaneID)
	if err != nil {
		t.Fatalf("WindowIDForPane: %v", err)
	}
	if got != want {
		t.Fatalf("window id = %d, want %d", got, want)
	}
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/wezterm/`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
gofmt -w .
git add internal/wezterm/
git commit -m "feat: add wezterm spawn/split/send-text, finishers, and WindowIDForPane"
```

---

### Task 8: session.Resolver — tty → claude pid → session id

**Files:**
- Create: `internal/session/resolver.go`
- Test: `internal/session/resolver_test.go`

The resolver builds a `map[tty]ClaudeSession` by: parsing `ps -axo pid,tty,command` to find `claude` processes and their ttys, then for each running `lsof -p <pid> -Fn` to find the open file under `<home>/.claude/projects/*/*.jsonl`. Session ID is the `.jsonl` filename stem; project dir is its parent. The `HOME`-relative projects root is injected for testability.

- [ ] **Step 1: Write failing tests with fake ps/lsof output**

Create `internal/session/resolver_test.go`:
```go
package session

import (
	"context"
	"testing"

	iexec "github.com/ssoriche/reclaude/internal/exec"
)

func TestResolveMapsTTYToSession(t *testing.T) {
	ps := "  PID TTY        COMMAND\n" +
		" 4321 ttys003    claude\n" +
		" 9999 ttys004    -fish\n"
	lsof4321 := "p4321\nn/Users/x/.claude/projects/-Volumes-proj/0b3f-sess.jsonl\n"
	f := &iexec.FakeRunner{Responses: map[string]iexec.FakeResponse{
		"ps -axo pid,tty,command": {Stdout: []byte(ps)},
		"lsof -p 4321 -Fn":        {Stdout: []byte(lsof4321)},
	}}
	r := New(f, "/Users/x/.claude/projects")
	got, err := r.Resolve(context.Background())
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	sess, ok := got["ttys003"]
	if !ok {
		t.Fatalf("no session for ttys003; got %v", got)
	}
	if sess.SessionID != "0b3f-sess" {
		t.Fatalf("SessionID = %q, want 0b3f-sess", sess.SessionID)
	}
	if sess.ProjectDir != "/Users/x/.claude/projects/-Volumes-proj" {
		t.Fatalf("ProjectDir = %q", sess.ProjectDir)
	}
	if _, ok := got["ttys004"]; ok {
		t.Fatal("fish tty should not be mapped")
	}
}

func TestResolveClaudeWithNoOpenJSONLIsSkipped(t *testing.T) {
	ps := " 4321 ttys003    claude\n"
	f := &iexec.FakeRunner{Responses: map[string]iexec.FakeResponse{
		"ps -axo pid,tty,command": {Stdout: []byte(ps)},
		"lsof -p 4321 -Fn":        {Stdout: []byte("p4321\nn/tmp/other.txt\n")},
	}}
	r := New(f, "/Users/x/.claude/projects")
	got, err := r.Resolve(context.Background())
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected no sessions, got %v", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/session/`
Expected: FAIL — `undefined: New`, `undefined: Resolver`.

- [ ] **Step 3: Implement resolver.go**

Create `internal/session/resolver.go`:
```go
// Package session resolves which Claude session a WezTerm pane is running by
// joining the pane's tty to the claude OS process and the .jsonl it holds open.
package session

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	iexec "github.com/ssoriche/reclaude/internal/exec"
)

// ClaudeSession identifies a resumable Claude session.
type ClaudeSession struct {
	SessionID  string
	ProjectDir string
}

// Resolver builds tty -> ClaudeSession maps.
type Resolver struct {
	run          iexec.Runner
	projectsRoot string // e.g. $HOME/.claude/projects
}

// New returns a Resolver. projectsRoot is injected for testability.
func New(r iexec.Runner, projectsRoot string) *Resolver {
	return &Resolver{run: r, projectsRoot: projectsRoot}
}

// Resolve returns a map from tty name (e.g. "ttys003") to ClaudeSession.
func (r *Resolver) Resolve(ctx context.Context) (map[string]ClaudeSession, error) {
	out, err := r.run.Run(ctx, "ps", "-axo", "pid,tty,command")
	if err != nil {
		return nil, fmt.Errorf("session: ps: %w", err)
	}
	result := map[string]ClaudeSession{}
	for _, line := range strings.Split(string(out), "\n") {
		pid, tty, cmd, ok := parsePS(line)
		if !ok || !isClaude(cmd) {
			continue
		}
		sess, ok, err := r.sessionForPID(ctx, pid)
		if err != nil {
			return nil, err
		}
		if ok {
			result[tty] = sess
		}
	}
	return result, nil
}

// parsePS splits a `ps -axo pid,tty,command` line into pid, tty, command.
func parsePS(line string) (pid, tty, cmd string, ok bool) {
	fields := strings.Fields(line)
	if len(fields) < 3 {
		return "", "", "", false
	}
	if _, err := fmt.Sscanf(fields[0], "%d", new(int)); err != nil {
		return "", "", "", false // header or malformed
	}
	pid = fields[0]
	tty = fields[1]
	cmd = strings.Join(fields[2:], " ")
	return pid, tty, cmd, true
}

// isClaude reports whether the command is a claude process (exact binary name,
// not merely containing "claude" in a path/arg).
func isClaude(cmd string) bool {
	fields := strings.Fields(cmd)
	if len(fields) == 0 {
		return false
	}
	return filepath.Base(fields[0]) == "claude"
}

func (r *Resolver) sessionForPID(ctx context.Context, pid string) (ClaudeSession, bool, error) {
	out, err := r.run.Run(ctx, "lsof", "-p", pid, "-Fn")
	if err != nil {
		// A dead/permission-denied pid is not fatal to the whole resolve.
		return ClaudeSession{}, false, nil
	}
	for _, line := range strings.Split(string(out), "\n") {
		if !strings.HasPrefix(line, "n") {
			continue
		}
		path := line[1:]
		if !strings.HasPrefix(path, r.projectsRoot) || !strings.HasSuffix(path, ".jsonl") {
			continue
		}
		return ClaudeSession{
			SessionID:  strings.TrimSuffix(filepath.Base(path), ".jsonl"),
			ProjectDir: filepath.Dir(path),
		}, true, nil
	}
	return ClaudeSession{}, false, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/session/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
gofmt -w .
git add internal/session/
git commit -m "feat: add session.Resolver joining tty to claude session"
```

---

## Chunk 3: snapshot model, conversion, store, hash

### Task 9: snapshot model + JSON round-trip

**Files:**
- Create: `internal/snapshot/model.go`
- Test: `internal/snapshot/model_test.go`

- [ ] **Step 1: Write failing JSON round-trip test**

Create `internal/snapshot/model_test.go`:
```go
package snapshot

import (
	"encoding/json"
	"testing"

	"github.com/ssoriche/reclaude/internal/layout"
)

func TestSnapshotJSONRoundTrip(t *testing.T) {
	s := Snapshot{
		Version:    1,
		CapturedAt: "2026-07-23T14:02:11Z",
		Windows: []Window{{
			Title:          "w",
			Workspace:      "default",
			ActiveTabIndex: 0,
			Tabs: []Tab{{
				ActivePaneIndex: 1,
				Layout: &LayoutNode{
					Split: string(layout.SplitVertical),
					Ratio: 0.5,
					Children: []*LayoutNode{
						{Pane: &Leaf{CWD: "/a"}},
						{Pane: &Leaf{CWD: "/b", Claude: &ClaudeSession{SessionID: "x", Resumable: true}}},
					},
				},
			}},
		}},
	}
	data, err := json.Marshal(s)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got Snapshot
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	leaf := got.Windows[0].Tabs[0].Layout.Children[1].Pane
	if leaf.Claude == nil || leaf.Claude.SessionID != "x" {
		t.Fatalf("claude session lost in round-trip: %+v", leaf)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/snapshot/`
Expected: FAIL — undefined types.

- [ ] **Step 3: Implement model.go**

Create `internal/snapshot/model.go`:
```go
// Package snapshot defines the serialized reclaude state model and its storage.
package snapshot

// Snapshot is the top-level captured state.
type Snapshot struct {
	Version        int      `json:"version"`
	CapturedAt     string   `json:"captured_at"`
	WeztermVersion string   `json:"wezterm_version,omitempty"`
	Windows        []Window `json:"windows"`
}

// Window groups tabs.
type Window struct {
	Title          string `json:"title"`
	Workspace      string `json:"workspace"`
	ActiveTabIndex int    `json:"active_tab_index"`
	Tabs           []Tab  `json:"tabs"`
}

// Tab holds a layout tree.
type Tab struct {
	Title           string      `json:"title"`
	ActivePaneIndex int         `json:"active_pane_index"`
	ZoomedPaneIndex *int        `json:"zoomed_pane_index"`
	Layout          *LayoutNode `json:"layout"`
}

// LayoutNode is a serialized guillotine tree node (split or leaf).
type LayoutNode struct {
	Split    string        `json:"split,omitempty"` // "" for leaf
	Ratio    float64       `json:"ratio,omitempty"`
	Children []*LayoutNode `json:"children,omitempty"`
	Pane     *Leaf         `json:"pane,omitempty"`
}

// Leaf is a restorable pane.
type Leaf struct {
	CWD    string         `json:"cwd"`
	Claude *ClaudeSession `json:"claude"`
	Cols   int            `json:"cols"`
	Rows   int            `json:"rows"`
}

// ClaudeSession is the resumable session on a pane.
type ClaudeSession struct {
	SessionID  string `json:"session_id"`
	ProjectDir string `json:"project_dir"`
	Resumable  bool   `json:"resumable"`
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/snapshot/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
gofmt -w .
git add internal/snapshot/
git commit -m "feat: add snapshot serialized model"
```

---

### Task 10: convert layout.Node + leaf map → snapshot.LayoutNode

**Files:**
- Create: `internal/snapshot/convert.go`
- Test: `internal/snapshot/convert_test.go`

- [ ] **Step 1: Write failing test**

Create `internal/snapshot/convert_test.go`:
```go
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
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/snapshot/`
Expected: FAIL — `undefined: FromLayout`.

- [ ] **Step 3: Implement convert.go**

Create `internal/snapshot/convert.go`:
```go
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
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/snapshot/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
gofmt -w .
git add internal/snapshot/
git commit -m "feat: convert geometric layout to serialized tree with payloads"
```

---

### Task 11: snapshot.Store — paths, atomic write, read, history rotation

**Files:**
- Create: `internal/snapshot/store.go`
- Test: `internal/snapshot/store_test.go`

The store takes a base directory (injected; production default `~/.local/state/reclaude`). `Save` writes `history/<ms-timestamp>.json` then atomically replaces `latest.json` (temp file + `os.Rename`), then prunes `history/` to the newest N. Timestamp string is passed in by the caller (so tests are deterministic and to respect the no-`time.Now`-in-pure-logic separation).

- [ ] **Step 1: Write failing tests using t.TempDir()**

Create `internal/snapshot/store_test.go`:
```go
package snapshot

import (
	"path/filepath"
	"testing"
)

func TestStoreSaveWritesLatestAndHistory(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)
	snap := &Snapshot{Version: 1, CapturedAt: "2026-07-23T14:02:11.123Z"}
	if err := s.Save(snap, "20260723T140211.123", 5); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := s.Latest()
	if err != nil {
		t.Fatalf("Latest: %v", err)
	}
	if got.CapturedAt != snap.CapturedAt {
		t.Fatalf("latest mismatch: %q", got.CapturedAt)
	}
	if _, err := s.Latest(); err != nil {
		t.Fatalf("Latest re-read: %v", err)
	}
	matches, _ := filepath.Glob(filepath.Join(dir, "history", "*.json"))
	if len(matches) != 1 {
		t.Fatalf("history files = %d, want 1", len(matches))
	}
}

func TestStoreRotatesHistoryToN(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)
	stamps := []string{"a", "b", "c", "d", "e"}
	for _, st := range stamps {
		if err := s.Save(&Snapshot{Version: 1}, st, 3); err != nil {
			t.Fatalf("Save %s: %v", st, err)
		}
	}
	matches, _ := filepath.Glob(filepath.Join(dir, "history", "*.json"))
	if len(matches) != 3 {
		t.Fatalf("history files = %d, want 3 (rotated)", len(matches))
	}
}

func TestLatestMissingErrors(t *testing.T) {
	s := NewStore(t.TempDir())
	if _, err := s.Latest(); err == nil {
		t.Fatal("expected error when latest.json missing")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/snapshot/`
Expected: FAIL — `undefined: NewStore`.

- [ ] **Step 3: Implement store.go**

Create `internal/snapshot/store.go`:
```go
package snapshot

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// Store persists snapshots under a base directory.
type Store struct {
	dir string
}

// NewStore returns a Store rooted at dir.
func NewStore(dir string) *Store { return &Store{dir: dir} }

func (s *Store) latestPath() string  { return filepath.Join(s.dir, "latest.json") }
func (s *Store) historyDir() string  { return filepath.Join(s.dir, "history") }

// Save writes the history entry for stamp, atomically updates latest.json, and
// prunes history to the newest keep entries.
func (s *Store) Save(snap *Snapshot, stamp string, keep int) error {
	if err := os.MkdirAll(s.historyDir(), 0o755); err != nil {
		return fmt.Errorf("snapshot: mkdir: %w", err)
	}
	data, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		return fmt.Errorf("snapshot: marshal: %w", err)
	}
	histFile := s.uniqueHistoryPath(stamp)
	if err := os.WriteFile(histFile, data, 0o644); err != nil {
		return fmt.Errorf("snapshot: write history: %w", err)
	}
	if err := atomicWrite(s.latestPath(), data); err != nil {
		return err
	}
	return s.rotate(keep)
}

// uniqueHistoryPath returns "<stamp>.json", or "<stamp>_N.json" on collision.
// The "_" suffix (0x5F) sorts AFTER "." (0x2E), so a collided (later) entry
// still orders after the original under the lexicographic rotate() sort.
func (s *Store) uniqueHistoryPath(stamp string) string {
	base := filepath.Join(s.historyDir(), stamp)
	path := base + ".json"
	for n := 1; ; n++ {
		if _, err := os.Stat(path); os.IsNotExist(err) {
			return path
		}
		path = fmt.Sprintf("%s_%d.json", base, n)
	}
}

func atomicWrite(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("snapshot: write temp: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("snapshot: rename: %w", err)
	}
	return nil
}

func (s *Store) rotate(keep int) error {
	matches, err := filepath.Glob(filepath.Join(s.historyDir(), "*.json"))
	if err != nil {
		return fmt.Errorf("snapshot: glob history: %w", err)
	}
	if len(matches) <= keep {
		return nil
	}
	sort.Strings(matches) // ms-timestamp names sort chronologically
	for _, old := range matches[:len(matches)-keep] {
		if err := os.Remove(old); err != nil {
			return fmt.Errorf("snapshot: prune %s: %w", old, err)
		}
	}
	return nil
}

// Latest reads latest.json.
func (s *Store) Latest() (*Snapshot, error) {
	data, err := os.ReadFile(s.latestPath())
	if err != nil {
		return nil, fmt.Errorf("snapshot: read latest: %w", err)
	}
	var snap Snapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		return nil, fmt.Errorf("snapshot: decode latest: %w", err)
	}
	return &snap, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/snapshot/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
gofmt -w .
git add internal/snapshot/
git commit -m "feat: add snapshot store with atomic write and history rotation"
```

---

### Task 12: snapshot content hash (excludes volatile fields)

**Files:**
- Create: `internal/snapshot/hash.go`
- Test: `internal/snapshot/hash_test.go`

- [ ] **Step 1: Write failing test**

Create `internal/snapshot/hash_test.go`:
```go
package snapshot

import "testing"

func TestContentHashIgnoresVolatileFields(t *testing.T) {
	a := &Snapshot{Version: 1, CapturedAt: "T1", WeztermVersion: "v1", Windows: []Window{{Title: "w"}}}
	b := &Snapshot{Version: 1, CapturedAt: "T2", WeztermVersion: "v2", Windows: []Window{{Title: "w"}}}
	if ContentHash(a) != ContentHash(b) {
		t.Fatal("hash should ignore captured_at and wezterm_version")
	}
}

func TestContentHashChangesWithLayout(t *testing.T) {
	a := &Snapshot{Windows: []Window{{Title: "w"}}}
	b := &Snapshot{Windows: []Window{{Title: "different"}}}
	if ContentHash(a) == ContentHash(b) {
		t.Fatal("hash should change when window content changes")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/snapshot/`
Expected: FAIL — `undefined: ContentHash`.

- [ ] **Step 3: Implement hash.go**

Create `internal/snapshot/hash.go`:
```go
package snapshot

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

// ContentHash hashes only the meaningful layout/session content, excluding the
// volatile CapturedAt and WeztermVersion, so the daemon can dedup unchanged
// snapshots.
func ContentHash(s *Snapshot) string {
	content := struct {
		Version int      `json:"version"`
		Windows []Window `json:"windows"`
	}{Version: s.Version, Windows: s.Windows}
	data, _ := json.Marshal(content)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/snapshot/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
gofmt -w .
git add internal/snapshot/
git commit -m "feat: add snapshot content hash excluding volatile fields"
```

---

## Chunk 4: capture orchestration

### Task 13: cwd normalization helper

**Files:**
- Create: `internal/capture/capture.go` (start the file with the helper)
- Test: `internal/capture/capture_test.go`

WezTerm reports cwd as `file://<host>/<path>`. Strip scheme+host to a plain absolute path.

- [ ] **Step 1: Write failing test**

Create `internal/capture/capture_test.go`:
```go
package capture

import "testing"

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
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/capture/`
Expected: FAIL — `undefined: normalizeCWD`.

- [ ] **Step 3: Implement the helper**

Create `internal/capture/capture.go`:
```go
// Package capture orchestrates a save: list panes, resolve sessions, build
// layout trees, and assemble a snapshot.
package capture

import "strings"

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
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/capture/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
gofmt -w .
git add internal/capture/
git commit -m "feat: add cwd normalization for wezterm file URLs"
```

---

### Task 14: Capture.Run — assemble a snapshot from panes + sessions

**Files:**
- Modify: `internal/capture/capture.go`
- Test: `internal/capture/capture_test.go`

`Capture` depends on two narrow interfaces it defines locally (so tests inject fakes without importing wezterm/session): a pane lister and a session resolver. Production wires the real `wezterm.Client` and `session.Resolver`.

- [ ] **Step 1: Write failing test with fake lister + resolver**

First, **replace the single-line import** at the top of `internal/capture/capture_test.go` (Task 13 created it as `import "testing"`) with this consolidated block (`testing` must stay — both test functions use it):
```go
import (
	"context"
	"testing"

	"github.com/ssoriche/reclaude/internal/session"
	"github.com/ssoriche/reclaude/internal/wezterm"
)
```

Then append the following (no additional `import` statement — all imports live in the block above):
```go
type fakeLister struct{ panes []wezterm.Pane }

func (f fakeLister) List(context.Context) ([]wezterm.Pane, error) { return f.panes, nil }

type fakeResolver struct{ m map[string]session.ClaudeSession }

func (f fakeResolver) Resolve(context.Context) (map[string]session.ClaudeSession, error) {
	return f.m, nil
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
	resolver := fakeResolver{m: map[string]session.ClaudeSession{
		"ttys003": {SessionID: "s1", ProjectDir: "/p/s1"},
	}}
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
	if left.Claude == nil || left.Claude.SessionID != "s1" {
		t.Fatalf("pane 1 should carry session s1: %+v", left)
	}
	if root.Children[1].Pane.Claude != nil {
		t.Fatal("pane 2 should have no claude session")
	}
	if snap.Windows[0].Tabs[0].ActivePaneIndex != 0 {
		t.Fatalf("active pane index = %d, want 0", snap.Windows[0].Tabs[0].ActivePaneIndex)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/capture/`
Expected: FAIL — `undefined: New`, `undefined: Capture`.

- [ ] **Step 3: Implement Capture.Run**

First, **replace the single-line import** at the top of `internal/capture/capture.go` (currently `import "strings"` from Task 13) with this consolidated block:
```go
import (
	"context"
	"strings"

	"github.com/ssoriche/reclaude/internal/layout"
	"github.com/ssoriche/reclaude/internal/session"
	"github.com/ssoriche/reclaude/internal/snapshot"
	"github.com/ssoriche/reclaude/internal/wezterm"
)
```

Then append the following declarations (no additional `import` statement — all imports live in the block above):
```go
// Lister lists WezTerm panes.
type Lister interface {
	List(ctx context.Context) ([]wezterm.Pane, error)
}

// SessionResolver maps tty -> ClaudeSession.
type SessionResolver interface {
	Resolve(ctx context.Context) (map[string]session.ClaudeSession, error)
}

// Capture orchestrates one snapshot.
type Capture struct {
	lister    Lister
	resolver  SessionResolver
	stamp     string
	weztermV  string
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
	sessions, err := c.resolver.Resolve(ctx)
	if err != nil {
		return nil, err
	}
	return c.assemble(panes, sessions), nil
}

func (c *Capture) assemble(panes []wezterm.Pane, sessions map[string]session.ClaudeSession) *snapshot.Snapshot {
	// Group: window -> tab -> panes, preserving first-seen order.
	type tabKey struct{ win, tab int }
	winOrder := []int{}
	winSeen := map[int]bool{}
	tabOrder := map[int][]int{}
	tabSeen := map[tabKey]bool{}
	byTab := map[tabKey][]wezterm.Pane{}
	winTitle := map[int]string{}
	winWorkspace := map[int]string{}

	for _, p := range panes {
		if !winSeen[p.WindowID] {
			winSeen[p.WindowID] = true
			winOrder = append(winOrder, p.WindowID)
			winTitle[p.WindowID] = p.WindowTitle
			winWorkspace[p.WindowID] = p.Workspace
		}
		k := tabKey{p.WindowID, p.TabID}
		if !tabSeen[k] {
			tabSeen[k] = true
			tabOrder[p.WindowID] = append(tabOrder[p.WindowID], p.TabID)
		}
		byTab[k] = append(byTab[k], p)
	}

	snap := &snapshot.Snapshot{
		Version:        1,
		CapturedAt:     c.stamp,
		WeztermVersion: c.weztermV,
	}
	for _, win := range winOrder {
		w := snapshot.Window{Title: winTitle[win], Workspace: winWorkspace[win]}
		for _, tabID := range tabOrder[win] {
			tp := byTab[tabKey{win, tabID}]
			w.Tabs = append(w.Tabs, buildTab(tp, sessions))
		}
		snap.Windows = append(snap.Windows, w)
	}
	return snap
}

func buildTab(panes []wezterm.Pane, sessions map[string]session.ClaudeSession) snapshot.Tab {
	rects := make([]layout.Rect, 0, len(panes))
	leaves := map[int]snapshot.Leaf{}
	for _, p := range panes {
		rects = append(rects, layout.Rect{
			ID: p.PaneID, Left: p.LeftCol, Top: p.TopRow, Cols: p.Size.Cols, Rows: p.Size.Rows,
		})
		leaves[p.PaneID] = leafFor(p, sessions)
	}
	tree, err := layout.BuildTree(rects)
	var node *snapshot.LayoutNode
	if err == nil {
		node = snapshot.FromLayout(tree, leaves)
	} else if len(rects) > 0 {
		// Degenerate fallback: treat the first pane as a lone leaf.
		leaf := leaves[rects[0].ID]
		node = &snapshot.LayoutNode{Pane: &leaf}
	}

	tab := snapshot.Tab{Layout: node}
	// Active/zoomed indices in canonical leaf order.
	if err == nil {
		order := layout.Leaves(tree)
		idx := map[int]int{}
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
	}
	return tab
}

func leafFor(p wezterm.Pane, sessions map[string]session.ClaudeSession) snapshot.Leaf {
	leaf := snapshot.Leaf{CWD: normalizeCWD(p.CWD), Cols: p.Size.Cols, Rows: p.Size.Rows}
	tty := strings.TrimPrefix(p.TTYName, "/dev/")
	if s, ok := sessions[tty]; ok {
		leaf.Claude = &snapshot.ClaudeSession{
			SessionID:  s.SessionID,
			ProjectDir: s.ProjectDir,
			Resumable:  true,
		}
	}
	return leaf
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/capture/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
gofmt -w .
git add internal/capture/
git commit -m "feat: assemble snapshot from panes and resolved sessions"
```

---

## Chunk 5: restore orchestration

### Task 15: resumability re-check + send-text command selection

**Files:**
- Create: `internal/restore/restore.go` (start with pure helpers)
- Test: `internal/restore/restore_test.go`

Pure helper `launchCommand(leaf, resumable) string` decides what to send: resumable Claude → `claude --resume <id>\n`; non-resumable Claude → `claude\n`; non-Claude → `""` (send nothing). Resumability is re-checked by testing for the `.jsonl` file; inject an `exists func(string) bool`.

- [ ] **Step 1: Write failing test**

Create `internal/restore/restore_test.go`:
```go
package restore

import (
	"testing"

	"github.com/ssoriche/reclaude/internal/snapshot"
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
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/restore/`
Expected: FAIL — undefined functions.

- [ ] **Step 3: Implement the helpers**

Create `internal/restore/restore.go`:
```go
// Package restore rebuilds a WezTerm layout from a snapshot.
package restore

import (
	"fmt"
	"path/filepath"

	"github.com/ssoriche/reclaude/internal/snapshot"
)

// launchCommand returns the text to send into a restored pane (with trailing
// newline), or "" to leave a bare shell.
func launchCommand(leaf *snapshot.Leaf, resumable bool) string {
	if leaf == nil || leaf.Claude == nil {
		return ""
	}
	if resumable {
		return fmt.Sprintf("claude --resume %s\n", leaf.Claude.SessionID)
	}
	return "claude\n"
}

// isResumable re-checks that the session's .jsonl still exists.
func isResumable(leaf *snapshot.Leaf, exists func(string) bool) bool {
	if leaf == nil || leaf.Claude == nil {
		return false
	}
	path := filepath.Join(leaf.Claude.ProjectDir, leaf.Claude.SessionID+".jsonl")
	return exists(path)
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/restore/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
gofmt -w .
git add internal/restore/
git commit -m "feat: add restore launch-command and resumability helpers"
```

---

### Task 16: Restore.Run — spawn windows, recurse splits, send text

**Files:**
- Modify: `internal/restore/restore.go`
- Test: `internal/restore/restore_test.go`

`Restore` depends on a local `Terminal` interface (spawn/split/send/finishers) so tests inject a fake recording the call sequence. Production wires `wezterm.Client`. Recursion: for a split node, the parent pane id is `children[0]`; split it toward `--right`/`--bottom` at `round((1-ratio)*100)` with the cwd of the first leaf of the `children[1]` subtree; recurse left reusing parent id, recurse right with the new id.

- [ ] **Step 1: Write failing test asserting the call sequence**

First, **replace the import block** at the top of `internal/restore/restore_test.go` (Task 15 created it with `testing` + `snapshot`) with this consolidated block:
```go
import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/ssoriche/reclaude/internal/snapshot"
	"github.com/ssoriche/reclaude/internal/wezterm"
)
```

Then append the fake and test. `fakeTerm` implements the **complete** `Terminal` interface (all nine methods, including `SpawnTab` and `WindowIDForPane`) so it does not need to change in Task 17 — this is what prevents interface drift. It records every call for assertions:
```go
type fakeTerm struct {
	calls  []string
	nextID int
	winID  int
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
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/restore/`
Expected: FAIL — `undefined: New`, `undefined: Terminal`.

- [ ] **Step 3: Implement Restore.Run**

First, **replace the import block** at the top of `internal/restore/restore.go` (Task 15 created it with `fmt`, `path/filepath`, `snapshot`) with this consolidated block — this is the file's *only* import statement:
```go
import (
	"context"
	"errors"
	"fmt"
	"math"
	"path/filepath"

	"github.com/ssoriche/reclaude/internal/snapshot"
	"github.com/ssoriche/reclaude/internal/wezterm"
)
```

Then append the following. The `Terminal` interface is declared here in its **final, complete form** (nine methods) so Task 17 adds no methods and nothing drifts. `buildTab` returns `([]int, error)` — the leaf pane ids in canonical order — which `applyFinishers` indexes. Task 16 wires only the first tab of each window; Task 17 adds the `tabs[1:]` loop:
```go
// Terminal is the subset of wezterm.Client that restore needs. It is complete
// as of this task; later tasks add no methods.
type Terminal interface {
	SpawnWindow(ctx context.Context, cwd string) (int, error)
	SpawnTab(ctx context.Context, windowID int, cwd string) (int, error)
	WindowIDForPane(ctx context.Context, paneID int) (int, error)
	SplitPane(ctx context.Context, paneID int, dir wezterm.Direction, percent int, cwd string) (int, error)
	SendText(ctx context.Context, paneID int, text string) error
	ActivatePane(ctx context.Context, paneID int) error
	SetTabTitle(ctx context.Context, paneID int, title string) error
	SetWindowTitle(ctx context.Context, paneID int, title string) error
	ZoomPane(ctx context.Context, paneID int) error
}

// Restore rebuilds layouts from a snapshot.
type Restore struct {
	term   Terminal
	exists func(string) bool
}

// New returns a Restore. exists re-checks session .jsonl presence.
func New(t Terminal, exists func(string) bool) *Restore {
	return &Restore{term: t, exists: exists}
}

// Run rebuilds every window in the snapshot. Failures in one subtree are
// collected (errors.Join) but do not abort the whole restore. Task 16 restores
// each window's first tab; Task 17 extends this to all tabs.
func (r *Restore) Run(ctx context.Context, snap *snapshot.Snapshot) error {
	var errs []error
	for _, w := range snap.Windows {
		if len(w.Tabs) == 0 || w.Tabs[0].Layout == nil {
			continue
		}
		rootPane, err := r.term.SpawnWindow(ctx, firstLeaf(w.Tabs[0].Layout).CWD)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		paneIDs, err := r.buildTab(ctx, rootPane, w.Tabs[0].Layout)
		if err != nil {
			errs = append(errs, err)
		}
		r.applyFinishers(ctx, w.Tabs[0], paneIDs)
		if w.Title != "" {
			_ = r.term.SetWindowTitle(ctx, rootPane, w.Title)
		}
		// tabs[1:] handled in Task 17.
	}
	return errors.Join(errs...)
}

// buildTab recursively creates splits for a tab whose root pane already exists
// as parentPane, sends launch commands to every leaf, and returns the leaf
// pane ids in canonical order (Children[0] before Children[1]).
func (r *Restore) buildTab(ctx context.Context, parentPane int, node *snapshot.LayoutNode) ([]int, error) {
	if node.Pane != nil { // leaf
		if err := r.launch(ctx, parentPane, node.Pane); err != nil {
			return nil, err
		}
		return []int{parentPane}, nil
	}
	dir := wezterm.DirRight
	if node.Split == "horizontal" {
		dir = wezterm.DirBottom
	}
	percent := int(math.Round((1 - node.Ratio) * 100))
	newPane, err := r.term.SplitPane(ctx, parentPane, dir, percent, firstLeaf(node.Children[1]).CWD)
	if err != nil {
		return nil, err
	}
	left, err := r.buildTab(ctx, parentPane, node.Children[0])
	if err != nil {
		return nil, err
	}
	right, err := r.buildTab(ctx, newPane, node.Children[1])
	if err != nil {
		return nil, err
	}
	return append(left, right...), nil
}

func (r *Restore) launch(ctx context.Context, pane int, leaf *snapshot.Leaf) error {
	cmd := launchCommand(leaf, isResumable(leaf, r.exists))
	if cmd == "" {
		return nil
	}
	return r.term.SendText(ctx, pane, cmd)
}

// applyFinishers restores zoom, focus, and tab title using the canonical
// leaf-ordered pane ids returned by buildTab.
func (r *Restore) applyFinishers(ctx context.Context, tab snapshot.Tab, paneIDs []int) {
	if tab.ZoomedPaneIndex != nil && *tab.ZoomedPaneIndex >= 0 && *tab.ZoomedPaneIndex < len(paneIDs) {
		_ = r.term.ZoomPane(ctx, paneIDs[*tab.ZoomedPaneIndex])
	}
	if tab.ActivePaneIndex >= 0 && tab.ActivePaneIndex < len(paneIDs) {
		_ = r.term.ActivatePane(ctx, paneIDs[tab.ActivePaneIndex])
	}
	if tab.Title != "" && len(paneIDs) > 0 {
		_ = r.term.SetTabTitle(ctx, paneIDs[0], tab.Title)
	}
}

// firstLeaf returns the first leaf reached by descending Children[0].
func firstLeaf(node *snapshot.LayoutNode) *snapshot.Leaf {
	for node.Pane == nil {
		node = node.Children[0]
	}
	return node.Pane
}
```

Note: the alpha does not set a restore workspace name — the spec's collision-guard `reclaude-<timestamp>` workspace is deferred (see the Deferred section). Restored windows land in the default workspace, which is acceptable for the alpha.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/restore/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
gofmt -w .
git add internal/restore/
git commit -m "feat: restore single-tab layouts with split recursion and resume"
```

---

### Task 17: multi-tab restore (tabs[1:] loop)

**Files:**
- Modify: `internal/restore/restore.go` (only `Run` — the `Terminal` interface and `buildTab`/`applyFinishers` are already final from Task 16)
- Test: `internal/restore/restore_test.go`

The `Terminal` interface already includes `SpawnTab` and `WindowIDForPane`, and `fakeTerm` already implements them (Task 16), so this task changes no signatures — it only teaches `Run` to build the remaining tabs.

- [ ] **Step 1: Write failing test for a 2-tab window**

Append to `internal/restore/restore_test.go` (reuses `fakeTerm` and `containsCall` from Task 16):
```go
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
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/restore/ -run TestRestoreSecondTabUsesSpawnTab`
Expected: FAIL — Task 16's `Run` only builds `tabs[0]`, so no `spawn-tab` call and the second tab's `zoom` finisher never runs.

- [ ] **Step 3: Add the tabs[1:] loop to Run**

In `internal/restore/restore.go`, replace the `// tabs[1:] handled in Task 17.` comment inside `Run` with the loop below (no other changes; no new imports):
```go
		if len(w.Tabs) > 1 {
			winID, wErr := r.term.WindowIDForPane(ctx, rootPane)
			if wErr != nil {
				errs = append(errs, wErr)
			} else {
				for _, tab := range w.Tabs[1:] {
					if tab.Layout == nil {
						continue
					}
					tabPane, sErr := r.term.SpawnTab(ctx, winID, firstLeaf(tab.Layout).CWD)
					if sErr != nil {
						errs = append(errs, sErr)
						continue
					}
					ids, bErr := r.buildTab(ctx, tabPane, tab.Layout)
					if bErr != nil {
						errs = append(errs, bErr)
					}
					r.applyFinishers(ctx, tab, ids)
				}
			}
		}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/restore/` then `go test ./...`
Expected: PASS across all packages.

- [ ] **Step 5: Commit**

```bash
gofmt -w .
git add internal/restore/
git commit -m "feat: restore additional tabs via SpawnTab"
```

---

## Chunk 6: daemon, launchd, CLI wiring

### Task 18: daemon change-detection loop

**Files:**
- Create: `internal/daemon/daemon.go`
- Test: `internal/daemon/daemon_test.go`

The loop is testable by factoring the per-tick decision into a pure method `shouldWrite(newHash, lastHash string) bool` plus a `Tick` that captures, hashes, and writes only on change. Injected: a capture function, a store, and a clock/stamp function. The infinite loop itself (`Run`) is a thin `for { Tick(); sleep }` and is not unit-tested.

- [ ] **Step 1: Write failing test for Tick dedup**

Create `internal/daemon/daemon_test.go`:
```go
package daemon

import (
	"context"
	"testing"

	"github.com/ssoriche/reclaude/internal/snapshot"
)

type recordingStore struct{ saves int }

func (s *recordingStore) Save(_ *snapshot.Snapshot, _ string, _ int) error {
	s.saves++
	return nil
}

func TestTickWritesOnlyOnChange(t *testing.T) {
	snapA := &snapshot.Snapshot{Version: 1, Windows: []snapshot.Window{{Title: "a"}}}
	captures := []*snapshot.Snapshot{snapA, snapA, {Version: 1, Windows: []snapshot.Window{{Title: "b"}}}}
	i := 0
	capture := func(context.Context) (*snapshot.Snapshot, error) {
		s := captures[i]
		i++
		return s, nil
	}
	store := &recordingStore{}
	d := New(capture, store, func() string { return "stamp" }, 5)

	_ = d.Tick(context.Background()) // a -> write
	_ = d.Tick(context.Background()) // a -> skip (unchanged)
	_ = d.Tick(context.Background()) // b -> write
	if store.saves != 2 {
		t.Fatalf("saves = %d, want 2", store.saves)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/daemon/`
Expected: FAIL — `undefined: New`.

- [ ] **Step 3: Implement daemon.go**

Create `internal/daemon/daemon.go`:
```go
// Package daemon runs periodic captures with change-detection.
package daemon

import (
	"context"
	"time"

	"github.com/ssoriche/reclaude/internal/snapshot"
)

// CaptureFunc produces a snapshot.
type CaptureFunc func(ctx context.Context) (*snapshot.Snapshot, error)

// Store is the subset of snapshot.Store the daemon uses.
type Store interface {
	Save(snap *snapshot.Snapshot, stamp string, keep int) error
}

// Daemon captures on a schedule, writing only when content changes.
type Daemon struct {
	capture  CaptureFunc
	store    Store
	stampFn  func() string
	keep     int
	lastHash string
}

// New returns a Daemon.
func New(c CaptureFunc, s Store, stampFn func() string, keep int) *Daemon {
	return &Daemon{capture: c, store: s, stampFn: stampFn, keep: keep}
}

// Tick captures once and writes only if the content hash changed.
func (d *Daemon) Tick(ctx context.Context) error {
	snap, err := d.capture(ctx)
	if err != nil {
		return err
	}
	h := snapshot.ContentHash(snap)
	if h == d.lastHash {
		return nil
	}
	if err := d.store.Save(snap, d.stampFn(), d.keep); err != nil {
		return err
	}
	d.lastHash = h
	return nil
}

// Run ticks every interval until ctx is cancelled. Errors are passed to logf
// and do not stop the loop.
func (d *Daemon) Run(ctx context.Context, interval time.Duration, logf func(error)) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		if err := d.Tick(ctx); err != nil && logf != nil {
			logf(err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/daemon/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
gofmt -w .
git add internal/daemon/
git commit -m "feat: add daemon tick loop with content-hash change detection"
```

---

### Task 19: launchd plist install/uninstall

**Files:**
- Create: `internal/daemon/launchd.go`
- Test: `internal/daemon/launchd_test.go`

Split the pure plist-rendering from the side-effecting install. `renderPlist(binPath, logPath string) string` is unit-tested for correct label, program args, RunAtLoad, KeepAlive. `Install`/`Uninstall` (writing the file + `launchctl bootstrap/bootout`) take a `Runner` and a target plist path; test `Install` with a `FakeRunner` and a `t.TempDir()` plist path, asserting the file is written and `launchctl bootstrap gui/<uid>` is invoked.

- [ ] **Step 1: Write failing tests**

Create `internal/daemon/launchd_test.go`:
```go
package daemon

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	iexec "github.com/ssoriche/reclaude/internal/exec"
)

func TestRenderPlistContainsProgramArgs(t *testing.T) {
	out := renderPlist("/usr/local/bin/reclaude", "/log/daemon.log")
	for _, want := range []string{"net.reclaude.daemon", "/usr/local/bin/reclaude", "<string>daemon</string>", "<string>run</string>", "RunAtLoad", "KeepAlive", "/log/daemon.log"} {
		if !strings.Contains(out, want) {
			t.Fatalf("plist missing %q:\n%s", want, out)
		}
	}
}

func TestInstallWritesPlistAndBootstraps(t *testing.T) {
	dir := t.TempDir()
	plist := filepath.Join(dir, "net.reclaude.daemon.plist")
	f := &iexec.FakeRunner{Responses: map[string]iexec.FakeResponse{
		"launchctl bootstrap gui/501 " + plist: {},
	}}
	err := Install(context.Background(), f, plist, "/bin/reclaude", "/log/daemon.log", "501")
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	if _, err := os.Stat(plist); err != nil {
		t.Fatalf("plist not written: %v", err)
	}
	found := false
	for _, c := range f.Calls {
		if strings.HasPrefix(c, "launchctl bootstrap gui/501") {
			found = true
		}
	}
	if !found {
		t.Fatalf("bootstrap not called: %v", f.Calls)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/daemon/`
Expected: FAIL — `undefined: renderPlist`, `undefined: Install`.

- [ ] **Step 3: Implement launchd.go**

Create `internal/daemon/launchd.go`:
```go
package daemon

import (
	"context"
	"fmt"
	"os"

	iexec "github.com/ssoriche/reclaude/internal/exec"
)

// Label is the LaunchAgent label.
const Label = "net.reclaude.daemon"

func renderPlist(binPath, logPath string) string {
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>%s</string>
  <key>ProgramArguments</key>
  <array>
    <string>%s</string>
    <string>daemon</string>
    <string>run</string>
  </array>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
  <key>StandardOutPath</key><string>%s</string>
  <key>StandardErrorPath</key><string>%s</string>
</dict>
</plist>
`, Label, binPath, logPath, logPath)
}

// Install writes the plist and bootstraps it into the user's gui domain.
func Install(ctx context.Context, run iexec.Runner, plistPath, binPath, logPath, uid string) error {
	if err := os.WriteFile(plistPath, []byte(renderPlist(binPath, logPath)), 0o644); err != nil {
		return fmt.Errorf("daemon: write plist: %w", err)
	}
	if _, err := run.Run(ctx, "launchctl", "bootstrap", "gui/"+uid, plistPath); err != nil {
		return fmt.Errorf("daemon: bootstrap: %w", err)
	}
	return nil
}

// Uninstall boots out the agent and removes the plist.
func Uninstall(ctx context.Context, run iexec.Runner, plistPath, uid string) error {
	if _, err := run.Run(ctx, "launchctl", "bootout", "gui/"+uid+"/"+Label); err != nil {
		return fmt.Errorf("daemon: bootout: %w", err)
	}
	if err := os.Remove(plistPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("daemon: remove plist: %w", err)
	}
	return nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/daemon/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
gofmt -w .
git add internal/daemon/
git commit -m "feat: add launchd plist install/uninstall"
```

---

### Task 20: paths + wiring helpers (production dependency assembly)

**Files:**
- Create: `internal/app/app.go` (composition root: builds real capture/restore/store/paths)
- Test: `internal/app/app_test.go`

Keep `main.go` tiny; put dependency assembly here. Provide: `StateDir()` (`$HOME/.local/state/reclaude`), `ProjectsRoot()` (`$HOME/.claude/projects`), `Stamp(time.Time)` (RFC3339 with ms → also the history filename form), `NewCapture()`, `NewRestore()`, `WeztermVersion(ctx)` (parse `wezterm --version`). Only pure helpers (`Stamp`, path builders given an injected home) get unit tests.

- [ ] **Step 1: Write failing test for Stamp + paths**

Create `internal/app/app_test.go`:
```go
package app

import (
	"testing"
	"time"
)

func TestStampFormat(t *testing.T) {
	ts := time.Date(2026, 7, 23, 14, 2, 11, 123000000, time.UTC)
	// Stamp is the human/RFC3339 value stored in CapturedAt.
	if got := Stamp(ts); got != "2026-07-23T14:02:11.123Z" {
		t.Fatalf("Stamp = %q", got)
	}
	// FileStamp is the sortable, filesystem-safe history filename base.
	if got := FileStamp(ts); got != "20260723T140211.123Z" {
		t.Fatalf("FileStamp = %q", got)
	}
}

func TestStateDirUsesHome(t *testing.T) {
	if got := stateDir("/home/x"); got != "/home/x/.local/state/reclaude" {
		t.Fatalf("stateDir = %q", got)
	}
}

func TestProjectsRootUsesHome(t *testing.T) {
	if got := projectsRoot("/home/x"); got != "/home/x/.claude/projects" {
		t.Fatalf("projectsRoot = %q", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/app/`
Expected: FAIL — undefined.

- [ ] **Step 3: Implement app.go**

Create `internal/app/app.go` with `Stamp`, `FileStamp`, `stateDir`, `projectsRoot` (pure, home injected), plus exported `StateDir()/ProjectsRoot()` reading `os.UserHomeDir()`, and constructors `NewCapture(ctx)`, `NewRestore()`, `WeztermVersion(ctx)` wiring `exec.OSRunner{}`, `wezterm.New`, `session.New`, `capture.New`, `restore.New`, `snapshot.NewStore`:
```go
// Stamp is the RFC3339-with-ms value stored in Snapshot.CapturedAt.
func Stamp(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000Z") }

// FileStamp is the sortable, filesystem-safe base for a history filename.
func FileStamp(t time.Time) string { return t.UTC().Format("20060102T150405.000Z") }

func stateDir(home string) string     { return filepath.Join(home, ".local", "state", "reclaude") }
func projectsRoot(home string) string { return filepath.Join(home, ".claude", "projects") }
```
`Stamp(t)` feeds `capture.New(..., stamp, ...)` (→ `CapturedAt`); `FileStamp(t)` feeds `store.Save(snap, fileStamp, keep)` (→ history filename). Both are called with the same `time.Now()` in `cmdSave`. (Constructors are exercised via the CLI integration in Task 21, not unit-tested, since they touch the real environment.)

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/app/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
gofmt -w .
git add internal/app/
git commit -m "feat: add app composition root and path/stamp helpers"
```

---

### Task 21: CLI command dispatch

**Files:**
- Modify: `cmd/reclaude/main.go`
- Test: `cmd/reclaude/main_test.go`

Dispatch is factored into `run(ctx, args, stdout, stderr) int` for testability; `main` calls it. Commands: `save`, `restore`, `list`, `status`, `daemon (install|uninstall|run)`. `save` builds a capture, runs it, writes via the store; if `wezterm cli list` fails because WezTerm isn't running, print a friendly message and return 0. `restore` reads latest and runs Restore. `list` prints history entries. `status` runs a capture and prints a summary without writing.

- [ ] **Step 1: Write failing test for arg dispatch/exit codes**

Create `cmd/reclaude/main_test.go`:
```go
package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestRunNoArgsUsage(t *testing.T) {
	var out, errb bytes.Buffer
	code := run(context.Background(), nil, &out, &errb)
	if code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
	if !strings.Contains(errb.String(), "usage") {
		t.Fatalf("expected usage text, got %q", errb.String())
	}
}

func TestRunUnknownCommand(t *testing.T) {
	var out, errb bytes.Buffer
	code := run(context.Background(), []string{"bogus"}, &out, &errb)
	if code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/reclaude/`
Expected: FAIL — `undefined: run`.

- [ ] **Step 3: Implement dispatch in main.go**

Rewrite `cmd/reclaude/main.go` so `main` delegates to `run(ctx, os.Args[1:], os.Stdout, os.Stderr)` and exits with its code. Implement `run` with a `switch args[0]` over the commands, each calling the `internal/app` constructors and the relevant package.

WezTerm-not-running detection is concrete: `wezterm.List` (Task 6) already returns the `wezterm.ErrNotRunning` sentinel whenever the command fails to execute (mux down or binary missing). `capture.Run` propagates it. So `cmdSave` does:
```go
func cmdSave(ctx context.Context, stdout, stderr io.Writer) int {
	cap, store, err := app.NewCapture(ctx)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	snap, err := cap.Run(ctx)
	if errors.Is(err, wezterm.ErrNotRunning) {
		fmt.Fprintln(stdout, "WezTerm is not running; nothing to capture.")
		return 0
	}
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	now := time.Now()
	if err := store.Save(snap, app.FileStamp(now), 20); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	fmt.Fprintf(stdout, "saved %d window(s)\n", len(snap.Windows))
	return 0
}
```
(Note `app.NewCapture` must stamp `CapturedAt` with `app.Stamp(now)` internally, or return the stamp so `cmdSave` passes it; wire whichever `app.NewCapture` signature Task 20 defined — the two stamps come from the same `now`.)

Provide the complete `run` function and the other helpers in the implementation (no placeholders): `cmdRestore` (reads `store.Latest()`, builds `app.NewRestore()`, calls `Run`; prints a friendly message and returns 1 if `latest.json` is missing), `cmdList` (globs the history dir, prints each file with its window/session counts), `cmdStatus` (runs a capture and prints a summary WITHOUT calling `store.Save`; same `ErrNotRunning` handling as `cmdSave`), and `cmdDaemon` (sub-switch over `install`/`uninstall`/`run` wiring `daemon.Install`/`daemon.Uninstall`/`daemon.New(...).Run(...)` with `app.StateDir()` paths and the current uid from `os.Getuid()`). Each returns an `int` exit code and writes user-facing text to the provided writers. No changes to the `wezterm` package are needed in this task.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./cmd/reclaude/` then `go test ./...`
Expected: PASS across all packages.

- [ ] **Step 5: Verify the binary builds and prints usage**

Run:
```bash
go build -o /tmp/reclaude ./cmd/reclaude && /tmp/reclaude
```
Expected: usage line on stderr, exit code 2.

- [ ] **Step 6: Commit**

```bash
gofmt -w .
git add cmd/reclaude/
git commit -m "feat: wire CLI command dispatch"
```

---

### Task 22: README + manual end-to-end verification

**Files:**
- Create: `README.md`

- [ ] **Step 1: Write the README**

Document: what reclaude does, install (`go build -o ~/.local/bin/reclaude ./cmd/reclaude`), the commands, where snapshots live, how to install the daemon, and the **manual E2E test** procedure below. No code test for this task.

- [ ] **Step 2: Perform the manual end-to-end test** (documented, run by the engineer)

Follow @superpowers:verification-before-completion. Procedure:
1. Open a WezTerm window; split it (e.g. one vertical + one horizontal split → 3 panes); start `claude` in two of the panes and leave the third as a shell.
2. Open a second WezTerm window with a fresh tab and one `claude`.
3. Run `reclaude save`. Confirm `~/.local/state/reclaude/latest.json` exists and its tree/leaf structure matches (eyeball window/tab/split counts and cwds; the two claude panes have non-null `claude` with a `session_id`).
4. Close all WezTerm windows.
5. Run `reclaude restore`.
6. Verify: windows/tabs/splits are recreated with the right cwds; the claude panes come back with their conversations resumed (`claude --resume` ran); the shell pane is a bare shell.
7. Record the observed result in the PR description (pass/fail with notes).

- [ ] **Step 3: Commit**

```bash
git add README.md
git commit -m "docs: add README and manual end-to-end verification procedure"
```

---

## Definition of Done

- [ ] `go build ./...` clean; `go vet ./...` clean.
- [ ] `go test ./...` all green.
- [ ] `gofmt -l .` prints nothing (all formatted).
- [ ] Manual E2E (Task 22) performed and its result recorded.
- [ ] No package other than `internal/exec` imports `os/exec`; `layout` imports no other internal package.

## Deferred (not in this plan; from spec)

- Ghostty backend (would slot behind the `wezterm` package boundary).
- `--force`/merge-into-existing-layout restore.
- `reclaude restore <timestamp>` for a specific history entry.
- Faithful multi-workspace restore (alpha consolidates into one fresh workspace — implemented as: `Run` sets a single workspace name; if not yet supported by the CLI wiring, windows land in the default workspace, which is acceptable for alpha).
- Configurable interval/history depth via config file.
