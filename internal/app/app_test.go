package app

import (
	"testing"
	"time"
)

func TestStampFormat(t *testing.T) {
	ts := time.Date(2026, 7, 23, 14, 2, 11, 123000000, time.UTC)
	// Stamp is the human/RFC3339 value stored in CapturedAt.
	if got := Stamp(ts); got != "2026-07-23T14:02:11.123Z" {
		t.Fatalf("Stamp = %q", got)
	}
	// FileStamp is the sortable, filesystem-safe history filename base.
	if got := FileStamp(ts); got != "20260723T140211.123Z" {
		t.Fatalf("FileStamp = %q", got)
	}
}

func TestStateDirUsesHome(t *testing.T) {
	if got := stateDir("/home/x"); got != "/home/x/.local/state/reclaude" {
		t.Fatalf("stateDir = %q", got)
	}
}

func TestProjectsRootUsesHome(t *testing.T) {
	if got := projectsRoot("/home/x"); got != "/home/x/.claude/projects" {
		t.Fatalf("projectsRoot = %q", got)
	}
}
