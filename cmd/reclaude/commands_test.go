package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ssoriche/reclaude/internal/snapshot"
	"github.com/ssoriche/reclaude/internal/wezterm"
)

// TestRunRestoreMissingLatest exercises the testable core behind cmdRestore:
// with no latest.json in the store, it must print a friendly message to
// stderr and exit 1 without ever invoking the restorer.
func TestRunRestoreMissingLatest(t *testing.T) {
	store := snapshot.NewStore(t.TempDir())
	var out, errb bytes.Buffer
	restorer := func(context.Context, *snapshot.Snapshot) error {
		t.Fatal("restorer must not be called when latest.json is missing")
		return nil
	}
	code := runRestore(context.Background(), &out, &errb, store, restorer)
	if code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	if !strings.Contains(errb.String(), "no saved session") {
		t.Fatalf("expected friendly message on stderr, got %q", errb.String())
	}
	if out.Len() != 0 {
		t.Fatalf("expected no stdout output, got %q", out.String())
	}
}

// TestRunSaveWezTermNotRunning exercises the testable core behind cmdSave:
// a capture failing with wezterm.ErrNotRunning is a friendly no-op on
// stdout with exit 0, not an error.
func TestRunSaveWezTermNotRunning(t *testing.T) {
	store := snapshot.NewStore(t.TempDir())
	var out, errb bytes.Buffer
	capture := func(context.Context) (*snapshot.Snapshot, error) { return nil, wezterm.ErrNotRunning }
	code := runSave(context.Background(), &out, &errb, time.Now(), capture, store)
	if code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	if !strings.Contains(out.String(), "not running") {
		t.Fatalf("expected friendly message on stdout, got %q", out.String())
	}
	if errb.Len() != 0 {
		t.Fatalf("expected no stderr output, got %q", errb.String())
	}
}

// TestRunStatusWezTermNotRunning mirrors TestRunSaveWezTermNotRunning for
// the status command's core.
func TestRunStatusWezTermNotRunning(t *testing.T) {
	var out, errb bytes.Buffer
	capture := func(context.Context) (*snapshot.Snapshot, error) { return nil, wezterm.ErrNotRunning }
	code := runStatus(context.Background(), &out, &errb, capture)
	if code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	if !strings.Contains(out.String(), "not running") {
		t.Fatalf("expected friendly message on stdout, got %q", out.String())
	}
	if errb.Len() != 0 {
		t.Fatalf("expected no stderr output, got %q", errb.String())
	}
}

// TestRunListShowsHistoryEntries exercises the testable core behind
// cmdList over a real store rooted at a temp dir with a couple of saved
// history entries.
func TestRunListShowsHistoryEntries(t *testing.T) {
	store := snapshot.NewStore(t.TempDir())
	snapA := &snapshot.Snapshot{Version: 1, Windows: []snapshot.Window{{Title: "a"}}}
	snapB := &snapshot.Snapshot{Version: 1, Windows: []snapshot.Window{{Title: "b"}, {Title: "c"}}}
	if err := store.Save(snapA, "20260101T000000.000Z", 20); err != nil {
		t.Fatalf("seed a: %v", err)
	}
	if err := store.Save(snapB, "20260102T000000.000Z", 20); err != nil {
		t.Fatalf("seed b: %v", err)
	}
	var out, errb bytes.Buffer
	code := runList(&out, &errb, store)
	if code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	got := out.String()
	if !strings.Contains(got, "20260101T000000.000Z.json") {
		t.Fatalf("expected first history entry listed, got %q", got)
	}
	if !strings.Contains(got, "20260102T000000.000Z.json") {
		t.Fatalf("expected second history entry listed, got %q", got)
	}
	if errb.Len() != 0 {
		t.Fatalf("expected no stderr output, got %q", errb.String())
	}
}

// TestRunListEmptyHistory covers the no-entries branch.
func TestRunListEmptyHistory(t *testing.T) {
	store := snapshot.NewStore(t.TempDir())
	var out, errb bytes.Buffer
	code := runList(&out, &errb, store)
	if code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	if !strings.Contains(out.String(), "no history entries") {
		t.Fatalf("expected no-history message, got %q", out.String())
	}
}
