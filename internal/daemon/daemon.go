// Package daemon runs periodic captures with change-detection.
package daemon

import (
	"context"
	"errors"
	"time"

	"github.com/ssoriche/reclaude/internal/snapshot"
	"github.com/ssoriche/reclaude/internal/wezterm"
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

// Tick captures once and writes only if the content hash changed. WezTerm
// not running is treated as "nothing to capture this tick", not an error:
// it is expected to happen routinely (e.g. between login and opening a
// terminal) and must not be logged as a failure on every interval.
func (d *Daemon) Tick(ctx context.Context) error {
	snap, err := d.capture(ctx)
	if errors.Is(err, wezterm.ErrNotRunning) {
		return nil
	}
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
