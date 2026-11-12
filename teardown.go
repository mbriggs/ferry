package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// A task leaves copies of itself wherever it has been. Handoffs never delete
// the far side's: after `ferry from kant`, kant keeps the worktree, its
// branch, ferry's record, refs/ferry/<task>/* and refs/ferry-sent/<task> in
// the repository, and parked copies, and a sandbox there may keep a VM for
// the worktree. `ferry teardown HOST` removes all of that, once nothing
// there is newer than the laptop's copy. The laptop's own worktree belongs to
// whatever made it (git, the worktree skill, Claude), so teardown leaves it
// and removes only ferry's refs and parked copies here.

// TeardownReq asks a machine to forget a task.
type TeardownReq struct {
	Task    string `json:"task"`
	WtRel   string `json:"wt_rel"`
	RepoRel string `json:"repo_rel"`
	Branch  string `json:"branch"`
}

// TeardownResult says what was removed, one line each.
type TeardownResult struct {
	Done []string `json:"done"`
}

// Teardown forgets task q on this machine: it runs the machine's teardown
// command for the worktree (~/.config/ferry/teardown, such as "bunk rm
// --dir", given the worktree's path), removes the worktree and its branch,
// and deletes ferry's record, refs, and parked copies. A repository's main
// checkout isn't removed: it may hold more than this task. The teardown
// command runs first, and if it fails nothing else is removed, so a sandbox
// that refuses (holding work that never came back) keeps the worktree too.
func (l *Local) Teardown(q TeardownReq) (TeardownResult, error) {
	var res TeardownResult
	wt, repo := l.abs(q.WtRel), l.abs(q.RepoRel)
	if cmd := l.teardownCommand(); len(cmd) > 0 && isDir(wt) {
		out, err := exec.Command(cmd[0], append(cmd[1:], wt)...).CombinedOutput()
		if err != nil {
			return res, fmt.Errorf("%s %s: %v %s", strings.Join(cmd, " "), wt, err, strings.TrimSpace(string(out)))
		}
		res.Done = append(res.Done, "ran "+strings.Join(cmd, " ")+" ~/"+q.WtRel)
	}
	switch {
	case !isDir(wt):
	case q.WtRel == q.RepoRel:
		res.Done = append(res.Done, "kept ~/"+q.WtRel+": it's the repository's main checkout")
	default:
		// --force: the laptop checked that nothing here is newer than its copy,
		// and untracked files it sent (.env) would otherwise stop this.
		if _, err := git(repo, "worktree", "remove", "--force", wt); err != nil {
			return res, err
		}
		res.Done = append(res.Done, "removed the worktree ~/"+q.WtRel)
		if q.Branch != "" {
			if _, err := git(repo, "branch", "-D", q.Branch); err == nil {
				res.Done = append(res.Done, "deleted branch "+q.Branch)
			}
		}
	}
	if n := l.forgetTask(repo, q.Task); n > 0 {
		res.Done = append(res.Done, fmt.Sprintf("deleted ferry's refs and parked copies (%d)", n))
	}
	if err := os.Remove(l.recordPath(q.Task)); err == nil {
		res.Done = append(res.Done, "deleted ferry's record")
	} else if !errors.Is(err, os.ErrNotExist) {
		return res, err
	}
	return res, nil
}

// forgetTask deletes refs/ferry/<task>/* and refs/ferry-sent/<task> in repo,
// and task's parked copies, and returns how many it deleted.
func (l *Local) forgetTask(repo, task string) int {
	n := 0
	if isDir(repo) {
		out, _ := git(repo, "for-each-ref", "--format=%(refname)", "refs/ferry/"+task+"/", sentRef(task))
		for _, ref := range strings.Fields(out) {
			if _, err := git(repo, "update-ref", "-d", ref); err == nil {
				n++
			}
		}
	}
	parked, _ := filepath.Glob(l.stateDir("parked", task+"-*"))
	for _, p := range parked {
		if os.RemoveAll(p) == nil {
			n++
		}
	}
	return n
}

// teardownCommand is the command this machine runs, with a worktree's path
// appended, when a task is torn down: the first line of
// ~/.config/ferry/teardown that isn't blank or a # comment. It undoes what
// the launch prefix (see launchPrefix) set up for the worktree.
func (l *Local) teardownCommand() []string { return firstLine(l.configDir("teardown")) }

func cmdTeardown(l *Local, args []string) int {
	fs := flag.NewFlagSet("teardown", flag.ContinueOnError)
	force := fs.Bool("force", false, "tear down even if HOST owns the task or has work the laptop doesn't")
	pos, err := parse(fs, args)
	if err != nil {
		return 2
	}
	if len(pos) != 1 && len(pos) != 2 {
		fmt.Fprint(os.Stderr, usage)
		return 2
	}
	remote, err := l.host(pos[0])
	if err != nil {
		return fail(err)
	}
	cwd, err := os.Getwd()
	if err != nil {
		return fail(err)
	}
	name := ""
	if len(pos) == 2 {
		name = pos[1]
	}
	t, err := chooseTo(l, cwd, name)
	if err != nil {
		return fail(err)
	}
	release, err := l.lockTask(t.Key)
	if err != nil {
		return fail(err)
	}
	defer release()
	remote.Task = t.Key
	if err := teardown(l, remote, t, *force); err != nil {
		return fail(l.hostHint(pos[0], err))
	}
	return 0
}

// teardown removes t from remote, then ferry's state for it here.
func teardown(l *Local, remote *Remote, t task, force bool) error {
	me, err := l.Info()
	if err != nil {
		return err
	}
	ri, err := remote.Info()
	if err != nil {
		return err
	}
	rec, err := remote.RecordGet(t.Key)
	if err != nil {
		return err
	}
	branch, _ := git(l.abs(t.WtRel), "symbolic-ref", "-q", "--short", "HEAD")
	if rec == nil {
		note("task", "%s has no record of %s", ri.Name, t.Name)
	} else {
		if rec.Owner != me.Name && !force {
			return fmt.Errorf("%s is on %s (its record says %s owns it); bring it back with `ferry from %s %s` first, or pass --force to tear it down there anyway", t.Name, ri.Name, rec.Owner, remote.Label, t.Name)
		}
		if rec.Branch != "" {
			branch = rec.Branch
		}
		if err := checkNothingNewer(l, remote, ri, rec, t, branch, force); err != nil {
			return err
		}
	}
	res, err := remote.Teardown(TeardownReq{Task: t.Key, WtRel: t.WtRel, RepoRel: t.RepoRel, Branch: branch})
	for _, d := range res.Done {
		step("teardown", "%s: %s", ri.Name, d)
	}
	if err != nil {
		return err
	}
	if n := l.forgetTask(l.abs(t.RepoRel), t.Key); n > 0 {
		step("teardown", "here: deleted ferry's refs and parked copies (%d); the worktree stays", n)
	}
	step("done", "%s is gone from %s", t.Name, ri.Name)
	return nil
}

// checkNothingNewer refuses when remote's copy of t has work the laptop's
// doesn't: uncommitted changes, or commits on the branch it never sent. It's
// the check a handoff makes before overwriting the receiver.
func checkNothingNewer(l *Local, remote *Remote, ri Info, rec *Record, t task, branch string, force bool) error {
	if branch == "" {
		return nil
	}
	state, err := remote.RepoState(t.RepoRel)
	if err != nil || !state.Exists {
		return err
	}
	if wt, err := remote.Worktree(t.WtRel); err != nil || wt.Key == "" {
		return nil // the worktree is already gone there
	}
	incoming := taskRef(t.Key, "incoming")
	if err := moveRefs(l, remote, t.RepoRel, "+refs/heads/"+branch+":"+incoming); err != nil {
		return err
	}
	u, err := remote.Unsent(UnsentReq{RepoRel: t.RepoRel, WtRel: t.WtRel, Branch: branch, Base: incoming,
		Previous: []string{rec.LastSent[ri.Name], rec.Attempt}})
	if err != nil || u.Empty() {
		return err
	}
	review("receiver", "%s: %s:\n%s", ri.Name, u.Summary(t.WtRel, branch), gutter(forTerminal(u.Detail())))
	if force {
		warn("teardown", "tearing it down anyway (--force)")
		return nil
	}
	return fmt.Errorf("%s has work the laptop doesn't; bring it back with `ferry from %s %s`, or pass --force to throw it away", ri.Name, remote.Label, t.Name)
}
