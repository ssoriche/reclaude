package session

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	iexec "github.com/ssoriche/reclaude/internal/exec"
)

func TestSlugFor(t *testing.T) {
	cases := map[string]string{
		"/Users/dev/projects/alpha":   "-Users-dev-projects-alpha",
		"/Users/dev/.config/tool_box": "-Users-dev--config-tool-box",
		"/a/b_c.d":                    "-a-b-c-d",
	}
	for in, want := range cases {
		if got := SlugFor(in); got != want {
			t.Fatalf("SlugFor(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestProcsParsesResumeIDAndFiltersNonClaude(t *testing.T) {
	ps := "  PID TTY        COMMAND\n" +
		" 4321 ttys003    claude --resume 0b3f-sess\n" +
		" 4322 ttys005    claude\n" +
		" 9999 ttys004    -fish\n"
	f := &iexec.FakeRunner{Responses: map[string]iexec.FakeResponse{
		"ps -axo pid,tty,command": {Stdout: []byte(ps)},
	}}
	r := New(f, "/Users/x/.claude/projects")
	got, err := r.Procs(context.Background())
	if err != nil {
		t.Fatalf("Procs: %v", err)
	}
	p3, ok := got["ttys003"]
	if !ok {
		t.Fatalf("no proc for ttys003; got %v", got)
	}
	if p3.ResumeID != "0b3f-sess" {
		t.Fatalf("ResumeID = %q, want 0b3f-sess", p3.ResumeID)
	}
	p5, ok := got["ttys005"]
	if !ok {
		t.Fatalf("no proc for ttys005; got %v", got)
	}
	if p5.ResumeID != "" {
		t.Fatalf("ResumeID = %q, want empty", p5.ResumeID)
	}
	if _, ok := got["ttys004"]; ok {
		t.Fatal("fish tty should not be mapped")
	}
}

func TestProcsSupportsResumeEqualsForm(t *testing.T) {
	ps := " 4321 ttys003    claude --resume=abc-123\n"
	f := &iexec.FakeRunner{Responses: map[string]iexec.FakeResponse{
		"ps -axo pid,tty,command": {Stdout: []byte(ps)},
	}}
	r := New(f, "/Users/x/.claude/projects")
	got, err := r.Procs(context.Background())
	if err != nil {
		t.Fatalf("Procs: %v", err)
	}
	if got["ttys003"].ResumeID != "abc-123" {
		t.Fatalf("ResumeID = %q, want abc-123", got["ttys003"].ResumeID)
	}
}

func TestLocateNewestTopLevelWinsOverSubagent(t *testing.T) {
	root := t.TempDir()
	cwd := "/Volumes/proj"
	slug := SlugFor(cwd)
	projectDir := filepath.Join(root, slug)
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	older := filepath.Join(projectDir, "older-sess.jsonl")
	newer := filepath.Join(projectDir, "newer-sess.jsonl")
	writeFile(t, older, "{}")
	writeFile(t, newer, "{}")

	base := time.Date(2026, 7, 23, 12, 0, 0, 0, time.UTC)
	chtimes(t, older, base)
	chtimes(t, newer, base.Add(time.Hour))

	// A subagent transcript that is NEWER than both top-level files must be
	// ignored because it lives in a subdir, not at the top level.
	subDir := filepath.Join(projectDir, "newer-sess", "subagents")
	if err := os.MkdirAll(subDir, 0o755); err != nil {
		t.Fatal(err)
	}
	subFile := filepath.Join(subDir, "x.jsonl")
	writeFile(t, subFile, "{}")
	chtimes(t, subFile, base.Add(24*time.Hour))

	r := New(nil, root)
	sess, ok := r.Locate(cwd, "")
	if !ok {
		t.Fatal("Locate: expected ok")
	}
	if sess.SessionID != "newer-sess" {
		t.Fatalf("SessionID = %q, want newer-sess (subagent file must be ignored)", sess.SessionID)
	}
	if sess.ProjectDir != projectDir {
		t.Fatalf("ProjectDir = %q, want %q", sess.ProjectDir, projectDir)
	}
}

func TestLocatePrefersValidResumeIDOverNewest(t *testing.T) {
	root := t.TempDir()
	cwd := "/Volumes/proj"
	projectDir := filepath.Join(root, SlugFor(cwd))
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	older := filepath.Join(projectDir, "resume-target.jsonl")
	newer := filepath.Join(projectDir, "newest-sess.jsonl")
	writeFile(t, older, "{}")
	writeFile(t, newer, "{}")
	base := time.Date(2026, 7, 23, 12, 0, 0, 0, time.UTC)
	chtimes(t, older, base)
	chtimes(t, newer, base.Add(time.Hour))

	r := New(nil, root)
	sess, ok := r.Locate(cwd, "resume-target")
	if !ok {
		t.Fatal("Locate: expected ok")
	}
	if sess.SessionID != "resume-target" {
		t.Fatalf("SessionID = %q, want resume-target (explicit resumeID should win)", sess.SessionID)
	}
}

func TestLocateFallsBackToNewestWhenResumeIDFileAbsent(t *testing.T) {
	root := t.TempDir()
	cwd := "/Volumes/proj"
	projectDir := filepath.Join(root, SlugFor(cwd))
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	newer := filepath.Join(projectDir, "newest-sess.jsonl")
	writeFile(t, newer, "{}")

	r := New(nil, root)
	sess, ok := r.Locate(cwd, "stale-resume-id")
	if !ok {
		t.Fatal("Locate: expected ok (fallback to newest)")
	}
	if sess.SessionID != "newest-sess" {
		t.Fatalf("SessionID = %q, want newest-sess (stale resumeID should fall back)", sess.SessionID)
	}
}

func TestLocateMissingProjectDirIsNotOK(t *testing.T) {
	root := t.TempDir()
	r := New(nil, root)
	if _, ok := r.Locate("/nonexistent/cwd", ""); ok {
		t.Fatal("expected ok=false for a cwd whose slug dir does not exist")
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func chtimes(t *testing.T, path string, at time.Time) {
	t.Helper()
	if err := os.Chtimes(path, at, at); err != nil {
		t.Fatal(err)
	}
}
