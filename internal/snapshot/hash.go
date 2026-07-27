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
