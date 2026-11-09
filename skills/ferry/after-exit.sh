#!/bin/sh
# after-exit.sh HOST PID SESSION WORKTREE: wait for the Claude session to sit
# idle, stop it with SIGTERM, then `ferry to HOST` the worktree. SIGTERM skips
# Claude's worktree exit, which deletes a worktree with no changes and refiles
# the transcript on Keep. Runs in its own Rex tab, started by the ferry skill.
set -u
host=$1 pid=$2 sid=$3 wt=$4
reg=$HOME/.claude/sessions/$pid.json

status() {
  python3 -I -c 'import json, sys
d = json.load(open(sys.argv[1]))
print(d.get("status", "") if d.get("sessionId") == sys.argv[2] else "other")' "$reg" "$sid" 2>/dev/null
}

# Check first, in this tab's environment (its PATH, its rsync), while the
# session still runs: a refusal now costs nothing.
cd "$wt" || exit 1
if ! ferry to "$host" --session "$sid" --check; then
  echo
  echo "Nothing was stopped; Claude keeps running. Fix the cause, then ask it to ferry again."
  exit 1
fi
echo
echo "Waiting for Claude (pid $pid) to finish its reply. Ctrl-C cancels."
idle=0 seen_idle=0
while :; do
  if ! kill -0 "$pid" 2>/dev/null; then
    echo "Claude exited on its own. Run: cd $wt && ferry to $host --session $sid"
    exit 1
  fi
  case $(status) in
  idle) idle=$((idle + 1)) seen_idle=1 ;;
  busy)
    if [ "$seen_idle" = 1 ]; then
      echo "The session started a new turn; not stopping it."
      exit 1
    fi
    idle=0
    ;;
  *)
    echo "pid $pid no longer runs session $sid; not stopping it."
    exit 1
    ;;
  esac
  [ "$idle" -ge 3 ] && break
  sleep 1
done

# REX_BLOCK is this tab's own block: bring it forward for ferry's prompts.
rex block focus "$REX_BLOCK" >/dev/null 2>&1
kill -TERM "$pid"
i=0
while kill -0 "$pid" 2>/dev/null && [ "$i" -lt 30 ]; do
  sleep 0.5
  i=$((i + 1))
done
if kill -0 "$pid" 2>/dev/null; then
  echo "Claude didn't exit after SIGTERM. Close its pane (don't /exit), then run: cd $wt && ferry to $host --session $sid"
  exit 1
fi

ferry to "$host" --session "$sid" && exit 0
echo
echo "ferry to $host failed. Fix the cause, then run: cd $wt && ferry to $host --session $sid"
echo "Or keep working here: cd $wt && claude --resume $sid"
exit 1
