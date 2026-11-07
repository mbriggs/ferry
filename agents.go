package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// AgentInfo is a session an agent has loaded on this machine, and the git
// worktree it's working in, if any.
type AgentInfo struct {
	Session Session `json:"session"`
	Busy    bool    `json:"busy"`
	Task    *task   `json:"task,omitempty"` // nil outside a git worktree under ~/src
	Rex     string  `json:"rex,omitempty"`  // label of the Rex session it runs in
}

// Agents lists the Claude and Codex sessions that are running here. They're
// what `ferry from` offers to pull, and how a worktree that never moved
// gets found.
func (l *Local) Agents() ([]AgentInfo, error) {
	var out []AgentInfo
	seen := map[string]bool{}
	add := func(agent, id string, pid int) {
		if seen[id] || checkSessionID(id) != nil {
			return
		}
		s, err := l.FindSession(FindSessionReq{Agent: agent, ID: id})
		if err != nil || s == nil {
			return
		}
		seen[id] = true
		live, _ := l.Liveness(*s)
		a := AgentInfo{Session: *s, Busy: live.Busy}
		dir := s.WorkDir
		if dir == "" {
			dir = s.Cwd
		}
		if t, err := taskHere(l, dir); err == nil {
			a.Task = &t
		}
		if sess, _ := rexOfPID(pid); sess != "" {
			a.Rex = rexLabel(sess)
		}
		out = append(out, a)
	}
	files, _ := filepath.Glob(filepath.Join(l.Home, ".claude", "sessions", "*.json"))
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		var e struct {
			PID       int    `json:"pid"`
			SessionID string `json:"sessionId"`
		}
		if json.Unmarshal(data, &e) == nil && pidAlive(e.PID) {
			add(agentClaude, e.SessionID, e.PID)
		}
	}
	locks, _ := filepath.Glob(filepath.Join(l.Home, ".codex", "thread-writer-locks", "*.lock"))
	for _, f := range locks {
		if held, err := flockHeld(f); err == nil && held {
			add(agentCodex, strings.TrimSuffix(filepath.Base(f), ".lock"), 0)
		}
	}
	return out, nil
}

// Worktree resolves a path under ~/src on this machine to its task, for a
// worktree that has no record yet.
func (l *Local) Worktree(rel string) (task, error) {
	return taskHere(l, l.abs(filepath.Clean(rel)))
}

// rexOfPID returns the Rex session and pane a process was started in. Rex
// puts them in the environment of everything it runs, and an agent inherits
// them. Only Linux exposes another process's environment this way; elsewhere
// it returns "".
func rexOfPID(pid int) (session, block string) {
	if pid <= 0 {
		return "", ""
	}
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/environ")
	if err != nil {
		return "", ""
	}
	for _, kv := range strings.Split(string(data), "\x00") {
		if v, ok := strings.CutPrefix(kv, "REX_SESSION="); ok {
			session = v
		} else if v, ok := strings.CutPrefix(kv, "REX_BLOCK="); ok {
			block = v
		}
	}
	return session, block
}

// rexLabel returns a Rex session's label, or "".
func rexLabel(id string) string {
	out, err := run("", nil, "rex", "ls", "--json")
	if err != nil {
		return ""
	}
	var ls struct {
		Sessions []struct {
			SessionID string `json:"session_id"`
			Label     string `json:"label"`
		} `json:"sessions"`
	}
	if json.Unmarshal([]byte(out), &ls) != nil {
		return ""
	}
	for _, s := range ls.Sessions {
		if s.SessionID == id {
			return s.Label
		}
	}
	return ""
}

// within reports whether rel is dir or inside it (both relative to home).
func within(rel, dir string) bool {
	return rel == dir || strings.HasPrefix(rel, dir+"/")
}
