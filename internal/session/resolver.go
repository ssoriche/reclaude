// Package session resolves which Claude session a WezTerm pane is running.
//
// Claude does not hold its session .jsonl file open, so the session cannot be
// found via lsof, nor does it publish the session id in the process
// environment. Instead a session transcript lives at
// ~/.claude/projects/<slug>/<session-id>.jsonl, where <slug> is derived from
// the pane's cwd (see SlugFor).
//
// Attribution therefore rests on two signals. An explicit `claude --resume
// <id>` hint from argv names the session outright, and is authoritative when
// its file still exists (it can be stale). Otherwise the pane is running a
// session it started itself, so its transcript is the one whose first recorded
// timestamp falls just after the claude process started -- in practice within
// a couple of seconds of it.
//
// Attribution is done for all panes at once (see Assign) rather than one pane
// at a time, because the answer for one pane constrains the answer for its
// neighbours: two panes sharing a cwd are necessarily running two different
// sessions, and resolving them independently would hand both the same one.
package session

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	iexec "github.com/ssoriche/reclaude/internal/exec"
)

// startSlack absorbs the sub-second truncation in ps elapsed times, so a
// transcript stamped a moment before its own pane's computed start time is
// still considered a candidate for that pane.
const startSlack = 5 * time.Second

// headScanLimit bounds how many records are read from the head of a transcript
// while looking for its first timestamp. Claude writes a short untimestamped
// header (last-prompt, mode, permission-mode) before the first stamped record.
const headScanLimit = 64

// ClaudeSession identifies a resumable Claude session.
type ClaudeSession struct {
	SessionID  string
	ProjectDir string
}

// Proc is a running claude process, keyed by tty.
type Proc struct {
	ResumeID  string    // from --resume <id>/--resume=<id> in argv, or "" if absent.
	StartedAt time.Time // when the process started; zero if ps gave no usable elapsed time.
}

// PaneProc is one claude-running pane awaiting a session assignment.
type PaneProc struct {
	PaneID    int
	CWD       string
	ResumeID  string
	StartedAt time.Time
}

// Resolver builds tty -> Proc maps and attributes sessions on disk to panes.
type Resolver struct {
	run          iexec.Runner
	projectsRoot string // e.g. $HOME/.claude/projects
	now          func() time.Time
}

// New returns a Resolver. projectsRoot is injected for testability.
func New(r iexec.Runner, projectsRoot string) *Resolver {
	return &Resolver{run: r, projectsRoot: projectsRoot, now: time.Now}
}

// Procs returns a map from tty name (e.g. "ttys003") to Proc, for every
// running claude process.
func (r *Resolver) Procs(ctx context.Context) (map[string]Proc, error) {
	out, err := r.run.Run(ctx, "ps", "-axo", "pid,tty,etime,command")
	if err != nil {
		return nil, fmt.Errorf("session: ps: %w", err)
	}
	now := r.now()
	result := map[string]Proc{}
	for line := range strings.SplitSeq(string(out), "\n") {
		_, tty, elapsed, cmd, ok := parsePS(line)
		if !ok || !isClaude(cmd) {
			continue
		}
		p := Proc{ResumeID: resumeIDFromArgv(cmd)}
		if d, ok := parseElapsed(elapsed); ok {
			p.StartedAt = now.Add(-d)
		}
		result[tty] = p
	}
	return result, nil
}

// Assign attributes an on-disk session to each pane, guaranteeing that no
// session is handed to more than one pane. Panes with no plausible session are
// absent from the result: the caller should treat those as claude running
// without a recoverable transcript rather than substituting another pane's
// session.
//
// Explicit --resume hints are honoured as given, including the degenerate case
// of two panes naming the same session; that state is only reachable by
// launching claude that way, and healing it is not Assign's call to make.
func (r *Resolver) Assign(panes []PaneProc) map[int]ClaudeSession {
	// Group by project dir so panes in unrelated cwds never compete, and so each
	// directory's transcripts are read once.
	byDir := map[string][]PaneProc{}
	dirOrder := []string{}
	for _, p := range panes {
		dir := filepath.Join(r.projectsRoot, SlugFor(p.CWD))
		if _, seen := byDir[dir]; !seen {
			dirOrder = append(dirOrder, dir)
		}
		byDir[dir] = append(byDir[dir], p)
	}

	out := make(map[int]ClaudeSession, len(panes))
	for _, dir := range dirOrder {
		assignDir(dir, byDir[dir], out)
	}
	return out
}

// assignDir resolves the panes sharing one project dir, writing results into out.
func assignDir(projectDir string, panes []PaneProc, out map[int]ClaudeSession) {
	claimed := map[string]bool{}
	unhinted := make([]PaneProc, 0, len(panes))

	// An explicit hint names the session outright. Claim it first so start-time
	// matching cannot hand the same session to a neighbouring pane.
	for _, p := range panes {
		if p.ResumeID == "" {
			unhinted = append(unhinted, p)
			continue
		}
		info, err := os.Stat(filepath.Join(projectDir, p.ResumeID+".jsonl"))
		if err != nil || !info.Mode().IsRegular() {
			unhinted = append(unhinted, p) // stale hint; fall back to matching
			continue
		}
		out[p.PaneID] = ClaudeSession{SessionID: p.ResumeID, ProjectDir: projectDir}
		claimed[p.ResumeID] = true
	}
	if len(unhinted) == 0 {
		return
	}

	// Walk transcripts oldest-first, giving each to the latest-started pane that
	// already existed when that session began. A pane cannot be running a session
	// that predates it, which keeps stale conversations from being resurrected;
	// going oldest-first stops an older pane whose own transcript is gone from
	// swallowing a newer pane's session.
	for _, tr := range readTranscripts(projectDir) {
		if claimed[tr.sessionID] {
			continue
		}
		best := -1
		for i, p := range unhinted {
			if _, taken := out[p.PaneID]; taken {
				continue
			}
			if p.StartedAt.After(tr.startedAt.Add(startSlack)) {
				continue
			}
			if best < 0 || betterMatch(p, unhinted[best]) {
				best = i
			}
		}
		if best < 0 {
			continue
		}
		out[unhinted[best].PaneID] = ClaudeSession{SessionID: tr.sessionID, ProjectDir: projectDir}
		claimed[tr.sessionID] = true
	}
}

// betterMatch reports whether p is a closer owner for a transcript than cur:
// the later-started pane wins. Panes restored together share a start time to
// the second, so pane id breaks the tie to keep assignment deterministic --
// which pane of such a group gets which session is not recoverable.
func betterMatch(p, cur PaneProc) bool {
	if p.StartedAt.Equal(cur.StartedAt) {
		return p.PaneID < cur.PaneID
	}
	return p.StartedAt.After(cur.StartedAt)
}

// transcript is one top-level session transcript and when its session began.
type transcript struct {
	sessionID string
	startedAt time.Time
}

// readTranscripts returns projectDir's top-level transcripts that carry a
// usable start timestamp, oldest session first. Subdirectories (e.g.
// <session-id>/subagents/) are ignored: those are not resumable sessions.
func readTranscripts(projectDir string) []transcript {
	entries, err := os.ReadDir(projectDir)
	if err != nil {
		return nil
	}
	out := make([]transcript, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		started, ok := sessionStartedAt(filepath.Join(projectDir, e.Name()))
		if !ok {
			continue
		}
		out = append(out, transcript{
			sessionID: strings.TrimSuffix(e.Name(), ".jsonl"),
			startedAt: started,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].startedAt.Equal(out[j].startedAt) {
			return out[i].sessionID < out[j].sessionID
		}
		return out[i].startedAt.Before(out[j].startedAt)
	})
	return out
}

// sessionStartedAt returns the first timestamp recorded in a transcript, which
// marks when the session began. Reading the head of the file is bounded: only
// the records before the first timestamp are of interest, and transcripts grow
// to many megabytes.
func sessionStartedAt(path string) (time.Time, bool) {
	f, err := os.Open(path)
	if err != nil {
		return time.Time{}, false
	}
	defer f.Close()

	dec := json.NewDecoder(f)
	var rec struct {
		Timestamp string `json:"timestamp"`
	}
	for range headScanLimit {
		rec.Timestamp = ""
		if err := dec.Decode(&rec); err != nil {
			return time.Time{}, false // malformed or exhausted before any timestamp
		}
		if rec.Timestamp == "" {
			continue
		}
		if ts, err := time.Parse(time.RFC3339, rec.Timestamp); err == nil {
			return ts, true
		}
	}
	return time.Time{}, false
}

// SlugFor returns the ~/.claude/projects subdirectory name for cwd: every
// character that is not an ASCII letter or digit is replaced with '-'.
func SlugFor(cwd string) string {
	var b strings.Builder
	b.Grow(len(cwd))
	for _, c := range cwd {
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') {
			b.WriteRune(c)
		} else {
			b.WriteByte('-')
		}
	}
	return b.String()
}

// parsePS splits a `ps -axo pid,tty,etime,command` line into its columns.
func parsePS(line string) (pid, tty, elapsed, cmd string, ok bool) {
	fields := strings.Fields(line)
	if len(fields) < 4 {
		return "", "", "", "", false
	}
	if _, err := strconv.Atoi(fields[0]); err != nil {
		return "", "", "", "", false // header or malformed
	}
	pid = fields[0]
	tty = fields[1]
	elapsed = fields[2]
	cmd = strings.Join(fields[3:], " ")
	return pid, tty, elapsed, cmd, true
}

// parseElapsed parses a ps ELAPSED field, which is "[[dd-]hh:]mm:ss".
func parseElapsed(s string) (time.Duration, bool) {
	var days int
	if before, after, found := strings.Cut(s, "-"); found {
		n, err := strconv.Atoi(before)
		if err != nil {
			return 0, false
		}
		days, s = n, after
	}
	parts := strings.Split(s, ":")
	if len(parts) < 2 || len(parts) > 3 {
		return 0, false
	}
	vals := make([]int, len(parts))
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			return 0, false
		}
		vals[i] = n
	}
	var hours int
	if len(vals) == 3 {
		hours, vals = vals[0], vals[1:]
	}
	return time.Duration(days)*24*time.Hour +
		time.Duration(hours)*time.Hour +
		time.Duration(vals[0])*time.Minute +
		time.Duration(vals[1])*time.Second, true
}

// isClaude reports whether the command is a claude process (exact binary name,
// not merely containing "claude" in a path/arg).
func isClaude(cmd string) bool {
	fields := strings.Fields(cmd)
	if len(fields) == 0 {
		return false
	}
	return filepath.Base(fields[0]) == "claude"
}

// resumeIDFromArgv extracts the session id from `--resume <id>` or
// `--resume=<id>` in a claude command line. Returns "" if absent.
func resumeIDFromArgv(cmd string) string {
	fields := strings.Fields(cmd)
	for i, f := range fields {
		if id, ok := strings.CutPrefix(f, "--resume="); ok {
			return id
		}
		if f == "--resume" && i+1 < len(fields) {
			return fields[i+1]
		}
	}
	return ""
}
