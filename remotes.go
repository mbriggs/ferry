package main

import (
	"fmt"
	"os"
	"os/exec"
	"slices"
	"sort"
	"strings"
)

// GitRemote is the part of a remote's config that travels: where it fetches
// from and pushes to. Fetch refspecs don't travel. One arriving from kant
// could map a fetch onto refs/heads/ and overwrite local branches, so a
// remote ferry adds gets git's default refspec instead.
type GitRemote struct {
	Name     string   `json:"name"`
	URLs     []string `json:"urls"`
	PushURLs []string `json:"push_urls,omitempty"`
}

func (r GitRemote) sameURLs(o GitRemote) bool {
	return slices.Equal(r.URLs, o.URLs) && slices.Equal(r.PushURLs, o.PushURLs)
}

// Upstream is a branch's branch.<name>.remote and branch.<name>.merge.
type Upstream struct {
	Remote string `json:"remote"`
	Merge  string `json:"merge"`
}

func (u Upstream) String() string {
	if u.Remote == "" {
		return "none"
	}
	return u.Remote + " " + strings.TrimPrefix(u.Merge, "refs/heads/")
}

// trackingStage is where a snapshot puts the sender's remote-tracking refs,
// so they move with the task's other refs and the receiver decides which to
// take.
func trackingStage(task string) string { return taskRef(task, "remotes") + "/" }

// remotesOf reads every remote's URLs from the repo's config, sorted by name.
func remotesOf(dir string) ([]GitRemote, error) {
	out, err := git(dir, "config", "-z", "--get-regexp", `^remote\..*\.(url|pushurl)$`)
	if err != nil {
		// Exit 1 means no remote is configured.
		return nil, nil
	}
	byName := map[string]*GitRemote{}
	for _, entry := range strings.Split(out, "\x00") {
		key, val, ok := strings.Cut(entry, "\n")
		if !ok {
			continue
		}
		rest := strings.TrimPrefix(key, "remote.")
		i := strings.LastIndexByte(rest, '.')
		if i <= 0 {
			continue
		}
		name, field := rest[:i], rest[i+1:]
		r := byName[name]
		if r == nil {
			r = &GitRemote{Name: name}
			byName[name] = r
		}
		if field == "url" {
			r.URLs = append(r.URLs, val)
		} else {
			r.PushURLs = append(r.PushURLs, val)
		}
	}
	var remotes []GitRemote
	for _, r := range byName {
		if len(r.URLs) > 0 {
			remotes = append(remotes, *r)
		}
	}
	sort.Slice(remotes, func(i, j int) bool { return remotes[i].Name < remotes[j].Name })
	return remotes, nil
}

func upstreamOf(dir, branch string) Upstream {
	remote, _ := git(dir, "config", "--get", "branch."+branch+".remote")
	merge, _ := git(dir, "config", "--get", "branch."+branch+".merge")
	if remote == "" || merge == "" {
		return Upstream{}
	}
	return Upstream{Remote: remote, Merge: merge}
}

// stageTracking replaces the task's staged tracking refs with this repo's
// refs/remotes/*. Symbolic refs like origin/HEAD stay behind.
func stageTracking(dir, task string) error {
	stage := trackingStage(task)
	var cmds []string
	kept := map[string]bool{}
	cur, _ := git(dir, "for-each-ref", "--format=%(refname) %(objectname) %(symref)", "refs/remotes/")
	for _, line := range strings.Split(cur, "\n") {
		f := strings.Fields(line)
		if len(f) != 2 {
			continue
		}
		ref := stage + strings.TrimPrefix(f[0], "refs/remotes/")
		kept[ref] = true
		cmds = append(cmds, "update "+ref+" "+f[1])
	}
	// One transaction can't both delete and update a ref.
	old, _ := git(dir, "for-each-ref", "--format=%(refname)", stage)
	for _, ref := range strings.Fields(old) {
		if !kept[ref] {
			cmds = append(cmds, "delete "+ref)
		}
	}
	return updateRefs(dir, cmds)
}

func updateRefs(dir string, cmds []string) error {
	if len(cmds) == 0 {
		return nil
	}
	args := []string{"update-ref", "--stdin"}
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), gitEnv...)
	cmd.Stdin = strings.NewReader(strings.Join(cmds, "\n") + "\n")
	_, err := output(cmd, "git", args)
	return err
}

type SyncRemotesReq struct {
	RepoRel  string      `json:"repo_rel"`
	Task     string      `json:"task"`
	Branch   string      `json:"branch"`
	Remotes  []GitRemote `json:"remotes"`
	Upstream Upstream    `json:"upstream"`
	// DryRun lists the config changes and changes nothing.
	DryRun bool `json:"dry_run,omitempty"`
	// Config makes the listed config changes. Without it only the
	// remote-tracking refs move.
	Config bool `json:"config,omitempty"`
}

type SyncRemotesResult struct {
	Changes []string `json:"changes"`
	// Tracking counts the remote-tracking refs that moved forward.
	Tracking int `json:"tracking"`
	// Differ names the remotes whose URLs still differ from the sender's.
	// Their tracking refs didn't move.
	Differ []string `json:"differ,omitempty"`
}

// SyncRemotes gives this repo the sender's remotes and the branch's
// upstream, then moves each remote-tracking ref forward to the sender's when
// the remote has the same URLs on both sides. It never removes a remote,
// changes one the sender doesn't have, or moves a tracking ref backward or
// sideways: whichever machine fetched last wins.
func (l *Local) SyncRemotes(q SyncRemotesReq) (SyncRemotesResult, error) {
	dir := l.abs(q.RepoRel)
	for _, r := range q.Remotes {
		if err := checkRemote(dir, r); err != nil {
			return SyncRemotesResult{}, err
		}
	}
	if q.Upstream.Remote != "" {
		if _, err := git(dir, "check-ref-format", q.Upstream.Merge); err != nil || !strings.HasPrefix(q.Upstream.Merge, "refs/") {
			return SyncRemotesResult{}, fmt.Errorf("upstream %q isn't a ref name", q.Upstream.Merge)
		}
	}
	have, err := remotesOf(dir)
	if err != nil {
		return SyncRemotesResult{}, err
	}
	mine := map[string]GitRemote{}
	for _, r := range have {
		mine[r.Name] = r
	}

	var res SyncRemotesResult
	var edits [][]string
	for _, r := range q.Remotes {
		cur, ok := mine[r.Name]
		switch {
		case !ok:
			res.Changes = append(res.Changes, fmt.Sprintf("add remote %s %s", r.Name, describeURLs(r)))
			edits = append(edits, []string{"remote", "add", r.Name, r.URLs[0]})
			for _, u := range r.URLs[1:] {
				edits = append(edits, []string{"config", "--add", "remote." + r.Name + ".url", u})
			}
			for _, u := range r.PushURLs {
				edits = append(edits, []string{"config", "--add", "remote." + r.Name + ".pushurl", u})
			}
		case !cur.sameURLs(r):
			res.Changes = append(res.Changes, fmt.Sprintf("set remote %s to %s (was %s)", r.Name, describeURLs(r), describeURLs(cur)))
			edits = append(edits, []string{"config", "--unset-all", "remote." + r.Name + ".url"})
			for _, u := range r.URLs {
				edits = append(edits, []string{"config", "--add", "remote." + r.Name + ".url", u})
			}
			if len(cur.PushURLs) > 0 {
				edits = append(edits, []string{"config", "--unset-all", "remote." + r.Name + ".pushurl"})
			}
			for _, u := range r.PushURLs {
				edits = append(edits, []string{"config", "--add", "remote." + r.Name + ".pushurl", u})
			}
		}
	}
	if up := upstreamOf(dir, q.Branch); q.Upstream.Remote != "" && up != q.Upstream {
		res.Changes = append(res.Changes, fmt.Sprintf("set %s's upstream to %s (was %s)", q.Branch, q.Upstream, up))
		edits = append(edits,
			[]string{"config", "branch." + q.Branch + ".remote", q.Upstream.Remote},
			[]string{"config", "branch." + q.Branch + ".merge", q.Upstream.Merge})
	}
	if q.DryRun {
		return res, nil
	}
	if q.Config {
		for _, e := range edits {
			if _, err := git(dir, e...); err != nil {
				return SyncRemotesResult{}, err
			}
		}
	}

	now, err := remotesOf(dir)
	if err != nil {
		return SyncRemotesResult{}, err
	}
	same := map[string]bool{}
	for _, r := range now {
		same[r.Name] = false
	}
	for _, r := range q.Remotes {
		i := slices.IndexFunc(now, func(n GitRemote) bool { return n.Name == r.Name })
		if i >= 0 && now[i].sameURLs(r) {
			same[r.Name] = true
		} else {
			res.Differ = append(res.Differ, r.Name)
		}
	}
	res.Tracking, err = takeTracking(dir, q.Task, same)
	return res, err
}

// checkRemote refuses a remote whose name or URLs can't be written to git
// config as they are.
func checkRemote(dir string, r GitRemote) error {
	if r.Name == "" || len(r.URLs) == 0 {
		return fmt.Errorf("remote %q has no URL", r.Name)
	}
	if _, err := git(dir, "check-ref-format", "refs/remotes/"+r.Name+"/x"); err != nil || strings.HasPrefix(r.Name, "-") {
		return fmt.Errorf("remote name %q isn't valid", r.Name)
	}
	for _, u := range append(slices.Clone(r.URLs), r.PushURLs...) {
		if u == "" || strings.ContainsAny(u, "\x00\n") || strings.HasPrefix(u, "-") {
			return fmt.Errorf("remote %s has an invalid URL %q", r.Name, u)
		}
	}
	return nil
}

func describeURLs(r GitRemote) string {
	s := strings.Join(r.URLs, ", ")
	if len(r.PushURLs) > 0 {
		s += " (push " + strings.Join(r.PushURLs, ", ") + ")"
	}
	return s
}

// takeTracking moves refs/remotes/<remote>/* forward to the staged copies
// for the remotes that same marks true, then deletes the stage. same names
// every remote this repo has. A ref this repo lacks is
// created; one already ahead of the sender's, or diverged from it, is left.
func takeTracking(dir, task string, same map[string]bool) (int, error) {
	stage := trackingStage(task)
	staged, _ := git(dir, "for-each-ref", "--format=%(refname) %(objectname)", stage)
	var cmds []string
	moved := 0
	for _, line := range strings.Split(staged, "\n") {
		f := strings.Fields(line)
		if len(f) != 2 {
			continue
		}
		cmds = append(cmds, "delete "+f[0])
		name := strings.TrimPrefix(f[0], stage)
		if !trackedBy(name, same) {
			continue
		}
		target := "refs/remotes/" + name
		cur, err := git(dir, "rev-parse", "--verify", "-q", target)
		switch {
		case err != nil:
			cmds = append(cmds, "create "+target+" "+f[1])
		case cur == f[1]:
			continue
		default:
			if _, err := git(dir, "merge-base", "--is-ancestor", cur, f[1]); err != nil {
				continue
			}
			cmds = append(cmds, "update "+target+" "+f[1]+" "+cur)
		}
		moved++
	}
	return moved, updateRefs(dir, cmds)
}

// trackedBy reports whether a ref name under refs/remotes/ belongs to a
// remote that same marks true. Remote names can hold slashes, so the longest
// matching name owns the ref, and a ref of "fork/a" isn't taken as one of
// "fork"'s.
func trackedBy(name string, same map[string]bool) bool {
	owner := ""
	for r := range same {
		if strings.HasPrefix(name, r+"/") && len(r) > len(owner) {
			owner = r
		}
	}
	return same[owner]
}
