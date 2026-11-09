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

// sentRef is the snapshot this machine last sent. It lives outside
// refs/ferry/<task>/, so a handoff in the other direction never overwrites it.
func sentRef(task string) string { return "refs/ferry-sent/" + task }

type RepoState struct {
	Exists bool     `json:"exists"`
	Roots  []string `json:"roots"`
	Branch string   `json:"branch"`
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
		// A repo with no commits yet, like a bare `git init`, has nothing to
		// keep, but its unborn HEAD may name a branch about to arrive. Move
		// it to the placeholder, as a new repo starts.
		if _, err := git(dir, "rev-parse", "-q", "--verify", "HEAD"); err != nil {
			_, err = git(dir, "symbolic-ref", "HEAD", "refs/heads/"+placeholderBranch)
			return err
		}
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
}

// FinishRepo moves a repo created by InitRepo off its placeholder branch,
// once branches have been pushed into it. Its remotes come later, from
// SyncRemotes.
func (l *Local) FinishRepo(q FinishRepoReq) error {
	dir := l.abs(q.RepoRel)
	head, _ := git(dir, "symbolic-ref", "--short", "-q", "HEAD")
	if head == placeholderBranch && q.Branch != "" {
		if _, err := git(dir, "checkout", "-q", q.Branch); err != nil {
			return err
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
	// Remotes and Upstream are the repo's remotes and the branch's upstream.
	// The snapshot stages the remote-tracking refs beside its commits.
	Remotes  []GitRemote `json:"remotes"`
	Upstream Upstream    `json:"upstream"`
}

// Snapshot records the worktree as three commits under refs/ferry/<task>/:
// base is HEAD, index is the real index on top of base, and wip is the whole
// working tree (tracked, untracked, deletions; not ignored files) with base
// and index as parents. It copies refs/remotes/* to
// refs/ferry/<task>/remotes/*. The user's index, branch, and files are
// untouched.
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
	if err := stageTracking(dir, q.Task); err != nil {
		return SnapshotResult{}, err
	}
	remotes, err := remotesOf(dir)
	if err != nil {
		return SnapshotResult{}, err
	}
	fp := fingerprintOf(base, idxTree, wipTree)
	status, _ := git(dir, "status", "--porcelain")
	dirty := 0
	if status != "" {
		dirty = len(strings.Split(status, "\n"))
	}
	return SnapshotResult{Base: base, Branch: branch, Fingerprint: fp, Dirty: dirty, Remotes: remotes, Upstream: upstreamOf(dir, branch)}, nil
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
	return fingerprintOf(head, idx, tree), nil
}

func fingerprintOf(head, indexTree, worktreeTree string) string {
	return "head=" + head + " index=" + indexTree + " tree=" + worktreeTree
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
	if isWorktree(wt) {
		if cur, err := fingerprint(wt); err != nil {
			return "", err
		} else if cur == q.Expected {
			return cur, nil
		}
	}
	if !q.Force && !q.Fresh {
		u, err := unsent(repo, wt, q.Branch, baseSHA, append([]string{q.Expected}, q.Previous...))
		if err != nil {
			return "", err
		}
		if !u.Empty() {
			return "", u.refusal(q.WtRel, q.Branch)
		}
	}

	if isWorktree(wt) {
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

// Unsent is the work on a machine that handing a task to it would
// overwrite. Each field is git's output, "" when there's nothing.
type Unsent struct {
	Changes string `json:"changes,omitempty"` // `git status --porcelain` of the worktree
	Commits string `json:"commits,omitempty"` // the worktree's commits missing from the incoming base
	Branch  string `json:"branch,omitempty"`  // the branch's commits missing from the incoming base
}

func (u Unsent) Empty() bool { return u.Changes == "" && u.Commits == "" && u.Branch == "" }

// refusal is Apply's error when the work it would overwrite isn't ferry's.
func (u Unsent) refusal(wtRel, branch string) error {
	return fmt.Errorf("%s; refusing to overwrite (pass --force to overwrite):\n%s", u.Summary(wtRel, branch), indent(u.Detail()))
}

// Summary says what u holds, in one line.
func (u Unsent) Summary(wtRel, branch string) string {
	var parts []string
	if u.Changes != "" {
		parts = append(parts, wtRel+" has changes that weren't handed off")
	}
	if u.Commits != "" {
		parts = append(parts, wtRel+" has commits that weren't handed off")
	}
	if u.Branch != "" {
		parts = append(parts, "branch "+branch+" has commits the incoming snapshot doesn't contain")
	}
	return strings.Join(parts, "; ")
}

// Detail is git's lines for u.
func (u Unsent) Detail() string {
	var out []string
	for _, s := range []string{u.Changes, u.Commits, u.Branch} {
		if s != "" {
			out = append(out, s)
		}
	}
	return strings.Join(out, "\n")
}

// unsent finds what overwriting the worktree wt and moving branch to
// baseSHA would lose. known are fingerprints ferry itself left here: a
// worktree matching one holds nothing that wasn't handed off, and a branch
// at one's head is where ferry put it.
func unsent(repo, wt, branch, baseSHA string, known []string) (Unsent, error) {
	var u Unsent
	if isWorktree(wt) {
		cur, err := fingerprint(wt)
		if err != nil {
			return u, err
		}
		if !knownTree(cur, known) {
			// Against the tree ferry last left here, the list is what changed
			// since; without one, it's everything uncommitted.
			if from := knownAtHead(cur, known); from != "" {
				u.Changes, err = git(wt, "diff", "--name-status", fpField(from, "tree"), fpField(cur, "tree"))
			} else {
				u.Changes, err = git(wt, "status", "--porcelain")
			}
			if err != nil {
				return u, err
			}
			if _, err := git(wt, "merge-base", "--is-ancestor", "HEAD", baseSHA); err != nil {
				u.Commits, _ = git(wt, "log", "--oneline", baseSHA+"..HEAD")
			}
		}
	}
	tip, err := git(repo, "rev-parse", "--verify", "-q", "refs/heads/"+branch)
	if err != nil || knownHead(tip, known) {
		return u, nil
	}
	if _, err := git(repo, "merge-base", "--is-ancestor", tip, baseSHA); err != nil {
		u.Branch, _ = git(repo, "log", "--oneline", baseSHA+".."+tip)
	}
	return u, nil
}

func knownTree(fp string, known []string) bool {
	for _, k := range known {
		if k != "" && sameHeadAndTree(fp, k) {
			return true
		}
	}
	return false
}

// knownAtHead is the known fingerprint with fp's HEAD and a tree, if any.
func knownAtHead(fp string, known []string) string {
	for _, k := range known {
		if fpField(k, "tree") != "" && fpField(k, "head") == fpField(fp, "head") {
			return k
		}
	}
	return ""
}

func knownHead(sha string, known []string) bool {
	for _, k := range known {
		if fpField(k, "head") == sha {
			return true
		}
	}
	return false
}

// UnsentReq asks, before a handoff starts, what it would overwrite here.
// Base is a ref holding the commit the sender's branch is on.
type UnsentReq struct {
	RepoRel  string   `json:"repo_rel"`
	WtRel    string   `json:"wt_rel"`
	Branch   string   `json:"branch"`
	Base     string   `json:"base"`
	Previous []string `json:"previous,omitempty"`
}

func (l *Local) Unsent(q UnsentReq) (Unsent, error) {
	repo := l.abs(q.RepoRel)
	baseSHA, err := git(repo, "rev-parse", "--verify", "-q", q.Base+"^{commit}")
	if err != nil {
		return Unsent{}, fmt.Errorf("%s is missing on this machine", q.Base)
	}
	return unsent(repo, l.abs(q.WtRel), q.Branch, baseSHA, q.Previous)
}

type SaveUnsentReq struct {
	RepoRel string `json:"repo_rel"`
	WtRel   string `json:"wt_rel"`
	Branch  string `json:"branch"`
	// Name is the branch to save the worktree to. It must not exist yet.
	Name string `json:"name"`
}

type SavedUnsent struct {
	// Branches are the branches created: Name for the worktree, and
	// Name-branch for Branch's tip when the worktree's copy doesn't hold it.
	Branches []string `json:"branches"`
	// Known are fingerprints of what was saved, for ApplyReq.Previous: Apply
	// then overwrites exactly that, and refuses anything changed since.
	Known []string `json:"known"`
}

// SaveUnsent copies the worktree (every file git doesn't ignore, as one
// commit on top of its HEAD) and Branch's tip to new branches, so a handoff
// can overwrite them without losing anything. The copy doesn't keep what was
// staged apart from what wasn't.
func (l *Local) SaveUnsent(q SaveUnsentReq) (SavedUnsent, error) {
	repo, wt := l.abs(q.RepoRel), l.abs(q.WtRel)
	var s SavedUnsent
	create := func(name, sha string) error {
		// An empty old value makes update-ref refuse a branch that exists.
		if _, err := git(repo, "update-ref", "refs/heads/"+name, sha, ""); err != nil {
			return fmt.Errorf("can't create branch %s: %w", name, err)
		}
		s.Branches = append(s.Branches, name)
		return nil
	}
	saved := ""
	if isWorktree(wt) {
		fp, err := fingerprint(wt)
		if err != nil {
			return s, err
		}
		msg := "ferry: ~/" + q.WtRel + " as it was before a handoff overwrote it"
		if saved, err = git(wt, "commit-tree", fpField(fp, "tree"), "-p", "HEAD", "-m", msg); err != nil {
			return s, err
		}
		if err := create(q.Name, saved); err != nil {
			return s, err
		}
		s.Known = append(s.Known, fp)
	}
	tip, err := git(repo, "rev-parse", "--verify", "-q", "refs/heads/"+q.Branch)
	if err != nil {
		return s, nil
	}
	s.Known = append(s.Known, "head="+tip)
	if saved != "" {
		if _, err := git(repo, "merge-base", "--is-ancestor", tip, saved); err == nil {
			return s, nil
		}
	}
	return s, create(q.Name+"-branch", tip)
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
