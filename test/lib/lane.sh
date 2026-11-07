# shellcheck shell=bash
# Sourced first by every script in test/ that creates files, directories, or
# Rex sessions under fixed names. In a linked worktree it loads worktree.env, so
# FERRY_LANE_SUFFIX (-lane-<n>) gives this lane its own names; in the primary
# checkout the suffix is empty and the names stay as they were.
#
# A linked worktree without worktree.env hasn't been provisioned, and its
# scripts would collide with the primary's, so they refuse to start.
# When WORKTREE_LANE already matches the file, a parent process loaded this
# lane on purpose, and its values stand.
lane_top=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
if [[ -f $lane_top/.git ]]; then
  if [[ ! -f $lane_top/worktree.env ]]; then
    echo "$0: this git worktree has no lane yet. Run bin/worktree setup in $lane_top first." >&2
    exit 1
  fi
  if [[ ${WORKTREE_LANE:-} != "$(sed -n 's/^WORKTREE_LANE=//p' "$lane_top/worktree.env")" ]]; then
    set -a
    # shellcheck disable=SC1091
    . "$lane_top/worktree.env"
    set +a
  fi
fi
: "${FERRY_LANE_SUFFIX:=}"
unset lane_top
