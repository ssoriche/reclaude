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

func (s *Store) latestPath() string { return filepath.Join(s.dir, "latest.json") }
func (s *Store) historyDir() string { return filepath.Join(s.dir, "history") }

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
	histFile, err := s.uniqueHistoryPath(stamp)
	if err != nil {
		return err
	}
	if err := atomicWrite(histFile, data); err != nil {
		return err
	}
	if err := atomicWrite(s.latestPath(), data); err != nil {
		return err
	}
	return s.rotate(keep)
}

// uniqueHistoryPath returns "<stamp>.json", or "<stamp>_NN.json" on collision.
// The "_" suffix (0x5F) sorts AFTER "." (0x2E), and the two-digit zero-padding
// keeps lexicographic order equal to chronological order for up to 99
// same-stamp collisions, so a collided (later) entry still orders after the
// original under the lexicographic rotate() sort.
func (s *Store) uniqueHistoryPath(stamp string) (string, error) {
	base := filepath.Join(s.historyDir(), stamp)
	path := base + ".json"
	for n := 1; ; n++ {
		_, err := os.Stat(path)
		if err == nil {
			path = fmt.Sprintf("%s_%02d.json", base, n)
			continue
		}
		if os.IsNotExist(err) {
			return path, nil
		}
		return "", fmt.Errorf("snapshot: stat history path: %w", err)
	}
}

func atomicWrite(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("snapshot: write temp: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("snapshot: rename: %w", err)
	}
	return nil
}

// HistoryFiles returns the history entry paths, sorted chronologically
// (ms-timestamp filenames sort lexicographically in chronological order).
func (s *Store) HistoryFiles() ([]string, error) {
	matches, err := filepath.Glob(filepath.Join(s.historyDir(), "*.json"))
	if err != nil {
		return nil, fmt.Errorf("snapshot: glob history: %w", err)
	}
	sort.Strings(matches)
	return matches, nil
}

func (s *Store) rotate(keep int) error {
	matches, err := s.HistoryFiles()
	if err != nil {
		return err
	}
	if len(matches) <= keep {
		return nil
	}
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
