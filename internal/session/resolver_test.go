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
	ps := "  PID TTY      ELAPSED COMMAND\n" +
		" 4321 ttys003  01:02:03 claude --resume 0b3f-sess\n" +
		" 4322 ttys005     10:00 claude\n" +
		" 9999 ttys004  02:03:04 -fish\n"
	f := &iexec.FakeRunner{Responses: map[string]iexec.FakeResponse{
		"ps -axo pid,tty,etime,command": {Stdout: []byte(ps)},
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
	ps := " 4321 ttys003    05:00 claude --resume=abc-123\n"
	f := &iexec.FakeRunner{Responses: map[string]iexec.FakeResponse{
		"ps -axo pid,tty,etime,command": {Stdout: []byte(ps)},
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

// StartedAt is what separates two panes running bare claude in one cwd, so the
// elapsed column must survive ps's several formats.
func TestProcsDerivesStartedAtFromElapsed(t *testing.T) {
	ps := " 4321 ttys003     10:30 claude\n" +
		" 4322 ttys005  02:10:30 claude\n" +
		" 4323 ttys006 3-02:10:30 claude\n" +
		" 4324 ttys007         ? claude\n"
	f := &iexec.FakeRunner{Responses: map[string]iexec.FakeResponse{
		"ps -axo pid,tty,etime,command": {Stdout: []byte(ps)},
	}}
	now := time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)
	r := New(f, "/Users/x/.claude/projects")
	r.now = func() time.Time { return now }

	got, err := r.Procs(context.Background())
	if err != nil {
		t.Fatalf("Procs: %v", err)
	}
	want := map[string]time.Duration{
		"ttys003": 10*time.Minute + 30*time.Second,
		"ttys005": 2*time.Hour + 10*time.Minute + 30*time.Second,
		"ttys006": 3*24*time.Hour + 2*time.Hour + 10*time.Minute + 30*time.Second,
	}
	for tty, ago := range want {
		if !got[tty].StartedAt.Equal(now.Add(-ago)) {
			t.Errorf("%s StartedAt = %v, want %v", tty, got[tty].StartedAt, now.Add(-ago))
		}
	}
	// An unparseable elapsed column must leave StartedAt zero rather than
	// claiming the process started now.
	if !got["ttys007"].StartedAt.IsZero() {
		t.Errorf("ttys007 StartedAt = %v, want zero", got["ttys007"].StartedAt)
	}
}

// Two panes in one cwd, each running a bare `claude`, must be assigned the two
// distinct sessions they are actually running -- never the same one twice.
func TestAssignGivesDistinctSessionsToPanesSharingACWD(t *testing.T) {
	root := t.TempDir()
	cwd := "/Volumes/proj"
	projectDir := filepath.Join(root, SlugFor(cwd))
	mkdirAll(t, projectDir)

	base := time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)
	// A session's transcript is stamped a moment after its pane's claude starts.
	writeTranscript(t, projectDir, "sess-early", base.Add(2*time.Second))
	writeTranscript(t, projectDir, "sess-late", base.Add(time.Hour+2*time.Second))

	r := New(nil, root)
	got := r.Assign([]PaneProc{
		{PaneID: 1, CWD: cwd, StartedAt: base},
		{PaneID: 2, CWD: cwd, StartedAt: base.Add(time.Hour)},
	})

	if got[1].SessionID != "sess-early" {
		t.Errorf("pane 1 SessionID = %q, want sess-early", got[1].SessionID)
	}
	if got[2].SessionID != "sess-late" {
		t.Errorf("pane 2 SessionID = %q, want sess-late", got[2].SessionID)
	}
	if got[1].SessionID == got[2].SessionID {
		t.Fatalf("both panes assigned the same session %q", got[1].SessionID)
	}
}

// When a cwd has fewer transcripts than claude panes, the surplus panes must be
// left unassigned rather than sharing a session with another pane.
func TestAssignLeavesSurplusPanesUnassigned(t *testing.T) {
	root := t.TempDir()
	cwd := "/Volumes/proj"
	projectDir := filepath.Join(root, SlugFor(cwd))
	mkdirAll(t, projectDir)

	base := time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)
	writeTranscript(t, projectDir, "only-sess", base.Add(2*time.Second))

	r := New(nil, root)
	got := r.Assign([]PaneProc{
		{PaneID: 1, CWD: cwd, StartedAt: base},
		{PaneID: 2, CWD: cwd, StartedAt: base.Add(time.Hour)},
	})

	if len(got) != 1 {
		t.Fatalf("assigned %d panes, want 1: %v", len(got), got)
	}
	if got[1].SessionID != "only-sess" {
		t.Fatalf("pane 1 SessionID = %q, want only-sess", got[1].SessionID)
	}
}

// A transcript that started before the pane's claude process cannot belong to
// it, so it must not be resurrected onto that pane.
func TestAssignIgnoresSessionsPredatingThePane(t *testing.T) {
	root := t.TempDir()
	cwd := "/Volumes/proj"
	projectDir := filepath.Join(root, SlugFor(cwd))
	mkdirAll(t, projectDir)

	base := time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)
	writeTranscript(t, projectDir, "yesterdays-sess", base.Add(-24*time.Hour))

	r := New(nil, root)
	got := r.Assign([]PaneProc{{PaneID: 1, CWD: cwd, StartedAt: base}})

	if len(got) != 0 {
		t.Fatalf("assigned %v, want no assignment (session predates the pane)", got)
	}
}

func TestAssignHonoursExplicitResumeHint(t *testing.T) {
	root := t.TempDir()
	cwd := "/Volumes/proj"
	projectDir := filepath.Join(root, SlugFor(cwd))
	mkdirAll(t, projectDir)

	base := time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)
	// The resumed session started long before the pane; the hint must still win,
	// and must not be handed to the bare pane by start-time matching.
	writeTranscript(t, projectDir, "resume-target", base.Add(-72*time.Hour))
	writeTranscript(t, projectDir, "fresh-sess", base.Add(2*time.Second))

	r := New(nil, root)
	got := r.Assign([]PaneProc{
		{PaneID: 1, CWD: cwd, ResumeID: "resume-target", StartedAt: base},
		{PaneID: 2, CWD: cwd, StartedAt: base},
	})

	if got[1].SessionID != "resume-target" {
		t.Errorf("pane 1 SessionID = %q, want resume-target", got[1].SessionID)
	}
	if got[2].SessionID != "fresh-sess" {
		t.Errorf("pane 2 SessionID = %q, want fresh-sess", got[2].SessionID)
	}
}

// A session claimed by an explicit hint is off the table for start-time
// matching, even when it would otherwise be the best match.
func TestAssignDoesNotReuseAHintedSessionForABarePane(t *testing.T) {
	root := t.TempDir()
	cwd := "/Volumes/proj"
	projectDir := filepath.Join(root, SlugFor(cwd))
	mkdirAll(t, projectDir)

	base := time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)
	writeTranscript(t, projectDir, "hinted-sess", base.Add(2*time.Second))

	r := New(nil, root)
	got := r.Assign([]PaneProc{
		{PaneID: 1, CWD: cwd, ResumeID: "hinted-sess", StartedAt: base},
		{PaneID: 2, CWD: cwd, StartedAt: base},
	})

	if got[1].SessionID != "hinted-sess" {
		t.Errorf("pane 1 SessionID = %q, want hinted-sess", got[1].SessionID)
	}
	if _, ok := got[2]; ok {
		t.Fatalf("pane 2 assigned %q, want unassigned", got[2].SessionID)
	}
}

func TestAssignIgnoresSubagentTranscripts(t *testing.T) {
	root := t.TempDir()
	cwd := "/Volumes/proj"
	projectDir := filepath.Join(root, SlugFor(cwd))
	mkdirAll(t, projectDir)

	base := time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)
	writeTranscript(t, projectDir, "top-level-sess", base.Add(2*time.Second))

	// A subagent transcript started later than the top-level one must be ignored
	// because it lives in a subdir, not at the top level.
	subDir := filepath.Join(projectDir, "top-level-sess", "subagents")
	mkdirAll(t, subDir)
	writeTranscript(t, subDir, "subagent-sess", base.Add(time.Minute))

	r := New(nil, root)
	got := r.Assign([]PaneProc{{PaneID: 1, CWD: cwd, StartedAt: base}})

	if got[1].SessionID != "top-level-sess" {
		t.Fatalf("SessionID = %q, want top-level-sess (subagent file must be ignored)", got[1].SessionID)
	}
	if got[1].ProjectDir != projectDir {
		t.Fatalf("ProjectDir = %q, want %q", got[1].ProjectDir, projectDir)
	}
}

func TestAssignMissingProjectDirYieldsNoAssignment(t *testing.T) {
	r := New(nil, t.TempDir())
	got := r.Assign([]PaneProc{{PaneID: 1, CWD: "/nonexistent/cwd"}})
	if len(got) != 0 {
		t.Fatalf("assigned %v, want none for a cwd whose slug dir does not exist", got)
	}
}

// Panes in different cwds must not compete for one another's transcripts.
func TestAssignScopesSessionsToTheirOwnProjectDir(t *testing.T) {
	root := t.TempDir()
	base := time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)

	alphaDir := filepath.Join(root, SlugFor("/Volumes/alpha"))
	betaDir := filepath.Join(root, SlugFor("/Volumes/beta"))
	mkdirAll(t, alphaDir)
	mkdirAll(t, betaDir)
	writeTranscript(t, alphaDir, "alpha-sess", base.Add(2*time.Second))
	writeTranscript(t, betaDir, "beta-sess", base.Add(2*time.Second))

	r := New(nil, root)
	got := r.Assign([]PaneProc{
		{PaneID: 1, CWD: "/Volumes/alpha", StartedAt: base},
		{PaneID: 2, CWD: "/Volumes/beta", StartedAt: base},
	})

	if got[1].SessionID != "alpha-sess" {
		t.Errorf("pane 1 SessionID = %q, want alpha-sess", got[1].SessionID)
	}
	if got[2].SessionID != "beta-sess" {
		t.Errorf("pane 2 SessionID = %q, want beta-sess", got[2].SessionID)
	}
}

func mkdirAll(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
}

// writeTranscript writes a transcript whose recorded session start is at. The
// leading untimestamped records mirror the real header Claude writes (and
// rewrites) at the top of a .jsonl.
func writeTranscript(t *testing.T, dir, sessionID string, at time.Time) {
	t.Helper()
	body := `{"type":"last-prompt","leafUuid":"x","sessionId":"` + sessionID + `"}` + "\n" +
		`{"type":"mode"}` + "\n" +
		`{"type":"attachment","timestamp":"` + at.UTC().Format(time.RFC3339Nano) + `"}` + "\n" +
		`{"type":"user","timestamp":"` + at.Add(time.Minute).UTC().Format(time.RFC3339Nano) + `"}` + "\n"
	writeFile(t, filepath.Join(dir, sessionID+".jsonl"), body)
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
