#!/bin/sh
# Model-check formal/Handoff.tla. See .claude/skills/verify-handoff/SKILL.md.
#
#   formal/check.sh              both modes, every invariant
#   formal/check.sh NoFork       both modes, one invariant
#
# happy:  Faults = FALSE (no crash, no failed call). Every invariant must hold.
# faults: Faults = TRUE. Every invariant must hold, or fail with exactly the
#         shortest trace listed for it in formal/known-findings.
# Exit 0 when all of that is true. Exit 1 on a new violation, a known finding
# whose trace changed or that now holds, or a TLC error. Traces are written to
# formal/out/<mode>-<invariant>.out.
set -eu
here=$(cd "$(dirname "$0")" && pwd)
cd "$here"

TLC_TOOL='http:tla2tools[url=https://github.com/tlaplus/tlaplus/releases/download/v1.7.4/tla2tools.jar,checksum=sha256:936a262061c914694dfd669a543be24573c45d5aa0ff20a8b96b23d01e050e88]@1.7.4'
INVARIANTS="TypeOK NoFork NoLoss OwnerCanResume"
MAXGEN=${MAXGEN:-6}

if [ -n "${TLA2TOOLS:-}" ]; then
  jar=$TLA2TOOLS
else
  dir=$(mise where "$TLC_TOOL" 2>/dev/null) || { mise install -q "$TLC_TOOL" >&2 && dir=$(mise where "$TLC_TOOL"); }
  jar=$dir/tla2tools.jar
fi
java=${JAVA:-java}
command -v "$java" >/dev/null || { echo "check.sh: needs Java 11 or later on PATH, or JAVA=/path/to/java" >&2; exit 2; }

# The action names along a TLC trace, comma-separated, then a hash of every
# state on it, so two traces with the same steps but different outcomes differ.
trace() {
  names=$(sed -n 's/^State [0-9]*: <\([A-Za-z]*\).*/\1/p' "$1" | paste -sd, -)
  hash=$(sed -n '/^State 1:/,/^[0-9]* states generated/p' "$1" | grep -v 'states generated' |
    sed 's/ line [0-9]*, col [0-9]* to line [0-9]*, col [0-9]* of module Handoff//' |
    sha256sum | cut -c1-12)
  echo "$hash $names"
}

mkdir -p out
status=0
for mode in happy faults; do
  faults=FALSE; [ $mode = faults ] && faults=TRUE
  for inv in ${1:-$INVARIANTS}; do
    base=out/$mode-$inv
    printf 'SPECIFICATION Spec\nCONSTANTS MaxGen = %s\n          Faults = %s\nCONSTRAINT GenBound\nINVARIANT %s\n' \
      "$MAXGEN" "$faults" "$inv" > "$base.cfg"
    rm -rf "$base.states"
    # One worker, so TLC reports the same shortest trace on every run.
    "$java" -XX:+UseParallelGC -cp "$jar" tlc2.TLC -workers 1 -deadlock \
      -config "$base.cfg" -metadir "$base.states" Handoff.tla > "$base.out" 2>&1 || true
    states=$(sed -n 's/.* \([0-9]*\) distinct states found.*/\1/p' "$base.out" | tail -1)
    want=""
    [ $mode = faults ] && want=$(awk -v i="$inv" '$1 == i {print $2, $3}' known-findings)
    if grep -q "Invariant $inv is violated" "$base.out"; then
      got=$(trace "$base.out")
      if [ -n "$want" ] && [ "$got" = "$want" ]; then
        echo "KNOWN  $mode $inv: known finding, same trace ($base.out)"
      elif [ -n "$want" ]; then
        echo "FAIL   $mode $inv: the trace changed ($base.out)"
        echo "       was: $want"
        echo "       now: $got"
        status=1
      else
        echo "FAIL   $mode $inv: violated ($base.out)"
        echo "       trace: $got"
        status=1
      fi
    elif grep -q "Model checking completed. No error has been found" "$base.out"; then
      if [ -n "$want" ]; then
        echo "FIXED? $mode $inv: holds over $states states, but formal/known-findings lists it"; status=1
      else
        echo "PASS   $mode $inv: holds over $states states (MaxGen = $MAXGEN)"
      fi
    else
      echo "ERROR  $mode $inv: TLC did not finish ($base.out)"; status=1
    fi
  done
done
exit $status
