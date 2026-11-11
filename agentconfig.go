package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Claude reads its project config as it starts: .claude/settings*.json,
// skills and commands, hooks that run commands at once (SessionStart runs
// on resume), and .mcp.json, whose servers it starts. Those travel from the other machine, so before ferry
// starts Claude in this terminal it checks whether any of it changed during
// the handoff, and asks if so.
//
// claudeConfig fingerprints that config for the project folders in dirs:
// every file under <dir>/.claude except Claude's own worktrees,
// <dir>/.mcp.json, and the files its settings and MCP servers run
// (hookFiles). Keys are absolute paths, values sha256. home is the home
// that "~/" and $HOME name in those commands.
func claudeConfig(home string, dirs []string) map[string]string {
	out := map[string]string{}
	for _, dir := range dirs {
		cdir := filepath.Join(dir, ".claude")
		filepath.WalkDir(cdir, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if d.IsDir() && p == filepath.Join(cdir, "worktrees") {
				return filepath.SkipDir
			}
			if d.Type().IsRegular() {
				if sum, err := fileSHA(p); err == nil {
					out[p] = sum
				}
			}
			return nil
		})
		if sum, err := fileSHA(filepath.Join(dir, ".mcp.json")); err == nil {
			out[filepath.Join(dir, ".mcp.json")] = sum
		}
		for _, f := range hookFiles(home, dir) {
			if sum, err := fileSHA(f); err == nil {
				out[f] = sum
			}
		}
	}
	return out
}

// hookFiles returns the files inside dir that commands in dir's Claude
// settings (hooks, statusLine, apiKeyHelper and the rest), or MCP servers in
// its .mcp.json, run, such as "$CLAUDE_PROJECT_DIR/.claude/hooks/start.sh" or
// "bin/check", and the files those name in turn: a hook script that sources
// lib/env.sh or runs scripts/lint runs them too. fileRefs says which names
// count; a path like "$HERE/lib/env.sh" or "$(dirname "$0")/lib.sh" is
// tried without its prefix.
// Code a hook reaches without naming a file (make's Makefile, a package
// script, a glob) isn't found.
func hookFiles(home, dir string) []string {
	var out, queue []string
	seen := map[string]bool{}
	add := func(text, base string) {
		for _, p := range fileRefs(text, home, dir, base) {
			if !seen[p] && len(seen) < maxHookFiles {
				seen[p] = true
				out = append(out, p)
				queue = append(queue, p)
			}
		}
	}
	for _, cmd := range settingsStrings(dir) {
		add(cmd, dir)
	}
	// An MCP server's command and args can name a project script too.
	if text, ok := scriptText(filepath.Join(dir, ".mcp.json")); ok {
		add(text, dir)
	}
	for len(queue) > 0 {
		p := queue[0]
		queue = queue[1:]
		if data, ok := scriptText(p); ok {
			add(data, filepath.Dir(p))
		}
	}
	return out
}

// maxHookFiles bounds the files hookFiles follows, so a script that names
// half the repo doesn't make every start slow.
const maxHookFiles = 200

// settingsStrings returns every string value in dir's Claude settings.
// Hooks aren't the only commands Claude runs from them: statusLine,
// apiKeyHelper, awsAuthRefresh and otelHeadersHelper do too, and new keys
// appear. So every value counts, and one that names no file adds nothing.
func settingsStrings(dir string) []string {
	var out []string
	var walk func(v any)
	walk = func(v any) {
		switch v := v.(type) {
		case string:
			out = append(out, v)
		case []any:
			for _, e := range v {
				walk(e)
			}
		case map[string]any:
			for _, e := range v {
				walk(e)
			}
		}
	}
	for _, name := range []string{"settings.json", "settings.local.json"} {
		data, err := os.ReadFile(filepath.Join(dir, ".claude", name))
		if err != nil {
			continue
		}
		var v any
		if json.Unmarshal(data, &v) == nil {
			walk(v)
		}
	}
	return out
}

var (
	pathToken = regexp.MustCompile(`[A-Za-z0-9_.~/+@%\\-]+`)
	// What a script puts in front of a path to find its own folder or the
	// project: "$HERE/", "${BASH_SOURCE%/*}/", "$(dirname "$0")/".
	pyModule  = regexp.MustCompile(`(?:^|[\s,\[])-m[\s,]+([A-Za-z_][A-Za-z0-9_]*(?:\.[A-Za-z_][A-Za-z0-9_]*)*)`)
	varPrefix = regexp.MustCompile("(\\$[A-Za-z_][A-Za-z0-9_]*|\\$\\{[^}]*\\}|\\$\\([^()]*\\)|`[^`]*`)/")
)

// fileRefs returns the regular files inside dir that text names. A relative
// name resolves against dir, against base, and against every folder inside
// dir that text names, because "cd tools && ./run.sh" runs tools/run.sh.
// A name with a backslash counts both as written and as the shell reads it,
// so `back\slash.sh` names backslash.sh too.
func fileRefs(text, home, dir, base string) []string {
	// Quotes go first: "$CLAUDE_PROJECT_DIR"/bin/x is one path.
	vars := []string{`"`, "", "'", "", "${CLAUDE_PROJECT_DIR}", dir, "$CLAUDE_PROJECT_DIR", dir}
	if home != "" {
		vars = append(vars, "${HOME}", home, "$HOME", home)
	}
	text = strings.NewReplacer(vars...).Replace(text)
	// "python3 -m tools.server" runs tools/server.py, or a package's
	// __main__.py; the dotted name never appears as a path.
	for _, m := range pyModule.FindAllStringSubmatch(text, -1) {
		mod := strings.ReplaceAll(m[1], ".", "/")
		text += " " + mod + ".py " + mod + "/__main__.py " + mod + "/__init__.py"
	}
	text = varPrefix.ReplaceAllString(text, "")
	var toks []string
	for _, tok := range pathToken.FindAllString(text, -1) {
		if home != "" && strings.HasPrefix(tok, "~/") {
			tok = filepath.Join(home, tok[2:])
		}
		toks = append(toks, tok)
		if u := shellUnescape(tok); u != tok {
			toks = append(toks, u)
		}
	}
	resolve := func(tok string, bases []string) []string {
		if filepath.IsAbs(tok) {
			return []string{filepath.Clean(tok)}
		}
		var out []string
		for _, b := range bases {
			out = append(out, filepath.Join(b, tok))
		}
		return out
	}
	bases := []string{dir, base}
	isBase := map[string]bool{dir: true, base: true}
	for _, tok := range toks {
		for _, p := range resolve(tok, []string{dir, base}) {
			if !isBase[p] && len(bases) < maxRefDirs && within(p, dir) && isDir(p) {
				isBase[p] = true
				bases = append(bases, p)
			}
		}
	}
	// A named folder can be what runs: "node tools/server" runs its
	// index.js, and a "directory" plugin marketplace's path holds plugins
	// whose hooks Claude runs. So every file in one counts too.
	var named []string
	for _, b := range bases[2:] {
		filepath.WalkDir(b, func(p string, d fs.DirEntry, err error) error {
			if err != nil || len(named) >= maxHookFiles {
				return filepath.SkipDir
			}
			if d.IsDir() && (d.Name() == ".git" || d.Name() == "node_modules") {
				return filepath.SkipDir
			}
			if d.Type().IsRegular() {
				named = append(named, p)
			}
			return nil
		})
	}
	var out []string
	seen := map[string]bool{}
	for _, tok := range toks {
		for _, p := range resolve(tok, bases) {
			if seen[p] || !within(p, dir) {
				continue
			}
			seen[p] = true
			if st, err := os.Stat(p); err == nil && st.Mode().IsRegular() {
				out = append(out, p)
			}
		}
	}
	for _, p := range named {
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	return out
}

// maxRefDirs bounds the folders one text's names resolve against.
const maxRefDirs = 32

// shellUnescape reads backslashes as the shell does outside quotes: `\x`
// is x, and `\\` is one backslash.
func shellUnescape(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) {
			i++
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// scriptText reads p to look for the files it names: text files up to 1 MB.
// Binaries are fingerprinted but not read.
func scriptText(p string) (string, bool) {
	st, err := os.Stat(p)
	if err != nil || st.Size() > 1<<20 {
		return "", false
	}
	data, err := os.ReadFile(p)
	if err != nil || bytes.IndexByte(data, 0) >= 0 {
		return "", false
	}
	return string(data), true
}

// configChanges lists, relative to home, the files that differ between two
// fingerprints. With no earlier fingerprint (a rerun), everything counts.
// A file that left the fingerprint counts only if it's gone.
func configChanges(home string, before, after map[string]string) []string {
	var out []string
	for p, sum := range after {
		if before == nil || before[p] != sum {
			out = append(out, mustRel(home, p))
		}
	}
	if before != nil {
		for p := range before {
			// A file that's still there but no longer named by any hook or
			// server doesn't run; the change that dropped it is listed.
			if _, ok := after[p]; !ok && !exists(p) {
				out = append(out, mustRel(home, p)+" (removed)")
			}
		}
	}
	sort.Strings(out)
	return out
}

// claudeDirs are the project folders Claude reads config from for a session
// at cwd in worktree wt: the worktree, and the folder the session started in
// if that's somewhere else (a folder above it).
func claudeDirs(wt, cwd string) []string {
	if cwd == "" || cwd == wt {
		return []string{wt}
	}
	return []string{wt, cwd}
}

func fileSHA(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
