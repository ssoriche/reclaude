package app

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// listen creates a live unix socket at dir/name. Socket paths are limited to
// ~104 bytes, so tests use a short temp dir rather than t.TempDir().
func listen(t *testing.T, dir, name string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	l, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("listen %s: %v", path, err)
	}
	t.Cleanup(func() { _ = l.Close() })
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
		}
	}()
	return path
}

// stale leaves a socket file behind with nothing listening, as a crashed GUI does.
func stale(t *testing.T, dir, name string) {
	t.Helper()
	l, err := net.Listen("unix", filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	l.(*net.UnixListener).SetUnlinkOnClose(false)
	_ = l.Close()
}

func shortDir(t *testing.T) string {
	t.Helper()
	d, err := os.MkdirTemp("", "rcgui")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(d) })
	return d
}

func TestDiscoverGUISocketFindsTheLiveGUI(t *testing.T) {
	dir := shortDir(t)
	stale(t, dir, "gui-sock-111")
	live := listen(t, dir, "gui-sock-222")
	listen(t, dir, "sock") // the headless mux server's socket must never be chosen

	got, err := discoverGUISocket([]string{filepath.Join(dir, "missing"), dir})
	if err != nil {
		t.Fatalf("discoverGUISocket: %v", err)
	}
	if got != live {
		t.Fatalf("got %q, want %q", got, live)
	}
}

func TestDiscoverGUISocketNoneRunning(t *testing.T) {
	dir := shortDir(t)
	stale(t, dir, "gui-sock-111")
	got, err := discoverGUISocket([]string{dir})
	if err != nil {
		t.Fatalf("discoverGUISocket: %v", err)
	}
	if got != "" {
		t.Fatalf("got %q, want empty (no live GUI)", got)
	}
}

// Two live GUIs cannot be disambiguated, and guessing would capture or restore
// into the wrong one.
func TestDiscoverGUISocketRejectsMultipleGUIs(t *testing.T) {
	dir := shortDir(t)
	listen(t, dir, "gui-sock-111")
	listen(t, dir, "gui-sock-222")
	_, err := discoverGUISocket([]string{dir})
	if err == nil || !strings.Contains(err.Error(), "WEZTERM_UNIX_SOCKET") {
		t.Fatalf("err = %v, want a multiple-GUI error naming WEZTERM_UNIX_SOCKET", err)
	}
}

// The same dir listed twice (XDG_RUNTIME_DIR unset on one path, say) must
// not make one GUI look like two.
func TestDiscoverGUISocketDedupesDirs(t *testing.T) {
	dir := shortDir(t)
	live := listen(t, dir, "gui-sock-222")
	got, err := discoverGUISocket([]string{dir, dir + "/"})
	if err != nil || got != live {
		t.Fatalf("got %q, %v; want %q", got, err, live)
	}
}

func TestEnsureGUISocketKeepsExplicitSetting(t *testing.T) {
	t.Setenv("WEZTERM_UNIX_SOCKET", "/explicit/sock")
	if err := ensureGUISocket([]string{shortDir(t)}); err != nil {
		t.Fatal(err)
	}
	if got := os.Getenv("WEZTERM_UNIX_SOCKET"); got != "/explicit/sock" {
		t.Fatalf("WEZTERM_UNIX_SOCKET = %q, want it untouched", got)
	}
}

func TestEnsureGUISocketSetsDiscoveredSocket(t *testing.T) {
	t.Setenv("WEZTERM_UNIX_SOCKET", "")
	dir := shortDir(t)
	live := listen(t, dir, "gui-sock-222")
	if err := ensureGUISocket([]string{dir}); err != nil {
		t.Fatal(err)
	}
	if got := os.Getenv("WEZTERM_UNIX_SOCKET"); got != live {
		t.Fatalf("WEZTERM_UNIX_SOCKET = %q, want %q", got, live)
	}
}
