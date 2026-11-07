#!/usr/bin/env bash
# Prototype 4: does the three-commit snapshot carry a dirty worktree exactly?
#
# Builds a "laptop" worktree with every kind of local state, snapshots it into
# base/index/wip commits without touching its index or tree, pushes only hidden
# refs/ferry/* to a "kant" clone, applies them into a fresh worktree there, and
# compares both sides. Then repeats against an empty directory to cover a repo
# kant has never seen. Local paths stand in for ssh remotes.
#
# usage: test/git-snapshot.sh
set -euo pipefail

root=/tmp/ferry-git
rm -rf "$root"
trap 'rm -rf "$root"' EXIT
mkdir -p "$root"
export GIT_AUTHOR_NAME=ferry GIT_AUTHOR_EMAIL=ferry@test GIT_COMMITTER_NAME=ferry GIT_COMMITTER_EMAIL=ferry@test
g() { git -c commit.gpgsign=false -c init.defaultBranch=main "$@"; }

pass=0 fail=0
check() { if eval "$2"; then echo "  ok    $1"; pass=$((pass + 1)); else echo "  FAIL  $1"; fail=$((fail + 1)); fi; }

# --- the ferry operations under test -------------------------------------

# snapshot DIR TASK: write refs/ferry/TASK/{base,index,wip} in DIR's repo; leaves DIR's index and tree alone
snapshot() {
  local dir=$1 task=$2 base idx wip tmp
  base=$(g -C "$dir" rev-parse HEAD)
  idx=$(g -C "$dir" commit-tree "$(g -C "$dir" write-tree)" -p "$base" -m "ferry index")
  tmp=$(mktemp)
  cp "$(g -C "$dir" rev-parse --git-path index)" "$tmp"
  GIT_INDEX_FILE=$tmp g -C "$dir" add -A
  wip=$(g -C "$dir" commit-tree "$(GIT_INDEX_FILE=$tmp g -C "$dir" write-tree)" -p "$base" -p "$idx" -m "ferry wip")
  rm -f "$tmp"
  g -C "$dir" update-ref "refs/ferry/$task/base" "$base"
  g -C "$dir" update-ref "refs/ferry/$task/index" "$idx"
  g -C "$dir" update-ref "refs/ferry/$task/wip" "$wip"
}

# send DIR REMOTE_REPO TASK: push only the hidden snapshot refs
send() { g -C "$1" push -q -f "$2" "refs/ferry/$3/*:refs/ferry/$3/*"; }

# apply REPO WORKTREE BRANCH TASK: materialize the snapshot in a new worktree
apply() {
  local repo=$1 wt=$2 branch=$3 task=$4
  g -C "$repo" worktree add -q -B "$branch" "$wt" "refs/ferry/$task/base"
  g -C "$wt" restore --source="refs/ferry/$task/wip" --worktree -- :/
  g -C "$wt" read-tree "refs/ferry/$task/index"
}

# fingerprint DIR: index tree, full worktree tree (incl. untracked), porcelain status
fingerprint() {
  local dir=$1 tmp
  tmp=$(mktemp)
  cp "$(g -C "$dir" rev-parse --git-path index)" "$tmp"
  GIT_INDEX_FILE=$tmp g -C "$dir" add -A
  echo "index $(g -C "$dir" write-tree)"
  echo "tree  $(GIT_INDEX_FILE=$tmp g -C "$dir" write-tree)"
  echo "head  $(g -C "$dir" rev-parse HEAD)"
  g -C "$dir" status --porcelain=v1 | sed 's/^/status /'
  rm -f "$tmp"
}

# --- fixture --------------------------------------------------------------

g init -q --bare "$root/origin.git"
g clone -q "$root/origin.git" "$root/laptop" 2>/dev/null
cd "$root/laptop"
printf 'a\n' > keep.txt; printf 'b\n' > gone.txt; printf 'c\n' > old-name.txt
printf 'line1\nline2\n' > partial.txt; printf '#!/bin/sh\n' > tool.sh
printf '.env\nnode_modules/\n' > .gitignore
g add -A && g commit -qm "initial" && g push -q origin main
g worktree add -q -b fix-auth ../laptop-wt/fix-auth
cd ../laptop-wt/fix-auth
printf 'local only\n' > unpushed.txt && g add unpushed.txt && g commit -qm "unpushed commit"
printf 'a staged\n' > keep.txt && g add keep.txt
printf 'line1 staged\nline2\n' > partial.txt && g add partial.txt
printf 'line1 staged\nline2 unstaged\n' > partial.txt
g rm -q gone.txt
g mv old-name.txt new-name.txt
chmod +x tool.sh
ln -s keep.txt link-to-keep
printf 'new\n' > untracked.txt
mkdir -p deep/dir && printf 'x\n' > deep/dir/untracked-nested.txt
printf 'SECRET=1\n' > .env
mkdir -p node_modules/pkg && printf 'dep\n' > node_modules/pkg/index.js
laptop=$PWD

before=$(fingerprint "$laptop")
snapshot "$laptop" fix-auth
base=$(g -C "$laptop" rev-parse refs/ferry/fix-auth/base)
wip=$(g -C "$laptop" rev-parse refs/ferry/fix-auth/wip)

echo "snapshot"
check "source fingerprint unchanged by snapshot" '[[ "$(fingerprint "$laptop")" == "$before" ]]'
check "base is the unpushed branch tip" '[[ "$base" == "$(g -C "$laptop" rev-parse fix-auth)" ]]'
check "ignored .env is not in wip" '! g -C "$laptop" cat-file -e "$wip:.env" 2>/dev/null'
check "ignored node_modules is not in wip" '! g -C "$laptop" cat-file -e "$wip:node_modules" 2>/dev/null'

# --- kant already has a clone ----------------------------------------------

echo "kant has the repo"
g clone -q "$root/origin.git" "$root/kant" 2>/dev/null
send "$laptop" "$root/kant" fix-auth
check "no branch refs pushed" '! g -C "$root/kant" rev-parse -q --verify refs/heads/fix-auth >/dev/null'
apply "$root/kant" "$root/kant-wt/fix-auth" fix-auth fix-auth
after=$(fingerprint "$root/kant-wt/fix-auth")
check "index, worktree, HEAD and status all match" '[[ "$after" == "$before" ]]'
[[ "$after" == "$before" ]] || { diff <(echo "$before") <(echo "$after") | sed 's/^/        /'; } || true
check "exec bit survived" '[[ -x "$root/kant-wt/fix-auth/tool.sh" ]]'
check "symlink survived as a symlink" '[[ -L "$root/kant-wt/fix-auth/link-to-keep" ]]'
check "deleted file stays deleted" '[[ ! -e "$root/kant-wt/fix-auth/gone.txt" ]]'
check "partial staging survived" '[[ "$(g -C "$root/kant-wt/fix-auth" diff --cached --stat -- partial.txt)" && "$(g -C "$root/kant-wt/fix-auth" diff --stat -- partial.txt)" ]]'
check "origin never saw refs/ferry" '[[ -z "$(g -C "$root/origin.git" for-each-ref refs/ferry)" ]]'

# --- round trip back: kant edits, laptop applies ---------------------------

echo "round trip"
printf 'edited on kant\n' >> "$root/kant-wt/fix-auth/untracked.txt"
g -C "$root/kant-wt/fix-auth" commit -qm "kant commit" -- new-name.txt 2>/dev/null || g -C "$root/kant-wt/fix-auth" commit -qm "kant commit"
before2=$(fingerprint "$root/kant-wt/fix-auth")
snapshot "$root/kant-wt/fix-auth" fix-auth
g -C "$laptop" fetch -q -f "$root/kant" "refs/ferry/fix-auth/*:refs/ferry/fix-auth/*"
check "laptop tree unchanged since it sent (safe to overwrite)" '[[ "$(fingerprint "$laptop")" == "$before" ]]'
g -C "$laptop" checkout -q -B fix-auth "refs/ferry/fix-auth/base" 2>/dev/null || g -C "$laptop" reset -q --hard "refs/ferry/fix-auth/base"
g -C "$laptop" clean -qfd -e .env -e node_modules
g -C "$laptop" restore --source="refs/ferry/fix-auth/wip" --worktree -- :/
g -C "$laptop" read-tree "refs/ferry/fix-auth/index"
check "laptop matches kant after the return trip" '[[ "$(fingerprint "$laptop")" == "$before2" ]]'
[[ "$(fingerprint "$laptop")" == "$before2" ]] || { diff <(echo "$before2") <(fingerprint "$laptop") | sed 's/^/        /'; } || true
check "laptop's ignored .env untouched" '[[ "$(cat "$laptop/.env")" == "SECRET=1" ]]'

# --- kant has never seen the repo -----------------------------------------

echo "kant lacks the repo"
# A fresh repo's HEAD names an unborn branch, and git refuses pushes to the
# checked-out branch even when it's unborn. Init on a placeholder, push, then switch.
g init -q --initial-branch=ferry-init "$root/kant2"
g -C "$laptop" push -q "$root/kant2" 'refs/heads/*:refs/heads/*' 'refs/tags/*:refs/tags/*'
g -C "$root/kant2" checkout -q main
g -C "$root/kant2" remote add origin "$(g -C "$laptop" remote get-url origin)"
snapshot "$laptop" fix-auth
before3=$(fingerprint "$laptop")
send "$laptop" "$root/kant2" fix-auth
apply "$root/kant2" "$root/kant2-wt/fix-auth" fix-auth fix-auth
check "new repo: everything matches" '[[ "$(fingerprint "$root/kant2-wt/fix-auth")" == "$before3" ]]'
check "new repo: placeholder branch never created" '! g -C "$root/kant2" rev-parse -q --verify refs/heads/ferry-init >/dev/null'
check "new repo: origin url copied" '[[ "$(g -C "$root/kant2" remote get-url origin)" == "$root/origin.git" ]]'

echo "$pass passed, $fail failed"
(( fail == 0 ))
