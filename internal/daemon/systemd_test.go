package daemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	iexec "github.com/ssoriche/reclaude/internal/exec"
)

func TestRenderUnitContainsServiceDefinition(t *testing.T) {
	out, err := renderUnit("/home/x/.local/bin/reclaude", "/home/x/.local/state/reclaude/daemon.log", "/usr/bin:/bin")
	if err != nil {
		t.Fatalf("renderUnit: %v", err)
	}
	for _, want := range []string{
		`ExecStart="/home/x/.local/bin/reclaude" daemon run`,
		"Restart=always",
		"StandardOutput=append:/home/x/.local/state/reclaude/daemon.log",
		"StandardError=append:/home/x/.local/state/reclaude/daemon.log",
		`Environment="PATH=/usr/bin:/bin"`,
		"WantedBy=default.target",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("unit missing %q:\n%s", want, out)
		}
	}
}

// systemd expands %-specifiers everywhere and $VARS in ExecStart, and splits
// ExecStart on whitespace, so paths must be escaped to survive verbatim.
func TestRenderUnitEscapesPaths(t *testing.T) {
	out, err := renderUnit(`/opt/my apps/re%claude$x"q\b`, "/log/100%.log", "/a%b:/c")
	if err != nil {
		t.Fatalf("renderUnit: %v", err)
	}
	for _, want := range []string{
		`ExecStart="/opt/my apps/re%%claude$$x\"q\\b" daemon run`,
		"StandardOutput=append:/log/100%%.log",
		`Environment="PATH=/a%%b:/c"`,
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("unit missing %q:\n%s", want, out)
		}
	}
}

func TestRenderUnitRejectsNewlines(t *testing.T) {
	if _, err := renderUnit("/bin/re\nclaude", "/log", "/usr/bin"); err == nil {
		t.Fatal("renderUnit accepted a newline in binPath")
	}
	if _, err := renderUnit("/bin/reclaude", "/lo\ng", "/usr/bin"); err == nil {
		t.Fatal("renderUnit accepted a newline in logPath")
	}
}

func systemdInstallResponses() map[string]iexec.FakeResponse {
	return map[string]iexec.FakeResponse{
		"systemctl --user daemon-reload":             {},
		"systemctl --user enable " + UnitName:        {},
		"systemctl --user restart " + UnitName:       {},
		"systemctl --user disable --now " + UnitName: {},
	}
}

func TestInstallSystemdWritesUnitAndStartsIt(t *testing.T) {
	unit := filepath.Join(t.TempDir(), "systemd", "user", UnitName)
	f := &iexec.FakeRunner{Responses: systemdInstallResponses()}
	if err := InstallSystemd(context.Background(), f, unit, "/bin/reclaude", "/log/daemon.log", "/usr/bin"); err != nil {
		t.Fatalf("InstallSystemd: %v", err)
	}
	data, err := os.ReadFile(unit)
	if err != nil {
		t.Fatalf("unit not written: %v", err)
	}
	if !strings.Contains(string(data), `ExecStart="/bin/reclaude" daemon run`) {
		t.Fatalf("unexpected unit:\n%s", data)
	}
	// restart (not start) so re-running install after rebuilding the binary
	// picks up the new one.
	want := []string{
		"systemctl --user daemon-reload",
		"systemctl --user enable " + UnitName,
		"systemctl --user restart " + UnitName,
	}
	if !slices.Equal(f.Calls, want) {
		t.Fatalf("calls = %v, want %v", f.Calls, want)
	}
}

func TestInstallSystemdSurfacesSystemctlFailure(t *testing.T) {
	unit := filepath.Join(t.TempDir(), UnitName)
	resp := systemdInstallResponses()
	resp["systemctl --user enable "+UnitName] = iexec.FakeResponse{Err: errors.New("exit status 1")}
	f := &iexec.FakeRunner{Responses: resp}
	if err := InstallSystemd(context.Background(), f, unit, "/bin/reclaude", "/log/daemon.log", "/usr/bin"); err == nil {
		t.Fatal("InstallSystemd succeeded despite systemctl enable failing")
	}
}

func TestUninstallSystemdStopsAndRemovesUnit(t *testing.T) {
	unit := filepath.Join(t.TempDir(), UnitName)
	if err := os.WriteFile(unit, []byte("dummy"), 0o644); err != nil {
		t.Fatal(err)
	}
	f := &iexec.FakeRunner{Responses: systemdInstallResponses()}
	if err := UninstallSystemd(context.Background(), f, unit); err != nil {
		t.Fatalf("UninstallSystemd: %v", err)
	}
	if _, err := os.Stat(unit); !os.IsNotExist(err) {
		t.Fatalf("unit not removed: %v", err)
	}
	want := []string{
		"systemctl --user disable --now " + UnitName,
		"systemctl --user daemon-reload",
	}
	if !slices.Equal(f.Calls, want) {
		t.Fatalf("calls = %v, want %v", f.Calls, want)
	}
}

// With no unit file there is nothing installed, so uninstall is a no-op
// rather than a systemctl error about an unknown unit.
func TestUninstallSystemdWithoutUnitIsNoOp(t *testing.T) {
	f := &iexec.FakeRunner{Responses: map[string]iexec.FakeResponse{}}
	if err := UninstallSystemd(context.Background(), f, filepath.Join(t.TempDir(), UnitName)); err != nil {
		t.Fatalf("UninstallSystemd: %v", err)
	}
	if len(f.Calls) != 0 {
		t.Fatalf("calls = %v, want none", f.Calls)
	}
}

func TestUninstallSystemdSurfacesDisableFailure(t *testing.T) {
	unit := filepath.Join(t.TempDir(), UnitName)
	if err := os.WriteFile(unit, []byte("dummy"), 0o644); err != nil {
		t.Fatal(err)
	}
	f := &iexec.FakeRunner{Responses: map[string]iexec.FakeResponse{}}
	if err := UninstallSystemd(context.Background(), f, unit); err == nil {
		t.Fatal("UninstallSystemd succeeded despite systemctl disable failing")
	}
	if _, err := os.Stat(unit); err != nil {
		t.Fatalf("unit removed even though the service could not be stopped: %v", err)
	}
}
