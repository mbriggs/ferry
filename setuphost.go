package main

import (
	"bytes"
	"debug/buildinfo"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"runtime/debug"
	"strings"
)

// setupHost writes "name target" to ~/.config/ferry/hosts, then, for a real
// ssh target, checks ssh and puts this build of ferry on the other machine
// if it runs a different one. It ends with doctor's checks for the host.
func setupHost(l *Local, name, target string, dryRun bool) int {
	if l.isSelf(name) {
		return fail(fmt.Errorf("%s is this machine's own name; set up the other machine's name", name))
	}
	old, ferryPath, found := l.hostEntry(name)
	if target == "" {
		target = old
	}
	// A dry run that would change the entry skips doctor's checks at the
	// end: they read the hosts file, which still has the old entry or none.
	unwritten := dryRun && !(found && old == target)
	switch {
	case found && old == target:
		step("hosts", "%s -> %s (already set)", name, target)
	case dryRun:
		step("hosts", "would set %s -> %s", name, target)
	default:
		if err := writeHostEntry(l, name, target); err != nil {
			return fail(err)
		}
		step("hosts", "%s -> %s, in ~/.config/ferry/hosts", name, target)
	}
	if strings.HasPrefix(target, "exec:") {
		return finishSetup(l, name, unwritten)
	}

	took, err := sshCheck(target)
	if err != nil {
		bad("ssh", "%s: %v", target, err)
		if fix := sshFix(err.Error()); fix != "" {
			bad("fix", "%s", fix)
		}
		return 1
	}
	step("ssh", "%s answered in %s", target, roundDuration(took))

	if err := pushFerry(name, target, ferryPath, dryRun); err != nil {
		return fail(err)
	}
	return finishSetup(l, name, unwritten)
}

// pushFerry puts this build of ferry at ferryPath on target, unless it
// already runs it, and checks that the installed copy answers as this build.
func pushFerry(name, target, ferryPath string, dryRun bool) error {
	if ferryPath == "" {
		ferryPath = "~/.local/bin/ferry"
	}
	have, _ := sshOutput(target, ferryPath+" version 2>/dev/null")
	if have == "ferry "+version {
		step("ferry", "%s already runs %s", name, version)
		return nil
	}
	uname, err := sshOutput(target, "uname -sm")
	if err != nil {
		return fmt.Errorf("asking %s for its OS: %v", name, err)
	}
	goos, goarch, err := platform(uname)
	if err != nil {
		return err
	}
	bin, err := binaryFor(goos, goarch)
	if err != nil {
		return err
	}
	was := strings.TrimPrefix(have, "ferry ")
	if was == "" {
		was = "nothing"
	}
	if dryRun {
		step("ferry", "would install %s (%s/%s) at %s on %s, replacing %s", version, goos, goarch, ferryPath, name, was)
		return nil
	}
	if err := installBinary(target, ferryPath, bin); err != nil {
		return fmt.Errorf("installing ferry on %s: %v", name, err)
	}
	if got, err := sshOutput(target, ferryPath+" version"); err != nil || got != "ferry "+version {
		return fmt.Errorf("installed %s at %s on %s, but it answers %q (%v)", bin, ferryPath, name, got, err)
	}
	step("ferry", "installed %s (%s/%s) at %s on %s, replacing %s", version, goos, goarch, ferryPath, name, was)
	return nil
}

func finishSetup(l *Local, name string, unwritten bool) int {
	if unwritten {
		fmt.Println("dry run, nothing written")
		return 0
	}
	fmt.Println()
	r := doctorHost(l, name)
	r.print(name)
	if r.failed() {
		return 1
	}
	return 0
}

// writeHostEntry sets name's line in the hosts file, keeping other lines.
func writeHostEntry(l *Local, name, target string) error {
	path := l.configDir("hosts")
	data, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	var lines []string
	replaced := false
	for _, line := range strings.Split(strings.TrimRight(string(data), "\n"), "\n") {
		f := strings.Fields(line)
		if len(f) >= 2 && f[0] == name {
			if replaced {
				continue
			}
			// Keep a FERRY_PATH that was set by hand.
			line = name + " " + target
			if len(f) > 2 {
				line += " " + strings.Join(f[2:], " ")
			}
			replaced = true
		}
		if line != "" || len(lines) > 0 {
			lines = append(lines, line)
		}
	}
	if !replaced {
		lines = append(lines, name+" "+target)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return writeAtomic(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644)
}

// sshOutput runs a shell command on target and returns its trimmed output.
func sshOutput(target, command string) (string, error) {
	cmd := exec.Command("ssh", append(append([]string{}, sshOptions...), target, command)...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(lastLine(errb.String()))
		if msg == "" {
			msg = err.Error()
		}
		return "", errors.New(msg)
	}
	return strings.TrimSpace(out.String()), nil
}

// platform maps `uname -sm` to Go's names.
func platform(uname string) (goos, goarch string, err error) {
	f := strings.Fields(uname)
	if len(f) != 2 {
		return "", "", fmt.Errorf("unexpected uname %q", uname)
	}
	switch f[0] {
	case "Linux":
		goos = "linux"
	case "Darwin":
		goos = "darwin"
	default:
		return "", "", fmt.Errorf("ferry doesn't build for %s", f[0])
	}
	switch f[1] {
	case "x86_64", "amd64":
		goarch = "amd64"
	case "aarch64", "arm64":
		goarch = "arm64"
	default:
		return "", "", fmt.Errorf("ferry doesn't build for %s", f[1])
	}
	return goos, goarch, nil
}

// binaryFor finds a ferry built for goos/goarch: this binary when it
// matches, or ferry-GOOS-GOARCH next to it or in ./dist (`make dist`).
func binaryFor(goos, goarch string) (string, error) {
	self, err := os.Executable()
	if err != nil {
		return "", err
	}
	if goos == runtime.GOOS && goarch == runtime.GOARCH {
		return self, nil
	}
	name := "ferry-" + goos + "-" + goarch
	want := buildCommit("")
	var stale error
	for _, p := range []string{filepath.Join(filepath.Dir(self), name), filepath.Join("dist", name)} {
		if st, err := os.Stat(p); err != nil || !st.Mode().IsRegular() {
			continue
		}
		got := buildCommit(p)
		if got != want {
			stale = fmt.Errorf("%s was built from %s, this ferry from %s: run `make dist` in the ferry repo", p, got, want)
			continue
		}
		if strings.HasSuffix(got, "+changes") {
			note("note", "%s and this ferry were both built from %s; ferry can't tell whether they hold the same changes (`make push` rebuilds both)", p, got)
		}
		return p, nil
	}
	if stale != nil {
		return "", stale
	}
	return "", fmt.Errorf("no ferry built for %s/%s here: run `make dist` in the ferry repo and run setup from there, or put %s next to this binary", goos, goarch, name)
}

// buildCommit is the commit a ferry binary was built from, with "+changes"
// when the tree had uncommitted changes; "" means this binary. go build
// records it (-trimpath drops -ldflags, so the version string isn't there).
// Two builds of the same commit with different uncommitted changes look
// alike, here and in the version string; binaryFor says so.
func buildCommit(bin string) string {
	var bi *debug.BuildInfo
	var err error
	if bin == "" {
		var ok bool
		if bi, ok = debug.ReadBuildInfo(); !ok {
			return "an unknown commit"
		}
	} else if bi, err = buildinfo.ReadFile(bin); err != nil {
		return "an unknown commit"
	}
	rev, modified := "", false
	for _, s := range bi.Settings {
		switch s.Key {
		case "vcs.revision":
			rev = s.Value
		case "vcs.modified":
			modified = s.Value == "true"
		}
	}
	if rev == "" {
		return "an unknown commit"
	}
	if len(rev) > 7 {
		rev = rev[:7]
	}
	if modified {
		rev += "+changes"
	}
	return rev
}

var remotePath = regexp.MustCompile(`^[A-Za-z0-9_./~-]+$`)

// installBinary copies bin to path on target, replacing it in one rename.
func installBinary(target, path, bin string) error {
	if !remotePath.MatchString(path) {
		return fmt.Errorf("refusing odd remote path %q", path)
	}
	f, err := os.Open(bin)
	if err != nil {
		return err
	}
	defer f.Close()
	dir := filepath.Dir(path)
	script := fmt.Sprintf("mkdir -p %s && cat > %s.new && chmod 755 %s.new && mv %s.new %s", dir, path, path, path, path)
	cmd := exec.Command("ssh", append(append([]string{}, sshOptions...), target, script)...)
	cmd.Stdin = f
	var errb bytes.Buffer
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%v: %s", err, strings.TrimSpace(errb.String()))
	}
	return nil
}
