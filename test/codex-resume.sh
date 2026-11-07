#!/usr/bin/env bash
# Prototype 2: does a Codex session resume on a machine that has only the rollout file?
#
# Simulates the receiving machine with an empty CODEX_HOME under /tmp/ferry-test:
# no state_5.sqlite, no thread_history_1.sqlite, just the rewritten rollout and
# a symlink to auth.json (a symlink, so credentials are never copied). Resumes
# with `codex exec resume`, then reports what Codex built from the file. The
# real ~/.codex is only read. Everything under /tmp/ferry-test is removed on exit.
#
# usage: test/codex-resume.sh SESSION_ID
set -euo pipefail

id=${1:?usage: codex-resume.sh SESSION_ID}
here=$(cd "$(dirname "$0")" && pwd)
rewrite=(python3 -I "$here/lib/rewrite_home.py")

src=$(find "$HOME/.codex/sessions" -name "rollout-*-$id.jsonl" -print -quit)
[[ -n "$src" ]] || { echo "no rollout for $id" >&2; exit 1; }
rel=${src#"$HOME/.codex/"}

old_cwd=$(jq -rn 'first(inputs | select(.type == "session_meta") | .payload.cwd)' "$src")
fake_home=/tmp/ferry-test/fakehome
new_cwd="$fake_home${old_cwd#"$HOME"}"
codex_home=/tmp/ferry-test/codex-home
dst="$codex_home/$rel"

cleanup() { rm -rf /tmp/ferry-test; }
trap cleanup EXIT
cleanup

orig_before=$(sha256sum "$src" | cut -c1-16)
mkdir -p "$new_cwd" "$(dirname "$dst")"
chmod 700 "$codex_home"
ln -s "$HOME/.codex/auth.json" "$codex_home/auth.json"

echo "session   $id"
echo "move      $old_cwd -> $new_cwd"
echo "rewrote   $("${rewrite[@]}" "$HOME" "$fake_home" "$src" "$dst") values in $rel"
lines_before=$(wc -l < "$dst")

echo "--- codex exec resume (CODEX_HOME=$codex_home, cwd $new_cwd)"
(cd "$new_cwd" && CODEX_HOME="$codex_home" codex exec --skip-git-repo-check resume "$id" \
  "Answer in two short lines. 1: what this session was about. 2: your current working directory." 2>&1 | tail -15)
echo "---"

echo "rollouts  $(find "$codex_home/sessions" -name '*.jsonl' | sed "s#$codex_home/##" | tr '\n' ' ')"
echo "copy      $lines_before -> $(wc -l < "$dst") lines"
if [[ -f "$codex_home/state_5.sqlite" ]]; then
  echo "threads   $(sqlite3 -readonly "file:$codex_home/state_5.sqlite?mode=ro" "select id || ' cwd=' || cwd || ' rollout=' || rollout_path from threads")"
else
  echo "threads   no state_5.sqlite created"
fi
if [[ -f "$codex_home/thread_history_1.sqlite" ]]; then
  echo "history   $(sqlite3 -readonly "file:$codex_home/thread_history_1.sqlite?mode=ro" "select count(*) from thread_items") items projected"
fi
echo "cwd seen  $(jq -r 'select(.type == "turn_context") | .payload.cwd' "$dst" | sort -u | tr '\n' ' ')"
[[ "$orig_before" == "$(sha256sum "$src" | cut -c1-16)" ]] && echo "original  unchanged" || echo "original  CHANGED"
