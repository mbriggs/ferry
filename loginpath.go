package main

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Commands the laptop runs over ssh get a bare environment: Tailscale SSH
// runs them with a plain `bash -c`, which reads no startup files, so tools
// installed through mise or similar aren't on PATH. Hooks and the agent
// version check need the user's real PATH, so the far side asks a login
// shell for it, once per process, and puts those entries in front.
//
// Only `ferry remote` does this. On the laptop ferry is started from a
// terminal that already has the right PATH.
var (
	useLoginPath  bool
	loginPathOnce sync.Once
	loginPATH     string
)

const loginPathTimeout = 5 * time.Second

// toolEnv is the environment for hooks and agent tools.
func toolEnv() []string {
	env := os.Environ()
	if !useLoginPath {
		return env
	}
	loginPathOnce.Do(func() { loginPATH = mergePath(askLoginShell(), os.Getenv("PATH")) })
	if loginPATH == "" {
		return env
	}
	out := env[:0:0]
	for _, kv := range env {
		if !strings.HasPrefix(kv, "PATH=") {
			out = append(out, kv)
		}
	}
	return append(out, "PATH="+loginPATH)
}

// lookTool finds a tool on the toolEnv PATH.
func lookTool(name string) (string, error) {
	for _, kv := range toolEnv() {
		if p, ok := strings.CutPrefix(kv, "PATH="); ok {
			for _, dir := range filepath.SplitList(p) {
				f := filepath.Join(dir, name)
				if st, err := os.Stat(f); err == nil && !st.IsDir() && st.Mode()&0o111 != 0 {
					return f, nil
				}
			}
		}
	}
	return exec.LookPath(name)
}

// askLoginShell returns the PATH a login shell sets up, or "" if the shell
// fails or hangs. Markers fence the value off from anything the profile prints.
func askLoginShell() string {
	shell := os.Getenv("SHELL")
	if shell == "" {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), loginPathTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, shell, "-lc", `printf '\n@@ferry-path@@%s@@\n' "$PATH"`)
	cmd.Stdin = nil
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	_, rest, ok := bytes.Cut(out, []byte("@@ferry-path@@"))
	if !ok {
		return ""
	}
	p, _, ok := bytes.Cut(rest, []byte("@@"))
	if !ok {
		return ""
	}
	return string(p)
}

// mergePath puts first's entries ahead of second's, dropping repeats and
// empty entries.
func mergePath(first, second string) string {
	seen := map[string]bool{}
	var out []string
	for _, p := range []string{first, second} {
		for _, dir := range filepath.SplitList(p) {
			if dir != "" && !seen[dir] {
				seen[dir] = true
				out = append(out, dir)
			}
		}
	}
	return strings.Join(out, string(filepath.ListSeparator))
}
