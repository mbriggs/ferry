package main

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Claude reads its project config as it starts: .claude/settings*.json,
// skills and commands, and hooks that run commands at once (SessionStart
// runs on resume). Those travel from the other machine, so before ferry
// starts Claude in this terminal it checks whether any of it changed during
// the handoff, and asks if so.
//
// claudeConfig fingerprints that config for the project folders in dirs:
// every file under <dir>/.claude except Claude's own worktrees, plus each
// file a hook command in the settings names. Keys are absolute paths, values
// sha256.
func claudeConfig(dirs []string) map[string]string {
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
		for _, f := range hookFiles(dir) {
			if sum, err := fileSHA(f); err == nil {
				out[f] = sum
			}
		}
	}
	return out
}

// hookFiles returns the files inside dir that hook commands in dir's Claude
// settings name, such as "$CLAUDE_PROJECT_DIR/.claude/hooks/start.sh" or
// "bin/check".
func hookFiles(dir string) []string {
	var out []string
	for _, name := range []string{"settings.json", "settings.local.json"} {
		data, err := os.ReadFile(filepath.Join(dir, ".claude", name))
		if err != nil {
			continue
		}
		var settings struct {
			Hooks map[string][]struct {
				Hooks []struct {
					Command string `json:"command"`
				} `json:"hooks"`
			} `json:"hooks"`
		}
		if json.Unmarshal(data, &settings) != nil {
			continue
		}
		for _, groups := range settings.Hooks {
			for _, g := range groups {
				for _, h := range g.Hooks {
					cmd := strings.NewReplacer("${CLAUDE_PROJECT_DIR}", dir, "$CLAUDE_PROJECT_DIR", dir).Replace(h.Command)
					for _, tok := range strings.Fields(cmd) {
						tok = strings.Trim(tok, `"';&|()`)
						if tok == "" {
							continue
						}
						p := tok
						if !filepath.IsAbs(p) {
							p = filepath.Join(dir, p)
						}
						p = filepath.Clean(p)
						if st, err := os.Stat(p); err == nil && st.Mode().IsRegular() && within(p, dir) {
							out = append(out, p)
						}
					}
				}
			}
		}
	}
	return out
}

// configChanges lists, relative to home, the files that differ between two
// fingerprints. With no earlier fingerprint (a rerun), everything counts.
func configChanges(home string, before, after map[string]string) []string {
	var out []string
	for p, sum := range after {
		if before == nil || before[p] != sum {
			out = append(out, mustRel(home, p))
		}
	}
	if before != nil {
		for p := range before {
			if _, ok := after[p]; !ok {
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
