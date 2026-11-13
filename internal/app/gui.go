package app

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// guiSocketEnv is how `wezterm cli` is told which GUI instance to talk to.
const guiSocketEnv = "WEZTERM_UNIX_SOCKET"

// guiDialTimeout bounds the liveness probe of each candidate GUI socket.
const guiDialTimeout = 500 * time.Millisecond

// EnsureGUISocket points `wezterm cli` at the running WezTerm GUI when
// WEZTERM_UNIX_SOCKET is not already set, as it is not under launchd or
// systemd. Without it, wezterm cli locates the GUI through a symlink keyed by
// $DISPLAY/$WAYLAND_DISPLAY, which a service manager's environment often does
// not match (e.g. DISPLAY=:0 vs the GUI's :0.0), and then talks to the
// headless mux server instead of the GUI.
func EnsureGUISocket() error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("app: home dir: %w", err)
	}
	return ensureGUISocket(guiSocketDirs(home))
}

// guiSocketDirs returns where WezTerm creates gui-sock-<pid>: its runtime dir,
// which is $XDG_RUNTIME_DIR/wezterm when that is set and ~/.local/share/wezterm
// otherwise. Both are searched because the caller's environment may not match
// the GUI's.
func guiSocketDirs(home string) []string {
	var dirs []string
	if rt := os.Getenv("XDG_RUNTIME_DIR"); filepath.IsAbs(rt) {
		dirs = append(dirs, filepath.Join(rt, "wezterm"))
	}
	return append(dirs, filepath.Join(home, ".local", "share", "wezterm"))
}

func ensureGUISocket(dirs []string) error {
	if os.Getenv(guiSocketEnv) != "" {
		return nil
	}
	sock, err := discoverGUISocket(dirs)
	if err != nil {
		return err
	}
	if sock == "" {
		return nil // no GUI running; wezterm cli will report it unreachable
	}
	return os.Setenv(guiSocketEnv, sock)
}

// discoverGUISocket returns the one live gui-sock-* socket across dirs, "" if
// none is live, or an error if several are (two GUI instances cannot be told
// apart, and guessing would capture or restore into the wrong one). A socket
// is live if it accepts a connection; crashed GUIs leave dead ones behind.
func discoverGUISocket(dirs []string) (string, error) {
	seen := map[string]bool{}
	var live []string
	for _, dir := range dirs {
		matches, err := filepath.Glob(filepath.Join(dir, "gui-sock-*"))
		if err != nil {
			return "", fmt.Errorf("app: glob gui sockets: %w", err)
		}
		for _, m := range matches {
			m = filepath.Clean(m)
			if seen[m] {
				continue
			}
			seen[m] = true
			c, err := net.DialTimeout("unix", m, guiDialTimeout)
			if err != nil {
				continue
			}
			_ = c.Close()
			live = append(live, m)
		}
	}
	switch len(live) {
	case 0:
		return "", nil
	case 1:
		return live[0], nil
	default:
		sort.Strings(live)
		return "", fmt.Errorf("app: %d WezTerm GUI instances are running (%s); set %s to choose one",
			len(live), strings.Join(live, ", "), guiSocketEnv)
	}
}
