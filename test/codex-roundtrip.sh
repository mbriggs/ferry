#!/usr/bin/env bash
# Prototype 2b: after a round trip, does Codex use the returned rollout or its own stale SQLite copy?
#
# Two empty CODEX_HOMEs, A and B, stand in for kant and the laptop:
#   1. A resumes the session and is told the word PELICAN.   (A's SQLite now knows the thread)
#   2. The rollout moves to B, which is told OTTER.
#   3. The rollout moves back to A. A is asked for both words.
# If step 3 only recalls PELICAN, A trusted its stale projection over the file.
#
# usage: test/codex-roundtrip.sh SESSION_ID
set -euo pipefail
source "$(dirname "$0")/lib/lane.sh"

id=${1:?usage: codex-roundtrip.sh SESSION_ID}
here=$(cd "$(dirname "$0")" && pwd)
rewrite=(python3 -I "$here/lib/rewrite_home.py")

src=$(find "$HOME/.codex/sessions" -name "rollout-*-$id.jsonl" -print -quit)
[[ -n "$src" ]] || { echo "no rollout for $id" >&2; exit 1; }
rel=${src#"$HOME/.codex/"}
old_cwd=$(jq -rn 'first(inputs | select(.type == "session_meta") | .payload.cwd)' "$src")
model=$(jq -rn 'first(inputs | select(.type == "turn_context") | .payload.model)' "$src")

root=/tmp/ferry-test$FERRY_LANE_SUFFIX
cleanup() { rm -rf "$root"; }
trap cleanup EXIT
cleanup

setup_home() {
  local name=$1
  mkdir -p "$root/$name/codex" "$root/$name/home${old_cwd#"$HOME"}"
  chmod 700 "$root/$name/codex"
  ln -s "$HOME/.codex/auth.json" "$root/$name/codex/auth.json"
}

# move_rollout LABEL FROM_FILE FROM_HOME TO_NAME: copy a rollout into TO_NAME, rewriting FROM_HOME to TO_NAME's home
move_rollout() {
  local label=$1 from=$2 from_home=$3 to=$4
  local dst="$root/$to/codex/$rel"
  mkdir -p "$(dirname "$dst")"
  echo "move      $label -> $to, rewrote $("${rewrite[@]}" "$from_home" "$root/$to/home" "$from" "$dst") values"
}

ask() {
  local name=$1 prompt=$2
  echo "--- $name: $prompt"
  (cd "$root/$name/home${old_cwd#"$HOME"}" &&
    CODEX_HOME="$root/$name/codex" codex exec --skip-git-repo-check -c model="$model" resume "$id" "$prompt" 2>/dev/null | tail -3)
  echo "          $name history items: $(sqlite3 -readonly "file:$root/$name/codex/thread_history_1.sqlite?mode=ro" "select count(*) from thread_items where thread_id='$id'")"
}

setup_home A
setup_home B
echo "session   $id (model $model)"

move_rollout real "$src" "$HOME" A
ask A "Remember the word PELICAN. Reply with just: ok"

move_rollout A "$root/A/codex/$rel" "$root/A/home" B
ask B "Also remember the word OTTER. Reply with just: ok"

move_rollout B "$root/B/codex/$rel" "$root/B/home" A
ask A "List every word I asked you to remember in this session, comma separated, nothing else."
