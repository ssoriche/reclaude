package wezterm

import (
	"context"
	"errors"
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
	if p.PaneID == 0 || p.TTYName == "" {
		t.Fatalf("pane not decoded: %+v", p)
	}
}

func TestClientListNotRunning(t *testing.T) {
	f := &iexec.FakeRunner{Responses: map[string]iexec.FakeResponse{
		"wezterm cli list --format json": {Err: errors.New("exit status 1")},
	}}
	if _, err := New(f).List(context.Background()); !errors.Is(err, ErrNotRunning) {
		t.Fatalf("want ErrNotRunning, got %v", err)
	}
}

func TestClientListContextCanceled(t *testing.T) {
	f := &iexec.FakeRunner{Responses: map[string]iexec.FakeResponse{
		"wezterm cli list --format json": {Err: errors.New("exit status 1")},
	}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := New(f).List(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled, got %v", err)
	}
	if errors.Is(err, ErrNotRunning) {
		t.Fatalf("context cancellation should not be reported as ErrNotRunning, got %v", err)
	}
}

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

func TestSplitPaneBottomBuildsCommand(t *testing.T) {
	f := &iexec.FakeRunner{Responses: map[string]iexec.FakeResponse{
		"wezterm cli split-pane --pane-id 3 --bottom --percent 30 --cwd /w": {Stdout: []byte("9\n")},
	}}
	c := New(f)
	id, err := c.SplitPane(context.Background(), 3, DirBottom, 30, "/w")
	if err != nil {
		t.Fatalf("SplitPane: %v", err)
	}
	if id != 9 {
		t.Fatalf("pane id = %d, want 9", id)
	}
}

func TestSpawnTabBuildsCommand(t *testing.T) {
	f := &iexec.FakeRunner{Responses: map[string]iexec.FakeResponse{
		"wezterm cli spawn --window-id 7 --cwd /home/y": {Stdout: []byte("13\n")},
	}}
	c := New(f)
	id, err := c.SpawnTab(context.Background(), 7, "/home/y")
	if err != nil {
		t.Fatalf("SpawnTab: %v", err)
	}
	if id != 13 {
		t.Fatalf("pane id = %d, want 13", id)
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

func TestSetTabTitleBuildsCommand(t *testing.T) {
	f := &iexec.FakeRunner{Responses: map[string]iexec.FakeResponse{
		"wezterm cli set-tab-title --pane-id 5 my title": {},
	}}
	c := New(f)
	if err := c.SetTabTitle(context.Background(), 5, "my title"); err != nil {
		t.Fatalf("SetTabTitle: %v", err)
	}
}

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

func TestWindowIDForPaneNotFound(t *testing.T) {
	data, _ := os.ReadFile("testdata/list.json")
	f := &iexec.FakeRunner{Responses: map[string]iexec.FakeResponse{
		"wezterm cli list --format json": {Stdout: data},
	}}
	c := New(f)
	if _, err := c.WindowIDForPane(context.Background(), -999999); err == nil {
		t.Fatal("expected error for unknown pane id, got nil")
	}
}

func TestActivatePaneBuildsCommand(t *testing.T) {
	f := &iexec.FakeRunner{Responses: map[string]iexec.FakeResponse{
		"wezterm cli activate-pane --pane-id 5": {},
	}}
	c := New(f)
	if err := c.ActivatePane(context.Background(), 5); err != nil {
		t.Fatalf("ActivatePane: %v", err)
	}
}

func TestActivateTabBuildsCommand(t *testing.T) {
	f := &iexec.FakeRunner{Responses: map[string]iexec.FakeResponse{
		"wezterm cli activate-tab --pane-id 5": {},
	}}
	c := New(f)
	if err := c.ActivateTab(context.Background(), 5); err != nil {
		t.Fatalf("ActivateTab: %v", err)
	}
}

func TestSetWindowTitleBuildsCommand(t *testing.T) {
	f := &iexec.FakeRunner{Responses: map[string]iexec.FakeResponse{
		"wezterm cli set-window-title --pane-id 5 my window title": {},
	}}
	c := New(f)
	if err := c.SetWindowTitle(context.Background(), 5, "my window title"); err != nil {
		t.Fatalf("SetWindowTitle: %v", err)
	}
}

func TestZoomPaneBuildsCommand(t *testing.T) {
	f := &iexec.FakeRunner{Responses: map[string]iexec.FakeResponse{
		"wezterm cli zoom-pane --pane-id 5 --zoom": {},
	}}
	c := New(f)
	if err := c.ZoomPane(context.Background(), 5); err != nil {
		t.Fatalf("ZoomPane: %v", err)
	}
}
