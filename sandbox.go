package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
)

// A sandbox is a punchclock box on a build host: a throwaway VM, one per
// task, that ferry reaches as the ssh host NAME.HOST.box. The laptop's ssh
// config routes those names through the build host:
//
//	Host *.kant.box
//	  UserKnownHostsFile ~/.ssh/known_hosts_punchclock
//	  StrictHostKeyChecking yes
//	  ProxyCommand ssh mbriggs@kant .local/bin/punchclock proxy %h
//
// so git, rsync, and ferry's own calls all get there with no hosts-file entry.
// `punchclock up` prints the box's freshly generated host key, which
// startSandbox pins in that known_hosts file before anything connects.

// isSandbox reports whether this machine is a sandbox: its image has a
// ~/.config/ferry/sandbox file. A sandbox is thrown away after its one task,
// and the VM is the boundary, so its agents trust whatever folder they're
// handed.
func (l *Local) isSandbox() bool { return exists(l.configDir("sandbox")) }

// sandboxTarget splits a sandbox's ssh target, NAME.HOST.box, into the box's
// name and its build host. ok is false for any other target.
func sandboxTarget(target string) (name, host string, ok bool) {
	rest, ok := strings.CutSuffix(target, ".box")
	if !ok {
		return "", "", false
	}
	name, host, ok = strings.Cut(rest, ".")
	return name, host, ok && name != "" && host != ""
}

// removeSandbox deletes the box behind target on its build host and forgets
// its host key. ferry calls it once a task is back from the box.
func removeSandbox(l *Local, target string) error {
	name, host, _ := sandboxTarget(target)
	knownHosts, err := sandboxSSHConfig(target)
	if err != nil {
		return err
	}
	cmd := punchclock(l, host, "rm", name)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%s: punchclock rm %s: %s", host, name, strings.TrimSpace(string(out)))
	}
	return forgetHostKey(knownHosts, target)
}

// forgetHostKey drops target's lines from a known_hosts file.
func forgetHostKey(path, target string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var keep []string
	for _, line := range strings.SplitAfter(string(data), "\n") {
		if host, _, _ := strings.Cut(line, " "); host != target && line != "" {
			keep = append(keep, line)
		}
	}
	return writeAtomic(path, []byte(strings.Join(keep, "")), 0o600)
}

// startSandbox creates a box for t on host and returns its ssh target.
func startSandbox(l *Local, host string, t task) (string, error) {
	name := boxName(t)
	target := name + "." + host + ".box"
	if _, _, ok := sandboxTarget(target); !ok {
		return "", fmt.Errorf("%s can't host a sandbox: its name must not contain a dot", host)
	}
	knownHosts, err := sandboxSSHConfig(target)
	if err != nil {
		return "", err
	}
	cmd := punchclock(l, host, "up", name)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("%s: punchclock up %s: %s", host, name, strings.TrimSpace(errb.String()))
	}
	key := strings.TrimSpace(out.String())
	if !strings.HasPrefix(key, "ssh-ed25519 ") {
		return "", fmt.Errorf("%s: punchclock up %s printed %q, not a host key", host, name, key)
	}
	// A box made earlier under this name, and removed without ferry, may
	// have left its key behind.
	if err := forgetHostKey(knownHosts, target); err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	f, err := os.OpenFile(knownHosts, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return "", err
	}
	defer f.Close()
	if _, err := fmt.Fprintf(f, "%s %s\n", target, key); err != nil {
		return "", err
	}
	step("sandbox", "%s: %s", target, lastLine(strings.TrimSpace(errb.String()))) // after any ssh warnings
	return target, f.Close()
}

// punchclock runs punchclock on host: over ssh, or directly when host is
// this machine, so a laptop can sandbox its own tasks.
func punchclock(l *Local, host string, args ...string) *exec.Cmd {
	if l.isSelf(host) {
		bin := "punchclock"
		for _, p := range []string{l.abs(".local/bin/punchclock"), l.abs(".local/share/punchclock/bin/punchclock")} {
			if exists(p) {
				bin = p
				break
			}
		}
		return exec.Command(bin, args...)
	}
	target, _, _ := l.hostEntry(host)
	return exec.Command("ssh", append(append(append([]string{}, sshOptions...), target, ".local/bin/punchclock"), args...)...)
}

// sandboxSSHConfig checks that ssh routes target through punchclock, and
// returns the known_hosts file it checks the box's key against.
func sandboxSSHConfig(target string) (string, error) {
	out, err := exec.Command("ssh", "-G", target).Output()
	if err != nil {
		return "", fmt.Errorf("ssh -G %s: %w", target, err)
	}
	var proxy, knownHosts string
	for _, line := range strings.Split(string(out), "\n") {
		k, v, _ := strings.Cut(line, " ")
		switch k {
		case "proxycommand":
			proxy = v
		case "userknownhostsfile":
			knownHosts, _, _ = strings.Cut(v, " ")
		}
	}
	host := strings.TrimSuffix(target[strings.Index(target, ".")+1:], ".box")
	if !strings.Contains(proxy, "punchclock proxy") || knownHosts == "" {
		return "", fmt.Errorf("ssh doesn't route %s through punchclock; add this to ~/.ssh/config:\n\n"+
			"Host *.%s.box\n  UserKnownHostsFile ~/.ssh/known_hosts_punchclock\n  StrictHostKeyChecking yes\n"+
			"  ProxyCommand ssh %s .local/bin/punchclock proxy %%h", target, host, host)
	}
	return knownHosts, nil
}

var notBoxChar = regexp.MustCompile(`[^a-z0-9]+`)

// boxName turns a task's name into a box name: 1-32 lowercase letters,
// digits, and dashes, starting with a letter. It's also the box's hostname.
func boxName(t task) string {
	n := strings.Trim(notBoxChar.ReplaceAllString(strings.ToLower(t.Name), "-"), "-")
	if n == "" {
		n = "task"
	} else if n[0] < 'a' || n[0] > 'z' {
		n = "t-" + n
	}
	if len(n) > 32 {
		n = strings.TrimRight(n[:32], "-")
	}
	return n
}
