package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Machine is one side of a handoff. Local runs operations in-process against
// a home directory; Remote runs the same operations through `ferry remote` on
// another machine. The handoff code only sees this interface.
type Machine interface {
	Info() (Info, error)
	FindSession(FindSessionReq) (*Session, error)
	Liveness(Session) (Liveness, error)
	StopAgent(StopReq) error
	Agents() ([]AgentInfo, error)
	Worktree(rel string) (task, error)
	RepoState(repoRel string) (RepoState, error)
	InitRepo(repoRel string) error
	FinishRepo(FinishRepoReq) error
	Snapshot(SnapshotReq) (SnapshotResult, error)
	Apply(ApplyReq) (string, error)
	SyncRemotes(SyncRemotesReq) (SyncRemotesResult, error)
	Unsent(UnsentReq) (Unsent, error)
	SaveUnsent(SaveUnsentReq) (SavedUnsent, error)
	ListExtras(wtRel string) (ExtrasList, error)
	GitHooks(repoRel string) ([]GitHook, error)
	InstallGitHooks(InstallGitHooksReq) error
	StageSession(InstallReq) (Session, error)
	PlaceSession(PlaceReq) error
	PrepareExtras(PrepareExtrasReq) (PrepareExtrasResult, error)
	RemoveExtras(RemoveExtrasReq) (RemoveExtrasResult, error)
	Park(ParkReq) error
	RecordGet(task string) (*Record, error)
	RecordPut(RecordPutReq) error
	RecordList() ([]Record, error)
	HookInfo(HookReq) (HookInfo, error)
	Approve(sha string) error
	RunHook(HookReq) (HookResult, error)
	Launch(LaunchReq) (LaunchResult, error)
	Teardown(TeardownReq) (TeardownResult, error)
	// GitURL is how git and rsync on the other machine reach rel on this one.
	GitURL(rel string) string
	IsLocal() bool
}

// protocol is the version of the `ferry remote` protocol. Bump it when a
// change means a peer built before it can't serve this one: an op removed or
// renamed, a request or response it would misread, or an op this side now
// needs. Adding an optional field doesn't need a bump. Both sides of a
// handoff must agree; a peer that predates the field reports 0.
const protocol = 8

type Info struct {
	Name     string `json:"name"`
	Protocol int    `json:"protocol"`
	Home     string `json:"home"`
	OS       string `json:"os"`
	Claude   string `json:"claude"`
	Codex    string `json:"codex"`
	Rex      bool   `json:"rex"`
	Rsync    string `json:"rsync"` // version, "" if missing or not rsync 3
	Ferry    string `json:"ferry"`
}

// --- remote transport ----------------------------------------------------

const sshControlDir = ".local/state/ferry/ssh"

// sshOptions apply to every ssh ferry starts, including git's. A handoff
// makes a few dozen calls, so they share one connection (ControlMaster),
// kept for a minute after the last one. Keepalives turn a dead link, such as
// a closed lid on the other end, into an error within a minute instead of a
// hang.
var sshOptions = []string{
	"-o", "BatchMode=yes",
	"-o", "ConnectTimeout=10",
	"-o", "ServerAliveInterval=15",
	"-o", "ServerAliveCountMax=4",
	"-o", "ControlMaster=auto",
	"-o", "ControlPath=~/" + sshControlDir + "/%C",
	"-o", "ControlPersist=60",
}

// Remote reaches another machine by running `ferry remote OP ARG` through a
// command prefix: ssh for real hosts, or this binary with a different HOME
// for tests ("exec:/path/to/home").
type Remote struct {
	Label string
	// Task, when set, makes each call hold the task's lock on the far side
	// (lockRemoteCall), so a call left running by a ferry that died finishes
	// before a rerun's calls start.
	Task string
	// Target and FerryPath are the ssh target and the far side's ferry
	// binary; Target is "" for the exec: test transport.
	Target    string
	FerryPath string
	checked   bool // the far side's protocol has been compared with ours
	prefix    []string
	env       []string
	gitURL    func(rel string) string
}

func NewRemote(label, target, ferryPath string) (*Remote, error) {
	if home, ok := strings.CutPrefix(target, "exec:"); ok {
		self, err := os.Executable()
		if err != nil {
			return nil, err
		}
		return &Remote{
			Label:  label,
			prefix: []string{self},
			env:    []string{"HOME=" + home},
			gitURL: func(rel string) string { return filepath.Join(home, rel) },
		}, nil
	}
	if ferryPath == "" {
		ferryPath = "~/.local/bin/ferry"
	}
	if home, err := os.UserHomeDir(); err == nil {
		os.MkdirAll(filepath.Join(home, sshControlDir), 0o700)
	}
	return &Remote{
		Label:     label,
		Target:    target,
		FerryPath: ferryPath,
		prefix:    append(append([]string{"ssh"}, sshOptions...), target, ferryPath),
		gitURL:    func(rel string) string { return target + ":" + rel },
	}, nil
}

func (r *Remote) IsLocal() bool            { return false }
func (r *Remote) GitURL(rel string) string { return r.gitURL(rel) }
func (r *Remote) String() string           { return r.Label }

// call runs one remote operation. req is sent as base64 JSON in argv so it
// survives the remote shell. stdin, when set, carries a request body too big
// for argv; stdout carries the JSON response.
func (r *Remote) call(op string, req any, resp any, stdin io.Reader) error {
	// Before the first real call, make sure the far side speaks this
	// protocol, so a stale binary fails with a fix instead of a decode error
	// partway through a handoff.
	if !r.checked && op != "info" {
		r.checked = true
		info, err := r.Info()
		if err != nil {
			return err
		}
		if info.Protocol != protocol {
			return fmt.Errorf("%s runs ferry %s (protocol %d), this is %s (protocol %d); run `ferry setup %s` to update it", r.Label, info.Ferry, info.Protocol, version, protocol, r.Label)
		}
	}
	arg, err := json.Marshal(req)
	if err != nil {
		return err
	}
	argv := append(append([]string{}, r.prefix...), "remote", op, base64.StdEncoding.EncodeToString(arg))
	if r.Task != "" {
		argv = append(argv, r.Task) // a ferry too old to know it ignores it
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Env = append(os.Environ(), r.env...)
	cmd.Stdin = stdin
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(errb.String())
		if m, ok := strings.CutPrefix(lastLine(msg), "ferry-error: "); ok {
			return fmt.Errorf("%s: %s", r.Label, m)
		}
		if msg == "" {
			msg = err.Error()
		}
		return fmt.Errorf("%s: %s %s: %s", r.Label, "remote", op, msg)
	}
	if resp != nil {
		if err := json.Unmarshal(out.Bytes(), resp); err != nil {
			return fmt.Errorf("%s: bad response to %s: %v", r.Label, op, err)
		}
	}
	return nil
}

func rpc[T any](r *Remote, op string, req any) (T, error) {
	var v T
	err := r.call(op, req, &v, nil)
	return v, err
}

// do runs an op that returns only an error.
func do(r *Remote, op string, req any) error {
	_, err := rpc[struct{}](r, op, req)
	return err
}

func (r *Remote) Info() (Info, error) { return rpc[Info](r, "info", nil) }
func (r *Remote) FindSession(q FindSessionReq) (*Session, error) {
	return rpc[*Session](r, "find-session", q)
}
func (r *Remote) Liveness(s Session) (Liveness, error) { return rpc[Liveness](r, "liveness", s) }
func (r *Remote) StopAgent(q StopReq) error            { return do(r, "stop-agent", q) }
func (r *Remote) Agents() ([]AgentInfo, error)         { return rpc[[]AgentInfo](r, "agents", nil) }
func (r *Remote) Worktree(rel string) (task, error)    { return rpc[task](r, "worktree", rel) }
func (r *Remote) RepoState(rel string) (RepoState, error) {
	return rpc[RepoState](r, "repo-state", rel)
}
func (r *Remote) InitRepo(rel string) error        { return do(r, "init-repo", rel) }
func (r *Remote) FinishRepo(q FinishRepoReq) error { return do(r, "finish-repo", q) }
func (r *Remote) Snapshot(q SnapshotReq) (SnapshotResult, error) {
	return rpc[SnapshotResult](r, "snapshot", q)
}
func (r *Remote) Apply(q ApplyReq) (string, error)   { return rpc[string](r, "apply", q) }
func (r *Remote) Unsent(q UnsentReq) (Unsent, error) { return rpc[Unsent](r, "unsent", q) }
func (r *Remote) SaveUnsent(q SaveUnsentReq) (SavedUnsent, error) {
	return rpc[SavedUnsent](r, "save-unsent", q)
}
func (r *Remote) SyncRemotes(q SyncRemotesReq) (SyncRemotesResult, error) {
	return rpc[SyncRemotesResult](r, "sync-remotes", q)
}
func (r *Remote) ListExtras(rel string) (ExtrasList, error) {
	return rpc[ExtrasList](r, "list-extras", rel)
}
func (r *Remote) StageSession(q InstallReq) (Session, error) {
	return rpc[Session](r, "stage-session", q)
}
func (r *Remote) PlaceSession(q PlaceReq) error { return do(r, "place-session", q) }
func (r *Remote) PrepareExtras(q PrepareExtrasReq) (PrepareExtrasResult, error) {
	return rpc[PrepareExtrasResult](r, "prepare-extras", q)
}
func (r *Remote) RemoveExtras(q RemoveExtrasReq) (RemoveExtrasResult, error) {
	return rpc[RemoveExtrasResult](r, "remove-extras", q)
}
func (r *Remote) Park(q ParkReq) error                   { return do(r, "park", q) }
func (r *Remote) RecordGet(task string) (*Record, error) { return rpc[*Record](r, "record-get", task) }
func (r *Remote) RecordPut(q RecordPutReq) error         { return do(r, "record-put", q) }
func (r *Remote) RecordList() ([]Record, error)          { return rpc[[]Record](r, "record-list", nil) }
func (r *Remote) HookInfo(q HookReq) (HookInfo, error)   { return rpc[HookInfo](r, "hook-info", q) }
func (r *Remote) Approve(sha string) error               { return do(r, "approve", sha) }
func (r *Remote) RunHook(q HookReq) (HookResult, error)  { return rpc[HookResult](r, "run-hook", q) }
func (r *Remote) Teardown(q TeardownReq) (TeardownResult, error) {
	return rpc[TeardownResult](r, "teardown", q)
}
func (r *Remote) Launch(q LaunchReq) (LaunchResult, error) {
	return rpc[LaunchResult](r, "launch", q)
}

// --- remote server -------------------------------------------------------

// serveRemote handles `ferry remote OP ARG` on the far side.
func serveRemote(args []string) int {
	useLoginPath = true
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "ferry-error: remote: missing operation")
		return 2
	}
	op := args[0]
	var raw []byte
	if len(args) > 1 {
		b, err := base64.StdEncoding.DecodeString(args[1])
		if err != nil {
			fmt.Fprintln(os.Stderr, "ferry-error: remote: bad argument encoding")
			return 2
		}
		raw = b
	}
	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintln(os.Stderr, "ferry-error:", err)
		return 1
	}
	l := &Local{Home: home}
	if len(args) > 2 {
		release, err := l.lockRemoteCall(args[2])
		if err != nil {
			fmt.Fprintln(os.Stderr, "ferry-error: "+oneLine(err.Error()))
			return 1
		}
		defer release()
	}
	resp, err := dispatch(l, op, raw)
	if err != nil {
		fmt.Fprintln(os.Stderr, "ferry-error: "+oneLine(err.Error()))
		return 1
	}
	if err := json.NewEncoder(os.Stdout).Encode(resp); err != nil {
		fmt.Fprintln(os.Stderr, "ferry-error:", err)
		return 1
	}
	return 0
}

func decode[T any](raw []byte) (T, error) {
	var v T
	if len(raw) == 0 {
		return v, nil
	}
	err := json.Unmarshal(raw, &v)
	return v, err
}

// handler serves one remote op from its JSON argument.
type handler func(l *Local, raw []byte) (any, error)

// query serves an op that returns a value.
func query[Q, R any](f func(*Local, Q) (R, error)) handler {
	return func(l *Local, raw []byte) (any, error) {
		q, err := decode[Q](raw)
		if err != nil {
			return nil, err
		}
		return f(l, q)
	}
}

// command serves an op that returns only an error.
func command[Q any](f func(*Local, Q) error) handler {
	return func(l *Local, raw []byte) (any, error) {
		q, err := decode[Q](raw)
		if err != nil {
			return nil, err
		}
		return struct{}{}, f(l, q)
	}
}

// noArg serves an op that takes no argument.
func noArg[R any](f func(*Local) (R, error)) handler {
	return func(l *Local, _ []byte) (any, error) { return f(l) }
}

var ops = map[string]handler{
	"info":           noArg((*Local).Info),
	"agents":         noArg((*Local).Agents),
	"record-list":    noArg((*Local).RecordList),
	"find-session":   query((*Local).FindSession),
	"liveness":       query((*Local).Liveness),
	"git-hooks":      query((*Local).GitHooks),
	"worktree":       query((*Local).Worktree),
	"repo-state":     query((*Local).RepoState),
	"snapshot":       query((*Local).Snapshot),
	"apply":          query((*Local).Apply),
	"sync-remotes":   query((*Local).SyncRemotes),
	"unsent":         query((*Local).Unsent),
	"save-unsent":    query((*Local).SaveUnsent),
	"list-extras":    query((*Local).ListExtras),
	"stage-session":  query((*Local).StageSession),
	"remove-extras":  query((*Local).RemoveExtras),
	"prepare-extras": query((*Local).PrepareExtras),
	"record-get":     query((*Local).RecordGet),
	"hook-info":      query((*Local).HookInfo),
	"run-hook":       query((*Local).RunHook),
	"launch":         query((*Local).Launch),
	"teardown":       query((*Local).Teardown),
	"stop-agent":     command((*Local).StopAgent),
	"init-repo":      command((*Local).InitRepo),
	"finish-repo":    command((*Local).FinishRepo),
	"place-session":  command((*Local).PlaceSession),
	"park":           command((*Local).Park),
	"record-put":     command((*Local).RecordPut),
	"approve":        command((*Local).Approve),
	"install-git-hooks": func(l *Local, raw []byte) (any, error) {
		return struct{}{}, serveInstallGitHooks(l, raw, os.Stdin)
	},
}

func dispatch(l *Local, op string, raw []byte) (any, error) {
	h, ok := ops[op]
	if !ok {
		return nil, errors.New("unknown remote operation " + op)
	}
	return h(l, raw)
}

func lastLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.LastIndexByte(s, '\n'); i >= 0 {
		return s[i+1:]
	}
	return s
}

func oneLine(s string) string { return strings.ReplaceAll(strings.TrimSpace(s), "\n", " | ") }
