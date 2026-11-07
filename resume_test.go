package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// A pull run from a terminal ends in the agent, started in this terminal in
// the worktree, instead of a command to copy.
func TestPullResumesInThisTerminal(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("uses util-linux script for a pseudo-terminal")
	}
	script, err := exec.LookPath("script")
	if err != nil {
		t.Skip("no script command")
	}
	w := newWorld(t)
	wt := w.laptopTask()
	w.mustFerry(wt, "to", "kant", "--yes", "--no-launch")

	bin := filepath.Join(w.root, "bin")
	ran := filepath.Join(w.root, "claude-ran")
	writeExec(t, filepath.Join(bin, "claude"), "#!/bin/sh\npwd > "+ran+"\nprintf '%s\\n' \"$@\" >> "+ran+"\n")

	cmd := exec.Command(script, "-qec", ferryBin+" from kant fix-auth --yes", "/dev/null")
	cmd.Dir = w.laptop
	cmd.Stdin = strings.NewReader("y\n") // restore on the laptop always asks
	cmd.Env = append(os.Environ(), "HOME="+w.laptop, "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("from: %v\n%s", err, out)
	}
	got, err := os.ReadFile(ran)
	if err != nil {
		t.Fatalf("claude wasn't started here:\n%s", out)
	}
	lines := strings.Split(string(got), "\n")
	if lines[0] != wt || lines[1] != "--resume" || lines[2] != sessionID {
		t.Fatalf("claude ran as %q", got)
	}
	if r := w.record(taskKey("src/app-wt/fix-auth")); r.Owner != "laptop" || r.State != stateOwned {
		t.Fatalf("the record wasn't finished before the agent started: %+v", r)
	}
}
