package wezterm

// Pane is one entry from `wezterm cli list --format json`.
type Pane struct {
	WindowID    int    `json:"window_id"`
	TabID       int    `json:"tab_id"`
	PaneID      int    `json:"pane_id"`
	Workspace   string `json:"workspace"`
	Size        Size   `json:"size"`
	Title       string `json:"title"`
	CWD         string `json:"cwd"` // file://host/path
	LeftCol     int    `json:"left_col"`
	TopRow      int    `json:"top_row"`
	TabTitle    string `json:"tab_title"`
	WindowTitle string `json:"window_title"`
	IsActive    bool   `json:"is_active"`
	IsZoomed    bool   `json:"is_zoomed"`
	TTYName     string `json:"tty_name"`
}

// Size is a pane's cell/pixel dimensions.
type Size struct {
	Rows int `json:"rows"`
	Cols int `json:"cols"`
}
