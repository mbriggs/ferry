#!/bin/sh
# PreToolUse(Bash): provision a linked worktree's lane before its first command.
# The primary checkout and provisioned worktrees cost a few git calls and exit 0.
# A failure blocks this one command, then leaves a marker so later commands run
# and the agent can fix the cause instead of re-running a slow setup each time.
# JSON goes through the system perl's JSON::PP, which bin/worktree's lock
# already needs; python3 may come from mise and be missing from the hook's PATH.

input=$(cat)
cwd=$(printf '%s' "$input" | /usr/bin/perl -MJSON::PP -0777 -ne 'my $j = eval { decode_json($_) } || {}; print $j->{cwd} // ""' 2>/dev/null)
[ -n "$cwd" ] || cwd=$PWD

top=$(git -C "$cwd" rev-parse --show-toplevel 2>/dev/null) || exit 0
[ -f "$top/.git" ] || exit 0                 # only linked worktrees have a .git file
# Only this project's own worktrees: the hook runs the checkout's bin/worktree,
# so a clone or submodule elsewhere that ships one must not get it run.
[ -n "${CLAUDE_PROJECT_DIR:-}" ] || exit 0
common=$(git -C "$CLAUDE_PROJECT_DIR" rev-parse --path-format=absolute --git-common-dir 2>/dev/null) || exit 0
git --git-dir="$common" worktree list --porcelain 2>/dev/null | grep -qxF "worktree $top" || exit 0
[ -x "$top/bin/worktree" ] || exit 0         # a branch from before the lane tooling
[ -f "$top/worktree.env" ] && exit 0         # already provisioned
[ -f "$top/tmp/worktree-setup.failed" ] && exit 0

mkdir -p "$top/tmp"
log="$top/tmp/worktree-setup.log"
if (cd "$top" && bin/worktree setup) >"$log" 2>&1; then
  (cd "$top" && bin/worktree info) | /usr/bin/perl -MJSON::PP -0777 -ne '
print JSON::PP->new->canonical->encode({hookSpecificOutput => {hookEventName => "PreToolUse", additionalContext =>
  "Provisioned this git worktree as its own lane. Use these, not the primary checkout'"'"'s port or databases:\n"
  . $_
  . "When the work here is done, ask the user whether to remove the worktree."}}), "\n"'
  exit 0
fi

cp "$log" "$top/tmp/worktree-setup.failed"
{
  echo "bin/worktree setup failed in $top. Read tmp/worktree-setup.failed, fix the cause, then run bin/worktree setup."
  tail -n 20 "$log"
} >&2
exit 2
