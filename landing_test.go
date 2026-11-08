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
// screen is what `rex capture` prints. The launched "agent" registers the
// way Claude does, as this test process, unless ~/../no-register exists.
func (w *world) fakeRex(screen string) (log string) {
	bin := filepath.Join(w.root, "bin")
	log = filepath.Join(w.root, "rex.log")
	w.write(w.root, "rex-screen", screen)
	w.write(w.root, "bin/rex", `#!/bin/sh
root="`+w.root+`"
echo "$*" >> "`+log+`"
case "$1" in
new) n=$(grep -c '^new ' "`+log+`")
  id=$(echo "$*" | sed -n 's/.* --resume \([^ ]*\).*/\1/p')
  if [ ! -e "$root/no-register" ] && [ -n "$id" ]; then
    mkdir -p "$HOME/.claude/sessions"
    entry="{\"pid\":`+fmt.Sprint(os.Getpid())+`,\"sessionId\":\"$id\",\"status\":\"idle\"}"
    if [ ! -e "$root/trusted" ] && grep -q 'No, exit' "$root/rex-screen"; then
      # Claude registers only once the trust dialog is accepted, and shows
      # none in a folder it trusts.
      echo "$entry" > "$root/pending"; echo "$HOME/.claude/sessions/fake-$n.json" > "$root/pending-path"
    else
      echo "$entry" > "$HOME/.claude/sessions/fake-$n.json"
    fi
  fi
  echo "{\"session_id\":\"session:fake-$n\",\"initial_windows\":[{\"block_ids\":[\"block:fake-$n\"]}]}" ;;
capture) cat "$root/rex-screen" ;;
send-key)
  if [ -e "$root/drop-key" ]; then rm "$root/drop-key"; exit 0; fi
  key=$(eval echo \${$#})
  if grep -q '❯ No, exit' "$root/rex-screen" && [ "$key" = down ]; then
    printf 'Accessing workspace\n   No, exit\n ❯ Yes, I trust this folder\n' > "$root/rex-screen"
  elif grep -q '❯ Yes, I trust' "$root/rex-screen" && [ "$key" = enter ]; then
    printf 'auto mode on\n' > "$root/rex-screen"
    if [ -e "$root/pending" ]; then cp "$root/pending" "$(cat "$root/pending-path")"; rm "$root/pending"; fi
  fi ;;
ls) echo '{"sessions":[]}' ;;
esac
`)
	os.Chmod(filepath.Join(bin, "rex"), 0o755)
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
	w.write(w.laptop, ".claude.json", fmt.Sprintf(`{"projects":{%q:{"hasTrustDialogAccepted":true}}}`, wt))
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
				w.write(w.root, "trusted", "")
			}
			w.mustFerry(wt, "to", "kant", "--yes")
			answered := countLines(log, "send-key") > 0
			if answered == kantTrusts {
				t.Fatalf("answered the trust prompt = %v with kant trusting = %v\n%s", answered, kantTrusts, w.read(w.root, "rex.log"))
			}
		})
	}
}

// The first key press can land before Claude's dialog takes input. The
// answer goes by where the cursor is, so a dropped key is sent again, and a
// key that did land is never repeated into "No, exit".
func TestTrustAnswerSurvivesADroppedKey(t *testing.T) {
	w := newWorld(t)
	log := w.fakeRex("Accessing workspace\n ❯ No, exit\n   Yes, I trust this folder\n")
	w.write(w.root, "drop-key", "")
	wt := w.laptopTask()
	w.write(w.laptop, ".claude.json", fmt.Sprintf(`{"projects":{%q:{"hasTrustDialogAccepted":true}}}`, wt))
	out := w.mustFerry(wt, "to", "kant", "--yes")
	if !strings.Contains(out, "launch     kant: rex session") {
		t.Fatalf("the agent didn't launch:\n%s\n%s", out, w.read(w.root, "rex.log"))
	}
	if n := countLines(log, "send-key"); n != 3 {
		t.Fatalf("sent %d keys, want down (dropped), down, enter\n%s", n, w.read(w.root, "rex.log"))
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

// Launch returns only once the agent has registered, because until then
// Liveness can't see it: a rerun would start a second agent, and a `ferry
// from` would park the transcript under it.
func TestLaunchKillsAnAgentThatNeverRegisters(t *testing.T) {
	w := newSerialWorld(t)
	// kant trusts the folder, so no trust dialog stands between the launch
	// and the wait for registration.
	log := w.fakeRex("auto mode on")
	w.write(w.kant, ".claude.json", fmt.Sprintf(`{"projects":{%q:{"hasTrustDialogAccepted":true}}}`, w.kant))
	w.write(w.root, "trusted", "")
	w.write(w.root, "no-register", "")
	t.Setenv("PATH", w.path)
	t.Setenv("HOME", w.kant)
	defer func(d time.Duration) { launchTimeout = d }(launchTimeout)
	launchTimeout = time.Second

	l := &Local{Home: w.kant}
	s := Session{Agent: agentClaude, ID: sessionID}
	_, err := l.Launch(LaunchReq{Name: "t", Argv: []string{"claude", "--resume", sessionID}, Agent: agentClaude, Session: s, AcceptTrust: false})
	if err == nil || !strings.Contains(err.Error(), "didn't load the session") {
		t.Fatalf("Launch = %v, want a timeout", err)
	}
	if n := countLines(log, "kill session:fake-1"); n != 1 {
		t.Fatalf("killed the unregistered agent's rex session %d times, want 1\n%s", n, w.read(w.root, "rex.log"))
	}

	os.Remove(filepath.Join(w.root, "no-register"))
	if id, err := l.Launch(LaunchReq{Name: "t", Argv: []string{"claude", "--resume", sessionID}, Agent: agentClaude, Session: s, AcceptTrust: false}); err != nil || id != "session:fake-2" {
		t.Fatalf("Launch of an agent that registers = %q, %v", id, err)
	}
}

func TestNoLaunchIntoAFolderNeitherMachineTrusts(t *testing.T) {
	w := newWorld(t)
	log := w.fakeRex("auto mode on")
	wt := w.laptopTask()
	out := w.mustFerry(wt, "to", "kant", "--yes")
	if n := countLines(log, "new "); n != 0 {
		t.Fatalf("started an agent that would wait at the trust prompt:\n%s", out)
	}
	if !strings.Contains(out, "claude --resume") {
		t.Fatalf("expected the resume command instead:\n%s", out)
	}
}

// Turns added on the source before a rerun finishes the handoff go to the
// receiver with it, instead of staying behind for the next pull to park.
func TestLandingRerunSendsTurnsAddedOnTheSource(t *testing.T) {
	w := newWorld(t)
	wt := w.laptopTask()
	w.failingRestore(wt)
	if out, err := w.ferry(wt, "to", "kant", "--yes", "--no-launch"); err == nil {
		t.Fatalf("first run should fail in restore:\n%s", out)
	}
	sent := w.record(taskKey("src/app-wt/fix-auth")).Source.Size
	src := filepath.Join(w.laptop, ".claude/projects", claudeSlug(wt), sessionID+".jsonl")
	w.idleAppend(src, fmt.Sprintf(`{"type":"user","cwd":%q,"message":"added on the laptop after the failed run"}`, wt))

	// While an agent still has it open, the rerun waits for you to exit it.
	w.liveAgent(w.laptop, sessionID)
	if out, err := w.ferry(wt, "to", "kant", "--yes", "--no-launch"); err == nil || !strings.Contains(out, "exit it, then run this again") {
		t.Fatalf("rerun with the source agent open: err %v\n%s", err, out)
	}
	os.RemoveAll(filepath.Join(w.laptop, ".claude/sessions"))

	w.write(w.kant, "allow-restore", "")
	out := w.mustFerry(wt, "to", "kant", "--yes", "--no-launch")
	if !strings.Contains(out, "sending it again") {
		t.Fatalf("the rerun didn't notice the new turns:\n%s", out)
	}
	kwt := filepath.Join(w.kant, "src", "app-wt", "fix-auth")
	if got := w.read(w.kant, ".claude/projects/"+claudeSlug(kwt)+"/"+sessionID+".jsonl"); !strings.Contains(got, "added on the laptop after the failed run") {
		t.Fatalf("kant's copy lacks the new turns:\n%s", got)
	}
	r := w.record(taskKey("src/app-wt/fix-auth"))
	if r.State != stateOwned || r.Source == nil || r.Source.Size <= sent {
		t.Fatalf("record after the rerun: state %s, source %+v, first sent %d bytes", r.State, r.Source, sent)
	}
}

// A Launch that a dead ferry left running must not start a second agent next
// to one you opened in the meantime.
func TestLaunchRefusesASessionAlreadyLoaded(t *testing.T) {
	w := newSerialWorld(t)
	log := w.fakeRex("auto mode on")
	t.Setenv("PATH", w.path)
	w.liveAgent(w.kant, kantSessionID)
	l := &Local{Home: w.kant}
	_, err := l.Launch(LaunchReq{Name: "tool", CwdRel: "src", Agent: agentClaude, AcceptTrust: true,
		Argv: []string{"claude", "--resume", kantSessionID}, Session: Session{Agent: agentClaude, ID: kantSessionID, Cwd: filepath.Join(w.kant, "src")}})
	if err == nil || !strings.Contains(err.Error(), "already running") {
		t.Fatalf("Launch = %v, want a refusal", err)
	}
	if n := countLines(log, "new "); n != 0 {
		t.Fatalf("started %d agents", n)
	}
}
