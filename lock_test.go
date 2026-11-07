package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSecondHandoffOfTheSameTaskIsRefused(t *testing.T) {
	w := newWorld(t)
	wt := w.laptopTask()
	started, release := filepath.Join(w.root, "started"), filepath.Join(w.root, "release")
	hook := strings.Replace(dehydrateScript, "set -e\n", "set -e\ntouch "+started+"\nfor i in $(seq 100); do test -f "+release+" && break; sleep 0.1; done\n", 1)
	w.write(wt, ".ferry/dehydrate", hook)
	w.approve(w.laptop, hook)

	first := exec.Command(ferryBin, "to", "kant", "--yes", "--no-launch")
	first.Dir = wt
	first.Env = append(os.Environ(), "HOME="+w.laptop)
	var out bytes.Buffer
	first.Stdout, first.Stderr = &out, &out
	if err := first.Start(); err != nil {
		t.Fatal(err)
	}
	for i := 0; !exists(started); i++ {
		if i > 100 {
			first.Process.Kill()
			t.Fatalf("first handoff never reached dehydrate:\n%s", out.String())
		}
		time.Sleep(50 * time.Millisecond)
	}

	second, err := w.ferry(wt, "to", "kant", "--yes", "--no-launch")
	if err == nil || !strings.Contains(second, "another ferry") {
		t.Fatalf("want the second run refused, got err=%v\n%s", err, second)
	}
	w.write(w.root, "release", "")
	if err := first.Wait(); err != nil {
		t.Fatalf("first handoff failed: %v\n%s", err, out.String())
	}
	if exists(filepath.Join(w.kant, ".local/state/ferry/tasks", taskKey("src/app-wt/fix-auth")+".lock")) {
		t.Fatal("the lock outlived the handoff")
	}
}

func TestExpiredLockIsTakenOver(t *testing.T) {
	l := &Local{Home: t.TempDir()}
	if err := l.Lock(LockReq{Task: "x", Token: "a", Holder: "dead", TTL: 1}); err != nil {
		t.Fatal(err)
	}
	if err := l.Lock(LockReq{Task: "x", Token: "b", Holder: "new", TTL: 60}); err == nil {
		t.Fatal("took a live lock")
	}
	time.Sleep(1100 * time.Millisecond)
	if err := l.Lock(LockReq{Task: "x", Token: "b", Holder: "new", TTL: 60}); err != nil {
		t.Fatalf("expired lock wasn't taken over: %v", err)
	}
}
