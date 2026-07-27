// Package snapshot defines the serialized reclaude state model and its storage.
package snapshot

// Snapshot is the top-level captured state.
type Snapshot struct {
	Version        int      `json:"version"`
	CapturedAt     string   `json:"captured_at"`
	WeztermVersion string   `json:"wezterm_version,omitempty"`
	Windows        []Window `json:"windows"`
}

// Window groups tabs.
type Window struct {
	Title          string `json:"title"`
	Workspace      string `json:"workspace"`
	ActiveTabIndex int    `json:"active_tab_index"`
	Tabs           []Tab  `json:"tabs"`
}

// Tab holds a layout tree.
type Tab struct {
	Title           string      `json:"title"`
	ActivePaneIndex int         `json:"active_pane_index"`
	ZoomedPaneIndex *int        `json:"zoomed_pane_index"`
	Layout          *LayoutNode `json:"layout"`
}

// LayoutNode is a serialized guillotine tree node (split or leaf).
type LayoutNode struct {
	Split    string        `json:"split,omitempty"` // "" for leaf
	Ratio    float64       `json:"ratio,omitempty"`
	Children []*LayoutNode `json:"children,omitempty"`
	Pane     *Leaf         `json:"pane,omitempty"`
}

// Leaf is a restorable pane.
type Leaf struct {
	CWD    string         `json:"cwd"`
	Claude *ClaudeSession `json:"claude"`
	Cols   int            `json:"cols"`
	Rows   int            `json:"rows"`
}

// ClaudeSession is the resumable session on a pane.
type ClaudeSession struct {
	SessionID  string `json:"session_id"`
	ProjectDir string `json:"project_dir"`
	Resumable  bool   `json:"resumable"`
}
