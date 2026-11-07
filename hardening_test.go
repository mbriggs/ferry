package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTaskKeysDontCollide(t *testing.T) {
	if taskKey("src/foo-bar") == taskKey("src/foo.bar") || taskKey("src/a-b/c") == taskKey("src/a/b-c") {
		t.Fatal("distinct worktrees share a task key")
	}
	if taskKey("src/app") != taskKey("src/app") {
		t.Fatal("task keys aren't stable")
	}
}

func TestSessionFromAnotherWorktreeIsRefused(t *testing.T) {
	w := newWorld(t)
	wt := w.laptopTask()
	other := filepath.Join(w.laptop, "src", "other")
	otherID := "22222222-3333-4444-5555-666666666666"
	w.write(w.laptop, ".claude/projects/"+claudeSlug(other)+"/"+otherID+".jsonl", `{"cwd":"`+other+`"}`+"\n")
	out, err := w.ferry(wt, "to", "kant", "--yes", "--no-launch", "--session", otherID)
	if err == nil || !strings.Contains(out, "not this worktree") {
		t.Fatalf("want a refusal, got err=%v\n%s", err, out)
	}
}

func TestStagedStateIsRefused(t *testing.T) {
	w := newWorld(t)
	wt := w.laptopTask()
	w.write(wt, ".ferry/state/db.dump", "secret rows\n")
	w.git(wt, "add", "-f", ".ferry/state/db.dump")
	out, err := w.ferry(wt, "to", "kant", "--yes", "--no-launch")
	if err == nil || !strings.Contains(out, "git rm -r --cached .ferry/state") {
		t.Fatalf("want a refusal, got err=%v\n%s", err, out)
	}
}

func TestStateNeverBecomesABlob(t *testing.T) {
	w := newWorld(t)
	wt := w.laptopTask()
	// Not ignored, untracked: the case where `git add -A` would store it.
	w.write(wt, ".gitignore", ".env\nnode_modules/\n.restored\n")
	content := "unique dump contents 8c1f\n"
	w.write(wt, ".ferry/state/db.dump", content)
	tmp := filepath.Join(w.root, "dump")
	os.WriteFile(tmp, []byte(content), 0o600)
	sha := w.git(wt, "hash-object", tmp)
	if _, err := worktreeTree(wt); err != nil {
		t.Fatal(err)
	}
	if _, err := git(wt, "cat-file", "-e", sha); err == nil {
		t.Fatal(".ferry/state content was written into the object database")
	}
}

func TestExcludeKeepsIgnoredFilesBehind(t *testing.T) {
	w := newWorld(t)
	wt := w.laptopTask()
	w.write(wt, ".gitignore", ".env\nconfig/\nnode_modules/\n.restored\n.ferry/state/\n")
	w.write(wt, ".ferry/exclude", "config/big/\n*.log\n")
	w.write(wt, "config/secrets.json", "{}\n")
	w.write(wt, "config/big/blob", "large\n")
	w.write(wt, "config/run.log", "noise\n")
	w.write(wt, "node_modules/pkg/.env", "deep in deps\n")
	got, err := (&Local{Home: w.laptop}).ListExtras("src/app-wt/fix-auth")
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range got.Files {
		names = append(names, e.Path)
	}
	list := strings.Join(names, ",")
	if !strings.Contains(list, "config/secrets.json") || !strings.Contains(list, ".env") {
		t.Fatalf("missing ignored files: %s", list)
	}
	if strings.Contains(list, "blob") || strings.Contains(list, "run.log") || strings.Contains(list, "node_modules") {
		t.Fatalf("sent files it shouldn't: %s", list)
	}
}

func TestFreshTranscriptCountsAsSettling(t *testing.T) {
	home := t.TempDir()
	l := &Local{Home: home}
	rel := ".claude/projects/-x/" + sessionID + ".jsonl"
	os.MkdirAll(filepath.Join(home, ".claude/projects/-x"), 0o755)
	os.WriteFile(filepath.Join(home, rel), []byte("{}\n"), 0o600)
	live, err := l.Liveness(Session{Agent: agentClaude, ID: sessionID, Files: []string{rel}})
	if err != nil || !live.Live || !live.Settling {
		t.Fatalf("a transcript written just now should be settling: %+v %v", live, err)
	}
}
