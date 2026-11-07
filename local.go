package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

const version = "0.1.0"

// Local is this machine, rooted at a home directory. Every path ferry touches
// is relative to Home, so tests can run two Locals side by side.
type Local struct {
	Home string
}

func (l *Local) IsLocal() bool            { return true }
func (l *Local) GitURL(rel string) string { return l.abs(rel) }
func (l *Local) String() string           { return "here" }

func (l *Local) abs(rel string) string { return filepath.Join(l.Home, rel) }

func (l *Local) stateDir(parts ...string) string {
	return filepath.Join(append([]string{l.Home, ".local", "state", "ferry"}, parts...)...)
}

func (l *Local) configDir(parts ...string) string {
	return filepath.Join(append([]string{l.Home, ".config", "ferry"}, parts...)...)
}

// Info names this machine and reports agent versions. The name comes from
// ~/.config/ferry/name when present, so two test homes on one host differ.
func (l *Local) Info() (Info, error) {
	name := ""
	if b, err := os.ReadFile(l.configDir("name")); err == nil {
		name = strings.TrimSpace(string(b))
	}
	if name == "" {
		h, err := os.Hostname()
		if err != nil {
			return Info{}, err
		}
		name, _, _ = strings.Cut(h, ".")
	}
	_, rexErr := exec.LookPath("rex")
	return Info{
		Name:   name,
		Home:   l.Home,
		OS:     runtime.GOOS,
		Claude: toolVersion("claude", "--version"),
		Codex:  toolVersion("codex", "--version"),
		Rex:    rexErr == nil,
		Ferry:  version,
	}, nil
}

// toolVersion returns the first dotted version number a tool prints, or "".
func toolVersion(name string, args ...string) string {
	out, err := exec.Command(name, args...).Output()
	if err != nil {
		return ""
	}
	for _, f := range strings.Fields(string(out)) {
		if f != "" && f[0] >= '0' && f[0] <= '9' && strings.Contains(f, ".") {
			return f
		}
	}
	return ""
}

// run executes a command and returns trimmed stdout, folding stderr into errors.
func run(dir string, env []string, name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), env...)
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(errb.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("%s %s: %s", name, strings.Join(args, " "), msg)
	}
	return strings.TrimSpace(out.String()), nil
}

func exists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}

func isDir(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

// writeAtomic writes data next to path and renames it into place. If path is
// a symlink, the link's target is replaced and the link survives.
func writeAtomic(path string, data []byte, mode os.FileMode) error {
	if real, err := filepath.EvalSymlinks(path); err == nil {
		path = real
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".ferry-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
