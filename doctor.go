package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// A check is one line of `ferry doctor`: something ferry depends on, whether
// it holds, and what to do when it doesn't.
type check struct {
	name   string
	status string // "ok", "warn" or "fail"
	detail string
	fix    string
}

type report struct{ checks []check }

func (r *report) ok(name, format string, a ...any) {
	r.checks = append(r.checks, check{name: name, status: "ok", detail: fmt.Sprintf(format, a...)})
}

func (r *report) warn(name, fix, format string, a ...any) {
	r.checks = append(r.checks, check{name: name, status: "warn", detail: fmt.Sprintf(format, a...), fix: fix})
}

func (r *report) fail(name, fix, format string, a ...any) {
	r.checks = append(r.checks, check{name: name, status: "fail", detail: fmt.Sprintf(format, a...), fix: fix})
}

func (r *report) failed() bool {
	for _, c := range r.checks {
		if c.status == "fail" {
			return true
		}
	}
	return false
}

func (r *report) print(title string) {
	fmt.Println(title)
	for _, c := range r.checks {
		fmt.Printf("  %-4s  %-10s %s\n", c.status, c.name, c.detail)
		if c.fix != "" {
			fmt.Printf("  %-4s  %-10s %s\n", "", "", "fix: "+c.fix)
		}
	}
}

func cmdDoctor(l *Local, args []string) int {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	pos, err := parse(fs, args)
	if err != nil {
		return 2
	}
	here := doctorLocal(l)
	info, _ := l.Info()
	here.print("this machine (" + info.Name + ")")
	failed := here.failed()
	hosts := pos
	if len(hosts) == 0 {
		hosts = l.hostNames()
		if len(hosts) == 0 {
			fmt.Println("\nno hosts in ~/.config/ferry/hosts. That's normal on the machine that only receives handoffs (kant); on the laptop, add one with `ferry setup HOST [USER@HOST]`")
		}
	}
	for _, h := range hosts {
		if l.isSelf(h) {
			continue
		}
		fmt.Println()
		r := doctorHost(l, h)
		r.print(h)
		failed = failed || r.failed()
	}
	if failed {
		return 1
	}
	return 0
}

// doctorLocal checks what ferry needs on this machine.
func doctorLocal(l *Local) *report {
	r := &report{}
	info, err := l.Info()
	if err != nil {
		r.fail("ferry", "", "can't describe this machine: %v", err)
		return r
	}
	r.ok("ferry", "%s, named %q (from ~/.config/ferry/name or the hostname)", version, info.Name)
	if _, err := exec.LookPath("git"); err != nil {
		r.fail("git", "install git", "git isn't on PATH")
	} else {
		r.ok("git", "found")
	}
	switch {
	case info.Claude == "" && info.Codex == "":
		r.warn("agents", "install Claude Code or Codex, or put it on PATH", "neither claude nor codex found; ferry will move code only")
	default:
		r.ok("agents", "%s", agentVersions(info))
	}
	if info.Rex {
		r.ok("rex", "found")
	} else {
		r.warn("rex", "", "not found; agents can't be launched or stopped here, and ferry prints resume commands instead")
	}
	if isDir(l.abs("src")) {
		r.ok("src", "~/src exists")
	} else {
		r.warn("src", "mkdir ~/src", "~/src doesn't exist; ferry only moves work under ~/src")
	}
	return r
}

// doctorHost checks one host from this machine: the hosts entry, ssh, the
// far side's ferry and agents, and the task records.
func doctorHost(l *Local, name string) *report {
	r := &report{}
	target, _, found := l.hostEntry(name)
	if found {
		r.ok("hosts", "%s -> %s", name, target)
	} else {
		r.warn("hosts", fmt.Sprintf("ferry setup %s USER@%s", name, name), "%s isn't in ~/.config/ferry/hosts; using it as the ssh target", name)
	}
	remote, err := l.host(name)
	if err != nil {
		r.fail("hosts", "", "%v", err)
		return r
	}
	if remote.Target != "" {
		took, err := sshCheck(remote.Target)
		if err != nil {
			r.fail("ssh", sshFix(err.Error()), "%s: %v", remote.Target, err)
			return r
		}
		r.ok("ssh", "%s answered in %s", remote.Target, roundDuration(took))
	}
	info, err := remote.Info()
	if err != nil {
		fix := ""
		if strings.Contains(err.Error(), "No such file") || strings.Contains(err.Error(), "not found") {
			fix = "ferry setup " + name + " installs it"
		}
		r.fail("ferry", fix, "%v", err)
		return r
	}
	if info.Protocol != protocol {
		r.fail("ferry", "ferry setup "+name+" updates it", "%s there speaks protocol %d, this build speaks %d; handoffs will refuse", info.Ferry, info.Protocol, protocol)
	} else if info.Ferry == version {
		r.ok("ferry", "%s", info.Ferry)
	} else {
		r.warn("ferry", "ferry setup "+name+" updates it", "%s there, %s here; both sides should run the same build", info.Ferry, version)
	}
	mine, _ := l.Info()
	if info.Name == mine.Name {
		r.fail("names", "set a different name in ~/.config/ferry/name on one of them", "both machines are named %q", info.Name)
	} else {
		r.ok("names", "%s and %s", mine.Name, info.Name)
	}
	switch {
	case info.Claude == "" && info.Codex == "":
		r.fail("agents", "install them there, or make sure `$SHELL -l` puts them on PATH (ferry asks the login shell for PATH)", "%s finds neither claude nor codex", name)
	default:
		detail := agentVersions(info)
		for _, agent := range []string{agentClaude, agentCodex} {
			have, there := agentVersion(mine, agent), agentVersion(info, agent)
			if have != "" && there != "" && versionLess(there, have) {
				r.warn("agents", "upgrade "+agent+" on "+name, "%s; %s is older there than here (%s), so sessions from here won't move", detail, agent, have)
				detail = ""
				break
			}
		}
		if detail != "" {
			r.ok("agents", "%s", detail)
		}
	}
	if info.Rex {
		r.ok("rex", "found; ferry to %s launches agents there", name)
	} else {
		r.warn("rex", "install rex there, and make sure the login shell's PATH reaches it", "not found; ferry to %s will print resume commands instead of launching", name)
	}
	recs, err := remote.RecordList()
	if err != nil {
		r.fail("records", "", "%v", err)
	} else {
		r.ok("records", "%d task(s) recorded", len(recs))
	}
	return r
}

func agentVersions(i Info) string {
	var parts []string
	if i.Claude != "" {
		parts = append(parts, "claude "+i.Claude)
	}
	if i.Codex != "" {
		parts = append(parts, "codex "+i.Codex)
	}
	return strings.Join(parts, ", ")
}

// sshCheck runs `true` on target with ferry's ssh options.
func sshCheck(target string) (time.Duration, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	start := time.Now()
	cmd := exec.CommandContext(ctx, "ssh", append(append([]string{}, sshOptions...), target, "true")...)
	var errb bytes.Buffer
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return 0, errors.New("no answer in 30s")
		}
		msg := strings.TrimSpace(lastLine(errb.String()))
		if msg == "" {
			msg = err.Error()
		}
		return 0, errors.New(msg)
	}
	return time.Since(start), nil
}

// sshFix suggests what to do about an ssh failure.
func sshFix(msg string) string {
	switch {
	case strings.Contains(msg, "Could not resolve"):
		return "check the name: with Tailscale, MagicDNS must be on, or use the tailnet IP"
	case strings.Contains(msg, "timed out") || strings.Contains(msg, "no answer") || strings.Contains(msg, "refused"):
		return "on that machine run `sudo tailscale set --ssh`, and give the tailnet policy an ssh rule that lets this device in with action \"accept\" (a \"check\" rule waits for a browser login, which BatchMode can't do)"
	case strings.Contains(msg, "Permission denied"):
		return "the user is wrong or not allowed: name it in the hosts file (USER@HOST), and allow it in the tailnet policy's ssh rule"
	case strings.Contains(msg, "Host key verification failed"):
		return "connect once with plain ssh to accept the host key"
	}
	return ""
}
