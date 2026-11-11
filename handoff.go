package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type handoffOpts struct {
	Agent        string
	SessionID    string
	Yes          bool
	NoLaunch     bool
	Force        bool
	ForceVersion bool
	// SkipScripts skips a .ferry script that would ask first, instead of
	// asking, or stopping when there is no terminal.
	SkipScripts bool
	// Check runs every check that can refuse the handoff, then stops before
	// anything is stopped, recorded, or moved.
	Check bool
}

// task identifies the worktree being moved. Paths are relative to home so
// they mean the same thing on both machines.
type task struct {
	Key     string
	Name    string
	WtRel   string
	RepoRel string
}

// taskHere resolves the git worktree containing dir on this machine.
func taskHere(l *Local, dir string) (task, error) {
	top, err := git(dir, "rev-parse", "--show-toplevel")
	if err != nil {
		return task{}, errors.New("not inside a git worktree (ferry v1 only moves git repos)")
	}
	common, err := git(dir, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return task{}, err
	}
	repo := filepath.Dir(common)
	home, _ := filepath.EvalSymlinks(l.Home)
	top, _ = filepath.EvalSymlinks(top)
	repo, _ = filepath.EvalSymlinks(repo)
	src := filepath.Join(home, "src")
	wtRel, err1 := filepath.Rel(home, top)
	repoRel, err2 := filepath.Rel(home, repo)
	if err1 != nil || err2 != nil || !strings.HasPrefix(top, src+string(filepath.Separator)) || !strings.HasPrefix(repo, src+string(filepath.Separator)) {
		return task{}, fmt.Errorf("%s is outside ~/src; ferry only moves work under ~/src", top)
	}
	return task{Key: taskKey(wtRel), Name: filepath.Base(top), WtRel: wtRel, RepoRel: repoRel}, nil
}

// move is one handoff in progress: the machine giving the task up, the one
// taking it, the hub that keeps the ownership record, and the record itself.
type move struct {
	src, dst, hub Machine
	t             task
	opts          handoffOpts
	si, di        Info
	direction     string // "to" or "from", as typed
	rec           *Record
	// saved are fingerprints of dst's work that checkReceiver saved to a
	// branch, so Apply may overwrite it.
	saved []string
}

// command is the ferry command that runs this handoff again. It names the
// other machine as typed, which is the hub, whichever way the task moves.
func (m *move) command() string {
	c := "ferry " + m.direction + " " + fmt.Sprint(m.hub)
	if m.direction == "from" {
		c += " " + m.t.Name
	}
	return c
}

// handoff moves task t from src to dst. hub keeps the ownership record; it is
// always the remote machine, because the laptop drives every handoff. Each
// step below is a method on move, in the order they run.
func handoff(src, dst, hub Machine, t task, opts handoffOpts) error {
	m := &move{src: src, dst: dst, hub: hub, t: t, opts: opts, direction: "to"}
	if !src.IsLocal() {
		m.direction = "from"
	}
	var err error
	if m.si, err = src.Info(); err != nil {
		return err
	}
	if m.di, err = dst.Info(); err != nil {
		return err
	}
	if m.si.Name == m.di.Name {
		return fmt.Errorf("both machines are named %q; set one in ~/.config/ferry/name", m.si.Name)
	}
	for _, i := range []Info{m.si, m.di} {
		if err := checkRsync(i.Name, i.Rsync); err != nil {
			return err
		}
	}
	header(t, m.si.Name, m.di.Name)
	clock = newStopwatch()
	defer clock.report()

	if m.rec, err = hub.RecordGet(t.Key); err != nil {
		return err
	}

	// A rerun after the commit point only needs to finish the last steps, and
	// it uses the sessions this handoff recorded rather than searching again.
	if r := m.rec; r != nil && r.State == stateLanding && r.Owner == m.di.Name && r.From == m.si.Name {
		if m.opts.Check {
			warn("check", "%s already owns %s; `%s` finishes the handoff", m.di.Name, t.Name, m.command())
			return nil
		}
		warn("owner", "%s owns %s %s; finishing the handoff", m.di.Name, t.Name, dim(fmt.Sprintf("(generation %d)", r.Generation)))
		if err := m.resendIfResumed(); err != nil {
			return err
		}
		return m.finish(r.Source, r.Installed, nil)
	}

	resuming, err := m.mayHandOff()
	if err != nil {
		return err
	}
	if err := m.checkReceiver(); err != nil {
		return err
	}
	sess, err := m.stopAgent()
	if err != nil {
		return err
	}
	if m.opts.Check {
		step("check", "nothing found that would stop the handoff")
		return nil
	}
	if err := m.claim(sess, resuming); err != nil {
		return err
	}
	clock.lap("preflight")

	// What Claude will load here, before anything from the other machine
	// lands, so finish() can tell what the handoff changed.
	var cfgBefore map[string]string
	if lm, ok := dst.(*Local); ok && sess != nil && sess.Agent == agentClaude {
		cfgBefore = claudeConfig(lm.Home, claudeDirs(lm.abs(t.WtRel), lm.abs(mustRel(m.si.Home, sess.Cwd))))
	}

	if err := m.dehydrate(); err != nil {
		return err
	}
	clock.lap("dehydrate")

	snap, err := m.sendCode()
	if err != nil {
		return err
	}
	clock.lap("git")

	extras, sentExtras, err := m.sendExtras()
	if err != nil {
		return err
	}
	clock.lap("extras")

	if err := m.sendGitHooks(); err != nil {
		return err
	}

	sess, installed, err := m.stageSession(sess)
	if err != nil {
		return err
	}
	clock.lap("session")

	if err := m.commit(snap, sess, installed, extras, sentExtras); err != nil {
		return err
	}
	return m.finish(sess, installed, cfgBefore)
}

// save writes the record as the next generation. It fails if anything else
// wrote the record since this run read it.
func (m *move) save() error {
	expect := m.rec.Generation
	m.rec.Generation = expect + 1
	return m.hub.RecordPut(RecordPutReq{Expect: expect, Record: *m.rec})
}

// mayHandOff decides whether src may hand the task off, and reports whether
// this run continues one src -> dst already started. It's decided before
// stopAgent, which may stop an agent on the other machine.
func (m *move) mayHandOff() (bool, error) {
	rec, si, t := m.rec, m.si, m.t
	if rec == nil {
		return false, nil
	}
	if rec.WtRel != t.WtRel || rec.RepoRel != t.RepoRel {
		return false, fmt.Errorf("task record %s is for ~/%s, not ~/%s", t.Key, rec.WtRel, t.WtRel)
	}
	resuming := rec.State == stateHandingOff && rec.From == si.Name && rec.To == m.di.Name
	switch {
	case resuming:
		warn("owner", "continuing an interrupted handoff %s", dim(fmt.Sprintf("(generation %d)", rec.Generation)))
	case rec.State == stateHandingOff && !m.opts.Force:
		return false, fmt.Errorf("a handoff %s -> %s is unfinished; finish it from that direction, or pass --force", rec.From, rec.To)
	case rec.State == stateLanding && !m.opts.Force:
		// Landing means the task moved and only the launch is left, which a
		// rerun retries. One that keeps failing (an agent waiting at a
		// prompt) ends with --no-launch, which finishes without starting it.
		finish := "ferry to " + fmt.Sprint(m.hub)
		if rec.To != fmt.Sprint(m.hub) {
			finish = "ferry from " + fmt.Sprint(m.hub) + " " + t.Name
		}
		return false, fmt.Errorf("the handoff %s -> %s hasn't finished; rerun `%s` first, or `%s --no-launch` to finish it without starting the agent", rec.From, rec.To, finish, finish)
	case rec.Owner != si.Name && !m.opts.Force:
		return false, fmt.Errorf("%s owns %s, not %s; use `ferry from %s %s`", rec.Owner, t.Name, si.Name, rec.Owner, t.Name)
	}
	return resuming, nil
}

// checkReceiver looks for work on dst that the handoff would overwrite,
// before anything is stopped or recorded, and offers to save it to a branch
// there first. It moves src's branch tip to dst to compare against. Apply
// checks again when it overwrites, so work done on dst after this still
// stops the handoff.
func (m *move) checkReceiver() error {
	src, dst, t, di := m.src, m.dst, m.t, m.di
	ds, err := dst.RepoState(t.RepoRel)
	if err != nil {
		return err
	}
	if !ds.Exists || len(ds.Roots) == 0 || ds.Branch == placeholderBranch {
		return nil // sendCode creates the repo; there's nothing to lose
	}
	ws, err := src.RepoState(t.WtRel)
	if err != nil {
		return err
	}
	if !sharesRoot(ws.Roots, ds.Roots) {
		return m.differentRepo()
	}
	if ws.Branch == "" {
		return nil // Snapshot refuses a detached HEAD
	}
	incoming := taskRef(t.Key, "incoming")
	if err := moveRefs(src, dst, t.RepoRel, "+refs/heads/"+ws.Branch+":"+incoming); err != nil {
		return err
	}
	u, err := dst.Unsent(UnsentReq{RepoRel: t.RepoRel, WtRel: t.WtRel, Branch: ws.Branch, Base: incoming, Previous: m.known()})
	if err != nil || u.Empty() {
		return err
	}
	review("receiver", "%s: %s; the handoff would overwrite it:\n%s", di.Name, u.Summary(t.WtRel, ws.Branch), gutter(forTerminal(u.Detail())))
	name := "ferry-saved/" + t.Name + "-" + time.Now().Format("20060102-150405")
	switch {
	case m.opts.Force:
		warn("receiver", "overwriting it (--force)")
		return nil
	case m.opts.Check:
		warn("receiver", "`%s` will offer to save it to a branch on %s first", m.command(), di.Name)
		return nil
	case !interactive():
		return fmt.Errorf("%s has work this handoff would overwrite; run ferry at a terminal to save it to a branch there first, commit or discard it on %s, or pass --force to overwrite it", di.Name, di.Name)
	}
	choice, err := pick(di.Name+"'s copy would be overwritten:", []string{
		"save it to branch " + name + " on " + di.Name + ", then hand off",
		"stop; change nothing",
	})
	if err != nil || choice != 0 {
		return errors.New("stopped; nothing was handed off")
	}
	saved, err := dst.SaveUnsent(SaveUnsentReq{RepoRel: t.RepoRel, WtRel: t.WtRel, Branch: ws.Branch, Name: name})
	if err != nil {
		return err
	}
	m.saved = saved.Known
	step("saved", "%s: %s %s", di.Name, strings.Join(saved.Branches, ", "), dim("(git -C ~/"+t.RepoRel+" show "+saved.Branches[0]+")"))
	return nil
}

// known are fingerprints of states ferry left in dst's worktree, which the
// handoff may overwrite: what dst had when it last sent the task away, what
// an interrupted attempt applied, and what checkReceiver saved.
func (m *move) known() []string {
	var k []string
	if m.rec != nil {
		k = append(k, m.rec.LastSent[m.di.Name], m.rec.Attempt)
	}
	return append(k, m.saved...)
}

func (m *move) differentRepo() error {
	return fmt.Errorf("%s:%s is a different repository (no shared root commit); move it aside first", m.di.Name, m.t.RepoRel)
}

// stopAgent finds the session to move and makes sure no agent has it
// loaded, stopping an idle one in Rex on kant. It returns nil when the
// worktree has no session, and the code moves alone.
func (m *move) stopAgent() (*Session, error) {
	src, rec, si, di, t := m.src, m.rec, m.si, m.di, m.t
	find := FindSessionReq{Agent: m.opts.Agent, ID: m.opts.SessionID, CwdRel: t.WtRel}
	switch {
	case m.opts.SessionID != "":
	case rec != nil && rec.State == stateHandingOff && rec.From == si.Name && rec.Source != nil:
		find = FindSessionReq{Agent: rec.Source.Agent, ID: rec.Source.ID}
	case rec != nil && rec.Session != "":
		// The session the task last moved with may live outside the
		// worktree's own project folder (it started in a folder above), so
		// it's a candidate alongside the worktree's newest; newest wins.
		find.KnownAgent, find.KnownID = rec.Agent, rec.Session
	}
	sess, err := src.FindSession(find)
	if err != nil {
		return nil, err
	}
	if sess == nil {
		note("agent", "no Claude or Codex session found for this worktree; moving code only")
		return nil, nil
	}
	if err := sessionFits(si.Home, *sess, t); err != nil {
		return nil, err
	}
	step("agent", "%s %s, %s", sess.Agent, short(sess.ID), humanSize(sess.Size))
	// Everything that can refuse runs before the agent is stopped: once
	// it's gone from the live registry, a rerun can't find it that way.
	if live, err := m.dst.Liveness(*sess); err != nil {
		return nil, err
	} else if live.Live && !live.Settling {
		return nil, fmt.Errorf("%s already has this session loaded (%s)", di.Name, live.Detail)
	}
	have, want := agentVersion(si, sess.Agent), agentVersion(di, sess.Agent)
	if sess.Version != "" {
		have = sess.Version
	}
	switch {
	case have == "" || want == "":
		warn("versions", "%s version unknown on one side (here %q, %s %q); not checked", sess.Agent, si.Claude+si.Codex, di.Name, want)
	case versionLess(want, have) && !m.opts.ForceVersion:
		return nil, fmt.Errorf("%s has %s %s, older than %s here; upgrade it or pass --force-version", di.Name, sess.Agent, want, have)
	default:
		step("versions", "%s %s on %s, %s on %s", sess.Agent, have, si.Name, want, di.Name)
	}
	if m.opts.Check {
		// A check runs before the agent exits, so a live agent is expected.
		return sess, nil
	}
	live, err := settle(src, *sess)
	if err != nil {
		return nil, err
	}
	if !live.Live {
		return sess, nil
	}
	if src.IsLocal() && sess.ClaudeWorktree != "" {
		return nil, fmt.Errorf("%s in its own worktree; close its pane (or /exit and pick Keep; /exit deletes a worktree with no changes), then run this again", live.Detail)
	}
	if src.IsLocal() {
		return nil, fmt.Errorf("%s; exit the agent, then run this again", live.Detail)
	}
	step("agent", "%s: stopping the idle agent in rex", si.Name)
	stop := StopReq{Session: *sess}
	if rec != nil && rec.Owner == si.Name {
		stop.RexSession, stop.Name = rec.RexSession, t.Name
	}
	if err := src.StopAgent(stop); err != nil {
		return nil, err
	}
	// The agent writes its last lines on the way out; the record keeps the
	// session as it is once that stops, since a rerun compares against it.
	if _, err := settle(src, *sess); err != nil {
		return nil, err
	}
	if sess, err = src.FindSession(FindSessionReq{Agent: sess.Agent, ID: sess.ID}); err != nil {
		return nil, err
	} else if sess == nil {
		return nil, fmt.Errorf("%s: the session disappeared after stopping the agent", si.Name)
	}
	return sess, nil
}

// claim records the handoff as under way, src -> dst, unless this run
// continues one that already is.
func (m *move) claim(sess *Session, resuming bool) error {
	t := m.t
	if m.rec == nil {
		m.rec = &Record{Task: t.Key, Name: t.Name, WtRel: t.WtRel, RepoRel: t.RepoRel, Owner: m.si.Name, State: stateOwned}
		step("owner", "new task")
	}
	if resuming {
		return nil
	}
	rec := m.rec
	rec.State, rec.From, rec.To = stateHandingOff, m.si.Name, m.di.Name
	rec.Source, rec.Installed = sess, nil
	if sess != nil {
		rec.Agent, rec.Session = sess.Agent, sess.ID
	}
	return m.save()
}

// dehydrate runs .ferry/dehydrate on src. Once a task has been on kant, the
// laptop's worktree holds code kant wrote, and dehydrate may run it
// (bin/rails, docker compose), so on the laptop it asks every time.
func (m *move) dehydrate() error {
	src, t := m.src, m.t
	ask, changes := false, ""
	if src.IsLocal() {
		if _, err := git(src.GitURL(t.WtRel), "rev-parse", "--verify", "-q", sentRef(t.Key)); err == nil {
			ask, changes = true, changeSummary(src.GitURL(t.WtRel), t.Key, "")
		}
	}
	_, err := runHook(src, m.si.Name, HookReq{WtRel: t.WtRel, RepoRel: t.RepoRel, Name: "dehydrate", Task: t.Name, Direction: m.direction}, ask, changes, m.opts.SkipScripts)
	return err
}

// sendCode creates the repo on dst if it's missing, snapshots src's
// worktree into refs/ferry/<task>/*, moves those refs, and has dst check out
// the snapshot and verify it.
func (m *move) sendCode() (SnapshotResult, error) {
	src, dst, t, di, rec := m.src, m.dst, m.t, m.di, m.rec
	ss, err := src.RepoState(t.RepoRel)
	if err != nil {
		return SnapshotResult{}, err
	}
	ds, err := dst.RepoState(t.RepoRel)
	if err != nil {
		return SnapshotResult{}, err
	}
	if ds.Exists && len(ds.Roots) > 0 && !sharesRoot(ss.Roots, ds.Roots) {
		return SnapshotResult{}, m.differentRepo()
	}
	// The snapshot comes first: it can refuse (a detached HEAD, a nested
	// checkout git can't carry), and a refusal shouldn't leave a new repo on dst.
	snap, err := src.Snapshot(SnapshotReq{WtRel: t.WtRel, Task: t.Key})
	if err != nil {
		return SnapshotResult{}, err
	}
	// A repo still on the placeholder branch was created by an earlier,
	// interrupted run; finish creating it the same way.
	fresh := !ds.Exists || len(ds.Roots) == 0 || ds.Branch == placeholderBranch
	if fresh {
		step("repo", "%s has no %s; creating it", di.Name, t.RepoRel)
		if err := dst.InitRepo(t.RepoRel); err != nil {
			return SnapshotResult{}, err
		}
		if err := moveRefs(src, dst, t.RepoRel, "+refs/heads/*:refs/heads/*", "+refs/tags/*:refs/tags/*"); err != nil {
			return SnapshotResult{}, err
		}
		if err := dst.FinishRepo(FinishRepoReq{RepoRel: t.RepoRel, Branch: ss.Branch}); err != nil {
			return SnapshotResult{}, err
		}
	} else {
		step("repo", "%s: %s exists, same history", di.Name, t.RepoRel)
	}
	step("snapshot", "branch %s at %s, %d changed paths", snap.Branch, snap.Base[:7], snap.Dirty)
	ref := "+refs/ferry/" + t.Key + "/*:refs/ferry/" + t.Key + "/*"
	if err := moveRefs(src, dst, t.RepoRel, ref); err != nil {
		return SnapshotResult{}, err
	}
	step("git", "moved %s to %s", dim("refs/ferry/"+t.Key+"/*"), di.Name)
	previous := m.known()
	if rec.Attempt != snap.Fingerprint {
		rec.Attempt = snap.Fingerprint
		if err := m.save(); err != nil {
			return SnapshotResult{}, err
		}
	}
	if _, err := dst.Apply(ApplyReq{RepoRel: t.RepoRel, WtRel: t.WtRel, Branch: snap.Branch, Task: t.Key, Expected: snap.Fingerprint, Previous: previous, Fresh: fresh, Force: m.opts.Force}); err != nil {
		return SnapshotResult{}, err
	}
	step("apply", "%s: %s matches the snapshot", di.Name, t.WtRel)
	if err := m.syncRemotes(snap, fresh); err != nil {
		return SnapshotResult{}, err
	}
	return snap, nil
}

// syncRemotes gives dst src's remotes and the branch's upstream, and moves
// dst's remote-tracking refs forward to src's. A repo ferry just created
// takes the config as it is. An existing one asks first, because a changed
// URL or upstream redirects the next push. Coming to the laptop, --yes
// doesn't answer: kant runs agents unattended, so a remote kant names is
// trusted no more than a hook kant wrote. Declining keeps dst's config; the
// tracking refs of remotes whose URLs match still move.
func (m *move) syncRemotes(snap SnapshotResult, fresh bool) error {
	dst, t, di := m.dst, m.t, m.di
	q := SyncRemotesReq{RepoRel: t.RepoRel, Task: t.Key, Branch: snap.Branch, Remotes: snap.Remotes, Upstream: snap.Upstream}
	q.DryRun = true
	plan, err := dst.SyncRemotes(q)
	if err != nil {
		return err
	}
	q.DryRun, q.Config = false, true
	if len(plan.Changes) > 0 {
		step("remotes", "%s: %s", di.Name, strings.Join(plan.Changes, "; "))
		if !fresh {
			yes := m.opts.Yes && !dst.IsLocal()
			if !yes && !confirm("change these remotes on "+di.Name+"?") {
				warn("remotes", "kept %s's remotes as they are", di.Name)
				q.Config = false
			}
		}
	}
	res, err := dst.SyncRemotes(q)
	if err != nil {
		return err
	}
	switch {
	case res.Tracking == 1:
		step("remotes", "%s: moved 1 remote-tracking ref forward", di.Name)
	case res.Tracking > 1:
		step("remotes", "%s: moved %d remote-tracking refs forward", di.Name, res.Tracking)
	}
	if len(res.Differ) > 0 {
		note("remotes", "%s's URLs differ on %s; left its remote-tracking refs", strings.Join(res.Differ, ", "), di.Name)
	}
	return nil
}

// sendExtras sends the ignored files that differ on dst, after a yes, and
// removes there the ones src deleted since dst sent them. A file dst changed
// since it last sent it is parked there before it's overwritten. It returns
// the extras src listed, and whether dst now has them.
func (m *move) sendExtras() ([]FileEntry, bool, error) {
	src, dst, t, si, di := m.src, m.dst, m.t, m.si, m.di
	found, err := src.ListExtras(t.WtRel)
	if err != nil {
		return nil, false, err
	}
	extras := found.Files
	if len(found.LeftBehind) > 0 {
		note("left", "staying on %s (dependencies, build output, .ferry/exclude): %s", si.Name, listNames(firstFew(found.LeftBehind)))
	}
	if len(found.Links) > 0 {
		note("left", "symlinks staying on %s: %s", si.Name, listNames(firstFew(found.Links)))
	}
	// Extras di sent last time that si no longer has were deleted here.
	incoming := map[string]bool{}
	var paths []string
	for _, e := range extras {
		incoming[e.Path] = true
		paths = append(paths, e.Path)
	}
	var gone []FileEntry
	var goneNames []string
	for _, e := range m.rec.Extras[di.Name] {
		if !incoming[e.Path] {
			gone = append(gone, e)
			goneNames = append(goneNames, e.Path)
		}
	}
	if len(extras) == 0 && len(gone) == 0 {
		return extras, true, nil
	}

	prep, err := dst.PrepareExtras(PrepareExtrasReq{WtRel: t.WtRel, Task: t.Key, Paths: paths})
	if err != nil {
		return nil, false, err
	}
	plan := planExtras(extras, prep.Have, m.rec.Extras[di.Name])
	if names := describeFiles(plan.send); len(names) > 0 {
		step("extras", "%s", strings.Join(names, ", "))
	}
	if names := describeFiles(plan.same); len(names) > 0 {
		note("extras", "already on %s, not sent: %s", di.Name, strings.Join(names, ", "))
	}
	if len(plan.conflicts) > 0 {
		warn("extras", "changed on %s too since it last sent them; its copies get parked first: %s", di.Name, strings.Join(describeFiles(plan.conflicts), ", "))
	}
	prompt := "send these ignored files to " + di.Name + "?"
	if len(gone) > 0 {
		step("extras", "deleted on %s since %s sent them: %s", si.Name, di.Name, listNames(goneNames))
		prompt = "send these ignored files to " + di.Name + " and remove the deleted ones there?"
	}
	switch {
	case len(plan.send) == 0 && len(gone) == 0:
		return extras, true, nil
	case !m.opts.Yes && !confirm(prompt):
		warn("extras", "skipped")
		return extras, false, nil
	}
	// Park dst's changed copies before rsync replaces them. One that changed
	// again since PrepareExtras looked is left alone and not sent.
	parked, err := dst.RemoveExtras(RemoveExtrasReq{WtRel: t.WtRel, Task: t.Key, Files: plan.conflicts})
	if err != nil {
		return nil, false, err
	}
	if len(parked.Removed) > 0 {
		step("extras", "%s: parked its copies of %s in ~/.local/state/ferry/parked/", di.Name, listNames(parked.Removed))
	}
	skip := map[string]bool{}
	for _, p := range parked.Kept {
		skip[p] = true
	}
	if len(parked.Kept) > 0 {
		warn("extras", "%s: not sending %s, changed there during the handoff", di.Name, listNames(parked.Kept))
	}
	job := rsyncJob{SrcRel: t.WtRel, DstRel: t.WtRel, Private: true, WorkDir: filepath.Join(di.Home, extrasWork(t.Key))}
	for _, e := range plan.send {
		if !skip[e.Path] {
			job.Files = append(job.Files, e.Path)
			job.Total += e.Size
		}
	}
	if err := rsyncTo(src, dst, job); err != nil {
		return nil, false, err
	}
	res, err := dst.RemoveExtras(RemoveExtrasReq{WtRel: t.WtRel, Task: t.Key, Files: gone})
	if err != nil {
		return nil, false, err
	}
	if len(res.Removed) > 0 {
		step("extras", "%s: removed %s (parked in ~/.local/state/ferry/parked/)", di.Name, listNames(res.Removed))
	}
	if len(res.Kept) > 0 {
		note("extras", "%s: kept %s, changed there since it was sent", di.Name, listNames(res.Kept))
	}
	return extras, true, nil
}

// extrasPlan sorts the extras a handoff found by what dst has.
type extrasPlan struct {
	send, same []FileEntry
	// conflicts are dst's own entries for files in send that dst changed
	// since it last sent them, or never sent. Overwriting one would lose
	// work, so it's parked first.
	conflicts []FileEntry
}

// planExtras compares the extras src found with the ones dst has, and with
// the ones dst sent when it last handed the task away. rsync keeps mtimes,
// so a file is the same on both sides when its size and mtime match, which
// is rsync's own quick check.
func planExtras(extras, have, dstSent []FileEntry) extrasPlan {
	there := map[string]FileEntry{}
	for _, e := range have {
		there[e.Path] = e
	}
	sent := map[string]FileEntry{}
	for _, e := range dstSent {
		sent[e.Path] = e
	}
	var p extrasPlan
	for _, e := range extras {
		h, ok := there[e.Path]
		switch {
		case ok && h.Size == e.Size && h.MTime == e.MTime:
			p.same = append(p.same, e)
		case ok && h != sent[e.Path]:
			p.send = append(p.send, e)
			p.conflicts = append(p.conflicts, h)
		default:
			p.send = append(p.send, e)
		}
	}
	return p
}

// firstFew shortens a long list of names for one line of output.
func firstFew(names []string) []string {
	if len(names) <= 8 {
		return names
	}
	return append(names[:8:8], fmt.Sprintf("and %d more", len(names)-8))
}

// sendGitHooks copies the hooks in src's .git/hooks that dst lacks or has
// different. A hook runs on the next git command in the repo, so hooks
// coming to the laptop install only with a yes on this run, like restore.
// Going to kant they install as they are.
func (m *move) sendGitHooks() error {
	srcHooks, err := m.src.GitHooks(m.t.RepoRel)
	if err != nil {
		return err
	}
	dstHooks, err := m.dst.GitHooks(m.t.RepoRel)
	if err != nil {
		return err
	}
	send := hooksToSend(srcHooks, dstHooks)
	if len(send) == 0 {
		return nil
	}
	var names []string
	for _, h := range send {
		names = append(names, h.Name)
	}
	step("hooks", "git hooks from %s: %s", m.si.Name, strings.Join(names, ", "))
	if m.dst.IsLocal() && !confirmRun("install these git hooks here? they run on git commands in this repo") {
		warn("hooks", "skipped; %s's hooks stay as they were", m.di.Name)
		return nil
	}
	return m.dst.InstallGitHooks(InstallGitHooksReq{RepoRel: m.t.RepoRel, Hooks: send})
}

// stageSession sends the session to dst and stages it there, with paths
// rewritten, but nothing live moves until after the commit point. A failure
// before then leaves the source exactly as it was. It returns the session as
// sent and as staged.
func (m *move) stageSession(sess *Session) (*Session, *Session, error) {
	if sess == nil {
		return nil, nil, nil
	}
	// Measure the session again just before sending it. finish() compares
	// the source against this, so it must describe what was sent, not what
	// was found before the steps above.
	cur, err := m.src.FindSession(FindSessionReq{Agent: sess.Agent, ID: sess.ID})
	if err != nil {
		return nil, nil, err
	} else if cur == nil {
		return nil, nil, fmt.Errorf("%s: session %s disappeared before it was sent", m.si.Name, short(sess.ID))
	}
	installed, err := m.sendSession(*cur)
	if err != nil {
		return nil, nil, err
	}
	return cur, installed, nil
}

// sendSession copies a session into in/<task>/session on dst and stages it
// in in/<task>/ready, with paths rewritten. Nothing live changes.
func (m *move) sendSession(sess Session) (*Session, error) {
	stage := filepath.Join(".local", "state", "ferry", "in", m.t.Key, "session")
	if err := rsyncTo(m.src, m.dst, rsyncJob{SrcRel: ".", DstRel: stage, Files: sess.Files, Dirs: true, Total: sess.Size}); err != nil {
		return nil, err
	}
	s, err := m.dst.StageSession(InstallReq{Session: sess, OldHome: m.si.Home, StageRel: stage, Task: m.t.Key})
	if err != nil {
		return nil, err
	}
	step("session", "%s staged on %s, %s", short(sess.ID), m.di.Name, dim(fmt.Sprintf("paths %s -> %s", m.si.Home, m.di.Home)))
	return &s, nil
}

// commit records dst as the owner. This is the commit point: before it, a
// failure leaves src owning the task untouched; after it, dst owns it and a
// rerun finishes the handoff.
func (m *move) commit(snap SnapshotResult, sess, installed *Session, extras []FileEntry, sentExtras bool) error {
	rec, si, di, t := m.rec, m.si, m.di, m.t
	if rec.LastSent == nil {
		rec.LastSent = map[string]string{}
	}
	rec.LastSent[si.Name] = snap.Fingerprint
	rec.Attempt = ""
	// A resumed claim kept the Source it recorded; a rerun of finish()
	// compares against the copy this run sent.
	rec.Source, rec.Installed = sess, installed
	delete(rec.LastSent, di.Name)
	if rec.Extras == nil {
		rec.Extras = map[string][]FileEntry{}
	}
	delete(rec.Extras, di.Name)
	delete(rec.Extras, si.Name)
	if sentExtras && len(extras) > 0 {
		rec.Extras[si.Name] = extras
	}
	rec.State, rec.Owner, rec.Branch, rec.RexSession = stateLanding, di.Name, snap.Branch, ""
	if err := m.save(); err != nil {
		return err
	}
	commitPoint("commit", "%s owns %s %s", di.Name, t.Name, dim(fmt.Sprintf("(generation %d)", rec.Generation)))
	if m.src.IsLocal() {
		// What this machine sent, kept here where the other machine can't
		// change it, for the change summary when the task comes back.
		if _, err := git(m.src.GitURL(t.WtRel), "update-ref", sentRef(t.Key), taskRef(t.Key, "wip")); err != nil {
			return err
		}
	}
	return nil
}

// resendIfResumed runs on a rerun that finishes a landing handoff. If the
// source's copy has grown since the commit recorded it, someone resumed it
// there, most likely because the failed run looked like it hadn't handed
// off. The receiver would otherwise get the older copy, and the next handoff
// back would park those turns. So the newer copy is sent and staged in its
// place, and the record is updated to say so. An agent still running it on
// the source would keep writing, so that refuses.
func (m *move) resendIfResumed() error {
	rec, si := m.rec, m.si
	if rec.Source == nil || rec.Installed == nil {
		return nil
	}
	cur, err := m.src.FindSession(FindSessionReq{Agent: rec.Source.Agent, ID: rec.Source.ID})
	if err != nil {
		return err
	}
	if cur == nil || cur.Size == rec.Source.Size {
		return nil
	}
	if live, err := m.src.Liveness(*cur); err != nil {
		return err
	} else if live.Live && !live.Settling {
		return fmt.Errorf("%s: session %s has turns added since it was handed off, and %s; exit it, then run this again to send them", si.Name, short(cur.ID), live.Detail)
	}
	step("session", "%s: %s grew since the handoff (%s, was %s); sending it again", si.Name, short(cur.ID), humanSize(cur.Size), humanSize(rec.Source.Size))
	installed, err := m.sendSession(*cur)
	if err != nil {
		return err
	}
	rec.Source, rec.Installed = cur, installed
	if err := m.save(); err != nil {
		return err
	}
	commitPoint("commit", "%s owns %s %s", m.di.Name, m.t.Name, dim(fmt.Sprintf("(generation %d)", rec.Generation)))
	return nil
}

// sessionFits checks that a session belongs to the worktree being moved: it
// started there, or it started in a folder above it inside ~/src and has
// since moved into it (Claude opened in ~/src, then working in ~/src/app).
func sessionFits(home string, s Session, t task) error {
	started := mustRel(home, s.Cwd)
	if within(started, t.WtRel) {
		return nil
	}
	if s.WorkDir != "" && within(mustRel(home, s.WorkDir), t.WtRel) && within(t.WtRel, started) && within(started, "src") {
		return nil
	}
	return fmt.Errorf("session %s belongs to ~/%s, not this worktree (~/%s)", short(s.ID), started, t.WtRel)
}

// listNames joins file names for the output, with control characters
// escaped: the names can come from the other machine.
func listNames(names []string) string {
	out := make([]string, len(names))
	for i, n := range names {
		out[i] = nameForTerminal(n)
	}
	return strings.Join(out, ", ")
}

// describeFiles lists files with their sizes for the output, names escaped
// as listNames does. A long list is folded into one line per top-level folder.
func describeFiles(files []FileEntry) []string {
	if len(files) <= 8 {
		var out []string
		for _, e := range files {
			out = append(out, fmt.Sprintf("%s (%s)", nameForTerminal(e.Path), humanSize(e.Size)))
		}
		return out
	}
	type group struct {
		n    int
		size int64
	}
	groups := map[string]*group{}
	var order []string
	for _, e := range files {
		top, _, nested := strings.Cut(filepath.ToSlash(e.Path), "/")
		if nested {
			top += "/"
		}
		g := groups[top]
		if g == nil {
			g = &group{}
			groups[top] = g
			order = append(order, top)
		}
		g.n++
		g.size += e.Size
	}
	var out []string
	for _, top := range order {
		g := groups[top]
		top = nameForTerminal(top)
		if g.n == 1 {
			out = append(out, fmt.Sprintf("%s (%s)", top, humanSize(g.size)))
		} else {
			out = append(out, fmt.Sprintf("%s (%d files, %s)", top, g.n, humanSize(g.size)))
		}
	}
	return out
}

// settle waits up to ten seconds for a session's transcript to stop changing.
func settle(m Machine, s Session) (Liveness, error) {
	live, err := m.Liveness(s)
	for i := 0; err == nil && live.Settling && i < 20; i++ {
		if i == 0 {
			note("agent", "%s; waiting for it to settle", live.Detail)
		}
		time.Sleep(500 * time.Millisecond)
		live, err = m.Liveness(s)
	}
	return live, err
}

// finish runs the steps after the commit point: move the staged session into
// place, run restore, and start the agent. Each is safe to repeat. The
// sending machine keeps its copy of the session, so either side can resume
// it; a copy the receiver already had is parked before it's replaced.
//
// cfgBefore is what Claude would load here before the handoff (see
// claudeConfig); nil when this machine isn't receiving a Claude session, or
// on a rerun, which can't know.
func (m *move) finish(sess, installed *Session, cfgBefore map[string]string) error {
	dst, t, rec, si, di, direction, opts := m.dst, m.t, m.rec, m.si, m.di, m.direction, m.opts
	if installed != nil {
		if err := dst.PlaceSession(PlaceReq{Session: *installed, Task: t.Key}); err != nil {
			return err
		}
		step("session", "%s: %s is live", di.Name, short(installed.ID))
	}
	clock.lap("commit")
	changes := ""
	if dst.IsLocal() {
		changes = changeSummary(dst.GitURL(t.WtRel), t.Key, taskRef(t.Key, "wip"))
	}
	summary, err := runHook(dst, di.Name, HookReq{WtRel: t.WtRel, RepoRel: t.RepoRel, Name: "restore", Task: t.Name, Direction: direction}, dst.IsLocal(), changes, opts.SkipScripts)
	if err != nil {
		bad("restore", "failed: %v", err)
		warn("next", "fix it, then run `%s` again to finish", m.command())
		return errors.New("restore failed; the task moved but the agent wasn't started")
	}
	clock.lap("restore")
	rexSession := ""
	var here []string // the agent to run in this terminal once the record is written
	switch {
	case installed == nil:
		step("done", "code is on %s at ~/%s", di.Name, t.WtRel)
	default:
		msg := resumeNote(si, di, t, *installed, summary)
		argv := resumeArgv(*installed, msg)
		cwdRel, _ := filepath.Rel(di.Home, installed.Cwd)
		// Claude runs project hooks the moment it starts, so if the handoff
		// changed what it loads here, starting it needs a yes on this run.
		var changed []string
		if lm, ok := dst.(*Local); ok && installed.Agent == agentClaude {
			changed = configChanges(lm.Home, cfgBefore, claudeConfig(lm.Home, claudeDirs(lm.abs(t.WtRel), installed.Cwd)))
			if len(changed) > 0 {
				warn("claude", "config Claude loads here, changed by the handoff: %s", listNames(changed))
			}
		}
		if dst.IsLocal() && !opts.NoLaunch && interactive() {
			// A rerun can come after you resumed the session here by hand;
			// starting it again would put two agents on one transcript.
			if live, err := dst.Liveness(*installed); err != nil {
				return err
			} else if live.Live && !live.Settling {
				note("resume", "%s is already running here (%s); not starting another", short(installed.ID), live.Detail)
				break
			}
			if len(changed) == 0 || confirmRun("start claude here now? hooks in that config run as it starts") {
				here = argv
				step("resume", "cd ~/%s && %s", cwdRel, shellJoin(argv[:len(argv)-1]))
				break
			}
		}
		if dst.IsLocal() || opts.NoLaunch || !di.Rex {
			step("resume", "cd ~/%s && %s", cwdRel, resumeCommand(argv))
			break
		}
		// A rerun may find the agent already started by the run it is finishing.
		if live, err := dst.Liveness(*installed); err != nil {
			return err
		} else if live.Live && !live.Settling {
			note("launch", "%s: %s is already running (%s); not starting another", di.Name, short(installed.ID), live.Detail)
			rexSession = rec.RexSession
			break
		}
		launched, err := dst.Launch(LaunchReq{Name: t.Name, CwdRel: cwdRel, Argv: argv, Agent: installed.Agent, Session: *installed, AcceptTrust: sess != nil && sess.Trusted})
		if err == nil && launched.Untrusted {
			warn("launch", "neither %s nor %s trusts ~/%s; not starting an agent that would wait at the trust prompt", di.Name, si.Name, cwdRel)
			warn("resume", "on %s: cd ~/%s && %s", di.Name, cwdRel, resumeCommand(argv))
			break
		}
		if err != nil {
			// The record stays landing, so rerunning this command retries
			// the launch. A rerun that finds the agent running, because it
			// was started by hand on di, just finishes.
			bad("launch", "failed: %v", err)
			warn("next", "run `%s` again to retry, `%s --no-launch` to finish without starting it, or on %s: cd ~/%s && %s", m.command(), m.command(), di.Name, cwdRel, resumeCommand(argv))
			return errors.New("the task moved but the agent didn't start")
		}
		rexSession = launched.Session
		step("launch", "%s: rex session %q running %s --resume %s", di.Name, t.Name, installed.Agent, short(installed.ID))
	}
	if installed != nil && rexSession != "" {
		clock.lap("launch")
	}
	rec.State, rec.RexSession = stateOwned, rexSession
	if err := m.save(); err != nil {
		return err
	}
	if here != nil {
		clock.report()
		return execHere(installed.Cwd, here)
	}
	return nil
}

// execHere replaces ferry with the agent, in the worktree, in this terminal.
// It returns only if the exec fails. ferry's files, the task lock among them,
// are close-on-exec, so the lock is released as the agent starts.
func execHere(dir string, argv []string) error {
	path, err := exec.LookPath(argv[0])
	if err != nil {
		return fmt.Errorf("the task is here, but %s isn't on PATH; run the resume command above", argv[0])
	}
	if err := os.Chdir(dir); err != nil {
		return err
	}
	return fmt.Errorf("starting %s: %w; run the resume command above", argv[0], syscall.Exec(path, argv, os.Environ()))
}

// stopwatch times each step of a handoff, leaving out time spent at
// prompts, and prints one line at the end, even when the handoff fails.
type stopwatch struct {
	last     time.Time
	start    time.Time
	waited   time.Duration
	laps     []string
	reported bool
}

// clock is the running handoff's stopwatch. A process runs one handoff.
var clock = newStopwatch()

func newStopwatch() *stopwatch {
	now := time.Now()
	return &stopwatch{last: now, start: now}
}

func (s *stopwatch) lap(name string) {
	now := time.Now()
	s.laps = append(s.laps, name+" "+roundDuration(now.Sub(s.last)))
	s.last = now
}

// pause stops the clock until the returned function is called.
func (s *stopwatch) pause() func() {
	began := time.Now()
	return func() {
		d := time.Since(began)
		s.last = s.last.Add(d)
		s.waited += d
	}
}

func (s *stopwatch) report() {
	if len(s.laps) == 0 || s.reported {
		return
	}
	s.reported = true
	total := time.Since(s.start) - s.waited
	line := strings.Join(s.laps, ", ") + "; total " + roundDuration(total)
	if s.waited > time.Second {
		line += " (plus " + roundDuration(s.waited) + " at prompts)"
	}
	note("time", "%s", dim(line))
}

func roundDuration(d time.Duration) string {
	switch {
	case d < time.Second:
		return d.Round(10 * time.Millisecond).String()
	case d < time.Minute:
		return d.Round(100 * time.Millisecond).String()
	}
	return d.Round(time.Second).String()
}

// moveRefs copies refs from src's repo to dst's using whichever side is local.
func moveRefs(src, dst Machine, repoRel string, refspecs ...string) error {
	env := append([]string{"GIT_SSH_COMMAND=" + shellJoin(append([]string{"ssh"}, sshOptions...))}, gitEnv...)
	git := func(dir string, args ...string) error {
		_, err := run(dir, env, "git", append([]string{"-c", "commit.gpgsign=false"}, args...)...)
		return err
	}
	if src.IsLocal() {
		return git(src.GitURL(repoRel), append([]string{"push", "-q", dst.GitURL(repoRel)}, refspecs...)...)
	}
	return git(dst.GitURL(repoRel), append([]string{"fetch", "-q", src.GitURL(repoRel)}, refspecs...)...)
}

// runHook runs a project hook on m after the user approves its contents.
// Approval is remembered on m by content hash; --yes never approves a hook.
//
// Trust runs one way. kant is where agents run unattended, so code arriving
// from it is less trusted than code arriving from the laptop. A hook on the
// laptop that may run code kant wrote asks every time (everyRun), and shows
// what changed since the laptop last sent the task, because hooks run the
// repo's build tooling, which a kant agent can edit without touching the
// hook itself. changes is computed on the laptop, never read from kant.
func runHook(m Machine, where string, q HookReq, everyRun bool, changes string, skip bool) (string, error) {
	info, err := m.HookInfo(q)
	if err != nil || !info.Exists {
		return "", err
	}
	if info.TooLarge {
		warn(q.Name, "%s: .ferry/%s is over %d KB, too large to review; not running it", where, q.Name, maxHookSize>>10)
		return "", nil
	}
	if info.Refused != "" {
		return "", fmt.Errorf("%s: not running .ferry/%s: %s", where, q.Name, info.Refused)
	}
	if !info.Approved || everyRun {
		why := "isn't approved on " + where
		if everyRun {
			why = "may run code from the other machine; ferry asks before every run here"
		}
		review(q.Name, ".ferry/%s %s %s:\n%s", q.Name, why, dim("(sha "+info.SHA[:12]+")"), gutter(forTerminal(info.Content)))
		if changes != "" {
			review("", "changed since this machine last sent the task:\n%s", gutter(forTerminal(changes)))
		}
		// Skipping a hook nobody was asked about would move the task
		// without what dehydrate saves, or start the agent without what
		// restore sets up, and exit 0 as if all went well. So skipping
		// takes --skip-scripts.
		switch {
		case skip:
			warn(q.Name, "%s: skipped .ferry/%s (--skip-scripts)", where, q.Name)
			return "", nil
		case !interactive():
			return "", fmt.Errorf(".ferry/%s on %s needs a yes before it runs, and there's no terminal to ask at; run ferry at a terminal, or pass --skip-scripts to go on without it", q.Name, where)
		}
		prompt := fmt.Sprintf("run this %s on %s, now and whenever it's unchanged?", q.Name, where)
		if everyRun {
			prompt = fmt.Sprintf("run this %s on %s now?", q.Name, where)
		}
		if !confirmRun(prompt) {
			warn(q.Name, "skipped")
			return "", nil
		}
		if err := m.Approve(info.SHA); err != nil {
			return "", err
		}
	}
	q.SHA = info.SHA
	res, err := m.RunHook(q)
	if err != nil {
		return "", err
	}
	if !res.OK {
		fmt.Println(gutter(res.Output))
		return "", fmt.Errorf("%s failed on %s", q.Name, where)
	}
	step(q.Name, "%s: %s", where, res.Summary)
	return res.Summary, nil
}

func resumeNote(si, di Info, t task, s Session, summary string) string {
	oldCwd := filepath.Join(si.Home, mustRel(di.Home, s.Cwd))
	var b strings.Builder
	fmt.Fprintf(&b, "[ferry] This session moved from %s to %s. ", si.Name, di.Name)
	fmt.Fprintf(&b, "The working directory is now %s (it was %s), and paths under %s are now under %s. ", s.Cwd, oldCwd, si.Home, di.Home)
	b.WriteString("Committed and uncommitted changes came along. ")
	if summary != "" {
		fmt.Fprintf(&b, "Environment: %s. ", summary)
	} else {
		b.WriteString("The environment here is cold: dependencies, databases and dev servers from the other machine did not come along. ")
	}
	b.WriteString("Nothing that was running on the other machine is running here. Briefly confirm where things stand, then continue.")
	return b.String()
}

func resumeArgv(s Session, note string) []string {
	if s.Agent == agentCodex {
		argv := []string{"codex", "--no-daemon"}
		if s.Model != "" {
			argv = append(argv, "-c", fmt.Sprintf("model=%q", s.Model))
		}
		return append(argv, "resume", s.ID, note)
	}
	return []string{"claude", "--resume", s.ID, note}
}

func agentVersion(i Info, agent string) string {
	if agent == agentCodex {
		return i.Codex
	}
	return i.Claude
}

// versionLess compares dotted numeric versions; non-numeric parts compare as 0.
func versionLess(a, b string) bool {
	pa, pb := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(pa) || i < len(pb); i++ {
		var x, y int
		if i < len(pa) {
			x, _ = strconv.Atoi(strings.TrimFunc(pa[i], func(r rune) bool { return r < '0' || r > '9' }))
		}
		if i < len(pb) {
			y, _ = strconv.Atoi(strings.TrimFunc(pb[i], func(r rune) bool { return r < '0' || r > '9' }))
		}
		if x != y {
			return x < y
		}
	}
	return false
}

func short(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

func humanSize(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.0f KB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}

func mustRel(base, p string) string {
	r, err := filepath.Rel(base, p)
	if err != nil {
		return p
	}
	return r
}

// resumeCommand is the command that resumes the agent. The note at its end
// is long and the same every time, so it's dim on a terminal.
func resumeCommand(argv []string) string {
	n := len(argv) - 1
	return shellJoin(argv[:n]) + " " + dim(shellJoin(argv[n:]))
}

func shellJoin(argv []string) string {
	var parts []string
	for _, a := range argv {
		if a != "" && strings.IndexFunc(a, func(r rune) bool {
			return !(r == '-' || r == '_' || r == '.' || r == '/' || r == '=' || r == ':' || (r >= '0' && r <= '9') || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z'))
		}) < 0 {
			parts = append(parts, a)
			continue
		}
		parts = append(parts, "'"+strings.ReplaceAll(a, "'", `'\''`)+"'")
	}
	return strings.Join(parts, " ")
}

// setupFiles are files that build and setup tools execute or obey. A change to
// one is called out before a hook from the other machine runs here.
var setupFiles = []string{
	"package.json", "package-lock.json", "pnpm-lock.yaml", "yarn.lock", "bun.lockb", ".npmrc",
	"Gemfile", "Gemfile.lock", "mix.exs", "mix.lock", "go.mod", "go.sum", "Cargo.toml", "Cargo.lock",
	"pyproject.toml", "uv.lock", "poetry.lock", "setup.py", "mise.toml", ".mise.toml", ".tool-versions",
	"Makefile", "Justfile", "Procfile", "Rakefile", ".envrc", "Dockerfile",
}

// changeSummary lists the files that differ between what this machine last
// sent (its own sentRef) and to, or the worktree when to is empty, with setup
// and build files called out. It reads only this machine's refs.
func changeSummary(dir, task, to string) string {
	from := sentRef(task)
	if _, err := git(dir, "rev-parse", "--verify", "-q", from); err != nil {
		return "ferry has no record here of what this machine last sent, so it can't list what changed"
	}
	// What was sent is a snapshot of the whole worktree, untracked files
	// included, so it's compared with the same snapshot of the worktree now.
	// `git diff` against the worktree sees only paths in the index, and
	// would list a sent untracked file as deleted.
	if to == "" {
		tree, err := worktreeTree(dir)
		if err != nil {
			return "ferry couldn't snapshot the worktree to list what changed: " + err.Error()
		}
		to = tree
	}
	names, _ := git(dir, "diff", "--name-only", from, to)
	if names == "" {
		return "no files changed"
	}
	var setup, other []string
	for _, n := range strings.Split(names, "\n") {
		base := filepath.Base(n)
		risky := strings.HasPrefix(n, "bin/") || strings.HasPrefix(n, "script/") || strings.HasPrefix(n, "scripts/") || strings.HasPrefix(n, ".ferry/") || strings.HasPrefix(base, "docker-compose") || strings.HasPrefix(base, "compose.") || strings.HasPrefix(base, "requirements")
		for _, s := range setupFiles {
			if base == s {
				risky = true
			}
		}
		if risky {
			setup = append(setup, n)
		} else {
			other = append(other, n)
		}
	}
	var b strings.Builder
	if len(setup) > 0 {
		fmt.Fprintf(&b, "build and setup files: %s\n", strings.Join(setup, ", "))
	}
	if len(other) > 0 {
		shown := other
		if len(shown) > 15 {
			shown = shown[:15]
		}
		fmt.Fprintf(&b, "other files (%d): %s", len(other), strings.Join(shown, ", "))
		if len(other) > len(shown) {
			b.WriteString(", ...")
		}
	}
	return strings.TrimRight(b.String(), "\n")
}
