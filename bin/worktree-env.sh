# shellcheck shell=sh
# The lane loader and guard for shell entry points. Copy to bin/worktree-env.sh
# unchanged. POSIX sh, so bash 3.2 and dash both read it.
#
#   . "$(dirname "$0")/worktree-env.sh"
#   worktree_load DIR    load the worktree.env of DIR's checkout, or refuse to
#                        start in a linked worktree that has none
#
# Call it before anything that touches what a lane isolates. Without it, a
# linked worktree whose setup failed runs against the primary's database.
#
# worktree.env values override what the process inherited, except when the
# inherited WORKTREE_LANE already equals the file's. Then a parent loaded this
# lane on purpose, like a test script that chose scratch databases, and its
# values stand. In the primary, worktree_load does nothing.

worktree_load() {
  wl_top=$(git -C "$1" rev-parse --show-toplevel 2>/dev/null) || return 0
  wl_top=$(cd "$wl_top" && pwd -P)
  # A linked worktree's .git is a file.
  [ -f "$wl_top/.git" ] || { unset wl_top; return 0; }
  if [ ! -f "$wl_top/worktree.env" ]; then
    # bin/worktree setup exports this while lane_provision runs, so the entry
    # points that provisioning calls can start before worktree.env exists.
    if [ "${WORKTREE_PROVISIONING:-}" = "$wl_top" ]; then unset wl_top; return 0; fi
    echo "$wl_top is a linked worktree with no lane, so it would use the primary checkout's resources. Run bin/worktree setup there first." >&2
    if [ -f "$wl_top/tmp/worktree-setup.failed" ]; then
      echo "The last setup failed; see $wl_top/tmp/worktree-setup.failed." >&2
    fi
    exit 1
  fi
  # Parse, never source: only lines of the form KEY=VALUE that bin/worktree
  # could have written count, so a worktree.env committed on a branch can't
  # run code.
  wl_lane=$(sed -n 's/^WORKTREE_LANE=//p' "$wl_top/worktree.env")
  wl_keep=
  if [ -n "${WORKTREE_LANE:-}" ] && [ "$WORKTREE_LANE" = "$wl_lane" ]; then wl_keep=1; fi
  while IFS= read -r wl_l; do
    case $wl_l in [A-Z_]*=*) ;; *) continue ;; esac
    wl_k=${wl_l%%=*} wl_v=${wl_l#*=}
    case $wl_k in *[!A-Z0-9_]*) continue ;; esac
    case $wl_v in *[!A-Za-z0-9_./:@%+,=-]*) continue ;; esac
    # Same lane as the parent: keep what the parent set.
    if [ -n "$wl_keep" ]; then eval "wl_set=\${$wl_k+x}"; [ -z "$wl_set" ] || continue; fi
    export "$wl_k=$wl_v"
  done < "$wl_top/worktree.env"
  unset wl_top wl_lane wl_keep wl_l wl_k wl_v wl_set
}
