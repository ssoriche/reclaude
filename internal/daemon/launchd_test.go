package daemon

import (
	"context"
	"encoding/xml"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	iexec "github.com/ssoriche/reclaude/internal/exec"
)

func TestRenderPlistContainsProgramArgs(t *testing.T) {
	out := renderPlist("/usr/local/bin/reclaude", "/log/daemon.log")
	for _, want := range []string{"net.reclaude.daemon", "/usr/local/bin/reclaude", "<string>daemon</string>", "<string>run</string>", "RunAtLoad", "KeepAlive", "/log/daemon.log"} {
		if !strings.Contains(out, want) {
			t.Fatalf("plist missing %q:\n%s", want, out)
		}
	}
}

// TestRenderPlistEscapesXML guards against a binPath/logPath containing XML
// metacharacters (e.g. "&") corrupting the plist.
func TestRenderPlistEscapesXML(t *testing.T) {
	out := renderPlist("/usr/local/bin/re&claude", "/log/da&mon.log")
	if strings.Contains(out, "re&claude") || strings.Contains(out, "da&mon.log") {
		t.Fatalf("plist contains a raw unescaped ampersand:\n%s", out)
	}
	if !strings.Contains(out, "re&amp;claude") || !strings.Contains(out, "da&amp;mon.log") {
		t.Fatalf("expected escaped ampersands, got:\n%s", out)
	}

	// The whole document must still be well-formed XML.
	dec := xml.NewDecoder(strings.NewReader(out))
	for {
		if _, err := dec.Token(); err != nil {
			if err == io.EOF {
				break
			}
			t.Fatalf("plist is not well-formed XML: %v\n%s", err, out)
		}
	}
}

func TestInstallWritesPlistAndBootstraps(t *testing.T) {
	dir := t.TempDir()
	plist := filepath.Join(dir, "net.reclaude.daemon.plist")
	f := &iexec.FakeRunner{Responses: map[string]iexec.FakeResponse{
		"launchctl bootstrap gui/501 " + plist: {},
	}}
	err := Install(context.Background(), f, plist, "/bin/reclaude", "/log/daemon.log", "501")
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	if _, err := os.Stat(plist); err != nil {
		t.Fatalf("plist not written: %v", err)
	}
	var bootoutIdx, bootstrapIdx = -1, -1
	for i, c := range f.Calls {
		if strings.HasPrefix(c, "launchctl bootout gui/501") {
			bootoutIdx = i
		}
		if strings.HasPrefix(c, "launchctl bootstrap gui/501") {
			bootstrapIdx = i
		}
	}
	if bootstrapIdx == -1 {
		t.Fatalf("bootstrap not called: %v", f.Calls)
	}
	if bootoutIdx == -1 {
		t.Fatalf("expected a best-effort bootout before bootstrap (idempotent install): %v", f.Calls)
	}
	if bootoutIdx > bootstrapIdx {
		t.Fatalf("bootout must precede bootstrap: %v", f.Calls)
	}
}

// TestInstallToleratesNoExistingService ensures a bootout failure (no
// FakeResponse registered simulates "service not currently loaded", the
// common case for a first-ever install) does not abort Install.
func TestInstallToleratesNoExistingService(t *testing.T) {
	dir := t.TempDir()
	plist := filepath.Join(dir, "net.reclaude.daemon.plist")
	f := &iexec.FakeRunner{Responses: map[string]iexec.FakeResponse{
		"launchctl bootstrap gui/501 " + plist: {},
	}}
	if err := Install(context.Background(), f, plist, "/bin/reclaude", "/log/daemon.log", "501"); err != nil {
		t.Fatalf("Install: %v", err)
	}
}

func TestUninstallRemovesPlistAndBootsOut(t *testing.T) {
	dir := t.TempDir()
	plist := filepath.Join(dir, "net.reclaude.daemon.plist")
	if err := os.WriteFile(plist, []byte("dummy"), 0o644); err != nil {
		t.Fatalf("seed plist: %v", err)
	}
	f := &iexec.FakeRunner{Responses: map[string]iexec.FakeResponse{
		"launchctl bootout gui/501/" + Label: {},
	}}
	if err := Uninstall(context.Background(), f, plist, "501"); err != nil {
		t.Fatalf("Uninstall: %v", err)
	}
	if _, err := os.Stat(plist); !os.IsNotExist(err) {
		t.Fatalf("plist not removed: %v", err)
	}
}

// TestUninstallToleratesNotLoaded ensures Uninstall still succeeds and
// removes the plist when the LaunchAgent is no longer loaded (bootout
// fails), the same spirit as its existing tolerance for a missing plist.
func TestUninstallToleratesNotLoaded(t *testing.T) {
	dir := t.TempDir()
	plist := filepath.Join(dir, "net.reclaude.daemon.plist")
	if err := os.WriteFile(plist, []byte("dummy"), 0o644); err != nil {
		t.Fatalf("seed plist: %v", err)
	}
	// No FakeResponse registered for bootout: FakeRunner returns
	// ErrNoFakeResponse, simulating launchctl's nonzero exit for "service
	// not loaded".
	f := &iexec.FakeRunner{Responses: map[string]iexec.FakeResponse{}}
	if err := Uninstall(context.Background(), f, plist, "501"); err != nil {
		t.Fatalf("Uninstall: %v", err)
	}
	if _, err := os.Stat(plist); !os.IsNotExist(err) {
		t.Fatalf("plist not removed: %v", err)
	}
}
