// Package app is reclaude's composition root: it wires the real exec.Runner,
// wezterm.Client, session.Resolver, capture.Capture, restore.Restore, and
// snapshot.Store together, deriving paths from the user's home directory.
package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ssoriche/reclaude/internal/capture"
	iexec "github.com/ssoriche/reclaude/internal/exec"
	"github.com/ssoriche/reclaude/internal/restore"
	"github.com/ssoriche/reclaude/internal/session"
	"github.com/ssoriche/reclaude/internal/snapshot"
	"github.com/ssoriche/reclaude/internal/wezterm"
)

// Stamp is the RFC3339-with-ms value stored in Snapshot.CapturedAt.
func Stamp(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000Z") }

// FileStamp is the sortable, filesystem-safe base for a history filename.
func FileStamp(t time.Time) string { return t.UTC().Format("20060102T150405.000Z") }

func stateDir(home string) string     { return filepath.Join(home, ".local", "state", "reclaude") }
func projectsRoot(home string) string { return filepath.Join(home, ".claude", "projects") }

// StateDir returns stateDir rooted at the real user home.
func StateDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("app: home dir: %w", err)
	}
	return stateDir(home), nil
}

// ProjectsRoot returns projectsRoot rooted at the real user home.
func ProjectsRoot() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("app: home dir: %w", err)
	}
	return projectsRoot(home), nil
}

// WeztermVersion runs `wezterm --version` and returns its trimmed output. A
// failure here (missing binary) is not fatal to callers: it just means the
// snapshot's wezterm_version field is left blank.
func WeztermVersion(ctx context.Context) (string, error) {
	out, err := iexec.OSRunner{}.Run(ctx, "wezterm", "--version")
	if err != nil {
		return "", fmt.Errorf("app: wezterm --version: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// NewStore wires the real Store rooted at StateDir.
func NewStore() (*snapshot.Store, error) {
	dir, err := StateDir()
	if err != nil {
		return nil, err
	}
	return snapshot.NewStore(dir), nil
}

// NewCapture wires the real Lister, SessionResolver, and Store for a save.
// now stamps the snapshot's CapturedAt (via Stamp); the caller should derive
// the history filename from the same now (via FileStamp) so both describe
// the same moment. Each call builds a fresh Capture, so a caller that
// invokes NewCapture again on a later tick (e.g. the daemon) gets an
// up-to-date CapturedAt/wezterm_version rather than one frozen at an earlier
// construction.
func NewCapture(ctx context.Context, now time.Time) (*capture.Capture, *snapshot.Store, error) {
	store, err := NewStore()
	if err != nil {
		return nil, nil, err
	}
	projects, err := ProjectsRoot()
	if err != nil {
		return nil, nil, err
	}
	run := iexec.OSRunner{}
	client := wezterm.New(run)
	resolver := session.New(run, projects)
	weztermV, err := WeztermVersion(ctx)
	if err != nil {
		weztermV = ""
	}
	c := capture.New(client, resolver, Stamp(now), weztermV)
	return c, store, nil
}

// NewRestore wires the real Terminal for a restore.
func NewRestore() *restore.Restore {
	client := wezterm.New(iexec.OSRunner{})
	return restore.New(client, fileExists)
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
