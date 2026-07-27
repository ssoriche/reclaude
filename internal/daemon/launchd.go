package daemon

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"os"

	iexec "github.com/ssoriche/reclaude/internal/exec"
)

// Label is the LaunchAgent label.
const Label = "net.reclaude.daemon"

// escapeXML makes s safe to interpolate into plist character data (e.g. a
// binPath/logPath containing "&", "<", or ">").
func escapeXML(s string) string {
	var buf bytes.Buffer
	// xml.EscapeText cannot fail writing into a bytes.Buffer.
	_ = xml.EscapeText(&buf, []byte(s))
	return buf.String()
}

func renderPlist(binPath, logPath string) string {
	bin := escapeXML(binPath)
	log := escapeXML(logPath)
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>%s</string>
  <key>ProgramArguments</key>
  <array>
    <string>%s</string>
    <string>daemon</string>
    <string>run</string>
  </array>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
  <key>StandardOutPath</key><string>%s</string>
  <key>StandardErrorPath</key><string>%s</string>
</dict>
</plist>
`, Label, bin, log, log)
}

// bootout runs `launchctl bootout gui/<uid>/<Label>`. Its error is
// deliberately not surfaced to the caller: launchctl reports a nonzero exit
// when the service isn't currently loaded (the common case — a first
// install, or an uninstall run twice), and our exec.Runner abstraction has
// no typed way to tell that apart from other failures. Treating it as
// best-effort keeps Install/Uninstall idempotent.
func bootout(ctx context.Context, run iexec.Runner, uid string) {
	_, _ = run.Run(ctx, "launchctl", "bootout", "gui/"+uid+"/"+Label)
}

// Install writes the plist and bootstraps it into the user's gui domain. It
// first best-effort boots out any existing registration under this label so
// re-running install (e.g. after rebuilding the binary) doesn't fail with
// "already bootstrapped".
func Install(ctx context.Context, run iexec.Runner, plistPath, binPath, logPath, uid string) error {
	bootout(ctx, run, uid)
	if err := os.WriteFile(plistPath, []byte(renderPlist(binPath, logPath)), 0o644); err != nil {
		return fmt.Errorf("daemon: write plist: %w", err)
	}
	if _, err := run.Run(ctx, "launchctl", "bootstrap", "gui/"+uid, plistPath); err != nil {
		return fmt.Errorf("daemon: bootstrap: %w", err)
	}
	return nil
}

// Uninstall boots out the agent and removes the plist. A bootout failure
// (the LaunchAgent isn't currently loaded) is tolerated in the same spirit
// as the os.IsNotExist tolerance below for an already-removed plist: either
// way, the end state Uninstall promises — nothing loaded, no plist file —
// is already achieved.
func Uninstall(ctx context.Context, run iexec.Runner, plistPath, uid string) error {
	bootout(ctx, run, uid)
	if err := os.Remove(plistPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("daemon: remove plist: %w", err)
	}
	return nil
}
