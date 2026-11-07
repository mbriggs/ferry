package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// placeholderBranch is the unborn branch a new repo starts on. git refuses a
// push to the checked-out branch even while it is unborn, so a new repo must
// not name a real branch until every branch has arrived.
const placeholderBranch = "ferry-init"

// stateDirRel is never part of a snapshot, so database dumps and the like
// can't become git objects. It travels as an extra instead.
const stateDirRel = ".ferry/state"

// Snapshot commits are internal. They skip signing (which may need an agent
// this process can't reach) and don't depend on the user's identity config.
var gitEnv = []string{
	"GIT_AUTHOR_NAME=ferry", "GIT_AUTHOR_EMAIL=ferry@localhost",
	"GIT_COMMITTER_NAME=ferry", "GIT_COMMITTER_EMAIL=ferry@localhost",
}

func git(dir string, args ...string) (string, error) {
	return run(dir, gitEnv, "git", append([]string{"-c", "commit.gpgsign=false"}, args...)...)
}

func gitIndex(dir, index string, args ...string) (string, error) {
	return run(dir, append(gitEnv, "GIT_INDEX_FILE="+index), "git", append([]string{"-c", "commit.gpgsign=false"}, args...)...)
}

func taskRef(task, name string) string { return "refs/ferry/" + task + "/" + name }

type RepoState struct {
	Exists bool     `json:"exists"`
	Roots  []string `json:"roots"`
	Branch string   `json:"branch"`
	Origin string   `json:"origin"`
}

func (l *Local) RepoState(repoRel string) (RepoState, error) {
	dir := l.abs(repoRel)
	if !exists(filepath.Join(dir, ".git")) {
		return RepoState{}, nil
	}
	s := RepoState{Exists: true}
	if roots, err := git(dir, "rev-list", "--max-parents=0", "--all"); err == nil && roots != "" {
		s.Roots = strings.Fields(roots)
		sort.Strings(s.Roots)
	}
	s.Branch, _ = git(dir, "symbolic-ref", "--short", "-q", "HEAD")
	s.Origin, _ = git(dir, "remote", "get-url", "origin")
	return s, nil
}

func sharesRoot(a, b []string) bool {
	set := map[string]bool{}
	for _, r := range a {
		set[r] = true
	}
	for _, r := range b {
		if set[r] {
			return true
		}
	}
	return false
}

func (l *Local) InitRepo(repoRel string) error {
	dir := l.abs(repoRel)
	if exists(filepath.Join(dir, ".git")) {
		return nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	_, err := git(dir, "init", "-q", "--initial-branch="+placeholderBranch)
	return err
}

type FinishRepoReq struct {
	RepoRel string `json:"repo_rel"`
	Branch  string `json:"branch"`
	Origin  string `json:"origin"`
}

// FinishRepo moves a repo created by InitRepo off its placeholder branch and
// gives it the source's origin, once branches have been pushed into it.
func (l *Local) FinishRepo(q FinishRepoReq) error {
	dir := l.abs(q.RepoRel)
	head, _ := git(dir, "symbolic-ref", "--short", "-q", "HEAD")
	if head == placeholderBranch && q.Branch != "" {
		if _, err := git(dir, "checkout", "-q", q.Branch); err != nil {
			return err
		}
	}
	if q.Origin != "" {
		if _, err := git(dir, "remote", "get-url", "origin"); err != nil {
			if _, err := git(dir, "remote", "add", "origin", q.Origin); err != nil {
				return err
			}
		}
	}
	return nil
}

type SnapshotReq struct {
	WtRel string `json:"wt_rel"`
	Task  string `json:"task"`
}

type SnapshotResult struct {
	Base        string `json:"base"`
	Branch      string `json:"branch"`
	Fingerprint string `json:"fingerprint"`
	Dirty       int    `json:"dirty"`
}

// Snapshot records the worktree as three commits under refs/ferry/<task>/:
// base is HEAD, index is the real index on top of base, and wip is the whole
// working tree (tracked, untracked, deletions; not ignored files) with base
// and index as parents. The user's index, branch, and files are untouched.
func (l *Local) Snapshot(q SnapshotReq) (SnapshotResult, error) {
	dir := l.abs(q.WtRel)
	base, err := git(dir, "rev-parse", "--verify", "-q", "HEAD")
	if err != nil {
		return SnapshotResult{}, fmt.Errorf("%s has no commits yet", q.WtRel)
	}
	branch, err := git(dir, "symbolic-ref", "--short", "-q", "HEAD")
	if err != nil {
		return SnapshotResult{}, fmt.Errorf("%s is on a detached HEAD; check out a branch first", q.WtRel)
	}
	if staged, _ := git(dir, "ls-files", "--cached", "--", stateDirRel); staged != "" {
		return SnapshotResult{}, fmt.Errorf("%s is in the git index; it travels outside git. Run `git rm -r --cached %s` first", stateDirRel, stateDirRel)
	}
	idxTree, err := git(dir, "write-tree")
	if err != nil {
		return SnapshotResult{}, fmt.Errorf("index can't be snapshotted (unresolved conflicts?): %w", err)
	}
	idx, err := git(dir, "commit-tree", idxTree, "-p", base, "-m", "ferry index")
	if err != nil {
		return SnapshotResult{}, err
	}
	wipTree, err := worktreeTree(dir)
	if err != nil {
		return SnapshotResult{}, err
	}
	wip, err := git(dir, "commit-tree", wipTree, "-p", base, "-p", idx, "-m", "ferry wip")
	if err != nil {
		return SnapshotResult{}, err
	}
	for name, sha := range map[string]string{"base": base, "index": idx, "wip": wip} {
		if _, err := git(dir, "update-ref", taskRef(q.Task, name), sha); err != nil {
			return SnapshotResult{}, err
		}
	}
	fp, err := fingerprint(dir)
	if err != nil {
		return SnapshotResult{}, err
	}
	status, _ := git(dir, "status", "--porcelain")
	dirty := 0
	if status != "" {
		dirty = len(strings.Split(status, "\n"))
	}
	return SnapshotResult{Base: base, Branch: branch, Fingerprint: fp, Dirty: dirty}, nil
}

// worktreeTree is the tree of everything `git add -A` would stage, built in a
// throwaway index, minus .ferry/state.
func worktreeTree(dir string) (string, error) {
	indexPath, err := git(dir, "rev-parse", "--git-path", "index")
	if err != nil {
		return "", err
	}
	if !filepath.IsAbs(indexPath) {
		indexPath = filepath.Join(dir, indexPath)
	}
	tmp, err := os.CreateTemp("", "ferry-index-*")
	if err != nil {
		return "", err
	}
	tmp.Close()
	defer os.Remove(tmp.Name())
	if data, err := os.ReadFile(indexPath); err == nil {
		if err := os.WriteFile(tmp.Name(), data, 0o600); err != nil {
			return "", err
		}
	} else {
		os.Remove(tmp.Name())
	}
	// Keep .ferry/state out of the add itself, so its files never become
	// blobs. An ignored directory is already skipped (and git rejects a
	// pathspec naming one), so the exclude is only needed when it isn't.
	add := []string{"add", "-A"}
	if _, err := git(dir, "check-ignore", "-q", stateDirRel+"/"); err != nil {
		add = append(add, "--", ".", ":(exclude)"+stateDirRel)
	}
	if _, err := gitIndex(dir, tmp.Name(), add...); err != nil {
		return "", err
	}
	if _, err := gitIndex(dir, tmp.Name(), "rm", "-r", "-q", "--cached", "--ignore-unmatch", "--", stateDirRel); err != nil {
		return "", err
	}
	return gitIndex(dir, tmp.Name(), "write-tree")
}

// fingerprint identifies a worktree's full state: HEAD, the index tree, and
// the working tree including untracked files. Two worktrees with the same
// fingerprint are interchangeable for ferry's purposes.
func fingerprint(dir string) (string, error) {
	head, err := git(dir, "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	idx, err := git(dir, "write-tree")
	if err != nil {
		return "", err
	}
	tree, err := worktreeTree(dir)
	if err != nil {
		return "", err
	}
	return "head=" + head + " index=" + idx + " tree=" + tree, nil
}

type ApplyReq struct {
	RepoRel  string `json:"repo_rel"`
	WtRel    string `json:"wt_rel"`
	Branch   string `json:"branch"`
	Task     string `json:"task"`
	Expected string `json:"expected"`
	// Previous are fingerprints ferry itself left in this worktree: this
	// machine's state when it last handed the task away, and what an earlier,
	// interrupted attempt of this handoff applied. A worktree matching one of
	// them holds nothing that wasn't handed off.
	Previous []string `json:"previous,omitempty"`
	// Fresh means ferry created this repo during this handoff.
	Fresh bool `json:"fresh,omitempty"`
	// Force overwrites whatever is there. The user asked for it.
	Force bool `json:"force,omitempty"`
}

// Apply makes the worktree at WtRel match the snapshot in refs/ferry/<task>
// and returns the resulting fingerprint. It never discards commits or edits
// that weren't handed off: an existing worktree or branch is overwritten only
// when ferry put it there, or when nothing in it is missing from the snapshot.
func (l *Local) Apply(q ApplyReq) (string, error) {
	repo, wt := l.abs(q.RepoRel), l.abs(q.WtRel)
	base, wip, idx := taskRef(q.Task, "base"), taskRef(q.Task, "wip"), taskRef(q.Task, "index")
	baseSHA, err := git(repo, "rev-parse", "--verify", "-q", base+"^{commit}")
	if err != nil {
		return "", fmt.Errorf("snapshot %s is missing on this machine", base)
	}
	ours := func(fp string) bool {
		for _, p := range append([]string{q.Expected}, q.Previous...) {
			if p != "" && sameHeadAndTree(fp, p) {
				return true
			}
		}
		return false
	}

	if isWorktree(wt) {
		cur, err := fingerprint(wt)
		if err != nil {
			return "", err
		}
		if cur == q.Expected {
			return cur, nil
		}
		if !q.Force && !q.Fresh && !ours(cur) {
			if err := nothingToLose(wt, "HEAD", baseSHA, q.WtRel); err != nil {
				return "", err
			}
		}
		if err := l.branchSafe(repo, q, baseSHA, ours); err != nil {
			return "", err
		}
		if _, err := git(wt, "checkout", "-q", "-f", "-B", q.Branch, base); err != nil {
			return "", err
		}
		if _, err := git(wt, "clean", "-q", "-fd"); err != nil {
			return "", err
		}
	} else {
		if exists(wt) {
			entries, _ := os.ReadDir(wt)
			if len(entries) > 0 {
				return "", fmt.Errorf("%s exists but isn't a git worktree of %s", q.WtRel, q.RepoRel)
			}
			os.Remove(wt)
		}
		if err := l.branchSafe(repo, q, baseSHA, ours); err != nil {
			return "", err
		}
		if err := os.MkdirAll(filepath.Dir(wt), 0o755); err != nil {
			return "", err
		}
		git(repo, "worktree", "prune")
		if _, err := git(repo, "worktree", "add", "-q", "-B", q.Branch, wt, base); err != nil {
			return "", err
		}
	}
	if _, err := git(wt, "restore", "--source="+wip, "--worktree", "--", ":/"); err != nil {
		return "", err
	}
	if _, err := git(wt, "read-tree", idx); err != nil {
		return "", err
	}
	got, err := fingerprint(wt)
	if err != nil {
		return "", err
	}
	if got != q.Expected {
		return "", fmt.Errorf("applied tree doesn't match the snapshot\n  want %s\n  got  %s", q.Expected, got)
	}
	return got, nil
}

// branchSafe refuses to move an existing branch off commits the snapshot
// doesn't contain, unless ferry itself left the branch there.
func (l *Local) branchSafe(repo string, q ApplyReq, baseSHA string, ours func(string) bool) error {
	tip, err := git(repo, "rev-parse", "--verify", "-q", "refs/heads/"+q.Branch)
	if err != nil || q.Force || q.Fresh {
		return nil
	}
	for _, p := range append([]string{q.Expected}, q.Previous...) {
		if fpField(p, "head") == tip {
			return nil
		}
	}
	if _, err := git(repo, "merge-base", "--is-ancestor", tip, baseSHA); err == nil {
		return nil
	}
	log, _ := git(repo, "log", "--oneline", baseSHA+".."+tip)
	return fmt.Errorf("branch %s here has commits the incoming snapshot doesn't contain; refusing to move it (pass --force to overwrite):\n%s", q.Branch, indent(log))
}

// nothingToLose succeeds when the worktree is clean and its HEAD is already
// contained in base, so overwriting it can't drop anything.
func nothingToLose(wt, head, baseSHA, label string) error {
	status, err := git(wt, "status", "--porcelain")
	if err != nil {
		return err
	}
	if status != "" {
		return fmt.Errorf("%s has changes that weren't handed off; refusing to overwrite (pass --force to overwrite):\n%s", label, indent(status))
	}
	if _, err := git(wt, "merge-base", "--is-ancestor", head, baseSHA); err != nil {
		log, _ := git(wt, "log", "--oneline", baseSHA+".."+head)
		return fmt.Errorf("%s has commits that weren't handed off; refusing to overwrite (pass --force to overwrite):\n%s", label, indent(log))
	}
	return nil
}

// fpField reads one field ("head", "index", "tree") from a fingerprint.
func fpField(fp, name string) string {
	for _, f := range strings.Fields(fp) {
		if v, ok := strings.CutPrefix(f, name+"="); ok {
			return v
		}
	}
	return ""
}

// sameHeadAndTree ignores the index, so a worktree interrupted between
// restoring files and reading the index still counts as ferry's own.
func sameHeadAndTree(a, b string) bool {
	return fpField(a, "head") != "" && fpField(a, "head") == fpField(b, "head") && fpField(a, "tree") == fpField(b, "tree")
}

func isWorktree(dir string) bool {
	if !exists(filepath.Join(dir, ".git")) {
		return false
	}
	top, err := git(dir, "rev-parse", "--show-toplevel")
	if err != nil {
		return false
	}
	a, _ := filepath.EvalSymlinks(top)
	b, _ := filepath.EvalSymlinks(dir)
	return a == b
}

func indent(s string) string {
	return "  " + strings.ReplaceAll(strings.TrimRight(s, "\n"), "\n", "\n  ")
}
