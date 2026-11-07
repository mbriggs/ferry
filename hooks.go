package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Project hooks (.ferry/dehydrate and .ferry/restore) come from the repo,
// which an agent on the other machine can edit. So a hook only runs when its
// exact contents have been approved on the machine that runs it.

type HookReq struct {
	WtRel     string `json:"wt_rel"`
	RepoRel   string `json:"repo_rel"`
	Name      string `json:"name"`
	Task      string `json:"task"`
	Direction string `json:"direction"`
	SHA       string `json:"sha,omitempty"`
}

type HookInfo struct {
	Exists   bool   `json:"exists"`
	Path     string `json:"path"`
	SHA      string `json:"sha"`
	Content  string `json:"content"`
	Approved bool   `json:"approved"`
}

type HookResult struct {
	OK      bool   `json:"ok"`
	Summary string `json:"summary"`
	Output  string `json:"output"`
}

// hookPath finds the hook in the worktree, or in ~/.config/ferry/projects/<repo>/
// for repos where you'd rather not commit them.
func (l *Local) hookPath(q HookReq) string {
	for _, p := range []string{
		filepath.Join(l.abs(q.WtRel), ".ferry", q.Name),
		l.configDir("projects", filepath.Base(q.RepoRel), q.Name),
	} {
		if st, err := os.Stat(p); err == nil && st.Mode().IsRegular() {
			return p
		}
	}
	return ""
}

func (l *Local) HookInfo(q HookReq) (HookInfo, error) {
	p := l.hookPath(q)
	if p == "" {
		return HookInfo{}, nil
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return HookInfo{}, err
	}
	sum := sha256.Sum256(data)
	sha := hex.EncodeToString(sum[:])
	content := string(data)
	if len(content) > 64<<10 {
		content = content[:64<<10] + "\n[truncated]"
	}
	return HookInfo{Exists: true, Path: p, SHA: sha, Content: content, Approved: l.approved(sha)}, nil
}

func (l *Local) approved(sha string) bool {
	data, err := os.ReadFile(l.configDir("approved"))
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == sha {
			return true
		}
	}
	return false
}

func (l *Local) Approve(sha string) error {
	if len(sha) != 64 || l.approved(sha) {
		return nil
	}
	if err := os.MkdirAll(l.configDir(), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(l.configDir("approved"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = fmt.Fprintln(f, sha)
	return err
}

// RunHook runs an approved hook. The caller passes the sha it showed the user;
// the hook runs only if the file still has that sha and it is approved here.
func (l *Local) RunHook(q HookReq) (HookResult, error) {
	info, err := l.HookInfo(q)
	if err != nil || !info.Exists {
		return HookResult{OK: true}, err
	}
	if info.SHA != q.SHA || !info.Approved {
		return HookResult{}, fmt.Errorf("%s changed or isn't approved; not running it", q.Name)
	}
	root := l.abs(q.WtRel)
	// Run through sh when the file isn't executable, rather than chmod it:
	// the exec bit is tracked, and ferry never changes the worktree.
	cmd := exec.Command(info.Path)
	if st, err := os.Stat(info.Path); err == nil && st.Mode().Perm()&0o111 == 0 {
		cmd = exec.Command("/bin/sh", info.Path)
	}
	cmd.Dir = root
	cmd.Env = append(os.Environ(),
		"FERRY_DIRECTION="+q.Direction,
		"FERRY_TASK="+q.Task,
		"FERRY_ROOT="+root,
		"FERRY_STATE="+filepath.Join(root, stateDirRel),
	)
	var stdout, all bytes.Buffer
	cmd.Stdout = &multi{&stdout, &all}
	cmd.Stderr = &all
	runErr := cmd.Run()
	res := HookResult{OK: runErr == nil, Summary: lastLine(stdout.String()), Output: tail(all.String(), 40)}
	return res, nil
}

type multi struct{ a, b *bytes.Buffer }

func (m *multi) Write(p []byte) (int, error) { m.a.Write(p); return m.b.Write(p) }

func tail(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
