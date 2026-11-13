package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestRunNoArgsUsage(t *testing.T) {
	var out, errb bytes.Buffer
	code := run(context.Background(), nil, &out, &errb)
	if code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
	if !strings.Contains(errb.String(), "usage") {
		t.Fatalf("expected usage text, got %q", errb.String())
	}
}

func TestRunUnknownCommand(t *testing.T) {
	var out, errb bytes.Buffer
	code := run(context.Background(), []string{"bogus"}, &out, &errb)
	if code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
	if !strings.Contains(errb.String(), "unknown command") {
		t.Fatalf("expected unknown-command text, got %q", errb.String())
	}
}

func TestPlatformServiceUnsupportedOS(t *testing.T) {
	if _, err := platformService("windows", t.TempDir(), nil); err == nil {
		t.Fatal("platformService(windows) succeeded, want an unsupported-platform error")
	}
}

func TestPlatformServicePaths(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/xdg")
	linux, err := platformService("linux", "/state", nil)
	if err != nil {
		t.Fatalf("linux: %v", err)
	}
	if linux.path != "/xdg/systemd/user/reclaude.service" {
		t.Errorf("linux path = %q", linux.path)
	}
	darwin, err := platformService("darwin", "/state", nil)
	if err != nil {
		t.Fatalf("darwin: %v", err)
	}
	if darwin.path != "/state/net.reclaude.daemon.plist" {
		t.Errorf("darwin path = %q", darwin.path)
	}
}
