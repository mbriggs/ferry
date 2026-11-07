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
	AcceptTrust bool     `json:"accept_trust"`
}

// Launch starts the agent in a new Rex session on this machine and returns
// the session id. When AcceptTrust is set (the folder was trusted on the
// sending machine), it answers the agent's folder-trust prompt, which would
// otherwise leave an unattended agent waiting. Claude's prompt defaults to
// "No, exit", so it needs Down before Enter; Codex defaults to trusting.
func (l *Local) Launch(q LaunchReq) (string, error) {
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
	return created.SessionID, nil
}

func answerTrust(session, block, agent string) {
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		screen, err := run("", nil, "rex", "capture", "-s", session, "-b", block, "--trim")
		if err != nil {
			return
		}
		switch {
		case agent == agentClaude && strings.Contains(screen, "trust this folder"):
			run("", nil, "rex", "send-key", "-s", session, "-b", block, "down")
			run("", nil, "rex", "send-key", "-s", session, "-b", block, "enter")
			return
		case agent == agentCodex && strings.Contains(screen, "Trust this folder"):
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
			if live, err := l.Liveness(q.Session); err == nil && !live.Live {
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
