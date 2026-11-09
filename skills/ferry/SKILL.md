---
name: ferry
description: Hand this Claude session and its git worktree to another machine with ferry. Use when the user says "ferry this to kant", "hand this off", or "move this session to kant".
---

# Ferry this session

`ferry to HOST` moves the git worktree this session is working in, plus this conversation, to HOST and resumes it there. It refuses while this session is live, so this session has to stop first and ferry has to run after that. A Rex tab does both: it waits for this reply to end, stops this session with SIGTERM, then runs `ferry to HOST` with you watching its prompts.

Don't use `/exit` and don't call `ExitWorktree` in this flow. If this session made its worktree (`claude --worktree`, or `EnterWorktree`), `/exit` deletes it when it has no changes and no commits. SIGTERM leaves the worktree in place, and ferry sends this session so that it resumes as a plain one.

The laptop drives every handoff, in both directions, and only the laptop has `~/.config/ferry/hosts`. If that file is missing, or doesn't name HOST, this session runs on kant or another machine that doesn't drive handoffs. Stop, and tell the user to run `ferry from THIS-MACHINE TASK` on the laptop, with THIS-MACHINE from `cat ~/.config/ferry/name` (or `hostname -s`) and TASK the worktree's folder name. Don't try other host names, and don't suggest changing ssh or Remote Login settings.

Claude Code's worktree isolation refuses a `rex run` whose arguments come from shell variables. So read the values first with `printenv REX_SESSION CLAUDE_PID CLAUDE_CODE_SESSION_ID`, and type them into the commands below as literal text. Below, SESSION, PID and ID stand for those three values, and WORKTREE for the worktree's path.

1. Find the worktree: `git rev-parse --show-toplevel`. It must be under `~/src`. HOST is the machine the user named, usually kant.
2. Check, and stop to tell the user if any check fails:
   - `command -v ferry` and `command -v rex` succeed.
   - `printenv REX_SESSION CLAUDE_PID CLAUDE_CODE_SESSION_ID` prints all three.
   - `~/.claude/sessions/PID.json` names ID.
   - `ferry to HOST --session ID --check`, run in the worktree, succeeds. It runs every check that can refuse the handoff (ssh, ferry and agent versions, rsync on both machines, the task's owner) and stops before it stops anything. If it reports that HOST has work the handoff would overwrite, tell the user: the tab will offer to save that work to a branch on HOST first.
3. Finish or stop your own background work (background Bash, Monitors, subagents). SIGTERM ends all of it. Uncommitted changes are fine; ferry moves them.
4. Open the tab, on one line, with HOME as your home directory spelled out:

   ```
   rex run -s SESSION -c WORKTREE --label "ferry to HOST" --keep-open --focus=false -- sh HOME/.claude/skills/ferry/after-exit.sh HOST PID ID WORKTREE
   ```

5. End your turn at once, with one line: "A 'ferry to HOST' tab stops this session when this reply ends, then hands it off; its prompts are in that tab." Run no more tools.

The tab runs the same `--check` first, in its own environment, which can differ from yours (a login shell without `~/.zshrc`, so a different PATH). If that check fails, the tab stops there and this session keeps running. Otherwise the tab waits until this session has been idle for 3 seconds. If the user types again first, the session goes busy and the tab gives up without stopping anything. Run this skill again when they're ready.

If the user would rather do it by hand: close this pane (or `/exit` and pick Keep, when Claude asks), then run `ferry to HOST` in the worktree. In the repo's main checkout, `ferry to HOST` lists the worktrees that have a session and asks which one to send; `ferry to HOST NAME` names one.
