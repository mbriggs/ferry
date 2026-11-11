package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// A sandbox is a bunk box on a build host: a throwaway VM, one per
// task, that ferry reaches as the ssh host NAME.HOST.box. The laptop's ssh
// config routes those names through the build host:
//
//	Host *.kant.box
//	  UserKnownHostsFile ~/.ssh/bunk/%h
//	  StrictHostKeyChecking yes
//	  ProxyCommand ssh mbriggs@kant .local/bin/bunk proxy %h
//
// so git, rsync, and ferry's own calls all get there with no hosts-file entry.
// `bunk up` prints the box's freshly generated host key, which
// startSandbox pins before anything connects. Each box has a known_hosts file
// of its own (the %h), so parallel handoffs never rewrite a shared one.

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

// removeSandbox deletes the box behind target on its build host and its
// known_hosts file. ferry calls it once a task is back from the box.
func removeSandbox(l *Local, target string) error {
	name, host, _ := sandboxTarget(target)
	knownHosts, err := sandboxSSHConfig(target)
	if err != nil {
		return err
	}
	cmd := bunk(l, host, "rm", name)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%s: bunk rm %s: %s", host, name, strings.TrimSpace(string(out)))
	}
	if err := os.Remove(knownHosts); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
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
	cmd := bunk(l, host, "up", name)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("%s: bunk up %s: %s", host, name, strings.TrimSpace(errb.String()))
	}
	key := strings.TrimSpace(out.String())
	if !strings.HasPrefix(key, "ssh-ed25519 ") {
		return "", fmt.Errorf("%s: bunk up %s printed %q, not a host key", host, name, key)
	}
	// The file holds only this box's key; writing it whole replaces one a
	// box made earlier under this name, and removed without ferry, left.
	if err := os.MkdirAll(filepath.Dir(knownHosts), 0o700); err != nil {
		return "", err
	}
	if err := writeAtomic(knownHosts, []byte(target+" "+key+"\n"), 0o600); err != nil {
		return "", err
	}
	step("sandbox", "%s: %s", target, lastLine(strings.TrimSpace(errb.String()))) // after any ssh warnings
	return target, nil
}

// bunk runs bunk on host: over ssh, or directly when host is
// this machine, so a laptop can sandbox its own tasks. It's found in
// ~/.local/bin or ~/.local/share/bunk/bin, or else on PATH.
func bunk(l *Local, host string, args ...string) *exec.Cmd {
	if l.isSelf(host) {
		bin := "bunk"
		for _, p := range []string{l.abs(".local/bin/bunk"), l.abs(".local/share/bunk/bin/bunk")} {
			if exists(p) {
				bin = p
				break
			}
		}
		return exec.Command(bin, args...)
	}
	target, _, _ := l.hostEntry(host)
	// ssh runs this in the remote user's shell, which may not have either
	// install location on PATH.
	remote := "PATH=$HOME/.local/bin:$HOME/.local/share/bunk/bin:$PATH bunk " + shellJoin(args)
	return exec.Command("ssh", append(append([]string{}, sshOptions...), target, remote)...)
}

// sandboxSSHConfig checks that ssh routes target through bunk, and
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
	// The known_hosts file must name the box (%h), so each box has its own.
	if !strings.Contains(proxy, "bunk proxy") || !strings.Contains(filepath.Base(knownHosts), target) {
		return "", fmt.Errorf("ssh doesn't route %s through bunk with a known_hosts file per box; add this to ~/.ssh/config, with your user and key:\n\n"+
			"Host *.%s.box\n  UserKnownHostsFile ~/.ssh/bunk/%%h\n  StrictHostKeyChecking yes\n"+
			"  ProxyCommand ssh %s .local/bin/bunk proxy %%h", target, host, host)
	}
	return knownHosts, nil
}

var notBoxChar = regexp.MustCompile(`[^a-z0-9]+`)

// boxName names t's box: t's name, cut to fit, and the hash that ends t's key,
// which is unique to the worktree. Rerunning a handoff gets the same box back;
// two tasks with the same name, or the same first 25 characters, get two. The
// result is 1-32 lowercase letters, digits, and dashes, starting with a
// letter. It's also the box's hostname.
func boxName(t task) string {
	n := strings.Trim(notBoxChar.ReplaceAllString(strings.ToLower(t.Name), "-"), "-")
	if n == "" {
		n = "task"
	} else if n[0] < 'a' || n[0] > 'z' {
		n = "t-" + n
	}
	hash := t.Key[strings.LastIndex(t.Key, "-")+1:]
	if len(n) > 31-len(hash) {
		n = strings.TrimRight(n[:31-len(hash)], "-")
	}
	return n + "-" + hash
}
