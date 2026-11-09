package main

// Claude Code worktree sessions: the ones `claude --worktree NAME` or the
// EnterWorktree tool start. Claude records the worktree in the transcript as
// {"type":"worktree-state","worktreeSession":{"worktreePath": ...}} and
// writes "worktreeSession": null when the session leaves it. How the session
// ended decides where the transcript is and whether the worktree survived:
//
//   - /exit with a clean worktree and no commits: the worktree is deleted.
//   - /exit then Keep, or ExitWorktree keep: the worktree stays, and the
//     transcript moves to the project folder of the directory the session
//     came from (the primary checkout), ending in a null worktree-state.
//   - SIGTERM, or closing the pane: the worktree stays, git-locked by the
//     dead pid, and the transcript stays in the worktree's folder with the
//     worktree-state still set.
//
// A resume with the worktree-state still set makes the session a worktree
// session again, and its next /exit deletes a clean worktree or asks. So
// ferry finds the transcript wherever it is, resumes it in the worktree, and
// ends the worktree-state in the copy it sends, which resumes as a plain
// session. test/liveness.sh rechecks these behaviours.

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// claudeTranscript is what ferry reads from a Claude transcript to place it.
type claudeTranscript struct {
	firstCwd string
	lastCwd  string
	// worktree is the last Claude worktree the session was in, "" if none;
	// inWorktree says the session hasn't left it.
	worktree   string
	inWorktree bool
}

func readClaudeTranscript(path string) (claudeTranscript, error) {
	var t claudeTranscript
	err := scanJSONL(path, func(m map[string]any) bool {
		if cwd, _ := m["cwd"].(string); cwd != "" {
			if t.firstCwd == "" {
				t.firstCwd = cwd
			}
			t.lastCwd = cwd
		}
		if p, ok := worktreeState(m); ok && p != "" {
			t.worktree, t.inWorktree = p, true
		} else if ok {
			t.inWorktree = false
		}
		return true
	})
	return t, err
}

// worktreeState reads a worktree-state record: ok says m is one, and path is
// the worktree it enters, "" when it leaves.
func worktreeState(m map[string]any) (path string, ok bool) {
	if m["type"] != "worktree-state" {
		return "", false
	}
	ws, _ := m["worktreeSession"].(map[string]any)
	path, _ = ws["worktreePath"].(string)
	return path, true
}

// resumeDir is where the session resumes. That's the Claude worktree if the
// session is still in it or was working there when it ended. A session that
// left its worktree and went on working elsewhere resumes where it went,
// since Claude refiled the transcript there. Any other session resumes where
// it started.
func (t claudeTranscript) resumeDir() string {
	switch {
	case t.worktree == "":
		return t.firstCwd
	case t.inWorktree || t.lastCwd == "" || within(t.lastCwd, t.worktree):
		return t.worktree
	}
	return t.lastCwd
}

// worktreeTranscripts lists transcripts outside the worktree's own project
// folder that name dir as their Claude worktree. Keep moves a transcript to
// the folder of the directory the session came from, which is in the same
// repo, so only the repo's folders are searched, and only transcripts
// written since the worktree was made.
func (l *Local) worktreeTranscripts(dir string) []string {
	gitFile, err := os.Stat(filepath.Join(dir, ".git"))
	if err != nil || gitFile.IsDir() { // only linked worktrees have a .git file
		return nil
	}
	common, err := git(dir, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return nil
	}
	// git resolves symlinks; Claude's paths, like dir, are under home as is.
	home, _ := filepath.EvalSymlinks(l.Home)
	resolved, _ := filepath.EvalSymlinks(filepath.Dir(common))
	repoRel, err := filepath.Rel(home, resolved)
	if err != nil || strings.HasPrefix(repoRel, "..") {
		return nil
	}
	repo := l.abs(repoRel)
	made := gitFile.ModTime()
	own := claudeSlug(dir)
	needle, _ := json.Marshal(dir)
	needle = append([]byte(`"worktreePath":`), needle...)
	folders, _ := filepath.Glob(filepath.Join(l.Home, ".claude", "projects", claudeSlug(repo)+"*"))
	var out []string
	for _, f := range folders {
		if filepath.Base(f) == own {
			continue
		}
		files, _ := filepath.Glob(filepath.Join(f, "*.jsonl"))
		for _, p := range files {
			if st, err := os.Stat(p); err != nil || st.ModTime().Before(made) {
				continue
			}
			if data, err := os.ReadFile(p); err == nil && bytes.Contains(data, needle) {
				out = append(out, p)
			}
		}
	}
	return out
}

// leaveClaudeWorktree appends a null worktree-state to a transcript still in
// a Claude worktree, so it resumes as a plain session in that directory. It
// decodes every line, as readClaudeTranscript does: a byte search would miss
// a record whose JSON is spelled with escapes.
func leaveClaudeWorktree(transcript []byte, id string) []byte {
	in := false
	for line := range bytes.Lines(transcript) {
		var m map[string]any
		if json.Unmarshal(line, &m) != nil {
			continue
		}
		if p, ok := worktreeState(m); ok {
			in = p != ""
		}
	}
	if !in {
		return transcript
	}
	rec, _ := json.Marshal(map[string]any{"type": "worktree-state", "worktreeSession": nil, "sessionId": id})
	out := bytes.Clone(transcript)
	if len(out) > 0 && out[len(out)-1] != '\n' {
		out = append(out, '\n')
	}
	return append(append(out, rec...), '\n')
}

var claudeLock = regexp.MustCompile(`^claude session .* \(pid ([0-9]+) `)

// unlockDeadClaude releases the git lock Claude puts on its worktree when the
// Claude that took it is gone, as after SIGTERM. Otherwise `git worktree
// remove` refuses the worktree for good. It reports whether it unlocked.
func unlockDeadClaude(dir string) bool {
	common, err := git(dir, "rev-parse", "--path-format=absolute", "--git-dir")
	if err != nil {
		return false
	}
	reason, err := os.ReadFile(filepath.Join(common, "locked"))
	if err != nil {
		return false
	}
	m := claudeLock.FindStringSubmatch(strings.TrimSpace(string(reason)))
	if m == nil {
		return false
	}
	if pid, _ := strconv.Atoi(m[1]); pidAlive(pid) {
		return false
	}
	_, err = git(dir, "worktree", "unlock", dir)
	return err == nil
}
