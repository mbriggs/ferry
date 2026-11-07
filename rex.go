package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

type LaunchReq struct {
	Name        string   `json:"name"`
	CwdRel      string   `json:"cwd_rel"`
	Argv        []string `json:"argv"`
	Agent       string   `json:"agent"`
	Session     Session  `json:"session"` // as placed here, to see when the agent has it loaded
	AcceptTrust bool     `json:"accept_trust"`
}

// launchTimeout is how long Launch waits for the agent to load the session.
var launchTimeout = 60 * time.Second

// Launch starts the agent in a new Rex session on this machine and returns
// the session id. When AcceptTrust is set (the folder was trusted on the
// sending machine), it answers the agent's folder-trust prompt, which would
// otherwise leave an unattended agent waiting. Claude's prompt defaults to
// "No, exit", so it needs Down before Enter; Codex defaults to trusting.
//
// Launch returns only once Liveness can see the agent. Until the agent
// registers (Claude writes ~/.claude/sessions/<pid>.json, Codex takes its
// thread lock), nothing else in ferry knows it is running: a `ferry from`
// would park the transcript under it, and a rerun would start a second one.
// So an agent that doesn't register within launchTimeout has its Rex session
// killed, and Launch fails.
func (l *Local) Launch(q LaunchReq) (string, error) {
	cwd := l.abs(q.CwdRel)
	trusted := (q.Agent == agentClaude && l.claudeTrusted(cwd)) || (q.Agent == agentCodex && l.codexTrusted(cwd))
	if trusted {
		q.AcceptTrust = false // there's no prompt to answer
	} else if !q.AcceptTrust {
		return "", fmt.Errorf("%s isn't trusted here and wasn't on the sending machine; not starting an agent that would wait at the trust prompt", cwd)
	}
	args := append([]string{"new", q.Name, "-c", l.abs(q.CwdRel), "--window", q.Agent, "--json", "--"}, q.Argv...)
	out, err := run(l.Home, nil, "rex", args...)
	if err != nil {
		return "", err
	}
	var created struct {
		SessionID      string `json:"session_id"`
		InitialWindows []struct {
			BlockIDs []string `json:"block_ids"`
		} `json:"initial_windows"`
	}
	if err := json.Unmarshal([]byte(out), &created); err != nil {
		return "", fmt.Errorf("rex new: %v", err)
	}
	if q.AcceptTrust && len(created.InitialWindows) > 0 && len(created.InitialWindows[0].BlockIDs) > 0 {
		answerTrust(created.SessionID, created.InitialWindows[0].BlockIDs[0], q.Agent)
	}
	deadline := time.Now().Add(launchTimeout)
	for {
		live, err := l.Liveness(q.Session)
		if err == nil && live.Live && !live.Settling {
			return created.SessionID, nil
		}
		if time.Now().After(deadline) {
			break
		}
		time.Sleep(300 * time.Millisecond)
	}
	if _, err := run("", nil, "rex", "kill", created.SessionID); err != nil {
		return "", fmt.Errorf("the agent didn't load the session within %v, and killing rex session %s failed (%v); kill it by hand before resuming", launchTimeout, created.SessionID, err)
	}
	return "", fmt.Errorf("the agent didn't load the session within %v (waiting at a prompt?); killed its rex session", launchTimeout)
}

// answerTrust answers the folder-trust dialog if it appears before the agent
// is ready. It matches the dialog's own option text, so a conversation that
// merely mentions trusting a folder never receives keystrokes.
func answerTrust(session, block, agent string) {
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		screen, err := run("", nil, "rex", "capture", "-s", session, "-b", block, "--trim")
		if err != nil {
			return
		}
		switch {
		case agent == agentClaude && strings.Contains(screen, "Yes, I trust this folder") && strings.Contains(screen, "No, exit"):
			run("", nil, "rex", "send-key", "-s", session, "-b", block, "down")
			run("", nil, "rex", "send-key", "-s", session, "-b", block, "enter")
			return
		case agent == agentCodex && strings.Contains(screen, "Trust and continue"):
			run("", nil, "rex", "send-key", "-s", session, "-b", block, "enter")
			return
		case strings.Contains(screen, "Ask Codex") || strings.Contains(screen, "? for shortcuts") || strings.Contains(screen, "auto mode") || strings.Contains(screen, "for agents"):
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
}

type StopReq struct {
	Session    Session `json:"session"`
	RexSession string  `json:"rex_session"`
	Name       string  `json:"name"` // rex session label, if the id was never recorded
}

// StopAgent asks an idle agent in a Rex session on this machine to exit and
// waits for it to let go of the session. It never interrupts a busy agent.
func (l *Local) StopAgent(q StopReq) error {
	live, err := l.Liveness(q.Session)
	if err != nil || !live.Live {
		return err
	}
	if live.Busy {
		return fmt.Errorf("%s is mid-turn; wait for it to finish", q.Session.Agent)
	}
	if q.RexSession == "" {
		q.RexSession = rexSessionNamed(q.Name)
	}
	if q.RexSession == "" {
		return fmt.Errorf("%s; exit it first", live.Detail)
	}
	block, state, err := agentBlock(q.RexSession)
	if err != nil {
		return err
	}
	if state == "working" {
		return fmt.Errorf("%s is mid-turn; wait for it to finish", q.Session.Agent)
	}
	pid := live.PID // 0 for codex, whose lock release already means it's done
	// Codex can drop input that lands as a turn finishes, so retry.
	for range 3 {
		run("", nil, "rex", "send", "-s", q.RexSession, "-b", block, "/exit")
		run("", nil, "rex", "send-key", "-s", q.RexSession, "-b", block, "enter")
		deadline := time.Now().Add(6 * time.Second)
		for time.Now().Before(deadline) {
			if live, err := l.Liveness(q.Session); err == nil && (!live.Live || live.Settling) {
				return waitExit(pid)
			}
			time.Sleep(300 * time.Millisecond)
		}
	}
	return errors.New("sent /exit but the agent is still running; exit it by hand")
}

// waitExit waits for a process that has released its session to finish
// exiting. Claude removes its registry file before it's done writing, so the
// transcript is only safe to move once the process is gone.
func waitExit(pid int) error {
	deadline := time.Now().Add(15 * time.Second)
	for pidAlive(pid) {
		if time.Now().After(deadline) {
			return fmt.Errorf("agent pid %d released the session but hasn't exited", pid)
		}
		time.Sleep(200 * time.Millisecond)
	}
	return nil
}

// agentBlock finds the pane running claude or codex in a Rex session.
func agentBlock(session string) (block, state string, err error) {
	out, err := run("", nil, "rex", "block", "ls", "-s", session, "--json")
	if err != nil {
		return "", "", err
	}
	var ls struct {
		Blocks []struct {
			BlockID string `json:"block_id"`
		} `json:"blocks"`
	}
	if err := json.Unmarshal([]byte(out), &ls); err != nil {
		return "", "", err
	}
	for _, b := range ls.Blocks {
		ps, err := run("", nil, "rex", "block", "call", "-s", session, "-b", b.BlockID, "program_status")
		if err != nil {
			continue
		}
		var st struct {
			Records []struct {
				State string `json:"state"`
				App   string `json:"app"`
			} `json:"records"`
		}
		if json.Unmarshal([]byte(ps), &st) != nil {
			continue
		}
		for _, r := range st.Records {
			if r.App == "claude-code" || r.App == "codex" {
				return b.BlockID, r.State, nil
			}
		}
	}
	return "", "", fmt.Errorf("no agent pane found in rex session %s", session)
}

// rexSessionNamed returns the id of the one rex session with this label, or "".
func rexSessionNamed(name string) string {
	if name == "" {
		return ""
	}
	out, err := run("", nil, "rex", "ls", "--json")
	if err != nil {
		return ""
	}
	var ls struct {
		Sessions []struct {
			SessionID string `json:"session_id"`
			Label     string `json:"label"`
		} `json:"sessions"`
	}
	if json.Unmarshal([]byte(out), &ls) != nil {
		return ""
	}
	id := ""
	for _, s := range ls.Sessions {
		if s.Label == name {
			if id != "" {
				return "" // ambiguous
			}
			id = s.SessionID
		}
	}
	return id
}
