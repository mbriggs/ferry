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
	// Refused says why the hook can't run whatever the answer
	// (checkInterpreter).
	Refused string `json:"refused,omitempty"`
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
	info := HookInfo{Exists: true, Path: p, SHA: sha, Content: string(data), Approved: l.approved(sha)}
	if err := l.checkInterpreter(data); err != nil {
		info.Refused = err.Error()
	}
	return info, nil
}

// checkInterpreter refuses a hook whose #! line names a file in this home,
// or a relative path, which runs from the worktree. Approval covers the
// hook's own bytes, so "#!./tools/interp" would run whatever the other
// machine put in tools/interp under a script that still reads as approved.
// "#!/bin/sh" and "#!/usr/bin/env python3" name nothing either machine's
// repos can change.
func (l *Local) checkInterpreter(data []byte) error {
	argv := hookArgv(data, "")
	for _, word := range argv[:len(argv)-1] {
		for _, w := range strings.Fields(word) {
			if !strings.Contains(w, "/") {
				continue
			}
			if !filepath.IsAbs(w) || l.inHome(w) {
				return fmt.Errorf("its #! line runs %s, a file the other machine can change; use an interpreter outside your home, like #!/bin/sh or #!/usr/bin/env NAME", w)
			}
		}
	}
	return nil
}

// inHome reports whether path is in this home by any name: as written, or
// through a symlink or a macOS firmlink like /System/Volumes/Data/Users/NAME,
// which names the same folder as /Users/NAME without sharing its prefix.
func (l *Local) inHome(path string) bool {
	path = filepath.Clean(path)
	if within(path, l.Home) {
		return true
	}
	paths := []string{path}
	if real, err := filepath.EvalSymlinks(path); err == nil && real != path {
		paths = append(paths, real)
	}
	for _, p := range paths {
		for dir := p; ; dir = filepath.Dir(dir) {
			if within(dir, l.Home) || sameDir(dir, l.Home) {
				return true
			}
			if dir == filepath.Dir(dir) {
				break
			}
		}
	}
	return false
}

func hashHook(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// forTerminal makes hook text safe to print for review: every control
// character except newline and tab is shown escaped, so a script can't use
// carriage returns or ANSI sequences to hide lines from the person approving.
func forTerminal(s string) string { return escapeControls(s, true) }

// nameForTerminal makes a file name from the other machine safe to print in
// a list: every control character is escaped, newline and tab included, so a
// name can't start a line that looks like ferry's own output.
func nameForTerminal(s string) string { return escapeControls(s, false) }

func escapeControls(s string, keepLines bool) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case keepLines && (r == '\n' || r == '\t'):
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
	if err := l.checkInterpreter(data); err != nil {
		return HookResult{}, fmt.Errorf("not running %s: %v", q.Name, err)
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
	argv := hookArgv(data, copy.Name())
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = root
	cmd.Env = append(toolEnv(),
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

// hookArgv is the command that runs the hook at path: its #! interpreter
// and the line's one optional argument, split as the kernel splits them, or
// /bin/sh when there's no #! line. Ferry runs the interpreter itself because
// path is a fresh copy, and macOS checks a file the first time it's exec'd,
// which adds about 150ms to every hook run.
func hookArgv(data []byte, path string) []string {
	line, _, _ := bytes.Cut(data, []byte("\n"))
	shebang, ok := bytes.CutPrefix(line, []byte("#!"))
	if !ok {
		return []string{"/bin/sh", path}
	}
	s := strings.TrimLeft(string(shebang), " \t")
	interp, arg := s, ""
	if i := strings.IndexAny(s, " \t"); i >= 0 {
		interp, arg = s[:i], strings.Trim(s[i:], " \t")
	}
	switch {
	case interp == "":
		return []string{path} // exec reports the bad #! line
	case arg == "":
		return []string{interp, path}
	}
	return []string{interp, arg, path}
}
