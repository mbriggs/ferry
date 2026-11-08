package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
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
	// finish() checks this too, but a Launch left running by a ferry that
	// died can reach here after you opened the session yourself.
	if live, err := l.Liveness(q.Session); err != nil {
		return "", err
	} else if live.Live && !live.Settling {
		return "", fmt.Errorf("%s is already running here (%s); not starting another", short(q.Session.ID), live.Detail)
	}
	args := append([]string{"new", q.Name, "-c", l.abs(q.CwdRel), "--window", q.Agent, "--json", "--"}, q.Argv...)
	out, err := rexRun(l.Home, args...)
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
	if _, err := rexRun("", "kill", created.SessionID); err != nil {
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
		screen, err := rexRun("", "capture", "-s", session, "-b", block, "--trim")
		if err != nil {
			return
		}
		switch {
		case agent == agentClaude && strings.Contains(screen, "Yes, I trust this folder") && strings.Contains(screen, "No, exit"):
			// Keys can arrive before the dialog takes input, so this goes by
			// where the cursor is and looks again: down from "No, exit",
			// Enter on "Yes". Pressing down blindly a second time could wrap
			// back to "No, exit".
			if cursorOn(screen, "Yes, I trust this folder") {
				rexRun("", "send-key", "-s", session, "-b", block, "enter")
			} else if cursorOn(screen, "No, exit") {
				rexRun("", "send-key", "-s", session, "-b", block, "down")
			}
			time.Sleep(700 * time.Millisecond)
			continue
		case agent == agentCodex && strings.Contains(screen, "Trust and continue"):
			rexRun("", "send-key", "-s", session, "-b", block, "enter")
			time.Sleep(700 * time.Millisecond)
			continue
		case strings.Contains(screen, "Ask Codex") || strings.Contains(screen, "? for shortcuts") || strings.Contains(screen, "auto mode") || strings.Contains(screen, "for agents"):
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// cursorOn reports whether a dialog's selection cursor is on the option.
func cursorOn(screen, option string) bool {
	return regexp.MustCompile(`❯\s*` + regexp.QuoteMeta(option)).MatchString(screen)
}

type StopReq struct {
	Session Session `json:"session"`
	// RexSession and Name come from the task record: the Rex session ferry
	// launched the agent in, or its label if the id was never recorded. They
	// are empty for an agent ferry didn't launch, which can only be stopped
	// through the pane its own environment names.
	RexSession string `json:"rex_session"`
	Name       string `json:"name"`
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
	// The agent's own environment names its pane exactly. The record's Rex
	// session is the fallback, for where that can't be read. Either way /exit
	// is only typed into a pane that Rex says is running this kind of agent.
	// A session from the record is one ferry made, with the agent as its
	// only command, so it's left empty once the agent exits.
	recorded := q.RexSession
	app := rexApp(q.Session.Agent)
	block, state := "", ""
	if sess, b := rexOfPID(agentPID(l, q.Session, live)); sess != "" && b != "" {
		got, st := programStatus(sess, b)
		if got != app {
			return fmt.Errorf("%s; its rex pane isn't showing %s now, so exit it by hand", live.Detail, q.Session.Agent)
		}
		q.RexSession, block, state = sess, b, st
	} else {
		if q.RexSession == "" {
			q.RexSession = rexSessionNamed(q.Name)
		}
		if q.RexSession == "" {
			return fmt.Errorf("%s; exit it first", live.Detail)
		}
		if block, state, err = agentBlock(q.RexSession, app); err != nil {
			return err
		}
	}
	if state == "working" {
		return fmt.Errorf("%s is mid-turn; wait for it to finish", q.Session.Agent)
	}
	pid := live.PID // 0 for codex, whose lock release already means it's done
	// Codex can drop input that lands as a turn finishes, so retry.
	for range 3 {
		rexRun("", "send", "-s", q.RexSession, "-b", block, "/exit")
		rexRun("", "send-key", "-s", q.RexSession, "-b", block, "enter")
		deadline := time.Now().Add(6 * time.Second)
		for time.Now().Before(deadline) {
			if live, err := l.Liveness(q.Session); err == nil && (!live.Live || live.Settling) {
				if err := waitExit(pid); err != nil {
					return err
				}
				if recorded != "" {
					closeIfEmpty(recorded)
				}
				return nil
			}
			time.Sleep(300 * time.Millisecond)
		}
	}
	return errors.New("sent /exit but the agent is still running; exit it by hand")
}

// closeIfEmpty kills a Rex session that has no blocks left, as one ferry
// launched an agent in is once the agent exits. A session with anything
// still in it is left alone.
func closeIfEmpty(session string) {
	out, err := rexRun("", "block", "ls", "-s", session, "--json")
	if err != nil {
		return
	}
	var ls struct {
		Blocks []json.RawMessage `json:"blocks"`
	}
	if json.Unmarshal([]byte(out), &ls) == nil && len(ls.Blocks) == 0 {
		rexRun("", "kill", session)
	}
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

// rexApp is the name Rex's program status gives an agent.
func rexApp(agent string) string {
	if agent == agentClaude {
		return "claude-code"
	}
	return agent
}

// agentPID is the process that has a session loaded. Claude's registry
// names it; for Codex it's whichever process holds the thread lock.
func agentPID(l *Local, s Session, live Liveness) int {
	if s.Agent == agentCodex {
		return lockHolder(filepath.Join(l.Home, ".codex", "thread-writer-locks", s.ID+".lock"))
	}
	return live.PID
}

// agentBlock finds the pane running app in a Rex session.
func agentBlock(session, app string) (block, state string, err error) {
	out, err := rexRun("", "block", "ls", "-s", session, "--json")
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
		if got, state := programStatus(session, b.BlockID); got == app {
			return b.BlockID, state, nil
		}
	}
	return "", "", fmt.Errorf("no %s pane found in rex session %s", app, session)
}

// programStatus returns the agent app a pane reports and its state, or "".
func programStatus(session, block string) (app, state string) {
	ps, err := rexRun("", "block", "call", "-s", session, "-b", block, "program_status")
	if err != nil {
		return "", ""
	}
	var st struct {
		Records []struct {
			State string `json:"state"`
			App   string `json:"app"`
		} `json:"records"`
	}
	if json.Unmarshal([]byte(ps), &st) != nil {
		return "", ""
	}
	for _, r := range st.Records {
		if r.App == "claude-code" || r.App == "codex" {
			return r.App, r.State
		}
	}
	return "", ""
}

// rexSessionNamed returns the id of the one rex session with this label, or "".
func rexSessionNamed(name string) string {
	if name == "" {
		return ""
	}
	out, err := rexRun("", "ls", "--json")
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
