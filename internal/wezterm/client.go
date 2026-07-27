// Package wezterm wraps the `wezterm cli` subcommands behind a Client.
package wezterm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

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
		if ctx.Err() != nil {
			return nil, fmt.Errorf("wezterm cli list: %w", ctx.Err())
		}
		return nil, fmt.Errorf("%w: %w", ErrNotRunning, err)
	}
	var panes []Pane
	if err := json.Unmarshal(out, &panes); err != nil {
		return nil, fmt.Errorf("wezterm cli list: decode: %w", err)
	}
	return panes, nil
}

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

// ActivatePane brings paneID's tab and window to the foreground and focuses it.
func (c *Client) ActivatePane(ctx context.Context, paneID int) error {
	_, err := c.run.Run(ctx, "wezterm", "cli", "activate-pane", "--pane-id", strconv.Itoa(paneID))
	return wrap("activate-pane", err)
}

// ActivateTab activates the tab containing paneID.
func (c *Client) ActivateTab(ctx context.Context, paneID int) error {
	_, err := c.run.Run(ctx, "wezterm", "cli", "activate-tab", "--pane-id", strconv.Itoa(paneID))
	return wrap("activate-tab", err)
}

// SetTabTitle sets the title of the tab containing paneID.
func (c *Client) SetTabTitle(ctx context.Context, paneID int, title string) error {
	_, err := c.run.Run(ctx, "wezterm", "cli", "set-tab-title", "--pane-id", strconv.Itoa(paneID), title)
	return wrap("set-tab-title", err)
}

// SetWindowTitle sets the title of the window containing paneID.
func (c *Client) SetWindowTitle(ctx context.Context, paneID int, title string) error {
	_, err := c.run.Run(ctx, "wezterm", "cli", "set-window-title", "--pane-id", strconv.Itoa(paneID), title)
	return wrap("set-window-title", err)
}

// ZoomPane toggles paneID to occupy its full tab.
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
