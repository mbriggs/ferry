package main

import (
	"bytes"
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
	first.Env = w.env(w.laptop)
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
}

func TestTaskLockIsExclusiveUntilReleased(t *testing.T) {
	l := &Local{Home: t.TempDir()}
	release, err := l.lockTask("src-app-abc123")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := l.lockTask("src-app-abc123"); err == nil {
		t.Fatal("took a held lock")
	}
	release()
	if _, err := l.lockTask("src-app-abc123"); err != nil {
		t.Fatalf("lock not released: %v", err)
	}
	if _, err := l.lockTask("../escape"); err == nil {
		t.Fatal("accepted a path as a task key")
	}
}

// A call left running on kant by a ferry that died holds the task's lock
// there, and the rerun's calls wait for it instead of interleaving with it.
func TestRemoteCallsWaitForAnEarlierCallOnTheTask(t *testing.T) {
	w := newWorld(t)
	wt := w.laptopTask()
	kant := &Local{Home: w.kant}
	release, err := kant.lockRemoteCall(taskKey("src/app-wt/fix-auth"))
	if err != nil {
		t.Fatal(err)
	}
	const held = 1500 * time.Millisecond
	time.AfterFunc(held, release)
	start := time.Now()
	w.mustFerry(wt, "to", "kant", "--yes", "--no-launch")
	if d := time.Since(start); d < held {
		t.Fatalf("the handoff finished in %v, before the earlier call released the lock", d)
	}
}
