package main

import (
	"os"
	"path/filepath"
	"testing"
)

// The sending machine's transcript is parked before the receiver gets a
// copy, and put back if the handoff fails before the receiver owns it.

func TestFailedInstallPutsTheTranscriptBack(t *testing.T) {
	w := newWorld(t)
	wt := w.laptopTask()
	transcript := filepath.Join(w.laptop, ".claude/projects", claudeSlug(wt), sessionID+".jsonl")
	w.write(w.kant, ".claude/projects", "not a directory, so installing the session fails")
	if out, err := w.ferry(wt, "to", "kant", "--yes", "--no-launch"); err == nil {
		t.Fatalf("handoff should fail:\n%s", out)
	}
	if !exists(transcript) {
		t.Fatal("the laptop transcript is still parked after the handoff failed")
	}
	if exists(filepath.Join(w.laptop, ".local/state/ferry/parked", taskKey("src/app-wt/fix-auth")+"-outgoing")) {
		t.Fatal("left an outgoing park behind")
	}
}

func TestRerunRecoversATranscriptParkedByACrashedRun(t *testing.T) {
	w := newWorld(t)
	wt := w.laptopTask()
	key := taskKey("src/app-wt/fix-auth")
	w.write(w.kant, ".claude/projects", "not a directory")
	w.ferry(wt, "to", "kant", "--yes", "--no-launch")
	os.Remove(filepath.Join(w.kant, ".claude/projects"))
	// A run that died after parking, before kant took ownership.
	laptop := &Local{Home: w.laptop}
	sess, err := laptop.FindSession(FindSessionReq{Agent: agentClaude, ID: sessionID})
	if err != nil || sess == nil {
		t.Fatalf("find: %v %v", sess, err)
	}
	if err := laptop.Park(ParkReq{Session: *sess, Task: key, As: "outgoing"}); err != nil {
		t.Fatal(err)
	}

	w.mustFerry(wt, "to", "kant", "--yes", "--no-launch")
	kslug := claudeSlug(filepath.Join(w.kant, "src/app-wt/fix-auth"))
	if !exists(filepath.Join(w.kant, ".claude/projects", kslug, sessionID+".jsonl")) {
		t.Fatal("kant didn't get the session")
	}
	if exists(filepath.Join(w.laptop, ".claude/projects", claudeSlug(wt), sessionID+".jsonl")) {
		t.Fatal("the laptop copy is still resumable")
	}
	parked, _ := filepath.Glob(filepath.Join(w.laptop, ".local/state/ferry/parked", key+"-*"))
	if len(parked) != 1 || filepath.Base(parked[0]) == key+"-outgoing" {
		t.Fatalf("want one archived park, got %v", parked)
	}
}

func TestParkNamesAreConfined(t *testing.T) {
	l := &Local{Home: t.TempDir()}
	for _, task := range []string{"../../x", "a/b", "", ".hidden"} {
		if err := l.Unpark(task); err == nil {
			t.Fatalf("Unpark accepted %q", task)
		}
	}
	if err := l.ArchivePark(ParkReq{Task: "ok", As: "../escape"}); err == nil {
		t.Fatal("ArchivePark accepted a path")
	}
}
