#!/bin/sh
# SessionStart and CwdChanged: make every Bash command load the worktree.env of
# the checkout it runs in. Claude Code captures the shell environment once per
# session, and EnterWorktree and ExitWorktree don't fire CwdChanged, so this
# writes one line that resolves the checkout per command instead of baking in a
# path or values. Subagent shells source the same file. Only linked worktrees
# have a worktree.env, so the primary keeps its own values.
#
# The captured PATH can also hold paths inside the primary, like its bin/ or
# node_modules/.bin (from mise, direnv, or a shell rc), which would run the
# primary's scripts from inside a worktree. The line rewrites each such entry
# to the same path inside the worktree.

cat >/dev/null
[ -n "$CLAUDE_ENV_FILE" ] || exit 0
grep -q 'worktree-lane-env' "$CLAUDE_ENV_FILE" 2>/dev/null && exit 0
cat >>"$CLAUDE_ENV_FILE" <<'LINE'
wl_top=$(git rev-parse --show-toplevel 2>/dev/null); if [ -n "$wl_top" ] && [ -f "$wl_top/worktree.env" ]; then set -a; . "$wl_top/worktree.env"; set +a; wl_primary=$(dirname "$(git rev-parse --path-format=absolute --git-common-dir)"); PATH=$(printf '%s' "$PATH" | awk -v RS=: -v ORS=: -v from="$wl_primary" -v to="$wl_top" '{ inlane = $0 == to || index($0, to "/") == 1; inprimary = $0 == from || index($0, from "/") == 1; print (inprimary && !inlane ? to substr($0, length(from) + 1) : $0) }' | sed 's/:$//'); export PATH; fi; unset wl_top wl_primary # worktree-lane-env
LINE
exit 0
