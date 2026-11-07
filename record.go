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

// A task lock keeps two ferry commands from handing off the same task at
// once. The record's generation check only guards state changes; the steps
// between them (apply, extras, the session) would otherwise interleave. The
// lock is a lease: the holder renews it while it works, so one left behind
// by a killed ferry expires on its own.
type LockReq struct {
	Task   string `json:"task"`
	Token  string `json:"token"`
	Holder string `json:"holder"`
	TTL    int    `json:"ttl"` // seconds
}

type taskLock struct {
	Token   string    `json:"token"`
	Holder  string    `json:"holder"`
	Since   time.Time `json:"since"`
	Expires time.Time `json:"expires"`
}

func (l *Local) lockPath(task string) string { return l.stateDir("tasks", task+".lock") }

// Lock takes or renews the task's lease. It fails while someone else holds
// an unexpired lease.
func (l *Local) Lock(q LockReq) error {
	if q.Token == "" || q.TTL <= 0 {
		return errors.New("lock: missing token or ttl")
	}
	return l.withRecordLock(func() error {
		now := time.Now().UTC()
		var cur taskLock
		if data, err := os.ReadFile(l.lockPath(q.Task)); err == nil && json.Unmarshal(data, &cur) == nil {
			if cur.Token != q.Token && now.Before(cur.Expires) {
				return fmt.Errorf("another ferry (%s, started %s) is handing off this task; wait for it, or for its lock to expire at %s",
					cur.Holder, cur.Since.Local().Format("15:04:05"), cur.Expires.Local().Format("15:04:05"))
			}
		}
		next := taskLock{Token: q.Token, Holder: q.Holder, Since: now, Expires: now.Add(time.Duration(q.TTL) * time.Second)}
		if cur.Token == q.Token {
			next.Since = cur.Since
		}
		data, _ := json.Marshal(next)
		return writeAtomic(l.lockPath(q.Task), append(data, '\n'), 0o600)
	})
}

// Unlock releases the lease if q.Token still holds it.
func (l *Local) Unlock(q LockReq) error {
	return l.withRecordLock(func() error {
		var cur taskLock
		data, err := os.ReadFile(l.lockPath(q.Task))
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		} else if err != nil {
			return err
		}
		if json.Unmarshal(data, &cur) == nil && cur.Token != q.Token {
			return nil
		}
		return os.Remove(l.lockPath(q.Task))
	})
}
