package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
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

// A dry run that would change the hosts entry writes nothing, and doesn't
// end with doctor's checks, which would read the entry it didn't write.
func TestSetupDryRunWritesNothingAndSkipsTheChecks(t *testing.T) {
	w := newWorld(t)
	w.fakeSSH(false)
	before := w.read(w.laptop, ".config/ferry/hosts")
	for _, args := range [][]string{
		{"setup", "kant2", "exec:" + w.kant, "--dry-run"},
		{"setup", "kant2", "mbriggs@fakehost", "--dry-run"},
	} {
		out := w.mustFerry(w.laptop, args...)
		if !strings.Contains(out, "would set kant2") || !strings.Contains(out, "dry run, nothing written") || strings.Contains(out, "isn't in ~/.config/ferry/hosts") {
			t.Fatalf("%v:\n%s", args, out)
		}
		if got := w.read(w.laptop, ".config/ferry/hosts"); got != before {
			t.Fatalf("%v changed the hosts file: %q", args, got)
		}
	}
	if _, err := os.Stat(filepath.Join(w.kant, ".local/bin/ferry")); err == nil {
		t.Fatal("a dry run installed ferry")
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
	w.mustFerry(wt, "to", "kant", "--yes", "--skip-scripts", "--no-launch")
	cmd := exec.Command(ferryBin, "ls", "kant")
	cmd.Env = w.env(w.kant)
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "fix-auth") {
		t.Fatalf("ls on kant: %v\n%s", err, out)
	}
	cmd = exec.Command(ferryBin, "to", "kant")
	cmd.Env = w.env(w.kant)
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
	w := newWorld(t)
	// Stands in for the other platform's build: it runs here, but records
	// no commit, like a build from an older tree. It builds with the real
	// HOME, so go uses the real build and module caches.
	old := filepath.Join(t.TempDir(), "ferry-old")
	build := exec.Command("go", "build", "-buildvcs=false", "-o", old, ".")
	build.Env = append(os.Environ(), "HOME="+realHome)
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatal(string(out))
	}
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

// FuzzOrderedJSON checks the codec setup edits settings.json with: it accepts
// exactly valid JSON, and its output decodes to the same keys, in the same
// order, with the same values and number spellings, and encodes back to
// itself.
func FuzzOrderedJSON(f *testing.F) {
	esc := string(rune(92)) + "u" // a JSON \u escape, spelled so editors leave it be
	for _, s := range []string{
		`{"permissions":{"allow":["Bash(ferry:*)"]},"model":"opus"}`,
		`{"a":1,"a":2,"b":{}}`, `[1e400, -0, 1.50, 12345678901234567890]`,
		`{"<k>&":"` + esc + `0000` + esc + `2028` + esc + `d800","":[[],{}]}`, `"x"`, `{"a" 1}`, `{} {}`,
		`{"Bash(echo ` + esc + `0022hi` + esc + `0022)":{"k` + esc + `000a":1}}`,
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, in string) {
		v, err := decodeOrdered([]byte(in))
		if (err == nil) != json.Valid([]byte(in)) {
			t.Fatalf("decodeOrdered error %v for %q, json.Valid %v", err, in, json.Valid([]byte(in)))
		}
		if err != nil {
			return
		}
		out, err := encodeOrdered(v)
		if err != nil {
			t.Fatal(err)
		}
		back, err := decodeOrdered(out)
		if err != nil {
			t.Fatalf("output doesn't decode: %v\n%s", err, out)
		}
		if !reflect.DeepEqual(v, back) {
			t.Fatalf("output means something else:\n%s\n%s", in, out)
		}
		if again, _ := encodeOrdered(back); !bytes.Equal(out, again) {
			t.Fatalf("encoding isn't stable:\n%s\n%s", out, again)
		}
	})
}

// settingsJSON is formatted the way ferry writes JSON, with a value of every
// kind, so a round trip that changes anything but the added rule shows up.
const settingsJSON = `{
  "model": "opus",
  "env": {
    "NOTE": "a <b> & \"c\" é \\ \t"
  },
  "cleanupPeriodDays": 1.50,
  "big": 1e3,
  "flags": [
    true,
    false,
    null
  ],
  "permissions": {
    "deny": [],
    "allow": [
      "Bash(go test:*)"
    ]
  },
  "hooks": {}
}
`

// Settings are often stowed from a dotfiles repo, so ferry writes through
// the symlink, keeps every other key and its order, keeps the file's mode,
// and backs up the original outside the repo.
func TestClaudeRulesEditSettingsThroughTheSymlink(t *testing.T) {
	w := newWorld(t)
	real := filepath.Join(w.laptop, "dotfiles", "claude-settings.json")
	w.write(w.laptop, "dotfiles/claude-settings.json", settingsJSON)
	os.Chmod(real, 0o600)
	link := filepath.Join(w.laptop, ".claude", "settings.json")
	os.MkdirAll(filepath.Dir(link), 0o755)
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}

	out := w.mustFerry(w.laptop, "setup", "--claude-rules")
	want := strings.Replace(settingsJSON, `"Bash(go test:*)"`, `"Bash(go test:*)",`+"\n      "+`"`+claudeRules[0]+`"`, 1)
	if got := w.read(w.laptop, "dotfiles/claude-settings.json"); got != want {
		t.Fatalf("settings after setup:\n%s\nwant:\n%s", got, want)
	}
	if st, err := os.Lstat(link); err != nil || st.Mode()&os.ModeSymlink == 0 {
		t.Fatal("settings.json is no longer a symlink")
	}
	if st, _ := os.Stat(real); st.Mode().Perm() != 0o600 {
		t.Fatalf("mode became %v", st.Mode().Perm())
	}
	backups, _ := filepath.Glob(filepath.Join(w.laptop, ".local/state/ferry/backups/claude-settings-*.json"))
	if len(backups) != 1 {
		t.Fatalf("backups: %v\n%s", backups, out)
	}
	if b, _ := os.ReadFile(backups[0]); string(b) != settingsJSON {
		t.Fatalf("backup isn't the original:\n%s", b)
	}

	out = w.mustFerry(w.laptop, "setup", "--claude-rules")
	if !strings.Contains(out, "already set") || w.read(w.laptop, "dotfiles/claude-settings.json") != want {
		t.Fatalf("a second run should change nothing:\n%s", out)
	}
}

// A dry run writes nothing, settings ferry can't parse as one JSON object
// stay as they were, and missing settings are created.
func TestClaudeRulesDryRunAndBadSettingsWriteNothing(t *testing.T) {
	w := newWorld(t)
	settings := filepath.Join(w.laptop, ".claude", "settings.json")
	out := w.mustFerry(w.laptop, "setup", "--claude-rules", "--dry-run")
	if !strings.Contains(out, "dry run, nothing written") {
		t.Fatalf("output:\n%s", out)
	}
	if _, err := os.Stat(settings); !os.IsNotExist(err) {
		t.Fatal("a dry run created settings.json")
	}

	for _, bad := range []string{`[]`, `{"a": 1} {}`, `{"a": `} {
		w.write(w.laptop, ".claude/settings.json", bad)
		if out, err := w.ferry(w.laptop, "setup", "--claude-rules"); err == nil {
			t.Fatalf("setup accepted %q:\n%s", bad, out)
		}
		if got := w.read(w.laptop, ".claude/settings.json"); got != bad {
			t.Fatalf("setup rewrote bad settings %q as %q", bad, got)
		}
	}

	os.Remove(settings)
	w.mustFerry(w.laptop, "setup", "--claude-rules")
	want := "{\n  \"permissions\": {\n    \"allow\": [\n      \"" + claudeRules[0] + "\"\n    ]\n  }\n}\n"
	if got := w.read(w.laptop, ".claude/settings.json"); got != want {
		t.Fatalf("new settings:\n%s", got)
	}
}
