package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"syscall"
	"time"
)

const (
	agentClaude = "claude"
	agentCodex  = "codex"
)

// Session is one agent conversation as files under a home directory.
type Session struct {
	Agent string `json:"agent"`
	ID    string `json:"id"`
	Cwd   string `json:"cwd"`
	// WorkDir is the directory the agent is in now. It can differ from Cwd,
	// where the session started (and where it resumes), after a cd.
	WorkDir string   `json:"work_dir,omitempty"`
	Files   []string `json:"files"` // relative to home; directories included whole
	Model   string   `json:"model,omitempty"`
	Trusted bool     `json:"trusted"`
	Size    int64    `json:"size"`
}

type FindSessionReq struct {
	Agent  string `json:"agent"`
	ID     string `json:"id"`
	CwdRel string `json:"cwd_rel"`
	// KnownAgent and KnownID name a session to consider alongside the
	// newest one for CwdRel; the more recently written of the two is found.
	KnownAgent string `json:"known_agent,omitempty"`
	KnownID    string `json:"known_id,omitempty"`
}

var nonAlnum = regexp.MustCompile(`[^A-Za-z0-9]`)

// Session ids from both agents are UUIDs. Anything else is refused, because
// ids are joined into paths and may arrive from the other machine.
var sessionIDPattern = regexp.MustCompile(`^[0-9A-Fa-f]{8}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{12}$`)

func checkSessionID(id string) error {
	if !sessionIDPattern.MatchString(id) {
		return fmt.Errorf("%q is not a session id", id)
	}
	return nil
}

// sessionPathOK reports whether rel (relative to home) is one of the files a
// session of this agent and id may consist of. projDir is the Claude project
// directory the session belongs to.
func sessionPathOK(agent, id, projDir, rel string) bool {
	rel = filepath.ToSlash(filepath.Clean(rel))
	switch agent {
	case agentClaude:
		proj := filepath.ToSlash(projDir)
		return rel == proj+"/"+id+".jsonl" ||
			strings.HasPrefix(rel, proj+"/"+id+"/") ||
			strings.HasPrefix(rel, proj+"/memory/") ||
			strings.HasPrefix(rel, ".claude/file-history/"+id+"/")
	case agentCodex:
		return codexRolloutPath.MatchString(rel) && strings.HasSuffix(rel, "-"+id+".jsonl")
	}
	return false
}

var codexRolloutPath = regexp.MustCompile(`^\.codex/sessions/[0-9]{4}/[0-9]{2}/[0-9]{2}/rollout-[^/]+\.jsonl$`)

// claudeSlug is the project directory Claude Code keeps a cwd's sessions in.
func claudeSlug(cwd string) string { return nonAlnum.ReplaceAllString(cwd, "-") }

// FindSession returns the newest session for a working directory, or the
// session with the given id. It returns nil when there is none.
func (l *Local) FindSession(q FindSessionReq) (*Session, error) {
	if q.ID != "" {
		if err := checkSessionID(q.ID); err != nil {
			return nil, err
		}
	}
	var found []Session
	if q.Agent == "" || q.Agent == agentClaude {
		if s, err := l.findClaude(q); err != nil {
			return nil, err
		} else if s != nil {
			found = append(found, *s)
		}
	}
	if q.Agent == "" || q.Agent == agentCodex {
		if s, err := l.findCodex(q); err != nil {
			return nil, err
		} else if s != nil {
			found = append(found, *s)
		}
	}
	if q.ID == "" && q.KnownID != "" && (q.Agent == "" || q.Agent == q.KnownAgent) {
		known, err := l.FindSession(FindSessionReq{Agent: q.KnownAgent, ID: q.KnownID})
		if err != nil {
			return nil, err
		}
		if known != nil && !slices.ContainsFunc(found, func(s Session) bool { return s.ID == known.ID }) {
			found = append(found, *known)
		}
	}
	if len(found) == 0 {
		return nil, nil
	}
	sort.Slice(found, func(i, j int) bool { return l.mtime(found[i].Files[0]).After(l.mtime(found[j].Files[0])) })
	return &found[0], nil
}

func (l *Local) mtime(rel string) time.Time {
	st, err := os.Stat(l.abs(rel))
	if err != nil {
		return time.Time{}
	}
	return st.ModTime()
}

func (l *Local) findClaude(q FindSessionReq) (*Session, error) {
	var transcript string
	if q.ID != "" {
		matches, _ := filepath.Glob(filepath.Join(l.Home, ".claude", "projects", "*", q.ID+".jsonl"))
		if len(matches) == 0 {
			return nil, nil
		}
		transcript = matches[0]
	} else {
		dir := filepath.Join(l.Home, ".claude", "projects", claudeSlug(l.abs(q.CwdRel)))
		matches, _ := filepath.Glob(filepath.Join(dir, "*.jsonl"))
		var newest time.Time
		for _, m := range matches {
			if st, err := os.Stat(m); err == nil && st.ModTime().After(newest) {
				newest, transcript = st.ModTime(), m
			}
		}
		if transcript == "" {
			return nil, nil
		}
	}
	id := strings.TrimSuffix(filepath.Base(transcript), ".jsonl")
	cwd, err := firstJSONField(transcript, func(m map[string]any) string { s, _ := m["cwd"].(string); return s })
	if err != nil {
		return nil, err
	}
	if cwd == "" {
		cwd = l.abs(q.CwdRel)
	}
	rel := func(p string) string { r, _ := filepath.Rel(l.Home, p); return r }
	s := &Session{Agent: agentClaude, ID: id, Cwd: cwd, Files: []string{rel(transcript)}}
	s.WorkDir, _ = lastJSONField(transcript, func(m map[string]any) string { s, _ := m["cwd"].(string); return s })
	if sub := strings.TrimSuffix(transcript, ".jsonl"); isDir(sub) {
		s.Files = append(s.Files, rel(sub))
	}
	// The project's memory belongs to every session in the folder, not just
	// this one, but it's what the agent knows about the project, so it goes
	// where the session goes. PlaceSession merges it with what's there.
	if mem := filepath.Join(filepath.Dir(transcript), "memory"); isDir(mem) {
		s.Files = append(s.Files, rel(mem))
	}
	if fh := filepath.Join(l.Home, ".claude", "file-history", id); isDir(fh) {
		s.Files = append(s.Files, rel(fh))
	}
	s.Trusted = l.claudeTrusted(cwd)
	s.Size = l.size(s.Files)
	return s, nil
}

func (l *Local) findCodex(q FindSessionReq) (*Session, error) {
	root := filepath.Join(l.Home, ".codex", "sessions")
	want := l.abs(q.CwdRel)
	var best string
	var bestTime time.Time
	var bestMeta map[string]any
	filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasPrefix(d.Name(), "rollout-") || !strings.HasSuffix(d.Name(), ".jsonl") {
			return nil
		}
		if q.ID != "" && !strings.HasSuffix(d.Name(), q.ID+".jsonl") {
			return nil
		}
		meta, err := codexMeta(p)
		if err != nil || meta == nil {
			return nil
		}
		if q.ID == "" && meta["cwd"] != want {
			return nil
		}
		info, err := d.Info()
		if err == nil && info.ModTime().After(bestTime) {
			best, bestTime, bestMeta = p, info.ModTime(), meta
		}
		return nil
	})
	if best == "" {
		return nil, nil
	}
	rel, _ := filepath.Rel(l.Home, best)
	cwd, _ := bestMeta["cwd"].(string)
	id, _ := bestMeta["id"].(string)
	s := &Session{Agent: agentCodex, ID: id, Cwd: cwd, Files: []string{rel}}
	s.WorkDir, _ = lastJSONField(best, func(m map[string]any) string {
		if m["type"] != "turn_context" {
			return ""
		}
		p, _ := m["payload"].(map[string]any)
		v, _ := p["cwd"].(string)
		return v
	})
	s.Model, _ = lastJSONField(best, func(m map[string]any) string {
		if m["type"] != "turn_context" {
			return ""
		}
		p, _ := m["payload"].(map[string]any)
		v, _ := p["model"].(string)
		return v
	})
	s.Trusted = l.codexTrusted(cwd)
	s.Size = l.size(s.Files)
	return s, nil
}

func codexMeta(path string) (map[string]any, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	r := bufio.NewReaderSize(f, 1<<20)
	line, err := r.ReadBytes('\n')
	if err != nil && len(line) == 0 {
		return nil, err
	}
	var m map[string]any
	if json.Unmarshal(line, &m) != nil || m["type"] != "session_meta" {
		return nil, nil
	}
	p, _ := m["payload"].(map[string]any)
	return p, nil
}

func scanJSONL(path string, fn func(map[string]any) bool) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 64<<20)
	for sc.Scan() {
		var m map[string]any
		if json.Unmarshal(sc.Bytes(), &m) == nil && !fn(m) {
			return nil
		}
	}
	return sc.Err()
}

func firstJSONField(path string, get func(map[string]any) string) (string, error) {
	v := ""
	err := scanJSONL(path, func(m map[string]any) bool { v = get(m); return v == "" })
	return v, err
}

func lastJSONField(path string, get func(map[string]any) string) (string, error) {
	v := ""
	err := scanJSONL(path, func(m map[string]any) bool {
		if s := get(m); s != "" {
			v = s
		}
		return true
	})
	return v, err
}

func (l *Local) size(rels []string) int64 {
	var n int64
	for _, r := range rels {
		filepath.WalkDir(l.abs(r), func(p string, d fs.DirEntry, err error) error {
			if err == nil && !d.IsDir() {
				if info, err := d.Info(); err == nil {
					n += info.Size()
				}
			}
			return nil
		})
	}
	return n
}

func (l *Local) claudeTrusted(cwd string) bool {
	data, err := os.ReadFile(filepath.Join(l.Home, ".claude.json"))
	if err != nil {
		return false
	}
	var cfg struct {
		Projects map[string]struct {
			HasTrustDialogAccepted bool `json:"hasTrustDialogAccepted"`
		} `json:"projects"`
	}
	if json.Unmarshal(data, &cfg) != nil {
		return false
	}
	return cfg.Projects[cwd].HasTrustDialogAccepted
}

// codexTrusted reads `[projects."<cwd>"] trust_level = "trusted"` from
// config.toml without a TOML parser; the shape Codex writes is fixed.
func (l *Local) codexTrusted(cwd string) bool {
	data, err := os.ReadFile(filepath.Join(l.Home, ".codex", "config.toml"))
	if err != nil {
		return false
	}
	header := fmt.Sprintf("[projects.%q]", cwd)
	in := false
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "[") {
			in = line == header
			continue
		}
		if in && strings.ReplaceAll(line, " ", "") == `trust_level="trusted"` {
			return true
		}
	}
	return false
}

// --- liveness ------------------------------------------------------------

type Liveness struct {
	Live bool `json:"live"`
	// Settling means no process holds the session, but its transcript was
	// written moments ago: Claude removes its registry entry before it has
	// finished writing, so the agent may still be exiting.
	Settling bool   `json:"settling,omitempty"`
	Busy     bool   `json:"busy"`
	PID      int    `json:"pid"`
	Detail   string `json:"detail"`
}

// Liveness reports whether an agent process has the session loaded. Claude
// keeps ~/.claude/sessions/<pid>.json with a status; Codex holds an exclusive
// flock on ~/.codex/thread-writer-locks/<id>.lock in whichever process (TUI
// or app-server daemon) has the thread loaded.
func (l *Local) Liveness(s Session) (Liveness, error) {
	switch s.Agent {
	case agentClaude:
		files, _ := filepath.Glob(filepath.Join(l.Home, ".claude", "sessions", "*.json"))
		for _, f := range files {
			data, err := os.ReadFile(f)
			if err != nil {
				continue
			}
			var e struct {
				PID       int    `json:"pid"`
				SessionID string `json:"sessionId"`
				Status    string `json:"status"`
			}
			if json.Unmarshal(data, &e) != nil || e.SessionID != s.ID || !pidAlive(e.PID) {
				continue
			}
			return Liveness{Live: true, Busy: e.Status == "busy", PID: e.PID, Detail: "claude pid " + fmt.Sprint(e.PID) + " is " + e.Status}, nil
		}
		if len(s.Files) > 0 && time.Since(l.mtime(s.Files[0])) < 2*time.Second {
			return Liveness{Live: true, Settling: true, Detail: "the transcript was written in the last 2 seconds"}, nil
		}
	case agentCodex:
		lock := filepath.Join(l.Home, ".codex", "thread-writer-locks", s.ID+".lock")
		if held, err := flockHeld(lock); err != nil {
			return Liveness{}, err
		} else if held {
			return Liveness{Live: true, Detail: "codex has the thread loaded (TUI, or the app-server daemon for up to a minute after you quit)"}, nil
		}
	}
	return Liveness{}, nil
}

func pidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

// flockHeld reports whether another process holds an exclusive flock on path.
func flockHeld(path string) (bool, error) {
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_SH|syscall.LOCK_NB); err != nil {
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return true, nil
		}
		return false, err
	}
	syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	return false, nil
}

// --- install and park ----------------------------------------------------

type InstallReq struct {
	Session  Session `json:"session"` // as it was on the source
	OldHome  string  `json:"old_home"`
	StageRel string  `json:"stage_rel"`
	Task     string  `json:"task"`
}

// StageSession checks and prepares session files staged from the other
// machine without touching anything live: every file must belong to the
// session, the source home is rewritten to this one, and Claude's project
// directory is renamed for the new cwd. The result sits in
// ~/.local/state/ferry/in/<task>/ready until PlaceSession moves it into
// place. File-history backups are file contents, so they are copied untouched.
func (l *Local) StageSession(q InstallReq) (Session, error) {
	if err := checkSessionID(q.Session.ID); err != nil {
		return Session{}, err
	}
	ready, err := l.readyDir(q.Task)
	if err != nil {
		return Session{}, err
	}
	stage := l.abs(q.StageRel)
	defer os.RemoveAll(stage)
	rel, err := filepath.Rel(q.OldHome, q.Session.Cwd)
	if err != nil || strings.HasPrefix(rel, "..") {
		return Session{}, fmt.Errorf("session cwd %s is outside %s", q.Session.Cwd, q.OldHome)
	}
	out := q.Session
	out.Cwd = l.abs(rel)
	oldProj := filepath.Join(".claude", "projects", claudeSlug(q.Session.Cwd))
	newProj := filepath.Join(".claude", "projects", claudeSlug(out.Cwd))
	mapRel := func(r string) string {
		if after, ok := strings.CutPrefix(r, oldProj+string(filepath.Separator)); ok {
			return filepath.Join(newProj, after)
		}
		return r
	}
	if err := os.RemoveAll(ready); err != nil {
		return Session{}, err
	}
	err = filepath.WalkDir(stage, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		r, _ := filepath.Rel(stage, p)
		if !d.Type().IsRegular() || !sessionPathOK(q.Session.Agent, q.Session.ID, oldProj, r) {
			return fmt.Errorf("refusing %s: not part of %s session %s", r, q.Session.Agent, q.Session.ID)
		}
		if (strings.HasSuffix(p, ".jsonl") || strings.HasSuffix(p, ".json")) && !strings.HasPrefix(r, filepath.Join(".claude", "file-history")) {
			data, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			rewrite := rewriteHomeLines
			if strings.HasSuffix(p, ".json") {
				rewrite = func(b []byte, o, n string) ([]byte, int, error) { return rewriteHome(b, o, n) }
			}
			got, _, err := rewrite(data, q.OldHome, l.Home)
			if err != nil {
				return fmt.Errorf("%s: %w", r, err)
			}
			if err := writeAtomic(p, got, 0o600); err != nil {
				return err
			}
		}
		return moveFile(p, filepath.Join(ready, mapRel(r)))
	})
	if err != nil {
		os.RemoveAll(ready)
		return Session{}, err
	}
	out.Files = nil
	for _, f := range q.Session.Files {
		out.Files = append(out.Files, mapRel(f))
	}
	return out, nil
}

type PlaceReq struct {
	Session Session `json:"session"` // as StageSession returned it
	Task    string  `json:"task"`
}

// PlaceSession moves a staged session into the agent's live directories,
// parking any copy already there. Once it's done there is nothing staged, so
// running it again does nothing.
func (l *Local) PlaceSession(q PlaceReq) error {
	if err := checkSessionID(q.Session.ID); err != nil {
		return err
	}
	ready, err := l.readyDir(q.Task)
	if err != nil || !isDir(ready) {
		return err
	}
	// This machine may have its own copy from an earlier trip, and someone
	// may have resumed it since the handoff began.
	if live, err := l.Liveness(q.Session); err != nil {
		return err
	} else if live.Live && !live.Settling {
		return fmt.Errorf("%s has session %s loaded here; exit it, then run this again", live.Detail, q.Session.ID)
	}
	proj := filepath.Join(".claude", "projects", claudeSlug(q.Session.Cwd))
	replaced := l.stateDir("parked", q.Task+"-replaced-"+time.Now().Format("20060102-150405"))
	err = filepath.WalkDir(ready, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		r, _ := filepath.Rel(ready, p)
		if !d.Type().IsRegular() || !sessionPathOK(q.Session.Agent, q.Session.ID, proj, r) {
			return fmt.Errorf("refusing %s: not part of %s session %s", r, q.Session.Agent, q.Session.ID)
		}
		if exists(l.abs(r)) {
			if sameFile(p, l.abs(r)) {
				return os.Remove(p)
			}
			if err := moveFile(l.abs(r), filepath.Join(replaced, r)); err != nil {
				return err
			}
		}
		return moveFile(p, l.abs(r))
	})
	if err != nil {
		return err
	}
	return os.RemoveAll(ready)
}

// sameFile reports whether two files hold the same bytes.
func sameFile(a, b string) bool {
	x, err1 := os.ReadFile(a)
	y, err2 := os.ReadFile(b)
	return err1 == nil && err2 == nil && bytes.Equal(x, y)
}

func (l *Local) readyDir(task string) (string, error) {
	if !taskPattern.MatchString(task) {
		return "", fmt.Errorf("bad task key %q", task)
	}
	return l.stateDir("in", task, "ready"), nil
}

type ParkReq struct {
	Session Session `json:"session"`
	Task    string  `json:"task"`
	// As names the parked copy: "g<generation>" for a handed-off session.
	As string `json:"as"`
}

var (
	parkName    = regexp.MustCompile(`^(extras-[0-9-]+|g[0-9]+)$`)
	taskPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]*$`)
)

// parkDir is where a task's parked session lives. Both names arrive over the
// remote protocol, so they're checked before they become a path.
func (l *Local) parkDir(task, as string) (string, error) {
	if !taskPattern.MatchString(task) || !parkName.MatchString(as) {
		return "", fmt.Errorf("bad park name %q %q", task, as)
	}
	return l.stateDir("parked", task+"-"+as), nil
}

// Park moves a session out of the agent's search path, so a habitual
// `claude --resume` or `codex resume` on this machine fails instead of
// forking the conversation. File history stays; it is only read on rewind.
func (l *Local) Park(q ParkReq) error {
	if err := checkSessionID(q.Session.ID); err != nil {
		return err
	}
	dest, err := l.parkDir(q.Task, q.As)
	if err != nil {
		return err
	}
	proj := filepath.Join(".claude", "projects", claudeSlug(q.Session.Cwd))
	for _, f := range q.Session.Files {
		if !sessionPathOK(q.Session.Agent, q.Session.ID, proj, f) && !sessionPathOK(q.Session.Agent, q.Session.ID, proj, f+"/x") {
			return fmt.Errorf("refusing to park %s: not part of session %s", f, q.Session.ID)
		}
		if strings.HasPrefix(f, filepath.Join(".claude", "file-history")) {
			continue
		}
		if !exists(l.abs(f)) {
			continue
		}
		if err := moveFile(l.abs(f), filepath.Join(dest, f)); err != nil {
			return err
		}
	}
	return nil
}

func moveFile(from, to string) error {
	if err := os.MkdirAll(filepath.Dir(to), 0o755); err != nil {
		return err
	}
	return os.Rename(from, to)
}
