package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Recovery after the commit point must act on the session this handoff
// moved, and must never start a second agent on it.

// fakeRex puts a rex on PATH that logs each call and pretends to launch.
// screen is what `rex capture` prints.
func (w *world) fakeRex(screen string) (log string) {
	bin := filepath.Join(w.root, "bin")
	log = filepath.Join(w.root, "rex.log")
	w.write(w.root, "rex-screen", screen)
	w.write(w.root, "bin/rex", `#!/bin/sh
echo "$*" >> "`+log+`"
case "$1" in
new) n=$(grep -c '^new ' "`+log+`"); echo "{\"session_id\":\"session:fake-$n\",\"initial_windows\":[{\"block_ids\":[\"block:fake-$n\"]}]}" ;;
capture) cat "`+filepath.Join(w.root, "rex-screen")+`" ;;
ls) echo '{"sessions":[]}' ;;
esac
`)
	os.Chmod(filepath.Join(bin, "rex"), 0o755)
	w.path = bin + string(os.PathListSeparator) + os.Getenv("PATH")
	return log
}

func countLines(path, prefix string) int {
	data, _ := os.ReadFile(path)
	n := 0
	for _, l := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(l, prefix) {
			n++
		}
	}
	return n
}

// failingRestore replaces the task's restore hook with one that fails until
// ~/allow-restore exists on kant, and approves it there.
func (w *world) failingRestore(wt string) {
	script := "#!/bin/sh\ntest -f \"$HOME/allow-restore\" || { echo 'restore: not yet'; exit 1; }\necho restored\n"
	w.write(wt, ".ferry/restore", script)
	w.approve(w.kant, script)
}

func TestLandingRerunParksOnlyTheHandedOffSession(t *testing.T) {
	w := newWorld(t)
	wt := w.laptopTask()
	slug := claudeSlug(wt)
	other := "99999999-0000-0000-0000-000000000000"
	otherPath := filepath.Join(w.laptop, ".claude/projects", slug, other+".jsonl")
	w.write(w.laptop, ".claude/projects/"+slug+"/"+other+".jsonl", `{"type":"user","cwd":"`+wt+`","message":"an older, unrelated chat"}`+"\n")
	old := time.Now().Add(-time.Hour)
	os.Chtimes(otherPath, old, old)
	w.failingRestore(wt)

	if out, err := w.ferry(wt, "to", "kant", "--yes", "--no-launch"); err == nil {
		t.Fatalf("first run should fail in restore:\n%s", out)
	}
	w.write(w.kant, "allow-restore", "")
	w.mustFerry(wt, "to", "kant", "--yes", "--no-launch")
	if !exists(otherPath) {
		t.Fatal("the rerun parked an unrelated session")
	}
}

func TestLandingRerunLaunchesOnce(t *testing.T) {
	w := newWorld(t)
	log := w.fakeRex("auto mode on")
	wt := w.laptopTask()
	w.failingRestore(wt)
	if out, err := w.ferry(wt, "to", "kant", "--yes"); err == nil {
		t.Fatalf("first run should fail in restore:\n%s", out)
	}
	if n := countLines(log, "new "); n != 0 {
		t.Fatalf("launched %d agents before restore succeeded", n)
	}
	w.write(w.kant, "allow-restore", "")
	w.mustFerry(wt, "to", "kant", "--yes")
	if n := countLines(log, "new "); n != 1 {
		t.Fatalf("launched %d agents, want 1", n)
	}
	if r := w.record(taskKey("src/app-wt/fix-auth")); r.State != stateOwned || r.RexSession != "session:fake-1" {
		t.Fatalf("record after launch: state %s, rex session %q", r.State, r.RexSession)
	}
	if out, err := w.ferry(wt, "to", "kant", "--yes"); err == nil {
		t.Fatalf("a finished handoff must not run again:\n%s", out)
	}
	if n := countLines(log, "new "); n != 1 {
		t.Fatalf("launched %d agents, want 1", n)
	}
}

func TestLandingRerunDoesNotRelaunchALiveAgent(t *testing.T) {
	w := newWorld(t)
	log := w.fakeRex("auto mode on")
	wt := w.laptopTask()
	w.failingRestore(wt)
	w.ferry(wt, "to", "kant", "--yes")
	w.write(w.kant, "allow-restore", "")
	// The interrupted run had already started the agent on kant.
	w.write(w.kant, ".claude/sessions/1.json", fmt.Sprintf(`{"pid":%d,"sessionId":%q,"status":"idle"}`, os.Getpid(), sessionID))

	out := w.mustFerry(wt, "to", "kant", "--yes")
	if !strings.Contains(out, "already running") {
		t.Fatalf("expected the rerun to notice the running agent:\n%s", out)
	}
	if n := countLines(log, "new "); n != 0 {
		t.Fatalf("started %d more agents", n)
	}
}

func TestTrustIsAnsweredOnlyWhenKantDoesntTrustTheFolder(t *testing.T) {
	dialog := "Accessing workspace\n ❯ No, exit\n   Yes, I trust this folder\n"
	for _, kantTrusts := range []bool{false, true} {
		t.Run(fmt.Sprint("kant trusts=", kantTrusts), func(t *testing.T) {
			w := newWorld(t)
			log := w.fakeRex(dialog)
			wt := w.laptopTask()
			kwt := filepath.Join(w.kant, "src/app-wt/fix-auth")
			w.write(w.laptop, ".claude.json", fmt.Sprintf(`{"projects":{%q:{"hasTrustDialogAccepted":true}}}`, wt))
			if kantTrusts {
				w.write(w.kant, ".claude.json", fmt.Sprintf(`{"projects":{%q:{"hasTrustDialogAccepted":true}}}`, kwt))
			}
			w.mustFerry(wt, "to", "kant", "--yes")
			answered := countLines(log, "send-key") > 0
			if answered == kantTrusts {
				t.Fatalf("answered the trust prompt = %v with kant trusting = %v\n%s", answered, kantTrusts, w.read(w.root, "rex.log"))
			}
		})
	}
}

func TestTrustIsNotAnsweredFromConversationText(t *testing.T) {
	w := newWorld(t)
	log := w.fakeRex("● I said we should trust this folder earlier.\n❯ \n  auto mode on\n")
	wt := w.laptopTask()
	w.write(w.laptop, ".claude.json", fmt.Sprintf(`{"projects":{%q:{"hasTrustDialogAccepted":true}}}`, wt))
	w.mustFerry(wt, "to", "kant", "--yes")
	if n := countLines(log, "send-key"); n != 0 {
		t.Fatalf("typed %d keys into a conversation", n)
	}
}
