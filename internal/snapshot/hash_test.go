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
