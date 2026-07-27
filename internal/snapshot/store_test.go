package snapshot

import (
	"os"
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

func TestStoreHistoryCollisionSuffix(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)
	first := &Snapshot{Version: 1, CapturedAt: "first"}
	second := &Snapshot{Version: 1, CapturedAt: "second"}
	if err := s.Save(first, "dup", 5); err != nil {
		t.Fatalf("Save first: %v", err)
	}
	if err := s.Save(second, "dup", 5); err != nil {
		t.Fatalf("Save second: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "history", "dup.json")); err != nil {
		t.Fatalf("dup.json missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "history", "dup_01.json")); err != nil {
		t.Fatalf("dup_01.json missing: %v", err)
	}
	got, err := s.Latest()
	if err != nil {
		t.Fatalf("Latest: %v", err)
	}
	if got.CapturedAt != second.CapturedAt {
		t.Fatalf("latest = %q, want %q", got.CapturedAt, second.CapturedAt)
	}
}

func TestStoreHistoryFiles(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)
	if err := s.Save(&Snapshot{Version: 1}, "20260101T000000.000Z", 20); err != nil {
		t.Fatalf("Save a: %v", err)
	}
	if err := s.Save(&Snapshot{Version: 1}, "20260102T000000.000Z", 20); err != nil {
		t.Fatalf("Save b: %v", err)
	}
	files, err := s.HistoryFiles()
	if err != nil {
		t.Fatalf("HistoryFiles: %v", err)
	}
	if len(files) != 2 {
		t.Fatalf("HistoryFiles = %v, want 2 entries", files)
	}
	if filepath.Base(files[0]) != "20260101T000000.000Z.json" || filepath.Base(files[1]) != "20260102T000000.000Z.json" {
		t.Fatalf("HistoryFiles not sorted chronologically: %v", files)
	}
}

func TestStoreHistoryFilesEmpty(t *testing.T) {
	s := NewStore(t.TempDir())
	files, err := s.HistoryFiles()
	if err != nil {
		t.Fatalf("HistoryFiles: %v", err)
	}
	if len(files) != 0 {
		t.Fatalf("HistoryFiles = %v, want empty", files)
	}
}

func TestStoreSaveLeavesNoTempFiles(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)
	if err := s.Save(&Snapshot{Version: 1}, "stamp", 5); err != nil {
		t.Fatalf("Save: %v", err)
	}
	histMatches, _ := filepath.Glob(filepath.Join(dir, "history", "*.tmp"))
	if len(histMatches) != 0 {
		t.Fatalf("leftover temp files in history: %v", histMatches)
	}
	topMatches, _ := filepath.Glob(filepath.Join(dir, "*.tmp"))
	if len(topMatches) != 0 {
		t.Fatalf("leftover temp files in dir: %v", topMatches)
	}
}
