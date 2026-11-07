package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A handoff must never discard work it wasn't handed, and rerunning the same
// command after an interruption must finish the job.

func TestRefusesToDropCommitsOnACleanReceiver(t *testing.T) {
	w := newWorld(t)
	wt := w.laptopTask()
	w.mustFerry(wt, "to", "kant", "--yes", "--no-launch")
	w.mustFerry(w.laptop, "from", "kant", "fix-auth", "--yes")
	kwt := filepath.Join(w.kant, "src", "app-wt", "fix-auth")
	w.write(kwt, "stray.txt", "committed on kant after handing off\n")
	w.git(kwt, "add", "-A")
	w.git(kwt, "commit", "-qm", "stray commit")
	stray := w.git(kwt, "rev-parse", "HEAD")

	out, err := w.ferry(wt, "to", "kant", "--yes", "--no-launch")
	if err == nil || !strings.Contains(out, "stray commit") {
		t.Fatalf("want a refusal listing the stray commit, got err=%v\n%s", err, out)
	}
	if !strings.Contains(w.git(kwt, "branch", "--contains", stray), "fix-auth") {
		t.Fatal("the kant commit was dropped from its branch")
	}
}

func TestRerunConvergesAfterTheSourceChanged(t *testing.T) {
	w := newWorld(t)
	wt := w.laptopTask()
	w.write(w.kant, ".claude/projects", "not a directory, so installing the session fails")
	if out, err := w.ferry(wt, "to", "kant", "--yes", "--no-launch"); err == nil {
		t.Fatalf("first run should fail:\n%s", out)
	}
	os.Remove(filepath.Join(w.kant, ".claude/projects"))
	// The laptop still owns the task, so work continues there.
	w.write(wt, "more.txt", "kept working on the laptop\n")
	want, _ := fingerprint(wt)
	w.mustFerry(wt, "to", "kant", "--yes", "--no-launch")
	if got, _ := fingerprint(filepath.Join(w.kant, "src/app-wt/fix-auth")); got != want {
		t.Fatalf("kant has a stale tree\nwant %s\ngot  %s", want, got)
	}
}

func TestApplyRecognizesItsOwnPartialApply(t *testing.T) {
	w := newWorld(t)
	wt := w.laptopTask()
	laptop := &Local{Home: w.laptop}
	kant := &Local{Home: w.kant}
	task := taskKey("src/app-wt/fix-auth")
	snap, err := laptop.Snapshot(SnapshotReq{WtRel: "src/app-wt/fix-auth", Task: task})
	if err != nil {
		t.Fatal(err)
	}
	kant.InitRepo("src/app")
	w.git(wt, "push", "-q", filepath.Join(w.kant, "src/app"), "+refs/heads/*:refs/heads/*", "+refs/ferry/"+task+"/*:refs/ferry/"+task+"/*")
	kant.FinishRepo(FinishRepoReq{RepoRel: "src/app", Branch: "main"})

	// Simulate a crash after restoring files but before reading the index.
	kwt := filepath.Join(w.kant, "src/app-wt/fix-auth")
	w.git(filepath.Join(w.kant, "src/app"), "worktree", "add", "-q", "-B", "fix-auth", kwt, "refs/ferry/"+task+"/base")
	w.git(kwt, "restore", "--source=refs/ferry/"+task+"/wip", "--worktree", "--", ":/")

	if _, err := kant.Apply(ApplyReq{RepoRel: "src/app", WtRel: "src/app-wt/fix-auth", Branch: "fix-auth", Task: task, Expected: snap.Fingerprint, Previous: []string{snap.Fingerprint}}); err != nil {
		t.Fatalf("Apply refused its own partial state: %v", err)
	}
}

func TestRerunFinishesARepoLeftOnThePlaceholderBranch(t *testing.T) {
	w := newWorld(t)
	wt := w.laptopTask()
	// An earlier run created the repo and pushed branches, then died before
	// checking out the default branch.
	repo := filepath.Join(w.kant, "src/app")
	(&Local{Home: w.kant}).InitRepo("src/app")
	w.git(wt, "push", "-q", repo, "+refs/heads/*:refs/heads/*")

	w.mustFerry(wt, "to", "kant", "--yes", "--no-launch")
	if head := w.git(repo, "symbolic-ref", "--short", "HEAD"); head != "main" {
		t.Fatalf("kant's repo is on %q, want main", head)
	}
	if w.git(repo, "remote", "get-url", "origin") == "" {
		t.Fatal("origin was never set")
	}
}

func TestHandsOffIntoAnExistingClone(t *testing.T) {
	w := newWorld(t)
	wt := w.laptopTask()
	w.git(w.root, "clone", "-q", filepath.Join(w.root, "origin.git"), filepath.Join(w.kant, "src/app"))
	want, _ := fingerprint(wt)
	w.mustFerry(wt, "to", "kant", "--yes", "--no-launch")
	if got, _ := fingerprint(filepath.Join(w.kant, "src/app-wt/fix-auth")); got != want {
		t.Fatalf("worktree differs\nwant %s\ngot  %s", want, got)
	}
}
