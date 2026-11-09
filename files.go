package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
)

// FileEntry is an extra as the sending machine listed it. rsync keeps the
// mtime, so size and mtime together say whether a file is still the one
// that was sent.
type FileEntry struct {
	Path  string `json:"path"`
	Size  int64  `json:"size"`
	MTime int64  `json:"mtime"` // nanoseconds
}

func (e FileEntry) matches(st fs.FileInfo) bool {
	return st.Mode().IsRegular() && st.Size() == e.Size && st.ModTime().UnixNano() == e.MTime
}

// ExtrasList is what ListExtras found: the files to send, and the ignored
// paths left behind, so what stays is visible in the output.
type ExtrasList struct {
	Files      []FileEntry `json:"files"`
	LeftBehind []string    `json:"left_behind,omitempty"`
	// Links are ignored symlinks. They stay: one from the other machine
	// could point anywhere in this home, and a write through it would land
	// there.
	Links []string `json:"links,omitempty"`
}

// ListExtras returns the ignored files in a worktree, which travel with it
// next to the git snapshot (tracked and untracked files ride in git). All of
// them go except:
//
//   - dependency and build trees (heavyDirs), which are built for one OS and
//     rebuilt by restore,
//   - git worktrees Claude keeps under .claude/worktrees,
//   - paths matching .ferry/exclude: one pattern per line, a glob against
//     the base name or the whole path, or a directory when it ends in /.
//
// .ferry/state always travels, even when it isn't ignored.
func (l *Local) ListExtras(wtRel string) (ExtrasList, error) {
	root := l.abs(wtRel)
	var exclude []string
	if data, err := os.ReadFile(filepath.Join(root, ".ferry", "exclude")); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			if line = strings.TrimSpace(line); line != "" && !strings.HasPrefix(line, "#") {
				exclude = append(exclude, line)
			}
		}
	}
	stays := func(rel string) bool {
		return neverExtra(rel) || (matchAny(exclude, rel) && !within(filepath.ToSlash(rel), stateDirRel))
	}
	out, err := git(root, "ls-files", "-z", "--others", "--ignored", "--exclude-standard", "--directory")
	if err != nil {
		return ExtrasList{}, err
	}
	seen := map[string]bool{}
	var entries []FileEntry
	var left, links []string
	add := func(rel string) {
		if seen[rel] {
			return
		}
		st, err := os.Lstat(filepath.Join(root, rel))
		switch {
		case err != nil:
		case st.Mode().IsRegular():
			seen[rel] = true
			entries = append(entries, FileEntry{Path: rel, Size: st.Size(), MTime: st.ModTime().UnixNano()})
		case st.Mode()&fs.ModeSymlink != 0:
			seen[rel] = true
			links = append(links, rel)
		}
	}
	for _, rel := range strings.Split(out, "\x00") {
		if rel == "" {
			continue
		}
		dir := strings.TrimSuffix(rel, "/")
		switch {
		case inHeavyDir(dir) || stays(dir):
			if !neverExtra(dir) {
				left = append(left, rel)
			}
		case strings.HasSuffix(rel, "/"):
			// git collapsed an ignored directory; take the files inside,
			// skipping dependency and build trees and excluded paths.
			walkIgnored(root, dir, stays, add, func(r string) { left = append(left, r) })
		default:
			add(rel)
		}
	}
	// .ferry/state may be untracked but not ignored; it's excluded from the
	// snapshot either way, so it always travels as an extra.
	walkFiles(filepath.Join(root, stateDirRel), func(p string) {
		r, _ := filepath.Rel(root, p)
		add(r)
	})
	return ExtrasList{Files: entries, LeftBehind: left, Links: links}, nil
}

// inHeavyDir reports whether any directory in rel is a dependency or build
// tree (see heavyDir).
func inHeavyDir(rel string) bool {
	parts := strings.Split(filepath.ToSlash(rel), "/")
	for i := range parts {
		if heavyDir(strings.Join(parts[:i+1], "/")) {
			return true
		}
	}
	return false
}

// heavyDir reports whether the directory rel is a dependency or build tree,
// rebuilt rather than moved. The names are guesses, so they don't apply in
// the agent's and ferry's own folders, where a skill can be called "build":
// only .git is skipped there.
func heavyDir(rel string) bool {
	rel = filepath.ToSlash(rel)
	name := path.Base(rel)
	if name == ".git" {
		return true
	}
	return heavyDirs[name] && !within(rel, ".claude") && !within(rel, ".ferry")
}

func neverExtra(rel string) bool {
	return within(filepath.ToSlash(rel), claudeWorktreesRel) || filepath.Base(rel) == ".DS_Store"
}

// matchAny reports whether rel matches a pattern: a glob against the base
// name or the whole path, or, for a pattern ending in /, anything under that
// directory.
func matchAny(patterns []string, rel string) bool {
	rel = filepath.ToSlash(rel)
	for _, p := range patterns {
		if dir, ok := strings.CutSuffix(p, "/"); ok {
			if within(rel, dir) {
				return true
			}
			continue
		}
		if ok, _ := filepath.Match(p, filepath.Base(rel)); ok {
			return true
		}
		if ok, _ := filepath.Match(p, rel); ok {
			return true
		}
	}
	return false
}

// heavyDirs are never searched for extras: they're rebuilt, not moved.
var heavyDirs = map[string]bool{
	"node_modules": true, "_build": true, "deps": true, "target": true, ".venv": true, "venv": true,
	"vendor": true, "dist": true, "build": true, ".next": true, ".cache": true, "tmp": true, "log": true, ".git": true,
	"__pycache__": true, ".pytest_cache": true, ".mypy_cache": true, ".ruff_cache": true, ".tox": true,
	".gradle": true, ".terraform": true, ".turbo": true, ".parcel-cache": true, ".svelte-kit": true, ".nuxt": true,
	".elixir_ls": true, ".expo": true, "elm-stuff": true, "coverage": true,
}

// walkIgnored walks the ignored directory dir (relative to root) and calls
// add for each file that travels. Directories that stay, dependency trees
// and paths .ferry/exclude names, go to left with a trailing slash, as do
// excluded files, so the output lists everything that stays. Claude's own
// worktrees stay without a mention.
func walkIgnored(root, dir string, stays func(string) bool, add, left func(string)) {
	filepath.WalkDir(filepath.Join(root, dir), func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		switch {
		case d.IsDir() && neverExtra(rel):
			return filepath.SkipDir
		case neverExtra(rel):
			return nil
		case d.IsDir() && rel != dir && (heavyDir(rel) || stays(rel)):
			left(filepath.ToSlash(rel) + "/")
			return filepath.SkipDir
		case !d.Type().IsRegular() && d.Type()&fs.ModeSymlink == 0:
		case stays(rel):
			left(filepath.ToSlash(rel))
		default:
			add(rel) // a file, or a symlink add lists as one
		}
		return nil
	})
}

func walkFiles(dir string, fn func(string)) {
	filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err == nil && d.Type().IsRegular() {
			fn(p)
		}
		return nil
	})
}

// extrasWork is where rsync keeps partial and temp files for a task's
// extras on the receiver, so a cut transfer leaves nothing in the worktree.
func extrasWork(task string) string {
	return filepath.Join(".local", "state", "ferry", "in", task, "extras")
}

type PrepareExtrasReq struct {
	WtRel string   `json:"wt_rel"`
	Task  string   `json:"task"`
	Paths []string `json:"paths"`
}

// PrepareExtras checks, before rsync writes anything, that every incoming
// extra lands inside the worktree. The worktree's tracked contents came from
// the other machine, so no component of a path may be a symlink (a tracked
// symlink could otherwise redirect a write anywhere in this home), and
// nothing goes under .git. Extras are often secrets, and rsync keeps the
// mode of a file it replaces until it's done, so each one already here loses
// its group and other bits first; safeDest has checked it's a regular file,
// not a symlink. It also makes the private directory rsync keeps its partial
// and temp files in, and returns the listed files this machine has, so the
// sender can tell which differ and which this machine changed.
func (l *Local) PrepareExtras(q PrepareExtrasReq) (PrepareExtrasResult, error) {
	var res PrepareExtrasResult
	if !taskPattern.MatchString(q.Task) {
		return res, fmt.Errorf("bad task key %q", q.Task)
	}
	root := l.abs(q.WtRel)
	for _, p := range q.Paths {
		if err := safeDest(root, p); err != nil {
			return res, err
		}
	}
	for _, p := range q.Paths {
		st, err := os.Lstat(filepath.Join(root, p))
		if errors.Is(err, fs.ErrNotExist) {
			continue
		} else if err != nil {
			return res, err
		}
		if err := os.Chmod(filepath.Join(root, p), st.Mode().Perm()&0o700); err != nil {
			return res, err
		}
		res.Have = append(res.Have, FileEntry{Path: p, Size: st.Size(), MTime: st.ModTime().UnixNano()})
	}
	work := l.abs(extrasWork(q.Task))
	for _, d := range []string{"partial", "tmp"} {
		if err := os.MkdirAll(filepath.Join(work, d), 0o700); err != nil {
			return res, err
		}
	}
	return res, nil
}

type PrepareExtrasResult struct {
	Have []FileEntry `json:"have,omitempty"`
}

type RemoveExtrasReq struct {
	WtRel string `json:"wt_rel"`
	Task  string `json:"task"`
	// Files are extras this machine sent last time that the other side no
	// longer has. Each is parked if it's still exactly what was sent.
	Files []FileEntry `json:"files"`
}

type RemoveExtrasResult struct {
	Removed []string `json:"removed,omitempty"`
	Kept    []string `json:"kept,omitempty"` // changed here since they were sent
}

// RemoveExtras carries deletions over. It never deletes: a removed extra
// moves to ~/.local/state/ferry/parked/<task>-extras-<time>/.
func (l *Local) RemoveExtras(q RemoveExtrasReq) (RemoveExtrasResult, error) {
	var res RemoveExtrasResult
	if len(q.Files) == 0 {
		return res, nil
	}
	root := l.abs(q.WtRel)
	dir, err := l.parkDir(q.Task, "extras-"+time.Now().Format("20060102-150405"))
	if err != nil {
		return res, err
	}
	for _, e := range q.Files {
		if err := safeDest(root, e.Path); err != nil {
			return res, err
		}
		p := filepath.Join(root, e.Path)
		st, err := os.Lstat(p)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil || !e.matches(st) {
			res.Kept = append(res.Kept, e.Path)
			continue
		}
		if err := moveFile(p, filepath.Join(dir, e.Path)); err != nil {
			return res, err
		}
		res.Removed = append(res.Removed, e.Path)
	}
	return res, nil
}

// safeDest checks that writing root/rel stays inside root: rel is relative and
// clean, no component is ".git" in any case, and no existing component along
// the way (including the file itself) is a symlink or a non-directory. The
// case matters on macOS, where ".GIT" names the same directory as ".git".
func safeDest(root, rel string) error {
	clean := filepath.Clean(rel)
	if filepath.IsAbs(clean) || clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return fmt.Errorf("refusing extra %q: outside the worktree", rel)
	}
	parts := strings.Split(clean, string(filepath.Separator))
	cur := root
	for i, part := range parts {
		if strings.EqualFold(part, ".git") {
			return fmt.Errorf("refusing extra %q: inside .git", rel)
		}
		cur = filepath.Join(cur, part)
		st, err := os.Lstat(cur)
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		} else if err != nil {
			return err
		}
		last := i == len(parts)-1
		switch {
		case st.Mode()&fs.ModeSymlink != 0:
			return fmt.Errorf("refusing extra %q: %s is a symlink", rel, cur)
		case !last && !st.IsDir():
			return fmt.Errorf("refusing extra %q: %s is not a directory", rel, cur)
		case last && !st.Mode().IsRegular():
			return fmt.Errorf("refusing extra %q: %s is not a regular file", rel, cur)
		}
	}
	return nil
}
