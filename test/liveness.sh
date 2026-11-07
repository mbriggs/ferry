#!/usr/bin/env bash
# Prototype 3: can ferry tell whether an agent session is live, and stop it cleanly?
#
# Drives a real Claude Code and a real Codex TUI in a throwaway Rex session and
# checks the signals ferry relies on:
#   Claude: ~/.claude/sessions/<pid>.json status flips idle -> busy -> idle,
#           and the file disappears after /exit.
#   Codex:  ~/.codex/thread-writer-locks/<thread>.lock is flock'd while the
#           session is loaded; with --no-daemon the TUI holds it and releases
#           it on exit.
#   Both:   Rex program_status reports working/idle/done for the pane.
# Rerun after agent upgrades: these are undocumented internals.
#
# Uses scratch repos under /tmp/ferry-live and accepts their trust prompts.
# usage: test/liveness.sh
set -euo pipefail
source "$(dirname "$0")/lib/lane.sh"

root=/tmp/ferry-live$FERRY_LANE_SUFFIX
mkdir -p "$root/claude" "$root/codex"
for d in "$root/claude" "$root/codex"; do [[ -d "$d/.git" ]] || git -C "$d" init -q; done

S=$(rex new "ferry-liveness-test$FERRY_LANE_SUFFIX" -c "$root" --window scratch --json | jq -r .session_id)
cleanup() { rex kill "$S" >/dev/null 2>&1 || true; }
trap cleanup EXIT

pass=0 fail=0
check() { if eval "$2"; then echo "  ok    $1"; pass=$((pass + 1)); else echo "  FAIL  $1"; fail=$((fail + 1)); fi; }
wait_for() { timeout "$1" sh -c "until $2; do sleep 0.5; done" 2>/dev/null; }
rex_state() { rex block call -s "$S" -b "$1" program_status | jq -r '[.records[].state] | join(",")'; }
screen() { rex capture -s "$S" -b "$1" --trim; }
lock_holder() { awk -v i="$(stat -c %i "$1")" '{split($6, a, ":"); if (a[3] == i) print $5}' /proc/locks | head -1; }

echo "claude"
B=$(rex run -s "$S" -c "$root/claude" --label claude --focus=false --json -- claude | jq -r '.block_ids[0]')
wait_for 30 "rex capture -s $S -b $B --trim | grep -qE 'trust this folder|Try \"'"
if screen "$B" | grep -q 'trust this folder'; then rex send-key -s "$S" -b "$B" down; rex send-key -s "$S" -b "$B" enter; fi
wait_for 30 "grep -lq '\"cwd\":\"$root/claude\"' ~/.claude/sessions/*.json"
reg=$(grep -l "\"cwd\":\"$root/claude\"" ~/.claude/sessions/*.json | head -1)
pid=$(jq -r .pid "$reg")
check "registry entry appears, status idle" '[[ $(jq -r .status "$reg") == idle ]]'
rex send -s "$S" -b "$B" 'Write a 200 word story about a ferry, then stop.'
rex send-key -s "$S" -b "$B" enter
wait_for 10 "[ \"\$(jq -r .status $reg)\" = busy ]" || true
check "registry flips to busy mid-turn" '[[ $(jq -r .status "$reg") == busy ]]'
check "rex program_status says working" '[[ $(rex_state "$B") == *working* ]]'
wait_for 120 "[ \"\$(jq -r .status $reg)\" = idle ]" || true
check "registry returns to idle after the turn" '[[ $(jq -r .status "$reg") == idle ]]'
rex send -s "$S" -b "$B" '/exit'
rex send-key -s "$S" -b "$B" enter
wait_for 20 "! kill -0 $pid 2>/dev/null" || true
check "/exit ends the process" '! kill -0 "$pid" 2>/dev/null'
check "/exit removes the registry entry" '[[ ! -e "$reg" ]]'

echo "codex (--no-daemon)"
B=$(rex run -s "$S" -c "$root/codex" --label codex --focus=false --json -- codex --no-daemon | jq -r '.block_ids[0]')
wait_for 30 "rex capture -s $S -b $B --trim | grep -qE 'Trust this folder|Ask Codex'"
if screen "$B" | grep -q 'Trust this folder'; then rex send-key -s "$S" -b "$B" enter; fi
wait_for 20 "rex capture -s $S -b $B --trim | grep -q 'Ask Codex'"
marker=$(mktemp)
rex send -s "$S" -b "$B" 'Write a 200 word story about a river barge, then stop.'
rex send-key -s "$S" -b "$B" enter
rex send-key -s "$S" -b "$B" enter   # Codex leaves pasted text in the composer until a second Enter
wait_for 30 "find ~/.codex/thread-writer-locks -name '*.lock' -newer $marker | grep -q ."
lock=$(find ~/.codex/thread-writer-locks -name '*.lock' -newer "$marker" | head -1)
rm -f "$marker"
tui=$(rex block call -s "$S" -b "$B" program_status | jq -r '.records[0].owner.pid')
check "a thread lock file appears" '[[ -n "$lock" ]]'
check "lock is held while live" '! flock -n -s "$lock" true'
check "the TUI itself holds the lock" '[[ $(lock_holder "$lock") == "$tui" ]]'
wait_for 15 "rex block call -s $S -b $B program_status | jq -e '[.records[].state] | index(\"working\")' >/dev/null" || true
wait_for 120 "! rex block call -s $S -b $B program_status | jq -e '[.records[].state] | index(\"working\")' >/dev/null" || true
for _ in 1 2 3; do   # Codex can drop input that lands as a turn finishes
  rex send -s "$S" -b "$B" '/exit'
  rex send-key -s "$S" -b "$B" enter
  wait_for 5 "! kill -0 $tui 2>/dev/null" && break
done
kill -0 "$tui" 2>/dev/null && { echo "  (codex screen at failure:)"; screen "$B" | grep -v '^\s*$' | tail -6 | sed 's/^/        /'; }
check "/exit ends the TUI" '! kill -0 "$tui" 2>/dev/null'
check "lock is free right after exit" 'flock -n -s "$lock" true'

echo "$pass passed, $fail failed"
(( fail == 0 ))
