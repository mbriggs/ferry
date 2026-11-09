package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const kantSessionID = "22222222-3333-4444-5555-666666666666"

// kantTool makes a repo that was born on kant and never moved, with a Claude
// session that was opened in ~/src and then went to work in ~/src/tool.
func (w *world) kantTool() (wt string) {
	wt = filepath.Join(w.kant, "src", "tool")
	w.git(w.root, "init", "-q", "--initial-branch=main", wt)
	w.write(wt, "main.go", "package main\n")
	w.git(wt, "add", "-A")
	w.git(wt, "commit", "-qm", "initial")
	w.write(wt, "wip.txt", "not committed\n")

	src := filepath.Join(w.kant, "src")
	var b strings.Builder
	for _, l := range []map[string]any{
		{"type": "user", "cwd": src, "message": "look at tool"},
		{"type": "assistant", "cwd": wt, "tool": map[string]any{"file_path": wt + "/main.go"}},
	} {
		j, _ := json.Marshal(l)
		b.Write(j)
		b.WriteByte('\n')
	}
	rel := ".claude/projects/" + claudeSlug(src) + "/" + kantSessionID + ".jsonl"
	w.write(w.kant, rel, b.String())
	old := time.Now().Add(-time.Minute)
	os.Chtimes(filepath.Join(w.kant, rel), old, old)
	return wt
}

func TestPullAWorktreeThatNeverMoved(t *testing.T) {
	w := newWorld(t)
	w.kantTool()
	out := w.mustFerry(w.laptop, "from", "kant", "src/tool", "--session", kantSessionID, "--yes", "--skip-scripts", "--no-launch")
	t.Log(out)

	lwt := filepath.Join(w.laptop, "src", "tool")
	if got := w.read(lwt, "wip.txt"); got != "not committed\n" {
		t.Fatalf("uncommitted work didn't come over: %q", got)
	}
	// The session started in ~/src, so it resumes from ~/src here too.
	lsrc := filepath.Join(w.laptop, "src")
	transcript := w.read(w.laptop, ".claude/projects/"+claudeSlug(lsrc)+"/"+kantSessionID+".jsonl")
	if strings.Contains(transcript, w.kant) || !strings.Contains(transcript, lwt) {
		t.Fatalf("transcript not rewritten for the laptop:\n%s", transcript)
	}
	if !strings.Contains(out, "cd ~/src && claude --resume "+kantSessionID) {
		t.Fatalf("expected a resume from ~/src:\n%s", out)
	}
	if r := w.record(taskKey("src/tool")); r.Owner != "laptop" || r.State != stateOwned {
		t.Fatalf("record: %+v", r)
	}
}

func TestSessionFromAnotherFolderIsRefused(t *testing.T) {
	w := newWorld(t)
	w.kantTool()
	other := filepath.Join(w.kant, "src", "other")
	w.git(w.root, "init", "-q", "--initial-branch=main", other)
	w.write(other, "x", "x\n")
	w.git(other, "add", "-A")
	w.git(other, "commit", "-qm", "x")
	out, err := w.ferry(w.laptop, "from", "kant", "src/other", "--session", kantSessionID, "--yes", "--no-launch")
	if err == nil || !strings.Contains(out, "belongs to ~/src, not this worktree") {
		t.Fatalf("a session working in src/tool shouldn't move with src/other:\n%s", out)
	}
}

// A running agent names its worktree by where it is working now, not where
// it was opened.
func TestAgentsReportTheWorktreeTheyAreIn(t *testing.T) {
	w := newWorld(t)
	w.kantTool()
	sleep := exec.Command("sleep", "30")
	sleep.Env = []string{"PATH=/usr/bin:/bin"}
	if err := sleep.Start(); err != nil {
		t.Fatal(err)
	}
	defer sleep.Process.Kill()
	w.write(w.kant, fmt.Sprintf(".claude/sessions/%d.json", sleep.Process.Pid),
		fmt.Sprintf(`{"pid":%d,"sessionId":%q,"cwd":%q,"status":"idle"}`, sleep.Process.Pid, kantSessionID, filepath.Join(w.kant, "src")))

	cmd := exec.Command(ferryBin, "remote", "agents")
	cmd.Env = w.env(w.kant)
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	var agents []AgentInfo
	if err := json.Unmarshal(out, &agents); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	if len(agents) != 1 || agents[0].Task == nil || agents[0].Task.WtRel != "src/tool" || agents[0].Session.ID != kantSessionID {
		t.Fatalf("agents: %s", out)
	}
}

type fakeHub struct {
	Machine
	agents []AgentInfo
	recs   []Record
}

func (f fakeHub) Agents() ([]AgentInfo, error)  { return f.agents, nil }
func (f fakeHub) RecordList() ([]Record, error) { return f.recs, nil }
func (f fakeHub) Worktree(rel string) (task, error) {
	return task{Key: taskKey(rel), Name: filepath.Base(rel), WtRel: rel, RepoRel: rel}, nil
}

func TestChoosePullMatchesAgentsRecordsAndPaths(t *testing.T) {
	tool := task{Key: taskKey("src/tool"), Name: "tool", WtRel: "src/tool", RepoRel: "src/tool"}
	hub := fakeHub{
		agents: []AgentInfo{{Session: Session{Agent: agentClaude, ID: kantSessionID}, Task: &tool, Rex: "tooling"}},
		recs: []Record{
			{Task: taskKey("src/app-wt/fix-auth"), Name: "fix-auth", WtRel: "src/app-wt/fix-auth", Owner: "kant", State: stateOwned},
			{Task: taskKey("src/mine"), Name: "mine", WtRel: "src/mine", Owner: "laptop", State: stateOwned},
		},
	}
	for name, want := range map[string]string{
		"tool": "src/tool", "tooling": "src/tool", "~/src/tool": "src/tool", kantSessionID[:8]: "src/tool",
		"fix-auth": "src/app-wt/fix-auth", "src/new/thing": "src/new/thing",
	} {
		p, err := choosePull(hub, "laptop", "kant", name)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if p.t.WtRel != want {
			t.Fatalf("%s picked %s, want %s", name, p.t.WtRel, want)
		}
		if (want == "src/tool") != (p.session == kantSessionID) {
			t.Fatalf("%s: session %q", name, p.session)
		}
	}
	if _, err := choosePull(hub, "laptop", "kant", "mine"); err == nil {
		t.Fatal("a task the laptop already owns isn't something to pull")
	}
}
