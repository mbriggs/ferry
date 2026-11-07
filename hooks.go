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
	// TooLarge hooks can't be shown in full, so they can't be approved.
	TooLarge bool `json:"too_large,omitempty"`
}

// maxHookSize is the largest hook ferry will show for approval.
const maxHookSize = 64 << 10

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
	sha := hashHook(data)
	if len(data) > maxHookSize {
		return HookInfo{Exists: true, Path: p, SHA: sha, TooLarge: true, Approved: l.approved(sha)}, nil
	}
	return HookInfo{Exists: true, Path: p, SHA: sha, Content: string(data), Approved: l.approved(sha)}, nil
}

func hashHook(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// forTerminal makes hook text safe to print for review: every control
// character except newline and tab is shown escaped, so a script can't use
// carriage returns or ANSI sequences to hide lines from the person approving.
func forTerminal(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == '\n' || r == '\t':
			b.WriteRune(r)
		case r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0) || r == 0x2028 || r == 0x2029 || r == 0x202e:
			fmt.Fprintf(&b, "\\x{%x}", r)
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
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
	p := l.hookPath(q)
	if p == "" {
		return HookResult{OK: true}, nil
	}
	// Hash and run the same bytes: copy them to a private file first, so the
	// hook can't be swapped between the check and the exec.
	data, err := os.ReadFile(p)
	if err != nil {
		return HookResult{}, err
	}
	if hashHook(data) != q.SHA || !l.approved(q.SHA) || len(data) > maxHookSize {
		return HookResult{}, fmt.Errorf("%s changed or isn't approved; not running it", q.Name)
	}
	if err := os.MkdirAll(l.stateDir("run"), 0o700); err != nil {
		return HookResult{}, err
	}
	copy, err := os.CreateTemp(l.stateDir("run"), q.Name+"-*")
	if err != nil {
		return HookResult{}, err
	}
	defer os.Remove(copy.Name())
	if _, err := copy.Write(data); err != nil {
		copy.Close()
		return HookResult{}, err
	}
	if err := copy.Chmod(0o700); err != nil {
		copy.Close()
		return HookResult{}, err
	}
	if err := copy.Close(); err != nil {
		return HookResult{}, err
	}
	root := l.abs(q.WtRel)
	cmd := exec.Command(copy.Name())
	if !bytes.HasPrefix(data, []byte("#!")) {
		cmd = exec.Command("/bin/sh", copy.Name())
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
