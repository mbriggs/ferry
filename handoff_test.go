package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// These tests run the real binary between two fake homes. The "kant" side is
// reached through the exec: transport, so every remote operation goes through
// `ferry remote` and its JSON protocol exactly as it would over ssh.

var ferryBin string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "ferry-bin-")
	if err != nil {
		panic(err)
	}
	ferryBin = filepath.Join(dir, "ferry")
	build := []string{"build", "-o", ferryBin}
	// Most tests run ferry as a subprocess, which `go test -cover` can't see
	// on its own. Under -cover, the binary is built with coverage too and
	// every ferry run, the kant side included, writes its counters into the
	// directory go test merges into its report and -coverprofile.
	flag.Parse()
	if cov := flag.Lookup("test.gocoverdir"); testing.CoverMode() != "" && cov != nil && cov.Value.String() != "" {
		build = append(build, "-cover", "-covermode="+testing.CoverMode())
		os.Setenv("GOCOVERDIR", cov.Value.String())
	}
	if out, err := exec.Command("go", append(build, ".")...).CombinedOutput(); err != nil {
		panic(string(out))
	}
	// Tests run in parallel, so they can't each t.Setenv. The process env is
	// set once instead: code that runs in the test process, not as a ferry
	// subprocess, must never reach the real home (~/.claude/sessions,
	// ~/.local/state/ferry), the real login profile, or the real rex and
	// ssh. Each world hands its own HOME, PATH and stray-call log to the
	// ferry it runs.
	home := filepath.Join(dir, "home")
	guard := filepath.Join(dir, "guard")
	fakes = filepath.Join(dir, "fakes")
	stray := filepath.Join(dir, "stray-calls")
	os.MkdirAll(home, 0o755)
	// Ferry must never run the real agents or read the real login profile:
	// both depend on the machine running the tests (mise wrappers, slow with
	// a strange HOME). Fake agents report a version. rex and ssh would reach
	// the real Rex server and network, so a call that reaches their fakes is
	// logged and fails the test, even if ferry handled the error. A test
	// that needs more writes its own tools into its world's bin (fakeRex,
	// paneRex, fakeSSH). Worlds share these fakes because macOS checks a new
	// executable on its first exec, which cost every world about 150ms per
	// tool when each wrote its own.
	for tool, body := range map[string]string{
		"claude": "echo '2.1.0 (Claude Code)'\n",
		"codex":  "echo 'codex-cli 0.100.0'\n",
		"rex":    strayScript("rex"),
		"ssh":    strayScript("ssh"),
	} {
		writeFile(filepath.Join(fakes, tool), "#!/bin/sh\n"+body)
		writeFile(filepath.Join(guard, tool), "#!/bin/sh\n"+strayScript(tool))
	}
	realHome = os.Getenv("HOME")
	os.Setenv("HOME", home)
	os.Setenv("SHELL", "")
	os.Setenv("FERRY_PLAIN", "1") // line prompts the pty tests can answer
	os.Setenv("FERRY_TEST_STRAY", stray)
	basePath = os.Getenv("PATH")
	os.Setenv("PATH", guard+string(os.PathListSeparator)+basePath)
	code := m.Run()
	if data, err := os.ReadFile(stray); err == nil {
		fmt.Fprintf(os.Stderr, "a test reached a real tool from the test process:\n%s", data)
		code = 1
	}
	os.RemoveAll(dir)
	os.Exit(code)
}

// strayScript logs a call to tool in $FERRY_TEST_STRAY and fails it.
func strayScript(tool string) string {
	return "echo \"" + tool + " $*\" >> \"$FERRY_TEST_STRAY\"\necho 'unexpected " + tool + " call in a test' >&2\nexit 1\n"
}

func writeFile(p, script string) {
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		panic(err)
	}
	if err := os.WriteFile(p, []byte(script), 0o755); err != nil {
		panic(err)
	}
}

var (
	// realHome is HOME as go test started, for the go command's caches.
	realHome string
	// basePath is PATH as go test started, without the test process's guards.
	basePath string
	// fakes holds the tools a world's ferry finds unless the test writes its
	// own: fake agents, and a rex and ssh that fail.
	fakes string
)

var parallelTests sync.Map

type world struct {
	t                  *testing.T
	root, laptop, kant string
	path               string // PATH for ferry; see env
}

const sessionID = "11111111-2222-3333-4444-555555555555"

// newWorld makes a world for a test that runs in parallel with the others.
// Its ferry runs only as a subprocess, with the world's HOME and PATH.
func newWorld(t *testing.T) *world {
	t.Helper()
	if _, again := parallelTests.LoadOrStore(t, true); !again {
		t.Parallel() // once per test, which may make several worlds
	}
	return makeWorld(t)
}

// newSerialWorld is newWorld for a test that changes the test process's own
// environment (t.Setenv) to call ferry's code directly. Such a test can't
// run in parallel.
func newSerialWorld(t *testing.T) *world {
	t.Helper()
	return makeWorld(t)
}

func makeWorld(t *testing.T) *world {
	t.Helper()
	root := t.TempDir()
	w := &world{t: t, root: root, laptop: filepath.Join(root, "Users", "matt"), kant: filepath.Join(root, "home", "mbriggs")}
	for name, home := range map[string]string{"laptop": w.laptop, "kant": w.kant} {
		w.write(home, ".config/ferry/name", name+"\n")
	}
	w.write(w.laptop, ".config/ferry/hosts", "kant exec:"+w.kant+"\n")
	stray := filepath.Join(root, "stray-calls")
	t.Cleanup(func() {
		if data, err := os.ReadFile(stray); err == nil {
			t.Errorf("a test reached rex or ssh without a fake:\n%s", data)
		}
	})
	w.path = strings.Join([]string{filepath.Join(root, "bin"), fakes, basePath}, string(os.PathListSeparator))
	return w
}

// env is the environment for a ferry run as home's user: the world's PATH
// (its own tools, then the shared fakes, then the real PATH) and its log of
// stray calls.
func (w *world) env(home string, extra ...string) []string {
	env := append(os.Environ(), "HOME="+home, "PATH="+w.path, "FERRY_TEST_STRAY="+filepath.Join(w.root, "stray-calls"))
	return append(env, extra...)
}

func (w *world) write(home, rel, content string) {
	w.t.Helper()
	p := filepath.Join(home, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		w.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		w.t.Fatal(err)
	}
}

func (w *world) read(home, rel string) string {
	w.t.Helper()
	b, err := os.ReadFile(filepath.Join(home, rel))
	if err != nil {
		w.t.Fatal(err)
	}
	return string(b)
}

func (w *world) git(dir string, args ...string) string {
	w.t.Helper()
	out, err := git(dir, args...)
	if err != nil {
		w.t.Fatal(err)
	}
	return out
}

// ferry runs the binary as the laptop user, in dir.
func (w *world) ferry(dir string, args ...string) (string, error) {
	// A ferry that hangs (or a mutant stuck in a loop, under bin/mutate)
	// would outlive the test binary's timeout, with the `ferry remote` it
	// started, and keep a core busy. Each runs in its own process group,
	// killed whole just before the test binary would give up.
	ctx := context.Background()
	if deadline, ok := w.t.Deadline(); ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithDeadline(ctx, deadline.Add(-5*time.Second))
		defer cancel()
	}
	cmd := exec.CommandContext(ctx, ferryBin, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.Dir = dir
	cmd.Env = w.env(w.laptop)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	return out.String(), err
}

func (w *world) mustFerry(dir string, args ...string) string {
	w.t.Helper()
	out, err := w.ferry(dir, args...)
	if err != nil {
		w.t.Fatalf("ferry %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return out
}

// idleAppend adds a line to a transcript as an agent that has since gone
// idle: the file is dated a minute ago, so ferry doesn't wait for it to
// settle.
func (w *world) idleAppend(transcript, line string) {
	w.t.Helper()
	f, err := os.OpenFile(transcript, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		w.t.Fatal(err)
	}
	fmt.Fprintln(f, line)
	f.Close()
	old := time.Now().Add(-time.Minute)
	os.Chtimes(transcript, old, old)
}

func (w *world) approve(home, content string) {
	sum := sha256.Sum256([]byte(content))
	p := filepath.Join(home, ".config/ferry/approved")
	f, err := os.OpenFile(p, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		w.t.Fatal(err)
	}
	fmt.Fprintln(f, hex.EncodeToString(sum[:]))
	f.Close()
}

func (w *world) record(task string) Record {
	w.t.Helper()
	var r Record
	if err := json.Unmarshal([]byte(w.read(w.kant, ".local/state/ferry/tasks/"+task+".json")), &r); err != nil {
		w.t.Fatal(err)
	}
	return r
}

const restoreScript = "#!/bin/sh\nset -e\necho restored-$FERRY_DIRECTION > \"$FERRY_ROOT/.restored\"\ntest -f \"$FERRY_STATE/db.dump\"\necho \"db loaded from $(cat \"$FERRY_STATE/db.dump\")\"\n"
const dehydrateScript = "#!/bin/sh\nset -e\nmkdir -p \"$FERRY_STATE\"\necho dump-from-$(cat \"$HOME/.config/ferry/name\") > \"$FERRY_STATE/db.dump\"\necho dumped\n"

// laptopTask builds ~/src/app with a linked worktree src/app-wt/fix-auth full
// of local state, plus a Claude session for that worktree.
func (w *world) laptopTask() string {
	origin := filepath.Join(w.root, "origin.git")
	w.git(w.root, "init", "-q", "--bare", "--initial-branch=main", origin)
	app := filepath.Join(w.laptop, "src", "app")
	w.git(w.root, "clone", "-q", origin, app)
	w.write(app, "keep.txt", "a\n")
	w.write(app, "gone.txt", "b\n")
	w.write(app, ".gitignore", ".env\n.ferry/state/\nnode_modules/\n.restored\n")
	w.write(app, ".ferry/restore", restoreScript)
	w.write(app, ".ferry/dehydrate", dehydrateScript)
	w.git(app, "add", "-A")
	w.git(app, "commit", "-qm", "initial")
	w.git(app, "push", "-q", "origin", "HEAD:main")
	wt := filepath.Join(w.laptop, "src", "app-wt", "fix-auth")
	w.git(app, "worktree", "add", "-q", "-b", "fix-auth", wt)
	w.write(wt, "unpushed.txt", "local commit\n")
	w.git(wt, "add", "unpushed.txt")
	w.git(wt, "commit", "-qm", "unpushed")
	w.write(wt, "keep.txt", "staged\n")
	w.git(wt, "add", "keep.txt")
	w.write(wt, "keep.txt", "staged\nthen unstaged\n")
	w.git(wt, "rm", "-q", "gone.txt")
	w.write(wt, "untracked.txt", "new\n")
	w.write(wt, ".env", "SECRET=1\n")
	w.write(wt, "node_modules/x/index.js", "dep\n")

	slug := claudeSlug(wt)
	lines := []map[string]any{
		{"type": "user", "cwd": wt, "message": "start"},
		{"type": "assistant", "cwd": wt, "tool": map[string]any{"file_path": wt + "/keep.txt", "command": "cd " + wt + " && ls"}},
		{"type": "file-history-snapshot", "snapshot": map[string]any{"realParentDir": wt}},
	}
	var b strings.Builder
	for _, l := range lines {
		j, _ := json.Marshal(l)
		b.Write(j)
		b.WriteByte('\n')
	}
	w.write(w.laptop, ".claude/projects/"+slug+"/"+sessionID+".jsonl", b.String())
	w.write(w.laptop, ".claude/projects/"+slug+"/"+sessionID+"/subagents/agent-1.jsonl", `{"cwd":"`+wt+`"}`+"\n")
	w.write(w.laptop, ".claude/file-history/"+sessionID+"/abc@v1", "backup of "+wt+"\n")
	// An idle session: ferry waits for a transcript written seconds ago.
	old := time.Now().Add(-time.Minute)
	os.Chtimes(filepath.Join(w.laptop, ".claude/projects", slug, sessionID+".jsonl"), old, old)
	w.approve(w.laptop, dehydrateScript)
	w.approve(w.kant, restoreScript)
	return wt
}

func TestRoundTrip(t *testing.T) {
	w := newWorld(t)
	wt := w.laptopTask()
	task := taskKey("src/app-wt/fix-auth")
	before, err := fingerprint(wt)
	if err != nil {
		t.Fatal(err)
	}

	out := w.mustFerry(wt, "to", "kant", "--yes", "--no-launch")
	t.Log(out)

	kwt := filepath.Join(w.kant, "src", "app-wt", "fix-auth")
	if got, _ := fingerprint(kwt); got != before {
		t.Fatalf("kant worktree differs\nwant %s\ngot  %s", before, got)
	}
	if got := w.read(w.kant, "src/app-wt/fix-auth/.env"); got != "SECRET=1\n" {
		t.Fatalf(".env = %q", got)
	}
	if st, _ := os.Stat(filepath.Join(kwt, ".env")); st.Mode().Perm() != 0o600 {
		t.Fatalf(".env mode %v, want 0600", st.Mode().Perm())
	}
	if exists(filepath.Join(kwt, "node_modules")) {
		t.Fatal("node_modules should not travel")
	}
	if got := w.read(w.kant, "src/app-wt/fix-auth/.restored"); got != "restored-to\n" {
		t.Fatalf("restore marker %q", got)
	}
	if !strings.Contains(out, "db loaded from dump-from-laptop") {
		t.Fatal("restore summary missing from output")
	}
	if w.git(filepath.Join(w.kant, "src", "app"), "remote", "get-url", "origin") != filepath.Join(w.root, "origin.git") {
		t.Fatal("new repo on kant should get the laptop's origin")
	}
	kslug := claudeSlug(kwt)
	transcript := w.read(w.kant, ".claude/projects/"+kslug+"/"+sessionID+".jsonl")
	if strings.Contains(transcript, `"cwd":"`+wt) || !strings.Contains(transcript, `"cwd":"`+kwt+`"`) {
		t.Fatalf("cwd not rewritten:\n%s", transcript)
	}
	// The transcript keeps the laptop's mtime: a copy dated when ferry wrote
	// it would read as an agent still exiting, and the next handoff would
	// wait for it to settle.
	src, _ := os.Stat(filepath.Join(w.laptop, ".claude/projects", claudeSlug(wt), sessionID+".jsonl"))
	if dst, _ := os.Stat(filepath.Join(w.kant, ".claude/projects", kslug, sessionID+".jsonl")); !dst.ModTime().Equal(src.ModTime()) {
		t.Fatalf("transcript mtime %v on kant, %v on the laptop", dst.ModTime(), src.ModTime())
	}
	if esc := strings.Repeat("\\u0026", 2); !strings.Contains(transcript, "cd "+wt+" "+esc+" ls") && !strings.Contains(transcript, "cd "+wt+" && ls") {
		t.Fatalf("free text should keep the old path:\n%s", transcript)
	}
	if sub := w.read(w.kant, ".claude/projects/"+kslug+"/"+sessionID+"/subagents/agent-1.jsonl"); !strings.Contains(sub, kwt) {
		t.Fatalf("subagent not rewritten: %s", sub)
	}
	if fh := w.read(w.kant, ".claude/file-history/"+sessionID+"/abc@v1"); fh != "backup of "+wt+"\n" {
		t.Fatalf("file history must be copied untouched, got %q", fh)
	}
	if !exists(filepath.Join(w.laptop, ".claude/projects", claudeSlug(wt), sessionID+".jsonl")) {
		t.Fatal("the laptop should keep its transcript")
	}
	r := w.record(task)
	if r.Owner != "kant" || r.State != stateOwned || r.Attempt != "" {
		t.Fatalf("record after to: %+v", r)
	}
	// Every write moves the generation on, or a run holding a stale copy
	// of the record could still write it.
	firstGen := r.Generation
	if firstGen <= 0 {
		t.Fatalf("generation after a handoff: %d", firstGen)
	}
	if !strings.Contains(out, "claude --resume "+sessionID) {
		t.Fatal("expected a resume command")
	}

	// The agent works on kant: a commit, a new file, more transcript.
	w.write(kwt, "from-kant.txt", "kant work\n")
	w.git(kwt, "add", "from-kant.txt")
	w.git(kwt, "commit", "-qm", "kant commit")
	w.write(kwt, "kant-untracked.txt", "wip\n")
	w.idleAppend(filepath.Join(w.kant, ".claude/projects", kslug, sessionID+".jsonl"), fmt.Sprintf(`{"type":"assistant","cwd":%q,"message":"did kant work"}`, kwt))
	kantFP, _ := fingerprint(kwt)

	w.approve(w.laptop, restoreScript)
	w.approve(w.kant, dehydrateScript)
	out = w.mustFerry(w.laptop, "from", "kant", "fix-auth", "--yes")
	t.Log(out)

	if got, _ := fingerprint(wt); got != kantFP {
		t.Fatalf("laptop worktree differs from kant's\nwant %s\ngot  %s", kantFP, got)
	}
	back := w.read(w.laptop, ".claude/projects/"+claudeSlug(wt)+"/"+sessionID+".jsonl")
	if !strings.Contains(back, "did kant work") || strings.Contains(back, `"cwd":"`+kwt) {
		t.Fatalf("transcript did not come back rewritten:\n%s", back)
	}
	if got := w.read(w.laptop, "src/app-wt/fix-auth/.ferry/state/db.dump"); got != "dump-from-kant\n" {
		t.Fatalf("state from kant's dehydrate: %q", got)
	}
	if !exists(filepath.Join(w.kant, ".claude/projects", kslug, sessionID+".jsonl")) {
		t.Fatal("kant should keep its transcript")
	}
	if r := w.record(task); r.Owner != "laptop" || r.State != stateOwned || r.Attempt != "" || r.Generation <= firstGen {
		t.Fatalf("record after from: %+v", r)
	}

	// A second trip to kant reuses its fenced worktree, which hasn't changed.
	w.write(wt, "second.txt", "again\n")
	secondFP, _ := fingerprint(wt)
	w.mustFerry(wt, "to", "kant", "--yes", "--no-launch")
	if got, _ := fingerprint(kwt); got != secondFP {
		t.Fatalf("second trip: kant worktree differs")
	}

	out = w.mustFerry(w.laptop, "ls", "kant")
	if !strings.Contains(out, "fix-auth") || !strings.Contains(out, "kant") {
		t.Fatalf("ls output:\n%s", out)
	}
}

func TestRefusesWhileAgentRunning(t *testing.T) {
	w := newWorld(t)
	wt := w.laptopTask()
	w.write(w.laptop, ".claude/sessions/99.json", fmt.Sprintf(`{"pid":%d,"sessionId":%q,"status":"idle"}`, os.Getpid(), sessionID))
	out, err := w.ferry(wt, "to", "kant", "--yes", "--no-launch")
	if err == nil || !strings.Contains(out, "exit the agent") {
		t.Fatalf("want a refusal, got err=%v\n%s", err, out)
	}
	if exists(filepath.Join(w.kant, "src", "app")) {
		t.Fatal("nothing should move while the agent runs")
	}
}

// An agent that just exited may still be writing its last lines, so a
// transcript written moments ago is waited on, not refused or sent half done.
func TestWaitsForAJustWrittenTranscript(t *testing.T) {
	w := newWorld(t)
	wt := w.laptopTask()
	now := time.Now()
	os.Chtimes(filepath.Join(w.laptop, ".claude/projects", claudeSlug(wt), sessionID+".jsonl"), now, now)
	out := w.mustFerry(wt, "to", "kant", "--yes", "--no-launch")
	if !strings.Contains(out, "waiting for it to settle") {
		t.Fatalf("expected a wait for the transcript:\n%s", out)
	}
}

func TestRefusesToOverwriteUnhandedChanges(t *testing.T) {
	w := newWorld(t)
	wt := w.laptopTask()
	w.mustFerry(wt, "to", "kant", "--yes", "--no-launch")
	w.mustFerry(w.laptop, "from", "kant", "fix-auth", "--yes")
	// Someone edits kant's copy after it was handed back.
	kwt := filepath.Join(w.kant, "src", "app-wt", "fix-auth")
	w.write(kwt, "stray.txt", "edited on kant after handing off\n")
	out, err := w.ferry(wt, "to", "kant", "--yes", "--no-launch")
	if err == nil || !strings.Contains(out, "weren't handed off") || !strings.Contains(out, "stray.txt") {
		t.Fatalf("want a refusal naming stray.txt, got err=%v\n%s", err, out)
	}
	if w.read(kwt, "stray.txt") == "" {
		t.Fatal("stray edit must survive")
	}
	// The check runs before the handoff is recorded or the agent stopped.
	if r := w.record(taskKey("src/app-wt/fix-auth")); r.State != stateOwned || r.Owner != "laptop" {
		t.Fatalf("a refusal before the handoff must leave the record alone: %+v", r)
	}
}

// At a terminal, ferry offers to save the receiver's unsent work to a
// branch there, then hands off over it.
func TestSavesUnhandedChangesToABranch(t *testing.T) {
	w := newWorld(t)
	wt := w.laptopTask()
	w.mustFerry(wt, "to", "kant", "--yes", "--no-launch")
	w.mustFerry(w.laptop, "from", "kant", "fix-auth", "--yes")
	kwt := filepath.Join(w.kant, "src", "app-wt", "fix-auth")
	w.write(kwt, "stray.txt", "edited on kant after handing off\n")
	// Without dehydrate, the save is the only prompt.
	os.Remove(filepath.Join(wt, ".ferry", "dehydrate"))

	cmd := inTerminal(t, ferryBin, "to", "kant", "--yes", "--no-launch")
	cmd.Dir = wt
	cmd.Env = w.env(w.laptop)
	out, err := typeAt(cmd, "pick 1-2: ", "1\n")
	if err != nil {
		t.Fatalf("to: %v\n%s", err, out)
	}
	if exists(filepath.Join(kwt, "stray.txt")) {
		t.Fatalf("kant's copy wasn't replaced:\n%s", out)
	}
	repo := filepath.Join(w.kant, "src", "app")
	saved := w.git(repo, "for-each-ref", "--format=%(refname:short)", "refs/heads/ferry-saved/")
	if !strings.HasPrefix(saved, "ferry-saved/fix-auth-") || strings.Contains(saved, "\n") {
		t.Fatalf("want one saved branch, got %q\n%s", saved, out)
	}
	if got := w.git(repo, "show", saved+":stray.txt"); got != "edited on kant after handing off" {
		t.Fatalf("saved branch has stray.txt = %q", got)
	}
	if r := w.record(taskKey("src/app-wt/fix-auth")); r.Owner != "kant" || r.State != stateOwned {
		t.Fatalf("handoff didn't finish: %+v", r)
	}
}

// --check reports what the handoff would refuse or ask about while the agent
// still runs, and leaves the agent, the record and both worktrees alone.
func TestCheckRunsWhileTheAgentDoes(t *testing.T) {
	w := newWorld(t)
	wt := w.laptopTask()
	w.mustFerry(wt, "to", "kant", "--yes", "--no-launch")
	w.mustFerry(w.laptop, "from", "kant", "fix-auth", "--yes")
	kwt := filepath.Join(w.kant, "src", "app-wt", "fix-auth")
	w.write(kwt, "stray.txt", "edited on kant after handing off\n")
	w.write(w.laptop, ".claude/sessions/99.json", fmt.Sprintf(`{"pid":%d,"sessionId":%q,"status":"idle"}`, os.Getpid(), sessionID))

	out := w.mustFerry(wt, "to", "kant", "--check")
	if !strings.Contains(out, "stray.txt") || !strings.Contains(out, "offer to save") || !strings.Contains(out, "nothing found") {
		t.Fatalf("check output:\n%s", out)
	}
	if w.read(kwt, "stray.txt") == "" {
		t.Fatal("check must not touch kant's worktree")
	}
	if r := w.record(taskKey("src/app-wt/fix-auth")); r.State != stateOwned || r.Owner != "laptop" {
		t.Fatalf("check must not change the record: %+v", r)
	}
}

// Pulling, the receiver is the laptop, but the command to run names kant.
func TestCheckOnAPullNamesTheRemote(t *testing.T) {
	w := newWorld(t)
	wt := w.laptopTask()
	w.mustFerry(wt, "to", "kant", "--yes", "--no-launch")
	w.write(wt, "stray.txt", "edited on the laptop after handing off\n")
	out := w.mustFerry(w.laptop, "from", "kant", "fix-auth", "--check")
	if !strings.Contains(out, "`ferry from kant fix-auth` will offer to save it to a branch on laptop") {
		t.Fatalf("check output:\n%s", out)
	}
}

// A repo at the same path on kant that shares no history with this one is
// someone else's work, so the handoff stops before touching it. One with no
// commits yet has nothing to lose, and the handoff fills it.
func TestRepoAlreadyAtThePathOnKant(t *testing.T) {
	w := newWorld(t)
	wt := w.laptopTask()
	other := filepath.Join(w.kant, "src", "app")
	w.git(w.root, "init", "-q", "--initial-branch=main", other)
	w.write(other, "theirs.txt", "unrelated\n")
	w.git(other, "add", "-A")
	w.git(other, "commit", "-qm", "unrelated")
	head := w.git(other, "rev-parse", "HEAD")
	out, err := w.ferry(wt, "to", "kant", "--yes", "--no-launch")
	if err == nil || !strings.Contains(out, "different repository") {
		t.Fatalf("want a refusal, got err=%v\n%s", err, out)
	}
	if w.git(other, "rev-parse", "HEAD") != head || exists(filepath.Join(w.kant, "src", "app-wt")) {
		t.Fatal("the refused handoff changed kant")
	}

	os.RemoveAll(other)
	w.git(w.root, "init", "-q", "--initial-branch=main", other)
	w.mustFerry(wt, "to", "kant", "--yes", "--no-launch")
	if w.read(w.kant, "src/app-wt/fix-auth/unpushed.txt") != "local commit\n" {
		t.Fatal("the handoff into an empty repo didn't land the code")
	}
}

// A worktree under ~/src of a repo kept elsewhere can't be recreated at the
// same place on kant, so it's refused like any work outside ~/src.
func TestRefusesAWorktreeOfARepoOutsideSrc(t *testing.T) {
	w := newWorld(t)
	repo := filepath.Join(w.laptop, "elsewhere", "app")
	w.git(w.root, "init", "-q", "--initial-branch=main", repo)
	w.write(repo, "a.txt", "a\n")
	w.git(repo, "add", "-A")
	w.git(repo, "commit", "-qm", "initial")
	wt := filepath.Join(w.laptop, "src", "app-wt")
	w.git(repo, "worktree", "add", "-q", "-b", "wt", wt)
	out, err := w.ferry(wt, "to", "kant", "--yes", "--no-launch")
	if err == nil || !strings.Contains(out, "outside ~/src") {
		t.Fatalf("want a refusal, got err=%v\n%s", err, out)
	}
}

// versionLess decides whether a handoff refuses an agent older than the
// session's, so versions of different lengths and prefixes must compare.
func TestVersionLess(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want bool
	}{
		{"2.1.0", "2.1.1", true},
		{"2.1.1", "2.1.0", false},
		{"2.1.0", "2.1.0", false},
		{"2.9.0", "2.10.0", true},
		{"2.10.0", "2.9.0", false},
		{"2.9.0", "2.8.0", false},
		{"2.8.0", "2.9.0", true},
		{"2.1", "2.1.0", false},
		{"2.1.0", "2.1", false},
		{"2.1", "2.1.1", true},
		{"2.1.1", "2.1", false},
		{"v2.0.0", "1.0.0", false},
		{"1.0.0", "v2.0.0", true},
	} {
		if got := versionLess(c.a, c.b); got != c.want {
			t.Errorf("versionLess(%q, %q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}

func TestUnapprovedHookIsSkipped(t *testing.T) {
	w := newWorld(t)
	wt := w.laptopTask()
	os.Remove(filepath.Join(w.kant, ".config/ferry/approved"))
	out := w.mustFerry(wt, "to", "kant", "--yes", "--no-launch")
	if !strings.Contains(out, "isn't approved") || !strings.Contains(out, "skipped") {
		t.Fatalf("expected the restore hook to be shown and skipped:\n%s", out)
	}
	if exists(filepath.Join(w.kant, "src/app-wt/fix-auth/.restored")) {
		t.Fatal("unapproved restore ran")
	}
	if !strings.Contains(out, "environment here is cold") {
		t.Fatal("resume note should say the environment is cold")
	}
}

func TestOwnerCheck(t *testing.T) {
	w := newWorld(t)
	wt := w.laptopTask()
	w.mustFerry(wt, "to", "kant", "--yes", "--no-launch")
	out, err := w.ferry(wt, "to", "kant", "--yes", "--no-launch")
	if err == nil || !strings.Contains(out, "kant owns fix-auth") {
		t.Fatalf("second push should be refused, got err=%v\n%s", err, out)
	}
}

func TestCodexSession(t *testing.T) {
	w := newWorld(t)
	wt := w.laptopTask()
	os.RemoveAll(filepath.Join(w.laptop, ".claude"))
	id := "019a0000-aaaa-bbbb-cccc-dddddddddddd"
	rel := ".codex/sessions/2026/11/06/rollout-2026-11-06T10-00-00-" + id + ".jsonl"
	w.write(w.laptop, rel,
		fmt.Sprintf(`{"type":"session_meta","payload":{"id":%q,"cwd":%q,"runtime_workspace_roots":[%q]}}`+"\n", id, wt, wt)+
			fmt.Sprintf(`{"type":"turn_context","payload":{"cwd":%q,"model":"gpt-test"}}`+"\n", wt))
	out := w.mustFerry(wt, "to", "kant", "--yes", "--no-launch")
	kwt := filepath.Join(w.kant, "src", "app-wt", "fix-auth")
	got := w.read(w.kant, rel)
	if strings.Contains(got, wt) || !strings.Contains(got, kwt) {
		t.Fatalf("rollout not rewritten:\n%s", got)
	}
	if !strings.Contains(out, `codex --no-daemon -c 'model="gpt-test"' resume `+id) {
		t.Fatalf("expected a codex resume with the recorded model:\n%s", out)
	}
	if !exists(filepath.Join(w.laptop, rel)) {
		t.Fatal("the laptop should keep its rollout")
	}
}
