package main

import (
	"os"
	"path/filepath"
	"testing"
)

// Nothing live moves until the receiver owns the task, and the sender's
// transcript is parked before the receiver's copy goes live.

// blockStaging makes the receiver's staging directory unwritable, so a
// handoff fails before its commit point.
func (w *world) blockStaging() func() {
	p := filepath.Join(w.kant, ".local/state/ferry/in")
	w.write(w.kant, ".local/state/ferry/in", "not a directory")
	return func() { os.Remove(p) }
}

func TestFailureBeforeCommitLeavesTheSourceAlone(t *testing.T) {
	w := newWorld(t)
	wt := w.laptopTask()
	transcript := filepath.Join(w.laptop, ".claude/projects", claudeSlug(wt), sessionID+".jsonl")
	before := w.read(w.laptop, ".claude/projects/"+claudeSlug(wt)+"/"+sessionID+".jsonl")
	defer w.blockStaging()()
	if out, err := w.ferry(wt, "to", "kant", "--yes", "--no-launch"); err == nil {
		t.Fatalf("handoff should fail:\n%s", out)
	}
	if !exists(transcript) || w.read(w.laptop, ".claude/projects/"+claudeSlug(wt)+"/"+sessionID+".jsonl") != before {
		t.Fatal("the laptop transcript was touched by a handoff that never committed")
	}
	if r := w.record(taskKey("src/app-wt/fix-auth")); r.Owner != "laptop" {
		t.Fatalf("owner = %s", r.Owner)
	}
}

func TestFailurePlacingTheSessionLeavesOneCopyAndRerunFinishes(t *testing.T) {
	w := newWorld(t)
	wt := w.laptopTask()
	laptopCopy := filepath.Join(w.laptop, ".claude/projects", claudeSlug(wt), sessionID+".jsonl")
	kantCopy := filepath.Join(w.kant, ".claude/projects", claudeSlug(filepath.Join(w.kant, "src/app-wt/fix-auth")), sessionID+".jsonl")
	// Staging succeeds, then moving into ~/.claude/projects fails.
	w.write(w.kant, ".claude/projects", "not a directory")
	if out, err := w.ferry(wt, "to", "kant", "--yes", "--no-launch"); err == nil {
		t.Fatalf("handoff should fail placing the session:\n%s", out)
	}
	if exists(laptopCopy) {
		t.Fatal("the laptop copy is still resumable after kant took ownership")
	}
	if r := w.record(taskKey("src/app-wt/fix-auth")); r.State != stateLanding || r.Owner != "kant" {
		t.Fatalf("record: %s owned by %s", r.State, r.Owner)
	}
	os.Remove(filepath.Join(w.kant, ".claude/projects"))
	w.mustFerry(wt, "to", "kant", "--yes", "--no-launch")
	if !exists(kantCopy) {
		t.Fatal("the rerun didn't put the session in place on kant")
	}
}

func TestRerunRefusesWhenTheSourceWasResumedAfterCommit(t *testing.T) {
	w := newWorld(t)
	wt := w.laptopTask()
	key := taskKey("src/app-wt/fix-auth")
	rel := ".claude/projects/" + claudeSlug(wt) + "/" + sessionID + ".jsonl"
	w.write(w.kant, ".claude/projects", "not a directory")
	w.ferry(wt, "to", "kant", "--yes", "--no-launch")
	os.Remove(filepath.Join(w.kant, ".claude/projects"))
	// Someone put the parked laptop copy back and kept talking to it.
	parked, _ := filepath.Glob(filepath.Join(w.laptop, ".local/state/ferry/parked", key+"-g*", rel))
	if len(parked) != 1 {
		t.Fatalf("parked copy: %v", parked)
	}
	data, _ := os.ReadFile(parked[0])
	w.write(w.laptop, rel, string(data)+`{"type":"user","cwd":"`+wt+`","message":"more on the laptop"}`+"\n")

	if out, err := w.ferry(wt, "to", "kant", "--yes", "--no-launch"); err == nil {
		t.Fatalf("rerun should refuse to drop the newer laptop turns:\n%s", out)
	}
	if !exists(filepath.Join(w.laptop, rel)) {
		t.Fatal("the newer laptop copy was parked")
	}
}
