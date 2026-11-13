package session

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
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
	r := New(f, "/Users/x/.claude/projects", t.TempDir())
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
	r := New(f, "/Users/x/.claude/projects", t.TempDir())
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
	r := New(f, "/Users/x/.claude/projects", t.TempDir())
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

	r := New(nil, root, t.TempDir())
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

	r := New(nil, root, t.TempDir())
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

	r := New(nil, root, t.TempDir())
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

	r := New(nil, root, t.TempDir())
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

	r := New(nil, root, t.TempDir())
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

	r := New(nil, root, t.TempDir())
	got := r.Assign([]PaneProc{{PaneID: 1, CWD: cwd, StartedAt: base}})

	if got[1].SessionID != "top-level-sess" {
		t.Fatalf("SessionID = %q, want top-level-sess (subagent file must be ignored)", got[1].SessionID)
	}
	if got[1].ProjectDir != projectDir {
		t.Fatalf("ProjectDir = %q, want %q", got[1].ProjectDir, projectDir)
	}
}

func TestAssignMissingProjectDirYieldsNoAssignment(t *testing.T) {
	r := New(nil, t.TempDir(), t.TempDir())
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

	r := New(nil, root, t.TempDir())
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

// writeRegistry writes Claude's per-process session record,
// <sessionsDir>/<pid>.json, as a running claude maintains it.
func writeRegistry(t *testing.T, sessionsDir string, pid int, sessionID string, startedAt time.Time) {
	t.Helper()
	body := `{"pid":` + strconv.Itoa(pid) + `,"sessionId":"` + sessionID + `","cwd":"/Volumes/proj","startedAt":` +
		strconv.FormatInt(startedAt.UnixMilli(), 10) + `,"kind":"interactive"}`
	writeFile(t, filepath.Join(sessionsDir, strconv.Itoa(pid)+".json"), body)
}

// The registry names the session a claude process is actually running, which
// argv cannot: a bare `--resume` picker, a `--resume <name>`, or a later
// /resume all leave argv saying nothing useful about the live session.
func TestProcsReadsSessionIDFromRegistry(t *testing.T) {
	ps := " 4321 pts/3     10:00 claude --resume\n" +
		" 4322 pts/5     10:00 claude --resume my-named-session\n" +
		" 4323 pts/6     10:00 claude\n"
	f := &iexec.FakeRunner{Responses: map[string]iexec.FakeResponse{
		"ps -axo pid,tty,etime,command": {Stdout: []byte(ps)},
	}}
	now := time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)
	started := now.Add(-10 * time.Minute)
	sessions := t.TempDir()
	// Claude writes its record a few seconds to a few minutes after exec.
	writeRegistry(t, sessions, 4321, "picked-sess", started.Add(3*time.Second))
	writeRegistry(t, sessions, 4322, "named-sess-uuid", started.Add(2*time.Minute))

	r := New(f, "/Users/x/.claude/projects", sessions)
	r.now = func() time.Time { return now }
	got, err := r.Procs(context.Background())
	if err != nil {
		t.Fatalf("Procs: %v", err)
	}
	if got["pts/3"].SessionID != "picked-sess" {
		t.Errorf("pts/3 SessionID = %q, want picked-sess", got["pts/3"].SessionID)
	}
	if got["pts/5"].SessionID != "named-sess-uuid" {
		t.Errorf("pts/5 SessionID = %q, want named-sess-uuid", got["pts/5"].SessionID)
	}
	if got["pts/6"].SessionID != "" {
		t.Errorf("pts/6 SessionID = %q, want empty (no registry record)", got["pts/6"].SessionID)
	}
}

// A record written before the process started belongs to an earlier process
// that held the same pid, and must not be trusted.
func TestProcsIgnoresRegistryRecordFromAReusedPID(t *testing.T) {
	ps := " 4321 pts/3     10:00 claude\n"
	f := &iexec.FakeRunner{Responses: map[string]iexec.FakeResponse{
		"ps -axo pid,tty,etime,command": {Stdout: []byte(ps)},
	}}
	now := time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)
	sessions := t.TempDir()
	writeRegistry(t, sessions, 4321, "dead-process-sess", now.Add(-24*time.Hour))

	r := New(f, "/Users/x/.claude/projects", sessions)
	r.now = func() time.Time { return now }
	got, err := r.Procs(context.Background())
	if err != nil {
		t.Fatalf("Procs: %v", err)
	}
	if got["pts/3"].SessionID != "" {
		t.Fatalf("SessionID = %q, want empty (record predates the process)", got["pts/3"].SessionID)
	}
}

func TestProcsFailsOnMalformedRegistryRecord(t *testing.T) {
	ps := " 4321 pts/3     10:00 claude\n"
	f := &iexec.FakeRunner{Responses: map[string]iexec.FakeResponse{
		"ps -axo pid,tty,etime,command": {Stdout: []byte(ps)},
	}}
	sessions := t.TempDir()
	writeFile(t, filepath.Join(sessions, "4321.json"), "{not json")

	r := New(f, "/Users/x/.claude/projects", sessions)
	if _, err := r.Procs(context.Background()); err == nil {
		t.Fatal("Procs succeeded on a malformed registry record, want error")
	}
}

func TestResumeIDFromArgvForms(t *testing.T) {
	cases := map[string]string{
		"claude --resume abc":                "abc",
		"claude --resume=abc":                "abc",
		"claude -r abc":                      "abc",
		"claude --resume":                    "",
		"claude --resume --model opus":       "",
		"claude -r --verbose":                "",
		"claude --resume abc --fork-session": "", // running a new forked session, not abc
		"claude --fork-session -r abc":       "",
	}
	for cmd, want := range cases {
		if got := resumeIDFromArgv(cmd); got != want {
			t.Errorf("resumeIDFromArgv(%q) = %q, want %q", cmd, got, want)
		}
	}
}

// Reproduces a pane that resumed an old session through the picker: its own
// transcript predates the process, so start-time matching alone handed it a
// newer, unrelated transcript from the same cwd. The registry must win.
func TestAssignPrefersRegistrySessionOverStartTimeMatching(t *testing.T) {
	root := t.TempDir()
	cwd := "/Volumes/proj"
	projectDir := filepath.Join(root, SlugFor(cwd))
	mkdirAll(t, projectDir)

	base := time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)
	writeTranscript(t, projectDir, "picked-old-sess", base.Add(-30*24*time.Hour))
	writeTranscript(t, projectDir, "unrelated-newer-sess", base.Add(time.Hour))

	r := New(nil, root, t.TempDir())
	got := r.Assign([]PaneProc{{PaneID: 1, CWD: cwd, SessionID: "picked-old-sess", StartedAt: base}})

	if got[1].SessionID != "picked-old-sess" {
		t.Fatalf("SessionID = %q, want picked-old-sess", got[1].SessionID)
	}
}

// The registry reflects the live session, so it outranks a stale argv hint
// (e.g. the pane ran `claude --resume X` and later switched with /resume).
func TestAssignPrefersRegistrySessionOverArgvHint(t *testing.T) {
	root := t.TempDir()
	cwd := "/Volumes/proj"
	projectDir := filepath.Join(root, SlugFor(cwd))
	mkdirAll(t, projectDir)

	base := time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)
	writeTranscript(t, projectDir, "argv-sess", base.Add(-time.Hour))
	writeTranscript(t, projectDir, "live-sess", base.Add(-2*time.Hour))

	r := New(nil, root, t.TempDir())
	got := r.Assign([]PaneProc{{PaneID: 1, CWD: cwd, ResumeID: "argv-sess", SessionID: "live-sess", StartedAt: base}})

	if got[1].SessionID != "live-sess" {
		t.Fatalf("SessionID = %q, want live-sess", got[1].SessionID)
	}
}

// A registry session whose transcript is absent (e.g. a fresh session that
// has not written one yet) falls through to the next signal.
func TestAssignFallsBackWhenRegistryTranscriptMissing(t *testing.T) {
	root := t.TempDir()
	cwd := "/Volumes/proj"
	projectDir := filepath.Join(root, SlugFor(cwd))
	mkdirAll(t, projectDir)

	base := time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)
	writeTranscript(t, projectDir, "argv-sess", base.Add(-time.Hour))

	r := New(nil, root, t.TempDir())
	got := r.Assign([]PaneProc{{PaneID: 1, CWD: cwd, ResumeID: "argv-sess", SessionID: "not-on-disk", StartedAt: base}})

	if got[1].SessionID != "argv-sess" {
		t.Fatalf("SessionID = %q, want argv-sess", got[1].SessionID)
	}
}
