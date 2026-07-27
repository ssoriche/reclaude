package daemon

import (
	"context"
	"testing"

	"github.com/ssoriche/reclaude/internal/snapshot"
	"github.com/ssoriche/reclaude/internal/wezterm"
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

// TestTickSkipsErrNotRunning ensures a tick where WezTerm isn't running is
// treated as "nothing to capture", not a logged error: Tick returns nil and
// nothing is written, so a caller's error-logging callback in Run is never
// invoked every interval just because WezTerm happens to be closed.
func TestTickSkipsErrNotRunning(t *testing.T) {
	capture := func(context.Context) (*snapshot.Snapshot, error) {
		return nil, wezterm.ErrNotRunning
	}
	store := &recordingStore{}
	d := New(capture, store, func() string { return "stamp" }, 5)

	if err := d.Tick(context.Background()); err != nil {
		t.Fatalf("Tick returned error for ErrNotRunning: %v", err)
	}
	if store.saves != 0 {
		t.Fatalf("saves = %d, want 0", store.saves)
	}
}
