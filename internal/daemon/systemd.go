package daemon

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	iexec "github.com/ssoriche/reclaude/internal/exec"
)

// UnitName is the systemd user unit (Linux).
const UnitName = "reclaude.service"

// systemdEscape doubles '%' so systemd does not expand it as a specifier.
func systemdEscape(s string) string { return strings.ReplaceAll(s, "%", "%%") }

// systemdQuote renders s as one double-quoted systemd word: backslash and
// double quote are backslash-escaped, and '%' is doubled.
func systemdQuote(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return `"` + systemdEscape(s) + `"`
}

// renderUnit returns the unit file. pathEnv is baked into the unit because the
// user manager's default PATH omits most user-installed bin dirs, and a
// wezterm missing from PATH is indistinguishable from WezTerm not running: the
// daemon would quietly capture nothing.
func renderUnit(binPath, logPath, pathEnv string) (string, error) {
	for name, v := range map[string]string{"binary path": binPath, "log path": logPath, "PATH": pathEnv} {
		if strings.ContainsAny(v, "\n\r") {
			return "", fmt.Errorf("daemon: %s contains a newline: %q", name, v)
		}
	}
	// '$' is expanded as an environment variable in ExecStart only.
	execBin := strings.ReplaceAll(systemdQuote(binPath), "$", "$$")
	log := systemdEscape(logPath)
	return fmt.Sprintf(`[Unit]
Description=reclaude periodic WezTerm layout snapshot

[Service]
ExecStart=%s daemon run
Environment=%s
Restart=always
RestartSec=5
StandardOutput=append:%s
StandardError=append:%s

[Install]
WantedBy=default.target
`, execBin, systemdQuote("PATH="+pathEnv), log, log), nil
}

func systemctl(ctx context.Context, run iexec.Runner, args ...string) error {
	if _, err := run.Run(ctx, "systemctl", append([]string{"--user"}, args...)...); err != nil {
		return fmt.Errorf("daemon: systemctl --user %s: %w", strings.Join(args, " "), err)
	}
	return nil
}

// InstallSystemd writes the user unit, enables it so it starts on login, and
// (re)starts it. Restarting rather than starting keeps re-running install
// after rebuilding the binary effective.
func InstallSystemd(ctx context.Context, run iexec.Runner, unitPath, binPath, logPath, pathEnv string) error {
	unit, err := renderUnit(binPath, logPath, pathEnv)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(unitPath), 0o755); err != nil {
		return fmt.Errorf("daemon: mkdir unit dir: %w", err)
	}
	if err := os.WriteFile(unitPath, []byte(unit), 0o644); err != nil {
		return fmt.Errorf("daemon: write unit: %w", err)
	}
	if err := systemctl(ctx, run, "daemon-reload"); err != nil {
		return err
	}
	if err := systemctl(ctx, run, "enable", UnitName); err != nil {
		return err
	}
	return systemctl(ctx, run, "restart", UnitName)
}

// UninstallSystemd stops and disables the unit, removes it, and reloads the
// user manager. With no unit file there is nothing installed and it is a
// no-op. If the service cannot be stopped, the unit file is left in place so
// the failure stays visible and retryable.
func UninstallSystemd(ctx context.Context, run iexec.Runner, unitPath string) error {
	if _, err := os.Stat(unitPath); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return fmt.Errorf("daemon: stat unit: %w", err)
	}
	if err := systemctl(ctx, run, "disable", "--now", UnitName); err != nil {
		return err
	}
	if err := os.Remove(unitPath); err != nil {
		return fmt.Errorf("daemon: remove unit: %w", err)
	}
	return systemctl(ctx, run, "daemon-reload")
}
