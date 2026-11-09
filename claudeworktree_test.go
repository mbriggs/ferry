package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// claudeWorktree builds ~/src/app with a Claude worktree .claude/worktrees/feat
// and a session in it, shaped the way the session ended (see
// claudeworktree.go). It returns the main checkout and the worktree.
func (w *world) claudeWorktree(ended string) (app, wt string) {
	w.t.Helper()
	app = filepath.Join(w.laptop, "src", "app")
	w.git(w.root, "init", "-q", "--initial-branch=main", app)
	w.write(app, "main.go", "package main\n")
	w.write(app, ".gitignore", ".claude/worktrees/\n")
	w.git(app, "add", "-A")
	w.git(app, "commit", "-qm", "initial")
	wt = filepath.Join(app, ".claude", "worktrees", "feat")
	w.git(app, "worktree", "add", "-q", "-b", "worktree-feat", wt)
	w.write(wt, "feat.txt", "work in progress\n")
	made := time.Now().Add(-time.Hour)
	os.Chtimes(filepath.Join(wt, ".git"), made, made)

	state := map[string]any{"type": "worktree-state", "sessionId": sessionID, "worktreeSession": map[string]any{
		"originalCwd": app, "worktreePath": wt, "worktreeName": "feat", "worktreeBranch": "worktree-feat", "sessionId": sessionID,
	}}
	left := map[string]any{"type": "worktree-state", "sessionId": sessionID, "worktreeSession": nil}
	turn := map[string]any{"type": "assistant", "cwd": wt, "message": "edited feat.txt"}
	var lines []map[string]any
	folder := wt
	switch ended {
	case "closed": // claude --worktree, then the pane was closed (SIGTERM)
		lines = []map[string]any{{"type": "user", "cwd": wt, "message": "start"}, state, turn}
		cmd := exec.Command("true")
		cmd.Run()
		w.git(app, "worktree", "lock", "--reason", "claude session feat (pid "+itoa(cmd.ProcessState.Pid())+" start Sun Nov  8 02:30:03 2026)", wt)
	case "kept": // claude --worktree, then /exit and Keep: refiled with the main checkout
		lines = []map[string]any{{"type": "user", "cwd": wt, "message": "start"}, state, turn,
			{"type": "relocated", "sessionId": sessionID, "relocatedCwd": app}, left}
		folder = app
	case "entered": // claude in the main checkout, then EnterWorktree
		lines = []map[string]any{{"type": "user", "cwd": app, "message": "start"},
			{"type": "relocated", "sessionId": sessionID, "relocatedCwd": wt}, state, turn}
	default:
		w.t.Fatalf("unknown ending %q", ended)
	}
	var b strings.Builder
	for _, l := range lines {
		j, _ := json.Marshal(l)
		b.Write(j)
		b.WriteByte('\n')
	}
	rel := ".claude/projects/" + claudeSlug(folder) + "/" + sessionID + ".jsonl"
	w.write(w.laptop, rel, b.String())
	old := time.Now().Add(-time.Minute)
	os.Chtimes(filepath.Join(w.laptop, rel), old, old)
	return app, wt
}

func itoa(n int) string { b, _ := json.Marshal(n); return string(b) }

// lastWorktreeState is the worktreeSession of a transcript's last
// worktree-state line, nil when it ends outside a worktree.
func lastWorktreeState(t *testing.T, transcript string) any {
	t.Helper()
	var last any = "no worktree-state line"
	for line := range strings.Lines(transcript) {
		var m map[string]any
		if json.Unmarshal([]byte(line), &m) == nil && m["type"] == "worktree-state" {
			last = m["worktreeSession"]
		}
	}
	return last
}

func TestClaudeWorktreeSessionsMoveAndResumePlain(t *testing.T) {
	for _, ended := range []string{"closed", "kept", "entered"} {
		t.Run(ended, func(t *testing.T) {
			w := newWorld(t)
			_, wt := w.claudeWorktree(ended)
			out := w.mustFerry(wt, "to", "kant", "--yes", "--no-launch")
			t.Log(out)

			kwt := filepath.Join(w.kant, "src", "app", ".claude", "worktrees", "feat")
			if got := w.read(kwt, "feat.txt"); got != "work in progress\n" {
				t.Fatalf("worktree didn't come over: %q", got)
			}
			transcript := w.read(w.kant, ".claude/projects/"+claudeSlug(kwt)+"/"+sessionID+".jsonl")
			if !strings.Contains(transcript, "edited feat.txt") {
				t.Fatalf("the conversation didn't come over:\n%s", transcript)
			}
			if got := lastWorktreeState(t, transcript); got != nil {
				t.Fatalf("kant's copy must resume outside Claude's worktree handling, last worktree-state %v", got)
			}
			if !strings.Contains(out, "cd ~/src/app/.claude/worktrees/feat && claude --resume "+sessionID) {
				t.Fatalf("expected a resume in the worktree:\n%s", out)
			}
			if lock, _ := git(wt, "worktree", "list", "--porcelain"); strings.Contains(lock, "locked claude session") {
				t.Fatalf("the dead Claude's lock should be released:\n%s", lock)
			}
		})
	}
}

func TestToFromMainCheckoutNamesAWorktree(t *testing.T) {
	w := newWorld(t)
	app, _ := w.claudeWorktree("kept")

	// Not on a terminal, a bare `ferry to` sends the main checkout, and the
	// worktree's refiled session stays out of it.
	out := w.mustFerry(app, "to", "kant", "--yes", "--no-launch")
	t.Log(out)
	if !strings.Contains(out, "ferry to HOST NAME") || !strings.Contains(out, "moving code only") {
		t.Fatalf("the main checkout should go alone, with a hint:\n%s", out)
	}

	out = w.mustFerry(app, "to", "kant", "feat", "--yes", "--no-launch")
	t.Log(out)
	if !strings.Contains(out, "task       feat") && !strings.Contains(out, "feat  (src/app/.claude/worktrees/feat") {
		t.Fatalf("NAME should pick the worktree:\n%s", out)
	}
	kwt := filepath.Join(w.kant, "src", "app", ".claude", "worktrees", "feat")
	if !exists(filepath.Join(w.kant, ".claude/projects", claudeSlug(kwt), sessionID+".jsonl")) {
		t.Fatal("the worktree's session should be on kant")
	}

	if out, err := w.ferry(app, "to", "kant", "nope"); err == nil || !strings.Contains(out, `matches "nope"`) {
		t.Fatalf("an unknown NAME should refuse:\n%s", out)
	}
}

// NAME picks a worktree from a linked worktree too, not only from the main
// checkout: the shell you type in isn't always the one you mean to send.
func TestToFromALinkedWorktreeNamesAnother(t *testing.T) {
	w := newWorld(t)
	app, _ := w.claudeWorktree("kept")
	here := filepath.Join(app, ".claude", "worktrees", "other")
	w.git(app, "worktree", "add", "-q", "-b", "other", here)
	w.write(here, "other.txt", "not this one\n")

	w.mustFerry(here, "to", "kant", "feat", "--yes", "--no-launch")
	kapp := filepath.Join(w.kant, "src", "app", ".claude", "worktrees")
	if !exists(filepath.Join(kapp, "feat", "feat.txt")) || exists(filepath.Join(kapp, "other")) {
		t.Fatal("NAME from a linked worktree should send that worktree, and only it")
	}
}

func TestLiveClaudeWorktreeSessionRefusesWithKeepAdvice(t *testing.T) {
	w := newWorld(t)
	_, wt := w.claudeWorktree("closed")
	w.write(w.laptop, ".claude/sessions/99.json", `{"pid":`+itoa(os.Getpid())+`,"sessionId":"`+sessionID+`","status":"idle"}`)
	out, err := w.ferry(wt, "to", "kant", "--yes", "--no-launch")
	if err == nil || !strings.Contains(out, "close its pane") {
		t.Fatalf("want a refusal that says how to exit safely:\n%s", out)
	}
}

// The round trip leaves the laptop with one copy of the session, under the
// worktree: the one Claude refiled with the main checkout is parked.
func TestClaudeWorktreeSessionRoundTrip(t *testing.T) {
	w := newWorld(t)
	app, wt := w.claudeWorktree("kept")
	w.mustFerry(wt, "to", "kant", "--yes", "--no-launch")
	kwt := filepath.Join(w.kant, "src", "app", ".claude", "worktrees", "feat")
	w.appendKant(kwt, `{"type":"assistant","cwd":"`+kwt+`","message":"did kant work"}`)

	out := w.mustFerry(w.laptop, "from", "kant", "feat", "--yes", "--no-launch")
	t.Log(out)
	back := w.read(w.laptop, ".claude/projects/"+claudeSlug(wt)+"/"+sessionID+".jsonl")
	if !strings.Contains(back, "did kant work") || lastWorktreeState(t, back) != nil {
		t.Fatalf("the laptop should get kant's copy, outside Claude's worktree handling:\n%s", back)
	}
	if exists(filepath.Join(w.laptop, ".claude/projects", claudeSlug(app), sessionID+".jsonl")) {
		t.Fatal("the refiled copy should be parked, not left for a resume by id to find")
	}
	if !strings.Contains(out, "cd ~/src/app/.claude/worktrees/feat && claude --resume "+sessionID) {
		t.Fatalf("expected a resume in the worktree:\n%s", out)
	}
}

// An agent on kant that went into a Claude worktree itself is stopped with
// SIGTERM: /exit would delete the worktree or stop at Keep/Remove.
func TestPullStopsAClaudeWorktreeAgentWithSigterm(t *testing.T) {
	w := newWorld(t)
	_, wt := w.claudeWorktree("closed")
	w.mustFerry(wt, "to", "kant", "--yes", "--no-launch")
	kwt := filepath.Join(w.kant, "src", "app", ".claude", "worktrees", "feat")
	w.appendKant(kwt, `{"type":"worktree-state","worktreeSession":{"worktreePath":"`+kwt+`"}}`)

	agent := exec.Command("sleep", "60")
	if err := agent.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan struct{})
	go func() { agent.Wait(); close(exited) }() // reap it, as a real agent's shell would
	t.Cleanup(func() { agent.Process.Kill(); <-exited })
	pid := itoa(agent.Process.Pid)
	w.write(w.kant, ".claude/sessions/"+pid+".json", `{"pid":`+pid+`,"sessionId":"`+sessionID+`","status":"idle"}`)
	log := w.paneRex("feat")

	out := w.mustFerry(w.laptop, "from", "kant", "feat", "--yes", "--no-launch")
	t.Log(out)
	if data, _ := os.ReadFile(log); strings.Contains(string(data), "/exit") {
		t.Fatalf("typed /exit into a Claude worktree session:\n%s", data)
	}
	select {
	case <-exited:
	default:
		t.Fatal("the agent should have been stopped")
	}
	back := w.read(w.laptop, ".claude/projects/"+claudeSlug(wt)+"/"+sessionID+".jsonl")
	if lastWorktreeState(t, back) != nil {
		t.Fatalf("the laptop's copy should resume outside Claude's worktree handling:\n%s", back)
	}
}

// appendKant adds a line to kant's copy of the session in worktree kwt, as
// the agent there would, and backdates it so it reads as idle.
func (w *world) appendKant(kwt, line string) {
	w.t.Helper()
	p := filepath.Join(w.kant, ".claude/projects", claudeSlug(kwt), sessionID+".jsonl")
	f, err := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		w.t.Fatal(err)
	}
	f.WriteString(line + "\n")
	f.Close()
	old := time.Now().Add(-time.Minute)
	os.Chtimes(p, old, old)
}

func TestProjDirOnlyNamesAProjectFolder(t *testing.T) {
	def := filepath.Join(".claude", "projects", claudeSlug("/h/src/app"))
	for dir, want := range map[string]string{
		"":                             def,
		".claude/projects/-h-src-app-": ".claude/projects/-h-src-app-",
		".claude/projects/..":          def,
		".claude/projects/":            def,
		".claude/projects/a/b":         def,
		".ssh":                         def,
		"../.claude/projects/x":        def,
	} {
		if got := (Session{Cwd: "/h/src/app", ProjDir: dir}).projDir(); got != want {
			t.Errorf("ProjDir %q: projDir() = %q, want %q", dir, got, want)
		}
	}
}

func TestLeaveClaudeWorktree(t *testing.T) {
	in := `{"type":"worktree-state","worktreeSession":{"worktreePath":"/w"}}` + "\n"
	got := string(leaveClaudeWorktree([]byte(in), sessionID))
	if lastWorktreeState(t, got) != nil || !strings.HasPrefix(got, in) {
		t.Fatalf("want a null worktree-state appended:\n%s", got)
	}
	left := in + `{"type":"worktree-state","worktreeSession":null}` + "\n"
	if got := string(leaveClaudeWorktree([]byte(left), sessionID)); got != left {
		t.Fatalf("a session already out of its worktree is unchanged:\n%s", got)
	}
	plain := `{"type":"user","cwd":"/w"}` + "\n"
	if got := string(leaveClaudeWorktree([]byte(plain), sessionID)); got != plain {
		t.Fatalf("a plain session is unchanged:\n%s", got)
	}
}

// FuzzLeaveClaudeWorktree checks that whatever transcript arrives, the copy
// leaveClaudeWorktree makes reads as out of its worktree, and only adds to
// the end of what was there.
func FuzzLeaveClaudeWorktree(f *testing.F) {
	in := `{"type":"worktree-state","worktreeSession":{"worktreePath":"/w"}}`
	f.Add(in + "\n")
	f.Add(`{"type":"user","cwd":"/w"}` + "\n" + in)
	f.Add(in + "\n" + `{"type":"worktree-state","worktreeSession":null}` + "\n")
	f.Add(`{"type":"worktree\u002dstate","worktreeSession":{"worktreePath":"/w"}}` + "\n")
	f.Add(in + "\r\n" + `{"type":"worktree-state","worktreeSession":"x"}` + "\n")
	dir := f.TempDir()
	f.Fuzz(func(t *testing.T, transcript string) {
		out := leaveClaudeWorktree([]byte(transcript), sessionID)
		if !strings.HasPrefix(string(out), transcript) {
			t.Fatalf("changed what was there:\n%q\n%q", transcript, out)
		}
		p := filepath.Join(dir, "t.jsonl")
		if err := os.WriteFile(p, out, 0o600); err != nil {
			t.Fatal(err)
		}
		got, err := readClaudeTranscript(p)
		if err != nil {
			t.Fatal(err)
		}
		if got.inWorktree {
			t.Fatalf("still in worktree %q after leaving:\n%q", got.worktree, out)
		}
	})
}
