package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGitOverSSHUsesFerrysOptions(t *testing.T) {
	w := newSerialWorld(t)
	wt := w.laptopTask()
	bin := filepath.Join(w.root, "bin")
	log := filepath.Join(w.root, "ssh.log")
	// A fake ssh that records its arguments and runs the remote command here.
	w.write(w.root, "bin/ssh", "#!/bin/sh\necho \"$@\" >> "+log+"\nwhile [ \"$1\" = -o ]; do shift 2; done\nshift\ncd "+w.kant+" && exec sh -c \"$*\"\n")
	os.Chmod(filepath.Join(bin, "ssh"), 0o755)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	kantRepo := filepath.Join(w.kant, "src/app")
	w.git(w.root, "init", "-q", "--bare", kantRepo)
	remote, err := NewRemote("kant", "fakehost", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := moveRefs(&Local{Home: w.laptop}, remote, "src/app", "+refs/heads/*:refs/heads/*"); err != nil {
		t.Fatal(err)
	}
	if w.git(kantRepo, "rev-parse", "fix-auth") != w.git(wt, "rev-parse", "fix-auth") {
		t.Fatal("push didn't arrive")
	}
	args := w.read(w.root, "ssh.log")
	for _, want := range []string{"ControlMaster=auto", "ServerAliveInterval=15", "BatchMode=yes", "fakehost"} {
		if !strings.Contains(args, want) {
			t.Fatalf("git's ssh ran without %s: %s", want, args)
		}
	}
}
