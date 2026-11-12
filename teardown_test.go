package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Once a task is back, teardown removes kant's copy of it: the worktree, its
// branch, ferry's record and refs, and what kant's teardown command removes,
// given the worktree's path. The laptop's worktree stays.
func TestTeardownRemovesTheTaskFromHost(t *testing.T) {
	w := newWorld(t)
	wt := w.laptopTask()
	task := taskKey("src/app-wt/fix-auth")
	w.mustFerry(wt, "to", "kant", "--yes", "--skip-scripts", "--no-launch")
	w.mustFerry(w.laptop, "from", "kant", "fix-auth", "--yes", "--skip-scripts")
	ran := filepath.Join(w.root, "teardown-ran")
	script := filepath.Join(w.root, "box-rm")
	writeFile(script, "#!/bin/sh\necho \"$@\" > "+ran+"\n")
	w.write(w.kant, ".config/ferry/teardown", "# the sandbox's half\n"+script+" --dir\n")

	out := w.mustFerry(wt, "teardown", "kant")
	t.Log(out)
	kapp, kwt := filepath.Join(w.kant, "src", "app"), filepath.Join(w.kant, "src", "app-wt", "fix-auth")
	if _, err := os.Stat(kwt); !os.IsNotExist(err) {
		t.Errorf("kant's worktree is still there: %v", err)
	}
	if got, _ := git(kapp, "branch", "--list", "fix-auth"); got != "" {
		t.Errorf("kant still has branch fix-auth")
	}
	if got, _ := git(kapp, "for-each-ref", "refs/ferry/", "refs/ferry-sent/"); got != "" {
		t.Errorf("kant still has ferry refs:\n%s", got)
	}
	if _, err := os.Stat(filepath.Join(w.kant, ".local/state/ferry/tasks", task+".json")); !os.IsNotExist(err) {
		t.Errorf("kant's record is still there")
	}
	if got := strings.TrimSpace(w.read(w.root, "teardown-ran")); got != "--dir "+kwt {
		t.Errorf("teardown command got %q, want %q", got, "--dir "+kwt)
	}
	if _, err := os.Stat(filepath.Join(wt, "unpushed.txt")); err != nil {
		t.Errorf("the laptop's worktree must stay: %v", err)
	}
	if got, _ := git(filepath.Join(w.laptop, "src", "app"), "for-each-ref", "refs/ferry/"+task+"/"); got != "" {
		t.Errorf("the laptop still has ferry's refs for the task:\n%s", got)
	}
}

// While kant owns the task, tearing it down there would throw away the only
// copy of its newest work.
func TestTeardownRefusesWhileHostOwnsTheTask(t *testing.T) {
	w := newWorld(t)
	wt := w.laptopTask()
	w.mustFerry(wt, "to", "kant", "--yes", "--skip-scripts", "--no-launch")
	out, err := w.ferry(wt, "teardown", "kant")
	if err == nil || !strings.Contains(out, "ferry from kant") {
		t.Fatalf("want a refusal pointing at ferry from, got err=%v\n%s", err, out)
	}
	if _, err := os.Stat(filepath.Join(w.kant, "src", "app-wt", "fix-auth", "unpushed.txt")); err != nil {
		t.Fatalf("kant's copy must survive the refusal: %v", err)
	}
}

// Work on kant the laptop never got stops teardown, unless --force.
func TestTeardownRefusesWorkTheLaptopLacks(t *testing.T) {
	w := newWorld(t)
	wt := w.laptopTask()
	w.mustFerry(wt, "to", "kant", "--yes", "--skip-scripts", "--no-launch")
	w.mustFerry(w.laptop, "from", "kant", "fix-auth", "--yes", "--skip-scripts")
	kwt := filepath.Join(w.kant, "src", "app-wt", "fix-auth")
	w.write(kwt, "stray.txt", "edited on kant after handing back\n")

	out, err := w.ferry(wt, "teardown", "kant")
	if err == nil || !strings.Contains(out, "stray.txt") {
		t.Fatalf("want a refusal naming stray.txt, got err=%v\n%s", err, out)
	}
	if w.read(kwt, "stray.txt") == "" {
		t.Fatal("the stray edit must survive the refusal")
	}
	w.mustFerry(wt, "teardown", "kant", "--force")
	if _, err := os.Stat(kwt); !os.IsNotExist(err) {
		t.Fatalf("--force must remove kant's worktree: %v", err)
	}
}

// A teardown command that fails (a sandbox holding work that never came back,
// say) stops teardown before anything else on kant is removed.
func TestTeardownStopsWhenTheTeardownCommandFails(t *testing.T) {
	w := newWorld(t)
	wt := w.laptopTask()
	w.mustFerry(wt, "to", "kant", "--yes", "--skip-scripts", "--no-launch")
	w.mustFerry(w.laptop, "from", "kant", "fix-auth", "--yes", "--skip-scripts")
	script := filepath.Join(w.root, "box-rm")
	writeFile(script, "#!/bin/sh\necho 'box holds work that did not come back' >&2\nexit 1\n")
	w.write(w.kant, ".config/ferry/teardown", script+" --dir\n")

	out, err := w.ferry(wt, "teardown", "kant")
	if err == nil || !strings.Contains(out, "did not come back") {
		t.Fatalf("want the teardown command's refusal, got err=%v\n%s", err, out)
	}
	if _, err := os.Stat(filepath.Join(w.kant, "src", "app-wt", "fix-auth")); err != nil {
		t.Fatalf("kant's worktree must stay when the teardown command fails: %v", err)
	}
}
