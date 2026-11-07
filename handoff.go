package main

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type handoffOpts struct {
	Agent        string
	SessionID    string
	Yes          bool
	NoLaunch     bool
	Force        bool
	ForceVersion bool
}

// task identifies the worktree being moved. Paths are relative to home so
// they mean the same thing on both machines.
type task struct {
	Key     string
	Name    string
	WtRel   string
	RepoRel string
}

func step(label, format string, a ...any) {
	fmt.Printf("%-10s %s\n", label, fmt.Sprintf(format, a...))
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

// findTask looks a task up in the hub's records by name or key.
func findTask(hub Machine, name string) (*Record, error) {
	recs, err := hub.RecordList()
	if err != nil {
		return nil, err
	}
	var hits []Record
	for _, r := range recs {
		if r.Name == name || r.Task == name || r.WtRel == name {
			hits = append(hits, r)
		}
	}
	switch len(hits) {
	case 0:
		return nil, fmt.Errorf("no task named %q; see `ferry ls`", name)
	case 1:
		return &hits[0], nil
	}
	var keys []string
	for _, h := range hits {
		keys = append(keys, h.WtRel)
	}
	return nil, fmt.Errorf("%q matches several tasks: %s", name, strings.Join(keys, ", "))
}

// handoff moves task t from src to dst. hub keeps the ownership record; it is
// always the remote machine, because the laptop drives every handoff.
func handoff(src, dst, hub Machine, t task, opts handoffOpts) error {
	direction := "to"
	if !src.IsLocal() {
		direction = "from"
	}
	si, err := src.Info()
	if err != nil {
		return err
	}
	di, err := dst.Info()
	if err != nil {
		return err
	}
	if si.Name == di.Name {
		return fmt.Errorf("both machines are named %q; set one in ~/.config/ferry/name", si.Name)
	}
	step("task", "%s  (%s, repo %s)", t.Name, t.WtRel, t.RepoRel)

	rec, err := hub.RecordGet(t.Key)
	if err != nil {
		return err
	}

	// A rerun after the commit point only needs to finish the last steps, and
	// it uses the sessions this handoff recorded rather than searching again.
	if rec != nil && rec.State == stateLanding && rec.Owner == di.Name && rec.From == si.Name {
		step("owner", "%s owns %s (generation %d); finishing the handoff", di.Name, t.Name, rec.Generation)
		return finish(src, dst, hub, t, rec, rec.Source, rec.Installed, si, di, direction, opts)
	}

	// --- session -----------------------------------------------------------
	find := FindSessionReq{Agent: opts.Agent, ID: opts.SessionID, CwdRel: t.WtRel}
	switch {
	case opts.SessionID != "":
	case rec != nil && rec.State == stateHandingOff && rec.From == si.Name && rec.Source != nil:
		find = FindSessionReq{Agent: rec.Source.Agent, ID: rec.Source.ID}
	case direction == "from" && rec != nil && rec.Session != "":
		find = FindSessionReq{Agent: rec.Agent, ID: rec.Session}
	}
	sess, err := src.FindSession(find)
	if err != nil {
		return err
	}

	if sess != nil {
		if rel := mustRel(si.Home, sess.Cwd); rel != t.WtRel && !strings.HasPrefix(rel, t.WtRel+"/") {
			return fmt.Errorf("session %s belongs to ~/%s, not this worktree (~/%s)", short(sess.ID), rel, t.WtRel)
		}
		step("agent", "%s %s, %s", sess.Agent, short(sess.ID), humanSize(sess.Size))
		live, err := settle(src, *sess)
		if err != nil {
			return err
		}
		if live.Live {
			if src.IsLocal() || rec == nil {
				return fmt.Errorf("%s; exit the agent, then run this again", live.Detail)
			}
			step("agent", "%s: stopping the idle agent in rex", si.Name)
			if err := src.StopAgent(StopReq{Session: *sess, RexSession: rec.RexSession, Name: t.Name}); err != nil {
				return err
			}
			// The agent writes its last lines on the way out; the record keeps
			// the session as it is once that stops, since a rerun compares
			// against it.
			if _, err := settle(src, *sess); err != nil {
				return err
			}
			if sess, err = src.FindSession(FindSessionReq{Agent: sess.Agent, ID: sess.ID}); err != nil {
				return err
			} else if sess == nil {
				return fmt.Errorf("%s: the session disappeared after stopping the agent", si.Name)
			}
		}
		if live, err := dst.Liveness(*sess); err != nil {
			return err
		} else if live.Live && !live.Settling {
			return fmt.Errorf("%s: %s already has this session loaded", di.Name, live.Detail)
		}
		have, want := agentVersion(si, sess.Agent), agentVersion(di, sess.Agent)
		switch {
		case have == "" || want == "":
			step("versions", "%s version unknown on one side (here %q, %s %q); not checked", sess.Agent, si.Claude+si.Codex, di.Name, want)
		case versionLess(want, have) && !opts.ForceVersion:
			return fmt.Errorf("%s has %s %s, older than %s here; upgrade it or pass --force-version", di.Name, sess.Agent, want, have)
		default:
			step("versions", "%s %s on %s, %s on %s", sess.Agent, have, si.Name, want, di.Name)
		}
	} else {
		step("agent", "no Claude or Codex session found for this worktree; moving code only")
	}

	// --- claim -------------------------------------------------------------
	if rec == nil {
		rec = &Record{Task: t.Key, Name: t.Name, WtRel: t.WtRel, RepoRel: t.RepoRel, Owner: si.Name, State: stateOwned}
		step("owner", "new task")
	}
	if rec.WtRel != t.WtRel || rec.RepoRel != t.RepoRel {
		return fmt.Errorf("task record %s is for ~/%s, not ~/%s", t.Key, rec.WtRel, t.WtRel)
	}
	resuming := rec.State == stateHandingOff && rec.From == si.Name && rec.To == di.Name
	switch {
	case resuming:
		step("owner", "continuing an interrupted handoff (generation %d)", rec.Generation)
	case rec.State == stateHandingOff && !opts.Force:
		return fmt.Errorf("a handoff %s -> %s is unfinished; finish it from that direction, or pass --force", rec.From, rec.To)
	case rec.State == stateLanding && !opts.Force:
		return fmt.Errorf("the handoff %s -> %s hasn't finished; rerun it first", rec.From, rec.To)
	case rec.Owner != si.Name && !opts.Force:
		return fmt.Errorf("%s owns %s, not %s; use `ferry from %s %s`", rec.Owner, t.Name, si.Name, rec.Owner, t.Name)
	}
	if !resuming {
		expect := rec.Generation
		rec.State, rec.From, rec.To, rec.Generation = stateHandingOff, si.Name, di.Name, expect+1
		rec.Source, rec.Installed = sess, nil
		if sess != nil {
			rec.Agent, rec.Session = sess.Agent, sess.ID
		}
		if err := hub.RecordPut(RecordPutReq{Expect: expect, Record: *rec}); err != nil {
			return err
		}
	}

	// --- dehydrate ---------------------------------------------------------
	// Once a task has been on kant, the laptop's worktree holds code kant
	// wrote, and dehydrate may run it (bin/rails, docker compose).
	askDehydrate, dehydrateChanges := false, ""
	if src.IsLocal() {
		if _, err := git(src.GitURL(t.WtRel), "rev-parse", "--verify", "-q", sentRef(t.Key)); err == nil {
			askDehydrate, dehydrateChanges = true, changeSummary(src.GitURL(t.WtRel), t.Key, "")
		}
	}
	if _, err := runHook(src, si.Name, HookReq{WtRel: t.WtRel, RepoRel: t.RepoRel, Name: "dehydrate", Task: t.Name, Direction: direction}, askDehydrate, dehydrateChanges); err != nil {
		return err
	}

	// --- code --------------------------------------------------------------
	ss, err := src.RepoState(t.RepoRel)
	if err != nil {
		return err
	}
	ds, err := dst.RepoState(t.RepoRel)
	if err != nil {
		return err
	}
	if ds.Exists && len(ds.Roots) > 0 && !sharesRoot(ss.Roots, ds.Roots) {
		return fmt.Errorf("%s:%s is a different repository (no shared root commit); move it aside first", di.Name, t.RepoRel)
	}
	// A repo still on the placeholder branch was created by an earlier,
	// interrupted run; finish creating it the same way.
	fresh := !ds.Exists || len(ds.Roots) == 0 || ds.Branch == placeholderBranch
	if fresh {
		step("repo", "%s has no %s; creating it", di.Name, t.RepoRel)
		if err := dst.InitRepo(t.RepoRel); err != nil {
			return err
		}
		if err := moveRefs(src, dst, t.RepoRel, "+refs/heads/*:refs/heads/*", "+refs/tags/*:refs/tags/*"); err != nil {
			return err
		}
		if err := dst.FinishRepo(FinishRepoReq{RepoRel: t.RepoRel, Branch: ss.Branch, Origin: ss.Origin}); err != nil {
			return err
		}
	} else {
		step("repo", "%s: %s exists, same history", di.Name, t.RepoRel)
	}
	snap, err := src.Snapshot(SnapshotReq{WtRel: t.WtRel, Task: t.Key})
	if err != nil {
		return err
	}
	step("snapshot", "branch %s at %s, %d changed paths", snap.Branch, snap.Base[:7], snap.Dirty)
	ref := "+refs/ferry/" + t.Key + "/*:refs/ferry/" + t.Key + "/*"
	if err := moveRefs(src, dst, t.RepoRel, ref); err != nil {
		return err
	}
	step("git", "moved refs/ferry/%s/* to %s", t.Key, di.Name)
	previous := []string{rec.LastSent[di.Name], rec.Attempt}
	if rec.Attempt != snap.Fingerprint {
		expect := rec.Generation
		rec.Attempt, rec.Generation = snap.Fingerprint, expect+1
		if err := hub.RecordPut(RecordPutReq{Expect: expect, Record: *rec}); err != nil {
			return err
		}
	}
	_, err = dst.Apply(ApplyReq{RepoRel: t.RepoRel, WtRel: t.WtRel, Branch: snap.Branch, Task: t.Key, Expected: snap.Fingerprint, Previous: previous, Fresh: fresh, Force: opts.Force})
	if err != nil {
		return err
	}
	step("apply", "%s: %s matches the snapshot", di.Name, t.WtRel)

	extras, err := src.ListExtras(t.WtRel)
	if err != nil {
		return err
	}
	// Extras di sent last time that si no longer has were deleted here.
	incoming := map[string]bool{}
	for _, e := range extras {
		incoming[e.Path] = true
	}
	var gone []FileEntry
	for _, e := range rec.Extras[di.Name] {
		if !incoming[e.Path] {
			gone = append(gone, e)
		}
	}
	sentExtras := false
	if len(extras) > 0 || len(gone) > 0 {
		var names, paths, goneNames []string
		for _, e := range extras {
			names = append(names, fmt.Sprintf("%s (%s)", e.Path, humanSize(e.Size)))
			paths = append(paths, e.Path)
		}
		for _, e := range gone {
			goneNames = append(goneNames, e.Path)
		}
		prompt := "send these ignored files to " + di.Name + "?"
		if len(names) > 0 {
			step("extras", "%s", strings.Join(names, ", "))
		}
		if len(gone) > 0 {
			step("extras", "deleted on %s since %s sent them: %s", si.Name, di.Name, strings.Join(goneNames, ", "))
			prompt = "send these ignored files to " + di.Name + " and remove the deleted ones there?"
		}
		if opts.Yes || confirm(prompt) {
			stage := filepath.Join(".local", "state", "ferry", "in", t.Key, "extras")
			if len(paths) > 0 {
				if err := transfer(src, ExportReq{BaseRel: t.WtRel, Paths: paths}, dst, ImportReq{BaseRel: stage, Clean: true, Private: true}); err != nil {
					return err
				}
			}
			res, err := dst.InstallExtras(InstallExtrasReq{WtRel: t.WtRel, StageRel: stage, Task: t.Key, Remove: gone})
			if err != nil {
				return err
			}
			if len(res.Removed) > 0 {
				step("extras", "%s: removed %s (parked in ~/.local/state/ferry/parked/)", di.Name, strings.Join(res.Removed, ", "))
			}
			if len(res.Kept) > 0 {
				step("extras", "%s: kept %s, changed there since it was sent", di.Name, strings.Join(res.Kept, ", "))
			}
			sentExtras = true
		} else {
			step("extras", "skipped")
		}
	}

	// --- session -----------------------------------------------------------
	// The session is staged on the receiver, with paths rewritten, but nothing
	// live moves until after the commit point. A failure before then leaves
	// the source exactly as it was.
	var installed *Session
	if sess != nil {
		stage := filepath.Join(".local", "state", "ferry", "in", t.Key, "session")
		if err := transfer(src, ExportReq{Paths: sess.Files}, dst, ImportReq{BaseRel: stage, Clean: true}); err != nil {
			return err
		}
		s, err := dst.StageSession(InstallReq{Session: *sess, OldHome: si.Home, StageRel: stage, Task: t.Key})
		if err != nil {
			return err
		}
		installed = &s
		step("session", "%s staged on %s, paths %s -> %s", short(sess.ID), di.Name, si.Home, di.Home)
	}

	// --- commit ------------------------------------------------------------
	expect := rec.Generation
	if rec.LastSent == nil {
		rec.LastSent = map[string]string{}
	}
	rec.LastSent[si.Name] = snap.Fingerprint
	rec.Attempt = ""
	rec.Installed = installed
	delete(rec.LastSent, di.Name)
	if rec.Extras == nil {
		rec.Extras = map[string][]FileEntry{}
	}
	delete(rec.Extras, di.Name)
	delete(rec.Extras, si.Name)
	if sentExtras && len(extras) > 0 {
		rec.Extras[si.Name] = extras
	}
	rec.State, rec.Owner, rec.Generation, rec.Branch, rec.RexSession = stateLanding, di.Name, expect+1, snap.Branch, ""
	if err := hub.RecordPut(RecordPutReq{Expect: expect, Record: *rec}); err != nil {
		return err
	}
	step("commit", "%s owns %s (generation %d)", di.Name, t.Name, rec.Generation)
	if src.IsLocal() {
		// What this machine sent, kept here where the other machine can't
		// change it, for the change summary when the task comes back.
		if _, err := git(src.GitURL(t.WtRel), "update-ref", sentRef(t.Key), taskRef(t.Key, "wip")); err != nil {
			return err
		}
	}

	return finish(src, dst, hub, t, rec, sess, installed, si, di, direction, opts)
}

// settle waits up to ten seconds for a session's transcript to stop changing.
func settle(m Machine, s Session) (Liveness, error) {
	live, err := m.Liveness(s)
	for i := 0; err == nil && live.Settling && i < 20; i++ {
		if i == 0 {
			step("agent", "%s; waiting for it to settle", live.Detail)
		}
		time.Sleep(500 * time.Millisecond)
		live, err = m.Liveness(s)
	}
	return live, err
}

// finish runs the steps after the commit point: park the source copy, move
// the staged session into place, run restore, and start the agent. Each is
// safe to repeat. The source is parked before the receiver's copy goes live,
// so at no point can both machines resume it.
func finish(src, dst, hub Machine, t task, rec *Record, sess, installed *Session, si, di Info, direction string, opts handoffOpts) error {
	if sess != nil {
		// On a rerun, the source copy is still live if parking never ran. If
		// someone resumed it since the commit, the staged copy is stale.
		cur, err := src.FindSession(FindSessionReq{Agent: sess.Agent, ID: sess.ID})
		if err != nil {
			return err
		}
		if cur != nil {
			if live, err := src.Liveness(*cur); err != nil {
				return err
			} else if live.Live && !live.Settling {
				return fmt.Errorf("%s: %s; exit it, then run this again", si.Name, live.Detail)
			}
			if cur.Size != sess.Size {
				return fmt.Errorf("%s: session %s changed after it was handed off (%s, was %s); %s owns the task now, so its copy wins. Move the newer one aside on %s to continue",
					si.Name, short(sess.ID), humanSize(cur.Size), humanSize(sess.Size), di.Name, si.Name)
			}
		}
		if err := src.Park(ParkReq{Session: *sess, Task: t.Key, As: fmt.Sprintf("g%d", rec.Generation)}); err != nil {
			return err
		}
		step("park", "%s: transcript moved to ~/.local/state/ferry/parked/", si.Name)
	}
	if installed != nil {
		if err := dst.PlaceSession(PlaceReq{Session: *installed, Task: t.Key}); err != nil {
			return err
		}
		step("session", "%s: %s is live", di.Name, short(installed.ID))
	}
	changes := ""
	if dst.IsLocal() {
		changes = changeSummary(dst.GitURL(t.WtRel), t.Key, taskRef(t.Key, "wip"))
	}
	summary, err := runHook(dst, di.Name, HookReq{WtRel: t.WtRel, RepoRel: t.RepoRel, Name: "restore", Task: t.Name, Direction: direction}, dst.IsLocal(), changes)
	if err != nil {
		step("restore", "failed: %v", err)
		step("next", "fix it, then run `ferry %s %s%s` again to finish", direction, di.Name, map[bool]string{true: " " + t.Name}[direction == "from"])
		return errors.New("restore failed; the task moved but the agent wasn't started")
	}
	rexSession := ""
	switch {
	case installed == nil:
		step("done", "code is on %s at ~/%s", di.Name, t.WtRel)
	default:
		note := resumeNote(si, di, t, *installed, summary)
		argv := resumeArgv(*installed, note)
		cwdRel, _ := filepath.Rel(di.Home, installed.Cwd)
		if dst.IsLocal() || opts.NoLaunch || !di.Rex {
			step("resume", "cd ~/%s && %s", cwdRel, shellJoin(argv))
			break
		}
		// A rerun may find the agent already started by the run it is finishing.
		if live, err := dst.Liveness(*installed); err != nil {
			return err
		} else if live.Live && !live.Settling {
			step("launch", "%s: %s is already running (%s); not starting another", di.Name, short(installed.ID), live.Detail)
			rexSession = rec.RexSession
			break
		}
		id, err := dst.Launch(LaunchReq{Name: t.Name, CwdRel: cwdRel, Argv: argv, Agent: installed.Agent, AcceptTrust: sess != nil && sess.Trusted})
		if err != nil {
			step("launch", "failed: %v", err)
			step("resume", "on %s: cd ~/%s && %s", di.Name, cwdRel, shellJoin(argv))
			break
		}
		rexSession = id
		step("launch", "%s: rex session %q running %s --resume %s", di.Name, t.Name, installed.Agent, short(installed.ID))
	}
	expect := rec.Generation
	rec.State, rec.RexSession, rec.Generation = stateOwned, rexSession, expect+1
	if err := hub.RecordPut(RecordPutReq{Expect: expect, Record: *rec}); err != nil {
		return err
	}
	return nil
}

// moveRefs copies refs from src's repo to dst's using whichever side is local.
func moveRefs(src, dst Machine, repoRel string, refspecs ...string) error {
	if src.IsLocal() {
		_, err := git(src.GitURL(repoRel), append([]string{"push", "-q", dst.GitURL(repoRel)}, refspecs...)...)
		return err
	}
	_, err := git(dst.GitURL(repoRel), append([]string{"fetch", "-q", src.GitURL(repoRel)}, refspecs...)...)
	return err
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
func runHook(m Machine, where string, q HookReq, everyRun bool, changes string) (string, error) {
	info, err := m.HookInfo(q)
	if err != nil || !info.Exists {
		return "", err
	}
	if info.TooLarge {
		step(q.Name, "%s: .ferry/%s is over %d KB, too large to review; not running it", where, q.Name, maxHookSize>>10)
		return "", nil
	}
	if !info.Approved || everyRun {
		why := "isn't approved on " + where
		if everyRun {
			why = "may run code from the other machine; ferry asks before every run here"
		}
		fmt.Printf("%-10s .ferry/%s %s (sha %s):\n%s\n", q.Name, q.Name, why, info.SHA[:12], indent(forTerminal(info.Content)))
		if changes != "" {
			fmt.Printf("%-10s changed since this machine last sent the task:\n%s\n", "", indent(forTerminal(changes)))
		}
		prompt := fmt.Sprintf("run this %s on %s, now and whenever it's unchanged?", q.Name, where)
		if everyRun {
			prompt = fmt.Sprintf("run this %s on %s now?", q.Name, where)
		}
		if !confirm(prompt) {
			step(q.Name, "skipped")
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
		fmt.Println(indent(res.Output))
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

func confirm(prompt string) bool {
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return false
	}
	defer tty.Close()
	fmt.Fprintf(tty, "%s [y/N] ", prompt)
	line, _ := bufio.NewReader(tty).ReadString('\n')
	line = strings.ToLower(strings.TrimSpace(line))
	return line == "y" || line == "yes"
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
	var names string
	if to != "" {
		names, _ = git(dir, "diff", "--name-only", from, to)
	} else {
		tracked, _ := git(dir, "diff", "--name-only", from)
		untracked, _ := git(dir, "ls-files", "--others", "--exclude-standard")
		names = strings.Trim(tracked+"\n"+untracked, "\n")
	}
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
