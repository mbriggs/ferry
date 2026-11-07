// ferry hands a coding task (a git worktree under ~/src plus its Claude Code
// or Codex session) between machines over SSH, and resumes the session there.
package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"strings"
)

const usage = `usage:
  ferry to HOST [--agent claude|codex] [--session ID] [--yes] [--no-launch]
  ferry from HOST [NAME] [--yes] [--no-launch]
  ferry ls HOST
  ferry setup [--dry-run]
  ferry version

HOST is a name in ~/.config/ferry/hosts ("kant mbriggs@kant" per line), or an
ssh target. Run "ferry to" inside the worktree you want to hand off. "ferry
from" with no NAME lists the agents running on HOST and asks which to pull;
NAME can be a task name, a Rex session label, or a path like src/app.
`

func main() {
	os.Exit(run1(os.Args[1:]))
}

func run1(args []string) int {
	if len(args) == 0 {
		fmt.Fprint(os.Stderr, usage)
		return 2
	}
	cmd, rest := args[0], args[1:]
	if cmd == "remote" {
		return serveRemote(rest)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return fail(err)
	}
	l := &Local{Home: home}
	switch cmd {
	case "to":
		return cmdTo(l, rest)
	case "from":
		return cmdFrom(l, rest)
	case "ls":
		return cmdLs(l, rest)
	case "setup":
		return cmdSetup(l, rest)
	case "version", "--version":
		fmt.Println("ferry", version)
		return 0
	case "help", "-h", "--help":
		fmt.Print(usage)
		return 0
	}
	fmt.Fprintf(os.Stderr, "ferry: unknown command %q\n\n%s", cmd, usage)
	return 2
}

func fail(err error) int {
	fmt.Fprintln(os.Stderr, "ferry:", err)
	return 1
}

// parse splits positional args from flags so flags may come anywhere.
func parse(fs *flag.FlagSet, args []string) ([]string, error) {
	var pos []string
	for len(args) > 0 {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		args = fs.Args()
		if len(args) > 0 {
			pos = append(pos, args[0])
			args = args[1:]
		}
	}
	return pos, nil
}

func handoffFlags(name string, o *handoffOpts) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.StringVar(&o.Agent, "agent", "", "claude or codex (default: the newest session for this worktree)")
	fs.StringVar(&o.SessionID, "session", "", "session id to move")
	fs.BoolVar(&o.Yes, "yes", false, "send listed ignored files without asking (never approves hooks)")
	fs.BoolVar(&o.NoLaunch, "no-launch", false, "print the resume command instead of starting the agent (in rex for to, in this terminal for from)")
	fs.BoolVar(&o.Force, "force", false, "take the task even if the record names another owner")
	fs.BoolVar(&o.ForceVersion, "force-version", false, "resume even if the receiving agent is older")
	return fs
}

func cmdTo(l *Local, args []string) int {
	var o handoffOpts
	pos, err := parse(handoffFlags("to", &o), args)
	if err != nil {
		return 2
	}
	if len(pos) != 1 {
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
	t, err := taskHere(l, cwd)
	if err != nil {
		return fail(err)
	}
	release, err := l.lockTask(t.Key)
	if err != nil {
		return fail(err)
	}
	defer release()
	remote.Task = t.Key
	if err := handoff(l, remote, remote, t, o); err != nil {
		return fail(err)
	}
	return 0
}

func cmdFrom(l *Local, args []string) int {
	var o handoffOpts
	pos, err := parse(handoffFlags("from", &o), args)
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
	name := ""
	if len(pos) == 2 {
		name = pos[1]
	}
	me, err := l.Info()
	if err != nil {
		return fail(err)
	}
	p, err := choosePull(remote, me.Name, pos[0], name)
	if err != nil {
		return fail(err)
	}
	t := p.t
	if o.SessionID == "" {
		o.SessionID = p.session
	}
	release, err := l.lockTask(t.Key)
	if err != nil {
		return fail(err)
	}
	defer release()
	remote.Task = t.Key
	if err := handoff(remote, l, remote, t, o); err != nil {
		return fail(err)
	}
	return 0
}

func cmdLs(l *Local, args []string) int {
	if len(args) != 1 {
		fmt.Fprint(os.Stderr, usage)
		return 2
	}
	remote, err := l.host(args[0])
	if err != nil {
		return fail(err)
	}
	recs, err := remote.RecordList()
	if err != nil {
		return fail(err)
	}
	if len(recs) == 0 {
		fmt.Println("no tasks")
		return 0
	}
	fmt.Printf("%-20s %-10s %-12s %-4s %-8s %s\n", "NAME", "OWNER", "STATE", "GEN", "AGENT", "PATH")
	for _, r := range recs {
		fmt.Printf("%-20s %-10s %-12s %-4d %-8s ~/%s\n", r.Name, r.Owner, r.State, r.Generation, r.Agent, r.WtRel)
	}
	return 0
}

// host resolves a host name through ~/.config/ferry/hosts. Each line is
// "NAME TARGET [FERRY_PATH]"; TARGET is an ssh target, or exec:HOME for tests.
// An unknown name is used as an ssh target directly.
func (l *Local) host(name string) (*Remote, error) {
	f, err := os.Open(l.configDir("hosts"))
	if err == nil {
		defer f.Close()
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			fields := strings.Fields(sc.Text())
			if len(fields) < 2 || strings.HasPrefix(fields[0], "#") || fields[0] != name {
				continue
			}
			path := ""
			if len(fields) > 2 {
				path = fields[2]
			}
			return NewRemote(name, fields[1], path)
		}
	}
	return NewRemote(name, name, "")
}
