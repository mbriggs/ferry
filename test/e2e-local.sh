#!/usr/bin/env bash
# End-to-end on one machine: a fake "laptop" home under /tmp/ferry-test-e2e
# hands a real Claude session to this machine's real home, a real agent
# resumes it in Rex and does work, and ferry brings it back.
#
# It writes a throwaway ~/src/ferry-e2e-probe, answers that folder's trust
# prompt (leaving a trust entry in ~/.claude.json), and starts a Rex session
# named ferry-e2e-probe (each name ends in -lane-<n> in a lane). Run `test/e2e-local.sh clean` to remove them.
#
# usage: test/e2e-local.sh [clean]
set -euo pipefail
source "$(dirname "$0")/lib/lane.sh"

here=$(cd "$(dirname "$0")/.." && pwd)
ferry="$here/ferry"
root=/tmp/ferry-test-e2e$FERRY_LANE_SUFFIX
probe=ferry-e2e-probe$FERRY_LANE_SUFFIX
lh=$root/laptop
wt=$lh/src/$probe
task=src-$probe  # record files are $task-<hash>.json
record() { ls "$HOME"/.local/state/ferry/tasks/$task-*.json 2>/dev/null | head -1; }

clean() {
  local rs
  rs=$(jq -r '.rex_session // empty' "$(record)" 2>/dev/null || true)
  rex ls 2>/dev/null | awk -v l="$probe" '$1 == l {print $2}' | while read -r s; do rex kill "$s" >/dev/null 2>&1 || true; done
  [[ -n "$rs" ]] && rex kill "$rs" >/dev/null 2>&1 || true
  rm -rf "$root" "$HOME/src/$probe" "$HOME"/.local/state/ferry/tasks/$task-*.json
  rm -rf "$HOME/.local/state/ferry/parked/$task"-* "$HOME/.claude/projects/-home-${USER}-src-$probe"
}

if [[ "${1:-}" == clean ]]; then clean; echo cleaned; exit 0; fi
clean

(cd "$here" && go build -o ferry .)
mkdir -p "$lh/.config/ferry" "$wt"
echo laptop > "$lh/.config/ferry/name"
echo "kant exec:$HOME" > "$lh/.config/ferry/hosts"
printf '{"projects":{"%s":{"hasTrustDialogAccepted":true}}}\n' "$wt" > "$lh/.claude.json"
git -C "$wt" init -q -b main
echo hi > "$wt/README.md"
git -C "$wt" add -A
git -C "$wt" -c commit.gpgsign=false -c user.name=t -c user.email=t@t commit -qm init
echo "uncommitted work" > "$wt/notes.txt"

echo "== create a real session on the fake laptop"
(cd "$wt" && claude -p "Remember the codeword HERON for later in this session. Reply with just: ok" | tail -1)
slug=$(printf '%s' "$wt" | sed 's#[^A-Za-z0-9]#-#g')
transcript=$(ls -t "$HOME/.claude/projects/$slug/"*.jsonl | head -1)
id=$(basename "$transcript" .jsonl)
mkdir -p "$lh/.claude/projects/$slug"
mv "$transcript" "$lh/.claude/projects/$slug/"
rmdir "$HOME/.claude/projects/$slug" 2>/dev/null || true

echo "== ferry to kant"
(cd "$wt" && HOME=$lh "$ferry" to kant --yes)

rs=$(jq -r .rex_session "$(record)")
block=$(rex block ls -s "$rs" --json | jq -r '.blocks[0].block_id')
reg() { grep -l "$id" "$HOME"/.claude/sessions/*.json 2>/dev/null | head -1; }
timeout 90 sh -c "until f=\$(grep -l $id $HOME/.claude/sessions/*.json 2>/dev/null) && [ \"\$(jq -r .status \$f)\" = idle ]; do sleep 1; done"

echo "== ask the resumed agent to work on kant"
rex send -s "$rs" -b "$block" 'What was the codeword? Then create a file kant-was-here.txt containing that word.'
rex send-key -s "$rs" -b "$block" enter
timeout 10 sh -c "until [ \"\$(jq -r .status $(reg))\" = busy ]; do sleep 0.5; done" || true
timeout 180 sh -c "until [ \"\$(jq -r .status $(reg))\" = idle ]; do sleep 1; done"
got=$(cat "$HOME/src/$probe/kant-was-here.txt")
echo "kant-was-here.txt: $got"

echo "== ferry from kant"
(cd /tmp && HOME=$lh "$ferry" from kant "$probe" --yes)

fail=0
[[ "$got" == *HERON* ]] || { echo "FAIL: resumed agent didn't recall the codeword"; fail=1; }
[[ "$(cat "$wt/kant-was-here.txt")" == *HERON* ]] || { echo "FAIL: kant's work didn't come back"; fail=1; }
back="$lh/.claude/projects/$slug/$id.jsonl"
grep -q "kant-was-here" "$back" || { echo "FAIL: transcript didn't come back with kant's turns"; fail=1; }
! grep -q "\"cwd\":\"$HOME/" "$back" || { echo "FAIL: transcript still has kant cwds"; fail=1; }
[[ -z "$(reg)" ]] || { echo "FAIL: agent still running on kant"; fail=1; }
(( fail == 0 )) && echo "e2e passed (run '$0 clean' to remove the probe)"
exit $fail
