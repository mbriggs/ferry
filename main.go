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
  ferry to HOST [NAME] [--agent claude|codex] [--session ID] [--yes] [--no-launch]
               [--force] [--force-version] [--check] [--prompt TEXT]
  ferry from HOST [NAME] [--yes] [--no-launch] [--force] [--force-version] [--check]
  ferry ls HOST
  ferry teardown HOST [NAME] [--force]
  ferry setup HOST [USER@HOST] [--dry-run]
  ferry setup --claude-rules [--dry-run]
  ferry doctor [HOST...]
  ferry push [HOST...] [--dry-run]
  ferry version

HOST is a name in ~/.config/ferry/hosts ("kant mbriggs@kant" per line), or an
ssh target. Run "ferry to" inside the worktree you want to hand off; in a
repo's main checkout, NAME (or a prompt) picks one of its worktrees. "ferry
from" with no NAME lists the agents running on HOST and asks which to pull;
NAME can be a task name, a Rex session label, the first 8 or more characters
of a running agent's session id, or a path like src/app. --force takes a task
whose record names another owner, and overwrites work the receiving machine
hasn't handed off without saving it (HOST for "to", this machine for "from");
--force-version resumes on an older agent. --check runs the checks that can
refuse a handoff, and stops before it stops an agent, records anything, or
touches either worktree. --prompt starts a new task instead of moving a
session: in a worktree with no session, it moves the code and starts Claude
on HOST with TEXT. To fan out, make a worktree per task (git worktree add)
and run "ferry to HOST --prompt TEXT" in each; "ferry from" brings each back
with its session. A HOST with ~/.config/ferry/launch puts that command in
front of every agent ferry starts there: "bunk run" runs each in a sandbox.
"ferry teardown HOST" removes a task from HOST once it's back here: the
worktree, its branch, ferry's record and refs, and whatever HOST's
~/.config/ferry/teardown command (such as "bunk rm --dir") removes for the
worktree. It refuses while HOST owns the task or has work this machine
doesn't. The worktree here stays; only ferry's refs and parked copies go.
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
	case "teardown":
		return cmdTeardown(l, rest)
	case "setup":
		return cmdSetup(l, rest)
	case "doctor":
		return cmdDoctor(l, rest)
	case "push":
		return cmdPush(l, rest)
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
	if !plain {
		fmt.Fprintln(os.Stderr, styleBad.Render("✗ ferry:"), err)
		return 1
	}
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
	fs.BoolVar(&o.Force, "force", false, "take the task even if the record names another owner, and overwrite the receiver's unsent work without saving it")
	fs.BoolVar(&o.ForceVersion, "force-version", false, "resume even if the receiving agent is older")
	fs.BoolVar(&o.SkipScripts, "skip-scripts", false, "skip .ferry/dehydrate and .ferry/restore when they would ask first; without a terminal to ask at, ferry stops instead")
	fs.BoolVar(&o.Check, "check", false, "run the checks that can refuse the handoff, then stop before stopping an agent or touching a worktree")
	if name == "to" {
		fs.StringVar(&o.Prompt, "prompt", "", "start a new task: move a worktree with no session and start Claude there with this prompt")
	}
	return fs
}

func cmdTo(l *Local, args []string) int {
	var o handoffOpts
	pos, err := parse(handoffFlags("to", &o), args)
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
	if unlockDeadClaude(l.abs(t.WtRel)) {
		step("worktree", "unlocked ~/%s: the Claude that locked it has exited", t.WtRel)
	}
	release, err := l.lockTask(t.Key)
	if err != nil {
		return fail(err)
	}
	defer release()
	remote.Task = t.Key
	if err := handoff(l, remote, remote, t, o); err != nil {
		return fail(l.hostHint(pos[0], err))
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
		return fail(l.hostHint(pos[0], err))
	}
	t := p.t
	if o.SessionID == "" {
		o.SessionID = p.session
	}
	if unlockDeadClaude(l.abs(t.WtRel)) {
		step("worktree", "unlocked ~/%s: the Claude that locked it has exited", t.WtRel)
	}
	release, err := l.lockTask(t.Key)
	if err != nil {
		return fail(err)
	}
	defer release()
	remote.Task = t.Key
	if err := handoff(remote, l, remote, t, o); err != nil {
		return fail(l.hostHint(pos[0], err))
	}
	return 0
}

func cmdLs(l *Local, args []string) int {
	if len(args) != 1 {
		fmt.Fprint(os.Stderr, usage)
		return 2
	}
	m, err := l.machine(args[0])
	if err != nil {
		return fail(err)
	}
	recs, err := m.RecordList()
	if err != nil {
		return fail(err)
	}
	if len(recs) == 0 {
		fmt.Println("no tasks")
		return 0
	}
	if plain {
		fmt.Printf("%-20s %-10s %-12s %-4s %-8s %s\n", "NAME", "OWNER", "STATE", "GEN", "AGENT", "PATH")
		for _, r := range recs {
			fmt.Printf("%-20s %-10s %-12s %-4d %-8s ~/%s\n", r.Name, r.Owner, r.State, r.Generation, r.Agent, r.WtRel)
		}
		return 0
	}
	fmt.Println(styleFaint.Render(fmt.Sprintf("    %-20s %-10s %-12s %-8s %s", "NAME", "OWNER", "STATE", "AGENT", "PATH")))
	for _, r := range recs {
		// Only a settled task gets a check. One mid-handoff is the one to
		// look at (a rerun finishes it), and so is a state this build
		// doesn't know, from a record another ferry wrote.
		look := tones[toneWarn]
		if r.State == stateOwned {
			look = tones[toneOK]
		}
		fmt.Printf("  %s %s %-10s %s %-8s %s %s\n", look.style.Render(look.glyph), styleBold.Render(fmt.Sprintf("%-20s", r.Name)), r.Owner,
			look.style.Render(fmt.Sprintf("%-12s", r.State)), r.Agent, "~/"+r.WtRel, dim(fmt.Sprintf("gen %d", r.Generation)))
	}
	return 0
}

// host resolves a host name through ~/.config/ferry/hosts. Each line is
// "NAME TARGET [FERRY_PATH]"; TARGET is an ssh target, or exec:HOME for tests.
// An unknown name is used as an ssh target directly.
// hostEntry looks name up in ~/.config/ferry/hosts ("NAME TARGET
// [FERRY_PATH]" per line). found is false when it isn't there, and then name
// is used as the ssh target.
func (l *Local) hostEntry(name string) (target, ferryPath string, found bool) {
	f, err := os.Open(l.configDir("hosts"))
	if err != nil {
		return name, "", false
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 2 || strings.HasPrefix(fields[0], "#") || fields[0] != name {
			continue
		}
		if len(fields) > 2 {
			ferryPath = fields[2]
		}
		return fields[1], ferryPath, true
	}
	return name, "", false
}

// hostNames lists the hosts in ~/.config/ferry/hosts, in order.
func (l *Local) hostNames() []string {
	data, err := os.ReadFile(l.configDir("hosts"))
	if err != nil {
		return nil
	}
	var out []string
	for _, line := range strings.Split(string(data), "\n") {
		if f := strings.Fields(line); len(f) >= 2 && !strings.HasPrefix(f[0], "#") {
			out = append(out, f[0])
		}
	}
	return out
}

// isSelf reports whether name is this machine's own ferry name.
func (l *Local) isSelf(name string) bool {
	me, err := l.name()
	return err == nil && me == name
}

func (l *Local) host(name string) (*Remote, error) {
	if l.isSelf(name) {
		return nil, fmt.Errorf("%s is this machine; run this from the other one", name)
	}
	target, path, _ := l.hostEntry(name)
	return NewRemote(name, target, path)
}

// hostHint adds what to do when ssh couldn't reach a host the hosts file
// doesn't name. The usual cause is running ferry on kant, which has no
// hosts file because the laptop drives every handoff.
func (l *Local) hostHint(name string, err error) error {
	if err == nil || !strings.Contains(err.Error(), "ssh: ") {
		return err
	}
	if _, _, found := l.hostEntry(name); found {
		return err
	}
	me, _ := l.name()
	return fmt.Errorf("%w\n%s isn't in ~/.config/ferry/hosts. The laptop drives every handoff, so if %s is the laptop, run `ferry from %s` or `ferry to %s` there instead; to reach it from here, run `ferry setup %s USER@%s` first", err, name, name, me, me, name, name)
}

// machine is host, except that this machine's own name gives this machine,
// for commands that only read, like ls.
func (l *Local) machine(name string) (Machine, error) {
	if l.isSelf(name) {
		return l, nil
	}
	return l.host(name)
}
