#!/usr/bin/env bash
# A throwaway two-machine world for driving ferry by hand: a fake "laptop"
# home and a fake "kant" home under one root, joined by ferry's exec:HOME
# transport, so every remote operation runs through `ferry remote` exactly as
# it would over ssh. ferry runs with HOME set to a fake home, detached from any
# terminal, and with stub `ssh`, `rex`, `claude` and `codex` first on PATH, so a
# wrong host name or a missing --no-launch fails instead of reaching a real
# machine, Rex, or a real agent.
#
# usage: world.sh all                    check the core journeys in a new world; stamp the tree for land
#        world.sh new [--hooks]          create a world, print its root
#        world.sh doctor ROOT            health check; changes nothing a later check reads
#        world.sh rebuild ROOT           rebuild the binary after editing ferry's source
#        world.sh ferry ROOT [--as laptop|kant] [-C DIR] ARGS...
#                                        run ferry, log output to evidence
#        world.sh tty ROOT [--as laptop|kant] [-C DIR] ARGS...
#                                        run ferry in a private tmux pane (to answer prompts)
#        world.sh keys ROOT [TEXT]       type TEXT + Enter into that pane (no TEXT: Enter only)
#        world.sh press ROOT KEY...      send tmux key names (Left, Down, Enter) to that pane
#        world.sh wait ROOT TEXT [SECS]  poll the pane until TEXT shows (default 30s)
#        world.sh screen ROOT [LABEL]    capture the pane to evidence and print it
#        world.sh kant-work ROOT         play the agent on kant: commit, new file, transcript turn
#        world.sh state ROOT [LABEL]     snapshot both homes into evidence
#        world.sh backdate FILE...       set FILE's mtime a minute back (GNU and BSD)
#        world.sh clean ROOT             kill this world's tmux server, remove ROOT (evidence stays)
set -euo pipefail

repo=$(cd "$(dirname "$0")/../../.." && pwd)
evroot=${FERRY_VERIFY_EVIDENCE:-$HOME/.cache/ferry-verify}
SID=11111111-2222-3333-4444-555555555555
NAME=demo
die() { echo "world.sh: $*" >&2; exit 1; }

load() {
  root=$(cd "${1:?ROOT required}" 2>/dev/null && pwd) || die "no world at $1"
  [[ -f $root/.ferry-verify-world ]] || die "$root is not a world.sh root"
  run=$(basename "$root")
  laptop=$root/laptop kant=$root/kant ferry=$root/bin/ferry
  wt=$laptop/src/$NAME kwt=$kant/src/$NAME
  ev=$evroot/$run
  sock=$run
}

slug() { printf '%s' "$1" | sed 's#[^A-Za-z0-9]#-#g'; }
g() { git -c commit.gpgsign=false -c user.name=verify -c user.email=verify@example.invalid "$@"; }
# A digest of everything `go build` reads, so a rebuild is needed exactly when it changes.
src_hash() {
  (cd "$repo" && git ls-files -z -co --exclude-standard -- '*.go' go.mod go.sum \
    | sort -z | xargs -0 sha256sum | sha256sum | cut -c1-16)
}
build() {
  (cd "$repo" && CGO_ENABLED=0 go build -trimpath \
    -ldflags "-X main.version=$(git describe --tags --always --dirty)" -o "$ferry" .)
  src_hash > "$root/bin/.src-hash"
}
fresh_or_die() {
  [[ $(src_hash) == "$(cat "$root/bin/.src-hash" 2>/dev/null)" ]] \
    || die "ferry's source changed since this world's binary was built; run: $0 rebuild $root"
}
# Run a command in a new session with no controlling terminal, and exit with
# its status. setsid(1) isn't on macOS; perl is on both.
detached() {
  perl -MPOSIX -e 'my $p = fork; if (!$p) { POSIX::setsid(); exec @ARGV; exit 127 }
    waitpid($p, 0); exit($? & 127 ? 128 + ($? & 127) : $? >> 8)' -- "$@"
}
# Set a file's mtime a minute back, so ferry doesn't wait for it to settle.
backdate() { perl -e '$t = time - 60; utime $t, $t, @ARGV or die "backdate: $!\n"' -- "$@"; }
# Permission bits in octal, on GNU or BSD stat.
mode_of() { stat -c %a "$1" 2>/dev/null || stat -f %Lp "$1"; }

seq_next() { local n; n=$(find "$ev" -maxdepth 1 -name '[0-9]*' 2>/dev/null | wc -l); printf "%02d" "$n"; }
label_of() { printf '%s' "$*" | tr -c 'A-Za-z0-9.-' '_' | cut -c1-60; }

# who/where flags shared by ferry and tty
parse_as() {
  as=laptop dir=
  while [[ $# -gt 0 ]]; do
    case $1 in
      --as) as=$2; shift 2 ;;
      -C) dir=$2; shift 2 ;;
      *) break ;;
    esac
  done
  case $as in
    laptop) h=$laptop; : "${dir:=$wt}" ;;
    kant) h=$kant; [[ -d $kwt ]] && : "${dir:=$kwt}"; : "${dir:=$kant}" ;;
    *) die "--as laptop|kant" ;;
  esac
  args=("$@")
}

cmd_new() {
  local hooks=0
  [[ ${1:-} == --hooks ]] && hooks=1
  root=$(mktemp -d /tmp/ferry-verify-XXXXXX)
  touch "$root/.ferry-verify-world"
  load "$root"
  mkdir -p "$root/bin" "$ev"
  # claude and codex answer --version (ferry's doctor and version check read
  # it) with $HOME/.stub-version, or 0.0.0, and block everything else.
  for stub in ssh rex claude codex; do
    {
      echo '#!/bin/sh'
      [[ $stub == claude || $stub == codex ]] &&
        echo '[ "$*" = --version ] && { echo "$(cat "$HOME/.stub-version" 2>/dev/null || echo 0.0.0) (world.sh stub)"; exit 0; }'
      printf 'echo "world.sh: blocked %s $*" >&2\nexit 1\n' "$stub"
    } > "$root/bin/$stub"
    chmod +x "$root/bin/$stub"
  done
  build
  for side in laptop kant; do
    mkdir -p "$root/$side/.config/ferry"
    echo "$side" > "$root/$side/.config/ferry/name"
  done
  echo "kant exec:$kant" > "$laptop/.config/ferry/hosts"
  echo "laptop exec:$laptop" > "$kant/.config/ferry/hosts"

  # A repo with an origin, an unpushed commit, staged + unstaged + untracked
  # changes, a deletion, an ignored .env that travels and node_modules that doesn't.
  g init -q --bare --initial-branch=main "$root/origin.git"
  g clone -q "$root/origin.git" "$wt" 2>/dev/null
  printf 'v1\n' > "$wt/app.txt"; printf 'old\n' > "$wt/gone.txt"
  printf '.env\nnode_modules/\n.restored\n.ferry/state/\n' > "$wt/.gitignore"
  if (( hooks )); then
    mkdir -p "$wt/.ferry"
    printf '#!/bin/sh\nset -e\nmkdir -p "$FERRY_STATE"\necho "dump from $(cat "$HOME/.config/ferry/name")" > "$FERRY_STATE/db.dump"\necho dumped\n' > "$wt/.ferry/dehydrate"
    printf '#!/bin/sh\nset -e\necho "restored-$FERRY_DIRECTION" > "$FERRY_ROOT/.restored"\necho "db loaded: $(cat "$FERRY_STATE/db.dump")"\n' > "$wt/.ferry/restore"
  fi
  g -C "$wt" add -A; g -C "$wt" commit -qm initial; g -C "$wt" push -q origin HEAD:main
  printf 'unpushed\n' > "$wt/unpushed.txt"; g -C "$wt" add unpushed.txt; g -C "$wt" commit -qm unpushed
  printf 'v2 staged\n' > "$wt/app.txt"; g -C "$wt" add app.txt
  printf 'v2 staged\nv3 unstaged\n' > "$wt/app.txt"
  g -C "$wt" rm -q gone.txt
  printf 'scratch\n' > "$wt/untracked.txt"
  printf 'SECRET=verify\n' > "$wt/.env"
  mkdir -p "$wt/node_modules/x"; printf 'dep\n' > "$wt/node_modules/x/index.js"

  # A Claude session for the repo, idle for a minute so ferry won't wait on it.
  local p=$laptop/.claude/projects/$(slug "$wt")
  mkdir -p "$p"
  {
    printf '{"type":"user","cwd":"%s","sessionId":"%s","message":{"role":"user","content":"Remember the codeword HERON."}}\n' "$wt" "$SID"
    printf '{"type":"assistant","cwd":"%s","sessionId":"%s","message":{"role":"assistant","content":"ok"}}\n' "$wt" "$SID"
  } > "$p/$SID.jsonl"
  backdate "$p/$SID.jsonl"
  printf '{"projects":{"%s":{"hasTrustDialogAccepted":true}}}\n' "$wt" > "$laptop/.claude.json"

  cat > "$root/env" <<EOF
export ROOT=$root LAPTOP=$laptop KANT=$kant FERRY=$ferry WT=$wt KWT=$kwt EVIDENCE=$ev SID=$SID
EOF
  echo "world: $root  evidence: $ev" > "$ev/00-world.txt"
  echo "$root"
}

cmd_doctor() {
  load "$1"
  local bad=0 want have out
  ok() { echo "ok    $*"; }
  no() { echo "FAIL  $*"; bad=1; }
  [[ -x $ferry ]] && ok "binary $ferry" || no "no binary at $ferry (run new again)"
  want=$(src_hash) have=$(cat "$root/bin/.src-hash" 2>/dev/null || echo none)
  [[ $have == "$want" ]] && ok "binary built from the current source ($("$ferry" version | awk '{print $2}'), src $have)" \
    || no "source changed since the build (src $have, now $want): run world.sh rebuild $root"
  local stub stubs_ok=1
  for stub in ssh rex claude codex; do
    [[ $(PATH=$root/bin:$PATH command -v $stub) == "$root/bin/$stub" ]] || stubs_ok=0
  done
  (( stubs_ok )) && ok "ssh, rex, claude, codex resolve to this world's stubs" \
    || no "stubs missing from $root/bin (a world from an older world.sh; run new again)"
  [[ $(cat "$laptop/.config/ferry/name") == laptop && $(cat "$kant/.config/ferry/name") == kant ]] \
    && ok "machine names laptop / kant" || no "machine names wrong"
  [[ $(cat "$laptop/.config/ferry/hosts") == "kant exec:$kant" ]] \
    && ok "laptop reaches kant through exec:, not ssh" || no "laptop hosts file doesn't point into this world"
  if out=$(detached env HOME="$laptop" SHELL= PATH="$root/bin:$PATH" "$ferry" ls kant </dev/null 2>&1); then ok "ferry ls kant: $(echo "$out" | head -1)"
  else no "ferry ls kant failed: $out"; fi
  if tmux -L "$sock" has-session 2>/dev/null; then
    ok "tmux server -L $sock is up (pane: $(tmux -L "$sock" display -p -t ferry '#{pane_current_command}' 2>/dev/null))"
  else ok "no tmux pane running"; fi
  return $bad
}

cmd_ferry() {
  load "$1"; shift; parse_as "$@"
  fresh_or_die
  local log rc=0
  log=$ev/$(seq_next)-$as-ferry-$(label_of "${args[*]}").log
  echo "\$ cd $dir && HOME=$h ferry ${args[*]}" > "$log"
  # detached: no controlling terminal, so ferry's /dev/tty prompts answer no.
  (cd "$dir" && detached env HOME="$h" SHELL= PATH="$root/bin:$PATH" "$ferry" "${args[@]}" </dev/null >>"$log" 2>&1) || rc=$?
  echo "[exit $rc]" >> "$log"
  cat "$log"
  return $rc
}

cmd_tty() {
  load "$1"; shift; parse_as "$@"
  fresh_or_die
  local q=() a
  for a in "${args[@]}"; do q+=("$(printf '%q' "$a")"); done
  tmux -L "$sock" kill-session -t ferry 2>/dev/null || true
  tmux -L "$sock" new-session -d -s ferry -x 200 -y 50 -c "$dir" \
    "env HOME=$(printf %q "$h") SHELL= PATH=$(printf %q "$root/bin:$PATH") $ferry ${q[*]}; echo \"[exit \$?]\"; sleep 86400"
  echo "started in tmux -L $sock (session ferry); read it with: world.sh screen $root"
}

cmd_keys() {
  load "$1"; shift
  (( $# )) && tmux -L "$sock" send-keys -t ferry -l "$*"
  tmux -L "$sock" send-keys -t ferry Enter
}
cmd_press() {
  load "$1"; shift
  tmux -L "$sock" send-keys -t ferry "$@"
}

cmd_wait() {
  load "$1"
  local text=${2:?TEXT required} secs=${3:-30} i
  for ((i = 0; i < secs * 4; i++)); do
    tmux -L "$sock" capture-pane -p -J -S - -t ferry 2>/dev/null | grep -qF -- "$text" && return 0
    sleep 0.25
  done
  die "pane never showed '$text' within ${secs}s"
}

cmd_screen() {
  load "$1"
  local f=$ev/$(seq_next)-screen-$(label_of "${2:-pane}").txt
  tmux -L "$sock" capture-pane -p -J -S - -t ferry | sed -e :a -e '/^\n*$/{$d;N;ba' -e '}' | tee "$f"
}

cmd_kant_work() {
  load "$1"
  [[ -d $kwt ]] || die "no worktree on kant yet; run ferry to kant first"
  printf 'from kant\n' > "$kwt/kant-was-here.txt"
  g -C "$kwt" add kant-was-here.txt; g -C "$kwt" commit -qm "work on kant" -- kant-was-here.txt
  printf 'v4 edited on kant\n' >> "$kwt/app.txt"
  local t=$kant/.claude/projects/$(slug "$kwt")/$SID.jsonl
  [[ -f $t ]] || die "no transcript on kant at $t"
  printf '{"type":"assistant","cwd":"%s","sessionId":"%s","message":{"role":"assistant","content":"The codeword is HERON. Wrote kant-was-here.txt."}}\n' "$kwt" "$SID" >> "$t"
  backdate "$t"
  echo "kant: committed kant-was-here.txt, edited app.txt, appended a transcript turn"
}

cmd_state() {
  load "$1"
  local f=$ev/$(seq_next)-state-$(label_of "${2:-snapshot}").txt side h w
  {
    for side in laptop kant; do
      h=$root/$side w=$h/src/$NAME
      echo "===== $side ($h)"
      if [[ -d $w/.git || -f $w/.git ]]; then
        echo "--- git -C ~/src/$NAME status --short --branch"; g -C "$w" status --short --branch
        echo "--- log"; g -C "$w" log --oneline -5
        echo "--- staged diff"; g -C "$w" diff --cached --stat
        echo "--- ignored files"; (cd "$w" && find . -path ./.git -prune -o -type f -print | sort | while read -r p; do g check-ignore -q "$p" && echo "$p"; done) || true
        if [[ -f $w/.env ]]; then echo ".env mode $(mode_of "$w/.env")"; fi
      else echo "(no ~/src/$NAME)"; fi
      echo "--- claude transcripts"; find "$h/.claude/projects" -name '*.jsonl' 2>/dev/null | sed "s#$h#~#" || true
      echo "--- parked"; find "$h/.local/state/ferry/parked" -type f 2>/dev/null | sed "s#$h#~#" || true
      echo "--- task records"
      for r in "$h"/.local/state/ferry/tasks/*.json; do [[ -f $r ]] || continue; jq -c '{name,owner,state,generation,from,to,agent,session}' "$r"; done
    done
  } 2>&1 | tee "$f"
}

cmd_rebuild() {
  load "$1"
  build
  echo "rebuilt $ferry ($("$ferry" version | awk '{print $2}'), src $(cat "$root/bin/.src-hash"))"
}

cmd_clean() {
  load "$1"
  tmux -L "$sock" kill-server 2>/dev/null || true
  rm -f "${TMUX_TMPDIR:-/tmp}/tmux-$(id -u)/$sock"
  rm -rf "$root"
  echo "removed $root; evidence kept in $ev"
}

# all: drive hand-off, bring-back and remotes in a new world with no
# terminal, checking both homes after each step. When every check passes it
# writes the committed tree it built from to $ev/verified, the stamp
# bin/worktree land looks for, and cleans the world. A failure keeps it.
cmd_all() {
  [[ -z $(cd "$repo" && git status --porcelain) ]] \
    || die "all stamps the committed tree, so commit or drop every change in $repo first"
  local tree r out rc fails=0 rec t
  tree=$(cd "$repo" && git rev-parse 'HEAD^{tree}')
  r=$(cmd_new | tail -1)
  load "$r"
  check() {
    local line="ok    $1"
    eval "$2" || { line="FAIL  $1"; fails=$((fails + 1)); }
    echo "$line" | tee -a "$ev/all.txt"
  }
  step() { rc=0; out=$(cmd_ferry "$root" "$@" 2>&1) || rc=$?; }
  record() { jq -r "$1" "$kant"/.local/state/ferry/tasks/*.json; }
  t() { echo "$1/.claude/projects/$(slug "$2")/$SID.jsonl"; }

  check "doctor passes" 'cmd_doctor "$root" >/dev/null'
  local before_status before_staged before_unstaged
  before_status=$(g -C "$wt" status --porcelain) before_staged=$(g -C "$wt" diff --cached) before_unstaged=$(g -C "$wt" diff)

  step to kant --yes --no-launch
  check "to-code: ferry to kant exits 0" '[[ $rc == 0 ]] && grep -q "kant owns demo" <<<"$out"'
  check "to-code: kant has the laptop's HEAD, index and working tree" \
    '[[ $(g -C "$kwt" rev-parse HEAD) == $(g -C "$wt" rev-parse HEAD) && $(g -C "$kwt" status --porcelain) == "$before_status" && $(g -C "$kwt" diff --cached) == "$before_staged" && $(g -C "$kwt" diff) == "$before_unstaged" ]]'
  check "to-extras: .env arrived 0600 and node_modules stayed" '[[ $(mode_of "$kwt/.env") == 600 && ! -e $kwt/node_modules ]]'
  check "to-session: kant's transcript has only kant cwds; the laptop kept its own" \
    '! grep -q "\"cwd\":\"$wt" "$(t "$kant" "$kwt")" && grep -q "\"cwd\":\"$kwt\"" "$(t "$kant" "$kwt")" && [[ -f $(t "$laptop" "$wt") ]]'
  check "to-record: kant owns the task" '[[ $(record "[.owner,.state]|join(\" \")") == "kant owned" ]]'
  check "remotes-fresh: kant has origin and main tracks origin/main" \
    '[[ $(g -C "$kwt" remote get-url origin) == "$root/origin.git" && $(g -C "$kwt" rev-parse --abbrev-ref "main@{upstream}") == origin/main ]]'

  step to kant --yes --no-launch
  check "to-refuse-owner: a second ferry to kant refuses" '[[ $rc != 0 ]] && grep -q "kant owns demo, not laptop" <<<"$out"'

  cmd_kant_work "$root" >/dev/null
  rm "$kwt/.env"
  step -C /tmp from kant demo --yes
  check "from-code: ferry from kant exits 0" '[[ $rc == 0 ]] && grep -q "laptop owns demo" <<<"$out"'
  check "from-code: kant's commit and edit came back, the laptop's index survived" \
    '[[ $(g -C "$wt" log -1 --format=%s) == "work on kant" && $(tail -1 "$wt/app.txt") == "v4 edited on kant" && $(g -C "$wt" diff --cached --name-only | xargs) == "app.txt gone.txt" && -f $wt/untracked.txt ]]'
  check "from-extras-delete: .env is gone from the laptop and parked" \
    '[[ ! -e $wt/.env ]] && find "$laptop/.local/state/ferry/parked" -name .env | grep -q .'
  check "from-session: the laptop's transcript has kant's turn with a laptop cwd" \
    'tail -1 "$(t "$laptop" "$wt")" | grep -q "The codeword is HERON" && tail -1 "$(t "$laptop" "$wt")" | grep -q "\"cwd\":\"$wt\""'
  check "from-record: the laptop owns the task" '[[ $(record .owner) == laptop ]]'
  check "remotes-quiet: nothing to sync prints no remotes line" '! grep -q "^remotes" <<<"$out"'

  step to kant --yes --no-launch
  g -C "$kwt" remote set-url origin https://example.invalid/elsewhere.git
  step -C /tmp from kant demo --yes
  check "remotes-declined: a URL kant changed doesn't reach the laptop with --yes" \
    '[[ $rc == 0 && $(g -C "$wt" remote get-url origin) == "$root/origin.git" ]] && grep -q "kept laptop'"'"'s remotes" <<<"$out"'

  if (( fails )); then
    echo "$fails checks failed; the world is kept at $root, evidence in $ev" >&2
    return 1
  fi
  echo "$tree" > "$ev/verified"
  echo "all checks passed; stamped tree $tree in $ev/verified"
  cmd_clean "$root" >/dev/null
}

c=${1:-}; shift || true
case $c in
  all) cmd_all "$@" ;;
  new) cmd_new "$@" ;;
  doctor) cmd_doctor "$@" ;;
  rebuild) cmd_rebuild "$@" ;;
  ferry) cmd_ferry "$@" ;;
  tty) cmd_tty "$@" ;;
  keys) cmd_keys "$@" ;;
  press) cmd_press "$@" ;;
  wait) cmd_wait "$@" ;;
  screen) cmd_screen "$@" ;;
  kant-work) cmd_kant_work "$@" ;;
  state) cmd_state "$@" ;;
  backdate) backdate "$@" ;;
  clean) cmd_clean "$@" ;;
  *) sed -n '2,25p' "$0" | sed 's/^# \{0,1\}//'; exit 2 ;;
esac
