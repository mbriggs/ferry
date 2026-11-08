package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// fakeSSH puts an ssh on PATH that runs the remote command in kant's home,
// as kant's user would, or fails like an unreachable host when fail is set.
func (w *world) fakeSSH(fail bool) {
	body := "#!/bin/sh\nwhile [ \"$1\" = -o ]; do shift 2; done\nshift\ncd " + w.kant + " && HOME=" + w.kant + " exec sh -c \"$*\"\n"
	if fail {
		body = "#!/bin/sh\necho 'ssh: connect to host fakehost port 22: Connection refused' >&2\nexit 255\n"
	}
	writeExec(w.t, filepath.Join(w.root, "bin", "ssh"), body)
}

func TestSetupWritesTheHostAndInstallsFerry(t *testing.T) {
	w := newWorld(t)
	w.fakeSSH(false)
	w.write(w.laptop, ".config/ferry/hosts", "# mine\nother other-host\n")
	out := w.mustFerry(w.laptop, "setup", "kant", "mbriggs@fakehost")
	if got := w.read(w.laptop, ".config/ferry/hosts"); got != "# mine\nother other-host\nkant mbriggs@fakehost\n" {
		t.Fatalf("hosts file: %q", got)
	}
	installed, err := os.ReadFile(filepath.Join(w.kant, ".local/bin/ferry"))
	if err != nil {
		t.Fatalf("ferry wasn't installed on kant:\n%s", out)
	}
	self, _ := os.ReadFile(ferryBin)
	if !bytes.Equal(installed, self) {
		t.Fatal("kant got a different binary")
	}
	if !strings.Contains(out, "installed dev") || !strings.Contains(out, "names      laptop and kant") {
		t.Fatalf("output:\n%s", out)
	}

	out = w.mustFerry(w.laptop, "setup", "kant")
	if !strings.Contains(out, "already set") || !strings.Contains(out, "already runs dev") {
		t.Fatalf("a second setup should change nothing:\n%s", out)
	}
}

func TestDoctorPassesForAWorkingPair(t *testing.T) {
	w := newWorld(t)
	out := w.mustFerry(w.laptop, "doctor")
	for _, want := range []string{"this machine (laptop)", "names      laptop and kant", "agents     claude 2.1.0", "records    0 task(s)"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q:\n%s", want, out)
		}
	}
}

func TestDoctorExplainsAnSSHFailure(t *testing.T) {
	w := newWorld(t)
	w.fakeSSH(true)
	w.write(w.laptop, ".config/ferry/hosts", "kant mbriggs@fakehost\n")
	out, err := w.ferry(w.laptop, "doctor", "kant")
	if err == nil {
		t.Fatalf("doctor should fail:\n%s", out)
	}
	if !strings.Contains(out, "Connection refused") || !strings.Contains(out, "tailscale set --ssh") {
		t.Fatalf("expected the ssh error and the Tailscale fix:\n%s", out)
	}
}

// On kant, `ferry ls kant` reads kant's own records instead of trying to
// ssh to itself.
func TestLsOnTheMachineThatKeepsTheRecords(t *testing.T) {
	w := newWorld(t)
	wt := w.laptopTask()
	w.mustFerry(wt, "to", "kant", "--yes", "--no-launch")
	cmd := exec.Command(ferryBin, "ls", "kant")
	cmd.Env = append(os.Environ(), "HOME="+w.kant)
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "fix-auth") {
		t.Fatalf("ls on kant: %v\n%s", err, out)
	}
	cmd = exec.Command(ferryBin, "to", "kant")
	cmd.Env = append(os.Environ(), "HOME="+w.kant)
	if out, err := cmd.CombinedOutput(); err == nil || !strings.Contains(string(out), "this machine") {
		t.Fatalf("ferry to itself should refuse: %v\n%s", err, out)
	}
}

// A far side running a ferry from before the current protocol is refused
// before any work, with the command that fixes it.
func TestOlderProtocolIsRefusedUpFront(t *testing.T) {
	w := newWorld(t)
	log := filepath.Join(w.root, "ssh.log")
	writeExec(t, filepath.Join(w.root, "bin", "ssh"), "#!/bin/sh\necho \"$*\" >> "+log+"\ncase \"$*\" in *' true') exit 0 ;; *' remote info'*) echo '{\"name\":\"kant\",\"ferry\":\"v0.0.1\"}' ;; *) exit 1 ;; esac\n")
	w.write(w.laptop, ".config/ferry/hosts", "kant mbriggs@fakehost\n")
	for _, args := range [][]string{{"ls", "kant"}, {"from", "kant"}} {
		out, err := w.ferry(w.laptop, args...)
		if err == nil || !strings.Contains(out, "protocol 0") || !strings.Contains(out, "ferry setup kant") {
			t.Fatalf("ferry %v should refuse the old peer: %v\n%s", args, err, out)
		}
	}
	if data, _ := os.ReadFile(log); strings.Contains(string(data), "record-list") || strings.Contains(string(data), "agents") {
		t.Fatalf("called the old peer before checking:\n%s", data)
	}
	out, _ := w.ferry(w.laptop, "doctor", "kant")
	if !strings.Contains(out, "speaks protocol 0") {
		t.Fatalf("doctor should name the mismatch:\n%s", out)
	}
}

// push installs this build on every host in the hosts file, and leaves a
// host that already runs it alone.
func TestPushUpdatesEveryHost(t *testing.T) {
	w := newWorld(t)
	w.fakeSSH(false)
	w.write(w.laptop, ".config/ferry/hosts", "kant mbriggs@fakehost\nlaptop self@nowhere\n")
	out := w.mustFerry(w.laptop, "push")
	installed, err := os.ReadFile(filepath.Join(w.kant, ".local/bin/ferry"))
	self, _ := os.ReadFile(ferryBin)
	if err != nil || !bytes.Equal(installed, self) {
		t.Fatalf("kant didn't get this build: %v\n%s", err, out)
	}
	if out := w.mustFerry(w.laptop, "push"); !strings.Contains(out, "kant already runs dev") {
		t.Fatalf("a second push should change nothing:\n%s", out)
	}
}

// A binary for the other machine's platform built from another commit is
// refused, not installed; once it matches this build, it goes.
func TestPushRefusesAStaleCrossBuild(t *testing.T) {
	if buildCommit(ferryBin) == "an unknown commit" {
		t.Skip("the test binary has no commit recorded (not built in a git checkout)")
	}
	// Stands in for the other platform's build: it runs here, but records
	// no commit, like a build from an older tree. Built before newWorld
	// moves HOME, so go uses the real module cache.
	old := filepath.Join(t.TempDir(), "ferry-old")
	if out, err := exec.Command("go", "build", "-buildvcs=false", "-o", old, ".").CombinedOutput(); err != nil {
		t.Fatal(string(out))
	}
	w := newWorld(t)
	w.fakeSSH(false)
	w.write(w.laptop, ".config/ferry/hosts", "kant mbriggs@fakehost\n")
	foreign, uname := "linux-amd64", "Linux x86_64"
	if runtime.GOOS == "linux" {
		foreign, uname = "darwin-arm64", "Darwin arm64"
	}
	writeExec(t, filepath.Join(w.root, "bin", "uname"), "#!/bin/sh\necho "+uname+"\n")
	repo := filepath.Join(w.root, "repo")
	dist := filepath.Join(repo, "dist", "ferry-"+foreign)
	os.MkdirAll(filepath.Dir(dist), 0o755)
	data, _ := os.ReadFile(old)
	os.WriteFile(dist, data, 0o755)
	out, err := w.ferry(repo, "push", "kant")
	if err == nil || !strings.Contains(out, "make dist") {
		t.Fatalf("push should refuse the stale build: %v\n%s", err, out)
	}
	if _, err := os.Stat(filepath.Join(w.kant, ".local/bin/ferry")); err == nil {
		t.Fatal("installed the stale build")
	}
	self, _ := os.ReadFile(ferryBin)
	os.WriteFile(dist, self, 0o755)
	if out := w.mustFerry(repo, "push", "kant"); !strings.Contains(out, "installed dev ("+strings.Replace(foreign, "-", "/", 1)+")") {
		t.Fatalf("push should install the matching build:\n%s", out)
	}
}
