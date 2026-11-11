# shellcheck shell=bash
# Sourced first by every script in test/ that creates files, directories, or
# Rex sessions under fixed names. It loads the lane through the shared loader,
# bin/worktree-env.sh, so in a linked worktree FERRY_LANE_SUFFIX (-lane-<n>)
# gives this lane its own names, and a worktree with no lane refuses to start.
# In the primary checkout the suffix is empty and the names stay as they were.
# shellcheck disable=SC1091
. "$(dirname "${BASH_SOURCE[0]}")/../../bin/worktree-env.sh"
worktree_load "$(dirname "${BASH_SOURCE[0]}")"
: "${FERRY_LANE_SUFFIX:=}"
