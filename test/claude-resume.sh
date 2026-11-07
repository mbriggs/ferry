#!/usr/bin/env bash
# Prototype 1: does a Claude Code session survive a move to a different home?
#
# Copies one session (transcript + subagent dir) into a fake home under
# /tmp/ferry-test, rewrites home-prefixed paths, resumes it there with
# `claude -p --resume`, and checks what changed. The original is never
# written; the copy is removed on exit.
#
# Limits of running on one machine: the copy shares its session id and
# ~/.claude/file-history/<id>/ with the original, so this can't show that
# file history arrives intact, only that its paths get rewritten.
#
# usage: test/claude-resume.sh SESSION_ID
set -euo pipefail

id=${1:?usage: claude-resume.sh SESSION_ID}
here=$(cd "$(dirname "$0")" && pwd)
rewrite=(python3 -I "$here/lib/rewrite_home.py")

src=$(find "$HOME/.claude/projects" -maxdepth 2 -name "$id.jsonl" -not -path '*-tmp-ferry-test-*' -print -quit)
[[ -n "$src" ]] || { echo "no transcript for $id" >&2; exit 1; }
src_dir=$(dirname "$src")

old_cwd=$(jq -rn 'first(inputs | select(.cwd != null) | .cwd)' "$src")
fake_home=/tmp/ferry-test/fakehome
new_cwd="$fake_home${old_cwd#"$HOME"}"
slug=$(printf '%s' "$new_cwd" | sed 's#[^A-Za-z0-9]#-#g')
dst_dir="$HOME/.claude/projects/$slug"
dst="$dst_dir/$id.jsonl"

case "$slug" in -tmp-ferry-test-*) ;; *) echo "refusing slug $slug" >&2; exit 1 ;; esac

cleanup() { rm -rf "$dst_dir" /tmp/ferry-test; }
trap cleanup EXIT
cleanup

sum() { find "$@" -type f -exec sha256sum {} + | sort | sha256sum | cut -c1-16; }
orig_before=$(sum "$src" "$src_dir/$id" 2>/dev/null || sum "$src")

mkdir -p "$new_cwd" "$dst_dir"
echo "session   $id"
echo "move      $old_cwd -> $new_cwd"
echo "slug      $slug"
echo "rewrote   transcript: $("${rewrite[@]}" "$HOME" "$fake_home" "$src" "$dst") values"

if [[ -d "$src_dir/$id" ]]; then
  find "$src_dir/$id" -type f | while read -r f; do
    rel=${f#"$src_dir/$id/"}
    mkdir -p "$dst_dir/$id/$(dirname "$rel")"
    case "$f" in
      *.jsonl|*.json) echo "rewrote   $rel: $("${rewrite[@]}" "$HOME" "$fake_home" "$f" "$dst_dir/$id/$rel") values" ;;
      *) cp -p "$f" "$dst_dir/$id/$rel" ;;
    esac
  done
fi

echo "leftover  $(grep -o "\"$HOME/" "$dst" | wc -l) quoted old-home paths, $(grep -c "$HOME" "$dst" || true) lines mention old home in any form"
lines_before=$(wc -l < "$dst")
types_before=$(jq -r .type "$dst" | sort | uniq -c | awk '{print $2, $1}')

echo "--- claude -p --resume (from $new_cwd)"
(cd "$new_cwd" && claude -p --resume "$id" \
  "Answer in three short lines. 1: what this session was about. 2: whether you used a subagent and what it did. 3: your current working directory.")
echo "---"

lines_after=$(wc -l < "$dst")
echo "copy      $lines_before -> $lines_after lines; entries added by the resume:"
join -a2 -e0 -o 0,1.2,2.2 <(echo "$types_before") <(jq -r .type "$dst" | sort | uniq -c | awk '{print $2, $1}') \
  | awk '$3 > $2 {printf "          +%d %s\n", $3 - $2, $1}'
echo "cwd seen  $(tail -n $((lines_after - lines_before)) "$dst" | jq -r 'select(.cwd != null) | .cwd' | sort -u | tr '\n' ' ')"

orig_after=$(sum "$src" "$src_dir/$id" 2>/dev/null || sum "$src")
[[ "$orig_before" == "$orig_after" ]] && echo "original  unchanged" || echo "original  CHANGED"
