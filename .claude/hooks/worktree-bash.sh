#!/bin/sh
# PreToolUse(Bash): provision a linked worktree's lane before its first command.
# The primary checkout and provisioned worktrees cost one git call and exit 0.
# A failure blocks this one command, then leaves a marker so later commands run
# and the agent can fix the cause instead of re-running a slow setup each time.

input=$(cat)
cwd=$(printf '%s' "$input" | python3 -I -c 'import json, sys; print(json.load(sys.stdin).get("cwd", ""))' 2>/dev/null)
[ -n "$cwd" ] || cwd=$PWD

top=$(git -C "$cwd" rev-parse --show-toplevel 2>/dev/null) || exit 0
[ -f "$top/.git" ] || exit 0                 # only linked worktrees have a .git file
[ -x "$top/bin/worktree" ] || exit 0         # a branch from before the lane tooling
[ -f "$top/worktree.env" ] && exit 0         # already provisioned
[ -f "$top/tmp/worktree-setup.failed" ] && exit 0

mkdir -p "$top/tmp"
log="$top/tmp/worktree-setup.log"
if (cd "$top" && bin/worktree setup) >"$log" 2>&1; then
  (cd "$top" && bin/worktree info) | python3 -I -c '
import json, sys
print(json.dumps({"hookSpecificOutput": {"hookEventName": "PreToolUse", "additionalContext":
  "Provisioned this git worktree as its own lane. Use these, not the primary checkout'"'"'s port or databases:\n"
  + sys.stdin.read()
  + "When the work here is done, ask the user whether to remove the worktree."}}))'
  exit 0
fi

cp "$log" "$top/tmp/worktree-setup.failed"
{
  echo "bin/worktree setup failed in $top. Read tmp/worktree-setup.failed, fix the cause, then run bin/worktree setup."
  tail -n 20 "$log"
} >&2
exit 2
