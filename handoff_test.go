package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
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
	if out, err := exec.Command("go", "build", "-o", ferryBin, ".").CombinedOutput(); err != nil {
		panic(string(out))
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

type world struct {
	t                  *testing.T
	root, laptop, kant string
}

const sessionID = "11111111-2222-3333-4444-555555555555"

func newWorld(t *testing.T) *world {
	t.Helper()
	root := t.TempDir()
	w := &world{t: t, root: root, laptop: filepath.Join(root, "Users", "matt"), kant: filepath.Join(root, "home", "mbriggs")}
	for name, home := range map[string]string{"laptop": w.laptop, "kant": w.kant} {
		w.write(home, ".config/ferry/name", name+"\n")
	}
	w.write(w.laptop, ".config/ferry/hosts", "kant exec:"+w.kant+"\n")
	return w
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
	cmd := exec.Command(ferryBin, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "HOME="+w.laptop)
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
	w.approve(w.laptop, dehydrateScript)
	w.approve(w.kant, restoreScript)
	return wt
}

func TestRoundTrip(t *testing.T) {
	w := newWorld(t)
	wt := w.laptopTask()
	task := "src-app-wt-fix-auth"
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
	if esc := strings.Repeat("\\u0026", 2); !strings.Contains(transcript, "cd "+wt+" "+esc+" ls") && !strings.Contains(transcript, "cd "+wt+" && ls") {
		t.Fatalf("free text should keep the old path:\n%s", transcript)
	}
	if sub := w.read(w.kant, ".claude/projects/"+kslug+"/"+sessionID+"/subagents/agent-1.jsonl"); !strings.Contains(sub, kwt) {
		t.Fatalf("subagent not rewritten: %s", sub)
	}
	if fh := w.read(w.kant, ".claude/file-history/"+sessionID+"/abc@v1"); fh != "backup of "+wt+"\n" {
		t.Fatalf("file history must be copied untouched, got %q", fh)
	}
	if exists(filepath.Join(w.laptop, ".claude/projects", claudeSlug(wt), sessionID+".jsonl")) {
		t.Fatal("laptop transcript should be parked")
	}
	if r := w.record(task); r.Owner != "kant" || r.State != stateOwned || r.Generation != 3 {
		t.Fatalf("record after to: %+v", r)
	}
	if !strings.Contains(out, "claude --resume "+sessionID) {
		t.Fatal("expected a resume command")
	}

	// The agent works on kant: a commit, a new file, more transcript.
	w.write(kwt, "from-kant.txt", "kant work\n")
	w.git(kwt, "add", "from-kant.txt")
	w.git(kwt, "commit", "-qm", "kant commit")
	w.write(kwt, "kant-untracked.txt", "wip\n")
	f, _ := os.OpenFile(filepath.Join(w.kant, ".claude/projects", kslug, sessionID+".jsonl"), os.O_APPEND|os.O_WRONLY, 0)
	fmt.Fprintf(f, `{"type":"assistant","cwd":%q,"message":"did kant work"}`+"\n", kwt)
	f.Close()
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
	if exists(filepath.Join(w.kant, ".claude/projects", kslug, sessionID+".jsonl")) {
		t.Fatal("kant transcript should be parked")
	}
	if r := w.record(task); r.Owner != "laptop" || r.State != stateOwned || r.Generation != 6 {
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
	if exists(filepath.Join(w.laptop, rel)) {
		t.Fatal("laptop rollout should be parked")
	}
}
