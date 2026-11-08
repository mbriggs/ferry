package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestExtrasDeletedOnKantAreRemovedOnReturn(t *testing.T) {
	w := newWorld(t)
	wt := w.laptopTask()
	w.write(wt, ".gitignore", ".env*\n.ferry/state/\nnode_modules/\n.restored\n")
	w.write(wt, ".env.local", "LOCAL=1\n")
	w.mustFerry(wt, "to", "kant", "--yes", "--no-launch")

	kwt := filepath.Join(w.kant, "src/app-wt/fix-auth")
	for _, f := range []string{".env", ".env.local"} {
		if err := os.Remove(filepath.Join(kwt, f)); err != nil {
			t.Fatal(err)
		}
	}
	// The laptop's .env.local changed after it was sent, so it isn't the
	// file kant deleted.
	w.write(wt, ".env.local", "LOCAL=2\n")
	w.approve(w.kant, dehydrateScript)

	out := w.mustFerry(w.laptop, "from", "kant", "fix-auth", "--yes")
	if exists(filepath.Join(wt, ".env")) {
		t.Fatalf(".env came back after kant deleted it:\n%s", out)
	}
	if !strings.Contains(out, "kept .env.local") || w.read(wt, ".env.local") != "LOCAL=2\n" {
		t.Fatalf(".env.local changed on the laptop and should be kept:\n%s", out)
	}
	parked, _ := filepath.Glob(filepath.Join(w.laptop, ".local/state/ferry/parked", taskKey("src/app-wt/fix-auth")+"-extras-*", ".env"))
	if len(parked) != 1 {
		t.Fatal("the removed .env wasn't parked")
	}
}

func TestUnchangedExtrasAreNotSentAgain(t *testing.T) {
	w := newWorld(t)
	wt := w.laptopTask()
	w.mustFerry(wt, "to", "kant", "--yes", "--no-launch")
	w.approve(w.kant, dehydrateScript)
	out := w.mustFerry(w.laptop, "from", "kant", "fix-auth", "--yes")
	if !strings.Contains(out, "already on laptop, not sent: .env") {
		t.Fatalf("the unchanged .env was sent back:\n%s", out)
	}
	if w.read(wt, ".env") != "SECRET=1\n" {
		t.Fatal(".env changed")
	}
	// The laptop's own copy was 0644; extras end 0600 even when not resent.
	if st, err := os.Stat(filepath.Join(wt, ".env")); err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("unchanged .env mode: %v %v", st.Mode().Perm(), err)
	}
}

func TestReplacedExtrasEndPrivate(t *testing.T) {
	w := newWorld(t)
	wt := w.laptopTask() // .env is 0644 on the laptop
	w.mustFerry(wt, "to", "kant", "--yes", "--no-launch")
	// The same size and the same whole second as the laptop's copy: rsync's
	// default quick check would call the two files equal and skip it.
	kenv := filepath.Join(w.kant, "src/app-wt/fix-auth", ".env")
	w.write(filepath.Dir(kenv), ".env", "SECRET=2\n")
	st, err := os.Stat(filepath.Join(wt, ".env"))
	if err != nil {
		t.Fatal(err)
	}
	sameSecond := st.ModTime().Truncate(time.Second).Add(time.Nanosecond)
	if sameSecond.Equal(st.ModTime()) {
		sameSecond = sameSecond.Add(time.Nanosecond)
	}
	os.Chtimes(kenv, sameSecond, sameSecond)
	w.approve(w.kant, dehydrateScript)
	w.mustFerry(w.laptop, "from", "kant", "fix-auth", "--yes")
	if w.read(wt, ".env") != "SECRET=2\n" {
		t.Fatal("kant's .env didn't come back")
	}
	if st, err := os.Stat(filepath.Join(wt, ".env")); err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("replaced .env mode: %v %v", st.Mode().Perm(), err)
	}
}
