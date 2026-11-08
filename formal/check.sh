#!/bin/sh
# Model-check formal/Handoff.tla. See .claude/skills/verify-handoff/SKILL.md.
#
#   formal/check.sh              every mode, every check
#   formal/check.sh NoFork       every mode, one check
#
# happy:   no crash, no failed call.
# faults:  crashes between steps, failed calls, lost replies.
# overlap: faults, plus a dead run's call on kant finishing after the rerun
#          started (DeadKinds = DeadCalls).
#
# In each mode, a promise must hold, unless formal/known-findings lists it for
# that mode. A listed promise must fail as the code is, and must hold once the
# switches on its line remove its cause; if it still fails, something else
# breaks it too. Each Reach* check must fail in its mode, which shows the
# model gets there.
#
# Exit 0 when all of that is true, 1 otherwise. TLC's output is written to
# formal/out/<mode>-<check>[-pinned].out.
set -eu
here=$(cd "$(dirname "$0")" && pwd)
cd "$here"

TLC_TOOL='http:tla2tools[url=https://github.com/tlaplus/tlaplus/releases/download/v1.7.4/tla2tools.jar,checksum=sha256:936a262061c914694dfd669a543be24573c45d5aa0ff20a8b96b23d01e050e88]@1.7.4'
PROMISES="TypeOK NoTwoAgents NoLostTurns NoDestroyedTurns OwnerCanResume OneAgent AgentHasTranscript"
REACH="happy:ReachRoundTrip happy:ReachLaunch happy:ReachExec happy:ReachStopAgent happy:ReachReplacedPark faults:ReachResend faults:ReachRerunFinish faults:ReachPlaceRefusal overlap:ReachDeadCall"
ALLDEAD="put stop import stage place launch"
MAXGEN=${MAXGEN:-12}
MAXV=${MAXV:-3}

if [ -n "${TLA2TOOLS:-}" ]; then
  jar=$TLA2TOOLS
else
  dir=$(mise where "$TLC_TOOL" 2>/dev/null) || { mise install -q "$TLC_TOOL" >&2 && dir=$(mise where "$TLC_TOOL"); }
  jar=$dir/tla2tools.jar
fi
java=${JAVA:-java}
command -v "$java" >/dev/null || { echo "check.sh: needs Java 11 or later on PATH, or JAVA=/path/to/java" >&2; exit 2; }

# Warn when the code the model covers has changed since the sync commit.
skill=../.claude/skills/verify-handoff/SKILL.md
if [ -f "$skill" ]; then
  sync=$(sed -n 's/.*Sync commit: `\([0-9a-f]*\)`.*/\1/p' "$skill")
  if [ -n "$sync" ] && ! git -C .. diff --quiet "$sync" -- handoff.go record.go session.go machine.go main.go rex.go 2>/dev/null; then
    echo "WARN   covered code changed since sync commit ${sync%"${sync#????????????}"}; recheck the coverage table in $skill"
  fi
fi

mkdir -p out
status=0

# check MODE NAME OUT [SWITCH...]: run TLC, print VIOLATED, PASS or ERROR.
# A switch is -KIND, which drops one kind of dead call.
check() {
  mode=$1 inv=$2 base=out/$3; shift 3
  faults=TRUE; dead=""
  [ "$mode" = happy ] && faults=FALSE
  [ "$mode" = overlap ] && dead=$ALLDEAD
  for s in "$@"; do
    case $s in
      -*) dead=$(echo " $dead " | sed "s/ ${s#-} / /; s/^ *//; s/ *$//") ;;
    esac
  done
  set_=$(echo "$dead" | awk '{for(i=1;i<=NF;i++) printf "%s\"%s\"", (i>1?", ":""), $i}')
  printf 'SPECIFICATION Spec\nCONSTANTS\n  MaxGen = %s\n  MaxV = %s\n  Faults = %s\n  DeadKinds = {%s}\nCONSTRAINT Bounds\nINVARIANT %s\n' \
    "$MAXGEN" "$MAXV" "$faults" "$set_" "$inv" > "$base.cfg"
  rm -rf "$base.states"
  "$java" -XX:+UseParallelGC -cp "$jar" tlc2.TLC -workers auto -deadlock \
    -config "$base.cfg" -metadir "$base.states" Handoff.tla > "$base.out" 2>&1 || true
  if grep -q "Invariant $inv is violated" "$base.out"; then echo VIOLATED
  elif grep -q "Model checking completed. No error has been found" "$base.out"; then echo PASS
  else echo ERROR; fi
}
trace() { sed -n 's/^State [0-9]*: <\([A-Za-z]*\).*/\1/p' "$1" | paste -sd, -; }

for mode in happy faults overlap; do
  for inv in ${1:-$PROMISES}; do
    case " $PROMISES " in *" $inv "*) ;; *) continue ;; esac
    line=$(awk -v m="$mode" -v i="$inv" '$1 == m && $2 == i' known-findings)
    got=$(check "$mode" "$inv" "$mode-$inv")
    if [ "$got" = ERROR ]; then
      echo "ERROR  $mode $inv: TLC did not finish (out/$mode-$inv.out)"; status=1; continue
    fi
    if [ -z "$line" ]; then
      if [ "$got" = PASS ]; then echo "PASS   $mode $inv"
      else echo "FAIL   $mode $inv: violated (out/$mode-$inv.out)"; echo "       $(trace "out/$mode-$inv.out")"; status=1; fi
      continue
    fi
    switches=$(echo "$line" | awk '{$1=$2=""; print}' | sed 's/^ *//')
    if [ "$got" = PASS ]; then
      echo "FIXED? $mode $inv: holds, but known-findings lists it"; status=1; continue
    fi
    # shellcheck disable=SC2086
    pinned=$(check "$mode" "$inv" "$mode-$inv-pinned" $switches)
    case $pinned in
      PASS) echo "KNOWN  $mode $inv: fails, and holds with $switches (out/$mode-$inv.out)" ;;
      VIOLATED) echo "FAIL   $mode $inv: still fails with $switches, so something else breaks it (out/$mode-$inv-pinned.out)"
                echo "       $(trace "out/$mode-$inv-pinned.out")"; status=1 ;;
      *) echo "ERROR  $mode $inv: pinned run did not finish (out/$mode-$inv-pinned.out)"; status=1 ;;
    esac
  done
done

for r in $REACH; do
  mode=${r%%:*} inv=${r#*:}
  [ -n "${1:-}" ] && [ "$1" != "$inv" ] && continue
  case $(check "$mode" "$inv" "$mode-$inv") in
    VIOLATED) echo "REACH  $mode $inv: reached" ;;
    PASS) echo "FAIL   $mode $inv: never reached, so the model is vacuous there"; status=1 ;;
    *) echo "ERROR  $mode $inv: TLC did not finish (out/$mode-$inv.out)"; status=1 ;;
  esac
done
exit $status
