package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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
