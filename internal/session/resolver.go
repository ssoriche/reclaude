// Package session resolves which Claude session a WezTerm pane is running.
//
// Claude does not hold its session .jsonl file open, so the session cannot be
// found via lsof. Instead a session transcript lives at
// ~/.claude/projects/<slug>/<session-id>.jsonl, where <slug> is derived from
// the pane's cwd (see SlugFor). The active session for a cwd is the
// most-recently-modified top-level *.jsonl in that directory; an explicit
// `claude --resume <id>` hint from argv is preferred when its file still
// exists, since it can be stale.
package session

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	iexec "github.com/ssoriche/reclaude/internal/exec"
)

// ClaudeSession identifies a resumable Claude session.
type ClaudeSession struct {
	SessionID  string
	ProjectDir string
}

// Proc is a running claude process's resume hint, keyed by tty.
type Proc struct {
	ResumeID string // from --resume <id>/--resume=<id> in argv, or "" if absent.
}

// Resolver builds tty -> Proc maps and locates sessions on disk.
type Resolver struct {
	run          iexec.Runner
	projectsRoot string // e.g. $HOME/.claude/projects
}

// New returns a Resolver. projectsRoot is injected for testability.
func New(r iexec.Runner, projectsRoot string) *Resolver {
	return &Resolver{run: r, projectsRoot: projectsRoot}
}

// Procs returns a map from tty name (e.g. "ttys003") to Proc, for every
// running claude process.
func (r *Resolver) Procs(ctx context.Context) (map[string]Proc, error) {
	out, err := r.run.Run(ctx, "ps", "-axo", "pid,tty,command")
	if err != nil {
		return nil, fmt.Errorf("session: ps: %w", err)
	}
	result := map[string]Proc{}
	for line := range strings.SplitSeq(string(out), "\n") {
		_, tty, cmd, ok := parsePS(line)
		if !ok || !isClaude(cmd) {
			continue
		}
		result[tty] = Proc{ResumeID: resumeIDFromArgv(cmd)}
	}
	return result, nil
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

// Locate finds the Claude session for cwd. If resumeID is non-empty and its
// .jsonl file exists in the project directory, that session is returned.
// Otherwise the newest top-level *.jsonl in the project directory is
// returned (subdirectories, e.g. <session-id>/subagents/, are ignored).
func (r *Resolver) Locate(cwd, resumeID string) (ClaudeSession, bool) {
	projectDir := filepath.Join(r.projectsRoot, SlugFor(cwd))

	if resumeID != "" {
		path := filepath.Join(projectDir, resumeID+".jsonl")
		if info, err := os.Stat(path); err == nil && info.Mode().IsRegular() {
			return ClaudeSession{SessionID: resumeID, ProjectDir: projectDir}, true
		}
	}

	entries, err := os.ReadDir(projectDir)
	if err != nil {
		return ClaudeSession{}, false
	}
	var newestName string
	var newestMod int64
	found := false
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		mod := info.ModTime().UnixNano()
		if !found || mod > newestMod {
			newestName = e.Name()
			newestMod = mod
			found = true
		}
	}
	if !found {
		return ClaudeSession{}, false
	}
	return ClaudeSession{
		SessionID:  strings.TrimSuffix(newestName, ".jsonl"),
		ProjectDir: projectDir,
	}, true
}

// parsePS splits a `ps -axo pid,tty,command` line into pid, tty, command.
func parsePS(line string) (pid, tty, cmd string, ok bool) {
	fields := strings.Fields(line)
	if len(fields) < 3 {
		return "", "", "", false
	}
	if _, err := strconv.Atoi(fields[0]); err != nil {
		return "", "", "", false // header or malformed
	}
	pid = fields[0]
	tty = fields[1]
	cmd = strings.Join(fields[2:], " ")
	return pid, tty, cmd, true
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
