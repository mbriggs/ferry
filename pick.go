package main

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// pullable is something `ferry from` can bring back: a worktree on the other
// machine, with the session to bring if an agent is running in it there.
type pullable struct {
	t       task
	session string // agent's session id, "" to let the handoff find one
	what    string // how it's shown in the picker
}

// choosePull works out what `ferry from HOST [NAME]` means. Running agents
// come first, since they name both a worktree and its session; then task
// records. A worktree that has never moved has no record, so a path under
// ~/src is resolved on the other machine directly. With no NAME, or a NAME
// that matches several, it asks.
func choosePull(remote Machine, here, there string, name string) (pullable, error) {
	agents, err := remote.Agents()
	if err != nil {
		return pullable{}, err
	}
	recs, err := remote.RecordList()
	if err != nil {
		return pullable{}, err
	}
	var all []pullable
	covered := map[string]bool{}
	for _, a := range agents {
		if a.Task == nil {
			continue
		}
		state := "idle"
		if a.Busy {
			state = "working"
		}
		what := fmt.Sprintf("%-16s ~/%-28s %s %s, %s", a.Task.Name, a.Task.WtRel, a.Session.Agent, short(a.Session.ID), state)
		if a.Rex != "" {
			what += fmt.Sprintf(", rex %q", a.Rex)
		}
		p := pullable{t: *a.Task, session: a.Session.ID, what: what}
		if name == "" || matches(name, p.t) || name == a.Rex || (len(name) >= 8 && strings.HasPrefix(a.Session.ID, name)) {
			all = append(all, p)
		}
		covered[a.Task.Key] = true
	}
	for _, r := range recs {
		// A task already here is only worth listing if its handoff is
		// unfinished, since rerunning `ferry from` is how that's finished.
		if covered[r.Task] || (r.Owner == here && r.State == stateOwned) {
			continue
		}
		t := task{Key: r.Task, Name: r.Name, WtRel: r.WtRel, RepoRel: r.RepoRel}
		if name == "" || matches(name, t) {
			all = append(all, pullable{t: t, what: fmt.Sprintf("%-16s ~/%-28s %s (no agent running)", t.Name, t.WtRel, r.State)})
		}
	}
	if len(all) == 0 && strings.Contains(name, "/") {
		t, err := remote.Worktree(strings.TrimPrefix(name, "~/"))
		if err != nil {
			return pullable{}, fmt.Errorf("%s: %v", there, err)
		}
		return pullable{t: t}, nil
	}
	switch {
	case len(all) == 0 && name == "":
		return pullable{}, fmt.Errorf("nothing to pull from %s: no agents running in a worktree under ~/src, and no tasks it owns. Name a worktree with `ferry from %s src/PATH`", there, there)
	case len(all) == 0:
		return pullable{}, fmt.Errorf("nothing on %s matches %q; run `ferry from %s` to see what's there, or give a path like src/%s", there, name, there, name)
	case len(all) == 1 && name != "":
		return all[0], nil
	}
	var lines []string
	for _, p := range all {
		lines = append(lines, p.what)
	}
	i, err := pick(fmt.Sprintf("pull from %s:", there), lines)
	if err != nil {
		return pullable{}, err
	}
	return all[i], nil
}

// matches reports whether a name the user typed refers to task t: its
// name, its path under home (with or without ~/), or its key.
func matches(name string, t task) bool {
	name = strings.TrimSuffix(strings.TrimPrefix(name, "~/"), "/")
	return name == t.Name || name == t.WtRel || name == t.Key
}

// pick asks the user to choose one of lines on the terminal.
func pick(title string, lines []string) (int, error) {
	defer clock.pause()()
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return 0, errors.New(title + " more than one match, and no terminal to ask on:\n  " + strings.Join(lines, "\n  "))
	}
	defer tty.Close()
	fmt.Fprintln(tty, title)
	for i, l := range lines {
		fmt.Fprintf(tty, "  %d  %s\n", i+1, l)
	}
	r := bufio.NewReader(tty)
	for {
		fmt.Fprintf(tty, "pick 1-%d: ", len(lines))
		line, err := r.ReadString('\n')
		n, convErr := strconv.Atoi(strings.TrimSpace(line))
		if convErr == nil && n >= 1 && n <= len(lines) {
			return n - 1, nil
		}
		if err != nil {
			return 0, errors.New("nothing picked")
		}
	}
}
