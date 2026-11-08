#!/bin/sh
# Drops the lane of every worktree whose directory is gone. Runs at session
# start, after ExitWorktree, and after a subagent stops. WorktreeRemove only
# fires for worktrees a WorktreeCreate hook made, so it can't be the trigger.
# Claude removes a finished subagent's worktree after SubagentStop, so --later
# waits in a detached process before sweeping.

root=${CLAUDE_PROJECT_DIR:-$(pwd)}
[ -x "$root/bin/worktree" ] || exit 0
cat >/dev/null
mkdir -p "$root/tmp"

if [ "$1" = "--later" ]; then
  nohup sh -c 'sleep 30; cd "$1" && bin/worktree teardown --stale' sh "$root" \
    >>"$root/tmp/worktree-sweep.log" 2>&1 </dev/null &
  exit 0
fi

cd "$root" && bin/worktree teardown --stale >>"$root/tmp/worktree-sweep.log" 2>&1
exit 0
