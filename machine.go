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
	RepoState(repoRel string) (RepoState, error)
	InitRepo(repoRel string) error
	FinishRepo(FinishRepoReq) error
	Snapshot(SnapshotReq) (SnapshotResult, error)
	Apply(ApplyReq) (string, error)
	ListExtras(wtRel string) ([]FileEntry, error)
	Export(req ExportReq, w io.Writer) error
	Import(req ImportReq, r io.Reader) error
	StageSession(InstallReq) (Session, error)
	PlaceSession(PlaceReq) error
	PlanExtras(PlanReq) (ExtrasPlan, error)
	InstallExtras(InstallExtrasReq) (InstallExtrasResult, error)
	Park(ParkReq) error
	RecordGet(task string) (*Record, error)
	RecordPut(RecordPutReq) error
	RecordList() ([]Record, error)
	HookInfo(HookReq) (HookInfo, error)
	Approve(sha string) error
	RunHook(HookReq) (HookResult, error)
	Launch(LaunchReq) (string, error)
	// GitURL is how the other machine's git reaches repoRel on this machine.
	GitURL(repoRel string) string
	IsLocal() bool
}

type Info struct {
	Name   string `json:"name"`
	Home   string `json:"home"`
	OS     string `json:"os"`
	Claude string `json:"claude"`
	Codex  string `json:"codex"`
	Rex    bool   `json:"rex"`
	Ferry  string `json:"ferry"`
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
	Task   string
	prefix []string
	env    []string
	gitURL func(rel string) string
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
		Label:  label,
		prefix: append(append([]string{"ssh"}, sshOptions...), target, ferryPath),
		gitURL: func(rel string) string { return target + ":" + rel },
	}, nil
}

func (r *Remote) IsLocal() bool            { return false }
func (r *Remote) GitURL(rel string) string { return r.gitURL(rel) }
func (r *Remote) String() string           { return r.Label }

// call runs one remote operation. req is sent as base64 JSON in argv so it
// survives the remote shell. stdin and stdout carry tar streams for Export
// and Import; otherwise stdout carries the JSON response.
func (r *Remote) call(op string, req any, resp any, stdin io.Reader, stdout io.Writer) error {
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
	if stdout != nil {
		cmd.Stdout = stdout
	} else {
		cmd.Stdout = &out
	}
	cmd.Stderr = &errb
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
	if resp != nil && stdout == nil {
		if err := json.Unmarshal(out.Bytes(), resp); err != nil {
			return fmt.Errorf("%s: bad response to %s: %v", r.Label, op, err)
		}
	}
	return nil
}

func rpc[T any](r *Remote, op string, req any) (T, error) {
	var v T
	err := r.call(op, req, &v, nil, nil)
	return v, err
}

func (r *Remote) Info() (Info, error) { return rpc[Info](r, "info", nil) }
func (r *Remote) FindSession(q FindSessionReq) (*Session, error) {
	return rpc[*Session](r, "find-session", q)
}
func (r *Remote) Liveness(s Session) (Liveness, error) { return rpc[Liveness](r, "liveness", s) }
func (r *Remote) StopAgent(q StopReq) error            { _, err := rpc[struct{}](r, "stop-agent", q); return err }
func (r *Remote) RepoState(rel string) (RepoState, error) {
	return rpc[RepoState](r, "repo-state", rel)
}
func (r *Remote) InitRepo(rel string) error { _, err := rpc[struct{}](r, "init-repo", rel); return err }
func (r *Remote) FinishRepo(q FinishRepoReq) error {
	_, err := rpc[struct{}](r, "finish-repo", q)
	return err
}
func (r *Remote) Snapshot(q SnapshotReq) (SnapshotResult, error) {
	return rpc[SnapshotResult](r, "snapshot", q)
}
func (r *Remote) Apply(q ApplyReq) (string, error) { return rpc[string](r, "apply", q) }
func (r *Remote) ListExtras(rel string) ([]FileEntry, error) {
	return rpc[[]FileEntry](r, "list-extras", rel)
}
func (r *Remote) Export(q ExportReq, w io.Writer) error { return r.call("export", q, nil, nil, w) }
func (r *Remote) Import(q ImportReq, rd io.Reader) error {
	var v struct{}
	return r.call("import", q, &v, rd, nil)
}
func (r *Remote) StageSession(q InstallReq) (Session, error) {
	return rpc[Session](r, "stage-session", q)
}
func (r *Remote) PlaceSession(q PlaceReq) error {
	_, err := rpc[struct{}](r, "place-session", q)
	return err
}
func (r *Remote) PlanExtras(q PlanReq) (ExtrasPlan, error) {
	return rpc[ExtrasPlan](r, "plan-extras", q)
}
func (r *Remote) InstallExtras(q InstallExtrasReq) (InstallExtrasResult, error) {
	return rpc[InstallExtrasResult](r, "install-extras", q)
}
func (r *Remote) Park(q ParkReq) error                   { _, err := rpc[struct{}](r, "park", q); return err }
func (r *Remote) RecordGet(task string) (*Record, error) { return rpc[*Record](r, "record-get", task) }
func (r *Remote) RecordPut(q RecordPutReq) error {
	_, err := rpc[struct{}](r, "record-put", q)
	return err
}
func (r *Remote) RecordList() ([]Record, error)         { return rpc[[]Record](r, "record-list", nil) }
func (r *Remote) HookInfo(q HookReq) (HookInfo, error)  { return rpc[HookInfo](r, "hook-info", q) }
func (r *Remote) Approve(sha string) error              { _, err := rpc[struct{}](r, "approve", sha); return err }
func (r *Remote) RunHook(q HookReq) (HookResult, error) { return rpc[HookResult](r, "run-hook", q) }
func (r *Remote) Launch(q LaunchReq) (string, error)    { return rpc[string](r, "launch", q) }

// --- remote server -------------------------------------------------------

// serveRemote handles `ferry remote OP ARG` on the far side.
func serveRemote(args []string) int {
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
	if resp != streamed {
		if err := json.NewEncoder(os.Stdout).Encode(resp); err != nil {
			fmt.Fprintln(os.Stderr, "ferry-error:", err)
			return 1
		}
	}
	return 0
}

var streamed = &struct{}{}

func decode[T any](raw []byte) (T, error) {
	var v T
	if len(raw) == 0 {
		return v, nil
	}
	err := json.Unmarshal(raw, &v)
	return v, err
}

func dispatch(l *Local, op string, raw []byte) (any, error) {
	none := struct{}{}
	switch op {
	case "info":
		return l.Info()
	case "find-session":
		q, err := decode[FindSessionReq](raw)
		if err != nil {
			return nil, err
		}
		return l.FindSession(q)
	case "liveness":
		q, err := decode[Session](raw)
		if err != nil {
			return nil, err
		}
		return l.Liveness(q)
	case "stop-agent":
		q, err := decode[StopReq](raw)
		if err != nil {
			return nil, err
		}
		return none, l.StopAgent(q)
	case "repo-state":
		q, err := decode[string](raw)
		if err != nil {
			return nil, err
		}
		return l.RepoState(q)
	case "init-repo":
		q, err := decode[string](raw)
		if err != nil {
			return nil, err
		}
		return none, l.InitRepo(q)
	case "finish-repo":
		q, err := decode[FinishRepoReq](raw)
		if err != nil {
			return nil, err
		}
		return none, l.FinishRepo(q)
	case "snapshot":
		q, err := decode[SnapshotReq](raw)
		if err != nil {
			return nil, err
		}
		return l.Snapshot(q)
	case "apply":
		q, err := decode[ApplyReq](raw)
		if err != nil {
			return nil, err
		}
		return l.Apply(q)
	case "list-extras":
		q, err := decode[string](raw)
		if err != nil {
			return nil, err
		}
		return l.ListExtras(q)
	case "export":
		q, err := decode[ExportReq](raw)
		if err != nil {
			return nil, err
		}
		return streamed, l.Export(q, os.Stdout)
	case "import":
		q, err := decode[ImportReq](raw)
		if err != nil {
			return nil, err
		}
		return none, l.Import(q, os.Stdin)
	case "stage-session":
		q, err := decode[InstallReq](raw)
		if err != nil {
			return nil, err
		}
		return l.StageSession(q)
	case "place-session":
		q, err := decode[PlaceReq](raw)
		if err != nil {
			return nil, err
		}
		return none, l.PlaceSession(q)
	case "plan-extras":
		q, err := decode[PlanReq](raw)
		if err != nil {
			return nil, err
		}
		return l.PlanExtras(q)
	case "install-extras":
		q, err := decode[InstallExtrasReq](raw)
		if err != nil {
			return nil, err
		}
		return l.InstallExtras(q)
	case "park":
		q, err := decode[ParkReq](raw)
		if err != nil {
			return nil, err
		}
		return none, l.Park(q)
	case "record-get":
		q, err := decode[string](raw)
		if err != nil {
			return nil, err
		}
		return l.RecordGet(q)
	case "record-put":
		q, err := decode[RecordPutReq](raw)
		if err != nil {
			return nil, err
		}
		return none, l.RecordPut(q)
	case "record-list":
		return l.RecordList()
	case "hook-info":
		q, err := decode[HookReq](raw)
		if err != nil {
			return nil, err
		}
		return l.HookInfo(q)
	case "approve":
		q, err := decode[string](raw)
		if err != nil {
			return nil, err
		}
		return none, l.Approve(q)
	case "run-hook":
		q, err := decode[HookReq](raw)
		if err != nil {
			return nil, err
		}
		return l.RunHook(q)
	case "launch":
		q, err := decode[LaunchReq](raw)
		if err != nil {
			return nil, err
		}
		return l.Launch(q)
	}
	return nil, errors.New("unknown remote operation " + op)
}

func lastLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.LastIndexByte(s, '\n'); i >= 0 {
		return s[i+1:]
	}
	return s
}

func oneLine(s string) string { return strings.ReplaceAll(strings.TrimSpace(s), "\n", " | ") }
