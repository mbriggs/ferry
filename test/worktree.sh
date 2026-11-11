#!/usr/bin/env bash
# Tests ferry's lane file, bin/worktree.lane, and the loader the test scripts
# source, test/lib/lane.sh, through bin/worktree's commands. bin/worktree
# itself is the shared script from the create-worktree-lanes skill, whose own
# tests cover the registry, locking, landing, merge, and sync.
#
# It runs in a throwaway repo with a bare remote, a fake HOME, a stub rex, and
# a stub gh, so nothing here touches a real lane, home, or Rex session. Lanes
# are narrowed to 97-99; their /tmp/ferry-*-lane-9[7-9] paths are real and are
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
unset WORKTREE_LANE WORKTREE_PRIMARY WORKTREE_KEYS FERRY_LANE_SUFFIX FERRY_VERIFY_EVIDENCE
lanepaths() { for n in 97 98 99; do echo /tmp/ferry-{test,test-e2e,git,live,claude-worktree}-lane-$n; done; }
cleanup() { chmod -R u+w "$root" /tmp/ferry-test-lane-9[7-9] 2>/dev/null || true; rm -rf "$root"; lanepaths | xargs rm -rf; }
for p in $(lanepaths); do [[ ! -e $p ]] || { echo "refusing: $p exists, a live lane may own it" >&2; rm -rf "$root"; exit 1; }; done
[[ -n ${KEEP:-} ]] || trap cleanup EXIT  # KEEP=1 keeps the root to debug
mkdir -p "$HOME/src" "$root/bin"

# A stub rex: sessions are "LABEL ID" lines in $root/rex-sessions, kills are logged.
cat > "$root/bin/rex" <<EOF
#!/bin/sh
case \$1 in
  ls) echo "LABEL ID"; cat "$root/rex-sessions" 2>/dev/null ;;
  kill) echo "\$2" >> "$root/rex-killed"; grep -v " \$2\$" "$root/rex-sessions" > "$root/rex-sessions.new" || true; mv "$root/rex-sessions.new" "$root/rex-sessions" ;;
esac
EOF
chmod +x "$root/bin/rex"
# A stub gh that has no pull requests and opens one on pr create. Calls are
# logged to $root/gh.log.
cat > "$root/bin/fake-gh" <<EOF
#!/bin/sh
echo "\$*" >> "$root/gh.log"
case "\$1 \$2" in
  "pr list") exit 0 ;;
  "pr view") case "\$*" in *number,state*) exit 1 ;; *) echo https://github.test/ferry/pull/1 ;; esac ;;
esac
EOF
chmod +x "$root/bin/fake-gh"
export PATH=$root/bin:$PATH WORKTREE_GH=$root/bin/fake-gh

pass=0 fail=0
check() { if eval "$2"; then echo "  ok    $1"; pass=$((pass + 1)); else echo "  FAIL  $1"; fail=$((fail + 1)); fi; }
g() { git -c commit.gpgsign=false -c init.defaultBranch=main "$@"; }
notty() { perl -MPOSIX -e 'POSIX::setsid(); exec @ARGV' "$@" </dev/null; }

# The repo: the lane tooling, the Makefile and what make install copies, and
# a tiny Go program for make build.
g init -q --bare "$root/origin.git"
g clone -q "$root/origin.git" "$root/p" 2>/dev/null
p=$root/p
mkdir -p "$p/bin" "$p/test/lib" "$p/skills/ferry" "$p/.claude/skills/verify-ferry"
cp "$here/bin/worktree" "$here/bin/worktree.lane" "$here/bin/worktree-env.sh" "$p/bin/"
cp "$here/test/lib/lane.sh" "$p/test/lib/"
cp "$here/Makefile" "$here/.gitignore" "$p/"
cp "$here/skills/ferry/SKILL.md" "$here/skills/ferry/after-exit.sh" "$p/skills/ferry/"
echo world > "$p/.claude/skills/verify-ferry/SKILL.md"
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
lane() { sed -n 's/^WORKTREE_LANE=//p' "$1/worktree.env"; }

echo "primary"
check "info reports the primary with no suffix" '(cd "$p" && bin/worktree info) | grep -x "FERRY_LANE_SUFFIX (empty)" >/dev/null'
check "lane.sh leaves the suffix empty" '[[ $("$p/test/probe.sh") == "suffix= lane=" ]]'

echo "setup"
mkdir /tmp/ferry-live-lane-97   # something already named for lane 97
a=$(wt a)
check "lane.sh refuses before setup" '! "$a/test/probe.sh" 2>/dev/null'
check "make install refuses in a lane without a terminal" \
  '! (cd "$a" && notty make -s install >/dev/null 2>&1) && [[ ! -e $HOME/.local/bin/ferry ]]'
(cd "$a" && bin/worktree setup >/dev/null)
check "skips the index a scratch dir is named for, takes 98" '[[ $(lane "$a") == 98 && ! -e $p/tmp/worktrees/97 ]]'
check "worktree.env names the suffix" 'grep -qx "FERRY_LANE_SUFFIX=-lane-98" "$a/worktree.env"'
check "the lane built its own binary" '[[ -x $a/ferry ]]'
check "lane.sh loads the lane" '[[ $("$a/test/probe.sh") == "suffix=-lane-98 lane=98" ]]'
check "an inherited matching lane keeps its values" '[[ $(WORKTREE_LANE=98 FERRY_LANE_SUFFIX=-scratch "$a/test/probe.sh") == "suffix=-scratch lane=98" ]]'
check "another lane's values get replaced" '[[ $(WORKTREE_LANE=5 FERRY_LANE_SUFFIX=-lane-5 "$a/test/probe.sh") == "suffix=-lane-98 lane=98" ]]'
check "info names the lane's scratch dirs and Rex sessions" \
  '(cd "$a" && bin/worktree info) >"$root/out" && grep -q "/tmp/ferry-git-lane-98" "$root/out" && grep -q "ferry-claude-worktree-lane-98" "$root/out"'
(cd "$a" && bin/worktree setup >/dev/null)
check "setup again keeps lane 98" '[[ $(ls "$p/tmp/worktrees" | xargs) == 98 ]]'
rmdir /tmp/ferry-live-lane-97
echo "ferry-claude-worktree-lane-97 s97x" > "$root/rex-sessions"   # a Rex session named for 97
b=$(wt b); (cd "$b" && bin/worktree setup >/dev/null)
check "skips the index a Rex session is named for, takes 99" '[[ $(lane "$b") == 99 ]]'
: > "$root/rex-sessions"
check "make -f a lane's Makefile from the primary still asks" \
  '! (cd "$p" && notty make -s -f "$b/Makefile" install >/dev/null 2>&1) && [[ ! -e $HOME/.local/bin/ferry ]]'
# ./ferry push here is the test's empty main, so the guard's message is the proof.
check "make push refuses in a lane without a terminal" \
  '! (cd "$b" && notty make -s push >"$root/out" 2>&1) && grep -q "make push needs confirmation on a terminal" "$root/out"'

echo "teardown --stale, every resource present"
s=-lane-99 P=$HOME/.claude/projects T=$HOME/.local/state/ferry
mkdir -p /tmp/ferry-{test,test-e2e,git,live,claude-worktree}$s/x "$HOME/src/ferry-e2e-probe$s" "$T/tasks" "$T/parked/src-ferry-e2e-probe$s-abc" \
  "$P/-Users-t-src-ferry-e2e-probe$s" "$P/-private-tmp-ferry-test$s-fakehome" "$P/-tmp-ferry-test-e2e$s-laptop" \
  "$P/-private-tmp-ferry-claude-worktree$s" "$P/-private-tmp-ferry-claude-worktree$s--claude-worktrees-feat" \
  "$P/-private-tmp-ferry-test-lane-990-x" "$P/-Users-t-src-ferry-e2e-probe"
touch "$T/tasks/src-ferry-e2e-probe$s-abc.json" "$T/tasks/src-ferry-e2e-probe-abc.json"
printf 'ferry-e2e-probe%s s1\nferry-liveness-test%s s2\nferry-claude-worktree%s s3\nferry-e2e-probe s0\nferry-liveness-test-lane-98 s98\n' "$s" "$s" "$s" > "$root/rex-sessions"
mkdir -p /tmp/ferry-git-lane-98
rm -rf "$b"
(cd "$p" && bin/worktree teardown --stale >"$root/out" 2>&1)
check "the gone lane's scratch dirs and probe are dropped" \
  '! ls -d /tmp/ferry-*$s "$HOME/src/ferry-e2e-probe$s" 2>/dev/null | grep -q .'
check "its records, parked state, and transcripts are dropped" \
  '[[ ! -e $T/tasks/src-ferry-e2e-probe$s-abc.json && ! -e $T/parked/src-ferry-e2e-probe$s-abc ]] && ! ls "$P" | grep -q -e "probe$s\$" -e "test$s-" -e "e2e$s-" -e "worktree$s"'
check "the primary's record and transcript, and a longer index's, stay" \
  '[[ -e $T/tasks/src-ferry-e2e-probe-abc.json && -d $P/-Users-t-src-ferry-e2e-probe && -d $P/-private-tmp-ferry-test-lane-990-x ]]'
check "only its Rex sessions are killed" '[[ $(sort "$root/rex-killed" | xargs) == "s1 s2 s3" ]]'
check "its registry entry is gone, and lane 98 is untouched" \
  '[[ ! -e $p/tmp/worktrees/99 && -e $p/tmp/worktrees/98 && -d /tmp/ferry-git-lane-98 ]] && grep -q "dropped lane 99" "$root/out"'

echo "teardown --stale, some resources gone"
c=$(wt c); (cd "$c" && bin/worktree setup >/dev/null); cn=$(lane "$c")
mkdir -p "/tmp/ferry-live-lane-$cn"
rm -rf "$c"
(cd "$p" && bin/worktree teardown --stale >"$root/out" 2>&1)
check "a lane with one scratch dir left drops cleanly" '[[ ! -e /tmp/ferry-live-lane-$cn && ! -e $p/tmp/worktrees/$cn ]]'
d=$(wt d); (cd "$d" && bin/worktree setup >/dev/null); dn=$(lane "$d")
mkdir -p "/tmp/ferry-test-lane-$dn/locked"; touch "/tmp/ferry-test-lane-$dn/locked/f"; chmod 555 "/tmp/ferry-test-lane-$dn/locked"
rm -rf "$d"
(cd "$p" && bin/worktree teardown --stale >"$root/out" 2>&1) || true
check "a scratch dir that won't delete keeps the registry entry" '[[ -e $p/tmp/worktrees/$dn ]] && grep -q "kept its registry entry" "$root/out"'
chmod 755 "/tmp/ferry-test-lane-$dn/locked"
(cd "$p" && bin/worktree teardown --stale >/dev/null 2>&1)
check "the next sweep drops it" '[[ ! -e $p/tmp/worktrees/$dn && ! -e /tmp/ferry-test-lane-$dn ]]'
g -C "$p" worktree prune; g -C "$p" branch -q -D b c d

echo "land"
echo change > "$a/change.txt"; g -C "$a" add change.txt; g -C "$a" commit -qm "lane change"
export FERRY_VERIFY_EVIDENCE=$root/evidence
check "land refuses a tree no verify-ferry run stamped" \
  '! (cd "$a" && bin/worktree land) >/dev/null 2>"$root/err" && grep -q "world.sh all" "$root/err" && ! g -C "$root/origin.git" rev-parse -q --verify refs/heads/a >/dev/null'
mkdir -p "$FERRY_VERIFY_EVIDENCE/w1"; g -C "$a" rev-parse "HEAD^{tree}" > "$FERRY_VERIFY_EVIDENCE/w1/verified"
check "a stamp under FERRY_VERIFY_EVIDENCE lets it land" \
  '(cd "$a" && bin/worktree land) >"$root/out" 2>&1 && grep -q "opened its pull request" "$root/out" && [[ $(g -C "$root/origin.git" rev-parse a) == $(g -C "$a" rev-parse HEAD) ]]'
echo changed >> "$p/.claude/skills/verify-ferry/SKILL.md"; g -C "$p" commit -qam "verify moved"; g -C "$p" push -q origin main 2>/dev/null
check "land names a verify-ferry change on origin's main as a tooling change" \
  '! (cd "$a" && bin/worktree land) >/dev/null 2>"$root/err" && grep -q "reread .claude/skills/worktree/SKILL.md" "$root/err" && grep -q verify-ferry "$root/err"'
check "merge is on: it asks for the lane's PR, not for the user to merge" \
  '! (cd "$p" && bin/worktree merge a) 2>"$root/err" && grep -q "has no pull request" "$root/err"'

echo "sync"
check "sync installs ferry and its skill into the home" \
  '(cd "$p" && bin/worktree sync) >/dev/null && [[ -x $HOME/.local/bin/ferry && -f $HOME/.claude/skills/ferry/SKILL.md ]]'
printf 'package main\n\nconst protocol = 2\n' > "$p/machine.go"; g -C "$p" commit -qam "protocol 2"; g -C "$p" push -q origin main 2>/dev/null
g -C "$p" reset -q --hard HEAD~1
check "sync says when the protocol changed" '(cd "$p" && bin/worktree sync) >"$root/out" 2>&1 && grep -q "protocol went from 1 to 2.*run make push" "$root/out"'

echo "teardown NAME"
(cd "$p" && bin/worktree teardown a --discard >/dev/null 2>&1)
check "--discard removes the lane and its scratch dirs" '[[ ! -d $a && ! -e $p/tmp/worktrees/98 && ! -d /tmp/ferry-git-lane-98 ]]'
check "the registry is empty" '[[ -z $(ls "$p/tmp/worktrees") ]]'

echo "$pass passed, $fail failed"
(( fail == 0 ))
