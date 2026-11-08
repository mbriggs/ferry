#!/usr/bin/env bash
# Rechecks what Claude Code does when a worktree session ends, which ferry
# relies on (claudeworktree.go). Drives a real Claude in a throwaway Rex session
# and a scratch repo, on either machine:
#   - /exit with a clean worktree and no commits deletes the worktree.
#   - SIGTERM keeps the worktree, and the transcript stays in its project
#     folder with the worktree-state still set.
#   - After a null worktree-state is appended (what ferry sends), a resume is a
#     plain session: /exit leaves even a dirty worktree, with no prompt.
#   - /exit with changes asks Keep/Remove; Keep keeps the worktree and refiles
#     the transcript with the main checkout, ending in a null worktree-state.
# Rerun after upgrading Claude Code: these are undocumented internals.
# Spends two one-word turns. Accepts the scratch repo's trust prompt.
# usage: test/claude-worktree.sh
set -euo pipefail
source "$(dirname "$0")/lib/lane.sh"

root=$(cd /tmp && pwd -P)/ferry-claude-worktree$FERRY_LANE_SUFFIX
slug() { printf '%s' "$1" | sed 's/[^A-Za-z0-9]/-/g'; }
projects=$HOME/.claude/projects
cleanup() {
  rex kill "$S" >/dev/null 2>&1 || true
  rm -rf "$root" "$projects/$(slug "$root")"*
}
rm -rf "$root"
mkdir -p "$root"
git -C "$root" init -q --initial-branch=main
echo x >"$root/x"
git -C "$root" add -A
git -C "$root" -c user.name=probe -c user.email=probe@localhost commit -qm init

S=$(rex new "ferry-claude-worktree$FERRY_LANE_SUFFIX" -c "$root" --window scratch --json | jq -r .session_id)
trap cleanup EXIT

pass=0 fail=0
check() { if eval "$2"; then echo "  ok    $1"; pass=$((pass + 1)); else echo "  FAIL  $1"; fail=$((fail + 1)); fi; }
# Conditions run in bash with registry exported, so they can call it.
wait_for() { timeout "$1" bash -c "until $2; do sleep 0.5; done" 2>/dev/null; }
screen() { rex capture -s "$S" -b "$1" --trim; }
# registry DIR: the registry file of the Claude whose cwd is DIR.
registry() { grep -l "\"cwd\":\"$1\"" ~/.claude/sessions/*.json 2>/dev/null | head -1; }
export -f registry
# last_state TRANSCRIPT: the last worktree-state's worktreePath, "null", or "none".
last_state() {
  python3 -I -c 'import json, sys
v = "none"
for line in open(sys.argv[1]):
    try: m = json.loads(line)
    except ValueError: continue
    if m.get("type") == "worktree-state":
        v = (m.get("worktreeSession") or {}).get("worktreePath") or "null"
print(v)' "$1"
}
# start NAME: a `claude --worktree NAME` session.
start() {
  wt=$root/.claude/worktrees/$1
  B=$(rex run -s "$S" -c "$root" --label "$1" --focus=false --json -- claude --worktree "$1" | jq -r '.block_ids[0]')
  wait_for 30 "[ -n \"\$(registry $wt)\" ]" || true
  reg=$(registry "$wt")
  pid=$(jq -r .pid "$reg")
  sid=$(jq -r .sessionId "$reg")
}
turn() {
  rex send -s "$S" -b "$B" 'Reply with the single word: ok'
  rex send-key -s "$S" -b "$B" enter
  wait_for 15 "[ \"\$(jq -r .status $reg)\" = busy ]" || true
  wait_for 120 "[ \"\$(jq -r .status $reg)\" = idle ]" || true
}
exit_claude() {
  rex send -s "$S" -b "$B" '/exit'
  rex send-key -s "$S" -b "$B" enter
}

# Trust the scratch repo once, the way you would; --worktree refuses otherwise.
B=$(rex run -s "$S" -c "$root" --label trust --focus=false --json -- claude | jq -r '.block_ids[0]')
wait_for 30 "rex capture -s $S -b $B --trim | grep -qE 'trust this folder|Try \"|for shortcuts'"
if screen "$B" | grep -q 'trust this folder'; then
  rex send-key -s "$S" -b "$B" down
  rex send-key -s "$S" -b "$B" enter
fi
wait_for 30 "[ -n \"\$(registry $root)\" ]" || true
trusted=$(registry "$root")
exit_claude
wait_for 20 "[ ! -e $trusted ]" || true

echo "clean /exit"
start clean
check "claude --worktree registers in the worktree" '[[ -n $reg ]]'
exit_claude
wait_for 20 "! kill -0 $pid 2>/dev/null" || true
check "/exit with no changes deletes the worktree" '[[ ! -d $wt ]]'

echo "SIGTERM, then a resume of what ferry sends"
start term
turn
echo wip >"$wt/wip.txt"
kill -TERM "$pid"
wait_for 20 "! kill -0 $pid 2>/dev/null" || true
transcript=$projects/$(slug "$wt")/$sid.jsonl
check "SIGTERM keeps the worktree" '[[ -d $wt ]]'
check "the transcript stays in the worktree's folder" '[[ -f $transcript ]]'
check "its worktree-state still names the worktree" '[[ $(last_state "$transcript") == "$wt" ]]'
printf '{"type":"worktree-state","worktreeSession":null,"sessionId":"%s"}\n' "$sid" >>"$transcript"
B=$(rex run -s "$S" -c "$wt" --label resume --focus=false --json -- claude --resume "$sid" | jq -r '.block_ids[0]')
wait_for 30 "[ -n \"\$(registry $wt)\" ]" || true
reg=$(registry "$wt")
pid=$(jq -r .pid "$reg")
exit_claude
wait_for 20 "! kill -0 $pid 2>/dev/null" || true
check "after a null worktree-state, /exit doesn't ask" '! kill -0 "$pid" 2>/dev/null'
check "and leaves the dirty worktree" '[[ -f $wt/wip.txt ]]'
kill -TERM "$pid" 2>/dev/null || true

echo "/exit with changes, then Keep"
start keep
turn
echo wip >"$wt/wip.txt"
exit_claude
wait_for 20 "rex capture -s $S -b $B --trim | grep -q 'Keep worktree'" || true
check "/exit with changes asks Keep/Remove" 'screen "$B" | grep -q "Keep worktree"'
rex send-key -s "$S" -b "$B" enter
wait_for 20 "! kill -0 $pid 2>/dev/null" || true
refiled=$projects/$(slug "$root")/$sid.jsonl
check "Keep keeps the worktree" '[[ -d $wt ]]'
check "Keep refiles the transcript with the main checkout" '[[ -f $refiled && ! -f $projects/$(slug "$wt")/$sid.jsonl ]]'
check "the refiled transcript ends outside the worktree" '[[ $(last_state "$refiled") == null ]]'
check "and still names the worktree" 'grep -q "\"worktreePath\":\"$wt\"" "$refiled"'

echo "$pass passed, $fail failed"
[[ $fail -eq 0 ]]
