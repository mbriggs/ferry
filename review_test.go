package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// liveAgent registers session id as loaded by a running process in home,
// the way Claude does. The process has no Rex variables in its environment,
// like an agent started outside Rex.
func (w *world) liveAgent(home, id string) {
	w.t.Helper()
	p := exec.Command("sleep", "60")
	p.Env = []string{"PATH=/usr/bin:/bin"}
	if err := p.Start(); err != nil {
		w.t.Fatal(err)
	}
	w.t.Cleanup(func() { p.Process.Kill(); p.Wait() })
	w.write(home, fmt.Sprintf(".claude/sessions/%d.json", p.Process.Pid),
		fmt.Sprintf(`{"pid":%d,"sessionId":%q,"status":"idle"}`, p.Process.Pid, id))
}

// paneRex puts a rex on PATH with one session, labeled label, whose only
// pane runs an idle Claude. Every call is logged.
func (w *world) paneRex(label string) (log string) {
	log = filepath.Join(w.root, "rex.log")
	writeExec(w.t, filepath.Join(w.root, "bin", "rex"), `#!/bin/sh
echo "$*" >> "`+log+`"
case "$1 $2" in
"ls --json") echo '{"sessions":[{"session_id":"session:other","label":"`+label+`"}]}' ;;
"block ls") echo '{"blocks":[{"block_id":"block:other"}]}' ;;
"block call") echo '{"records":[{"state":"idle","app":"claude-code"}]}' ;;
esac
`)
	return log
}

// An agent ferry didn't launch is only stopped through the pane its own
// environment names. A Rex session that happens to share the worktree's name
// may hold someone else's agent.
func TestPullNeverStopsAPaneItCannotTieToTheAgent(t *testing.T) {
	w := newWorld(t)
	w.kantTool()
	w.liveAgent(w.kant, kantSessionID)
	log := w.paneRex("tool")
	out, err := w.ferry(w.laptop, "from", "kant", "src/tool", "--yes", "--no-launch")
	if err == nil || !strings.Contains(out, "exit it first") {
		t.Fatalf("expected a refusal:\n%s", out)
	}
	if data, _ := os.ReadFile(log); strings.Contains(string(data), "send") {
		t.Fatalf("typed into an unrelated pane:\n%s", data)
	}
}

// A check that refuses must come before the agent is stopped, or a rerun
// can no longer find the session through the live agent.
func TestPullRefusesBeforeStoppingTheAgent(t *testing.T) {
	w := newWorld(t)
	w.kantTool()
	w.liveAgent(w.kant, kantSessionID)
	w.liveAgent(w.laptop, kantSessionID) // the laptop has it loaded too
	log := w.paneRex("tool")
	out, err := w.ferry(w.laptop, "from", "kant", "src/tool", "--yes", "--no-launch")
	if err == nil || !strings.Contains(out, "already has this session loaded") {
		t.Fatalf("expected the receiver check to refuse first:\n%s", out)
	}
	if data, _ := os.ReadFile(log); strings.Contains(string(data), "send") {
		t.Fatalf("stopped the agent before refusing:\n%s", data)
	}
}

// A session that started above its worktree goes back with `ferry to`, found
// through the record, though it isn't in the worktree's own project folder.
func TestSessionStartedAboveTheWorktreeMakesTheReturnTrip(t *testing.T) {
	w := newWorld(t)
	w.kantTool()
	w.mustFerry(w.laptop, "from", "kant", "src/tool", "--session", kantSessionID, "--yes", "--no-launch")
	lwt := filepath.Join(w.laptop, "src", "tool")
	out := w.mustFerry(lwt, "to", "kant", "--yes", "--no-launch")
	if !strings.Contains(out, "agent      claude "+short(kantSessionID)) {
		t.Fatalf("the session didn't go back with the worktree:\n%s", out)
	}
}

// Placing a session never swaps the file under an agent that has it loaded.
func TestPlaceSessionRefusesALoadedCopy(t *testing.T) {
	w := newWorld(t)
	task := taskKey("src/tool")
	cwd := filepath.Join(w.kant, "src")
	rel := ".claude/projects/" + claudeSlug(cwd) + "/" + kantSessionID + ".jsonl"
	w.write(w.kant, ".local/state/ferry/in/"+task+"/ready/"+rel, "incoming\n")
	w.write(w.kant, rel, "resumed here\n")
	w.liveAgent(w.kant, kantSessionID)

	l := &Local{Home: w.kant}
	err := l.PlaceSession(PlaceReq{Task: task, Session: Session{Agent: agentClaude, ID: kantSessionID, Cwd: cwd, Files: []string{rel}}})
	if err == nil || !strings.Contains(err.Error(), "loaded here") {
		t.Fatalf("PlaceSession = %v, want a refusal", err)
	}
	if got := w.read(w.kant, rel); got != "resumed here\n" {
		t.Fatalf("the loaded copy was replaced: %q", got)
	}
}

// ferry remote runs rex through the same PATH it found rex on.
func TestRexRunsFromTheLoginShellPath(t *testing.T) {
	dir := t.TempDir()
	tools := filepath.Join(dir, "tools")
	writeExec(t, filepath.Join(tools, "rex"), "#!/bin/sh\necho login-rex\n")
	shell := filepath.Join(dir, "loginsh")
	writeExec(t, shell, "#!/bin/sh\nPATH="+tools+":$PATH\nexport PATH\nexec /bin/sh -c \"$2\"\n")
	t.Setenv("SHELL", shell)
	t.Setenv("PATH", "/usr/bin:/bin")
	useLoginPath = true
	defer func() { useLoginPath = false }()

	if out, err := rexRun("", "ls"); err != nil || out != "login-rex" {
		t.Fatalf("rexRun = %q, %v", out, err)
	}
}

// Every ignored file travels, the agent's project folder included, except
// dependency and build trees and Claude's own worktrees, which are listed as
// staying.
func TestIgnoredFilesTravelAndLeftoversAreListed(t *testing.T) {
	w := newWorld(t)
	wt := w.laptopTask()
	w.write(wt, ".gitignore", ".env\n.claude/\nformal/out/\nnode_modules/\ndist/\n.restored\n.ferry/state/\n")
	w.write(wt, ".claude/skills/verify/SKILL.md", "skill\n")
	w.write(wt, ".claude/worktrees/side/README", "a whole checkout\n")
	w.write(wt, "formal/out/run.out", "generated\n")
	w.write(wt, "dist/ferry-linux", "binary\n")
	// Dependency-folder names are guesses that don't apply in the agent's
	// folder; deeper ones elsewhere stay, and are listed.
	w.write(wt, ".claude/skills/build/SKILL.md", "a skill named build\n")
	w.write(wt, "formal/out/deps/x", "nested dependency tree\n")
	w.write(wt, "formal/out/.DS_Store", "junk\n")

	got, err := (&Local{Home: w.laptop}).ListExtras("src/app-wt/fix-auth")
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range got.Files {
		names = append(names, e.Path)
	}
	list := strings.Join(names, ",")
	for _, want := range []string{".claude/skills/verify/SKILL.md", ".claude/skills/build/SKILL.md", "formal/out/run.out", ".env"} {
		if !strings.Contains(list, want) {
			t.Fatalf("%s should travel: %s", want, list)
		}
	}
	if strings.Contains(list, "worktrees") || strings.Contains(list, "dist/") || strings.Contains(list, "node_modules") || strings.Contains(list, "deps/") || strings.Contains(list, ".DS_Store") {
		t.Fatalf("sent something that should stay: %s", list)
	}
	left := strings.Join(got.LeftBehind, ",")
	if !strings.Contains(left, "dist/") || !strings.Contains(left, "node_modules/") || !strings.Contains(left, "formal/out/deps/") || strings.Contains(left, ".claude") || strings.Contains(left, ".DS_Store") {
		t.Fatalf("left behind = %s", left)
	}
}

// The project's memory goes with the session and merges with what the
// receiver has.
func TestProjectMemoryTravelsWithTheSession(t *testing.T) {
	w := newWorld(t)
	wt := w.laptopTask()
	kwt := filepath.Join(w.kant, "src", "app-wt", "fix-auth")
	w.write(w.laptop, ".claude/projects/"+claudeSlug(wt)+"/memory/MEMORY.md", "- [deploy](deploy.md)\n")
	w.write(w.laptop, ".claude/projects/"+claudeSlug(wt)+"/memory/deploy.md", "ship on fridays\n")
	w.write(w.kant, ".claude/projects/"+claudeSlug(kwt)+"/memory/kant-only.md", "kant knows this\n")
	w.mustFerry(wt, "to", "kant", "--yes", "--no-launch")
	mem := ".claude/projects/" + claudeSlug(kwt) + "/memory/"
	if got := w.read(w.kant, mem+"deploy.md"); got != "ship on fridays\n" {
		t.Fatalf("memory didn't travel: %q", got)
	}
	if got := w.read(w.kant, mem+"kant-only.md"); got != "kant knows this\n" {
		t.Fatalf("kant's own memory was lost: %q", got)
	}
}

// Git hooks go to kant as they are. Coming back, they're installed only with
// a yes on that run, which --yes doesn't give.
func TestGitHooksTravel(t *testing.T) {
	w := newWorld(t)
	wt := w.laptopTask()
	lhook := filepath.Join(w.laptop, "src", "app", ".git", "hooks", "pre-commit")
	writeExec(t, lhook, "#!/bin/sh\necho laptop\n")
	w.mustFerry(wt, "to", "kant", "--yes", "--no-launch")
	khook := filepath.Join(w.kant, "src", "app", ".git", "hooks", "pre-commit")
	st, err := os.Stat(khook)
	if err != nil || st.Mode().Perm()&0o100 == 0 {
		t.Fatalf("hook not installed executable on kant: %v %v", st, err)
	}

	writeExec(t, khook, "#!/bin/sh\necho kant changed this\n")
	out := w.mustFerry(w.laptop, "from", "kant", "fix-auth", "--yes", "--no-launch")
	if !strings.Contains(out, "git hooks from kant: pre-commit") || !strings.Contains(out, "skipped") {
		t.Fatalf("expected the hook to be offered and skipped without a terminal:\n%s", out)
	}
	if got, _ := os.ReadFile(lhook); string(got) != "#!/bin/sh\necho laptop\n" {
		t.Fatalf("kant's hook was installed without a yes: %q", got)
	}
}

// Once its agent exits, a Rex session ferry launched is empty and gets
// closed; a session with anything left in it stays.
func TestEmptyRexSessionIsClosed(t *testing.T) {
	for _, blocks := range []string{`[]`, `[{"block_id":"block:shell"}]`} {
		w := newSerialWorld(t)
		log := filepath.Join(w.root, "rex.log")
		writeExec(t, filepath.Join(w.root, "bin", "rex"), "#!/bin/sh\necho \"$*\" >> "+log+"\ncase \"$1 $2\" in \"block ls\") echo '{\"blocks\":"+blocks+"}' ;; esac\n")
		t.Setenv("PATH", w.path)
		closeIfEmpty("session:launched")
		data, _ := os.ReadFile(log)
		killed := strings.Contains(string(data), "kill session:launched")
		if killed != (blocks == `[]`) {
			t.Fatalf("blocks %s: killed = %v\n%s", blocks, killed, data)
		}
	}
}
