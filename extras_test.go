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
	w.mustFerry(wt, "to", "kant", "--yes", "--skip-scripts", "--no-launch")

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

	out := w.mustFerry(w.laptop, "from", "kant", "fix-auth", "--yes", "--skip-scripts")
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
	w.mustFerry(wt, "to", "kant", "--yes", "--skip-scripts", "--no-launch")
	w.approve(w.kant, dehydrateScript)
	out := w.mustFerry(w.laptop, "from", "kant", "fix-auth", "--yes", "--skip-scripts")
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
	w.mustFerry(wt, "to", "kant", "--yes", "--skip-scripts", "--no-launch")
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
	w.mustFerry(w.laptop, "from", "kant", "fix-auth", "--yes", "--skip-scripts")
	if w.read(wt, ".env") != "SECRET=2\n" {
		t.Fatal("kant's .env didn't come back")
	}
	if st, err := os.Stat(filepath.Join(wt, ".env")); err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("replaced .env mode: %v %v", st.Mode().Perm(), err)
	}
}

// An ignored file edited on both machines: the receiver's copy is parked
// before the sender's replaces it, and the output says so.
func TestExtrasChangedOnBothSidesAreParked(t *testing.T) {
	w := newWorld(t)
	wt := w.laptopTask()
	w.mustFerry(wt, "to", "kant", "--yes", "--skip-scripts", "--no-launch")
	w.write(filepath.Join(w.kant, "src/app-wt/fix-auth"), ".env", "SECRET=kant\n")
	w.write(wt, ".env", "SECRET=laptop\n")
	w.approve(w.kant, dehydrateScript)
	out := w.mustFerry(w.laptop, "from", "kant", "fix-auth", "--yes", "--skip-scripts")
	if w.read(wt, ".env") != "SECRET=kant\n" {
		t.Fatalf("kant's .env didn't come back:\n%s", out)
	}
	if !strings.Contains(out, "parked its copies of .env") {
		t.Fatalf("the output doesn't say the laptop's .env was parked:\n%s", out)
	}
	parked, _ := filepath.Glob(filepath.Join(w.laptop, ".local/state/ferry/parked", taskKey("src/app-wt/fix-auth")+"-extras-*", ".env"))
	if len(parked) != 1 || w.read(filepath.Dir(parked[0]), ".env") != "SECRET=laptop\n" {
		t.Fatalf("the laptop's .env wasn't parked: %v", parked)
	}
}

// An ignored script stays executable, though only for its owner; a symlink
// stays and is listed; a name with a newline is escaped in the output, and
// isn't sent again when nothing changed.
func TestExtrasModesLinksAndOddNames(t *testing.T) {
	w := newWorld(t)
	wt := w.laptopTask()
	w.write(wt, ".gitignore", ".env*\nbin-local/\n.ferry/state/\nnode_modules/\n.restored\n")
	w.write(wt, "bin-local/run", "#!/bin/sh\n")
	os.Chmod(filepath.Join(wt, "bin-local/run"), 0o755)
	os.Symlink("/etc/hosts", filepath.Join(wt, "bin-local/hosts"))
	w.write(wt, ".env\nfake", "x\n")
	out := w.mustFerry(wt, "to", "kant", "--yes", "--skip-scripts", "--no-launch")
	kwt := filepath.Join(w.kant, "src/app-wt/fix-auth")
	if st, err := os.Stat(filepath.Join(kwt, "bin-local/run")); err != nil || st.Mode().Perm() != 0o700 {
		t.Fatalf("bin-local/run mode on kant: %v %v", st, err)
	}
	if _, err := os.Lstat(filepath.Join(kwt, "bin-local/hosts")); err == nil {
		t.Fatal("the symlink was sent")
	}
	if !strings.Contains(out, "symlinks staying on laptop: bin-local/hosts") {
		t.Fatalf("the symlink isn't listed as staying:\n%s", out)
	}
	if strings.Contains(out, ".env\nfake") || !strings.Contains(out, `.env\x{a}fake`) {
		t.Fatalf("the newline in a name wasn't escaped:\n%s", out)
	}
	if w.read(kwt, ".env\nfake") != "x\n" {
		t.Fatal("the file with a newline in its name didn't arrive")
	}
	w.approve(w.kant, dehydrateScript)
	out = w.mustFerry(w.laptop, "from", "kant", "fix-auth", "--yes", "--skip-scripts")
	_, notSent, _ := strings.Cut(out, "already on laptop, not sent:")
	notSent, _, _ = strings.Cut(notSent, "\n")
	if !strings.Contains(notSent, `.env\x{a}fake`) || !strings.Contains(notSent, "bin-local/run") {
		t.Fatalf("unchanged extras were sent again:\n%s", out)
	}
}
