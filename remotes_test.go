package main

import (
	"path/filepath"
	"strings"
	"testing"
)

// pushToOrigin commits a file on branch in a clone of origin and pushes it,
// as if someone else had pushed, and returns the new commit.
func (w *world) pushToOrigin(branch, file string) string {
	w.t.Helper()
	other := filepath.Join(w.root, "other")
	if !exists(other) {
		w.git(w.root, "clone", "-q", filepath.Join(w.root, "origin.git"), other)
	}
	w.git(other, "fetch", "-q", "origin")
	start := "origin/main"
	if _, err := git(other, "rev-parse", "--verify", "-q", "origin/"+branch); err == nil {
		start = "origin/" + branch
	}
	w.git(other, "checkout", "-q", "-B", branch, start)
	w.write(other, file, file+"\n")
	w.git(other, "add", file)
	w.git(other, "commit", "-qm", file)
	w.git(other, "push", "-q", "origin", branch)
	return w.git(other, "rev-parse", "HEAD")
}

func TestRemotesTravel(t *testing.T) {
	w := newWorld(t)
	wt := w.laptopTask()
	app := filepath.Join(w.laptop, "src", "app")
	kapp := filepath.Join(w.kant, "src", "app")
	origin := filepath.Join(w.root, "origin.git")
	w.git(app, "remote", "add", "upstream", "https://example.com/upstream.git")
	w.git(app, "remote", "set-url", "--push", "upstream", "git@example.com:upstream.git")
	w.git(app, "config", "branch.fix-auth.remote", "origin")
	w.git(app, "config", "branch.fix-auth.merge", "refs/heads/fix-auth")
	feature := w.pushToOrigin("feature", "f1.txt")
	w.git(app, "fetch", "-q", "origin")

	// A new repo on kant takes every remote, the upstream, and the tracking
	// refs without asking.
	w.mustFerry(wt, "to", "kant", "--yes", "--skip-scripts", "--no-launch")
	if got := w.git(kapp, "remote", "get-url", "origin"); got != origin {
		t.Fatalf("kant origin = %q", got)
	}
	if got := w.git(kapp, "remote", "get-url", "--push", "upstream"); got != "git@example.com:upstream.git" {
		t.Fatalf("kant upstream push url = %q", got)
	}
	if got := upstreamOf(kapp, "fix-auth"); got != (Upstream{"origin", "refs/heads/fix-auth"}) {
		t.Fatalf("kant fix-auth upstream = %v", got)
	}
	if got := w.git(kapp, "rev-parse", "origin/feature"); got != feature {
		t.Fatalf("kant origin/feature = %q, want %s", got, feature)
	}
	if staged := w.git(kapp, "for-each-ref", "refs/ferry/*/remotes/"); staged != "" {
		t.Fatalf("kant kept the staged tracking refs:\n%s", staged)
	}

	w.mustFerry(w.laptop, "from", "kant", "fix-auth", "--yes", "--skip-scripts")

	// The laptop fetches feature, kant fetches main. Each side's newer ref
	// survives the next trip; neither moves backward.
	feature = w.pushToOrigin("feature", "f2.txt")
	w.git(app, "fetch", "-q", "origin", "feature:refs/remotes/origin/feature")
	main := w.pushToOrigin("main", "m1.txt")
	w.git(kapp, "fetch", "-q", "origin", "main:refs/remotes/origin/main")
	out := w.mustFerry(wt, "to", "kant", "--yes", "--skip-scripts", "--no-launch")
	if got := w.git(kapp, "rev-parse", "origin/feature"); got != feature {
		t.Fatalf("kant origin/feature = %q, want the laptop's %s\n%s", got, feature, out)
	}
	if got := w.git(kapp, "rev-parse", "origin/main"); got != main {
		t.Fatalf("kant origin/main = %q, want kant's newer %s\n%s", got, main, out)
	}

	// A URL changed on kant doesn't reach the laptop without a yes on the
	// terminal, and that remote's tracking refs stay put.
	w.git(kapp, "remote", "set-url", "origin", "https://example.com/elsewhere.git")
	w.git(kapp, "fetch", "-q", origin, "main:refs/remotes/origin/main")
	before := w.git(app, "rev-parse", "origin/main")
	out = w.mustFerry(w.laptop, "from", "kant", "fix-auth", "--yes", "--skip-scripts")
	if got := w.git(app, "remote", "get-url", "origin"); got != origin {
		t.Fatalf("laptop origin = %q, want it unchanged\n%s", got, out)
	}
	if !strings.Contains(out, "kept laptop's remotes") || !strings.Contains(out, "elsewhere.git") {
		t.Fatalf("want the declined change listed\n%s", out)
	}
	if got := w.git(app, "rev-parse", "origin/main"); got != before {
		t.Fatalf("laptop origin/main moved to %s with a different origin URL", got)
	}
}

func TestTrackedByPicksTheLongestRemote(t *testing.T) {
	same := map[string]bool{"fork": true, "fork/a": false}
	if trackedBy("fork/a/main", same) {
		t.Fatal("fork/a/main belongs to fork/a, whose URLs differ")
	}
	if !trackedBy("fork/main", same) {
		t.Fatal("fork/main belongs to fork")
	}
	if trackedBy("gone/main", same) {
		t.Fatal("a ref of a remote this repo lacks isn't taken")
	}
}
