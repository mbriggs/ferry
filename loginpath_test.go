package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// Tailscale SSH runs the remote command with a bare PATH. ferry remote asks
// the login shell for the real one, so a claude installed through mise is
// still found, even when the profile prints noise.
func TestRemoteFindsToolsOnTheLoginShellPath(t *testing.T) {
	dir := t.TempDir()
	tools := filepath.Join(dir, "mise-bin")
	writeExec(t, filepath.Join(tools, "claude"), "#!/bin/sh\necho '2.9.1 (Claude Code)'\n")
	shell := filepath.Join(dir, "loginsh")
	writeExec(t, shell, "#!/bin/sh\necho 'welcome to kant'\nPATH="+tools+":$PATH\nexport PATH\nexec /bin/sh -c \"$2\"\n")

	cmd := exec.Command(ferryBin, "remote", "info")
	cmd.Env = []string{"HOME=" + dir, "PATH=/usr/bin:/bin", "SHELL=" + shell}
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("remote info: %v", err)
	}
	var info Info
	if err := json.Unmarshal(out, &info); err != nil {
		t.Fatal(err)
	}
	if info.Claude != "2.9.1" {
		t.Fatalf("claude version = %q, want 2.9.1 from the login shell's PATH", info.Claude)
	}
}

func writeExec(t *testing.T, p, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
}
