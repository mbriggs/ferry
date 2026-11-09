#!/usr/bin/env bash
# Tests bin/worktree in a throwaway repo with a bare remote, a fake HOME, and a
# stub rex, so nothing here touches a real lane, home, or Rex session. Lanes are
# narrowed to 97-99; their /tmp/ferry-*-lane-9[7-9] paths are real and are
# removed at the end.
#
# usage: test/worktree.sh
set -euo pipefail

here=$(cd "$(dirname "$0")/.." && pwd)
root=$(mktemp -d /tmp/ferry-worktree-test-XXXXXX)
root=$(cd "$root" && pwd -P)
export HOME=$root/home WORKTREE_LANE_RANGE=97-99
export GIT_AUTHOR_NAME=t GIT_AUTHOR_EMAIL=t@t GIT_COMMITTER_NAME=t GIT_COMMITTER_EMAIL=t@t
export GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_NOSYSTEM=1
unset WORKTREE_LANE FERRY_LANE_SUFFIX WORKTREE_PRIMARY
lanepaths() { for n in 97 98 99; do echo /tmp/ferry-{test,test-e2e,git,live}-lane-$n; done; }
cleanup() { rm -rf "$root"; lanepaths | xargs rm -rf; }
[[ -n ${KEEP:-} ]] || trap cleanup EXIT  # KEEP=1 keeps the root to debug
cleanup; mkdir -p "$root" "$HOME/src" "$root/bin"
for p in $(lanepaths); do [[ ! -e $p ]] || { echo "refusing: $p exists, a live lane may own it" >&2; exit 1; }; done

# A stub rex: sessions are lines in $root/rex-sessions, kills are logged.
cat > "$root/bin/rex" <<EOF
#!/bin/sh
case \$1 in
  ls) echo "LABEL ID"; cat "$root/rex-sessions" 2>/dev/null ;;
  kill) echo "\$2" >> "$root/rex-killed"; grep -v " \$2\$" "$root/rex-sessions" > "$root/rex-sessions.new" || true; mv "$root/rex-sessions.new" "$root/rex-sessions" ;;
esac
EOF
chmod +x "$root/bin/rex"
# A stub gh with one pull request, #1. `pr create` opens it for its --head
# branch. $R/gh-state holds OPEN, whose head is that branch on origin, or
# "MERGED <sha>". A squash merge commits the PR head's tree onto origin's main.
# $R/gh-checks is the exit code of `pr checks` (0 passed, 8 pending, 1 failed),
# and $R/gh-merge-lost makes a merge go through but report failure. Calls are
# logged to $R/gh.log.
cat > "$root/bin/fake-gh" <<'EOF'
#!/bin/sh
R=$FAKE_ROOT O=$FAKE_ROOT/origin.git
echo "$*" >> "$R/gh.log"
state() {
  [ -e "$R/gh-state" ] || return 1
  read -r s m < "$R/gh-state"
  if [ "$s" = MERGED ]; then echo "MERGED $m"; else echo "OPEN $(git -C "$O" rev-parse "refs/heads/$(cat "$R/gh-branch")")"; fi
}
case "$1 $2" in
  "pr create")
    while [ $# -gt 0 ]; do [ "$1" = --head ] && echo "$2" > "$R/gh-branch"; shift; done
    echo OPEN > "$R/gh-state"; echo https://github.test/ferry/pull/1 ;;
  "pr list") state | grep -q '^OPEN' && echo 1; exit 0 ;;
  "pr view")
    st=$(state) || exit 1
    case "$*" in
      *number,state,headRefOid*) echo "1 $st" ;;
      *state,headRefOid*) echo "$st" ;;
      *"json state "*) echo "${st%% *}" ;;
      *) echo https://github.test/ferry/pull/1 ;;
    esac ;;
  "pr checks") exit "$(cat "$R/gh-checks" 2>/dev/null || echo 0)" ;;
  "api -X")
    case "$*" in
      *PUT*/merge*)
        for a in "$@"; do case $a in sha=*) sha=${a#sha=} ;; commit_title=*) title=${a#commit_title=} ;; esac; done
        new=$(git -C "$O" commit-tree "$sha^{tree}" -p refs/heads/main -m "$title") || exit 1
        git -C "$O" update-ref refs/heads/main "$new"
        echo "MERGED $sha" > "$R/gh-state"
        [ ! -e "$R/gh-merge-lost" ] || { echo "HTTP 502: reply lost" >&2; exit 1; } ;;
    esac ;;
esac
EOF
chmod +x "$root/bin/fake-gh"
export PATH=$root/bin:$PATH WORKTREE_GH=$root/bin/fake-gh FAKE_ROOT=$root
export FERRY_VERIFY_EVIDENCE=$root/evidence WORKTREE_INSTALL='echo x >> "$FAKE_ROOT/installs"'
installs() { if [[ -f $root/installs ]]; then wc -l < "$root/installs" | tr -d ' '; else echo 0; fi; }

pass=0 fail=0
check() { if eval "$2"; then echo "  ok    $1"; pass=$((pass + 1)); else echo "  FAIL  $1"; fail=$((fail + 1)); fi; }
g() { git -c commit.gpgsign=false -c init.defaultBranch=main "$@"; }

# The repo: bin/worktree, the lane loader, the Makefile, and a tiny Go program for `make build`.
g init -q --bare "$root/origin.git"
g clone -q "$root/origin.git" "$root/p" 2>/dev/null
p=$root/p
mkdir -p "$p/bin" "$p/test/lib"
cp "$here/bin/worktree" "$p/bin/"; cp "$here/test/lib/lane.sh" "$p/test/lib/"; cp "$here/Makefile" "$here/.gitignore" "$p/"
printf 'module ferry\n\ngo 1.21\n' > "$p/go.mod"
printf 'package main\n\nfunc main() {}\n' > "$p/main.go"
printf 'package main\n\nconst protocol = 1\n' > "$p/machine.go"
cat > "$p/test/probe.sh" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
source "$(dirname "$0")/lib/lane.sh"
echo "suffix=$FERRY_LANE_SUFFIX lane=${WORKTREE_LANE:-}"
EOF
chmod +x "$p/test/probe.sh"
g -C "$p" add -A; g -C "$p" commit -qm init; g -C "$p" push -q origin main 2>/dev/null
wt() { g -C "$p" worktree add -q -b "$1" "$p/.claude/worktrees/$1" 2>/dev/null; echo "$p/.claude/worktrees/$1"; }

echo "primary"
check "setup refuses in the primary" '! (cd "$p" && bin/worktree setup) 2>/dev/null'
check "info reports the primary" '(cd "$p" && bin/worktree info) | grep "(primary)" >/dev/null'
check "guard --deny does nothing in the primary" '(cd "$p" && bin/worktree guard --deny x)'
check "lane.sh leaves the suffix empty" '[[ $("$p/test/probe.sh") == "suffix= lane=" ]]'

echo "setup"
mkdir /tmp/ferry-live-lane-97   # something already named for lane 97
a=$(wt a)
check "lane.sh refuses before setup" '! "$a/test/probe.sh" 2>/dev/null'
check "guard --deny exits 3 in a lane" '(cd "$a" && bin/worktree guard --deny "make release" 2>/dev/null); [[ $? == 3 ]]'
check "guard --warn continues in a lane" '(cd "$a" && bin/worktree guard --warn x 2>/dev/null)'
check "guard --confirm refuses without a terminal" \
  '(cd "$a" && perl -MPOSIX -e "POSIX::setsid(); exec @ARGV" bin/worktree guard --confirm x </dev/null 2>/dev/null); [[ $? == 3 ]]'
check "make install refuses without a terminal" \
  '! (cd "$a" && perl -MPOSIX -e "POSIX::setsid(); exec @ARGV" make -s install BIN="$root/installed" </dev/null >/dev/null 2>&1) && [[ ! -e $root/installed/ferry ]]'
(cd "$a" && bin/worktree setup >/dev/null)
check "skips the index something is named for, takes 98" '[[ $(cat "$p/tmp/worktrees/98") == "$a" ]] && [[ ! -e $p/tmp/worktrees/97 ]]'
check "worktree.env names lane 98" 'grep -qx "FERRY_LANE_SUFFIX=-lane-98" "$a/worktree.env"'
check "the lane built its own binary" '[[ -x $a/ferry ]]'
check "lane.sh loads the lane" '[[ $("$a/test/probe.sh") == "suffix=-lane-98 lane=98" ]]'
check "an inherited matching lane keeps its values" '[[ $(WORKTREE_LANE=98 FERRY_LANE_SUFFIX=-scratch "$a/test/probe.sh") == "suffix=-scratch lane=98" ]]'
check "another lane's values get replaced" '[[ $(WORKTREE_LANE=5 FERRY_LANE_SUFFIX=-lane-5 "$a/test/probe.sh") == "suffix=-lane-98 lane=98" ]]'
(cd "$a" && bin/worktree setup >/dev/null)
check "setup again keeps lane 98" '[[ $(ls "$p/tmp/worktrees") == 98 ]]'
rmdir /tmp/ferry-live-lane-97
b=$(wt b); (cd "$b" && bin/worktree setup >/dev/null)
check "a second worktree takes 97" '[[ $(cat "$p/tmp/worktrees/97") == "$b" ]]'
check "guard judges the checkout that holds it, not the working directory" '(cd "$p" && "$b/bin/worktree" guard --deny x 2>/dev/null); [[ $? == 3 ]]'
check "make -f a lane's Makefile from the primary still asks" \
  '! (cd "$p" && perl -MPOSIX -e "POSIX::setsid(); exec @ARGV" make -s -f "$b/Makefile" install BIN="$root/installed" </dev/null >/dev/null 2>&1) && [[ ! -e $root/installed/ferry ]]'
check "info names lane 97" '(cd "$b" && bin/worktree info) | grep "/tmp/ferry-git-lane-97" >/dev/null'
d=$(wt d)
check "the range can't widen past 99" '! (cd "$d" && WORKTREE_LANE_RANGE=97-150 bin/worktree setup) 2>/dev/null && [[ ! -f $d/worktree.env ]]'
check "a full range refuses" '! (cd "$d" && WORKTREE_LANE_RANGE=97-98 bin/worktree setup) 2>/dev/null && [[ $(ls "$p/tmp/worktrees") == "97 98" || $(ls "$p/tmp/worktrees" | xargs) == "97 98" ]]'
g -C "$p" worktree remove "$d"; g -C "$p" branch -q -D d

echo "concurrency"
e=$(wt e)
(cd "$e" && bin/worktree setup >"$root/e1.log" 2>&1) & (cd "$e" && bin/worktree setup >"$root/e2.log" 2>&1) & wait
check "two setups racing in one worktree both succeed with one lane" '[[ $(grep -l "$e" "$p"/tmp/worktrees/* | wc -l) -eq 1 ]] && grep -q "already provisioned" "$root/e1.log" "$root/e2.log"'
g -C "$p" worktree remove --force "$e"; (cd "$p" && bin/worktree teardown --stale >/dev/null)
check "the racing lane is swept" '! grep -qs "$e" "$p"/tmp/worktrees/*'
perl -MFcntl=:flock -e 'open(my $l, ">>", shift); flock($l, LOCK_EX); sleep 60' "$p/tmp/worktrees.lock" & holder=$!
sleep 0.3; kill -9 "$holder"; wait "$holder" 2>/dev/null || true
check "a lock whose holder died doesn't block" '(cd "$p" && bin/worktree teardown --stale >/dev/null)'

echo "teardown --stale"
echo "x" > "$p/tmp/worktrees/150"
mkdir -p /tmp/ferry-test-lane-97/x /tmp/ferry-git-lane-98 "$HOME/src/ferry-e2e-probe-lane-97" "$HOME/.local/state/ferry/tasks" "$HOME/.claude/projects/-tmp-ferry-test-lane-97-fakehome"
touch "$HOME/.local/state/ferry/tasks/src-ferry-e2e-probe-lane-97-abc.json" "$HOME/.local/state/ferry/tasks/src-ferry-e2e-probe-abc.json"
printf 'ferry-e2e-probe-lane-97 s97\nferry-e2e-probe s0\nferry-liveness-test-lane-98 s98\n' > "$root/rex-sessions"
rm -rf "$b"
mkdir -p /tmp/ferry-live-lane-99   # named for lane 99, which has no registry entry
(cd "$p" && bin/worktree teardown --stale >/dev/null 2>&1)
check "the gone lane's dirs are dropped" '[[ ! -e /tmp/ferry-test-lane-97 && ! -e $HOME/src/ferry-e2e-probe-lane-97 && ! -e $HOME/.claude/projects/-tmp-ferry-test-lane-97-fakehome ]]'
check "its task record is dropped, the primary's stays" '[[ ! -e $HOME/.local/state/ferry/tasks/src-ferry-e2e-probe-lane-97-abc.json && -e $HOME/.local/state/ferry/tasks/src-ferry-e2e-probe-abc.json ]]'
check "only its Rex session is killed" '[[ $(cat "$root/rex-killed") == s97 ]]'
check "its registry entry is gone" '[[ ! -e $p/tmp/worktrees/97 ]]'
check "the live lane is untouched" '[[ -e $p/tmp/worktrees/98 && -d /tmp/ferry-git-lane-98 ]]'
check "an unregistered lane's leftovers are untouched" '[[ -d /tmp/ferry-live-lane-99 ]]'
check "an out-of-range entry is ignored" '[[ -e $p/tmp/worktrees/150 ]]'
rm "$p/tmp/worktrees/150"; rmdir /tmp/ferry-live-lane-99

echo "sync"
check "sync refuses in a lane" '! (cd "$a" && bin/worktree sync) 2>/dev/null'
check "sync installs main the first time" '(cd "$p" && bin/worktree sync) | grep "installing it" >/dev/null && [[ $(installs) == 1 ]]'
check "sync again doesn't reinstall" '(cd "$p" && bin/worktree sync) | grep "installed$" >/dev/null && [[ $(installs) == 1 ]]'
echo synced > "$p/synced.txt"; g -C "$p" add synced.txt; g -C "$p" commit -qm "main moved for sync"; g -C "$p" push -q origin main 2>/dev/null
check "a failed install says NOT INSTALLED" '! (cd "$p" && WORKTREE_INSTALL=false bin/worktree sync) 2>&1 | grep -q "NOT INSTALLED" ; [[ $(installs) == 1 ]]'
check "the next sync installs what the failed one didn't" '(cd "$p" && bin/worktree sync) | grep "installing it" >/dev/null && [[ $(installs) == 2 ]]'
echo local > "$p/local.txt"; g -C "$p" add local.txt; g -C "$p" commit -qm "local only"
check "sync refuses commits origin's main lacks" '! (cd "$p" && bin/worktree sync) 2>/dev/null'
g -C "$p" reset -q --hard origin/main

echo "land"
printf 'package main\n\nconst protocol = 2\n' > "$a/machine.go"
echo change > "$a/change.txt"; g -C "$a" add change.txt machine.go; g -C "$a" commit -qm "lane change"
check "teardown refuses a lane that isn't merged" '! (cd "$p" && bin/worktree teardown a) >/dev/null 2>&1 && [[ -d $a ]]'
echo wip > "$a/wip.txt"; g -C "$a" add wip.txt
check "land refuses tracked changes in the lane" '! (cd "$a" && bin/worktree land) 2>/dev/null && ! g -C "$root/origin.git" rev-parse -q --verify refs/heads/a >/dev/null'
g -C "$a" rm -q --cached wip.txt
check "land without a NAME refuses in the primary" '! (cd "$p" && bin/worktree land) 2>/dev/null'
check "land refuses a lane behind origin's main and says to rebase" \
  '! (cd "$a" && bin/worktree land) 2>"$root/err" && grep -q "git rebase origin/main" "$root/err" && ! grep -q reread "$root/err" && ! g -C "$root/origin.git" rev-parse -q --verify refs/heads/a >/dev/null'
g -C "$a" rebase -q origin/main
check "land refuses a tree no verify run stamped" '! (cd "$a" && bin/worktree land) 2>"$root/err" && grep -q "world.sh all" "$root/err"'
mkdir -p "$FERRY_VERIFY_EVIDENCE/w1"; g -C "$a" rev-parse "HEAD^{tree}" > "$FERRY_VERIFY_EVIDENCE/w1/verified"
check "a stamped lane opens a pull request titled by its commit" \
  '(cd "$a" && bin/worktree land) | grep "opened its pull request" >/dev/null && grep -q "pr create.*--title lane change" "$root/gh.log"'
check "an untracked file in the lane stayed out of the push" '[[ -f $a/wip.txt ]] && ! g -C "$root/origin.git" cat-file -e a:wip.txt 2>/dev/null'
rm "$a/wip.txt"
check "the pushed branch is the lane's HEAD" '[[ $(g -C "$root/origin.git" rev-parse a) == $(g -C "$a" rev-parse HEAD) ]]'
check "main didn't move on origin" '[[ $(g -C "$root/origin.git" rev-parse main) == $(g -C "$p" rev-parse main) ]]'
echo more > "$a/more.txt"; g -C "$a" add more.txt; g -C "$a" commit -qm "more"
check "land refuses two commits without --message" '! (cd "$a" && bin/worktree land --no-verify t) >/dev/null 2>"$root/err" && grep -q "pass --message FILE" "$root/err"'
printf 'Land the lane change\n\nWhy it changed.\n' > "$root/msg"; printf 'The PR description.\n' > "$root/body"
check "--message squashes the lane into one commit with that message, and updates the open PR" \
  '(cd "$p" && bin/worktree land a --message "$root/msg" --body "$root/body" --no-verify "tests only") | grep "updated pull request #1" >/dev/null && [[ $(g -C "$a" rev-list --count origin/main..HEAD) == 1 && $(g -C "$a" log -1 --format=%s) == "Land the lane change" && $(g -C "$root/origin.git" rev-parse a) == $(g -C "$a" rev-parse HEAD) && $(grep -c "pr create" "$root/gh.log") == 1 ]]'
check "the PR got the new title, the --body text, and the --no-verify reason" \
  'grep -q "api -X PATCH repos/{owner}/{repo}/pulls/1 -f title=Land the lane change -f body=The PR description." "$root/gh.log" && grep -q "Not verified with verify-ferry.*tests only" "$root/gh.log"'
echo "# a tooling change" >> "$p/bin/worktree"; g -C "$p" commit -qam "tooling moved"; g -C "$p" push -q origin main 2>/dev/null
check "land names a tooling change on origin's main" '! (cd "$a" && bin/worktree land --no-verify t) >/dev/null 2>"$root/err" && grep -q "reread .claude/skills/worktree/SKILL.md" "$root/err"'
g -C "$a" rebase -q origin/main
check "landing a rebased lane force-pushes it" '(cd "$a" && bin/worktree land --no-verify t >/dev/null) && [[ $(g -C "$root/origin.git" rev-parse a) == $(g -C "$a" rev-parse HEAD) ]]'

echo "merge"
check "merge runs only in the primary" '! (cd "$a" && bin/worktree merge a) 2>/dev/null'
echo 8 > "$root/gh-checks"
check "merge refuses while checks run" '! (cd "$p" && bin/worktree merge a) 2>"$root/err" && grep -q "still running" "$root/err"'
echo 1 > "$root/gh-checks"
check "merge refuses failed checks" '! (cd "$p" && bin/worktree merge a) 2>"$root/err" && grep -q "didn.t pass" "$root/err"'
echo 0 > "$root/gh-checks"
echo extra > "$a/extra.txt"; g -C "$a" add extra.txt; g -C "$a" commit -qm "not landed"
check "merge refuses a lane that moved past its PR's head" '! (cd "$p" && bin/worktree merge a) 2>"$root/err" && grep -q "Land again" "$root/err"'
g -C "$a" reset -q --hard HEAD~1
check "no merge was sent yet" '! grep -q "/merge" "$root/gh.log"'
touch "$root/gh-merge-lost"; before=$(installs)
(cd "$p" && bin/worktree merge a) >"$root/out" 2>&1 || true
check "a merge whose reply is lost is sent once, pinned to the lane's HEAD" \
  '[[ $(grep -c "pulls/1/merge" "$root/gh.log") == 1 ]] && grep -q "sha=$(g -C "$a" rev-parse HEAD)" "$root/gh.log" && grep -q "merged pull request #1" "$root/out"'
check "origin's main got one squash commit with the PR number" \
  '[[ $(g -C "$root/origin.git" log -1 --format=%s main) == "Land the lane change (#1)" && $(g -C "$root/origin.git" rev-parse "main^{tree}") == $(g -C "$a" rev-parse "HEAD^{tree}") ]]'
check "merge synced the primary: main fast-forwarded and installed" \
  '[[ $(g -C "$p" rev-parse main) == $(g -C "$root/origin.git" rev-parse main) && $(installs) == $((before + 1)) ]]'
check "sync says when the protocol changed" 'grep -q "protocol went from 1 to 2" "$root/out"'
check "merging again reports the merge without sending another" \
  '(cd "$p" && bin/worktree merge a) | grep "already merged" >/dev/null && [[ $(grep -c "pulls/1/merge" "$root/gh.log") == 1 ]]'
echo extra > "$a/extra.txt"; g -C "$a" add extra.txt; g -C "$a" commit -qm "after the merge"
check "teardown refuses when the merged head isn't the lane's HEAD" '! (cd "$p" && bin/worktree teardown a) >/dev/null 2>&1 && [[ -d $a ]]'
g -C "$a" reset -q --hard HEAD~1

echo "teardown NAME"
c=$(wt c); (cd "$c" && bin/worktree setup >/dev/null); echo junk > "$c/junk.txt"
check "teardown refuses uncommitted changes" '! (cd "$p" && bin/worktree teardown c) >/dev/null 2>&1 && [[ -d $c ]]'
cn=$(sed -n 's/^WORKTREE_LANE=//p' "$c/worktree.env"); mkdir -p "/tmp/ferry-git-lane-$cn"
(cd "$p" && bin/worktree teardown c --discard >/dev/null 2>&1)
check "--discard removes it anyway, with its branch and lane" '[[ ! -d $c && ! -e $p/tmp/worktrees/$cn && ! -d /tmp/ferry-git-lane-$cn ]] && ! g -C "$p" rev-parse -q --verify refs/heads/c >/dev/null'
(cd "$p" && bin/worktree teardown a >/dev/null 2>&1)
check "a landed lane tears down" '[[ ! -d $a && ! -e $p/tmp/worktrees/98 && ! -d /tmp/ferry-git-lane-98 ]] && ! g -C "$p" rev-parse -q --verify refs/heads/a >/dev/null'
check "the registry is empty" '[[ -z $(ls "$p/tmp/worktrees") ]]'

echo "land, Markdown only"
m=$(wt m); echo docs > "$m/README.md"; g -C "$m" add README.md; g -C "$m" commit -qm "docs"
check "a Markdown-only lane lands with no verify run" '(cd "$m" && bin/worktree land) | grep "only Markdown changed" >/dev/null'
g -C "$p" worktree remove --force "$m"; g -C "$p" branch -q -D m

echo "$pass passed, $fail failed"
(( fail == 0 ))
