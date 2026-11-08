package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
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
