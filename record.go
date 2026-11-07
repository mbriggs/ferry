package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
)

// A task moves owned -> handing-off -> landing -> owned. landing means the
// receiver owns it (the commit point has passed) but parking, restore, or the
// launch hasn't finished; rerunning the same command completes it.
const (
	stateOwned      = "owned"
	stateHandingOff = "handing-off"
	stateLanding    = "landing"
)

// Record is kant's view of one task: who owns it, and enough to find it again.
// Every write is a compare-and-swap on Generation.
type Record struct {
	Task       string            `json:"task"`
	Name       string            `json:"name"`
	WtRel      string            `json:"wt_rel"`
	RepoRel    string            `json:"repo_rel"`
	Branch     string            `json:"branch"`
	Owner      string            `json:"owner"`
	State      string            `json:"state"`
	From       string            `json:"from,omitempty"`
	To         string            `json:"to,omitempty"`
	Generation int               `json:"generation"`
	Agent      string            `json:"agent,omitempty"`
	Session    string            `json:"session,omitempty"`
	RexSession string            `json:"rex_session,omitempty"`
	LastSent   map[string]string `json:"last_sent,omitempty"`
	// Attempt is the fingerprint this handoff last applied on the receiver,
	// so a rerun recognizes the receiver's state as its own.
	Attempt string `json:"attempt,omitempty"`
	// Source is the session as found on the sending machine when this handoff
	// claimed the task, and Installed is it as installed on the receiver.
	// Recovery uses these instead of searching again, because by then the
	// source copy may be parked and a search would find a different session.
	Source    *Session `json:"source,omitempty"`
	Installed *Session `json:"installed,omitempty"`
	// Extras is what each machine sent last time it handed the task off, so
	// an extra deleted on the other side can be removed when the task returns.
	Extras map[string][]FileEntry `json:"extras,omitempty"`
	// Changes summarizes what the sending machine changed, shown before a
	// hook from it runs on the laptop.
	Changes string `json:"changes,omitempty"`
	Updated string `json:"updated"`
}

type RecordPutReq struct {
	Expect int    `json:"expect"`
	Record Record `json:"record"`
}

func (l *Local) recordPath(task string) string { return l.stateDir("tasks", task+".json") }

func (l *Local) withRecordLock(fn func() error) error {
	dir := l.stateDir("tasks")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(dir, ".lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	return fn()
}

func (l *Local) readRecord(task string) (*Record, error) {
	data, err := os.ReadFile(l.recordPath(task))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	var r Record
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, fmt.Errorf("task record %s: %w", task, err)
	}
	return &r, nil
}

func (l *Local) RecordGet(task string) (*Record, error) {
	var r *Record
	err := l.withRecordLock(func() (err error) { r, err = l.readRecord(task); return })
	return r, err
}

// RecordPut writes a record if the stored generation still equals Expect
// (0 for a task with no record yet).
func (l *Local) RecordPut(q RecordPutReq) error {
	return l.withRecordLock(func() error {
		cur, err := l.readRecord(q.Record.Task)
		if err != nil {
			return err
		}
		have := 0
		if cur != nil {
			have = cur.Generation
		}
		if have != q.Expect {
			return fmt.Errorf("task %s changed underneath this handoff (generation %d, expected %d); rerun the command", q.Record.Task, have, q.Expect)
		}
		q.Record.Updated = time.Now().UTC().Format(time.RFC3339)
		data, err := json.MarshalIndent(q.Record, "", "  ")
		if err != nil {
			return err
		}
		return writeAtomic(l.recordPath(q.Record.Task), append(data, '\n'), 0o600)
	})
}

func (l *Local) RecordList() ([]Record, error) {
	var out []Record
	err := l.withRecordLock(func() error {
		files, _ := filepath.Glob(l.stateDir("tasks", "*.json"))
		for _, f := range files {
			r, err := l.readRecord(strings.TrimSuffix(filepath.Base(f), ".json"))
			if err != nil {
				return err
			}
			if r != nil {
				out = append(out, *r)
			}
		}
		return nil
	})
	sort.Slice(out, func(i, j int) bool { return out[i].Updated > out[j].Updated })
	return out, err
}

// taskKey turns a worktree path relative to home into a name usable in file
// names and git refs, such as "src-app-wt-fix-auth-3f9a1c". The readable part
// collapses punctuation, so a hash of the exact path keeps "foo.bar" and
// "foo-bar" apart.
func taskKey(wtRel string) string {
	rel := filepath.ToSlash(wtRel)
	sum := sha256.Sum256([]byte(rel))
	return strings.Trim(nonAlnum.ReplaceAllString(rel, "-"), "-") + "-" + hex.EncodeToString(sum[:3])
}

// lockTask keeps two ferry commands from handing off the same task at once.
// The record's generation check only guards state changes; the steps between
// them (apply, extras, the session) would otherwise interleave. Every handoff
// is driven from this machine, so a local flock is enough, and the kernel
// drops it the moment ferry exits, however it exits.
func (l *Local) lockTask(task string) (release func(), err error) {
	if !taskPattern.MatchString(task) {
		return nil, fmt.Errorf("bad task key %q", task)
	}
	dir := l.stateDir("locks")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(dir, task+".lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, errors.New("another ferry is handing off this task; wait for it to finish")
		}
		return nil, err
	}
	return func() { f.Close() }, nil
}
