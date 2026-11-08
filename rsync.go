package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
)

// Files that git doesn't carry (ignored extras and the agent's session) move
// with rsync, run on the laptop, which is always one end of a handoff. rsync
// skips files the receiver already has (same size and mtime), resumes a cut
// transfer from its partial file, and checks every file against the sender's
// checksum.
//
// During a pull kant is the sender, and a modified rsync there could put
// anything in the file list. The receiver confines writes to the destination
// directory, and the laptop's rsync must be 3.2.5 or newer, which adds two
// checks on a remote sender's list: no top-level items beyond the ones asked
// for, and no names matching the client's filter rules. Every job excludes
// .git, so the second check rejects a .git anywhere in the list. Extras jobs
// don't recurse, so a sender can't turn a listed file into a directory and
// fill it. PrepareExtras has already checked each listed path. macOS's own
// /usr/bin/rsync is openrsync, which is older than 3.2.5.

var minRsync = [3]int{3, 2, 5}

var rsyncVersionRE = regexp.MustCompile(`^rsync\s+version\s+v?(\d+)\.(\d+)\.(\d+)`)

// rsyncVersion is the version of the rsync on PATH, or "" when there's none
// or it isn't samba rsync (openrsync answers "openrsync: protocol version 29").
func rsyncVersion() string {
	out, err := exec.Command("rsync", "--version").Output()
	if err != nil {
		return ""
	}
	m := rsyncVersionRE.FindStringSubmatch(string(out))
	if m == nil {
		return ""
	}
	return m[1] + "." + m[2] + "." + m[3]
}

// checkRsync says what's wrong with machine's rsync, if anything.
func checkRsync(machine, v string) error {
	fix := "install rsync 3.2.5 or newer (on a Mac, `brew install rsync`) and put it ahead of /usr/bin on PATH"
	if v == "" {
		return fmt.Errorf("%s has no rsync 3 on PATH; %s", machine, fix)
	}
	var got [3]int
	for i, s := range strings.SplitN(v, ".", 3) {
		got[i], _ = strconv.Atoi(s)
	}
	for i := range got {
		if got[i] != minRsync[i] {
			if got[i] < minRsync[i] {
				return fmt.Errorf("%s has rsync %s; %s", machine, v, fix)
			}
			break
		}
	}
	return nil
}

// rsyncJob copies Files, relative to SrcRel on src, to DstRel on dst.
type rsyncJob struct {
	SrcRel, DstRel string
	Files          []string
	// Dirs lets Files name directories, copied with everything in them.
	// Without it rsync copies only the listed files.
	Dirs bool
	// Private makes new files 0600, for secrets. rsync keeps the mode of a
	// file the receiver already has, so PrepareExtras sets those to 0600 first.
	Private bool
	// WorkDir, an absolute path on dst, holds partial and temp files, so a
	// cut transfer leaves nothing in DstRel. Without it, partial files go in
	// .rsync-partial beside the file.
	WorkDir string
	// Total is about how many bytes will move; a big transfer shows rsync's
	// progress on a terminal.
	Total int64
}

// rsyncTo runs the job and returns the files rsync sent, or with dryRun,
// would send.
func rsyncTo(src, dst Machine, j rsyncJob, dryRun bool) ([]string, error) {
	if len(j.Files) == 0 {
		return nil, nil
	}
	// --8-bit-output keeps non-ASCII names as they are in --out-format;
	// rsync still escapes control characters (see sendExtras).
	args := []string{"--times", "--omit-dir-times", "--mkpath", "--from0", "--files-from=-", "--out-format=%n", "--8-bit-output", "--exclude=.git"}
	if j.Dirs {
		args = append(args, "--recursive")
	}
	shell := ""
	for _, m := range []Machine{src, dst} {
		if r, ok := m.(*Remote); ok && r.Target != "" {
			if shell != "" {
				return nil, errors.New("rsync can't copy between two remote machines")
			}
			shell = "ssh " + strings.Join(sshOptions, " ")
		}
	}
	if shell != "" {
		args = append(args, "--rsh="+shell)
	}
	if j.Private {
		args = append(args, "--chmod=F600")
	} else {
		args = append(args, "--perms")
	}
	if j.WorkDir != "" {
		args = append(args, "--partial-dir="+j.WorkDir+"/partial", "--temp-dir="+j.WorkDir+"/tmp")
	} else {
		args = append(args, "--partial-dir=.rsync-partial")
	}
	if dryRun {
		args = append(args, "--dry-run")
	}
	showProgress := !dryRun && !plain && j.Total >= 8<<20
	if showProgress {
		args = append(args, "--info=progress2")
	}
	args = append(args, src.GitURL(j.SrcRel)+"/", dst.GitURL(j.DstRel)+"/")

	cmd := exec.Command("rsync", args...)
	cmd.Stdin = strings.NewReader(strings.Join(j.Files, "\x00"))
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if showProgress {
		cmd.Stdout = os.Stdout
	}
	err := cmd.Run()
	if showProgress {
		fmt.Println()
	}
	if err != nil {
		msg := strings.TrimSpace(errb.String())
		if msg == "" {
			msg = err.Error()
		}
		return nil, fmt.Errorf("rsync: %s", msg)
	}
	var sent []string
	for _, line := range strings.Split(out.String(), "\n") {
		// Directories rsync creates on the way are listed with a trailing /.
		if line != "" && !strings.HasSuffix(line, "/") {
			sent = append(sent, line)
		}
	}
	return sent, nil
}
