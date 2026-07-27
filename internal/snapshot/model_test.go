package snapshot

import (
	"encoding/json"
	"testing"

	"github.com/ssoriche/reclaude/internal/layout"
)

func TestSnapshotJSONRoundTrip(t *testing.T) {
	s := Snapshot{
		Version:    1,
		CapturedAt: "2026-07-23T14:02:11Z",
		Windows: []Window{{
			Title:          "w",
			Workspace:      "default",
			ActiveTabIndex: 0,
			Tabs: []Tab{{
				ActivePaneIndex: 1,
				Layout: &LayoutNode{
					Split: string(layout.SplitVertical),
					Ratio: 0.5,
					Children: []*LayoutNode{
						{Pane: &Leaf{CWD: "/a"}},
						{Pane: &Leaf{CWD: "/b", Claude: &ClaudeSession{SessionID: "x", Resumable: true}}},
					},
				},
			}},
		}},
	}
	data, err := json.Marshal(s)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got Snapshot
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	leaf := got.Windows[0].Tabs[0].Layout.Children[1].Pane
	if leaf.Claude == nil || leaf.Claude.SessionID != "x" {
		t.Fatalf("claude session lost in round-trip: %+v", leaf)
	}
}
